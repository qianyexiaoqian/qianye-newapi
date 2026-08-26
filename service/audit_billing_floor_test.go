package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audit_billing_floor_test.go —— 「一次结算永远不能变成一次充值」这条地板的
// 两个漏点。两条都是实测能凭空生成额度 / 凭空免单的资金缺陷。
//
// ═══════════ 漏点一:阶梯计价 + 工具附加费 ═══════════
//
// TryTieredSettle 在返回前把负的表达式结果夹到 0 并打了 SysError。但只要这一笔
// 带任何工具附加费(web_search 的单价是硬编码默认 10.0,不需要运营配任何东西),
// composeTieredTextQuota 就不用那个已夹好的值,而是拿**没夹过**的
// ActualQuotaBeforeGroup 重新算一遍再加附加费 —— 负值原样返回。
// 负 quota → 负 delta → 资金来源执行 Increase,扣费变充值,循环无上限。
//
// ═══════════ 漏点二:上游报负 token 数 ═══════════
//
// summary.TotalTokens = PromptTokens + CompletionTokens 不夹分量,
// hasBillableUsage() 只看合计 > 0。prompt=1000 / completion=-1000000 于是被判成
// 「这笔没有可计费用量」,连真实发生的 prompt 侧一起免单,而且那行 quota=0 的
// 日志和真正的免费模型长得一模一样。

// TestTieredSurchargeCompositionNeverCreditsTheUser 把「表达式算出负数」与
// 「这一笔带工具附加费」这两件事同时喂进去 —— 那正是唯一能绕过 TryTieredSettle
// 那道地板的组合。
//
// 期望是独立算出来的:表达式为负时那一段按 0 计,最终金额 == 纯附加费;
// 表达式为正时逐位等于 before_group × group_ratio + 附加费。
func TestTieredSurchargeCompositionNeverCreditsTheUser(t *testing.T) {
	cases := []struct {
		name            string
		beforeGroup     float64
		groupRatio      float64
		surchargeQuota  int64
		wantQuota       int
		wantNotNegative bool
	}{
		{
			// 实测形态:tier("promo", p*3 + c*15 - 20000) 在 p=100/c=0 上算出
			// -9850,附加费 5000 → 旧代码返回 -4850(每请求凭空生成 4850 额度)。
			name:            "negative expression plus a web_search surcharge bills only the surcharge",
			beforeGroup:     -9850,
			groupRatio:      1,
			surchargeQuota:  5000,
			wantQuota:       5000,
			wantNotNegative: true,
		},
		{
			// 表达式负得比附加费还多时旧代码同样返回负数,新代码仍然只收附加费。
			name:            "deeply negative expression still cannot produce a credit",
			beforeGroup:     -1_000_000,
			groupRatio:      1,
			surchargeQuota:  5000,
			wantQuota:       5000,
			wantNotNegative: true,
		},
		{
			// 分组倍率把正的表达式压成负数是不可能的,但倍率必须仍然被乘上:
			// 这一格确认地板没有把正常路径一起改掉。
			name:           "positive expression is unchanged: before_group x ratio + surcharge",
			beforeGroup:    100_000,
			groupRatio:     0.5,
			surchargeQuota: 5000,
			wantQuota:      55_000,
		},
		{
			name:           "positive expression with no group discount",
			beforeGroup:    12_345,
			groupRatio:     1,
			surchargeQuota: 1_000,
			wantQuota:      13_345,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relayInfo := &relaycommon.RelayInfo{
				OriginModelName:       "tiered-surcharge-model",
				TieredBillingSnapshot: makeSnapshot(`tier("promo", p * 3 - 20000)`, tc.groupRatio, 0, 0),
			}
			summary := textQuotaSummary{
				ToolCallSurchargeQuota: decimal.NewFromInt(tc.surchargeQuota),
			}
			tr := &billingexpr.TieredResult{ActualQuotaBeforeGroup: tc.beforeGroup}

			// tieredQuota 传的是 TryTieredSettle 已经夹好的 0 —— 缺陷正是
			// 这一支把它丢掉,所以这里必须传"正确的那个值"才能证明它被丢了。
			got := composeTieredTextQuota(relayInfo, summary, 0, tr)

			assert.Equal(t, tc.wantQuota, got)
			if tc.wantNotNegative {
				assert.GreaterOrEqual(t, got, 0, "a settlement must never be a credit")
			}
		})
	}
}

// TestNegativeUpstreamTokensDoNotZeroTheWholeCharge 走真正的
// calculateTextQuotaSummary,因为缺陷不在某个算术式里,而在
// TotalTokens 与 hasBillableUsage() 这两步的配合上。
func TestNegativeUpstreamTokensDoNotZeroTheWholeCharge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name       string
		prompt     int
		completion int
		wantPrompt int
		wantComp   int
		wantQuota  int
	}{
		{
			// 实测形态:上游回 {prompt:1000, completion:-1000000} → 旧代码 quota=0,
			// 同一模型同一分组的正常 1000/1000 那笔收 4000。
			name:       "negative completion tokens must not cancel the real prompt side",
			prompt:     1000,
			completion: -1_000_000,
			wantPrompt: 1000,
			wantComp:   0,
			wantQuota:  1000,
		},
		{
			name:       "negative prompt tokens are clamped the same way",
			prompt:     -5000,
			completion: 200,
			wantPrompt: 0,
			wantComp:   200,
			wantQuota:  200,
		},
		{
			name:       "both negative means there really is nothing to bill",
			prompt:     -1,
			completion: -1,
			wantPrompt: 0,
			wantComp:   0,
			wantQuota:  0,
		},
		{
			name:       "ordinary usage is untouched",
			prompt:     1000,
			completion: 1000,
			wantPrompt: 1000,
			wantComp:   1000,
			wantQuota:  2000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

			relayInfo := &relaycommon.RelayInfo{
				OriginModelName: "negative-usage-model",
				UsingGroup:      "default",
				PriceData: hosttypes.PriceData{
					ModelRatio:      1,
					CompletionRatio: 1,
					GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
				},
			}
			summary := calculateTextQuotaSummary(ctx, relayInfo, &dto.Usage{
				PromptTokens:     tc.prompt,
				CompletionTokens: tc.completion,
				TotalTokens:      tc.prompt + tc.completion,
			})

			assert.Equal(t, tc.wantPrompt, summary.PromptTokens)
			assert.Equal(t, tc.wantComp, summary.CompletionTokens)
			assert.Equal(t, tc.wantQuota, summary.Quota)
			assert.GreaterOrEqual(t, summary.TotalTokens, 0)
		})
	}
}

