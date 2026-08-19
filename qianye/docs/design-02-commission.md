# 需求2a:佣金账本与返佣触发

# 需求 2 前半：佣金账本与返佣触发 — 实施设计

> 归属包：`qianye/`。本设计只覆盖**账本 + 触发 + 结算入账**，提现（站内兑换 / 线下法币）由另一份设计承接；本设计负责为其准备好 `available_quota` / `available_fiat` / `frozen_quota` / `withdrawn_quota` 四个字段与加锁契约（见 §10.6）。

---

## 0. 模块边界与包结构

```
qianye/
  config/commission.go          YAML 段解析（三开关 + 热路径参数 + salt）
  model/commission.go           本文 §1 全部 GORM 模型（绑 qianye/db.DB）
  service/commission/
    hook.go        钩子实现（注册到 model.QyOn*）
    inviter_cache.go  邀请人缓存（GAPS §一.11）
    accrual.go     计佣：口径判定 / 费率解析 / 风控 / 落 accrual
    settle.go      结算：余数累积 + floor 入账（GAPS §一.10）
    topup_watch.go GORM callback + 低水位重扫（GAPS §三.2(6)）
    clawback.go    退款冲正
    lease.go       新库租约锁（架构 §8）
  controller/commission_user.go / commission_admin.go
model/qy_export.go              ★ 新增文件（同包），声明 4 个钩子函数变量
```

**关键解耦手段**：`model` 包**不能** import `qianye`（会与 `qianye/service → model` 成环）。因此在 `model/qy_export.go`（纯新增文件）里声明**默认 no-op 的函数变量**，`qianye.Init()` 时赋值。这是项目已有范式（`main.go:138-144` 的 `service.GetTaskAdaptorFunc = ...`）。于是原文件里插入的每一行都只是**调用同包符号**，与上游合并时冲突面 = 那一行本身。

```go
// model/qy_export.go —— 纯新增文件，不改任何现有文件
package model

import "github.com/gin-gonic/gin"

// 扩展钩子。默认 no-op；由 qianye.Init() 在 HTTP 服务启动前一次性赋值。
// 赋值严格早于服务开始接收流量，故用普通变量即可（与 service.GetTaskAdaptorFunc 同范式）。
var (
	QyOnConsumeLog           = func(c *gin.Context, userId int, params RecordConsumeLogParams) {}
	QyOnTaskBillingLog       = func(params RecordTaskBillingLogParams) {}
	QyOnRedeemSuccess        = func(userId int, redemptionId int, quota int) {}
	QyOnManualTopUpCompleted = func(tradeNo string, userId int) {}
)

// 打通 model 包私有能力（供跨库两阶段与提现模块复用）
func QyLockForUpdate(tx *gorm.DB) *gorm.DB              { return lockForUpdate(tx) }
func QyCacheApplyUserQuotaDelta(userId int, d int64) error { return cacheIncrUserQuota(userId, d) }
```

---

## 1. 完整表结构

全部落**独立 MySQL**（`qianye/db.DB`），`AutoMigrate` 必须 gate `common.IsMasterNode`（照抄 `model/main.go:197-199`）。

金额精度总原则：
- **quota 域**用 `decimal(30,10)`。理由：`quota` 上限 `MaxInt32≈2.1e9`（10 位整数），比例最小到 0.0001（万分之一），10 位小数足够无损承载 `base_quota × rate`；累计余数远不会超 `10^20`。
- **法币域**用 `decimal(18,6)`，并**在计佣时刻冻结 `operation_setting.USDExchangeRate`**（D1 硬性要求；该变量在 `setting/operation_setting/payment_setting_old.go:18`，管理员可通过 `model/option.go:424` 随时热改）。

### 1.1 `qy_commission_accrual` — 计佣明细（用户所说的"佣金流水"）

