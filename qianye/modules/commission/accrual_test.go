package commission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalcGrossKeepsFullPrecision(t *testing.T) {
	// units 是内部整数费率(百分比 × 100):500 = 5%,1025 = 10.25%。
	cases := []struct {
		base  int64
		units int
		want  string
	}{
		{10, 500, "0.5"},   // 裸 int 转换会变成 0
		{1, 500, "0.05"},   // 裸 int 转换会变成 0
		{3, 333, "0.0999"}, // 3.33% 这种带小数的比例也不能丢
		{200, 500, "10"},
		{1_000_000, 1000, "100000"},
		{10000, 1025, "1025"}, // 两位小数的百分比必须精确
		{1, 1, "0.0001"},      // 0.01% 是最小可配的非零费率
		{0, 500, "0"},
		{100, 0, "0"},
		{-100, 500, "0"}, // 负基数由冲正路径显式构造,正向计佣拒绝
	}
	for _, tc := range cases {
		got := calcGross(tc.base, tc.units)
		assert.Equal(t, tc.want, got.String(), "base=%d units=%d", tc.base, tc.units)
	}
}

func TestCapGross(t *testing.T) {
	g := decimal.RequireFromString("120.5")

	// 第二个返回值是"削掉了多少"。它必须与封顶后的金额一起交出来,否则
	// 那一行从此 base × rate ≠ gross 而没有任何字段解释得了差额。
	for _, tc := range []struct {
		name         string
		gross        decimal.Decimal
		cap          int64
		want, shaved string
	}{
		{"上限为 0 表示不限制", g, 0, "120.5", "0"},
		{"触顶", g, 100, "100", "20.5"},
		{"没触顶", g, 1000, "120.5", "0"},
		// 冲正是负额,封顶必须对称,否则一笔巨额退款会把邀请人的余额抽干。
		{"负额触顶", g.Neg(), 100, "-100", "-20.5"},
	} {
		got, shaved := capGross(tc.gross, tc.cap)
		assert.Equal(t, tc.want, got.String(), tc.name)
		assert.Equal(t, tc.shaved, shaved.String(), tc.name+" 的削减量")
		// 恒等式:封顶后的金额 + 削减量 == 原始金额。落库之后它就是
		// gross_amount + capped_amount == base_quota × rate_bps / 10000。
		assert.True(t, got.Add(shaved).Equal(tc.gross), tc.name+" 削减量必须补得平")
	}
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
	vip := rateDecision{Units: 500, Group: "vip"}
	fx := fiatDecision{Rate: decimal.NewFromFloat(7.3), Layer: fiatLayerGroup, Group: "vip"}
	assert.Equal(t, "consume:7:20260730:vip:500:7.3:h7:u3", consumeIdemKey(3, 7, "20260730", vip, fx, 7))

	// 费率或分组一变就必须换一行:日聚合桶是"边增长边结算"的,把新费率
	// 算出的 gross 累加进一行标着旧费率的记录里,那一行从此
	// base × rate ≠ gross,永远对不平也没法向用户解释。
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, fx, 7),
		consumeIdemKey(3, 7, "20260730", rateDecision{Units: 800, Group: "vip"}, fx, 7))
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, fx, 7),
		consumeIdemKey(3, 7, "20260730", rateDecision{Units: 500, Group: "default"}, fx, 7))
	// 法币折算比例同理:usd_rate 不参与 gross 的算术,但结算按它的加权平均
	// 折算 available_fiat —— 一行标着 7.3 却有一半 gross 是在比例改成 7.5
	// 之后挣的,那半笔钱就永久按旧比例入账,而账面上看不出这里调过价。
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, fx, 7),
		consumeIdemKey(3, 7, "20260730", vip,
			fiatDecision{Rate: decimal.NewFromFloat(7.5), Layer: fiatLayerGroup, Group: "vip"}, 7))
	// Matched / Layer / Group 只用于日志与管理端解释,不参与算钱,
	// 更不该影响幂等键 —— 同一个比例走哪一层落到账上是同一笔钱。
	assert.Equal(t, consumeIdemKey(3, 7, "20260730", vip, fx, 7),
		consumeIdemKey(3, 7, "20260730", rateDecision{Units: 500, Group: "vip", Matched: true},
			fiatDecision{Rate: decimal.NewFromFloat(7.3), Layer: fiatLayerDefault}, 7))

	// 上线换了就必须换一行。ON CONFLICT 的 DoUpdates 只累加金额、不改
	// inviter_id,上线不在键里的话,换绑当天下线后续的消费会撞上旧上线那一行,
	// 钱被原子累加进去而 inviter_id 保持旧值 —— 结结实实发给了前一个上线,
	// 而三条恒等式全部成立,没有任何降级计数器会响。
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, fx, 7),
		consumeIdemKey(4, 7, "20260730", vip, fx, 7))

	// 成熟期变了也必须换一行。日聚合桶的 ON CONFLICT 只累加金额、**不改
	// mature_at**:成熟期不在键里的话,运营中午把 holding_days 从 7 改成 0,
	// 当天已经建过桶的下线在那之后的消费会累加进一行标着旧成熟期的记录里,
	// 那部分钱按旧策略再压 7 天,而界面按新配置写着 T+1。
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, fx, 7),
		consumeIdemKey(3, 7, "20260730", vip, fx, 0))
	// 负的成熟期与 0 必须落同一个键:bucketMatureAt 把负数钳到 0,键里不钳的话
	// 同一个成熟时刻会分裂成两个桶。
	assert.Equal(t, consumeIdemKey(3, 7, "20260730", vip, fx, 0),
		consumeIdemKey(3, 7, "20260730", vip, fx, -1))

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

// TestHoldingDaysZeroFromYAMLMaturesSameDay 走完整条链路:YAML 里的 0 →
// config.Load → 计提行的 mature_at。
//
// 上一条测试只喂常量 0,而实际出事的地方在配置层:holding_days: 0 被
// 静默补成默认的 7,mature_at 落在"当天结束 + 7 天",结算条件
// mature_at <= now 于是要等 8 天才成立,qy_commission_balance 一直空着。
// 运营查配置看到 0,用户看到可提现佣金为 0,双方都以为是对方的问题。
func TestHoldingDaysZeroFromYAMLMaturesSameDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
commission:
  enabled: true
  holding_days: 0
`), 0o600))
	t.Setenv(config.EnvConfigPath, path)

	prev := qyConfig.Load()
	t.Cleanup(func() { qyConfig.Store(prev) })
	require.NoError(t, config.Load())

	holding := config.Get().Commission.HoldingDays
	require.Equal(t, 0, holding, "显式写的 0 不得被默认值替换")

	dayStart := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC).Unix()
	assert.Equal(t, dayStart+86400, bucketMatureAt("20260730", holding),
		"成熟期为 0 时佣金应当在当天结束即可结算,而不是再等 7 天")
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
	assert.EqualValues(t, common.MaxQuota, quotaFromDecimal(decimal.NewFromInt(int64(common.MaxQuota)+1)))
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
