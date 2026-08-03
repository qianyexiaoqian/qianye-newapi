# 地基:配置 / 独立库 / 包结构 / 跨库范式

# 模块 0 — 地基（qianye/ 扩展骨架）设计

> 本章定义所有后续模块（划转 / 返佣 / 提现 / 钱包 UI / 日志两列 / 分组与可用率 / 违规检测）共用的地基。后续模块**不得**自建配置加载、DB 连接、单号生成、租约、审计、降级判断。

---

## 0. 包结构与依赖方向（硬性）

```
qianye/
  config/          leaf。只依赖 common + yaml.v3
    config.go        Config 全字段 + Load/Get/Enabled/Reload
    path.go          路径解析
    defaults.go      applyDefaults()
    validate.go      validate()
    qianye.example.yaml
  db/              依赖 config。自建 *gorm.DB + 熔断 + 迁移 + 租约底座
    db.go health.go migrate.go locking.go
  model/           依赖 db。新库 GORM 模型（全部 qy_ 前缀表）
  guard/           依赖 config + db。降级判断（leaf-ish，供所有调用点 1 行使用）
  service/
    twophase/      跨库两阶段通用抽象
    lease/         新库租约锁
    audit/         统一审计写入
    ...            各业务模块 service
  controller/      HTTP handler
  router.go        RegisterRoutes(*gin.Engine)
  bootstrap.go     Init() / StartBackgroundTasks() / Close()
```

**依赖方向铁律（违反即编译期循环依赖）：**

| 允许 | 禁止 |
|---|---|
| `qianye/*` → `model` / `service` / `common` | `model` → `qianye/*` |
| `controller`（上游）→ `qianye`（仅 main.go 需要 import） | `service` → `qianye/*` |
| — | `pkg/perf_metrics` → `qianye/*` |

因此**上游低层包（`model`/`service`/`pkg/perf_metrics`）里的所有 hook 一律用「hook 函数变量注入」范式**（项目已有先例：`main.go:138-144` 的 `service.GetTaskAdaptorFunc`）：

- hook 变量声明在**新增文件** `model/qy_export.go` / `service/qy_export.go` / `pkg/perf_metrics/qy_export.go`（纯新增，0 行修改）；
- 上游既有文件里只插入 1 行 `if QyXxx != nil { QyXxx(...) }`；
- **因为变量与调用点同包，上游文件的 import 块 0 改动** —— 这是把总预算压进 40 行的关键。

---

## 1. `qianye/config` 完整设计

### 1.1 路径解析（`path.go`）

```go
// 优先级：显式 env → CWD → CWD/data
// Docker 下 WORKDIR=/data，故 ./qianye.yaml 即宿主 ./data/qianye.yaml（.gitignore:29 已覆盖 /data/）
func resolvePath() (string, bool) {
	if p := common.GetEnvOrDefaultString("QIANYE_CONFIG", ""); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
		// 显式指定却不存在 = 运维事故，必须报错而不是静默禁用
		return p, false
	}
	for _, p := range []string{"./qianye.yaml", "./data/qianye.yaml"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}
```

语义区分（重要）：

| 情形 | 行为 |
|---|---|
| 三个路径都不存在 | `Load()` 返回 `nil`，`Enabled()=false`，主程序照常启动（优雅降级，与上游默认行为一致） |
| `QIANYE_CONFIG` 显式指定但文件不存在 | `Load()` 返回 error → `main.go` 走 `FatalLog`（配置写错了必须炸，不能静默跑成没功能） |
| 文件存在但 YAML 语法错 / 校验失败 | 返回 error → FatalLog |
| 文件存在且 `enabled: false` | `Enabled()=false`，不连库、不注册路由、不起后台任务 |

### 1.2 Config 全字段

```go
package config

type Config struct {
	Enabled         bool            `yaml:"enabled"`
	Database        Database        `yaml:"database"`
	Runtime         Runtime         `yaml:"runtime"`
	TwoPhase        TwoPhase        `yaml:"two_phase"`
	Audit           Audit           `yaml:"audit"`
	Transfer        Transfer        `yaml:"transfer"`
	Commission      Commission      `yaml:"commission"`
	Withdraw        Withdraw        `yaml:"withdraw"`
	Wallet          Wallet          `yaml:"wallet"`
	UsageLog        UsageLog        `yaml:"usage_log"`
	GroupVisibility GroupVisibility `yaml:"group_visibility"`
	Availability    Availability    `yaml:"availability"`
	Violation       Violation       `yaml:"violation"`
}

type Database struct {
	DSN                    string `yaml:"dsn"`                        // 必填。MySQL only
	TablePrefix            string `yaml:"table_prefix"`               // 默认 qy_ ；允许与主库同 schema 共存
	MaxIdleConns           int    `yaml:"max_idle_conns"`             // 20
	MaxOpenConns           int    `yaml:"max_open_conns"`             // 100
	ConnMaxLifetimeSeconds int    `yaml:"conn_max_lifetime_seconds"`  // 600
	ConnMaxIdleTimeSeconds int    `yaml:"conn_max_idle_time_seconds"` // 120
	ConnectTimeoutSeconds  int    `yaml:"connect_timeout_seconds"`    // 5，启动期 Ping 超时
	SlowThresholdMs        int    `yaml:"slow_threshold_ms"`          // 200（对齐上游 defaultSlowThresholdMs）
	LogLevel               string `yaml:"log_level"`                  // silent|error|warn|info，默认 warn
	AutoMigrate            bool   `yaml:"auto_migrate"`               // true
}

type Runtime struct {
	HotPathFailOpen         bool `yaml:"hot_path_fail_open"`          // true。硬性约束，置 false 需二次确认
	HotPathTimeoutMs        int  `yaml:"hot_path_timeout_ms"`         // 200
	ColdPathTimeoutMs       int  `yaml:"cold_path_timeout_ms"`        // 3000
	HealthIntervalSeconds   int  `yaml:"health_interval_seconds"`     // 15
	BreakerFailureThreshold int  `yaml:"breaker_failure_threshold"`   // 5
	BreakerOpenSeconds      int  `yaml:"breaker_open_seconds"`        // 30
	BackgroundEnabled       bool `yaml:"background_enabled"`          // true
	LeaseTTLSeconds         int  `yaml:"lease_ttl_seconds"`           // 60
	LeaseRenewSeconds       int  `yaml:"lease_renew_seconds"`         // 20（必须 < TTL/2）
	ConfigReloadSeconds     int  `yaml:"config_reload_seconds"`       // 0=关闭；>0 时轮询 mtime 热载「安全段」
	HotHookQueueSize        int  `yaml:"hot_hook_queue_size"`         // 4096，热路径 hook 的异步队列
	HotHookWorkers          int  `yaml:"hot_hook_workers"`            // 2
}

type TwoPhase struct {
	MainOutboxEnabled         bool `yaml:"main_outbox_enabled"`          // true（关闭=放弃精确对账，见 §4.6）
	CompensateIntervalSeconds int  `yaml:"compensate_interval_seconds"`  // 30
	PendingGraceSeconds       int  `yaml:"pending_grace_seconds"`        // 60，pending 多久后才允许被补偿任务触碰
	MaxProbeAttempts          int  `yaml:"max_probe_attempts"`           // 10
	BatchSize                 int  `yaml:"batch_size"`                   // 200
	ManualReviewAfterSeconds  int  `yaml:"manual_review_after_seconds"`  // 900，超时转人工并告警
	OutboxRetentionDays       int  `yaml:"outbox_retention_days"`        // 30，仅清理已终态单
}

type Audit struct {
	Enabled          bool `yaml:"enabled"`            // true
	RecordIP         bool `yaml:"record_ip"`          // true
	SnapshotMaxBytes int  `yaml:"snapshot_max_bytes"` // 4096，before/after 快照截断上限
	RetentionDays    int  `yaml:"retention_days"`     // 0 = 永久保留（资金审计默认不删）
}

type Transfer struct {
	Enabled                bool    `yaml:"enabled"`
	MinQuota               int64   `yaml:"min_quota"`                 // 500000 = $1
	MaxPerTxQuota          int64   `yaml:"max_per_tx_quota"`          // 50000000 = $100
	DailyMaxQuota          int64   `yaml:"daily_max_quota"`           // 200000000
	DailyMaxCount          int     `yaml:"daily_max_count"`           // 20
	FeePercent             float64 `yaml:"fee_percent"`               // 0
	FeeMinQuota            int64   `yaml:"fee_min_quota"`             // 0
	CooldownSeconds        int     `yaml:"cooldown_seconds"`          // 10
	RecipientLookup        string  `yaml:"recipient_lookup"`          // "id" | "id_or_email"；禁止用户名模糊搜索（防枚举）
	NewAccountFreezeHours  int     `yaml:"new_account_freeze_hours"`  // 24，新号不可转出
	RequireReceiverEnabled bool    `yaml:"require_receiver_enabled"`  // true
}

type Commission struct {
	Enabled              bool    `yaml:"enabled"`
	TopupRatePercent     float64 `yaml:"topup_rate_percent"`    // 10
	ConsumeRatePercent   float64 `yaml:"consume_rate_percent"`  // 5
	Levels               int     `yaml:"levels"`                // 1（当前只支持一级）
	MinSettleQuota       int64   `yaml:"min_settle_quota"`      // 1000，累计余数达到才结算成整数 quota
	MaxPerOrderQuota     int64   `yaml:"max_per_order_quota"`   // 单笔返佣上限，防异常大额
	SettleIntervalSecond int     `yaml:"settle_interval_seconds"` // 300
	InviterCacheSeconds  int     `yaml:"inviter_cache_seconds"` // 300，消费返佣热路径查 inviter_id 的缓存
	TopupScanIntervalSec int     `yaml:"topup_scan_interval_seconds"` // 60
	TopupScanLookbackHrs int     `yaml:"topup_scan_lookback_hours"`   // 72，低水位 + 回扫窗口

	// —— D4 三个风控开关，默认全部宽松（false = 不排除 / 不冲正）——
	ExcludeRedemptionAndManual bool `yaml:"exclude_redemption_and_manual"` // false
	ExcludeSubscriptionConsume bool `yaml:"exclude_subscription_consume"`  // false
	RefundClawback             bool `yaml:"refund_clawback"`               // false
	// 无条件硬排除（无开关）：other.violation_fee == true 的消费日志永不返佣
}

type Withdraw struct {
	Enabled            bool     `yaml:"enabled"`
	Methods            []string `yaml:"methods"`               // ["balance","fiat"]
	MinBalanceQuota    int64    `yaml:"min_balance_quota"`     // 500000
	MinFiatAmount      float64  `yaml:"min_fiat_amount"`       // 100.00
	FiatCurrency       string   `yaml:"fiat_currency"`         // "CNY"
	FiatFeePercent     float64  `yaml:"fiat_fee_percent"`      // 0
	RateFreezeSource   string   `yaml:"rate_freeze_source"`    // "operation_setting"|"fixed"
	RateFreezeFixed    float64  `yaml:"rate_freeze_fixed"`     // rate_freeze_source=fixed 时使用
	AutoCreditOnApprove bool    `yaml:"auto_credit_on_approve"`// true：站内额度兑换审核通过后自动到账
	DailyMaxCount      int      `yaml:"daily_max_count"`       // 3
	PayoutAccountMax   int      `yaml:"payout_account_max"`    // 3，每人最多保存几个收款方式
	PayoutAccountAESKey string  `yaml:"payout_account_aes_key"`// 32 字节 hex；为空则收款信息不落库（只允许站内兑换）
	ReviewSLAHours     int      `yaml:"review_sla_hours"`      // 72，超时在管理端标红
}

type Wallet struct {
	ShowTransferEntry   bool `yaml:"show_transfer_entry"`
	ShowCommissionEntry bool `yaml:"show_commission_entry"`
	ShowWithdrawEntry   bool `yaml:"show_withdraw_entry"`
	TabsKeepMounted     bool `yaml:"tabs_keep_mounted"` // true，规避 Base UI Tabs.Panel 默认卸载
}

type UsageLog struct {
	ShowReasoningEffort   bool   `yaml:"show_reasoning_effort"`
	ShowCacheRatio        bool   `yaml:"show_cache_ratio"`
	CacheSemanticFallback string `yaml:"cache_semantic_fallback"` // auto|openai|claude，老日志无 usage_semantic 时的口径
	EnableFilter          bool   `yaml:"enable_filter"`           // false：开启需旁路表，成本高
}

type GroupVisibility struct {
	Enabled           bool `yaml:"enabled"`            // true
	FilterPricing     bool `yaml:"filter_pricing"`     // true
	FilterPerfMetrics bool `yaml:"filter_perf_metrics"`// true
	FilterGroupAPI    bool `yaml:"filter_group_api"`   // true
	IncludeAutoGroup  bool `yaml:"include_auto_group"` // true，"auto" 永远保留
}

type Availability struct {
	Enabled              bool `yaml:"enabled"`
	SampleAttemptLevel   bool `yaml:"sample_attempt_level"`   // true=在 processChannelError 加渠道级采样
	BucketSeconds        int  `yaml:"bucket_seconds"`        // 300
	FlushIntervalSeconds int  `yaml:"flush_interval_seconds"`// 60
	RetentionDays        int  `yaml:"retention_days"`        // 30
	MaxSeriesPerQuery    int  `yaml:"max_series_per_query"`  // 200
}

type Violation struct {
	Enabled                   bool    `yaml:"enabled"`
	PrecheckEnabled           bool    `yaml:"precheck_enabled"`             // 转发前拦截（中间件，固定额或纯拦截）
	PostChargeEnabled         bool    `yaml:"post_charge_enabled"`          // 事后按模型价格倍数扣费
	FeeMultiplier             float64 `yaml:"fee_multiplier"`               // 1.0，× 本次请求计费额
	FixedFeeAmount            float64 `yaml:"fixed_fee_amount"`             // 0.05（美元），无 PriceData 时使用
	MaxFeeQuota               int64   `yaml:"max_fee_quota"`                // 单次违规扣费硬上限
	InsufficientBalancePolicy string  `yaml:"insufficient_balance_policy"`  // clamp|negative|ban，默认 clamp
	AutoBanThreshold          int     `yaml:"auto_ban_threshold"`           // 0=不自动封号
	AutoBanWindowHours        int     `yaml:"auto_ban_window_hours"`        // 24
	EvidenceMaxBytes          int     `yaml:"evidence_max_bytes"`           // 8192，证据截断
	EvidenceRetentionDays     int     `yaml:"evidence_retention_days"`      // 90
	RuleCacheSeconds          int     `yaml:"rule_cache_seconds"`           // 60
	ScanTimeoutMs             int     `yaml:"scan_timeout_ms"`              // 20
}
```

