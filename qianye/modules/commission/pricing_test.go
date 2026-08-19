package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// pricing_test.go —— 「费率与法币折算比例都按上线分组、且取自同一次解析」
// 这条口径的守卫。
//
// 本文件里的每一条都必须能杀死"改回按下线分组"这个变异。所以所有场景里
// **上线与下线一律在不同的分组**,而且两个分组各自配了不同的费率 ——
// 取错人不会回落到某个碰巧相等的默认值,会直接体现成一个错的金额。

// seedTwoTierGroups 写两档互不相同的分组费率,供本文件复用。
//
//	vip        充值 12%  消费 8%
//	wholesale  充值 3%   消费 1.5%
//
// 全局默认(由 commissionRateConfig 给)是充值 10% / 消费 5%,与两者都不同,
// 于是"命中 vip""命中 wholesale""回落全局"三种结局在断言上互不重叠。
func seedTwoTierGroups(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	seedGroupRate(t, gdb, "vip", "12", "8", true)
	seedGroupRate(t, gdb, "wholesale", "3", "1.5", true)
}

// TestInviterGroupDecidesRate 是本轮口径变更的正面断言,DB 级。
//
// 只测 resolveRate 是不够的:它只认一个字符串,把 resolveInviterPricing 里的
// e.Group 换成下线的分组,resolveRate 的表驱动照样全绿,而全站的返佣比例
// 一夜之间改由被推广的人决定 —— 推广人看不见,账本每一行仍然自洽。
// 因此这里让 accrueConsume / accrueOneShot 真的落库,再回读冻结下来的那一行。
func TestInviterGroupDecidesRate(t *testing.T) {
	cases := []struct {
		name         string
		inviterGroup string
		inviteeGroup string
		source       string
		wantUnits    int
		wantGroup    string
		wantGross    string
	}{
		{
			name: "上线 vip 下线 wholesale:按上线的 8% 返",
			// 取错人的话是 1.5% ⇒ gross 150,与 800 差得一眼看得出来。
			inviterGroup: "vip", inviteeGroup: "wholesale",
			source: SourceConsume, wantUnits: 800, wantGroup: "vip", wantGross: "800",
		},
		{
			name:         "上线 wholesale 下线 vip:按上线的 1.5% 返",
			inviterGroup: "wholesale", inviteeGroup: "vip",
			source: SourceConsume, wantUnits: 150, wantGroup: "wholesale", wantGross: "150",
		},
		{
			name:         "上线没配分组档:回落全局默认 5%,下线是 vip 也没用",
			inviterGroup: "default", inviteeGroup: "vip",
			source: SourceConsume, wantUnits: 500, wantGroup: "default", wantGross: "500",
		},
		{
			name:         "充值档同口径:上线 vip 的 12%",
			inviterGroup: "vip", inviteeGroup: "wholesale",
			source: SourceTopup, wantUnits: 1200, wantGroup: "vip", wantGross: "1200",
		},
		{
			name:         "兑换码没单独配时跟随上线的充值档",
			inviterGroup: "wholesale", inviteeGroup: "vip",
			source: SourceRedemption, wantUnits: 300, wantGroup: "wholesale", wantGross: "300",
		},
		{
			// 上线分组为空(历史行/被直接改过库)按 default 判定,与 billingGroup
			// 在费率那一侧的口径一致 —— 出钱这一侧不能是全仓最宽松的口径。
			name:         "上线分组为空按 default 判定",
			inviterGroup: "", inviteeGroup: "vip",
			source: SourceConsume, wantUnits: 500, wantGroup: "default", wantGross: "500",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			useConfig(t, commissionRateConfig("10", "5"))
			seedTwoTierGroups(t, gdb)

			cacheUser(42, 0, tc.inviterGroup)
			cacheUser(900, 42, tc.inviteeGroup)

			ctx := context.Background()
			if tc.source == SourceConsume {
				require.NoError(t, accrueConsume(ctx,
					consumeEvent{InviteeId: 900, Quota: 10000, At: common.GetTimestamp()}))
			} else {
				require.NoError(t, accrueOneShot(ctx, 900, 10000, decimal.Zero,
					tc.source, tc.source+":idem-1", "REF-1"))
			}

			row := accrualOfInvitee(t, gdb, 900)
			assert.Equal(t, tc.wantUnits, row.RateUnits)
			assert.Equal(t, tc.wantGroup, row.RateGroup,
				"冻结进账本的必须是**上线**当时的分组")
			assert.Equal(t, tc.wantGross, row.GrossAmount.String())
		})
	}
}

