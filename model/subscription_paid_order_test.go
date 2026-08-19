package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// subscription_paid_order_test.go —— 已经收到钱的订单必须发得出货,而且发的是
// **下单那一刻**的那份合同。
//
// ═══════════ 被修掉的两个洞 ═══════════
//
// F4 订单永久卡在 pending:CompleteSubscriptionOrder 在事务里调
//    CreateUserSubscriptionFromPlanTx,而后者的 MaxPurchasePerUser 检查与
//    「你已经永久拥有该用户组」拒绝对 source=="order" 一视同仁地返回错误 →
//    整个事务回滚 → 订单停在 pending、订阅不发、top_ups 一行都没有。
//    没有定时任务会关掉它,管理端也没有补单或退款接口(AdminBindSubscription
//    走同一个函数,补发撞同一堵墙),只能手工改库。
//    名额闸门早就为这一档写了「source==order 永不拒绝」,这两条没有跟上。
//
// F5 价与货脱钩:回调按 order.PlanId **现读**套餐,只有 order.Money 是快照。
//    实测「下单时 1 元 / 1000 额度」的订单在套餐被就地改成
//    「199 元 / 9,000,000 额度 / vip」之后回调 → 用户花 1 元拿到 9,000,000。

func usePaidOrderDB(t *testing.T) {
	t.Helper()
	dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "paidorder.db")) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	require.NoError(t, gdb.AutoMigrate(
		&User{}, &Log{}, &TopUp{},
		&SubscriptionPlan{}, &UserSubscription{}, &SubscriptionOrder{},
	))
	prev, prevLog := DB, LOG_DB
	DB, LOG_DB = gdb, gdb
	t.Cleanup(func() {
		DB, LOG_DB = prev, prevLog
		_ = sqlDB.Close()
	})
}

func seedPaidOrderPlan(t *testing.T, plan *SubscriptionPlan) *SubscriptionPlan {
	t.Helper()
	require.NoError(t, DB.Create(plan).Error)
	InvalidateSubscriptionPlanCache(plan.Id)
	t.Cleanup(func() { InvalidateSubscriptionPlanCache(plan.Id) })
	return plan
}

func seedPendingOrder(t *testing.T, userId int, plan *SubscriptionPlan, tradeNo string, withSnapshot bool) *SubscriptionOrder {
	t.Helper()
	order := &SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      common.GetTimestamp(),
	}
	if withSnapshot {
		order.PlanSnapshot = SubscriptionPlanSnapshot(plan)
	}
	require.NoError(t, DB.Create(order).Error)
	return order
}

func orderByTradeNo(t *testing.T, tradeNo string) *SubscriptionOrder {
	t.Helper()
	var order SubscriptionOrder
	require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&order).Error)
	return &order
}

func countRows(t *testing.T, model any, query string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, DB.Model(model).Where(query, args...).Count(&n).Error)
	return n
}

// TestPaidOrderCompletesDespitePurchaseLimit 限购撞上限时,已付款的订单照样成交。
func TestPaidOrderCompletesDespitePurchaseLimit(t *testing.T) {
	usePaidOrderDB(t)
	const userId = 78_001
	seedGroupUser(t, userId, "default")

	plan := seedPaidOrderPlan(t, &SubscriptionPlan{
		Id: 78_101, Title: "限购一份", Enabled: true, PriceAmount: 30,
		TotalAmount: 20_000, MaxPurchasePerUser: 1,
		DurationUnit: SubscriptionDurationCustom, CustomSeconds: 30 * 24 * 3600,
	})
	// 第一份已经到手(等价于另一个收银台先完成、或兑换码/管理员先发过)。
	buyPlan(t, userId, plan)

	seedPendingOrder(t, userId, plan, "SUBLIMIT001", true)
	require.NoError(t, CompleteSubscriptionOrder("SUBLIMIT001", "", PaymentProviderEpay, ""))

	assert.Equal(t, common.TopUpStatusSuccess, orderByTradeNo(t, "SUBLIMIT001").Status,
		"钱已经收了,订单绝不能停在 pending —— 没有任何路径能把它捞回来")
	assert.Equal(t, int64(2), countRows(t, &UserSubscription{}, "user_id = ?", userId),
		"付了第二份就该拿到第二份")
	assert.Equal(t, int64(1), countRows(t, &TopUp{}, "trade_no = ?", "SUBLIMIT001"),
		"收入必须落 top_ups,否则这笔付款在站内完全不可见")
}

