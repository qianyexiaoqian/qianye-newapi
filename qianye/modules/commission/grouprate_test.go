package commission

import (
	"context"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/groupname"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件守两件事:
//
//  1. 分组费率**真的接到了计佣路径上**,而不是只有一个算得对的纯函数。
//     本模块的历次缺陷全是这个形状 —— 纯函数对了、调度层没接。因此除了
//     resolveRate 之外,必须让 accrueConsume / accrueOneShot 真的写库,
//     再回读那一行确认冻结下来的费率与分组。
//  2. 换成百分比之后小额佣金依然零损耗(与 TestSmallConsumeCommissionIsNeverLost
//     同规格的数值走查)。

// commissionRateConfig 返回一份带全局默认费率的配置。
func commissionRateConfig(topupPercent, consumePercent string) *config.Config {
	c := commissionConfig(1)
	c.Commission.TopupRatePercent = topupPercent
	c.Commission.ConsumeRatePercent = consumePercent
	return c
}

// seedGroupRate 写一条分组费率规则(百分比入参,内部换算成整数)。
func seedGroupRate(t *testing.T, gdb *gorm.DB, group, topup, consume string, enabled bool) {
	t.Helper()
	topupUnits, err := config.RatePercentUnits(topup)
	require.NoError(t, err)
	consumeUnits, err := config.RatePercentUnits(consume)
	require.NoError(t, err)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&GroupRate{
		GroupName:        group,
		TopupRateUnits:   topupUnits,
		ConsumeRateUnits: consumeUnits,
		Enabled:          enabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}).Error)
	invalidateGroupRates()
}

// seedGroupRateUpdate 就地改一条已存在规则的费率(模拟运营在管理端调价)。
func seedGroupRateUpdate(t *testing.T, gdb *gorm.DB, group, topup, consume string) {
	t.Helper()
	topupUnits, err := config.RatePercentUnits(topup)
	require.NoError(t, err)
	consumeUnits, err := config.RatePercentUnits(consume)
	require.NoError(t, err)
	require.NoError(t, gdb.Model(&GroupRate{}).Where("group_name = ?", group).
		Updates(map[string]any{
			"topup_rate_units":   topupUnits,
			"consume_rate_units": consumeUnits,
			"updated_at":         common.GetTimestamp(),
		}).Error)
	invalidateGroupRates()
}

// accrualOfInvitee 回读某个下线名下**唯一**的一条计佣行。
// 断言"只有一条"是刻意的:多出来的行往往正是幂等键写错的信号。
func accrualOfInvitee(t *testing.T, gdb *gorm.DB, inviteeId int) Accrual {
	t.Helper()
	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", inviteeId).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 1, "下线 %d 应当恰好有一条计佣行", inviteeId)
	return rows[0]
}

