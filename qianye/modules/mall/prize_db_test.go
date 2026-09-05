package mall

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// prize_db_test.go —— 抽奖中奖生成的商城订单(source=lottery,prize.go)。
//
// 每一条都是对外契约或不丢中奖的不变量:GrantPrizeTx 幂等(同一出款号只建一张单)、
// 码不够时单据停在 paid 并在上传后自动补齐、套餐奖立即发订阅且**不触发星屑返还**、
// 中奖者补填地址的三条边界(非奖品单 403 / 已有地址 409 / 未填地址不能发货)。

// grantPrize 在一个扩展库事务里跑 GrantPrizeTx。
func grantPrize(t *testing.T, env *mallEnv, p *Product, refNo string) *GrantResult {
	t.Helper()
	var out *GrantResult
	require.NoError(t, env.ext.Transaction(func(tx *gorm.DB) error {
		g, err := GrantPrizeTx(tx, GrantPrizeInput{
			UserId: testUserId, ProductNo: p.ProductNo, RefType: "lot_payout", RefNo: refNo, ActNo: "LT-TEST",
		})
		out = g
		return err
	}))
	return out
}

func TestGrantPrizeTxIsIdempotentPerPayout(t *testing.T) {
	env := newMallEnv(t, nil)
	p := seedProduct(t, env, KindPhysical, 30, func(p *Product) { p.Stock = 5; p.Enabled = false })

	first := grantPrize(t, env, p, "PO-1")
	require.False(t, first.Replayed)
	o := loadOrder(t, env, first.Order.OrderNo)
	assert.Equal(t, SourceLottery, o.Source)
	assert.Equal(t, "PO-1", o.RefNo)
	assert.Zero(t, o.Price, "奖品单不扣星屑,价格恒 0")
	assert.Empty(t, o.LedgerNo)
	assert.Equal(t, StatusPaid, o.Status)
	assert.True(t, addressMissing(&o), "实物奖品单在中奖那一刻没有地址")
	assert.Equal(t, 1, loadProduct(t, env, p.Id).Sold, "下架的商品照常建单(奖档已进承诺),库存跟着少一件")

	// 同一出款号重放:拿回原单,不建第二张、不再 sold+1。
	again := grantPrize(t, env, p, "PO-1")
	assert.True(t, again.Replayed)
	assert.Equal(t, first.Order.OrderNo, again.Order.OrderNo)
	var n int64
	require.NoError(t, env.ext.Model(&Order{}).Where("source = ?", SourceLottery).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	assert.Equal(t, 1, loadProduct(t, env, p.Id).Sold)
	assert.Zero(t, balanceOf(t, env, testUserId), "整条路不碰账本")
	assert.Empty(t, ledgerKinds(t, env, testUserId))

	// 用户列表带 source / ref_no / address_missing。
	r := newRouter()
	status, resp := call(t, r, http.MethodGet, "/api/qy/mall/orders?source=lottery", "", nil)
	requireOK(t, status, resp)
	items, _ := dataOf(resp)["items"].([]any)
	require.Len(t, items, 1)
	row, _ := items[0].(map[string]any)
	assert.Equal(t, SourceLottery, row["source"])
	assert.Equal(t, "PO-1", row["ref_no"])
	assert.Equal(t, true, row["address_missing"])
}

func TestCodePrizeAwaitsStockAndFillsOnUpload(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	seedPayPassword(t, env, testUserId, testPayPwd)
	p := seedProduct(t, env, KindCode, 30, nil)

	// 库存为空:不丢中奖,订单停在 paid 并记 await_code 事件。
	g := grantPrize(t, env, p, "PO-code-1")
	require.True(t, g.AwaitingCode)
	o := loadOrder(t, env, g.Order.OrderNo)
	assert.Equal(t, StatusPaid, o.Status)
	assert.Zero(t, o.CodeStockId)
	events, err := loadEvents(env.ext, o.Id)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, ActionGrant, events[0]["action"])
	assert.Equal(t, ActionAwaitCode, events[1]["action"])
	// 重放同样报告"仍在等码"。
	assert.True(t, grantPrize(t, env, p, "PO-code-1").AwaitingCode)

	// 运营上传码:上传接口顺手把等码的单补齐(filled=1),订单 done、码可揭示。
	status, resp := call(t, r, http.MethodPost, "/api/qy/admin/mall/products/"+p.ProductNo+"/codes",
		`{"codes":["PRIZE-CODE-1"]}`, nil)
	requireOK(t, status, resp)
	assert.EqualValues(t, 1, dataOf(resp)["accepted"])
	assert.EqualValues(t, 1, dataOf(resp)["filled"])
	o = loadOrder(t, env, o.OrderNo)
	assert.Equal(t, StatusDone, o.Status)
	assert.NotZero(t, o.CodeStockId)
	status, resp = call(t, r, http.MethodGet, "/api/qy/mall/orders/"+o.OrderNo+"/code", "",
		map[string]string{payHeader: testPayPwd})
	requireOK(t, status, resp)
	assert.Equal(t, "PRIZE-CODE-1", dataOf(resp)["code"])
	assert.Zero(t, balanceOf(t, env, testUserId))

	// 有货时当场发码:订单直接 done。
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/products/"+p.ProductNo+"/codes",
		`{"codes":["PRIZE-CODE-2"]}`, nil)
	requireOK(t, status, resp)
	assert.EqualValues(t, 0, dataOf(resp)["filled"], "没有等码的单就不补")
	g2 := grantPrize(t, env, p, "PO-code-2")
	assert.False(t, g2.AwaitingCode)
	assert.Equal(t, StatusDone, g2.Order.Status)
	assert.NotZero(t, g2.Order.CodeStockId)
}

