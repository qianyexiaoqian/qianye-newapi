package violation

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// aireview_cost_visibility_test.go —— 成本页上的那个数字**准不准**必须查得出来。
//
// # 这里守的不是"花了多少",而是"这个数能不能信"
//
// 花费的合计口径由 aireview_failover_test.go 的 TestChainCostCountsEveryAttempt
// 守着(链上每一次调用都要记)。这一份守的是它的另一半:合计有时候只是一个
// **下界**,而界面必须知道自己拿到的是下界。
//
// 成本页原先用 `total_tokens > 0 AND cost_usd <= 0` 找"算不出钱的调用"。
// 那个判据只认得"一分钱都没算出来"这一种。故障转移之后出现了第二种:
//
//	混价链  一个填了单价的渠道 + 一个没填的,两次都产生了 token。
//	        cost_usd 是**正数**,于是它从旧判据下面溜过去 —— 界面把一个偏低的
//	        数字当成准确值展示,而"这个月比预算省了 40%"没有人会来查。
//
// 池子里只要有一个渠道没填单价,加权随机随时会抽到它,所以这不是理论情形。

// TestAIReviewRowFlagsUnderstatedCost 钉住落库那一步的判据。
//
// 断言的是 CostUnknown 而不是 Priced:Priced 早就算对了(它在
// TestChainCostCountsEveryAttempt 里有断言),缺的是把它**写下来** ——
// 统计只能查库,查不到一个只活在内存里的布尔。
func TestAIReviewRowFlagsUnderstatedCost(t *testing.T) {
	tests := []struct {
		name string
		out  *aiOutcome
		want bool
		why  string
	}{
		{
			name: "链上每一次调用都有单价:这个数字是真值",
			out: &aiOutcome{Outcome: OutcomeClean, TotalTokens: 2000,
				CostUsd: decimal.RequireFromString("0.0023"), Priced: true},
			want: false,
		},
		{
			name: "混价链:cost_usd 是正数,但它只是下界",
			out: &aiOutcome{Outcome: OutcomeClean, TotalTokens: 2000,
				CostUsd: decimal.RequireFromString("0.0012"), Priced: false},
			want: true,
			why:  "这一格正是 `cost_usd <= 0` 那个旧判据漏掉的那一种",
		},
		{
			name: "整条链都没单价:一分钱都算不出来",
			out: &aiOutcome{Outcome: OutcomeClean, TotalTokens: 1500,
				CostUsd: decimal.Zero, Priced: false},
			want: true,
		},
		{
			name: "一次调用都没产生 token(全是连不上)",
			out:  &aiOutcome{Outcome: OutcomeUpstreamError, CostUsd: decimal.Zero, Priced: false},
			want: false,
			why: "这一行的花费 0 是**准的**。标成算不准会让「成本被低估」" +
				"这条告警被一堆网络故障灌满,于是真正被低估的那几行淹在里面",
		},
		{
			name: "一个渠道都没配",
			out:  nil,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := newAIReviewRow(recordCtx{RequestId: "req-1", UserId: 7}, PhasePrompt, tc.out, 0, 0)
			require.NotNil(t, row)
			assert.Equal(t, tc.want, row.CostUnknown, tc.why)
		})
	}
}

