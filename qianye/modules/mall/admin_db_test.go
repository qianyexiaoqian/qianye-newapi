package mall

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// admin_db_test.go —— 管理端接口的契约(design §6.4 / 契约 §5),真路由真库。
//
// 钉的是:kind 不可改、码上传的四种拒绝(含"本站兑换码不可上架")、删商品的闸门与
// 清理范围、发货 / 标失败 / 撤码三条状态机与退款至多一次、地址揭示写审计、
// root 裁决的两个结论各自落成什么、以及 role<100 进不了裁决端点。

func TestAdminProductLifecycle(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()

	// 创建:kind 非法 / 价格 0 / 库存 -2 都拒;正常创建返回 product_no。
	for _, body := range []string{
		`{"kind":"gift","title":"x","price":1,"stock":-1,"enabled":true}`,
		`{"kind":"code","title":"x","price":0,"stock":-1,"enabled":true}`,
		`{"kind":"physical","title":"x","price":1,"stock":-2,"enabled":true}`,
		`{"kind":"physical","title":"","price":1,"stock":-1,"enabled":true}`,
	} {
		status, resp := call(t, r, http.MethodPost, "/api/qy/admin/mall/products", body, nil)
		assert.Equalf(t, http.StatusBadRequest, status, "body=%s resp=%v", body, resp)
	}
	status, resp := call(t, r, http.MethodPost, "/api/qy/admin/mall/products",
		`{"kind":"code","title":"卡密","description":"d","price":30,"stock":5,"per_user_limit":1,"enabled":true,"sort_order":2}`, nil)
	requireOK(t, status, resp)
	productNo, _ := dataOf(resp)["product_no"].(string)
	require.NotEmpty(t, productNo)
	assert.EqualValues(t, 0, dataOf(resp)["stock"], "code 类的库存由库存表算,创建时是 0 枚")
	assert.Equal(t, false, dataOf(resp)["available"])

	// 修改:kind 不可改;其余字段(含 enabled=false 这种零值)要真的写进去。
	status, resp = call(t, r, http.MethodPut, "/api/qy/admin/mall/products/"+productNo,
		`{"kind":"physical","title":"卡密2","price":40,"stock":-1,"enabled":false}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)
	status, resp = call(t, r, http.MethodPut, "/api/qy/admin/mall/products/"+productNo,
		`{"title":"卡密2","price":40,"stock":-1,"per_user_limit":0,"enabled":false,"sort_order":0}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, "卡密2", dataOf(resp)["title"])
	assert.Equal(t, false, dataOf(resp)["enabled"])
	assert.EqualValues(t, 40, dataOf(resp)["price"])
	// 下架的商品对用户就是不存在。
	status, _ = call(t, r, http.MethodGet, "/api/qy/mall/products/"+productNo, "", nil)
	assert.Equal(t, http.StatusNotFound, status)

	// 上传兑换码:空 / 超长 / 批内重复 / 本站兑换码 各拒一条,其余入库为密文。
	require.NoError(t, env.main.Create(&model.Redemption{Id: 1, UserId: testAdminId, Key: strings.Repeat("k", 32), Name: "site", Quota: 100}).Error)
	codes := []string{"GOOD-1", "", strings.Repeat("x", 129), "GOOD-1", strings.Repeat("k", 32), "GOOD-2"}
	body, _ := json.Marshal(map[string]any{"codes": codes})
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/products/"+productNo+"/codes", string(body), nil)
	requireOK(t, status, resp)
	assert.EqualValues(t, 2, dataOf(resp)["accepted"])
	rejected, _ := dataOf(resp)["rejected"].([]any)
	require.Len(t, rejected, 4)
	reasons := make([]string, 0, 4)
	for _, rj := range rejected {
		m := rj.(map[string]any)
		reasons = append(reasons, m["reason"].(string))
	}
	assert.Equal(t, []string{"empty", "too_long", "duplicate", "本站兑换码不可上架"}, reasons)
	var stock []CodeStock
	require.NoError(t, env.ext.Order("id asc").Find(&stock).Error)
	require.Len(t, stock, 2)
	for _, row := range stock {
		assert.NotContains(t, string(row.CodeCipher), "GOOD", "库里不能有明文")
		assert.Equal(t, 1, row.KeyVersion)
	}
	plain, err := openCode(&stock[1], productNo)
	require.NoError(t, err)
	assert.Equal(t, "GOOD-2", plain)
	uploads := auditRows(t, env, "mall.codes.upload")
	require.Len(t, uploads, 1)
	assert.NotContains(t, uploads[0].Reason, "GOOD", "审计里一个字符的码都不能有")
	assert.EqualValues(t, 2, uploads[0].AmountQuota)

	// 非 code 商品不收码。
	phys := seedProduct(t, env, KindPhysical, 10, nil)
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/products/"+phys.ProductNo+"/codes", `{"codes":["a"]}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)

	// 删除:有未完结订单 → 409;删掉之后 unused 的码一起没了,已发的留着。
	var p Product
	require.NoError(t, env.ext.Where("product_no = ?", productNo).Take(&p).Error)
	require.NoError(t, env.ext.Model(&CodeStock{}).Where("id = ?", stock[0].Id).
		Updates(map[string]any{"status": CodeIssued, "order_id": 1}).Error)
	open := Order{OrderNo: newOrderNo(), UserId: testUserId, ProductId: phys.Id, ProductNo: phys.ProductNo,
		Kind: KindPhysical, Title: "t", Price: 1, Status: StatusPaid, IdemKey: "x:1", CreatedAt: 1, UpdatedAt: 1}
	require.NoError(t, env.ext.Create(&open).Error)
	status, resp = call(t, r, http.MethodDelete, "/api/qy/admin/mall/products/"+phys.ProductNo, "", nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_has_open_orders", codeOf(resp))

	status, resp = call(t, r, http.MethodDelete, "/api/qy/admin/mall/products/"+productNo, "", nil)
	requireOK(t, status, resp)
	var n int64
	require.NoError(t, env.ext.Model(&Product{}).Where("id = ?", p.Id).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, env.ext.Model(&CodeStock{}).Where("product_id = ?", p.Id).Count(&n).Error)
	assert.EqualValues(t, 1, n, "已发出的码是证据,不随商品删除")
	status, _ = call(t, r, http.MethodDelete, "/api/qy/admin/mall/products/"+productNo, "", nil)
	assert.Equal(t, http.StatusNotFound, status)

	// 商品数上限:MaxProducts=1 时第二件拒(换一个只允许一件商品的环境)。
	newMallEnv(t, func(c *config.Config) { c.Mall.MaxProducts = 1 })
	r2 := newRouter()
	status, _ = call(t, r2, http.MethodPost, "/api/qy/admin/mall/products", `{"kind":"physical","title":"a","price":1,"stock":-1,"enabled":true}`, nil)
	requireOK(t, status, nil)
	status, resp = call(t, r2, http.MethodPost, "/api/qy/admin/mall/products", `{"kind":"physical","title":"b","price":1,"stock":-1,"enabled":true}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "qy_ml_max_products", codeOf(resp))
}

// seedPhysicalOrder 直接落一张 paid 的实物订单(地址密文齐全),并把星屑账做平。
func seedPhysicalOrder(t *testing.T, env *mallEnv, p *Product) Order {
	t.Helper()
	now := common.GetTimestamp()
	o := Order{
		OrderNo: newOrderNo(), UserId: testUserId, ProductId: p.Id, ProductNo: p.ProductNo,
		Kind: KindPhysical, Title: p.Title, Price: p.Price, Status: StatusPaid,
		IdemKey: idemKeyOf(testUserId, "ph-"+common.GetUUID()[:8]), CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, sealAddress(&o, "北京市 某街 2 号", "contact@example.com"))
	require.NoError(t, env.ext.Create(&o).Error)
	require.NoError(t, env.ext.Model(&Product{}).Where("id = ?", p.Id).Update("sold", p.Sold+1).Error)
	p.Sold++
	return o
}

func TestAdminShipFailAndAddress(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	grantStardust(t, env, testUserId, 5)
	p := seedProduct(t, env, KindPhysical, 40, nil)
	a := seedPhysicalOrder(t, env, p)
	b := seedPhysicalOrder(t, env, p)

	// 发货:单号必填;发货后 shipped;done=true 再一步到 done。
	status, resp := call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+a.OrderNo+"/ship", `{"tracking_no":"  "}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+a.OrderNo+"/ship", `{"tracking_no":"SF123","ship_note":"顺丰"}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusShipped, dataOf(resp)["status"])
	assert.Equal(t, "SF123", dataOf(resp)["tracking_no"])
	// shipped 之后用户不能再取消。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders/"+a.OrderNo+"/cancel", "", nil)
	assert.Equal(t, http.StatusConflict, status)
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+a.OrderNo+"/ship", `{"done":true}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusDone, dataOf(resp)["status"])
	assert.NotZero(t, loadOrder(t, env, a.OrderNo).FulfilledAt)
	// done 之后再标失败 → 409。
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+a.OrderNo+"/fail", `{"reason":"迟了"}`, nil)
	assert.Equal(t, http.StatusConflict, status)

	// 标失败:事由必填;退 40;第二次 409 不再退。
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+b.OrderNo+"/fail", `{"reason":" "}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+b.OrderNo+"/fail", `{"reason":"缺货"}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusFailed, dataOf(resp)["status"])
	assert.Equal(t, "缺货", dataOf(resp)["fail_reason"])
	assert.EqualValues(t, 45, balanceOf(t, env, testUserId))
	assert.Equal(t, 1, loadProduct(t, env, p.Id).Sold)
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+b.OrderNo+"/fail", `{"reason":"缺货"}`, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.EqualValues(t, 45, balanceOf(t, env, testUserId))
	fails := auditRows(t, env, "mall.order.fail")
	require.GreaterOrEqual(t, len(fails), 2)
	assert.Equal(t, testUserId, fails[0].TargetUserId)

	// 地址揭示:明文只走这里,每次都写审计;非实物 409。
	status, resp = call(t, r, http.MethodGet, "/api/qy/admin/mall/orders/"+a.OrderNo+"/address", "", nil)
	requireOK(t, status, resp)
	assert.Equal(t, "北京市 某街 2 号", dataOf(resp)["address"])
	assert.Equal(t, "contact@example.com", dataOf(resp)["contact"])
	reveals := auditRows(t, env, "mall.address.reveal")
	require.Len(t, reveals, 1)
	assert.Equal(t, a.OrderNo, reveals[0].TraceNo)
	assert.NotContains(t, reveals[0].Reason+reveals[0].AfterSnap, "北京", "审计里不许有明文")

	// 管理端列表带用户名与资金单号列。
	status, resp = call(t, r, http.MethodGet, "/api/qy/admin/mall/orders?status=done", "", nil)
	requireOK(t, status, resp)
	items, _ := dataOf(resp)["items"].([]any)
	require.Len(t, items, 1)
	row := items[0].(map[string]any)
	assert.Equal(t, "buyer", row["username"])
	assert.EqualValues(t, testUserId, row["user_id"])
	_, hasAddr := row["address"]
	assert.False(t, hasAddr)
}

