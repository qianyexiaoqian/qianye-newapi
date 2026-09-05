package mall

import (
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// plan_db_test.go —— kind=plan 的跨库两阶段契约(design §6.3),两库真跑。
//
// 成功一支要证明的是"三个库表同一笔钱对得上":扩展库订单 done + 资金单 success +
// 星屑流水,主库 user_subscriptions + subscription_orders(provider=stardust)+ 探针行 +
// 账本日志。失败一支要证明的是唯一的退款判据:Failed 且探针 MainNotApplied 才退。

func TestPlanOrderEndToEnd(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	grantStardust(t, env, testUserId, 100)
	plan := seedPlan(t, env, nil)
	p := seedProduct(t, env, KindPlan, 50, func(p *Product) { p.PlanId = plan.Id; p.Stock = 10 })

	// 商品详情附带 preview:普通套餐是 new。
	status, resp := call(t, r, http.MethodGet, "/api/qy/mall/products/"+p.ProductNo, "", nil)
	requireOK(t, status, resp)
	preview, _ := dataOf(resp)["preview"].(map[string]any)
	require.NotNil(t, preview)
	assert.Equal(t, "new", preview["action"])
	assert.Equal(t, true, preview["seat_available"])
	assert.Equal(t, true, dataOf(resp)["available"])

	// 套餐不验密(订阅落在本人账号上,带不走):不带 pay_password 直接下单。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders", orderBody(p.ProductNo, "crid-plan-1", nil), nil)
	requireOK(t, status, resp)
	d := dataOf(resp)
	orderNo, _ := d["order_no"].(string)
	assert.Equal(t, StatusDone, d["status"])
	assert.Equal(t, false, d["replayed"])
	assert.EqualValues(t, 50, balanceOf(t, env, testUserId))

	// 扩展库:订单 done、回填订阅 id、资金单 success(kind=mall_plan,金额是星屑数)。
	o := loadOrder(t, env, orderNo)
	assert.NotZero(t, o.UserSubscriptionId)
	assert.False(t, o.SubRenewed)
	assert.Equal(t, "SUBSD"+orderNo, o.TradeNo)
	assert.NotEmpty(t, o.FundOrderNo)
	assert.NotZero(t, o.FulfilledAt)
	var fo qymodel.FundOrder
	require.NoError(t, env.ext.Where("order_no = ?", o.FundOrderNo).Take(&fo).Error)
	assert.Equal(t, qymodel.StatusSuccess, fo.Status)
	assert.Equal(t, qymodel.KindMallPlan, fo.Kind)
	assert.EqualValues(t, 50, fo.AmountQuota)
	assert.Equal(t, orderNo, fo.RefId)
	assert.NotZero(t, fo.AfterCommitAt, "提交后收尾必须被认领(账本行只写一次)")
	assert.Equal(t, 1, loadProduct(t, env, p.Id).Sold)

	// 主库:订阅行、订阅订单行(provider=stardust、payload 带 mall_order_no)、探针行、账本日志。
	var sub model.UserSubscription
	require.NoError(t, env.main.Where("user_id = ? AND plan_id = ?", testUserId, plan.Id).Take(&sub).Error)
	assert.Equal(t, sub.Id, o.UserSubscriptionId)
	assert.Equal(t, "active", sub.Status)
	assert.Equal(t, "stardust", sub.Source)
	var so model.SubscriptionOrder
	require.NoError(t, env.main.Where("trade_no = ?", o.TradeNo).Take(&so).Error)
	assert.Equal(t, "stardust", so.PaymentProvider)
	assert.Equal(t, "success", so.Status)
	assert.Equal(t, plan.Id, so.PlanId)
	assert.Contains(t, so.ProviderPayload, "mall_order_no="+orderNo)
	assert.Contains(t, so.ProviderPayload, "stardust=50")
	var probes int64
	require.NoError(t, env.main.Model(&model.QyFundOutbox{}).Where("order_no = ?", o.FundOrderNo).Count(&probes).Error)
	assert.EqualValues(t, 1, probes)
	var logs []model.Log
	require.NoError(t, env.main.Where("user_id = ? AND type = ?", testUserId, model.LogTypeTopup).Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Contains(t, logs[0].Content, orderNo)

	// 幂等重放:拿回原单,三个库一动不动。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders", orderBody(p.ProductNo, "crid-plan-1", nil), nil)
	requireOK(t, status, resp)
	assert.Equal(t, orderNo, dataOf(resp)["order_no"])
	assert.Equal(t, true, dataOf(resp)["replayed"])
	assert.EqualValues(t, 50, balanceOf(t, env, testUserId))
	var subs int64
	require.NoError(t, env.main.Model(&model.UserSubscription{}).Where("user_id = ?", testUserId).Count(&subs).Error)
	assert.EqualValues(t, 1, subs)

	// 详情:时间线里有支付与发放两条,且 user_subscription_id 可见。
	status, resp = call(t, r, http.MethodGet, "/api/qy/mall/orders/"+orderNo, "", nil)
	requireOK(t, status, resp)
	assert.EqualValues(t, sub.Id, dataOf(resp)["user_subscription_id"])
	events, _ := dataOf(resp)["events"].([]any)
	assert.Len(t, events, 2)
}

// 主库事务里被拒(名额)→ 资金单 Failed → 探针说没生效 → 退星屑 → 订单 failed。
//
// 用 hook 变量把"预检放行、事务内拒绝"这个竞态原样造出来:预检(tx == nil)通过,
// CreateUserSubscriptionFromPlanTx 内的强一致判定(tx != nil)拒绝。
func TestPlanOrderRejectedInMainRefunds(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	grantStardust(t, env, testUserId, 100)
	plan := seedPlan(t, env, nil)
	p := seedProduct(t, env, KindPlan, 50, func(p *Product) { p.PlanId = plan.Id; p.Stock = 1 })

	prevGate := model.QyGateSubscriptionSeat
	model.QyGateSubscriptionSeat = func(tx *gorm.DB, plan *model.SubscriptionPlan, userId int, source string, err error) error {
		if err != nil || tx == nil {
			return err
		}
		return errors.New("该套餐全站名额已满(上限 1 人),暂时无法购买")
	}
	t.Cleanup(func() { model.QyGateSubscriptionSeat = prevGate })

	status, resp := call(t, r, http.MethodPost, "/api/qy/mall/orders", orderBody(p.ProductNo, "crid-plan-2", nil), nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_sold_out", codeOf(resp))

	// 星屑原路退回;订单 failed 且带退款流水;库存放回;主库没有任何订阅与订阅订单。
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId))
	assert.Equal(t, []string{"manual", "mall_order", "mall_refund"}, ledgerKinds(t, env, testUserId))
	var o Order
	require.NoError(t, env.ext.Where("user_id = ? AND kind = ?", testUserId, KindPlan).Take(&o).Error)
	assert.Equal(t, StatusFailed, o.Status)
	assert.NotEmpty(t, o.RefundLedgerNo)
	assert.Contains(t, o.FailReason, "名额")
	assert.Equal(t, 0, loadProduct(t, env, p.Id).Sold)
	var fo qymodel.FundOrder
	require.NoError(t, env.ext.Where("order_no = ?", o.FundOrderNo).Take(&fo).Error)
	assert.Equal(t, qymodel.StatusFailed, fo.Status)
	var n int64
	require.NoError(t, env.main.Model(&model.UserSubscription{}).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, env.main.Model(&model.SubscriptionOrder{}).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, env.main.Model(&model.QyFundOutbox{}).Count(&n).Error)
	assert.Zero(t, n, "主库事务回滚,探针行不能留下")

	// 审计:失败那条挂在订单号上。
	rows := auditRows(t, env, "mall.order.create")
	require.Len(t, rows, 1)
	assert.Equal(t, qymodel.ResultFail, rows[0].Result)
	assert.Equal(t, o.OrderNo, rows[0].TraceNo)

	// 再来一次同一个键:原单是 failed,资金单幂等命中 → 回原单(重放),不再扣钱。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders", orderBody(p.ProductNo, "crid-plan-2", nil), nil)
	requireOK(t, status, resp)
	assert.Equal(t, true, dataOf(resp)["replayed"])
	assert.Equal(t, StatusFailed, dataOf(resp)["status"])
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId))
}