// TestResolveRateByInviterGroup 锁定分组费率的解析口径。
//
// resolveRate 只认一个分组字符串,"这个字符串是谁的"由 resolveInviterPricing
// 决定(见 TestInviterGroupDecidesRate)。本表守的是另一半:给定分组,三档
// 各自取到哪个值、回落到哪里、以及冻结进流水的分组是不是判定用的那个值。
func TestResolveRateByInviterGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))

	seedGroupRate(t, gdb, "vip", "12.5", "8.25", true)
	seedGroupRate(t, gdb, "wholesale", "3", "1.5", true)
	// 被禁用的规则等价于"没配",不等于零费率 —— 想要零费率要显式填 0。
	seedGroupRate(t, gdb, "paused", "0.5", "0.5", false)

	s := effective()
	require.Equal(t, 1000, s.TopupRateUnits, "前提:全局默认费率已生效")
	require.Equal(t, 500, s.ConsumeRateUnits)

	ctx := context.Background()
	cases := []struct {
		name        string
		group       string
		source      string
		wantUnits   int
		wantMatched bool
	}{
		{"命中分组的消费费率", "vip", SourceConsume, 825, true},
		{"命中分组的充值费率", "vip", SourceTopup, 1250, true},
		{"另一个分组各走各的", "wholesale", SourceConsume, 150, true},
		{"未配置的分组回落全局默认", "default", SourceConsume, 500, false},
		{"未配置的分组回落全局默认(充值)", "default", SourceTopup, 1000, false},
		{"被禁用的规则等同未配置", "paused", SourceConsume, 500, false},
		// 空分组按 default 判定(见 billingGroup)。这里没有配 default 的规则,
		// 因此仍然回落全局默认 —— 但记录下来的分组是 default,不是空串。
		{"分组为空按 default 判定", "", SourceTopup, 1000, false},
		{"两侧空白不影响匹配", "  vip  ", SourceConsume, 825, true},
		{"兑换码走充值那一档", "vip", SourceRedemption, 1250, true},
		// 大小写折叠:存储层的排序规则大小写不敏感,库里根本不可能同时存在
		// VIP 与 vip 两行。不折叠得到的不是"两条独立规则",而是判定查不到、
		// 而管理端的保存却改掉了另一行的三方错位。
		{"大小写变体命中同一条规则", "VIP", SourceConsume, 825, true},
		{"大小写变体命中同一条规则(充值)", "Vip", SourceTopup, 1250, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveRate(ctx, tc.group, tc.source, s)
			assert.Equal(t, tc.wantUnits, got.Units)
			assert.Equal(t, tc.wantMatched, got.Matched)
			assert.Equal(t, billingGroup(tc.group), got.Group,
				"冻结进流水的分组必须是判定用的那个值 —— 否则拿着流水行复算会得出另一个费率")
		})
	}
}

// TestAccrueConsumeFreezesGroupRate 是"调度层真的接上了"的那一条。
//
// 只测 resolveRate 是不够的:把 hook.go 里的 rate.Units 改回
// s.ConsumeRateUnits,resolveRate 的测试照样全绿,而所有分组费率
// 一夜之间全部退回全局默认 —— 少发的钱没人会发现。
// 这里让 accrueConsume 真的落库,再回读那一行。
func TestAccrueConsumeFreezesGroupRate(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGroupRate(t, gdb, "vip", "12", "8", true)
	// wholesale 也配一档,而且与 vip 的数字完全不同 —— 下线被放进这一档,
	// 于是"取错了人的分组"会体现成一个具体的、错得很明显的金额(1.5% 而不是
	// 8%),而不是碰巧与回落值相等。
	seedGroupRate(t, gdb, "wholesale", "3", "1.5", true)

	// 上线 42 在 vip;上线 43 没有分组规则,必须回落全局默认。
	cacheUser(42, 0, "vip")
	cacheUser(43, 0, "default")
	// 两个下线**都**在 wholesale。费率按上线取,所以下线在哪一档完全不影响结果。
	cacheUser(900, 42, "wholesale")
	cacheUser(901, 43, "wholesale")

	at := common.GetTimestamp()
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: 900, Quota: 10000, At: at}))
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: 901, Quota: 10000, At: at}))

	vipRow := accrualOfInvitee(t, gdb, 900)
	assert.Equal(t, 800, vipRow.RateUnits, "上线 42 在 vip,必须按 vip 的 8% 返")
	assert.Equal(t, "vip", vipRow.RateGroup, "分组必须冻结进行,否则事后解释不了这一行")
	assert.Equal(t, "800", vipRow.GrossAmount.String(), "10000 × 8% = 800")

	defRow := accrualOfInvitee(t, gdb, 901)
	assert.Equal(t, 500, defRow.RateUnits, "上线 43 没配分组档,必须回落全局默认 5%")
	assert.Equal(t, "default", defRow.RateGroup)
	assert.Equal(t, "500", defRow.GrossAmount.String())
}

