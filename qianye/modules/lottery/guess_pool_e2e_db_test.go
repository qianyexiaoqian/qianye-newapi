package lottery

// guess_pool_e2e_db_test.go —— 真打一场竞猜,核对「平台净流入 == 手续费」。
//
// 项目方的原话是「竞猜怎么感觉就像选择题一样?这不是给人送钱吗?」。
// SplitPool 的纯函数守恒式(fund_flow_db_test.go)只能证明**分配**对得上,
// 证明不了"平台到底进出了多少真钱":那要看主库 users.quota 的前后差。
// 两者中间隔着报名扣款、结算落计划、出款 worker 加钱三段跨库路径,
// 任何一段多发一笔,纯函数照样全绿。
//
// 所以这里从建活动一路跑到出款完成,唯一的判据是:
//
//	Σ(全部参与者的主库额度前后差) == -fee
//
// 也就是**用户侧净流出恰好等于活动行上记的 platform_fee_quota**,
// 平台一分钱都没有垫付。fee=0 的公益场里这个数是 0(不是负数)。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	guessE2EAdminId = 7001
	// 三个下注人。id 连号只是为了断言好读。
	guessE2EUserA = 7011
	guessE2EUserB = 7012
	guessE2EUserC = 7013
)

// guessE2EActor 是当前这一次请求的用户 id。竞猜必须有对手盘,
// 而 ballE2ERouter 那种"路由上写死一个 id"的写法只能跑单人。
var guessE2EActor int

// newGuessMainDB 建主库并接到 model.DB,给三个下注人各发一笔起始额度。
func newGuessMainDB(t *testing.T, quota int) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}, &model.Log{}, &model.QyFundOutbox{}))

	prevType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	prevDB, prevLogDB := model.DB, model.LOG_DB
	prevMem, prevRedis := common.MemoryCacheEnabled, common.RedisEnabled
	prevOptions := common.OptionMap
	model.DB, model.LOG_DB = gdb, gdb
	common.OptionMap = map[string]string{}
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = prevDB, prevLogDB
		common.SetMainDatabaseType(prevType)
		common.MemoryCacheEnabled, common.RedisEnabled = prevMem, prevRedis
		common.OptionMap = prevOptions
		_ = sqlDB.Close()
	})

	for i, id := range guessE2EBettors() {
		require.NoError(t, gdb.Create(&model.User{
			Id: id, Username: "qy-guess-" + strconv.Itoa(i), Password: "x",
			AffCode: "affg" + strconv.Itoa(i), Group: "default",
			Quota: quota, Status: common.UserStatusEnabled,
		}).Error)
	}
	return gdb
}

func guessE2EBettors() []int {
	return []int{guessE2EUserA, guessE2EUserB, guessE2EUserC}
}

// guessE2ERouter 挂真实路由;用户腿按 guessE2EActor 注入,所以同一个路由
// 能依次扮演三个下注人。
func guessE2ERouter() *gin.Engine {
	r := gin.New()
	admin := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", guessE2EAdminId); h(c) }
	}
	user := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", guessE2EActor); h(c) }
	}
	r.POST("/admin/lottery/activities", admin(handleCreateActivity))
	r.POST("/admin/lottery/activities/:act_no/publish", admin(handlePublishActivity))
	r.POST("/admin/lottery/activities/:act_no/guess-result", admin(handleSetGuessResult))
	r.GET("/lottery/activities/:act_no", user(handleGetActivity))
	r.POST("/lottery/activities/:act_no/entries", user(handleCreateEntry))
	return r
}

// guessE2EEnv 拉起一场竞猜要用到的全部环境,返回扩展库句柄与路由。
func guessE2EEnv(t *testing.T, startQuota int) (*gorm.DB, *gorm.DB, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeQuota:          5_000_000,
		MaxTotalPrizeQuota:     5_000_000,
		MaxActiveActivities:    16,
		MaxPrizeTiers:          8,
		MaxOptions:             8,
		MaxTotalEntriesHard:    1_000,
		DefaultGuessFeeBps:     500,
		MaxGuessFeeBps:         2_000,
	})
	// 审计表与运营覆盖表都要建:少了 qy_settings,effectiveCtx 会走"读不到覆盖
	// 就回落 YAML"那条**降级**分支,而线上跑的是"读到空表、按基线合并"那条。
	// 费率的上下界判定就住在合并里,拿降级分支去测等于没测。
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}, &qymodel.Setting{}))
	// 运营覆盖是带 60 秒缓存的进程级单例,上一条用例留下的快照会串味。
	invalidateSettings()
	t.Cleanup(invalidateSettings)
	main := newGuessMainDB(t, startQuota)
	return ext, main, guessE2ERouter()
}

