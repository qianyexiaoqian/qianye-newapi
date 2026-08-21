package controller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---- Shared types ----

type SubscriptionPlanDTO struct {
	Plan model.SubscriptionPlan `json:"plan"`
}

type SubscriptionBalancePayRequest struct {
	PlanId int `json:"plan_id"`
}

// ---- User APIs ----

func GetSubscriptionPlans(c *gin.Context) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		common.ApiSuccess(c, []SubscriptionPlanDTO{})
		return
	}

	var plans []model.SubscriptionPlan
	if err := model.DB.Where("enabled = ?", true).Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	result := make([]SubscriptionPlanDTO, 0, len(plans))
	for _, p := range plans {
		p.NormalizeDefaults()
		result = append(result, SubscriptionPlanDTO{
			Plan: p,
		})
	}
	common.ApiSuccess(c, result)
}

// GetSubscriptionSelf 下发当前用户的订阅列表。
//
// 曾经还下发一个 billing_preference(每用户可改的扣费顺序)。扣费顺序现在写死为
// 「套餐有余额且本次用得上就扣套餐,否则扣钱包」,不再是一个设置,因此这个字段
// 连同 PUT /api/subscription/self/preference 一起去掉了。
func GetSubscriptionSelf(c *gin.Context) {
	userId := c.GetInt("id")

	// Get all subscriptions (including expired)
	allSubscriptions, err := model.GetAllUserSubscriptions(userId)
	if err != nil {
		allSubscriptions = []model.SubscriptionSummary{}
	}

	// Get active subscriptions for backward compatibility
	activeSubscriptions, err := model.GetAllActiveUserSubscriptions(userId)
	if err != nil {
		activeSubscriptions = []model.SubscriptionSummary{}
	}

	common.ApiSuccess(c, gin.H{
		"subscriptions":     activeSubscriptions, // all active subscriptions
		"all_subscriptions": allSubscriptions,    // all subscriptions including expired
	})
}

func SubscriptionRequestBalancePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	userId := c.GetInt("id")
	var req SubscriptionBalancePayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	if err := model.PurchaseSubscriptionWithBalance(userId, req.PlanId); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// ---- Admin APIs ----

// 套餐写接口的审计动作标识。稳定英文串,与 qy_audit_logs.action 一一对应。
const (
	auditActionPlanCreate = "subscription.plan.create"
	auditActionPlanUpdate = "subscription.plan.update"
	auditActionPlanStatus = "subscription.plan.status"
)

// planAuditSnapshot 冻结一份"这个套餐此刻能不能被买、按什么价、买到什么"的快照。
//
// 只取决定资金与可售性的字段,不是整行 dump:整行里混着一堆展示字段,
// 每次改标题都会让 before/after 长得像一次大改动,真正要看的那一格反而找不到。
func planAuditSnapshot(p *model.SubscriptionPlan) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":                    p.Id,
		"title":                 p.Title,
		"enabled":               p.Enabled,
		"price_amount":          p.PriceAmount,
		"currency":              p.Currency,
		"duration_unit":         p.DurationUnit,
		"duration_value":        p.DurationValue,
		"custom_seconds":        p.CustomSeconds,
		"sale_start_at":         p.SaleStartAt,
		"sale_end_at":           p.SaleEndAt,
		"no_quota":              p.NoQuota,
		"total_amount":          p.TotalAmount,
		"max_purchase_per_user": p.MaxPurchasePerUser,
		"upgrade_group":         p.UpgradeGroup,
		"downgrade_group":       p.DowngradeGroup,
		"quota_reset_period":    p.QuotaResetPeriod,
	}
}