// TestAccrueConsumeSplitsRowWhenRateChanges 锁定"日聚合桶不混两套费率"。
//
// 桶会跨越一整天,期间下线可能换分组、运营可能调费率。若幂等键只按
// (下线, 日期) 聚合,后来的增量会按新费率算出 gross 累加进一行标着旧费率的
// 记录,那一行从此 base × rate ≠ gross,永远对不平。
func TestAccrueConsumeSplitsRowWhenRateChanges(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGroupRate(t, gdb, "vip", "12", "8", true)

	at := common.GetTimestamp()
	cacheUser(42, 0, "vip")
	cacheUser(900, 42, "default")
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	// 同一天里把 vip 的消费费率从 8% 调到 6%。
	seedGroupRateUpdate(t, gdb, "vip", "12", "6")
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", 900).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "费率变了必须落新的一行,不能并进旧桶")
	for _, r := range rows {
		want := calcGross(r.BaseQuota, r.RateUnits)
		assert.Equal(t, want.String(), r.GrossAmount.String(),
			"每一行都必须自洽:base × rate 必须等于 gross(accrual_no=%s)", r.AccrualNo)
	}
	assert.Equal(t, 800, rows[0].RateUnits)
	assert.Equal(t, 600, rows[1].RateUnits)
}

// TestAccrueOneShotFreezesGroupRate 覆盖充值/兑换码那一路。
//
// 充值的幂等键是订单号,不掺费率 —— 一笔充值无论如何只能返一次佣,
// 改费率不能让它变成两行。
func TestAccrueOneShotFreezesGroupRate(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGroupRate(t, gdb, "vip", "12.5", "8", true)

	cacheUser(42, 0, "vip")
	cacheUser(900, 42, "default")
	require.NoError(t, accrueOneShot(context.Background(), 900, 10000,
		decimal.Zero, SourceTopup, topupIdemKey("TX-1"), "TX-1"))

	row := accrualOfInvitee(t, gdb, 900)
	assert.Equal(t, 1250, row.RateUnits, "vip 的 12.5% 没有生效")
	assert.Equal(t, "vip", row.RateGroup)
	assert.Equal(t, "1250", row.GrossAmount.String(), "10000 × 12.5% = 1250")

	// 改了费率再重放同一个订单号,必须仍然只有一行、金额不变。
	seedGroupRateUpdate(t, gdb, "vip", "50", "8")
	require.NoError(t, accrueOneShot(context.Background(), 900, 10000,
		decimal.Zero, SourceTopup, topupIdemKey("TX-1"), "TX-1"))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", 900).Find(&rows).Error)
	require.Len(t, rows, 1, "同一笔充值被重扫时不能再返一次佣")
	assert.Equal(t, "1250", rows[0].GrossAmount.String())
}

// TestGroupRateCrudTakesEffectImmediately 锁定管理端增删改后缓存立即失效。
//
// 缓存 60 秒的话,运营改完费率会以为没生效然后再改一次 —— 这类"改完看不见"
// 正是本项目反复吃亏的形状。删除必须回落全局默认,而不是变成零费率。
func TestGroupRateCrudTakesEffectImmediately(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	ctx := context.Background()

	require.Empty(t, groupRates(ctx), "前提:一条规则都没有")
	assert.Equal(t, 500, resolveRate(ctx, "vip", SourceConsume, effective()).Units)

	row := GroupRate{GroupName: "vip", TopupRateUnits: 1250, ConsumeRateUnits: 825, Enabled: true}
	require.NoError(t, upsertGroupRate(ctx, &row))
	assert.Equal(t, 825, resolveRate(ctx, "vip", SourceConsume, effective()).Units,
		"新增规则后必须立刻生效")

	// 同名再写一次是覆盖而不是插一行,否则唯一索引之外还会多出一行影子规则。
	row2 := GroupRate{GroupName: "vip", TopupRateUnits: 300, ConsumeRateUnits: 200, Enabled: true}
	require.NoError(t, upsertGroupRate(ctx, &row2))
	all, err := listGroupRates(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, 200, resolveRate(ctx, "vip", SourceConsume, effective()).Units)

	removed, err := deleteGroupRate(ctx, "vip")
	require.NoError(t, err)
	assert.True(t, removed)
	assert.Equal(t, 500, resolveRate(ctx, "vip", SourceConsume, effective()).Units,
		"删除规则应回落全局默认,而不是变成零费率")

	removed, err = deleteGroupRate(ctx, "vip")
	require.NoError(t, err)
	assert.False(t, removed, "删不存在的规则要能被调用方识别为 404,而不是假装成功")

	_ = gdb
}

