package commission

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fiatrate_test.go —— 按分组的法币折算比例。
//
// 守四件事,每一件都对应一种"纯函数对了但钱算错了"的具体形状:
//
//  1. **层级**:分组档 → 兜底档 → 全站充值汇率,而且"没配"与"显式 0"分得开。
//  2. **口径**:比例取【上线】的分组,费率取【下线】的分组。这两个答案会算出
//     不同的钱,所以必须有一条测试在两侧分组各配一个**不同**的比例,
//     把选择钉死 —— 只用同一个分组测的话,把口径改成下线照样全绿。
//  3. **接上了**:accrueConsume / accrueOneShot 真的写库,回读 usd_rate。
//  4. **不追溯**:改比例不重算任何一分已经入账的 available_fiat。

// seedFiatRate 写一条分组法币比例。
func seedFiatRate(t *testing.T, gdb *gorm.DB, group, rate string, enabled bool) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&FiatRate{
		GroupName: group,
		Rate:      decimal.RequireFromString(rate),
		Enabled:   enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}).Error)
	invalidateFiatRates()
}

// seedFiatDefault 把兜底档写进 qy_settings(与管理端 PUT /config 落的是同一行)。
func seedFiatDefault(t *testing.T, gdb *gorm.DB, raw string) {
	t.Helper()
	require.NoError(t, gdb.Create(&qymodel.Setting{
		Scope: settingScope, K: keyFiatRateDefault, V: raw,
		UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()
}

// cacheUser 把一个账号的分组塞进邀请关系缓存。
//
// inviterId 为 0 表示"这个人自己没有上线",上线正是这种样子 ——
// resolveInviterPricing 读的是同一份缓存里那条记录的 Group(= 这个账号的分组),
// 费率与法币折算比例两档都从它取值。
func cacheUser(userId, inviterId int, group string) {
	getInviterCache().Set(userId, inviterEntry{
		InviterId:      inviterId,
		InviteeName:    "u" + itoa(userId),
		InviteeCreated: common.GetTimestamp() - 30*86400,
		Group:          group,
	})
}

func ptrRate(raw string) *decimal.Decimal {
	d := decimal.RequireFromString(raw)
	return &d
}

// TestParseFiatRate 钉住写入侧的取值区间,尤其是 0 的语义。
//
// 0 在这一档既不是"免费"也不是"没配":它会让 applyFiat 一分法币都不加而
// 额度照加,available_fiat 与 available_quota 从此永久漂移。想停掉某个分组的
// 佣金,既有的杠杆是把返佣费率设成 0%(两侧都停在 0,账是平的)。
func TestParseFiatRate(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // 空表示应当报错
	}{
		{"正常比例", "7.3", "7.3"},
		{"前后空白被吃掉", "  7.3  ", "7.3"},
		{"尾随零被规范化,避免审计出现假差异", "7.30", "7.3"},
		{"整数", "7", "7"},
		{"八位小数是上限内的合法精度", "0.00000001", "0.00000001"},
		{"上界本身合法", "1000000", "1000000"},
		{"空串不是「没配」而是错误:兜底档不可清空", "", ""},
		{"纯空白同上", "   ", ""},
		{"零是非法值,不是「免费」", "0", ""},
		{"零的其它写法一样拒", "0.000", ""},
		{"负数", "-1", ""},
		{"九位小数超出存储精度,拒绝而不是四舍五入", "0.000000001", ""},
		{"超出上界", "1000000.01", ""},
		{"非数字", "abc", ""},
		{"百分号是运营把它当百分比填了", "7.3%", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFiatRate(tc.raw)
			if tc.want == "" {
				require.Error(t, err, "这个输入必须被拒")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())
		})
	}
}

// TestFiatRateLayerFallback 是层级判定的表驱动。
//
// 全站充值汇率固定成 6,与两个上层的取值都不同 —— 三层各自生效时算出来的
// 数字必须互不相同,否则"回落到哪一层"这件事在断言上根本看不出来。
func TestFiatRateLayerFallback(t *testing.T) {
	useMoneyGlobals(t, 6, 500000)

	enabledRule := &FiatRate{GroupName: "vip", Rate: decimal.RequireFromString("8.5"), Enabled: true}
	disabledRule := &FiatRate{GroupName: "vip", Rate: decimal.RequireFromString("8.5"), Enabled: false}
	// 库里可以被人手工 UPDATE 出这两种值,写入侧的 400 挡不住它们。
	zeroRule := &FiatRate{GroupName: "vip", Rate: decimal.Zero, Enabled: true}
	hugeRule := &FiatRate{GroupName: "vip", Rate: decimal.RequireFromString("2000000"), Enabled: true}

	cases := []struct {
		name      string
		rule      *FiatRate
		def       *decimal.Decimal
		wantRate  string
		wantLayer string
		degraded  bool
	}{
		{"分组档命中,兜底档与全站汇率都不插手", enabledRule, ptrRate("7.3"), "8.5", fiatLayerGroup, false},
		{"分组档没配,回落兜底档", nil, ptrRate("7.3"), "7.3", fiatLayerDefault, false},
		{"分组档与兜底档都没配,回落全站充值汇率", nil, nil, "6", fiatLayerGlobal, false},
		{"分组档被禁用等于没配,不等于比例 0", disabledRule, ptrRate("7.3"), "7.3", fiatLayerDefault, false},
		{"分组档没配但有兜底档时,全站汇率不参与", nil, ptrRate("7.3"), "7.3", fiatLayerDefault, false},
		{"库里被手工改成 0 的分组档:回落并留痕,绝不按 0 折算", zeroRule, ptrRate("7.3"), "7.3", fiatLayerDefault, true},
		{"库里被手工改超上界的分组档同上", hugeRule, ptrRate("7.3"), "7.3", fiatLayerDefault, true},
		{"兜底档也被改坏时继续回落全站汇率", nil, ptrRate("0"), "6", fiatLayerGlobal, true},
		{"分组档坏 + 没兜底档 → 全站汇率", zeroRule, nil, "6", fiatLayerGlobal, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := fiatRateFor(tc.rule, opSettings{FiatRateDefault: tc.def})
			assert.Equal(t, tc.wantRate, d.Rate.String())
			assert.Equal(t, tc.wantLayer, d.Layer)
			assert.Equal(t, tc.degraded, d.Degraded != "",
				"降级与否决定运营事后知不知道「那段时间的佣金要复核」")
		})
	}
}

