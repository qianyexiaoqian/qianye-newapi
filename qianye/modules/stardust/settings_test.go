package stardust

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settings_test.go —— 运营覆盖层的三条契约:合规门、越界回落、写侧拒绝。
//
// 合规门是这里唯一与"值钱的东西"直接相关的规则:邀请类星屑能在商城换套餐,
// 把它绕过去的表现只是"多发了一些奖励",不会有任何报错。

// withCompliance 钉住支付合规声明的确认状态,测完还原。
func withCompliance(t *testing.T, confirmed bool) {
	t.Helper()
	ps := operation_setting.GetPaymentSetting()
	prev := *ps
	ps.ComplianceConfirmed = confirmed
	if confirmed {
		ps.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	}
	t.Cleanup(func() { *ps = prev })
}

func stardustConfig(mutate func(*config.Stardust)) *config.Config {
	sd := config.Stardust{
		Enabled: true, Name: "星屑", ConsumeBps: 10_000,
		InviteTopupBps: 300, InviteRedeemBps: 200, InviteRegisterStardust: 50, HeldAlertDays: 7,
	}
	if mutate != nil {
		mutate(&sd)
	}
	return &config.Config{
		Enabled: true, Stardust: sd,
		Invite: config.Invite{Enabled: true, InviterCacheSecs: 300},
	}
}

func TestInviteRewardsCollapseToZeroUntilComplianceConfirmed(t *testing.T) {
	newTestDB(t)
	useConfig(t, stardustConfig(nil))

	withCompliance(t, false)
	got := effective()
	assert.Zero(t, got.InviteTopupBps)
	assert.Zero(t, got.InviteRedeemBps)
	assert.Zero(t, got.InviteRegisterStardust)
	assert.Equal(t, 10_000, got.ConsumeBps, "消费返不受合规门约束")

	// 确认合规之后**立即**生效,不受 60 秒快照钉住。
	withCompliance(t, true)
	got = effective()
	assert.Equal(t, 300, got.InviteTopupBps)
	assert.Equal(t, 200, got.InviteRedeemBps)
	assert.EqualValues(t, 50, got.InviteRegisterStardust)
}

func TestOutOfRangeOverridesFallBackToYaml(t *testing.T) {
	useConfig(t, stardustConfig(nil))
	base := baseSettings(config.Get().Stardust)

	cases := []struct {
		name string
		rows map[string]string
		want opSettings
	}{
		{"合法覆盖全部采纳", map[string]string{
			keyName: " 星尘 ", keyShowEntry: "0", keyConsumeBps: "5000", keyInviteTopupBps: "0",
			keyInviteRegisterStardust: "12", keyHeldAlertDays: "30",
		}, opSettings{Name: "星尘", ShowEntry: false, ConsumeBps: 5000, InviteTopupBps: 0,
			InviteRedeemBps: 200, InviteRegisterStardust: 12, HeldAlertDays: 30}},
		{"空白名字回落", map[string]string{keyName: "   "}, base},
		{"超长名字回落", map[string]string{keyName: strings.Repeat("星", 17)}, base},
		{"比例超上界回落", map[string]string{keyConsumeBps: "10000001"}, base},
		{"比例为负回落", map[string]string{keyInviteRedeemBps: "-1"}, base},
		{"告警天数超上界回落", map[string]string{keyHeldAlertDays: "366"}, base},
		{"非数字回落", map[string]string{keyInviteRegisterStardust: "many"}, base},
		{"show_entry 认 true/false", map[string]string{keyShowEntry: "false"}, func() opSettings {
			s := base
			s.ShowEntry = false
			return s
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mergeOverrides(base, tc.rows))
		})
	}
}

