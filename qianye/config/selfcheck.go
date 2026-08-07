package config

// selfcheck.go —— "配置项必须有消费方"的启动期防线。
//
// # 为什么需要这个文件
//
// 本扩展已经四次出现同一个失败模式:配置项定义了、applyDefaults 给了默认值、
// validate 校验了、示例 YAML 里写得明明白白 —— 但没有任何代码读它。
//
//	C1  withdraw 的四项风控限额     定义齐全,create 流程一个都没查
//	C2  violation 的两个熔断阈值     定义齐全,breaker 用的是硬编码
//	OLD-1 transfer.lookup_log_retain_days  清理任务用包内常量 30
//	OLD-2 group_visibility.filter_group_api 访问器零调用点
//
// 这类缺陷对运维完全不可见:改完 YAML、重启、日志一切正常,闸门却是空的。
// 靠人工 review 抓四次都没抓住,所以必须让它在启动日志里自己喊出来。
//
// # 怎么做到的
//
// fieldConsumers 是一张"每个配置项由谁消费"的登记表。启动时反射展开 Config,
// 与登记表对账,三类问题各打一条告警:
//
//	Unregistered  结构体里有、登记表里没有 —— 新增配置项时忘了登记
//	Stale         登记表里有、结构体里没有 —— 字段被删/改名,登记表没跟上
//	Unconsumed    登记为"经核查确无消费方" —— 闸门是空的,改它不会有任何效果
//
// 登记表不是靠自觉维护的注释:selfcheck_test.go 会真的去解析 file 指向的源文件,
// 确认里面确实引用了该字段(或它的访问器)。因此"登记表说 reconcile.go 消费了
// lookup_log_retain_days,实际那里用的是常量"这种谎话会让测试直接失败 ——
// 这正是 OLD-1 的形状。
//
// # 新增配置项时要做什么
//
// 在下面加一行,file 填真正读它的那个源文件。如果填不出来,说明这个开关
// 还没有消费方,那就先把消费方接上,而不是先把配置项合进来。

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// consumer 记录一个配置项"最终改变了什么行为"。
type consumer struct {
	// file 是消费点所在的源文件,仓库相对路径。
	// 空串表示"经核查确无消费方",启动期会对它打显式告警。
	file string
	// note 一句话说明这个值改变了什么。它是给运维看的,不是给编译器看的。
	note string
}

