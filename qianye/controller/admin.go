package controller

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupratio"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/groupmatrix"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"
	"github.com/QuantumNous/new-api/qianye/modules/planentitlement"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// retiredTables 是「已经没有任何代码引用、但还留在扩展库里」的表清单。
//
// 登记在这里而不是各模块自己的 Health():退役表的定义就是**模块已经不在了**,
// 让一个已删模块去汇报自己的遗留表是做不到的。qy_group_seen 是例外(它的模块还在,
// 只是那张表退役了),由 groupmatrix.Health() 自报,两处不重复。
//
// 加一张退役表时**必须同时**改 qianye/docs/retired_tables.md,由
// TestRetiredTablesMatchTheDoc 对账 —— 两边一旦分叉,这一段就变成一份不可信的清单,
// 而它存在的全部理由就是可信。
var retiredTables = []gin.H{
	{
		"table":      "qy_group_price_rule",
		"retired_by": "分组定价(grouppricing)整模块下线,改由用户分组 × 模型分组矩阵表达",
		"note":       "未 DROP:观察期内保留以便回读。数据未迁移。",
	},
	{
		"table":      "qy_group_price_shadow",
		"retired_by": "同上(影子模式的比对记录)",
		"note":       "未 DROP:观察期内保留以便回读。数据未迁移。",
	},
	{
		"table":      "qy_group_price_rule_version",
		"retired_by": "同上(规则版本快照)",
		"note":       "未 DROP:观察期内保留以便回读。数据未迁移。",
	},
}