// TestFiatRateNoUsableLayer 覆盖三层都拿不出正比例的那一格。
//
// 管理员把全站充值汇率改成 0 且没配兜底档:结果与本档出现之前完全一致
// (currentUsdRate 原样返回 0),这里锁的是"它必须被计为一次降级",
// 否则 available_fiat 会在无人知晓的情况下停止增长而额度照加。
func TestFiatRateNoUsableLayer(t *testing.T) {
	useMoneyGlobals(t, 0, 500000)
	d := fiatRateFor(nil, opSettings{})
	assert.Equal(t, fiatLayerNone, d.Layer)
	assert.True(t, d.Rate.IsZero())
	assert.NotEmpty(t, d.Degraded)
}

// TestFiatRateUsesInviterGroupNotInviteeGroup 是本档最重要的一条:口径。
//
// 上线 42 在 promoter 组,下线 900 在 vip 组。两个分组各自配了**互不相同**的
// 费率与法币比例,于是"取错了人"会体现成两个具体的错数字,而不是碰巧相等:
//
//	费率     必须取上线 promoter 的 1%(取成下线是 8%)
//	法币比例 必须取上线 promoter 的 9 (取成下线是 3、取成全站汇率是 6)
//
// 2026-08-18 之前这里断言的是"费率取下线、比例取上线" —— 那个分叉不是设计,
// 是两轮开发各自选了一个口径。现在两档同源,见 grouprate.go 的口径说明与
// pricing.go 的文件头。
//
// 把 hook.go 里的 e.InviterId 改成 ev.InviteeId,这条测试立刻变红;
// 而只在一个分组上配比例的测试,改成哪一侧都照样全绿。
func TestFiatRateUsesInviterGroupNotInviteeGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)

	seedGroupRate(t, gdb, "vip", "12", "8", true)
	seedGroupRate(t, gdb, "promoter", "1", "1", true)
	seedFiatRate(t, gdb, "vip", "3", true)
	seedFiatRate(t, gdb, "promoter", "9", true)

	cacheUser(900, 42, "vip")    // 下线
	cacheUser(42, 0, "promoter") // 上线

	at := common.GetTimestamp()
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	row := accrualOfInvitee(t, gdb, 900)
	assert.Equal(t, 100, row.RateUnits, "费率必须按上线 promoter 的 1%;取成下线的会是 8%")
	assert.Equal(t, "promoter", row.RateGroup)
	assert.Equal(t, "9", row.UsdRate.String(),
		"法币比例必须按上线 promoter 的 9;取成下线的会是 3,取成全站汇率会是 6")
	// 同一行上的两档必须出自同一个分组 —— 这正是本轮统一口径的那条不变量。
	assert.Equal(t, row.RateGroup, "promoter",
		"rate_group 与 usd_rate 必须能被同一个分组解释")
}

