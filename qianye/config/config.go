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
	Wallet          Wallet          `yaml:"wallet"`
	LogMetrics      LogMetrics      `yaml:"log_metrics"`
	GroupVisibility GroupVisibility `yaml:"group_visibility"`
	Availability    Availability    `yaml:"availability"`
	Violation       Violation       `yaml:"violation"`
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
	SlowThresholdMs        int    `yaml:"slow_threshold_ms"`
	LogLevel               string `yaml:"log_level"`
	AutoMigrate            *bool  `yaml:"auto_migrate"`
}

// Runtime 运行期行为与降级策略。
type Runtime struct {
	// HotPathFailOpen 决定新库不可用时 relay 热路径的行为。
	// 必须保持 true:置 false 会让扩展成为主业务的单点故障。
	HotPathFailOpen         *bool `yaml:"hot_path_fail_open"`
	HotPathTimeoutMs        int   `yaml:"hot_path_timeout_ms"`
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
	PendingGraceSeconds       int   `yaml:"pending_grace_seconds"`
	MaxProbeAttempts          int   `yaml:"max_probe_attempts"`
	BatchSize                 int   `yaml:"batch_size"`
	ManualReviewAfterSeconds  int   `yaml:"manual_review_after_seconds"`
	OutboxRetentionDays       int   `yaml:"outbox_retention_days"`
}

// Audit 统一审计。资金审计默认永久保留。
type Audit struct {
	Enabled          *bool `yaml:"enabled"`
	RecordIP         *bool `yaml:"record_ip"`
	SnapshotMaxBytes int   `yaml:"snapshot_max_bytes"`
	RetentionDays    int   `yaml:"retention_days"`
}

// Transfer 用户间余额划转。
type Transfer struct {
	Enabled       bool  `yaml:"enabled"`
	MinQuota      int64 `yaml:"min_quota"`
	MaxPerTxQuota int64 `yaml:"max_per_tx_quota"`
	DailyMaxQuota int64 `yaml:"daily_max_quota"`
	DailyMaxCount int   `yaml:"daily_max_count"`
	FeeBps        int   `yaml:"fee_bps"`
	FeeMinQuota   int64 `yaml:"fee_min_quota"`
	CooldownSecs  int   `yaml:"cooldown_seconds"`
	// RecipientLookup 限定收款人查找方式。刻意不提供用户名模糊搜索:
	// 那等于开放用户枚举,且与"已邀请用户列表要脱敏"的隐私要求自相矛盾。
	RecipientLookup        string `yaml:"recipient_lookup"`
	NewAccountFreezeHours  int    `yaml:"new_account_freeze_hours"`
	RequireReceiverEnabled *bool  `yaml:"require_receiver_enabled"`
}

// Commission 邀请返佣。
//
// 口径默认宽松(全返)。三个排除开关默认关闭,发现套利后可单独打开而无需改代码。
// 违规扣费产生的消费日志(other.violation_fee == true)永不返佣 —— 那属于逻辑
// 错误而非口径偏好,因此不设开关。
type Commission struct {
	Enabled            bool  `yaml:"enabled"`
	TopupRateBps       int   `yaml:"topup_rate_bps"`
	ConsumeRateBps     int   `yaml:"consume_rate_bps"`
	Levels             int   `yaml:"levels"`
	MinSettleQuota     int64 `yaml:"min_settle_quota"`
	MaxPerOrderQuota   int64 `yaml:"max_per_order_quota"`
	HoldingDays        int   `yaml:"holding_days"`
	SettleIntervalSecs int   `yaml:"settle_interval_seconds"`
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
	DailyMaxCount       int      `yaml:"daily_max_count"`
	PayeeAccountMax     int      `yaml:"payee_account_max"`
	ReviewSLAHours      int      `yaml:"review_sla_hours"`
	RemarkMaxRunes      int      `yaml:"remark_max_runes"`
	// PIIKey 是收款信息的 AES-GCM 密钥(base64,32 字节)。为空则禁止 fiat 方式:
	// 收款信息属 PII,明文落库不可接受。
	PIIKey        string `yaml:"pii_key"`
	PIIKeyVersion int    `yaml:"pii_key_version"`
	// DigestKey 独立于 PIIKey 且不轮换,用于跨账户风控索引。
	// 与加密密钥分离,否则轮换后历史 digest 全部失效。
	DigestKey string `yaml:"digest_key"`
}

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
type GroupVisibility struct {
	Enabled           *bool `yaml:"enabled"`
	FilterPricing     *bool `yaml:"filter_pricing"`
	FilterPerfMetrics *bool `yaml:"filter_perf_metrics"`
	FilterGroupAPI    *bool `yaml:"filter_group_api"`
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
// ShadowMode 是安全阀而非可选项:一条 `.*` 正则能在 30 秒内封掉全站用户。
// 上线必须先跑影子模式观察命中分布,确认误判率后再切真实模式。
type Violation struct {
	Enabled           bool  `yaml:"enabled"`
	ShadowMode        *bool `yaml:"shadow_mode"`
	PrecheckEnabled   bool  `yaml:"precheck_enabled"`
	PostChargeEnabled bool  `yaml:"post_charge_enabled"`
	// FeeMultiplier 为 decimal 字符串(如 "1.0"),避免浮点误差进入计费。
	FeeMultiplier  string `yaml:"fee_multiplier"`
	FixedFeeAmount string `yaml:"fixed_fee_amount"`
	MaxFeeQuota    int64  `yaml:"max_fee_quota"`
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

func (r Runtime) FailOpen() bool               { return boolOr(r.HotPathFailOpen, true) }
func (r Runtime) BackgroundOn() bool           { return boolOr(r.BackgroundEnabled, true) }
func (t TwoPhase) OutboxEnabled() bool         { return boolOr(t.MainOutboxEnabled, true) }
func (a Audit) On() bool                       { return boolOr(a.Enabled, true) }
func (a Audit) ShouldRecordIP() bool           { return boolOr(a.RecordIP, true) }
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
func (g GroupVisibility) GroupAPIOn() bool     { return boolOr(g.FilterGroupAPI, true) }
func (g GroupVisibility) KeepAutoGroup() bool  { return boolOr(g.IncludeAutoGroup, true) }
func (v Violation) IsShadow() bool             { return boolOr(v.ShadowMode, true) }

// HasWithdrawMethod 判断某种提现方式是否启用。
func (w Withdraw) HasWithdrawMethod(m string) bool {
	for _, v := range w.Methods {
		if v == m {
			return true
		}
	}
	return false
}

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