// TestGroupRateKeyIsCaseFolded 锁定分组费率表的大小写口径。
//
// 缺陷:group_name 是 varchar(64) 且没有指定排序规则,而扩展库固定是 MySQL ——
// AutoMigrate 建表时继承库默认排序规则(5.7 utf8mb4_general_ci、8.0
// utf8mb4_0900_ai_ci),两者都**大小写不敏感**;而 Go 侧的 map 查表是精确匹配。
// 于是给 VIP 配费率时 ON DUPLICATE KEY 会命中已有的 vip 那一行,而 group_name
// 不在 DoUpdates 里,那行仍叫 vip:管理端显示"VIP 已保存 50%",实际是 vip 的
// 用户按 50% 返佣,审计日志指向一个库里不存在的分组名。
//
// 修复走的是"代码跟上存储"(全链路折叠大小写)而不是 ALTER TABLE,所以这条
// 测试在 sqlite 上同样是真防线:sqlite 的 varchar 是二进制比较,缺了归一化
// 第二次 upsert 会插出**第二行**,下面的 require.Len(all, 1) 直接翻红。
// 两个数据库的失败表现不同,但归一化让两边都只可能有一行。
func TestGroupRateKeyIsCaseFolded(t *testing.T) {
	newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	ctx := context.Background()

	up := GroupRate{GroupName: " VIP ", TopupRateUnits: 1250, ConsumeRateUnits: 825, Enabled: true}
	require.NoError(t, upsertGroupRate(ctx, &up))
	assert.Equal(t, "vip", up.GroupName,
		"写库前必须归一:落进去一个没归一的键,ci 排序规则下会改掉别人那一行")

	low := GroupRate{GroupName: "vip", TopupRateUnits: 300, ConsumeRateUnits: 200, Enabled: true}
	require.NoError(t, upsertGroupRate(ctx, &low))

	all, err := listGroupRates(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1, "只差大小写的两次保存必须落在同一行,而不是两条影子规则")
	assert.Equal(t, "vip", all[0].GroupName)
	assert.Equal(t, 200, all[0].ConsumeRateUnits, "第二次保存必须是覆盖")

	// 判定侧:任何大小写写法都必须命中同一条规则、拿到同一个费率。
	s := effective()
	for _, g := range []string{"vip", "VIP", "Vip", "  vIp  "} {
		d := resolveRate(ctx, g, SourceConsume, s)
		assert.True(t, d.Matched, "%q 没有命中 vip 的费率规则", g)
		assert.Equal(t, 200, d.Units)
		assert.Equal(t, "vip", d.Group)
	}

	// 读与删同样按归一化后的键走。否则管理端用 VIP 删会得到 404,
	// 而那条规则还在按 vip 生效 —— 运营会以为已经删掉了。
	got, err := findGroupRate(ctx, "VIP")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "vip", got.GroupName)

	removed, err := deleteGroupRate(ctx, "  VIP ")
	require.NoError(t, err)
	assert.True(t, removed)
	assert.False(t, resolveRate(ctx, "vip", SourceConsume, effective()).Matched,
		"删掉之后必须回落全局默认")
}