// TestInviteeGroupChangeNeitherSplitsRowNorChangesRate 是本轮修掉的那个缺陷
// 的直接回归。
//
// 旧口径下,下线自己买个套餐换了分组就会**静默改掉上线的返佣比例**:当天的
// 日聚合桶裂成两行、后一段按另一个费率发钱,而推广人完全不知道自己的费率
// 被别人改了 —— 账本每一行仍然自洽(base × rate = gross),没有任何降级
// 计数器会响,这正是它能活到今天的原因。
//
// 新口径下这件事必须**什么都不发生**:同一行继续累加,费率一个字不变。
func TestInviteeGroupChangeNeitherSplitsRowNorChangesRate(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedTwoTierGroups(t, gdb)

	at := common.GetTimestamp()
	cacheUser(42, 0, "vip")
	cacheUser(900, 42, "wholesale")

	ctx := context.Background()
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	// 下线在同一天里从 wholesale 换到 default,再换到 vip。
	cacheUser(900, 42, "default")
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 900, Quota: 10000, At: at}))
	cacheUser(900, 42, "vip")
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	row := accrualOfInvitee(t, gdb, 900) // 它自己 require 恰好一行
	assert.Equal(t, int64(30000), row.BaseQuota, "三笔消费必须并进同一行")
	assert.Equal(t, 800, row.RateUnits, "下线换组不得改动上线的费率")
	assert.Equal(t, "vip", row.RateGroup)
	assert.Equal(t, "2400", row.GrossAmount.String(), "30000 × 8% = 2400")
}

// TestInviterGroupChangeSplitsRowSameDay 是同一天里**上线**换分组的处理方式:
// 落两行,换组之前那段归旧档、之后那段归新档。
//
// 不裂行的后果是那一行从此 base × rate ≠ gross —— 永远对不平,也没法向
// 推广人解释"我今天到底按几个点返的"。裂行是账面上应该看得见的事实。
func TestInviterGroupChangeSplitsRowSameDay(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedTwoTierGroups(t, gdb)

	at := common.GetTimestamp()
	cacheUser(42, 0, "wholesale")
	cacheUser(900, 42, "default")

	ctx := context.Background()
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	// 上线升级到 vip(套餐购买 → model.QyOnUserGroupChanged → invalidateInviter,
	// 这里直接改缓存值等价于失效后回源读到了新分组)。
	cacheUser(42, 0, "vip")
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", 900).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "上线换组必须落新的一行,不能并进旧桶")

	assert.Equal(t, "wholesale", rows[0].RateGroup)
	assert.Equal(t, 150, rows[0].RateUnits)
	assert.Equal(t, int64(10000), rows[0].BaseQuota, "换组之前那一行不得被后来的消费改动")
	assert.Equal(t, "150", rows[0].GrossAmount.String())

	assert.Equal(t, "vip", rows[1].RateGroup)
	assert.Equal(t, 800, rows[1].RateUnits)
	assert.Equal(t, int64(10000), rows[1].BaseQuota)
	assert.Equal(t, "800", rows[1].GrossAmount.String())

	for _, r := range rows {
		assert.Equal(t, calcGross(r.BaseQuota, r.RateUnits).String(), r.GrossAmount.String(),
			"每一行都必须自洽:base × rate = gross(accrual_no=%s)", r.AccrualNo)
	}
	// 两行加起来正好是喂进去的两笔消费,一分不多一分不少。
	assert.Equal(t, int64(20000), rows[0].BaseQuota+rows[1].BaseQuota)
}

