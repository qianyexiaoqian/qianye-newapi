// Package violation 实现违规检测:转发前提示词拦截、事后上游错误检测扣费、
// 累计计数自动封号、证据留存与误判申诉。
//
// 三条铁律,违反任何一条都会造成全站事故或资损:
//
//  1. 影子模式是默认状态。一条 `.*` 正则能在 30 秒内封掉全站用户,所以还叠加了
//     熔断:拦截率或封号速率越界时强制回落影子模式。全局开关可由管理端在
//     qy_settings 里切换(见 settings.go),YAML 只是它的兜底默认值;
//     影子命中除了不扣费不阻断不封号,**也不推进违规计数**(裁决 2)。
//  2. 热路径永不阻塞。规则只读进程内快照,所有落库走 guard.HotAsync;
//     扩展库故障一律 fail-open(放行),扩展绝不能成为 relay 的单点故障。
//  3. 扣费金额只走 common.Quota* 转换并强制上限。违规扣费是"惩罚"而不是"计费",
//     配置写错的后果是一次扣光用户余额,因此规则级与全局级两道上限都必须生效。
package violation

import (
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// 规则生效阶段。拆开 Phase 与 MatchType 的理由:同一份词表既可能拦 prompt
// 也可能匹配上游返回,两者执行时机与开销完全不同,合并会让热路径跑无用规则。
const (
	PhasePrompt       = "prompt"        // 转发上游之前(能力 B)
	PhaseUpstreamErr  = "upstream_err"  // 上游返回错误之后(能力 A)
	PhaseRejectReason = "reject_reason" // 上游软违规信号 ContextKeyAdminRejectReason
)

// 匹配方式。
const (
	MatchKeyword      = "keyword"       // 换行分隔词表,走 service.AcSearch(AC 自动机)
	MatchRegex        = "regex"         // Go RE2,线性时间,无回溯灾难
	MatchErrorCode    = "error_code"    // 逗号分隔的 types.ErrorCode 精确值
	MatchStatusCode   = "status_code"   // 逗号分隔 HTTP 状态码或区间 "400-499"
	MatchUpstreamText = "upstream_text" // 换行分隔子串,匹配上游错误文本
	// MatchRequestRate 是唯一一种不看文本的匹配方式:pattern 是一个整数阈值,
	// 命中条件为"该用户 rateWindowSeconds 内已通过校验、即将发往上游的**非流式**
	// 请求条数 >= 阈值"。这条判据服务于"防蒸馏":批量采集训练语料的一方要的是
	// 完整 JSON,不会开 stream;开 stream 的通常是真人在等字。
	//
	// **它的局限必须公开写在管理端表单里**:客户端加一行 "stream": true 就能
	// 完全绕过。它是一道减速带,不是一堵墙。判据本身无法根治这一点 ——
	// 逐字节比对流式与非流式的产出没有可行的在线实现。
	MatchRequestRate = "request_rate"
)

// 分组作用域的名单方向。
//
// 只加这一列、不加第二份名单:两张能互相矛盾的名单(白名单 + 黑名单)必然漂移,
// 而"到底哪张说了算"没有任何一个取值组合能自解释。空 GroupScope 时这一列恒为
// include(见 ruleUpsertReq.apply),因为"空黑名单"与"空白名单"语义完全相同,
// 留两个等价状态只会让人以为它们不同。
const (
	GroupScopeInclude = "include" // 名单非空 = 只对名单内的分组生效
	GroupScopeExclude = "exclude" // 名单非空 = 对名单内的分组豁免,其余全部生效
)

// 处置动作。
const (
	ActionRecord         = "record"           // 仅记录
	ActionCharge         = "charge"           // 扣费,不阻断
	ActionBlock          = "block"            // 阻断(仅 prompt 阶段有效)
	ActionBlockAndCharge = "block_and_charge" // 阻断 + 扣费
)

// 扣费方式。
const (
	FeeNone               = "none"
	FeeFixed              = "fixed"                // 固定美元金额
	FeeModelPriceMultiple = "model_price_multiple" // 模型价格 × 倍数
)

// 违规记录状态。
const (
	RecordActive   = "active"
	RecordRevoked  = "revoked"
	RecordAppealed = "appealed"
)

// CounterAfterShadow 是影子记录 counter_after 列的固定取值。
//
// 影子命中不推进违规计数(裁决 2:「不扣费,不封号,不记录违规次数」),
// 所以"命中之后计数是多少"这个问题对它根本没有答案。取 0 会与"计数确实是 0"
// 混淆;-1 是 bumpCounter 不可能产生的哨兵值,直接查库的人也能一眼看出这一行
// 从未参与计数。管理端列表按 counted=false 显示 "-",不读这一列。
//
// **已知代价(不要在文档里糊过去)**:管理员因此失去了 O(1) 的
// "这个用户在影子模式下已经攒了多少次"。要得到这个数只能去 qy_violation_record
// 按 (user_id, shadow=true, created_at >= 窗口起点) 做一次 COUNT ——
// 在这张增长最快的表上是一次范围扫描,比读计数器贵得多。
// 这是裁决 2 明确接受的代价:另加一列"影子计数"就是在记录违规次数,
// 而它一旦存在,下一次改动就会有人把它接回封号判据。
const CounterAfterShadow = -1

// 扣费结果。want 与实际两列并存,是"余额不足被截断"这类偏差唯一的可审计留痕。
const (
	FeeStatusNone         = "none"
	FeeStatusCharged      = "charged"
	FeeStatusTruncated    = "truncated"    // 余额不足,按 clamp 策略只扣了一部分
	FeeStatusInsufficient = "insufficient" // 余额为 0,一分未扣
	FeeStatusShadow       = "shadow"       // 影子模式:算出了金额但没扣
	FeeStatusFailed       = "failed"
	FeeStatusRefunded     = "refunded"
	FeeStatusSkippedDup   = "skipped_dup_builtin" // 上游内置 Grok 违规扣费已扣过
)

// 封禁状态。
const (
	BanPending = "pending" // 已认领,主库六步尚未完成 → 补偿任务扫描
	BanBanned  = "banned"
	BanSkipped = "skipped" // 目标是 root / 已禁用 / 已删除
	BanFailed  = "failed"
	// BanDeferred 表示"计数确实达到了阈值,但当时的策略不允许执行封号"
	// (目前只有小时封号速率闸会产生它)。
	//
	// 它存在的唯一理由是把这个事实持久化。没有它,"该封号"只活在一次函数调用里:
	// 速率闸一挡,信号就消失了,管理端也看不到任何痕迹。deferred 行不会被补偿任务
	// 自动执行 —— 速率闸的语义就是"先让人看一眼",自动补做等于把它彻底架空 ——
	// 而是由管理员在封禁列表里判定"不予封禁"(unbanUser 接受 deferred),
	// 或等该用户下一次违规时在条件允许的情况下被提升执行。
	BanDeferred = "deferred"
	BanUnbanned = "unbanned"
)

// 申诉状态。
const (
	AppealPending   = "pending"
	AppealApproved  = "approved"
	AppealRejected  = "rejected"
	AppealWithdrawn = "withdrawn"
)

// Rule 是一条违规规则。
//
// 软删而非硬删:历史记录冗余了 rule_name,但管理端复核时仍需要回看规则原文,
// 硬删会让申诉流程失去判断依据。
type Rule struct {
	Id     int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Name   string `json:"name" gorm:"type:varchar(128);not null"`
	Remark string `json:"remark" gorm:"type:varchar(512);not null;default:''"`
	// PublicReason 是写进用户计费日志与用户端列表的对外文案。
	// 与 Name 分开:Name 常含内部代号(如 "csam_v3_高危"),直接给用户看等于泄漏规则库。
	PublicReason string `json:"public_reason" gorm:"type:varchar(128);not null;default:''"`

	Enabled bool `json:"enabled" gorm:"not null;index:idx_qy_vr_enabled_phase,priority:1"`
	// DryRun 是规则级影子:允许单条新规则先灰度观察,而不必把全局 shadow_mode 打开。
	DryRun   bool `json:"dry_run" gorm:"not null;default:false"`
	Priority int  `json:"priority" gorm:"not null;default:100"` // 升序,小者先判

	Phase     string `json:"phase" gorm:"type:varchar(24);not null;index:idx_qy_vr_enabled_phase,priority:2"`
	MatchType string `json:"match_type" gorm:"type:varchar(24);not null"`
	Pattern   string `json:"pattern" gorm:"type:text;not null"`
	// CaseSensitive 只对 keyword / upstream_text / regex 有意义。
	CaseSensitive bool `json:"case_sensitive" gorm:"not null;default:false"`

	// ModelScope / GroupScope 对应需求原文"某个模型(全部分组或特定分组下)"。
	// 空 = 全部;否则逗号分隔,模型支持 "gpt-4*" / "*-vision" 前后缀通配。
	ModelScope string `json:"model_scope" gorm:"type:varchar(2048);not null;default:''"`
	GroupScope string `json:"group_scope" gorm:"type:varchar(1024);not null;default:''"`
	// GroupScopeMode 决定 GroupScope 是白名单还是黑名单(include / exclude)。
	// 空串按 include 处理:滚动升级期间旧节点写下的行、以及 DBA 手工建的表都可能
	// 留空,而 include 正是这一列出现之前的唯一语义,回落到它不改变任何既有规则。
	GroupScopeMode string `json:"group_scope_mode" gorm:"type:varchar(8);not null;default:'include'"`

	Action  string `json:"action" gorm:"type:varchar(24);not null;default:'record'"`
	FeeMode string `json:"fee_mode" gorm:"type:varchar(24);not null;default:'none'"`
	// FeeFixed / FeeMultiple 为 0 时回落到 YAML 的 fixed_fee_amount / fee_multiplier,
	// 这样"改一次配置调整全部规则"与"单条规则特殊定价"两种用法都成立。
	FeeFixed    decimal.Decimal `json:"fee_fixed" gorm:"type:decimal(18,8);not null;default:0"`
	FeeMultiple decimal.Decimal `json:"fee_multiple" gorm:"type:decimal(18,6);not null;default:0"`
	// FeeMaxQuota 是规则级单笔上限(quota,0 = 不限)。
	// model_price_multiple 遇到高价模型 + 大倍数会一次扣穿余额,必须有闸。
	FeeMaxQuota int64 `json:"fee_max_quota" gorm:"not null;default:0"`

	// CountWeight = 0 允许"只扣费不累计封号";Severity 仅用于管理端排序与告警。
	CountWeight int `json:"count_weight" gorm:"not null;default:1"`
	Severity    int `json:"severity" gorm:"not null;default:1"`

	ArchiveContext bool `json:"archive_context" gorm:"not null;default:false"`
	// BlockMessage 是返回给客户端的文案。严禁把命中词写进来 —— 等于告诉刷子绕过方法。
	BlockMessage string `json:"block_message" gorm:"type:varchar(512);not null;default:''"`

	CreatedAt int64          `json:"created_at" gorm:"not null"`
	UpdatedAt int64          `json:"updated_at" gorm:"not null"`
	CreatedBy int            `json:"created_by" gorm:"not null;default:0"`
	UpdatedBy int            `json:"updated_by" gorm:"not null;default:0"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Rule) TableName() string { return "qy_violation_rule" }

// RuleVersion 是单行表,每次规则写操作 +1。
//
// 多节点部署下节点 A 改规则、节点 B 必须感知。轮询这张单行表(一次主键查询)
// 比轮询全表便宜三个数量级,所以快照刷新永远先读它、版本没变就不拉规则。
type RuleVersion struct {
	// autoIncrement:false 是必须的:GORM 默认会把整型主键建成 AUTO_INCREMENT,
	// 而这三张表的主键都是外部指定的业务键(恒为 1 / user_id / record_id),
	// 让数据库替它们自增只会制造误导。
	Id        int   `json:"id" gorm:"primaryKey;autoIncrement:false"` // 恒为 1
	Version   int64 `json:"version" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null"`
}

func (RuleVersion) TableName() string { return "qy_violation_rule_version" }

// Record 是一条违规记录。
//
// RecNo 唯一索引是幂等的唯一保障:defer 在 panic-recover 与未来上游改动下可能重入,
// 重试循环也可能让同一 request_id 多次走到检测点。
type Record struct {
	Id    int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	RecNo string `json:"rec_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_vrec_no"`

	UserId int `json:"user_id" gorm:"not null;index:idx_qy_vrec_user,priority:1"`
	// Username / TokenName 冗余存一份:管理端列表跨库 join 主库是不可行的。
	Username  string `json:"username" gorm:"type:varchar(64);not null;default:''"`
	TokenId   int    `json:"token_id" gorm:"not null;default:0"`
	TokenName string `json:"token_name" gorm:"type:varchar(64);not null;default:''"`

	RuleId   int64  `json:"rule_id" gorm:"not null;index:idx_qy_vrec_rule"`
	RuleName string `json:"rule_name" gorm:"type:varchar(128);not null;default:''"`
	// PublicReason 冻结命中当时的对外文案。规则改名后用户端列表仍应显示原文案,
	// 否则"我当时为什么被扣钱"永远对不上。
	PublicReason string `json:"public_reason" gorm:"type:varchar(128);not null;default:''"`
	Phase        string `json:"phase" gorm:"type:varchar(24);not null"`
	Action       string `json:"action" gorm:"type:varchar(24);not null"`
	// Shadow = true 表示影子模式(全局开关、熔断回落或规则级 dry_run)下的记录:
	// 不扣费、不阻断、不封号、**不计违规次数**,只留这一行 + 证据供管理员核查。
	// fee_quota_want 仍然是算准的("若真实执行会扣多少钱"),那是影子模式的全部价值。
	Shadow  bool `json:"shadow" gorm:"not null;default:false"`
	Blocked bool `json:"blocked" gorm:"not null;default:false"`

	ModelName   string `json:"model_name" gorm:"type:varchar(128);not null;default:''"`
	UsingGroup  string `json:"using_group" gorm:"column:using_group;type:varchar(64);not null;default:''"`
	ChannelId   int    `json:"channel_id" gorm:"not null;default:0"`
	RelayFormat string `json:"relay_format" gorm:"type:varchar(32);not null;default:''"`

	// RequestId 与主库 logs.request_id 对齐,是"新库记录 ↔ 主库计费日志"唯一的对账钥匙。
	RequestId string `json:"request_id" gorm:"type:varchar(64);not null;default:'';index:idx_qy_vrec_reqid"`
	Ip        string `json:"ip" gorm:"type:varchar(64);not null;default:''"`

	// MatchedTerms / MatchSnippet 是仅管理员可见的命中证据,列表页直接看这两列,
	// 不必拉 payload。写入前必须截断:AcSearch 在大词表上可能返回上万个命中。
	MatchedTerms string `json:"matched_terms" gorm:"type:varchar(1024);not null;default:''"`
	MatchSnippet string `json:"match_snippet" gorm:"type:varchar(2048);not null;default:''"`

	FeeMode      string          `json:"fee_mode" gorm:"type:varchar(24);not null;default:'none'"`
	FeeBaseUsd   decimal.Decimal `json:"fee_base_usd" gorm:"type:decimal(18,8);not null;default:0"`
	FeeMultiple  decimal.Decimal `json:"fee_multiple" gorm:"type:decimal(18,6);not null;default:0"`
	GroupRatio   decimal.Decimal `json:"group_ratio" gorm:"type:decimal(18,6);not null;default:0"`
	FeeQuotaWant int64           `json:"fee_quota_want" gorm:"not null;default:0"`
	FeeQuota     int64           `json:"fee_quota" gorm:"not null;default:0"`
	FeeStatus    string          `json:"fee_status" gorm:"type:varchar(24);not null;default:'none'"`
	FeeError     string          `json:"fee_error" gorm:"type:varchar(512);not null;default:''"`

	// BillingSource / SubscriptionId 冻结"这笔罚款当时到底从哪个池扣走"。
	//
	// service.PostConsumeQuota 按 relayInfo.BillingSource 把扣款路由到钱包或订阅池,
	// 并且无条件同步扣减 tokens.remain_quota(TokenId 那一列)。退款如果一律加回钱包:
	// 订阅用户的订阅池消耗永远不会归还、钱包却凭空多出等额额度;令牌额度则永久少掉
	// 这一笔。这两件事都无法事后从主库反推(计费日志里没有路由信息),所以扣费当时
	// 必须把上下文冻结进记录 —— 退款只能退回"当初扣的那个池、那个令牌"。
	//
	// 刻意不冻结 token_key:那是明文 API 密钥,落进扩展库等于把凭证复制一份,
	// 而且 Record 会被管理端列表接口整行返回。退款时按 TokenId 直接改 tokens 行,
	// 缓存则用 InvalidateUserTokensCache 整体失效,不需要密钥原文。
	BillingSource  string `json:"billing_source" gorm:"type:varchar(16);not null;default:''"`
	SubscriptionId int    `json:"subscription_id" gorm:"not null;default:0"`
	// QuotaClamp 落 common.QuotaClamp.AuditMap() 的 JSON。额度饱和几乎必然意味着
	// 规则配错,必须能在管理端单独筛出来。
	QuotaClamp string `json:"quota_clamp" gorm:"type:varchar(512);not null;default:''"`

	// CountWeight 是这次命中**本该**给违规计数加的权重。影子记录也写它
	// (回答"若真实执行会加几"),但影子命中不会推进计数器,见 CounterAfterShadow。
	CountWeight int `json:"count_weight" gorm:"not null;default:0"`
	// Counted 表示这次命中是否真的推进了 qy_violation_counter。
	// 影子记录恒为 false —— 撤销记录时的计数回退因此不会对它做无中生有的减法。
	Counted bool `json:"counted" gorm:"not null;default:false"`
	// CounterAfter 是推进之后的窗口内计数。影子记录取 CounterAfterShadow。
	CounterAfter int `json:"counter_after" gorm:"not null;default:0"`

	Status       string `json:"status" gorm:"type:varchar(16);not null;default:'active';index:idx_qy_vrec_status"`
	RevokedBy    int    `json:"revoked_by" gorm:"not null;default:0"`
	RevokedAt    int64  `json:"revoked_at" gorm:"not null;default:0"`
	RevokeReason string `json:"revoke_reason" gorm:"type:varchar(512);not null;default:''"`
	RefundQuota  int64  `json:"refund_quota" gorm:"not null;default:0"`

	HasPayload bool  `json:"has_payload" gorm:"not null;default:false"`
	CreatedAt  int64 `json:"created_at" gorm:"not null;index:idx_qy_vrec_user,priority:2;index:idx_qy_vrec_created"`
}

func (Record) TableName() string { return "qy_violation_record" }

// Payload 是违规上下文归档,与 Record 1:0..1 共主键。
//
// 为什么必须拆表:主表行约 1KB,payload 行可达数百 KB。放一起会让"按用户查最近
// 20 条违规"这种列表查询被 InnoDB 溢出页 IO 拖垮,保留期清理也必须动主表。
type Payload struct {
	RecordId int64 `json:"record_id" gorm:"primaryKey;autoIncrement:false"`

	Codec string `json:"codec" gorm:"type:varchar(16);not null;default:'gzip'"`
	// OriginBytes 是剥离 base64 之前的原始体积,RawBytes 是入压缩前的体积。
	// 两者的差值就是"我们替用户省下的存储",也是管理员判断证据是否够用的依据。
	OriginBytes int64 `json:"origin_bytes" gorm:"not null;default:0"`
	RawBytes    int64 `json:"raw_bytes" gorm:"not null;default:0"`
	StoredBytes int64 `json:"stored_bytes" gorm:"not null;default:0"`
	Truncated   bool  `json:"truncated" gorm:"not null;default:false"`

	Body []byte `json:"-" gorm:"type:mediumblob"`

	Redacted    bool   `json:"redacted" gorm:"not null;default:false"`
	RedactStats string `json:"redact_stats" gorm:"type:varchar(512);not null;default:''"`
	// FilesSummary 是多模态描述符 JSON 数组。绝不含二进制:一条含 10 张 1MB 图片的
	// base64 请求归档下来就是 10MB/条,1000 条/天即 300GB/月,不可接受。
	FilesSummary string `json:"files_summary" gorm:"type:text"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_vpay_created"`
}

func (Payload) TableName() string { return "qy_violation_payload" }

// Counter 是用户维度的滚动窗口违规计数,也是自动封号判据的唯一数据源。
//
// 单行 upsert(窗口过期判断与重置都在同一条语句里)+ 同事务回读,是"多节点并发
// 把计数推过阈值"唯一的正确解法。刻意不用 LAST_INSERT_ID():它是会话级变量,
// GORM 连接池会把 Exec 与 Raw 发到不同连接上 —— 详见 bumpCounter 的说明。
//
// **只有真实命中会写这张表。** 影子命中一律不推进(裁决 2),否则影子模式会把
// 用户推过封号线,而这条线上的下一次真实命中就会立刻落封禁行。
type Counter struct {
	UserId      int   `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	WindowStart int64 `json:"window_start" gorm:"not null;default:0"`
	HitCount    int   `json:"hit_count" gorm:"not null;default:0"`
	TotalCount  int64 `json:"total_count" gorm:"not null;default:0"`
	// BanCycle 在每次解封时 +1。不 +1 则下次达阈值时封禁认领的唯一键会永远冲突,
	// 自动封号从此静默失效 —— 这是本模块最隐蔽的一个失效模式。
	BanCycle  int   `json:"ban_cycle" gorm:"not null;default:0"`
	LastHitAt int64 `json:"last_hit_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Counter) TableName() string { return "qy_violation_counter" }

// Ban 是封禁认领与执行记录。
//
// (user_id, ban_cycle) 唯一 = 分布式互斥的"封号认领锁",一个周期内
// 只可能有一个节点插入成功,从根本上杜绝重复封号。
type Ban struct {
	Id       int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId   int   `json:"user_id" gorm:"not null;uniqueIndex:uk_qy_vban_user_cycle,priority:1"`
	BanCycle int   `json:"ban_cycle" gorm:"not null;uniqueIndex:uk_qy_vban_user_cycle,priority:2"`

	TriggerRecordId int64 `json:"trigger_record_id" gorm:"not null;default:0"`
	HitCountAt      int   `json:"hit_count_at" gorm:"not null;default:0"`
	Threshold       int   `json:"threshold" gorm:"not null;default:0"`

	Status    string `json:"status" gorm:"type:varchar(16);not null;default:'pending';index:idx_qy_vban_status"`
	Attempts  int    `json:"attempts" gorm:"not null;default:0"`
	LastError string `json:"last_error" gorm:"type:varchar(512);not null;default:''"`

	BannedAt   int64  `json:"banned_at" gorm:"not null;default:0"`
	UnbannedAt int64  `json:"unbanned_at" gorm:"not null;default:0"`
	UnbannedBy int    `json:"unbanned_by" gorm:"not null;default:0"`
	UnbanNote  string `json:"unban_note" gorm:"type:varchar(512);not null;default:''"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;index:idx_qy_vban_created"`
}

func (Ban) TableName() string { return "qy_violation_ban" }

// Appeal 是误判申诉。自动扣费 + 自动封号必然产生误判,没有申诉通道时
// 误判会全部变成工单;申诉通过率本身也是"这条规则该不该改"的反馈回路。
type Appeal struct {
	Id       int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId   int   `json:"user_id" gorm:"not null;index:idx_qy_vap_user"`
	RecordId int64 `json:"record_id" gorm:"not null;uniqueIndex:uk_qy_vap_record"`

	Reason string `json:"reason" gorm:"type:varchar(2000);not null;default:''"`

	Status     string `json:"status" gorm:"type:varchar(16);not null;default:'pending';index:idx_qy_vap_status"`
	ReviewerId int    `json:"reviewer_id" gorm:"not null;default:0"`
	ReviewNote string `json:"review_note" gorm:"type:varchar(1000);not null;default:''"`
	ReviewedAt int64  `json:"reviewed_at" gorm:"not null;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_vap_created"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Appeal) TableName() string { return "qy_violation_appeal" }
