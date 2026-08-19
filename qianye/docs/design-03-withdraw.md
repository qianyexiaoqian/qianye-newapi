# 需求2b:提现申请、审核与历史

# 需求 2 后半 —— 佣金提现（申请 / 审核 / 历史）设计

## 0. 模块边界与依赖契约

本模块只拥有「提现单」这条链路，不拥有佣金账户。与需求 2 前半（返佣账本）的边界用接口契约固定，禁止跨模块直写表：

```go
// qianye/service/commission_port.go —— 本模块只声明并消费，实现由返佣模块提供
type CommissionPort interface {
    // 在调用方给定的【新库事务】内执行；available -= amt, frozen += amt
    // CAS: WHERE available_balance >= amt；RowsAffected==0 → ErrInsufficientCommission
    Freeze(tx *gorm.DB, userId int, amt decimal.Decimal, refNo string) error
    // 解冻退回：frozen -= amt, available += amt（拒绝/撤销/失败）
    Release(tx *gorm.DB, userId int, amt decimal.Decimal, refNo string) error
    // 核销：frozen -= amt（钱真的出去了，不回到 available）
    Commit(tx *gorm.DB, userId int, amt decimal.Decimal, refNo string) error
    // 可提现余额 = available 中 earned_at <= now - maturityDays 的部分
    Withdrawable(userId int, maturityDays int) (decimal.Decimal, error)
}
var Commission CommissionPort // 由 qianye/bootstrap.go 在 Init() 里注入
```

三个 `refNo` 全部传 `withdraw_no`，返佣模块据此在 `qy_commission_ledgers` 里落 `withdraw_freeze / withdraw_release / withdraw_commit` 三种流水，**保证佣金账户的每一次变动都能追到提现单**。

佣金余额单位统一为 **quota（含小数余数，`decimal(24,8)`）**，不是法币。法币金额是提现单上的派生量，靠冻结汇率换算。

依赖的既有能力（全部只读引用，零改动）：`model.DB`、`model.LOG_DB`、`model.GetUserById`、`model.IncreaseUserQuota` 的底层写法、`model.InvalidateUserCache`、`model.RecordLogWithAdminInfo`、`service.NotifyUser`、`common.QuotaFromDecimal`、`common.MaxQuota`、`common.QuotaPerUnit`、`operation_setting.USDExchangeRate`、`operation_setting.IsPaymentComplianceConfirmed()`、`middleware.UserAuth/AdminAuth/CriticalRateLimit`。

**导入方向说明（防循环）**：`qianye` → `service` → `model` 是单向的，安全。本模块不在 `model`/`service` 里埋任何 hook，因此可以放心 `import service`。（返佣模块在 `model/log.go` 的 hook 必须走 `model/qy_export.go` 里的函数变量注入，不能反向 import `qianye`。）

---

## 1. 表结构

全部建在独立 MySQL，`utf8mb4_0900_ai_ci`，引擎 InnoDB。时间统一 `bigint` unix 秒（`common.GetTimestamp()`），与原项目一致。

### 1.1 `qy_withdrawals` —— 提现单主表

```go
// qianye/model/withdrawal.go
type QyWithdrawal struct {
    Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

    // ── 单号体系 ────────────────────────────────────────────────
    WithdrawNo string `json:"withdraw_no" gorm:"type:varchar(40);not null;uniqueIndex:uk_qywd_no"`
    ClaimNo    string `json:"claim_no"    gorm:"type:varchar(40);not null;uniqueIndex:uk_qywd_claim"`
    RequestKey string `json:"-"           gorm:"type:varchar(64);not null;uniqueIndex:uk_qywd_reqkey,priority:2"`

    // ── 主体 ────────────────────────────────────────────────────
    UserId   int    `json:"user_id"  gorm:"type:int;not null;uniqueIndex:uk_qywd_reqkey,priority:1;index:idx_qywd_user_created,priority:1"`
    Username string `json:"username" gorm:"type:varchar(64);not null;default:''"`

    Method string `json:"method" gorm:"type:varchar(16);not null;index:idx_qywd_status,priority:2"` // quota | fiat
    Status string `json:"status" gorm:"type:varchar(24);not null;index:idx_qywd_status,priority:1;index:idx_qywd_status_updated,priority:1"`

    // ── 佣金侧（新库口径，单位 = quota） ─────────────────────────
    CommissionAmount decimal.Decimal `json:"commission_amount" gorm:"type:decimal(24,8);not null;default:0"`

    // ── 站内额度侧（method=quota 才有值） ───────────────────────
    Quota int `json:"quota" gorm:"type:int;not null;default:0"`

    // ── 法币侧（method=fiat 才有值），全部在申请时冻结 ──────────
    Currency           string          `json:"currency"              gorm:"type:varchar(8);not null;default:''"`
    FrozenQuotaPerUnit decimal.Decimal `json:"frozen_quota_per_unit" gorm:"type:decimal(20,6);not null;default:0"`
    FrozenFxRate       decimal.Decimal `json:"frozen_fx_rate"        gorm:"type:decimal(20,8);not null;default:0"`
    GrossAmount        decimal.Decimal `json:"gross_amount"          gorm:"type:decimal(18,4);not null;default:0"`
    FeeAmount          decimal.Decimal `json:"fee_amount"            gorm:"type:decimal(18,4);not null;default:0"`
    NetAmount          decimal.Decimal `json:"net_amount"            gorm:"type:decimal(18,4);not null;default:0"`
    FeeRuleSnapshot    string          `json:"fee_rule_snapshot"     gorm:"type:varchar(255);not null;default:''"`

    // ── 收款信息（脱敏投影，明文在 qy_withdrawal_payees） ───────
    PayeeChannel string `json:"payee_channel" gorm:"type:varchar(24);not null;default:''"`
    PayeeMasked  string `json:"payee_masked"  gorm:"type:varchar(128);not null;default:''"`
    PayeeDigest  string `json:"-"             gorm:"type:char(64);not null;default:'';index:idx_qywd_payee_digest"`

    // ── 用户自定义说明（≤200 rune） ─────────────────────────────
    UserNote string `json:"user_note" gorm:"type:varchar(255);not null;default:''"`

    // ── 审核 ────────────────────────────────────────────────────
    ReviewerId   int    `json:"reviewer_id"   gorm:"type:int;not null;default:0"`
    ReviewerName string `json:"reviewer_name" gorm:"type:varchar(64);not null;default:''"`
    ReviewedAt   int64  `json:"reviewed_at"   gorm:"type:bigint;not null;default:0"`
    RejectReason string `json:"reject_reason" gorm:"type:varchar(255);not null;default:''"`

    // ── 打款 / 到账 ─────────────────────────────────────────────
    PayoutOperatorId   int    `json:"payout_operator_id"   gorm:"type:int;not null;default:0"`
    PayoutOperatorName string `json:"payout_operator_name" gorm:"type:varchar(64);not null;default:''"`
    PaidAt             int64  `json:"paid_at"              gorm:"type:bigint;not null;default:0;index:idx_qywd_paid_at"`
    PayoutRef          string `json:"payout_ref"           gorm:"type:varchar(128);not null;default:''"`
    PayoutNote         string `json:"payout_note"          gorm:"type:varchar(255);not null;default:''"`
    FailReason         string `json:"fail_reason"          gorm:"type:varchar(255);not null;default:''"`

    // ── 跨库两阶段 / 对账 ───────────────────────────────────────
    PayingStartedAt int64  `json:"-" gorm:"type:bigint;not null;default:0"`
    MainLogHit      string `json:"-" gorm:"type:varchar(32);not null;default:''"`  // ""|found|missing
    ReconcileState  string `json:"reconcile_state" gorm:"type:varchar(16);not null;default:'';index:idx_qywd_reconcile"` // ""|hold|resolved

    // ── 元信息 ──────────────────────────────────────────────────
    ClientIp    string `json:"-" gorm:"type:varchar(64);not null;default:''"`
    CreatedAt   int64  `json:"created_at" gorm:"type:bigint;not null;index:idx_qywd_user_created,priority:2"`
    UpdatedAt   int64  `json:"updated_at" gorm:"type:bigint;not null;index:idx_qywd_status_updated,priority:2"`
    PiiPurgedAt int64  `json:"-" gorm:"type:bigint;not null;default:0"`
}
func (QyWithdrawal) TableName() string { return "qy_withdrawals" }
```

**逐字段存在理由**