### 1.3 加载与访问

```go
var current atomic.Pointer[Config]
var loadedPath atomic.Value // string
var loadedModTime atomic.Int64

func Load() error {
	path, ok := resolvePath()
	if !ok {
		if path != "" { // QIANYE_CONFIG 显式指定却不存在
			return fmt.Errorf("qianye: config file %q not found (QIANYE_CONFIG)", path)
		}
		common.SysLog("qianye: no config file found, extension disabled")
		current.Store(&Config{Enabled: false})
		return nil
	}
	c, err := parseFile(path)
	if err != nil { return err }
	current.Store(c)
	loadedPath.Store(path)
	common.SysLog(fmt.Sprintf("qianye: config loaded from %s (enabled=%v)", path, c.Enabled))
	return nil
}

func parseFile(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil { return nil, err }
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)                      // 严格模式：拼错的 key 直接报错，避免风控开关默默不生效
	if err := dec.Decode(&c); err != nil {
		// 未知字段单独降级为告警重解，兼容「配置比二进制新」的回滚场景
		if !strings.Contains(err.Error(), "field") { return nil, err }
		common.SysError("qianye: config has unknown fields, re-parsing leniently: " + err.Error())
		c = Config{}
		if err2 := yaml.Unmarshal(raw, &c); err2 != nil { return nil, err2 }
	}
	applyDefaults(&c)
	if err := validate(&c); err != nil { return nil, err }
	return &c, nil
}

func Get() *Config { c := current.Load(); if c == nil { return &Config{} }; return c }
func Enabled() bool { return Get().Enabled }
func Path() string { p, _ := loadedPath.Load().(string); return p }
```

`applyDefaults` 逐字段兜底，判据是「**这个键在 YAML 里出现过没有**」，不是「值是不是 0」。

> 这里曾经写的是「0 值 → 上表默认值」，那是个真缺陷：0 在本配置里遍地都是有含义的取值（冷却期 0 = 不限制、成熟期 0 = 当天结算、上限 0 = 不设限），业务代码里那些 `if cfg.X > 0` 的守卫就是证据，而旧判据让 else 分支永远点不亮。实测形态是 `commission.holding_days: 0` 被补成 7，佣金要多等 8 天才结算，而配置文件上仍写着 0。
>
> 现在的做法：解析前用 `markNumbersUnset` 把每个数值字段填成哨兵（`math.MinInt`），解析后仍是哨兵的就是「文件里没这个键」。**新增数值配置项时不要另写 `if x == 0 { x = def }`**，照旧调用 `intDefault` / `int64Default` 即可，判据已经在里面。详见 `qianye/config/defaults.go` 末尾那段说明。

布尔量的默认值问题用「**显式指针 + 反解**」处理不划算，改用约定：**所有 `Enabled` 类布尔的语义都是「false=关」，示例文件里全部写全**；`hot_path_fail_open` / `main_outbox_enabled` / `auto_migrate` 这三个「默认应为 true」的字段用 `*bool` 承载：

```go
HotPathFailOpen   *bool `yaml:"hot_path_fail_open"`   // nil → true
MainOutboxEnabled *bool `yaml:"main_outbox_enabled"`  // nil → true
AutoMigrate       *bool `yaml:"auto_migrate"`         // nil → true
```

`validate()` 至少覆盖：

- `Enabled && Database.DSN == ""` → error
- DSN 必须能被 `mysql.ParseDSN` 解析且不是 `postgres://` / `clickhouse://` / `local` 前缀 → 明确报「本扩展仅支持 MySQL」
- `MaxOpenConns >= MaxIdleConns > 0`
- `LeaseRenewSeconds*2 < LeaseTTLSeconds`
- 所有 `*_percent` ∈ [0,100]，`fee_multiplier` ∈ [0,100]
- `Commission.Levels == 1`（>1 当前不支持，直接报错而不是静默降级）
- `Withdraw.Methods` ⊆ {balance,fiat}；含 `fiat` 时 `PayoutAccountAESKey` 必须为 64 hex 字符
- `Violation.InsufficientBalancePolicy` ∈ {clamp,negative,ban}
- 金额上限类字段 `<= common.MaxQuota`（int32），否则报错

### 1.4 热重载（可选，`config_reload_seconds > 0`）

仿 `model/option.go:199-205` 轮询范式，goroutine 每 N 秒 `os.Stat` 比对 mtime，变化则 `parseFile` → 合成新 Config：**`Database` 段与 `Runtime.LeaseTTLSeconds` 一律沿用旧值**（DSN/连接池不可热切），其余段整体替换 `current`。变更前后写一条 `qy_audit_logs`（category=config）。无 fsnotify 依赖。

### 1.5 `qianye.example.yaml` 完整内容