func TestAdminRevokeCodeRefundsAndBurnsTheCode(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	seedPayPassword(t, env, testUserId, testPayPwd)
	grantStardust(t, env, testUserId, 30)
	p := seedProduct(t, env, KindCode, 30, nil)
	seedCodes(t, env, p, "ONLY-ONE")
	status, resp := call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-rv", map[string]any{"pay_password": testPayPwd}), nil)
	requireOK(t, status, resp)
	orderNo, _ := dataOf(resp)["order_no"].(string)

	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+orderNo+"/revoke-code", `{"reason":"卡密作废"}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusRevoked, dataOf(resp)["status"])
	assert.EqualValues(t, 30, balanceOf(t, env, testUserId))
	var stock CodeStock
	require.NoError(t, env.ext.Where("product_id = ?", p.Id).Take(&stock).Error)
	assert.Equal(t, CodeRevoked, stock.Status, "码标 revoked,不回到 unused")
	assert.NotEmpty(t, stock.CodeCipher, "撤回不清空密文:它是争议时唯一的证据")
	assert.Equal(t, 1, loadProduct(t, env, p.Id).Sold, "兑换码撤回不放回库存")

	// 撤回后:用户揭示 → 409;再撤一次 → 409 且不再退。
	status, resp = call(t, r, http.MethodGet, "/api/qy/mall/orders/"+orderNo+"/code", "", map[string]string{payHeader: testPayPwd})
	assert.Equal(t, http.StatusConflict, status)
	assert.Nil(t, dataOf(resp)["code"])
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+orderNo+"/revoke-code", `{"reason":"再撤"}`, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.EqualValues(t, 30, balanceOf(t, env, testUserId))
	assert.Equal(t, []string{"manual", "mall_order", "mall_refund"}, ledgerKinds(t, env, testUserId))
}

func TestAdminAdjudicate(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	grantStardust(t, env, testUserId, 1)
	p := seedProduct(t, env, KindPlan, 50, func(p *Product) { p.PlanId = 1; p.Sold = 2 })
	old := common.GetTimestamp() - 3600

	// 两张 held 单:资金单都 failed;A 的探针行在(主库其实发了),B 的不在。
	a, aFO := seedPlanOrder(t, env, p, qymodel.StatusFailed, old)
	b, _ := seedPlanOrder(t, env, p, qymodel.StatusFailed, old)
	for _, no := range []string{a.OrderNo, b.OrderNo} {
		require.NoError(t, env.ext.Model(&Order{}).Where("order_no = ?", no).Update("status", StatusHeld).Error)
	}
	require.NoError(t, env.main.Create(&model.QyFundOutbox{OrderNo: aFO.OrderNo, Kind: qymodel.KindMallPlan, UserId: testUserId, Amount: 50, CreatedAt: old}).Error)
	require.NoError(t, env.main.Create(&model.SubscriptionOrder{
		UserId: testUserId, PlanId: 1, TradeNo: a.TradeNo, PaymentProvider: "stardust", Status: "success",
		CreateTime: old, CompleteTime: old,
		ProviderPayload: "mall_order_no=" + a.OrderNo + ";stardust=50;user_subscription_id=42;renewed=0",
	}).Error)
	// 一张 paid(不是 held)的单:裁决只收 held。
	c, _ := seedPlanOrder(t, env, p, qymodel.StatusFailed, old)

	// role<100 进不了门。
	status, resp := call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+a.OrderNo+"/adjudicate",
		`{"verdict":"applied","reason":"查了 user_subscriptions 有行"}`, map[string]string{"X-Test-Role": "10"})
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "ROOT_ACTION_REQUIRED", codeOf(resp))
	assert.Equal(t, StatusHeld, loadOrder(t, env, a.OrderNo).Status)

	// 结论与依据的形状。
	status, _ = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+a.OrderNo+"/adjudicate", `{"verdict":"maybe","reason":"1234"}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)
	status, _ = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+a.OrderNo+"/adjudicate", `{"verdict":"applied","reason":"短"}`, nil)
	assert.Equal(t, http.StatusBadRequest, status)
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+c.OrderNo+"/adjudicate", `{"verdict":"applied","reason":"查过主库了"}`, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_bad_status", codeOf(resp))

	// applied:资金单 failed → success,Resolver 把订单推到 done 并回填订阅 id;一分钱不退。
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+a.OrderNo+"/adjudicate", `{"verdict":"applied","reason":"查了 user_subscriptions 有行"}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusDone, dataOf(resp)["status"])
	got := loadOrder(t, env, a.OrderNo)
	assert.Equal(t, 42, got.UserSubscriptionId)
	var fo qymodel.FundOrder
	require.NoError(t, env.ext.Where("order_no = ?", aFO.OrderNo).Take(&fo).Error)
	assert.Equal(t, qymodel.StatusSuccess, fo.Status)
	assert.EqualValues(t, 1, balanceOf(t, env, testUserId))
	var logs int64
	require.NoError(t, env.main.Model(&model.Log{}).Where("user_id = ?", testUserId).Count(&logs).Error)
	assert.EqualValues(t, 1, logs, "PostCommit 补做账本日志")

	// not_applied:退星屑,订单 failed,资金单保持 failed。
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+b.OrderNo+"/adjudicate", `{"verdict":"not_applied","reason":"主库没有这条订阅"}`, nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusFailed, dataOf(resp)["status"])
	gotB := loadOrder(t, env, b.OrderNo)
	assert.NotEmpty(t, gotB.RefundLedgerNo)
	assert.EqualValues(t, 51, balanceOf(t, env, testUserId))
	assert.Equal(t, 1, loadProduct(t, env, p.Id).Sold)

	// 已落定的单再裁 → 409;审计成功两条 + 若干失败。
	status, _ = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+b.OrderNo+"/adjudicate", `{"verdict":"not_applied","reason":"主库没有这条订阅"}`, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.EqualValues(t, 51, balanceOf(t, env, testUserId))
	okRows := 0
	for _, row := range auditRows(t, env, "mall.order.adjudicate") {
		if row.Result == qymodel.ResultOK {
			okRows++
		}
	}
	assert.Equal(t, 2, okRows)
}

// 管理员不能给自己名下的订单退款(自营),也不能对同级 / 更高权限账号动手。
func TestAdminRefundActorGate(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	p := seedProduct(t, env, KindPhysical, 40, nil)
	mine := seedPhysicalOrder(t, env, p)
	require.NoError(t, env.ext.Model(&Order{}).Where("id = ?", mine.Id).Update("user_id", testAdminId).Error)

	status, resp := call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+mine.OrderNo+"/fail", `{"reason":"缺货"}`, nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "qy_self_dealing", codeOf(resp))
	assert.Equal(t, StatusPaid, loadOrder(t, env, mine.OrderNo).Status)
	rows := auditRows(t, env, "mall.order.fail")
	require.Len(t, rows, 1)
	assert.Equal(t, qymodel.ResultFail, rows[0].Result)
}