| 字段 | 为什么必须有 |
|---|---|
| `WithdrawNo` | 面向用户/客服的业务单号，前端展示、工单沟通用。唯一索引防重放。 |
| `ClaimNo` | **跨库幂等键**。写进主库 `logs.content` + `admin_info`，是「主库加了钱没有」的唯一反查凭据（GAPS §3.2(1)：主库没有结构化 transfer_no）。与 `WithdrawNo` 分开，是为了让财务单号可对外公开、幂等键保持内部语义。 |
| `RequestKey` | 前端生成的 UUID，`(user_id, request_key)` 唯一索引 —— 双击/多标签/网络重试的**唯一可靠去重手段**。进程内锁和限流都只是辅助。 |
| `Username` | 快照。用户可改名、可软删除，管理端队列不能靠实时 join 主库。 |
| `Method` | D1 的判别式。`quota` = 站内额度兑换，`fiat` = 线下法币打款。 |
| `CommissionAmount` | **唯一的资金真值**（从佣金账户扣走多少）。`decimal(24,8)` 是因为佣金按比例计算会有小数余数（GAPS §10），int 会大量截断成 0。 |
| `Quota` | `method=quota` 时实际写入主库 `users.quota` 的整数值。`int` 对应主库 `int32`，与 `common.MaxQuota` 同域，便于溢出校验。 |
| `FrozenQuotaPerUnit` / `FrozenFxRate` | **GAPS §一.1 的致命点**。`common.QuotaPerUnit`（`model/option.go:578`）和 `operation_setting.USDExchangeRate`（`model/option.go:424`）**都是管理员可随时改、`SyncOptions` 秒级热更新的全局变量**。不冻结，一年后没有任何办法复现「当时这笔佣金折合多少人民币」，对账永远对不上。两个都要冻，只冻汇率仍会被 QuotaPerUnit 改动破坏。 |
| `GrossAmount/FeeAmount/NetAmount` | 应付/手续费/实付三段拆开，`decimal(18,4)` 而非 `(18,2)`：手续费按比率算会出现分以下位，先按 4 位存、展示时再 `Round(2)`，避免「三个字段加不上」。 |
| `FeeRuleSnapshot` | 手续费规则原文快照（如 `rate=0.02;fixed=2.00;min=2.00`）。规则改了不影响历史单据可解释性。 |
| `PayeeChannel/PayeeMasked` | 列表页与导出只用这两个，**永远不解密**。 |
| `PayeeDigest` | HMAC-SHA256(收款信息规范化 JSON)。用途：① 风控 —— 同一收款账号被多个 user_id 使用 → 刷单告警；② 完整性校验；③ PII 清除后仍可去重。**索引在这里而不在明文上**。 |
| `UserNote` | 需求原文「发起请求内容可自定义 200 字内」。varchar(255) 给 emoji 留头（MySQL utf8mb4 按字符计长度，4 字节 emoji 算 1 字符，与 Go rune 口径一致）。 |
| `Reviewer*/ReviewedAt/RejectReason` | 需求原文「什么时候拒绝的，拒绝理由是什么」。 |
| `PaidAt/PayoutRef/PayoutOperator*` | 需求原文「什么时候打的款」。`PayoutRef` 存银行流水号/支付宝订单号/链上 txid —— 争议时的唯一证据。 |
| `PayingStartedAt` | `paying` 中间态的超时判定基准（不能用 `UpdatedAt`，它会被其他写覆盖）。 |
| `MainLogHit` / `ReconcileState` | 对账任务的结论缓存 + 人工介入队列标记。 |
| `PiiPurgedAt` | PII 保留期到期清除的时间戳，证明「我们确实按期删了」。 |

**索引清单**

| 索引 | 列 | 服务的查询 |
|---|---|---|
| `uk_qywd_no` (U) | `withdraw_no` | 单号查询 |
| `uk_qywd_claim` (U) | `claim_no` | 跨库幂等 |
| `uk_qywd_reqkey` (U) | `user_id, request_key` | **防重复提交** |
| `idx_qywd_user_created` | `user_id, created_at` | 用户提现历史分页 + 日/月次数统计 |
| `idx_qywd_status` | `status, method` | 管理端审核队列 |
| `idx_qywd_status_updated` | `status, updated_at` | 补偿任务扫超时 |
| `idx_qywd_paid_at` | `paid_at` | 财务按打款日导出 |
| `idx_qywd_payee_digest` | `payee_digest` | 同收款账号多账户风控（指纹只覆盖账号字段，见 §8.1） |
| `idx_qywd_reconcile` | `reconcile_state` | 对账异常队列 |

### 1.2 `qy_withdrawal_payees` —— 收款信息（PII，1:1，独立表）

**独立成表的唯一理由：保留期到了要能整行删掉，而不动财务单据。**

```go
type QyWithdrawalPayee struct {
    Id           int64  `gorm:"primaryKey;autoIncrement"`
    WithdrawalId int64  `gorm:"type:bigint;not null;uniqueIndex:uk_qywdp_wid"`
    WithdrawNo   string `gorm:"type:varchar(40);not null;default:''"`
    UserId       int    `gorm:"type:int;not null;index:idx_qywdp_user"`

    Channel    string `gorm:"type:varchar(24);not null"`               // alipay|wechat|bank|usdt_trc20|paypal
    CipherAlg  string `gorm:"type:varchar(24);not null;default:'aes-256-gcm'"`
    KeyVersion int    `gorm:"type:int;not null;default:1"`             // 支持密钥轮换
    Nonce      []byte `gorm:"type:varbinary(16);not null"`
    Cipher     []byte `gorm:"type:varbinary(4096)"`                    // 明文 JSON 的密文；清除时置 NULL
    Digest     string `gorm:"type:char(64);not null;index:idx_qywdp_digest"`
    Masked     string `gorm:"type:varchar(128);not null;default:''"`

    CreatedAt int64 `gorm:"type:bigint;not null"`
    PurgedAt  int64 `gorm:"type:bigint;not null;default:0"`
}
func (QyWithdrawalPayee) TableName() string { return "qy_withdrawal_payees" }
```

明文 JSON 形态（按 channel 动态）：

```json
{"channel":"bank","real_name":"张三","bank_name":"招商银行","branch":"深圳分行","account_no":"6214...","id_last4":"1234"}
{"channel":"alipay","real_name":"张三","account":"13800138000"}
{"channel":"usdt_trc20","address":"TXxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","network":"TRC20"}
{"channel":"paypal","email":"a@b.com"}
```

### 1.3 `qy_withdrawal_events` —— 状态机事件流水（时间线数据源）

```go
type QyWithdrawalEvent struct {
    Id           int64  `gorm:"primaryKey;autoIncrement"`
    WithdrawalId int64  `gorm:"type:bigint;not null;index:idx_qywde_wid,priority:1"`
    WithdrawNo   string `gorm:"type:varchar(40);not null;default:''"`

    FromStatus string `gorm:"type:varchar(24);not null;default:''"`
    ToStatus   string `gorm:"type:varchar(24);not null"`
    Action     string `gorm:"type:varchar(32);not null"`  // submit|approve|reject|cancel|settle|pay|fail|hold|resolve
    ActorType  string `gorm:"type:varchar(16);not null"`  // user|admin|system
    ActorId    int    `gorm:"type:int;not null;default:0"`
    ActorName  string `gorm:"type:varchar(64);not null;default:''"`

    Reason string `gorm:"type:varchar(255);not null;default:''"`
    Detail string `gorm:"type:varchar(1024);not null;default:''"` // JSON：金额/汇率/claim_no/错误码
    Ip     string `gorm:"type:varchar(64);not null;default:''"`

    CreatedAt int64 `gorm:"type:bigint;not null;index:idx_qywde_wid,priority:2"`
}
func (QyWithdrawalEvent) TableName() string { return "qy_withdrawal_events" }
```

**为什么不把时间线塞进主表 JSON 字段**：单据主表要能被 SQL 聚合统计，事件要能按时间倒序分页；且事件是 append-only，永不 UPDATE，天然免并发。主表上的 `ReviewedAt/PaidAt/RejectReason` 是事件的**冗余投影**，为了列表页免 join。

### 1.4 `qy_pii_audits` —— PII 访问审计

```go
type QyPiiAudit struct {
    Id         int64  `gorm:"primaryKey;autoIncrement"`
    Resource   string `gorm:"type:varchar(32);not null"`   // withdrawal_payee
    ResourceId int64  `gorm:"type:bigint;not null;index:idx_qypii_res"`
    TargetUserId int  `gorm:"type:int;not null;default:0;index:idx_qypii_target"`

    AdminId   int    `gorm:"type:int;not null;index:idx_qypii_admin,priority:1"`
    AdminName string `gorm:"type:varchar(64);not null;default:''"`
    Action    string `gorm:"type:varchar(24);not null"`   // view_plain|export_plain
    Fields    string `gorm:"type:varchar(255);not null;default:''"`
    Reason    string `gorm:"type:varchar(255);not null;default:''"` // 建议：强制填写查看事由

    Ip        string `gorm:"type:varchar(64);not null;default:''"`
    UserAgent string `gorm:"type:varchar(255);not null;default:''"`
    CreatedAt int64  `gorm:"type:bigint;not null;index:idx_qypii_admin,priority:2"`
}
func (QyPiiAudit) TableName() string { return "qy_pii_audits" }
```

### 1.5 复用地基的 `qy_task_leases`

后台任务（对账补偿、PII 清除）必须走地基第 8 条的**新库租约锁表**，不用 `common.IsMasterNode`（它只是环境变量，多节点都配 master 会双跑；而对账任务双跑 = 重复置 paid）。本模块占用两个 lease key：`withdrawal_reconcile`、`withdrawal_pii_purge`。

---

## 2. YAML 配置（`qianye.yaml` 的 `withdrawal` 段）

```yaml
withdrawal:
  enabled: true
  methods: ["quota", "fiat"]          # 空数组 = 提现整体关闭
  payee_channels: ["alipay", "bank", "usdt_trc20"]

  # PII 加密（method 含 fiat 时必填；缺失或长度不对 → fiat 强制下线并打 SysError）
  pii_key: "base64:0000000000000000000000000000000000000000000="   # 32 字节
  pii_key_version: 1
  pii_retention_days: 180

  maturity_days: 7                    # 佣金冻结期（防充值返佣→提现→退款套利）
  note_max_runes: 200

  # 站内额度兑换
  quota_min: 500000                   # = common.QuotaPerUnit，$1
  quota_max_per_order: 200000000      # $400，且硬顶 common.MaxQuota
  quota_auto_settle_on_approve: true  # D1：审核通过自动到账
  quota_fee_rate: "0"

  # 线下法币
  fiat_currency: "CNY"
  fiat_min_amount: "50.00"
  fiat_max_amount_per_order: "20000.00"
  fiat_fee_rate: "0.00"               # 比例手续费
  fiat_fee_fixed: "0.00"              # 固定手续费
  fiat_fee_min: "0.00"

  # 风控
  max_per_day: 3
  max_per_month: 10
  max_amount_per_day_quota: 200000000
  cooldown_seconds: 0
  max_pending_orders: 1               # 同时最多几张未终态单

  # 两阶段与补偿
  paying_timeout_seconds: 300
  reconcile_interval_seconds: 60
  reconcile_lookback_days: 7

  notify: true
```

