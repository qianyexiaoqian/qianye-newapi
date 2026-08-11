package violation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// api_admin_category.go —— 违规类型的管理端接口:列表、写入、归档。
//
// 每个写动作都写审计,成功与失败同一出口(writeCategoryAudit)。这张表决定
// "某一类累计几次会被处置",与策略档同级 —— 它比规则表更接近"直接改账号状态"。

// categoryUpsertReq 是违规类型的新建/编辑入参。
//
// 没有 is_fallback 字段:兜底类型的身份由**种子**决定,任何请求体都不能声明或
// 撤销它。让请求体能设置 is_fallback 就等于开了两条路 —— 一条能造出第二个兜底
// (于是"没绑类型的规则落到哪里"变成看运气),一条能把兜底降级
// (那就是"删掉兜底类型"的另一种写法,而规则会因此变成孤儿)。
type categoryUpsertReq struct {
	// Id 为 0 表示新建。编辑时 Key 允许改,但改 key 会让所有按 key 绑定的外部
	// 审核来源(见 CategoryByKey)当场失联,所以界面上要给出明确警告。
	Id  int64  `json:"id"`
	Key string `json:"key"`

	Name   string `json:"name"`
	Remark string `json:"remark"`

	PublicTitle string `json:"public_title"`
	PublicDesc  string `json:"public_desc"`
	Published   bool   `json:"published"`

	Enabled     bool `json:"enabled"`
	WindowHours int  `json:"window_hours"`
	Threshold   int  `json:"threshold"`
	SortOrder   int  `json:"sort_order"`

	// Confirm 是二次确认位,与策略档同口径:收紧类型阈值会**立刻**把一批存量账号
	// 推到越线状态,他们会在各自下一次命中时被处置。服务端不信任前端弹没弹窗,
	// 它信任的是"这一位只可能由那次交互产生"。
	Confirm bool `json:"confirm"`
}

var (
	errFallbackCategoryUndeletable = errors.New(
		"「未分类」是兜底类型,不可归档:归档它会让没有显式类型的规则变成无类型的孤儿")
	errFallbackCategoryImmutableKey = errors.New("「未分类」兜底类型的标识不可修改")
)

// adminListCategories 返回全部类型 + 每一类当前绑着多少条规则。
//
// 规则条数必须一起给:管理员要归档一个类型时,第一个问题就是"它下面还有几条规则"。
// 让界面自己去规则列表数一遍会在类型多起来之后变成 N+1 次请求,而这个数在
// 归档确认弹窗里是必须显示的 —— 它决定了那句"这 N 条规则会被改绑到 X"。
func adminListCategories(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	rows := make([]Category, 0, 16)
	// 兜底排最后:它是"还没归类"的落点,不是一个业务类型,排在最前会被当成主类型。
	if err := gdb.Order("is_fallback asc, sort_order asc, id asc").Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}

	// 一次聚合拿到全部类型的规则条数。Unscoped 排除:软删的规则不该算进
	// "归档这一类会影响几条规则"—— 它们已经不生效了。
	type catCount struct {
		CategoryId int64
		N          int64
	}
	counts := make([]catCount, 0, len(rows))
	if err := gdb.Model(&Rule{}).
		Select("category_id as category_id, count(*) as n").
		Group("category_id").Scan(&counts).Error; err != nil {
		internalError(c, err)
		return
	}
	byId := make(map[int64]int64, len(counts))
	for _, v := range counts {
		byId[v.CategoryId] = v.N
	}
	// 没绑类型的规则(category_id=0)在运行期折进兜底类型,这里也必须算进兜底那一行,
	// 否则管理员看到「未分类 0 条规则」而线上正有一批命中记到它头上。
	var fallbackId int64
	for _, r := range rows {
		if r.IsFallback {
			fallbackId = r.Id
		}
	}

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		n := byId[r.Id]
		if r.IsFallback {
			n += byId[0]
		}
		items = append(items, gin.H{"category": r, "rule_count": n})
	}
	respond(c, gin.H{
		"items":       items,
		"fallback_id": fallbackId,
		// 阈值口径必须与用户端公示、与封号判定是同一句话,所以由后端下发而不是
		// 让前端把它写死在文案里 —— 两处各写一份,改一处就会出现"界面说 OR、
		// 实际是 AND"这种没人查得出来的落差。
		"threshold_semantics": "any_line",
	})
}

