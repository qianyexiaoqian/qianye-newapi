package stardust

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// topup_scan_test.go —— 邀请返(下线充值)的扫描链路:读游标 → 扫 top_ups → 排除 →
// 邀请判定 → 记账 → 写游标。判据打在 qy_sd_ledger 上:游标值与日志都可能"正常",
// 唯一能证明该发的发了、不该发的没发的东西是流水行。

// TestRunTopupScanCreditsInviterAndSkipsExcludedOrders 是前向扫描 + 迟付回收的整条链路。
func TestRunTopupScanCreditsInviterAndSkipsExcludedOrders(t *testing.T) {
	env := newTaskEnv(t, func(s *config.Stardust) { s.InviteTopupBps = 5000 })
	withCompliance(t, true)
	ctx := context.Background()
	now := common.GetTimestamp()

	require.NoError(t, env.ext.Create(&GroupRate{UserGroup: "vip", InviteTopupBps: bpsPtr(10_000), Enabled: true}).Error)
	invalidateGroupRates()

	seedMainUser(t, env.main, model.User{Id: 101, Group: "vip"})
	seedMainUser(t, env.main, model.User{Id: 102, InviterId: 101})
	seedMainUser(t, env.main, model.User{Id: 103})
	seedMainUser(t, env.main, model.User{Id: 104, InviterId: 103})
	// 105 ↔ 106 互邀:首次解析时被自动拉黑(与佣金共用同一份判据)。
	seedMainUser(t, env.main, model.User{Id: 105, InviterId: 106})
	seedMainUser(t, env.main, model.User{Id: 106, InviterId: 105})
	seedMainUser(t, env.main, model.User{Id: 107})

	setCursor(t, env.ext, topupLowWaterKey, 200)
	const twentyDollars = int64(10_000_000)
	seedTopUp(t, env.main, model.TopUp{Id: 201, UserId: 102, Amount: twentyDollars})
	seedTopUp(t, env.main, model.TopUp{Id: 202, UserId: 104, Amount: twentyDollars})
	// 订阅付费单在 top_ups 里的形状:provider 空、Amount=0、按 Money 记;trade_no 与 subscription_orders 相同。
	seedTopUp(t, env.main, model.TopUp{Id: 203, UserId: 104, TradeNo: "SUB203", PaymentMethod: "epay", Money: 20})
	require.NoError(t, env.main.Create(&model.SubscriptionOrder{UserId: 104, PlanId: 1, TradeNo: "SUB203", Status: "success"}).Error)
	seedTopUp(t, env.main, model.TopUp{Id: 204, UserId: 104, Amount: twentyDollars, PaymentProvider: model.PaymentProviderBalance})
	seedTopUp(t, env.main, model.TopUp{Id: 205, UserId: 104, Amount: twentyDollars, CompleteSource: model.TopUpCompleteSourceAdmin})
	seedTopUp(t, env.main, model.TopUp{Id: 206, UserId: 106, Amount: twentyDollars})
	seedTopUp(t, env.main, model.TopUp{Id: 207, UserId: 107, Amount: twentyDollars})
	seedTopUp(t, env.main, model.TopUp{Id: 208, UserId: 104, Amount: twentyDollars, Status: common.TopUpStatusPending, CreateTime: now - 100})

	runTopupScan(ctx)

	l101 := ledgerOf(t, env.ext, 101)
	require.Len(t, l101, 1)
	assert.Equal(t, string(KindInviteTopup), l101[0].Kind)
	assert.EqualValues(t, 20, l101[0].Amount, "上线在 vip 档 100%:$20 → 20 星屑")
	assert.Equal(t, topupIdemScope, l101[0].IdemScope)
	assert.Equal(t, "topup:T201", l101[0].IdemKey)
	assert.Equal(t, "topup", l101[0].RefType)
	assert.Equal(t, "T201", l101[0].RefNo)
	assert.Equal(t, 102, l101[0].PeerUserId)
	assert.Equal(t, 10_000, l101[0].RateBps)
	assert.Equal(t, "vip", l101[0].RateGroup)
	assert.EqualValues(t, twentyDollars, l101[0].BaseQuota)
	assert.EqualValues(t, 20, balanceOf(t, env.ext, 101).Available)

	l103 := ledgerOf(t, env.ext, 103)
	require.Len(t, l103, 1, "订阅单、余额支付、管理员补单三笔都不返")
	assert.EqualValues(t, 10, l103[0].Amount, "全站 50%:$20 → 10 星屑")
	assert.Equal(t, "T202", l103[0].RefNo)
	assert.Equal(t, 5000, l103[0].RateBps)
	assert.Equal(t, "default", l103[0].RateGroup)
	assert.Empty(t, ledgerOf(t, env.ext, 105), "互邀环路被拉黑,不返")
	assert.Empty(t, ledgerOf(t, env.ext, 106))
	assert.EqualValues(t, 2, ledgerCount(t, env.ext))
	assert.EqualValues(t, 207, cursorOf(t, env.ext, topupLowWaterKey), "游标钉在窗口内未决订单 208 之前")

	// 再扫一遍、以及把游标倒回去重扫:幂等键让任何重叠都不会双发。
	runTopupScan(ctx)
	setCursor(t, env.ext, topupLowWaterKey, 200)
	runTopupScan(ctx)
	assert.EqualValues(t, 2, ledgerCount(t, env.ext))
	assert.EqualValues(t, 20, balanceOf(t, env.ext, 101).Available)
	assert.EqualValues(t, 207, cursorOf(t, env.ext, topupLowWaterKey))

	// 管理员补单按 stardust.exclude_manual_topup 放行。
	off := false
	useConfig(t, stardustConfig(func(s *config.Stardust) {
		s.QuotaPerUnit, s.InviteTopupBps, s.ExcludeManualTopup = 500_000, 5000, &off
	}))
	setCursor(t, env.ext, topupLowWaterKey, 200)
	runTopupScan(ctx)
	l103 = ledgerOf(t, env.ext, 103)
	require.Len(t, l103, 2)
	assert.Equal(t, "T205", l103[1].RefNo)
	assert.EqualValues(t, 3, ledgerCount(t, env.ext), "余额支付与订阅单仍然不返")

	// 迟付回收:游标早已越过的 id、之后才转 success 的订单,按 complete_time 水位捞回来;
	// 订阅单在这一趟同样要过 subscription_orders 那一层。
	seedTopUp(t, env.main, model.TopUp{Id: 150, UserId: 102, Amount: twentyDollars, CreateTime: now - 86400, CompleteTime: now})
	seedTopUp(t, env.main, model.TopUp{Id: 151, UserId: 104, TradeNo: "SUB151", PaymentMethod: "epay", Money: 20, CreateTime: now - 86400, CompleteTime: now})
	require.NoError(t, env.main.Create(&model.SubscriptionOrder{UserId: 104, PlanId: 1, TradeNo: "SUB151", Status: "success"}).Error)
	runTopupScan(ctx)
	l101 = ledgerOf(t, env.ext, 101)
	require.Len(t, l101, 2)
	assert.Equal(t, "T150", l101[1].RefNo)
	assert.EqualValues(t, 4, ledgerCount(t, env.ext), "SUB151 被排除")
}

