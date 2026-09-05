package mall

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconcile_db_test.go —— mall.reconcile 与 mall.prune 的收敛契约(design §6.3 / §6.4)。
//
// 对账处置的四种分支各钉一条:Success 推 done、Failed+NotApplied 退、Failed+Applied 挂起、
// 未定局挂起;宽限期内的单一律不碰。地址清理钉"到期才清、开着的不清、清了就解不开"。

// seedPlanOrder 落一张停在 paid 的套餐订单及其资金单(状态由调用方给)。
func seedPlanOrder(t *testing.T, env *mallEnv, p *Product, fundStatus int8, createdAt int64) (Order, qymodel.FundOrder) {
	t.Helper()
	o := Order{
		OrderNo: newOrderNo(), UserId: testUserId, ProductId: p.Id, ProductNo: p.ProductNo,
		Kind: KindPlan, Title: p.Title, Price: p.Price, Status: StatusPaid,
		IdemKey: idemKeyOf(testUserId, "seed-"+common.GetUUID()[:8]), LedgerNo: "SD-seed",
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	o.TradeNo = "SUBSD" + o.OrderNo
	fo := qymodel.FundOrder{
		OrderNo: "MP-" + o.OrderNo, Kind: qymodel.KindMallPlan, Status: fundStatus,
		IdemScope: idemScope, IdemKey: o.IdemKey, UserId: testUserId, AmountQuota: p.Price,
		RefType: refTypeMallOrder, RefId: o.OrderNo, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	o.FundOrderNo = fo.OrderNo
	require.NoError(t, env.ext.Create(&o).Error)
	require.NoError(t, env.ext.Create(&fo).Error)
	return o, fo
}

func TestReconcileFinalizesSuccessfulPaidOrder(t *testing.T) {
	env := newMallEnv(t, nil)
	p := seedProduct(t, env, KindPlan, 50, func(p *Product) { p.PlanId = 1; p.Sold = 1 })
	old := common.GetTimestamp() - 3600
	o, _ := seedPlanOrder(t, env, p, qymodel.StatusSuccess, old)
	require.NoError(t, env.main.Create(&model.SubscriptionOrder{
		UserId: testUserId, PlanId: 1, TradeNo: o.TradeNo, PaymentMethod: "stardust", PaymentProvider: "stardust",
		Status: "success", CreateTime: old, CompleteTime: old,
		ProviderPayload: "mall_order_no=" + o.OrderNo + ";stardust=50;user_subscription_id=77;renewed=1",
	}).Error)
	// 一张还在宽限期内的单:对账不该碰它。
	fresh, _ := seedPlanOrder(t, env, p, qymodel.StatusSuccess, common.GetTimestamp())

	runReconcile(context.Background())

	got := loadOrder(t, env, o.OrderNo)
	assert.Equal(t, StatusDone, got.Status)
	assert.Equal(t, 77, got.UserSubscriptionId)
	assert.True(t, got.SubRenewed)
	assert.NotZero(t, got.FulfilledAt)
	assert.Equal(t, StatusPaid, loadOrder(t, env, fresh.OrderNo).Status)
	events := make([]OrderEvent, 0, 2)
	require.NoError(t, env.ext.Where("order_id = ?", got.Id).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, ActionDone, events[0].Action)

	// 再跑一轮:幂等,不会写第二条 done 事件。
	runReconcile(context.Background())
	require.NoError(t, env.ext.Where("order_id = ?", got.Id).Find(&events).Error)
	assert.Len(t, events, 1)
}

func TestReconcileFailedOrderRefundsOnlyWhenMainNotApplied(t *testing.T) {
	env := newMallEnv(t, nil)
	grantStardust(t, env, testUserId, 1) // 退款前余额 1,退一笔 50 之后应是 51
	p := seedProduct(t, env, KindPlan, 50, func(p *Product) { p.PlanId = 1; p.Sold = 2 })
	old := common.GetTimestamp() - 3600
	notApplied, _ := seedPlanOrder(t, env, p, qymodel.StatusFailed, old)
	applied, appliedFO := seedPlanOrder(t, env, p, qymodel.StatusFailed, old)
	// 第二张的探针行在:主库其实动过,绝不能退。
	require.NoError(t, env.main.Create(&model.QyFundOutbox{
		OrderNo: appliedFO.OrderNo, Kind: qymodel.KindMallPlan, UserId: testUserId, Amount: 50, CreatedAt: old,
	}).Error)

	runReconcile(context.Background())

	got := loadOrder(t, env, notApplied.OrderNo)
	assert.Equal(t, StatusFailed, got.Status)
	assert.NotEmpty(t, got.RefundLedgerNo)
	assert.EqualValues(t, 51, balanceOf(t, env, testUserId))

	held := loadOrder(t, env, applied.OrderNo)
	assert.Equal(t, StatusHeld, held.Status)
	assert.Empty(t, held.RefundLedgerNo)
	assert.EqualValues(t, 51, balanceOf(t, env, testUserId), "探针说已生效的单一分都不退")
	assert.Equal(t, 1, loadProduct(t, env, p.Id).Sold, "只有真退掉的那一张把库存放回")

	// 挂起的单不再被重复退;再跑一轮余额不变。
	runReconcile(context.Background())
	assert.EqualValues(t, 51, balanceOf(t, env, testUserId))
	assert.Equal(t, []string{"manual", "mall_refund"}, ledgerKinds(t, env, testUserId))
}

func TestReconcileHoldsUnsettledOrders(t *testing.T) {
	env := newMallEnv(t, nil)
	p := seedProduct(t, env, KindPlan, 50, func(p *Product) { p.PlanId = 1 })
	old := common.GetTimestamp() - 3600
	pending, _ := seedPlanOrder(t, env, p, qymodel.StatusPending, old)
	uncertain, _ := seedPlanOrder(t, env, p, qymodel.StatusUncertain, old)

	runReconcile(context.Background())

	for _, no := range []string{pending.OrderNo, uncertain.OrderNo} {
		got := loadOrder(t, env, no)
		assert.Equal(t, StatusHeld, got.Status)
		assert.Empty(t, got.RefundLedgerNo)
	}
	assert.Zero(t, balanceOf(t, env, testUserId), "未定局的单绝不退")
}

func TestPruneClearsExpiredAddressesOnly(t *testing.T) {
	env := newMallEnv(t, nil)
	old := common.GetTimestamp() - 200*86400
	seed := func(status string, updatedAt int64) Order {
		o := Order{
			OrderNo: newOrderNo(), UserId: testUserId, ProductId: 1, Kind: KindPhysical, Title: "t", Price: 1,
			Status: status, IdemKey: idemKeyOf(testUserId, "pr-"+common.GetUUID()[:8]),
			CreatedAt: updatedAt, UpdatedAt: updatedAt,
		}
		require.NoError(t, sealAddress(&o, "地址", "电话"))
		require.NoError(t, env.ext.Create(&o).Error)
		return o
	}
	expired := seed(StatusDone, old)
	stillOpen := seed(StatusShipped, old)
	recent := seed(StatusCancelled, common.GetTimestamp())

	runPrune(context.Background())

	got := loadOrder(t, env, expired.OrderNo)
	assert.NotZero(t, got.AddressPrunedAt)
	_, _, err := openAddress(&got)
	assert.ErrorIs(t, err, errAddressPruned)

	for _, no := range []string{stillOpen.OrderNo, recent.OrderNo} {
		g := loadOrder(t, env, no)
		assert.Zero(t, g.AddressPrunedAt)
		addr, _, err := openAddress(&g)
		require.NoError(t, err)
		assert.Equal(t, "地址", addr)
	}
}
