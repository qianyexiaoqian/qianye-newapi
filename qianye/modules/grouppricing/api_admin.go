package grouppricing

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/shopspring/decimal"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin.go —— 规则 CRUD、最终生效价试算、影子差额对账。
//
// 每一次写操作都必须留下 before/after 审计:这张表决定每一笔请求扣多少钱,
// 事后"为什么这个分组这个月账单翻倍了"只能靠它来回答。审计写在与业务同一个
// 扩展库事务里 —— 价格改了却没有审计记录,是不可接受的。

const auditCategoryGroupPricing = "group_pricing"

// ruleUpsertReq 是规则的新建/编辑入参。
//
// Value 走字符串而不是 JSON number:前端的 number 是 float64,0.1 往返一次
// 就可能变成 0.10000000000000001,而它会被直接乘进用户的账单。
type ruleUpsertReq struct {
	GroupName string `json:"group_name"`
	ModelName string `json:"model_name"`
	Mode      string `json:"mode"`
	Value     string `json:"value"`
	Enabled   bool   `json:"enabled"`
	Remark    string `json:"remark"`
}

func (r *ruleUpsertReq) apply(dst *Rule) error {
	group := normalizeGroup(r.GroupName)
	if len(group) > maxGroupNameLen {
		return fmt.Errorf("分组名超过 %d 字节", maxGroupNameLen)
	}
	modelName := strings.TrimSpace(r.ModelName)
	if modelName == "" {
		return fmt.Errorf("模型名不能为空(填 * 表示该分组下全部模型)")
	}
	if len(modelName) > maxModelNameLen {
		return fmt.Errorf("模型名超过 %d 字节", maxModelNameLen)
	}
	value, err := decimal.NewFromString(strings.TrimSpace(r.Value))
	if err != nil {
		return fmt.Errorf("value 不是合法数值: %q", r.Value)
	}
	// 与快照编译共用同一份判定。两处各写一套,严的那一处迟早会形同虚设。
	if err := ValidateValue(r.Mode, value); err != nil {
		return err
	}

	dst.GroupName = group
	dst.ModelName = modelName
	dst.Mode = r.Mode
	dst.Value = value
	dst.Enabled = r.Enabled
	dst.Remark = truncate(r.Remark, maxRemarkLen)
	return nil
}

// ruleView 是规则的对外形态:规则本身 + 折算后的最终生效价。
//
// effective 不是可选字段。用户选的是"分组级价 × 分组倍率"的相乘方案,
// 运营在输入框里填的那个数不是最终价;不把折算结果直接摆在同一行,
// 这套 UI 就是在鼓励人算错价。
type ruleView struct {
	Rule
	Effective Effective `json:"effective"`
}

func viewOf(r Rule) ruleView {
	return ruleView{Rule: r, Effective: computeEffective(r.GroupName, r.ModelName, r.Mode, r.Value)}
}

func adminListRules(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupPricing) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Rule{})
	if v := strings.TrimSpace(c.Query("group_name")); v != "" {
		q = q.Where("group_name = ?", normalizeGroup(v))
	}
	if v := strings.TrimSpace(c.Query("model_name")); v != "" {
		q = q.Where("model_name LIKE ?", "%"+v+"%")
	}
	if v := strings.TrimSpace(c.Query("mode")); v != "" {
		q = q.Where("mode = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []Rule
	if err := q.Order("group_name asc, model_name asc, id asc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	views := make([]ruleView, 0, len(rows))
	for _, r := range rows {
		views = append(views, viewOf(r))
	}
	respond(c, gin.H{
		"items": views,
		"total": total,
		// shadow_mode 必须跟着列表一起返回:同一张列表在影子模式下是"预演",
		// 在真实模式下是"正在扣的钱",两者看起来一模一样。
		"shadow_mode": config.Get().GroupPricing.IsShadow(),
	})
}

func adminCreateRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupPricing) {
		return
	}
	var req ruleUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	row := &Rule{}
	if err := req.apply(row); err != nil {
		badRequest(c, err.Error())
		return
	}

	var count int64
	if err := db.Get().Model(&Rule{}).Count(&count).Error; err != nil {
		internalError(c, err)
		return
	}
	if count >= int64(config.Get().GroupPricing.MaxRules) {
		badRequest(c, fmt.Sprintf("规则数量已达上限 %d(group_pricing.max_rules),请先清理不再使用的规则",
			config.Get().GroupPricing.MaxRules))
		return
	}

	now := common.GetTimestamp()
	row.CreatedAt, row.UpdatedAt = now, now
	row.CreatedBy, row.UpdatedBy = c.GetInt("id"), c.GetInt("id")

	err := db.Get().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		if err := bumpVersion(tx); err != nil {
			return err
		}
		return audit.WriteTx(tx, ruleAuditEntry(c, "create", nil, row))
	})
	if err != nil {
		writeRuleFailure(c, "create", nil, row, err)
		if isDuplicateRule(err) {
			badRequest(c, "该分组下该模型已存在规则,请直接编辑那一条")
			return
		}
		internalError(c, err)
		return
	}
	refreshAfterWrite()
	respond(c, viewOf(*row))
}

func adminUpdateRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupPricing) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "id 非法")
		return
	}
	var req ruleUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}

	// before/loaded 提到事务之外:失败审计要在回滚之后才写,
	// 那时闭包里的局部变量已经不可见,而"改之前是什么"正是那条审计的一半价值。
	var before, updated Rule
	loaded := false
	err := db.Get().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).Take(&before).Error; err != nil {
			return err
		}
		loaded = true
		after := before
		if err := req.apply(&after); err != nil {
			return err
		}
		after.UpdatedAt = common.GetTimestamp()
		after.UpdatedBy = c.GetInt("id")
		if err := tx.Save(&after).Error; err != nil {
			return err
		}
		if err := bumpVersion(tx); err != nil {
			return err
		}
		updated = after
		return audit.WriteTx(tx, ruleAuditEntry(c, "update", &before, &after))
	})
	if err != nil && loaded {
		// 规则本身不存在(ErrRecordNotFound)时 loaded 为 false,不写审计:
		// 那是一次打空的请求,记进资金审计只会稀释它 —— 请求台账
		// (qy_request_audits)已经记下了这次调用。
		writeRuleFailure(c, "update", &before, nil, err)
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		notFound(c)
		return
	case err != nil && isDuplicateRule(err):
		badRequest(c, "该分组下该模型已存在规则")
		return
	case err != nil && isValidationError(err):
		badRequest(c, err.Error())
		return
	case err != nil:
		internalError(c, err)
		return
	}
	refreshAfterWrite()
	respond(c, viewOf(updated))
}

func adminDeleteRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupPricing) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "id 非法")
		return
	}
	// 同 adminUpdateRule:before 必须活到事务之外,失败审计才拿得到它。
	var before Rule
	loaded := false
	err := db.Get().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).Take(&before).Error; err != nil {
			return err
		}
		loaded = true
		if err := tx.Delete(&Rule{}, id).Error; err != nil {
			return err
		}
		if err := bumpVersion(tx); err != nil {
			return err
		}
		return audit.WriteTx(tx, ruleAuditEntry(c, "delete", &before, nil))
	})
	if err != nil && loaded {
		writeRuleFailure(c, "delete", &before, nil, err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		notFound(c)
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	refreshAfterWrite()
	respond(c, gin.H{"deleted": id})
}

// adminPreview 是只读试算:运营边打边看最终生效价。
//
// 它不落库、不改任何状态,存在的唯一理由是让"× 分组倍率 = 实际扣费"
// 在按下保存之前就摆在眼前。
func adminPreview(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupPricing) {
		return
	}
	var req ruleUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	value, err := decimal.NewFromString(strings.TrimSpace(req.Value))
	if err != nil {
		badRequest(c, fmt.Sprintf("value 不是合法数值: %q", req.Value))
		return
	}
	if err := ValidateValue(req.Mode, value); err != nil {
		badRequest(c, err.Error())
		return
	}
	respond(c, computeEffective(req.GroupName, strings.TrimSpace(req.ModelName), req.Mode, value))
}

// adminShadowSummary 是影子模式的对账接口:按模型/分组/时间聚合出差额。
//
// 它回答的是切换前唯一重要的那个问题:「这个月会多收还是少收多少?」
func adminShadowSummary(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupPricing) {
		return
	}
	now := common.GetTimestamp()
	start := httpq.Int64(c, "start", now-7*86400)
	end := httpq.Int64(c, "end", now)
	sum, err := buildShadowSummary(start, end)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	respond(c, gin.H{
		"summary": sum,
		// 影子模式关闭后这张表停止增长,汇总看起来会"变少"。
		// 不把当前模式一起返回,运营会以为是数据丢了。
		"shadow_mode": config.Get().GroupPricing.IsShadow(),
	})
}

// ─────────────────────────────── 辅助 ───────────────────────────────