**降级语义**（地基第 4 条）：配置缺失 → 整个提现模块 `Enabled()=false`，前端 `GET /api/qy/withdrawal/config` 返回 `{enabled:false}`，钱包页入口卡片不渲染。新库连不上 → 提现是**非热路径**，全部接口返回 **503 + 文案「提现服务暂不可用，请稍后重试」**，绝不 fail-open（fail-open 在资金路径上等于送钱）。

---

## 3. 状态机

```
                        ┌──────────── cancel(user) ────────────┐
                        │                                      ▼
   [submit(user)]   pending ── reject(admin, reason必填) ──► rejected
        │              │
        │              └── approve(admin) ──► approved
        │                                        │
        │        method=quota & auto_settle ─────┤
        │                                        ▼
        │                                     paying ──► paid
        │                                        │  \
        │                                        │   └─► failed
        │                                        └─► reconcile_hold ──resolve(admin)──► paid | failed
        │
        └── method=fiat：approved 停留在【待打款】
                            │
                            ├── pay(admin, payout_ref, paid_at) ──► paid
                            └── fail(admin, fail_reason) ──────────► failed
```

| 状态 | 中文 | 终态 | 佣金账户 |
|---|---|---|---|
| `pending` | 待审核 | 否 | frozen |
| `approved` | 已通过 / 待打款 | 否 | frozen |
| `paying` | 兑现中 | 否（≤5min） | frozen |
| `paid` | 已到账 / 已打款 | **是** | committed（核销） |
| `rejected` | 已拒绝 | **是** | released（退回 available） |
| `cancelled` | 已撤销 | **是** | released |
| `failed` | 失败 | **是** | released |
| （`reconcile_state=hold`） | 对账异常（非独立状态，附着在 `paying` 上） | 否 | frozen |

**每个转移的触发者与副作用**

| # | 转移 | 触发者 | 副作用（全部在**一个新库事务**内完成，除非注明） |
|---|---|---|---|
| T1 | – → `pending` | 用户 | 建单；`Commission.Freeze`；写 event `submit` |
| T2 | `pending` → `cancelled` | 用户本人 | CAS `WHERE status='pending'`；`Commission.Release`；event `cancel` |
| T3 | `pending` → `rejected` | 管理员 | CAS；`Commission.Release`；写 `reject_reason/reviewer/reviewed_at`；event `reject`；**异步通知用户** |
| T4 | `pending` → `approved` | 管理员 | CAS；写 `reviewer/reviewed_at`；event `approve`。`method=quota && auto_settle` 时立即触发 T5 |
| T5 | `approved` → `paying` | 系统 | **CAS `WHERE status='approved'`**（这是全链路唯一的「跨库执行准入闸门」）；`paying_started_at=now`；event `settle_start` |
| T6 | `paying` → `paid` | 系统 | 主库已加额度后回写；`Commission.Commit`；`paid_at`；event `settle_done`；通知用户 |
| T7 | `paying` → `failed` | 系统 | **仅当主库返回确定性业务错误**；`Commission.Release`；`fail_reason`；event `settle_failed`；通知 |
| T8 | `paying` → `reconcile_state=hold` | 系统（补偿任务） | 主库结果不确定时；event `hold`；**告警管理员** |
| T9 | hold → `paid` / `failed` | 管理员裁决 | event `resolve`，`Detail` 记裁决依据 |
| T10 | `approved` → `paid` | 管理员（fiat） | 校验 `payout_ref` 非空；**后端硬校验 `method=fiat`**；`Commission.Commit`；`paid_at/payout_operator/payout_ref/payout_note`；event `pay`；通知 |
| T11 | `approved` → `failed` | 管理员（fiat） | `fail_reason` 必填；`Commission.Release`；event `fail`；通知 |
| T12 | `approved` → `paying` | 管理员（quota，手动） | 与 T5 同一条边、同一道 CAS，只是触发者是人：管理员点「立即兑现」显式调 `creditQuota`。`auto_credit_on_approve=false` 时这是 quota 单唯一的正向出口 |

> **T10 的 `method=fiat` 是资金安全边界，不是参数校验。** `markPaid` 会把佣金
> `frozen → withdrawn`，而它全程不碰主库 `users.quota`。对 quota 单执行一次即
> 「佣金被永久核销、用户一分钱没拿到」，且 `paid` 无出边、无反向接口、`order_no`
> 为空的 `paid` 单不会被对账再看一眼 —— 静默、不可逆、无检测。前端隐藏按钮不算
> 防线（绕过它只需一个 curl，批量运维脚本更是一发就中），后端返回
> `qy_wd_not_fiat_order`。反之，`method=fiat` 的单也不能走 T12（`qy_wd_not_quota_order`）：
> 线下已经打过款，再加一笔站内额度就是重复支付。

**非法转移一律靠 `UPDATE ... WHERE id=? AND status=?` 的 `RowsAffected==0` 拒绝**，不靠先读后写（先读后写在多节点下必错）。

需求原文的三个问题由此闭环：
- 「什么时候打的款」→ `paid_at` + `payout_ref` + event `pay/settle_done`
- 「什么时候拒绝的」→ `reviewed_at` + event `reject`
- 「拒绝理由是什么」→ `reject_reason`（后端强校验非空且 ≤200 rune）

---

## 4. 关键流程

### 4.1 提交提现申请（POST /api/qy/withdrawal）

```
 1. 门禁：qy 模块 Enabled → withdrawal.enabled → method ∈ methods
         → operation_setting.IsPaymentComplianceConfirmed()（沿用原项目合规门禁）
 2. 取 userId := c.GetInt("id")；查 model.GetUserById(userId, false)
         → 不存在/软删除/status != common.UserStatusEnabled → 400
 3. 参数校验（后端，全部硬校验，不信前端）：
    - request_key：非空，长度 8..64
    - commission_amount：decimal，> 0
    - user_note：utf8.RuneCountInString(note) <= 200
    - method=quota：quota := common.QuotaFromDecimal(commission_amount)
                    要求 commission_amount 恰为整数（有小数余数则拒绝，提示"请输入整数额度"）
                    quota >= quota_min && quota <= min(quota_max_per_order, common.MaxQuota)
    - method=fiat：payee_channel ∈ payee_channels；按 channel 校验必填字段与格式
 4. 风控（4.4 节，全部在同一个新库只读查询批次里做）
 5. 进程内 per-user TryLock（照抄 controller/user.go:1348 getTopUpLock 的思路，
    只是防抖，不作为正确性依据）
 6. 生成单号：
    withdraw_no = "QYW" + yyyymmdd + 12位随机（common.GetRandomString）
    claim_no    = "QYC" + strings.ReplaceAll(common.GetUUID(), "-", "")
 7. 冻结价格参数（★ 关键，2026-08-19 修订：金额改由佣金账本给出）：
    frozen_quota_per_unit = decimal.NewFromFloat(common.QuotaPerUnit)   // 仍然冻结，>0 否则拒绝建单
    gross = commission.QuoteWithdrawFiat(tx, user_id, quota)            // == 本次冻结从
                                                                       //    qy_commission_balance.available_fiat
                                                                       //    削走的绝对值
    frozen_fx_rate = gross / (quota / frozen_quota_per_unit)            // 反解，只用于展示与对账

    ⚠ 本条曾写作 `frozen_fx_rate = operation_setting.USDExchangeRate`，即提现侧
    自带一套汇率。它与 design-02-commission §法币折算（分组档 → 兜底档 → 全站汇率，
    逐笔冻进 available_fiat）是两个互不相干的数：实测账本冻走 850 CNY、单据只让运营
    付 100 CNY，而「VIP 按更高比例结汇」这个杠杆对打款完全失效。现在两侧同源，
    withdraw.rate_freeze_mode / rate_freeze_fixed 两个配置键已下线。
    fee   = max(gross * fiat_fee_rate + fiat_fee_fixed, fiat_fee_min).RoundBank(4)
    net   = gross - fee ；要求 net > 0 且 gross >= fiat_min_amount
 8. PII 处理（method=fiat）：
    account := normalize(payee[账号字段])  // 只取该渠道「钱去哪」的那一个字段并归一
    digest := hex(HMAC-SHA256(digestKey, "payee-account-v1" + channel + account))
    nonce  := crypto/rand 12 字节
    cipher := AES-256-GCM.Seal(nonce, plain, AAD=withdraw_no)  // AAD 绑定单号，防密文换单
    masked := maskByChannel(payee)
 ─────────── 新库事务 T_new 开始 ───────────
 9.  Commission.Freeze(tx, userId, commission_amount, withdraw_no)
     └ 内部 CAS: UPDATE qy_commission_accounts
                 SET available=available-?, frozen=frozen+?
                 WHERE user_id=? AND available >= ?
       RowsAffected==0 → ErrInsufficientCommission → 整个事务回滚 → 400「可提现佣金不足」
10.  tx.Create(&QyWithdrawal{... status: "pending" ...})
     └ 唯一键冲突 (user_id, request_key) → 回滚 → 直接返回**已存在的那张单**（幂等成功，不是错误）
11.  tx.Create(&QyWithdrawalPayee{...})   // fiat only
12.  tx.Create(&QyWithdrawalEvent{ToStatus:"pending", Action:"submit", ActorType:"user"})
 ─────────── 新库事务 T_new 提交 ───────────
13. 异步：通知管理员有新待审（可选）；返回 {withdraw_no, status, ...}（**不返回明文收款信息**）
```