```yaml
# ============================================================================
# 千夜扩展配置 (qianye extension)
# 放置位置（按优先级）：$QIANYE_CONFIG → ./qianye.yaml → ./data/qianye.yaml
# Docker 下容器 WORKDIR=/data，因此 ./qianye.yaml 即宿主机 ./data/qianye.yaml
# 本文件含数据库密码：/data/ 已在 .gitignore:29 中忽略，不会误提交
# 文件不存在 = 整个扩展静默禁用，主程序行为与上游完全一致
# ============================================================================

enabled: true

# ---------------------------------------------------------------------------
# 独立 MySQL（需求 1）。仅支持 MySQL 5.7.8+ / 8.x
# 表统一带 table_prefix 前缀，因此可以安全地与主库指向同一个 schema
# ---------------------------------------------------------------------------
database:
  dsn: "qy_user:CHANGE_ME@tcp(127.0.0.1:3306)/qianye?charset=utf8mb4&parseTime=true&loc=Local"
  table_prefix: "qy_"
  max_idle_conns: 20
  max_open_conns: 100
  conn_max_lifetime_seconds: 600
  conn_max_idle_time_seconds: 120
  connect_timeout_seconds: 5
  slow_threshold_ms: 200
  log_level: warn          # silent | error | warn | info
  auto_migrate: true       # 仅 master 节点（NODE_TYPE != slave）实际执行

# ---------------------------------------------------------------------------
# 运行时与降级
# ---------------------------------------------------------------------------
runtime:
  hot_path_fail_open: true       # 新库不可用时 relay 热路径一律放行，只记日志。不要改 false
  hot_path_timeout_ms: 200
  cold_path_timeout_ms: 3000
  health_interval_seconds: 15
  breaker_failure_threshold: 5
  breaker_open_seconds: 30
  background_enabled: true
  lease_ttl_seconds: 60
  lease_renew_seconds: 20
  config_reload_seconds: 0       # 0 关闭；>0 时轮询 mtime 热载（database 段永不热载）
  hot_hook_queue_size: 4096
  hot_hook_workers: 2

# ---------------------------------------------------------------------------
# 跨库两阶段（划转 / 佣金兑现 / 提现到账 共用）
# ---------------------------------------------------------------------------
two_phase:
  main_outbox_enabled: true          # 在主库建 qy_fund_outbox 作为唯一精确探针，强烈建议保持 true
  compensate_interval_seconds: 30
  pending_grace_seconds: 60
  max_probe_attempts: 10
  batch_size: 200
  manual_review_after_seconds: 900   # 超时转人工，管理端告警
  outbox_retention_days: 30

audit:
  enabled: true
  record_ip: true
  snapshot_max_bytes: 4096
  retention_days: 0                  # 0 = 永久（资金审计默认不清理）

# ---------------------------------------------------------------------------
# 需求 2：用户间余额划转
# ---------------------------------------------------------------------------
transfer:
  enabled: true
  min_quota: 500000                  # 500000 quota = $1
  max_per_tx_quota: 50000000         # $100
  daily_max_quota: 200000000         # $400
  daily_max_count: 20
  fee_percent: 0
  fee_min_quota: 0
  cooldown_seconds: 10
  recipient_lookup: "id"             # id | id_or_email。不提供用户名模糊搜索，防用户枚举
  new_account_freeze_hours: 24
  require_receiver_enabled: true

# ---------------------------------------------------------------------------
# 需求 3-a：邀请返佣
# 默认口径宽松（全返），三个风控开关默认关闭；上线后如发现套利再逐个打开
# ---------------------------------------------------------------------------
commission:
  enabled: true
  topup_rate_percent: 10
  consume_rate_percent: 5
  levels: 1
  min_settle_quota: 1000             # 佣金按 decimal 累计，累到该值才结算成整数 quota（防小额截断归零）
  max_per_order_quota: 50000000
  settle_interval_seconds: 300
  inviter_cache_seconds: 300         # 消费返佣热路径的 inviter_id 缓存，避免给主库加等量读压
  topup_scan_interval_seconds: 60
  topup_scan_lookback_hours: 72      # top_ups 无 updated_at，只能低水位 + 回扫窗口去重

  exclude_redemption_and_manual: false  # true = 兑换码充值与管理员补单不返佣
  exclude_subscription_consume: false   # true = 订阅额度消费不返佣（只返 wallet_quota_deducted > 0 部分）
  refund_clawback: false                # true = 退款时冲正已发佣金
  # 硬排除（无开关）：other.violation_fee == true 的消费日志永不返佣

# ---------------------------------------------------------------------------
# 需求 3-b：提现（两种形态并存，用户自选）
# ---------------------------------------------------------------------------
withdraw:
  enabled: true
  methods: ["balance", "fiat"]       # balance=佣金兑换平台额度；fiat=线下法币打款
  min_balance_quota: 500000
  min_fiat_amount: 100.00
  fiat_currency: "CNY"
  fiat_fee_percent: 0
  rate_freeze_source: "operation_setting"  # operation_setting=取 USDExchangeRate 并冻结 | fixed
  rate_freeze_fixed: 7.3
  auto_credit_on_approve: true       # 站内额度兑换：审核通过后自动到账
  daily_max_count: 3
  payout_account_max: 3
  payout_account_aes_key: ""         # 32 字节 hex（64 字符）。为空时禁止保存收款信息，methods 不得含 fiat
  review_sla_hours: 72

# ---------------------------------------------------------------------------
# 需求 4：钱包页（直接改原文件，不 fork）
# ---------------------------------------------------------------------------
wallet:
  show_transfer_entry: true
  show_commission_entry: true
  show_withdraw_entry: true
  tabs_keep_mounted: true

# ---------------------------------------------------------------------------
# 需求 5：使用日志新增两列
# ---------------------------------------------------------------------------
usage_log:
  show_reasoning_effort: true
  show_cache_ratio: true
  cache_semantic_fallback: "auto"    # auto | openai | claude（老日志无 usage_semantic）
  enable_filter: false               # 开启筛选/排序需要旁路表，成本显著上升

# ---------------------------------------------------------------------------
# 需求 6-a：修复无权分组泄漏（D2：用 UserUsableGroups 白名单交集裁剪）
# ---------------------------------------------------------------------------
group_visibility:
  enabled: true
  filter_pricing: true
  filter_perf_metrics: true
  filter_group_api: true
  include_auto_group: true

# ---------------------------------------------------------------------------
# 需求 6-b：模型可用率监控
# ---------------------------------------------------------------------------
availability:
  enabled: true
  sample_attempt_level: true         # true 才能拿到渠道级健康度（需在 processChannelError 加采样）
  bucket_seconds: 300
  flush_interval_seconds: 60
  retention_days: 30
  max_series_per_query: 200

# ---------------------------------------------------------------------------
# 需求 7：违规检测与扣费
# ---------------------------------------------------------------------------
violation:
  enabled: true
  precheck_enabled: true             # 转发前拦截（中间件，拿不到模型价格，只能纯拦截/固定额）
  post_charge_enabled: true          # 事后按模型价格倍数扣费（controller/relay.go defer）
  fee_multiplier: 1.0
  fixed_fee_amount: 0.05             # 美元
  max_fee_quota: 5000000             # $10 单次上限
  insufficient_balance_policy: "clamp"   # clamp（扣到 0 为止）| negative（允许负数）| ban
  auto_ban_threshold: 0              # 0 = 不自动封号
  auto_ban_window_hours: 24
  evidence_max_bytes: 8192
  evidence_retention_days: 90
  rule_cache_seconds: 60
  scan_timeout_ms: 20
```

---

## 2. `qianye/db` 完整设计

### 2.1 连接建立

```go
var handle atomic.Pointer[gorm.DB]

func Init(cfg config.Database) error {
	dsn := normalizeDSN(cfg.DSN) // 照抄 model/main.go:152-163：无 parseTime 则补
	gcfg := &gorm.Config{
		PrepareStmt: true,
		Logger: gormlogger.New(log.New(os.Stdout, "[QY-DB] ", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             time.Duration(cfg.SlowThresholdMs) * time.Millisecond,
			LogLevel:                  parseLogLevel(cfg.LogLevel),
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
		NamingStrategy: schema.NamingStrategy{TablePrefix: cfg.TablePrefix}, // 所有表带 qy_ 前缀
		SkipDefaultTransaction: true,
	}
	db, err := gorm.Open(mysql.Open(dsn), gcfg)
	if err != nil { return err }
	sqlDB, err := db.DB()
	if err != nil { return err }
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSeconds) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(cfg.ConnMaxIdleTimeSeconds) * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil { return err }

	handle.Store(db)
	markHealthy()
	return nil
}
```

- **绝不调用** `common.SetMainDatabaseType` / `SetLogDatabaseType`。
- **不复用** `SQL_MAX_IDLE_CONNS` 等主库 env。
- `normalizeDSN` 额外做一件事：若 DSN 缺 `charset`，追加 `charset=utf8mb4`（新库是我们自建的，直接要求 utf8mb4，不复制上游 `checkMySQLChineseSupport` 那 90 行）。

### 2.2 迁移（gate `IsMasterNode` + 跨节点 DDL 互斥）

```go
func Migrate(models ...any) error {
	if !common.IsMasterNode { common.SysLog("qy: slave node, skip automigrate"); return nil }
	if !config.Get().Database.autoMigrate() { return nil }
	db := Get()
	sqlDB, _ := db.DB()
	conn, err := sqlDB.Conn(context.Background()) // GET_LOCK 是连接级的，必须固定同一条连接
	if err != nil { return err }
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", "qy_schema_migrate", 30).Scan(&got); err != nil { return err }
	if !got.Valid || got.Int64 != 1 {
		common.SysLog("qy: another node holds the migrate lock, skip automigrate")
		return nil                                  // 不是错误：别的 master 正在迁移
	}
	defer conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", "qy_schema_migrate")
	return db.AutoMigrate(models...)
}
```

> `common.IsMasterNode` 只是 `NODE_TYPE != "slave"` 的环境变量，多个节点都可能是 master —— 所以 gate 之外**必须**再加 `GET_LOCK`，否则并发 `AutoMigrate` 会在 MySQL 上互相锁表甚至死锁（GAPS §3.2(7) 的 DDL 版本）。

模型清单由 `qianye/model.AllTables()` 提供，`bootstrap.go` 传入 —— 保持 `db` 包是 leaf。

### 2.3 健康探针 + 熔断（`Available()`）

```go
var (
	available   atomic.Bool
	failStreak  atomic.Int32
	openUntil   atomic.Int64 // unix 秒
)

func Available() bool {
	if handle.Load() == nil { return false }
	if time.Now().Unix() < openUntil.Load() { return false } // 熔断打开
	return available.Load()
}

// MarkFailure 由所有新库读写调用点在拿到 error 时调用（一行 defer 或 wrap）
func MarkFailure(err error) {
	if err == nil || errors.Is(err, gorm.ErrRecordNotFound) { return }
	if !isConnLevelError(err) { return }   // 业务错误（唯一键冲突等）不计入熔断
	if failStreak.Add(1) >= int32(config.Get().Runtime.BreakerFailureThreshold) {
		openUntil.Store(time.Now().Unix() + int64(config.Get().Runtime.BreakerOpenSeconds))
		available.Store(false)
		common.SysError("qy: db breaker OPEN: " + err.Error())
	}
}
func MarkSuccess() { failStreak.Store(0) }

// 后台健康循环：每 health_interval_seconds 做一次 PingContext(3s)，成功则半开→闭合
func startHealthLoop() { ... }
```

`isConnLevelError`：`driver.ErrBadConn` / `mysql.ErrInvalidConn` / `net.Error` / `context.DeadlineExceeded` / MySQL 1040(too many connections)、1053、2002、2006、2013。

### 2.4 行锁与关闭