// writePlanAudit 是套餐写接口的唯一审计出口,成功与失败共用同一条路径。
//
// # 为什么这一组接口必须留痕
//
// enabled 是手动上下架,sale_start_at / sale_end_at 是到点自动上下架 —— 三者
// 合起来决定"此刻谁能付款"。改错任何一格的表现都是"套餐从货架上消失了"或
// "已经该停售的套餐还在收钱",而接口本身照常 200、界面照常渲染、没有任何报错。
// 在这条埋点之前,整个 controller/subscription.go 零审计:改动落库即生效,
// 事后连"谁在什么时候把停售时间提前了"都无从查起,而同一站点的佣金侧
// 每一次配置写入都有审计 —— 资金相关配置面上两套标准。
//
// 校验失败(400)不在这里留痕:那一类没有产生任何存储副作用,由请求台账覆盖,
// 与 withdraw/handleUploadProof 的下界取 1 同理。
func writePlanAudit(c *gin.Context, action string, result string, reason string, before, after *model.SubscriptionPlan) {
	// 两个快照必须显式留成 any(nil) 而不是直接塞 planAuditSnapshot 的返回值:
	// 一个 nil map 装进 any 之后不等于 nil(接口带着类型),audit.snapshotJSON
	// 的 `v == nil` 分支会漏过去,新建操作的 before 就会落成字面量 "null" ——
	// 与"这一格真的被改成了 null"在详情页上无法区分。
	var beforeSnap, afterSnap any
	if before != nil {
		beforeSnap = planAuditSnapshot(before)
	}
	if after != nil {
		afterSnap = planAuditSnapshot(after)
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: action,
		Result: result,
		Reason: reason,
		Before: beforeSnap,
		After:  afterSnap,
	})
}

func AdminListSubscriptionPlans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	if err := model.DB.Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	result := make([]SubscriptionPlanDTO, 0, len(plans))
	for _, p := range plans {
		p.NormalizeDefaults()
		result = append(result, SubscriptionPlanDTO{
			Plan: p,
		})
	}
	common.ApiSuccess(c, result)
}

type AdminUpsertSubscriptionPlanRequest struct {
	Plan model.SubscriptionPlan `json:"plan"`
}

func AdminCreateSubscriptionPlan(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req AdminUpsertSubscriptionPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	req.Plan.Id = 0
	if strings.TrimSpace(req.Plan.Title) == "" {
		common.ApiErrorMsg(c, "套餐标题不能为空")
		return
	}
	if req.Plan.PriceAmount < 0 {
		common.ApiErrorMsg(c, "价格不能为负数")
		return
	}
	if req.Plan.PriceAmount > 9999 {
		common.ApiErrorMsg(c, "价格不能超过9999")
		return
	}
	if req.Plan.Currency == "" {
		req.Plan.Currency = "USD"
	}
	req.Plan.Currency = "USD"
	if req.Plan.AllowBalancePay == nil {
		req.Plan.AllowBalancePay = common.GetPointer(true)
	}
	if req.Plan.AllowWalletOverflow == nil {
		req.Plan.AllowWalletOverflow = common.GetPointer(true)
	}
	if req.Plan.DurationUnit == "" {
		req.Plan.DurationUnit = model.SubscriptionDurationMonth
	}
	// 永久档不用填时长:补一个默认的 1 只会在管理端显示成「永久 · 1 个月」。
	if req.Plan.DurationValue <= 0 &&
		req.Plan.DurationUnit != model.SubscriptionDurationCustom &&
		req.Plan.DurationUnit != model.SubscriptionDurationPermanent {
		req.Plan.DurationValue = 1
	}
	if req.Plan.MaxPurchasePerUser < 0 {
		common.ApiErrorMsg(c, "购买上限不能为负数")
		return
	}
	if req.Plan.TotalAmount < 0 {
		common.ApiErrorMsg(c, "总额度不能为负数")
		return
	}
	if err := model.ValidatePlanSaleWindow(req.Plan.SaleStartAt, req.Plan.SaleEndAt); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	req.Plan.UpgradeGroup = strings.TrimSpace(req.Plan.UpgradeGroup)
	req.Plan.DowngradeGroup = strings.TrimSpace(req.Plan.DowngradeGroup)
	if err := validatePlanUserGroups(req.Plan.UpgradeGroup, req.Plan.DowngradeGroup); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	req.Plan.QuotaResetPeriod = model.NormalizeResetPeriod(req.Plan.QuotaResetPeriod)
	if req.Plan.QuotaResetPeriod == model.SubscriptionResetCustom && req.Plan.QuotaResetCustomSeconds <= 0 {
		common.ApiErrorMsg(c, "自定义重置周期需大于0秒")
		return
	}
	err := model.DB.Create(&req.Plan).Error
	if err != nil {
		writePlanAudit(c, auditActionPlanCreate, qymodel.ResultFail, err.Error(), nil, &req.Plan)
		common.ApiError(c, err)
		return
	}
	model.InvalidateSubscriptionPlanCache(req.Plan.Id)
	writePlanAudit(c, auditActionPlanCreate, qymodel.ResultOK, "", nil, &req.Plan)
	common.ApiSuccess(c, req.Plan)
}