// TestAccrueOneShotFreezesInviterFiatRate 覆盖充值/兑换码那一路。
func TestAccrueOneShotFreezesInviterFiatRate(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)
	seedFiatRate(t, gdb, "promoter", "9", true)

	cacheUser(900, 42, "vip")
	cacheUser(42, 0, "promoter")

	require.NoError(t, accrueOneShot(context.Background(), 900, 10000,
		decimal.Zero, SourceTopup, topupIdemKey("TX-1"), "TX-1"))

	row := accrualOfInvitee(t, gdb, 900)
	assert.Equal(t, "9", row.UsdRate.String())
}

// TestAccrueConsumeSplitsRowWhenFiatRateChanges 锁定"日聚合桶不混两套折算比例"。
//
// usd_rate 不参与 gross 的算术,所以混两套不会当场破坏 base × rate = gross;
// 它坏的是结算那一步:available_fiat 按 usd_rate 的加权平均折算,一行标着 9
// 却有一半 gross 是在比例改成 12 之后挣的,那半笔钱永久按旧比例入账,
// 而账面上没有任何东西显示这里调过价。
func TestAccrueConsumeSplitsRowWhenFiatRateChanges(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)
	seedFiatRate(t, gdb, "promoter", "9", true)

	at := common.GetTimestamp()
	cacheUser(900, 42, "vip")
	cacheUser(42, 0, "promoter")
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	// 同一天里把 promoter 的折算比例从 9 调到 12。
	require.NoError(t, gdb.Model(&FiatRate{}).Where("group_name = ?", "promoter").
		Update("rate", decimal.RequireFromString("12")).Error)
	invalidateFiatRates()
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: 900, Quota: 10000, At: at}))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", 900).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "折算比例变了必须落新的一行,不能并进旧桶")
	assert.Equal(t, "9", rows[0].UsdRate.String())
	assert.Equal(t, "12", rows[1].UsdRate.String())
	for _, r := range rows {
		assert.Equal(t, calcGross(r.BaseQuota, r.RateUnits).String(), r.GrossAmount.String(),
			"每一行仍必须自洽:base × rate = gross(accrual_no=%s)", r.AccrualNo)
	}
}

