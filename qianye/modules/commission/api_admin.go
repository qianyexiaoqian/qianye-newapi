package commission

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// adminGetConfig 返回生效配置 + YAML 只读快照。
//
// 分成两块是刻意的:YAML 段(三个口径开关、扫描参数)涉及安全与启动行为,
// 只能改文件后重载;运营参数(费率、成熟期、封顶)才允许在这里改。
func adminGetConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	overrides, err := loadOverrides()
	if err != nil {
		internalError(c, err)
		return
	}
	s := effective()
	cm := config.Get().Commission
	respond(c, gin.H{
		"effective": gin.H{
			keyTopupRateBps:      s.TopupRateBps,
			keyConsumeRateBps:    s.ConsumeRateBps,
			keyMinSettleQuota:    s.MinSettleQuota,
			keyMaxPerOrderQuota:  s.MaxPerOrderQuota,
			keyHoldingDays:       s.HoldingDays,
			keyDailyCapQuota:     s.DailyCapQuota,
			keyLargeAlertQuota:   s.LargeAlertQuota,
			keyMinInviteeAgeHour: s.MinInviteeAgeHours,
		},
		"overrides":     overrides,
		"editable_keys": editableKeys,
		"yaml_readonly": gin.H{
			"enabled":                       cm.Enabled,
			"topup_rate_bps":                cm.TopupRateBps,
			"consume_rate_bps":              cm.ConsumeRateBps,
			"exclude_redemption_and_manual": cm.ExcludeRedemptionAndManual,
			"exclude_subscription_consume":  cm.ExcludeSubscriptionConsume,
			"refund_clawback":               cm.RefundClawback,
			"settle_interval_seconds":       cm.SettleIntervalSecs,
			"topup_scan_interval_seconds":   cm.TopupScanIntervalSec,
			"topup_scan_lookback_hours":     cm.TopupScanLookbackHours,
			"inviter_cache_seconds":         cm.InviterCacheSecs,
		},
	})
}

// adminPutConfig 修改运营参数。
//
// 每一次改动都必须写审计:费率直接决定平台要付多少钱,
// "谁在什么时候把 3% 改成 8%"事后必须能查到人。
func adminPutConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req map[string]int64
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if len(req) == 0 {
		badRequest(c, "qy_invalid_param", "没有需要修改的配置项")
		return
	}
	for k := range req {
		if !editable(k) {
			badRequest(c, "qy_invalid_param", "不可修改的配置项: "+k)
			return
		}
	}
	if v, ok := req[keyTopupRateBps]; ok && (v < 0 || v > 10000) {
		badRequest(c, "qy_invalid_param", "充值返佣比例必须在 0~10000 bps 之间")
		return
	}
	if v, ok := req[keyConsumeRateBps]; ok && (v < 0 || v > 10000) {
		badRequest(c, "qy_invalid_param", "消费返佣比例必须在 0~10000 bps 之间")
		return
	}
	if v, ok := req[keyMinSettleQuota]; ok && v <= 0 {
		badRequest(c, "qy_invalid_param", "最小结算额度必须大于 0")
		return
	}

	before := effective()
	operatorId := c.GetInt("id")
	for k, v := range req {
		if err := writeSetting(k, strconv.FormatInt(v, 10), operatorId); err != nil {
			internalError(c, err)
			return
		}
	}
	invalidateSettings()
	after := effective()

	beforeSnap, _ := common.Marshal(before)
	afterSnap, _ := common.Marshal(after)
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      "commission.config.update",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: operatorId,
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      "修改返佣运营参数",
		BeforeSnap:  string(beforeSnap),
		AfterSnap:   string(afterSnap),
	})
	respond(c, gin.H{"effective": after})
}

func editable(key string) bool {
	for _, k := range editableKeys {
		if k == key {
			return true
		}
	}
	return false
}