func AdminUpdateSubscriptionPlan(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		common.ApiErrorMsg(c, "无效的ID")
		return
	}
	var req AdminUpsertSubscriptionPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if strings.TrimSpace(req.Plan.Title) == "" {
		common.ApiErrorMsg(c, "套餐标题不能为空")
		return
	}
	if req.Plan.PriceAmount < 0 {
		common.ApiErrorMsg(c, "价格不能为负数")
		return
	}
	if req.Plan.PriceAmount > 9999 {
		common.ApiErrorMsg(c, "价格不能超过9999")
		return
	}
	req.Plan.Id = id
	if req.Plan.Currency == "" {
		req.Plan.Currency = "USD"
	}
	req.Plan.Currency = "USD"
	if req.Plan.DurationUnit == "" {
		req.Plan.DurationUnit = model.SubscriptionDurationMonth
	}
	// 永久档不用填时长:补一个默认的 1 只会在管理端显示成「永久 · 1 个月」。
	if req.Plan.DurationValue <= 0 &&
		req.Plan.DurationUnit != model.SubscriptionDurationCustom &&
		req.Plan.DurationUnit != model.SubscriptionDurationPermanent {
		req.Plan.DurationValue = 1
	}
	if req.Plan.MaxPurchasePerUser < 0 {
		common.ApiErrorMsg(c, "购买上限不能为负数")
		return
	}
	if req.Plan.TotalAmount < 0 {
		common.ApiErrorMsg(c, "总额度不能为负数")
		return
	}
	if err := model.ValidatePlanSaleWindow(req.Plan.SaleStartAt, req.Plan.SaleEndAt); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	req.Plan.UpgradeGroup = strings.TrimSpace(req.Plan.UpgradeGroup)
	req.Plan.DowngradeGroup = strings.TrimSpace(req.Plan.DowngradeGroup)
	if err := validatePlanUserGroups(req.Plan.UpgradeGroup, req.Plan.DowngradeGroup); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	req.Plan.QuotaResetPeriod = model.NormalizeResetPeriod(req.Plan.QuotaResetPeriod)
	if req.Plan.QuotaResetPeriod == model.SubscriptionResetCustom && req.Plan.QuotaResetCustomSeconds <= 0 {
		common.ApiErrorMsg(c, "自定义重置周期需大于0秒")
		return
	}

	// Before 快照直接读主库而不是走 getSubscriptionPlanByIdTx:后者会命中
	// 300 秒的套餐缓存,拿到的可能是上一次改动之前的样子,而审计的 before
	// 必须是"这次写入真正覆盖掉的那一行"。查不到行时留空快照 —— 对不存在的
	// id 调用本接口在改动前就是静默成功(Updates 影响 0 行),不在这里改行为。
	var before model.SubscriptionPlan
	beforePtr := &before
	if err := model.DB.Where("id = ?", id).First(&before).Error; err != nil {
		beforePtr = nil
	}

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		// update plan (allow zero values updates with map)
		updateMap := map[string]interface{}{
			"title":          req.Plan.Title,
			"subtitle":       req.Plan.Subtitle,
			"price_amount":   req.Plan.PriceAmount,
			"currency":       req.Plan.Currency,
			"duration_unit":  req.Plan.DurationUnit,
			"duration_value": req.Plan.DurationValue,
			"custom_seconds": req.Plan.CustomSeconds,
			"enabled":        req.Plan.Enabled,
			"sort_order":     req.Plan.SortOrder,
			// 两列恒在 map 里,所以「把已配的发售/停售时间清回不限制」是做得到的。
			// 走 Updates(struct) 的话 0 会被当成零值跳过,运营再也取消不了停售时间。
			"sale_start_at":              req.Plan.SaleStartAt,
			"sale_end_at":                req.Plan.SaleEndAt,
			"stripe_price_id":            req.Plan.StripePriceId,
			"creem_product_id":           req.Plan.CreemProductId,
			"waffo_pancake_product_id":   req.Plan.WaffoPancakeProductId,
			"max_purchase_per_user":      req.Plan.MaxPurchasePerUser,
			"no_quota":                   req.Plan.NoQuota,
			"total_amount":               req.Plan.TotalAmount,
			"upgrade_group":              req.Plan.UpgradeGroup,
			"downgrade_group":            req.Plan.DowngradeGroup,
			"quota_reset_period":         req.Plan.QuotaResetPeriod,
			"quota_reset_custom_seconds": req.Plan.QuotaResetCustomSeconds,
			"updated_at":                 common.GetTimestamp(),
		}
		if req.Plan.AllowBalancePay != nil {
			updateMap["allow_balance_pay"] = *req.Plan.AllowBalancePay
		}
		if req.Plan.AllowWalletOverflow != nil {
			updateMap["allow_wallet_overflow"] = *req.Plan.AllowWalletOverflow
		}
		if err := tx.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Updates(updateMap).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		writePlanAudit(c, auditActionPlanUpdate, qymodel.ResultFail, err.Error(), beforePtr, &req.Plan)
		common.ApiError(c, err)
		return
	}
	model.InvalidateSubscriptionPlanCache(id)
	writePlanAudit(c, auditActionPlanUpdate, qymodel.ResultOK, "", beforePtr, &req.Plan)
	common.ApiSuccess(c, nil)
}