// TestUpgradeDayDoesNotDoublePay 是**口径切换那一天会不会重复发钱**的实证。
//
// 切换前落下的行,幂等键里的分组段是**下线**的组;切换后新事件算出的键,
// 分组段是**上线**的组。两个键不同 ⇒ 同一个 (下线, 自然日) 桶里会同时存在
// 两行。问题是:这算不算把同一批消费返了两次佣?
//
// 不算,而且这一条把理由变成可执行的断言:base_quota 与 gross_amount 是
// **按事件增量累加**上去的(writeAccrual 的 Accumulate 分支),不是从主库
// 日志重算的。每一个消费事件只被投递一次、只落进它当时那把键对应的那一行。
// 切换只是把**后续**事件引到了另一行上,存量行一个字节都不会被碰。
//
// 这里先按旧口径的键手写一行(模拟升级前已经落下的日聚合桶),再让新代码
// 跑一笔真实事件,然后核对:总额 == 喂进去的事件总额,旧行原地不动。
func TestUpgradeDayDoesNotDoublePay(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedTwoTierGroups(t, gdb)

	at := common.GetTimestamp()
	day := bucketDate(at)
	s := effective()

	cacheUser(42, 0, "vip")
	cacheUser(900, 42, "wholesale")

	ctx := context.Background()

	// ── 升级前:按**下线**分组(wholesale, 1.5%)落下的那一行 ──
	//
	// 用 writeAccrual 而不是直接 Create,是为了让这一行与真实的存量行逐字段
	// 同构(accrual_no、idem_scope、成熟期都由同一段代码生成)。
	legacyRate := rateDecision{Units: 150, Group: "wholesale", Matched: true}
	legacyFiat := resolveFiatRate(ctx, "vip", s)
	_, err := writeAccrual(ctx, accrualInput{
		SourceType: SourceConsume,
		IdemKey:    consumeIdemKey(42, 900, day, legacyRate, legacyFiat, s.HoldingDays),
		InviterId:  42,
		InviteeId:  900,
		BaseQuota:  10000,
		RateUnits:  legacyRate.Units,
		RateGroup:  legacyRate.Group,
		Gross:      calcGross(10000, legacyRate.Units),
		UsdRate:    legacyFiat.Rate,
		MatureAt:   bucketMatureAt(day, s.HoldingDays),
		BucketDate: day,
		Status:     StatusAccrued,
		Accumulate: true,
	})
	require.NoError(t, err)

	before := accrualOfInvitee(t, gdb, 900)
	require.Equal(t, "wholesale", before.RateGroup)
	require.Equal(t, "150", before.GrossAmount.String())

	// ── 升级后:同一天、同一个下线,再来一笔真实消费 ──
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", 900).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "口径变了 ⇒ 落新的一行,与改费率当天完全同一条处理方式")

	// 存量行原地不动:base 与 gross 都是升级前那个数。
	assert.Equal(t, before.Id, rows[0].Id)
	assert.Equal(t, int64(10000), rows[0].BaseQuota, "存量行的基数被改动了 —— 那才是重复计算")
	assert.Equal(t, "150", rows[0].GrossAmount.String(), "存量行的金额被重算了")
	assert.Equal(t, "wholesale", rows[0].RateGroup, "存量行的冻结分组是事实,不迁移不改写")

	// 新行只承载升级之后那一笔。
	assert.Equal(t, int64(10000), rows[1].BaseQuota)
	assert.Equal(t, "vip", rows[1].RateGroup)
	assert.Equal(t, "800", rows[1].GrossAmount.String())

	// ── 结论性断言:总基数 == 喂进去的事件总额 ──
	//
	// 两笔各 10000 的消费 ⇒ 20000。如果升级把旧那一批消费按新口径又算了一遍,
	// 这里会是 30000。
	var totalBase int64
	var totalGross decimal.Decimal
	for _, r := range rows {
		totalBase += r.BaseQuota
		totalGross = totalGross.Add(r.GrossAmount)
	}
	assert.Equal(t, int64(20000), totalBase, "同一批消费被计了两次基数")
	assert.Equal(t, "950", totalGross.String(), "150(旧口径那笔)+ 800(新口径那笔)")
}

// TestRateAndFiatComeFromTheSameGroup 钉死本轮的核心不变量:
// 两档比例取的是**同一个人在同一时刻**的分组。
//
// 这正是本轮之前那个不一致的根因 —— 费率读下线、法币读上线,而一条 accrual
// 行的三条恒等式(base × rate = gross、gross = settled + outstanding、
// usd_rate 加权平均)在这种情况下**全部成立**,没有任何守卫会响。
func TestRateAndFiatComeFromTheSameGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)
	seedTwoTierGroups(t, gdb)
	// 两个分组的法币比例也各不相同,而且都与全站充值汇率(6)不同 ——
	// 取错人会体现成一个具体的错比例,不会碰巧落到同一个数字上。
	seedFiatRate(t, gdb, "vip", "8.5", true)
	seedFiatRate(t, gdb, "wholesale", "4.25", true)

	ctx := context.Background()
	s := effective()
	for _, tc := range []struct {
		inviterGroup string
		inviteeGroup string
		wantRate     int
		wantFiat     string
	}{
		{"vip", "wholesale", 800, "8.5"},
		{"wholesale", "vip", 150, "4.25"},
		{"default", "vip", 500, "6"}, // 没配分组档 ⇒ 费率回落全局、比例回落全站汇率
		{"", "vip", 500, "6"},
	} {
		cacheUser(42, 0, tc.inviterGroup)
		cacheUser(900, 42, tc.inviteeGroup)

		p := resolveInviterPricing(ctx, 42, SourceConsume, s)
		assert.True(t, p.sameGroup(),
			"费率取 %q、法币取 %q —— 两档必须取自同一个人同一时刻的分组",
			p.Rate.Group, p.Fiat.Group)
		assert.Equal(t, billingGroup(tc.inviterGroup), p.Rate.Group)
		assert.Equal(t, tc.wantRate, p.Rate.Units)
		assert.Equal(t, tc.wantFiat, p.Fiat.Rate.String())
	}
}

