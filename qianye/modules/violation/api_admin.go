package violation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"
	"github.com/QuantumNous/new-api/service"

	"github.com/shopspring/decimal"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// idemScopeViolationRefund 是违规退款的幂等域,与 qy_fund_orders 的
// (idem_scope, idem_key) 双列唯一索引配套;幂等键是 Record.RecNo。
// 提成常量是因为对账任务要按同一个域反查资金单,两处写死字面量迟早会漂。
const idemScopeViolationRefund = "violation_refund"

// ruleUpsertReq 是规则的新建/编辑入参。
//
// 金额与倍数走字符串:JSON number 在前端是 float64,0.1 这类值往返一次就会
// 变成 0.10000000000000001,而它会被直接乘进用户的账单。
type ruleUpsertReq struct {
	Name         string `json:"name"`
	Remark       string `json:"remark"`
	PublicReason string `json:"public_reason"`
	Enabled      bool   `json:"enabled"`
	// Mode 是 "shadow" / "enforce"。收字符串而不是布尔:管理端表单上这一项叫
	// 「影子模式 / 真实模式」,是个二选一的单选,不是"要不要打开某个东西"。
	// 空串在 apply 里被折回 shadow —— 漏传字段的默认必须是不扣钱的那一侧。
	Mode string `json:"mode"`
	// CategoryId 是这条规则归属的违规类型。0 / 缺省 → 由 resolveRuleCategory
	// 落到「未分类」兜底类型,绝不留 0(那会让这条规则的命中在管理端显示成
	// 一个查不到的类型)。
	CategoryId int64 `json:"category_id"`
	// 指针:0 是一档合法优先级(最先判),漏传才落出厂默认 100。
	Priority       *int   `json:"priority,omitempty"`
	Phase          string `json:"phase"`
	MatchType      string `json:"match_type"`
	Pattern        string `json:"pattern"`
	CaseSensitive  bool   `json:"case_sensitive"`
	StatusScope    string `json:"status_scope"`
	ModelScope     string `json:"model_scope"`
	GroupScope     string `json:"group_scope"`
	GroupScopeMode string `json:"group_scope_mode"`
	Action         string `json:"action"`
	FeeMode        string `json:"fee_mode"`
	FeeFixed       string `json:"fee_fixed"`
	FeeMultiple    string `json:"fee_multiple"`
	FeeMaxQuota    int64  `json:"fee_max_quota"`
	// AIMinConfidence 是 ai_review 规则的置信度下限,走字符串与 fee_* 同理:
	// JSON number 在前端是 float64,0.8 往返一次会变成 0.8000000000000001,
	// 而它是一道决定这次命中算不算数的闸。
	AIMinConfidence string `json:"ai_min_confidence"`
	// CountWeight 是"这一次命中给计数加几"(账号总量线与所绑类型线同一个数)。
	// 0 = 只按 Action / FeeMode 处置,一条线都不推进。见 Rule.CountWeight。
	//
	// 这里曾经还有一个 severity。它没有任何读点,已随表单一并移除,
	// 数据库列也已删除(见 dropLegacySeverityColumn)。
	// 旧前端继续发这个字段仍然是无害的:JSON 解码忽略未知键,保存照常成功。
	// 指针:必须分得开"漏传这个字段"与"显式填 0"。0 是一档合法配置
	// (只处置、不推进任何一条线),而漏传要落在出厂默认 1 上。
	CountWeight    *int   `json:"count_weight,omitempty"`
	ArchiveContext bool   `json:"archive_context"`
	BlockMessage   string `json:"block_message"`
}

func (r *ruleUpsertReq) apply(dst *Rule) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("规则名称不能为空")
	}
	fixed, err := parseDecimal(r.FeeFixed)
	if err != nil {
		return fmt.Errorf("fee_fixed 不是合法数值: %q", r.FeeFixed)
	}
	mult, err := parseDecimal(r.FeeMultiple)
	if err != nil {
		return fmt.Errorf("fee_multiple 不是合法数值: %q", r.FeeMultiple)
	}
	conf, err := parseDecimal(r.AIMinConfidence)
	if err != nil {
		return fmt.Errorf("ai_min_confidence 不是合法数值: %q", r.AIMinConfidence)
	}

	// 长度不在这里截断,交给 ValidateRule 报错(见 rules.go 的 ruleVarcharLimits)。
	//
	// 原本这几行是 `truncate(..., 128/512/...)` 的静默截断,有两处不对:
	// 一是管理员保存成功、回到列表才发现备注被拦腰截断,界面上没有任何提示;
	// 二是 truncate 按**字节**切,一段 300 字的中文(约 900 字节)会在第 170 字处
	// 被切断,而 300 字在 varchar(512) 的字符口径下完全合法。
	// 现在超长一律 400 + "备注过长(600 字,上限 512 字)",哪一格填多了一目了然。
	dst.Name = strings.TrimSpace(r.Name)
	dst.Remark = r.Remark
	dst.PublicReason = strings.TrimSpace(r.PublicReason)
	dst.Enabled = r.Enabled
	// 空串折回影子。漏传字段(旧前端、脚本、curl 手敲)必须落在不扣钱的那一侧;
	// 其余非法取值交给 ValidateRule 明确报错,而不是在这里静默纠正成 shadow ——
	// 静默纠正会让"我明明填了 enforce"变成一个查不出来的问题。
	dst.Mode = strings.ToLower(strings.TrimSpace(r.Mode))
	if dst.Mode == "" {
		dst.Mode = ModeShadow
	}
	dst.CategoryId = r.CategoryId
	if r.Priority != nil {
		dst.Priority = *r.Priority
	}
	dst.Phase = r.Phase
	dst.MatchType = r.MatchType
	dst.Pattern = r.Pattern
	dst.CaseSensitive = r.CaseSensitive
	dst.StatusScope = strings.TrimSpace(r.StatusScope)
	dst.ModelScope = r.ModelScope
	dst.GroupScope = r.GroupScope
	// 名单为空时把方向强制回 include:"空黑名单"与"空白名单"都表示"全部分组生效",
	// 留两个等价状态只会让界面上出现一个看得见、却什么都不改变的开关。
	dst.GroupScopeMode = strings.ToLower(strings.TrimSpace(r.GroupScopeMode))
	if dst.GroupScopeMode == "" || len(splitList(dst.GroupScope)) == 0 {
		dst.GroupScopeMode = GroupScopeInclude
	}
	dst.Action = r.Action
	dst.FeeMode = r.FeeMode
	dst.FeeFixed = fixed
	dst.FeeMultiple = mult
	dst.FeeMaxQuota = r.FeeMaxQuota
	dst.AIMinConfidence = conf
	// nil = 这次请求没提这个字段:新建时保留构造函数给的出厂默认,
	// 编辑时保留这一行原来的值。绝不能在这里折成 0。
	if r.CountWeight != nil {
		dst.CountWeight = *r.CountWeight
	}
	dst.ArchiveContext = r.ArchiveContext
	dst.BlockMessage = r.BlockMessage
	return ValidateRule(dst)
}

// resolveRuleCategory 把规则的类型归属落实到一个**真实存在且未归档**的类型 id。
//
// 刻意不放进 ruleUpsertReq.apply / ValidateRule:那两个函数是纯的(不碰数据库),
// 而"这个类型存不存在"只能查库。混进去会让全部规则校验用例都必须先建一张类型表,
// 而它们要断言的是匹配方式与作用域,与类型无关。
//
// 落不到任何类型时**不是**报错,而是回落兜底:一次类型表抖动不该让运营连规则
// 都保存不了 —— 而漏掉 category_id 的规则在运行期由 categoryForRule 兜住,
// 影响仅限于管理端列表上显示成「未分类」。指向一个已归档类型才报错:
// 那是运营明确选错了,静默改写会让他以为自己配的是另一类。
func resolveRuleCategory(row *Rule) error {
	gdb := db.Get()
	if gdb == nil {
		return nil
	}
	if row.CategoryId > 0 {
		var cat Category
		if err := gdb.Where("id = ?", row.CategoryId).Take(&cat).Error; err != nil {
			return fmt.Errorf("违规类型 %d 不存在或已归档,请重新选择", row.CategoryId)
		}
		return nil
	}
	var fallback Category
	if err := gdb.Where("is_fallback = ?", true).Take(&fallback).Error; err != nil {
		common.SysError("qianye/violation: 「未分类」兜底类型缺失,本次规则保存不带类型: " + err.Error())
		return nil
	}
	row.CategoryId = fallback.Id
	return nil
}

func parseDecimal(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(s)
}

// ───────────────────────────── 规则 ─────────────────────────────

func adminListRules(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Rule{})
	if v := c.Query("phase"); v != "" {
		q = q.Where("phase = ?", v)
	}
	if v := c.Query("keyword"); v != "" {
		q = q.Where("name LIKE ?", "%"+v+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	// 下发给前端的数组一律显式初始化:nil 切片会被序列化成 null 而不是 [],
	// 前端对着 null 调 .find/.map 直接白屏。判据与机器校验见
	// qianye/json_array_guard_test.go。本文件下面四个列表接口同理。
	rows := make([]Rule, 0, size)
	if err := q.Order("priority asc, id asc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": rows, "total": total})
}

func adminCreateRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req ruleUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	// 出厂默认写在这里而不是 gorm `default:` 标签上:标签会让 GORM 在 INSERT 时
	// 跳过零值,把管理员显式填的 0 改写成数据库默认值(count_weight 0→1 会让一条
	// 「只拦截不计数」的规则开始把人推向封号,priority 0→100 会让"最先判"失效)。
	row := &Rule{
		CountWeight: 1,
		Priority:    100,
		CreatedAt:   common.GetTimestamp(),
		UpdatedAt:   common.GetTimestamp(),
		CreatedBy:   c.GetInt("id"),
	}
	if err := req.apply(row); err != nil {
		badRequest(c, err.Error())
		return
	}
	if err := resolveRuleCategory(row); err != nil {
		badRequest(c, err.Error())
		return
	}
	row.UpdatedBy = row.CreatedBy
	if err := db.Get().Create(row).Error; err != nil {
		internalError(c, err)
		return
	}
	afterRuleChange(c, "rules.create", row, "")
	respond(c, gin.H{"id": row.Id})
}

func adminUpdateRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "非法的规则 id")
		return
	}
	var req ruleUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	var row Rule
	if err := db.Get().Where("id = ?", id).Take(&row).Error; err != nil {
		notFound(c)
		return
	}
	before := common.MapToJsonStr(map[string]any{
		"enabled": row.Enabled, "mode": row.Mode, "action": row.Action, "pattern": row.Pattern,
	})
	if err := req.apply(&row); err != nil {
		badRequest(c, err.Error())
		return
	}
	if err := resolveRuleCategory(&row); err != nil {
		badRequest(c, err.Error())
		return
	}
	row.UpdatedAt = common.GetTimestamp()
	row.UpdatedBy = c.GetInt("id")
	if err := db.Get().Save(&row).Error; err != nil {
		internalError(c, err)
		return
	}
	afterRuleChange(c, "rules.update", &row, before)
	respond(c, gin.H{})
}

// errRuleWontCompile 包住"这条规则编译不过"。
//
// 单独一个哨兵是为了让启停接口把它翻成 400 而不是 500:编译不过是入参问题
// (库里那一行本身有问题),不是服务端故障,而 500 会让管理员去查后端日志。
var errRuleWontCompile = errors.New("这条规则无法编译,启用后不会命中任何请求")

// setRuleEnabled 只翻转一条规则的 enabled 列,并回答"这次是否真的改变了状态"。
//
// # 为什么不复用整体更新(PUT /rules/:id)
//
// 整体更新会把**前端持有的那一整份规则**写回库。而那份拷贝是列表页在 15 秒的
// staleTime 里拉下来的:这期间同事改窄了 pattern、把 mode 从 enforce 调回 shadow、
// 调了作用域,都会被这次"我只是想关一下开关"原样覆盖回去 —— 一次没有人按下过的
// 静默回滚,而且回滚的是决定谁被扣钱、谁被封号的那几列。applyUpgrade 的注释里
// 写的是同一个坑,那里的结论也是"只写需要的列"。
//
// # 三件必须做的事
//
//  1. **启用前先编译一次。** reloadCtx 对编译失败的规则是静默跳过的(只写一条
//     后端日志),于是"启用成功、界面显示已启用、线上永不命中"是一个完全无声的
//     结局 —— 与内置规则包从没导入过是同一个形状。停用不需要这道闸:把一条坏规则
//     关掉永远是允许的。
//  2. **CAS 带上读到的旧值。** 两个管理员同时点同一行时,后到的那次 RowsAffected=0,
//     据此回读并如实上报"没有变化",而不是对着一个已经被别人改过的状态写审计。
//  3. **只写三列。** enabled 是本次意图;updated_at / updated_by 是"谁在什么时候
//     关的"在列表上的可见投影 —— 不写它们的话,列表里那条规则的更新时间会停在
//     上一次编辑,而审计日志与界面就此对不上。
func setRuleEnabled(gdb *gorm.DB, id int64, enabled bool, operatorId int, now int64) (*Rule, bool, error) {
	if gdb == nil {
		return nil, false, db.ErrNotReady
	}
	var row Rule
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		return nil, false, err
	}
	if row.Enabled == enabled {
		return &row, false, nil
	}
	if enabled {
		if _, err := compile(row); err != nil {
			return nil, false, fmt.Errorf("%w: %v", errRuleWontCompile, err)
		}
	}
	res := gdb.Model(&Rule{}).Where("id = ? AND enabled = ?", id, row.Enabled).
		Updates(map[string]any{"enabled": enabled, "updated_at": now, "updated_by": operatorId})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return nil, false, res.Error
	}
	// 无论 CAS 中没中,都回读一次再返回,**不能**拿手上那份快照改三个字段交差。
	//
	// CAS 的 WHERE 只锁 enabled 一列:入口处 Take 到这次 UPDATE 之间,别人完全
	// 可以把 pattern 改窄、把 mode 从 enforce 调回 shadow,而这次更新照样成功
	// (那正是"只写一列"想要的效果)。但调用方拿这份返回值去写审计的 AfterSnap,
	// 于是审计里会记下一份库里已经不存在的规则 —— 事后追"关掉它的那一刻它是
	// 什么模式"得到的是错的答案,而这条无症状路径的审计是唯一的证据。
	// 代价是一次额外查询,只发生在真正写过一次的调用上。
	var latest Rule
	if err := gdb.Where("id = ?", id).Take(&latest).Error; err != nil {
		return nil, false, err
	}
	// RowsAffected == 0:别人抢先把 enabled 改成了同一个值。如实上报"没有变化",
	// 而不是对着一个不是自己造成的状态写一条审计。
	return &latest, res.RowsAffected > 0, nil
}

// adminSetRuleEnabled 是规则列表行内的快速启停。
//
// 停用一条防护规则不会有任何症状:接口照常 200,业务照常跑,只是从此零命中 ——
// 与"内置规则包从没导入过"完全同形。所以这条路径的审计不是可选项,
// 它是事后唯一能回答"谁在什么时候把哪条规则关了"的东西。
func adminSetRuleEnabled(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "非法的规则 id")
		return
	}
	// 收 *bool 而不是 bool:漏传字段的 bool 零值是 false,于是一次拼错字段名的
	// 调用会变成"静默停用"—— 而停用正是这个接口里唯一没有症状的那个方向。
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		badRequest(c, "enabled 字段必填,且只能是 true 或 false")
		return
	}

	row, changed, err := setRuleEnabled(ctxDB(c), id, *req.Enabled, c.GetInt("id"), common.GetTimestamp())
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		notFound(c)
		return
	case errors.Is(err, errRuleWontCompile):
		badRequest(c, err.Error())
		return
	case err != nil:
		internalError(c, err)
		return
	}
	if !changed {
		// 已经是目标状态(重复点击、或别人抢先改过)。不 bump 版本、不重载、
		// 不写审计 —— 什么都没发生的一次调用不该在审计里留下一条"改过"。
		respond(c, gin.H{"enabled": row.Enabled, "changed": false})
		return
	}
	// changed 为真时旧值必然是 !*req.Enabled —— CAS 的 WHERE 条件就是它。
	// 不写成 !row.Enabled:row 是 UPDATE 之后回读的最新行,并发下它未必还是
	// 本次写进去的那个值,拿它取反就不再是"我改之前是什么"。
	before := common.MapToJsonStr(map[string]any{"enabled": !*req.Enabled})
	afterRuleChange(c, "rules.set_enabled", row, before)
	respond(c, gin.H{"enabled": row.Enabled, "changed": true})
}

func adminDeleteRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "非法的规则 id")
		return
	}
	// 软删:历史记录的 rule_id 指向这里,硬删会让申诉复核失去规则上下文。
	if err := db.Get().Where("id = ?", id).Delete(&Rule{}).Error; err != nil {
		internalError(c, err)
		return
	}
	afterRuleChange(c, "rules.delete", &Rule{Id: id}, "")
	respond(c, gin.H{})
}

// afterRuleChange 统一处理规则写入后的三件事:版本号 +1(让其他节点感知)、
// 本节点立即重载、写审计。
//
// 审计是强制的:规则直接决定谁被扣钱、谁被封号,"这条规则是谁什么时候加的"
// 事后必须能自证。
func afterRuleChange(c *gin.Context, action string, row *Rule, before string) {
	bumpRuleVersion()
	if err := reload(true); err != nil {
		common.SysError("qianye/violation: 规则变更后重载失败: " + err.Error())
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		TraceNo:     fmt.Sprintf("rule:%d", row.Id),
		BeforeSnap:  before,
		AfterSnap: common.MapToJsonStr(map[string]any{
			"id": row.Id, "name": row.Name, "enabled": row.Enabled,
			"phase": row.Phase, "action": row.Action, "fee_mode": row.FeeMode,
			// mode 是本模块唯一决定"要不要真的扣钱/封号"的开关,
			// 把它改成 enforce 是这一页最重的一个动作,必须在审计里看得见。
			"mode": row.Mode, "source": row.Source, "builtin_key": row.BuiltinKey,
			"pattern": truncate(row.Pattern, 1024),
		}),
	})
}

