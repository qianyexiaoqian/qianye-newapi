// Package config 加载并持有千夜扩展的 YAML 配置。
//
// 设计约束(见 qianye/docs/design-00-foundation.md §1):
//   - 绝不注册进 setting/config.GlobalConfig —— 那套体系持久化到原项目主库的
//     options 表,会违背"新功能数据必须进独立数据库"的核心约束。
//   - 绝不在 init() 里加载 —— init() 早于 main.go 的 godotenv.Load(".env"),
//     读不到 .env 提供的环境变量。加载时机固定为 qianye.Init()。
//   - 配置文件缺失即整个扩展静默禁用,主程序行为与上游保持一致。
package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"

	"gopkg.in/yaml.v3"
)

// Config 是扩展的完整配置。字段分段与 qianye.example.yaml 一一对应。
//
// 比例一律用 bps(万分比整数,5% = 500)而非浮点百分比:整数可复现、可入库比较、
// 不受浮点误差影响。倍数(如违规扣费倍数)用 decimal 字符串承载。
type Config struct {
	Enabled bool `yaml:"enabled"`

	Database Database `yaml:"database"`
	Runtime  Runtime  `yaml:"runtime"`
	TwoPhase TwoPhase `yaml:"two_phase"`
	Audit    Audit    `yaml:"audit"`

	Transfer        Transfer        `yaml:"transfer"`
	Commission      Commission      `yaml:"commission"`
	Withdraw        Withdraw        `yaml:"withdraw"`
	Ticket          Ticket          `yaml:"ticket"`
	Wallet          Wallet          `yaml:"wallet"`
	LogMetrics      LogMetrics      `yaml:"log_metrics"`
	GroupVisibility GroupVisibility `yaml:"group_visibility"`
	Availability    Availability    `yaml:"availability"`
	Violation       Violation       `yaml:"violation"`
	GroupPricing    GroupPricing    `yaml:"group_pricing"`
	GroupMatrix     GroupMatrix     `yaml:"group_matrix"`
	Lottery         Lottery         `yaml:"lottery"`

	// declared 是 YAML 文件里【实际写出来】的键路径集合,由 parseFile 填。
	//
	// 存在的理由:模块级的 Enabled 是普通 bool,零值 false —— 于是"配置里根本
	// 没有 lottery: 这一段"与"运维想清楚了、显式写了 enabled: false"在进程内
	// 是同一个字节。数值字段靠 defaults.go 的哨兵区分这两者,布尔开关没有
	// 第三个值可打哨兵,只能回头问 YAML 文本本身。判定与告警见 sections.go。
	//
	// 不导出、且 yaml:"-":它不是配置项,是关于配置文件的元信息。tag 让本包
	// 全部按 yaml tag 遍历 Config 的反射逻辑(leafFields / markNumbersUnset /
	// numericLeaves)原样跳过它。
	declared map[string]bool `yaml:"-"`
}

// Database 独立 MySQL 连接配置。仅支持 MySQL —— 扩展自建连接,
// 不复用主库的 SQL_MAX_IDLE_CONNS 等环境变量,也不触碰 common.SetMainDatabaseType。
//
// 表前缀由各 model 的 TableName() 硬编码为 qy_,不走 GORM NamingStrategy:
// 两处都设会双重加前缀,且配置项会让人误以为前缀可改。
type Database struct {
	DSN                    string `yaml:"dsn"`
	MaxIdleConns           int    `yaml:"max_idle_conns"`
	MaxOpenConns           int    `yaml:"max_open_conns"`
	ConnMaxLifetimeSeconds int    `yaml:"conn_max_lifetime_seconds"`
	ConnMaxIdleTimeSeconds int    `yaml:"conn_max_idle_time_seconds"`
	ConnectTimeoutSeconds  int    `yaml:"connect_timeout_seconds"`
	// ReadTimeoutSeconds / WriteTimeoutSeconds 是写进 DSN 的驱动层硬上界。
	// 它们是连接级的兜底闸门(防止一条撞锁的语句占着连接等满
	// innodb_lock_wait_timeout),不是热路径的 200ms 上界 —— 后者由 ctx 负责。
	// 必须大于最慢的一次合法后台操作,否则正常的结算事务会被驱动层切断。
	ReadTimeoutSeconds  int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds int    `yaml:"write_timeout_seconds"`
	SlowThresholdMs     int    `yaml:"slow_threshold_ms"`
	LogLevel            string `yaml:"log_level"`
	AutoMigrate         *bool  `yaml:"auto_migrate"`
}

// Runtime 运行期行为与降级策略。
type Runtime struct {
	// HotPathFailOpen 决定新库不可用时 relay 热路径的行为。
	// 必须保持 true:置 false 会让扩展成为主业务的单点故障。
	HotPathFailOpen  *bool `yaml:"hot_path_fail_open"`
	HotPathTimeoutMs int   `yaml:"hot_path_timeout_ms"`
	// HotAsyncTimeoutMs 是队列 worker 的预算,与 HotPathTimeoutMs 分开。
	//
	// worker 跑在自己的 goroutine 上,不占 relay 线程,因此没有理由套用为
	// "别拖住 relay"设计的几百毫秒。返佣的一次冷缓存轮需要五六次数据库往返,
	// 用同步预算去卡它,超时后既无重试也无补偿,佣金直接永久丢失。
	HotAsyncTimeoutMs       int   `yaml:"hot_async_timeout_ms"`
	ColdPathTimeoutMs       int   `yaml:"cold_path_timeout_ms"`
	HealthIntervalSeconds   int   `yaml:"health_interval_seconds"`
	BreakerFailureThreshold int   `yaml:"breaker_failure_threshold"`
	BreakerOpenSeconds      int   `yaml:"breaker_open_seconds"`
	BackgroundEnabled       *bool `yaml:"background_enabled"`
	LeaseTTLSeconds         int   `yaml:"lease_ttl_seconds"`
	LeaseRenewSeconds       int   `yaml:"lease_renew_seconds"`
	ConfigReloadSeconds     int   `yaml:"config_reload_seconds"`
	HotHookQueueSize        int   `yaml:"hot_hook_queue_size"`
	HotHookWorkers          int   `yaml:"hot_hook_workers"`
}

