package withdraw

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// review_payout_gate_test.go —— 「提现只做佣金扣除,金额由管理员手动发放」这条
// 产品口径的资金侧证明。
//
// 本文件守四件事,每一件都直接对应一种资损:
//
//	P1  **系统一分钱都不许自己动。** 审核通过、标记已发放、标记发放失败,三条路
//	    走完主库 users.quota 必须纹丝不动。这不是"顺便断言一下" —— 自动到账刚
//	    被整条删掉,任何人把它以任何形式加回来(哪怕只是"顺手给 quota 单加一下"),
//	    都必须在这里变红。
//	P2  **标记已发放要过两道闸。** 凭证必填 + 实发金额必须与单据金额相等。
//	    paid 是终态、无出边、无反向接口:误标记一次 = 佣金被永久核销而用户
//	    一分钱没拿到。一个对着 approved 队列无差别 POST 的脚本必须被挡住。
//	P3  **驳回 / 发放失败必须真的把佣金退回可用池。** 人工发放模型新引入的敞口
//	    就是"扣了佣金迟迟不发",而退回是这个敞口唯一的出口。
//	P4  **账本恒等式在每一步都成立:**
//	        available + frozen + withdrawn == 申请前的 available
//	    冻结、核销、退回三种动作各自只在桶之间搬,总量不变。
//
// 断言一律落在**佣金余额与主库额度的实际数值**上:只断言 HTTP 状态码的话,
// 把闸门删掉后返回码固然会变,但"佣金被核销了多少、主库被加了多少"没有任何
// 测试盯着 —— 而那才是资损本身。

// reviewEnv 是一次人工审核操作的完整环境:扩展库 + 主库 + 配置快照。
type reviewEnv struct {
	ext  *gorm.DB
	main *gorm.DB
}

// newReviewEnv 把提现、佣金两张账都接到真实句柄上,并另起一个**真实的主库**。
//
// 主库在这里不是陪衬:本模块改成人工发放之后对它的正确行为是"一次都不写",
// 而"一次都不写"只有在真的有一张 users 表可以被写的时候才证明得了。
func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()

	ext, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	extConn, err := ext.DB()
	require.NoError(t, err)
	// 内存库按连接隔离;整条链路是单协程顺序执行的,一条连接足够。
	extConn.SetMaxOpenConns(1)
	// qianye/db.LockForUpdate 无条件挂 FOR UPDATE(扩展库固定是 MySQL),
	// sqlite 不认这个语法。把 FOR 子句渲染成空,语句其余部分原样执行。
	ext.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, ext.AutoMigrate(
		&Withdrawal{}, &Event{},
		&commission.Balance{}, &commission.FreezeRecord{},
		&qymodel.AuditLog{},
	))

	main, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	mainConn, err := main.DB()
	require.NoError(t, err)
	mainConn.SetMaxOpenConns(1)
	require.NoError(t, main.AutoMigrate(&model.User{}, &model.Log{}))

	prevHandle := qyDBHandle.Swap(ext)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled: true,
		Withdraw: config.Withdraw{
			Enabled: true,
			Methods: []string{config.WithdrawMethodQuota, config.WithdrawMethodFiat},
		},
	})
	prevMain, prevLog := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = main, main
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		model.DB, model.LOG_DB = prevMain, prevLog
		common.RedisEnabled = prevRedis
		_ = extConn.Close()
		_ = mainConn.Close()
	})
	return &reviewEnv{ext: ext, main: main}
}

// seedApproved 造出"审核已通过、佣金已冻结、用户在主库有账号"的完整初态。
//
// FreezeRecord 不是可省的陪衬:SettleFrozen 与 UnfreezeForWithdraw 都先
// loadFreeze 核对"这笔钱当初真的被冻过且金额一致",缺了它两条路径都会直接
// 报 ErrFreezeMissing —— 那样测试会因为错误的原因变绿。
func (e *reviewEnv) seedApproved(t *testing.T, no, method string, quota int64, mainQuota int) *Withdrawal {
	return e.seedApprovedAs(t, no, method, quota, mainQuota, common.RoleCommonUser)
}