// TestFiatRateFallsBackThroughSettings 端到端走一遍三层,判据打在**库行**上。
//
// 只测 fiatRateFor 是不够的:兜底档要经过 qy_settings → applyOverrides →
// opSettings 这条链,而链上任何一环把空串读成 0、或者干脆没接上,
// 纯函数的测试照样全绿。
func TestFiatRateFallsBackThroughSettings(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)
	ctx := context.Background()

	cacheUser(900, 42, "vip")
	cacheUser(42, 0, "promoter")

	// 第 3 层:什么都没配,走全站充值汇率 6。这是每一个存量站点升级上来的样子。
	require.Nil(t, effective().FiatRateDefault, "前提:兜底档从未配过")
	assert.Equal(t, "6", resolveInviterPricing(ctx, 42, SourceConsume, effective()).Fiat.Rate.String())
	assert.Equal(t, fiatLayerGlobal, resolveInviterPricing(ctx, 42, SourceConsume, effective()).Fiat.Layer)

	// 第 2 层:配上兜底档。
	seedFiatDefault(t, gdb, "7.3")
	require.NotNil(t, effective().FiatRateDefault, "兜底档没能从 qy_settings 读出来")
	d := resolveInviterPricing(ctx, 42, SourceConsume, effective()).Fiat
	assert.Equal(t, "7.3", d.Rate.String())
	assert.Equal(t, fiatLayerDefault, d.Layer)

	// 第 1 层:再配上分组档。
	seedFiatRate(t, gdb, "promoter", "9", true)
	d = resolveInviterPricing(ctx, 42, SourceConsume, effective()).Fiat
	assert.Equal(t, "9", d.Rate.String())
	assert.Equal(t, fiatLayerGroup, d.Layer)
	assert.Equal(t, "promoter", d.Group)

	// 删掉分组档回落兜底档 —— 不是回落全站汇率,更不是变成 0。
	removed, err := deleteFiatRate(ctx, "promoter")
	require.NoError(t, err)
	require.True(t, removed)
	d = resolveInviterPricing(ctx, 42, SourceConsume, effective()).Fiat
	assert.Equal(t, "7.3", d.Rate.String())
	assert.Equal(t, fiatLayerDefault, d.Layer)
}

// TestFiatRateDefaultOverrideIgnoresUnusableValues 守 qy_settings 那一行被写坏的情形。
//
// 这一行是可以被人手工 UPDATE 的。钳到边界会静默生效一个谁都没批准的比例;
// 读成 0 更糟 —— 那是"额度照加、法币不加"这种无声漂移的唯一入口。
// 两种情况都必须变成"当作没配",回落下一层。
func TestFiatRateDefaultOverrideIgnoresUnusableValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want *string
	}{
		{"合法值生效", "7.3", strPtr("7.3")},
		{"空串 = 没配,保持 nil", "", nil},
		{"零被丢弃而不是读成「不折算」", "0", nil},
		{"负数被丢弃", "-3", nil},
		{"超上界被丢弃而不是钳到上界", "99999999", nil},
		{"非数字被丢弃", "seven", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := opSettings{}
			applyOverrides(&s, map[string]string{keyFiatRateDefault: tc.raw})
			if tc.want == nil {
				assert.Nil(t, s.FiatRateDefault)
				return
			}
			require.NotNil(t, s.FiatRateDefault)
			assert.Equal(t, *tc.want, s.FiatRateDefault.String())
		})
	}
}

func strPtr(s string) *string { return &s }

// TestFiatRateChangeIsNotRetroactive 是"不追溯"那一条,判据打在余额行上。
//
// 比例是**逐笔冻结**的:改比例只影响此后产生的计佣行与由它们驱动的结算。
// 已经算进 available_fiat 的钱是按当时比例算出的绝对值,绝不能被重算 ——
// 那会让全部历史对账当场作废。
func TestFiatRateChangeIsNotRetroactive(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useMoneyGlobals(t, 6, 500000)
	seedFiatRate(t, gdb, "promoter", "9", true)
	cacheUser(42, 0, "promoter")

	// 第一轮:1000000 额度 @ 比例 9 → 1000000/500000 × 9 = 18。
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = 42
		a.GrossAmount = decimal.NewFromInt(1_000_000)
		a.UsdRate = decimal.NewFromInt(9)
	})
	settleUserOnce(t, 42)

	var bal Balance
	require.NoError(t, gdb.Where("user_id = ?", 42).Take(&bal).Error)
	require.Equal(t, int64(1_000_000), bal.AvailableQuota)
	require.Equal(t, "18", bal.AvailableFiat.String(), "前提:第一轮按 9 折算")

	// 运营把 promoter 的比例调到 20,并立即再结一次(此时没有新的计佣行)。
	require.NoError(t, gdb.Model(&FiatRate{}).Where("group_name = ?", "promoter").
		Update("rate", decimal.RequireFromString("20")).Error)
	invalidateFiatRates()
	settleUserOnce(t, 42)

	require.NoError(t, gdb.Where("user_id = ?", 42).Take(&bal).Error)
	assert.Equal(t, "18", bal.AvailableFiat.String(),
		"改比例不得重算存量:按新比例反算会是 40")

	// 此后产生的计佣行才按新比例入账:再来 1000000 额度 @ 20 → +40。
	seedAccrual(t, gdb, 2, func(a *Accrual) {
		a.InviterId = 42
		a.GrossAmount = decimal.NewFromInt(1_000_000)
		a.UsdRate = decimal.NewFromInt(20)
	})
	settleUserOnce(t, 42)

	require.NoError(t, gdb.Where("user_id = ?", 42).Take(&bal).Error)
	assert.Equal(t, int64(2_000_000), bal.AvailableQuota)
	assert.Equal(t, "58", bal.AvailableFiat.String(),
		"存量 18(按 9)+ 新增 40(按 20);整体按新比例重算会是 80")
}

