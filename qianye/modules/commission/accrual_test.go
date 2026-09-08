package commission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/qianye/config"
	qydb "github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalcGrossKeepsFullPrecision(t *testing.T) {
	// units 是内部整数费率(百分比 × 100):500 = 5%,1025 = 10.25%。
	// qpu 是刻度(1 星屑 = 多少额度);gross 的单位是**星屑**。
	cases := []struct {
		base  int64
		units int
		qpu   int64
		want  string
	}{
		// qpu = 1 这一组只验"比例那一半"的精度,与改成星屑之前逐位一致。
		{10, 500, 1, "0.5"},   // 裸 int 转换会变成 0
		{1, 500, 1, "0.05"},   // 裸 int 转换会变成 0
		{3, 333, 1, "0.0999"}, // 3.33% 这种带小数的比例也不能丢
		{200, 500, 1, "10"},
		{1_000_000, 1000, 1, "100000"},
		{10000, 1025, 1, "1025"}, // 两位小数的百分比必须精确
		{1, 1, 1, "0.0001"},      // 0.01% 是最小可配的非零费率
		{0, 500, 1, "0"},
		{100, 0, 1, "0"},
		{-100, 500, 1, "0"}, // 负基数由冲正路径显式构造,正向计佣拒绝

		// 默认刻度(1 星屑 = 500000 额度 = $1)下的真实量级。这几行是本模块
		// 存在的理由:一次对话的佣金落在 1e-5 星屑,任何一步提前取整都归零。
		{500_000, 1000, 500_000, "0.1"}, // 下线花掉 1 星屑等值,按 10% 返 0.1
		{5_000_000, 1000, 500_000, "1"}, // 花掉 10 星屑等值,按 10% 恰好返 1
		{100, 500, 500_000, "0.00001"},  // 一次对话按 5%:0.00001 星屑,不能变成 0
		{1, 1, 500_000, "0.0000000002"}, // 最小可配费率 × 最小基数,仍然不是 0

		// 刻度读不到时一律 0:当 1 用意味着把额度数当星屑发,默认刻度下是
		// 50 万倍的超发,那比"这一笔不计佣"严重得多。
		{500_000, 1000, 0, "0"},
		{500_000, 1000, -1, "0"},
	}
	for _, tc := range cases {
		got := calcGross(tc.base, tc.units, tc.qpu)
		assert.Equal(t, tc.want, got.String(), "base=%d units=%d qpu=%d", tc.base, tc.units, tc.qpu)
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
		// gross_amount + capped_amount == base_quota × rate_bps / 10000 / quota_per_unit。
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
	assert.Equal(t, "consume:7:20260730:vip:500:h7:u3:q500000", consumeIdemKey(3, 7, "20260730", vip, 7, 500000))

	// 费率或分组一变就必须换一行:日聚合桶是"边增长边结算"的,把新费率
	// 算出的 gross 累加进一行标着旧费率的记录里,那一行从此
	// base × rate ≠ gross,永远对不平也没法向用户解释。
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, 7, 500000),
		consumeIdemKey(3, 7, "20260730", rateDecision{Units: 800, Group: "vip"}, 7, 500000))
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, 7, 500000),
		consumeIdemKey(3, 7, "20260730", rateDecision{Units: 500, Group: "default"}, 7, 500000))
	// Matched 只用于日志与管理端解释,不参与算钱,更不该影响幂等键。
	assert.Equal(t, consumeIdemKey(3, 7, "20260730", vip, 7, 500000),
		consumeIdemKey(3, 7, "20260730", rateDecision{Units: 500, Group: "vip", Matched: true}, 7, 500000))

	// 上线换了就必须换一行。ON CONFLICT 的 DoUpdates 只累加金额、不改
	// inviter_id,上线不在键里的话,换绑当天下线后续的消费会撞上旧上线那一行,
	// 钱被原子累加进去而 inviter_id 保持旧值 —— 结结实实发给了前一个上线,
	// 而三条恒等式全部成立,没有任何降级计数器会响。
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, 7, 500000),
		consumeIdemKey(4, 7, "20260730", vip, 7, 500000))

	// 刻度变了也必须换一行。它与费率一样被冻结进行(Accrual.QuotaPerUnit),
	// 而且事后真的决定这一行怎么被处置 —— 退款冲正按 origin.QuotaPerUnit 重算。
	// 不在键里的话,运营中午把 quota_per_unit 从 50 万调到 25 万,下午的增量会按
	// 新刻度算出 gross 累加进一行标着旧刻度的记录,I3 恒等式在那一行上不再成立,
	// 随后的退款按行上那个已经不对的刻度冲正 —— 调小时冲少了,调大时冲多了。
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, 7, 500000),
		consumeIdemKey(3, 7, "20260730", vip, 7, 250000))

	// 成熟期变了也必须换一行。日聚合桶的 ON CONFLICT 只累加金额、**不改
	// mature_at**:成熟期不在键里的话,运营中午把 holding_days 从 7 改成 0,
	// 当天已经建过桶的下线在那之后的消费会累加进一行标着旧成熟期的记录里,
	// 那部分钱按旧策略再压 7 天,而界面按新配置写着 T+1。
	assert.NotEqual(t, consumeIdemKey(3, 7, "20260730", vip, 7, 500000),
		consumeIdemKey(3, 7, "20260730", vip, 0, 500000))
	// 负的成熟期与 0 必须落同一个键:bucketMatureAt 把负数钳到 0,键里不钳的话
	// 同一个成熟时刻会分裂成两个桶。
	assert.Equal(t, consumeIdemKey(3, 7, "20260730", vip, 0, 500000),
		consumeIdemKey(3, 7, "20260730", vip, -1, 500000))

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
stardust:
  enabled: true
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
	// stardust 段是 D-16 之后 commission.enabled 的前置条件(佣金以星屑结算,
	// 星屑关着时算得出佣金却无处可发),所以上面那份 YAML 必须带它 —— 否则
	// config.Load 在校验期就失败,这条用例连 holding_days 都读不到。

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

