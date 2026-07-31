package commission

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalcGrossKeepsFullPrecision(t *testing.T) {
	cases := []struct {
		base int64
		bps  int
		want string
	}{
		{10, 500, "0.5"},   // 裸 int 转换会变成 0
		{1, 500, "0.05"},   // 裸 int 转换会变成 0
		{3, 333, "0.0999"}, // 万分之一级别的比例也不能丢
		{200, 500, "10"},
		{1_000_000, 1000, "100000"},
		{0, 500, "0"},
		{100, 0, "0"},
		{-100, 500, "0"}, // 负基数由冲正路径显式构造,正向计佣拒绝
	}
	for _, tc := range cases {
		got := calcGross(tc.base, tc.bps)
		assert.Equal(t, tc.want, got.String(), "base=%d bps=%d", tc.base, tc.bps)
	}
}

func TestCapGross(t *testing.T) {
	g := decimal.RequireFromString("120.5")
	assert.Equal(t, "120.5", capGross(g, 0).String(), "上限为 0 表示不限制")
	assert.Equal(t, "100", capGross(g, 100).String())
	assert.Equal(t, "120.5", capGross(g, 1000).String())
	// 冲正是负额,封顶必须对称,否则一笔巨额退款会把邀请人的余额抽干。
	assert.Equal(t, "-100", capGross(g.Neg(), 100).String())
}

// TestNormalizeIdemKeyIsInjective 确认超长键不会被截断成同一个值。
//
// trade_no 在主库是 varchar(255)。若直接截断到 96,两个前缀相同的订单会
// 撞成同一个幂等键 —— 第二笔充值就永远不会返佣。
func TestNormalizeIdemKeyIsInjective(t *testing.T) {
	short := SourceTopup + ":TX123456"
	assert.Equal(t, short, normalizeIdemKey(short))

	prefix := strings.Repeat("A", 120)
	a := normalizeIdemKey(prefix + "1")
	b := normalizeIdemKey(prefix + "2")
	require.NotEqual(t, a, b)
	assert.True(t, len(a) <= 96)
	assert.True(t, strings.HasPrefix(a, "h:"))
	assert.Equal(t, a, normalizeIdemKey(prefix+"1"), "同一输入必须稳定")
}

func TestIdemKeyShapes(t *testing.T) {
	assert.Equal(t, "consume:7:20260730", consumeIdemKey(7, "20260730"))
	assert.Equal(t, "topup:TX-1", topupIdemKey(" TX-1 "))
	assert.Equal(t, "redemption:99", redemptionIdemKey(99))

	// 有任务号时冲正键必须可复现:worker 重试会用同一个键,
	// 否则一次退款会被冲正好几次。
	k1 := clawbackIdemKey("task-abc", 5, 100)
	assert.Equal(t, k1, clawbackIdemKey("task-abc", 5, 100))
	assert.NotEqual(t, k1, clawbackIdemKey("task-abc", 5, 101))
	// 无任务号时上游没给任何稳定标识,只能一次性随机。
	assert.NotEqual(t, clawbackIdemKey("", 5, 100), clawbackIdemKey("", 5, 100))
}

// TestBucketBoundaries 锁定日聚合的时间口径。
//
// 必须是 UTC:多节点分布在不同时区时,用本地时间会让同一笔消费在不同
// 节点落进不同的桶,唯一索引失效、行数翻倍、结算重复。
func TestBucketBoundaries(t *testing.T) {
	dayStart := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix()
	assert.Equal(t, "20260730", bucketDate(dayStart))
	assert.Equal(t, "20260730", bucketDate(dayStart+86399))
	assert.Equal(t, "20260731", bucketDate(dayStart+86400))

	// 成熟时间从"当天结束"起算,而不是从首次写入起算:后者会让当天
	// 晚些时候的消费提前成熟,削弱成熟期的防套利作用。
	assert.Equal(t, dayStart+86400+7*86400, bucketMatureAt("20260730", 7))
	assert.Equal(t, dayStart+86400, bucketMatureAt("20260730", 0))
}

func TestAmountSaneRejectsAbsurdValues(t *testing.T) {
	assert.True(t, amountSane(decimal.RequireFromString("123456789.0123456789")))
	assert.True(t, amountSane(decimal.NewFromInt(-1000)))
	assert.False(t, amountSane(decimal.New(1, 20)))
	assert.False(t, amountSane(decimal.New(-1, 20)))
}

// TestTopUpBaseQuotaByProvider 锁定各支付渠道的额度换算口径。
//
// 统一按 Amount × QuotaPerUnit 会算错:creem 的 Amount 本身就是额度,
// stripe 与订阅付费单要按 Money 换算。算错基数 = 返错佣金。
func TestTopUpBaseQuotaByProvider(t *testing.T) {
	original := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	defer func() { common.QuotaPerUnit = original }()

	cases := []struct {
		name      string
		topUp     model.TopUp
		wantQuota int64
		wantMoney string
	}{
		{
			name:      "creem 的 Amount 本身就是额度",
			topUp:     model.TopUp{PaymentProvider: model.PaymentProviderCreem, Amount: 1_500_000, Money: 3},
			wantQuota: 1_500_000,
			wantMoney: "3",
		},
		{
			name:      "stripe 按 Money 换算",
			topUp:     model.TopUp{PaymentProvider: model.PaymentProviderStripe, Amount: 0, Money: 10},
			wantQuota: 5_000_000,
			wantMoney: "10",
		},
		{
			name:      "订阅付费单的 provider 为空,同样按 Money 换算",
			topUp:     model.TopUp{PaymentProvider: "", Amount: 0, Money: 2.5},
			wantQuota: 1_250_000,
			wantMoney: "2.5",
		},
		{
			name:      "epay 按 Amount 换算",
			topUp:     model.TopUp{PaymentProvider: model.PaymentProviderEpay, Amount: 4, Money: 29.2},
			wantQuota: 2_000_000,
			wantMoney: "29.2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quota, money := topUpBaseQuota(&tc.topUp)
			assert.Equal(t, tc.wantQuota, quota)
			assert.Equal(t, tc.wantMoney, money.String())
		})
	}
}

// TestQuotaFromDecimalNeverNegative 确认被篡改/异常的订单金额不会变成
// 负数基数 —— 负基数会算出负佣金,那是一笔凭空的欠账。
func TestQuotaFromDecimalNeverNegative(t *testing.T) {
	assert.EqualValues(t, 0, quotaFromDecimal(decimal.NewFromInt(-100)))
	assert.EqualValues(t, 10, quotaFromDecimal(decimal.RequireFromString("10.9")))
	assert.EqualValues(t, common.MaxQuota, quotaFromDecimal(decimal.NewFromInt(9_000_000_000)))
}

// TestExcludedTopUp 锁定充值口径。
func TestExcludedTopUp(t *testing.T) {
	// 用余额支付的订单必须硬排除:那笔余额充值进来时已经返过一次佣金。
	assert.True(t, excludedTopUp(&model.TopUp{PaymentProvider: model.PaymentProviderBalance}))
	assert.True(t, excludedTopUp(&model.TopUp{PaymentMethod: model.PaymentMethodBalance}))
	// 默认宽松:真实支付渠道一律返佣。
	assert.False(t, excludedTopUp(&model.TopUp{PaymentProvider: model.PaymentProviderEpay}))
	assert.False(t, excludedTopUp(&model.TopUp{PaymentProvider: model.PaymentProviderStripe}))
}