func bumpRuleVersion() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	now := common.GetTimestamp()
	const t = "qy_violation_rule_version"
	if err := gdb.Exec(`INSERT INTO `+t+` (id, version, updated_at)
		VALUES (1, 1, ?)
		`+db.UpsertHead(gdb, t, "id")+` version = `+t+`.version + 1, updated_at = ?`, now, now).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/violation: 规则版本号自增失败,其他节点可能延迟感知: " + err.Error())
	}
}

// 试跑输入的字段标识。
//
// 它们既是请求体的 JSON 键,也是**管理端表单该渲染哪几个格子**的答案:
// 试跑的目的是测这条规则的逻辑,所以它该问的是这条规则真正会读到的东西
// (上下文、上游正文、状态码、错误码、频率计数),而不是雷打不动地问"模型 / 分组"。
//
// 模型与分组保留下来,但降级成可选的作用域输入:它们只决定"这条规则在不在
// 作用域内",一个字节都不参与"内容匹不匹配"。
const (
	TestInputRequestText  = "request_text"
	TestInputUpstreamText = "upstream_text"
	TestInputRejectReason = "reject_reason"
	TestInputStatusCode   = "status_code"
	TestInputErrorCode    = "error_code"
	TestInputRateCount    = "rate_count"
	// AI 审核这一维度的三格:结论 / 违规类型 / 置信度。
	//
	// 它没有"文本样本"这一说 —— ai_review 规则读的不是上下文本身,而是外部模型
	// 对上下文给出的**结论**(见 matchAIRule)。让试跑去问一段文本,等于把面板
	// 变成"我猜模型会怎么判",而那正是这条规则唯一无法在本地复现的一步。
	// 所以这里直接问结论:管理员想验证的是"模型判了 sexual@0.72,我这条
	// min_confidence=0.8 的规则会不会命中",而这个问题本地完全答得出来。
	TestInputAIVerdict    = "ai_verdict"
	TestInputAICategory   = "ai_category"
	TestInputAIConfidence = "ai_confidence"
	TestInputModel        = "model"
	TestInputGroup        = "group"
)

// 试跑结论。三态是这个面板的最低要求:只回一个布尔时,"不在作用域"与
// "在作用域但没命中"会长得一模一样,而这两者对管理员的下一步完全相反 ——
// 前者要去改作用域,后者要去改模式串。分不清就只能两边乱改。
const (
	TestOutcomeOutOfScope = "out_of_scope"
	TestOutcomeNoMatch    = "no_match"
	TestOutcomeMatched    = "matched"
	// TestOutcomeTimeout 是第四种,也是最不能被折叠进"未命中"的那一种:
	// 扫描预算耗尽时线上同样什么都不拦,但原因是规则太重,不是模式串写错。
	TestOutcomeTimeout = "timeout"
)

// 作用域闸的标识,回答"是哪一道闸把它挡在门外"。
const (
	TestScopeFailModel  = "model"
	TestScopeFailGroup  = "group"
	TestScopeFailStatus = "status"
)

// ruleTestReq 是规则试跑的入参。
//
// 提成命名类型而不是留在 handler 里做匿名结构:守卫测试要用反射逐字段比对它与
// 线上 scanInput 的对齐关系(见 api_admin_testrule_test.go),匿名结构在包外、
// 甚至在同包的测试里都拿不到。
type ruleTestReq struct {
	Rule ruleUpsertReq `json:"rule"`
	// Sample 是旧版试跑面板的唯一文本输入:一段样本同时灌进请求上下文与上游正文。
	// 保留它纯粹为了兼容 —— 老前端、老脚本、以及任何存着旧请求体的人。
	// 新面板一律改填下面按维度拆开的字段。
	Sample string `json:"sample_text"`
	// RequestText / UpstreamText 用指针:**缺字段**与**显式空串**必须能区分。
	// 缺字段回落到 Sample(旧行为),显式空串就是空 —— 新面板给上游规则
	// 只填 upstream_text 时,request_text 必须真的是空,而不是被 Sample 偷偷灌满。
	RequestText  *string `json:"request_text"`
	UpstreamText *string `json:"upstream_text"`
	// RejectReason 是上游软违规信号(ContextKeyAdminRejectReason)。
	// 线上 scanPostText 把它拼在上游正文之后一起参与匹配,而在此之前试跑面板
	// 根本没有这一格 —— 于是一条专门盯 `openai_finish_reason=content_filter`
	// 的规则,在试跑里只能靠"把它粘进样本框"这种碰巧成立的办法测。
	RejectReason string `json:"reject_reason"`
	// Model / Group 是作用域输入,可选。
	Model string `json:"model"`
	Group string `json:"group"`
	// RateCount 让 request_rate 规则也能试跑。没有它,频率规则在试跑面板里
	// 永远显示"未命中" —— 一个看起来权威、实则只是没有输入的结论,
	// 比不给试跑更容易让人放心上线。
	RateCount int `json:"rate_count"`
	// StatusCode / ErrorCode 让上游阶段的规则也能试跑,理由与 RateCount 完全相同。
	//
	// 状态码作用域出现之后这一项从"锦上添花"变成必需:一条配了
	// status_scope=400 的规则,在没有状态码输入的试跑里恒为"不在作用域",
	// 管理员会以为自己写错了正文,反复改一个本来就对的模式串。
	StatusCode int    `json:"status_code"`
	ErrorCode  string `json:"error_code"`
	// AIVerdict 是外部审核给出的结论:空 = 这一条压根没送审(或审核失败/超时),
	// 也就是 ai_review 规则**必然不命中**的那一档 —— 那正是"失败即放行"在试跑里
	// 的表达,试跑必须能重现它。非空时取 OutcomeClean / OutcomeViolation。
	AIVerdict  string `json:"ai_verdict"`
	AICategory string `json:"ai_category"`
	// AIConfidence 走字符串,与本文件其余 decimal 字段同口径:JSON number 在前端
	// 是 float64,0.8 往返一次可能变成 0.8000000000000000444,而它要跟规则的
	// ai_min_confidence 做大小比较 —— 差一个 ulp 就是"命中"与"不命中"之别。
	AIConfidence string `json:"ai_confidence"`
}

// input 把试跑请求翻成线上判据的输入。
//
// 试跑输入必须与线上的 scanInput 逐字段对齐:少一个字段,那一维度的规则在试跑里
// 就恒为"未命中" —— 而"试跑说不命中、线上却命中"是这个面板最坏的失效方式。
// 守卫测试拿反射钉住这份对齐(scanInput 新增字段而这里没跟上就变红)。
func (r *ruleTestReq) input() scanInput {
	requestText, upstreamText := r.Sample, r.Sample
	if r.RequestText != nil {
		requestText = *r.RequestText
	}
	if r.UpstreamText != nil {
		upstreamText = *r.UpstreamText
	}
	var ai *aiOutcome
	// 只有 decided() 认的两个取值才构造结论。其余一律留 nil —— 未送审、审核失败、
	// 审核超时在线上是同一档(不命中),试跑没有理由把它们区分成三种假象。
	if r.AIVerdict == OutcomeClean || r.AIVerdict == OutcomeViolation {
		// 走 float64 + clampConfidence,与线上从模型响应里取置信度的那一步是
		// 同一个函数、同一个夹取与舍入(4 位)。自己在这里 decimal 解析会得到一个
		// 与线上差一个 ulp 的数,而它要跟 ai_min_confidence 做大小比较 ——
		// 差一个 ulp 就是"命中"与"不命中"之别。
		conf, err := strconv.ParseFloat(strings.TrimSpace(r.AIConfidence), 64)
		if err != nil {
			conf = 0
		}
		violated := r.AIVerdict == OutcomeViolation
		// 归一走线上同一个函数、同一份类型闭集:模型返回的 category 也走它。
		// 两侧不同口径就会得到"试跑说命中、线上不命中"—— 本模块已经在
		// groupname 上栽过两次。闭集取当前快照,与线上这一刻用的是同一份。
		res := Snapshot().aiVocab.resolveCategory(r.AICategory, violated)
		ai = &aiOutcome{
			Outcome:         r.AIVerdict,
			Violated:        violated,
			Category:        res.Key,
			RawCategory:     clipRunes(res.Raw, 64),
			CategoryUnknown: res.Fallback,
			Confidence:      clampConfidence(conf),
		}
	}
	return scanInput{
		Model:        r.Model,
		Group:        r.Group,
		Text:         clipHeadTail(requestText, maxScanBytes),
		RateCount:    r.RateCount,
		StatusCode:   r.StatusCode,
		ErrCode:      r.ErrorCode,
		UpstreamText: clipHeadTail(upstreamText, maxScanBytes),
		RejectReason: clipHeadTail(r.RejectReason, maxScanBytes),
		AI:           ai,
	}
}