```go
type CommissionAccrual struct {
	Id         int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	AccrualNo  string `gorm:"type:varchar(40);not null;uniqueIndex:uk_qy_ca_no" json:"accrual_no"`

	// —— 幂等键（§10.1）：(source_type, source_no) 唯一
	SourceType string `gorm:"type:varchar(24);not null;uniqueIndex:uk_qy_ca_src,priority:1" json:"source_type"`
	SourceNo   string `gorm:"type:varchar(128);not null;uniqueIndex:uk_qy_ca_src,priority:2" json:"source_no"`

	InviterId int `gorm:"not null;index:idx_qy_ca_inviter_status,priority:1;index:idx_qy_ca_inviter_bucket,priority:1" json:"inviter_id"`
	InviteeId int `gorm:"not null;index:idx_qy_ca_invitee" json:"invitee_id"`

	// —— 计佣基数
	BaseQuota int64           `gorm:"not null;default:0" json:"base_quota"`               // int64 防求和溢出
	BaseMoney decimal.Decimal `gorm:"type:decimal(18,6);not null;default:0" json:"base_money"` // 法币基数（充值路径有值）
	Currency  string          `gorm:"type:varchar(8);not null;default:'USD'" json:"currency"`

	// —— 比例
	RateBps    int    `gorm:"not null;default:0" json:"rate_bps"`     // 万分之一，5% = 500
	RateSource string `gorm:"type:varchar(16);not null;default:'global'" json:"rate_source"` // user|group|tier|global

	// —— 佣金（quota 域，全精度，可为负 = 冲正）
	GrossAmount   decimal.Decimal `gorm:"type:decimal(30,10);not null;default:0" json:"gross_amount"`
	SettledAmount decimal.Decimal `gorm:"type:decimal(30,10);not null;default:0" json:"settled_amount"` // 已被结算吸收的部分（§2）

	// —— 汇率冻结（D1）
	UsdRate      decimal.Decimal `gorm:"type:decimal(12,6);not null;default:0" json:"usd_rate"`
	FiatCurrency string          `gorm:"type:varchar(8);not null;default:'CNY'" json:"fiat_currency"`

	Status     string `gorm:"type:varchar(16);not null;default:'accrued';index:idx_qy_ca_inviter_status,priority:2;index:idx_qy_ca_status_mature,priority:1" json:"status"`
	RiskFlags  string `gorm:"type:varchar(255);not null;default:''" json:"risk_flags"`
	MatureAt   int64  `gorm:"not null;default:0;index:idx_qy_ca_status_mature,priority:2" json:"mature_at"`
	BucketDate string `gorm:"type:char(8);not null;default:'';index:idx_qy_ca_inviter_bucket,priority:2" json:"bucket_date"` // yyyymmdd

	RefAccrualId int64 `gorm:"not null;default:0;index:idx_qy_ca_ref" json:"ref_accrual_id"` // 冲正指向原单
	SettlementId int64 `gorm:"not null;default:0;index:idx_qy_ca_settlement" json:"settlement_id"`

	ReviewerId  int    `gorm:"not null;default:0" json:"reviewer_id"`
	ReviewedAt  int64  `gorm:"not null;default:0" json:"reviewed_at"`
	RejectReason string `gorm:"type:varchar(255);not null;default:''" json:"reject_reason"`

	Remark    string `gorm:"type:varchar(255);not null;default:''" json:"remark"`
	CreatedAt int64  `gorm:"autoCreateTime;not null;index:idx_qy_ca_created" json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime;not null" json:"updated_at"`
}
func (CommissionAccrual) TableName() string { return "qy_commission_accrual" }
```

字段存在理由（逐条）：

| 字段 | 为什么必须有 |
|---|---|
| `AccrualNo` | 面向用户/客服的单号；`Id` 会暴露平台佣金总笔数，不外发 |
| `SourceType`+`SourceNo` | **唯一幂等键**。充值 = `trade_no`；兑换码 = `RD{id}`；消费 = `{inviteeId}:{yyyymmdd}`（日聚合，见 §2.2）；冲正 = `CB:...` |
| `BaseQuota` (int64) | 消费日聚合会把整天基数累加，`int32` 会溢出（用户日消费可轻松超 21 亿 quota = $4295） |
| `BaseMoney`/`Currency` | 法币提现对账需要原始付款金额，不能从 quota 反算（`QuotaPerUnit` 可改） |
| `RateBps` (int) | 用整数万分比存比例，避免 float 比例落库后不可复现；5% = 500 |
| `RateSource` | 审计"这笔为什么是 5% 而不是 3%" |
| `GrossAmount` decimal(30,10) | **GAPS §一.10 的核心**：不截断的精确佣金。`int` 会把 5%×200quota=10 变 10，但 5%×10quota=0.5 变 0 |
| `SettledAmount` | 支持"行可继续增长、结算增量吸收"，使日聚合行既能实时累加又能多次安全结算（§2.3） |
| `UsdRate`/`FiatCurrency` | **D1 硬要求**：佣金产生时冻结汇率，否则管理员改 `USDExchangeRate` 后历史对账全错 |
| `Status` | `pending_review`/`risk_hold`/`accrued`/`settled`/`voided`/`rejected`。承载"管理员审核佣金"需求 |
| `MatureAt` | T+N 成熟期（防"充值→拿佣金→退款"套利，见 §12.2） |
| `BucketDate` | 消费日聚合键 + 邀请人日封顶统计的索引维度 |
| `RefAccrualId` | 冲正单指向原单，管理端可一键看到"这笔被冲了多少" |
| `SettlementId` | 反查这笔被哪个结算批次吸收 |

### 1.2 `qy_commission_balance` — 佣金余额（含**未结算余数**）

```go
type CommissionBalance struct {
	UserId int `gorm:"primaryKey" json:"user_id"` // 邀请人

	// ★ 未结算余数账本（GAPS §一.10 的落点）：所有不足 1 quota 的零头 + 尚未 floor 的精确累计
	UnsettledAmount decimal.Decimal `gorm:"type:decimal(30,10);not null;default:0" json:"unsettled_amount"` // 可为负 = 欠账

	AvailableQuota     int64 `gorm:"not null;default:0" json:"available_quota"`      // 可提现整数额度
	FrozenQuota        int64 `gorm:"not null;default:0" json:"frozen_quota"`         // 提现审核中（提现模块维护）
	WithdrawnQuota     int64 `gorm:"not null;default:0" json:"withdrawn_quota"`      // 已提现累计（提现模块维护）
	TotalEarnedQuota   int64 `gorm:"not null;default:0" json:"total_earned_quota"`
	TotalClawbackQuota int64 `gorm:"not null;default:0" json:"total_clawback_quota"`

	// 与 AvailableQuota 同步维护的、按冻结汇率折算的法币值（D1 法币提现直接读这个）
	AvailableFiat decimal.Decimal `gorm:"type:decimal(18,6);not null;default:0" json:"available_fiat"`
	FiatCurrency  string          `gorm:"type:varchar(8);not null;default:'CNY'" json:"fiat_currency"`

	InviteeCount int    `gorm:"not null;default:0" json:"invitee_count"`
	DebtBlocked  bool   `gorm:"not null;default:false" json:"debt_blocked"` // 欠账未清，禁止提现
	Version      int64  `gorm:"not null;default:0" json:"-"`               // 乐观锁（SQLite/无锁场景兜底）

	LastSettledAt int64 `gorm:"not null;default:0" json:"last_settled_at"`
	CreatedAt     int64 `gorm:"autoCreateTime;not null" json:"created_at"`
	UpdatedAt     int64 `gorm:"autoUpdateTime;not null;index:idx_qy_cb_updated" json:"updated_at"`
}
func (CommissionBalance) TableName() string { return "qy_commission_balance" }
```

> **为什么"未结算余数"不单独建表**：余数与 `AvailableQuota` 必须在**同一个事务、同一把行锁**下原子变更（`grant = floor(unsettled)` → `unsettled -= grant` → `available += grant`）。拆两张表只会引入第二次加锁与新的中间态，没有任何收益。`qy_commission_settlement`（§1.3）记录每批的 `carry_before/carry_after`，提供余数的**完整审计轨迹**——这才是"余数表"真正需要的东西。

### 1.3 `qy_commission_settlement` — 结算批次（余数审计轨迹）

```go
type CommissionSettlement struct {
	Id       int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	SettleNo string `gorm:"type:varchar(40);not null;uniqueIndex:uk_qy_cs_no" json:"settle_no"`
	UserId   int    `gorm:"not null;index:idx_qy_cs_user_created,priority:1" json:"user_id"`

	AccrualCount int             `gorm:"not null;default:0" json:"accrual_count"`
	DeltaAmount  decimal.Decimal `gorm:"type:decimal(30,10);not null;default:0" json:"delta_amount"`  // 本批吸收的精确佣金（可为负）
	CarryBefore  decimal.Decimal `gorm:"type:decimal(30,10);not null;default:0" json:"carry_before"`  // ★ 结算前余数
	CarryAfter   decimal.Decimal `gorm:"type:decimal(30,10);not null;default:0" json:"carry_after"`   // ★ 结算后余数
	GrantedQuota int64           `gorm:"not null;default:0" json:"granted_quota"`                      // 本批入账整数（可为负 = 回收）
	ClippedQuota int64           `gorm:"not null;default:0" json:"clipped_quota"`                      // 被日封顶削掉、留待明日的部分

	UsdRateWeighted decimal.Decimal `gorm:"type:decimal(12,6);not null;default:0" json:"usd_rate_weighted"`
	FiatDelta       decimal.Decimal `gorm:"type:decimal(18,6);not null;default:0" json:"fiat_delta"`

	CreatedAt int64 `gorm:"autoCreateTime;not null;index:idx_qy_cs_user_created,priority:2" json:"created_at"`
}
func (CommissionSettlement) TableName() string { return "qy_commission_settlement" }
```

### 1.4 `qy_commission_rate` — 佣金比例（全局 / 分层 / 分组 / 按用户）

```go
type CommissionRate struct {
	Id        int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Scope     string `gorm:"type:varchar(16);not null;uniqueIndex:uk_qy_cr,priority:1" json:"scope"`      // global|group|tier|user
	ScopeKey  string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:uk_qy_cr,priority:2" json:"scope_key"` // group 名 / tier 门槛(quota,字符串) / user_id
	SourceType string `gorm:"type:varchar(24);not null;default:'any';uniqueIndex:uk_qy_cr,priority:3" json:"source_type"` // recharge|consume|any

	RateBps      int   `gorm:"not null;default:0" json:"rate_bps"`
	MinBaseQuota int64 `gorm:"not null;default:0" json:"min_base_quota"` // 单笔基数低于此值不计佣（默认 0）
	Enabled      bool  `gorm:"not null;default:true" json:"enabled"`
	EffectiveFrom int64 `gorm:"not null;default:0" json:"effective_from"`
	EffectiveTo   int64 `gorm:"not null;default:0" json:"effective_to"` // 0 = 永久

	OperatorId int    `gorm:"not null;default:0" json:"operator_id"`
	Remark     string `gorm:"type:varchar(255);not null;default:''" json:"remark"`
	CreatedAt  int64  `gorm:"autoCreateTime;not null" json:"created_at"`
	UpdatedAt  int64  `gorm:"autoUpdateTime;not null" json:"updated_at"`
}
func (CommissionRate) TableName() string { return "qy_commission_rate" }
```

解析优先级：`user` > `group`（邀请人的 `users.group`）> `tier`（取 `ScopeKey ≤ 邀请人累计基数` 的最大门槛）> `global`。同优先级内 `SourceType` 精确匹配优于 `any`。**全表常驻内存**（行数 ≤ 数百），`SyncRates()` 每 60s 从新库刷新，热路径零查询。

### 1.5 `qy_invite_relation` — 邀请关系快照（脱敏列表 + 风控 + 单下线封顶）

```go
type InviteRelation struct {
	InviteeId int `gorm:"primaryKey" json:"invitee_id"`
	InviterId int `gorm:"not null;index:idx_qy_ir_inviter" json:"inviter_id"`

	MaskedName string `gorm:"type:varchar(64);not null;default:''" json:"masked_name"` // ★ 服务端脱敏后缓存，列表页不回主库
	InviteeRef string `gorm:"type:varchar(16);not null;uniqueIndex:uk_qy_ir_ref" json:"invitee_ref"` // sha256(salt+id)[:12]，对外唯一标识，不暴露 user_id

	BoundAt          int64 `gorm:"not null;default:0" json:"bound_at"`  // = users.created_at（项目无独立绑定时间）
	FirstRechargeAt  int64 `gorm:"not null;default:0" json:"first_recharge_at"`
	LastActiveAt     int64 `gorm:"not null;default:0" json:"last_active_at"`

	TotalBaseQuota       int64 `gorm:"not null;default:0" json:"total_base_quota"`
	TotalCommissionQuota int64 `gorm:"not null;default:0" json:"total_commission_quota"`

	RiskScore     int    `gorm:"not null;default:0;index:idx_qy_ir_risk" json:"risk_score"`
	RiskFlags     string `gorm:"type:varchar(255);not null;default:''" json:"risk_flags"`
	Blocked       bool   `gorm:"not null;default:false" json:"blocked"` // 管理员拉黑该邀请关系，不再计佣
	BlockedReason string `gorm:"type:varchar(255);not null;default:''" json:"blocked_reason"`

	CreatedAt int64 `gorm:"autoCreateTime;not null" json:"created_at"`
	UpdatedAt int64 `gorm:"autoUpdateTime;not null" json:"updated_at"`
}
func (InviteRelation) TableName() string { return "qy_invite_relation" }
```

> 项目**没有**邀请关系绑定时间列（`users.inviter_id` 是注册时一次性写入，见 `controller/user.go:263-275`），故 `BoundAt` 只能取 `users.created_at`。这是数据事实，写进设计避免后续返工。

### 1.6 `qy_commission_setting` — 运营级配置（管理端可改）

```go
type CommissionSetting struct {
	Key        string `gorm:"type:varchar(64);primaryKey" json:"key"`
	Value      string `gorm:"type:varchar(1024);not null;default:''" json:"value"`
	OperatorId int    `gorm:"not null;default:0" json:"operator_id"`
	UpdatedAt  int64  `gorm:"autoUpdateTime;not null" json:"updated_at"`
}
func (CommissionSetting) TableName() string { return "qy_commission_setting" }
```

键位：`settle_mode`(auto|manual)、`holding_days`、`min_settle_quota`、`clawback_policy`(debt|ignore)、`fiat_currency`、`risk.min_invitee_age_hours`、`risk.bind_window_days`、`risk.max_daily_quota_per_inviter`、`risk.max_total_quota_per_invitee`、`risk.burst_invitee_per_day`、`risk.auto_hold`、`cap_overflow_policy`(carry|void)、`exclude_user_ids`。

**为什么不放 YAML**：这些要管理员在 UI 改，YAML 改需重启。**为什么不进 `setting/config.GlobalConfig`**：它持久化到原项目主库 `options` 表（`model/option.go:214`），违背独立库约束（架构 §3）。

### 1.7 辅助表

```go
// 管理员补单标记（D4 exclude_redemption_and_manual 的判据来源，见 §4.3）
type ManualTopUpMark struct {
	TradeNo  string `gorm:"type:varchar(191);primaryKey" json:"trade_no"`
	UserId   int    `gorm:"not null;default:0" json:"user_id"`
	MarkedAt int64  `gorm:"autoCreateTime;not null" json:"marked_at"`
}
func (ManualTopUpMark) TableName() string { return "qy_manual_topup_mark" }

// 充值重扫低水位（GAPS §三.2(6)）
type ScanCursor struct {
	Name       string `gorm:"type:varchar(64);primaryKey" json:"name"`
	LowWaterId int64  `gorm:"not null;default:0" json:"low_water_id"`
	LastScanAt int64  `gorm:"not null;default:0" json:"last_scan_at"`
	UpdatedAt  int64  `gorm:"autoUpdateTime;not null" json:"updated_at"`
}
func (ScanCursor) TableName() string { return "qy_scan_cursor" }