// fieldConsumers 登记每个配置项的消费点,key 是 yaml 路径。
//
// 一个配置项常常在多处被读到(限额既在受理时判定、又在 /limits 接口回显),
// 这里只登记"真正据此改变行为"的那一处 —— 回显接口把值原样吐给前端,
// 不足以证明闸门接上了。
var fieldConsumers = map[string]consumer{
	"enabled": {"qianye/bootstrap.go", "扩展总开关:false 时不建连、不迁移、不注册路由与后台任务"},

	// ─────────────────────────── database ───────────────────────────
	"database.dsn":                        {"qianye/db/db.go", "扩展库连接串,gorm.Open 的输入"},
	"database.max_idle_conns":             {"qianye/db/db.go", "sql.DB.SetMaxIdleConns"},
	"database.max_open_conns":             {"qianye/db/db.go", "sql.DB.SetMaxOpenConns"},
	"database.conn_max_lifetime_seconds":  {"qianye/db/db.go", "sql.DB.SetConnMaxLifetime"},
	"database.conn_max_idle_time_seconds": {"qianye/db/db.go", "sql.DB.SetConnMaxIdleTime"},
	"database.connect_timeout_seconds":    {"qianye/db/db.go", "写进 DSN 的 timeout=,建连硬上界"},
	"database.read_timeout_seconds":       {"qianye/db/db.go", "写进 DSN 的 readTimeout=,每次结果包读取的驱动层硬上界"},
	"database.write_timeout_seconds":      {"qianye/db/db.go", "写进 DSN 的 writeTimeout="},
	"database.slow_threshold_ms":          {"qianye/db/db.go", "GORM 慢查询日志阈值"},
	"database.log_level":                  {"qianye/db/db.go", "GORM 日志级别"},
	"database.auto_migrate":               {"qianye/db/migrate.go", "false 时跳过 AutoMigrate,由 DBA 手工建表"},

	// ─────────────────────────── runtime ───────────────────────────
	"runtime.hot_path_fail_open": {"qianye/config/validate.go",
		"⚠ 只被校验器消费:置 false 只打一条告警,运行时恒为 fail-open —— " +
			"guard.Hot 从不把扩展的错误返回给 relay。这是刻意的,扩展不允许成为主业务的单点故障"},
	"runtime.hot_path_timeout_ms":       {"qianye/guard/guard.go", "跑在 relay 线程上的同步 hook 的 ctx 预算"},
	"runtime.hot_async_timeout_ms":      {"qianye/guard/guard.go", "队列 worker 的 ctx 预算(与同步预算分开)"},
	"runtime.cold_path_timeout_ms":      {"qianye/guard/guard.go", "管理端/后台冷路径的 ctx 预算"},
	"runtime.health_interval_seconds":   {"qianye/db/health.go", "扩展库健康探测周期"},
	"runtime.breaker_failure_threshold": {"qianye/db/db.go", "连续多少次连接级错误打开熔断"},
	"runtime.breaker_open_seconds":      {"qianye/db/db.go", "熔断打开后的静默时长"},
	"runtime.background_enabled":        {"qianye/bootstrap.go", "false 时一个后台任务都不启动"},
	"runtime.lease_ttl_seconds":         {"qianye/service/lease/lease.go", "租约过期时间,决定接管速度"},
	"runtime.lease_renew_seconds":       {"qianye/service/lease/lease.go", "续租间隔"},
	"runtime.config_reload_seconds":     {"qianye/bootstrap.go", "配置热更新轮询周期,0 表示不热更新"},
	"runtime.hot_hook_queue_size":       {"qianye/guard/guard.go", "HotAsync 有界队列容量,满了即丢弃并告警"},
	"runtime.hot_hook_workers":          {"qianye/guard/guard.go", "队列 worker 数量"},

	// ─────────────────────────── two_phase ───────────────────────────
	"two_phase.main_outbox_enabled":         {"qianye/service/twophase/execute.go", "是否在主库资金事务内写 qy_fund_outbox 探针"},
	"two_phase.compensate_interval_seconds": {"qianye/service/twophase/compensate.go", "补偿任务周期"},
	"two_phase.pending_grace_seconds":       {"qianye/service/twophase/compensate.go", "多久之后才认为一笔跨库操作卡住了"},
	"two_phase.max_probe_attempts":          {"qianye/service/twophase/compensate.go", "探测多少轮仍不可判定即转人工"},
	"two_phase.batch_size":                  {"qianye/service/twophase/compensate.go", "每轮扫描条数"},
	"two_phase.manual_review_after_seconds": {"qianye/service/twophase/compensate.go", "多久之后标记为需人工裁决"},
	"two_phase.outbox_retention_days":       {"qianye/service/twophase/compensate.go", "PruneOutbox 清理主库探针行的保留期"},

	// ─────────────────────────── audit ───────────────────────────
	"audit.enabled":            {"qianye/service/audit/audit.go", "false 时不写审计记录"},
	"audit.record_ip":          {"qianye/service/audit/audit.go", "是否记录操作者 IP"},
	"audit.snapshot_max_bytes": {"qianye/service/audit/audit.go", "审计快照的截断上限"},
	"audit.request_enabled": {"qianye/service/audit/middleware.go",
		"HTTP 请求台账(qy_request_audits)的开关。false 时中间件直接放行、异步写入器一条都不入队;" +
			"与 audit.enabled 是与关系,后者关掉时它也不生效"},
	"audit.retention_days": {"qianye/service/audit/retention.go",
		"Prune 的保留期。0(默认)= 永久保留;大于 0 时按主键开窗分批删除超期行," +
			"取值不得低于 config.MinAuditRetentionDays(365 天),低于下限直接拒绝启动而非静默夹取"},

	// ─────────────────────────── transfer ───────────────────────────
	"transfer.enabled":                     {"qianye/guard/guard.go", "featureOn(FlagTransfer):关掉后划转接口一律返回 qy_feature_off"},
	"transfer.min_quota":                   {"qianye/modules/transfer/validate.go", "单笔下限,受理时拒绝;YAML 是默认值,可被管理端在 qy_settings(scope=transfer)在线覆盖"},
	"transfer.max_per_tx_quota":            {"qianye/modules/transfer/validate.go", "单笔上限,受理时拒绝;同上,可被 qy_settings 在线覆盖"},
	"transfer.daily_max_quota":             {"qianye/modules/transfer/risk.go", "日累计额度,在加锁事务内判定;可被 qy_settings 在线覆盖"},
	"transfer.daily_max_count":             {"qianye/modules/transfer/risk.go", "日累计笔数,在加锁事务内判定;可被 qy_settings 在线覆盖"},
	"transfer.fee_bps":                     {"qianye/modules/transfer/validate.go", "手续费率,computeFee 的输入;可被 qy_settings 在线覆盖"},
	"transfer.fee_min_quota":               {"qianye/modules/transfer/validate.go", "手续费下限;可被 qy_settings 在线覆盖"},
	"transfer.cooldown_seconds":            {"qianye/modules/transfer/risk.go", "两笔划转之间的最小间隔;可被 qy_settings 在线覆盖"},
	"transfer.recipient_lookup":            {"qianye/modules/transfer/lookup.go", "收款人可按什么解析(id / id_or_email)"},
	"transfer.new_account_freeze_hours":    {"qianye/modules/transfer/service.go", "新注册账号多久内不可转出;可被 qy_settings 在线覆盖"},
	"transfer.require_receiver_enabled":    {"qianye/modules/transfer/lookup.go", "收款方被封禁时是否拒绝"},
	"transfer.receiver_daily_max_in_count": {"qianye/modules/transfer/risk.go", "单账号每日可接收的笔数上限;可被 qy_settings 在线覆盖"},
	"transfer.lookup_log_retain_days":      {"qianye/modules/transfer/reconcile.go", "收款人解析日志的保留天数,pruneLookupLogs 的扫描下界"},

	// ─────────────────────────── commission ───────────────────────────
	"commission.enabled":              {"qianye/guard/guard.go", "featureOn(FlagCommission)"},
	"commission.topup_rate_percent":   {"qianye/modules/commission/settings.go", "充值返佣百分比(全局默认),在此换算成内部整数费率并可被运营覆盖"},
	"commission.consume_rate_percent": {"qianye/modules/commission/settings.go", "消费返佣百分比(全局默认),同上"},
	"commission.topup_rate_bps": {"qianye/config/defaults.go",
		"⚠ 已废弃:仅作兼容,加载时由 adoptDeprecatedRates 换算进 topup_rate_percent 并告警;与新字段矛盾时启动失败"},
	"commission.consume_rate_bps": {"qianye/config/defaults.go",
		"⚠ 已废弃:同 topup_rate_bps,请改用 consume_rate_percent"},
	"commission.levels": {"qianye/config/validate.go",
		"只被校验器消费:当前仅支持 1 级,填 2 会直接启动失败 —— 静默降级成一级会让运营以为二级佣金在发"},
	"commission.min_settle_quota":              {"qianye/modules/commission/settle.go", "低于此额度不结算"},
	"commission.max_per_order_quota":           {"qianye/modules/commission/hook.go", "单笔返佣上限"},
	"commission.holding_days":                  {"qianye/modules/commission/hook.go", "佣金冻结期"},
	"commission.settle_interval_seconds":       {"qianye/modules/commission/module.go", "结算任务周期"},
	"commission.inviter_cache_seconds":         {"qianye/modules/commission/inviter.go", "users.inviter_id 的缓存时长"},
	"commission.topup_scan_interval_seconds":   {"qianye/modules/commission/module.go", "充值扫描任务周期"},
	"commission.topup_scan_lookback_hours":     {"qianye/modules/commission/topup_scan.go", "充值扫描的回扫窗口"},
	"commission.exclude_redemption_and_manual": {"qianye/modules/commission/topup_scan.go", "兑换码与管理员补单是否不返佣"},
	"commission.exclude_subscription_consume":  {"qianye/modules/commission/hook.go", "订阅消费是否不返佣"},
	"commission.refund_clawback":               {"qianye/modules/commission/hook.go", "退款时是否追回已发佣金"},

	// ─────────────────────────── withdraw ───────────────────────────
	"withdraw.enabled":                {"qianye/guard/guard.go", "featureOn(FlagWithdraw)"},
	"withdraw.methods":                {"qianye/modules/withdraw/validate.go", "允许的提现方式,不在表内即拒绝"},
	"withdraw.min_quota":              {"qianye/modules/withdraw/validate.go", "最低提现额度"},
	"withdraw.min_fiat_amount":        {"qianye/modules/withdraw/pricing.go", "法币方式的最低金额"},
	"withdraw.fiat_currency":          {"qianye/modules/withdraw/create.go", "法币币种,落单时冻结"},
	"withdraw.fiat_fee_bps":           {"qianye/modules/withdraw/create.go", "法币打款手续费率"},
	"withdraw.rate_freeze_mode":       {"qianye/modules/withdraw/pricing.go", "汇率取运营配置还是固定值"},
	"withdraw.rate_freeze_fixed":      {"qianye/modules/withdraw/pricing.go", "固定汇率取值"},
	"withdraw.auto_credit_on_approve": {"qianye/modules/withdraw/credit.go", "审核通过是否自动到账"},
	"withdraw.daily_max_count":        {"qianye/modules/withdraw/create.go", "每日提现笔数上限"},
	"withdraw.payee_account_max":      {"qianye/modules/withdraw/payee.go", "每人可保存的收款方式数量"},
	"withdraw.review_sla_hours":       {"qianye/modules/withdraw/view.go", "审核时限,用于展示与超时标记"},
	"withdraw.remark_max_runes":       {"qianye/modules/withdraw/validate.go", "用户备注字数上限"},
	"withdraw.pii_key":                {"qianye/modules/withdraw/crypto.go", "收款信息 AES-GCM 主密钥"},
	"withdraw.pii_key_version":        {"qianye/modules/withdraw/crypto.go", "新密文写入时记录的密钥版本"},
	"withdraw.pii_keys_retired":       {"qianye/modules/withdraw/crypto.go", "历史密钥,解密轮换前写入的密文"},
	"withdraw.digest_key":             {"qianye/modules/withdraw/crypto.go", "跨账户风控指纹的独立密钥"},
	"withdraw.cooldown_seconds":       {"qianye/modules/withdraw/create.go", "两次申请之间的最小间隔"},
	"withdraw.max_pending_orders":     {"qianye/modules/withdraw/create.go", "同时存在的未终态单数量上限"},
	"withdraw.max_quota_per_order":    {"qianye/modules/withdraw/validate.go", "单笔提现上限"},
	"withdraw.daily_max_quota":        {"qianye/modules/withdraw/create.go", "单日提现总额上限"},
	"withdraw.pii_retention_days":     {"qianye/modules/withdraw/payee.go", "收款信息密文的保留天数"},
	"withdraw.proof_enabled": {"qianye/modules/withdraw/proof.go",
		"是否允许给法币提现附一张凭证图片(经 ProofOn(),已并入「法币方式已开放」这一前提);" +
			"关掉后上传接口直接拒绝,已存在的图片仍可下载直到被清理"},
	"withdraw.proof_max_bytes": {"qianye/modules/withdraw/proof.go",
		"单张凭证的字节上限,在读第一个字节之前就作为 http.MaxBytesReader 的参数生效"},

	// ─────────────────────────── wallet ───────────────────────────
	"wallet.show_transfer_entry": {"qianye/controller/config.go", "下发给前端,决定钱包页是否渲染划转入口"},
	// ─────────────────────────── ticket ───────────────────────────
	"ticket.enabled":                 {"qianye/guard/guard.go", "featureOn(FlagTicket)"},
	"ticket.title_max_runes":         {"qianye/modules/ticket/validate.go", "工单标题字数上限"},
	"ticket.body_max_runes":          {"qianye/modules/ticket/validate.go", "单条消息正文字数上限(Markdown 源码)"},
	"ticket.max_open_per_user":       {"qianye/modules/ticket/create.go", "单人未关闭工单数上限"},
	"ticket.daily_max_count":         {"qianye/modules/ticket/create.go", "单人每日新建工单数上限"},
	"ticket.cooldown_seconds":        {"qianye/modules/ticket/create.go", "两次新建工单之间的最小间隔"},
	"ticket.reply_cooldown_seconds":  {"qianye/modules/ticket/reply.go", "两次追加回复之间的最小间隔"},
	"ticket.max_messages_per_ticket": {"qianye/modules/ticket/reply.go", "单张工单的消息条数上限"},
	"ticket.auto_close_days": {"qianye/modules/ticket/tasks.go",
		"管理员已回复后用户多久没回应即自动关闭(只作用于 replied,绝不碰在等客服的状态)"},
	"ticket.image_enabled": {"qianye/modules/ticket/attachment.go",
		"是否接受图片上传。落本地磁盘,多节点部署时各存各的"},
	"ticket.image_max_bytes":       {"qianye/modules/ticket/attachment.go", "单张图片的字节上限,请求体在读第一个字节前即按它截断"},
	"ticket.image_max_per_message": {"qianye/modules/ticket/validate.go", "单条消息可附的图片数上限"},
	"ticket.image_retention_days":  {"qianye/modules/ticket/tasks.go", "工单关闭后图片的保留天数,从关闭时刻起算"},
	"ticket.image_user_quota_bytes": {"qianye/modules/ticket/attachment.go",
		"单人磁盘总量闸:未绑定上传数在图片提交后归零,只有这一条约束已经落进工单里的字节"},
	"wallet.show_commission_entry": {"qianye/controller/config.go", "下发给前端,决定钱包页是否渲染佣金入口"},
	"wallet.show_withdraw_entry":   {"qianye/controller/config.go", "下发给前端,决定钱包页是否渲染提现入口"},

	// ─────────────────────────── log_metrics ───────────────────────────
	"log_metrics.show_reasoning_effort": {"qianye/modules/logmetrics/logmetrics.go", "是否采集并下发推理强度列"},
	"log_metrics.show_cache_ratio":      {"qianye/modules/logmetrics/logmetrics.go", "是否采集并下发缓存百分比列"},
	"log_metrics.enable_filter": {"qianye/controller/config.go",
		"下发给前端,决定日志页是否展示按新列筛选的入口;后端没有 SQL 层实现,打开只是让前端知道可以筛"},

	// ─────────────────────────── group_visibility ───────────────────────────
	"group_visibility.enabled":             {"qianye/modules/groupvis/groupvis.go", "三个裁剪 hook 的总开关"},
	"group_visibility.filter_pricing":      {"qianye/modules/groupvis/groupvis.go", "模型广场是否裁剪每个模型的 enable_groups"},
	"group_visibility.filter_perf_metrics": {"qianye/modules/groupvis/groupvis.go", "运营数据接口是否收窄分组白名单"},
	"group_visibility.include_auto_group":  {"qianye/modules/groupvis/groupvis.go", "裁剪时是否保留 auto 伪分组"},

	// ─────────────────────────── availability ───────────────────────────
	"availability.enabled":                {"qianye/guard/guard.go", "featureOn(FlagAvailability)"},
	"availability.sample_attempt_level":   {"qianye/modules/availability/sample.go", "按单次尝试还是按请求采样"},
	"availability.bucket_seconds":         {"qianye/modules/availability/aggregate.go", "时间桶粒度"},
	"availability.flush_interval_seconds": {"qianye/modules/availability/flush.go", "内存聚合落库周期"},
	"availability.retention_days":         {"qianye/modules/availability/flush.go", "采样数据保留天数"},
	"availability.max_series_per_query":   {"qianye/modules/availability/query.go", "单次查询的曲线条数上限"},
	"availability.count_client_errors":    {"qianye/modules/availability/outcome.go", "4xx 是否计入不可用"},
	"availability.count_rate_limited":     {"qianye/modules/availability/outcome.go", "限流是否计入不可用"},

	// ─────────────────────────── violation ───────────────────────────
	// violation.shadow_mode 已删除:影子/真实绑定在规则行上(qy_violation_rule.mode),
	// 不再有全局开关,也就没有对应的配置项。
	"violation.enabled":                        {"qianye/guard/guard.go", "featureOn(FlagViolation)"},
	"violation.precheck_enabled":               {"qianye/modules/violation/guard.go", "relay 前置扫描挂载点是否生效"},
	"violation.post_charge_enabled":            {"qianye/modules/violation/guard.go", "计费后扫描挂载点是否生效"},
	"violation.fee_multiplier":                 {"qianye/modules/violation/fee.go", "违规扣费倍数"},
	"violation.fixed_fee_amount":               {"qianye/modules/violation/fee.go", "违规固定扣费"},
	"violation.max_fee_quota":                  {"qianye/modules/violation/fee.go", "单次扣费上限"},
	"violation.insufficient_balance_policy":    {"qianye/modules/violation/fee.go", "余额不足时 clamp / negative / ban"},
	"violation.auto_ban_threshold":             {"qianye/modules/violation/counter.go", "窗口内命中多少次自动封号,0 表示不自动封"},
	"violation.auto_ban_window_hours":          {"qianye/modules/violation/counter.go", "自动封号的统计窗口"},
	"violation.global_block_rate_limit_bps":    {"qianye/modules/violation/breaker.go", "全站拦截率熔断阈值,超了自动回落影子模式"},
	"violation.global_ban_rate_limit_per_hour": {"qianye/modules/violation/breaker.go", "全站封号速率熔断阈值"},
	"violation.evidence_max_bytes":             {"qianye/modules/violation/evidence.go", "证据快照截断上限"},
	"violation.evidence_retention_days":        {"qianye/modules/violation/tasks.go", "证据保留天数"},
	"violation.rule_cache_seconds":             {"qianye/modules/violation/rules.go", "规则缓存时长"},
	"violation.scan_timeout_ms":                {"qianye/modules/violation/rules.go", "单次正则扫描的预算"},

	// ─────────────────────── group_pricing(已下线)───────────────────────
	//
	// 整段只剩一个 map 占位。登记它不是为了描述一个功能,而是为了让这条对账
	// 说得出真话:这个键**仍然会被解析**(否则严格解析会让存量部署起不来),
	// 但它的唯一消费方是那句告警。
	"group_pricing": {"qianye/config/defaults.go",
		"⚠ 已下线:「模型按分组单独定价」整个模块已删除。本段被 adoptRetiredGroupPricing " +
			"整段忽略并告警,里面写什么都不生效。分组级价格改由「用户分组 × 模型分组」倍率矩阵表达"},

	// ─────────────────────────── group_matrix ───────────────────────────
	"group_matrix.enabled": {"qianye/modules/groupmatrix/snapshot.go",
		"L1 kill switch:关掉后 QyResolveUsableGroups 恒等返回上游那张 map," +
			"写入侧校验一并失效,管理端接口 404。判定不依赖扩展库可达性"},
	"group_matrix.cache_seconds": {"qianye/modules/groupmatrix/snapshot.go",
		"清单内存快照的刷新周期(清单读取在 relay 热路径上,每次请求查库不可接受)"},
	"group_matrix.max_stale_seconds": {"qianye/modules/groupmatrix/snapshot.go",
		"陈旧上限,超过即限频告警。**不丢弃快照** —— 丢弃只能回落到上游宽松白名单," +
			"那意味着被收紧的用户重新可以把令牌指向 ratio=0 的免费分组"},
	"group_namespace.enabled": {"qianye/modules/groupns/snapshot.go",
		"分组登记与默认模型分组解析的 L1 kill switch;关掉后三个 hook 全部恒等返回上游语义"},
	"group_namespace.cache_seconds": {"qianye/modules/groupns/snapshot.go",
		"登记快照的刷新周期(默认模型分组的解析在 relay 热路径上)"},
	"group_namespace.max_stale_seconds": {"qianye/modules/groupns/snapshot.go",
		"快照陈旧上限,超过只告警不丢弃(丢弃会让已配 pin 的空分组令牌当场回到 503)"},
	"group_namespace.default_model_group_enabled": {"qianye/modules/groupns/snapshot.go",
		"「用户分组的默认模型分组」的独立子开关(经 DefaultModelGroupOn 读取)"},
	"group_namespace.missing_ratio_policy": {"qianye/modules/groupns/hook.go",
		"分组倍率缺失时是否在鉴权处 403;legacy_one=上游 fail-open,deny=拒绝"},
	"group_namespace.funding_gate_mode": {"qianye/modules/groupns/hook.go",
		"套餐耗尽后钱包出资闸门的档位:off/shadow/enforce"},
	"group_namespace.auto_backfill": {"qianye/modules/groupns/groupns.go",
		"是否自动把观测到的分组名回填进两张登记表(经 AutoBackfillOn 读取)"},

	"group_matrix.preview_log_days":     {"qianye/modules/groupmatrix/preview.go", "影响面预览回看日志库的天数"},
	"group_matrix.max_preview_pairs":    {"qianye/modules/groupmatrix/preview.go", "一次预览最多展开的分组对数,超出即 preview_incomplete"},
	"group_matrix.preview_sample_limit": {"qianye/modules/groupmatrix/preview.go", "每一对最多返回的令牌样本条数"},
	"group_matrix.max_grants":           {"qianye/modules/groupmatrix/api_admin.go", "清单总行数上限,写入时判定"},
	"group_matrix.write_guard_enabled":  {"qianye/modules/groupmatrix/hook.go", "令牌写入侧校验的独立开关(经 WriteGuardOn 读取)"},
	// 这两个键随「新分组默认全遮断」一并下线。消费点指向 defaults.go 而不是删掉登记:
	// 自检面板必须能回答"我 YAML 里还写着这一行,它现在起什么作用",
	// 而答案恰恰是"什么作用都没有,只会在启动时喊一声"。(同 commission.*_rate_bps)
	"group_matrix.new_group_default_deny": {"qianye/config/defaults.go",
		"⚠ 已下线:「新分组默认全遮断」与新口径(未设定范围 = 全部模型分组可用)完全相反,已整体撤销。" +
			"加载时由 adoptRetiredNewGroupDeny 告警并忽略,填 true 也不会让任何东西收紧"},
	"group_matrix.new_group_scan_interval_seconds": {"qianye/config/defaults.go",
		"⚠ 已下线:新分组对账任务已随上一项一并移除,该键不再有任何效果"},

	// ─────────────────────────── plan_entitlement ───────────────────────────
	"plan_entitlement.enabled": {"qianye/modules/planentitlement/snapshot.go",
		"kill switch:关掉后 QyPlanUnlockGroups / QyPlanUnlockedGroup 恒等返回," +
			"已购套餐解锁的模型分组当场失效,余额的「仅限」范围也不再生效。判定不依赖扩展库可达性"},
	"plan_entitlement.cache_seconds": {"qianye/modules/planentitlement/snapshot.go",
		"第一层(套餐 → 解锁分组 + 余额范围)内存快照的刷新周期。这一层纯内存零 I/O," +
			"热路径与订阅扣费事务内都要读它"},
	"plan_entitlement.user_cache_seconds": {"qianye/modules/planentitlement/entitlement.go",
		"第二层(userId → 活跃套餐)的新鲜期,本模块唯一的主库 I/O 面。" +
			"「没有任何活跃套餐」那一档只缓存它的 1/4 —— 刚买完套餐的人几乎必然在那一档"},
	"plan_entitlement.user_max_stale_seconds": {"qianye/modules/planentitlement/snapshot.go",
		"serve-stale 上限:刷新失败时继续沿用上一次成功结果多久,超过才降级为「无解锁」。" +
			"同时是第一层快照的陈旧告警线"},

	// ─────────────────────────── lottery ───────────────────────────
	"lottery.enabled": {"qianye/guard/guard.go",
		"featureOn(FlagLottery):关掉后用户端与创建接口 404,但管理端只读与证据链仍可用"},
	"lottery.show_entry": {"qianye/controller/config.go",
		"前端是否渲染娱乐入口。与 enabled 分开:关掉入口后已参与的用户仍要能查记录"},
	"lottery.proof_public": {"qianye/modules/lottery/api_proof.go",
		"证据链端点是否匿名可访问。默认开 —— 需要账号才能取证的公正性不叫公正性"},
	"lottery.max_active_activities": {"qianye/modules/lottery/settings.go",
		"同时处于 published/locked/settling 的活动数上限"},
	"lottery.max_stake_quota": {"qianye/modules/lottery/api_admin.go",
		"单次参与费/单注上限,创建时判定"},
	"lottery.max_total_prize_quota": {"qianye/modules/lottery/settings.go",
		"单场奖品总额度硬上限。抽奖派奖是净增发,这是唯一能拦住「多写一个零」的闸门"},
	"lottery.large_prize_alert_quota": {"qianye/modules/lottery/settings.go",
		"大额活动告警阈值,只告警不阻断"},
	"lottery.pay_password_threshold_quota": {"qianye/modules/lottery/entry.go",
		"参与费超过它就要验支付密码 —— 参与是不可逆消费,盗号者能用它把余额烧光"},
	"lottery.entry_close_grace_seconds": {"qianye/modules/lottery/api_admin.go",
		"封盘前停止受理的提前量,给两阶段的未决单留收敛窗口"},
	"lottery.reveal_delay_seconds": {"qianye/modules/lottery/api_admin.go",
		"close→draw 的强制最小间隔:名单哈希必须先于种子公开"},
	"lottery.lock_scan_interval_seconds": {"qianye/modules/lottery/module.go",
		"自动封盘扫描周期(管理端没有提前截止按钮)"},
	"lottery.reveal_scan_interval_seconds": {"qianye/modules/lottery/module.go",
		"自动开奖与逾期流局的扫描周期(管理端没有立即开奖按钮)"},
	"lottery.payout_interval_seconds": {"qianye/modules/lottery/module.go", "派奖/赔付/退款 worker 周期"},
	"lottery.payout_max_attempts": {"qianye/modules/lottery/payout.go",
		"单笔出款重试上限,耗尽转人工(绝不自动放弃)"},
	"lottery.excluded_manual_after_seconds": {"qianye/modules/lottery/lifecycle.go",
		"参与单卡在不可判定态多久之后转人工"},
	"lottery.max_total_entries_hard": {"qianye/modules/lottery/api_admin.go",
		"单场名单规模上界(名单冻结要在单事务里流式算完)"},
	"lottery.max_prize_tiers":       {"qianye/modules/lottery/api_admin.go", "奖档数量上限"},
	"lottery.max_options":           {"qianye/modules/lottery/api_admin.go", "竞猜选项数量上限"},
	"lottery.default_guess_fee_bps": {"qianye/modules/lottery/settings.go", "竞猜手续费默认值"},
	"lottery.max_guess_fee_bps": {"qianye/modules/lottery/settings.go",
		"手续费硬上界,防把 5% 手滑打成 50%"},
	"lottery.spend_scan_interval_seconds": {"qianye/modules/lottery/module.go", "消费增量扫描周期"},
	"lottery.spend_scan_batch":            {"qianye/modules/lottery/spend.go", "消费增量扫描单批行数"},
	"lottery.spend_gap_guard_seconds": {"qianye/modules/lottery/spend.go",
		"自增 id 提交乱序的时间护栏:裸 id > watermark 会跳过后提交的小 id 行"},
	"lottery.spend_max_lookback_days": {"qianye/modules/lottery/spend.go",
		"「近 N 日消费」允许的最大回看天数"},
	"lottery.spend_retention_days": {"qianye/modules/lottery/spend.go",
		"消费日桶保留期。它是本模块唯一允许被清理的表(可从 logs 重建的派生数据)"},
}

