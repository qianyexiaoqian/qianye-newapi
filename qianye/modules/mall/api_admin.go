package mall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin.go —— 管理端接口(契约 §5)。传入的组已挂 AdminAuth。
//
// 改商品 / 发码 / 履行 / 退款 / 揭示明文,每一条都写审计,成功与失败各一条
// (writeAdminAudit 是唯一出口)。退款类动作(fail / revoke-code / adjudicate)还要过
// guard.ActorMayActOnCtx:退回去的星屑落在订单持有人账上,不许自营、不许对同级或
// 更高权限的账号动手。

// productsGate 是"商品总数上限"这道闸门的锚点行键(qymodel.LockGate)。
// 计数与插入必须在同一把锁内,否则并发创建各在各的快照里读到旧计数、同时通过。
const productsGate = "mall:products"

// handleAdminListProducts 分页返回全部商品,可按 kind / enabled 筛。
func handleAdminListProducts(c *gin.Context) {
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

	q := gdb.Model(&Product{})
	if v := c.Query("kind"); v != "" {
		q = q.Where("kind = ?", v)
	}
	switch strings.ToLower(c.Query("enabled")) {
	case "1", "true":
		q = q.Where("enabled = ?", true)
	case "0", "false":
		q = q.Where("enabled = ?", false)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计商品", err))
		return
	}
	rows := make([]Product, 0, size)
	if err := q.Order("sort_order asc, id desc").Offset(httpq.Offset(page, size)).Limit(size).
		Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询商品", err))
		return
	}
	counts, err := codeCountsByProduct(gdb, productIdsOf(rows))
	if err != nil {
		respondErr(c, err)
		return
	}
	now := common.GetTimestamp()
	items := make([]gin.H, 0, len(rows))
	for i := range rows {
		items = append(items, adminProductView(&rows[i], counts[rows[i].Id], now))
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// handleAdminCreateProduct 上架一件商品。kind=plan 且 outbox 关闭时 400(acceptProductInput)。
func handleAdminCreateProduct(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	var in productInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "mall.product.create", "", 0, 0, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	p, err := acceptProductInput(in, nil)
	if err != nil {
		writeAdminAudit(c, "mall.product.create", "", 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(ctx)
	adminId := c.GetInt("id")
	limit := config.Get().Mall.MaxProducts
	if limit <= 0 {
		limit = 200
	}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		if err := qymodel.LockGate(tx, productsGate); err != nil {
			return err
		}
		var cnt int64
		if err := tx.Model(&Product{}).Count(&cnt).Error; err != nil {
			return err
		}
		if cnt >= int64(limit) {
			return errMaxProducts
		}
		if err := tx.Create(p).Error; err != nil {
			return err
		}
		return bindCover(tx, p.Id, "", p.CoverRef, adminId)
	})
	if err != nil {
		err = bizOrInternal("创建商品", err)
		writeAdminAudit(c, "mall.product.create", p.ProductNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.product.create", p.ProductNo, 0, 0, qymodel.ResultOK, "", "", snapText(productSnapshot(p)))
	respondOK(c, adminProductView(p, codeCounts{}, common.GetTimestamp()))
}

// handleAdminUpdateProduct 修改一件商品(kind 不可改)。
func handleAdminUpdateProduct(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	productNo := c.Param("no")
	var in productInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "mall.product.update", productNo, 0, 0, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(ctx)
	existing, err := loadProductByNo(gdb, productNo)
	if err != nil {
		writeAdminAudit(c, "mall.product.update", productNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	before := snapText(productSnapshot(existing))
	p, err := acceptProductInput(in, existing)
	if err != nil {
		writeAdminAudit(c, "mall.product.update", productNo, 0, 0, qymodel.ResultFail, auditReason(err), before, "")
		respondErr(c, err)
		return
	}
	adminId := c.GetInt("id")
	err = gdb.Transaction(func(tx *gorm.DB) error {
		if err := bindCover(tx, existing.Id, existing.CoverRef, p.CoverRef, adminId); err != nil {
			return err
		}
		// 用 map 而不是结构体:结构体 Updates 会跳过零值,一件被改成 enabled=false /
		// stock=0 / per_user_limit=0 的商品会被静默写回旧值。
		return tx.Model(&Product{}).Where("id = ?", existing.Id).Updates(map[string]any{
			"title": p.Title, "description": p.Description, "cover_ref": p.CoverRef,
			"price": p.Price, "stock": p.Stock, "per_user_limit": p.PerUserLimit,
			"sale_start_at": p.SaleStartAt, "sale_end_at": p.SaleEndAt,
			"enabled": p.Enabled, "sort_order": p.SortOrder, "plan_id": p.PlanId,
			"updated_at": p.UpdatedAt,
		}).Error
	})
	if err != nil {
		err = bizOrInternal("修改商品", err)
		writeAdminAudit(c, "mall.product.update", productNo, 0, 0, qymodel.ResultFail, auditReason(err), before, "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.product.update", productNo, 0, 0, qymodel.ResultOK, "", before, snapText(productSnapshot(p)))
	counts, err := codeCountsByProduct(gdb, []int64{p.Id})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, adminProductView(p, counts[p.Id], common.GetTimestamp()))
}

// handleAdminDeleteProduct 下架并删除一件商品。有未完结订单 → 409。
//
// 删商品只清 **unused** 的码:已发出(issued / revoked)的码是"这个人拿到的是哪一枚"的
// 证据,与订单、事件一样永不随商品删除。封面只打 detached_at,由回收任务删文件。
func handleAdminDeleteProduct(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	productNo := c.Param("no")
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(ctx)
	p, err := loadProductByNo(gdb, productNo)
	if err != nil {
		writeAdminAudit(c, "mall.product.delete", productNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	before := snapText(productSnapshot(p))
	// 还挂在进行中的抽奖奖档上的商品不能删:中奖那一刻要按商品行建单,行没了就
	// 是一次没人能履行的中奖。判定由 lottery 经 ProductReferenced 提供。
	if referenced, err := ProductReferenced(ctx, p.ProductNo); err != nil {
		writeAdminAudit(c, "mall.product.delete", productNo, 0, 0, qymodel.ResultFail, auditReason(err), before, "")
		respondErr(c, err)
		return
	} else if referenced {
		writeAdminAudit(c, "mall.product.delete", productNo, 0, 0, qymodel.ResultFail, errProductReferenced.Message(), before, "")
		respondErr(c, errProductReferenced)
		return
	}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		var open int64
		if err := tx.Model(&Order{}).Where("product_id = ? AND status IN ?", p.Id, openStatuses).
			Count(&open).Error; err != nil {
			return err
		}
		if open > 0 {
			return errHasOpenOrders
		}
		if err := tx.Where("product_id = ? AND status = ?", p.Id, CodeUnused).Delete(&CodeStock{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Cover{}).Where("product_id = ? AND detached_at = 0", p.Id).
			Updates(map[string]any{"detached_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", p.Id).Delete(&Product{}).Error
	})
	if err != nil {
		err = bizOrInternal("删除商品", err)
		writeAdminAudit(c, "mall.product.delete", productNo, 0, 0, qymodel.ResultFail, auditReason(err), before, "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.product.delete", productNo, 0, 0, qymodel.ResultOK, "", before, "")
	respondOK(c, gin.H{"product_no": productNo})
}

// codesInput 是批量上传兑换码的请求体。整个 body 由凭证构成,登记 credentialBodyRoutes。
type codesInput struct {
	Codes []string `json:"codes"`
}

// handleAdminUploadCodes 批量入库兑换码。审计里**只记条数**,一个字符的码都不进去。
func handleAdminUploadCodes(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	productNo := c.Param("no")
	var in codesInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "mall.codes.upload", productNo, 0, 0, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(ctx)
	p, err := loadProductByNo(gdb, productNo)
	if err != nil {
		writeAdminAudit(c, "mall.codes.upload", productNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	accepted, rejected, err := uploadCodes(ctx, p, in.Codes)
	if err != nil {
		writeAdminAudit(c, "mall.codes.upload", productNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	// 顺手把等码的奖品单补齐:运营补码的动机往往就是"有人中了奖还没拿到"。
	// 补码失败不影响上传本身的结果 —— 码已经入库,下一次上传或人工都能续上。
	filled := 0
	if accepted > 0 {
		if filled, err = fillAwaitingCodeOrders(ctx, gdb, p); err != nil {
			common.SysError("qianye/mall: 商品 " + productNo + " 补发等码的奖品单失败: " + err.Error())
		}
	}
	writeAdminAudit(c, "mall.codes.upload", productNo, 0, int64(accepted), qymodel.ResultOK,
		fmt.Sprintf("accepted=%d rejected=%d filled=%d", accepted, len(rejected), filled), "", "")
	respondOK(c, gin.H{"accepted": accepted, "rejected": rejected, "filled": filled})
}

// handleAdminListCodes 分页返回一件商品的码库存行,可按 status 筛。
//
// **不回明文,一个字符都不回**:这条是列表,一次越权就是全量泄漏。明文只有
// handleAdminTakeCode 一条出口,逐枚、验密、写审计。
func handleAdminListCodes(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	ctx := c.Request.Context()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(ctx)
	p, err := loadProductByNo(gdb, c.Param("no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	rows, total, err := listCodes(ctx, gdb, p, c.Query("status"), page, size)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 提卡人的用户名从主库批量取,与订单列表同形(Unscoped:管理员账号可能已被删,
	// 但"当初是谁提走的"必须还能显示)。
	takers := adminNamesOf(ctx, rows)
	// 已发出的码要显示"发给了哪张单":order_id 是内部自增 id,不下发,换成单号。
	orderNos, err := orderNosByIds(gdb, rows)
	if err != nil {
		respondErr(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for i := range rows {
		items = append(items, codeStockView(&rows[i], orderNos[rows[i].OrderId], takers[rows[i].TakenBy]))
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// handleAdminTakeCode 由管理员提走一枚未使用的兑换码,响应体里带明文。
//
// 路由上挂着 paypass.Middleware()(请求头 X-Qy-Pay-Password),这里不再验密 ——
// 判据只有一处。明文只出现在这一次响应里:不进审计、不进请求台账(POST 的响应体
// 本来就不入库)、不进任何日志。审计记的是"谁在什么时候提走了哪一枚(id)"。
func handleAdminTakeCode(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	productNo := c.Param("no")
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		writeAdminAudit(c, "mall.code.take", productNo, 0, 0, qymodel.ResultFail, "码 id 不合法", "", "")
		respondErr(c, errCodeNotFound)
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	p, err := loadProductByNo(handle.WithContext(ctx), productNo)
	if err != nil {
		writeAdminAudit(c, "mall.code.take", productNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	plain, row, err := takeCode(ctx, p, id, c.GetInt("id"))
	if err != nil {
		writeAdminAudit(c, "mall.code.take", productNo, 0, 0, qymodel.ResultFail, auditReason(err),
			snapText(map[string]any{"code_id": id}), "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.code.take", productNo, 0, 0, qymodel.ResultOK, fmt.Sprintf("code_id=%d", id),
		snapText(map[string]any{"code_id": id, "status": CodeUnused}),
		snapText(map[string]any{"code_id": id, "status": row.Status, "taken_at": row.TakenAt}))
	respondOK(c, gin.H{"id": row.Id, "code": plain, "status": row.Status, "taken_at": row.TakenAt})
}

// handleAdminDeleteCode 删掉一枚未使用的兑换码。
//
// 已发出 / 已撤回 / 已提取的行删不掉(deleteCode 里判):它们是发放去向的证据。
func handleAdminDeleteCode(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	productNo := c.Param("no")
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		writeAdminAudit(c, "mall.code.delete", productNo, 0, 0, qymodel.ResultFail, "码 id 不合法", "", "")
		respondErr(c, errCodeNotFound)
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(ctx)
	p, err := loadProductByNo(gdb, productNo)
	if err != nil {
		writeAdminAudit(c, "mall.code.delete", productNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if err := deleteCode(ctx, gdb, p, id); err != nil {
		writeAdminAudit(c, "mall.code.delete", productNo, 0, 0, qymodel.ResultFail, auditReason(err),
			snapText(map[string]any{"code_id": id}), "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.code.delete", productNo, 0, 0, qymodel.ResultOK, fmt.Sprintf("code_id=%d", id),
		snapText(map[string]any{"code_id": id, "status": CodeUnused}), "")
	respondOK(c, gin.H{"id": id})
}

// handleAdminListOrders 分页返回全部订单,可按 status / kind / user_id 筛;附用户名。
func handleAdminListOrders(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	ctx := c.Request.Context()
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb := handle.WithContext(ctx)

	q := gdb.Model(&Order{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("kind"); v != "" {
		q = q.Where("kind = ?", v)
	}
	if uid := httpq.Int(c, "user_id", 0); uid > 0 {
		q = q.Where("user_id = ?", uid)
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
	// 用户名从主库按 id 批量取。Unscoped:订单不随账号删除而删除,一个已删账号的
	// 订单仍要能显示它当初叫什么。
	names := make(map[int]string, len(rows))
	if len(rows) > 0 && model.DB != nil {
		ids := make([]int, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].UserId)
		}
		users := make([]model.User, 0, len(ids))
		if err := model.DB.WithContext(ctx).Unscoped().Model(&model.User{}).
			Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
			respondErr(c, wrapInternal("读取用户名", err))
			return
		}
		for _, u := range users {
			names[u.Id] = u.Username
		}
	}
	items := make([]gin.H, 0, len(rows))
	for i := range rows {
		items = append(items, adminOrderView(&rows[i], names[rows[i].UserId]))
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// handleAdminShipOrder 给实物订单发货(tracking_no 必填;done=true 一步完结)。
func handleAdminShipOrder(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	orderNo := c.Param("no")
	var in shipInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "mall.order.ship", orderNo, 0, 0, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	o, err := shipOrder(ctx, orderNo, in, c.GetInt("id"))
	if err != nil {
		writeAdminAudit(c, "mall.order.ship", orderNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.order.ship", orderNo, o.UserId, 0, qymodel.ResultOK, "", "",
		snapText(map[string]any{"status": o.Status, "tracking_no": o.TrackingNo, "done": in.Done}))
	respondOK(c, adminOrderView(o, ""))
}

// loadAdminMoneyTarget 取一张即将被退款的单,并挡住自营与越级。
//
// 三个退款动作(fail / revoke-code / adjudicate)全部从这里取单,是刻意的单一入口:
// 「自己下单 → 自己给自己退星屑」在这里断掉。两种被拒都留痕:被拒的尝试不是手滑,
// 它是这条链上最容易被反复尝试的一步,而事后仲裁只认审计表。
func loadAdminMoneyTarget(c *gin.Context, action, orderNo string) (*Order, bool) {
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return nil, false
	}
	o, err := loadOrderByNo(handle.WithContext(c.Request.Context()), orderNo)
	if err != nil {
		writeAdminAudit(c, action, orderNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return nil, false
	}
	switch err := guard.ActorMayActOnCtx(c, o.UserId); {
	case err == nil:
		return o, true
	case errors.Is(err, guard.ErrActorIsTarget):
		writeAdminAudit(c, action, orderNo, o.UserId, o.Price, qymodel.ResultFail, errSelfDealing.Message(), "", "")
		respondErr(c, errSelfDealing)
	case errors.Is(err, guard.ErrTargetMissing):
		writeAdminAudit(c, action, orderNo, o.UserId, o.Price, qymodel.ResultFail, errTargetMissing.Message(), "", "")
		respondErr(c, errTargetMissing)
	case errors.Is(err, guard.ErrTargetNotLower):
		writeAdminAudit(c, action, orderNo, o.UserId, o.Price, qymodel.ResultFail, errTargetHigher.Message(), "", "")
		respondErr(c, errTargetHigher)
	default:
		writeAdminAudit(c, action, orderNo, o.UserId, o.Price, qymodel.ResultFail, "操作人判据: "+err.Error(), "", "")
		respondErr(c, wrapInternal("操作人判据", err))
	}
	return nil, false
}

// handleAdminFailOrder 把实物订单标记为失败并退星屑(事由必填)。
func handleAdminFailOrder(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	orderNo := c.Param("no")
	var in reasonInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "mall.order.fail", orderNo, 0, 0, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason, err := acceptReason(in.Reason)
	if err != nil {
		writeAdminAudit(c, "mall.order.fail", orderNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	target, ok := loadAdminMoneyTarget(c, "mall.order.fail", orderNo)
	if !ok {
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	o, err := failOrder(ctx, orderNo, reason, c.GetInt("id"))
	if err != nil {
		writeAdminAudit(c, "mall.order.fail", orderNo, target.UserId, target.Price, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.order.fail", orderNo, o.UserId, o.Price, qymodel.ResultOK, reason,
		snapText(map[string]any{"status": target.Status}),
		snapText(map[string]any{"status": o.Status, "refund_ledger_no": o.RefundLedgerNo}))
	respondOK(c, adminOrderView(o, ""))
}

// handleAdminRevokeCode 撤回一枚已发出的兑换码并退星屑(事由必填)。
func handleAdminRevokeCode(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	orderNo := c.Param("no")
	var in reasonInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "mall.code.revoke", orderNo, 0, 0, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason, err := acceptReason(in.Reason)
	if err != nil {
		writeAdminAudit(c, "mall.code.revoke", orderNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	target, ok := loadAdminMoneyTarget(c, "mall.code.revoke", orderNo)
	if !ok {
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	o, err := revokeCode(ctx, orderNo, reason, c.GetInt("id"))
	if err != nil {
		writeAdminAudit(c, "mall.code.revoke", orderNo, target.UserId, target.Price, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.code.revoke", orderNo, o.UserId, o.Price, qymodel.ResultOK, reason,
		snapText(map[string]any{"status": target.Status}),
		snapText(map[string]any{"status": o.Status, "refund_ledger_no": o.RefundLedgerNo}))
	respondOK(c, adminOrderView(o, ""))
}

// handleAdminRevealAddress 解出一张实物订单的收货地址与联系方式。
//
// 这是 PII 明文的唯一出口:请求台账(sensitiveReads)记调用事实,这里的业务审计记
// "谁看了哪张单";两者都**只记单号,不记明文**。
func handleAdminRevealAddress(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	orderNo := c.Param("no")
	handle := db.Get()
	if handle == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	o, err := loadOrderByNo(handle.WithContext(c.Request.Context()), orderNo)
	if err != nil {
		writeAdminAudit(c, "mall.address.reveal", orderNo, 0, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if o.Kind != KindPhysical {
		writeAdminAudit(c, "mall.address.reveal", orderNo, o.UserId, 0, qymodel.ResultFail, "不是实物订单", "", "")
		respondErr(c, errBadStatus)
		return
	}
	if addressMissing(o) {
		writeAdminAudit(c, "mall.address.reveal", orderNo, o.UserId, 0, qymodel.ResultFail, errAddressMissing.Message(), "", "")
		respondErr(c, errAddressMissing)
		return
	}
	address, contact, err := openAddress(o)
	if err != nil {
		writeAdminAudit(c, "mall.address.reveal", orderNo, o.UserId, 0, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.address.reveal", orderNo, o.UserId, 0, qymodel.ResultOK, "", "", "")
	respondOK(c, gin.H{"address": address, "contact": contact})
}

// handleAdminAdjudicate 是人工裁决的 HTTP 落点。路由上挂着 RootActionGate(RootActionMallAdjudicate),
// 这里不再判角色 —— 判据只有一处。
func handleAdminAdjudicate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	orderNo := c.Param("no")
	var in adjudicateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "mall.order.adjudicate", orderNo, 0, 0, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	verdict := strings.TrimSpace(in.Verdict)
	if verdict != VerdictApplied && verdict != VerdictNotApplied {
		writeAdminAudit(c, "mall.order.adjudicate", orderNo, 0, 0, qymodel.ResultFail, "核对结论非法: "+verdict, "", "")
		respondErr(c, errBadRequest("verdict 只能是 applied(订阅确实已发放)或 not_applied(确实没发放)"))
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(reason); n < 4 || n > maxReasonRunes {
		writeAdminAudit(c, "mall.order.adjudicate", orderNo, 0, 0, qymodel.ResultFail, "未填写核对依据", "", "")
		respondErr(c, errBadRequest("必须写清楚核对依据(查了主库的什么、看到了什么,4~255 个字符),它是这笔星屑事后唯一的解释"))
		return
	}
	target, ok := loadAdminMoneyTarget(c, "mall.order.adjudicate", orderNo)
	if !ok {
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	after, err := adjudicateOrder(ctx, orderNo, verdict, reason, c.GetInt("id"))
	if err != nil {
		writeAdminAudit(c, "mall.order.adjudicate", orderNo, target.UserId, target.Price, qymodel.ResultFail, auditReason(err),
			snapText(map[string]any{"status": target.Status, "fund_order_no": target.FundOrderNo}), "")
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.order.adjudicate", orderNo, after.UserId, after.Price, qymodel.ResultOK, verdict+": "+reason,
		snapText(map[string]any{"status": target.Status, "fund_order_no": target.FundOrderNo}),
		snapText(map[string]any{"status": after.Status, "verdict": verdict, "refund_ledger_no": after.RefundLedgerNo}))
	common.SysLog("qianye/mall: 套餐订单 " + orderNo + " 已按人工核对结论落定(" + verdict + ")")
	respondOK(c, gin.H{"order_no": orderNo, "status": after.Status, "verdict": verdict})
}

// handleAdminUploadCover 接收一张封面,返回可以填进商品的 ref。已挂 CriticalRateLimit:
// 上传要落磁盘,是本模块唯一一条能消耗宿主机存储的入口。
func handleAdminUploadCover(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	row, err := acceptCoverUpload(c, c.GetInt("id"))
	if err != nil {
		// 失败的上传没有产生任何存储副作用,调用事实由请求台账覆盖。
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.cover.upload", row.Ref, 0, 0, qymodel.ResultOK, "", "",
		snapText(map[string]any{"ref": row.Ref, "mime_type": row.MimeType, "size": row.Size}))
	respondOK(c, gin.H{"ref": row.Ref, "mime_type": row.MimeType, "size": row.Size, "created_at": row.CreatedAt})
}

// handleAdminDiscardCover 丢弃一张【自己上传且尚未用在任何商品上】的封面。
func handleAdminDiscardCover(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagMall) {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		respondErr(c, errCoverNotFound)
		return
	}
	if err := discardPendingCover(c.Request.Context(), c.GetInt("id"), ref); err != nil {
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "mall.cover.discard", ref, 0, 0, qymodel.ResultOK, "", snapText(map[string]any{"ref": ref}), "")
	respondOK(c, gin.H{"ref": ref})
}