func guessCall(t *testing.T, r *gin.Engine, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// createGuess 建一场两选项的竞猜并发布,返回 act_no。
func createGuess(t *testing.T, r *gin.Engine, feeBps int, stake int64) string {
	t.Helper()
	now := common.GetTimestamp()
	create := `{
		"kind":"guess","title":"下个版本会不会涨价",
		"stake_quota":` + strconv.FormatInt(stake, 10) + `,
		"fee_bps":` + strconv.Itoa(feeBps) + `,
		"open_at":` + strconv.FormatInt(now-60, 10) + `,
		"close_at":` + strconv.FormatInt(now+3600, 10) + `,
		"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
		"options":[
			{"opt_no":1,"label":"会涨"},
			{"opt_no":2,"label":"不会涨"}
		]}`
	code, body := guessCall(t, r, http.MethodPost, "/admin/lottery/activities", create)
	require.Equalf(t, http.StatusOK, code, "建竞猜失败: %s", body)
	actNo := jsonString(t, body, "data", "act_no")
	code, body = guessCall(t, r, http.MethodPost,
		"/admin/lottery/activities/"+actNo+"/publish", `{}`)
	require.Equalf(t, http.StatusOK, code, "发布竞猜失败: %s", body)
	return actNo
}

// placeBet 让某个用户押一个选项。amount 为 0 时走"不填金额 = 按单注额"。
func placeBet(t *testing.T, r *gin.Engine, actNo string, userId, optNo int, amount int64) {
	t.Helper()
	guessE2EActor = userId
	body := `{"client_request_id":"qy-bet-` + strconv.Itoa(userId) + `-` + actNo +
		`","opt_no":` + strconv.Itoa(optNo)
	if amount > 0 {
		body += `,"amount":` + strconv.FormatInt(amount, 10)
	}
	body += `}`
	code, resp := guessCall(t, r, http.MethodPost,
		"/lottery/activities/"+actNo+"/entries", body)
	require.Equalf(t, http.StatusOK, code, "用户 %d 下注失败: %s", userId, resp)
}

func quotaOf(t *testing.T, main *gorm.DB, userId int) int64 {
	t.Helper()
	var u model.User
	require.NoError(t, main.Where("id = ?", userId).Take(&u).Error)
	return int64(u.Quota)
}

// settleGuess 封盘 → 录结果 → 把出款全部驱动到终态。
func settleGuess(t *testing.T, ext *gorm.DB, r *gin.Engine, actNo string, winOptNo int) *Activity {
	t.Helper()
	var act Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
	require.NoError(t, lockActivity(context.Background(), ext, &act))

	code, body := guessCall(t, r, http.MethodPost,
		"/admin/lottery/activities/"+actNo+"/guess-result",
		`{"opt_no":`+strconv.Itoa(winOptNo)+`,"evidence":"官网公告 2026-08-24"}`)
	require.Equalf(t, http.StatusOK, code, "录结果失败: %s", body)

	// 出款 worker 一次只捡一批,循环到没有活的计划为止。
	for i := 0; i < 10; i++ {
		var live int64
		require.NoError(t, ext.Model(&Payout{}).
			Where("act_id = ? AND status IN ?", act.Id,
				[]string{PayoutPlanned, PayoutPaying, PayoutFailed}).
			Count(&live).Error)
		if live == 0 {
			break
		}
		DrivePayouts(context.Background())
	}
	return loadAct(t, ext, act.Id)
}

// 三人下注、一人猜中:用户侧净流出必须恰好等于手续费,一个单位都不许多。
//
// 独立算出的期望(不从被测代码回读):
//
//	下注 1000 + 1000 + 1000       → pool = 3000
//	fee = 3000 × 500 / 10000       → 150
//	net = 3000 − 150               → 2850
//	唯一的赢家 A 拿走全部 net       → 2850
//	A 的额度差 = −1000 + 2850      → +1850
//	B / C 的额度差                 → −1000 各一笔
//	Σ = 1850 − 1000 − 1000         → −150 == −fee
func TestGuessThreeBettorsOneWinner_PlatformNetInflowIsExactlyFee(t *testing.T) {
	const start = 100_000
	ext, main, r := guessE2EEnv(t, start)
	actNo := createGuess(t, r, 500, 1000)

	placeBet(t, r, actNo, guessE2EUserA, 1, 0)
	placeBet(t, r, actNo, guessE2EUserB, 2, 0)
	placeBet(t, r, actNo, guessE2EUserC, 2, 0)

	// 下注即扣款:池子还没结算时,三个人一共少了 3000。
	for _, id := range guessE2EBettors() {
		assert.EqualValues(t, start-1000, quotaOf(t, main, id),
			"用户 %d 的下注没有真的从主库扣走", id)
	}

	act := settleGuess(t, ext, r, actNo, 1)

	assert.EqualValues(t, 3000, act.PoolQuota, "奖池 = 三注之和")
	assert.EqualValues(t, 150, act.PlatformFeeQuota, "fee = 3000 × 5%")
	assert.Equal(t, OutcomeDrawn, act.Outcome)

	assert.EqualValues(t, start+1850, quotaOf(t, main, guessE2EUserA),
		"唯一的赢家应当拿走 net=2850,净赚 1850 —— 这笔钱来自 B 与 C,不是平台发的")
	assert.EqualValues(t, start-1000, quotaOf(t, main, guessE2EUserB))
	assert.EqualValues(t, start-1000, quotaOf(t, main, guessE2EUserC))

	var delta int64
	for _, id := range guessE2EBettors() {
		delta += quotaOf(t, main, id) - start
	}
	assert.EqualValues(t, -150, delta,
		"用户侧净流出必须恰好等于手续费 —— 大于它是平台多抽,小于它是平台倒贴")
	assert.EqualValues(t, act.PlatformFeeQuota, -delta,
		"活动行上记的手续费必须与主库真金的净流入逐单位一致")
}

// 手续费为 0 的公益场:平台净流入是 0,**不是负数**。
//
// 这一条单独写,是因为 fee=0 是唯一一个"守恒式两边都是 pool"的取值,
// 逐笔截断的残差在这里全部要落到最后一名赢家头上。
func TestGuessZeroFee_PlatformNeitherGainsNorPays(t *testing.T) {
	const start = 100_000
	ext, main, r := guessE2EEnv(t, start)
	actNo := createGuess(t, r, 0, 1000)

	// 两个赢家 + 一个输家:net=3000 按 1000:1000 分,各 1500。
	placeBet(t, r, actNo, guessE2EUserA, 1, 0)
	placeBet(t, r, actNo, guessE2EUserB, 1, 0)
	placeBet(t, r, actNo, guessE2EUserC, 2, 0)

	act := settleGuess(t, ext, r, actNo, 1)
	assert.EqualValues(t, 0, act.PlatformFeeQuota)

	assert.EqualValues(t, start+500, quotaOf(t, main, guessE2EUserA))
	assert.EqualValues(t, start+500, quotaOf(t, main, guessE2EUserB))
	assert.EqualValues(t, start-1000, quotaOf(t, main, guessE2EUserC))

	var delta int64
	for _, id := range guessE2EBettors() {
		delta += quotaOf(t, main, id) - start
	}
	assert.Zero(t, delta, "零费率场平台既不抽水也不垫付,用户侧总额度必须原地不动")
}

// 全场都押中同一个选项:原样退回,平台一分钱不收、也一分钱不垫。
//
// 这是界面上必须在**下注之前**说清的那一条(qy_lot_no_winner_note),
// 而它在资金上的表现是"三个人的额度全部回到起点"。
func TestGuessEverybodyRight_RefundsPrincipalAndTakesNoFee(t *testing.T) {
	const start = 100_000
	ext, main, r := guessE2EEnv(t, start)
	actNo := createGuess(t, r, 500, 1000)

	placeBet(t, r, actNo, guessE2EUserA, 1, 0)
	placeBet(t, r, actNo, guessE2EUserB, 1, 0)
	placeBet(t, r, actNo, guessE2EUserC, 1, 0)

	act := settleGuess(t, ext, r, actNo, 1)
	assert.Equal(t, OutcomeVoidAllCorrect, act.Outcome,
		"全场都押中时不该走 drawn —— 那会让三个人各分回自己那一注还被抽走 5%")
	assert.EqualValues(t, 0, act.PlatformFeeQuota, "没有发生任何再分配,收费没有对价")

	for _, id := range guessE2EBettors() {
		assert.EqualValues(t, start, quotaOf(t, main, id),
			"用户 %d 的本金没有原样退回", id)
	}
}

// 没有人押中:同样全额退款、零手续费。
func TestGuessNobodyRight_RefundsPrincipalAndTakesNoFee(t *testing.T) {
	const start = 100_000
	ext, main, r := guessE2EEnv(t, start)
	actNo := createGuess(t, r, 500, 1000)

	placeBet(t, r, actNo, guessE2EUserA, 1, 0)
	placeBet(t, r, actNo, guessE2EUserB, 1, 0)
	placeBet(t, r, actNo, guessE2EUserC, 1, 0)

	act := settleGuess(t, ext, r, actNo, 2)
	assert.Equal(t, OutcomeVoidNoWinner, act.Outcome)
	assert.EqualValues(t, 0, act.PlatformFeeQuota)

	for _, id := range guessE2EBettors() {
		assert.EqualValues(t, start, quotaOf(t, main, id),
			"用户 %d 的本金没有原样退回", id)
	}
}

// 只有一个参与者:他自己就是全场,winSum == pool,原样退回。
//
// 这一条守的是"独中的人不会被抽走手续费":若 SplitPool 少了 winSum == pool
// 那条分支,他会付 5% 买回自己那一注,而那 5% 是平台凭空拿走的。
func TestGuessSoleBettor_GetsPrincipalBackWithoutFee(t *testing.T) {
	const start = 100_000
	ext, main, r := guessE2EEnv(t, start)
	actNo := createGuess(t, r, 500, 1000)

	placeBet(t, r, actNo, guessE2EUserA, 1, 0)

	act := settleGuess(t, ext, r, actNo, 1)
	assert.EqualValues(t, 1000, act.PoolQuota)
	assert.EqualValues(t, 0, act.PlatformFeeQuota)
	assert.EqualValues(t, start, quotaOf(t, main, guessE2EUserA),
		"全场唯一一注押中了自己,拿回的必须是整整 1000")
}

// 竞猜**不能**配奖品:奖品是平台净增发,一旦允许,平台就真的可能倒贴。
//
// buildActivity 的 KindGuess 分支根本不调 buildPrizes,所以带 prizes 的请求
// 只会被静默忽略。这条断言把"忽略"钉成事实:库里一行奖档都不许有。
func TestGuessCannotCarryPrizes_NoNetIssuancePathExists(t *testing.T) {
	ext, _, r := guessE2EEnv(t, 100_000)
	now := common.GetTimestamp()
	create := `{
		"kind":"guess","title":"带着奖品的竞猜","stake_quota":1000,"fee_bps":500,
		"open_at":` + strconv.FormatInt(now-60, 10) + `,
		"close_at":` + strconv.FormatInt(now+3600, 10) + `,
		"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
		"options":[{"opt_no":1,"label":"会涨"},{"opt_no":2,"label":"不会涨"}],
		"prizes":[{"tier":1,"name":"一等奖","prize_type":"quota","count":1,"amount_quota":4000000}]}`
	code, body := guessCall(t, r, http.MethodPost, "/admin/lottery/activities", create)
	require.Equalf(t, http.StatusOK, code, "建竞猜失败: %s", body)
	actNo := jsonString(t, body, "data", "act_no")

	var act Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
	var prizes int64
	require.NoError(t, ext.Model(&Prize{}).Where("act_id = ?", act.Id).Count(&prizes).Error)
	assert.Zero(t, prizes,
		"竞猜挂上了奖档 —— 奖品对用户额度是净增发,竞猜一旦能配奖品,平台就真的会倒贴")

	// 创建接口自陈的"最坏净增发"也必须是 0。它是运营在发布按钮之前看到的那个数,
	// 与库里没有奖档这件事必须一致 —— 两者漂开时,界面会替一场根本发不出奖的
	// 活动报一个金额。
	assert.EqualValues(t, 0, jsonNumber(t, body, "data", "worst_case_net_issue"),
		"竞猜的最坏净增发必须恒为 0:它的赔付全部从池子里切,没有第二条出钱的路")
}

// jsonNumber 从响应信封里按路径取一个数字字段。
func jsonNumber(t *testing.T, body []byte, path ...string) float64 {
	t.Helper()
	var root map[string]any
	require.NoErrorf(t, common.Unmarshal(body, &root), "响应不是合法 JSON: %s", body)
	var cur any = root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		require.Truef(t, ok, "路径 %v 在 %s 上走不通", path, body)
		cur = m[key]
	}
	out, ok := cur.(float64)
	require.Truef(t, ok, "%v 不是数字: %s", path, body)
	return out
}

