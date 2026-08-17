package commission

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件守「兑换码单独一档返佣比例」的全部口径。
//
// 这一档的唯一不可让步的约束是**存量行为不变**:这一档出现之前,兑换码走的是
// 充值档(resolveRate 的 `source != consume → TopupRateUnits`,分组优先)。
// 因此"两级都没配"这一格必须逐条与旧行为对齐,而 0% 必须与"没配"分得开 ——
// 用 0 兼任"没配"的话,一次升级就会把全站兑换码返佣静默清零,而账本上
// 每一行都自洽、看不出任何异常。
//
// 所以这里不只测 resolveRate 这个纯函数:本模块历次缺陷全是"纯函数对了、
// 调度层没接"的形状,因此兑换码那条路必须真的从 onRedeemSuccess 的下游
// (accrueOneShot)走一遍、写进库、再回读那一行冻结下来的费率。

// seedRedemptionGroupRate 写一条带兑换码档的分组规则。
// redemption 为 nil 表示该分组不单独配兑换码档。
func seedRedemptionGroupRate(t *testing.T, gdb *gorm.DB, group, topup, consume string, redemption *string) {
	t.Helper()
	topupUnits, err := config.RatePercentUnits(topup)
	require.NoError(t, err)
	consumeUnits, err := config.RatePercentUnits(consume)
	require.NoError(t, err)
	var redemptionUnits *int
	if redemption != nil {
		u, err := config.RatePercentUnits(*redemption)
		require.NoError(t, err)
		redemptionUnits = &u
	}
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&GroupRate{
		GroupName:           group,
		TopupRateUnits:      topupUnits,
		ConsumeRateUnits:    consumeUnits,
		RedemptionRateUnits: redemptionUnits,
		Enabled:             true,
		CreatedAt:           now,
		UpdatedAt:           now,
	}).Error)
	invalidateGroupRates()
}

// seedGlobalRedemptionOverride 往 qy_settings 写一条全局兑换码档覆盖。
func seedGlobalRedemptionOverride(t *testing.T, gdb *gorm.DB, percent string) {
	t.Helper()
	require.NoError(t, gdb.Create(&qymodel.Setting{
		Scope:     settingScope,
		K:         keyRedemptionRatePercent,
		V:         percent,
		UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()
}

func strptr(s string) *string { return &s }

// TestRedemptionRateLadder 锁定兑换码档的四级取值顺序。
//
// 顺序:分组兑换码档 → 全局兑换码档 → 分组充值档 → 全局充值档。
// 表格刻意把"两级都没配"与"显式 0%"排在一起 —— 它们是这一档全部风险的所在。
func TestRedemptionRateLadder(t *testing.T) {
	ctx := context.Background()
	zero := 0
	units := func(v int) *int { return &v }

	cases := []struct {
		name string
		// 分组规则;matched=false 表示这个分组没有规则。
		matched     bool
		groupTopup  int
		groupRedeem *int
		// 全局。
		globalTopup  int
		globalRedeem *int

		want int
	}{
		{
			name:        "两级都没配:跟随全局充值档(升级前行为)",
			matched:     false,
			globalTopup: 1000,
			want:        1000,
		},
		{
			name:        "两级都没配但命中分组:跟随分组充值档(升级前行为)",
			matched:     true,
			groupTopup:  1250,
			globalTopup: 1000,
			want:        1250,
		},
		{
			name:         "只配了全局兑换码档:全局说了算",
			matched:      false,
			globalTopup:  1000,
			globalRedeem: units(300),
			want:         300,
		},
		{
			name:         "配了全局兑换码档,分组只配了充值档:全局兑换码档仍然生效",
			matched:      true,
			groupTopup:   1250,
			globalTopup:  1000,
			globalRedeem: units(300),
			want:         300,
		},
		{
			name:         "分组兑换码档覆盖全局兑换码档",
			matched:      true,
			groupTopup:   1250,
			groupRedeem:  units(800),
			globalTopup:  1000,
			globalRedeem: units(300),
			want:         800,
		},
		{
			name:        "分组兑换码档在没有全局兑换码档时也生效",
			matched:     true,
			groupTopup:  1250,
			groupRedeem: units(800),
			globalTopup: 1000,
			want:        800,
		},
		{
			// 这一格与"两级都没配"必须给出不同的数,否则 0 就等于没配。
			name:         "全局显式 0%:兑换码不返佣,不是回落充值档",
			matched:      false,
			globalTopup:  1000,
			globalRedeem: &zero,
			want:         0,
		},
		{
			name:         "分组显式 0% 压过全局的非零兑换码档",
			matched:      true,
			groupTopup:   1250,
			groupRedeem:  &zero,
			globalTopup:  1000,
			globalRedeem: units(300),
			want:         0,
		},
		{
			name:        "分组显式 0% 压过分组自己的充值档",
			matched:     true,
			groupTopup:  1250,
			groupRedeem: &zero,
			globalTopup: 1000,
			want:        0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := GroupRate{TopupRateUnits: tc.groupTopup, RedemptionRateUnits: tc.groupRedeem}
			s := opSettings{TopupRateUnits: tc.globalTopup, RedemptionRateUnits: tc.globalRedeem}
			assert.Equal(t, tc.want, redemptionRateUnits(rule, tc.matched, s))
		})
	}

	// 三档互不串:同一份配置下,三个来源各取各的那一列。
	t.Run("三档互不串", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))
		seedRedemptionGroupRate(t, gdb, "vip", "12.5", "8.25", strptr("2.5"))
		s := effective()

		assert.Equal(t, 1250, resolveRate(ctx, "vip", SourceTopup, s).Units, "充值档")
		assert.Equal(t, 825, resolveRate(ctx, "vip", SourceConsume, s).Units, "消费档")
		assert.Equal(t, 250, resolveRate(ctx, "vip", SourceRedemption, s).Units, "兑换码档")
		// 任务补扣等其余来源仍然走充值档,兑换码档不得渗进来。
		assert.Equal(t, 1250, resolveRate(ctx, "vip", SourceManual, s).Units, "其余来源仍走充值档")
	})
}

