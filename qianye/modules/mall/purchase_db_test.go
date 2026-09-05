package mall

import (
	"context"
	"net/http"
	"testing"

	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// purchase_db_test.go —— kind=code / physical 的下单契约(design §6.2)。
//
// 每一条都是资金不变量或对外契约:库存与余额同一事务、幂等重放不动余额、换参 409、
// 星屑不足 / 限购 / 售罄不留残行、码揭示必须过验密、取消退款至多一次。

const payHeader = "X-Qy-Pay-Password"

func TestCodeOrderEndToEnd(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	seedPayPassword(t, env, testUserId, testPayPwd)
	grantStardust(t, env, testUserId, 100)
	p := seedProduct(t, env, KindCode, 30, func(p *Product) { p.PerUserLimit = 2 })
	seedCodes(t, env, p, "CODE-AAA", "CODE-BBB")

	// 验密失败:什么都没发生 —— 没有订单、余额没动、码没发。
	status, resp := call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-1", map[string]any{"pay_password": "wrong-pwd"}), nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "qy_pay_pwd_wrong", codeOf(resp))
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId))
	var orders int64
	require.NoError(t, env.ext.Model(&Order{}).Count(&orders).Error)
	assert.Zero(t, orders, "验密失败的请求不能落单")

	// 正常下单:订单 done、扣 30、发出一枚码。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-1", map[string]any{"pay_password": testPayPwd}), nil)
	requireOK(t, status, resp)
	d := dataOf(resp)
	orderNo, _ := d["order_no"].(string)
	require.NotEmpty(t, orderNo)
	assert.Equal(t, StatusDone, d["status"])
	assert.Equal(t, false, d["replayed"])
	assert.Equal(t, true, d["code_available"])
	assert.EqualValues(t, 70, balanceOf(t, env, testUserId))
	o := loadOrder(t, env, orderNo)
	assert.NotZero(t, o.CodeStockId)
	assert.NotEmpty(t, o.LedgerNo)
	var issued int64
	require.NoError(t, env.ext.Model(&CodeStock{}).Where("status = ? AND order_id = ?", CodeIssued, o.Id).Count(&issued).Error)
	assert.EqualValues(t, 1, issued)
	assert.Len(t, auditRows(t, env, "mall.order.create"), 1, "验密失败发生在下单之前,由 paypass 自己留痕;这里只有成功那一条")

	// 幂等重放:同一个 client_request_id 拿回原单,余额与库存一动不动。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-1", nil), nil)
	requireOK(t, status, resp)
	assert.Equal(t, orderNo, dataOf(resp)["order_no"])
	assert.Equal(t, true, dataOf(resp)["replayed"])
	assert.EqualValues(t, 70, balanceOf(t, env, testUserId))
	require.NoError(t, env.ext.Model(&CodeStock{}).Where("status = ?", CodeIssued).Count(&issued).Error)
	assert.EqualValues(t, 1, issued)

	// 换参重放:同一个键换了商品 → 409,不落第二单。
	other := seedProduct(t, env, KindPhysical, 5, nil)
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(other.ProductNo, "crid-1", map[string]any{"pay_password": testPayPwd, "address": "x"}), nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_idem_key_conflict", codeOf(resp))

	// 揭示:没带密码 → 拒(码一个字节都不下发);带对密码 → 明文。
	status, resp = call(t, r, http.MethodGet, "/api/qy/mall/orders/"+orderNo+"/code", "", nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "qy_pay_pwd_required", codeOf(resp))
	assert.Nil(t, dataOf(resp)["code"])
	status, resp = call(t, r, http.MethodGet, "/api/qy/mall/orders/"+orderNo+"/code", "",
		map[string]string{payHeader: testPayPwd})
	requireOK(t, status, resp)
	assert.Equal(t, "CODE-AAA", dataOf(resp)["code"], "库存按 id 升序发码")
	reveals := auditRows(t, env, "mall.code.reveal")
	require.Len(t, reveals, 1)
	assert.Equal(t, qymodel.ResultOK, reveals[0].Result)
	assert.Equal(t, orderNo, reveals[0].TraceNo)

	// 别人的单:同一个单号换个用户揭示 → 404(不区分"不存在"与"不是你的")。
	seedPayPassword(t, env, testOtherId, testPayPwd)
	status, resp = call(t, r, http.MethodGet, "/api/qy/mall/orders/"+orderNo+"/code", "",
		map[string]string{payHeader: testPayPwd, testUserHeader: "9102"})
	assert.Equal(t, http.StatusNotFound, status)
	assert.Nil(t, dataOf(resp)["code"])

	// 第二件:限购 2 还能买;第三件 → 限购;库存只有两枚 → 第三个人买不到。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-2", map[string]any{"pay_password": testPayPwd}), nil)
	requireOK(t, status, resp)
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-3", map[string]any{"pay_password": testPayPwd}), nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_limit", codeOf(resp))
	assert.EqualValues(t, 40, balanceOf(t, env, testUserId), "被限购顶回去的请求不能扣钱")

	grantStardust(t, env, testOtherId, 100)
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-o1", map[string]any{"pay_password": testPayPwd}),
		map[string]string{testUserHeader: "9102"})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_sold_out", codeOf(resp))
	assert.EqualValues(t, 100, balanceOf(t, env, testOtherId), "售罄回滚整个事务,星屑不能少")
	assert.Equal(t, 2, loadProduct(t, env, p.Id).Sold, "售罄那一次 sold+1 必须随事务回滚")
}