```go
// 新库固定 MySQL，无需 SQLite 分支；不要复用 model.lockForUpdate（它判的是主库类型）
func LockForUpdate(tx *gorm.DB) *gorm.DB { return tx.Clauses(clause.Locking{Strength: "UPDATE"}) }

func Close() error {
	db := handle.Load()
	if db == nil { return nil }
	sqlDB, err := db.DB(); if err != nil { return err }
	return sqlDB.Close()
}
```

`Close()` **不**接进 `main.go:71-76` 的 defer（省 1 行预算）；进程退出由 OS 回收，租约按 TTL 自然过期。

---

## 3. 降级语义统一实现：`qianye/guard`

```go
package guard

// Enabled 配置存在且 enabled: true
func Enabled() bool { return config.Enabled() }

// Available 扩展启用 + 新库可用（含熔断状态）
func Available() bool { return Enabled() && db.Available() }

// Feature 单个功能开关 ∧ Available
func Feature(f Flag) bool { ... }   // Flag: FlagTransfer/FlagCommission/FlagWithdraw/FlagViolation/...

// ---------------- 热路径（relay / 消费日志 / 采样）：fail-open ----------------

// Hot 是所有热路径 hook 的唯一入口。语义：
//   1) 扩展禁用 / 新库不可用 / 熔断打开 → 直接返回，什么都不做（放行）
//   2) 内部 panic 一律吞掉并记日志，绝不冒泡到 relay
//   3) fn 在 hot_path_timeout_ms 的 ctx 下执行，超时即放弃
//   4) 错误只写日志（按 name 做 1/分钟 的限频，防日志刷屏），永不返回给调用方
func Hot(name string, fn func(ctx context.Context) error)

// HotAsync 把工作丢进有界队列由 worker 消费；队列满直接丢弃并计数。
// 消费返佣、可用率采样这类「可容忍少量丢失」的 hook 必须用它，禁止在 relay 线程内同步查主库。
func HotAsync(name string, job func(ctx context.Context) error)

// ---------------- 非热路径（划转 / 佣金 / 提现 HTTP handler）：fail-closed ----------------

// RequireAPI 一行判断。不可用时写 503 并 Abort，返回 false。
func RequireAPI(c *gin.Context, f Flag) bool {
	if !Enabled() {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"success": false, "code": "qy_disabled",
			"message": common.TranslateMessage(c, "qy.disabled")})
		return false
	}
	if !Feature(f) {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"success": false, "code": "qy_feature_off", ...})
		return false
	}
	if !db.Available() {
		c.Header("Retry-After", "30")
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "qy_unavailable",
			"message": common.TranslateMessage(c, "qy.unavailable")})
		return false
	}
	return true
}
```

调用点长这样（每处 1 行）：

```go
// 热路径（model/log.go 里注入的 hook 实现内部）
guard.HotAsync("commission.consume", func(ctx context.Context) error { ... })

// 非热路径（qianye/controller 内）
func CreateTransfer(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) { return }
	...
}
```

> 前端侧：`web/src/lib/http-client.ts:103` 的错误拦截器已经读 `error.response.data.message`，因此 503 + `{success:false,message}` 的响应体能被现有 UI 正确展示，无需改动 http-client。

---

## 4. 跨库两阶段通用抽象 `qianye/service/twophase`

### 4.1 表：`qy_fund_orders`（新库，两阶段状态机）

```go
package model // qianye/model

type FundOrder struct {
	Id           int64  `gorm:"primaryKey;autoIncrement"`
	OrderNo      string `gorm:"type:varchar(64);not null;uniqueIndex:uk_fund_order_no"`
	Kind         string `gorm:"type:varchar(32);not null;index:idx_fund_kind_status,priority:1"`
	Status       int8   `gorm:"not null;default:0;index:idx_fund_kind_status,priority:2"`

	IdemScope    string `gorm:"type:varchar(32);not null;uniqueIndex:uk_fund_idem,priority:1"`
	IdemKey      string `gorm:"type:varchar(96);not null;uniqueIndex:uk_fund_idem,priority:2"`

	UserId       int    `gorm:"not null;index:idx_fund_user_created,priority:1"`
	PeerUserId   int    `gorm:"not null;default:0;index"`
	AmountQuota  int64  `gorm:"not null"`
	FeeQuota     int64  `gorm:"not null;default:0"`

	RefType      string `gorm:"type:varchar(32);not null;default:''"`
	RefId        string `gorm:"type:varchar(64);not null;default:'';index:idx_fund_ref,priority:2"`

	Attempts     int    `gorm:"not null;default:0"`
	NextProbeAt  int64  `gorm:"not null;default:0;index:idx_fund_probe"`
	LastError    string `gorm:"type:varchar(512);not null;default:''"`
	NodeName     string `gorm:"type:varchar(128);not null;default:''"`

	CreatedAt    int64  `gorm:"not null;index:idx_fund_user_created,priority:2"`
	UpdatedAt    int64  `gorm:"not null;index:idx_fund_status_updated,priority:2"`
	SettledAt    int64  `gorm:"not null;default:0"`
}
```

字段存在理由：

| 字段 | 为什么必须有 |
|---|---|
| `OrderNo` + 唯一索引 | 跨库唯一关联键；写进主库 outbox 与 `logs.request_id`，是对账的锚点 |
| `Kind` | 一张表承载划转/佣金兑现/提现到账/佣金冲正，补偿任务只需一个 |
| `Status` | 两阶段状态机；与 `Kind` 组合索引供补偿任务扫描 |
| `IdemScope`+`IdemKey` 唯一索引 | 幂等的**唯一**保证。重复提交/重复扫描直接命中唯一冲突，返回已有单 |
| `UserId`/`PeerUserId` | 资金主体与对手方（划转）；用户侧列表查询走 `idx_fund_user_created` |
| `AmountQuota` `int64` | 主库 quota 是 int32，但新库用 int64 承载聚合与中间量；跨库前必须 clamp 校验 |
| `FeeQuota` | 划转手续费；即使当前配置为 0 也要落库，否则改配置后历史单不可解释 |
| `RefType`/`RefId` | 溯源（`topup:trade_no` / `log:id` / `withdraw:id`），也是佣金冲正找原单的依据 |
| `Attempts`/`NextProbeAt` | 补偿任务的指数退避，防止一条坏单打爆主库 |
| `LastError` | 无此字段则中间态无法排障；截断 512 防止大错误串撑爆行 |
| `NodeName` | 多节点下定位是哪台机器留下的 pending |
| `SettledAt` | 与 `UpdatedAt` 分开，便于财务按结算时间对账 |

状态常量：

```go
const (
	StatusPending   int8 = 0 // 新库已落单，主库尚未确认
	StatusSuccess   int8 = 2 // 主库已生效且新库已回写
	StatusFailed    int8 = 3 // 主库明确未生效（业务校验失败/事务回滚）
	StatusUncertain int8 = 4 // 探针无法判定，转人工
	StatusReversed  int8 = 5 // 已被冲正（退款回收佣金等）
)
```

### 4.2 主库探针表：`qy_fund_outbox`（主库，唯一精确探针）

定义在 `model/qy_export.go`（见 §6）。它是**解决 GAPS §3.2(1)「中间态无人对账」的唯一手段**：

- `model.RecordLog` 走的是 `LOG_DB`（`model/log.go:103` `LOG_DB.Create`），**无法加入主库事务** —— 把单号写进 content/other 只解决「运营能看见」，不解决「进程在 commit 与写日志之间崩溃」。
- outbox 行与 `users.quota` 的 UPDATE **在同一个主库事务里**，要么都在要么都不在，探针 100% 精确。
- 附带收益：`order_no` 唯一索引让**主库侧操作本身幂等** —— 补偿任务重跑主库事务时，`INSERT ... ON DUPLICATE KEY DO NOTHING` 返回 0 行即代表「已应用过」，直接跳过扣加款。这是安全重试的前提。
- 代价：主库多一张 6 列小表，纯新增文件创建，上游合并冲突为 0。可用 `two_phase.main_outbox_enabled: false` 关闭，届时降级为「按 `logs.request_id = order_no` 探针」（有索引 `idx_logs_request_id`，但存在 commit 成功 / 日志未写的窗口，pending 只能转人工）。

### 4.3 单号生成（不使用 math/rand）

```go
var seq atomic.Uint64

// 形如：TR20260730T091533-3f2a-9c1e4b7d20  (共 31 字符，varchar(64) 足够)
func NewOrderNo(kind string) string {
	code := kindCode(kind)                         // 2 字符：TR/CM/WD/RV
	ts := time.Now().UTC().Format("20060102T150405")
	sq := strconv.FormatUint(seq.Add(1)%1679616, 36) // 进程内单调，抗同秒碰撞
	rnd := common.GetUUID()[:10]                   // google/uuid v4 → crypto/rand，非 math/rand
	return fmt.Sprintf("%s%s-%s-%s", code, ts, sq, rnd)
}
```

碰撞由 `uk_fund_order_no` 唯一索引兜底：`Create` 冲突则重生成重试一次，二次冲突返回错误。**不使用** `math/rand`、`common.GetRandomString`（内部是 `math/rand`）、时间戳自增等可预测方案。

### 4.4 幂等键约定（各模块必须遵守）

| Kind | IdemScope | IdemKey | 说明 |
|---|---|---|---|
| 划转 | `transfer` | `<fromUserId>:<客户端 X-Qy-Idempotency-Key>` | 前端每次打开表单生成一个 UUID，重复提交自动归并 |
| 充值返佣 | `commission_topup` | `topup:<trade_no>` | **天然幂等键**。GAPS §3.2(6) 的 O(N) 回扫因此安全 |
| 消费返佣 | `commission_consume` | `log:<logId>` 或 `agg:<userId>:<bucketTs>` | 按笔或按聚合窗口 |
| 佣金冲正 | `commission_reverse` | `reverse:<原 orderNo>` | 一笔佣金只能被冲正一次 |
| 佣金→额度兑现 | `withdraw_balance` | `withdraw:<withdrawId>` | 审核通过只到账一次 |
| 法币提现扣减 | `withdraw_fiat` | `withdraw:<withdrawId>` | 同上 |

### 4.5 关键流程（编号步骤，标出事务边界 / 加锁 / 幂等 / 回滚）