// 后台任务分布式租约（架构 §8：不写主库，新库自建）
// 若基础设施模块已定义同名表则复用，勿重复 AutoMigrate。
type TaskLease struct {
	Name      string `gorm:"type:varchar(64);primaryKey" json:"name"`
	Owner     string `gorm:"type:varchar(96);not null;default:''" json:"owner"` // common.NodeName + pid
	ExpiresAt int64  `gorm:"not null;default:0;index:idx_qy_tl_exp" json:"expires_at"`
	Version   int64  `gorm:"not null;default:0" json:"version"`
	UpdatedAt int64  `gorm:"autoUpdateTime;not null" json:"updated_at"`
}
func (TaskLease) TableName() string { return "qy_task_lease" }
```

---

## 2. 精度方案 —— 解决 GAPS §一.10（小额消费返佣归零）

### 2.1 问题复述
`quota` 是 int32，单次对话常见 20~500 quota。5% 佣金 = 1~25 quota，但 `10 × 0.05 = 0.5`，`int()` 截断为 **0**。一天几千次请求全部归零，用户看到"用了一天没佣金"。

### 2.2 三段式解法

**① 计佣阶段：全精度 decimal，永不截断**

```go
// qianye/service/commission/accrual.go
func calcGross(baseQuota int64, rateBps int) decimal.Decimal {
	return decimal.NewFromInt(baseQuota).
		Mul(decimal.NewFromInt(int64(rateBps))).
		Div(decimal.NewFromInt(10000))          // bps → 比例，无精度损失
}
```
落库到 `qy_commission_accrual.gross_amount decimal(30,10)`。**这一步绝不调用 `common.QuotaFromDecimal`**（它 `Round(0)` 会把 0.5 变成 1 或把 0.4 变成 0）。

**② 聚合阶段：消费按 (被邀请人, 自然日) 日聚合，原子 upsert**

单条消费不落一行（relay QPS 量级会撑爆表），而是 upsert 到日桶：

```go
db.Clauses(clause.OnConflict{
	Columns: []clause.Column{{Name: "source_type"}, {Name: "source_no"}},
	DoUpdates: clause.Assignments(map[string]interface{}{
		"base_quota":   gorm.Expr("qy_commission_accrual.base_quota + VALUES(base_quota)"),
		"gross_amount": gorm.Expr("qy_commission_accrual.gross_amount + VALUES(gross_amount)"),
		"updated_at":   now,
	}),
}).Create(&row)   // source_no = fmt.Sprintf("%d:%s", inviteeId, "20260730")
```
（范式与 `model/perf_metric.go` 的 `UpsertPerfMetric` 完全一致。）
行数上界 = 活跃下线数 × 天数，**与 QPS 无关**。同时保留了"这天这个下线消费了多少、返了多少"的可审计粒度。

**③ 结算阶段：余数按邀请人挂账，floor 入账**

结算任务每 `settle_interval_seconds`（默认 300s）执行，**单个邀请人一个事务**：

```
delta   = Σ (accrual.gross_amount - accrual.settled_amount)   // 只取 status=accrued 且 mature_at<=now
carry0  = balance.unsettled_amount                            // 上次剩下的余数
total   = carry0 + delta
grant   = floor(total)                                        // ★ floor 不是 round：绝不超发
if grant > 0 && grant < min_settle_quota  { grant = 0 }        // 门槛内也不丢，留在 carry
carry1  = total - grant                                        // ★ 余数回写，永不丢失
```

`floor` 与项目强制的安全转换的衔接（AGENTS.md 禁止裸 `int()`）：

```go
floored := total.Floor()                                   // decimal.Floor，精确
grantInt, clamp := common.QuotaFromDecimalChecked(floored)  // 整数已 floor，Round(0) 为恒等；仅做 int32 饱和
if clamp != nil {
	// 记 audit（clamp.AuditMap()）+ 告警：单次结算触顶 MaxInt32，需人工介入
}
```

**数值走查**：rate=5%，每次消费 10 quota。
- 第 1~19 次：`carry` 从 0.5 累到 9.5，`grant=0`（但**没有丢失**）。
- 第 20 次：`carry=10.0` → `grant=10`，`carry=0`。
- 一天 2000 次：`total=1000.0` → 全额入账 1000 quota。**零损耗**。

对比裸 `int()`：2000 次全部归零，损耗 100%。

### 2.3 为什么要 `settled_amount` 而不是 status 翻转
日桶行在当天是"开放"的（还会继续累加）。若结算靠 `status: accrued→settled` 翻转，则当天的佣金必须等到次日才能结算（用户体验差）。用 `settled_amount` 记录"已被吸收多少"，结算取增量 `gross - settled`，同一行可被多次安全结算，做到**准实时**（延迟 = flush 间隔 + 结算间隔 ≈ 5 分钟）。

⚠️ **必须写"读到的那个值"而不是 `gross_amount`**（§10.2 会展开）：
```sql
UPDATE qy_commission_accrual
   SET settled_amount = :gross_read, settlement_id = :sid
 WHERE id = :id AND settled_amount = :settled_read;   -- CAS，防丢失更新
```

---

## 3. 热路径与邀请人缓存 —— 解决 GAPS §一.11

### 3.1 硬约束
`model.RecordConsumeLog` 是**同步调用**（`service/text_quota.go:526`），跑在 relay 全量流量上。裸查 `users.inviter_id` 会给主库带来与 relay QPS 等量的读压力。

### 3.2 方案：纯内存 LRU + TTL + 负缓存 + singleflight

**明确否决使用 `pkg/cachex.HybridCache`**：它在 `common.RedisEnabled` 时走 Redis，等于给每次消费加一次网络往返。邀请关系是**注册时一次性写入、之后几乎不变**的数据，per-node 内存缓存 + TTL 完全够用。

```go
// qianye/service/commission/inviter_cache.go
var inviterCache *hot.HotCache[int, int]   // key=userId, value=inviterId（0 = 无邀请人，即负缓存）
var inviterSF   singleflight.Group          // golang.org/x/sync 已是直接依赖，零新增

func initInviterCache(cfg Config) {
	inviterCache = hot.NewHotCache[int, int](hot.LRU, cfg.InviterCacheSize).   // 默认 200000
		WithTTL(time.Duration(cfg.InviterCacheTTLSeconds) * time.Second).       // 默认 600s
		WithJanitor().
		Build()
}
```
（`hot` 来自 `github.com/samber/hot v0.11.0`，已是 `go.mod` 直接依赖，`service/channel_affinity.go:100-105` 有现成用法可照抄。）

**负缓存是关键**：绝大多数用户 `inviter_id = 0`。把 `0` 当作**正常值**缓存，命中即 `return`，永不回源。

### 3.3 热路径分级：钩子只做"查内存 + 非阻塞投递"

```go
// model.QyOnConsumeLog 的实现（跑在 relay goroutine 上）
func onConsumeLog(c *gin.Context, userId int, p model.RecordConsumeLogParams) {
	defer recoverAndLog()                       // ★ 任何 panic 不得污染主链路

	if !enabled.Load() || !consumeRebateEnabled { return }   // atomic bool，1ns
	if p.Quota <= 0 { return }
	if isHardExcluded(p) { return }              // 违规扣费 / 渠道测试，见 §4.2

	if inviterId, ok, _ := inviterCache.Get(userId); ok {
		if inviterId == 0 { return }             // ★ 负缓存命中：最常见路径，到此为止
		enqueue(consumeEvent{Inviter: inviterId, Invitee: userId, ...})
		return
	}
	// 缓存 miss：不在此处查库，投递 "未解析" 事件，由聚合器解析
	enqueue(consumeEvent{Inviter: -1, Invitee: userId, ...})
}