// 手续费万分比越界一律 400,而不是被截断成一个"差不多"的值。
//
// 上界写在 max_guess_fee_bps 里(这里 2000 = 20%);负数是"平台倒贴给赢家"的
// 唯一入口,必须在请求校验就被挡住,而不是等 SplitPool 把它钳成 0。
func TestGuessFeeBpsOutOfRangeIsRejected(t *testing.T) {
	_, _, r := guessE2EEnv(t, 100_000)
	now := common.GetTimestamp()
	for _, tc := range []struct {
		name   string
		feeBps int
	}{
		{"负费率就是平台倒贴", -1},
		{"超过运营上界", 2001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create := `{
				"kind":"guess","title":"越界费率 ` + tc.name + `","stake_quota":1000,
				"fee_bps":` + strconv.Itoa(tc.feeBps) + `,
				"open_at":` + strconv.FormatInt(now-60, 10) + `,
				"close_at":` + strconv.FormatInt(now+3600, 10) + `,
				"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
				"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
				"options":[{"opt_no":1,"label":"甲"},{"opt_no":2,"label":"乙"}]}`
			code, body := guessCall(t, r, http.MethodPost, "/admin/lottery/activities", create)
			assert.Equalf(t, http.StatusBadRequest, code, "越界费率被放行了: %s", body)
		})
	}
}