// adminCategoryImpact 是"把这一类调成这套阈值,现在有多少存量账号已经越线"的只读预览。
//
// 独立成 GET 而不是只在保存时回 409,理由与策略档那条完全一致:管理员需要**在填表的
// 过程中**看到这个数,而不是在按下保存之后。一个只在保存时才出现的数字,会让人先按
// 一次保存去探路 —— 那正是二次确认要防的动作。
//
// (409 仍然保留:它兜的是绕过界面直接调接口的那条路。)
func adminCategoryImpact(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	// id 为 0 表示"还没保存的新类型":它还没有计数行,影响面恒为 0,
	// 但仍然要返回一个结构完整的结果,否则前端得为这一种情况单独写一条分支。
	cat := Category{
		Id:          httpq.Int64(c, "id", 0),
		Enabled:     c.Query("enabled") != "false",
		WindowHours: httpq.Int(c, "window_hours", 24),
		Threshold:   httpq.Int(c, "threshold", 0),
	}
	impact, err := countCategoryImpact(c.Request.Context(), gdb, cat)
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"impact": impact})
}

// adminUpsertCategory 新建或编辑一个违规类型。
func adminUpsertCategory(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var req categoryUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}

	var before Category
	hasBefore := false
	if req.Id > 0 {
		if err := gdb.Where("id = ?", req.Id).Take(&before).Error; err != nil {
			notFound(c)
			return
		}
		hasBefore = true
	}

	next := Category{
		Id:          before.Id,
		Key:         req.Key,
		Name:        req.Name,
		Remark:      req.Remark,
		PublicTitle: req.PublicTitle,
		PublicDesc:  req.PublicDesc,
		Published:   req.Published,
		Enabled:     req.Enabled,
		WindowHours: req.WindowHours,
		Threshold:   req.Threshold,
		SortOrder:   req.SortOrder,
		IsFallback:  before.IsFallback,
		CreatedAt:   before.CreatedAt,
	}
	if hasBefore && before.IsFallback {
		// 兜底类型的 key 是 categoryForRule 与迁移的落点,改掉它等于当场制造一批孤儿。
		// 其余字段(名称、说明、阈值)允许改 —— 站点完全可以给「未分类」配一条线。
		if strings.ToLower(strings.TrimSpace(req.Key)) != before.Key {
			badRequest(c, errFallbackCategoryImmutableKey.Error())
			return
		}
	}
	if err := validateCategory(&next); err != nil {
		badRequest(c, err.Error())
		return
	}

	// key 冲突要给一句人话。靠唯一索引兜的话,管理员看到的是
	// "处理失败,请稍后重试"(internalError 的固定文案),而真正的问题是重名。
	var dup Category
	// `key` 在三种数据库里都是保留字,列名必须由方言层加引号(PostgreSQL 是 "key",
	// MySQL/SQLite 是 `key`)。写死反引号的那一份 SQL 在 PostgreSQL 上直接语法错误。
	dupQ := gdb.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: next.Key})
	if next.Id > 0 {
		dupQ = dupQ.Where("id <> ?", next.Id)
	}
	if err := dupQ.Take(&dup).Error; err == nil {
		badRequest(c, fmt.Sprintf("类型标识 %q 已被「%s」占用,请换一个", next.Key, dup.Name))
		return
	}

	// 收紧判据与策略档同形:阈值变小(含 0 → 正数)、窗口变长、从停用转启用,
	// 三者都会让原本够不到线的账号够到。放宽不拦。
	tightens := categoryTightens(hasBefore, before, next)
	impact, impactErr := countCategoryImpact(c.Request.Context(), gdb, next)
	if impactErr != nil {
		// 与策略档同口径:算不出影响面不能一律 500 —— 事故当天最需要能执行的动作
		// 恰恰是放宽,而"算不出"的原因往往就是当天的慢查询。
		common.SysError("qianye/violation: 违规类型影响面评估失败: " + impactErr.Error())
		if !req.Confirm && tightens {
			respondFailData(c, 409, "confirm_required",
				"无法评估这次调整会让多少存量账号越过这一类的线(影响面查询失败),确认后才会保存",
				gin.H{"impact": impact, "impact_error": true})
			return
		}
	} else if !req.Confirm && tightens && impact.Matched > 0 {
		respondFailData(c, 409, "confirm_required",
			fmt.Sprintf("这次收紧之后已有 %d 个存量账号越过这一类的线,他们会在各自下一次违规命中时按所在分组的处置策略处置"+
				"(保存本身不会处置任何人),请确认后重试", impact.Matched),
			gin.H{"impact": impact})
		return
	}

	now := common.GetTimestamp()
	next.UpdatedAt = now
	next.UpdatedBy = c.GetInt("id")
	if !hasBefore {
		next.CreatedAt = now
	}
	// Save 全字段写:Updates(struct) 会跳过零值,而 threshold=0(这一类不单独触发)、
	// published=false、enabled=false 都是**有意义的取值**,跳过它们等于改不掉。
	if err := gdb.Save(&next).Error; err != nil {
		db.MarkFailure(err)
		writeCategoryAudit(c, "categories.upsert", qymodel.ResultFail, before, next, err)
		internalError(c, err)
		return
	}
	afterCategoryChange()
	writeCategoryAudit(c, "categories.upsert", qymodel.ResultOK, before, next, nil)
	respond(c, gin.H{"category": next, "impact": impact})
}