func enqueue(ev consumeEvent) {
	select {
	case eventCh <- ev:                          // 有缓冲 chan，默认 65536
	default:
		droppedCounter.Add(1)                    // ★ fail-open：满了就丢，绝不阻塞 relay（架构 §4）
	}
}
```

热路径开销：一次 atomic load + 一个 map 查找 + （少数情况）一次 chan send。**无锁、无 I/O、无主库访问**。

聚合器 goroutine（后台）负责：
1. `Inviter == -1` 的事件 → `inviterSF.Do(key)` 合并并发回源 → `model.DB.Model(&model.User{}).Select("inviter_id").Where("id = ?", uid)` → 写回缓存（含 0）。**singleflight 防缓存击穿**：同一用户 1000 个并发 miss 只打 1 次库。
2. 内存按 `(inviterId, inviteeId, yyyymmdd)` 累加 `baseQuota int64` + `gross decimal`。
3. 每 `consume_flush_interval_seconds`（默认 30s）批量 upsert 到 `qy_commission_accrual`。

### 3.4 缓存失效
- TTL 到期自然失效（10 分钟，邀请关系变更容忍度足够）。
- 新用户注册后立刻消费：缓存 miss → 回源 → 正确。
- 管理员改 `users.inviter_id`（`controller/user.go` 的用户编辑）：提供 `POST /api/qy/admin/commission/cache/invalidate` 手动失效；同时后台 `SyncInviteRelations` 任务（每 10 分钟）扫描 `users` 增量（`WHERE id > cursor OR updated 时间窗`）刷新 `qy_invite_relation` 并 purge 对应缓存键。

---

## 4. 触发点设计（D4 宽松口径 + 三开关 + 硬排除）

### 4.1 口径总表

| 来源 | 默认（宽松） | 受控开关 | 判据 |
|---|---|---|---|
| 六条真实支付充值（epay/stripe/creem/waffo/waffo_pancake/管理员补单） | **返** | — | `top_ups.status='success'` |
| 订阅付费购买（stripe/creem/epay/pancake） | **返** | — | `CompleteSubscriptionOrder` 会 `upsertSubscriptionTopUpTx` 写 `top_ups` 行（`model/subscription.go:643-677`），自动被捕获 |
| 兑换码充值 | **返** | `exclude_redemption_and_manual` | `model/redemption.go:184` hook |
| 管理员补单 | **返** | `exclude_redemption_and_manual` | `qy_manual_topup_mark` 标记 |
| 余额购订阅 | **不返（硬排除）** | — | 不产生 `top_ups` 行（`model/subscription.go:796-810` 只建 `SubscriptionOrder`）；且余额在充值时已返过，再返 = 双重出血 |
| 普通消费（text/audio/wss/task/mj） | **返** | — | `RecordConsumeLog` / `RecordTaskBillingLog(LogTypeConsume)` |
| 订阅额度消费 | **返** | `exclude_subscription_consume` | `other.billing_source == "subscription"` |
| **违规扣费产生的消费日志** | **永不返（无开关）** | — | `other.violation_fee == true`（`service/violation_fee.go:139`） |
| 渠道测试消费 | **永不返（无开关，建议）** | — | `TokenName=="模型测试" && TokenId==0`（`controller/channel-test.go:505`）+ `exclude_user_ids` 兜底 |
| 退款 / 任务退费 | 冲正 | `refund_clawback` | `RecordTaskBillingLog(LogTypeRefund)` |

### 4.2 硬排除判定代码

```go
func isHardExcluded(p model.RecordConsumeLogParams) bool {
	if v, ok := p.Other["violation_fee"].(bool); ok && v { return true }  // D4 无条件硬排除
	if p.TokenId == 0 && p.TokenName == "模型测试" { return true }          // 渠道测试（建议）
	return false
}
func isSubscriptionConsume(p model.RecordConsumeLogParams) bool {
	s, _ := p.Other["billing_source"].(string)
	return s == "subscription"    // appendBillingInfo，service/log_info_generate.go:161
}
```

> ⚠️ 不要用 `other.wallet_quota_deducted > 0` 判钱包消费：该键**只在订阅分支被写为 0**（`service/log_info_generate.go:205`），钱包分支根本不写这个键，取零值会把所有钱包消费误排除。

### 4.3 充值返佣的挂载方式：GORM callback + 低水位重扫（**原项目 0 行改动**）

见 §5 完整方案。管理员补单需要 1 行标记 hook（§9 表）。

### 4.4 消费返佣的挂载点

唯一覆盖全部消费路径的单点是 `model/log.go:343 RecordConsumeLog`（覆盖 `text_quota.go:526`、`quota.go:245/368`、`task_billing.go:55`、`mjproxy_handler.go:245/551`、`violation_fee.go:150`）。
**必须插在 `:344` 的 `if !common.LogConsumeEnabled { return }` 之前**，否则管理员关闭消费日志时返佣静默失效。

另需覆盖 `model/log.go:419 RecordTaskBillingLog`（任务差额补扣 `LogTypeConsume` + 退款 `LogTypeRefund`），同样插在 `:420` 早退之前。

---

## 5. 充值漏单方案 —— 解决 GAPS §三.2(6)

### 5.1 为什么纯轮询不成立（复核结论）
- `TopUp` 无 `UpdatedAt`（`model/topup.go:14-25`）。
- epay 路径**从不写 `CompleteTime`**（`controller/topup.go:385-408` 只改 `Status`，`CompleteTime` 恒 0）。
- 订单先 `pending` 插入、后转 `success`，纯 `id > cursor` 必然漏单。

### 5.2 方案：**实时 GORM callback（主）+ 低水位重扫（兜底）+ 唯一索引（去重）**

**(a) 实时信号 —— 原文件 0 行改动**

`qianye.Init()` 里向主库注册回调（`model.InitDB()` 在 `main.go:307`，早于 `qianye.Init()` 的 `:365+`，顺序安全）：

```go
_ = model.DB.Callback().Create().After("gorm:create").Register("qy:topup_created", onTopUpWrite)
_ = model.DB.Callback().Update().After("gorm:update").Register("qy:topup_updated", onTopUpWrite)

func onTopUpWrite(tx *gorm.DB) {
	defer recoverAndLog()                          // ★ 绝不能 panic 掉主项目的写
	var tradeNo string
	switch d := tx.Statement.Dest.(type) {
	case *model.TopUp: tradeNo = d.TradeNo
	case model.TopUp:  tradeNo = d.TradeNo
	default:           return                       // 非 TopUp 写入，最快路径退出
	}
	if tradeNo == "" { return }
	select { case topupSignalCh <- tradeNo: default: }   // 非阻塞
}
```

已核对：**全部写路径**（`topUp.Update()`→`DB.Save`、`Recharge`/`RechargeCreem`/`RechargeWaffo`/`RechargeWaffoPancake`/`ManualCompleteTopUp`/`UpdatePendingTopUpStatus` 的 `tx.Save(topUp)`、`upsertSubscriptionTopUpTx` 的 `tx.Create/tx.Save`）`Statement.Dest` 恒为 `*model.TopUp` / `model.TopUp`。

⚠️ **回调在外层事务提交之前触发**，所以它**只是唤醒信号，不是数据源**。

**(b) 确认读 —— 数据源是重新读出来的行**

worker 收到 trade_no 后，延迟 `recharge_grace_seconds`（默认 10s，避开未提交窗口 + 管理员补单标记写入窗口），然后 `model.GetTopUpByTradeNo(tradeNo)` 重读，校验 `Status == common.TopUpStatusSuccess` 才计佣。读到 pending 就按 `2s → 5s → 15s → 60s` 退避重试 4 次，仍不成功则丢弃（交给重扫）。

**(c) 兜底重扫 —— 低水位算法**

`qy_scan_cursor` 存 `low_water_id`。每 `recharge_rescan_interval_seconds`（默认 120s，master + 新库租约）执行：

```
1. rows = SELECT id,user_id,amount,money,trade_no,payment_method,payment_provider,create_time,status
            FROM top_ups WHERE id > :low_water ORDER BY id LIMIT 2000        -- id 是 PK，range 扫描
2. 对 status='success' 的行执行计佣（唯一索引天然去重）
3. minPending = min(id) among rows where status='pending' AND create_time >= now - window_hours*3600
   -- create_time 早于窗口的 pending 视为已死单（epay 订单会 expire），不再守候
4. new_low_water = minPending 若存在，否则 max(scanned id)
5. UPDATE qy_scan_cursor SET low_water_id = new_low_water
```
`window_hours` 默认 72。重扫成本 = 窗口内未决订单之后的行数，**不随历史订单总量增长**（这正是 GAPS 指出的 O(N) 问题的解）。

**(d) 去重**：`uk_qy_ca_src(source_type='recharge', source_no=trade_no)` + `clause.OnConflict{DoNothing:true}` + 检查 `RowsAffected`。三条路径（回调、重扫、管理员手动 rescan）任意重叠都不会重复返佣。

### 5.3 基数计算：**按 provider 分派（易踩的坑）**

各充值路径的 quota 计算方式**不一致**，不能统一 `Amount × QuotaPerUnit`：

| provider | 到账 quota 计算 | 出处 |
|---|---|---|
| `stripe` | `Money × QuotaPerUnit` | `model/topup.go:143` |
| `creem` | **`Amount` 直接就是 quota** | `model/topup.go:427` |
| `epay` / `waffo` / `waffo_pancake` | `Amount × QuotaPerUnit` | `controller/topup.go:398-400`、`model/topup.go:498-500`、`:561` |
| 管理员补单 | stripe→`Money×QPU`，其余→`Amount×QPU` | `model/topup.go:354-361` |
| 订阅付费单（`payment_provider` 为**空串**，`Amount=0`） | `Money × QuotaPerUnit` | `model/subscription.go:651-660` |

```go
func topUpBaseQuota(t *model.TopUp) (int64, decimal.Decimal) {
	qpu := decimal.NewFromFloat(common.QuotaPerUnit)
	switch t.PaymentProvider {
	case model.PaymentProviderCreem:
		return t.Amount, decimal.NewFromFloat(t.Money)
	case model.PaymentProviderStripe, "":            // "" = 订阅付费单
		return decimal.NewFromFloat(t.Money).Mul(qpu).IntPart(), decimal.NewFromFloat(t.Money)
	default:
		return decimal.NewFromInt(t.Amount).Mul(qpu).IntPart(), decimal.NewFromFloat(t.Money)
	}
}
```
若 `baseQuota <= 0` → 直接跳过（订阅单 `Amount=0` 但 `Money>0`，走 `""` 分支正确）。

---

## 6. 退款冲正（D4 `refund_clawback`）

### 6.1 冲正来源盘点（代码事实）

| 退款路径 | 是否已发过佣 | 冲正做法 |
|---|---|---|
| `BillingSession.Refund`（`service/billing_session.go:82`，请求失败退预扣） | **否** —— 失败请求根本不走 `RecordConsumeLog` | 无需处理 |
| `RefundTaskQuota`（`service/task_billing.go:166`→`RecordTaskBillingLog` LogTypeRefund） | **是** —— `LogTaskConsumption`（`task_billing.go:55`）已计佣 | `QyOnTaskBillingLog` 钩子自动冲正 |
| `RecalculateTaskQuota` 负 delta（`task_billing.go:253`） | 是 | 同上 |
| `controller/midjourney.go:222` LogTypeRefund | 是 | 同上 |
| 充值退款 / 拒付 | 项目**无此代码路径** | 仅管理端手动冲正 API |

### 6.2 冲正记录设计
冲正**永远是一条独立的负额 accrual 行**（不去改原行），保证账本 append-only：

```
source_type = 'clawback'
source_no   = "CB:{taskId}:{refundQuota}"   // taskId 来自 other["task_id"]（task_billing.go:184/257 均已写入）
            = "CB:U{uuid}"                   // 无 task_id 时钩子处生成，保证 worker 重试幂等
            = "CB:M{adminOpId}"              // 管理端手动冲正