// TestViolationFeeQuotaSaturatesInsteadOfWrappingOrVanishing 钉住违规罚款的额度
// 换算:此前它用 decimal…Round(0).IntPart() 再 int(),越界走 big.Int.Int64() 的
// 回绕 —— 一档算出垃圾值(3.7e13 → 53,255,926,290,448,384),另一档静默回绕成
// 负数被 `<=0 → 0` 吞掉(罚款凭空消失、零日志)。
//
// 期望值不是"某个具体数字",而是「永远落在 [0, MaxQuota] 里,且大额输入必须饱和
// 到 MaxQuota 而不是变成 0 或垃圾值」。
func TestViolationFeeQuotaSaturatesInsteadOfWrappingOrVanishing(t *testing.T) {
	cases := []struct {
		name       string
		amount     float64
		groupRatio float64
		want       int
	}{
		{name: "default fee is unchanged", amount: 0.05, groupRatio: 1, want: 25_000},
		{name: "group ratio applies", amount: 0.05, groupRatio: 0.5, want: 12_500},
		{name: "zero amount charges nothing", amount: 0, groupRatio: 1, want: 0},
		{name: "negative amount charges nothing", amount: -1, groupRatio: 1, want: 0},
		{name: "zero group ratio charges nothing", amount: 100, groupRatio: 0, want: 0},
		// 以下几档在旧实现下分别是 5e11 / 5e18 / 53255926290448384 / 0。
		// 5e11 现在落在 common.MaxQuota(2^43)以内,所以它的正确结果就是 5e11
		// 本身 —— 这一档要钉的是"不再回绕成垃圾值",不是"一定被夹住"。
		{name: "1e6 is representable and passes through", amount: 1e6, groupRatio: 1, want: 500_000_000_000},
		// 上界两侧各一格:QuotaPerUnit=500000 时最后一个可表示的 amount 是
		// (MaxQuota-1)/500000 = 17592186,再多一美元就必须饱和。
		{name: "one unit below the bound passes through", amount: float64((common.MaxQuota - 1) / 500_000),
			groupRatio: 1, want: (common.MaxQuota - 1) / 500_000 * 500_000},
		{name: "one unit above the bound saturates", amount: float64((common.MaxQuota-1)/500_000 + 1),
			groupRatio: 1, want: common.MaxQuota},
		{name: "1e13 saturates", amount: 1e13, groupRatio: 1, want: common.MaxQuota},
		{name: "3.7e13 saturates instead of wrapping to garbage", amount: 3.7e13, groupRatio: 1, want: common.MaxQuota},
		{name: "1.9e13 saturates instead of vanishing to zero", amount: 1.9e13, groupRatio: 1, want: common.MaxQuota},
		{name: "1e30 saturates instead of vanishing to zero", amount: 1e30, groupRatio: 1, want: common.MaxQuota},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := calcViolationFeeQuota(tc.amount, tc.groupRatio)
			assert.Equal(t, tc.want, got)
			assert.GreaterOrEqual(t, got, 0, "a fine must never be negative")
			assert.LessOrEqual(t, got, common.MaxQuota, "a fine must never exceed common.MaxQuota")
		})
	}
}

// TestSettlementFailureIsRecordedOnTheConsumeLog —— 结算失败时日志仍按全额记账,
// 那一行必须自己说出「这笔没收到」。
//
// 没有这个键的话,漏收的一笔与正常收讫的一笔在 logs 表里完全无法分辨,只有后端
// 日志里一句 LogError,对账不变量
// quota == subscription_consumed + wallet_quota_deducted + subscription_written_off
// 不成立却查不出差在哪。
func TestSettlementFailureIsRecordedOnTheConsumeLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	t.Run("failure is nested under admin_info", func(t *testing.T) {
		relayInfo := &relaycommon.RelayInfo{
			UserId:          77,
			OriginModelName: "settle-fail-model",
			BillingSource:   BillingSourceWallet,
			SettleFailure:   "dial tcp: connection refused",
		}
		other := map[string]interface{}{}
		attachSettleFailure(ctx, relayInfo, other)

		adminInfo, ok := other["admin_info"].(map[string]interface{})
		require.True(t, ok, "the marker must live under admin_info so non-admin log views strip it")
		marker, ok := adminInfo["settle_failed"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "dial tcp: connection refused", marker["error"])
		assert.Equal(t, BillingSourceWallet, marker["billing_source"])
	})

	t.Run("a successful settlement leaves no marker", func(t *testing.T) {
		relayInfo := &relaycommon.RelayInfo{UserId: 77, OriginModelName: "settle-fail-model"}
		other := map[string]interface{}{}
		attachSettleFailure(ctx, relayInfo, other)
		assert.NotContains(t, other, "admin_info")
	})
}