func TestCodeOrderInsufficientLeavesNoTrace(t *testing.T) {
	env := newMallEnv(t, nil)
	seedPayPassword(t, env, testUserId, testPayPwd)
	grantStardust(t, env, testUserId, 10)
	p := seedProduct(t, env, KindCode, 30, nil)
	seedCodes(t, env, p, "CODE-AAA")

	_, _, err := placeDirectOrder(context.Background(), directOrderInput{
		UserId: testUserId, ClientRequestId: "crid-poor", Product: p,
	})
	require.Error(t, err)
	be, ok := AsBizError(err)
	require.True(t, ok)
	assert.Equal(t, "qy_sd_insufficient", be.ErrCode())

	// 商品行的 sold+1 与订单都随事务回滚;码仍是 unused;余额原样。
	assert.Equal(t, 0, loadProduct(t, env, p.Id).Sold)
	var n int64
	require.NoError(t, env.ext.Model(&Order{}).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, env.ext.Model(&CodeStock{}).Where("status = ?", CodeUnused).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	assert.EqualValues(t, 10, balanceOf(t, env, testUserId))
	assert.Equal(t, []string{string(stardust.KindManual)}, ledgerKinds(t, env, testUserId), "不能留下任何扣款流水")
}

func TestPhysicalOrderCancelRefundsExactlyOnce(t *testing.T) {
	env := newMallEnv(t, nil)
	r := newRouter()
	seedPayPassword(t, env, testUserId, testPayPwd)
	grantStardust(t, env, testUserId, 100)
	p := seedProduct(t, env, KindPhysical, 40, func(p *Product) { p.Stock = 3 })

	// 地址必填:没有地址连验密都不该走到。
	status, resp := call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-p0", map[string]any{"pay_password": testPayPwd}), nil)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "qy_ml_address_required", codeOf(resp))

	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders",
		orderBody(p.ProductNo, "crid-p1", map[string]any{
			"pay_password": testPayPwd, "address": "上海市 某路 1 号", "contact": "13800000000",
		}), nil)
	requireOK(t, status, resp)
	orderNo, _ := dataOf(resp)["order_no"].(string)
	assert.Equal(t, StatusPaid, dataOf(resp)["status"])
	assert.Equal(t, false, dataOf(resp)["code_available"])
	assert.EqualValues(t, 60, balanceOf(t, env, testUserId))
	assert.Equal(t, 1, loadProduct(t, env, p.Id).Sold)

	// 地址落的是密文,解出来与输入一致;用户视图与详情里一个明文都没有。
	o := loadOrder(t, env, orderNo)
	address, contact, err := openAddress(&o)
	require.NoError(t, err)
	assert.Equal(t, "上海市 某路 1 号", address)
	assert.Equal(t, "13800000000", contact)
	status, resp = call(t, r, http.MethodGet, "/api/qy/mall/orders/"+orderNo, "", nil)
	requireOK(t, status, resp)
	assert.NotContains(t, resp, "address")
	events, _ := dataOf(resp)["events"].([]any)
	assert.Len(t, events, 1, "支付一条事件")

	// 取消:退 40、cancelled、库存放回;再取消 → 409 且不再退。
	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders/"+orderNo+"/cancel", "", nil)
	requireOK(t, status, resp)
	assert.Equal(t, StatusCancelled, dataOf(resp)["status"])
	refundNo, _ := dataOf(resp)["refund_ledger_no"].(string)
	assert.NotEmpty(t, refundNo)
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId))
	assert.Equal(t, 0, loadProduct(t, env, p.Id).Sold, "取消要把库存放回")

	status, resp = call(t, r, http.MethodPost, "/api/qy/mall/orders/"+orderNo+"/cancel", "", nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "qy_ml_bad_status", codeOf(resp))
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId), "第二次取消不能再退一次")
	assert.Equal(t, []string{"manual", "mall_order", "mall_refund"}, ledgerKinds(t, env, testUserId))

	// 管理员对一张已取消的单再标失败:同样 409,同样不退第二次。
	status, resp = call(t, r, http.MethodPost, "/api/qy/admin/mall/orders/"+orderNo+"/fail",
		`{"reason":"缺货"}`, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId))
	assert.Equal(t, 0, loadProduct(t, env, p.Id).Sold)

	// 直接调退款函数重放同一张单:幂等键 mallrf:<order_no> 命中已有流水,余额不动,
	// 而状态 CAS 落空让整个事务回滚 —— 两道闸都在,才敢说"至多一次"。
	err = env.ext.Transaction(func(tx *gorm.DB) error {
		lo, err := lockOrderByNo(tx, orderNo)
		if err != nil {
			return err
		}
		return refundAndTransition(tx, lo, "replay", transition{
			From: []string{StatusPaid}, To: StatusCancelled, Action: ActionCancel,
		})
	})
	require.ErrorIs(t, err, errStatusConflict)
	assert.EqualValues(t, 100, balanceOf(t, env, testUserId))
	assert.Equal(t, 0, loadProduct(t, env, p.Id).Sold, "重放的退款连库存放回也要随事务回滚")

	// 取消不写码、不动其它用户;审计成功一条 + 失败一条。
	cancels := auditRows(t, env, "mall.order.cancel")
	require.Len(t, cancels, 2)
	assert.Equal(t, qymodel.ResultOK, cancels[0].Result)
	assert.Equal(t, qymodel.ResultFail, cancels[1].Result)
}