// TestRedemptionRateUnconfiguredMatchesLegacy 是**存量行为**的锁。
//
// 断言的不是"某个数字",而是"兑换码这一档在没人配它的时候,与充值档逐格相等"。
// 任何让默认值不再等价于跟随充值档的改动 —— 把 *int 换成 int、把 nil 当成 0、
// 把回落顺序里的分组充值档那一级删掉 —— 都会在这里失败。
func TestRedemptionRateUnconfiguredMatchesLegacy(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedRedemptionGroupRate(t, gdb, "vip", "12.5", "8.25", nil)
	seedRedemptionGroupRate(t, gdb, "wholesale", "3", "1.5", nil)

	ctx := context.Background()
	s := effective()
	require.Nil(t, s.RedemptionRateUnits, "前提:YAML 没写兑换码档,库里也没有覆盖")

	for _, group := range []string{"vip", "wholesale", "default", "从未配过的分组", ""} {
		topup := resolveRate(ctx, group, SourceTopup, s)
		redemption := resolveRate(ctx, group, SourceRedemption, s)
		assert.Equal(t, topup.Units, redemption.Units,
			"分组 %q:没配兑换码档时,它必须与充值档逐格相等", group)
		assert.Equal(t, topup.Group, redemption.Group)
		assert.Equal(t, topup.Matched, redemption.Matched)
	}
}

// TestRedemptionAccrualUsesRedemptionRate 让兑换码那条路真的走到库里。
//
// 从 accrueOneShot 进(onRedeemSuccess 的直接下游,幂等键与来源都由它给),
// 再回读那一行冻结下来的 rate_units 与 gross_amount。只测 resolveRate 的话,
// 把 hook.go 里的 SourceRedemption 改回 SourceTopup 测试照样全绿 —— 而那正是
// 这一档最可能被接错的地方。
func TestRedemptionAccrualUsesRedemptionRate(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGlobalRedemptionOverride(t, gdb, "3")

	getInviterCache().Set(900, inviterEntry{
		InviterId:      42,
		InviteeName:    "u900",
		InviteeCreated: common.GetTimestamp() - 30*86400,
		InviteeGroup:   "vip",
	})

	ctx := context.Background()
	require.NoError(t, accrueOneShot(ctx, 900, 20000, decimal.Zero,
		SourceRedemption, redemptionIdemKey(77), "RD77"))

	row := accrualOfInvitee(t, gdb, 900)
	assert.Equal(t, SourceRedemption, row.SourceType)
	assert.Equal(t, 300, row.RateUnits, "冻结进账本的必须是兑换码档,不是充值档 1000")
	assert.Equal(t, "vip", row.RateGroup)
	// 20000 × 3% = 600。费率全程整数,gross 走 decimal,不经过 float64。
	assert.Equal(t, "600", row.GrossAmount.String())
}