// TestEmptyGroupBillsAtDefaultGroupRate 是审计 F7:出钱这一侧不能是全仓最宽松的口径。
//
// 主库 users.group 的列默认值就是 'default',但历史行与被直接改过库的账号可能
// 留着空串,它们在业务上就是默认分组的用户。旧口径下 resolveRate 对空分组直接
// 早退回落**全局默认费率**,于是给 default 配的低费率对这批账号完全不生效 ——
// 平台按更高的全局默认费率给他们的邀请人付钱,而流水行上什么都看不出来。
// transfer 的分组规则早就把空串折叠成 default 并写了理由,同仓两套口径。
func TestEmptyGroupBillsAtDefaultGroupRate(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGroupRate(t, gdb, "default", "1", "1", true)
	ctx := context.Background()
	s := effective()

	for _, g := range []string{"", "   ", "default", "DEFAULT"} {
		d := resolveRate(ctx, g, SourceConsume, s)
		assert.True(t, d.Matched, "%q 必须按 default 分组判定,而不是逃到全局默认费率", g)
		assert.Equal(t, 100, d.Units, "应当按 default 的 1% 返,而不是全局默认的 5%")
		assert.Equal(t, "default", d.Group)
	}
	// 充值那一档同样。
	assert.Equal(t, 100, resolveRate(ctx, "", SourceTopup, s).Units)
}

// TestGroupRateWriteRejectsEmptyGroup 空分组名不能落库。
//
// 它既不等于 default(判定侧会把空串折叠成 default,于是这一行永远查不到),
// 又占着 group_name 的唯一索引位,是一条只能靠人工删库收拾的僵尸规则。
// 校验放在数据层而不是只放在 HTTP 处理器里:新的调用点不该有机会绕过它。
func TestGroupRateWriteRejectsEmptyGroup(t *testing.T) {
	newTestDB(t)
	ctx := context.Background()

	row := GroupRate{GroupName: "   ", TopupRateUnits: 1000, ConsumeRateUnits: 500, Enabled: true}
	require.ErrorIs(t, upsertGroupRate(ctx, &row), errEmptyGroupName)

	_, err := deleteGroupRate(ctx, "")
	require.ErrorIs(t, err, errEmptyGroupName)

	all, err := listGroupRates(ctx)
	require.NoError(t, err)
	assert.Empty(t, all, "空分组名的规则不能落库")
}

// TestCommissionGroupKeyFollowsSharedContract 把本模块的分组名口径钉在共享实现上。
//
// 存在理由:本模块的费率表与 transfer 的规则表都以分组名为键,同仓一度有三份
// 各自演化的 normalizeGroup,而出钱的这一份恰好是最宽松的(只 TrimSpace、
// 不折叠大小写、空串逃逸)。两个模块现在都只转调 qianye/groupname,这条断言
// 保证的是"没有人把它悄悄换回一份私有实现"。
//
// 两个函数的分工也一并钉住:normalizeGroup 保留空串(api_admin 靠 `== ""`
// 拒绝没填分组名的请求),billingGroup 才折叠成 default。
func TestCommissionGroupKeyFollowsSharedContract(t *testing.T) {
	for _, in := range []string{"vip", "VIP", " Vip ", "", "   ", "DEFAULT", "内部测试组"} {
		assert.Equal(t, groupname.Normalize(in), normalizeGroup(in),
			"比较键口径必须与 qianye/groupname 一致(输入 %q)", in)
		assert.Equal(t, groupname.Effective(in), billingGroup(in),
			"计费口径必须与 qianye/groupname 一致(输入 %q)", in)
	}
	assert.Equal(t, "", normalizeGroup("  "),
		"normalizeGroup 不能折叠空串,否则 api_admin 的空值校验会静默失效")
	assert.Equal(t, "default", billingGroup("  "))
}