// TestFiatRateCrudTakesEffectImmediately 锁定管理端增删改后缓存立即失效。
//
// 缓存 60 秒的话,运营改完会以为没生效然后再改一次 —— 这类"改完看不见"
// 是本项目反复吃亏的形状。同名再写一次必须是覆盖,否则唯一索引之外还会
// 多出一条谁都查不到的影子规则。
func TestFiatRateCrudTakesEffectImmediately(t *testing.T) {
	newTestDB(t)
	useConfig(t, commissionConfig(1))
	useMoneyGlobals(t, 6, 500000)
	ctx := context.Background()

	require.Empty(t, fiatRates(ctx), "前提:一条分组档都没有")
	assert.Equal(t, fiatLayerGlobal, resolveFiatRate(ctx, "promoter", effective()).Layer)

	row := FiatRate{GroupName: "Promoter", Rate: decimal.RequireFromString("9"), Enabled: true}
	require.NoError(t, upsertFiatRate(ctx, &row))
	assert.Equal(t, "promoter", row.GroupName, "写入侧必须归一化分组名")
	d := resolveFiatRate(ctx, "PROMOTER", effective())
	assert.Equal(t, "9", d.Rate.String(), "新增后必须立刻生效,且判定与写入同一套归一化")

	row2 := FiatRate{GroupName: "promoter", Rate: decimal.RequireFromString("11"), Enabled: true}
	require.NoError(t, upsertFiatRate(ctx, &row2))
	all, err := listFiatRates(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1, "同名再写是覆盖,不是插一行")
	assert.Equal(t, "11", resolveFiatRate(ctx, "promoter", effective()).Rate.String())

	removed, err := deleteFiatRate(ctx, "promoter")
	require.NoError(t, err)
	require.True(t, removed)
	assert.Equal(t, fiatLayerGlobal, resolveFiatRate(ctx, "promoter", effective()).Layer,
		"删掉分组档回落层级,不是变成 0")

	removed, err = deleteFiatRate(ctx, "promoter")
	require.NoError(t, err)
	assert.False(t, removed, "本来就不存在与删除成功必须分得开")

	// 写入侧同样不接受不可用的比例:管理端的 400 之外再守一道,
	// 因为 upsertFiatRate 也服务于将来可能出现的非 HTTP 调用方。
	assert.Error(t, upsertFiatRate(ctx, &FiatRate{GroupName: "x", Rate: decimal.Zero, Enabled: true}))
	assert.Error(t, upsertFiatRate(ctx,
		&FiatRate{GroupName: "", Rate: decimal.RequireFromString("9"), Enabled: true}))
}