gross_amount = -1 × min(原单已发佣金, 按退款基数×当时费率算出的佣金)
ref_accrual_id = 原 accrual.Id（消费类指向该下线该日的日桶行）
rate_bps / usd_rate = ★ 复制原单的值，不用当前值
```
负额行与正额行**在结算里走完全相同的路径**（`delta = gross - settled`），所以余数、封顶、审计全部自动一致。

### 6.3 佣金已被提现时怎么办（**必答项**）

结算事务内，当 `total = carry + delta < 0`：

```
1. shortfall = ceil(-total)                                   // 需要回收的整数额度
2. reclaim   = min(balance.available_quota, shortfall)        // ★ 只从"未提现的可用余额"回收
   available_quota -= reclaim
   available_fiat  *= (available_quota_new / available_quota_old)   // 按比例缩减，保持冻结汇率均值一致
   total           += reclaim
   total_clawback_quota += reclaim
3. 若 total 仍 < 0：
   - clawback_policy = "debt"（默认）：unsettled_amount 保持负值 = 欠账；
     置 balance.debt_blocked = true → 提现模块必须拒绝提现（契约见 §10.6）；
     未来新佣金自动优先抵扣欠账，抵清后自动解除 debt_blocked。
   - clawback_policy = "ignore"：把负数截断为 0，记 audit 日志 + 管理端告警（用于不想追讨的运营策略）。
4. 绝不触碰主库 users.quota。
```

**理由**：佣金一旦提现进平台余额，可能已被消费，主库倒扣会造成用户余额意外变负（`DecreaseUserQuota` 无余额校验，`model/user.go:1274`）。欠账模型把损失限制在"未来佣金"上，可控且不惊扰用户。

---

## 7. 完整 API 清单

统一前缀 `/api/qy/`，由 `qianye.RegisterRoutes(server *gin.Engine)` 在 `main.go:196`（**`router.SetRouter` 之前**）挂载。响应体统一 `{success, message, data}`。所有金额型 decimal 字段以**字符串**下发（避免 JS `Number` 精度丢失）。

### 7.1 用户端（`middleware.UserAuth()`）

| # | Method | Path | 说明 |
|---|---|---|---|
| U1 | GET | `/api/qy/affiliate/summary` | 我的邀请看板 |
| U2 | GET | `/api/qy/affiliate/invitees` | 已邀请用户列表（脱敏） |
| U3 | GET | `/api/qy/affiliate/commissions` | 我的佣金流水 |
| U4 | GET | `/api/qy/affiliate/trend` | 近 N 天佣金趋势（图表） |

**U1** `GET /api/qy/affiliate/summary`
```jsonc
// data
{
  "enabled": true,
  "compliance_ok": true,                 // operation_setting.IsPaymentComplianceConfirmed()
  "aff_code": "a1b2",
  "invitee_count": 12,
  "paying_invitee_count": 5,
  "available_quota": 1250000,            // 可提现（整数 quota）
  "frozen_quota": 0,                     // 提现审核中
  "withdrawn_quota": 500000,
  "total_earned_quota": 1750000,         // 累计已结算
  "total_clawback_quota": 0,
  "unsettled_amount": "3.4271000000",    // ★ 未结算余数（精确字符串）
  "pending_mature_quota": 42000,         // 未过成熟期的 accrual 合计（floor）
  "pending_review_quota": 0,             // settle_mode=manual 时待审核合计
  "debt_blocked": false,
  "available_fiat": "9.125000", "fiat_currency": "CNY",
  "rate": { "recharge_bps": 500, "consume_bps": 300, "source": "group" },
  "policy": { "settle_mode": "auto", "holding_days": 0, "min_settle_quota": 1,
              "min_withdraw_quota": 500000, "next_settle_at": 1785312000 }
}
```

**U2** `GET /api/qy/affiliate/invitees?p=1&page_size=20&sort=commission_desc`
```jsonc
{ "total": 12, "items": [
  { "ref": "a3f91c7d",                  // ★ 不下发 user_id
    "masked_name": "zh***ng",           // ★ 服务端脱敏，见 §11.3
    "bound_at": 1750000000,
    "first_recharge_at": 1750100000,
    "total_base_quota": 12000000,
    "total_commission_quota": 600000,
    "status": "active" }                 // active|inactive|blocked
]}
```
排序白名单：`bound_at_desc|commission_desc|base_desc`。**不下发** email / 真实用户名 / user_id / IP。

**U3** `GET /api/qy/affiliate/commissions?p=&page_size=&source_type=&status=&start_ts=&end_ts=`
```jsonc
{ "total": 88, "items": [
  { "accrual_no": "CA26073000012345",
    "source_type": "recharge",           // recharge|redemption|consume|clawback
    "source_ref": "****4821",            // ★ trade_no 脱敏（属于下线订单隐私）
    "invitee_ref": "a3f91c7d", "invitee_masked_name": "zh***ng",
    "base_quota": 5000000, "rate_bps": 500,
    "gross_amount": "250000.0000000000",
    "status": "settled",                 // pending_review|risk_hold|accrued|settled|voided|rejected
    "settle_no": "ST26073000009", "mature_at": 0, "created_at": 1753900000 }
]}
```

**U4** `GET /api/qy/affiliate/trend?days=30` → `[{date, base_quota, gross_amount, granted_quota}]`

### 7.2 管理端（`middleware.AdminAuth()` — 挂上即自动留审计，`middleware/auth.go:68-75`）

| # | Method | Path | 说明 |
|---|---|---|---|
| A1 | GET | `/api/qy/admin/commission/settings` | 读运营配置 + YAML 只读快照 |
| A2 | PUT | `/api/qy/admin/commission/settings` | 改运营配置（YAML 三开关只读，返回 `readonly:true`） |
| A3 | GET | `/api/qy/admin/commission/rates` | 费率列表（全局/分层/分组/按用户） |
| A4 | POST | `/api/qy/admin/commission/rates` | upsert 费率 |
| A5 | DELETE | `/api/qy/admin/commission/rates/:id` | 删费率 |
| A6 | GET | `/api/qy/admin/commission/accruals` | 佣金/审核队列（多条件筛选） |
| A7 | POST | `/api/qy/admin/commission/accruals/review` | 批量审核（approve/reject） |
| A8 | POST | `/api/qy/admin/commission/clawback` | 人工冲正 |
| A9 | GET | `/api/qy/admin/commission/balances` | 佣金余额列表（可按 debt_blocked 筛） |
| A10 | GET | `/api/qy/admin/commission/relations` | 邀请关系 + 风控视图 |
| A11 | POST | `/api/qy/admin/commission/relations/block` | 拉黑/解封某条邀请关系 |
| A12 | GET | `/api/qy/admin/commission/stats` | 总览（今日/本月计佣、发放、冲正、欠账、丢弃事件数） |
| A13 | POST | `/api/qy/admin/commission/rescan` | 手动触发充值重扫（指定 trade_no 或 id 区间） |
| A14 | POST | `/api/qy/admin/commission/settle` | 手动触发结算（指定 user_id 或全量） |
| A15 | POST | `/api/qy/admin/commission/cache/invalidate` | 失效邀请人缓存（改 inviter_id 后用） |
| A16 | GET | `/api/qy/admin/commission/health` | 新库连通性、事件丢弃数、队列水位、租约持有者、低水位游标 |

**A2** 请求 / 响应
```jsonc
// PUT body
{ "settle_mode": "manual", "holding_days": 7, "min_settle_quota": 1,
  "clawback_policy": "debt", "fiat_currency": "CNY",
  "risk": { "min_invitee_age_hours": 24, "bind_window_days": 0,
            "max_daily_quota_per_inviter": 5000000, "max_total_quota_per_invitee": 0,
            "burst_invitee_per_day": 20, "auto_hold": true },
  "cap_overflow_policy": "carry", "exclude_user_ids": [1] }
// data 额外回带（只读，来自 YAML）
{ "yaml_readonly": { "recharge_rebate_enabled": true, "consume_rebate_enabled": true,
    "exclude_redemption_and_manual": false, "exclude_subscription_consume": false,
    "refund_clawback": false } }
```

**A7** `POST /accruals/review`
```jsonc
{ "ids": [101,102], "action": "approve", "reason": "" }   // action: approve|reject
// approve → status: pending_review|risk_hold → accrued（下个结算周期入账）
// reject  → status → rejected，gross 不计入余额（幂等：已 settled 的返回 409）
```

**A8** `POST /clawback`
```jsonc
{ "accrual_id": 101, "quota": 25000, "reason": "chargeback #TX123", "idempotency_key": "adm-2026-0730-01" }
// 生成 source_no = "CB:M{idempotency_key}" 的负额 accrual，唯一索引保证重复提交无副作用
```

### 7.3 权限与限流
- 用户端全部只读 → `middleware.UserAuth()`；U2/U3 加 `middleware.SearchRateLimit()`。
- 管理端写接口（A2/A4/A5/A7/A8/A11/A13/A14/A15）加 `middleware.CriticalRateLimit()`。
- **优雅降级（架构 §4）**：`config.Enabled()==false` → 全部路由不注册（前端 404 → 入口隐藏）。新库连不上 → 用户端 GET 返回 `503 + {success:false, message:"qy_commission_unavailable"}`（**非热路径，允许 503**）；relay 热路径钩子只丢事件 + 计数，永不报错。

---

## 8. 关键流程（编号步骤 · 标注事务边界 / 加锁点 / 幂等键 / 回滚路径）

### 8.1 充值返佣

```
① [主库·他人事务内] 支付回调更新 top_ups → GORM callback 捕获 trade_no
   └ 非阻塞投递到 topupSignalCh；chan 满 → 丢弃（由 ⑦ 兜底）
② [worker] 延迟 recharge_grace_seconds(10s) 后，model.GetTopUpByTradeNo(trade_no) 重读
   ├ status != success → 退避重试 2s/5s/15s/60s，仍失败则丢弃（由 ⑦ 兜底）
   └ 【事务边界：无。这是主库只读】
③ [worker] 口径判定
   ├ exclude_redemption_and_manual=true 且 qy_manual_topup_mark 命中 trade_no → 丢弃
   └ baseQuota = topUpBaseQuota(t)（§5.3 provider 分派）；<=0 → 丢弃