// TestSettingsPercentOverride 锁定运营覆盖的费率读写口径。
func TestSettingsPercentOverride(t *testing.T) {
	t.Run("百分比键生效", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))

		setSettingOverride(t, gdb, keyConsumeRatePercent, "8.25")
		assert.Equal(t, 825, effective().ConsumeRateUnits)
		assert.Equal(t, "8.25", effective().ConsumeRatePercent())
	})

	// 升级之后运营还没重新保存过配置时,库里只有 1.x 的万分比键。
	// 不读它就等于"升级即掉费率",而且是静默的。
	t.Run("回落读取 1.x 的万分比键", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))

		setSettingOverride(t, gdb, legacyKeyConsumeRateBps, "825")
		assert.Equal(t, 825, effective().ConsumeRateUnits)
	})

	t.Run("百分比键优先于旧键", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))

		setSettingOverride(t, gdb, legacyKeyConsumeRateBps, "100")
		setSettingOverride(t, gdb, keyConsumeRatePercent, "8")
		assert.Equal(t, 800, effective().ConsumeRateUnits)
	})

	// qy_settings 是可以被人手工 UPDATE 的。被写坏的值必须整条丢弃回落
	// YAML 默认,而不是钳到 100% —— 后者会静默地按全额返佣。
	t.Run("越界与非法值一律丢弃", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))

		setSettingOverride(t, gdb, keyConsumeRatePercent, "999")
		assert.Equal(t, 500, effective().ConsumeRateUnits)

		setSettingOverride(t, gdb, keyConsumeRatePercent, "abc")
		assert.Equal(t, 500, effective().ConsumeRateUnits)

		setSettingOverride(t, gdb, keyConsumeRatePercent, "-1")
		assert.Equal(t, 500, effective().ConsumeRateUnits)
	})

	t.Run("旧键越界同样丢弃", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))

		setSettingOverride(t, gdb, legacyKeyConsumeRateBps, "99999")
		assert.Equal(t, 500, effective().ConsumeRateUnits)
	})
}

// TestPercentRatesKeepSmallCommissionIntact 是改成百分比之后的数值走查,
// 与 TestSmallConsumeCommissionIsNeverLost 同规格。
//
// 场景:每次消费 10 额度,一天 2000 次。裸 int 转换会把每次的佣金截断成 0,
// 用户忙活一天佣金是 0 而钱被平台吞了。换成百分比配置之后,这条性质
// 必须一字不变地成立 —— 包括两位小数的费率。
func TestPercentRatesKeepSmallCommissionIntact(t *testing.T) {
	const (
		perCall = int64(10)
		calls   = 2000
	)
	cases := []struct {
		percent string
		// wantTotal 是 2000 次 × 10 额度 × 费率的精确总额。
		wantTotal string
	}{
		{"5", "1000"},     // 20000 × 5%
		{"10.25", "2050"}, // 两位小数不该带来任何损耗
		{"0.01", "2"},     // 最小可表达的非零费率
		{"33.33", "6666"}, // 除不尽的比例
		{"100", "20000"},  // 上边界
		{"0.05", "10"},    // 每次佣金 0.005,单看一次连 0.01 都不到
	}
	for _, tc := range cases {
		t.Run(tc.percent+"%", func(t *testing.T) {
			units, err := config.RatePercentUnits(tc.percent)
			require.NoError(t, err)

			// 第一路:日聚合成一行,末尾一次结算。
			bucket := decimal.Zero
			for i := 0; i < calls; i++ {
				bucket = bucket.Add(calcGross(perCall, units))
			}
			require.Equal(t, tc.wantTotal, bucket.String(),
				"全精度累计的总额不对,说明费率换算或计佣算术有偏差")

			out := computeSettlement(decimal.Zero, bucket, 0, 1, -1)
			assert.Equal(t, tc.wantTotal, strconv.FormatInt(out.NetQuota, 10),
				"日聚合后一次结算必须一分不差")
			assert.True(t, out.CarryAfter.IsZero())

			// 第二路(最坏情况):每一次消费都立刻单独结算一轮。
			// 余数机制必须让 2000 轮的发放总额仍然精确等于同一个数。
			carry := decimal.Zero
			var granted int64
			for i := 0; i < calls; i++ {
				r := computeSettlement(carry, calcGross(perCall, units), granted, 1, -1)
				granted += r.NetQuota
				carry = r.CarryAfter
				require.False(t, carry.IsNegative(), "第 %d 轮余数不应为负", i)
			}
			assert.Equal(t, tc.wantTotal, strconv.FormatInt(granted, 10),
				"逐笔结算 2000 轮之后总额漂了 —— 零头被吃掉了")
			assert.True(t, carry.IsZero())
		})
	}
}