```go
type Request struct {
	Kind, IdemScope, IdemKey string
	UserId, PeerUserId       int
	AmountQuota, FeeQuota    int64
	RefType, RefId           string
	// MainApply 在主库事务内执行业务副作用；claimed=false 表示本单主库侧此前已生效，必须直接 return nil
	MainApply  func(tx *gorm.DB, claimed bool) error
	// LocalCommit 在新库回写 success 的同一事务内执行业务副作用（如扣减佣金余额）
	LocalCommit func(tx *gorm.DB, order *model.FundOrder) error
	// AfterCommit 主库提交后执行：缓存同步 + RecordLog（不可失败，失败只记日志）
	AfterCommit func(order *model.FundOrder)
}

func Execute(ctx context.Context, req Request) (*model.FundOrder, error)
```

**流程：**

1. **【新库事务 A 开始】** `db.Get().Transaction`：
   1.1 `INSERT qy_fund_orders(status=Pending, order_no=NewOrderNo(kind), idem_*)`。
   1.2 唯一键 `uk_fund_idem` 冲突 → 回滚，改为 `SELECT` 已有单：
   - `Status=Success` → 直接返回该单（幂等命中，不再执行任何副作用）；
   - `Status=Failed` → 返回原失败原因；
   - `Status=Pending` 且 `now-CreatedAt < pending_grace` → 返回 409「处理中，请稍候」；
   - `Status=Pending` 且已超时 → 继续走第 2 步（重试是安全的，因为 outbox 幂等）。
   1.3 业务明细行（`qy_transfer_details` / `qy_commission_records` …）在**同一事务**内插入，外键为 `order_no`。
   **【新库事务 A 提交】** —— 此刻新库已有 pending，主库尚未动。

2. **【主库事务 B 开始】** `model.DB.Transaction`：
   2.1 `claimed, err := model.QyClaimFundOutbox(tx, &model.QyFundOutbox{OrderNo, Kind, UserId, PeerId, Amount})`
   —— 冲突（`claimed=false`）说明主库侧此前已生效，**跳过所有资金变更**，直接 `return nil`。
   2.2 `claimed=true` → 执行 `req.MainApply(tx, true)`：
   - **加锁点**：涉及多用户时用 `model.QyLockForUpdate(tx)` 按 **user id 升序**依次锁行（防 A→B / B→A 死锁）。
   - **CAS 兜底**：扣款一律 `Where("id = ? AND quota >= ?", uid, amt).Update("quota", gorm.Expr("quota - ?", amt))` 并检查 `RowsAffected == 1`。
   - **溢出校验**：加款前校验 `receiver.Quota + amt <= common.MaxQuota`。
   - 禁止调用 `user.Update()` / `IncrementUserAuthVersionWithTx`（会吊销会话）；禁止 `tx.Save(user)`。
   **【主库事务 B 提交/回滚】**

3. **提交成功后（非事务）**：
   3.1 `model.InvalidateUserCache(uid)`（划转两侧都要），比增量 `cacheIncrUserQuota` 更稳。
   3.2 `model.QyRecordLedgerLog(uid, LogTypeTopup/Consume, content, orderNo, other)` —— 单号进 `logs.request_id`（有索引）与 `other.qy_order_no`，满足既定范式且运营可检索。
   3.3 `req.AfterCommit(order)`。以上任意一步失败**只记日志，不影响结果**。

4. **【新库事务 C】** `UPDATE qy_fund_orders SET status=Success, settled_at=? WHERE order_no=? AND status=Pending` + `req.LocalCommit(tx, order)`。
   - `RowsAffected == 0` → 说明补偿任务已抢先处理，直接返回成功。

5. **失败路径：**
   - 步骤 2 事务回滚（余额不足 / 用户禁用 / 溢出）→ 【新库事务】置 `Failed` + `LastError`；outbox 行随事务一并回滚，**不留残留**。
   - 步骤 2 提交成功但步骤 4 失败（新库断连 / 进程崩溃）→ 单停在 `Pending`，由 §4.6 补偿任务修复。
   - 步骤 1 失败 → 主库完全未动，直接返回错误。

### 4.6 补偿任务（`twophase.Compensator`）

在新库租约 `qy:twophase:compensate` 保护下运行，每 `compensate_interval_seconds` 执行：

1. `SELECT * FROM qy_fund_orders WHERE status=0 AND updated_at < now-pending_grace AND next_probe_at <= now ORDER BY id LIMIT batch_size`。
2. 对每条：`applied, err := model.QyProbeFundOutbox(order.OrderNo)`
   - `applied=true` → 补做步骤 3.1/3.2/3.3 + 步骤 4，置 `Success`（`AfterCommit` 幂等由各模块保证）。
   - `applied=false` 且 `now-CreatedAt > manual_review_after_seconds` → 置 `Failed`（主库确定没动，安全）。
   - `applied=false` 且未超时 → `attempts++`，`next_probe_at = now + min(2^attempts, 300)`。
   - 探针本身报错（主库不可用）→ 只 `attempts++` 退避，**不改状态**。
   - `attempts >= max_probe_attempts` → 置 `Uncertain`，写审计 + `common.SysError` 告警，出现在管理端对账台。
3. `main_outbox_enabled=false` 时，探针退化为 `LOG_DB` 查 `request_id = order_no`；查不到一律置 `Uncertain`（**不敢**置 Failed，因为无法区分「没做」和「做了但日志没写」）。
4. 清理：`created_at < now - outbox_retention_days` 且对应单已终态 → `QyPruneFundOutbox` 分批删除。

---

## 5. 新库租约锁表（后台任务分布式互斥）

### 5.1 表：`qy_task_leases`

```go
type TaskLease struct {
	Name       string `gorm:"type:varchar(64);primaryKey"`        // 任务名，如 "twophase.compensate"
	Holder     string `gorm:"type:varchar(160);not null;default:''"` // NodeName + ":" + processId
	Fence      int64  `gorm:"not null;default:0"`                 // 每次易主 +1，防旧持有者回写
	LeaseUntil int64  `gorm:"not null;default:0;index"`           // 秒级 unix，**以 MySQL 时钟为准**
	AcquiredAt int64  `gorm:"not null;default:0"`
	UpdatedAt  int64  `gorm:"not null;default:0"`
}
```

`Holder = common.NodeName + ":" + processID`，`processID` 为进程启动时一次性的 `common.GetUUID()[:8]` —— `NodeName` 单独不够，同机多实例会重名。

### 5.2 获取 / 续租 / 接管（全部以 DB 时钟比较，消除节点时钟漂移）

```go
// 获取：先尝试插入，冲突则条件抢占已过期的租约
func Acquire(name, holder string, ttl int) (bool, int64, error) {
	err := db.Get().Exec(`INSERT INTO qy_task_leases (name,holder,fence,lease_until,acquired_at,updated_at)
	     VALUES (?,?,1,UNIX_TIMESTAMP()+?,UNIX_TIMESTAMP(),UNIX_TIMESTAMP())`, name, holder, ttl).Error
	if err == nil { return true, 1, nil }
	if !isDuplicateKey(err) { return false, 0, err }

	res := db.Get().Exec(`UPDATE qy_task_leases
	     SET holder=?, fence=fence+1, lease_until=UNIX_TIMESTAMP()+?, acquired_at=UNIX_TIMESTAMP(), updated_at=UNIX_TIMESTAMP()
	     WHERE name=? AND lease_until < UNIX_TIMESTAMP()`, holder, ttl, name)
	if res.Error != nil { return false, 0, res.Error }
	if res.RowsAffected == 0 { return false, 0, nil }   // 别人持有中
	var fence int64
	db.Get().Raw(`SELECT fence FROM qy_task_leases WHERE name=?`, name).Scan(&fence)
	return true, fence, nil
}

// 续租：必须同时匹配 holder 与 fence，且租约未过期
func Renew(name, holder string, fence int64, ttl int) error {
	res := db.Get().Exec(`UPDATE qy_task_leases SET lease_until=UNIX_TIMESTAMP()+?, updated_at=UNIX_TIMESTAMP()
	     WHERE name=? AND holder=? AND fence=? AND lease_until >= UNIX_TIMESTAMP()`, ttl, name, holder, fence)
	if res.Error != nil { return res.Error }
	if res.RowsAffected == 0 { return ErrLeaseLost }
	return nil
}

func Release(name, holder string, fence int64) error // SET lease_until=0 WHERE name=? AND holder=? AND fence=?
```

### 5.3 运行器

```go
// Run 启动一个受租约保护的周期任务。fn 收到的 ctx 在续租失败时立刻 cancel，
// fn 必须在每个循环/批次开头检查 ctx.Err()，确保失去租约后不再写库。
func Run(name string, interval time.Duration, fn func(ctx context.Context))
```

- 每 `lease_renew_seconds` 续租一次（必须 < TTL/2，`validate()` 已强制）。
- 续租失败 → cancel ctx → 停止 fn → 下个 tick 重新竞争。
- **fence 的作用**：老持有者卡在 GC/网络分区，恢复后其 `fence` 已过期，所有续租与业务写入（业务写入应带 `AND EXISTS(lease 校验)` 或至少在批次开头 `Renew` 一次）都失败，不会双跑写脏。
- 所有新库后台任务（补偿、佣金结算、充值扫描、可用率 flush、证据清理、审计清理、outbox 清理）**一律**通过 `lease.Run` 启动，**禁止**只靠 `common.IsMasterNode`（它只是环境变量，多节点都可配 master，见 GAPS §3.2(7)）。

---

## 6. `model/qy_export.go` 完整内容

> 纯新增文件，package model，**修改上游既有文件 0 行**。它是「扩展 ↔ 主库」的**唯一**耦合面。
> 铁律：本文件禁止 import 任何 `qianye/*` 包。