// bumpVersion 推进规则版本号,让其它节点在下一个刷新周期感知到本次改动。
//
// 与业务写在同一个事务里:版本号没推进等于改动只对当前节点生效,
// 而"我改了价格但只有一台机器生效"是最难排查的一类计费故障。
func bumpVersion(tx *gorm.DB) error {
	var ver RuleVersion
	err := tx.Where("id = ?", 1).Take(&ver).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&RuleVersion{Id: 1, Version: 1, UpdatedAt: common.GetTimestamp()}).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&RuleVersion{}).Where("id = ?", 1).
		Updates(map[string]interface{}{
			"version":    gorm.Expr("version + 1"),
			"updated_at": common.GetTimestamp(),
		}).Error
}

// refreshAfterWrite 让本节点立刻用上新规则,不等下一个刷新周期。
//
// 其它节点靠版本号轮询感知(最多晚 rule_cache_seconds)。这里刻意用 force=false:
// 版本号已经推进过,普通刷新一定会拉到新数据,而 force 会跳过版本比对、
// 让每次写入都多一次全表扫描。
func refreshAfterWrite() {
	nextRefreshAt.Store(0)
	if err := reload(false); err != nil {
		common.SysError("qianye/grouppricing: 写入后刷新快照失败(其它节点仍会按周期刷新): " + err.Error())
	}
}

// writeRuleFailure 在规则写入事务回滚之后补一条失败审计。
//
// 成功路径的审计写在 WriteTx 里(与规则变更同生共死),这是对的;
// 但它的副作用是**失败路径零留痕** —— 事务一回滚,那条审计跟着消失,
// 于是"有人反复尝试把某个分组的价格改成 0、每次都被唯一索引挡回去"
// 在库里查不到任何痕迹。这条补写必须在事务之外。
func writeRuleFailure(c *gin.Context, action string, before, after *Rule, err error) {
	e := ruleAuditEntry(c, action, before, after)
	e.Result = qymodel.ResultFail
	e.Reason = "分组定价写入失败(事务已回滚): " + err.Error() + " | " + e.Reason
	audit.Write(c, e)
}

func ruleAuditEntry(c *gin.Context, action string, before, after *Rule) audit.Entry {
	e := audit.Entry{
		Category:    auditCategoryGroupPricing,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		BeforeSnap:  ruleSnapshot(before),
		AfterSnap:   ruleSnapshot(after),
	}
	// Reason 是审计列表里唯一一眼能看懂的那一列,必须把"改了什么"写进去,
	// 而不是让人去比对两段 JSON。
	switch {
	case before == nil && after != nil:
		e.Reason = fmt.Sprintf("新增分组价:%s/%s %s=%s", after.GroupName, after.ModelName, after.Mode, after.Value.String())
	case before != nil && after == nil:
		e.Reason = fmt.Sprintf("删除分组价:%s/%s %s=%s", before.GroupName, before.ModelName, before.Mode, before.Value.String())
	case before != nil && after != nil:
		e.Reason = fmt.Sprintf("修改分组价:%s/%s %s %s → %s %s",
			before.GroupName, before.ModelName,
			before.Mode, before.Value.String(), after.Mode, after.Value.String())
	}
	return e
}

func ruleSnapshot(r *Rule) string {
	if r == nil {
		return ""
	}
	b, err := common.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

// isDuplicateRule 判断错误是不是 (group_name, model_name) 唯一索引冲突。
// 只做字符串判定:GORM 不暴露统一的错误码,而扩展库固定是 MySQL。
func isDuplicateRule(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate entry") ||
		strings.Contains(s, "unique constraint") ||
		strings.Contains(s, "unique failed")
}

// isValidationError 区分"入参不合法"(400)与"数据库出错"(500)。
// req.apply 在事务闭包内被调用,它的错误必须原样回给用户而不是被当成内部错误。
func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "覆盖值") || strings.Contains(s, "口径") ||
		strings.Contains(s, "模型名") || strings.Contains(s, "分组名") ||
		strings.Contains(s, "不是合法数值")
}

// ─────────────────────────── HTTP 信封与分页 ───────────────────────────

func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func respondFail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": msg})
}

func badRequest(c *gin.Context, msg string) {
	respondFail(c, http.StatusBadRequest, "qy_gp_bad_request", msg)
}

func notFound(c *gin.Context) {
	respondFail(c, http.StatusNotFound, "qy_gp_not_found", "规则不存在")
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/grouppricing: 接口处理失败: " + err.Error())
	respondFail(c, http.StatusInternalServerError, "qy_internal_error", "处理失败,请稍后重试")
}

// listPaging 是分组定价规则列表的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 页码上限(httpq.MaxPage)是收敛到 httpq 时补上的 —— 这份拷贝原本只夹了页长。
var listPaging = httpq.Spec{}

func pathInt64(c *gin.Context, key string) (int64, bool) {
	return httpq.PathInt64(c, key)
}