// TestPaidOrderCompletesWhenGroupAlreadyOwnedPermanently 永久同组重复购买那一档。
//
// 这一档最现实(不需要并发):用户的永久 vip 来自管理员赠送或兑换码,随后自己
// 走网关买一件同组纯商品 → 100% 撞上「你已经永久拥有该用户组」。
func TestPaidOrderCompletesWhenGroupAlreadyOwnedPermanently(t *testing.T) {
	usePaidOrderDB(t)
	const userId = 78_002
	seedGroupUser(t, userId, "default")

	permanent := seedPaidOrderPlan(t, &SubscriptionPlan{
		Id: 78_201, Title: "永久VIP", Enabled: true, NoQuota: true, UpgradeGroup: "vip",
		DurationUnit: SubscriptionDurationPermanent,
	})
	buyPlan(t, userId, permanent)

	monthly := seedPaidOrderPlan(t, &SubscriptionPlan{
		Id: 78_202, Title: "月付VIP", Enabled: true, NoQuota: true, UpgradeGroup: "vip",
		PriceAmount: 68, DurationUnit: SubscriptionDurationCustom, CustomSeconds: 30 * 24 * 3600,
	})
	seedPendingOrder(t, userId, monthly, "SUBPERM001", true)

	require.NoError(t, CompleteSubscriptionOrder("SUBPERM001", "", PaymentProviderEpay, ""))
	assert.Equal(t, common.TopUpStatusSuccess, orderByTradeNo(t, "SUBPERM001").Status)
	assert.Equal(t, int64(1), countRows(t, &TopUp{}, "trade_no = ?", "SUBPERM001"),
		"付款必须在站内可见,运营才有东西可退")
}

// TestUnpaidPathsStillRejectPurchaseLimit 未收款的三条路必须照旧被拒。
//
// 放行只给"钱已经收了"那一档。余额购买/兑换码/管理员发放回滚是安全的
// (扣款在同一个事务里),把限购一起放开等于删掉这个功能。
func TestUnpaidPathsStillRejectPurchaseLimit(t *testing.T) {
	usePaidOrderDB(t)
	const userId = 78_003
	seedGroupUser(t, userId, "default")

	plan := seedPaidOrderPlan(t, &SubscriptionPlan{
		Id: 78_301, Title: "限购一份", Enabled: true, PriceAmount: 30,
		TotalAmount: 20_000, MaxPurchasePerUser: 1,
		DurationUnit: SubscriptionDurationCustom, CustomSeconds: 30 * 24 * 3600,
	})
	buyPlan(t, userId, plan)

	for _, source := range []string{PaymentMethodBalance, RedemptionSubscriptionSource, "admin"} {
		err := DB.Transaction(func(tx *gorm.DB) error {
			_, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, source)
			return err
		})
		require.Errorf(t, err, "source=%s 未收款,限购必须照旧生效", source)
		assert.Contains(t, err.Error(), "购买上限")
	}
	assert.Equal(t, int64(1), countRows(t, &UserSubscription{}, "user_id = ?", userId))
}

// TestOrderShipsThePlanSnapshotNotTheLivePlan 发的是下单那一刻的合同。
func TestOrderShipsThePlanSnapshotNotTheLivePlan(t *testing.T) {
	usePaidOrderDB(t)
	const userId = 78_004
	seedGroupUser(t, userId, "default")

	plan := seedPaidOrderPlan(t, &SubscriptionPlan{
		Id: 78_401, Title: "小额度", Enabled: true, PriceAmount: 1, TotalAmount: 1_000,
		DurationUnit: SubscriptionDurationCustom, CustomSeconds: 30 * 24 * 3600,
	})
	seedPendingOrder(t, userId, plan, "SUBSNAP001", true)

	// 订单还挂着的时候,运营就地把这张套餐改成一个完全不同的商品。
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", plan.Id).
		Updates(map[string]any{
			"price_amount": 199, "total_amount": 9_000_000,
			"upgrade_group": "vip", "enabled": false,
		}).Error)
	InvalidateSubscriptionPlanCache(plan.Id)

	require.NoError(t, CompleteSubscriptionOrder("SUBSNAP001", "", PaymentProviderEpay, ""))

	var sub UserSubscription
	require.NoError(t, DB.Where("user_id = ?", userId).First(&sub).Error)
	assert.Equal(t, int64(1_000), sub.AmountTotal,
		"用户付的是 1 元那份合同,拿到的必须也是那一份")
	assert.Empty(t, sub.UpgradeGroup, "下单时这张套餐不改用户组,回调也不该改")
	assert.Equal(t, "default", userGroupOf(t, userId))
}