**事务边界**：步骤 9–12 是单一新库事务，不涉及主库，因此**申请阶段完全没有跨库风险**。这是把 GAPS §3.2(2) 的风险前移消解的关键：**佣金在申请那一刻就离开了 `available`**，之后无论兑现阶段发生什么，用户都不可能拿同一笔佣金再发一单。最坏情况只是「佣金被卡在 frozen」，需要人工处理，**永远不会变成「佣金可无限重复领取」**。

### 4.2 站内额度兑现（跨库两阶段，GAPS §3.2(2) 的正面解法）

触发点：T4 审核通过后（`quota_auto_settle_on_approve: true`），或补偿任务重新拾起 `approved` 单。

```
 S1  【新库事务】准入闸门（幂等键 = id + status CAS）
     r := qyDB.Model(&QyWithdrawal{}).
            Where("id = ? AND status = ?", id, "approved").
            Updates(map[string]any{"status":"paying","paying_started_at":now,"updated_at":now})
     if r.RowsAffected == 0 { return }   // 别的节点已在执行 → 直接放弃，不重试
     写 event settle_start（含 claim_no）
     ★ 这一步保证【主库加钱操作在全集群最多被执行一次】

 S2  【主库事务】model.DB.Transaction(func(tx *gorm.DB) error {
         var u model.User
         if err := model.QyLockForUpdate(tx).Where("id = ?", userId).First(&u).Error; err != nil {
             return ErrDeterministic(err)                      // 用户不存在/被删 → 确定性失败
         }
         if u.Status != common.UserStatusEnabled { return ErrDeterministic(...) }
         if u.Quota > common.MaxQuota - quota  { return ErrDeterministic(ErrQuotaOverflow) }
         return tx.Model(&model.User{}).Where("id = ?", userId).
                   Update("quota", gorm.Expr("quota + ?", quota)).Error
     })
     ⚠ 禁止调 model.IncreaseUserQuota（无事务、无溢出校验、db=false 时进批量队列）
     ⚠ 禁止 tx.Save(&user)（全字段覆盖）
     ⚠ 禁止触碰 auth_version / 调 user.Update()（会 bump auth 版本 → 吊销用户会话）

 S3  提交成功后（不在事务内）：
     a) model.InvalidateUserCache(userId)                       // 最稳，见 recon-user-quota §8.3 方案B
     b) content := fmt.Sprintf("佣金提现到账 %s [%s]", logger.LogQuota(quota), claimNo)
        model.RecordLogWithAdminInfo(userId, model.LogTypeTopup, content, map[string]any{
            "qy_module":     "withdrawal",
            "qy_claim_no":   claimNo,
            "qy_withdraw_no": withdrawNo,
            "qy_quota":      quota,
        })
        ★ 同步执行，失败重试 3 次（退避 200ms/1s/3s）。claim_no **同时**写进 content 文本
          和 admin_info —— 保证不管 LOG_DB 是 MySQL 还是 ClickHouse，都能用 LIKE 反查。
          这是 GAPS §3.2(1) 「主库无结构化单号」的补丁。

 S4  【新库事务】收尾
     CAS: UPDATE qy_withdrawals SET status='paid', paid_at=now, main_log_hit='found'
          WHERE id=? AND status='paying'
     Commission.Commit(tx, userId, commission_amount, withdrawNo)
     写 event settle_done
```

**失败与回滚路径（严格区分「确定性失败」与「不确定失败」）**

| S2 的结果 | 判定 | 动作 |
|---|---|---|
| 事务返回 `ErrDeterministic`（用户不存在/禁用/溢出/GORM 记录未找到） | 主库**确定未加钱**（错误在 UPDATE 之前抛出） | 新库 CAS `paying → failed`，`Commission.Release`，`fail_reason` 落库，通知用户 |
| 事务返回 nil | 已加钱 | 走 S3/S4 |
| 事务返回 **超时 / 连接断开 / driver.ErrBadConn / context deadline** | **不确定** | **绝不自动重试 S2**（重试 = 重复加钱）。置 `reconcile_state='hold'`，交补偿任务 |
| S3 或 S4 崩溃 / 进程被杀 | 主库已加钱，新库停在 `paying` | 补偿任务处理 |

**补偿任务 `withdrawal_reconcile`**（每 `reconcile_interval_seconds` 一次，持新库租约锁）：

```
 R1  扫描 status='paying' AND paying_started_at < now - paying_timeout_seconds
 R2  去主库日志库反查证据：
     model.LOG_DB.Model(&model.Log{}).
        Where("user_id = ? AND type = ? AND created_at >= ?", userId, model.LogTypeTopup, createdAt).
        Where("content LIKE ?", "%"+claimNo+"%").Count(&n)
 R3  n > 0  → 主库确已加钱 → 补做 S4（CAS paying→paid, Commit），main_log_hit='found'
     n == 0 →【不自动判 failed】。因为 RecordLog 本身可能失败而钱已加。
              置 reconcile_state='hold'，main_log_hit='missing'，
              写 event hold，并 service.NotifyRootUser 告警。
 R4  管理端「对账异常」队列人工裁决（T9）：
     管理员对照主库用户额度变动 / logs，点「确认已到账」或「确认未到账并退回」。
```

> **为什么宁可人工也不自动判 failed**：自动判 failed 会 `Release` 佣金退回 available，若主库其实已加钱，用户就白拿了一笔 —— 这是可被主动构造（在兑现瞬间打满 LOG_DB 让写入失败）的套利面。人工兜底最坏只是运营成本，不产生资金损失。同时因为 S1 的 CAS 闸门存在，hold 的单永远不会被第二次执行。

**扫尾任务**：`approved` 且 `method=quota` 且 `updated_at < now-60s` 的单（说明 T4 后 auto-settle 未启动，如进程崩在 T4 与 S1 之间）→ 重新进 S1。S1 的 CAS 天然幂等。

> **`auto_credit_on_approve: false` 的语义**（对应实现里的 `withdraw.auto_credit_on_approve`）：
> 该开关只门住「从 `approved` 自动起步」这一件事 —— T4 之后不自动兑现，上面这个扫尾任务也一并停手。
> 此时 quota 单由管理员在管理端点「立即兑现」显式触发（T12 / A7b），走的是与自动兑现**完全相同**的
> `creditQuota` 与 S1 CAS 闸门，因此重复点击不会重复加钱。
> 一旦单据进入 `paying`，收尾与补偿（S2、扫尾任务的 paying 分支、twophase 的 Resolver）都不再看这个开关，
> 崩溃恢复照常。
>
> **这个开关不能没有手动出口**：`approved → paying` 是 quota 单走到 `paid` 的唯一入边，缺了手动兑现，
> 关掉自动到账就等于让佣金无限期冻在 `frozen` —— 用户拿不到钱，管理端看起来可用的按钮只剩
> 「标记打款失败」（退回佣金、用户白等）与 T10 的「标记已打款」（核销佣金、一分钱不付）。
> 后者已被后端硬拒（见 T10 下方的说明）。

### 4.3 线下法币打款（无跨库风险）

```
 P1  管理员在队列点「查看完整收款信息」→ 先过一次 2FA/Passkey（scope: withdraw.payee.read）
     → 带 X-Security-Proof 调 .../payee
     解密 → 写 qy_pii_audits → 返回明文（前端仅在弹窗内展示，不落 localStorage/不入 react-query 缓存）
 P2  管理员线下转账
 P3  POST .../pay { payout_ref（必填）, paid_at（可选，默认 now）, payout_note }
     【新库事务】CAS approved→paid + Commission.Commit + event pay
 P4  打款失败：POST .../fail { fail_reason（必填） }
     【新库事务】CAS approved→failed + Commission.Release + event fail
```

法币路径**不触碰主库任何数据**，因此不存在两阶段问题。`paid` 之后的冲正（管理员误打款）不提供自动接口 —— 需要管理员在原项目「用户管理」里手工调额度 + 在本模块留一条 `PayoutNote` 说明，避免给出一个可被误用的资金反向通道。

### 4.4 风控校验（申请时同批执行）

```
 C1  status ∈ (pending, approved, paying) 的单数 < max_pending_orders
 C2  今日（本地 0 点起）非 cancelled 的单数 < max_per_day
 C3  本月非 cancelled 的单数 < max_per_month
 C4  今日累计 commission_amount + 本次 <= max_amount_per_day_quota
 C5  距上一张单 created_at >= cooldown_seconds
 C6  Commission.Withdrawable(userId, maturity_days) >= commission_amount
     └ 佣金冻结期：只有 earned_at <= now - 7d 的佣金可提。
       与 D4 的 refund_clawback 联动：clawback 优先冲抵未成熟佣金；
       若佣金已被提现核销，clawback 记为佣金账户负余额，下次返佣先抵扣。
 C7  payee_digest 命中其他 user_id 的历史单 → 不拒绝，但打 risk_flag，管理端队列标红
 C8  用户注册时长 < register_min_days（建议项，默认 0=关闭）
```