// TwoPhase 跨库两阶段(新库记账 → 主库动钱 → 新库回写)的参数。
type TwoPhase struct {
	// MainOutboxEnabled 决定是否在主库建 qy_fund_outbox 探针表。
	// 关闭意味着放弃精确对账:model.RecordLog 走的是 LOG_DB,无法进主库事务,
	// "主库已提交但新库回写失败"的中间态将无法判定。强烈建议保持 true。
	MainOutboxEnabled         *bool `yaml:"main_outbox_enabled"`
	CompensateIntervalSeconds int   `yaml:"compensate_interval_seconds"`
	BatchSize                 int   `yaml:"batch_size"`
	// PendingGraceSeconds / MaxProbeAttempts / ManualReviewAfterSeconds 是补偿任务
	// 判定"这笔跨库操作已经久到不像还在正常执行中"的三个刻度。
	//
	// 与本文件其余闸门相反,这三项【不接受 0】:这里的 0 不是"不设这道限制",
	// 而是"立刻下判决"。宽限期 0 会在主库事务尚未提交时就去探针;重试次数 0
	// 让第一次退避直接把单子转人工裁决;人工复核阈值 0 让任何存活超过一秒的
	// pending 单被判 failed —— 主库随后提交,两库账目就此分叉。
	// 因此由 validateTwoPhase 拒绝启动,而不是静默替运维补一个值。
	PendingGraceSeconds      int `yaml:"pending_grace_seconds"`
	MaxProbeAttempts         int `yaml:"max_probe_attempts"`
	ManualReviewAfterSeconds int `yaml:"manual_review_after_seconds"`
	// OutboxRetentionDays 主库探针行的保留天数。0 表示关掉清理(永久保留);
	// 不写这个键则取默认的 30 天。
	OutboxRetentionDays int `yaml:"outbox_retention_days"`
}

// Audit 统一审计。资金审计默认永久保留。
type Audit struct {
	Enabled          *bool `yaml:"enabled"`
	RecordIP         *bool `yaml:"record_ip"`
	SnapshotMaxBytes int   `yaml:"snapshot_max_bytes"`
	RetentionDays    int   `yaml:"retention_days"`
	// RequestEnabled 单独控制 HTTP 请求台账(qy_request_audits)。
	//
	// 与 Enabled 分开是因为两张表的量级差三个数量级:资金审计一天几十行,
	// 请求台账一天几千行。运维需要能在"扩展库写入吃紧"时先关掉后者,
	// 而不是被迫连资金仲裁凭据一起关掉。默认开 —— 默认没有留痕才是缺陷。
	RequestEnabled *bool `yaml:"request_enabled"`
}

// Transfer 用户间余额划转。
//
// 下面几道闸门一律是"0 表示不设这道限制"(业务代码里对应 `if cfg.X > 0` 的守卫)。
// 只有【不写这个键】才会拿到默认值 —— 判据是键缺失而不是零值,见 defaults.go。
type Transfer struct {
	Enabled bool `yaml:"enabled"`
	// MinQuota / MaxPerTxQuota 单笔金额的上下界,0 表示该侧不限制。
	// 两侧都大于 0 时必须 min <= max,否则任何金额都不合法(等于把划转静默关停),
	// 那个组合由 ValidateTransfer 拒绝启动。
	MinQuota      int64 `yaml:"min_quota"`
	MaxPerTxQuota int64 `yaml:"max_per_tx_quota"`
	// DailyMaxQuota / DailyMaxCount 单个发起方每日的总额与笔数上限,0 表示不限制。
	DailyMaxQuota int64 `yaml:"daily_max_quota"`
	DailyMaxCount int   `yaml:"daily_max_count"`
	FeeBps        int   `yaml:"fee_bps"`
	FeeMinQuota   int64 `yaml:"fee_min_quota"`
	// CooldownSecs 两次划转之间的最小间隔。0 表示不限制。
	CooldownSecs int `yaml:"cooldown_seconds"`
	// RecipientLookup 限定收款人查找方式。刻意不提供用户名模糊搜索:
	// 那等于开放用户枚举,且与"已邀请用户列表要脱敏"的隐私要求自相矛盾。
	RecipientLookup        string `yaml:"recipient_lookup"`
	NewAccountFreezeHours  int    `yaml:"new_account_freeze_hours"`
	RequireReceiverEnabled *bool  `yaml:"require_receiver_enabled"`
	// ReceiverDailyMaxInCount 限制单个收款人每日可接收的划转笔数。
	// 不限制的话,一个账号可以被无数小号集中打款,是典型的洗号路径。
	// 0 表示不限制。
	ReceiverDailyMaxInCount int `yaml:"receiver_daily_max_in_count"`
	// LookupLogRetainDays 收款人查找日志的保留天数。这些日志用于发现
	// 批量探测用户 ID 的行为,但含用户输入,不宜长期保留。
	// 0 表示关掉清理(永久保留);不写这个键则取默认的 30 天。
	LookupLogRetainDays int `yaml:"lookup_log_retain_days"`
}