// TestAccrualFreezesBothTiersFromTheInviterGroup 把上一条延伸到落库那一侧:
// 同一行 accrual 上的 rate_group 与 usd_rate 必须出自同一个分组。
//
// 分开测是有必要的 —— resolveInviterPricing 返回得再对,hook.go 里只要有一处
// 把 p.Fiat 换回一次独立解析,这条不变量就没了,而上一条纯函数测试全绿。
func TestAccrualFreezesBothTiersFromTheInviterGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)
	seedTwoTierGroups(t, gdb)
	seedFiatRate(t, gdb, "vip", "8.5", true)
	seedFiatRate(t, gdb, "wholesale", "4.25", true)

	cacheUser(42, 0, "vip")
	cacheUser(900, 42, "wholesale")

	ctx := context.Background()
	require.NoError(t, accrueConsume(ctx,
		consumeEvent{InviteeId: 900, Quota: 10000, At: common.GetTimestamp()}))
	require.NoError(t, accrueOneShot(ctx, 900, 10000, decimal.Zero,
		SourceRedemption, redemptionIdemKey(77), "RD77"))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", 900).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2)
	for _, r := range rows {
		assert.Equal(t, "vip", r.RateGroup, "费率分组必须是上线的 vip(来源 %s)", r.SourceType)
		assert.Equal(t, "8.5", r.UsdRate.String(),
			"法币比例必须来自同一个 vip 分组档,不是下线的 wholesale(4.25)也不是全站汇率(6)"+
				"(来源 %s)", r.SourceType)
	}
}

// TestUnresolvableInviterSkipsGroupLayerForBothTiers 是主库读不到上线时的降级。
//
// 两条性质缺一不可:
//
//  1. **仍然计佣**。guard.HotAsync 没有重试,返回错误等于这笔佣金永远丢了。
//  2. **两档一起跳过分组层**,而不是按 default 分组判定。后者会让一次主库
//     抖动被冻结成"这批人在默认分组"这个事实 —— 而运营恰好给 default 配了
//     一档费率时,那一档会被当成真相写进账本。
//
// 冻结下来的 rate_group 是**空串**,与 "default" 分得开:前者是"没解析出来",
// 后者是"确实在默认分组"。
func TestUnresolvableInviterSkipsGroupLayerForBothTiers(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)
	// default 也配一档,而且与全局默认不同:降级若按 default 判定,
	// 会拿到 1% 与 9.9 —— 与期望的 5% 与 6 完全分得开。
	seedGroupRate(t, gdb, "default", "1", "1", true)
	seedFiatRate(t, gdb, "default", "9.9", true)

	// 下线在缓存里,上线不在;主库句柄为 nil ⇒ resolveInviter 必然报错。
	cacheUser(900, 42, "vip")
	getInviterCache().Delete(42)
	require.Nil(t, model.DB, "本条依赖主库不可用来制造上线解析失败")

	before := inviterGroupDegrade.stats()["count"].(int64)
	ctx := context.Background()
	require.NoError(t, accrueConsume(ctx,
		consumeEvent{InviteeId: 900, Quota: 10000, At: common.GetTimestamp()}),
		"主库读不到上线不得让这笔佣金丢掉 —— HotAsync 不重试")

	row := accrualOfInvitee(t, gdb, 900)
	assert.Equal(t, 500, row.RateUnits, "必须回落全局默认 5%,而不是 default 分组档的 1%")
	assert.Equal(t, "", row.RateGroup,
		`降级行的分组必须是空串:它与 "default" 是两件事,账本上必须分得开`)
	assert.Equal(t, "6", row.UsdRate.String(), "法币比例同样跳过分组层,回落全站充值汇率")
	assert.Greater(t, inviterGroupDegrade.stats()["count"].(int64), before,
		"降级必须计数 —— 它是这条路上唯一的痕迹,而空串在库里安静得很")
}