// TestRedemptionAccrualFollowsTopupWhenUnset 是上一条的反面:没配就跟随充值档,
// 而且账本行与走充值档时长得完全一样。
func TestRedemptionAccrualFollowsTopupWhenUnset(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedRedemptionGroupRate(t, gdb, "vip", "12.5", "8.25", nil)

	getInviterCache().Set(900, inviterEntry{
		InviterId:      42,
		InviteeName:    "u900",
		InviteeCreated: common.GetTimestamp() - 30*86400,
		InviteeGroup:   "vip",
	})

	ctx := context.Background()
	require.NoError(t, accrueOneShot(ctx, 900, 20000, decimal.Zero,
		SourceRedemption, redemptionIdemKey(88), "RD88"))

	row := accrualOfInvitee(t, gdb, 900)
	assert.Equal(t, 1250, row.RateUnits, "没配兑换码档 ⇒ 走 vip 的充值档 12.5%")
	// 20000 × 12.5% = 2500。
	assert.Equal(t, "2500", row.GrossAmount.String())
}

// TestGlobalRedemptionOverrideZeroIsNotUnset 守零值陷阱在**配置读取**这一侧。
//
// qy_settings 里的 "0" 必须解析成显式 0%,而"这一行不存在"必须解析成 nil。
// 这两件事在 opSettings 上是 *int 的 0 与 nil,在接口上是 "0" 与 ""。
func TestGlobalRedemptionOverrideZeroIsNotUnset(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))

	before := effective()
	require.Nil(t, before.RedemptionRateUnits, "没有覆盖行 ⇒ nil")
	assert.Equal(t, "", before.RedemptionRatePercent(), "配的是什么:空")
	assert.Equal(t, 1000, before.EffectiveRedemptionRateUnits(), "实际按几个点:跟随充值档")
	assert.Equal(t, "10", before.EffectiveRedemptionRatePercent())

	seedGlobalRedemptionOverride(t, gdb, "0")

	after := effective()
	require.NotNil(t, after.RedemptionRateUnits, `"0" 是显式配置,不是"没配"`)
	assert.Equal(t, 0, *after.RedemptionRateUnits)
	assert.Equal(t, "0", after.RedemptionRatePercent())
	assert.Equal(t, 0, after.EffectiveRedemptionRateUnits(), "显式 0% 不得回落充值档")
}

// TestGlobalRedemptionOverrideBlankFallsBackToYaml 守"库里存着空值"这一格。
//
// 覆盖行的 v 为空(手工 UPDATE、或某个版本写进去的空串)不得被读成 0%,
// 它与"没有这一行"是同一个意思:回落 YAML。
func TestGlobalRedemptionOverrideBlankFallsBackToYaml(t *testing.T) {
	gdb := newTestDB(t)
	cfg := commissionRateConfig("10", "5")
	cfg.Commission.RedemptionRatePercent = "4"
	useConfig(t, cfg)

	seedGlobalRedemptionOverride(t, gdb, "   ")

	s := effective()
	require.NotNil(t, s.RedemptionRateUnits)
	assert.Equal(t, 400, *s.RedemptionRateUnits, "空覆盖 ⇒ 回落 YAML 的 4%,不是 0%")
}

// TestConfigNullableRateUnits 锁定 YAML 侧的换算:空 ⇒ nil,"0" ⇒ 显式 0,
// 写坏了 ⇒ nil(跟随充值档),而不是 configRateUnits 那种回落 0。
//
// 回落方向的选择是有代价差的:回落 nil 只是维持存量行为;回落 0 等于替一个
// 填错格式的运营做出"兑换码不返佣"的决定,而那笔少发的佣金没人会来投诉。
func TestConfigNullableRateUnits(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want *int
	}{
		{"空串 ⇒ 没配", "", nil},
		{"纯空白 ⇒ 没配", "   ", nil},
		{"显式 0 ⇒ 0%", "0", func() *int { v := 0; return &v }()},
		{"两位小数", "2.55", func() *int { v := 255; return &v }()},
		{"非数值 ⇒ 没配", "abc", nil},
		{"负数 ⇒ 没配", "-1", nil},
		{"超过 100% ⇒ 没配", "101", nil},
		{"三位小数 ⇒ 没配", "1.005", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := configNullableRateUnits("redemption_rate_percent", tc.raw)
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tc.want, *got)
		})
	}
}