C1–C5 用一条聚合 SQL（走 `idx_qywd_user_created`）拿到 `count_today / count_month / sum_today / last_created_at`，避免 5 次往返。

---

## 5. API 清单

统一前缀 `/api/qy/`，在 `qianye/router.go` 的 `RegisterRoutes(*gin.Engine)` 内注册（地基已预算的 `main.go` 一行调用，本模块不再消耗后端改动预算）。

响应体统一 `{ success, message, data }`（`common.ApiSuccess` / `common.ApiError` / `common.ApiErrorI18n`）。

### 5.1 用户端（`middleware.UserAuth()`）

| # | Method | Path | 说明 |
|---|---|---|---|
| U1 | GET | `/api/qy/withdrawal/config` | 提现配置与当前可提额度 |
| U2 | POST | `/api/qy/withdrawal` | 提交申请（挂 `middleware.CriticalRateLimit()`） |
| U3 | GET | `/api/qy/withdrawal/self` | 提现历史分页 |
| U4 | GET | `/api/qy/withdrawal/self/:withdraw_no` | 详情 + 时间线 |
| U5 | POST | `/api/qy/withdrawal/self/:withdraw_no/cancel` | 撤销（仅 pending） |
| U6 | GET | `/api/qy/withdrawal/payee/recent` | 最近用过的收款方式（**只返回脱敏 + payee_ref**） |

**U1 响应**
```jsonc
{ "success": true, "data": {
  "enabled": true,
  "methods": ["quota", "fiat"],
  "payee_channels": ["alipay", "bank", "usdt_trc20"],
  "withdrawable_commission": "1250000.00000000",   // string，避免 JS 精度丢失
  "frozen_commission": "500000.00000000",
  "maturing_commission": "80000.00000000",         // 未过冻结期
  "maturity_days": 7,
  "note_max_runes": 200,
  "quota": { "min": 500000, "max_per_order": 200000000, "fee_rate": "0" },
  "fiat": { "currency": "CNY", "min_amount": "50.00", "max_amount_per_order": "20000.00",
            "fee_rate": "0.00", "fee_fixed": "0.00", "fee_min": "0.00",
            "preview_quota_per_unit": "500000", "preview_fx_rate": "7.30000000" },
  "limits": { "max_per_day": 3, "used_today": 0, "max_per_month": 10, "used_this_month": 2,
              "cooldown_seconds": 0, "next_allowed_at": 0, "max_pending_orders": 1, "pending_orders": 0 }
}}
```
> `preview_*` 明确命名为「预览」，前端必须提示「实际汇率以提交时为准」。

**U2 请求**
```jsonc
{
  "request_key": "5c1f...-uuid",
  "method": "fiat",                       // quota | fiat
  "commission_amount": "500000",          // string decimal，单位 quota
  "user_note": "本月推广结算，麻烦周五前处理",   // ≤200 rune
  "payee_channel": "alipay",              // method=fiat 必填
  "payee": { "real_name": "张三", "account": "13800138000" },
  "payee_ref": 0,                         // 可选：复用历史收款信息（与 payee 二选一）
  "confirmed_fx_rate": "7.30000000"       // 可选：前端预估汇率，用于偏差二次确认
}
```
**U2 响应**
```jsonc
{ "success": true, "data": {
  "withdraw_no": "QYW2026073094f1a2b3c4d5", "status": "pending", "method": "fiat",
  "commission_amount": "500000", "currency": "CNY",
  "frozen_fx_rate": "7.30000000", "frozen_quota_per_unit": "500000",
  "gross_amount": "7.30", "fee_amount": "0.00", "net_amount": "7.30",
  "payee_masked": "支付宝 138****8000 / 张*", "created_at": 1785340800
}}
```
> 若 `confirmed_fx_rate` 与服务端实际值相对偏差 > 1%，返回 `409` + `data.actual_fx_rate`，前端弹二次确认。**幂等**：同 `request_key` 重复提交返回 200 + 原单，不报错。

**U3 请求**：`?p=1&page_size=20&status=pending,paid&method=fiat&start=&end=`
**U3 响应**：`{ items: [WithdrawalBrief], total, page, page_size, summary:{ total_paid_quota, total_paid_fiat, pending_count } }`
`WithdrawalBrief` 不含 `payee_digest`、不含任何明文 PII。

**U4 响应**：单据全字段（脱敏）+ `events: [{action, from_status, to_status, actor_type, actor_name, reason, created_at}]`。
> **用户可见 `reject_reason` 与 `payout_ref`**（需求明确要求）；但 `actor_name` 对普通用户降级为「管理员」，不泄漏具体管理员账号；`Detail` 字段不下发给普通用户。

### 5.2 管理端（`middleware.AdminAuth()` —— 自动挂 `beginAdminAudit`，见 `middleware/auth.go:68-75`）

| # | Method | Path | 说明 |
|---|---|---|---|
| A1 | GET | `/api/qy/admin/withdrawal` | 审核队列（多维筛选 + 排序） |
| A2 | GET | `/api/qy/admin/withdrawal/stats` | 角标：待审 / 待打款 / 对账异常 数与金额 |
| A3 | GET | `/api/qy/admin/withdrawal/:id` | 详情（**脱敏**）+ 完整时间线 + 用户画像（历史提现次数/金额、risk_flag） |
| A4 | POST | `/api/qy/admin/withdrawal/:id/payee` | **查看明文收款信息**（写 PII 审计） |
| A5 | POST | `/api/qy/admin/withdrawal/:id/approve` | 审核通过 |
| A6 | POST | `/api/qy/admin/withdrawal/:id/reject` | 拒绝（`reason` 必填） |
| A7 | POST | `/api/qy/admin/withdrawal/:id/pay` | 标记已打款（`payout_ref` 必填，**仅 `method=fiat`**，见 T10） |
| A7b | POST | `/api/qy/admin/withdrawal/:id/credit` | 立即兑现（**仅 `method=quota` 的 `approved` 单**，见 T12） |
| A8 | POST | `/api/qy/admin/withdrawal/:id/fail` | 标记打款失败（`fail_reason` 必填） |
| A9 | POST | `/api/qy/admin/withdrawal/:id/resolve` | 对账异常裁决 |
| A10 | POST | `/api/qy/admin/withdrawal/batch` | 批量 approve / reject（≤100 条） |
| A11 | GET | `/api/qy/admin/withdrawal/export` | CSV 导出 |
| A12 | GET | `/api/qy/admin/pii-audit` | PII 访问审计查询 |

**A1 请求**：`?p=1&page_size=20&status=pending&method=&user_id=&keyword=&start=&end=&amount_min=&amount_max=&risk_only=false&sort=created_at&order=asc`
**A1 响应 item**：单据全字段（脱敏）+ `user: {id, username, email_masked, created_at}` + `risk: {duplicate_payee_users:[..], first_withdrawal:bool}`

**A4 请求/响应**
```jsonc
// req
{ "reason": "核对银行卡户名" }              // 建议强制，≥4 字符
// resp
{ "success": true, "data": { "channel":"bank", "payee": { "real_name":"张三", "bank_name":"招商银行",
  "branch":"深圳分行", "account_no":"6214830112345678" }, "audit_id": 8811 } }
```

**A6 请求**：`{ "reason": "收款人姓名与实名信息不一致" }`
后端：`reason` trim 后非空 且 `utf8.RuneCountInString(reason) <= 200`，否则 400。

**A7 请求**：`{ "payout_ref": "20260730123456789", "paid_at": 1785340800, "payout_note": "招行转账" }`
`payout_ref` 必填（1..128）；`paid_at` 为 0 时取 now；不允许 `paid_at > now + 300`。

**A9 请求**：`{ "decision": "confirmed_paid" | "not_paid", "evidence": "主库 logs id=99123 已确认" }`

**A10 请求**：`{ "action": "approve"|"reject", "ids": [1,2,3], "reason": "..." }`
**A10 响应**：`{ succeeded:[...], failed:[{id, message}] }` —— **逐单独立事务**，不允许一单失败回滚全部（批量审核里 A 单成功 B 单余额异常是正常业务，不是系统故障）。

**A11 导出**：默认列不含明文 PII。`?include_payee=true` 时**逐单解密并逐单写 `qy_pii_audits`（action=export_plain）**，且需要 `RootAuth()` 级别（建议）。

---

## 6. 原项目改动清单

### 6.1 后端：**0 个原有文件、0 行**

本模块所有后端代码位于新建的 `qianye/` 包，通过地基已预算的三处 `main.go` 单行 hook 挂载，**不额外消耗 D3 的 ≤10 文件 / ≤40 行预算**。

唯一涉及原项目 `model` 包的是**纯新增文件**（不改任何现有文件，合并上游零冲突）：

| 文件 | 类型 | 内容 | 冲突风险 |
|---|---|---|---|
| `model/qy_export.go` | **新建**（与地基/其他模块共用同一文件） | 本模块只需一个符号 | **低**（`Qy` 前缀规避上游同名） |

```go
package model

import "gorm.io/gorm"

// QyLockForUpdate 导出 model 包私有的 lockForUpdate（model/locking.go:20）。
// 提现兑现在主库事务内锁 users 行时必须用它 —— SQLite 上它是 no-op，
// 因此调用方还必须自带 WHERE 条件 + RowsAffected 兜底。
func QyLockForUpdate(tx *gorm.DB) *gorm.DB { return lockForUpdate(tx) }
```

