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

// 本文件守两条"钱去哪了"的边:
//
//	M1  markPaid 必须拒绝 quota 单。它会把佣金从 frozen 转成 withdrawn 却
//	    全程不碰主库 users.quota —— 对 quota 单执行一次,佣金就被永久核销
//	    而用户一分钱没拿到。paid 是终态、无出边、无反向接口、order_no 为空的
//	    paid 单连对账都扫不到,静默且不可逆。
//	M2  auto_credit_on_approve = false 时 quota 单必须仍有一条**正确的**
//	    完成路径。approved → paying 是它走到 paid 的唯一入边,而那条边只由
//	    creditQuota 写;关掉自动到账后两个自动调用点都停摆,单据会无限期停在
//	    approved,管理端只剩"标记打款失败"(用户白等)与 markPaid(吃掉佣金)。
//
// 两条都必须从**真实的 HTTP handler** 进,并且断言落在**佣金余额与主库额度的
// 实际数值**上:只断言 HTTP 状态码的话,把方式闸门删掉后返回码固然会变,
// 但"佣金被核销了多少"这件事没有任何测试盯着 —— 而那才是资损本身。

// reviewEnv 是一次人工审核操作的完整环境:扩展库 + 主库 + 配置快照。
type reviewEnv struct {
	ext  *gorm.DB
	main *gorm.DB
}

// newReviewEnv 把提现、佣金、资金单三张账都接到真实句柄上。
//
// 不 mock 任何一层的理由与 testdb_test.go 相同:这两条缺陷都住在"谁调了谁"
// 与"哪一张账被改了"里,而不是住在某个纯函数的返回值里。SettleFrozen 改的是
// qy_commission_balance,creditMainQuota 改的是主库 users.quota —— 只有让两张
// 真表各挨一次写,"核销了佣金却没加额度"才会以数值差的形式暴露出来。
func newReviewEnv(t *testing.T, autoCredit *bool) *reviewEnv {
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
		&qymodel.FundOrder{}, &qymodel.AuditLog{},
	))

	main, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	mainConn, err := main.DB()
	require.NoError(t, err)
	mainConn.SetMaxOpenConns(1)
	require.NoError(t, main.AutoMigrate(&model.User{}, &model.Log{}, &model.QyFundOutbox{}))

	prevHandle := qyDBHandle.Swap(ext)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled: true,
		Withdraw: config.Withdraw{
			Enabled:             true,
			Methods:             []string{config.WithdrawMethodQuota, config.WithdrawMethodFiat},
			AutoCreditOnApprove: autoCredit,
		},
	})
	prevMain, prevLog := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = main, main
	// common.RedisEnabled 的包内默认值是 true(只有 InitRedisClient 拿不到连接串
	// 时才会置 false),而测试里 RDB 是 nil。不关掉它,afterCredit 的第一行
	// InvalidateUserCache 就会 panic —— 那个 panic 被 safeAfterCommit 吞掉,
	// 结果是"钱到账了但账本日志那段代码从没跑过"却看不出来。
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
		CreatedAt:  now,
		UpdatedAt:  now,
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
		Id: 7, Username: "u7", Quota: mainQuota, Status: common.UserStatusEnabled,
	}).Error)
	return w
}

// balance 回读佣金余额。frozen / withdrawn 这两个数就是 M1 的全部证据。
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

// callAdmin 从真实 handler 打一次管理端 POST。
//
// 走 handler 而不是直接调 markPaid / creditNow:本项目反复出现"纯函数改对了、
// 调度层没接上"的形状,只有从 HTTP 入口进才能同时验到闸门与接线。
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

func boolPtr(v bool) *bool { return &v }