type AdminUpdateSubscriptionPlanStatusRequest struct {
	Enabled *bool `json:"enabled"`
}

func AdminUpdateSubscriptionPlanStatus(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		common.ApiErrorMsg(c, "无效的ID")
		return
	}
	var req AdminUpdateSubscriptionPlanStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	var before model.SubscriptionPlan
	beforePtr := &before
	if err := model.DB.Where("id = ?", id).First(&before).Error; err != nil {
		beforePtr = nil
	}
	after := before
	after.Id = id
	after.Enabled = *req.Enabled

	if err := model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Update("enabled", *req.Enabled).Error; err != nil {
		writePlanAudit(c, auditActionPlanStatus, qymodel.ResultFail, err.Error(), beforePtr, &after)
		common.ApiError(c, err)
		return
	}
	model.InvalidateSubscriptionPlanCache(id)
	writePlanAudit(c, auditActionPlanStatus, qymodel.ResultOK, "", beforePtr, &after)
	common.ApiSuccess(c, nil)
}

type AdminBindSubscriptionRequest struct {
	UserId int `json:"user_id"`
	PlanId int `json:"plan_id"`
}

func AdminBindSubscription(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req AdminBindSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserId <= 0 || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	// 绑定套餐等于免费发一份付费权益（额度、分组、并发档）。上游给
	// 「管理员管理用户」装了 canManageTargetRole，却漏了这条同样能给用户
	// 发钱的路径 —— 对 role=10 来说它同时挡住自己和同级。
	if requireManageableUser(c, req.UserId) {
		return
	}
	msg, err := model.AdminBindSubscription(req.UserId, req.PlanId, "")
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

// ---- Admin: user subscription management ----

func AdminListUserSubscriptions(c *gin.Context) {
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	subs, err := model.GetAllUserSubscriptions(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, subs)
}

type AdminCreateUserSubscriptionRequest struct {
	PlanId int `json:"plan_id"`
}

type AdminResetSubscriptionRequest struct {
	PlanId           int   `json:"plan_id"`
	AdvanceResetTime *bool `json:"advance_reset_time"`
}

func resolveAdvanceResetTime(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func recordSubscriptionResetUserLogs(result *model.SubscriptionResetResult, adminInfo map[string]interface{}) {
	if result == nil || result.ResetCount == 0 {
		return
	}
	content := fmt.Sprintf("管理员重置订阅套餐 %s（ID: %d）额度", result.PlanTitle, result.PlanId)
	for _, userId := range result.AffectedUserIds {
		model.RecordLogWithAdminInfo(userId, model.LogTypeManage, content, adminInfo)
	}
}

// AdminCreateUserSubscription creates a new user subscription from a plan (no payment).
func AdminCreateUserSubscription(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	var req AdminCreateUserSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	// 与 AdminBindSubscription 同一件事，只是目标走路径参数，判据必须一致。
	if requireManageableUser(c, userId) {
		return
	}
	msg, err := model.AdminBindSubscription(userId, req.PlanId, "")
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

func AdminResetUserSubscriptionsByPlan(c *gin.Context) {
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	var req AdminResetSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	// 重置把这个人这条套餐的已用量清回 0，也就是再送一轮额度；
	// advance_reset_time 还会把下一次自动重置的时刻一起往前推。
	// 与绑定同属「给这个用户发钱」，判据必须一致。
	if requireManageableUser(c, userId) {
		return
	}
	advanceResetTime := resolveAdvanceResetTime(req.AdvanceResetTime)
	result, err := model.AdminResetUserSubscriptionsByPlan(userId, req.PlanId, advanceResetTime)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordSubscriptionResetUserLogs(result, auditOperatorInfo(c))
	recordManageAuditFor(c, userId, "subscription.user_plan_reset", map[string]interface{}{
		"target_user_id":     userId,
		"plan_id":            result.PlanId,
		"plan_title":         result.PlanTitle,
		"reset_count":        result.ResetCount,
		"user_count":         result.UserCount,
		"advance_reset_time": result.AdvanceResetTime,
	})
	common.ApiSuccess(c, result)
}

// subscriptionActorOf 取「这次订阅管理动作是谁发起的」。
//
// 与 withdraw 的 actorOf 同一个概念:目标不是报文里的某个 user_id 而是一整批人时,
// 判据只能下沉到数据层逐行套用,而数据层看不到 gin.Context —— 这个函数就是那道
// 桥。它也是 qianye/actor_gate_guard_test.go 能断言「整盘重置真的把操作人传下去了」
// 的锚点:内联成一个 composite literal 的话,AST 守卫扫不到调用,断链不会变红。
func subscriptionActorOf(c *gin.Context) model.SubscriptionActor {
	return model.SubscriptionActor{UserId: c.GetInt("id"), Role: c.GetInt("role")}
}

func AdminResetPlanSubscriptions(c *gin.Context) {
	planId, _ := strconv.Atoi(c.Param("id"))
	if planId <= 0 {
		common.ApiErrorMsg(c, "无效的ID")
		return
	}
	var req AdminResetSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	advanceResetTime := resolveAdvanceResetTime(req.AdvanceResetTime)
	// 整盘重置 = 给每一个持有者「再送一轮额度」。按人那条路上有
	// requireManageableUser，这条路上原先一道判据都没有，于是 role=10 只要自己
	// 名下有这张套餐，一次调用就把**自己**的已用量清零(自益)，顺带动了
	// role=100。把同一条判据逐行套上去：管不着的人跳过并在响应里报出条数。
	result, err := model.AdminResetPlanSubscriptions(planId, advanceResetTime, subscriptionActorOf(c))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordSubscriptionResetUserLogs(result, auditOperatorInfo(c))
	common.SysLog(fmt.Sprintf("admin reset subscription plan %d quota: reset_count=%d user_count=%d skipped=%d advance_reset_time=%t",
		result.PlanId, result.ResetCount, result.UserCount, result.SkippedCount, result.AdvanceResetTime))
	recordManageAudit(c, "subscription.plan_reset", map[string]interface{}{
		"plan_id":            result.PlanId,
		"plan_title":         result.PlanTitle,
		"reset_count":        result.ResetCount,
		"user_count":         result.UserCount,
		"skipped_count":      result.SkippedCount,
		"affected_user_ids":  result.AffectedUserIds,
		"advance_reset_time": result.AdvanceResetTime,
	})
	common.ApiSuccess(c, result)
}

// AdminInvalidateUserSubscription cancels a user subscription immediately.
func AdminInvalidateUserSubscription(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorMsg(c, "无效的订阅ID")
		return
	}
	// 作废是纯损害动作:把一条已生效(可能是真金白银买的)订阅立刻取消,
	// 并把对方的用户分组打回默认组。目标不在报文里,而在订阅行的归属人上,
	// 所以必须先回查归属人再过 canManageTargetRole —— 与 relations/unbind 同形。
	ownerId, err := model.GetUserSubscriptionOwner(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if requireManageableUser(c, ownerId) {
		return
	}
	msg, err := model.AdminInvalidateUserSubscription(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, ownerId, "subscription.user_subscription_invalidate", map[string]interface{}{
		"target_user_id":       ownerId,
		"user_subscription_id": subId,
	})
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func AdminDeleteUserSubscription(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorMsg(c, "无效的订阅ID")
		return
	}
	// 硬删除比作废更狠:行没了之后连「谁的订阅被谁删了」都无从回答
	// (source=admin / 兑换码来源的订阅连订单都没有)。同一条归属人判据。
	ownerId, err := model.GetUserSubscriptionOwner(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if requireManageableUser(c, ownerId) {
		return
	}
	recordManageAuditFor(c, ownerId, "subscription.user_subscription_delete", map[string]interface{}{
		"target_user_id":       ownerId,
		"user_subscription_id": subId,
	})
	msg, err := model.AdminDeleteUserSubscription(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

// validatePlanUserGroups 校验套餐的升级 / 降级分组。
//
// ── 这里原本是一个纯 bug ──
//
// upgrade_group / downgrade_group 写进的是 **users.group**(购买套餐时把用户搬到
// 那个用户分组,到期再搬回来),但校验用的是 ratio_setting.GetGroupRatioCopy(),
// 也就是**模型分组**的清单。两者是两个不同的命名空间,只是碰巧共用一套字符串:
//
//	一个真实存在、但没配过倍率的用户分组  → 被拒绝(「升级分组不存在」)
//	一个纯粹的模型分组(没有任何用户)     → 被接受,然后把用户搬进一个没人的分组
//
// 改用 users.group 的 distinct 清单。查库失败时**放行**:这条校验只是防手误,
// 而挡住一次合法的套餐编辑比放过一个错字更贵 —— 真正的搬迁在购买时才发生。
//
// ── 为什么清单不能只有 users.group ──
//
// 「有人在用」与「合法」不是同一件事。upgrade_group 的语义是"买了这个套餐就把
// 用户搬到哪一档",而**一个还没有任何人的新档次天然不在 users.group 里** ——
// 只认 users.group 会让"为一个新档次创建套餐"这件事在结构上不可能完成:
// 运营新建「年费 SVIP」并填 upgrade_group=svip,接口报「svip 不是一个用户分组」,
// 唯一的绕法是先手工把某个真人改成 svip 再回来建套餐,一个纯粹的先有鸡还是先有蛋。
// 同一段逻辑还会误伤无关编辑:一个只剩 0 个用户的历史分组会让"改这个套餐的价格"
// 也被拒,而错误文案指向分组,与运营正在做的事无关。
//
// 所以清单是三者的并集:
//
//	users.group 的 distinct   已经有人在用的档
//	qy_user_groups 登记表      已声明、暂时无人的档(扩展未启用时为空)
//	现存套餐已经在用的值        存量行必须能被继续编辑,否则改标题都会被拒
func validatePlanUserGroups(upgradeGroup, downgradeGroup string) error {
	if upgradeGroup == "" && downgradeGroup == "" {
		return nil
	}
	names, err := model.QyDistinctUserGroups()
	if err != nil {
		common.SysError("校验套餐升降级分组时读取用户分组清单失败(已放行): " + err.Error())
		return nil
	}
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	for _, name := range service.QyDeclaredUserGroups() {
		known[name] = true
	}
	inUse, err := model.QyPlanUserGroupsInUse()
	if err != nil {
		common.SysError("校验套餐升降级分组时读取存量套餐分组失败(已放行): " + err.Error())
		return nil
	}
	for _, name := range inUse {
		known[name] = true
	}
	if upgradeGroup != "" && !known[upgradeGroup] {
		return errors.New("升级分组 " + upgradeGroup + " 不是一个用户分组(它必须是 users.group 里真实存在的值,或已在分组登记表里声明过)")
	}
	if downgradeGroup != "" && !known[downgradeGroup] {
		return errors.New("降级分组 " + downgradeGroup + " 不是一个用户分组(它必须是 users.group 里真实存在的值,或已在分组登记表里声明过)")
	}
	return nil
}

// GetSubscriptionPurchasePreview 在下单**之前**告诉用户这次购买会发生什么。
//
// ═══════════════════════ 为什么必须有这个接口 ═══════════════════════
//
// 用户组商品的跨组购买会把旧组剩余的时间**直接作废**,不折算、不退款
// (项目方 2026-08-14 拍板)。这是不可逆的,所以它必须在用户付钱**之前**就
// 写在屏幕上 —— 事后再解释等于让客服替一次静默的资损背书。
//
// 只读、不写、不锁:它跑在用户浏览商品的路径上,而不是下单路径上。
// 真正的执行判据在 model.applyUserGroupPurchaseRulesTx 里,两者判据同源
// (都看 upgrade_group 与 SubscriptionActiveEndTimeSQL),但生命周期不同 ——
// 预览会被反复刷,执行只能发生一次且必须在事务里带行锁。
//
// 普通套餐(upgrade_group 为空)返回 action=new,前端据此不弹任何额外确认。
func GetSubscriptionPurchasePreview(c *gin.Context) {
	planId, err := strconv.Atoi(c.Param("id"))
	if err != nil || planId <= 0 {
		common.ApiErrorMsg(c, "无效的套餐 id")
		return
	}
	plan, err := model.GetSubscriptionPlanById(planId)
	if err != nil || plan == nil {
		common.ApiErrorMsg(c, "套餐不存在")
		return
	}
	if !plan.Enabled {
		common.ApiErrorMsg(c, "该套餐已下架")
		return
	}
	// 未开售 / 已停售同样在预览这一步就说清楚。预览是购买弹窗打开时就发的,
	// 让"买不了"在按下付款之前出现,而不是等到网关下单接口把它退回来。
	if err := model.PlanSaleWindowError(plan, common.GetTimestamp()); err != nil {
		common.ApiError(c, err)
		return
	}
	preview, err := model.PreviewUserGroupPurchase(c.GetInt("id"), plan)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, preview)
}