> 本模块**不需要** `cacheIncrUserQuota` 的导出封装：提现兑现走 `model.InvalidateUserCache`（已导出）更稳（recon-user-quota §8.3 方案 B 的建议，低频高价值操作用失效而非增量）。

### 6.2 前端：2 个原有文件、共 4 行

| # | 文件:行号 | 插入的确切代码 | 冲突风险 |
|---|---|---|---|
| F1 | `web/src/features/wallet/index.tsx` import 区（约 :19-56 之间，紧邻其他 feature import） | `import { QyWithdrawalEntryCard } from '@/features/qy-withdrawal/components/qy-withdrawal-entry-card'` | **低**（import 块尾部追加） |
| F2 | `web/src/features/wallet/index.tsx:350` 之后（`<AffiliateRewardsCard ... />` 的自闭合 `/>` 之后、:351 的 `</div>` 之前） | `<QyWithdrawalEntryCard />` | **中**（该页是上游高频改动页；但只有 1 行、位于块尾，冲突易解） |
| F3 | `web/src/hooks/use-sidebar-data.ts:110` 之后（`id:'personal'` 分组内 `Wallet` 条目 `},` 之后） | `{ title: t('qy_wd_nav_title'), url: '/qy-withdrawal', icon: BanknoteArrowDown },` | **中**（纯数组元素插入） |
| F4 | `web/src/hooks/use-sidebar-data.ts` 的 `id:'admin'` 分组 items 末尾（约 :159） | `{ title: t('qy_wd_admin_nav_title'), url: '/qy-withdrawal-review', icon: ClipboardCheck },` | **中**（同上；该分组自动只对 `role>=10` 可见） |

> F3/F4 的图标需在该文件顶部 `lucide-react` import 里补两个符号 —— 若要把改动压到最小，可复用已 import 的图标（如 `Wallet`/`ShieldCheck`），则 F3/F4 各仍是 1 行。
> F1/F2 是「钱包页直接改原文件」（D4 已拍板不 fork）。
> `web/src/routeTree.gen.ts` 会被 TanStack 插件自动重写 —— 合并上游冲突时**删除后重跑 `bun run build`**，写进合并流程文档。

**纯追加、不算改动**：`web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` × 7，全部 `qy_wd_*` 下划线扁平键（**禁止点号**，i18next `keySeparator` 默认 `.` 会当嵌套）。

**改动预算小计：后端 0 行，前端 4 行（+7 个 locale 文件纯追加）。**

---

## 7. 并发与边界

### 7.1 竞态清单

| 竞态 | 场景 | 防护 |
|---|---|---|
| 重复提交 | 用户双击 / 多标签 / 客户端超时重试 / 多节点负载均衡 | `(user_id, request_key)` **唯一索引**（唯一硬防线）+ 冲突时返回原单（幂等成功）+ 进程内 per-user `TryLock`（防抖）+ `CriticalRateLimit()` |
| 佣金超额提现 | 两张单并发申请，各自读到同一个 available | `Commission.Freeze` 内 `UPDATE ... WHERE available >= ?` 的 **CAS**，靠 `RowsAffected` 判定，不靠先读后写 |
| 审核并发 | 两个管理员同时点通过 / 一个通过一个拒绝 | `UPDATE ... WHERE id=? AND status='pending'`，`RowsAffected==0` → 409「该申请已被处理」并返回当前状态 |
| 兑现并发 / 重复加钱 | 多节点补偿任务同时拾起同一张 approved 单 | S1 的 `approved → paying` CAS 是**全集群唯一准入闸门**；`RowsAffected==0` 直接放弃。**S2 任何情况下都不自动重试** |
| 后台任务双跑 | `common.IsMasterNode` 只是环境变量，多节点都配 master | 新库 `qy_task_leases` 租约锁（地基第 8 条），lease key = `withdrawal_reconcile` / `withdrawal_pii_purge` |
| 撤销 vs 审核 | 用户点撤销的同时管理员点通过 | 两侧都是 `WHERE status='pending'` 的 CAS，先到者胜，后到者 409 |
| 提现冻结 vs 返佣入账 vs clawback | 三方并发改同一个佣金账户行 | 全部在新库事务内对账户行 `SELECT ... FOR UPDATE`（新库是 MySQL，`FOR UPDATE` 有效），且写操作一律带 CAS 条件 |
| 主库死锁 | 兑现只锁**一行** `users`（不像划转要锁两行） | 天然无死锁；但仍固定「先锁 users 再做任何事」的顺序 |

### 7.2 边界与异常

| 边界 | 处理 |
|---|---|
| `commission_amount <= 0` | 400 |
| `method=quota` 且 `commission_amount` 含小数余数 | 400「站内额度兑换需为整数额度」（避免静默截断吞掉用户的钱） |
| `quota > common.MaxQuota` | 400；配置 `quota_max_per_order` 在加载时被硬顶到 `common.MaxQuota` |
| **接收方溢出**：`user.Quota + quota > common.MaxQuota` | 在 S2 主库事务内、UPDATE **之前**校验 → `ErrDeterministic` → 单据 `failed` + 佣金退回 + 文案「您的账户余额已达上限，请先消费后再提现」。**原项目 `increaseUserQuota` 完全没有这个校验**，MySQL 严格模式会报错、非严格模式会截断 |
| `user.Quota` 为负（历史欠费） | 不阻止提现（加钱是把负数往回填），但在管理端详情展示告警 |
| `frozen_fx_rate <= 0` 或 `frozen_quota_per_unit <= 0` | 拒绝建单，500 + `SysError` 告警（管理员把 `USDExchangeRate` 改成 0 是可能的） |
| `net_amount <= 0`（手续费吃掉全部） | 400「扣除手续费后金额为 0」 |
| `gross < fiat_min_amount` | 400，且响应带 `min_amount` 供前端提示 |
| 用户被禁用 / 软删除 | 申请时拒绝；**审核通过时二次校验**（申请到审核可能隔几天）；S2 内第三次校验 |
| `user_note` / `reject_reason` 超 200 rune | 400，前后端双校验 |
| PII 密钥缺失或非 32 字节 | 加载配置时 `fiat` 从 `methods` 中剔除 + `SysError`，**绝不明文落库** |
| 解密失败（密钥轮换后旧密文） | 按 `key_version` 选历史密钥；仍失败 → 返回 `payee_masked` + 明确错误「收款信息无法解密，请联系用户重新提供」，不 500 |
| 新库不可用 | 提现全部接口 **503**（非热路径，fail-close）；已在 `paying` 的单由补偿任务恢复 |
| LOG_DB 是 ClickHouse | 补偿任务的反查用 `content LIKE '%claimNo%'`（不依赖 JSON 函数），故 claim_no 必须进 content 文本 |
| 时区 | 「今日/本月」按服务器本地时区，统一由一个 `dayStart()/monthStart()` 工具计算；配置里给 `stats_timezone_offset_minutes` 允许覆盖（建议） |

### 7.3 金额计算规范（AGENTS.md 强制）

- **quota ↔ int 的每一次转换**必须过 `common.QuotaFromDecimal(d)` / `common.QuotaRound(f)`，禁止裸 `int(x)`。
- **佣金/法币的每一次算术**用 `github.com/shopspring/decimal`（已是项目依赖，`common/quota_math.go:7`），禁止 float64 中间量。
- 换算链：
  ```go
  usd   := commissionAmount.Div(frozenQuotaPerUnit)            // decimal，不 Round
  gross := usd.Mul(frozenFxRate).Round(4)
  fee   := decimal.Max(gross.Mul(feeRate).Add(feeFixed), feeMin).Round(4)
  net   := gross.Sub(fee)                                       // 恒等式：gross == fee + net
  ```
  `Round(4)` 只在写库前做一次，中间量保持全精度。落库前断言 `gross.Equal(fee.Add(net))`，不成立则拒绝建单（防止四舍五入把三个字段算不平）。
- 前端与后端之间**所有 decimal 走 string**，禁止 JSON number（JS `Number` 精度 2^53，`decimal(24,8)` 会丢位）。

---

## 8. PII 处理（建议补充，但强烈建议按此实施）

### 8.1 存储

- 算法：**AES-256-GCM**，`AAD = withdraw_no`。绑定 AAD 使密文无法被搬到另一张单上复用。
- 密钥来源：YAML `withdrawal.pii_key`（base64 32 字节），**不落任何数据库**。支持 `pii_key_version` + `pii_keys_history` 数组做轮换。
- Nonce：每条 12 字节 `crypto/rand`，独立列存储。
- 落库：`Cipher varbinary(4096)`，`Nonce varbinary(16)`。
- 索引/去重靠 `Digest = HMAC-SHA256(digest_key, 归一化后的账号)`，**明文永不建索引**。

  ⚠ 原像曾是整条收款信息（`canonicalJSON`，含自由文本 `real_name` / `bank_name` /
  可选 `branch`，且 email 不折大小写）。那与本列的用途——「同一**收款账号**被多个
  user_id 使用」——不是同一个问题：同一张卡只要在支行栏里多打一个字符、把「招商银行」
  写成「招商银行股份有限公司」，指纹就完全不同，`shared_payee` 永不触发。端到端实测
  三个账号同一张卡，只有字段逐字相同的那个报警。而且它不需要攻击者知道这回事——
  三个小号各自手填一遍，写法上的自然差异就够让报警消失，漏报是常态而非例外。
  现在原像只取该渠道的账号字段（bank→`account_no`、alipay/wechat→`account`、
  paypal→`email`、usdt_trc20→`address`）并按渠道归一：一律去空白；邮箱/账号折小写；
  银行账号折大写并去连字符；USDT 地址是 Base58，大小写是地址本身，一个字符都不折。
  认不出账号字段时回落到整条记录的旧口径——**不能**回落到空原像，否则一批记录会
  摘出同一个指纹，既互相误标红，又会让幂等重放把两个不同的收款人判成同一笔。
  改口径之前写下的行仍是旧口径，与新行互不命中：本仓从未上线，无存量线索需要迁移。