④ [worker] 解析邀请人：inviterCache → miss 则 singleflight 查 users.inviter_id
   └ inviter==0 / inviter==userId → 丢弃
⑤ [worker] 风控（§12）：qy_invite_relation.Blocked / min_invitee_age_hours / 单下线累计封顶
   └ 命中 auto_hold → status='risk_hold'；否则 settle_mode=manual → 'pending_review'，auto → 'accrued'
⑥ [新库·单条 INSERT] 写 qy_commission_accrual
   ├ 【幂等键：uk_qy_ca_src(source_type='recharge', source_no=trade_no)】
   ├ clause.OnConflict{DoNothing:true}；RowsAffected==0 → 已处理过，静默返回
   ├ gross = calcGross(baseQuota, rateBps)（全精度 decimal，不截断）
   ├ usd_rate = decimal(operation_setting.USDExchangeRate)  ★ 冻结当时汇率（D1）
   └ mature_at = now + holding_days*86400
   【事务边界：单条 INSERT 自成事务；失败 → 重试 3 次，仍失败则丢弃，由 ⑦ 兜底】
⑦ [兜底] 低水位重扫（§5.2c），master + 新库租约，每 120s，唯一索引去重
```

### 8.2 消费返佣

```
① [relay goroutine·model.RecordConsumeLog 第一行] QyOnConsumeLog
   ├ 开关关 / Quota<=0 / violation_fee / 渠道测试 → return（≈20ns）
   ├ inviterCache.Get(userId) 命中 0 → return（最常见路径，无 I/O）
   └ 投递 consumeEvent 到 chan（满则丢弃 + 计数）  【无锁 · 无 I/O · 无事务】
② [聚合器 goroutine] 逐事件处理
   ├ Inviter==-1 → singleflight 回源 users.inviter_id → 写缓存（含负缓存 0）
   ├ exclude_subscription_consume=true 且 other.billing_source=="subscription" → 丢弃
   └ 内存 map[(inviter,invitee,yyyymmdd)] 累加 baseQuota(int64) + gross(decimal)
③ [聚合器] 每 30s flush
   └ [新库] 批量 upsert qy_commission_accrual
      【幂等键：uk_qy_ca_src(source_type='consume', source_no="{inviteeId}:{yyyymmdd}")】
      ON DUPLICATE KEY UPDATE base_quota = base_quota + VALUES(...), gross_amount = gross_amount + VALUES(...)
      【回滚路径：flush 失败 → 内存 map 不清空，下轮重试；连续失败超阈值则丢弃最旧桶 + 告警】
      ★ 注意：flush 失败必须保留内存态，清空后重试才是数据丢失
④ [进程退出] 优雅关闭时强制 flush 一次（挂在 main.go 的 srv.Shutdown 之后不现实，
   故在 StartBackgroundTasks 内注册 signal handler，或接受最多 30s 数据丢失 —— 默认接受，
   因为 ⑦ 无法为消费兜底。若不可接受，把 flush 间隔调到 5s）
```

### 8.3 结算（余数 floor 入账）—— **加锁与事务的核心**

```
① [master + 新库租约 "commission_settle"] 每 settle_interval_seconds(300s)
② 选取候选邀请人：
   SELECT DISTINCT inviter_id FROM qy_commission_accrual
    WHERE status='accrued' AND mature_at<=now AND settled_amount < gross_amount
    LIMIT 500                                     -- 分批，避免长事务
③ 【对每个邀请人开一个新库事务】
   3.1 rows = SELECT id, gross_amount, settled_amount, usd_rate
                FROM qy_commission_accrual
               WHERE inviter_id=? AND status='accrued' AND mature_at<=now
                 AND settled_amount < gross_amount
               ORDER BY id LIMIT 1000              -- 一次最多吸收 1000 行
   3.2 delta = Σ(gross_i - settled_i)；weightedRate = Σ(delta_i × rate_i)/delta
   3.3 balance = SELECT * FROM qy_commission_balance WHERE user_id=? FOR UPDATE
       【★ 唯一加锁点。提现模块必须用同一把锁（§10.6 契约）】
       不存在则 INSERT（ON DUPLICATE KEY 忽略后重读）
   3.4 total = balance.unsettled_amount + delta
       grant  = total.Floor()                      -- ★ floor，绝不 round
       日封顶：remain = max_daily_quota_per_inviter - 今日已发；grant>remain → clipped=grant-remain; grant=remain
              （cap_overflow_policy=carry → clipped 留在 carry；=void → 生成 voided 负额 accrual）
       if 0 < grant < min_settle_quota { grant = 0 }
       grantInt, clamp := common.QuotaFromDecimalChecked(grant)   -- int32 饱和保护 + audit
   3.5 若 grantInt >= 0：
         available_quota += grantInt
         available_fiat  += grantInt / QuotaPerUnit × weightedRate     ★ 冻结汇率折算
         total_earned_quota += grantInt
       若 grantInt < 0（冲正回收，见 §6.3）：
         reclaim = min(available_quota, -grantInt)
         available_quota -= reclaim
         available_fiat  *= (available_new / available_old)            ★ 按比例缩减
         total_clawback_quota += reclaim
         grantInt = -reclaim
       unsettled_amount = total - decimal(grantInt)   -- ★ 余数回写，永不丢失
       debt_blocked = (unsettled_amount < 0)
       version += 1
   3.6 逐行 CAS 回写：
       UPDATE qy_commission_accrual
          SET settled_amount = :gross_read, settlement_id = :sid, status = 'settled'
        WHERE id = :id AND settled_amount = :settled_read
       【★ 必须写"读到的 gross"而非 gross_amount 列本身 —— 见 §10.2】
       status 只在 gross_read == 当前 gross 时才置 settled；否则保持 accrued（行还在增长）
   3.7 INSERT qy_commission_settlement（carry_before / carry_after / granted / clipped / weighted_rate）
   3.8 UPDATE qy_invite_relation 累计（total_commission_quota）
   【事务提交】
④ 【回滚路径】事务任一步失败 → 整体回滚。accrual 的 settled_amount 未变，
   下轮结算自然重算（幂等）。不会漏发也不会重发。
⑤ 【中间态】结算全程只在新库，无跨库两阶段 —— 这是本模块的重要安全属性。
   跨库两阶段只发生在"提现兑现"（提现模块负责）。
```

### 8.4 退款冲正

```
① [model.RecordTaskBillingLog 第一行] QyOnTaskBillingLog(params)
   ├ refund_clawback=false → return
   ├ LogType==LogTypeConsume → 走 §8.2 消费返佣（正向）
   └ LogType==LogTypeRefund  → 生成 clawbackEvent
      source_no = "CB:" + other["task_id"] + ":" + quota  （无 task_id → "CB:U"+uuid，钩子处生成，worker 重试复用）
② [worker] 解析 inviter（同 §8.2④）→ 查该 invitee 最近的可冲正 accrual
   → 冲正额 = min(该 invitee 累计已计佣, refundQuota × 原 rate_bps / 10000)
③ [新库·单条 INSERT] status='accrued', gross_amount = -X, ref_accrual_id = 原单 id,
   rate_bps / usd_rate 复制原单值
   【幂等键：uk_qy_ca_src('clawback', source_no)】