// leafField 是展开后的一个配置项。
type leafField struct {
	// path 是 yaml 路径,如 transfer.min_quota。
	path string
	// name 是 Go 字段名。测试据此去消费点文件里找引用,所以必须一并带出来。
	name string
}

// leafFields 递归展开结构体,返回全部叶子配置项。
//
// 只有匿名段(Transfer / Withdraw 这类)会被递归,其余一律视为叶子 ——
// 包括 *bool、[]string、map[int]string:它们都是运维直接填的一个值。
func leafFields(t reflect.Type, prefix string) []leafField {
	var out []leafField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if f.Type.Kind() == reflect.Struct {
			out = append(out, leafFields(f.Type, path)...)
			continue
		}
		out = append(out, leafField{path: path, name: f.Name})
	}
	return out
}

// ConsumerCheck 是一次自检的结果。三类问题分开返回,因为处理方式不同:
// 前两类是登记表与代码脱节(改登记表或改代码),第三类是闸门真的空着(接消费方)。
type ConsumerCheck struct {
	Unregistered []string
	Stale        []string
	Unconsumed   []string
}

func (c ConsumerCheck) clean() bool {
	return len(c.Unregistered) == 0 && len(c.Stale) == 0 && len(c.Unconsumed) == 0
}

// CheckFieldConsumers 比对 Config 结构体与消费方登记表。
func CheckFieldConsumers() ConsumerCheck {
	return checkConsumers(leafFields(reflect.TypeOf(Config{}), ""), fieldConsumers)
}

