package mall

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// api_user.go —— 普通用户接口(契约 §4)。路由挂在 UserAuth 之后,不挂 TokenAuth:
// API Key 是给机器用的、可批量分发,允许它调用等于允许脚本化下单。
//
// 下单是唯一会动钱的入口。幂等键只防得住"同一次点击的重试",防不住脚本用不同
// client_request_id 连续发单,所以路由上还挂了 CriticalRateLimit + UserCriticalRateLimit
// (两把桶都要挂:前者按 IP,后者按账号)。限流是节流不是授权 —— 授权是支付密码(D-12)。

// orderInput 是下单请求体(契约 §4)。
type orderInput struct {
	ProductNo       string   `json:"product_no"`
	ClientRequestId string   `json:"client_request_id"`
	PayPassword     string   `json:"pay_password"`
	Address         string   `json:"address"`
	Contact         string   `json:"contact"`
	ExpectAction    string   `json:"expect_action"`
	ExpectSupersede []string `json:"expect_superseded"`
}

// handleListProducts 返回上架商品(分页)。available / my_count 按当前用户算。
func handleListProducts(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(c.Request.Context())

	q := gdb.Model(&Product{}).Where("enabled = ?", true)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计商品", err))
		return
	}
	// 下发给前端的数组一律显式初始化,理由见 qianye/json_array_guard_test.go。
	rows := make([]Product, 0, size)
	if err := q.Order("sort_order asc, id desc").Offset(httpq.Offset(page, size)).Limit(size).
		Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询商品", err))
		return
	}
	ids := productIdsOf(rows)
	counts, err := codeCountsByProduct(gdb, ids)
	if err != nil {
		respondErr(c, err)
		return
	}
	mine, err := myCountsByProduct(gdb, c.GetInt("id"), ids)
	if err != nil {
		respondErr(c, err)
		return
	}
	now := common.GetTimestamp()
	items := make([]gin.H, 0, len(rows))
	for i := range rows {
		items = append(items, userProductView(&rows[i], counts[rows[i].Id], mine[rows[i].Id], now))
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// handleGetProduct 返回一件商品;套餐附带 preview(这一单会新开 / 续期 / 顶替 / 拒绝)。
// 已下架的商品对用户就是不存在:不区分"没有"与"下架",否则商品号成了存在性预言机。
func handleGetProduct(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(c.Request.Context())
	p, err := loadProductByNo(gdb, c.Param("no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	if !p.Enabled {
		respondErr(c, errProductNotFound)
		return
	}
	uid := c.GetInt("id")
	counts, err := codeCountsByProduct(gdb, []int64{p.Id})
	if err != nil {
		respondErr(c, err)
		return
	}
	mine, err := myCountsByProduct(gdb, uid, []int64{p.Id})
	if err != nil {
		respondErr(c, err)
		return
	}
	v := userProductView(p, counts[p.Id], mine[p.Id], common.GetTimestamp())
	if p.Kind == KindPlan {
		v["preview"] = previewPlan(uid, p)
	} else {
		v["preview"] = nil
	}
	respondOK(c, v)
}

// handleCreateOrder 下单。顺序不变量(design §6.2 / §6.3):
//
//  1. 幂等重放先于一切 —— 重放的请求已经付过钱,不能再被验密 / 售罄 / 限购顶回去;
//  2. code / physical 强制 paypass.Require(ShouldBindJSON 之后、开事务之前、事务之外);
//     plan 不验密:订阅落在本人账号上,带不走(D-12);
//  3. 然后才是各自的事务。
func handleCreateOrder(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	var req orderInput
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	uid := c.GetInt("id")
	crid := strings.TrimSpace(req.ClientRequestId)
	if crid == "" || len(crid) > maxClientRequestID {
		respondErr(c, errBadClientRequestID)
		return
	}
	// 幂等键必须落在三方言比较一致的字符集里(大小写折叠、只留 ASCII),
	// 否则 MySQL 的默认排序规则会把两个键判成同一个,而 PostgreSQL 不会。
	folded, ok := qymodel.NormalizeIdemClientKey(crid)
	if !ok {
		respondErr(c, errBadClientRequestID)
		return
	}
	productNo := strings.TrimSpace(req.ProductNo)
	if productNo == "" {
		respondErr(c, errBadRequest("product_no 不能为空"))
		return
	}

	// 动钱的路径用冷路径预算而不是请求 ctx:客户端中途断连不该把一笔已经进了
	// 事务的扣款半路取消 —— 那会留下"钱扣了、单没落"这种最难解释的状态。
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(ctx)

	// (1) 幂等重放:命中比商品号,不一致 409。重放不写审计 —— 一次点击重试三次
	// 不该看起来像下了三单。
	if existing, err := findOrderByIdemKey(gdb, idemKeyOf(uid, folded)); err != nil {
		respondErr(c, err)
		return
	} else if existing != nil {
		if existing.ProductNo != productNo {
			respondErr(c, errIdemConflict)
			return
		}
		respondOK(c, orderReceipt(existing, true))
		return
	}

	p, err := loadProductByNo(gdb, productNo)
	if err != nil {
		respondErr(c, err)
		return
	}
	if !p.Enabled {
		respondErr(c, errOffSale)
		return
	}

	var (
		o        *Order
		replayed bool
	)
	switch p.Kind {
	case KindCode, KindPhysical:
		address := strings.TrimSpace(req.Address)
		contact := strings.TrimSpace(req.Contact)
		if p.Kind == KindPhysical {
			if address == "" {
				respondErr(c, errAddressRequired)
				return
			}
			if utf8.RuneCountInString(address) > maxAddressRunes || utf8.RuneCountInString(contact) > maxContactRunes {
				respondErr(c, errBadRequest("收货地址不超过 500 个字符,联系方式不超过 128 个字符"))
				return
			}
		}
		// (2) 验密。Require 不通过时已写好响应并 Abort;它没有任何可以表达豁免的入参。
		if !paypass.Require(c, uid, req.PayPassword) {
			return
		}
		o, replayed, err = placeDirectOrder(ctx, directOrderInput{
			UserId: uid, ClientRequestId: folded, Product: p, Address: address, Contact: contact,
		})
	case KindPlan:
		o, replayed, err = placePlanOrder(ctx, planOrderInput{
			UserId: uid, ClientRequestId: folded, Product: p,
			ExpectAction: req.ExpectAction, ExpectSuperseded: req.ExpectSupersede,
		})
	default:
		err = errBadRequest("未知的商品种类")
	}

	if err != nil {
		traceNo := productNo
		if o != nil {
			// 套餐订单已落库并退款(failed):审计挂在单号上,让事后能按单号串起来。
			traceNo = o.OrderNo
		}
		audit.Write(c, userAuditEntry(c, "mall.order.create", traceNo, p.Price, qymodel.ResultFail, auditReason(err)))
		respondErr(c, err)
		return
	}
	if !replayed {
		audit.Write(c, userAuditEntry(c, "mall.order.create", o.OrderNo, o.Price, qymodel.ResultOK,
			"kind="+o.Kind+" status="+o.Status+" product="+productNo))
	}
	respondOK(c, orderReceipt(o, replayed))
}

// addressInput 是中奖者补填收货地址的请求体。
type addressInput struct {
	Address string `json:"address"`
	Contact string `json:"contact"`
}

// handleSetOrderAddress 给一张抽奖所得的实物订单补填收货地址(只允许一次)。
//
// 写的是 PII 密文,成功失败各一条审计;审计里**只记单号,不记地址**。
// 非奖品单 403(qy_ml_not_prize_order)、已有地址 409(qy_ml_address_exists)。
func handleSetOrderAddress(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	orderNo := c.Param("no")
	var req addressInput
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	address := strings.TrimSpace(req.Address)
	contact := strings.TrimSpace(req.Contact)
	if address == "" {
		respondErr(c, errAddressRequired)
		return
	}
	if utf8.RuneCountInString(address) > maxAddressRunes || utf8.RuneCountInString(contact) > maxContactRunes {
		respondErr(c, errBadRequest("收货地址不超过 500 个字符,联系方式不超过 128 个字符"))
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	o, err := setPrizeOrderAddress(ctx, c.GetInt("id"), orderNo, address, contact)
	if err != nil {
		audit.Write(c, userAuditEntry(c, "mall.order.address", orderNo, 0, qymodel.ResultFail, auditReason(err)))
		respondErr(c, err)
		return
	}
	audit.Write(c, userAuditEntry(c, "mall.order.address", orderNo, 0, qymodel.ResultOK, "source="+o.Source))
	respondOK(c, userOrderView(o))
}

// handleListMyOrders 返回当前用户的订单(分页),可按 status / kind / source 筛。
func handleListMyOrders(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(c.Request.Context())

	q := gdb.Model(&Order{}).Where("user_id = ?", c.GetInt("id"))
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("kind"); v != "" {
		q = q.Where("kind = ?", v)
	}
	if v := c.Query("source"); v != "" {
		q = q.Where("source = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计订单", err))
		return
	}
	rows := make([]Order, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询订单", err))
		return
	}
	items := make([]gin.H, 0, len(rows))
	for i := range rows {
		items = append(items, userOrderView(&rows[i]))
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// handleGetMyOrder 返回一张订单的详情与时间线(不含码、不含地址明文)。
func handleGetMyOrder(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(c.Request.Context())
	o, err := loadUserOrder(gdb, c.GetInt("id"), c.Param("no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	events, err := loadEvents(gdb, o.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	v := userOrderView(o)
	v["events"] = events
	respondOK(c, v)
}

// handleRevealCode 解出自己订单上的兑换码。路由上挂着 paypass.Middleware()(请求头
// X-Qy-Pay-Password),这里不再验密 —— 判据只有一处。每一次揭示都写审计,成功失败各一条:
// "谁在什么时候看了哪一枚码"是争议时唯一能拿出来的东西;审计里**只记单号,不记码**。
func handleRevealCode(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	orderNo := c.Param("no")
	code, err := revealCode(c.Request.Context(), c.GetInt("id"), orderNo)
	if err != nil {
		audit.Write(c, userAuditEntry(c, "mall.code.reveal", orderNo, 0, qymodel.ResultFail, auditReason(err)))
		respondErr(c, err)
		return
	}
	audit.Write(c, userAuditEntry(c, "mall.code.reveal", orderNo, 0, qymodel.ResultOK, ""))
	respondOK(c, gin.H{"code": code})
}

// handleCancelOrder 由用户本人取消一张 paid 态的实物订单(全额退星屑)。写审计,成功失败各一条。
func handleCancelOrder(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	orderNo := c.Param("no")
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	o, err := cancelOrder(ctx, c.GetInt("id"), orderNo)
	if err != nil {
		audit.Write(c, userAuditEntry(c, "mall.order.cancel", orderNo, 0, qymodel.ResultFail, auditReason(err)))
		respondErr(c, err)
		return
	}
	audit.Write(c, userAuditEntry(c, "mall.order.cancel", orderNo, o.Price, qymodel.ResultOK,
		"refund_ledger_no="+o.RefundLedgerNo))
	respondOK(c, gin.H{"order_no": o.OrderNo, "status": o.Status, "refund_ledger_no": o.RefundLedgerNo})
}
