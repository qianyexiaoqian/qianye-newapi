package violation

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/shopspring/decimal"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ruleUpsertReq 是规则的新建/编辑入参。
//
// 金额与倍数走字符串:JSON number 在前端是 float64,0.1 这类值往返一次就会
// 变成 0.10000000000000001,而它会被直接乘进用户的账单。
type ruleUpsertReq struct {
	Name           string `json:"name"`
	Remark         string `json:"remark"`
	PublicReason   string `json:"public_reason"`
	Enabled        bool   `json:"enabled"`
	DryRun         bool   `json:"dry_run"`
	Priority       int    `json:"priority"`
	Phase          string `json:"phase"`
	MatchType      string `json:"match_type"`
	Pattern        string `json:"pattern"`
	CaseSensitive  bool   `json:"case_sensitive"`
	ModelScope     string `json:"model_scope"`
	GroupScope     string `json:"group_scope"`
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
	dst.DryRun = r.DryRun
	dst.Priority = r.Priority
	dst.Phase = r.Phase
	dst.MatchType = r.MatchType
	dst.Pattern = r.Pattern
	dst.CaseSensitive = r.CaseSensitive
	dst.ModelScope = truncate(r.ModelScope, 2048)
	dst.GroupScope = truncate(r.GroupScope, 1024)
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
	page, size := pageParams(c)
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
		Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
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
	before := common.MapToJsonStr(map[string]any{"enabled": row.Enabled, "action": row.Action, "pattern": row.Pattern})
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
			"dry_run": row.DryRun, "pattern": truncate(row.Pattern, 1024),
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
	in := scanInput{Model: req.Model, Group: req.Group, Text: clipHeadTail(req.Sample, maxScanBytes)}
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

func adminListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := pageParams(c)
	q := db.Get().Model(&Record{})
	if v := queryInt(c, "user_id", 0); v > 0 {
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
	if v := queryInt64(c, "rule_id", 0); v > 0 {
		q = q.Where("rule_id = ?", v)
	}
	if v := queryInt64(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := queryInt64(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []Record
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
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
//   - 状态条件 UPDATE(WHERE status='active')保证连点两次只退一次款;
//   - 退款走 twophase,以 rec_no 为幂等键,即使补偿任务重跑也不会重复退;
//   - 计数回退带窗口条件,窗口滚动后不回退(旧值已经不在窗口内)。
func revokeRecord(c *gin.Context, rec *Record, reason string, refund bool, operatorId int) (int64, error) {
	now := common.GetTimestamp()
	// 条件里必须同时接受 appealed:用户提交申诉时记录已经从 active 变成 appealed,
	// 只认 active 会让"申诉通过"永远撤销不了任何记录 —— 申诉闭环直接断掉。
	res := db.Get().Model(&Record{}).
		Where("id = ? AND status IN ?", rec.Id, []string{RecordActive, RecordAppealed}).
		Updates(map[string]any{
			"status":        RecordRevoked,
			"revoked_by":    operatorId,
			"revoked_at":    now,
			"revoke_reason": truncate(reason, 512),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, nil // 已撤销,幂等返回
	}

	// 计数回退。撤销一条误判记录后,用户不该继续背着这次计数走向封号。
	if rec.Counted && rec.CountWeight > 0 {
		var counter Counter
		if err := db.Get().Where("user_id = ?", rec.UserId).Take(&counter).Error; err == nil {
			if e := revertCounter(rec.UserId, rec.CountWeight, counter.WindowStart); e != nil {
				common.SysError("qianye/violation: 撤销时回退计数失败: " + e.Error())
			}
		}
	}

	var refunded int64
	if refund && rec.FeeQuota > 0 &&
		(rec.FeeStatus == FeeStatusCharged || rec.FeeStatus == FeeStatusTruncated) {
		if err := refundFee(rec); err != nil {
			// 退款失败不回滚撤销:记录已经标记为误判是正确的,
			// 钱可以由管理员重试或人工补,但把误判标记撤回去只会更混乱。
			common.SysError("qianye/violation: 违规扣费退还失败: " + err.Error())
		} else {
			refunded = rec.FeeQuota
			_ = db.Get().Model(&Record{}).Where("id = ?", rec.Id).
				Updates(map[string]any{"fee_status": FeeStatusRefunded, "refund_quota": refunded}).Error
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

// refundFee 通过跨库两阶段把违规扣费退还给用户。
//
// 必须走 twophase 而不是裸 IncreaseUserQuota:退款是"扩展库记账 + 主库动钱",
// 中间崩溃会留下"记录已标记退款但钱没到账"的悬案,而 outbox 探针是唯一
// 能精确判定主库到底动没动的手段。
func refundFee(rec *Record) error {
	amount := rec.FeeQuota
	if amount <= 0 || amount > int64(common.MaxQuota) {
		return fmt.Errorf("退款金额越界: %d", amount)
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	_, err := twophase.Execute(ctx, twophase.Request{
		Kind:        qymodel.KindViolationFee,
		IdemScope:   "violation_refund",
		IdemKey:     rec.RecNo,
		UserId:      rec.UserId,
		AmountQuota: amount,
		RefType:     "violation_record",
		RefId:       fmt.Sprint(rec.Id),
		MainApply: func(tx *gorm.DB, order *qymodel.FundOrder) error {
			// 加款前的上限校验:users.quota 是 int32,加爆会翻成负数,
			// 那等于把误判赔偿变成账号清零。
			res := tx.Model(&model.User{}).
				Where("id = ? AND quota <= ?", rec.UserId, int64(common.MaxQuota)-amount).
				Update("quota", gorm.Expr("quota + ?", amount))
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return fmt.Errorf("退款会导致额度溢出,已拒绝(user=%d)", rec.UserId)
			}
			return nil
		},
		AfterCommit: func(order *qymodel.FundOrder) {
			if e := model.QyApplyUserQuotaCacheDelta(rec.UserId, amount); e != nil {
				common.SysError("qianye/violation: 退款后刷新额度缓存失败: " + e.Error())
			}
			// 用 LogTypeRefund 而不是 LogTypeConsume:退款计进消费统计
			// 会让"本月消费"凭空变小,财务对账直接对不上。
			model.QyRecordLedgerLog(rec.UserId, model.LogTypeRefund,
				fmt.Sprintf("违规扣费撤销退还 %d(记录 %s)", amount, rec.RecNo),
				order.OrderNo, map[string]interface{}{
					"qy_violation_rec_no": rec.RecNo,
					"qy_refund_quota":     amount,
				})
		},
	})
	return err
}

// ───────────────────────────── 封禁 ─────────────────────────────

func adminListBans(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := pageParams(c)
	q := db.Get().Model(&Ban{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := queryInt(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []Ban
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": rows, "total": total})
}

func adminUnban(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	userId := queryIntParam(c, "userId")
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
	var ban Ban
	err := db.Get().Where("user_id = ? AND status IN ?", userId,
		[]string{BanBanned, BanPending, BanFailed}).Order("id desc").Take(&ban).Error
	if err != nil {
		return fmt.Errorf("该用户没有待解除的违规封禁")
	}
	now := common.GetTimestamp()
	res := db.Get().Model(&Ban{}).
		Where("id = ? AND status <> ?", ban.Id, BanUnbanned).
		Updates(map[string]any{
			"status":      BanUnbanned,
			"unbanned_at": now,
			"unbanned_by": operatorId,
			"unban_note":  truncate(note, 512),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil // 已解封,幂等
	}
	// 周期 +1 必须与解封同时发生,否则该用户的自动封号从此静默失效。
	if e := openNewBanCycle(userId, resetCounter); e != nil {
		common.SysError("qianye/violation: 解封时推进封禁周期失败: " + e.Error())
	}
	if e := enableUserAfterUnban(userId, &ban, operatorId); e != nil {
		return e
	}
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryViolation,
		Action:       "bans.unban",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  operatorId,
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		TraceNo:      fmt.Sprintf("ban:%d", ban.Id),
		Reason:       truncate(note, 512),
	})
	return nil
}

// ───────────────────────────── 申诉 ─────────────────────────────

func adminListAppeals(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := pageParams(c)
	q := db.Get().Model(&Appeal{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := queryInt(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []Appeal
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
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
	hours := queryInt(c, "hours", 24)
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
		},
		"policy": gin.H{
			"insufficient_balance": config.Get().Violation.InsufficientBalancePolicy,
			"auto_ban_threshold":   config.Get().Violation.AutoBanThreshold,
			"auto_ban_window_h":    config.Get().Violation.AutoBanWindowHours,
			"max_fee_quota":        config.Get().Violation.MaxFeeQuota,
		},
	})
}

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

func queryIntParam(c *gin.Context, key string) int {
	v, ok := pathInt64(c, key)
	if !ok || v > int64(^uint32(0)>>1) {
		return 0
	}
	return int(v)
}