// 探针关闭时 paid→failed(退)不可达,每一笔失败都成 held —— 所以干脆不卖。
func TestPlanOrderNeedsOutbox(t *testing.T) {
	off := false
	env := newMallEnv(t, func(c *config.Config) { c.TwoPhase.MainOutboxEnabled = &off })
	r := newRouter()
	grantStardust(t, env, testUserId, 100)
	plan := seedPlan(t, env, nil)
	p := seedProduct(t, env, KindPlan, 50, func(p *Product) { p.PlanId = plan.Id })

	status, resp := call(t, r, http.MethodPost, "/api/qy/mall/orders", orderBody(p.ProductNo, "crid-plan-3", nil), nil)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "qy_ml_plan_needs_outbox", codeOf(resp))
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId))
	var n int64
	require.NoError(t, env.ext.Model(&Order{}).Count(&n).Error)
	assert.Zero(t, n)

	// 商品视图也把它标成不可售;管理端建套餐商品同样 400。
	status, resp = call(t, r, http.MethodGet, "/api/qy/mall/products/"+p.ProductNo, "", nil)
	requireOK(t, status, resp)
	assert.Equal(t, false, dataOf(resp)["available"])
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/products",
		`{"kind":"plan","title":"x","price":1,"stock":-1,"enabled":true,"plan_id":`+itoa(plan.Id)+`}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "qy_ml_plan_needs_outbox", codeOf(resp))
}

// 跨组顶替必须由用户确认过,且主库事务里复核的集合与确认的一致。
func TestPlanOrderSupersedeNeedsConfirmation(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	grantStardust(t, env, testUserId, 100)
	gold := seedPlan(t, env, func(pl *model.SubscriptionPlan) { pl.Title = "gold"; pl.NoQuota = true; pl.UpgradeGroup = "gold" })
	vip := seedPlan(t, env, func(pl *model.SubscriptionPlan) { pl.Title = "vip"; pl.NoQuota = true; pl.UpgradeGroup = "vip" })
	now := common.GetTimestamp()
	require.NoError(t, env.main.Create(&model.UserSubscription{
		UserId: testUserId, PlanId: gold.Id, AmountTotal: 1, NoQuota: true, StartTime: now - 86400,
		EndTime: now + 86400*20, Status: "active", Source: "order", UpgradeGroup: "gold", PrevUserGroup: "default",
	}).Error)
	require.NoError(t, env.main.Model(&model.User{}).Where("id = ?", testUserId).Update("group", "gold").Error)
	p := seedProduct(t, env, KindPlan, 30, func(p *Product) { p.PlanId = vip.Id })

	status, resp := call(t, r, http.MethodGet, "/api/qy/mall/products/"+p.ProductNo, "", nil)
	requireOK(t, status, resp)
	preview, _ := dataOf(resp)["preview"].(map[string]any)
	require.NotNil(t, preview)
	assert.Equal(t, "supersede", preview["action"])
	assert.Equal(t, []any{"gold"}, preview["superseded_groups"])

	// 没确认 → 409,一分钱不扣。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders", orderBody(p.ProductNo, "crid-sup-1", nil), nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_plan_state_changed", codeOf(resp))
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId))
	// 确认的是另一个集合 → 同样 409。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders", orderBody(p.ProductNo, "crid-sup-1",
		map[string]any{"expect_action": "supersede", "expect_superseded": []string{"silver"}}), nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_plan_state_changed", codeOf(resp))

	// 确认无误 → 发放,用户组换到 vip,订单里存的是复核用的集合。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders", orderBody(p.ProductNo, "crid-sup-1",
		map[string]any{"expect_action": "supersede", "expect_superseded": []string{"gold"}}), nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusDone, dataOf(resp)["status"])
	assert.EqualValues(t, 70, balanceOf(t, env, testUserId))
	orderNo, _ := dataOf(resp)["order_no"].(string)
	o := loadOrder(t, env, orderNo)
	assert.Equal(t, "supersede", o.ExpectAction)
	assert.Equal(t, "gold", o.ExpectSuperseded)
	var u model.User
	require.NoError(t, env.main.Where("id = ?", testUserId).Take(&u).Error)
	assert.Equal(t, "vip", u.Group)
}

// supersedeVerdict 与 model.PreviewUserGroupPurchase 的判据必须逐字相同:
// 主库事务里复核的正是它。
func TestSupersedeVerdictMirrorsPreview(t *testing.T) {
	sub := func(group string, end int64) model.UserSubscription {
		return model.UserSubscription{UpgradeGroup: group, EndTime: end}
	}
	cases := []struct {
		name    string
		actives []model.UserSubscription
		target  string
		action  string
		groups  []string
	}{
		{"没有任何升组订阅 → new", nil, "vip", "new", []string{}},
		{"同组永久 → reject", []model.UserSubscription{sub("vip", 0)}, "vip", "reject", []string{}},
		{"同组未到期 → extend", []model.UserSubscription{sub("vip", 99)}, "vip", "extend", []string{}},
		{"别的组 → supersede,集合排序", []model.UserSubscription{sub("gold", 99), sub("bronze", 99)}, "vip",
			"supersede", []string{"bronze", "gold"}},
		{"同组优先于别的组", []model.UserSubscription{sub("gold", 99), sub("vip", 99)}, "vip", "extend", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, groups := supersedeVerdict(tc.actives, tc.target)
			assert.Equal(t, tc.action, action)
			assert.Equal(t, tc.groups, groups)
		})
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