// M1:标记已打款是**法币专用**的终态入口。
//
// 它调 commission.SettleFrozen 把佣金 frozen → withdrawn,而这条路径全程不碰
// 主库 users.quota。对 quota 单放行一次,用户的佣金就被永久核销、账户余额纹丝
// 不动,且 paid 无出边、无反向接口、order_no 为空的 paid 单不会被对账再看一眼。
//
// 两个方向必须同时断言:
//   - 删掉方式闸门 → quota 那条从 409 变 200,frozen 500000 → 0、withdrawn 0 →
//     500000,主库额度仍是 1000(资损现形);
//   - 把闸门写成"一律拒绝" → fiat 那条从 200 变 409,线下打款的正常流程被堵死。
//
// 只留其中一条,另一半的错误改法会静默通过。
func TestMarkPaidIsFiatOnly(t *testing.T) {
	cases := []struct {
		name         string
		method       string
		wantStatus   int
		wantCode     string
		wantOrder    string
		wantFrozen   int64
		wantWithdraw int64
	}{
		{
			name: "法币单正常核销", method: config.WithdrawMethodFiat,
			wantStatus: http.StatusOK, wantCode: "",
			wantOrder: StatusPaid, wantFrozen: 0, wantWithdraw: 500000,
		},
		{
			name: "站内额度单必须被拒", method: config.WithdrawMethodQuota,
			wantStatus: http.StatusConflict, wantCode: errNotFiatOrder.Code,
			wantOrder: StatusApproved, wantFrozen: 500000, wantWithdraw: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newReviewEnv(t, nil) // auto_credit 取默认值 true
			w := env.seedApproved(t, "WD-"+tc.method, tc.method, 500000, 1000)

			res := callAdmin(t, handleAdminMarkPaid, w.Id,
				`{"payout_ref":"BANK-2026-0001","payout_note":"n"}`)
			// 刻意用 assert 而非 require:状态码只是表象,下面那几条"佣金被动了
			// 多少、主库额度被动了多少"才是资损本身,不能因为状态码先失败就跳过。
			assert.Equal(t, tc.wantStatus, res.Code, "body=%s", res.Body.String())
			if tc.wantCode != "" {
				assert.Equal(t, tc.wantCode, respCode(t, res))
			}

			assert.Equal(t, tc.wantOrder, env.status(t, w.Id))
			bal := env.balance(t)
			assert.Equal(t, tc.wantFrozen, bal.FrozenQuota,
				"冻结佣金被动了多少,是这条缺陷的唯一物证")
			assert.Equal(t, tc.wantWithdraw, bal.WithdrawnQuota,
				"withdrawn 增加就意味着佣金已核销;quota 单核销而主库未加钱即为资损")
			// 无论走哪条分支,markPaid 都不该碰主库额度:法币是线下打款,
			// 站内额度单则根本不该走到这里。
			assert.Equal(t, 1000, env.mainQuota(t),
				"markPaid 不是加额度的路径,它动了主库就说明有人把两条路混了")
		})
	}
}