func TestSaveOverridesRejectsBeforeWriting(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, stardustConfig(nil))
	withCompliance(t, false)
	ctx := context.Background()

	cases := []struct {
		name  string
		patch map[string]string
		code  string
	}{
		{"白名单之外的键", map[string]string{"max_manual_adjust": "1"}, codeBadSetting},
		{"比例越界", map[string]string{keyConsumeBps: "10000001"}, codeBadSetting},
		{"名字超长", map[string]string{keyName: strings.Repeat("a", 17)}, codeBadSetting},
		{"show_entry 非布尔", map[string]string{keyShowEntry: "yes"}, codeBadSetting},
		{"合规未确认时邀请类正值", map[string]string{keyInviteTopupBps: "1"}, codeComplianceRequired},
		{"合规未确认时注册奖正值", map[string]string{keyInviteRegisterStardust: "1"}, codeComplianceRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := saveOverrides(ctx, tc.patch, 1)
			be, ok := AsBizError(err)
			require.True(t, ok, "必须是可回给管理员的业务错误: %v", err)
			assert.Equal(t, tc.code, be.ErrCode())
		})
	}
	var n int64
	require.NoError(t, gdb.Model(&qymodel.Setting{}).Where("scope = ?", settingScope).Count(&n).Error)
	assert.Zero(t, n, "被拒绝的保存一行都不该落库")

	// 合规未确认时邀请类写 0 是允许的(那是"关掉"),其余键照常写入并立即生效。
	require.NoError(t, saveOverrides(ctx, map[string]string{
		keyName: "星尘", keyShowEntry: "true", keyConsumeBps: "2500", keyInviteTopupBps: "0",
	}, 42))
	got := effective()
	assert.Equal(t, "星尘", got.Name)
	assert.True(t, got.ShowEntry)
	assert.Equal(t, 2500, got.ConsumeBps)

	var rows []qymodel.Setting
	require.NoError(t, gdb.Where("scope = ?", settingScope).Order("k asc").Find(&rows).Error)
	require.Len(t, rows, 4)
	for _, r := range rows {
		assert.Equal(t, 42, r.OperatorId)
		if r.K == keyShowEntry {
			assert.Equal(t, "1", r.V, "布尔归一成 0/1 落库")
		}
	}
	assert.Equal(t, "星尘", UnitName())
	assert.Equal(t, map[string]any{
		keyName: "星尘", keyShowEntry: int64(1), keyConsumeBps: int64(2500), keyInviteTopupBps: int64(0),
		keyInviteRedeemBps: int64(0), keyInviteRegisterStardust: int64(0), keyInviteConsumeBps: int64(0), keyHeldAlertDays: int64(7),
	}, snapshot(), "快照键名与白名单逐字一致;邀请类三项在合规未确认时按 0 下发")
}

func TestGroupRateLookupNormalizesAndInvalidates(t *testing.T) {
	gdb := newTestDB(t)
	ctx := context.Background()
	bps := 7000
	require.NoError(t, gdb.Create(&GroupRate{UserGroup: "vip", ConsumeBps: &bps, Enabled: true}).Error)
	require.NoError(t, gdb.Create(&GroupRate{UserGroup: "off", ConsumeBps: &bps, Enabled: false}).Error)

	r, ok := groupRateFor(ctx, " VIP ")
	require.True(t, ok, "查表按 groupname.Effective 归一,大小写与空白不影响命中")
	require.NotNil(t, r.ConsumeBps)
	assert.Equal(t, 7000, *r.ConsumeBps)
	assert.Nil(t, r.InviteTopupBps, "没配的档位是 nil,不是 0")

	_, ok = groupRateFor(ctx, "off")
	assert.False(t, ok, "禁用的行等价于没配")
	_, ok = groupRateFor(ctx, "")
	assert.False(t, ok, "空分组按 default 判定,default 没配就回落")

	// 写入之后不失效,60 秒内读到的仍是旧快照;失效之后立即看到新行。
	require.NoError(t, gdb.Create(&GroupRate{UserGroup: "default", ConsumeBps: &bps, Enabled: true}).Error)
	_, ok = groupRateFor(ctx, "")
	assert.False(t, ok)
	invalidateGroupRates()
	_, ok = groupRateFor(ctx, "")
	assert.True(t, ok)
}

func TestNormalizeSourcesIsAClosedSet(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"", DefaultPlanRewardSources, true},
		{"order, balance", "balance,order", true},
		{"ADMIN,admin,redemption", "admin,redemption", true},
		{"order,paypal", "", false},
		{",", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeSources(tc.raw)
		assert.Equal(t, tc.ok, ok, tc.raw)
		assert.Equal(t, tc.want, got, tc.raw)
	}
}
