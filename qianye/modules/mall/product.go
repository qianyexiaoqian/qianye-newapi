package mall

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// product.go —— 商品的校验、读取与视图。

const (
	maxTitleRunes       = 128
	maxDescriptionRunes = 2000
	// coverURLPrefix 是公开封面端点的前缀(module.go 里的 PublicRouter)。
	coverURLPrefix = "/api/qy/mall/covers/"
)

// productInput 是创建 / 修改商品的请求体(契约 §5)。
type productInput struct {
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	CoverRef     string `json:"cover_ref"`
	Price        int64  `json:"price"`
	Stock        int    `json:"stock"`
	PerUserLimit int    `json:"per_user_limit"`
	SaleStartAt  int64  `json:"sale_start_at"`
	SaleEndAt    int64  `json:"sale_end_at"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int    `json:"sort_order"`
	PlanId       int    `json:"plan_id"`
}

// acceptProductInput 把请求体归一成待落库的商品行。existing 非 nil 表示修改。
//
// kind 不可改:三种商品的履行方式完全不同,一件已经发过码的商品改成实物,
// 它名下的订单没有任何一条能解释。price 的上界与 stardust.post 同源。
func acceptProductInput(in productInput, existing *Product) (*Product, error) {
	kind := strings.TrimSpace(in.Kind)
	if existing != nil {
		if kind != "" && kind != existing.Kind {
			return nil, errBadRequest("商品种类不可修改")
		}
		kind = existing.Kind
	}
	if kind != KindPlan && kind != KindCode && kind != KindPhysical {
		return nil, errBadRequest("kind 只能是 plan / code / physical")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || utf8.RuneCountInString(title) > maxTitleRunes {
		return nil, errBadRequest("标题必填且不超过 128 个字符")
	}
	desc := strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(desc) > maxDescriptionRunes {
		return nil, errBadRequest("商品说明不超过 2000 个字符")
	}
	if in.Price < 1 || in.Price > int64(common.MaxQuota) {
		return nil, errBadRequest("售价必须是 1 到系统上界之间的整数")
	}
	stock := in.Stock
	if kind == KindCode {
		// code 类的库存由库存表算,这一列恒为不限。
		stock = StockUnlimited
	}
	if stock < StockUnlimited {
		return nil, errBadRequest("库存必须是 -1(不限)或非负整数")
	}
	if in.PerUserLimit < 0 {
		return nil, errBadRequest("每人限购必须是非负整数(0 = 不限)")
	}
	// 发售窗的判据与上游套餐同一份:0 是不限、左闭右开、start == end 是空窗口。
	if err := model.ValidatePlanSaleWindow(in.SaleStartAt, in.SaleEndAt); err != nil {
		return nil, errBadRequest(err.Error())
	}
	planId := 0
	if kind == KindPlan {
		if in.PlanId <= 0 {
			return nil, errBadRequest("套餐商品必须指定 plan_id")
		}
		if _, err := model.GetSubscriptionPlanById(in.PlanId); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errBadRequest("plan_id 对应的套餐不存在")
			}
			return nil, wrapInternal("读取套餐", err)
		}
		planId = in.PlanId
		// 套餐商品的失败退款判据是主库 outbox 探针,探针关掉就不许上架:
		// 创建一律拒;修改只在"要上架"时拒,允许运营把它下架。
		if !config.Get().TwoPhase.OutboxEnabled() && (existing == nil || in.Enabled) {
			return nil, errPlanNeedsOutbox
		}
	}
	now := common.GetTimestamp()
	p := &Product{
		Kind:         kind,
		Title:        title,
		Description:  desc,
		CoverRef:     strings.TrimSpace(in.CoverRef),
		Price:        in.Price,
		Stock:        stock,
		PerUserLimit: in.PerUserLimit,
		SaleStartAt:  in.SaleStartAt,
		SaleEndAt:    in.SaleEndAt,
		Enabled:      in.Enabled,
		SortOrder:    in.SortOrder,
		PlanId:       planId,
		UpdatedAt:    now,
	}
	if existing != nil {
		p.Id, p.ProductNo, p.Sold, p.CreatedAt = existing.Id, existing.ProductNo, existing.Sold, existing.CreatedAt
	} else {
		p.ProductNo, p.CreatedAt = newProductNo(), now
	}
	return p, nil
}

// loadProductByNo 按商品号取一件商品。gdb 必须是已经绑好 ctx 的句柄。
func loadProductByNo(gdb *gorm.DB, productNo string) (*Product, error) {
	var p Product
	err := gdb.Where("product_no = ?", productNo).Take(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errProductNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取商品", err)
	}
	return &p, nil
}

// saleWindowOpen 判"此刻在不在发售窗内"。0 是不限;窗口左闭右开,与上游套餐同口径。
func saleWindowOpen(p *Product, now int64) bool {
	if p.SaleStartAt != 0 && now < p.SaleStartAt {
		return false
	}
	if p.SaleEndAt != 0 && now >= p.SaleEndAt {
		return false
	}
	return true
}

// codeCounts 是一件 code 商品的库存分态计数。
type codeCounts struct {
	Unused  int64
	Issued  int64
	Revoked int64
	Taken   int64
}

// codeCountsByProduct 一次分组查出一批商品的库存计数。列表页逐行各查一次就是
// 一页 20×4 次往返。
func codeCountsByProduct(gdb *gorm.DB, productIds []int64) (map[int64]codeCounts, error) {
	out := make(map[int64]codeCounts, len(productIds))
	if len(productIds) == 0 {
		return out, nil
	}
	rows := make([]struct {
		ProductId int64
		Status    string
		Cnt       int64
	}, 0, len(productIds)*4)
	err := gdb.Model(&CodeStock{}).
		Select("product_id, status, COUNT(*) AS cnt").
		Where("product_id IN ?", productIds).
		Group("product_id, status").Scan(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计兑换码库存", err)
	}
	for _, r := range rows {
		c := out[r.ProductId]
		switch r.Status {
		case CodeUnused:
			c.Unused = r.Cnt
		case CodeIssued:
			c.Issued = r.Cnt
		case CodeRevoked:
			c.Revoked = r.Cnt
		case CodeTaken:
			c.Taken = r.Cnt
		}
		out[r.ProductId] = c
	}
	return out, nil
}

// myCountsByProduct 数出某个用户在一批商品上已占的限购名额(失败与取消不算)。
func myCountsByProduct(gdb *gorm.DB, userId int, productIds []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(productIds))
	if userId <= 0 || len(productIds) == 0 {
		return out, nil
	}
	rows := make([]struct {
		ProductId int64
		Cnt       int64
	}, 0, len(productIds))
	err := countedStatusesSQL(gdb.Model(&Order{}).
		Select("product_id, COUNT(*) AS cnt").
		Where("user_id = ? AND product_id IN ?", userId, productIds)).
		Group("product_id").Scan(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计我的购买", err)
	}
	for _, r := range rows {
		out[r.ProductId] = r.Cnt
	}
	return out, nil
}

// planSummary 是商品视图里附带的套餐摘要;套餐不存在时返回 nil(JSON null)。
func planSummary(planId int) gin.H {
	if planId <= 0 {
		return nil
	}
	plan, err := model.GetSubscriptionPlanById(planId)
	if err != nil {
		return nil
	}
	return gin.H{
		"title":          plan.Title,
		"price_amount":   plan.PriceAmount,
		"upgrade_group":  plan.UpgradeGroup,
		"no_quota":       plan.NoQuota,
		"duration_unit":  plan.DurationUnit,
		"duration_value": plan.DurationValue,
	}
}

// productAvailable 回答"此刻能不能买":上架、在窗、有货;套餐还要套餐本身可售且探针开着。
func productAvailable(p *Product, counts codeCounts, now int64) bool {
	if !p.Enabled || !saleWindowOpen(p, now) {
		return false
	}
	switch p.Kind {
	case KindCode:
		return counts.Unused > 0
	case KindPhysical:
		return p.Stock == StockUnlimited || p.Sold < p.Stock
	case KindPlan:
		if !config.Get().TwoPhase.OutboxEnabled() {
			return false
		}
		plan, err := model.GetSubscriptionPlanById(p.PlanId)
		if err != nil || !plan.Enabled || model.PlanSaleWindowError(plan, now) != nil {
			return false
		}
		return p.Stock == StockUnlimited || p.Sold < p.Stock
	}
	return false
}

// effectiveStock 是视图里的 stock:code 类回库存表的 unused 数,其余回列值。
func effectiveStock(p *Product, counts codeCounts) int64 {
	if p.Kind == KindCode {
		return counts.Unused
	}
	return int64(p.Stock)
}

// userProductView 是用户端商品行(契约 §4)。
func userProductView(p *Product, counts codeCounts, myCount int64, now int64) gin.H {
	coverURL := ""
	if p.CoverRef != "" {
		coverURL = coverURLPrefix + p.CoverRef
	}
	return gin.H{
		"product_no":     p.ProductNo,
		"kind":           p.Kind,
		"title":          p.Title,
		"description":    p.Description,
		"cover_url":      coverURL,
		"price":          p.Price,
		"stock":          effectiveStock(p, counts),
		"sold":           p.Sold,
		"per_user_limit": p.PerUserLimit,
		"sale_start_at":  p.SaleStartAt,
		"sale_end_at":    p.SaleEndAt,
		"plan_id":        p.PlanId,
		"plan":           planSummary(p.PlanId),
		"available":      productAvailable(p, counts, now),
		"my_count":       myCount,
	}
}

// adminProductView 是管理端商品行(契约 §5):用户端 + enabled / sort_order / code_stock / 时间戳。
func adminProductView(p *Product, counts codeCounts, now int64) gin.H {
	v := userProductView(p, counts, 0, now)
	v["enabled"] = p.Enabled
	v["sort_order"] = p.SortOrder
	v["cover_ref"] = p.CoverRef
	v["code_stock"] = gin.H{
		"unused": counts.Unused, "issued": counts.Issued,
		"revoked": counts.Revoked, "taken": counts.Taken,
	}
	v["created_at"] = p.CreatedAt
	v["updated_at"] = p.UpdatedAt
	return v
}

// productSnapshot 是审计快照:显式挑字段,不把整行(含 Id)序列化进去。
func productSnapshot(p *Product) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"product_no": p.ProductNo, "kind": p.Kind, "title": p.Title, "price": p.Price,
		"stock": p.Stock, "sold": p.Sold, "per_user_limit": p.PerUserLimit,
		"sale_start_at": p.SaleStartAt, "sale_end_at": p.SaleEndAt,
		"enabled": p.Enabled, "sort_order": p.SortOrder, "plan_id": p.PlanId, "cover_ref": p.CoverRef,
	}
}

// productIdsOf 抽出一批商品的 id,供分组统计。
func productIdsOf(rows []Product) []int64 {
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].Id)
	}
	return ids
}

// codeStockView 是一行码库存下发给管理端的形状。
//
// 显式挑字段,**不含任何密文列**:CodeStock 上的 code_cipher / code_nonce /
// key_version 虽然带 json:"-",但把整行交给序列化器等于把"不泄漏"这件事托付给
// 三个 tag —— 而 secret_guard 守的是包内引用,守不住 tag 被谁删掉。
func codeStockView(row *CodeStock, orderNo, takerName string) gin.H {
	return gin.H{
		"id":         row.Id,
		"status":     row.Status,
		"order_no":   orderNo,
		"created_at": row.CreatedAt,
		"issued_at":  row.IssuedAt,
		"taken_at":   row.TakenAt,
		"taken_by":   row.TakenBy,
		"taken_name": takerName,
	}
}

// orderNosByIds 把一页码库存行上的 order_id 批量换成单号。
//
// 换而不是直下发:order_id 是内部自增 id,可枚举,而单号才是这个系统里对外说
// "哪一张单"的那个词(前端订单页也只认单号)。
func orderNosByIds(gdb *gorm.DB, rows []CodeStock) (map[int64]string, error) {
	out := make(map[int64]string, len(rows))
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		if rows[i].OrderId > 0 {
			ids = append(ids, rows[i].OrderId)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	orders := make([]Order, 0, len(ids))
	if err := gdb.Model(&Order{}).Select("id", "order_no").Where("id IN ?", ids).
		Find(&orders).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取兑换码对应的订单", err)
	}
	for i := range orders {
		out[orders[i].Id] = orders[i].OrderNo
	}
	return out, nil
}

// adminNamesOf 把一页码库存行上的提卡人 id 批量换成用户名(主库)。
//
// Unscoped:提走码的那个管理员账号可能已经被删,但"当初是谁提走的"必须还能显示 ——
// 一个只剩数字 id 的去向记录在事后追问时等于没有。主库不可用时退化成空名字而不是
// 整个列表失败:码库存本身在扩展库里,读得到。
func adminNamesOf(ctx context.Context, rows []CodeStock) map[int]string {
	out := make(map[int]string, len(rows))
	if model.DB == nil {
		return out
	}
	ids := make([]int, 0, len(rows))
	for i := range rows {
		if rows[i].TakenBy > 0 {
			ids = append(ids, rows[i].TakenBy)
		}
	}
	if len(ids) == 0 {
		return out
	}
	users := make([]model.User, 0, len(ids))
	if err := model.DB.WithContext(ctx).Unscoped().Model(&model.User{}).
		Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		common.SysError("qianye/mall: 读取提卡管理员用户名失败: " + err.Error())
		return out
	}
	for _, u := range users {
		out[u.Id] = u.Username
	}
	return out
}
