package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// AdminHealth 返回扩展的运行状态,是排障的第一入口。
func AdminHealth(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	leases, _ := lease.List()
	ok(c, gin.H{
		"db":        db.Stats(),
		"hot_queue": guard.QueueStats(),
		"two_phase": twophase.Stats(),
		"leases":    leases,
		"migrate": gin.H{
			"table_count": db.TableCount(),
		},
		"config": gin.H{
			"path":      config.Path(),
			"loaded_at": config.LoadedAt(),
			"mtime":     config.ModTime(),
		},
		"node": gin.H{
			"name":      common.NodeName,
			"is_master": common.IsMasterNode,
			"holder":    lease.Holder(),
		},
	})
}

// AdminListFundOrders 分页查询资金单,支持按状态/类型/用户/单号筛选。
func AdminListFundOrders(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	page, size := pagination(c)
	q := db.Get().Model(&qymodel.FundOrder{})

	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", intQuery(c, "status", 0))
	}
	if v := c.Query("kind"); v != "" {
		q = q.Where("kind = ?", v)
	}
	if v := c.Query("order_no"); v != "" {
		q = q.Where("order_no = ?", v)
	}
	if v := c.Query("ref_id"); v != "" {
		q = q.Where("ref_id = ?", v)
	}
	if v := intQuery(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ? OR peer_user_id = ?", v, v)
	}
	if v := intQuery(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := intQuery(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		serverError(c, err)
		return
	}
	var items []qymodel.FundOrder
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		db.MarkFailure(err)
		serverError(c, err)
		return
	}
	ok(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// AdminReprobeFundOrder 立即对某笔单重跑一次主库探针,不必等补偿任务的下一轮。
func AdminReprobeFundOrder(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	orderNo := c.Param("order_no")
	if orderNo == "" {
		badRequest(c, "qy_invalid_param", "缺少单号")
		return
	}
	var order qymodel.FundOrder
	if err := db.Get().Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		fail(c, http.StatusNotFound, "qy_order_not_found", "单据不存在")
		return
	}
	applied, err := model.QyProbeFundOutbox(orderNo)
	if err != nil {
		serverError(c, err)
		return
	}
	ok(c, gin.H{
		"order_no":     orderNo,
		"status":       qymodel.StatusName(order.Status),
		"main_applied": applied,
	})
}

// AdminResolveFundOrder 人工裁决一笔无法自动判定的资金单。
//
// 只允许裁决 Uncertain 态:Pending 应交给补偿任务自动收敛,
// 已终态的单不允许改写 —— 那会破坏账目的不可变性。
func AdminResolveFundOrder(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	orderNo := c.Param("order_no")
	var req struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.Reason == "" {
		// 人工改动资金状态必须留下理由,否则事后无法复盘。
		badRequest(c, "qy_reason_required", "必须填写裁决理由")
		return
	}
	var target int8
	switch req.Decision {
	case "success":
		target = qymodel.StatusSuccess
	case "failed":
		target = qymodel.StatusFailed
	default:
		badRequest(c, "qy_invalid_param", "裁决结果只能是 success 或 failed")
		return
	}

	var order qymodel.FundOrder
	if err := db.Get().Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		fail(c, http.StatusNotFound, "qy_order_not_found", "单据不存在")
		return
	}
	if order.Status != qymodel.StatusUncertain {
		badRequest(c, "qy_order_not_uncertain",
			"只有处于人工裁决态的单据才能被裁决,当前状态: "+qymodel.StatusName(order.Status))
		return
	}

	now := common.GetTimestamp()
	res := db.Get().Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status = ?", orderNo, qymodel.StatusUncertain).
		Updates(map[string]any{
			"status":     target,
			"settled_at": now,
			"updated_at": now,
			"last_error": "人工裁决: " + req.Reason,
		})
	if res.Error != nil {
		serverError(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		badRequest(c, "qy_order_changed", "单据状态已变化,请刷新后重试")
		return
	}

	audit.Write(c, audit.Entry{
		TraceNo:      orderNo,
		Category:     qymodel.AuditCategoryFund,
		Action:       "fund.resolve",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: order.UserId,
		AmountQuota:  order.AmountQuota,
		Result:       qymodel.ResultOK,
		Reason:       req.Reason,
		BeforeSnap:   `{"status":"uncertain"}`,
		AfterSnap:    `{"status":"` + qymodel.StatusName(target) + `"}`,
	})
	ok(c, gin.H{"order_no": orderNo, "status": qymodel.StatusName(target)})
}

// AdminListAuditLogs 分页查询审计流水。
func AdminListAuditLogs(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	page, size := pagination(c)
	q := db.Get().Model(&qymodel.AuditLog{})

	if v := c.Query("category"); v != "" {
		q = q.Where("category = ?", v)
	}
	if v := c.Query("action"); v != "" {
		q = q.Where("action = ?", v)
	}
	if v := c.Query("trace_no"); v != "" {
		q = q.Where("trace_no = ?", v)
	}
	if v := intQuery(c, "actor_user_id", 0); v > 0 {
		q = q.Where("actor_user_id = ?", v)
	}
	if v := intQuery(c, "target_user_id", 0); v > 0 {
		q = q.Where("target_user_id = ?", v)
	}
	if v := intQuery(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := intQuery(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		serverError(c, err)
		return
	}
	var items []qymodel.AuditLog
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		db.MarkFailure(err)
		serverError(c, err)
		return
	}
	ok(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// AdminListLeases 展示后台任务的租约持有情况,用于确认多节点没有双跑。
func AdminListLeases(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	rows, err := lease.List()
	if err != nil {
		serverError(c, err)
		return
	}
	now := common.GetTimestamp()
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{
			"name":        r.Name,
			"holder":      r.Holder,
			"fence":       r.Fence,
			"lease_until": r.LeaseUntil,
			"expired":     r.LeaseUntil < now,
			"acquired_at": r.AcquiredAt,
		})
	}
	ok(c, gin.H{"items": items, "self": lease.Holder()})
}

// AdminReloadConfig 重新加载 YAML 配置。
//
// database 段永不重载 —— 连接池与 DSN 不能热切,那会让正在进行的事务落到旧连接上。
func AdminReloadConfig(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	before := config.Path()
	if err := config.Reload(); err != nil {
		badRequest(c, "qy_config_invalid", "配置重载失败: "+err.Error())
		return
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      "config.reload",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      "从 " + before + " 重新加载配置",
	})
	ok(c, gin.H{"reloaded": true, "path": config.Path(), "loaded_at": config.LoadedAt()})
}