// ruleTestInputs 列出一条规则**真正会读到**的试跑输入,顺序即表单渲染顺序。
//
// 这是"试跑该问什么"的唯一出处。让前端自己按 match_type 推一份,是本仓已经
// 踩过的那类坑:两份判据迟早不一致,而不一致的表现恰好最难发现 —— 界面问了
// 一个规则根本不看的字段(填了没用),或者漏问了它唯一看的那个(永远不命中)。
func ruleTestInputs(phase, matchType string) []string {
	var out []string
	switch matchType {
	case MatchRequestRate:
		// 频率判据不看任何文本:pattern 是阈值,输入是窗口内的请求条数。
		out = []string{TestInputRateCount}
	case MatchErrorCode:
		out = []string{TestInputErrorCode}
	case MatchStatusCode:
		out = []string{TestInputStatusCode}
	case MatchAIReview:
		out = []string{TestInputAIVerdict, TestInputAICategory, TestInputAIConfidence}
	default:
		// keyword / regex / upstream_text 都是文本判据,差别只在扫哪一段文本:
		// prompt 阶段扫请求上下文,上游阶段扫"上游正文 + 软违规原因"的拼接。
		if phase == PhasePrompt {
			out = []string{TestInputRequestText}
		} else {
			out = []string{TestInputUpstreamText, TestInputRejectReason}
		}
	}
	// 状态码作用域与全部匹配方式正交(见 Rule.StatusScope):哪怕判据是错误码,
	// 一条配了 status_scope=400 的规则在没有状态码输入的试跑里恒为"不在作用域"。
	//
	// 只有这两个阶段手上真的有上游状态码。写成 `phase != PhasePrompt` 会把
	// post_async(转发后异步审核)也算进来,而那一档跑在异步 worker 上、
	// 根本没有上游响应 —— 多摆一格填了也不生效的输入,与这次改动要修的毛病同形。
	if (phase == PhaseUpstreamErr || phase == PhaseRejectReason) &&
		matchType != MatchStatusCode {
		out = append(out, TestInputStatusCode)
	}
	// 作用域输入排在最后,且永远可选:它们不参与内容匹配。
	return append(out, TestInputModel, TestInputGroup)
}

// ruleTestScopeFail 回答"是哪一道作用域闸把它挡在门外",空串表示在作用域内。
//
// 三道闸复用 compiledRule 自己的方法,一个判据都不重写:applies 的注释已经写明
// 两处各写一遍 && 的后果,而"试跑说不在作用域、线上照样命中"正是那个后果。
func ruleTestScopeFail(cr *compiledRule, in scanInput) string {
	switch {
	case !cr.groupInScope(in.Group):
		return TestScopeFailGroup
	case !cr.modelInScope(in.Model):
		return TestScopeFailModel
	case !cr.statusInScope(in.StatusCode):
		return TestScopeFailStatus
	}
	return ""
}

// ruleTestBlanks 列出这条规则会读、但本次试跑没给值的输入。
//
// 它回答三态之外的第四个问题:"未命中"到底是规则写错了,还是样本没填全。
// 一条 error_code 规则在错误码留空时必然未命中 —— 不说破的话,管理员会去改
// 一个本来就对的模式串。
//
// 模型/分组不算(它们空着就是"不限")。拒绝原因也不算:线上它经常是空的,
// 上游正文非空时这条规则完全测得出来,报它只会变成常态噪声 —— 所以上游文本
// 是否为空按 scanPostText 的拼接结果判,与线上参与匹配的那一段严格一致。
func ruleTestBlanks(in scanInput, inputs []string) []string {
	blank := []string{}
	for _, id := range inputs {
		switch {
		case id == TestInputRequestText && in.Text == "":
			blank = append(blank, id)
		case id == TestInputUpstreamText && scanPostText(in) == "":
			blank = append(blank, id)
		case id == TestInputStatusCode && in.StatusCode == 0:
			blank = append(blank, id)
		case id == TestInputErrorCode && in.ErrCode == "":
			blank = append(blank, id)
		case id == TestInputRateCount && in.RateCount == 0:
			blank = append(blank, id)
		case id == TestInputAIVerdict && in.AI == nil:
			blank = append(blank, id)
		}
	}
	return blank
}

// ruleTestOutcome 跑一次试跑并给出三态(+ 超时)结论。
//
// 与线上共用 scan / scanPostText / applies:试跑面板的全部价值是它与线上判据
// 逐字节相同,任何"试跑专用的简化版匹配"都会把这个价值清零。
func ruleTestOutcome(cr *compiledRule, phase string, in scanInput) (string, string, *verdict) {
	text := in.Text
	if phase != PhasePrompt {
		text = scanPostText(in)
	}
	scopeFail := ruleTestScopeFail(cr, in)
	v := scan([]*compiledRule{cr}, cr.words, in, text)
	switch {
	case v != nil && v.Rule != nil:
		return TestOutcomeMatched, scopeFail, v
	case scopeFail != "":
		// 作用域优先于超时:预算耗尽时 scan 在检查 applies 之前就退出了,
		// 但"这条规则压根不作用于你填的模型/分组/状态码"是更靠前、更可行动的答案。
		return TestOutcomeOutOfScope, scopeFail, v
	case v != nil && v.Timeout:
		return TestOutcomeTimeout, scopeFail, v
	}
	return TestOutcomeNoMatch, scopeFail, v
}

// adminTestRule 是规则试跑:按当前规则真正会用到的维度填样本,立刻看到
// 在不在作用域、命中没命中、命中什么、耗时多少。
//
// 这是本模块最重要的一个接口。没有它,管理员只能"改完上线看线上炸不炸",
// 而线上一炸就是全站用户被误扣误封。
func adminTestRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req ruleTestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	row := &Rule{}
	if err := req.Rule.apply(row); err != nil {
		badRequest(c, err.Error())
		return
	}
	cr, err := compile(*row)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	in := req.input()
	inputs := ruleTestInputs(row.Phase, row.MatchType)
	outcome, scopeFail, v := ruleTestOutcome(cr, row.Phase, in)
	out := gin.H{
		"outcome": outcome,
		// scope_ok / matched 是旧版响应的两个布尔,原样保留:老前端还在读它们,
		// 而这次改动的目的是把结论说清楚,不是让旧界面变成一片空白。
		"scope_ok":     scopeFail == "",
		"scope_fail":   scopeFail,
		"matched":      outcome == TestOutcomeMatched,
		"terms":        []string{},
		"snippet":      "",
		"inputs":       inputs,
		"blank_inputs": ruleTestBlanks(in, inputs),
	}
	if v != nil {
		out["elapsed_us"] = v.Elapsed.Microseconds()
		if v.Rule != nil {
			out["terms"] = v.Terms
			out["snippet"] = v.Snippet
		}
	}
	respond(c, out)
}

// ───────────────────────────── 记录 ─────────────────────────────

// recordQuery 把查询参数翻成 WHERE 条件。
//
// 列表与导出必须共用它:两个入口各写一份筛选,导出出来的 CSV 迟早与屏幕上看到的
// 不是同一批行 —— 而导出的用途恰恰是"把屏幕上这批行拿去分析"。
func recordQuery(c *gin.Context, q *gorm.DB) *gorm.DB {
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	if v := c.Query("model"); v != "" {
		q = q.Where("model_name = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("phase"); v != "" {
		q = q.Where("phase = ?", v)
	}
	if v := c.Query("request_id"); v != "" {
		q = q.Where("request_id = ?", v)
	}
	if v := httpq.Int64(c, "rule_id", 0); v > 0 {
		q = q.Where("rule_id = ?", v)
	}
	// shadow 是项目方那个用例的核心筛选:「把规则设成影子 → 抓涉嫌违规用户的
	// 日志和上下文来分析」。没有它,影子命中混在真实命中里,分析的第一步就做不了。
	//
	// 三态:不传 = 全部;1 = 只看影子;0 = 只看真实。刻意不用 httpq.Int 的默认值
	// 兜 —— "没传"与"传了 0"必须区分开,否则永远筛不出真实命中。
	if v := c.Query("shadow"); v == "1" || v == "true" {
		q = q.Where("shadow = ?", true)
	} else if v == "0" || v == "false" {
		q = q.Where("shadow = ?", false)
	}
	if v := c.Query("shadow_reason"); v != "" {
		q = q.Where("shadow_reason = ?", v)
	}
	if v := httpq.Int64(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := httpq.Int64(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}
	return q
}

func adminListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := recordQuery(c, db.Get().Model(&Record{}))
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	rows := make([]Record, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": rows, "total": total})
}

// adminGetEvidence 返回归档的违规上下文。
//
// 这是"查看他人输入原文"的操作,必须留痕。审计写在读取成功之后、返回之前,
// 顺序不能反 —— 先返回再写审计,进程崩溃就会留下无痕的查看行为。
func adminGetEvidence(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "非法的记录 id")
		return
	}
	var rec Record
	if err := db.Get().Where("id = ?", id).Take(&rec).Error; err != nil {
		notFound(c)
		return
	}
	var p Payload
	if err := db.Get().Where("record_id = ?", id).Take(&p).Error; err != nil {
		respond(c, gin.H{"record": rec, "has_payload": false})
		return
	}
	text, err := decodeEvidence(&p)
	if err != nil {
		internalError(c, err)
		return
	}

	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryViolation,
		Action:       "records.view_evidence",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: rec.UserId,
		TraceNo:      rec.RecNo,
		Reason:       "查看违规归档上下文",
	})

	respond(c, gin.H{
		"record":       rec,
		"has_payload":  true,
		"context":      text,
		"files":        p.FilesSummary,
		"truncated":    p.Truncated,
		"redacted":     p.Redacted,
		"redact_stats": p.RedactStats,
		"origin_bytes": p.OriginBytes,
		"stored_bytes": p.StoredBytes,
	})
}