// TestRunTopupScanPinsCursorBeforeFailedOrder 钉住游标落库值:扫描语句是 `WHERE id > low`,
// 单向不可回头,记账失败的订单越过去就是永久漏发。
func TestRunTopupScanPinsCursorBeforeFailedOrder(t *testing.T) {
	env := newTaskEnv(t, func(s *config.Stardust) { s.InviteTopupBps = 5000 })
	withCompliance(t, true)
	ctx := context.Background()
	seedMainUser(t, env.main, model.User{Id: 111})
	seedMainUser(t, env.main, model.User{Id: 112, InviterId: 111})
	seedMainUser(t, env.main, model.User{Id: 113})
	seedMainUser(t, env.main, model.User{Id: 114, InviterId: 113})
	// 113 的余额已在上界:给他记账会报 ErrOverflow。
	require.NoError(t, env.ext.Create(&Balance{UserId: 113, Available: int64(common.MaxQuota)}).Error)
	setCursor(t, env.ext, topupLowWaterKey, 300)
	seedTopUp(t, env.main, model.TopUp{Id: 301, UserId: 112, Amount: 10_000_000})
	seedTopUp(t, env.main, model.TopUp{Id: 302, UserId: 114, Amount: 10_000_000})
	seedTopUp(t, env.main, model.TopUp{Id: 303, UserId: 112, Amount: 10_000_000})

	runTopupScan(ctx)
	assert.EqualValues(t, 301, cursorOf(t, env.ext, topupLowWaterKey), "游标停在记账失败的 302 之前")
	assert.Len(t, ledgerOf(t, env.ext, 111), 2, "失败的订单不挡住同批后面的订单")
	assert.Empty(t, ledgerOf(t, env.ext, 113))

	require.NoError(t, env.ext.Model(&Balance{}).Where("user_id = ?", 113).Update("available", 0).Error)
	runTopupScan(ctx)
	assert.EqualValues(t, 303, cursorOf(t, env.ext, topupLowWaterKey))
	l113 := ledgerOf(t, env.ext, 113)
	require.Len(t, l113, 1)
	assert.Equal(t, "T302", l113[0].RefNo)
	assert.Len(t, ledgerOf(t, env.ext, 111), 2, "重扫 303 命中幂等键,不双发")
}