```go
package model

// qy_export.go —— 千夜扩展与上游 model 包之间的唯一耦合面（纯新增文件）。
// 命名统一用 Qy 前缀，规避上游未来同名符号冲突。
// 禁止 import qianye/*：否则 model → qianye → model 形成循环依赖。
// 扩展侧回调一律通过本文件底部的 hook 变量在 qianye.Init() 中注入。

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ───────────────────────── 1. 私有能力导出 ─────────────────────────

// QyLockForUpdate 暴露 model/locking.go:20 的 lockForUpdate（SQLite 下自动降级为 no-op）。
func QyLockForUpdate(tx *gorm.DB) *gorm.DB { return lockForUpdate(tx) }

// QyApplyUserQuotaCacheDelta 暴露 model/user_cache.go:147 的 cacheIncrUserQuota。
// delta 为负即扣减。仅在主库事务提交之后调用；禁止用整体用户快照覆盖 Quota。
func QyApplyUserQuotaCacheDelta(userId int, delta int64) error {
	return cacheIncrUserQuota(userId, delta)
}

// QyCommonGroupCol / QyLogGroupCol 暴露 model/main.go:22-28 的方言相关列名常量，
// 供扩展在 users / logs 上做 group 维度查询时正确加引号。
func QyCommonGroupCol() string { return commonGroupCol }
func QyLogGroupCol() string    { return logGroupCol }
func QyCommonTrueVal() string  { return commonTrueVal }
func QyCommonFalseVal() string { return commonFalseVal }

// QyLogDB 暴露日志库句柄；QyLogDBIsMainDB 用于判断 logs 是否与主库同库。
func QyLogDB() *gorm.DB      { return LOG_DB }
func QyLogDBIsMainDB() bool  { return LOG_DB == DB }

// ────────────────── 2. 主库 outbox：跨库两阶段的唯一精确探针 ──────────────────

// QyFundOutbox 与资金变更在同一个主库事务内写入，是补偿任务判定
// 「主库副作用是否已生效」的唯一权威依据。order_no 唯一索引同时让主库侧操作幂等。
type QyFundOutbox struct {
	Id        int64  `gorm:"primaryKey;autoIncrement"`
	OrderNo   string `gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_outbox_no"`
	Kind      string `gorm:"type:varchar(32);not null"`
	UserId    int    `gorm:"not null;index:idx_qy_outbox_user"`
	PeerId    int    `gorm:"not null;default:0"`
	Amount    int64  `gorm:"not null"`
	CreatedAt int64  `gorm:"not null;index:idx_qy_outbox_created"`
}

func (QyFundOutbox) TableName() string { return "qy_fund_outbox" }

// QyEnsureFundOutbox 只在 master 节点建表；调用方还需自行做跨节点 DDL 互斥。
func QyEnsureFundOutbox() error {
	if !common.IsMasterNode {
		return nil
	}
	return DB.AutoMigrate(&QyFundOutbox{})
}

// QyClaimFundOutbox 在调用方的主库事务内登记单号。
// 返回 false 表示该单号此前已登记（主库副作用已生效），调用方必须跳过重复扣加款。
func QyClaimFundOutbox(tx *gorm.DB, row *QyFundOutbox) (bool, error) {
	if row == nil || row.OrderNo == "" {
		return false, errors.New("qy: empty fund outbox order no")
	}
	if row.CreatedAt == 0 {
		row.CreatedAt = common.GetTimestamp()
	}
	res := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "order_no"}},
		DoNothing: true,
	}).Create(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// QyProbeFundOutbox 供补偿任务判定主库侧是否已生效。
func QyProbeFundOutbox(orderNo string) (bool, error) {
	var cnt int64
	if err := DB.Model(&QyFundOutbox{}).Where("order_no = ?", orderNo).Count(&cnt).Error; err != nil {
		return false, err
	}
	return cnt > 0, nil
}

// QyPruneFundOutbox 清理已终态单的历史 outbox 行。
func QyPruneFundOutbox(before int64, batch int) (int64, error) {
	res := DB.Where("created_at < ?", before).Limit(batch).Delete(&QyFundOutbox{})
	return res.RowsAffected, res.Error
}

// ─────────────── 3. 带单号的账本日志（写 LOG_DB，供运营与对账检索） ───────────────

// QyRecordLedgerLog 写一条 logs 记录，把扩展单号放进 request_id
// （logs.request_id 有 idx_logs_request_id 索引，可直接按单号检索），
// 并把结构化信息放进 other.qy_*。仅管理员可见的内容请放进 other["admin_info"]
// （model.formatUserLogs 会为普通用户自动剥离）。
func QyRecordLedgerLog(userId int, logType int, content string, orderNo string, other map[string]interface{}) {
	username, _ := GetUsernameById(userId, false)
	payload := make(map[string]interface{}, len(other)+1)
	for k, v := range other {
		payload[k] = v
	}
	payload["qy_order_no"] = orderNo
	log := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      logType,
		Content:   content,
		RequestId: orderNo,
		Other:     common.MapToJsonStr(payload),
	}
	if err := createLog(log); err != nil {
		common.SysLog("qy: failed to record ledger log: " + err.Error())
	}
}

// ──────────────── 4. 违规封号：状态 + 鉴权版本 + 缓存 的原子实现 ────────────────

var ErrQyUserAlreadyDisabled = errors.New("qy: user already disabled")

// QyDisableUser 在一个主库事务内完成「置为禁用 + 递增 auth_version」，
// 提交后清理用户缓存并留审计日志。缺少任何一步都会出现「被禁用但旧 token 仍可用」的安全洞。
// 已处于禁用态时返回 ErrQyUserAlreadyDisabled（幂等）。
func QyDisableUser(userId int, reason string) error {
	if userId <= 0 {
		return errors.New("qy: invalid user id")
	}
	already := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&User{}).
			Where("id = ? AND status = ?", userId, common.UserStatusEnabled).
			Update("status", common.UserStatusDisabled)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			already = true
			return nil
		}
		_, err := IncrementUserAuthVersionWithTx(tx, userId)
		return err
	})
	if err != nil {
		return err
	}
	if already {
		return ErrQyUserAlreadyDisabled
	}
	_ = invalidateUserCache(userId)
	RecordLog(userId, LogTypeManage, fmt.Sprintf("账户因违规被系统自动禁用：%s", reason))
	return nil
}

// ─────────────────── 5. Hook 变量（上游文件里 1 行挂载，零 import 改动） ───────────────────
//
// 赋值时机：qianye.Init()（main.go:365 之后，早于任何请求与后台协程），
// 因此使用普通变量而非 atomic —— 不存在并发读写窗口。
// 所有 hook 的实现体必须走 guard.Hot / guard.HotAsync：吞 panic、限时、失败只记日志。

var (
	// QyOnConsumeLog 在 RecordConsumeLog 入口触发（必须挂在 LogConsumeEnabled 早退之前，
	// 否则关闭消费日志的部署会完全收不到返佣事件）。
	QyOnConsumeLog func(c *gin.Context, userId int, params RecordConsumeLogParams)

	// QyOnUserQuotaChanged 供充值/退款类事件驱动返佣与冲正（可选，若走轮询扫描则不挂）。
	QyOnUserQuotaChanged func(userId int, delta int, source string, refId string)
)
```

**为什么只导出这些**：`IncrementUserAuthVersionWithTx`、`InvalidateUserCache`、`GetUserById`、`GetUsernameById`、`GetUserQuota`、`RecordLog`、`RecordConsumeLog`、`UpdateUserUsedQuotaAndRequestCount`、`UpdateChannelUsedQuota`、`DB`、`LOG_DB`、`User`、`Log`、`TopUp` 已全部导出，扩展直接用即可，不必再包一层。`newGormConfig` / `closeDB` / `checkMySQLChineseSupport` 不导出（复制成本远低于合并冲突成本）。

**配套的另两个新增文件（同样 0 行修改，各模块按需）：**

```go
// service/qy_export.go
package service
// QyAttachQuotaSaturation 暴露 service/log_info_generate.go:25 的 attachQuotaSaturationToOther，
// 让扩展写的消费日志也能满足 AGENTS.md 的 clamp 审计规范。
func QyAttachQuotaSaturation(other map[string]interface{}, clamp *common.QuotaClamp) {
	attachQuotaSaturationToOther(other, clamp)
}
// QyOnRelayError 由 controller/relay.go 的 defer 块调用（该文件已 import service，故调用点零 import 改动）。
var QyOnRelayError func(c *gin.Context, relayInfo *relaycommon.RelayInfo, apiErr *types.NewAPIError)
```

```go
// pkg/perf_metrics/qy_export.go
package perf_metrics
// QyOnSample 由 Record() 末尾调用，供扩展自建可用率维度。
var QyOnSample func(sample Sample)
```

---

## 7. `bootstrap.go` / `router.go` 骨架

### 7.1 `qianye/bootstrap.go`

```go
package qianye

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/hooks"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"
)

// Init 是扩展的唯一初始化入口。挂载点：main.go InitResources() 尾部（第 365 行之后）。
// 此时 .env、common.InitEnv()、logger、主库、casbin、OptionMap、日志库、Redis、i18n 均已就绪。
func Init() error {
	if err := config.Load(); err != nil {
		return err // 只有「显式指定的配置文件不存在 / 解析失败 / 校验失败」才会到这里
	}
	if !config.Enabled() {
		common.SysLog("qianye: extension disabled")
		return nil
	}
	if err := db.Init(config.Get().Database); err != nil {
		return err // 启动期连不上视为配置错误，直接 FatalLog；运行期断连才走 fail-open
	}
	if err := db.Migrate(qymodel.AllTables()...); err != nil {
		return err
	}
	if config.Get().TwoPhase.MainOutboxEnabled() {
		if err := model.QyEnsureFundOutbox(); err != nil {
			return err
		}
	}
	db.StartHealthLoop()
	hooks.Install() // 给 model.QyOnConsumeLog / service.QyOnRelayError / perfmetrics.QyOnSample 赋值
	common.SysLog("qianye: extension initialized")
	return nil
}

// StartBackgroundTasks 挂载点：main.go:152（service.StartSystemTaskRunner() 之后）。
// 所有任务都跑在新库租约下，多节点安全。
func StartBackgroundTasks() {
	if !config.Enabled() || !config.Get().Runtime.BackgroundEnabled {
		return
	}
	c := config.Get()
	lease.Run("twophase.compensate", dur(c.TwoPhase.CompensateIntervalSeconds), twophase.Compensate)
	lease.Run("twophase.prune_outbox", 6*time.Hour, twophase.PruneOutbox)
	// 各业务模块在此追加：commission.settle / commission.scan_topup /
	// availability.flush / violation.prune_evidence / audit.prune
	if c.Runtime.ConfigReloadSeconds > 0 {
		go config.WatchReload()
	}
}

// Close 可选；当前不挂进 main.go 的 defer（省预算），进程退出由 OS 回收，租约按 TTL 过期。
func Close() error { return db.Close() }
```

### 7.2 `qianye/router.go`

```go
package qianye