// 全额退回那一支也必须与资金单交叉核对,否则平台真的会净流出。
//
// # 这一条挡的是什么
//
// 「全部猜错」与「全场押中同一项」两种收场都落在 isFullRefundOutcome 里,
// runSettle 随后会再跑一次 planFullRefund —— 而那一次是**核对资金单**的
// (refundAmountOf,两者不等按较小值退并落 refund_drift)。可是
// settleGuessResult 先登记过一批退款计划,两条路径共用
// uk(act_id, entry_id, kind),**先登记的那一条赢**。
//
// 于是只要在封盘之后把 qy_lot_entry.amount 与 qy_lot_activity.pool_quota
// 同时改大(「重算奖池 == 物化计数」那条交叉核对只要求两个数互相一致,
// 一起改就照样通过),平台就会退出一笔从没收进来的钱。
//
// 这里真的做那次篡改,并核对:退款按资金单封顶、主库额度不多一分、
// 库里留下一条 refund_drift。
func TestGuessFullRefundIsCappedByFundOrder(t *testing.T) {
	const start = 100_000
	ext, main, r := guessE2EEnv(t, start)
	actNo := createGuess(t, r, 500, 1000)

	// 三个人押同一项 → 全场押中,走全额退回。
	placeBet(t, r, actNo, guessE2EUserA, 1, 0)
	placeBet(t, r, actNo, guessE2EUserB, 1, 0)
	placeBet(t, r, actNo, guessE2EUserC, 1, 0)

	var act Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
	require.NoError(t, lockActivity(context.Background(), ext, &act))

	// 篡改:把 A 那一条从 1000 改成 900000,并把活动行的物化奖池一起垫高,
	// 好让 settleGuessResult 里"重算奖池 == 物化计数"那条判定照常通过。
	var victim Entry
	require.NoError(t, ext.Where("act_id = ? AND user_id = ?", act.Id, guessE2EUserA).
		Take(&victim).Error)
	require.NoError(t, ext.Model(&Entry{}).Where("id = ?", victim.Id).
		Update("amount", 900_000).Error)
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
		Update("pool_quota", 900_000+1000+1000).Error)

	code, body := guessCall(t, r, http.MethodPost,
		"/admin/lottery/activities/"+actNo+"/guess-result",
		`{"opt_no":1,"evidence":"官网公告"}`)
	require.Equalf(t, http.StatusOK, code, "录结果失败: %s", body)
	for i := 0; i < 10; i++ {
		var live int64
		require.NoError(t, ext.Model(&Payout{}).
			Where("act_id = ? AND status IN ?", act.Id,
				[]string{PayoutPlanned, PayoutPaying, PayoutFailed}).
			Count(&live).Error)
		if live == 0 {
			break
		}
		DrivePayouts(context.Background())
	}

	// 退款按资金单封顶:被改过的那一条仍然只退 1000。
	var plan Payout
	require.NoError(t, ext.Where("act_id = ? AND entry_id = ? AND kind = ?",
		act.Id, victim.Id, PayoutRefund).Take(&plan).Error)
	assert.EqualValues(t, 1000, plan.AmountQuota,
		"退款金额跟着被篡改的 entry.amount 走了 —— 平台退出了一笔从没收进来的钱")

	for _, id := range guessE2EBettors() {
		assert.EqualValues(t, start, quotaOf(t, main, id),
			"用户 %d 的额度不该因为一次篡改而变多", id)
	}

	var flags int64
	require.NoError(t, ext.Model(&Flag{}).
		Where("act_id = ? AND code = ?", act.Id, FlagRefundDrift).Count(&flags).Error)
	assert.EqualValues(t, 1, flags,
		"金额被改过却一条痕迹都没有 —— 退款封顶必须同时留痕,否则没人知道发生过")
}