// TestFiatRateEmptyGroupFoldsToDefault 与费率那一侧同口径。
//
// 主库 users.group 的列默认值是 'default',但历史行与被直接改过库的账号
// 可能留着空串,而它们在业务上就是默认分组的用户。不折叠的话,给 default
// 配的比例对这批账号完全不生效。
func TestFiatRateEmptyGroupFoldsToDefault(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useMoneyGlobals(t, 6, 500000)
	seedFiatRate(t, gdb, "default", "9", true)

	ctx := context.Background()
	for _, g := range []string{"", "  ", "default", "DEFAULT"} {
		d := resolveFiatRate(ctx, g, effective())
		assert.Equal(t, "9", d.Rate.String(), "分组 %q 应当命中 default 的分组档", g)
		assert.Equal(t, "default", d.Group)
	}
}

// TestAdminPutConfigClearsFiatRateDefaultWithNull 守"第三层必须回得去"。
//
// fiatRateFor 声明的第三层是全站充值汇率,而在这条路补上之前,兜底档一旦配过
// 就再也回不去:写入侧对空串直接 400,而全仓没有任何 DELETE 入口。后果不是
// 界面难看 —— 手填一个与当前 USDExchangeRate 相同的数字只是**数值上**的巧合:
// 充值汇率此后再改,佣金折算不会跟着走,界面上却仍写着 layer=default。
// 产品自己定义的一层永久不可达,运营只能进库里 DELETE。
//
// 同时钉住:清空只认显式 null,空串仍然必须 400。这两个方向是一对 ——
// 只补"能清"会让运营把输入框清空再保存就静默改掉全站佣金折算口径。
func TestAdminPutConfigClearsFiatRateDefaultWithNull(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	// 全站充值汇率 6:第三层拿得出一个合法比例,回落之后才有东西可看。
	useMoneyGlobals(t, 6, 500000)

	// 先配上兜底档,把生效层推到 default。
	rec := callAdminHandler(t, http.MethodPut, "/admin/commission/config",
		`{"fiat_rate_default":"7.30"}`, adminPutConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s := effective()
	require.NotNil(t, s.FiatRateDefault)
	require.Equal(t, "7.3", s.FiatRateDefault.String())
	require.Equal(t, fiatLayerDefault, fiatRateFor(nil, s).Layer)

	// 空串仍然是误操作,不是"清空"。
	rec = callAdminHandler(t, http.MethodPut, "/admin/commission/config",
		`{"fiat_rate_default":""}`, adminPutConfig)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NotNil(t, effective().FiatRateDefault,
		"空串把兜底档清掉了 —— 一次误触就能改掉全站佣金折算口径")

	// 显式 null 才是"回落全站充值汇率"。
	rec = callAdminHandler(t, http.MethodPut, "/admin/commission/config",
		`{"fiat_rate_default":null}`, adminPutConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var rows []qymodel.Setting
	require.NoError(t, gdb.Where("scope = ? AND k = ?", settingScope, keyFiatRateDefault).
		Find(&rows).Error)
	assert.Empty(t, rows, "清空必须删掉覆盖行,不能留一个 0 或空值行")

	s = effective()
	assert.Nil(t, s.FiatRateDefault, "清空之后必须是「没配」,不是 0")
	d := fiatRateFor(nil, s)
	assert.Equal(t, fiatLayerGlobal, d.Layer, "第三层没有回得去")
	assert.Equal(t, "6", d.Rate.String(), "回落之后必须真的跟随全站充值汇率")
	assert.Empty(t, d.Degraded, "回落到第三层是正常状态,不是降级")

	// 真正的判据:回落之后全站汇率再改一次,折算必须跟着走。
	// 手填一个等于当时汇率的数字做不到这一点,这正是"填个一样的数"顶不了清空的原因。
	useMoneyGlobals(t, 8, 500000)
	assert.Equal(t, "8", fiatRateFor(nil, effective()).Rate.String(),
		"回落之后没有跟随全站充值汇率 = 第三层名存实亡")

	// 每一次改动都要留痕,包括这次清空。
	logs := configAuditLogs(t, gdb)
	require.Len(t, logs, 2, "只有两次成功的改动应当留下配置审计")
	assert.Contains(t, logs[1].BeforeSnap, `"fiat_rate_default":"7.3"`)
	assert.Contains(t, logs[1].AfterSnap, `"fiat_rate_default":""`)
}