func adminRevokeRecord(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "非法的记录 id")
		return
	}
	var req struct {
		Reason string `json:"reason"`
		Refund bool   `json:"refund"`
	}
	_ = c.ShouldBindJSON(&req)

	var rec Record
	if err := db.Get().Where("id = ?", id).Take(&rec).Error; err != nil {
		notFound(c)
		return
	}
	// refund=true 时这条路径会把扣掉的额度退回 rec.UserId —— 撤销自己的违规
	// 记录并勾上退款,是这套系统里最短的一条自营取钱路径。
	if denyActorOverTarget(c, "records.revoke", rec.UserId) {
		return
	}
	refunded, err := revokeRecord(c, &rec, req.Reason, req.Refund, c.GetInt("id"))
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"refunded_quota": refunded})
}

// revokeRecord 撤销一条违规记录,可选退还扣费。
//
// 三步都必须幂等:
//   - 撤销是状态条件 UPDATE,连点两次只有一次会真正翻转状态;
//   - 退款走 twophase,以 rec_no 为幂等键,即使补偿任务重跑也不会重复退;
//   - 计数回退带窗口条件,窗口滚动后不回退(旧值已经不在窗口内)。
//
// 但"幂等"不等于"第二次什么都不做":撤销与退款是两步跨库操作,第一次点击完全
// 可能停在"记录已 revoked、退款没成功"的中间态。所以第二次点击必须继续往下走到
// 退款分支(由 fee_status 决定要不要补做),这是唯一的自愈入口。
func revokeRecord(c *gin.Context, rec *Record, reason string, refund bool, operatorId int) (int64, error) {
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	first, err := claimRevoke(gdb, rec, reason, operatorId)
	if err != nil {
		return 0, err
	}
	if rec.Status != RecordRevoked {
		return 0, nil // 记录不在可撤销集合内,保持幂等
	}

	// 计数回退。撤销一条误判记录后,用户不该继续背着这次计数走向封号。
	// 只在本次真正完成撤销时做:回退是无条件减法,重复执行会把当前窗口里
	// 其他违规的合法计数一起扣掉,反而放过真正的违规用户。
	if first {
		revertHitCounters(gdb, rec)
	}

	var refunded int64
	if refund && rec.FeeQuota > 0 &&
		(rec.FeeStatus == FeeStatusCharged || rec.FeeStatus == FeeStatusTruncated) {
		got, err := refundFee(rec)
		if err != nil {
			// 退款失败不回滚撤销:记录已经标记为误判是正确的,
			// 钱可以由管理员重试(见上面的自愈说明)或人工补,
			// 但把误判标记撤回去只会更混乱。
			common.SysError("qianye/violation: 违规扣费退还失败: " + err.Error())
		} else {
			// 金额取 refundFee 回读到的 refund_quota,不是本次算出来的 rec.FeeQuota:
			// 只有库里真的写着"已退款"才承认退过。见 confirmRefundSettled。
			refunded = got
		}
	}

	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryViolation,
		Action:       "records.revoke",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  operatorId,
		ActorName:    c.GetString("username"),
		TargetUserId: rec.UserId,
		TraceNo:      rec.RecNo,
		AmountQuota:  refunded,
		Reason:       truncate(reason, 512),
	})
	return refunded, nil
}

// claimRevoke 把记录置为 revoked,并回答"本次是否是首次撤销"。
//
// CAS 落空(RowsAffected == 0)时绝不能早退。撤销与退款是两步跨库操作,第一次点击
// 可能停在"记录已 revoked、退款还没成功"的中间态:退款失败只记日志不回滚,扩展库
// 回写超时、进程重启同理。早退会把这里变成死路 —— 管理员再点一次连 refundFee 都
// 进不去,只能走人工补单,而人工补单必然重复退款。所以落空时回读记录,让调用方基于
// 最新的 fee_status 决定要不要补做退款。
func claimRevoke(gdb *gorm.DB, rec *Record, reason string, operatorId int) (bool, error) {
	now := common.GetTimestamp()
	// 条件里必须同时接受 appealed:用户提交申诉时记录已经从 active 变成 appealed,
	// 只认 active 会让"申诉通过"永远撤销不了任何记录 —— 申诉闭环直接断掉。
	res := gdb.Model(&Record{}).
		Where("id = ? AND status IN ?", rec.Id, []string{RecordActive, RecordAppealed}).
		Updates(map[string]any{
			"status":        RecordRevoked,
			"revoked_by":    operatorId,
			"revoked_at":    now,
			"revoke_reason": truncate(reason, 512),
		})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		rec.Status = RecordRevoked
		rec.RevokedBy = operatorId
		rec.RevokedAt = now
		rec.RevokeReason = truncate(reason, 512)
		return true, nil
	}
	var latest Record
	if err := gdb.Where("id = ?", rec.Id).Take(&latest).Error; err != nil {
		return false, err
	}
	*rec = latest
	return false, nil
}

// markRecordRefunded 幂等地把记录标成已退款。
//
// 条件里的 fee_status 集合就是幂等保证:重复执行(LocalCommit 一次、补偿
// Resolver 再来一次)不会把 refund_quota 写第二遍。
func markRecordRefunded(gdb *gorm.DB, recNo string, amount int64) error {
	return gdb.Model(&Record{}).
		Where("rec_no = ? AND fee_status IN ?", recNo,
			[]string{FeeStatusCharged, FeeStatusTruncated}).
		Updates(map[string]any{
			"fee_status":   FeeStatusRefunded,
			"refund_quota": amount,
		}).Error
}

// resolveAfterCompensation 由 twophase 补偿任务在确认主库已生效后回调。
//
// 没有它,"主库已退款但扩展库回写没跑成"这个中间态会永远停在 fee_status=charged:
// 补偿任务会把资金单直接标成 success,而用户端的 SUM(fee_quota) 仍显示罚款在被收取。
// 管理员再点一次退款只会撞上幂等键拿到原单,最终走人工补单 —— 同一笔退两次。
// 必须幂等:同一单可能被补偿多轮。
func resolveAfterCompensation(ctx context.Context, order *qymodel.FundOrder) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	// IdemKey 就是 rec_no,见 refundFee。
	return markRecordRefunded(gdb.WithContext(ctx), order.IdemKey, order.AmountQuota)
}

// postRefundFromOrder 是"主库已确认退款生效"之后必须补做的非事务收尾。
//
// 它与 refundFee 里那个 AfterCommit 闭包做同一件事,区别只在于**信息来源**:
// 闭包捎带着 rec 结构体,只存在于发起退款的那个进程里;主库事务在 COMMIT 阶段
// 断连、或扩展库回写失败时,那个闭包一次都不会跑,而钱已经退了。补偿任务与人工
// 裁决只有 qy_fund_orders 那一行,因此这里从 rec_no 把记录读回来重建。
//
// 令牌缓存必须一并失效:退款会回冲 tokens.remain_quota,只失效用户缓存的话,
// 那张令牌在缓存 TTL 内仍按退款前的额度判定。
func postRefundFromOrder(ctx context.Context, order *qymodel.FundOrder) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	var rec Record
	if err := gdb.WithContext(ctx).Where("rec_no = ?", order.IdemKey).Take(&rec).Error; err != nil {
		return err
	}
	if rec.TokenId > 0 {
		if e := model.InvalidateUserTokensCache(rec.UserId); e != nil {
			common.SysError("qianye/violation: 补做退款收尾时失效令牌缓存失败: " + e.Error())
		}
	}
	model.QyRecordLedgerLog(rec.UserId, model.LogTypeRefund,
		fmt.Sprintf("违规扣费撤销退还 %d(记录 %s)", order.AmountQuota, rec.RecNo),
		order.OrderNo, map[string]interface{}{
			"qy_violation_rec_no": rec.RecNo,
			"qy_refund_quota":     order.AmountQuota,
			"qy_billing_source":   rec.BillingSource,
			"qy_token_id":         rec.TokenId,
		})
	return nil
}