// RegisterRoutes 挂载点：main.go:195 之后、router.SetRouter(server, ...) 之前。
// 必须在 SetRouter 之前：SetWebRouter 用 engine 级 router.Use() 注册了
// gzip / GlobalWebRateLimit / Cache / static.Serve，之后注册的路由会被全部污染。
// /api/qy 与上游 /api 组前缀重叠但无通配冲突（上游 /api 下没有 /:param 路由），
// 代价是拿不到 SetApiRouter 的 gzip/GlobalAPIRateLimit，因此这里自行挂中间件。
func RegisterRoutes(engine *gin.Engine) {
	if !config.Enabled() {
		return
	}
	root := engine.Group("/api/qy")
	root.Use(middleware.RouteTag("api"))
	root.Use(gzip.Gzip(gzip.DefaultCompression))
	root.Use(middleware.GlobalAPIRateLimit())
	{
		root.GET("/status", qyctl.GetStatus) // 匿名可访问，返回 feature flags

		user := root.Group("")
		user.Use(middleware.UserAuth())
		{
			registerTransferRoutes(user)   // 模块 2
			registerCommissionRoutes(user) // 模块 3
			registerWithdrawRoutes(user)   // 模块 3
		}

		admin := root.Group("/admin")
		admin.Use(middleware.AdminAuth()) // 自动带管理审计（middleware/auth.go:68-75）
		{
			admin.GET("/health", qyctl.AdminHealth)
			admin.GET("/fund-orders", qyctl.AdminListFundOrders)
			admin.POST("/fund-orders/:order_no/reprobe", qyctl.AdminReprobeFundOrder)
			admin.POST("/fund-orders/:order_no/resolve", middleware.CriticalRateLimit(), qyctl.AdminResolveFundOrder)
			admin.GET("/audit-logs", qyctl.AdminListAuditLogs)
			admin.GET("/leases", qyctl.AdminListLeases)
			admin.POST("/config/reload", middleware.CriticalRateLimit(), qyctl.AdminReloadConfig)
			registerAdminModuleRoutes(admin) // 各业务模块
		}
	}
}
```

---

## 8. 统一审计表（所有资金操作强制写入）

### 8.1 表：`qy_audit_logs`（新库）

```go
type AuditLog struct {
	Id           int64  `gorm:"primaryKey;autoIncrement"`
	TraceNo      string `gorm:"type:varchar(64);not null;default:'';index:idx_audit_trace"`
	Category     string `gorm:"type:varchar(32);not null;index:idx_audit_cat_created,priority:1"`
	Action       string `gorm:"type:varchar(64);not null"`
	ActorType    string `gorm:"type:varchar(16);not null"`
	ActorUserId  int    `gorm:"not null;default:0;index:idx_audit_actor,priority:1"`
	ActorName    string `gorm:"type:varchar(64);not null;default:''"`
	TargetUserId int    `gorm:"not null;default:0;index:idx_audit_target,priority:1"`

	AmountQuota  int64           `gorm:"not null;default:0"`
	AmountFiat   decimal.Decimal `gorm:"type:decimal(18,4);not null;default:0"`
	Currency     string          `gorm:"type:varchar(8);not null;default:''"`
	FrozenRate   decimal.Decimal `gorm:"type:decimal(18,8);not null;default:0"`

	Result       string `gorm:"type:varchar(16);not null"`
	Reason       string `gorm:"type:varchar(512);not null;default:''"`
	BeforeSnap   string `gorm:"type:text"`
	AfterSnap    string `gorm:"type:text"`

	IP           string `gorm:"type:varchar(64);not null;default:''"`
	UserAgent    string `gorm:"type:varchar(256);not null;default:''"`
	RequestId    string `gorm:"type:varchar(64);not null;default:'';index"`
	NodeName     string `gorm:"type:varchar(128);not null;default:''"`
	CreatedAt    int64  `gorm:"not null;index:idx_audit_cat_created,priority:2;index:idx_audit_actor,priority:2;index:idx_audit_target,priority:2"`
}
```

字段理由：

- `TraceNo` = `qy_fund_orders.order_no` 或业务单号 —— 一笔资金的全生命周期（申请→审核→到账→冲正）靠它串起来。
- `Category` ∈ `{fund, transfer, commission, withdraw, violation, config, admin}`；`Action` 是稳定英文标识（如 `withdraw.approve`），**不存自然语言**，前端按 `qy_audit_<action>` i18n 渲染（与上游 `RecordOperationAuditLog` 的 `other.op` 思路一致）。
- `ActorType` ∈ `{user, admin, system}` —— 补偿任务与结算任务写的是 `system`，必须能与人工操作区分。
- `Amount*` 三件套：quota 与法币分列，`FrozenRate` 冻结当时 `operation_setting.USDExchangeRate`（**D1 硬性要求**，该变量管理员可随时改，不冻结则历史对账永远对不上）。
- `Before/AfterSnap`：JSON 快照，按 `audit.snapshot_max_bytes` 截断（超出时保留头部并追加 `"...[truncated]"`）。
- `Result` ∈ `{ok, fail, pending}`；`Reason` 承载拒绝理由/失败原因。
- 表**只追加不更新不删除**（除按 `retention_days` 归档，默认 0=永久）。

### 8.2 写入接口

```go
package audit

// Write 永不返回错误：审计失败不能阻塞业务，但必须 SysError 告警。
func Write(c *gin.Context, e Entry)
// WriteTx 在给定新库事务内写入，用于「业务状态变更与审计必须同生共死」的场景（如提现审核）。
func WriteTx(tx *gorm.DB, e Entry) error
```

**强制规则**：`twophase.Execute` 内部在状态每次跃迁（Pending→Success/Failed/Uncertain/Reversed）时自动写一条审计，业务模块无需重复埋点；业务模块只需为「人工决策」（审核通过/拒绝、配置变更、手工对账）额外写审计。

---

## 9. API 清单（模块 0 部分，前缀 `/api/qy/`）

统一响应信封（与上游一致）：`{"success":bool,"message":string,"data":any}`，非 200 时额外带 `"code"`。

| # | Method + Path | 权限 | 请求 | 响应 data |
|---|---|---|---|---|
| 1 | `GET /api/qy/status` | 匿名 | — | `{enabled, available, version, features:{transfer,commission,withdraw,wallet_transfer_entry,...}, wallet:{tabs_keep_mounted}, usage_log:{show_reasoning_effort,show_cache_ratio}}` |
| 2 | `GET /api/qy/admin/health` | 管理员 | — | `{db:{available,breaker_open_until,ping_ms,open_conns,in_use,idle,wait_count}, migrate:{applied,table_count}, two_phase:{pending,uncertain,oldest_pending_age_sec}, leases:[{name,holder,fence,lease_until,expired}], config:{path,loaded_at,mtime}}` |
| 3 | `GET /api/qy/admin/fund-orders` | 管理员 | query: `status,kind,user_id,order_no,ref_id,start_ts,end_ts,p,page_size` | `{items:[FundOrder],total}` |
| 4 | `POST /api/qy/admin/fund-orders/:order_no/reprobe` | 管理员 | — | `{order_no,status,main_applied,resolved}` 立即重跑一次探针 |
| 5 | `POST /api/qy/admin/fund-orders/:order_no/resolve` | 管理员 | `{decision:"success"\|"failed", reason}` | `{order_no,status}` 仅允许对 `Uncertain` 单执行；写审计 |
| 6 | `GET /api/qy/admin/audit-logs` | 管理员 | query: `category,action,actor_user_id,target_user_id,trace_no,start_ts,end_ts,p,page_size` | `{items:[AuditLog],total}` |
| 7 | `GET /api/qy/admin/leases` | 管理员 | — | `{items:[TaskLease]}` |
| 8 | `POST /api/qy/admin/config/reload` | 管理员 | — | `{reloaded:bool, changed_sections:[...]}`；`database` 段永不重载 |

说明：

- 1 号接口**不**走 `guard.RequireAPI`（禁用时也要返回 `{enabled:false}`，前端据此隐藏入口而不是报错）。
- 2–8 号一律 `guard.RequireAPI(c, guard.FlagCore)`。
- 5、8 挂 `middleware.CriticalRateLimit()`。
- 所有管理端接口天然带上游审计（`middleware/auth.go:68-75`，`AdminAuth()` 自动 `beginAdminAudit`），无需手工埋点。
- 分页统一 `p`（1 起）+ `page_size`（默认 20，上限 100）。

---

## 10. 原项目改动清单（模块 0 部分，精确到行）

| # | 文件:行号 | 插入的确切代码 | 行数 | 冲突风险 |
|---|---|---|---|---|
| 1 | `main.go:31` 之后（import 块内） | `	"github.com/QuantumNous/new-api/qianye"` | 1 | **低**（import 块，gofmt 稳定；插在 `service/authz` 与 `_ setting/performance_setting` 之间保持字母序） |
| 2 | `main.go:365` 之后（`service.StartAuthArtifactCleanup()` 与 `return nil` 之间） | ```	if err := qianye.Init(); err != nil {```<br>```		common.SysError("failed to initialize qianye extension: " + err.Error())```<br>```		return err```<br>```	}``` | 4 | **低**（`InitResources()` 尾部，上游极少改动） |
| 3 | `main.go:195` 之后（`InjectGoogleAnalytics()` 与 `// 设置路由` 之间） | `	qianye.RegisterRoutes(server)` | 1 | **低**（必须在 `router.SetRouter` 之前，否则被 `SetWebRouter` 的 engine 级 gzip/GlobalWebRateLimit/Cache/static.Serve 污染） |
| 4 | `main.go:152` 之后（`service.StartSystemTaskRunner()` 之后） | `	qianye.StartBackgroundTasks()` | 1 | **低** |

**模块 0 小计：1 个原项目文件、7 行。** 剩余预算：9 个文件 / 33 行，分配给模块 2–7。

**纯新增文件（不计入改动预算，0 行修改）：**

| 文件 | 用途 | 冲突风险 |
|---|---|---|
| `model/qy_export.go` | 主库耦合面 + hook 变量 | 低（Qy 前缀规避同名） |
| `service/qy_export.go` | `QyAttachQuotaSaturation` + `QyOnRelayError` hook 变量 | 低 |
| `pkg/perf_metrics/qy_export.go` | `QyOnSample` hook 变量 | 低 |
| `qianye/**` | 扩展全部实现 | 无 |

**明确不改：** `go.mod` / `go.sum`（yaml.v3 在 go.mod:57、uuid 在 :28、decimal 在 :41，均已是直接依赖）、`.gitignore`（`/data/` 已在 :29）、`Dockerfile`、`docker-compose.yml`、`router/main.go`、`router/api-router.go`。

