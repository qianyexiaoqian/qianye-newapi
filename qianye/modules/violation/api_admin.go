package violation

import (
	"context"
	"errors"
	"fmt"
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
	Mode           string `json:"mode"`
	Priority       int    `json:"priority"`
	Phase          string `json:"phase"`
	MatchType      string `json:"match_type"`
	Pattern        string `json:"pattern"`
	CaseSensitive  bool   `json:"case_sensitive"`
	ModelScope     string `json:"model_scope"`
	GroupScope     string `json:"group_scope"`
	GroupScopeMode string `json:"group_scope_mode"`
	Action         string `json:"action"`
	FeeMode        string `json:"fee_mode"`
	FeeFixed       string `json:"fee_fixed"`
	FeeMultiple    string `json:"fee_multiple"`
	FeeMaxQuota    int64  `json:"fee_max_quota"`
	CountWeight    int    `json:"count_weight"`
	Severity       int    `json:"severity"`
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

	dst.Name = truncate(strings.TrimSpace(r.Name), 128)
	dst.Remark = truncate(r.Remark, 512)
	dst.PublicReason = truncate(strings.TrimSpace(r.PublicReason), 128)
	dst.Enabled = r.Enabled
	// 空串折回影子。漏传字段(旧前端、脚本、curl 手敲)必须落在不扣钱的那一侧;
	// 其余非法取值交给 ValidateRule 明确报错,而不是在这里静默纠正成 shadow ——
	// 静默纠正会让"我明明填了 enforce"变成一个查不出来的问题。
	dst.Mode = strings.ToLower(strings.TrimSpace(r.Mode))
	if dst.Mode == "" {
		dst.Mode = ModeShadow
	}
	dst.Priority = r.Priority
	dst.Phase = r.Phase
	dst.MatchType = r.MatchType
	dst.Pattern = r.Pattern
	dst.CaseSensitive = r.CaseSensitive
	dst.ModelScope = truncate(r.ModelScope, 2048)
	dst.GroupScope = truncate(r.GroupScope, 1024)
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
	dst.CountWeight = r.CountWeight
	dst.Severity = r.Severity
	dst.ArchiveContext = r.ArchiveContext
	dst.BlockMessage = truncate(r.BlockMessage, 512)
	return ValidateRule(dst)
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
	var rows []Rule
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
	row := &Rule{CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(), CreatedBy: c.GetInt("id")}
	if err := req.apply(row); err != nil {
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
	row.UpdatedAt = common.GetTimestamp()
	row.UpdatedBy = c.GetInt("id")
	if err := db.Get().Save(&row).Error; err != nil {
		internalError(c, err)
		return
	}
	afterRuleChange(c, "rules.update", &row, before)
	respond(c, gin.H{})
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
	if err := gdb.Exec(`INSERT INTO qy_violation_rule_version (id, version, updated_at)
		VALUES (1, 1, ?)
		ON DUPLICATE KEY UPDATE version = version + 1, updated_at = ?`, now, now).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/violation: 规则版本号自增失败,其他节点可能延迟感知: " + err.Error())
	}
}

// adminTestRule 是规则试跑:粘一段文本,立刻看到是否命中、命中什么、耗时多少。
//
// 这是本模块最重要的一个接口。没有它,管理员只能"改完上线看线上炸不炸",
// 而线上一炸就是全站用户被误扣误封。
func adminTestRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req struct {
		Rule   ruleUpsertReq `json:"rule"`
		Sample string        `json:"sample_text"`
		Model  string        `json:"model"`
		Group  string        `json:"group"`
		// RateCount 让 request_rate 规则也能试跑。没有它,频率规则在试跑面板里
		// 永远显示"未命中" —— 一个看起来权威、实则只是没有输入的结论,
		// 比不给试跑更容易让人放心上线。
		RateCount int `json:"rate_count"`
	}
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
	in := scanInput{
		Model:     req.Model,
		Group:     req.Group,
		Text:      clipHeadTail(req.Sample, maxScanBytes),
		RateCount: req.RateCount,
	}
	inScope := cr.inScope(req.Model, req.Group)
	v := scan([]*compiledRule{cr}, cr.words, in, in.Text)
	out := gin.H{"scope_ok": inScope, "matched": false, "terms": []string{}, "snippet": ""}
	if v != nil && v.Rule != nil {
		out["matched"] = true
		out["terms"] = v.Terms
		out["snippet"] = v.Snippet
		out["elapsed_us"] = v.Elapsed.Microseconds()
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
	var rows []Record
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
	// 只在本次真正完成撤销时做:revertCounter 是无条件减法,重复执行会把当前窗口里
	// 其他违规的合法计数一起扣掉,反而放过真正的违规用户。
	if first && rec.Counted && rec.CountWeight > 0 {
		var counter Counter
		if err := gdb.Where("user_id = ?", rec.UserId).Take(&counter).Error; err == nil {
			if e := revertCounter(rec.UserId, rec.CountWeight, counter.WindowStart); e != nil {
				common.SysError("qianye/violation: 撤销时回退计数失败: " + e.Error())
			}
		}
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
	var rows []Ban
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
	var rows []Appeal
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
	var byRule, byModel []bucket
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
	var rows []Counter
	if err := q.Order("hit_count desc, user_id asc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{
		"items": rows, "total": total,
		// 阈值一并下发:光看 hit_count 无法判断"离封号还有几次",
		// 而前端自己抄一份阈值就是同一个值的第二份拷贝。
		"threshold":    config.Get().Violation.AutoBanThreshold,
		"window_hours": config.Get().Violation.AutoBanWindowHours,
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

	before, reset, err := resetUserCounter(c.Request.Context(), db.Get(), userId)
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
		}),
	})
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"reset": reset, "hit_count_before": before.HitCount})
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