// TestAdminPutRedemptionRateClearsOverride 守管理端那条"改回跟随"的路。
//
// 传空串必须**删掉** qy_settings 里那一行,而不是写一个 0 进去。写 0 的话
// 界面下次打开会显示"兑换码 0%",而运营的本意是"跟随充值档" —— 这两件事
// 在钱上差的正是整整一档费率。
func TestAdminPutRedemptionRateClearsOverride(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)

	rec := callAdminHandler(t, http.MethodPut, "/admin/commission/config",
		`{"redemption_rate_percent":"3.5"}`, adminPutConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s := effective()
	require.NotNil(t, s.RedemptionRateUnits)
	require.Equal(t, 350, *s.RedemptionRateUnits)

	rec = callAdminHandler(t, http.MethodPut, "/admin/commission/config",
		`{"redemption_rate_percent":""}`, adminPutConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var rows []qymodel.Setting
	require.NoError(t, gdb.Where("scope = ? AND k = ?", settingScope, keyRedemptionRatePercent).
		Find(&rows).Error)
	assert.Empty(t, rows, `清空必须删掉覆盖行,不能留一个 0 或空值行`)
	assert.Nil(t, effective().RedemptionRateUnits, "清空之后回到「跟随充值档」")

	// 审计里必须看得出"从 3.5% 改回了跟随":两条快照的兑换码字段分别是 "3.5" 与 ""。
	logs := configAuditLogs(t, gdb)
	require.Len(t, logs, 2)
	assert.Contains(t, logs[1].BeforeSnap, `"redemption_rate_percent":"3.5"`)
	assert.Contains(t, logs[1].AfterSnap, `"redemption_rate_percent":""`)
	assert.Contains(t, logs[1].AfterSnap, `"redemption_rate_effective_percent":"10"`,
		"审计快照必须同时留下「实际按几个点」,否则事后看不出跟随到了哪个数")
}

// TestAdminPutGroupRateRedemptionRoundTrip 守分组那一层的可空往返。
//
// 三个动作各走一遍同一个 upsert 接口:不带该字段(老客户端)、显式 0%、
// 显式 null(取消)。最后一步是最容易漏的 —— DoUpdates 里少写这一列的话,
// "取消"会变成一次静默的空操作。
func TestAdminPutGroupRateRedemptionRoundTrip(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)

	put := func(body string) *httptest.ResponseRecorder {
		return callAdminHandler(t, http.MethodPut, "/admin/commission/group-rates",
			body, adminPutGroupRate)
	}
	read := func() GroupRate {
		var rows []GroupRate
		require.NoError(t, gdb.Where("group_name = ?", "vip").Find(&rows).Error)
		require.Len(t, rows, 1)
		return rows[0]
	}

	// 1. 老客户端的报文里根本没有这个字段 ⇒ 不配 ⇒ 该分组维持升级前行为。
	rec := put(`{"group_name":"vip","topup_rate_percent":"12.5","consume_rate_percent":"8.25","enabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Nil(t, read().RedemptionRateUnits)
	assert.Contains(t, rec.Body.String(), `"redemption_rate_percent":null`,
		"回显必须是 null 而不是 \"\" 或 \"0\"")

	// 2. 显式 0%。
	rec = put(`{"group_name":"vip","topup_rate_percent":"12.5","consume_rate_percent":"8.25","redemption_rate_percent":"0","enabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row := read()
	require.NotNil(t, row.RedemptionRateUnits, `"0" 必须落成显式 0,不是 NULL`)
	assert.Equal(t, 0, *row.RedemptionRateUnits)
	assert.Contains(t, rec.Body.String(), `"redemption_rate_percent":"0"`)

	invalidateGroupRates()
	assert.Equal(t, 0, resolveRate(context.Background(), "vip", SourceRedemption, effective()).Units,
		"显式 0% 必须真的让这个分组的兑换码返佣归零")

	// 3. 取消:改回 null。
	rec = put(`{"group_name":"vip","topup_rate_percent":"12.5","consume_rate_percent":"8.25","redemption_rate_percent":null,"enabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Nil(t, read().RedemptionRateUnits,
		"取消必须把列改回 NULL —— upsert 的 DoUpdates 漏掉这一列时,这里会读到 0")

	invalidateGroupRates()
	assert.Equal(t, 1250, resolveRate(context.Background(), "vip", SourceRedemption, effective()).Units,
		"取消之后回到「跟随本组充值档」")
}

// TestAdminPutGroupRateRejectsBadRedemptionPercent 守写入侧的格式校验。
// 越界值一律 400,绝不钳到边界 —— 与全局费率同一条理由。
func TestAdminPutGroupRateRejectsBadRedemptionPercent(t *testing.T) {
	newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)

	for _, bad := range []string{`"101"`, `"-1"`, `"1.005"`, `"abc"`} {
		rec := callAdminHandler(t, http.MethodPut, "/admin/commission/group-rates",
			`{"group_name":"vip","topup_rate_percent":"12.5","consume_rate_percent":"8.25",`+
				`"redemption_rate_percent":`+bad+`,"enabled":true}`, adminPutGroupRate)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "兑换码比例 %s 必须被拒", bad)
		assert.True(t, strings.Contains(rec.Body.String(), "兑换码返佣比例"),
			"错误信息要点名是哪一档: %s", rec.Body.String())
	}
}