// TestOrderWithoutSnapshotFallsBackToTheLivePlan 存量订单(本列上线之前创建的)
// 必须照旧能成交。
//
// 空串是"没有快照",不是"零额度套餐" —— 把它当合同用会让所有存量 pending
// 订单发出一份空订阅。
func TestOrderWithoutSnapshotFallsBackToTheLivePlan(t *testing.T) {
	usePaidOrderDB(t)
	const userId = 78_005
	seedGroupUser(t, userId, "default")

	plan := seedPaidOrderPlan(t, &SubscriptionPlan{
		Id: 78_501, Title: "存量单", Enabled: true, PriceAmount: 5, TotalAmount: 7_777,
		DurationUnit: SubscriptionDurationCustom, CustomSeconds: 30 * 24 * 3600,
	})
	order := seedPendingOrder(t, userId, plan, "SUBLEGACY001", false)
	require.Empty(t, order.PlanSnapshot)

	require.NoError(t, CompleteSubscriptionOrder("SUBLEGACY001", "", PaymentProviderEpay, ""))
	var sub UserSubscription
	require.NoError(t, DB.Where("user_id = ?", userId).First(&sub).Error)
	assert.Equal(t, int64(7_777), sub.AmountTotal)
}

// TestStalePendingOrdersAreSweptButStillShippable 超龄 pending 订单被打成 expired,
// 而真的付了钱的回调照样发货。
//
// 两件事必须一起断言:只扫不发货 = 把"没人关单"换成"收了钱不发货",
// 那比原来的问题更糟。
func TestStalePendingOrdersAreSweptButStillShippable(t *testing.T) {
	usePaidOrderDB(t)
	const userId = 78_006
	seedGroupUser(t, userId, "default")

	plan := seedPaidOrderPlan(t, &SubscriptionPlan{
		Id: 78_601, Title: "陈年单", Enabled: true, PriceAmount: 9, TotalAmount: 3_333,
		DurationUnit: SubscriptionDurationCustom, CustomSeconds: 30 * 24 * 3600,
	})
	stale := seedPendingOrder(t, userId, plan, "SUBSTALE001", true)
	seedPendingOrder(t, userId, plan, "SUBFRESH001", true)
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("id = ?", stale.Id).
		Update("create_time", common.GetTimestamp()-90*24*3600).Error)

	n, err := ExpireStalePendingSubscriptionOrders(72*3600, 200)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, common.TopUpStatusExpired, orderByTradeNo(t, "SUBSTALE001").Status)
	assert.Equal(t, common.TopUpStatusPending, orderByTradeNo(t, "SUBFRESH001").Status,
		"没超龄的一分都不许碰")

	require.NoError(t, CompleteSubscriptionOrder("SUBSTALE001", "", PaymentProviderEpay, ""),
		"扫描只是运营视图;钱真的到了就必须发货")
	assert.Equal(t, common.TopUpStatusSuccess, orderByTradeNo(t, "SUBSTALE001").Status)
	assert.Equal(t, int64(1), countRows(t, &UserSubscription{}, "user_id = ?", userId))
}

// TestStaleSweepIsOptOutAndLeavesTerminalOrdersAlone TTL <= 0 关闭清扫;
// 已成交/已过期的订单不在扫描范围内。
func TestStaleSweepIsOptOutAndLeavesTerminalOrdersAlone(t *testing.T) {
	usePaidOrderDB(t)
	const userId = 78_007
	seedGroupUser(t, userId, "default")

	plan := seedPaidOrderPlan(t, &SubscriptionPlan{
		Id: 78_701, Title: "陈年单", Enabled: true, PriceAmount: 9, TotalAmount: 3_333,
		DurationUnit: SubscriptionDurationCustom, CustomSeconds: 30 * 24 * 3600,
	})
	stale := seedPendingOrder(t, userId, plan, "SUBOPT001", true)
	done := seedPendingOrder(t, userId, plan, "SUBDONE001", true)
	require.NoError(t, DB.Model(&SubscriptionOrder{}).
		Where("id IN ?", []int{stale.Id, done.Id}).
		Update("create_time", common.GetTimestamp()-90*24*3600).Error)
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("id = ?", done.Id).
		Update("status", common.TopUpStatusSuccess).Error)

	n, err := ExpireStalePendingSubscriptionOrders(0, 200)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "TTL <= 0 必须整段关闭清扫")
	assert.Equal(t, common.TopUpStatusPending, orderByTradeNo(t, "SUBOPT001").Status)

	n, err = ExpireStalePendingSubscriptionOrders(72*3600, 200)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, common.TopUpStatusSuccess, orderByTradeNo(t, "SUBDONE001").Status,
		"已成交的订单不许被扫描碰到")
}