**给后续模块的挂载点约定（各模块自行核销预算，此处只锁定形态）：** 上游低层包里的 hook 一律写成
`if model.QyOnConsumeLog != nil { model.QyOnConsumeLog(c, userId, params) }`
这种同包变量调用 —— **1 行、0 import 改动**。这是把总量压进 40 行的唯一办法，任何模块都不得改成直接 `import qianye/...`（除 `main.go` 外，那样既加 import 行又会在 `model`/`service` 造成循环依赖）。

---

## 11. 并发与边界

### 11.1 竞态

| 竞态 | 处理 |
|---|---|
| 同一用户重复提交划转/提现 | `uk_fund_idem` 唯一索引（幂等键含客户端 UUID）+ `Pending` 期内返回 409；前端按钮 disable 只是辅助 |
| A→B 与 B→A 并发划转死锁 | 主库事务内**按 user id 升序**依次 `QyLockForUpdate`，全项目统一顺序 |
| 补偿任务与业务线程同时结算同一单 | 状态跃迁一律带 CAS：`WHERE order_no=? AND status=Pending`，`RowsAffected==0` 即让出 |
| 主库事务被重试导致重复扣款 | `QyClaimFundOutbox` 唯一索引：`claimed=false` 直接跳过资金变更 |
| 多 master 节点并发 `AutoMigrate` | `IsMasterNode` gate + MySQL `GET_LOCK`（固定同一条 `*sql.Conn`，否则 RELEASE_LOCK 打在别的连接上） |
| 多节点后台任务双跑 | `qy_task_leases` 租约 + fence；所有时间比较用 `UNIX_TIMESTAMP()`（DB 时钟），消除节点时钟漂移 |
| 老持有者网络分区恢复后写脏 | fence 单调递增；续租与批次开头校验失败即 cancel ctx 停止工作 |
| 热路径 hook 与 relay 争抢主库连接 | `guard.HotAsync` 有界队列 + 独立 worker；inviter_id 走 `inviter_cache_seconds` 缓存（GAPS §11） |
| hook 变量读写竞态 | 只在 `qianye.Init()`（`main.go:55` 内，早于所有 goroutine 与 HTTP 监听）赋值一次，无并发窗口；不得在运行期改写 |
| 配置热载与读取 | `atomic.Pointer[Config]` 整体替换，读方永远拿到自洽快照 |

### 11.2 边界与异常

| 场景 | 处理 |
|---|---|
| 余额不足 | 主库 `WHERE quota >= ?` + `RowsAffected==0` → 事务回滚 → 单置 `Failed`。**禁止**用 `DecreaseUserQuota`（它无余额校验、会扣成负数、且 `db=false` 时可能进批量队列） |
| 接收方 quota 溢出 | 事务内校验 `receiver.Quota > common.MaxQuota - amount` → 拒绝。`users.quota` 是 int32（`common.MaxQuota = MaxInt32` ≈ $4294.97），上游全无此校验 |
| 新库 int64 金额跨库到主库 int32 | 跨库前统一 `if amt <= 0 \|\| amt > int64(common.MaxQuota) { return ErrAmountOutOfRange }`，绝不静默截断 |
| 金额计算 | **一律** `common.QuotaFromDecimal` / `QuotaFromFloat` / `QuotaRound`（AGENTS.md 强制），禁止 `int(float64(x)*r)`、`int(d.IntPart())`。佣金比例用 `shopspring/decimal` 全程精确，只在最终落 quota 时转换；`*Checked` 变体产出的 `*common.QuotaClamp` 通过 `service.QyAttachQuotaSaturation` 写进日志 `other.admin_info.quota_saturation` |
| 小额佣金截断归零 | 佣金按 `decimal(24,8)` 累计到 `qy_commission_balances.pending_amount`，达 `min_settle_quota` 才结算成整数 quota（GAPS §10） |
| 用户软删除 | `users` 有 `DeletedAt`，收款方查询用默认作用域（自动过滤），**禁止** `Unscoped()` |
| 用户被禁用 | 划转两侧、提现到账前均校验 `Status == common.UserStatusEnabled` |
| 新库启动期连不上 | `Init()` 返回 error → `main.go:56-59` `FatalLog`（配置写错就该炸） |
| 新库运行期断连 | 熔断打开 → 热路径 `guard.Hot` 静默放行（fail-open）；非热路径 `RequireAPI` 返回 503 + `Retry-After: 30` |
| 主库断连（新库正常） | 两阶段停在 `Pending`，补偿任务探针失败只退避不改状态；`Uncertain` 后进管理端对账台 |
| YAML 字段拼错 | `KnownFields(true)` 严格解码直接报错 —— 风控开关拼错却静默失效是最危险的失败模式 |
| 配置文件权限泄露 | 启动时检查 `qianye.yaml` 权限，若为 world-readable 且 DSN 含密码，`SysError` 告警（不阻塞启动） |
| `logs.request_id` 长度 | varchar(64)，单号 31 字符，安全 |
| `Uncertain` 单 | 永远不自动结算，只能管理员在对账台 `resolve`，且必须填 `reason` 并落审计 |

---

## 12. 前端页面（模块 0 部分）

| 文件 | 新建/修改 | 说明 |
|---|---|---|
| `web/src/features/qy-shared/api.ts` | 新建 | `getQyStatus()`；统一处理 `code === 'qy_unavailable'`（503）→ 展示「扩展服务暂不可用，请稍后再试」空态而非红色报错；`qy_disabled`/`qy_feature_off`（404）→ 直接隐藏入口 |
| `web/src/features/qy-shared/use-qy-status.ts` | 新建 | 全局 hook，缓存 `/api/qy/status` 结果（staleTime 5min），**所有模块的入口渲染都依赖它**，扩展禁用时前端零痕迹 |
| `web/src/features/qy-shared/components/qy-unavailable.tsx` | 新建 | 统一降级空态组件（图标 + 文案 + 重试按钮） |
| `web/src/features/qy-admin/reconcile/index.tsx` | 新建 | 对账台：`Pending`/`Uncertain` 单列表、单号搜索、重跑探针、人工裁决对话框（必填 reason） |
| `web/src/features/qy-admin/audit/index.tsx` | 新建 | 审计日志表：category/action/actor/target/trace_no 筛选，行展开显示 before/after 快照 diff |
| `web/src/features/qy-admin/health/index.tsx` | 新建 | 健康面板：DB 可用性 / 熔断状态 / 连接池 / 租约持有者 / 配置来源路径与加载时间；配置重载按钮 |
| `web/src/routes/_authenticated/qy/...` | 新建 | TanStack Router 路由文件；`routeTree.gen.ts` 冲突时**删除重新 build**（写进合并流程文档） |
| `web/src/hooks/use-sidebar-data.ts` | **修改** | 只加 1 个「千夜工作区」入口，子页面走 `SIDEBAR_VIEWS`；由 `useQyStatus().enabled` 控制显隐 |
| `web/src/i18n/locales/*.json` ×7 | **修改（纯追加）** | key 用 `qy_` 下划线扁平命名，如 `qy_reconcile_title`、`qy_status_uncertain`、`qy_error_unavailable`。**绝不能用点号**（i18next `keySeparator` 默认 `.`，会被当嵌套路径） |

---

## 13. 我建议补充的

标注为**建议**，用户未提出但地基缺了会返工：

1. **主库 outbox 表（`two_phase.main_outbox_enabled`）** — 已写进正文，但这是最需要拍板的一项。不做它，GAPS §3.2(1) 无解：`model.RecordLog` 写的是 `LOG_DB`、无法进主库事务，「commit 成功 / 日志未写」的窗口会永久留下无法判定的 pending 单。代价是主库多一张 6 列小表（纯新增文件创建，上游冲突 0）。**强烈建议保持 true。**

2. **`table_prefix: qy_`** — 让扩展表可以与主库共存于同一 schema。小规模部署可以只给一个 DSN 而不必真的开第二个库，同时大部署仍然完全独立。零额外成本。

3. **`guard.HotAsync` 有界队列 + 丢弃计数** — GAPS §11 指出消费返佣 hook 在 relay 同步路径上。必须在地基层就规定「热路径 hook 一律异步 + 有界 + 可丢弃」，否则每个模块各自实现会出现同步查主库的实现。丢弃数暴露在 `/admin/health`。

4. **`KnownFields(true)` 严格 YAML 解码** — D4 的三个风控开关拼错却静默按默认值跑，是「上线即穿」级别的失败模式，必须在解析层堵死。

5. **`Uncertain` 状态 + 人工对账台** — 不设这个状态，补偿任务只能在「重试到死」和「猜一个结果」之间选。资金系统必须有「我不知道，交给人」的合法出口，且裁决动作必须落审计。

6. **法币汇率冻结字段进审计表** — D1 已要求佣金产生时冻结汇率，但**审计记录也要带**，否则「为什么这笔按 7.3 算、那笔按 7.1 算」在事后无法自证。

7. **配置文件权限自检** — `qianye.yaml` 含 DB 密码与 `payout_account_aes_key`，启动时检查是否 world-readable 并告警（不阻塞）。

8. **`payout_account_aes_key` 为空时禁止 `methods` 含 `fiat`** — 收款信息是 PII，明文落库不可接受。在 `validate()` 强制，而不是留给模块 3 自己记得。

9. **`/api/qy/status` 匿名可访问且永远返回 200** — 让前端在扩展禁用/不可用时能「零痕迹隐藏」而不是满屏红色报错。这是「优雅降级」在前端的对应实现，必须在地基层定义。

10. **限额与冷却期全部进 YAML 而非硬编码** — `transfer.daily_max_*`、`withdraw.daily_max_count`、`violation.max_fee_quota`、`commission.max_per_order_quota`。资金类功能上线后第一周一定要调参，硬编码就意味着改代码重新发版。

11. **`new_account_freeze_hours`（新号 24h 内不可转出）与 `recipient_lookup: "id"`** — 划转是天然的套现/洗号通道；不提供用户名模糊搜索同时也避免了与「用户名脱敏」需求自相矛盾（开放搜索等于开放用户枚举）。

12. **错误文案统一走 i18n key** — 后端返回 `code`（`qy_unavailable` / `qy_insufficient_quota` / `qy_amount_out_of_range` / `qy_daily_limit_exceeded` / `qy_order_processing` / `qy_receiver_disabled` / `qy_overflow`），前端按 code 映射到 `qy_error_*` 文案。后端 `message` 只作兜底。避免每个模块各写一套中文硬编码。