// refundFee 通过跨库两阶段把违规扣费退还给用户,并返回**确实**退还的额度。
//
// 必须走 twophase 而不是裸 IncreaseUserQuota:退款是"扩展库记账 + 主库动钱",
// 中间崩溃会留下"记录已标记退款但钱没到账"的悬案,而 outbox 探针是唯一
// 能精确判定主库到底动没动的手段。
//
// 返回值是回读到的 refund_quota 而不是入参金额:twophase 的幂等命中不会执行
// LocalCommit,光凭 Execute 返回 nil 断言"退款完成"会报出并不存在的退款。
// 详见 confirmRefundSettled。
func refundFee(rec *Record) (int64, error) {
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	amount := rec.FeeQuota
	if amount <= 0 || amount > int64(common.MaxQuota) {
		return 0, fmt.Errorf("退款金额越界: %d", amount)
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	order, err := twophase.Execute(ctx, twophase.Request{
		Kind:        qymodel.KindViolationFee,
		IdemScope:   idemScopeViolationRefund,
		IdemKey:     rec.RecNo,
		UserId:      rec.UserId,
		AmountQuota: amount,
		RefType:     "violation_record",
		RefId:       fmt.Sprint(rec.Id),
		MainApply: func(tx *gorm.DB, order *qymodel.FundOrder) error {
			return applyRefundOnMainDB(tx, rec, amount)
		},
		AfterCommit: func(order *qymodel.FundOrder) {
			// 用整体失效而不是增量刷新缓存:退款现在同时动了钱包/订阅池、令牌额度
			// 与 used_quota,增量刷新只能覆盖其中一处;而令牌缓存的键是明文密钥,
			// 本来也只能整体失效。失效是幂等的,重复执行不会让缓存漂移。
			if e := model.InvalidateUserCache(rec.UserId); e != nil {
				common.SysError("qianye/violation: 退款后失效用户缓存失败: " + e.Error())
			}
			if rec.TokenId > 0 {
				if e := model.InvalidateUserTokensCache(rec.UserId); e != nil {
					common.SysError("qianye/violation: 退款后失效令牌缓存失败: " + e.Error())
				}
			}
			// 用 LogTypeRefund 而不是 LogTypeConsume:退款计进消费统计
			// 会让"本月消费"凭空变小,财务对账直接对不上。
			model.QyRecordLedgerLog(rec.UserId, model.LogTypeRefund,
				fmt.Sprintf("违规扣费撤销退还 %d(记录 %s)", amount, rec.RecNo),
				order.OrderNo, map[string]interface{}{
					"qy_violation_rec_no": rec.RecNo,
					"qy_refund_quota":     amount,
					"qy_billing_source":   rec.BillingSource,
					"qy_token_id":         rec.TokenId,
				})
		},
		// LocalCommit 与资金单回写 success 同事务:不会出现"钱退了但记录还写着
		// charged",也不会出现"记录标了 refunded 但资金单还是 pending"。
		LocalCommit: func(tx *gorm.DB, order *qymodel.FundOrder) error {
			return markRecordRefunded(tx, rec.RecNo, amount)
		},
	})
	if err != nil {
		return 0, err
	}
	return confirmRefundSettled(gdb.WithContext(ctx), rec, order, amount)
}

// confirmRefundSettled 在 twophase 返回成功之后,确认记录**真的**被标成了 refunded。
//
// 为什么不能省:twophase 的幂等命中走 resolveExisting,原单已经是 Success 时直接
// `return order, nil`,LocalCommit 在这条路径上根本不执行。升级前那批由旧补偿任务
// (当时还没有 Resolver)推成 Success、fee_status 却仍是 charged 的退款单,因此会
// 变成一个纯误报面:管理员每点一次"撤销+退款"都拿到 200 + refunded_quota,还写下
// 一条 records.revoke 成功审计,而库里纹丝不动 —— 点几次写几条假审计。
// markSuccess 的 CAS 落空(补偿任务抢先推成 Success)也是同一形状。
//
// 这里先补做一次幂等回写 —— 用的就是 LocalCommit 与补偿 Resolver 同一个
// markRecordRefunded,重复执行不会把 refund_quota 写第二遍 —— 再回读确认。
// 仍然收敛不了就必须报错:接口宁可让管理员看到"退款未落定",也不能对外声称
// 退了一笔并不存在的款。
func confirmRefundSettled(gdb *gorm.DB, rec *Record, order *qymodel.FundOrder, amount int64) (int64, error) {
	if order == nil || order.Status != qymodel.StatusSuccess {
		// Execute 返回 nil 但单据没到 Success,只有一种情况:主库已生效、扩展库回写
		// 失败,单据留在 pending 等补偿任务。钱大概率已经动了,但此刻不能声称完成
		// (也不能重试,幂等键已经被占住)。
		status := "缺失"
		if order != nil {
			status = qymodel.StatusName(order.Status)
		}
		return 0, fmt.Errorf("记录 %s 的退款单尚未落定(状态 %s),请稍后在资金单列表复核",
			rec.RecNo, status)
	}
	if err := markRecordRefunded(gdb, rec.RecNo, amount); err != nil {
		return 0, err
	}
	var latest Record
	if err := gdb.Where("id = ?", rec.Id).Take(&latest).Error; err != nil {
		return 0, err
	}
	rec.FeeStatus = latest.FeeStatus
	rec.RefundQuota = latest.RefundQuota
	if latest.FeeStatus != FeeStatusRefunded {
		return 0, fmt.Errorf("记录 %s 的扣费状态仍是 %s,退款未落定,请人工核对资金单",
			rec.RecNo, latest.FeeStatus)
	}
	return latest.RefundQuota, nil
}

// applyRefundOnMainDB 在主库事务内把罚款退回"当初扣走它的那个池"。
//
// 扣费经 service.PostConsumeQuota 一次动了两处:钱包或订阅池(按 BillingSource
// 路由)+ tokens.remain_quota;chargeFee 之后还额外把 users.used_quota 加了一笔。
// 退款必须把这三处全部回冲,少任何一处都是"退错了账户":
//   - 只加钱包 → 订阅用户的订阅池消耗永不归还,钱包却凭空多出等额额度;
//   - 不还令牌 → 该令牌永久少掉这笔可用额度,用户看得见却无法自证;
//   - 不回冲 used_quota → 用户的"已用额度"与消费统计永远虚高。
//
// 三处全部放进同一个主库事务(而不是 AfterCommit),是为了让 outbox 探针
// "执行且只执行一次"的保证同时覆盖它们,而不只覆盖钱包那一处。
func applyRefundOnMainDB(tx *gorm.DB, rec *Record, amount int64) error {
	if rec.BillingSource == service.BillingSourceSubscription && rec.SubscriptionId > 0 {
		if err := refundToSubscription(tx, rec, amount); err != nil {
			return err
		}
	} else if err := creditWallet(tx, rec.UserId, amount); err != nil {
		return err
	}

	// used_quota 的下界用 CASE WHEN 夹住:主库可以是 MySQL / PostgreSQL / SQLite,
	// GREATEST 在 SQLite 上不存在,CASE WHEN 是三种方言都支持的写法。
	// 夹下界是必要的 —— 用户的 used_quota 可能在扣费之后被管理员重置过。
	if err := tx.Model(&model.User{}).Where("id = ?", rec.UserId).
		Update("used_quota", gorm.Expr(
			"CASE WHEN used_quota >= ? THEN used_quota - ? ELSE 0 END", amount, amount)).Error; err != nil {
		return err
	}

	if rec.TokenId <= 0 {
		return nil
	}
	// 令牌可能已被用户删除,那就没有可退的令牌额度。这里刻意不检查 RowsAffected:
	// 为一个已经不存在的令牌回滚整笔退款,只会让用户连钱包里的钱也拿不回来。
	return tx.Model(&model.Token{}).
		Where("id = ? AND user_id = ?", rec.TokenId, rec.UserId).
		Updates(map[string]any{
			"remain_quota": gorm.Expr("remain_quota + ?", amount),
			"used_quota": gorm.Expr(
				"CASE WHEN used_quota >= ? THEN used_quota - ? ELSE 0 END", amount, amount),
		}).Error
}

// creditWallet 把额度加回钱包。
func creditWallet(tx *gorm.DB, userId int, amount int64) error {
	// 加款前的上限校验:users.quota 是 int32,加爆会翻成负数,
	// 那等于把误判赔偿变成账号清零。
	res := tx.Model(&model.User{}).
		Where("id = ? AND quota <= ?", userId, int64(common.MaxQuota)-amount).
		Update("quota", gorm.Expr("quota + ?", amount))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("退款会导致额度溢出,已拒绝(user=%d)", userId)
	}
	return nil
}