// Commission 邀请返佣。
//
// 口径默认宽松(全返)。三个排除开关默认关闭,发现套利后可单独打开而无需改代码。
// 违规扣费产生的消费日志(other.violation_fee == true)永不返佣 —— 那属于逻辑
// 错误而非口径偏好,因此不设开关。
type Commission struct {
	Enabled bool `yaml:"enabled"`

	// TopupRatePercent / ConsumeRatePercent 是全局默认返佣比例,单位是**百分比**,
	// 最多两位小数:"10"、"10.5"、"10.25"。
	//
	// 用字符串而不是 float64:10.25 在二进制浮点里不可精确表示,而这是决定
	// 平台要付多少钱的参数。加载后由 RatePercentUnits 换算成整数
	// (百分比 × 100,10.25% → 1025),之后全程整数运算,绝不引入浮点。
	TopupRatePercent   string `yaml:"topup_rate_percent"`
	ConsumeRatePercent string `yaml:"consume_rate_percent"`

	// TopupRateBpsDeprecated / ConsumeRateBpsDeprecated 是 1.x 的万分比字段。
	//
	// Deprecated: 请改用 topup_rate_percent / consume_rate_percent。
	// 保留它们只为兼容:本包是严格解析(KnownFields(true)),直接删字段会让
	// 所有已有部署在升级二进制的那一刻启动失败。加载时换算进新字段并告警。
	//
	// 必须是指针:0 是合法费率(关掉返佣),普通 int 无法区分"写了 0"和"没写"。
	TopupRateBpsDeprecated   *int `yaml:"topup_rate_bps"`
	ConsumeRateBpsDeprecated *int `yaml:"consume_rate_bps"`

	Levels           int   `yaml:"levels"`
	MinSettleQuota   int64 `yaml:"min_settle_quota"`
	MaxPerOrderQuota int64 `yaml:"max_per_order_quota"`
	// HoldingDays 佣金成熟期,从消费所在自然日结束起算(见 modules/commission
	// 的 bucketMatureAt)。0 是合法策略:当天结束即可结算,不设防套利延迟。
	// 不写这个键才取默认的 7 天。
	HoldingDays        int `yaml:"holding_days"`
	SettleIntervalSecs int `yaml:"settle_interval_seconds"`
	// InviterCacheSecs 缓存 users.inviter_id。消费返佣挂在 relay 结算路径上,
	// 裸查会给主库加上与 relay QPS 等量的读压力。
	InviterCacheSecs     int `yaml:"inviter_cache_seconds"`
	TopupScanIntervalSec int `yaml:"topup_scan_interval_seconds"`
	// TopupScanLookbackHours: top_ups 表没有 updated_at,且部分支付路径不写
	// complete_time,纯 id 游标必然漏单,只能低水位 + 回扫窗口 + 唯一索引去重。
	TopupScanLookbackHours int `yaml:"topup_scan_lookback_hours"`

	ExcludeRedemptionAndManual bool `yaml:"exclude_redemption_and_manual"`
	ExcludeSubscriptionConsume bool `yaml:"exclude_subscription_consume"`
	RefundClawback             bool `yaml:"refund_clawback"`
}

// Withdraw 佣金提现。两种方式并存,用户自选:
// quota = 佣金兑换为平台余额(审核通过自动到账);fiat = 线下法币打款(人工)。
type Withdraw struct {
	Enabled             bool     `yaml:"enabled"`
	Methods             []string `yaml:"methods"`
	MinQuota            int64    `yaml:"min_quota"`
	MinFiatAmount       string   `yaml:"min_fiat_amount"`
	FiatCurrency        string   `yaml:"fiat_currency"`
	FiatFeeBps          int      `yaml:"fiat_fee_bps"`
	RateFreezeMode      string   `yaml:"rate_freeze_mode"`
	RateFreezeFixed     string   `yaml:"rate_freeze_fixed"`
	AutoCreditOnApprove *bool    `yaml:"auto_credit_on_approve"`
	// DailyMaxCount 单个用户每日可提交的提现单数,0 表示不限制。
	DailyMaxCount int `yaml:"daily_max_count"`
	// PayeeAccountMax 单个用户可保存的收款方式数量,0 表示不限制。
	PayeeAccountMax int `yaml:"payee_account_max"`
	// ReviewSLAHours 审核时限,超时的待审单在队列里标红。0 表示不计时限。
	ReviewSLAHours int `yaml:"review_sla_hours"`
	// RemarkMaxRunes 用户自定义说明的字数上限,必须落在 1..2000 —— 0 不是
	// "不限制"而是"一个字都不许填",validateWithdraw 会拒绝启动。
	RemarkMaxRunes int `yaml:"remark_max_runes"`
	// PIIKey 是收款信息的 AES-GCM 密钥(base64,32 字节)。为空则禁止 fiat 方式:
	// 收款信息属 PII,明文落库不可接受。
	PIIKey        string `yaml:"pii_key"`
	PIIKeyVersion int    `yaml:"pii_key_version"`
	// PIIKeysRetired 是已停用的历史密钥(版本号 → base64 密钥),用于解密轮换之前
	// 写入的密文。KeyVersion 列存在的全部意义就在这里。
	//
	// 轮换步骤:把当前 pii_key 连同它的 pii_key_version 搬进本表,再填新的 pii_key
	// 与更大的 pii_key_version。少了搬运这一步,队列里全部待打款单的收款账号会
	// 同时变成不可解密 —— 钱打不出去,而佣金还锁在 frozen 里。
	//
	// 旧密钥要留到对应密文被 pii_retention_days 清干净为止,不能提前删。
	PIIKeysRetired map[int]string `yaml:"pii_keys_retired"`
	// DigestKey 独立于 PIIKey 且不轮换,用于跨账户风控索引。
	// 与加密密钥分离,否则轮换后历史 digest 全部失效。
	DigestKey string `yaml:"digest_key"`
	// CooldownSecs 两次提现申请之间的最小间隔。0 表示不限制。
	CooldownSecs int `yaml:"cooldown_seconds"`
	// MaxPendingOrders 限制同时存在的未终态提现单数量。资金安全由佣金冻结
	// 保证(不会超提),但没有这个限制时审核队列会被大量小额单淹没。0 表示不限制。
	MaxPendingOrders int `yaml:"max_pending_orders"`
	// MaxQuotaPerOrder / DailyMaxQuota 提现额度上限。不设的话单笔上界只有
	// 主库 int32 容量,一次异常申请就会占满整个佣金池。0 表示不限制。
	MaxQuotaPerOrder int64 `yaml:"max_quota_per_order"`
	DailyMaxQuota    int64 `yaml:"daily_max_quota"`
	// PIIRetentionDays 收款信息的保留天数,到期后清除密文只保留脱敏串。
	// 收款信息属个人敏感信息,不应在提现完成后无限期留存。
	//
	// 不写这个键会取默认的 180 天 —— 清理默认开着是刻意的:少配一个键就让
	// 一批银行卡号永久留存,不该是默认结局。要彻底关掉清理请显式写 0。
	PIIRetentionDays int `yaml:"pii_retention_days"`
	// ProofEnabled 决定是否允许用户给法币提现附一张收款/打款凭证图片。
	//
	// 图片落在【本地磁盘】(与本配置文件同级的 qy-withdraw-proofs/),不入库。
	// 因此它带着一条部署约束:多节点各存各的,A 节点收到的上传 B 节点下载不到。
	// 单节点部署无碍;多节点需要共享存储(NFS/EFS)或后续接对象存储。
	// 不想在磁盘上留 PII 图片就显式置 false —— 上传接口会直接拒绝。
	ProofEnabled *bool `yaml:"proof_enabled"`
	// ProofMaxBytes 单张凭证的字节上限,上界见 MaxWithdrawProofBytes。
	// 请求体在读第一个字节之前就被 http.MaxBytesReader 按它截断。
	// 必须落在 1..MaxWithdrawProofBytes:0 不是"不限制"(那等于把堆交给上传者),
	// validateWithdraw 会拒绝启动。不想收凭证请用 proof_enabled: false。
	ProofMaxBytes int64 `yaml:"proof_max_bytes"`
}