// adminArchiveCategory 归档一个违规类型。**这不是删除。**
//
// # 三件事必须同时成立,少一件这个动作就是破坏性的
//
//  1. **历史违规记录一行不动。** 那是证据,而且每一行都冻结了类型 id、内部名与
//     公示文案(见 Record 的三列),归档之后仍然能独立解释自己算哪一类。
//     级联删除历史记录是本接口绝不会做的事 —— 它会把申诉复核、退款争议、
//     以及"这个账号为什么被封"的全部依据一次抹掉。
//  2. **规则不能变成孤儿。** 这一类下面的规则会被改绑到 reassign_to
//     (缺省是「未分类」兜底类型),改绑条数在报文里如实返回。
//     不改绑而依赖 categoryForRule 的运行期折叠也能跑,但那会让管理端列表里
//     这些规则显示成一个查不到的 category_id —— 与"写了但找不到"是同一种形状。
//  3. **类型计数行保留。** 归档不清 qy_violation_cat_counter:那些数字是历史事实,
//     而且管理员随时可能把这一类恢复回来(手工把 deleted_at 置空)。
//     它们不再推进,因为类型已经不在快照里。
func adminArchiveCategory(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok || id <= 0 {
		badRequest(c, "非法的类型 id")
		return
	}
	var row Category
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		badRequest(c, "违规类型不存在")
		return
	}
	if row.IsFallback {
		// 这一次拒绝也要留痕:它不是"入参格式不对"那一类,而是一次对着库里某一行
		// 发起的破坏性动作被挡下。事后要回答的问题是"谁试过归档兜底类型"。
		writeCategoryAudit(c, "categories.archive", qymodel.ResultFail, row, Category{},
			errFallbackCategoryUndeletable)
		badRequest(c, errFallbackCategoryUndeletable.Error())
		return
	}

	// 接管类型:显式指定优先,否则落兜底。指定的必须是一个还活着的、不是自己的类型 ——
	// 把规则改绑到另一个正要被归档的类型上,下一次归档就会把它们再推一次,
	// 而每推一次都会换掉这些规则的计数桶。
	// 请求参数的解析一律走 qianye/httpq:strconv 的上界是 MaxInt64,
	// 而上界必须是解析的一部分(见 qianye/httpq_guard_test.go 的解析锁)。
	//
	// "填了但填错"必须报错,不能落进下面的兜底分支:一个拼错的 reassign_to
	// 会让这一类的规则被静默改绑到「未分类」,而管理员以为它们去了自己选的那一类 ——
	// 于是这批规则的命中从此记进另一个计数桶,阈值判定全错。
	var target Category
	if strings.TrimSpace(c.Query("reassign_to")) != "" {
		targetId := httpq.Int64(c, "reassign_to", 0)
		if targetId <= 0 {
			badRequest(c, "非法的接管类型 id")
			return
		}
		if targetId == id {
			badRequest(c, "接管类型不能是正在归档的这一个")
			return
		}
		if err := gdb.Where("id = ?", targetId).Take(&target).Error; err != nil {
			badRequest(c, "接管类型不存在或已归档")
			return
		}
	} else {
		if err := gdb.Where("is_fallback = ?", true).Take(&target).Error; err != nil {
			internalError(c, fmt.Errorf("「未分类」兜底类型缺失,无法接管规则: %w", err))
			return
		}
	}

	moved, err := archiveCategory(gdb, id, target.Id)
	if err != nil {
		db.MarkFailure(err)
		writeCategoryAudit(c, "categories.archive", qymodel.ResultFail, row, target, err)
		internalError(c, err)
		return
	}
	afterCategoryChange()
	writeCategoryAudit(c, "categories.archive", qymodel.ResultOK, row, target, nil)
	respond(c, gin.H{
		"archived":       true,
		"reassigned_to":  target.Id,
		"moved_rules":    moved,
		"records_intact": true,
	})
}