// refundToSubscription 把额度退回订阅池。
func refundToSubscription(tx *gorm.DB, rec *Record, amount int64) error {
	var sub model.UserSubscription
	err := model.QyLockForUpdate(tx).
		Where("id = ? AND user_id = ?", rec.SubscriptionId, rec.UserId).Take(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 订阅已经不存在(到期清理 / 管理员作废)。钱确实是从订阅池扣走的,但那个池
		// 没了,只能退到钱包。不这样兜底的话本次 MainApply 报错 → 资金单落 failed,
		// 而幂等键(rec_no)决定它再也不会成功,这笔误判罚款永远退不回去。
		common.SysError(fmt.Sprintf(
			"qianye/violation: 记录 %s 的订阅 %d 已不存在,退款回落到钱包",
			rec.RecNo, rec.SubscriptionId))
		return creditWallet(tx, rec.UserId, amount)
	}
	if err != nil {
		return err
	}
	newUsed := sub.AmountUsed - amount
	if newUsed < 0 {
		// 订阅池可能在扣费之后被按周期重置,下界夹到 0,
		// 与 model.PostConsumeUserSubscriptionDelta 的口径保持一致。
		newUsed = 0
	}
	return tx.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).
		Update("amount_used", newUsed).Error
}

// ───────────────────────────── 封禁 ─────────────────────────────

func adminListBans(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Ban{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	rows := make([]Ban, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": rows, "total": total})
}

func adminUnban(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	userId := pathIntParam(c, "userId")
	if userId <= 0 {
		badRequest(c, "非法的用户 id")
		return
	}
	var req struct {
		Note         string `json:"note"`
		ResetCounter bool   `json:"reset_counter"`
	}
	_ = c.ShouldBindJSON(&req)

	if denyActorOverTarget(c, "bans.unban", userId) {
		return
	}

	if err := unbanUser(c, userId, req.Note, req.ResetCounter, c.GetInt("id")); err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{})
}

func unbanUser(c *gin.Context, userId int, note string, resetCounter bool, operatorId int) error {
	ban, err := claimUnban(db.Get(), userId, note, operatorId)
	if err != nil {
		return err
	}
	// 周期 +1 必须与解封同时发生,否则该用户的自动封号从此静默失效:
	// 下次达阈值时 claimBan 的 (user_id, ban_cycle) 唯一键会撞上这一行,
	// 而它已经是 unbanned,既不会被提升也不会被补偿任务执行。
	if e := openNewBanCycle(userId, resetCounter); e != nil {
		common.SysError("qianye/violation: 解封时推进封禁周期失败: " + e.Error())
	}
	// deferred 是速率闸挡下的认领:主库那六步一次都没跑过,这个账号从来没有被
	// 这一行禁用。对它调 enableUserAfterUnban 会把一个正因别的原因(管理员手动停用、
	// 风控停用)被禁用的账号直接放出来,并给用户日志里写一条从未发生过的"封禁已解除"。
	// 判定用的是被 CAS 真正命中的那个状态,claimUnban 保证它不是旧值。
	if ban.Status != BanDeferred {
		if e := enableUserAfterUnban(userId, ban, operatorId); e != nil {
			return e
		}
	}
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryViolation,
		Action:       "bans.unban",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  operatorId,
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		TraceNo:      fmt.Sprintf("ban:%d", ban.Id),
		// 了结前的状态必须进审计:deferred 是"不予封禁"的裁决,banned 才是解封,
		// 两者在封禁列表上都会变成 unbanned,事后只能靠这里分辨。
		BeforeSnap: common.MapToJsonStr(map[string]any{"ban_status": ban.Status}),
		Reason:     truncate(note, 512),
	})
	return nil
}

// claimUnban 找出该用户待人工了结的封禁行,原子地把它标成 unbanned,
// 并回答"了结之前它是什么状态"。
//
// 状态集合里必须有 BanDeferred。速率闸挡下的封号以 deferred 落行,语义是
// "先让人看一眼";但在此之前 adminUnban 只认 banned/pending/failed,管理员在封禁
// 列表里看得见这一行却动不了它 —— 速率闸承诺的人工出口根本不存在,deferred 行唯一
// 的归宿是"等该用户再违规一次被自动提升执行"。那等于速率闸只是延迟了封号,
// 而不是把决定权交给人,与它自身的语义相反。
//
// CAS 精确锁定读到的那个状态,落空就重读重试,而不是用宽松的 `status <> unbanned`:
// 调用方要拿这个状态决定要不要去主库放人,读到写之间 resolveBanClaim 可能刚把
// deferred 提升成 pending 并真的把人禁用了。拿旧状态走"不予封禁"分支,会让用户
// 永久留在禁用态而封禁行却写着已解除。
func claimUnban(gdb *gorm.DB, userId int, note string, operatorId int) (*Ban, error) {
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	for attempt := 0; attempt < 3; attempt++ {
		var ban Ban
		err := gdb.Where("user_id = ? AND status IN ?", userId,
			[]string{BanBanned, BanPending, BanFailed, BanDeferred}).
			Order("id desc").Take(&ban).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("该用户没有待解除的违规封禁")
			}
			return nil, err
		}
		res := gdb.Model(&Ban{}).
			Where("id = ? AND status = ?", ban.Id, ban.Status).
			Updates(map[string]any{
				"status":      BanUnbanned,
				"unbanned_at": common.GetTimestamp(),
				"unbanned_by": operatorId,
				"unban_note":  truncate(note, 512),
			})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			return &ban, nil
		}
	}
	// 连续三轮都被别的路径抢先改写。宁可让管理员重试,也不能猜一个状态往下走。
	return nil, fmt.Errorf("该用户的封禁状态正在变化中,请稍后重试")
}

// ───────────────────────────── 申诉 ─────────────────────────────