// seedApprovedAs 是 seedApproved 的带目标角色版本,供越级互批用例使用。
func (e *reviewEnv) seedApprovedAs(t *testing.T, no, method string, quota int64,
	mainQuota, targetRole int) *Withdrawal {
	t.Helper()
	now := common.GetTimestamp()
	w := &Withdrawal{
		WithdrawNo: no,
		IdemScope:  idemScope,
		IdemKey:    idemKeyOf(7, "seed-"+no),
		UserId:     7,
		Method:     method,
		Status:     StatusApproved,
		Quota:      quota,
		ReviewedAt: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if method == config.WithdrawMethodFiat {
		// 法币单的应付/实付在申请时就由账本定死(priceFiatInTx)。这里直接给出
		// 一个不整的实付额:标记已发放的复核比的是这个数,用 100.000000 这种
		// 圆整值会让"比错了列"(拿 gross 或 quota 去比)照样通过。
		w.Currency = "CNY"
		w.GrossAmount = decimal.RequireFromString("850.000000")
		w.FeeAmount = decimal.RequireFromString("12.750000")
		w.NetAmount = decimal.RequireFromString("837.250000")
		w.FeeBps = 150
	}
	require.NoError(t, e.ext.Create(w).Error)
	require.NoError(t, e.ext.Create(&commission.Balance{
		UserId: 7, FrozenQuota: quota, AvailableQuota: 0,
		AvailableFiat: decimal.Zero, UnsettledAmount: decimal.Zero,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, e.ext.Create(&commission.FreezeRecord{
		RefNo: no, Action: commission.FreezeActionFreeze,
		UserId: 7, Quota: quota, Fiat: decimal.Zero, CreatedAt: now,
	}).Error)
	require.NoError(t, e.main.Create(&model.User{
		Id: 7, Username: "u7", Quota: mainQuota,
		Role: targetRole, Status: common.UserStatusEnabled,
	}).Error)
	return w
}

// balance 回读佣金余额。三个桶的数值就是全部证据。
func (e *reviewEnv) balance(t *testing.T) commission.Balance {
	t.Helper()
	var bal commission.Balance
	require.NoError(t, e.ext.Where("user_id = ?", 7).Take(&bal).Error)
	return bal
}

func (e *reviewEnv) mainQuota(t *testing.T) int {
	t.Helper()
	var u model.User
	require.NoError(t, e.main.Where("id = ?", 7).Take(&u).Error)
	return u.Quota
}

func (e *reviewEnv) status(t *testing.T, id int64) string {
	t.Helper()
	var w Withdrawal
	require.NoError(t, e.ext.Where("id = ?", id).Take(&w).Error)
	return w.Status
}

func (e *reviewEnv) reload(t *testing.T, id int64) Withdrawal {
	t.Helper()
	var w Withdrawal
	require.NoError(t, e.ext.Where("id = ?", id).Take(&w).Error)
	return w
}

// assertLedgerIdentity 断言账本恒等式:三个桶之和恒等于建单前的可用额度。
//
// 冻结把 available → frozen,核销把 frozen → withdrawn,退回把 frozen →
// available。三种动作都只在桶之间搬,总量在任何一步都不变 —— 任何一条路径
// 少加或多减一次,这里立刻对不上。
func (e *reviewEnv) assertLedgerIdentity(t *testing.T, total int64) {
	t.Helper()
	bal := e.balance(t)
	assert.Equal(t, total, bal.AvailableQuota+bal.FrozenQuota+bal.WithdrawnQuota,
		"账本恒等式被破坏: available=%d frozen=%d withdrawn=%d,应合计 %d",
		bal.AvailableQuota, bal.FrozenQuota, bal.WithdrawnQuota, total)
}

// callAdmin 从真实 handler 打一次管理端 POST。
//
// 走 handler 而不是直接调 markPayout:本项目反复出现"纯函数改对了、调度层没
// 接上"的形状,只有从 HTTP 入口进才能同时验到闸门与接线。
func callAdmin(t *testing.T, h gin.HandlerFunc, id int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/qy/admin/withdraw/"+
		strconv.FormatInt(id, 10)+"/action", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	c.Set("id", 99)
	c.Set("username", "admin")
	// 角色必须写进 context:guard.ActorMayActOnCtx 只认鉴权中间件写进来的身份,
	// 不设就是 role=0,四个决定会全部被越级闸门挡下 —— 而那不是本文件要验的东西。
	c.Set("role", common.RoleAdminUser)

	h(c)
	return res
}

func respCode(t *testing.T, res *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	return body.Code
}

// P1 + P2 的正向面:两种方式都能被标记已发放,而且都不碰主库额度。
//
// 这里同时钉住了 method 闸门的下线是**刻意**的:quota 单在这条路上从 409 变成
// 200,是产品口径变更的直接后果,不是回归。它的替代闸门在下一个用例里。
func TestMarkPayoutSettlesBothMethodsAndNeverTouchesMainQuota(t *testing.T) {
	cases := []struct {
		name   string
		method string
		body   string
	}{
		{
			name: "站内额度单:管理员手工加完额度回来登记",
			// payout_ref 填的是那次手工加额度的操作记录标识。
			method: config.WithdrawMethodQuota,
			body:   `{"payout_ref":"MANUAL-LOG-8812","confirm_quota":500000,"payout_note":"已在用户编辑页加额度"}`,
		},
		{
			name:   "法币单:管理员线下打完款回来登记",
			method: config.WithdrawMethodFiat,
			body:   `{"payout_ref":"BANK-2026-0001","confirm_amount":"837.25","payout_note":"招行"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newReviewEnv(t)
			w := env.seedApproved(t, "WD-"+tc.method, tc.method, 500000, 1000)

			res := callAdmin(t, handleAdminMarkPaid, w.Id, tc.body)
			assert.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

			got := env.reload(t, w.Id)
			assert.Equal(t, StatusPaid, got.Status)
			assert.NotEmpty(t, got.PayoutRef, "发放凭证必须落库,它是争议时唯一的物证")
			assert.Positive(t, got.PaidAt)
			assert.Equal(t, 99, got.PayoutOperatorId, "发放人必须记名")

			bal := env.balance(t)
			assert.EqualValues(t, 0, bal.FrozenQuota)
			assert.EqualValues(t, 500000, bal.WithdrawnQuota, "标记已发放即核销佣金")
			env.assertLedgerIdentity(t, 500000)

			// P1:这是全文件最重要的一条断言。系统只记账,不发钱。
			assert.Equal(t, 1000, env.mainQuota(t),
				"标记已发放不得给主库加额度 —— 钱由管理员自己发,系统碰它就是重复发放")

			// 幂等:管理员连点两次不能把佣金核销两次。
			again := callAdmin(t, handleAdminMarkPaid, w.Id, tc.body)
			assert.Equal(t, http.StatusConflict, again.Code)
			assert.EqualValues(t, 500000, env.balance(t).WithdrawnQuota)
			env.assertLedgerIdentity(t, 500000)
		})
	}
}

// P2:标记已发放的两道闸 —— 凭证必填、实发金额必须与单据金额相等。
//
// 这两道闸取代了下线掉的 method 闸门。它们要挡的是同一件事:一次没有人真正
// 核对过金额的、不可逆的终态操作(典型形状是一个对着 approved 队列无差别
// POST 的运维脚本)。
//
// 每一条都必须断言**佣金一分没动**:只看返回码的话,把校验挪到核销之后
// 照样是 400,而佣金已经被核销掉了。
func TestMarkPayoutRefusesUnconfirmedPayouts(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		body     string
		wantCode string
	}{
		{
			name: "缺凭证", method: config.WithdrawMethodQuota,
			body: `{"confirm_quota":500000}`, wantCode: errPayoutRefMissing.Code,
		},
		{
			name: "凭证只有空白", method: config.WithdrawMethodQuota,
			body: `{"payout_ref":"   ","confirm_quota":500000}`, wantCode: errPayoutRefMissing.Code,
		},
		{
			name: "额度单没填实发额度", method: config.WithdrawMethodQuota,
			body: `{"payout_ref":"LOG-1"}`, wantCode: errPayoutAmountRequired.Code,
		},
		{
			name: "额度单实发额度对不上", method: config.WithdrawMethodQuota,
			body: `{"payout_ref":"LOG-1","confirm_quota":499999}`, wantCode: errPayoutAmountMismatch.Code,
		},
		{
			name: "法币单没填实发金额", method: config.WithdrawMethodFiat,
			body: `{"payout_ref":"BANK-1"}`, wantCode: errPayoutAmountRequired.Code,
		},
		{
			name: "法币单实发金额不是数字", method: config.WithdrawMethodFiat,
			body: `{"payout_ref":"BANK-1","confirm_amount":"八百三十七"}`, wantCode: errPayoutAmountRequired.Code,
		},
		{
			name: "法币单实发金额对不上", method: config.WithdrawMethodFiat,
			body: `{"payout_ref":"BANK-1","confirm_amount":"837.20"}`, wantCode: errPayoutAmountMismatch.Code,
		},
		{
			// 拿应付(gross)当实付填是最容易发生的一种错:两个数只差一笔手续费,
			// 界面上又挨在一起。放行一次就是每单多付 12.75 元。
			name: "法币单误填应付金额", method: config.WithdrawMethodFiat,
			body: `{"payout_ref":"BANK-1","confirm_amount":"850.00"}`, wantCode: errPayoutAmountMismatch.Code,
		},
		{
			// 额度单的复核只看 confirm_quota。填了法币字段不算数 ——
			// 否则脚本只要多带一个字段就能绕过整道闸。
			name: "额度单用法币字段冒充", method: config.WithdrawMethodQuota,
			body: `{"payout_ref":"LOG-1","confirm_amount":"500000"}`, wantCode: errPayoutAmountRequired.Code,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newReviewEnv(t)
			w := env.seedApproved(t, "WD-"+tc.method, tc.method, 500000, 1000)

			res := callAdmin(t, handleAdminMarkPaid, w.Id, tc.body)
			assert.Equal(t, http.StatusBadRequest, res.Code, "body=%s", res.Body.String())
			assert.Equal(t, tc.wantCode, respCode(t, res))

			assert.Equal(t, StatusApproved, env.status(t, w.Id), "被拒的登记不许改状态")
			bal := env.balance(t)
			assert.EqualValues(t, 500000, bal.FrozenQuota, "佣金必须原样冻着")
			assert.EqualValues(t, 0, bal.WithdrawnQuota, "没过闸就核销佣金 = 用户白扣")
			assert.Equal(t, 1000, env.mainQuota(t))
			env.assertLedgerIdentity(t, 500000)
		})
	}
}

// P1 的审核面:审核通过只是把单据放进待发放队列,不动任何一张账。
//
// 自动到账刚被整条删掉,这条用例钉住的就是"它没有以任何形式回来"。
func TestApproveOnlyQueuesAndMovesNoMoney(t *testing.T) {
	for _, method := range []string{config.WithdrawMethodQuota, config.WithdrawMethodFiat} {
		t.Run(method, func(t *testing.T) {
			env := newReviewEnv(t)
			w := env.seedApproved(t, "WD-"+method, method, 500000, 1000)
			require.NoError(t, env.ext.Model(&Withdrawal{}).Where("id = ?", w.Id).
				Updates(map[string]any{"status": StatusPending, "reviewed_at": 0}).Error)

			res := callAdmin(t, handleAdminApprove, w.Id, `{}`)
			require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

			got := env.reload(t, w.Id)
			assert.Equal(t, StatusApproved, got.Status)
			assert.Positive(t, got.ReviewedAt, "发放时限从 reviewed_at 起算,它必须被写上")

			bal := env.balance(t)
			assert.EqualValues(t, 500000, bal.FrozenQuota, "审核通过不改变佣金的任何一桶")
			assert.EqualValues(t, 0, bal.WithdrawnQuota)
			assert.EqualValues(t, 0, bal.AvailableQuota)
			assert.Equal(t, 1000, env.mainQuota(t),
				"审核通过不得给主库加额度 —— 自动到账已经下线,它回来了就在这里变红")
			env.assertLedgerIdentity(t, 500000)
		})
	}
}

// P3:驳回与发放失败都必须把佣金原样退回可用池。
//
// 这是"扣了佣金但管理员不发放"这个新敞口唯一的出口,两个阶段各有一个:
// 待审阶段用驳回,待发放阶段用发放失败。少一个,那个阶段的单据就只剩下
// "标记已发放"这一条终态出口 —— 而那条路要求人真的把钱发出去。
func TestRefundPathsReturnCommissionToAvailable(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		handler gin.HandlerFunc
		body    string
		want    string
	}{
		{
			name: "待审阶段驳回", from: StatusPending, handler: handleAdminReject,
			body: `{"reason":"收款信息与实名不符"}`, want: StatusRejected,
		},
		{
			name: "待发放阶段标记发放失败", from: StatusApproved, handler: handleAdminFail,
			body: `{"reason":"银行退回,账户已注销"}`, want: StatusFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newReviewEnv(t)
			w := env.seedApproved(t, "WD-refund", config.WithdrawMethodFiat, 500000, 1000)
			require.NoError(t, env.ext.Model(&Withdrawal{}).
				Where("id = ?", w.Id).Update("status", tc.from).Error)

			res := callAdmin(t, tc.handler, w.Id, tc.body)
			require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

			assert.Equal(t, tc.want, env.status(t, w.Id))
			bal := env.balance(t)
			assert.EqualValues(t, 500000, bal.AvailableQuota, "佣金必须回到可用池")
			assert.EqualValues(t, 0, bal.FrozenQuota)
			assert.EqualValues(t, 0, bal.WithdrawnQuota, "退回不是核销")
			assert.Equal(t, 1000, env.mainQuota(t))
			env.assertLedgerIdentity(t, 500000)
		})
	}

	t.Run("理由必填", func(t *testing.T) {
		for _, handler := range []gin.HandlerFunc{handleAdminReject, handleAdminFail} {
			env := newReviewEnv(t)
			w := env.seedApproved(t, "WD-noreason", config.WithdrawMethodFiat, 500000, 1000)

			res := callAdmin(t, handler, w.Id, `{"reason":"  "}`)
			assert.Equal(t, http.StatusBadRequest, res.Code)
			assert.Equal(t, errReasonRequired.Code, respCode(t, res))
			assert.EqualValues(t, 500000, env.balance(t).FrozenQuota)
		}
	})
}

// 管理端路由必须**恰好**是这四个决定。
//
// 上半段是接线守卫(写对了但没挂路由,所有直接调函数的测试照样全绿);
// 下半段是回退守卫:自动到账的两个入口(立即兑现 / 人工裁决)已经随整条链路
// 删除,任何人把它们加回来都必须在这里变红 —— 那两条路会真的动主库额度,
// 而产品口径是"系统不发钱"。
func TestAdminRoutesAreExactlyTheFourDecisions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mod{}.RegisterAdminRoutes(engine.Group("/api/qy/admin"))

	paths := make(map[string]bool)
	for _, r := range engine.Routes() {
		paths[r.Method+" "+r.Path] = true
	}
	for _, p := range []string{
		"POST /api/qy/admin/withdraw/:id/approve",
		"POST /api/qy/admin/withdraw/:id/reject",
		"POST /api/qy/admin/withdraw/:id/mark-paid",
		"POST /api/qy/admin/withdraw/:id/fail",
	} {
		assert.True(t, paths[p], "人工决定 %s 没挂上路由", p)
	}
	for _, p := range []string{
		"POST /api/qy/admin/withdraw/:id/credit",
		"POST /api/qy/admin/withdraw/:id/resolve",
	} {
		assert.False(t, paths[p],
			"%s 属于已下线的自动到账链路,重新挂上等于让系统又开始自己发钱", p)
	}
}

// 越级互批闸门:管理员不能对同级或更高权限账号的提现单下结论。
//
// 与自审自批(review_self_approval_test.go)是同一条链的两半:只挡"同一个人"
// 的话,两个 role=10 管理员互相批对方的单,闸门一次都不会响,整条链只是从
// 一个人变成两个人。四个决定逐个验,并且一律断言佣金纹丝不动 ——
// 只看返回码的话,把闸门挪到核销之后照样是 403,而钱已经动了。
func TestAdminDecisionsRefusePeerAndHigherRoleWithdrawals(t *testing.T) {
	cases := []struct {
		name    string
		handler gin.HandlerFunc
		status  string
		body    string
	}{
		{"通过", handleAdminApprove, StatusPending, `{}`},
		{"驳回", handleAdminReject, StatusPending, `{"reason":"资料不符"}`},
		{"标记已发放", handleAdminMarkPaid, StatusApproved,
			`{"payout_ref":"LOG-1","confirm_quota":500000}`},
		{"标记发放失败", handleAdminFail, StatusApproved, `{"reason":"账户已注销"}`},
	}
	for _, tc := range cases {
		for _, targetRole := range []int{common.RoleAdminUser, common.RoleRootUser} {
			t.Run(tc.name+"/target_role_"+strconv.Itoa(targetRole), func(t *testing.T) {
				env := newReviewEnv(t)
				w := env.seedApprovedAs(t, "WD-peer", config.WithdrawMethodQuota,
					500000, 1000, targetRole)
				require.NoError(t, env.ext.Model(&Withdrawal{}).
					Where("id = ?", w.Id).Update("status", tc.status).Error)

				res := callAdmin(t, tc.handler, w.Id, tc.body)

				assert.Equal(t, http.StatusForbidden, res.Code, "body=%s", res.Body.String())
				assert.Equal(t, errPeerReview.Code, respCode(t, res))
				assert.Equal(t, tc.status, env.status(t, w.Id), "被拒的决定不许改状态")
				bal := env.balance(t)
				assert.EqualValues(t, 500000, bal.FrozenQuota, "佣金必须原样冻着")
				assert.EqualValues(t, 0, bal.WithdrawnQuota)
				assert.EqualValues(t, 0, bal.AvailableQuota)
				env.assertLedgerIdentity(t, 500000)

				var denials []qymodel.AuditLog
				require.NoError(t, env.ext.Where("action = ?", "withdraw.peer_review_denied").
					Find(&denials).Error)
				require.Len(t, denials, 1, "被拒的越级审核必须留痕")
				assert.Equal(t, qymodel.ResultFail, denials[0].Result)
			})
		}
	}
}