// categoryTightens 判断一次写入是否会扩大处置面。三条判据是 OR,与
// tightensBanPolicy 同形(新建一律算收紧:一个新类型的线对谁都是新加的)。
func categoryTightens(hasBefore bool, before, next Category) bool {
	if !next.Enabled || next.Threshold <= 0 {
		return false // 这一类根本不出线,不可能让任何人越线
	}
	if !hasBefore {
		return true
	}
	if !before.Enabled || before.Threshold <= 0 {
		return true // 从"不出线"变成"出线"
	}
	return next.Threshold < before.Threshold || next.WindowHours > before.WindowHours
}

// categoryImpact 是"把这一类调成这套阈值,有多少存量账号已经越过这条线"。
//
// 语义与 banPolicyImpact 逐字一致(见那里的长注释):它**不是**"这次保存会封掉
// 几个人"——保存只写类型表,已越线的账号会在各自下一次命中时才被处置。
// 报文与界面文案必须照这个语义写。
type categoryImpact struct {
	Matched     int   `json:"matched"`
	Capped      bool  `json:"capped"`
	Threshold   int   `json:"threshold"`
	WindowHours int   `json:"window_hours"`
	UserIds     []int `json:"user_ids"`
}

// countCategoryImpact 数出已经越过这一类阈值的账号。
//
// 与 countBanPolicyImpact 不同,这里**不跨库**:类型计数就在扩展库,而"这些人
// 现在是不是可用状态"由处置那一刻的 disableUserForViolation 判定,不必在预览里
// 提前过滤 —— 类型线不带分组条件,少一次跨库扫描换来的是这个预览可以做得很轻。
// 代价是数字可能略微高估(含已被限制的账号),接口层文案照此说明。
func countCategoryImpact(ctx context.Context, gdb *gorm.DB, cat Category) (categoryImpact, error) {
	out := categoryImpact{
		Threshold: cat.Threshold, WindowHours: cat.WindowHours,
		UserIds: make([]int, 0, impactSampleSize),
	}
	if gdb == nil {
		return out, db.ErrNotReady
	}
	if cat.Id <= 0 || !cat.Enabled || cat.Threshold <= 0 {
		return out, nil
	}
	windowHours := cat.WindowHours
	if windowHours <= 0 {
		windowHours = 24
	}
	winFrom := common.GetTimestamp() - int64(windowHours)*3600

	var ids []int
	if err := gdb.WithContext(ctx).Model(&CategoryCounter{}).
		Where("category_id = ? AND hit_count >= ? AND window_start >= ?", cat.Id, cat.Threshold, winFrom).
		Order("hit_count desc, user_id asc").
		Limit(impactScanCap+1).
		Pluck("user_id", &ids).Error; err != nil {
		db.MarkFailure(err)
		return out, err
	}
	if len(ids) > impactScanCap {
		out.Capped = true
		ids = ids[:impactScanCap]
	}
	out.Matched = len(ids)
	for i, id := range ids {
		if i >= impactSampleSize {
			break
		}
		out.UserIds = append(out.UserIds, id)
	}
	return out, nil
}

// afterCategoryChange 让类型变更被全部节点感知。
//
// 借规则版本号而不是给类型表另建一张版本表:类型与规则装在同一份快照里
// (见 category.go 顶部),两者本来就必须一起刷新。另建一张版本表意味着同一份
// 快照有两个失效源,而"规则刷了、类型没刷"这个组合会让命中记到旧类型上。
func afterCategoryChange() {
	bumpRuleVersion()
	if err := reload(true); err != nil {
		common.SysError("qianye/violation: 违规类型变更后重载失败: " + err.Error())
	}
}

// writeCategoryAudit 是这张表全部写动作的唯一审计出口,成功与失败同一出口。
// 一个出口而不是每个 handler 各写一段:漏写的那一次一定是失败分支。
func writeCategoryAudit(c *gin.Context, action, result string, before, after Category, err error) {
	reason := ""
	if err != nil {
		reason = truncate("失败: "+err.Error(), 512)
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		TraceNo:     fmt.Sprintf("vio-cat:%d", before.Id),
		Result:      result,
		Reason:      reason,
		BeforeSnap:  common.MapToJsonStr(categoryAuditSnap(before)),
		AfterSnap:   common.MapToJsonStr(categoryAuditSnap(after)),
	})
}

func categoryAuditSnap(cat Category) map[string]any {
	return map[string]any{
		"id": cat.Id, "key": cat.Key, "name": cat.Name,
		"published": cat.Published, "enabled": cat.Enabled,
		"window_hours": cat.WindowHours, "threshold": cat.Threshold,
		"is_fallback": cat.IsFallback,
	}
}