### 8.2 展示脱敏

`Masked` 在**写入时一次生成**并明文存储（列表页零解密）：

| channel | 脱敏规则 | 示例 |
|---|---|---|
| alipay / wechat | 手机号保留前 3 后 4；邮箱保留首字符 + 域名 | `支付宝 138****8000 / 张*` |
| bank | 卡号保留后 4；银行名完整；姓名保留姓 | `招商银行 ****5678 / 张*` |
| usdt_trc20 | 地址保留前 6 后 6 | `TRC20 TXabcd…mn12PQ` |
| paypal | 邮箱保留首字符 | `PayPal z***@gmail.com` |

姓名脱敏用 rune 切片（`[]rune(name)[:1] + "*"`），不能用字节切片（会切坏中文）。

### 8.2.1 人工决定的四眼原则

六个管理端人工决定（通过 / 拒绝 / 标记已打款 / 立即兑现 / 标记打款失败 / 人工裁决）
一律经 `loadDecidableWithdrawal` 取单，**申请人是操作人本人时返回 403
`qy_wd_self_review`** 并写一条 `withdraw.self_review_denied` 失败审计。

理由是资金安全而不是流程洁癖：管理端还有一条手工增减佣金的接口，
「自己给自己记一笔佣金 → 自己发起提现 → 自己批准」在改造前是一条无需第二个账号、
零事前控制的自助铸币链（`quota` 单在批准瞬间直接跨库落进主库 `users.quota`）。
判据在 `qianye/guard/fund_actor.go`，与手工增减佣金共用同一份。

拒绝**包含**退回佣金的那几个决定：四眼原则管的是「谁有资格对这张单下结论」，
不是「这次结论对申请人有没有好处」。申请人想撤掉自己的单仍然走用户端的
`POST /withdraw/:id/cancel`，不需要管理员身份，所以这道闸门不会把任何人的单据锁死。

### 8.3 访问控制与审计

- 明文只能通过 **A4（需 `AdminAuth()`）** 获取，且：
  - **必须带一张 `withdraw.payee.read` 范围的安全证明**（`X-Security-Proof`，由 2FA
    或 Passkey 现场签发，见 `middleware.SecurityProofScopeWithdrawPayeeRead`）。
    这道闸门排在事由校验与取单之前，且 **PAT 认证一律过不去**（PAT 没有会话身份，
    `RequireSecurityProof` 直接判 `SECURITY_PROOF_INVALID`）——
    「拿到管理员会话/PAT ＝ 拿到全站收款明文」这条路因此被封死。
    刻意**不**抬成 `RootAuth()`：收款账号是法币打款动作本身必须看到的东西，
    收成 root 专属等于全站只有 root 能付款，运营会绕道把明文抄进工单，PII 反而流得更散。
  - 强制填写 `reason`（≥4 字符）；
  - 每次调用写一条 `qy_pii_audits`；
  - 前端**不缓存**（`react-query` 用 `gcTime: 0`），弹窗关闭即从内存丢弃，不写 `localStorage`；
  - 建议对 `role < RootUser` 的管理员加**日访问次数上限**（如 50 次/日），超限告警。
- A11 导出明文建议提升到 `RootAuth()`。
- `qy_pii_audits` 只增不改不删，管理端 A12 可查（建议仅 `RootAuth()` 可查，防止管理员自查自删痕迹）。
- **单据本身不会被审计漏掉**：`AdminAuth()` 中间件（`middleware/auth.go:68-75`）已自动为所有管理端写接口留原项目审计。

### 8.4 保留期

后台任务 `withdrawal_pii_purge`（每日 1 次，持租约锁）：

```sql
UPDATE qy_withdrawal_payees p JOIN qy_withdrawals w ON w.id = p.withdrawal_id
SET p.cipher = NULL, p.nonce = 0x, p.purged_at = :now
WHERE w.status IN ('paid','rejected','cancelled','failed')
  AND w.updated_at < :now - :retention_days*86400
  AND p.purged_at = 0;
```
同步置 `qy_withdrawals.pii_purged_at`。**保留 `Masked` 与 `Digest`**（风控与对账仍可用），删除明文能力。财务单据本体永久保留（法务/税务需要）。

---

## 9. 风控（建议补充）

已在 §4.4 列出校验项，此处补充策略取值理由与联动：

| 项 | 默认 | 理由 |
|---|---|---|
| `maturity_days = 7` | 佣金冻结期 | **必须有**。没有它，攻击链是：充值 → 拿返佣 → 立即提现 → 申请退款。7 天覆盖主流支付渠道的争议窗口起点。与 D4 的 `refund_clawback` 联动：clawback 优先冲抵未成熟佣金；已提现部分转为账户负余额，下次返佣先抵扣，负余额存在时禁止新提现 |
| `max_pending_orders = 1` | 同时最多 1 张未终态单 | 极大简化并发推理；也让「批量小额单淹没审核队列」的骚扰不成立 |
| `max_per_day = 3` / `max_per_month = 10` | 次数限流 | 兜住脚本刷单；同时保护人工审核工作量 |
| `quota_min = QuotaPerUnit`（$1） | 最低提现额 | 与原项目 `TransferAffQuotaToQuota`（`model/user.go:503`）的最小额度语义一致，用户不困惑 |
| `fiat_min_amount = 50 CNY` | 法币最低额 | 线下打款有人工成本，小额不划算 |
| `cooldown_seconds` | 冷却 | 默认 0（quota 路径体验优先），法币建议 3600 |
| `payee_digest` 跨用户命中 | 不拒绝，标红 | 家庭共用账号是真实场景，硬拒绝误伤大；交人工判断 |
| 首次提现 | 标记 `first_withdrawal` | 管理端优先人工看 |
| 手续费 | 默认 0 | 平台策略；但字段与快照必须先建好，后加不用改表 |

**并发重复提交的三层防护**（缺一不可）：`request_key` 唯一索引（正确性） > 进程内 TryLock（降低 DB 冲突） > `CriticalRateLimit()`（防刷）。

---

## 10. 前端页面

### 10.1 新建文件（纯新增，不改原项目）

**用户端**

| 文件 | 说明 |
|---|---|
| `web/src/routes/_authenticated/qy-withdrawal/index.tsx` | 路由。**无 `beforeLoad`**（`_authenticated` 已保证登录）；`validateSearch` = zod `{p, page_size, status[], method}` |
| `web/src/features/qy-withdrawal/index.tsx` | 页面入口 `export function QyWithdrawal()`，`SectionPageLayout` + `SectionPageLayout.Title/Actions/Content` |
| `web/src/features/qy-withdrawal/api.ts` | `import { api } from '@/lib/api'`，U1–U6 六个函数 |
| `web/src/features/qy-withdrawal/types.ts` | 与后端 DTO 一一对应；**所有 decimal 字段类型为 `string`** |
| `web/src/features/qy-withdrawal/constants.ts` | `WITHDRAWAL_STATUS`、`STATUS_TONE`（映射 `StatusBadge` 的 tone）、`PAYEE_CHANNELS` |
| `web/src/features/qy-withdrawal/lib/withdrawal-form.ts` | `getWithdrawalFormSchema(t)` —— zod，含 **rune 计数校验** |
| `web/src/features/qy-withdrawal/lib/format.ts` | `formatCommission`、`formatFiat`、`maskPreview` |
| `web/src/features/qy-withdrawal/hooks/use-withdrawal-config.ts` | `useQuery(['qy-wd-config'])` |
| `web/src/features/qy-withdrawal/components/qy-withdrawal-entry-card.tsx` | **挂在钱包页**的入口卡（可提现佣金 + 「申请提现」+ 「提现记录」） |
| `.../components/withdrawal-summary-cards.tsx` | 可提现 / 冻结中 / 累计已提 三个指标 |
| `.../components/withdrawal-apply-drawer.tsx` | 申请表单（`Sheet` + RHF + zod） |
| `.../components/payee-fields.tsx` | **按 channel 动态渲染**收款字段 |
| `.../components/withdrawal-history-table.tsx` | `DataTablePage` + `useTableUrlState` + `useDataTable`（`manualPagination`） |
| `.../components/withdrawal-columns.tsx` | 列定义 |
| `.../components/withdrawal-detail-dialog.tsx` | 详情弹窗 |
| `.../components/withdrawal-timeline.tsx` | **时间线组件**（复用于管理端） |

**管理端**