func TestPrizeOrderAddressEndpoint(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	seedPayPassword(t, env, testUserId, testPayPwd)
	grantStardust(t, env, testUserId, 100)
	p := seedProduct(t, env, KindPhysical, 30, nil)

	// 自购的实物单已有地址:补填入口 403。
	status, resp := call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-self", map[string]any{"pay_password": testPayPwd, "address": "自购地址", "contact": "13800000000"}), nil)
	requireOK(t, status, resp)
	selfNo, _ := dataOf(resp)["order_no"].(string)
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders/"+selfNo+"/address",
		`{"address":"改地址","contact":"x"}`, nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "qy_ml_not_prize_order", codeOf(resp))

	// 奖品单:未填地址时不能发货;补填一次成功;第二次 409;管理端能解出明文。
	g := grantPrize(t, env, p, "PO-phys-1")
	no := g.Order.OrderNo
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+no+"/ship", `{"tracking_no":"SF1"}`, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_address_missing", codeOf(resp))

	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders/"+no+"/address", `{"address":"","contact":"x"}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "qy_ml_address_required", codeOf(resp))

	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders/"+no+"/address",
		`{"address":"中奖地址 1 号","contact":"13900000000"}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, false, dataOf(resp)["address_missing"])
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders/"+no+"/address",
		`{"address":"再改一次","contact":"x"}`, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_address_exists", codeOf(resp))
	// 别人的单:404(不区分"不存在"与"不是你的")。
	status, _ = call(t, r, http.MethodPost, "/api/qy/mall/orders/"+no+"/address",
		`{"address":"x","contact":"y"}`, map[string]string{testUserHeader: "9102"})
	assert.Equal(t, http.StatusNotFound, status)

	rows := auditRows(t, env, "mall.order.address")
	require.Len(t, rows, 4, "成功一条、失败三条(403 / 400 / 409),都留痕")
	for _, row := range rows {
		assert.NotContains(t, row.Reason, "中奖地址", "审计里只记单号,不记地址")
	}

	status, resp = call(t, r, http.MethodGet, "/api/qy/admin/mall/orders/"+no+"/address", "", nil)
	requireOK(t, status, resp)
	assert.Equal(t, "中奖地址 1 号", dataOf(resp)["address"])
	assert.Equal(t, "13900000000", dataOf(resp)["contact"])

	// 发货照旧;标记失败不退款(没有钱可退)但库存放回。
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+no+"/ship", `{"tracking_no":"SF1"}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusShipped, dataOf(resp)["status"])
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+no+"/fail",
		`{"reason":"用户放弃奖品"}`, map[string]string{"X-Test-Role": "100"})
	requireOK(t, status, resp)
	o := loadOrder(t, env, no)
	assert.Equal(t, StatusFailed, o.Status)
	assert.Empty(t, o.RefundLedgerNo, "奖品单没有退款流水")
	assert.EqualValues(t, 70, balanceOf(t, env, testUserId), "自购扣的 30 不受影响,奖品单一分都不退")
}