// TestExplicitZeroGroupRateIsNotUnset 守分组费率这一档的零值陷阱。
//
// 显式 0% 与"没配"必须分得开:前者是运营的决定(这一档人不返佣),
// 后者回落全局默认。分不开的表现是把 0% 悄悄发成 5%,或者把没配的分组
// 静默停发 —— 两种都是钱错。
func TestExplicitZeroGroupRateIsNotUnset(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGroupRate(t, gdb, "zerotier", "0", "0", true)

	ctx := context.Background()
	s := effective()

	zero := resolveRate(ctx, "zerotier", SourceConsume, s)
	assert.True(t, zero.Matched, "显式 0% 必须算命中,不是没配")
	assert.Equal(t, 0, zero.Units)

	unset := resolveRate(ctx, "nosuchtier", SourceConsume, s)
	assert.False(t, unset.Matched)
	assert.Equal(t, 500, unset.Units, "没配的分组回落全局默认,不是 0%")

	// 落库那一侧:0% 的上线一行都不该有(gross 为零直接早退),
	// 没配的上线正常拿 5%。两者在库里的差别是"零行"与"一行",最难被含糊过去。
	cacheUser(42, 0, "zerotier")
	cacheUser(900, 42, "vip")
	cacheUser(43, 0, "nosuchtier")
	cacheUser(901, 43, "vip")

	at := common.GetTimestamp()
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 900, Quota: 10000, At: at}))
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 901, Quota: 10000, At: at}))

	var zeroRows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", 900).Find(&zeroRows).Error)
	assert.Empty(t, zeroRows, "0% 分组的上线不该产生任何计佣行")

	paid := accrualOfInvitee(t, gdb, 901)
	assert.Equal(t, 500, paid.RateUnits)
	assert.Equal(t, "nosuchtier", paid.RateGroup)
}

// TestClawbackWithoutOriginUsesInviterGroup 覆盖冲正的兜底路径。
//
// 一条原单都取不到时,冲正按**当刻的完整口径**算:费率与法币比例都走
// resolveInviterPricing。分开取会让配了分组档的上线在这条路上按另一套口径
// 冲正,而他的原单是按分组档发出去的 —— 差额永久留在 available_fiat 上。
func TestClawbackWithoutOriginUsesInviterGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)
	seedTwoTierGroups(t, gdb)
	seedFiatRate(t, gdb, "vip", "8.5", true)

	cacheUser(42, 0, "vip")
	cacheUser(900, 42, "wholesale")

	ctx := context.Background()
	// 先用充值路径给这一对挣出一点净佣金:冲正上限是"这个下线给这个上线
	// 一共挣过多少",净额为零时 clawback 什么都不做。
	require.NoError(t, accrueOneShot(ctx, 900, 100000, decimal.Zero,
		SourceTopup, topupIdemKey("TX-CB"), "TX-CB"))

	// 消费那一档一行都没有 ⇒ consumeOriginForRefund 取不到原单,走兜底。
	require.NoError(t, clawback(ctx, 900, 10000, SourceClawback+":t:1", "task-1", "task refund"))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ? AND source_type = ?", 900, SourceClawback).
		Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, 800, rows[0].RateUnits, "兜底冲正按上线 vip 的消费档 8%")
	assert.Equal(t, "vip", rows[0].RateGroup)
	assert.Equal(t, "8.5", rows[0].UsdRate.String(),
		"法币比例必须来自同一个 vip 分组档,不是全站充值汇率 6")
	assert.Equal(t, "-800", rows[0].GrossAmount.String())
}

// TestUserGroupChangeHookInvalidatesInviterCache 钉住"换上线分组立即生效"
// 那条链路的**扩展侧**一端。
//
// 主库那一侧(套餐升降级、管理端改用户、分组迁移三个出口都调了
// model.QyOnUserGroupChanged)由 pricing_single_resolver_guard_test.go 的
// AST 断言守住;这里守的是注入进去的那个实现真的会清掉缓存 ——
// 两端都到位,换组才是立刻生效而不是最多晚 InviterCacheSecs。
func TestUserGroupChangeHookInvalidatesInviterCache(t *testing.T) {
	installHooks()
	require.NotNil(t, model.QyOnUserGroupChanged)

	cacheUser(42, 0, "wholesale")
	_, ok := peekInviter(42)
	require.True(t, ok, "前提:缓存里有这一条")

	model.QyOnUserGroupChanged(42)
	_, ok = peekInviter(42)
	assert.False(t, ok, "分组变了必须清掉这一条,否则新费率最多晚 InviterCacheSecs 才生效")

	// 批量迁移传 0 ⇒ 整表清空。
	cacheUser(42, 0, "vip")
	cacheUser(43, 0, "vip")
	model.QyOnUserGroupChanged(0)
	_, ok = peekInviter(42)
	assert.False(t, ok)
	_, ok = peekInviter(43)
	assert.False(t, ok, "批量改写传 0 必须整表清空,而不是只清 user 0")
}