④ 下个结算周期由 §8.3 自动吸收（负额走完全相同路径）
```

---

## 9. 原项目改动清单（预算核算依据）

### 9.1 本模块独占（**3 个文件 / 4 行**）

| # | 文件:行号 | 插入的**确切代码行**（tab 缩进） | 位置说明 | 冲突风险 |
|---|---|---|---|---|
| 1 | `model/log.go` — 在**第 343 行之后**、第 344 行 `if !common.LogConsumeEnabled {` **之前** | `	QyOnConsumeLog(c, userId, params)` | 函数体第一行 | **低**（函数签名稳定；插在早退之前是硬性要求） |
| 2 | `model/log.go` — 在**第 419 行之后**、第 420 行 `if params.LogType == LogTypeConsume && !common.LogConsumeEnabled {` **之前** | `	QyOnTaskBillingLog(params)` | 函数体第一行 | **低** |
| 3 | `model/redemption.go` — 在**第 184 行之后**、第 185 行 `return redemption.Quota, nil` **之前** | `	QyOnRedeemSuccess(userId, redemption.Id, redemption.Quota)` | 事务外，紧跟 `RecordLog` | **低** |
| 4 | `model/topup.go` — 在**第 386 行 `}` 之后**、第 388 行注释 `// 事务外记录日志` **之前** | `	QyOnManualTopUpCompleted(tradeNo, userId)` | 事务已提交，`userId` 已赋值（幂等早退时为 0，实现侧容忍） | **低** |

**新增文件（0 预算，纯 append）**：`model/qy_export.go`（§0 全文）。

### 9.2 与其他模块共享的改动（**不重复计入本模块预算**）

| 文件:行号 | 插入代码 | 归属 |
|---|---|---|
| `main.go` import 块（建议第 31 行后） | `	"github.com/QuantumNous/new-api/qianye"` | 需求 1 / 配置模块 |
| `main.go:365` 之后、`:367 return nil` 之前 | `	if err := qianye.Init(); err != nil {` / `		common.SysError("failed to initialize qianye extension: " + err.Error())` / `		return err` / `	}` | 需求 1 |
| `main.go:195` 之后、`:198 router.SetRouter` 之前 | `	qianye.RegisterRoutes(server)` | 需求 1 |
| `main.go:152` 之后 | `	qianye.StartBackgroundTasks()` | 需求 1（本模块的 3 个后台任务挂在这里） |

### 9.3 明确**不改**的文件及理由

| 本可能改的地方 | 不改的替代方案 |
|---|---|
| `model/topup.go` × 5、`controller/topup.go` × 1（六条充值路径 hook） | GORM callback + 低水位重扫（§5.2），**省 6 行 / 2 文件** |
| `model/subscription.go`（订阅购买 hook） | `CompleteSubscriptionOrder` 已 `upsertSubscriptionTopUpTx` 写 `top_ups` 行，被 callback 自动捕获；余额购订阅**本就不该返佣** |
| `router/api-router.go` | 路由走 `main.go` 的 `qianye.RegisterRoutes(server)`（且必须在 `SetRouter` 前，避开 gzip/Cache/static 全局中间件） |
| `controller/user.go`（注册时记 IP 做防刷） | 降级为不做注册 IP 风控（见 §12.1）。若确需，+1 行，冲突风险**中** |

### 9.4 前端改动

| 文件 | 改动 | 冲突风险 |
|---|---|---|
| `web/src/features/wallet/index.tsx` | +1 import，`:350` 之后 +1 个 `<QyAffiliateSummaryCard />`（约 3 行） | **中**（该页上游高频改；D3 已拍板直接改不 fork） |
| `web/src/hooks/use-sidebar-data.ts` | `personal` 分组（`:102`）+1 项，指向 `/qy-affiliate` | **中**（纯数组插入；建议与其他模块合并为 1 个工作区入口） |
| `web/src/i18n/locales/*.json` ×7 | 追加 `qy_aff_*` 下划线扁平键 | **高频但纯追加** |
| `web/src/routeTree.gen.ts` | 自动重生成 | **高但可重生成**（合并流程：冲突即删除重跑 build） |

---

## 10. 并发与边界

### 10.1 幂等键总表

| 来源 | `source_type` | `source_no` | 唯一约束 |
|---|---|---|---|
| 充值（含订阅付费） | `recharge` | `top_ups.trade_no`（>128 字符则 `sha256hex` 前 64） | `uk_qy_ca_src` |
| 兑换码 | `redemption` | `RD{redemption.Id}` | 同上（兑换码单次使用，`model/redemption.go:165-177` CAS 保证） |
| 消费（日聚合） | `consume` | `{inviteeId}:{yyyymmdd}` | 同上，upsert 累加 |
| 冲正 | `clawback` | `CB:{taskId}:{quota}` / `CB:U{uuid}` / `CB:M{adminIdemKey}` | 同上 |
| 结算批次 | — | `SettleNo` | `uk_qy_cs_no` |

### 10.2 竞态清单（逐条给出处理）

| # | 竞态 | 处理 |
|---|---|---|
| R1 | 同一充值被 callback + 重扫 + 管理员 rescan 三路同时计佣 | `uk_qy_ca_src` + `OnConflict{DoNothing}` + 检查 `RowsAffected` |
| R2 | 多节点同时 flush 同一个 (invitee, day) 日桶 | `INSERT ... ON DUPLICATE KEY UPDATE gross = gross + VALUES(gross)`，DB 层原子，无需锁 |
| R3 | **结算读到 gross=10.5，写回时并发 flush 已把 gross 变成 12.3** | 回写用 `settled_amount = :gross_read`（不是 `= gross_amount`），并 `WHERE settled_amount = :settled_read` 做 CAS。多出的 1.8 下轮吸收。**写成 `settled_amount = gross_amount` 会静默吞掉 1.8 佣金** |
| R4 | 两个结算 runner 并发处理同一邀请人 | ① 新库租约锁 `qy_task_lease`（NAME='commission_settle'，CAS 续约）；② `balance FOR UPDATE`；③ R3 的行级 CAS。三重保险 |
| R5 | `common.IsMasterNode` 是环境变量不是租约，多节点都配 master | 所有后台任务（结算/重扫/关系同步/对账）**必须先拿新库租约**才执行（架构 §8） |
| R6 | 结算与提现同时改 balance | 双方都必须 `SELECT ... FOR UPDATE qy_commission_balance WHERE user_id=?`（§10.6 契约） |
| R7 | 缓存击穿：某热门邀请人的 1000 个下线同时首次消费 | `singleflight.Group` 合并回源 |
| R8 | 事件 chan 满（突发流量） | 非阻塞 `select default` 丢弃 + `droppedCounter`，A16 健康接口暴露。**fail-open：relay 永不受影响**（架构 §4） |
| R9 | GORM callback panic 污染主项目写事务 | 回调体首行 `defer recoverAndLog()` |
| R10 | 管理员补单标记晚于计佣落地 | `recharge_grace_seconds`(10s) 延迟 + 若已计佣则 `QyOnManualTopUpCompleted` 触发补偿冲正（`CB:M{tradeNo}`） |
| R11 | 进程退出丢失内存聚合（最多 30s） | 缩短 flush 间隔可配；默认接受，A16 暴露 `pending_buckets` 供运维判断 |
| R12 | **多节点:在 node A 改分组/费率/法币比例/拉黑,node B 的进程内快照仍是旧的** | 五把缓存(邀请关系+上线分组、运营参数、拉黑名单、分组费率、法币比例)全部只在本进程失效,而费率与折算比例是**逐笔冻结**进账本的,那段窗口里的每一分钱都按旧档永久发错(改比例不追溯)。修法:每次本地失效同时往 `qy_commission_cache_invalidation` 追加一行,各节点每 2 秒重放游标之后的流水并在本地失效(`cachesync.go`)。窗口从 300s/60s 收敛到一个轮询周期;`GET /admin/commission/health` 的 `cache_sync` 段暴露 enabled/cursor/published/applied/failed |
| R13 | **同一自然日里改 `day_offset_minutes`,日封顶原地满血复活** | 「今日已发」曾按 `SUM(granted_quota) WHERE created_at >= dayStart(now)` 现算,日界一挪已发的结算行整批掉出窗口。改成把窗口起点与已发额度记在 `qy_commission_balance.daily_cap_window_start/daily_cap_granted` 上,**新窗口只在距上一个窗口起点满 86400 秒时才开**(`settle.go: resolveCapWindow`)。存量行(起点=0)按旧口径补一次今日已发 |

### 10.3 边界条件

| 条件 | 处理 |
|---|---|
| `inviter_id == 0` | 无邀请人，负缓存，直接返回 |
| `inviter_id == userId` | 数据异常，跳过 + 记 `risk_flags='self_invite'` |
| 邀请人已删除（`users.deleted_at`）/ 禁用（`status=2`） | **仍然计佣**（钱是他挣的），但提现由提现模块按用户状态拒绝 |
| `base_quota <= 0` | 跳过（订阅付费单 `Amount=0` 走 `Money` 分支，见 §5.3） |
| `rate_bps == 0` | 不生成 accrual 行（避免全 0 行污染） |
| `base_quota < rate.min_base_quota` | 跳过 |
| 佣金 < 1 quota | **不丢弃**，全精度进 `unsettled_amount`（§2 的全部意义） |
| 单次结算 grant 超 `MaxInt32` | `common.QuotaFromDecimalChecked` 饱和 + `clamp.AuditMap()` 落 `qy_commission_settlement.remark` + 管理端告警。余下部分留在 `unsettled`，下轮继续发 |
| `available_quota` 累计溢出 int64 | 不可能（int64 上限 9.2e18 ≈ $1.8e13） |
| **提现进主库时 `users.quota + amount > MaxInt32`** | 提现模块必须校验（`increaseUserQuota` 无上限检查，`model/user.go:1249`）。本模块在 U1 返回 `available_quota`，提现模块负责 clamp |
| `unsettled_amount` 为负（欠账） | `debt_blocked=true`，禁止提现；未来佣金自动抵扣 |
| 新库不可用 | 热路径 fail-open（丢事件+计数）；非热路径 API 返 503 |
| MySQL 严格模式下 decimal 溢出 | `decimal(30,10)` 上限 `10^20`，业务上不可达；仍在写入前 `Abs().GreaterThan(1e19)` 时告警拒写 |

### 10.4 金额计算强制规范（AGENTS.md）
- 计佣：`decimal.NewFromInt(base).Mul(decimal.NewFromInt(bps)).Div(decimal.NewFromInt(10000))` —— **禁止** `int(float64(q)*rate)`。
- 取整：`total.Floor()` → `common.QuotaFromDecimalChecked(...)` —— **禁止** `d.IntPart()`（`service/violation_fee.go:91-99` 就是违反此约定的反面教材，勿照抄）。
- 法币：`decimal.NewFromInt(grant).Div(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(usdRate)`，结果 `Round(6)` 后落 `decimal(18,6)`。

### 10.5 对账任务（每小时，master + 租约）
1. `Σ qy_commission_settlement.granted_quota` 按 user 分组 vs `balance.total_earned_quota - total_clawback_quota` → 不等则告警。
2. `balance.unsettled_amount` vs `Σ(accrual.gross - accrual.settled) - Σ settlement.delta + ...` 交叉校验。
3. `qy_commission_accrual` 中 `status='accrued' && mature_at <= now - 1h && settled < gross` 的行数（结算滞后告警）。
4. 长期 `pending_review` 超 N 天 → 提醒管理员。

### 10.6 与提现模块的接口契约（**必须共同遵守**）
```
提现发起：新库事务内
  SELECT * FROM qy_commission_balance WHERE user_id=? FOR UPDATE
  要求 debt_blocked = false 且 available_quota >= amount
  available_quota -= amount ; frozen_quota += amount
  available_fiat  *= (available_new/available_old)      -- 保持冻结汇率均值
提现完成：frozen_quota -= amount ; withdrawn_quota += amount
提现拒绝：frozen_quota -= amount ; available_quota += amount（法币按原冻结值回补）
★ 本模块的结算任务只会增/减 available_quota 与 unsettled_amount，绝不触碰 frozen/withdrawn。
★ 法币金额直接取 available_fiat（已按计佣时刻汇率冻结），禁止用当前 USDExchangeRate 反算。
```

---

## 11. 前端页面

### 11.1 新建文件（纯新增，零冲突）

```
web/src/routes/_authenticated/qy-affiliate/index.tsx          用户页路由（无 beforeLoad，_authenticated 已保证登录）
web/src/routes/_authenticated/qy-commission/index.tsx         管理页路由（beforeLoad: role < ROLE.ADMIN → redirect /403）
web/src/routes/_authenticated/qy-commission/$section.tsx      管理页分区（settings/rates/review/balances/risk）

web/src/features/qy-affiliate/
  index.tsx                       SectionPageLayout + 3 个区块
  api.ts                          U1~U4
  types.ts
  components/summary-cards.tsx    累计佣金 / 待结算(含精确余数) / 已提现 / 可提现 / 邀请人数
  components/invitee-table.tsx    DataTablePage + useTableUrlState
  components/commission-table.tsx 佣金流水（source_type / status 筛选）
  components/trend-chart.tsx      @visactor/vchart 折线
  components/share-card.tsx       邀请码 + 链接 + 复制（复用 wallet/lib/affiliate.ts 的链接生成）

web/src/features/qy-affiliate/components/summary-card-compact.tsx  ★ 供钱包页嵌入
web/src/features/qy-commission-admin/**                            管理端（section-registry 范式）
```

### 11.2 修改的原有文件

| 文件 | 改动 |
|---|---|
| `web/src/features/wallet/index.tsx` | `:350` `<AffiliateRewardsCard/>` 之后插入 `<QyAffiliateSummaryCompact />`（展示"待结算余数 + 可提现 + 去看板"入口）。+1 import +3 行 JSX |
| `web/src/hooks/use-sidebar-data.ts` | `personal` 分组（`:102`）加 `{ title: t('qy_aff_nav'), url: '/qy-affiliate', icon: Users }`；`admin` 分组（`:118`）加 `{ title: t('qy_commission_nav'), url: '/qy-commission' }` |
| `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` | 追加 `qy_aff_*` / `qy_commission_*` 下划线扁平键（**不能用点号**，`keySeparator` 默认 `.` 会当嵌套） |

主要交互：
- 看板顶部 5 张统计卡；"待结算"卡片 tooltip 展示精确余数与"还差 X quota 可结算"，直接回应"用了一天没佣金"的困惑。
- 邀请用户表：脱敏名 + 绑定时间 + 累计贡献 + 累计佣金；**空态**文案引导复制邀请链接。
- 佣金流水表：`source_type` 彩色 Badge（充值/消费/兑换码/冲正），冲正行负数红色显示。
- 管理端审核队列：批量勾选 → 通过/拒绝；风控命中行高亮 + `risk_flags` Badge。

### 11.3 用户名脱敏算法（**服务端执行，前端只拿脱敏结果**）

```go
// qianye/service/commission/mask.go
func MaskUsername(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" { return "***" }

	// 邮箱形态：本地部分按下面规则脱敏，域名只留 TLD
	if at := strings.LastIndex(s, "@"); at > 0 {
		local, domain := s[:at], s[at+1:]
		tld := domain
		if dot := strings.LastIndex(domain, "."); dot >= 0 { tld = domain[dot+1:] }
		return maskCore(local) + "@***." + tld
	}
	return maskCore(s)
}

// 按 rune 处理，中日韩单字符也正确（不能按 byte）
func maskCore(s string) string {
	r := []rune(s)
	switch n := len(r); {
	case n == 1: return string(r[0]) + "**"            // "王"       → "王**"
	case n == 2: return string(r[0]) + "**"            // "张三"     → "张**"
	case n <= 4: return string(r[0]) + "**" + string(r[n-1])        // "abcd" → "a**d"
	default:     return string(r[:2]) + "***" + string(r[n-2:])     // "zhangsan" → "zh***an"
	}
}
```

对外标识用 `InviteeRef`（**不下发 user_id**）：
```go
func InviteeRef(userId int, salt string) string {
	sum := sha256.Sum256([]byte(salt + "|qy-invitee|" + strconv.Itoa(userId)))
	return hex.EncodeToString(sum[:])[:8]        // 8 hex，冲突概率对单邀请人的下线规模可忽略
}
```
`salt` 来自 YAML `commission.privacy.invitee_ref_salt`，**部署时必须改**（启动时若为默认值打警告）。

脱敏结果缓存在 `qy_invite_relation.masked_name`，列表页零主库访问。

**明确不下发给邀请人**：真实用户名、邮箱、`user_id`、IP、下线的具体模型/消费明细（只给聚合数字）、完整 `trade_no`（脱敏为 `****后4位`）。这与需求 5 的"划转按用户 ID + 脱敏确认、不开放用户名枚举"保持一致。

---

## 12. 我建议补充的（用户未提，但必要）

### 12.1 防刷（建议 · 高优先级）

| 措施 | 配置键 | 默认 | 说明 |
|---|---|---|---|
| **自我邀请（同人多账号）** | — | 硬检测 | ① `inviter==invitee` 直接拒；② 二级环路检测（A 邀 B、B 邀 A）→ 拒并标记；③ 同邮箱主域 + 用户名编辑距离 ≤2 → `risk_hold` |
| **邀请关系成熟期** | `risk.min_invitee_age_hours` | 24 | 下线注册满 N 小时后的行为才计佣。防"注册即充→返佣→退" |
| **绑定时间窗** | `risk.bind_window_days` | 0（不限） | 只对绑定后 N 天内的行为计佣。0 = 终身返佣（默认宽松） |
| **佣金成熟期 T+N** | `holding_days` | 7 | accrual 落库后 N 天才可结算/提现。**这是防"充值→拿佣金→退款"套利最有效的一招**（比 `refund_clawback` 更根本） |
| **单邀请人日封顶** | `risk.max_daily_quota_per_inviter` | 0（不限） | 在结算时钳制（已持锁，零额外开销）；超出按 `cap_overflow_policy` 顺延或作废 |
| **单下线累计封顶** | `risk.max_total_quota_per_invitee` | 0 | 防单个下线被反复刷 |
| **邀请突增检测** | `risk.burst_invitee_per_day` | 20 | 单邀请人单日新增下线超阈值 → 当日新关系全部 `risk_hold` |
| **纯刷检测** | — | 建议 | 下线充值后 N 天零消费 → `risk_hold`（走审核） |
| **注册 IP 同源检测** | — | **不做** | 项目注册链路（`controller/user.go:263-275`）未存注册 IP，实现需 +1 行 hook 改高频文件。**建议放弃**，用上面 7 条已足够 |

命中风控 → `status='risk_hold'` + `risk_flags` → 进管理端审核队列，**不静默丢弃**（防误杀真实用户）。

### 12.2 审核形态：推荐"自动 + 成熟期"而非"逐笔人工审核"（建议）
需求原文提"审核佣金"，但 D4 定的是宽松口径。人工逐笔审核在消费返佣场景不可行（即使日聚合，也是 下线数×天数 条）。建议默认 `settle_mode=auto` + `holding_days=7` + 风控命中才进队列，把"审核"收敛为**例外处理**。`settle_mode=manual` 保留为全量审核的可配置项（小平台可用）。

### 12.3 审计（建议）
- 管理端所有写接口挂 `middleware.AdminAuth()`，自动落原项目审计（`middleware/auth.go:68-75`），零埋点。
- 费率变更、审核决定、人工冲正额外写 `qy_commission_accrual.remark` / `qy_commission_rate.operator_id`，保证"谁在什么时候把费率从 3% 改到 8%"可查。
- 结算批次表本身就是余数与发放的完整审计链。

### 12.4 可观测性（建议 · A16 接口暴露）
`events_dropped_total`（chan 满丢弃数）、`inviter_cache_hit_rate`、`flush_fail_total`、`pending_buckets`、`settle_lag_seconds`、`accrual_write_fail_total`、`recharge_low_water_id`、`lease_owner`、`db_reachable`。**`events_dropped_total > 0` 必须告警**——这是唯一会造成真实佣金丢失的路径。

### 12.5 错误文案与空态（建议，i18n key 用 `qy_` 前缀）
| 场景 | key | 中文 |
|---|---|---|
| 功能未开启 | `qy_aff_disabled` | 邀请返佣功能当前未开启 |
| 新库不可用 | `qy_aff_unavailable` | 佣金服务暂时不可用，请稍后重试（不影响你的正常使用） |
| 未合规确认 | `qy_aff_compliance_required` | 管理员尚未完成支付合规确认，返佣暂不可用 |
| 零邀请空态 | `qy_aff_empty_invitees` | 还没有人通过你的链接注册。复制邀请链接分享给朋友吧 |
| 余数未达门槛 | `qy_aff_unsettled_hint` | 已累计 {{amount}} 佣金，满 1 额度后自动结算（不会丢失） |
| 成熟期中 | `qy_aff_holding_hint` | {{quota}} 佣金将在 {{days}} 天后可提现 |
| 欠账冻结 | `qy_aff_debt_blocked` | 因存在退款冲正产生的欠账，提现已暂停，请联系管理员 |
| 风控待审 | `qy_aff_risk_hold` | 该笔佣金正在人工复核中 |

### 12.6 沿用现有合规门禁（建议）
现有所有邀请奖励均被 `operation_setting.IsPaymentComplianceConfirmed()`（`setting/operation_setting/payment_setting.go:33`）门禁（见 `model/user.go:636`、`controller/user.go:440`）。**新返佣建议沿用同一门禁**，否则会出现"注册奖励关着、比例返佣开着"的不一致。门禁关闭时：仍然计佣入账本（不丢数据），但用户端 U1 返回 `compliance_ok:false` 并隐藏提现入口。

### 12.7 与现有 `AffQuota` 的关系（建议 · 必须明确）
现有 `users.aff_quota`（注册赠送池）**与新佣金体系完全隔离**：
- 不复用、不合并、不迁移。原 `AffQuota` 继续由 `common.QuotaForInviter` 的注册赠送逻辑维护（`model/user.go:492-501`），原"Transfer to Balance"按钮保持可用。
- 钱包页 `AffiliateRewardsCard`（`web/src/features/wallet/components/affiliate-rewards-card.tsx`）展示的三个数字仍来自 `aff_quota/aff_history_quota/aff_count`，**不改**；新卡片并列展示比例返佣的余额。
- 理由：两者的钱在不同库、语义不同（一次性赠送 vs 比例分成），合并会引入跨库两阶段却毫无收益。前端需在文案上区分（`qy_aff_legacy_hint`：注册奖励 / 比例返佣）。