// MaxWithdrawProofBytes 是 withdraw.proof_max_bytes 的硬上界。
//
// 存在的理由是上传缓冲:校验魔数需要把整张图读进内存,配得越大,一次
// CriticalRateLimit 放行的并发上传能吃掉的堆就越多。凭证是手机拍的收款截图,
// 8 MiB 足够,再大只说明配错了。
const MaxWithdrawProofBytes = 8 << 20

// Ticket 工单系统。用户提问、管理员答复,正文按 Markdown 渲染,可附图片。
//
// 全部旋钮都是**防滥用**的:工单是站点里唯一一条"任何登录用户都能往数据库里
// 写长文本 + 往磁盘里写图片"的路径,没有闸门时一个脚本几分钟就能把客服队列
// 和磁盘一起打满。每一项的 0 语义都在字段注释里写死,不要凭直觉猜。
type Ticket struct {
	Enabled bool `yaml:"enabled"`

	// TitleMaxRunes / BodyMaxRunes 按 rune 计(中文与 emoji 都算 1),必须大于 0 ——
	// 0 不是"不限制"而是"一个字都不许填",validateTicket 会拒绝启动。
	// 正文是 Markdown 源码,上限要留得下一段带代码块的报错粘贴。
	TitleMaxRunes int `yaml:"title_max_runes"`
	BodyMaxRunes  int `yaml:"body_max_runes"`

	// MaxOpenPerUser 单个用户同时挂着的未关闭工单数上限,0 表示不限制。
	//
	// 这是最重要的一道闸:它同时限制了"刷工单"和"同一个人在十张单里问同一件事"。
	// 与之配套的出口有两个 —— 用户自己可以关单,AutoCloseDays 也会替长期没人
	// 回应的单收尾;两个出口都没有的话这道闸会把正常用户永久锁死。
	MaxOpenPerUser int `yaml:"max_open_per_user"`
	// DailyMaxCount 单个用户每日可新建的工单数,0 表示不限制。
	DailyMaxCount int `yaml:"daily_max_count"`
	// CooldownSecs 两次新建工单之间的最小间隔,0 表示不限制。
	CooldownSecs int `yaml:"cooldown_seconds"`
	// ReplyCooldownSecs 同一用户两次追加回复之间的最小间隔,0 表示不限制。
	// 与 CooldownSecs 分开是因为追加回复是正常对话节奏的一部分,
	// 用新建工单那档间隔去卡它,只会让人在补充一句话时被拒。
	ReplyCooldownSecs int `yaml:"reply_cooldown_seconds"`
	// MaxMessagesPerTicket 单张工单的消息条数上限(双方合计),必须大于 0。
	// 没有它,一张工单可以被追加到几万条,详情接口从此再也打不开。
	MaxMessagesPerTicket int `yaml:"max_messages_per_ticket"`

	// AutoCloseDays:管理员已回复、用户超过这么多天没有再回应的工单自动关闭。
	// 0 表示不自动关闭。
	//
	// 只对"等用户回话"的状态生效(replied),绝不碰 open / user_replied ——
	// 那两个状态在等的是客服,自动关掉等于把没人处理的工单悄悄抹掉。
	AutoCloseDays int `yaml:"auto_close_days"`

	// ImageEnabled 决定是否允许附图片。图片落在【本地磁盘】(与本配置文件同级的
	// qy-ticket-images/),不入库 —— 理由与提现凭证一致(见 service/imagestore)。
	//
	// 它带着同一条部署约束:多节点各存各的,A 节点收到的上传 B 节点下载不到。
	// 单节点部署无碍;多节点需要共享存储或后续接对象存储。
	// 不想在磁盘上留用户上传的图就显式置 false —— 上传接口会直接拒绝。
	ImageEnabled *bool `yaml:"image_enabled"`
	// ImageMaxBytes 单张图片的字节上限,必须落在 1..MaxTicketImageBytes。
	// 0 不是"不限制"(那等于把堆交给上传者),validateTicket 会拒绝启动;
	// 不想收图请用 image_enabled: false。
	ImageMaxBytes int64 `yaml:"image_max_bytes"`
	// ImageMaxPerMessage 单条消息可附的图片数上限,必须大于 0。
	ImageMaxPerMessage int `yaml:"image_max_per_message"`
	// ImageUserQuotaBytes 单个用户在磁盘上可占用的工单图片总字节数,0 表示不限制。
	//
	// 这是唯一一道**总量**闸。单条消息数、未绑定上传数都只管"一次能传几张",
	// 图片一旦随消息提交,那些计数就归零;而自动关闭只碰 replied,一个每次收到
	// 回复就再补一句的账号可以让自己的工单永远不进入可清理状态 ——
	// 于是它的图片永远不会被保留期扫到。没有这一条,单个账号即可长期钉住
	// max_open_per_user × max_messages_per_ticket × image_max_per_message ×
	// image_max_bytes 那么多字节(默认口径下是 3 GiB),而磁盘写满会让整个进程
	// 一起挂,不只是工单。
	ImageUserQuotaBytes int64 `yaml:"image_user_quota_bytes"`
	// ImageRetentionDays 工单关闭之后图片的保留天数,0 表示永久保留。
	//
	// 只从**关闭时刻**起算,不从上传时刻:一张挂了半年还在争议中的工单,
	// 它的截图正是争议的物证,按上传时间清掉等于在还需要它的时候销毁证据。
	ImageRetentionDays int `yaml:"image_retention_days"`
}

// MaxTicketImageBytes 是 ticket.image_max_bytes 的硬上界。
//
// 与 MaxWithdrawProofBytes 同一条理由(校验魔数要整张读进内存),
// 真正的判定常量在 qianye/service/imagestore.MaxBytes,这里只是把它搬到
// config 包里供校验器引用 —— config 是被 imagestore 依赖的一方,不能反向 import。
const MaxTicketImageBytes = 8 << 20