// adminListRecords 分页查询全平台计佣流水。
func adminListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	page, size := pageParams(c)
	q := db.Get().Model(&Accrual{})

	if v := queryInt(c, "inviter_id", 0); v > 0 {
		q = q.Where("inviter_id = ?", v)
	}
	if v := queryInt(c, "invitee_id", 0); v > 0 {
		q = q.Where("invitee_id = ?", v)
	}
	if v := c.Query("source_type"); v != "" {
		q = q.Where("source_type = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("accrual_no"); v != "" {
		q = q.Where("accrual_no = ?", v)
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
	var rows []Accrual
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	// 管理端返回原始行:管理员本就有权看到完整的 user_id 与订单号,
	// 脱敏只针对"邀请人看下线"这个方向。
	respond(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size})
}

// adminClawback 人工冲正。
func adminClawback(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		AccrualId       int64  `json:"accrual_id"`
		Quota           int64  `json:"quota"`
		Reason          string `json:"reason"`
		ClientRequestId string `json:"client_request_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.Reason == "" {
		// 人工改动资金必须留下理由,否则事后无法复盘。
		badRequest(c, "qy_reason_required", "必须填写冲正理由")
		return
	}
	if req.ClientRequestId == "" {
		badRequest(c, "qy_invalid_param", "缺少 client_request_id")
		return
	}
	operatorId := c.GetInt("id")
	created, err := manualClawback(req.AccrualId, req.Quota,
		itoa(operatorId)+":"+req.ClientRequestId, req.Reason)
	if err != nil {
		badRequest(c, "qy_clawback_failed", err.Error())
		return
	}
	audit.Write(c, audit.Entry{
		TraceNo:      created.AccrualNo,
		Category:     qymodel.AuditCategoryCommission,
		Action:       "commission.clawback",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  operatorId,
		ActorName:    c.GetString("username"),
		TargetUserId: created.InviterId,
		AmountQuota:  req.Quota,
		Result:       qymodel.ResultOK,
		Reason:       req.Reason,
	})
	respond(c, gin.H{"accrual_no": created.AccrualNo, "gross_amount": created.GrossAmount.String()})
}

// adminSettle 立即结算,不必等下一个周期。
func adminSettle(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	userId := queryInt(c, "user_id", 0)
	if userId <= 0 {
		badRequest(c, "qy_invalid_param", "必须指定 user_id")
		return
	}
	if err := settleOne(userId); err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"settled": true, "user_id": userId})
}

// adminBlockRelation 拉黑/解封一条邀请关系。
//
// 拉黑只停止未来计佣,不回收已发放的佣金 —— 回收要走冲正,那是另一个决定,
// 必须由人显式做出并留下理由。
func adminBlockRelation(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		InviteeId int    `json:"invitee_id"`
		Blocked   bool   `json:"blocked"`
		Reason    string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.InviteeId <= 0 {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	err := db.Get().Model(&InviteRelation{}).Where("invitee_id = ?", req.InviteeId).
		Updates(map[string]any{
			"blocked":    req.Blocked,
			"risk_flags": truncate(req.Reason, 255),
			"updated_at": common.GetTimestamp(),
		}).Error
	if err != nil {
		internalError(c, err)
		return
	}
	invalidateBlocked()
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryCommission,
		Action:       "commission.relation.block",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: req.InviteeId,
		Result:       qymodel.ResultOK,
		Reason:       req.Reason,
	})
	respond(c, gin.H{"invitee_id": req.InviteeId, "blocked": req.Blocked})
}

// adminInvalidateCache 在管理员改过 users.inviter_id 之后手动失效缓存。
func adminInvalidateCache(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	invalidateInviter(queryInt(c, "user_id", 0))
	invalidateSettings()
	invalidateBlocked()
	respond(c, gin.H{"invalidated": true})
}

// adminHealth 暴露返佣链路的关键指标。
//
// hot_queue.dropped > 0 必须告警:那是本模块唯一会造成
// "用户该拿的钱没拿到"的路径。
func adminHealth(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	pending, _ := pendingInviters(settleInviterBatch)
	respond(c, gin.H{
		"metrics":            metricsSnapshot(),
		"hot_queue":          guard.QueueStats(),
		"inviter_cache":      inviterCacheStats(),
		"topup_low_water":    peekTopupCursor(),
		"pending_inviters":   len(pending),
		"blocked_relations":  len(blockedInvitees()),
		"effective_settings": effective(),
	})
}