// TestAIReviewStatsCountsUnderstatedCostChains 走完整条路:
// 真的跑一条混价重试链 → 落库 → 打统计接口。
//
// 不手工造那一行:标记从 aiOutcome.Priced 到 AIReview.CostUnknown 再到统计
// SQL,中间任何一段漏掉,界面拿到的都是同一个症状(一个偏低的数字,没有告警)。
// 只测最后那条 SQL 会得到一组全绿的断言,而线上照旧不提示。
func TestAIReviewStatsCountsUnderstatedCostChains(t *testing.T) {
	gdb := newAIReviewStatsEnv(t)

	// 第一个渠道:回得出 usage、回不出结构化结论(bad_json,唯一一种**已经
	// 付过钱**的可重试失败),而且没填单价。第二个渠道填了单价并给出结论。
	badJSON := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"这段内容我觉得还行"}}],` +
			`"usage":{"prompt_tokens":900,"completion_tokens":100,"total_tokens":1000}}`))
	})
	good := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(okVerdict(false, "none", 0.1, 800, 200)))
	})
	rt := rtWith(chRT(1, "没填单价的", badJSON.URL, 1), chRT(2, "填了单价的", good.URL, 1))
	rt.Channels[0].PriceInPerM = decimal.Zero
	rt.Channels[0].PriceOutPerM = decimal.Zero
	// 指定 + 转移:链的顺序因此是确定的(先坏后好),期望值才算得死。
	out := runAIReview(context.Background(), rt,
		&aiScopeRT{ChannelId: 1, ChannelFailover: true}, "内容", 3000)

	require.NotNil(t, out)
	require.Equal(t, OutcomeClean, out.Outcome)
	require.Equal(t, 2, out.Attempts)
	require.Equal(t, 2000, out.TotalTokens, "两次调用都真的发生了")
	// 独立算出的期望:算得出的那一半是 800×$1/M + 200×$2/M = $0.0012。
	// 另外那 1000 个 token 算不出钱,所以 0.0012 是**下界**,不是真值。
	require.Equal(t, "0.0012", out.CostUsd.String())

	mixed := newAIReviewRow(recordCtx{RequestId: "mixed", UserId: 7}, PhasePrompt, out, 0, 0)
	require.True(t, mixed.CostUnknown)
	require.NoError(t, gdb.Create(mixed).Error)

	// 另外三行覆盖其余三种形态。它们与上面那一行一起,才说得清判据的边界。
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&[]AIReview{
		{
			ReviewNo: "ai_allpriced_prompt", UserId: 7, Phase: PhasePrompt,
			Outcome: OutcomeClean, TotalTokens: 2000, PromptTokens: 1700, CompletionTokens: 300,
			CostUsd: decimal.RequireFromString("0.0023"), CostUnknown: false,
			Attempts: 2, CreatedAt: now,
		},
		{
			ReviewNo: "ai_nopriceatall_prompt", UserId: 7, Phase: PhasePrompt,
			Outcome: OutcomeClean, TotalTokens: 1500, PromptTokens: 1000, CompletionTokens: 500,
			CostUsd: decimal.Zero, CostUnknown: true,
			Attempts: 1, CreatedAt: now,
		},
		{
			// 一个 token 都没有:全是连不上。这一行的 0 是准的。
			ReviewNo: "ai_notokens_prompt", UserId: 7, Phase: PhasePrompt,
			Outcome: OutcomeUpstreamError, CostUsd: decimal.Zero, CostUnknown: false,
			Attempts: 2, CreatedAt: now,
		},
	}).Error)

	data := readViolationData(t, "/violation/ai-review/stats?days=7", adminAIReviewStats)

	var unpriced int64
	require.NoError(t, common.Unmarshal(data["unpriced_calls"], &unpriced))
	assert.Equal(t, int64(2), unpriced,
		"混价链那一行的 cost_usd 是正数(0.0012),旧判据 `cost_usd <= 0` 看不见它 —— "+
			"于是界面把一个偏低的数字当成准确值展示,而偏低正是最没人会去核对的方向")

	var totalCost string
	require.NoError(t, common.Unmarshal(data["total_cost_usd"], &totalCost))
	assert.Equal(t, "0.0035", totalCost,
		"0.0012(下界)+ 0.0023 + 0 —— 总额本身没变,变的是它旁边那句"+
			"「这个数是下界」还说不说得出口")
}

// newAIReviewStatsEnv 接上一个只承载审核明细的内存库。
//
// 单独一份而不是复用 newEmptyViolationEnv:那一份没有建 AIReview 表,而统计
// 接口在表不存在时会走 internalError 返回 500,断言会退化成"接口挂了"。
func newAIReviewStatsEnv(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&AIReview{}))

	prevHandle := qyDBHandleForCtxTest.Swap(gdb)
	prevHealthy := qyDBHealthyForJSONTest.Swap(true)
	prevCfg := qyConfigForJSONTest.Swap(&config.Config{
		Enabled:   true,
		Violation: config.Violation{Enabled: true},
	})
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prevHandle)
		qyDBHealthyForJSONTest.Store(prevHealthy)
		qyConfigForJSONTest.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}