func TestFulfillPrizePlanGrantsSubscriptionWithoutRebate(t *testing.T) {
	env := newMallEnv(t, nil)
	plan := seedPlan(t, env, nil)
	p := seedProduct(t, env, KindPlan, 50, func(p *Product) { p.PlanId = plan.Id; p.Stock = 3 })

	g := grantPrize(t, env, p, "PO-plan-1")
	require.True(t, g.NeedsPlanFulfill)
	require.Equal(t, StatusPaid, g.Order.Status)
	require.Equal(t, "SUBSD"+g.Order.OrderNo, g.Order.TradeNo)

	require.NoError(t, FulfillPrizePlan(context.Background(), g.Order.OrderNo))
	o := loadOrder(t, env, g.Order.OrderNo)
	assert.Equal(t, StatusDone, o.Status)
	assert.NotZero(t, o.UserSubscriptionId)
	assert.Empty(t, o.FundOrderNo, "奖品套餐不走资金单")
	var sub model.UserSubscription
	require.NoError(t, env.main.Where("user_id = ? AND plan_id = ?", testUserId, plan.Id).Take(&sub).Error)
	assert.Equal(t, "lottery", sub.Source)
	assert.Equal(t, "active", sub.Status)
	var so model.SubscriptionOrder
	require.NoError(t, env.main.Where("trade_no = ?", o.TradeNo).Take(&so).Error)
	assert.Equal(t, "lottery", so.PaymentProvider)
	assert.Contains(t, so.ProviderPayload, "source=lottery")

	// 不返星屑:账本上没有任何行;来源闭集本身也不认 lottery(D-F 的同一道门)。
	assert.Empty(t, ledgerKinds(t, env, testUserId))
	assert.False(t, stardust.DefaultPlanReward(plan.Id).SourceAllowed("lottery"))

	// 重入:主库已发过 → 按 trade_no 回读,一份订阅不发第二次。
	require.NoError(t, FulfillPrizePlan(context.Background(), o.OrderNo))
	var subs int64
	require.NoError(t, env.main.Model(&model.UserSubscription{}).Where("user_id = ?", testUserId).Count(&subs).Error)
	assert.EqualValues(t, 1, subs)

	// 对账兜底:一张过了宽限期仍停在 paid 的奖品套餐单由 runReconcile 补发。
	g2 := grantPrize(t, env, p, "PO-plan-2")
	require.NoError(t, env.ext.Model(&Order{}).Where("id = ?", g2.Order.Id).
		Update("created_at", common.GetTimestamp()-3600).Error)
	runReconcile(context.Background())
	o2 := loadOrder(t, env, g2.Order.OrderNo)
	assert.Equal(t, StatusDone, o2.Status)
	assert.NotZero(t, o2.UserSubscriptionId)
	require.NoError(t, env.main.Model(&model.UserSubscription{}).Where("user_id = ?", testUserId).Count(&subs).Error)
	assert.EqualValues(t, 2, subs, "普通套餐(不改组)每次发放各一行,对账补发的那一份也在")
	assert.Empty(t, ledgerKinds(t, env, testUserId), "对账补发同样不返星屑")

	// 主库业务拒绝(套餐已停用):订单 failed 并写明原因,不会永远停在 paid。
	require.NoError(t, env.main.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Update("enabled", false).Error)
	g3 := grantPrize(t, env, p, "PO-plan-3")
	require.NoError(t, FulfillPrizePlan(context.Background(), g3.Order.OrderNo))
	o3 := loadOrder(t, env, g3.Order.OrderNo)
	assert.Equal(t, StatusFailed, o3.Status)
	assert.NotEmpty(t, o3.FailReason)
	assert.Empty(t, o3.RefundLedgerNo)
}

// 顶替对奖品单不问用户:立即生效,顶掉的分组记进订单与事件。
func TestFulfillPrizePlanSupersedesWithoutConfirmation(t *testing.T) {
	env := newMallEnv(t, nil)
	gold := seedPlan(t, env, func(pl *model.SubscriptionPlan) { pl.Title = "gold"; pl.NoQuota = true; pl.UpgradeGroup = "gold" })
	vip := seedPlan(t, env, func(pl *model.SubscriptionPlan) { pl.Title = "vip"; pl.NoQuota = true; pl.UpgradeGroup = "vip" })
	now := common.GetTimestamp()
	require.NoError(t, env.main.Create(&model.UserSubscription{
		UserId: testUserId, PlanId: gold.Id, AmountTotal: 1, NoQuota: true, StartTime: now - 86400,
		EndTime: now + 86400*20, Status: "active", Source: "order", UpgradeGroup: "gold", PrevUserGroup: "default",
	}).Error)
	require.NoError(t, env.main.Model(&model.User{}).Where("id = ?", testUserId).Update("group", "gold").Error)
	p := seedProduct(t, env, KindPlan, 30, func(p *Product) { p.PlanId = vip.Id })

	g := grantPrize(t, env, p, "PO-sup-1")
	require.NoError(t, FulfillPrizePlan(context.Background(), g.Order.OrderNo))
	o := loadOrder(t, env, g.Order.OrderNo)
	assert.Equal(t, StatusDone, o.Status)
	assert.Equal(t, model.UserGroupPurchaseActionSupersede, o.ExpectAction)
	assert.Equal(t, "gold", o.ExpectSuperseded)
	var u model.User
	require.NoError(t, env.main.Where("id = ?", testUserId).Take(&u).Error)
	assert.Equal(t, "vip", u.Group, "立即生效:分组当场切换")
	events, err := loadEvents(env.ext, o.Id)
	require.NoError(t, err)
	var noted bool
	for _, e := range events {
		note, _ := e["note"].(string)
		if e["action"] == ActionDone && strings.Contains(note, "顶替") && strings.Contains(note, "gold") {
			noted = true
		}
	}
	assert.True(t, noted, "顶掉的分组必须写进事件行 —— 它是用户事后唯一能读到的解释")

	// 商品删除闸门:仍挂在进行中活动奖档上的商品由 lottery 经 ProductReferenced 拒绝。
	prev := ProductReferenced
	ProductReferenced = func(context.Context, string) (bool, error) { return true, nil }
	t.Cleanup(func() { ProductReferenced = prev })
	r := newRouter()
	status, resp := call(t, r, http.MethodDelete, "/api/qy/admin/mall/products/"+p.ProductNo, "", nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_product_referenced", codeOf(resp))
	rows := auditRows(t, env, "mall.product.delete")
	require.Len(t, rows, 1)
	assert.Equal(t, qymodel.ResultFail, rows[0].Result)
}
