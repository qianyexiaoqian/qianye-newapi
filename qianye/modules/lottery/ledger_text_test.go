package lottery

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ledger_text_test.go —— 账本日志正文里的金额必须是**站内余额**。
//
// 这不是措辞偏好。抽奖是模块里唯一直接改用户 quota 的两条路径,而用户核对
// "我到底被扣了多少 / 到账了多少"的地方只有原项目的「日志」页。那一页上的
// 消费、签到、划转全部已经是站内余额,唯独抽奖两行是 quota 原始整数 ——
// 同一列里 `＄0.002000 额度` 与 `1000 额度` 并排,读者只能理解成两笔差 500 倍
// 的钱。用户据此判断"是不是被多扣了"并来找客服,是这条缺陷真正的代价。
//
// 三条断言:
//  1. 换算跟着站点的额度展示设置走(货币制 / TOKENS 两种都要对);
//  2. 扣费与派奖两条路径共用同一个拼法,不会各漂各的;
//  3. 换算**只改文案** —— 落库、进哈希、进 other 的仍是原始整数,
//     所以这里同时钉死"文案里不出现裸 quota"。

// withQuotaDisplay 把站点的额度展示设置临时切到指定形态。
//
// generalSetting 是包级单例,测试改完必须还原,否则同包里其它用到
// logger.LogQuota / FormatQuota 的测试会读到被污染的展示口径。
func withQuotaDisplay(t *testing.T, displayType, symbol string, rate float64) {
	t.Helper()
	g := operation_setting.GetGeneralSetting()
	old := *g
	t.Cleanup(func() { *operation_setting.GetGeneralSetting() = old })
	g.QuotaDisplayType = displayType
	g.CustomCurrencySymbol = symbol
	g.CustomCurrencyExchangeRate = rate
}

func TestLedgerLogContentRendersSiteBalance(t *testing.T) {
	// 期望值全部按 common.QuotaPerUnit 独立算出:
	//   500000 quota = 1 USD、1250 quota = 0.0025 USD。
	require.Equal(t, 500000.0, common.QuotaPerUnit,
		"下面的期望值是按 500000 quota = 1 USD 手算的,单位变了必须重算")
	require.Equal(t, 7.3, operation_setting.USDExchangeRate,
		"CNY 一行的期望值按 1 USD = 7.3 CNY 手算")

	cases := []struct {
		name        string
		displayType string
		symbol      string
		rate        float64
		action      string
		quota       int64
		no          string
		want        string
	}{
		{
			name:        "USD 扣费",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			action:      "参与活动扣除",
			quota:       1250,
			no:          "LOTE0001",
			// 1250 / 500000 = 0.0025
			want: "参与活动扣除 ＄0.002500 额度 [LOTE0001]",
		},
		{
			name:        "USD 派奖",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			action:      "抽奖中奖到账",
			quota:       500000,
			no:          "LOTP0001",
			want:        "抽奖中奖到账 ＄1.000000 额度 [LOTP0001]",
		},
		{
			name:        "CNY 派奖",
			displayType: operation_setting.QuotaDisplayTypeCNY,
			action:      "竞猜赔付到账",
			quota:       500000,
			no:          "LOTP0002",
			// 1 USD × 7.3
			want: "竞猜赔付到账 ¥7.300000 额度 [LOTP0002]",
		},
		{
			name:        "自定义币种退款",
			displayType: operation_setting.QuotaDisplayTypeCustom,
			symbol:      "¤",
			rate:        2,
			action:      "活动退款到账",
			quota:       250000,
			no:          "LOTP0003",
			// 0.5 USD × 2
			want: "活动退款到账 ¤1.000000 额度 [LOTP0003]",
		},
		{
			// 关掉货币展示时的回落形态:原始整数 + 「点额度」。
			// 用户仍然看得到那个大整数,但它带着单位,不会与货币制的数字混淆。
			name:        "TOKENS 回落成原始点数",
			displayType: operation_setting.QuotaDisplayTypeTokens,
			action:      "参与活动扣除",
			quota:       1250,
			no:          "LOTE0002",
			want:        "参与活动扣除 1250 点额度 [LOTE0002]",
		},
		{
			// 0 是合法值:文本奖的派奖腿金额恒为 0。
			name:        "零额文本奖",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			action:      "抽奖中奖到账",
			quota:       0,
			no:          "LOTP0004",
			want:        "抽奖中奖到账 ＄0.000000 额度 [LOTP0004]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withQuotaDisplay(t, tc.displayType, tc.symbol, tc.rate)
			assert.Equal(t, tc.want, ledgerLogContent(tc.action, tc.quota, tc.no))
		})
	}
}

// 货币制下正文里**不得**出现裸 quota 整数。
//
// 单独一条是因为上一个表只能证明"当前拼法的输出等于期望";有人把
// logger.LogQuota 换回 %d 时,期望值会被一并改掉而测试照样全绿。这一条钉的是
// 不变量本身:1250 这个数字在货币制的文案里就是不该出现。
func TestLedgerLogContentDropsRawQuota(t *testing.T) {
	withQuotaDisplay(t, operation_setting.QuotaDisplayTypeUSD, "", 0)

	got := ledgerLogContent("参与活动扣除", 1250, "LOTE0003")
	assert.NotContains(t, got, "1250",
		"货币制下正文出现了原始 quota,说明金额没走 logger.LogQuota")
	assert.Contains(t, got, "＄")
}

// 扣费与派奖两条路径的文案形状必须一致。
//
// 它们在「日志」页是上下相邻的两行。两边各自 fmt.Sprintf 是本仓反复出现的漂移
// 形状,而这里漂移的后果是同一场活动的一进一出看起来像两个不同系统写的。
func TestLedgerLogContentSharedShape(t *testing.T) {
	withQuotaDisplay(t, operation_setting.QuotaDisplayTypeUSD, "", 0)

	debit := ledgerLogContent("参与活动扣除", 500000, "LOTE0004")
	credit := ledgerLogContent(payoutLabel(PayoutPrize), 500000, "LOTP0005")

	assert.Equal(t, "参与活动扣除 ＄1.000000 额度 [LOTE0004]", debit)
	assert.Equal(t, "抽奖中奖到账 ＄1.000000 额度 [LOTP0005]", credit)
}