| 文件 | 说明 |
|---|---|
| `web/src/routes/_authenticated/qy-withdrawal-review/index.tsx` | `beforeLoad` 内 `if (!auth.user \|\| auth.user.role < ROLE.ADMIN) throw redirect({to:'/403'})` |
| `web/src/features/qy-withdrawal-review/index.tsx` | 队列页，顶部 `Tabs`（待审核 / 待打款 / 对账异常 / 全部），Tab 上带数量角标（A2） |
| `web/src/features/qy-withdrawal-review/api.ts` / `types.ts` / `constants.ts` | A1–A12 |
| `.../components/review-queue-table.tsx` | `DataTablePage` + `DataTableBulkActions`（批量） |
| `.../components/review-columns.tsx` | 含 `risk_flag` 标红列 |
| `.../components/dialogs/approve-dialog.tsx` | 通过确认（`ConfirmDialog`），展示「通过后将自动到账 $X」 |
| `.../components/dialogs/reject-dialog.tsx` | **拒绝理由必填**，预设理由下拉 + 自定义 |
| `.../components/dialogs/mark-paid-dialog.tsx` | 打款单号必填 + 打款时间 `DatetimePicker` + 备注 |
| `.../components/dialogs/payee-reveal-dialog.tsx` | **查看明文**：先填事由 → 调 A4 → 展示 + 逐字段 `CopyButton` + 顶部红色审计提示条 |
| `.../components/dialogs/reconcile-resolve-dialog.tsx` | 对账裁决 |
| `.../components/batch-review-bar.tsx` | 批量操作条 |
| `.../components/export-button.tsx` | 导出（明文导出二次确认 + 事由） |
| `web/src/lib/qy-route-guards.ts` | `requireQyAdmin()`，只给自己的新路由用，不重构原有 8 处 inline guard |

### 10.2 主要交互

**申请表单（`withdrawal-apply-drawer.tsx`）**
1. 顶部展示 `withdrawable_commission`（可提）/ `maturing_commission`（冻结期内，带 tooltip 说明「返佣满 7 天后可提」）。
2. **方式选择**：Base UI `Tabs`（站内额度兑换 / 线下法币打款），由 `config.methods` 门控；只有一种时不显示 Tab。
3. **金额输入统一以佣金 quota 为准**（法币路径也是），实时联动展示：
   - quota 路径：`将到账 500,000 额度（≈ $1.00）`
   - fiat 路径：`应付 ¥7.30 − 手续费 ¥0.00 = 实付 ¥7.30`，下方灰字「汇率以提交时为准」
   > **为什么不让用户直接输法币金额**：反算 quota 必然产生舍入，会出现「输 ¥100 实扣 ¥100.0001 佣金」的对账噪声。统一以 quota 为输入，换算永远单向。
4. 快捷按钮：25% / 50% / 全部。
5. **收款信息**（fiat）：channel 选择 → `payee-fields.tsx` 动态渲染。提供「使用上次的收款方式」（U6，选后只显示脱敏值 + `payee_ref`）。
6. **说明输入**：`Textarea` + 实时计数器 `{count}/200`，计数用 `[...value].length`（code point，与 Go rune 一致；`value.length` 会把 emoji 算成 2）。zod：
   ```ts
   userNote: z.string().max(1000).refine((s) => [...s].length <= 200, { message: t('qy_wd_note_too_long') })
   ```
7. 提交前 `ConfirmDialog` 二次确认，列出：方式、扣除佣金、到账金额、冻结汇率、收款信息（脱敏）、说明。
8. `request_key` 用 `crypto.randomUUID()` 在 **Drawer 打开时**生成并存 ref，提交失败重试沿用同一个（保证幂等）。
9. `onSuccess` → `invalidateQueries(['qy-wd-list'])`、`(['qy-wd-config'])`、`(['self'])`。

**历史列表（`withdrawal-history-table.tsx`）**
- 列：单号 / 方式 / 佣金额 / 到账金额 / 状态 / 申请时间 / 完成时间 / 操作。
- 状态用 `StatusBadge`：`pending`=warning、`approved`=info、`paying`=info(带 spinner)、`paid`=success、`rejected`/`failed`=destructive、`cancelled`=muted。
- **`rejected` 行直接在展开区/副标题显示拒绝理由前 40 字**（需求的核心诉求，不能只藏在详情里），全文点详情看。
- `paid` 行显示 `paid_at` + `payout_ref`（`TruncatedCell` + `CopyButton`）。
- 移动端 `MobileCardList`（不会自动继承桌面列，需单独配 card renderer）。
- **空态**（`EmptyState`）：无记录时展示「还没有提现记录」+ 「立即申请」按钮；若 `withdrawable=0` 则改为「暂无可提现佣金，去看看如何推广」+ 跳钱包页邀请卡。

**时间线（`withdrawal-timeline.tsx`）**
垂直时间轴，每个节点 = 一条 event：图标 + 状态文案 + 操作者（普通用户看到「管理员」）+ 时间 + reason。`paying` 中的单显示脉冲动画 + 「正在为您入账，通常 1 分钟内完成」。`reconcile_state=hold` 对用户显示「处理中，如超过 24 小时未到账请联系客服」（**不暴露内部对账术语**）。

**管理端队列**
- 顶部 4 个统计卡（待审核数/金额、待打款数/金额、对账异常数、今日已处理）。
- 表格支持多选 → `DataTableBulkActions` 批量通过/拒绝（批量拒绝要求填统一理由）。
- 行内快捷按钮：通过 / 拒绝 / 查看收款信息 / 标记打款。
- **危险操作全部二次确认**；「查看收款信息」按钮带锁图标 + 悬浮提示「此操作将被审计记录」。
- 批量结果用 toast 汇总 `成功 8 条，失败 2 条`，失败明细在弹窗列出。

### 10.3 i18n

全部 `qy_wd_*` 下划线扁平键，写进 7 个 locale。示例：
```
qy_wd_nav_title / qy_wd_admin_nav_title / qy_wd_apply / qy_wd_method_quota / qy_wd_method_fiat
qy_wd_status_pending|approved|paying|paid|rejected|cancelled|failed
qy_wd_reject_reason / qy_wd_reject_reason_required / qy_wd_paid_at / qy_wd_payout_ref
qy_wd_note_label / qy_wd_note_placeholder / qy_wd_note_too_long
qy_wd_insufficient_commission / qy_wd_maturity_tip / qy_wd_fx_frozen_tip / qy_wd_fx_changed_confirm
qy_wd_limit_daily / qy_wd_limit_monthly / qy_wd_limit_pending / qy_wd_cooldown
qy_wd_empty_title / qy_wd_empty_desc / qy_wd_reveal_payee / qy_wd_reveal_audit_notice
qy_wd_service_unavailable
```
常量里的状态文案（不是 `t('字面量')` 形式）必须登记到 `web/src/i18n/static-keys.ts`。新文件全部需要 AGPL 版权头（`bun run copyright`）。

---

## 11. 我建议补充的

> 以下均为用户未提、但不做会出问题的设计，明确标注为**建议**。

1. **【建议·高】佣金冻结期 `maturity_days`**。不做的话，「充值 → 拿返佣 → 提现 → 退款」是一条闭合的套利链，且 D4 的 `refund_clawback` 开关默认关闭，等于门户大开。这是本模块最重要的风控项。

2. **【建议·高】拒绝自动判 failed，改人工 hold**。§4.2 的 R3 已详述。自动化在资金路径上的每一次「猜」都会成为攻击面。

3. **【建议·高】PII 加密 + 明文访问审计 + 保留期**。收款信息含银行卡号/实名，明文落库在很多司法辖区直接违规。加密方案（§8）实现成本约 120 行 Go，收益极高。密钥缺失时**降级为关闭法币路径**而不是降级为明文。

4. **【建议·中】`payee_digest` 跨账户命中告警**。刷单党的典型特征是「N 个小号 → 同一个收款账号」。有了 digest 索引，这是一条 SQL 的事。

5. **【建议·中】用户端不暴露具体管理员姓名**。event 的 `actor_name` 对普通用户降级为「管理员」，管理端才显示真名 —— 否则用户可以枚举出全部管理员账号。

6. **【建议·中】通知**。复用**已导出、零改动**的 `service.NotifyUser(userId, email, userSetting, dto.NewNotify(...))`（`service/user_notify.go:50`），它自带邮件/Webhook/Bark 三通道 + `CheckNotificationLimit` 频控。触发点：审核通过、拒绝（带理由）、已打款（带单号）、失败（带原因）。**同时**用 `model.RecordLog(userId, model.LogTypeSystem, content)` 写一条主库日志 —— 用户在原项目「日志」页就能看到，零前端改动的兜底通知。通知失败一律吞掉只记 SysLog，**绝不能因为通知失败回滚资金操作**。

7. **【建议·中】管理端「对账异常」独立 Tab + `NotifyRootUser` 告警**。hold 状态的单必须有人看，不能静默堆积。

8. **【建议·中】错误文案分级**。资金类错误对用户要具体（「可提现佣金不足，当前可提 $2.50」），系统类错误要模糊（「服务暂不可用，请稍后重试」，细节只进 `SysError`）—— 不要把「新库连接超时」这种内部信息回显给用户。

9. **【建议·中】导出与统计的口径固定**。CSV 列固定为：单号、用户ID、用户名、方式、佣金额(quota)、冻结QuotaPerUnit、冻结汇率、应付、手续费、实付、币种、收款渠道、收款(脱敏)、状态、申请时间、审核时间、审核人、拒绝理由、打款时间、打款单号、打款人。**冻结的两个价格参数必须进导出**，否则财务无法复核。

10. **【建议·低】`stats_timezone_offset_minutes`**。「今日 3 次」的边界在跨国团队里会引发争议，给个配置比事后解释便宜。

11. **【建议·低】提现单据的软删除策略：不删**。财务单据只有归档没有删除。若确实要清理，只清 `qy_withdrawal_events` 中超过 3 年的行，主表永久保留。

12. **【明确不做】`paid` 之后的自动冲正接口**。误打款走人工（原项目「用户管理 → 减少额度」+ 本模块 `payout_note` 留痕）。提供自动反向通道 = 提供一个可被误用/滥用的扣款接口，风险大于收益。