// Wallet 钱包页入口开关。
type Wallet struct {
	ShowTransferEntry   *bool `yaml:"show_transfer_entry"`
	ShowCommissionEntry *bool `yaml:"show_commission_entry"`
	ShowWithdrawEntry   *bool `yaml:"show_withdraw_entry"`
}

// LogMetrics 使用日志新增列(推理强度 / 缓存百分比)。
type LogMetrics struct {
	ShowReasoningEffort *bool `yaml:"show_reasoning_effort"`
	ShowCacheRatio      *bool `yaml:"show_cache_ratio"`
	// EnableFilter 打开后需要旁路物化表才能在 SQL 层筛选/排序,成本显著上升。
	EnableFilter bool `yaml:"enable_filter"`
}

// GroupVisibility 无权分组泄漏修复。
//
// 刻意没有 filter_group_api:分组 API(controller.GetUserGroups)本身就是
// GroupRatio ∩ service.GetUserUsableGroups 的交集,匿名请求退化为运营方
// 主动配置的公开分组,不存在泄漏,没有任何东西可过滤。留着一个恒真却
// 不接任何代码的开关,只会让运维以为那一路也被"关掉过滤"影响 —— 详见
// qianye/modules/groupvis/groupvis.go 的说明。
type GroupVisibility struct {
	Enabled           *bool `yaml:"enabled"`
	FilterPricing     *bool `yaml:"filter_pricing"`
	FilterPerfMetrics *bool `yaml:"filter_perf_metrics"`
	IncludeAutoGroup  *bool `yaml:"include_auto_group"`
}

// Availability 模型可用率监控。
type Availability struct {
	Enabled              bool `yaml:"enabled"`
	SampleAttemptLevel   bool `yaml:"sample_attempt_level"`
	BucketSeconds        int  `yaml:"bucket_seconds"`
	FlushIntervalSeconds int  `yaml:"flush_interval_seconds"`
	RetentionDays        int  `yaml:"retention_days"`
	MaxSeriesPerQuery    int  `yaml:"max_series_per_query"`
	CountClientErrors    bool `yaml:"count_client_errors"`
	CountRateLimited     bool `yaml:"count_rate_limited"`
}

// Violation 违规检测。
//
// **这里没有 shadow_mode。** 影子/真实是每条规则自己的属性(qy_violation_rule.mode),
// 新建与内置导入一律落影子,改模式就是改那条规则。曾经存在的全局 shadow_mode
// 已随 qy_settings 覆盖与 PUT /violation/mode 一并删除:两层结构让"把这条规则设成
// 影子来观察"这个用例需要先确认全局在哪一档,而全局默认为影子时规则级怎么调都不生效。
// 规则事故的兜底仍在,由下面两个熔断阈值负责(见 modules/violation/breaker.go)。
type Violation struct {
	Enabled bool `yaml:"enabled"`
	// PrecheckEnabled / PostChargeEnabled 是普通 bool,零值为 false。
	// 也就是说只写 enabled: true 而不显式打开这两个开关,两个挂载点都是空转 ——
	// 这是安全的默认,但运维容易误以为"开了却不生效",示例配置里已注明。
	PrecheckEnabled   bool `yaml:"precheck_enabled"`
	PostChargeEnabled bool `yaml:"post_charge_enabled"`
	// FeeMultiplier 为 decimal 字符串(如 "1.0"),避免浮点误差进入计费。
	FeeMultiplier  string `yaml:"fee_multiplier"`
	FixedFeeAmount string `yaml:"fixed_fee_amount"`
	// MaxFeeQuota 单次违规扣费的全局硬上限(规则级上限另有 FeeMaxQuota)。
	// 0 表示不设全局上限,此时只剩规则级上限与主库 int32 容量兜底。
	MaxFeeQuota int64 `yaml:"max_fee_quota"`
	// InsufficientBalancePolicy: clamp(扣到 0 为止) | negative(允许负数) | ban。
	// 上游的 DecreaseUserQuota 没有余额校验,不指定策略会把余额扣成负数。
	InsufficientBalancePolicy string `yaml:"insufficient_balance_policy"`
	AutoBanThreshold          int    `yaml:"auto_ban_threshold"`
	AutoBanWindowHours        int    `yaml:"auto_ban_window_hours"`
	// GlobalBlockRateLimitBps / GlobalBanRateLimitPerHour 是熔断阈值:
	// 拦截率或封号速率异常时自动回落影子模式,防止规则写错造成全站事故。
	GlobalBlockRateLimitBps   int `yaml:"global_block_rate_limit_bps"`
	GlobalBanRateLimitPerHour int `yaml:"global_ban_rate_limit_per_hour"`
	EvidenceMaxBytes          int `yaml:"evidence_max_bytes"`
	EvidenceRetentionDays     int `yaml:"evidence_retention_days"`
	RuleCacheSeconds          int `yaml:"rule_cache_seconds"`
	ScanTimeoutMs             int `yaml:"scan_timeout_ms"`
}

// GroupPricing 模型按分组单独定价。
//
// 语义(用户已拍板,不可改):分组级价格与分组倍率是**相乘**关系,
// 最终扣费 = 分组级模型价 × 分组倍率。没有配置分组级价格的模型完全走原路径
// (全局价 × 分组倍率),升级不改变任何既有计费结果。
//
// ShadowMode 与 violation 的同名开关是同一种东西,而且更严格:这里改错的不是
// "要不要封号",而是每一笔请求实际扣走多少钱,且扣完不可逆。默认 true 表示
// 完整算出"若启用会扣多少"并记录差额,但实际仍按旧价扣费;运营对账确认后
// 再显式改 false。
type GroupPricing struct {
	Enabled    bool  `yaml:"enabled"`
	ShadowMode *bool `yaml:"shadow_mode"`
	// RuleCacheSeconds 是规则内存快照的刷新周期。规则读取在 relay 热路径上,
	// 每次请求查库不可接受。
	RuleCacheSeconds int `yaml:"rule_cache_seconds"`
	// MaxStaleSeconds 是快照允许的最大陈旧时间。超过它仍未刷新成功,
	// 查找一律回落成"无覆盖"(走全局价),绝不继续按一份来历不明的旧规则扣钱。
	MaxStaleSeconds int `yaml:"max_stale_seconds"`
	// ShadowFlushIntervalSeconds 是影子差额从内存落库的周期。
	ShadowFlushIntervalSeconds int `yaml:"shadow_flush_interval_seconds"`
	// ShadowRetentionDays 是影子差额聚合行的保留天数。
	ShadowRetentionDays int `yaml:"shadow_retention_days"`
	// MaxRules 是规则总数上限。规则表每个刷新周期被全量拉取,不设上界的话
	// 一次脚本误操作就会让每个节点定期拉一张大表。
	MaxRules int `yaml:"max_rules"`
}