// 支付合规门必须挡住**全部三条**推广获得线,而不只是充值那一条。
//
// 这条断言存在的理由是本仓真实出现过的形状:门只装在 accrueTopUp 上,
// 下线消费返(accrueConsume)与兑换码返(onRedeemSuccess)一路走到入账。
// 三条线全部经过 writeAccrual,所以闸门装在那个漏斗上,而这里钉住它 ——
// 少了这条断言,把闸门挪回某一个来源上不会有任何东西变红。
//
// 不需要数据库:闸门排在 db.Get() 之前,这正是它该在的位置。
func TestWriteAccrualHonorsPaymentComplianceGate(t *testing.T) {
	ps := operation_setting.GetPaymentSetting()
	origConfirmed, origVersion := ps.ComplianceConfirmed, ps.ComplianceTermsVersion
	t.Cleanup(func() {
		ps.ComplianceConfirmed, ps.ComplianceTermsVersion = origConfirmed, origVersion
	})

	in := accrualInput{
		SourceType: SourceConsume,
		IdemKey:    "compliance-gate-probe",
		InviterId:  1, InviteeId: 2,
		BaseQuota: 1_000_000,
		RateUnits: 500,
		Gross:     decimal.NewFromFloat(0.1),
		Status:    StatusAccrued,
	}

	// 合规未确认:不落账、不报错。返回 (false, nil) 与"幂等命中"同形,
	// 于是充值扫描的游标照常前进、热路径 worker 不把它当失败去重试。
	ps.ComplianceConfirmed = false
	before := complianceSkipped.Load()
	inserted, err := writeAccrual(context.Background(), in)
	require.NoError(t, err, "合规未确认不是错误,只是不发")
	assert.False(t, inserted, "合规未确认时一条计佣行都不该落库")
	assert.Equal(t, before+1, complianceSkipped.Load(),
		"静默跳过必须留下计数,否则「为什么一分佣金都没有」在管理端没有任何线索")

	// 合规已确认:闸门放行,后面因为测试环境没有扩展库而停在 ErrNotReady ——
	// 那正好证明它**越过了闸门**,而不是被闸门挡回来的。
	ps.ComplianceConfirmed = true
	ps.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	stillSkipped := complianceSkipped.Load()
	_, err = writeAccrual(context.Background(), in)
	require.ErrorIs(t, err, qydb.ErrNotReady,
		"合规已确认时必须走到取库句柄那一步")
	assert.Equal(t, stillSkipped, complianceSkipped.Load(), "放行的那次不该计入跳过")
}