// M2:关掉自动到账后,approved 的 quota 单必须仍能被管理员手动兑现到 paid。
//
// 这是 auto_credit_on_approve = false 唯一正确的正向出口。缺了它,该取值就是
// 一个吃钱的稳态:佣金无限期冻在 frozen,用户既拿不到额度也退不回来,而管理端
// 看起来可用的按钮只有"标记打款失败"(用户白等)和 markPaid(佣金被吃掉)。
//
// 断言必须一路走到**主库额度真的加了**:只断言单据状态变成 paid 的话,把
// creditNow 换成一句 markPaid 式的"核销佣金"也照样绿 —— 那正是 M1 的形状。
func TestCreditNowSettlesQuotaOrderWhenAutoCreditIsOff(t *testing.T) {
	env := newReviewEnv(t, boolPtr(false))
	w := env.seedApproved(t, "WD-manual", config.WithdrawMethodQuota, 500000, 1000)

	// 前提复现:关掉自动到账后,对账任务这一侧不会再拾起这张单。
	// 少了这一步,下面的成功就可能是别的路径顺手完成的。
	resumeApproved(t.Context(), common.GetTimestamp()+3600, 100)
	require.Equal(t, StatusApproved, env.status(t, w.Id),
		"auto_credit=false 时对账不该自动兑现,否则这个测试验的不是手动兑现")

	res := callAdmin(t, handleAdminCreditNow, w.Id, `{}`)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	assert.Equal(t, StatusPaid, env.status(t, w.Id))
	assert.Equal(t, 1000+500000, env.mainQuota(t),
		"手动兑现必须真的给主库加额度,否则它和 markPaid 一样是在核销佣金")
	bal := env.balance(t)
	assert.EqualValues(t, 0, bal.FrozenQuota)
	assert.EqualValues(t, 500000, bal.WithdrawnQuota)

	// 走的必须是 twophase 那条带主库 outbox 探针的正规路径,而不是绕过它直接
	// 改两张表:少了资金单,进程崩在中途时补偿任务无从判定"主库到底动没动"。
	var order qymodel.FundOrder
	require.NoError(t, env.ext.Where("ref_id = ?", w.WithdrawNo).Take(&order).Error)
	assert.EqualValues(t, qymodel.StatusSuccess, order.Status)
	var probed int64
	require.NoError(t, env.main.Model(&model.QyFundOutbox{}).
		Where("order_no = ?", order.OrderNo).Count(&probed).Error)
	assert.EqualValues(t, 1, probed, "主库 outbox 是判定资金是否生效的唯一精确探针")

	// 用户能在原项目的"日志"页看到这笔到账,这是零前端改动的兜底通知。
	var ledger []model.Log
	require.NoError(t, env.main.Where("user_id = ?", 7).Find(&ledger).Error)
	require.Len(t, ledger, 1)
	assert.Contains(t, ledger[0].Content, w.WithdrawNo)

	// 幂等:管理员连点两次不能加两次额度。准入闸门是 startPaying 的
	// approved → paying CAS,此时单据已是 paid,第二次直接被状态判断拒掉。
	again := callAdmin(t, handleAdminCreditNow, w.Id, `{}`)
	assert.Equal(t, http.StatusConflict, again.Code)
	assert.Equal(t, 1000+500000, env.mainQuota(t), "重复兑现不能重复加钱")
}

// 手动兑现是 quota 单专用的入口,法币单打进来必须被挡:fiat 的钱在线下,
// 给它加一笔站内额度等于凭空多付一次。
func TestCreditNowRejectsWrongMethodAndStatus(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		status   string
		wantCode string
	}{
		{"法币单不能兑现为额度", config.WithdrawMethodFiat, StatusApproved, errNotQuotaOrder.Code},
		{"待审核单不能直接兑现", config.WithdrawMethodQuota, StatusPending, errIllegalTransition.Code},
		{"已终态单不能再兑现", config.WithdrawMethodQuota, StatusPaid, errIllegalTransition.Code},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newReviewEnv(t, boolPtr(false))
			w := env.seedApproved(t, "WD-"+tc.name, tc.method, 500000, 1000)
			require.NoError(t, env.ext.Model(&Withdrawal{}).
				Where("id = ?", w.Id).Update("status", tc.status).Error)

			res := callAdmin(t, handleAdminCreditNow, w.Id, `{}`)
			assert.Equal(t, http.StatusConflict, res.Code, "body=%s", res.Body.String())
			assert.Equal(t, tc.wantCode, respCode(t, res))
			assert.Equal(t, 1000, env.mainQuota(t), "被拒的请求不得动主库额度")
			assert.EqualValues(t, 500000, env.balance(t).FrozenQuota)
		})
	}
}

// 手动兑现必须真的挂在管理端路由上。
//
// 这条测试单独存在的理由就是本项目反复出现的"断链":creditNow 写得再对,
// 只要 RegisterAdminRoutes 里少一行,auto_credit=false 依旧是那个吃钱的稳态,
// 而所有直接调用被测函数的测试都会照常全绿。
func TestAdminRoutesExposeManualCredit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mod{}.RegisterAdminRoutes(engine.Group("/api/qy/admin"))

	paths := make(map[string]bool)
	for _, r := range engine.Routes() {
		paths[r.Method+" "+r.Path] = true
	}
	assert.True(t, paths["POST /api/qy/admin/withdraw/:id/credit"],
		"quota 单的手动兑现入口没挂上路由,auto_credit_on_approve=false 就没有正向出口")
	// 一并钉住它的两个邻居,避免有人"顺手"把资金相关的路由整块挪走。
	assert.True(t, paths["POST /api/qy/admin/withdraw/:id/mark-paid"])
	assert.True(t, paths["POST /api/qy/admin/withdraw/:id/fail"])
}