// GroupMatrix 用户分组 × 模型分组的**权威可选清单**。
//
// 语义(项目方拍板,不可改):一个用户分组一旦在扩展库里有 scope 行,它能选哪些
// 模型分组就**完全由扩展侧的清单决定** —— 不再叠加上游全局白名单、不再应用
// GroupSpecialUsableGroup 的 +:/-: 差分、也不再无条件把用户分组自己补回去。
// 没有 scope 行的用户分组保持上游原行为,这是回退能力的基础。
//
// 倍率**不在这里**:唯一真相源仍是上游 options.GroupGroupRatio。扩展库只存
// 成员资格。存一份倍率镜像就是给同一份事实两个源,而同步失败的表现正是
// 「管理端显示 A、热路径乘 B」——本轮 grouppricing 的 effective 修复要消灭的
// 就是这个形状,不能一边修一边再造一个。
type GroupMatrix struct {
	// Enabled 是 L1 kill switch。关掉时 hook 恒等返回,**不依赖扩展库可达性** ——
	// 即使扩展库挂了、快照是坏的,读 YAML 快照的那次 atomic load 也走到恒等分支。
	Enabled bool `yaml:"enabled"`
	// CacheSeconds 是清单内存快照的刷新周期。清单读取在 relay 热路径上,
	// 每次请求查库不可接受。
	CacheSeconds int `yaml:"cache_seconds"`
	// MaxStaleSeconds 超过即限频告警,但**绝不丢弃快照**。
	//
	// 这一条与 group_pricing.max_stale_seconds 的处置**刻意相反**,理由见
	// qianye/modules/groupmatrix 的包注释:陈旧的**钱**丢弃更安全,
	// 陈旧的**可见性**保留更安全 —— 丢弃只能回落到上游宽松白名单,
	// 而那意味着被收紧的用户重新可以把令牌指向 ratio=0 的免费分组。
	MaxStaleSeconds int `yaml:"max_stale_seconds"`
	// PreviewLogDays 是影响面预览里「过去 N 天真的有人在用吗」的回看天数。
	PreviewLogDays int `yaml:"preview_log_days"`
	// MaxPreviewPairs 是一次预览最多展开的 (用户分组, 模型分组) 对数。
	MaxPreviewPairs int `yaml:"max_preview_pairs"`
	// PreviewSampleLimit 是每一对最多返回的令牌样本条数。
	PreviewSampleLimit int `yaml:"preview_sample_limit"`
	// MaxGrants 是清单总行数上限。清单每个刷新周期被全量拉取,不设上界的话
	// 一次脚本误操作就会让每个节点定期拉一张大表。
	MaxGrants int `yaml:"max_grants"`
	// WriteGuardEnabled 是令牌写入侧校验的独立开关。出事时可以单独摘掉写侧,
	// 而不影响读侧已经生效的收紧 —— 反过来则不允许(见包注释)。
	WriteGuardEnabled *bool `yaml:"write_guard_enabled"`

	// NewGroupDefaultDeny 决定**新出现的用户分组**是否一建出来就被全遮断
	// (自动建一条 mode=enforce、零条 grant 的 scope 行)。
	//
	// ══════════════ 为什么它必须能关掉,而且必须默认打开 ══════════════
	//
	// 项目方的要求是「新加的用户分组默认把所有模型分组屏蔽,直到手动添加」。
	// 这是一条**违反最小惊讶**的默认:运营在上游「系统设置-分组倍率」页加一个
	// key,回到令牌页却发现这一档的人一个模型分组都选不了,而他刚才那次操作
	// 没有任何地方提到过遮断。所以两件事都是硬要求:
	//
	//	1. 它必须是一个开关(本字段),关掉之后新分组与上游行为逐位一致;
	//	2. 打开时管理端矩阵页必须常驻一条提示,把「新分组默认全遮断」这句话
	//	   摆在运营眼前 —— 一个只写在 YAML 注释里的默认等于没有告知。
	//
	// *bool 且默认打开:普通 bool 的零值是 false,那样"没写这一行"与"读过文档、
	// 想清楚了、显式关掉"在进程内是同一个字节,而本仓已经因为这个形状栽过三次。
	NewGroupDefaultDeny *bool `yaml:"new_group_default_deny"`

	// NewGroupScanIntervalSeconds 是「新分组对账」后台任务的周期。
	//
	// 上游没有任何"分组被创建"的事件可挂 —— 用户分组的事实清单就是
	// options.GroupRatio 的键集合(controller/group.go 直接把它的 key 当分组列表
	// 下发),而它由通用的 model.UpdateOption 写入,没有分组语义的钩子。
	// 为它在上游加 hook 要动 setting/ratio_setting 或 model/option.go,
	// 而对账任务一行上游代码都不用改,代价只是最长一个周期的发现延迟。
	//
	// 延迟是安全方向的:窗口期内新分组按上游宽松白名单放行(与今天一致),
	// 而不是先拒后放。
	NewGroupScanIntervalSeconds int `yaml:"new_group_scan_interval_seconds"`
}

// WriteGuardOn 表示令牌写入侧校验是否打开(默认打开)。
func (g GroupMatrix) WriteGuardOn() bool { return boolOr(g.WriteGuardEnabled, true) }

// NewGroupDefaultDenyOn 表示新出现的用户分组是否默认全遮断(默认打开)。
func (g GroupMatrix) NewGroupDefaultDenyOn() bool { return boolOr(g.NewGroupDefaultDeny, true) }