// AdminHealth 返回扩展的运行状态,是排障的第一入口。
func AdminHealth(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	// 健康页刻意吞掉租约读取错误(扩展库不可用时其余各段仍然有诊断价值),
	// 但吞错误的代价是 leases 会是 nil —— 而这是排障页最需要能打开的时刻。
	// nil 切片序列化成 null,前端对着 null 调 .map 直接白屏。
	leases, err := lease.List()
	if err != nil || leases == nil {
		leases = []qymodel.TaskLease{}
	}
	ok(c, gin.H{
		"db":        db.Stats(),
		"hot_queue": guard.QueueStats(),
		// request_audit.dropped > 0 意味着那段时间的写请求没有 HTTP 留痕。
		// 台账允许异步、允许丢,但绝不允许**静默**丢 —— 悄悄缺失的审计
		// 比没有审计更危险,因为它看起来是完整的。
		"request_audit": audit.RequestQueueStats(),
		"two_phase":     twophase.Stats(),
		"leases":        leases,
		"migrate": gin.H{
			"table_count": db.TableCount(),
		},
		// 每个模块的配置段现状。state=missing_section / missing_key 表示
		// "这个模块编译进来了,但配置文件里没人对它做过决定,它正静默关着" ——
		// 启动时同一件事会打 SysError,但启动日志会被滚走,而排障的人往往
		// 是在事后才来看这一页。判据与告警文案见 qianye/config/sections.go。
		"modules": config.ModuleSectionStatus(),
		// 分组倍率失配。上游 GetGroupRatio 查不到分组时返回 1 并只写一行 SysLog,
		// 而 SysLog 会被滚走 —— 这一段是那条 fail-open 唯一常驻的信号。
		// last_scan 缺省表示本进程还没扫过(不等于"没有问题"),
		// 完整报表在 /api/qy/admin/group-ratio/orphans。
		"group_ratio": groupratio.Health(),
		// 权威可选清单的快照状态。snapshot_loaded=false 表示清单**完全没生效**,
		// 当前按上游全局白名单放行 —— 那是比"配置写错了"更隐蔽的一种失效:
		// 矩阵页照常显示、保存照常成功,只是没有任何一条收紧在起作用。
		"group_matrix": groupmatrix.Health(),
		// 分组登记与默认模型分组解析。它与 group_matrix 分开报是刻意的:
		// 两者的 needs_attention 指向完全不同的动作 —— 那边是"可选清单陈旧了",
		// 这边是"已配的默认模型分组可能没生效",而后者的表现是整组用户 503。
		"group_namespace": groupns.Health(),
		// 套餐解锁的快照状态与 fail-closed 计数。后者非 0 意味着有付费用户
		// 因为"套餐权限暂时读不到"而被当成无解锁处理 —— 它的表现是偶发 403,
		// 与"这个分组真的没配"长得一模一样,不在这里出数就永远查不出来。
		"plan_entitlement": planentitlement.Health(),
		// 已退役但**仍留在扩展库里**的表。
		//
		// 它们已经从 AutoMigrate 里摘掉、没有任何代码再引用,但刻意不 DROP:
		// 观察期内还要能回读(下线的可逆性就押在这上面)。这一段是那些表唯一的
		// 运行时说明 —— 少了它,半年后在库里看到这些表的人只能得出两个结论:
		// 「漏了一次迁移」或者「不敢动」,而正确答案是第三个:受控退役。
		// 每一张的来历、手工 DROP 语句与可逆性见 qianye/docs/retired_tables.md。
		"retired_tables": retiredTables,
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
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&qymodel.FundOrder{})

	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", httpq.Int(c, "status", 0))
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
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ? OR peer_user_id = ?", v, v)
	}
	// 时间戳走 Int64。此前这两行用的是本包那份上界为 100 万的 intQuery,
	// 而任何真实 Unix 时间戳都远大于 100 万 —— 解析恒回落 0,`v > 0` 恒不成立,
	// WHERE 从来没有被拼上去:资金单列表的时间范围筛选一直是死的,
	// 而前端只会觉得"筛选没生效"。这是给分页加上界时波及到同一个 helper 的
	// 其他调用点造成的,也是本次收敛要根治的那种漂移。
	if v := httpq.Int64(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := httpq.Int64(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		serverError(c, err)
		return
	}
	// 下发给前端的数组一律显式初始化:nil 切片会被序列化成 null 而不是 [],
	// 前端对着 null 调 .find/.map 直接白屏。判据与机器校验见
	// qianye/json_array_guard_test.go。
	items := make([]qymodel.FundOrder, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&items).Error; err != nil {
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

// maxResolveReasonRunes 与提现审核的拒绝理由上限保持一致(withdraw 的
// maxReasonRunes),两处理由最终都落进同一张审计表的 varchar(512)。
const maxResolveReasonRunes = 200

// checkResolveReason 校验并规范化人工裁决理由。
// 第二、三个返回值分别是给前端做 i18n 的错误码与可读提示,code 为空表示通过。
//
// 上限必须在写库之前校验,而不是等数据库报错:理由要拼进 qy_fund_orders.last_error
// 与审计 reason 两个 varchar(512),超长在 MySQL 严格模式下是 1406 Data too long,
// 而 serverError 只回"处理失败,请稍后重试" —— 管理员看不出是理由太长,原样重试
// 只会再失败一次,这笔资金单就永远停在 uncertain,而 uncertain 单不会被补偿任务
// 收敛(它只扫 pending),人工裁决是它唯一的出口。
//
// 按字符而不是字节计:一个汉字 3 字节,按字节卡会让 170 字的中文理由被拒,
// 与提现审核 checkRunes 的口径也会不一致。
func checkResolveReason(raw string) (reason, code, msg string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		// 人工改动资金状态必须留下理由,否则事后无法复盘。
		return "", "qy_reason_required", "必须填写裁决理由"
	}
	// 先用字节数做廉价上界剪枝:rune 最多 4 字节,超过 4*max 必然超限,
	// 不必对一个超大请求体做完整遍历。
	if len(s) > maxResolveReasonRunes*4 || utf8.RuneCountInString(s) > maxResolveReasonRunes {
		return "", "qy_reason_too_long",
			fmt.Sprintf("裁决理由过长,请控制在 %d 字以内", maxResolveReasonRunes)
	}
	return s, "", ""
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
	reason, code, msg := checkResolveReason(req.Reason)
	if code != "" {
		badRequest(c, code, msg)
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
			// 前置校验已按字符卡过 200,这里再按 last_error 的字节宽度做一次
			// rune 安全兜底:列宽是按字符还是按字节取决于方言与字符集,
			// 兜底不依赖那个判断。
			"last_error": audit.Truncate("人工裁决: "+reason, 512),
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
		Reason:       reason,
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
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&qymodel.AuditLog{})

	if v := c.Query("category"); v != "" {
		q = q.Where("category = ?", v)
	}
	if v := c.Query("action"); v != "" {
		q = ApplyActionPrefix(q, v)
	}
	if v := c.Query("result"); v != "" {
		q = q.Where("result = ?", v)
	}
	if v := c.Query("actor_type"); v != "" {
		q = q.Where("actor_type = ?", v)
	}
	if v := c.Query("ip"); v != "" {
		q = q.Where("ip = ?", v)
	}
	if v := c.Query("trace_no"); v != "" {
		q = q.Where("trace_no = ?", v)
	}
	if v := httpq.Int(c, "actor_user_id", 0); v > 0 {
		q = q.Where("actor_user_id = ?", v)
	}
	if v := httpq.Int(c, "target_user_id", 0); v > 0 {
		q = q.Where("target_user_id = ?", v)
	}
	if v := httpq.Int64(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := httpq.Int64(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		serverError(c, err)
		return
	}
	items := make([]qymodel.AuditLog, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&items).Error; err != nil {
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
