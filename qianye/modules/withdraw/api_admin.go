package withdraw

import (
	"errors"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 管理端一律用 FlagCore 而不是 FlagWithdraw 做门禁。
//
// 提现功能被临时关停时,队列里已经存在的单仍然必须能被处理 ——
// 否则用户的佣金会被永久冻在 frozen 里,关一个开关就制造一批资金纠纷。

// handleAdminList 是审核队列。
func handleAdminList(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	page, size := paginate(c)
	q := db.Get().Model(&Withdrawal{})
	q = applyStatusFilter(q, c.Query("status"))
	if v := strings.TrimSpace(c.Query("method")); v != "" {
		q = q.Where("method = ?", v)
	}
	if v := intQuery(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	if v := strings.TrimSpace(c.Query("withdraw_no")); v != "" {
		q = q.Where("withdraw_no = ?", v)
	}
	if c.Query("reconcile") == "hold" {
		q = q.Where("reconcile_state = ?", ReconcileHold)
	}
	if c.Query("risk_only") == "true" {
		q = q.Where("risk_flags <> ''")
	}
	if v := int64Query(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := int64Query(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	var rows []Withdrawal
	// 默认按申请时间正序:审核队列要先进先出,超时的单才会浮在最上面。
	order := "id asc"
	if c.Query("order") == "desc" {
		order = "id desc"
	}
	if err := q.Order(order).Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	items := make([]*adminOrderView, 0, len(rows))
	for i := range rows {
		items = append(items, toAdminView(&rows[i], nil))
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleAdminStats 返回队列角标:待审 / 待打款 / 对账异常。
func handleAdminStats(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	type bucket struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
		Quota  int64  `json:"quota"`
	}
	var rows []bucket
	if err := db.Get().Model(&Withdrawal{}).
		Select("status, COUNT(*) AS count, COALESCE(SUM(quota), 0) AS quota").
		Where("status IN ?", []string{StatusPending, StatusApproved, StatusPaying}).
		Group("status").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Status < rows[j].Status })

	var hold, slaBreached int64
	if err := db.Get().Model(&Withdrawal{}).
		Where("reconcile_state = ?", ReconcileHold).Count(&hold).Error; err != nil {
		db.MarkFailure(err)
	}
	if hours := config.Get().Withdraw.ReviewSLAHours; hours > 0 {
		deadline := common.GetTimestamp() - int64(hours)*3600
		if err := db.Get().Model(&Withdrawal{}).
			Where("status = ? AND created_at < ?", StatusPending, deadline).
			Count(&slaBreached).Error; err != nil {
			db.MarkFailure(err)
		}
	}
	respondOK(c, gin.H{"buckets": rows, "reconcile_hold": hold, "sla_breached": slaBreached})
}

// handleAdminGet 返回单据详情与完整时间线(含 Detail 与真实操作者姓名)。
func handleAdminGet(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	w, err := loadWithdrawal(id)
	if err != nil {
		respondErr(c, err)
		return
	}
	events, err := loadEvents(w.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, toAdminView(w, events))
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

type markPaidRequest struct {
	PayoutRef  string `json:"payout_ref"`
	PaidAt     int64  `json:"paid_at"`
	PayoutNote string `json:"payout_note"`
}

type resolveRequest struct {
	Decision string `json:"decision"`
	Evidence string `json:"evidence"`
}

func handleAdminApprove(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := approve(c, id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleAdminReject 拒绝申请。理由必填 —— 需求原文的核心诉求之一。
func handleAdminReject(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req reasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := reject(c, id, req.Reason)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

func handleAdminMarkPaid(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req markPaidRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := markPaid(c, id, req.PayoutRef, req.PaidAt, req.PayoutNote)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

func handleAdminFail(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req reasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := markFailed(c, id, req.Reason)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

func handleAdminResolve(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req resolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := resolveHold(c, id, req.Decision, req.Evidence)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleAdminRevealPayee 返回收款信息明文。
//
// 这是全模块唯一能拿到明文的出口,因此:
//   - 强制填写事由(≥4 字符),没有事由的访问事后无法区分正常核对与顺手看看
//   - 每次调用写一条 qy_pii_audits + 一条全局审计,只增不改
//   - 解密失败不回 500,而是回脱敏值与明确提示 —— 密钥轮换后旧密文解不开是
//     可预期的运维状态,不是程序错误
func handleAdminRevealPayee(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	reason := strings.TrimSpace(c.Query("reason"))
	if len([]rune(reason)) < 4 {
		respondErr(c, errReasonRequired)
		return
	}

	var payee Payee
	err := db.Get().Where("withdrawal_id = ?", id).Take(&payee).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondErr(c, errPayeeNotFound)
		return
	}
	if err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	data, err := openPayee(payee.Nonce, payee.Cipher, withdrawAAD(payee.WithdrawNo))
	if err != nil {
		// 失败的访问同样要留痕:反复解不开也是一种需要被发现的异常。
		recordPiiAccess(c, &payee, reason, "view_plain_failed", "")
		respondErr(c, err)
		return
	}

	fields := make([]string, 0, len(data))
	for k := range data {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	recordPiiAccess(c, &payee, reason, "view_plain", strings.Join(fields, ","))

	respondOK(c, gin.H{
		"channel":     payee.Channel,
		"masked":      payee.Masked,
		"payee":       data,
		"withdraw_no": payee.WithdrawNo,
	})
}

// handleAdminPiiAudits 查询明文访问记录。
func handleAdminPiiAudits(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	page, size := paginate(c)
	q := db.Get().Model(&PiiAudit{})
	if v := intQuery(c, "admin_id", 0); v > 0 {
		q = q.Where("admin_id = ?", v)
	}
	if v := intQuery(c, "target_user_id", 0); v > 0 {
		q = q.Where("target_user_id = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	var rows []PiiAudit
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size})
}

// recordPiiAccess 记录一次明文访问。写失败只告警不阻断 ——
// 但这是必须被发现的异常:审计静默丢失会让事故无法复盘。
func recordPiiAccess(c *gin.Context, payee *Payee, reason, action, fields string) {
	a := actorOf(c)
	row := &PiiAudit{
		Resource:     "withdrawal_payee",
		ResourceId:   payee.WithdrawalId,
		TargetUserId: payee.UserId,
		AdminId:      a.Id,
		AdminName:    truncate(a.Name, 64),
		Action:       action,
		Fields:       truncate(fields, 255),
		Reason:       truncate(reason, 255),
		Ip:           truncate(a.IP, 64),
		UserAgent:    truncate(c.Request.UserAgent(), 255),
		CreatedAt:    common.GetTimestamp(),
	}
	if err := db.Get().Create(row).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 写入 PII 访问审计失败: " + err.Error())
	}
	audit.Write(c, audit.Entry{
		TraceNo:      payee.WithdrawNo,
		Category:     qymodel.AuditCategoryWithdraw,
		Action:       "withdraw.payee." + action,
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  a.Id,
		ActorName:    a.Name,
		TargetUserId: payee.UserId,
		Result:       qymodel.ResultOK,
		Reason:       reason,
	})
}