// Lottery 娱乐功能:抽奖(kind=draw)与竞猜(kind=guess)共用一套配置。
//
// 两者的资格引擎、扣费链路、名单冻结、逐笔派奖状态机、证据链与退款路径完全同构,
// 唯一差别是"谁是赢家"这个纯函数,因此没有拆成两段配置。
type Lottery struct {
	Enabled bool `yaml:"enabled"`
	// ShowEntry 是需求原文里的"系统设置前端是否显示"。它与 Enabled 分开:
	// 关掉入口之后接口仍然可用(已参与的用户要能查自己的记录与历史证据),
	// 关掉 Enabled 才是整套功能下线。
	ShowEntry *bool `yaml:"show_entry"`
	// ProofPublic 决定证据链端点是否匿名可访问。默认开 —— 需要注册账号才能
	// 取证的公正性不叫公正性。留这个开关只是为了应对被爬虫打爆时的应急关停。
	ProofPublic *bool `yaml:"proof_public"`

	MaxActiveActivities int   `yaml:"max_active_activities"`
	MaxStakeQuota       int64 `yaml:"max_stake_quota"`
	// MaxTotalPrizeQuota 是抽奖奖品总额度的硬闸门。
	//
	// 抽奖是"平台收参与费、平台出奖品",派奖对 users.quota 是**净增发**,
	// 下游没有任何环节能拦住一个多写了零的奖品金额 —— 这里是唯一的闸门。
	MaxTotalPrizeQuota int64 `yaml:"max_total_prize_quota"`
	// LargePrizeAlertQuota 只告警不阻断:运营确实可能办大活动。
	LargePrizeAlertQuota int64 `yaml:"large_prize_alert_quota"`
	// PayPasswordThresholdQuota 是参与费触发支付密码的门槛。参与是不可逆消费,
	// 盗号者能用"参与抽奖"把余额烧光而不留下划转/提现痕迹。
	PayPasswordThresholdQuota int64 `yaml:"pay_password_threshold_quota"`

	// EntryCloseGraceSeconds 是封盘前停止受理新报名的提前量,给两阶段的
	// pending 单留出收敛窗口,让"封盘时还有未决参与"降到近零。
	EntryCloseGraceSeconds int `yaml:"entry_close_grace_seconds"`
	// RevealDelaySeconds 是 close_at → draw_at 的强制最小间隔。
	// 它是整个承诺-揭示协议的关键:名单哈希必须先于种子公开,
	// 中间要留出足够时间让任何人抓到一份平台无法否认的快照。不允许为 0。
	RevealDelaySeconds int `yaml:"reveal_delay_seconds"`

	LockScanIntervalSeconds   int `yaml:"lock_scan_interval_seconds"`
	RevealScanIntervalSeconds int `yaml:"reveal_scan_interval_seconds"`
	PayoutIntervalSeconds     int `yaml:"payout_interval_seconds"`
	PayoutMaxAttempts         int `yaml:"payout_max_attempts"`
	// ExcludedManualAfterSeconds 是"参与单卡在不可判定态多久之后转人工"。
	ExcludedManualAfterSeconds int `yaml:"excluded_manual_after_seconds"`

	// MaxTotalEntriesHard 是名单规模上界:冻结要在单个事务里流式算完。
	MaxTotalEntriesHard int `yaml:"max_total_entries_hard"`
	MaxPrizeTiers       int `yaml:"max_prize_tiers"`
	MaxOptions          int `yaml:"max_options"`
	DefaultGuessFeeBps  int `yaml:"default_guess_fee_bps"`
	// MaxGuessFeeBps 防运营把 5% 手滑打成 50%。
	MaxGuessFeeBps int `yaml:"max_guess_fee_bps"`

	SpendScanIntervalSeconds int `yaml:"spend_scan_interval_seconds"`
	SpendScanBatch           int `yaml:"spend_scan_batch"`
	// SpendGapGuardSeconds 是消费扫描的时间护栏:自增 id 在插入时分配、提交
	// 可能更晚,裸 `id > watermark` 会跳过后提交的小 id 行。
	SpendGapGuardSeconds int `yaml:"spend_gap_guard_seconds"`
	SpendMaxLookbackDays int `yaml:"spend_max_lookback_days"`
	SpendRetentionDays   int `yaml:"spend_retention_days"`
}

// EntryShown 表示前端是否渲染娱乐入口。
func (l Lottery) EntryShown() bool { return boolOr(l.ShowEntry, true) }

// ProofOpen 表示证据链端点是否匿名可访问。
func (l Lottery) ProofOpen() bool { return boolOr(l.ProofPublic, true) }

// ───────────────────────────── 布尔取值辅助 ─────────────────────────────
//
// *bool 承载"默认为 true"的开关:普通 bool 的零值是 false,无法区分
// "用户显式写了 false"和"用户没写"。applyDefaults 会把 nil 补成默认值,
// 因此下列方法在 Load 之后不会遇到 nil,但仍做防御以支持零值 Config。

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func (d Database) ShouldAutoMigrate() bool { return boolOr(d.AutoMigrate, true) }

func (r Runtime) FailOpen() bool       { return boolOr(r.HotPathFailOpen, true) }
func (r Runtime) BackgroundOn() bool   { return boolOr(r.BackgroundEnabled, true) }
func (t TwoPhase) OutboxEnabled() bool { return boolOr(t.MainOutboxEnabled, true) }
func (a Audit) On() bool               { return boolOr(a.Enabled, true) }
func (a Audit) ShouldRecordIP() bool   { return boolOr(a.RecordIP, true) }

// RequestOn 表示是否写 HTTP 请求台账。
//
// 与 audit.enabled 是**与**的关系:整套审计关掉时,请求台账不该还在写 ——
// 否则"我把审计关了"这句话在两张表上含义不同,而运维只会记住一句。
func (a Audit) RequestOn() bool                { return a.On() && boolOr(a.RequestEnabled, true) }
func (t Transfer) ReceiverMustBeEnabled() bool { return boolOr(t.RequireReceiverEnabled, true) }
func (w Withdraw) AutoCredit() bool            { return boolOr(w.AutoCreditOnApprove, true) }
func (w Wallet) TransferEntry() bool           { return boolOr(w.ShowTransferEntry, true) }
func (w Wallet) CommissionEntry() bool         { return boolOr(w.ShowCommissionEntry, true) }
func (w Wallet) WithdrawEntry() bool           { return boolOr(w.ShowWithdrawEntry, true) }
func (l LogMetrics) ReasoningColumn() bool     { return boolOr(l.ShowReasoningEffort, true) }
func (l LogMetrics) CacheRatioColumn() bool    { return boolOr(l.ShowCacheRatio, true) }
func (g GroupVisibility) On() bool             { return boolOr(g.Enabled, true) }
func (g GroupVisibility) PricingOn() bool      { return boolOr(g.FilterPricing, true) }
func (g GroupVisibility) PerfMetricsOn() bool  { return boolOr(g.FilterPerfMetrics, true) }
func (g GroupVisibility) KeepAutoGroup() bool  { return boolOr(g.IncludeAutoGroup, true) }