// checkConsumers 是对账逻辑本体。
// 把两个输入都做成参数,是为了让单元测试能用合成数据直接验证这段判定 ——
// 用真实 Config 验证的话,任何人加一个配置项都会牵动它,而它要守的是判定本身。
func checkConsumers(fields []leafField, registry map[string]consumer) ConsumerCheck {
	var res ConsumerCheck
	known := make(map[string]bool, len(fields))
	for _, f := range fields {
		known[f.path] = true
		c, ok := registry[f.path]
		switch {
		case !ok:
			res.Unregistered = append(res.Unregistered, f.path)
		case c.file == "":
			res.Unconsumed = append(res.Unconsumed, f.path)
		}
	}
	for path := range registry {
		if !known[path] {
			res.Stale = append(res.Stale, path)
		}
	}
	sort.Strings(res.Unregistered)
	sort.Strings(res.Stale)
	sort.Strings(res.Unconsumed)
	return res
}

// LogFieldConsumerCheck 把自检结果打进启动日志。
//
// 一律用 SysError 而不是 SysLog:这三类问题都意味着"运维以为生效的东西没生效",
// 淹没在 info 里就等于没有。但都不阻断启动 —— 空闸门本身不会造成资损,
// 让主程序起不来才会。
func LogFieldConsumerCheck() {
	res := CheckFieldConsumers()
	if res.clean() {
		return
	}
	for _, path := range res.Unconsumed {
		common.SysError(fmt.Sprintf(
			"qianye: 配置项 %s 没有任何消费方 —— 改它不会产生任何效果。%s",
			path, fieldConsumers[path].note))
	}
	for _, path := range res.Unregistered {
		common.SysError(fmt.Sprintf(
			"qianye: 配置项 %s 未登记消费方 —— 请在 qianye/config/selfcheck.go 的 fieldConsumers 中"+
				"补上真正读它的源文件;如果填不出来,说明这个开关还没接上代码", path))
	}
	for _, path := range res.Stale {
		common.SysError(fmt.Sprintf(
			"qianye: 消费方登记表里的 %s 在配置结构体中已不存在 —— 字段被删或改名后登记表没跟上", path))
	}
}