func adminListAppeals(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Appeal{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	rows := make([]Appeal, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": rows, "total": total})
}

func adminReviewAppeal(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "非法的申诉 id")
		return
	}
	var req struct {
		Decision     string `json:"decision"` // approved | rejected
		Note         string `json:"note"`
		Refund       bool   `json:"refund"`
		Unban        bool   `json:"unban"`
		ResetCounter bool   `json:"reset_counter"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	if req.Decision != AppealApproved && req.Decision != AppealRejected {
		badRequest(c, "decision 只能是 approved 或 rejected")
		return
	}

	var ap Appeal
	if err := db.Get().Where("id = ?", id).Take(&ap).Error; err != nil {
		notFound(c)
		return
	}
	// 闸门必须落在状态翻转**之前**:approved 分支会顺手撤记录、退款、解封、
	// 清计数,四件事全部由申诉人自己批准的话,前面三道闸门都成了摆设。
	if denyActorOverTarget(c, "appeals.review", ap.UserId) {
		return
	}
	now := common.GetTimestamp()
	res := db.Get().Model(&Appeal{}).
		Where("id = ? AND status = ?", ap.Id, AppealPending).
		Updates(map[string]any{
			"status":      req.Decision,
			"reviewer_id": c.GetInt("id"),
			"review_note": truncate(req.Note, 1000),
			"reviewed_at": now,
			"updated_at":  now,
		})
	if res.Error != nil {
		internalError(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		badRequest(c, "该申诉已处理")
		return
	}

	out := gin.H{"refunded_quota": int64(0), "unbanned": false}
	if req.Decision == AppealApproved {
		var rec Record
		if err := db.Get().Where("id = ?", ap.RecordId).Take(&rec).Error; err == nil {
			refunded, err := revokeRecord(c, &rec, "申诉通过:"+req.Note, req.Refund, c.GetInt("id"))
			if err != nil {
				common.SysError("qianye/violation: 申诉通过后撤销记录失败: " + err.Error())
			}
			out["refunded_quota"] = refunded
		}
		if req.Unban {
			if err := unbanUser(c, ap.UserId, "申诉通过", req.ResetCounter, c.GetInt("id")); err != nil {
				common.SysError("qianye/violation: 申诉通过后解封失败: " + err.Error())
			} else {
				out["unbanned"] = true
			}
		}
	} else {
		// 驳回后把记录放回 active:否则它会永远停在 appealed,
		// 管理员事后即使发现确实是误判也再撤销不了。
		_ = db.Get().Model(&Record{}).
			Where("id = ? AND status = ?", ap.RecordId, RecordAppealed).
			Update("status", RecordActive).Error
	}

	// 申诉裁决本身必须留痕。这个函数能一次性撤销封禁 + 翻转扣费(退款),
	// 在这条埋点之前它整个是零审计的:revokeRecord 与 unbanUser 各自写的是
	// "记录被撤销""用户被解封",没有任何一行回答"是谁、依据哪条申诉批的"。
	// 而这两个子操作还都是 fail-open 的(失败只 SysError),裁决记录与它们的
	// 实际结果对不上正是事后要查的东西,所以 refunded/unbanned 一并入快照。
	refunded, _ := out["refunded_quota"].(int64)
	audit.Write(c, audit.Entry{
		TraceNo:      fmt.Sprintf("appeal-%d", ap.Id),
		Category:     qymodel.AuditCategoryViolation,
		Action:       "appeals.review",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: ap.UserId,
		AmountQuota:  refunded,
		Result:       qymodel.ResultOK,
		Reason:       truncate("申诉裁决("+req.Decision+"): "+req.Note, 500),
		BeforeSnap: fmt.Sprintf(`{"appeal_id":%d,"record_id":%d,"status":%q}`,
			ap.Id, ap.RecordId, AppealPending),
		AfterSnap: fmt.Sprintf(
			`{"status":%q,"refund_requested":%t,"unban_requested":%t,"reset_counter":%t,"refunded_quota":%d,"unbanned":%t}`,
			req.Decision, req.Refund, req.Unban, req.ResetCounter, refunded, out["unbanned"] == true),
	})
	respond(c, out)
}

// ───────────────────────────── 统计与运维 ─────────────────────────────

// adminStats 汇总命中分布、熔断状态与影子模式命中量。
//
// shadow_hits 是切真实模式前唯一的决策依据:它回答"如果现在打开真实模式,
// 过去 N 小时会有多少用户被扣费或封号"。
func adminStats(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	hours := httpq.Int(c, "hours", 24)
	if hours <= 0 || hours > 24*90 {
		hours = 24
	}
	since := common.GetTimestamp() - int64(hours)*3600

	type bucket struct {
		Key      string `json:"key"`
		Cnt      int64  `json:"cnt"`
		FeeQuota int64  `json:"fee_quota"`
	}
	// 这两个桶走的是 db.Scan():GORM 在结果集一行都没有时**根本不会碰 dest**
	// (finisher_api.go 先 rows.Next() 再 ScanRows),nil 切片序列化成 null
	// 而不是 [] —— 与提现队列角标炸掉的是同一个形状,新站点第一天打开统计页
	// 就会命中。判据与机器校验见 qianye/json_array_guard_test.go。
	byRule := make([]bucket, 0, 50)
	byModel := make([]bucket, 0, 50)
	gdb := db.Get()
	if err := gdb.Model(&Record{}).
		Select("rule_name as `key`, COUNT(*) as cnt, COALESCE(SUM(fee_quota),0) as fee_quota").
		Where("created_at >= ?", since).Group("rule_name").Order("cnt desc").Limit(50).
		Scan(&byRule).Error; err != nil {
		internalError(c, err)
		return
	}
	if err := gdb.Model(&Record{}).
		Select("model_name as `key`, COUNT(*) as cnt, COALESCE(SUM(fee_quota),0) as fee_quota").
		Where("created_at >= ?", since).Group("model_name").Order("cnt desc").Limit(50).
		Scan(&byModel).Error; err != nil {
		internalError(c, err)
		return
	}

	var totalFee, shadowCnt, blockedCnt, clampCnt, recCnt int64
	gdb.Model(&Record{}).Where("created_at >= ?", since).Count(&recCnt)
	gdb.Model(&Record{}).Where("created_at >= ?", since).Select("COALESCE(SUM(fee_quota),0)").Scan(&totalFee)
	gdb.Model(&Record{}).Where("created_at >= ? AND shadow = ?", since, true).Count(&shadowCnt)
	gdb.Model(&Record{}).Where("created_at >= ? AND blocked = ?", since, true).Count(&blockedCnt)
	gdb.Model(&Record{}).Where("created_at >= ? AND quota_clamp <> ''", since).Count(&clampCnt)

	var banCnt int64
	gdb.Model(&Ban{}).Where("created_at >= ?", since).Count(&banCnt)

	snap := Snapshot()
	respond(c, gin.H{
		"hours":        hours,
		"record_count": recCnt,
		"blocked":      blockedCnt,
		"shadow_count": shadowCnt,
		"fee_quota":    totalFee,
		"clamp_count":  clampCnt,
		"ban_count":    banCnt,
		"by_rule":      byRule,
		"by_model":     byModel,
		"breaker":      breakerStats(),
		"rules": gin.H{
			"version":     snap.version,
			"loaded_at":   snap.loadAt,
			"prompt_rule": len(snap.promptRules),
			"post_rule":   len(snap.postRules),
			// 删掉全局开关之后,"现在有没有规则在真实扣钱"由这两个数回答。
			// 不下发的话前端只能自己按分页拉规则再数一遍 —— 那是同一个事实的
			// 第二份拷贝,而且拉不全(列表是分页的)。
			"shadow_rule":  snap.shadowRules,
			"enforce_rule": snap.enforceRules,
		},
		"policy": gin.H{
			"insufficient_balance": config.Get().Violation.InsufficientBalancePolicy,
			"auto_ban_threshold":   config.Get().Violation.AutoBanThreshold,
			"auto_ban_window_h":    config.Get().Violation.AutoBanWindowHours,
			"max_fee_quota":        config.Get().Violation.MaxFeeQuota,
		},
	})
}

// ───────────────────────────── 违规计数器 ─────────────────────────────

// adminListCounters 列出用户维度的滚动窗口违规计数。
//
// 没有它,"重置计数器"这个动作就是盲操作:管理员根本不知道该重置谁。
// 影响自动封号的是 hit_count,所以默认按它倒序 —— 排在最前面的就是"最接近
// 封号线"的那批账号。
func adminListCounters(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Counter{})
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	rows := make([]Counter, 0, size)
	if err := q.Order("hit_count desc, user_id asc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	// 阈值与窗口一并下发:光看 hit_count 无法判断"离封号还有几次",
	// 而前端自己抄一份阈值就是同一个值的第二份拷贝。
	//
	// 取的是**兜底策略档**,不是 YAML 的两个旧字段。判定侧早就换成了按分组解析的
	// 策略档(resolveBanPolicy),YAML 只在库里连兜底行都没有时当种子用 ——
	// 继续下发 YAML 值,管理员改完兜底档之后这张卡片会一直显示改之前的数字,
	// 而它标题上写的正是"离封号还有几次"。窗口可能是 WindowUnlimited(-1),
	// 由前端换成"累计"的说法。
	fallback := banPolicies().fallback
	respond(c, gin.H{
		"items": rows, "total": total,
		"threshold":    fallback.Threshold,
		"window_hours": effectiveWindowHours(fallback.WindowHours),
	})
}

// adminResetCounter 把某个用户当前窗口的违规计数清零。
//
// 本轮之前影子命中会照常推进计数(见 persist),现网的计数器因此已经被污染,
// 而历史行无法区分哪几次来自影子。这个动作是唯一的补救出口:显式、逐个、写审计。
// 绝不提供"一键清全库"—— 那会把真实违规的累计一起抹掉,且事后无从解释。
func adminResetCounter(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	userId := pathIntParam(c, "userId")
	if userId <= 0 {
		badRequest(c, "非法的用户 id")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)

	if denyActorOverTarget(c, "counters.reset", userId) {
		return
	}

	before, catsBefore, reset, err := resetUserCounter(c.Request.Context(), db.Get(), userId)
	// 类型线是与账号总量线并列的封号触发器,而管理端没有任何页面显示它。
	// 重置把两条线一起清了,那么"清之前每条线上各是多少"就必须一起进审计与响应,
	// 否则管理员根本不知道刚才那一下动了什么。
	catBefore := make([]map[string]any, 0, len(catsBefore))
	for _, cc := range catsBefore {
		catBefore = append(catBefore, map[string]any{
			"category_id": cc.CategoryId, "hit_count": cc.HitCount,
			"window_start": cc.WindowStart, "total_count": cc.TotalCount,
		})
	}
	// 审计写在返回之前,成功与失败都写:清零会直接改变"这个账号离封号还有几次",
	// 事后必须能追溯到人。
	result, reason := qymodel.ResultOK, truncate(req.Reason, 512)
	if err != nil {
		result = qymodel.ResultFail
		reason = truncate(req.Reason, 400) + "(失败: " + err.Error() + ")"
	}
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryViolation,
		Action:       "counters.reset",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		Result:       result,
		Reason:       reason,
		BeforeSnap: common.MapToJsonStr(map[string]any{
			"hit_count": before.HitCount, "window_start": before.WindowStart,
			"total_count": before.TotalCount, "ban_cycle": before.BanCycle,
			"category_counters": catBefore,
		}),
	})
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{
		"reset": reset, "hit_count_before": before.HitCount,
		"category_counters_before": catBefore,
	})
}

// adminResetBreaker 手动解除**熔断**导致的强制影子回落。
//
// 它管不到全局影子开关(YAML 或 qy_settings 的覆盖)—— 那条路走 adminPutMode。
// 两者必须分开:熔断是"系统自己踩的刹车",全局开关是"人定的发布口径",
// 让一个按钮同时松开两者,会让一次熔断恢复顺手把还没准备好的规则全部放出去。
func adminResetBreaker(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	clearForcedShadow()
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      "breaker.reset",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Reason:      "手动解除自动回落的影子模式",
	})
	respond(c, breakerStats())
}

// pathIntParam 读的是**路径**参数(/:userId),不是查询参数。
//
// 它以前叫 queryIntParam —— 一个读 c.Param 却叫 query 的名字。这类命名漂移
// 正是"同一概念的第 N 份拷贝"能悄悄长出来的土壤:下一个人搜 queryInt 会搜到它,
// 以为查询参数解析在本包里还有一份,于是照着再抄一份。
func pathIntParam(c *gin.Context, key string) int {
	v, ok := pathInt64(c, key)
	if !ok || v > httpq.MaxQueryInt {
		return 0
	}
	return int(v)
}