// IsShadow 为 true 时分组定价只记录差额、不改变实际扣费。默认 true。
func (g GroupPricing) IsShadow() bool { return boolOr(g.ShadowMode, true) }

// HasWithdrawMethod 判断某种提现方式是否启用。
func (w Withdraw) HasWithdrawMethod(m string) bool {
	for _, v := range w.Methods {
		if v == m {
			return true
		}
	}
	return false
}

// ProofOn 表示凭证图片功能当前是否可用。
//
// 把"法币方式已开放"并进这一个判定,而不是让每个调用点各写一遍
// `HasWithdrawMethod(fiat) && ProofEnabled`:凭证只服务于法币打款(站内额度
// 兑换没有任何要凭证的场景),两个条件漏写一个的方向恰好是"quota-only 的站点
// 也开始往磁盘上收 PII 图片"。
func (w Withdraw) ProofOn() bool {
	return w.HasWithdrawMethod(WithdrawMethodFiat) && boolOr(w.ProofEnabled, true)
}

// ImageOn 表示工单是否允许附图片。默认开。
func (t Ticket) ImageOn() bool { return boolOr(t.ImageEnabled, true) }

// ───────────────────────────── 加载与访问 ─────────────────────────────

var (
	current    atomic.Pointer[Config]
	loadedPath atomic.Value // string
	loadedAt   atomic.Int64
	loadedMod  atomic.Int64
)

// Load 定位并解析配置文件。
//
// 返回 error 的场景仅限"运维事故":显式指定的文件不存在、YAML 语法错、校验失败。
// 这些必须让主程序 FatalLog 而不是静默跑成没功能 —— 风控开关拼错却默默不生效
// 是最危险的失败模式。
//
// 找不到任何配置文件不是错误:返回 nil 且 Enabled() == false,主程序照常启动。
func Load() error {
	path, found := resolvePath()
	if !found {
		if path != "" {
			return fmt.Errorf("qianye: QIANYE_CONFIG 指向的配置文件不存在: %s", path)
		}
		common.SysLog("qianye: 未找到配置文件,扩展已禁用")
		current.Store(&Config{})
		return nil
	}
	c, mod, err := parseFile(path)
	if err != nil {
		return err
	}
	current.Store(c)
	loadedPath.Store(path)
	loadedAt.Store(common.GetTimestamp())
	loadedMod.Store(mod)
	warnIfWorldReadable(path, c)
	common.SysLog(fmt.Sprintf("qianye: 已从 %s 加载配置 (enabled=%v)", path, c.Enabled))
	return nil
}

func parseFile(path string) (*Config, int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("qianye: 读取配置文件失败: %w", err)
	}
	var mod int64
	if st, statErr := os.Stat(path); statErr == nil {
		mod = st.ModTime().Unix()
	}

	c := &Config{}
	// 解析前给数值字段打哨兵,解析后由 applyDefaults 判定"这个键写没写"。
	// yaml.v3 只写它在文件里见到的键,没见到的字段原样留着哨兵。
	markNumbersUnset(reflect.ValueOf(c).Elem())

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	// 严格模式:未知字段一律报错,绝不降级为宽松解析。
	//
	// 理由:风控开关名拼错却被静默忽略,是本系统最危险的失败模式 ——
	// 运维以为"排除管理员补单返佣"已经打开,实际一直没生效,等发现时钱已经流走了。
	// 宁可启动失败也不能带着一个失效的开关跑。
	//
	// 代价是"回滚二进制但保留新版配置"的场景会启动失败,但那时错误信息会
	// 直接点名是哪个字段,处理起来一目了然。
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		if strings.Contains(err.Error(), "not found in type") {
			return nil, 0, fmt.Errorf(
				"qianye: 配置文件含无法识别的字段: %w\n"+
					"  这通常是字段名拼写错误;若你刚回滚过版本,请同步移除新版本才有的配置项。\n"+
					"  为避免风控开关静默失效,此处不做兼容处理", err)
		}
		return nil, 0, fmt.Errorf("qianye: 解析配置文件失败: %w", err)
	}

	// 严格解析已经过关,文件语法必定合法。再走一遍 yaml.Node 只为记下
	// "哪些键被写出来了" —— 这个事实在 Config 结构体上表达不出来,而它是
	// sections.go 区分"没写这一段"与"显式关掉"的唯一依据。
	c.declared = declaredPaths(raw)

	applyDefaults(c)
	if err := validate(c); err != nil {
		return nil, 0, err
	}
	return c, mod, nil
}

// warnIfWorldReadable 在配置文件对所有用户可读时告警。
// 文件含数据库密码与 PII 密钥,但权限问题不阻塞启动 —— 容器场景下往往无法修正。
func warnIfWorldReadable(path string, c *Config) {
	if c.Database.DSN == "" && c.Withdraw.PIIKey == "" {
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	if st.Mode().Perm()&0o044 != 0 {
		common.SysError(fmt.Sprintf(
			"qianye: 配置文件 %s 权限为 %#o,其他用户可读,但其中含数据库密码/密钥,建议改为 0600",
			path, st.Mode().Perm()))
	}
}

// Get 返回当前配置快照。永不返回 nil。
func Get() *Config {
	if c := current.Load(); c != nil {
		return c
	}
	return &Config{}
}

// Enabled 表示扩展是否启用(配置文件存在且 enabled: true)。
func Enabled() bool { return Get().Enabled }

// Path 返回配置文件的实际加载路径,供管理端健康面板显示。
func Path() string {
	p, _ := loadedPath.Load().(string)
	return p
}

// LoadedAt 返回配置加载时间(unix 秒)。
func LoadedAt() int64 { return loadedAt.Load() }

// ModTime 返回加载时配置文件的修改时间(unix 秒)。
func ModTime() int64 { return loadedMod.Load() }
