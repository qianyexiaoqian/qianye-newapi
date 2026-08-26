# 需求1:用户余额划转

# 需求 1 — 用户间余额划转 · 实施设计

## 0. 模块边界与一句话结论

`users.quota` 在**原项目主库**，无法搬迁；本模块把**流水、风控计数、租约锁**放新库，把**双向 quota 变更**放主库事务，用「新库 pending → 主库事务 → 新库 success」的跨库两阶段串起来，并用 `logs.request_id = 划转单号`（LOG_DB 索引列）作为主库侧对账锚点。

**本需求对原项目后端的独占改动 = 0 行**（完全复用架构地基已锁定的 `main.go` 4 处共享 hook + 纯新增文件 `model/qy_export.go`）。前端改 2 个原文件共 7 行。

---

## 1. 表结构（新库 MySQL，`qianye/model/transfer.go`）

### 1.1 金额精度约定（先定死，后面所有字段依此）

| 项 | 决定 | 理由 |
|---|---|---|
| 划转金额存储类型 | `int64`（三方言一律 64 位） | 2026-08-26 更正：原先写"与主库 `users.quota` 逐位对齐、后者是 32 位"。那个前提是假的 —— `gorm:"type:int"` 选中的是 GORM 的通用 `schema.Int` 种类，方言仍按 64 位 Go int 定型，实测三方言建出来的都是 64 位（见 `model.TestQuotaColumnsAre64BitOnEveryDialect`）。真正的闸门是 `twophase.validateAmount` 的 `amount ≤ common.MaxQuota`，那是一条**算术**上界而不是列宽 |
| 累计/聚合类型 | `bigint`（`int64`） | `day_out_quota` / `lifetime_out_quota` 是多笔求和，必然超 int32 |
| 计算过程 | 一律 `decimal.Decimal` 或 `int64`，落库前经 `common.QuotaFromDecimalChecked` / 显式 `> common.MaxQuota` 判断 | AGENTS.md 强制；且 `QuotaFromDecimal` 会 clamp 而非报错，**必须用 `Checked` 变体拿到 `*QuotaClamp` 并当错误处理**，划转场景绝不允许静默截断 |
| 手续费 | 独立列 `fee_quota int`，默认 0 | 用户当前没要手续费，但列先建好，未来开启不需要 DDL |

> **金额单位**：全链路 quota（`common.QuotaPerUnit = 500000` = $1，`common/constants.go:22`）。前端用 `parseQuotaFromDollars` / `formatQuota`（`web/src/lib/format.ts:83/72`）换算，**后端只认 quota 整数**，绝不接受前端传美元浮点。

### 1.2 主表 `qy_transfer_orders`

```go
package model // qianye/model

// TransferOrder 是划转流水的唯一事实来源（新库）。
// 主库只有 users.quota 的最终数字，没有任何"谁转给谁"的记录，
// 所以这张表既是用户账单、也是跨库两阶段的状态机、也是对账依据。
type TransferOrder struct {
	Id int64 `gorm:"primaryKey;autoIncrement" json:"id"`

	// TransferNo 全局唯一单号，格式 QYT{fromUserId}T{toUserId}N{unixnano}{rand6}，<=40 字符。
	// 存在理由：① 跨库两阶段的关联键；② 写进主库 logs.request_id 作为对账锚点
	//（logs.request_id 是 varchar(64) 且带索引 idx_logs_request_id，model/log.go:78），
	//   所以单号长度必须 <=64；③ 用户报障时的凭证。
	TransferNo string `gorm:"type:varchar(40);not null;uniqueIndex:uk_qy_transfer_no" json:"transfer_no"`

	// IdempotencyKey = "{fromUserId}:{前端 request_id(UUID)}"。
	// 存在理由：防止用户双击/网络重试造成重复扣款。唯一索引是唯一可靠的防重手段
	//（进程内锁在多节点无效，Redis 锁会因宕机丢失）。
	IdempotencyKey string `gorm:"type:varchar(96);not null;uniqueIndex:uk_qy_transfer_idem" json:"-"`

	FromUserId int `gorm:"type:int;not null;index:idx_qy_tr_from_created,priority:1" json:"from_user_id"`
	ToUserId   int `gorm:"type:int;not null;index:idx_qy_tr_to_created,priority:1"   json:"to_user_id"`

	// 冗余快照用户名：主库用户可改名/软删，事后查流水时必须还原"当时是谁"。
	// 不做外键（跨库不可能有外键），也不实时 join 主库（会给主库加读压力）。
	FromUsername string `gorm:"type:varchar(64);not null;default:''" json:"from_username"`
	ToUsername   string `gorm:"type:varchar(64);not null;default:''" json:"to_username"`

	// Amount 收款方实收 quota；FeeQuota 额外从发起方扣的手续费（默认 0，不进收款方）。
	// 发起方实扣 = Amount + FeeQuota。分成两列而不是存"总额+费率"，
	// 是因为费率是可变配置，事后重算对不上账。
	Amount   int `gorm:"type:int;not null"           json:"amount"`
	FeeQuota int `gorm:"type:int;not null;default:0" json:"fee_quota"`

	// 状态机：pending -> success | failed | unknown。
	// unknown 专指"主库结果不可判定"（LOG_DB 独立部署时的锚点丢失窗口），
	// 必须与 failed 分开，否则补偿任务会把已扣款的单子错判为失败并二次退款。
	Status string `gorm:"type:varchar(16);not null;default:'pending';index:idx_qy_tr_status_created,priority:1" json:"status"`

	// FailCode 机器可读（前端 i18n 用），FailReason 人读原文（管理员排障用）。
	FailCode   string `gorm:"type:varchar(48);not null;default:''"  json:"fail_code"`
	FailReason string `gorm:"type:varchar(255);not null;default:''" json:"fail_reason"`

	// 用户填写的备注，双方流水都可见。限长 200，防止被当作留言板刷量。
	Remark string `gorm:"type:varchar(200);not null;default:''" json:"remark"`

	// 余额快照：主库事务内读到的锁定值。存在理由是事后争议时能证明
	// "扣款瞬间余额是多少"，主库 users 表没有历史版本。
	FromQuotaBefore int `gorm:"type:int;not null;default:0" json:"from_quota_before"`
	FromQuotaAfter  int `gorm:"type:int;not null;default:0" json:"from_quota_after"`
	ToQuotaBefore   int `gorm:"type:int;not null;default:0" json:"to_quota_before"`
	ToQuotaAfter    int `gorm:"type:int;not null;default:0" json:"to_quota_after"`

	// 风控取证：IP + UA。批量套现/盗号划转的排查全靠这两列。
	ClientIp  string `gorm:"type:varchar(64);not null;default:''"  json:"client_ip"`
	UserAgent string `gorm:"type:varchar(255);not null;default:''" json:"-"`

	// LogAnchored 标记主库锚点日志是否已确认写入。
	// true 时补偿任务可直接判定主库已提交，无需回查 LOG_DB。
	LogAnchored bool `gorm:"not null;default:false" json:"-"`

	CreatedAt       int64 `gorm:"type:bigint;not null;index:idx_qy_tr_from_created,priority:2;index:idx_qy_tr_to_created,priority:2;index:idx_qy_tr_status_created,priority:2" json:"created_at"`
	MainCommittedAt int64 `gorm:"type:bigint;not null;default:0" json:"main_committed_at"` // 主库事务提交时刻
	SettledAt       int64 `gorm:"type:bigint;not null;default:0" json:"settled_at"`        // 新库置终态时刻

	// 补偿任务已尝试次数 + 最后一次尝试时刻，用于退避与"卡死告警"。
	ReconcileAttempts int   `gorm:"type:int;not null;default:0"    json:"-"`
	LastReconcileAt   int64 `gorm:"type:bigint;not null;default:0" json:"-"`
}

func (*TransferOrder) TableName() string { return "qy_transfer_orders" }
```

**索引说明**

| 索引 | 用途 | 为什么必须有 |
|---|---|---|
| `uk_qy_transfer_no` (UNIQUE) | 单号查询 / 两阶段回写 | 唯一性是对账前提 |
| `uk_qy_transfer_idem` (UNIQUE) | 幂等 | **防重复扣款的最后一道也是唯一可靠的一道防线** |
| `idx_qy_tr_from_created` (from_user_id, created_at) | 我的转出流水分页 + 日累计聚合 | 风控每笔都要跑 `SUM` |
| `idx_qy_tr_to_created` (to_user_id, created_at) | 我的收款流水分页 + 收款方日限 | 同上 |
| `idx_qy_tr_status_created` (status, created_at) | 补偿任务扫 pending | 没这个索引，补偿任务会全表扫 |

### 1.3 风控状态表 `qy_transfer_user_state`

```go
// TransferUserState 每个用户一行，是风控限额的"串行化点"。
// 存在理由（关键）：日累计上限如果用 SELECT SUM(...) FROM qy_transfer_orders 现算，
// 两笔并发请求会同时读到旧值、同时通过校验（TOCTOU），限额形同虚设。
// 把限额校验 + pending 插入放进同一个新库事务、并对本行 FOR UPDATE，
// 才能真正串行化。
type TransferUserState struct {
	UserId int `gorm:"type:int;primaryKey" json:"user_id"`

	// DayBucket 形如 20260730（按 config.timezone_offset 计算的自然日）。
	// 与下面 day_* 字段配套：读到 bucket != 今天 就地重置，省掉定时清零任务。
	DayBucket int32 `gorm:"type:int;not null;default:0" json:"day_bucket"`

	DayOutQuota int64 `gorm:"type:bigint;not null;default:0" json:"day_out_quota"` // 今日累计转出（含手续费）
	DayOutCount int   `gorm:"type:int;not null;default:0"    json:"day_out_count"` // 今日转出笔数
	DayInCount  int   `gorm:"type:int;not null;default:0"    json:"day_in_count"`  // 今日收款笔数（防集中收款号）

	LastOutAt int64 `gorm:"type:bigint;not null;default:0" json:"last_out_at"` // 冷却期判定

	LifetimeOutQuota int64 `gorm:"type:bigint;not null;default:0" json:"lifetime_out_quota"`
	LifetimeInQuota  int64 `gorm:"type:bigint;not null;default:0" json:"lifetime_in_quota"`

	// PendingCount 未结算笔数。>0 时禁止发起新划转，
	// 避免"两阶段中间态"叠加导致余额与流水双重不一致。
	PendingCount int `gorm:"type:int;not null;default:0" json:"-"`

	UpdatedAt int64 `gorm:"type:bigint;not null;default:0" json:"-"`
}

func (*TransferUserState) TableName() string { return "qy_transfer_user_state" }
```

### 1.4 收款人解析审计表 `qy_transfer_lookup_logs`（**建议**，防用户名枚举）

```go
// 存在理由：/resolve 接口天然可枚举用户名。限流只能限速率，
// 无法发现"每天不超限但持续 30 天扫库"的慢速枚举。这张表提供事后追溯与告警依据。
// 保留期由后台任务按 config.lookup_log_retain_days 清理（默认 30 天）。
type TransferLookupLog struct {
	Id         int64  `gorm:"primaryKey;autoIncrement"`
	UserId     int    `gorm:"type:int;not null;index:idx_qy_lk_user_created,priority:1"`
	Identifier string `gorm:"type:varchar(64);not null;default:''"` // 原样保存用户输入
	ByType     string `gorm:"type:varchar(8);not null;default:''"`  // "id" | "name"
	Hit        bool   `gorm:"not null;default:false"`               // 是否命中
	ClientIp   string `gorm:"type:varchar(64);not null;default:''"`
	CreatedAt  int64  `gorm:"type:bigint;not null;index:idx_qy_lk_user_created,priority:2"`
}

func (*TransferLookupLog) TableName() string { return "qy_transfer_lookup_logs" }
```

### 1.5 后台任务租约表 `qy_task_leases`（**共享基础设施**）

架构地基 §8 要求"新库自建租约锁表"。本模块的补偿任务需要它；**若其它模块（返佣结算/提现对账）已定义同名表，直接复用，不要重复建**。

```go
// 存在理由：common.IsMasterNode 只是 NODE_TYPE 环境变量（common/init.go:89），
// 不是租约。多节点都配 master 时补偿任务会双跑，可能把同一笔 pending 结算两次。
type TaskLease struct {
	TaskKey   string `gorm:"type:varchar(64);primaryKey"`          // 如 "transfer_reconcile"
	Owner     string `gorm:"type:varchar(96);not null;default:''"` // 节点标识 common.NodeName + pid
	ExpiresAt int64  `gorm:"type:bigint;not null;default:0"`
	UpdatedAt int64  `gorm:"type:bigint;not null;default:0"`
}

func (*TaskLease) TableName() string { return "qy_task_leases" }
```

抢锁 SQL（单条原子 UPDATE，无需事务）：

```go
now := common.GetTimestamp()
res := db.Model(&TaskLease{}).
    Where("task_key = ? AND (expires_at < ? OR owner = ?)", key, now, owner).
    Updates(map[string]any{"owner": owner, "expires_at": now + ttl, "updated_at": now})
acquired := res.RowsAffected == 1
// 行不存在时先 INSERT IGNORE 兜底一次再重试
```

---

## 2. YAML 配置（`qianye/config`，`transfer` 段）

```yaml
transfer:
  enabled: true                       # 总开关；false 时所有 /api/qy/transfer/* 返回 403 且前端入口隐藏
  min_amount_quota: 500000            # 最小划转额，默认 $1（对齐 TransferAffQuotaToQuota 的既有下限，model/user.go:503）
  max_amount_per_tx_quota: 50000000   # 单笔上限，默认 $100
  max_daily_out_quota: 100000000      # 日累计转出上限，默认 $200
  max_daily_out_count: 10             # 日累计转出笔数
  receiver_max_daily_in_count: 20     # 收款方日收款笔数（防集中号）
  cooldown_seconds: 60                # 两笔转出之间的冷却
  min_account_age_hours: 24           # 账号注册满 N 小时才能转出（防批量小号）
  require_email_verified: false       # 是否要求发起方已绑定邮箱

  fee_rate: 0                         # 手续费率（0~1），默认 0
  fee_min_quota: 0                    # 手续费下限（rate>0 时生效）

  allow_lookup_by_username: true      # 见 §5 关键取舍。false 时只能按用户ID解析
  allow_admin_receiver: true          # 是否允许转给管理员账号
  gift_quota_policy: "unrestricted"   # unrestricted | require_topup | capped_by_topup（见 §7）
  max_lifetime_out_quota: 0           # 终身转出上限，0 = 不限

  timezone_offset_hours: 8            # 日累计的"自然日"口径
  reconcile_interval_seconds: 60      # 补偿任务周期
  reconcile_grace_seconds: 120        # pending 超过多久才进入对账
  reconcile_giveup_seconds: 600       # 超过多久仍无锚点则判 failed / unknown
  lookup_log_retain_days: 30
```

**降级语义（硬性）**：
- 配置文件缺失 → `config.Enabled() == false` → 路由不注册，前端 `GET /api/qy/transfer/config` 404 → 入口卡片不渲染。
- 配置存在但新库连不上 → 划转属**非热路径**，一律返回 **503**（`{"success":false,"message":"划转服务暂不可用，请稍后重试"}`）。**绝不能 fail-open**——fail-open 意味着扣了主库的钱却没有任何流水。

---

## 3. 打通 model 包私有能力：`model/qy_export.go`（**纯新增文件，0 行改动现有文件**）

```go
package model

import "gorm.io/gorm"

// QyLockForUpdate 导出 lockForUpdate（model/locking.go:20）。
// SQLite 下是 no-op，调用方必须自带 WHERE quota >= ? + RowsAffected 兜底。
func QyLockForUpdate(tx *gorm.DB) *gorm.DB { return lockForUpdate(tx) }

// QyLogDBSharesMainDB 报告 LOG_DB 是否就是主库。
// LOG_SQL_DSN 为空时 model/main.go:213 会执行 LOG_DB = DB，此时锚点日志
// 可以写在主库事务内，获得真正的原子性。
func QyLogDBSharesMainDB() bool { return LOG_DB != nil && LOG_DB == DB }

// QyCreateLogWithTx 在给定事务内写一条 logs 记录（仅在 QyLogDBSharesMainDB() 时可用）。
func QyCreateLogWithTx(tx *gorm.DB, log *Log) error {
	ensureLogRequestId(log)
	return tx.Create(log).Error
}

// QyCreateLog 走标准 createLog（写 LOG_DB）。
func QyCreateLog(log *Log) error { return createLog(log) }

// QyCacheApplyUserQuotaDelta 导出 cacheIncrUserQuota（model/user_cache.go:147）。
// 本模块默认不用它（改用 InvalidateUserCache），保留给需要增量同步的场景。
func QyCacheApplyUserQuotaDelta(userId int, delta int64) error {
	return cacheIncrUserQuota(userId, delta)
}
```

> **命名硬约束**：全部 `Qy` 前缀，规避上游未来加同名符号导致的编译冲突。

---

## 4. 完整 API 清单（前缀 `/api/qy/transfer`）

路由在 `qianye/router.go` 的 `RegisterRoutes(*gin.Engine)` 内注册，**不碰 `router/api-router.go`**：

```go
func RegisterRoutes(server *gin.Engine) {
	if !config.Enabled() { return }
	g := server.Group("/api/qy")
	// 复用原项目中间件，鉴权/审计/限流全部继承
	user := g.Group("/transfer", middleware.UserAuth())
	{
		user.GET("/config",  ctl.GetTransferConfig)
		user.POST("/resolve", middleware.SearchRateLimit(),   ctl.ResolveReceiver)
		user.POST("",         middleware.CriticalRateLimit(), ctl.CreateTransfer)
		user.GET("/self",     ctl.ListSelfTransfers)
		user.GET("/detail/:transfer_no", ctl.GetTransferDetail)
	}
	admin := g.Group("/transfer/admin", middleware.AdminAuth()) // 自动挂 beginAdminAudit（middleware/auth.go:68-75）
	{
		admin.GET("",  ctl.AdminListTransfers)
		admin.POST("/:transfer_no/reconcile", ctl.AdminReconcile)
	}
}
```

响应体统一 `{success, message, data}`（`common.ApiSuccess` / `common.ApiError`，`common/gin.go:212/199`）。

### 4.1 `GET /api/qy/transfer/config` — 普通用户

无请求体。响应 `data`：

```go
type TransferConfigDTO struct {
	Enabled              bool  `json:"enabled"`
	MinAmountQuota       int   `json:"min_amount_quota"`
	MaxAmountPerTxQuota  int   `json:"max_amount_per_tx_quota"`
	MaxDailyOutQuota     int64 `json:"max_daily_out_quota"`
	MaxDailyOutCount     int   `json:"max_daily_out_count"`
	CooldownSeconds      int   `json:"cooldown_seconds"`
	FeeRate              float64 `json:"fee_rate"`
	AllowLookupByUsername bool `json:"allow_lookup_by_username"`
	// 当前用户的实时可用额度，供前端直接渲染剩余额度条
	RemainingDailyQuota  int64 `json:"remaining_daily_out_quota"`
	RemainingDailyCount  int   `json:"remaining_daily_out_count"`
	CooldownUntil        int64 `json:"cooldown_until"`        // 0 = 不在冷却
	TransferableQuota    int   `json:"transferable_quota"`    // 见 §7 赠送额度策略
	BlockedReason        string `json:"blocked_reason"`        // "" | "account_too_new" | "no_topup" | "pending_exists"
}
```

### 4.2 `POST /api/qy/transfer/resolve` — 普通用户 + `SearchRateLimit`

```go
type ResolveReq struct {
	Identifier string `json:"identifier"` // 纯数字 => 按用户ID；否则按用户名精确匹配
}
type ResolveResp struct {
	Exists         bool   `json:"exists"`
	UserId         int    `json:"user_id"`          // 不存在时为 0
	MaskedUsername string `json:"masked_username"`  // 脱敏，如 "al***ce"
	MaskedEmail    string `json:"masked_email"`     // 脱敏，如 "a***@gmail.com"；无邮箱则 ""
	Receivable     bool   `json:"receivable"`       // 是否可作为收款方（未禁用、非自己、未超收款日限）
	BlockedReason  string `json:"blocked_reason"`   // "self" | "disabled" | "not_found" | "receiver_daily_limit" | "admin_receiver_blocked"
}
```

**绝不返回**：真实用户名全文、真实邮箱全文、quota、group、role、created_at。

### 4.3 `POST /api/qy/transfer` — 普通用户 + `CriticalRateLimit`

```go
type CreateTransferReq struct {
	ToUserId  int    `json:"to_user_id"`  // 必填，必须来自 /resolve 的返回值
	Amount    int    `json:"amount"`      // quota 整数，必填
	Remark    string `json:"remark"`      // 可选，<=200
	RequestId string `json:"request_id"`  // 必填，前端 crypto.randomUUID()，幂等键
	Confirm   bool   `json:"confirm"`     // 必须为 true，服务端二次确认标记
}
type CreateTransferResp struct {
	TransferNo   string `json:"transfer_no"`
	Status       string `json:"status"`         // success | pending
	Amount       int    `json:"amount"`
	FeeQuota     int    `json:"fee_quota"`
	ToUserId     int    `json:"to_user_id"`
	ToMasked     string `json:"to_masked_username"`
	MyQuotaAfter int    `json:"my_quota_after"`  // 前端直接刷新余额，不用再打 /api/user/self
	CreatedAt    int64  `json:"created_at"`
}
```

幂等命中（同 `RequestId` 重复提交）→ 返回**原单结果**（200 + `success:true`），不再次扣款。

### 4.4 `GET /api/qy/transfer/self` — 普通用户

Query：`p`（页码，默认 1）、`page_size`（默认 20，上限 100）、`direction`（`all|out|in`，默认 `all`）、`status`（可选）、`start`/`end`（秒级时间戳，可选）。

```go
type SelfTransferItem struct {
	TransferNo   string `json:"transfer_no"`
	Direction    string `json:"direction"`      // "out" | "in"
	Counterparty string `json:"counterparty"`   // 对手方脱敏用户名
	CounterpartyId int  `json:"counterparty_id"`
	Amount       int    `json:"amount"`
	FeeQuota     int    `json:"fee_quota"`      // direction=in 时恒为 0
	Status       string `json:"status"`
	FailCode     string `json:"fail_code"`
	Remark       string `json:"remark"`
	CreatedAt    int64  `json:"created_at"`
	SettledAt    int64  `json:"settled_at"`
}
// data: { items: []SelfTransferItem, total: int64, page: int, page_size: int }
```

**权限裁剪**：`WHERE from_user_id = :me OR to_user_id = :me`，且对手方用户名**服务端脱敏后**才出网；`client_ip` / `user_agent` / `fail_reason` 不下发给普通用户。

### 4.5 `GET /api/qy/transfer/detail/:transfer_no` — 普通用户

只允许查 `from_user_id = me || to_user_id = me` 的单据；否则统一返回"记录不存在"（**不要返回 403**，403 会泄漏"该单号存在"）。

### 4.6 `GET /api/qy/transfer/admin` — 管理员

Query 增加 `user_id`、`transfer_no`、`status`、`min_amount`、`start`/`end`。返回**完整字段**（含 `client_ip`、`fail_reason`、余额快照、真实用户名，不脱敏）。挂 `AdminAuth()` 后自动留审计痕迹。

### 4.7 `POST /api/qy/transfer/admin/:transfer_no/reconcile` — 管理员

对单笔 `pending`/`unknown` 立即执行一次对账判定。

```go
type AdminReconcileReq struct {
	// 仅当自动判定仍为 unknown 时允许人工裁决，必须显式给出，且会写审计日志
	ForceStatus string `json:"force_status"` // "" | "success" | "failed"
	Note        string `json:"note"`
}
```

---

## 5. 关键取舍：按用户名查收款人 vs. 用户名可枚举

GAPS §五.5 指出的矛盾属实。**结论如下：**

| 决定 | 内容 |
|---|---|
| **只做精确匹配** | `WHERE username = ?` 全等；**不提供** `LIKE`、前缀补全、下拉搜索。用户必须完整知道对方用户名或用户ID |
| **默认双通道，可降级到单通道** | `allow_lookup_by_username: true`（默认，体验优先）；平台若担心枚举，改成 `false` 后只能按用户ID解析 |
| **回显一律脱敏** | `/resolve` 与确认弹窗只回显 `al***ce` + 用户ID + 脱敏邮箱。**永不回显完整用户名**，与需求 2 的脱敏口径一致 |
| **限流** | `/resolve` 挂 `middleware.SearchRateLimit()`（按用户ID限流，默认 10 次/60s，`common/constants.go:217`），比 IP 限流抗代理轮换 |
| **审计** | 每次 `/resolve` 写 `qy_transfer_lookup_logs`；命中率 < 20% 且日调用 > 50 次 → 记 `common.SysError` 告警（**建议**） |
| **不存在与不可收款返回同构响应** | `{exists:false, blocked_reason:"not_found"}` 与 `{exists:true, receivable:false}` 走同一 HTTP 200 + 同一响应结构，且服务端加固定 150ms 延迟，避免时序侧信道 |

**脱敏函数（后端，`qianye/service/mask.go`）**：

```go
func MaskUsername(s string) string {
	r := []rune(s)
	switch {
	case len(r) == 0: return ""
	case len(r) == 1: return string(r[0]) + "*"
	case len(r) <= 4: return string(r[0]) + strings.Repeat("*", len(r)-2) + string(r[len(r)-1])
	default:          return string(r[:2]) + "***" + string(r[len(r)-2:])
	}
}
func MaskEmail(s string) string {
	at := strings.LastIndex(s, "@")
	if at <= 0 { return "" }
	return string([]rune(s[:at])[0:1]) + "***" + s[at:]
}
```

> 脱敏在**后端**做。前端不得拿到原文再脱敏——那等于没脱敏。

---

## 6. 关键流程（编号步骤，标注事务边界 / 加锁点 / 幂等键 / 回滚路径）

### 6.1 阶段 A — 前端解析收款人

1. 用户在钱包页点「转账给用户」→ 打开 `QyTransferDialog`。
2. 输入 `identifier`（用户ID 或完整用户名）→ 失焦/点「校验」→ `POST /api/qy/transfer/resolve`。
3. 后端：`SearchRateLimit` → 纯数字走 `model.DB.Where("id = ?")`，否则走 `model.DB.Where("username = ?")`（**依赖 GORM 软删除自动过滤 `deleted_at IS NULL`，禁止 `Unscoped()`**）。
4. 校验 `Id != me`、`Status == common.UserStatusEnabled`、`allow_admin_receiver` 门禁、收款方日收款笔数。
5. 写 `qy_transfer_lookup_logs`（异步 `gopool.Go`，失败仅记日志，不阻塞）。
6. 返回脱敏结果；前端显示「收款人：**al\*\*\*ce**（ID 1234）」并解锁金额输入。

### 6.2 阶段 B — 二次确认

7. 用户填金额（前端 `parseQuotaFromDollars` 转 quota）+ 备注 → 点「下一步」。
8. **前端弹出确认弹窗**（`ConfirmDialog`，`destructive` 变体），必须同时展示：收款人脱敏名 + ID、转出 quota 与显示金额、手续费（若 > 0）、转出后我的余额预估、以及**不可逆红字警示**（文案见 §9.4）。
9. 用户必须**勾选**「我已确认收款人信息，理解划转不可撤销」复选框，「确认划转」按钮才可点。
10. 点击时前端生成 `request_id = crypto.randomUUID()`（**在打开弹窗时生成并缓存，重试沿用同一个**，这是幂等的前提）。

### 6.3 阶段 C — 后端两阶段（核心）

#### C-1 前置校验（无事务）

11. `amount <= 0 || amount > common.MaxQuota` → 400 `invalid_amount`。
12. `Confirm != true` → 400 `confirm_required`。
13. `ToUserId == me` → 400 `self_transfer`（**硬编码拒绝，不做配置项**）。
14. 手续费计算（decimal，避免浮点）：
    ```go
    feeD := decimal.NewFromInt(int64(amount)).Mul(decimal.NewFromFloat(cfg.FeeRate))
    fee, clamp := common.QuotaFromDecimalChecked(feeD)
    if clamp != nil { return errFeeOverflow }          // 绝不允许静默 clamp
    if fee < cfg.FeeMinQuota { fee = cfg.FeeMinQuota }
    totalI64 := int64(amount) + int64(fee)
    if totalI64 > int64(common.MaxQuota) { return errAmountOverflow }
    total := int(totalI64)
    ```
15. 重查收款人（**不信任前端传的 `to_user_id`**）：`model.GetUserById(toUserId, false)`（`model/user.go:453`）→ 校验状态。同时校验发起方 `min_account_age_hours`、`require_email_verified`、`gift_quota_policy`（§7）。

#### C-2 新库事务：限额串行化 + 写 pending

> **事务边界 1 开始（新库）**

16. `qydb.DB.Transaction(func(tx *gorm.DB) error { ... })`
17. **加锁点 1**：按 `user_id 升序`对 `qy_transfer_user_state` 的两行 `SELECT ... FOR UPDATE`（行不存在则先 `INSERT ... ON DUPLICATE KEY UPDATE user_id=user_id` 兜底再锁）。
    ```go
    first, second := fromUserId, toUserId
    if first > second { first, second = second, first }
    // 必须升序，否则 A→B 与 B→A 并发会在新库死锁
    tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", first).First(&s1)
    tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", second).First(&s2)
    ```
18. 日 bucket 滚动：`if s.DayBucket != todayBucket { s.DayBucket = todayBucket; s.DayOutQuota, s.DayOutCount, s.DayInCount = 0,0,0 }`。
19. 逐项校验（任一失败 → `return err`，**事务回滚，主库完全没被触碰**）：
    - `sender.PendingCount > 0` → `pending_exists`
    - `now - sender.LastOutAt < cooldown` → `cooldown`
    - `sender.DayOutCount + 1 > max_daily_out_count` → `daily_count_exceeded`
    - `sender.DayOutQuota + total > max_daily_out_quota` → `daily_quota_exceeded`
    - `max_lifetime_out_quota > 0 && sender.LifetimeOutQuota + total > 上限` → `lifetime_exceeded`
    - `receiver.DayInCount + 1 > receiver_max_daily_in_count` → `receiver_daily_limit`
20. **预占**计数（乐观预扣，失败时在 C-4 回滚）：`DayOutQuota += total; DayOutCount++; DayInCount++（收款方）; LifetimeOutQuota += total; LifetimeInQuota += amount; LastOutAt = now; PendingCount++`。
21. `tx.Create(&TransferOrder{ ..., Status: "pending", TransferNo: no, IdempotencyKey: key })`。
    - **幂等键**：`uk_qy_transfer_idem` 冲突 → 捕获 MySQL 1062 → 回滚事务 → 查出原单 → **直接返回原单结果**（不重复扣款）。

> **事务边界 1 结束（提交）**

22. 新库事务失败（连接不可用/超时）→ **返回 503，绝不进入 C-3**。

#### C-3 主库事务：双向 quota + 锚点日志

> **事务边界 2 开始（主库 `model.DB`）**

```go
var fromAfter, toAfter, fromBefore, toBefore int
txErr := model.DB.Transaction(func(tx *gorm.DB) error {
    // ---- 加锁点 2：按 user id 升序取行锁，避免 A→B / B→A 并发死锁 ----
    first, second := fromUserId, toUserId
    if first > second { first, second = second, first }
    var u1, u2 model.User
    if err := model.QyLockForUpdate(tx).Where("id = ?", first).First(&u1).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) { return ErrUserNotFound }
        return err
    }
    if err := model.QyLockForUpdate(tx).Where("id = ?", second).First(&u2).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) { return ErrUserNotFound }
        return err
    }
    sender, receiver := &u1, &u2
    if sender.Id != fromUserId { sender, receiver = &u2, &u1 }

    // ---- 锁内复检（TOCTOU：C-1 之后用户可能被封禁）----
    if sender.Status   != common.UserStatusEnabled { return ErrSenderDisabled }
    if receiver.Status != common.UserStatusEnabled { return ErrReceiverDisabled }
    if sender.Quota < total                        { return ErrInsufficientQuota }
    if receiver.Quota > common.MaxQuota-amount     { return ErrReceiverOverflow }

    fromBefore, toBefore = sender.Quota, receiver.Quota

    // ---- 扣款：WHERE quota >= ? + RowsAffected 兜底（SQLite 下 lockForUpdate 是 no-op）----
    r := tx.Model(&model.User{}).
        Where("id = ? AND quota >= ?", fromUserId, total).
        Update("quota", gorm.Expr("quota - ?", total))
    if r.Error != nil { return r.Error }
    if r.RowsAffected == 0 { return ErrInsufficientQuota }

    // ---- 加款：同样带 CAS 条件，防止接收侧 int32 溢出 ----
    r2 := tx.Model(&model.User{}).
        Where("id = ? AND quota <= ?", toUserId, common.MaxQuota-amount).
        Update("quota", gorm.Expr("quota + ?", amount))
    if r2.Error != nil { return r2.Error }
    if r2.RowsAffected == 0 { return ErrReceiverOverflow }

    fromAfter, toAfter = fromBefore-total, toBefore+amount

    // ---- 对账锚点：LOG_DB 与主库同库时写在事务内，获得真正的原子性 ----
    if model.QyLogDBSharesMainDB() {
        if err := model.QyCreateLogWithTx(tx, buildOutLog(order, fromAfter)); err != nil { return err }
        if err := model.QyCreateLogWithTx(tx, buildInLog(order, toAfter));    err != nil { return err }
        logAnchored = true
    }
    return nil
})
```

> **事务边界 2 结束**

锚点日志构造（`RequestId` 即单号，命中 `logs` 的 `idx_logs_request_id` 索引，`model/log.go:78`）：

```go
func buildOutLog(o *TransferOrder, after int) *model.Log {
	return &model.Log{
		UserId:    o.FromUserId,
		Username:  o.FromUsername,
		CreatedAt: common.GetTimestamp(),
		Type:      model.LogTypeTopup,                    // 余额变动统一用 Topup，与 subscription.go:833 一致
		Quota:     -(o.Amount + o.FeeQuota),
		RequestId: o.TransferNo,                          // ★ 对账锚点，varchar(64) 带索引
		Ip:        o.ClientIp,
		Content: fmt.Sprintf("余额划转转出 %s 至用户 %s(ID %d)，单号 %s，划转后余额 %s",
			logger.LogQuota(o.Amount), MaskUsername(o.ToUsername), o.ToUserId,
			o.TransferNo, logger.LogQuota(after)),
		Other: common.MapToJsonStr(map[string]any{
			"qy_transfer_no":   o.TransferNo,             // 结构化，供管理端筛选
			"qy_transfer_dir":  "out",
			"qy_counterparty":  o.ToUserId,
			"qy_fee_quota":     o.FeeQuota,
		}),
	}
}
```

> **注意**：`Other` 里**不要**用 `admin_info` 包裹——`formatUserLogs`（`model/log.go:116`）会对普通用户剥离 `admin_info`，那样用户在日志页就看不到单号。直接放顶层键，普通用户可见且无敏感信息。

**主库事务被禁止做的事**（会引发严重副作用）：
- ❌ `user.Update()` / `user.Edit()` / `IncrementUserAuthVersionWithTx` → 触发 Redis fence + 会话吊销，把双方踢下线
- ❌ `tx.Save(&user)` → 全字段覆盖，会把并发的其它字段改动写回（`TransferAffQuotaToQuota` 的反面教材，`model/user.go:503`）
- ❌ `model.IncreaseUserQuota/DecreaseUserQuota` → 无事务、无余额校验、`db=false` 时可能进批量队列
- ❌ `updateUserCache(user)` → `model/user_cache.go:83-85` 明确约定 Quota 只能由增量路径维护

#### C-4 收尾

23. **主库失败** → 新库开事务 2'（同样按 user_id 升序锁 state）：`Status='failed'`、写 `FailCode/FailReason`、**回滚 C-2 预占的所有计数**、`PendingCount--`。返回业务错误。
    - 若这次回滚也失败：仅记 `common.SysError`，**不重试**。后果是发起方当日限额被多消耗一次（保守方向，可接受），补偿任务会在扫描时修正。
24. **主库成功**：
    ```go
    // ① 缓存失效（方案 B，一致性最强；划转低频高价值，回源代价可接受）
    _ = model.InvalidateUserCache(fromUserId)   // model/user_cache.go:72（已导出）
    _ = model.InvalidateUserCache(toUserId)
    // ② LOG_DB 独立部署时，锚点日志在此补写（best-effort）
    if !model.QyLogDBSharesMainDB() {
        if err := model.QyCreateLog(buildOutLog(...)); err == nil { logAnchored = true }
        _ = model.QyCreateLog(buildInLog(...))
    }
    // ③ 新库置终态
    qydb.DB.Model(&TransferOrder{}).
        Where("transfer_no = ? AND status = ?", no, "pending").
        Updates(map[string]any{"status":"success","main_committed_at":committedAt,
                               "settled_at":common.GetTimestamp(),"log_anchored":logAnchored,
                               "from_quota_before":fromBefore,"from_quota_after":fromAfter,
                               "to_quota_before":toBefore,"to_quota_after":toAfter})
    // ④ PendingCount--（独立事务，锁 sender state 一行即可）
    ```
25. 步骤 ③/④ 失败 → **仍向前端返回成功**（钱确实已经转了，报失败会诱导用户重试）。记 `common.SysError`，交给补偿任务。

### 6.4 补偿任务（`qianye/service/transfer_reconcile.go`）

由 `qianye.StartBackgroundTasks()` 启动，每 `reconcile_interval_seconds` 跑一次：

1. 抢 `qy_task_leases` 的 `transfer_reconcile` 租约（TTL = 3× 间隔）。抢不到直接返回。
2. `SELECT * FROM qy_transfer_orders WHERE status='pending' AND created_at < now-grace ORDER BY created_at LIMIT 200`（走 `idx_qy_tr_status_created`）。
3. 对每条：
   - `log_anchored == true` → 主库已提交（同库场景下这是事务内写的，绝对可信）→ 置 `success`，补 `InvalidateUserCache` 双方。
   - 否则回查 LOG_DB：`SELECT id FROM logs WHERE request_id = ? LIMIT 1`（**走 `idx_logs_request_id` 索引点查，ClickHouse 下 request_id 也在排序键里，`model/log.go:113` `clickHouseLogOrder`**）。
     - 命中 → 置 `success` + `InvalidateUserCache`。
     - 未命中 且 `now - created_at < reconcile_giveup_seconds` → 跳过，下轮再试（`ReconcileAttempts++`）。
     - 未命中 且 超过 giveup：
       - `model.QyLogDBSharesMainDB()` → 锚点与扣款同事务，缺锚点即证明**未提交** → 置 `failed('main_tx_not_committed')` + 回滚风控计数。
       - LOG_DB 独立 → **置 `unknown`**，`common.SysError` 告警，进管理端待办。**绝不自动判 failed**——那会把已扣款的单子当作没扣，用户来投诉时无据可依。
4. 顺带清理 `qy_transfer_lookup_logs` 中超过 `lookup_log_retain_days` 的记录。

---

## 7. 赠送额度是否可转（套现风险分析 + 配置化方案）

### 7.1 事实与风险

主库 `users.quota` 是**单一标量**，注册赠送（`common.QuotaForNewUser`）、邀请赠送（`QuotaForInvitee/Inviter`）、兑换码、真实付款**全部混在同一列**（`model/user.go:632-646`、`model/redemption.go`），**没有任何来源标记**。

套现路径：批量注册小号 → 每号白拿 `QuotaForNewUser` → 全部划转到一个主号 → 主号用返佣/提现/或直接大量消费兑现。**平台净出血 = 小号数 × 注册赠送额**。

### 7.2 「精确识别赠送额度」为什么做不到（重要的负面结论）

我核查了唯一可能的反推数据源 `top_ups` 表，**结论是不可靠**：

- `controller/topup.go:398-400`（易支付）：`quotaToAdd = topUp.Amount × QuotaPerUnit` → `Amount` 是**美元数**
- `model/topup.go:143`（Stripe）：`quota = topUp.Money × QuotaPerUnit` → 用的是 `Money`，`Amount` 另有含义
- `model/topup.go:427`（Creem）：注释明写「Creem 直接使用 Amount 作为充值额度」→ `Amount` 是 **quota**

即 **`top_ups.amount` 的单位随支付渠道而变**。任何 `SUM(amount)` 都会算错。兑换码充值更是只有 `logs` 里一句自由文本，`Log.Quota` 列为 0（`RecordTopupLog` 不填 `Quota`），无法求和。

### 7.3 配置化方案（三档，默认宽松）

`gift_quota_policy` 三选一：

| 值 | 行为 | 适用 |
|---|---|---|
| **`unrestricted`（默认）** | 不区分来源，全额可转。仅靠 §8 的限额/冷却/账号年龄兜底 | 注册赠送为 0 或很小的平台 |
| **`require_topup`（推荐）** | 发起方必须存在至少一笔成功充值：`SELECT 1 FROM top_ups WHERE user_id=? AND status='success' LIMIT 1`（**EXISTS 判断，与金额单位无关，完全可靠**）。否则 `blocked_reason: "no_topup"` | 绝大多数平台。一行 EXISTS 就把「零成本批量小号套现」彻底堵死，且没有单位坑 |
| **`capped_by_topup`（严格）** | 可转上限 = `Σ已充值quota估算 − lifetime_out_quota`。需在 YAML 额外配 `topup_amount_unit_by_provider: {epay: dollar, stripe: money, creem: quota, waffo: dollar, waffo_pancake: dollar}` 逐渠道换算。**必须在实施文档中标注这是估算口径，Creem 等渠道存在已知偏差** | 赠送额度很大、且能接受运维维护渠道映射表的平台 |

**我的建议**：默认发布用 `require_topup`。它的成本是一次索引 EXISTS 查询（`top_ups.user_id` 有索引，`model/topup.go:16`），可靠性 100%，且不需要维护任何单位映射表。`capped_by_topup` 作为二期能力保留，不在一期实现代码里。

> 收到的划转额度是否可再转出？**默认允许**，但会形成转账链。风控由 `receiver_max_daily_in_count` + `max_lifetime_out_quota` 兜底。若选 `require_topup`，中转号本身也必须有充值记录，链条自然被截断。

---

## 8. 并发与边界（穷举）

### 8.1 竞态清单

| # | 竞态 | 处理 |
|---|---|---|
| R1 | A→B 与 B→A 同时发起 → 主库行锁死锁 | 两把 `SELECT ... FOR UPDATE` **按 user id 升序**加。原项目从无此场景，无先例可抄 |
| R2 | 同样的死锁发生在新库 `qy_transfer_user_state` | 同样**按 user_id 升序**锁 |
| R3 | 同一用户并发发起两笔，绕过日累计上限（TOCTOU） | 限额校验 + 计数预占 + pending 插入放在**同一个新库事务**内，并对 sender state 行 `FOR UPDATE` 串行化 |
| R4 | 用户双击提交 / 网络重试造成重复扣款 | `uk_qy_transfer_idem` 唯一索引 + 前端 `request_id` 在**打开弹窗时生成**（不是点击时），重试沿用同一个。命中冲突返回原单 |
| R5 | C-1 校验通过后、C-3 加锁前，发起方或收款方被管理员封禁 | 主库事务**锁内复检** `Status == common.UserStatusEnabled` |
| R6 | C-1 校验通过后，发起方余额被并发消费扣空 | `WHERE quota >= ?` + `RowsAffected == 0` 兜底（SQLite 下 `lockForUpdate` 是 no-op，`model/locking.go:21-23`，这是**唯一**的正确性保障） |
| R7 | 收款方并发收到多笔，撞 int32 上限 | 加款也用 CAS：`WHERE id = ? AND quota <= MaxQuota - amount`，`RowsAffected == 0` → `ErrReceiverOverflow` |
| R8 | 多节点同时跑补偿任务，同一笔被结算两次 | `qy_task_leases` 租约 + 回写带 `AND status='pending'` 条件（幂等） |
| R9 | 主库提交成功、进程崩溃、新库回写丢失 | 锚点日志（同库时在事务内）+ 补偿任务；判不定时置 `unknown` 而非 `failed` |
| R10 | 收款方在划转过程中被软删除 | `First(&u)` 自动 `deleted_at IS NULL` 过滤 → `gorm.ErrRecordNotFound` → 主库事务回滚 → 新库置 `failed('receiver_not_found')`。**严禁 `Unscoped()`** |
| R11 | `common.BatchUpdateEnabled=true` 时，其它节点的消费扣费尚未 flush，划转看到的余额偏高 | 接受。`DB.Transaction` 直写不受批量队列影响，语义与 `PurchaseSubscriptionWithBalance` 完全一致 |

### 8.2 数值边界

| 场景 | 处理 |
|---|---|
| `amount <= 0` | 400 `invalid_amount`。**永不允许负数**（负数划转 = 反向抢钱） |
| `amount > common.MaxQuota` | 400 `amount_overflow`（`common/quota_math.go:15`） |
| `amount < min_amount_quota` | 400 `below_minimum` |
| `amount + fee` 溢出 | 用 `int64` 中间量相加后与 `MaxQuota` 比较，**不在 int 上直接相加** |
| 手续费 decimal 转 quota 被 clamp | `QuotaFromDecimalChecked` 返回非 nil `*QuotaClamp` → 直接报错，**绝不接受静默截断** |
| 发起方余额不足 | 锁内 `sender.Quota < total` + CAS `RowsAffected==0` 双保险 → `insufficient_quota` |
| 发起方余额为负（历史数据可能有，`DecreaseUserQuota` 允许扣负，`model/user.go:1274`） | `sender.Quota < total` 自然拒绝；额外在 `/config` 返回 `transferable_quota = max(0, quota)` |
| 收款方余额溢出 int32 | 锁内预检 + CAS 双保险 → `receiver_overflow`，错误文案提示「对方余额已达上限」 |
| `remark` 超长 / 含控制字符 | 服务端截断到 200 runes + 过滤 `\x00-\x1f`，防止日志注入 |
| `identifier` 超长 | 限制 64 字符（`username` 本身 `validate:"max=20"`，`model/user.go:83`），超长直接判不存在 |

### 8.3 金额计算合规

全链路遵守 AGENTS.md：手续费与任何比例运算一律 `shopspring/decimal` → `common.QuotaFromDecimalChecked`；纯整数加减用 `int64` 中间量 + 显式 `common.MaxQuota` 边界判断。**不出现任何 `int(float64 * ratio)` 形式的裸转换。**

---

## 9. 前端

### 9.1 新建文件（纯新增，零冲突）

| 路径 | 内容 |
|---|---|
| `web/src/routes/_authenticated/qy-transfer/index.tsx` | 路由。无 `beforeLoad`（`_authenticated` 已保证登录）；`validateSearch` = zod `{ tab: 'new'\|'records', scope: 'self'\|'all', p, page_size, direction, status }` |
| `web/src/features/qy-transfer/index.tsx` | 页面入口 `export function QyTransfer()`。`SectionPageLayout` + `Tabs`（`发起划转` / `划转记录`）。管理员额外看到 `全部/仅我` 的 scope Tab（参考 `features/usage-logs/index.tsx:133-152` 的受控 Tabs 写法） |
| `web/src/features/qy-transfer/api.ts` | `getTransferConfig` / `resolveReceiver` / `createTransfer` / `getSelfTransfers` / `getAdminTransfers`。统一 `import { api } from '@/lib/api'` |
| `web/src/features/qy-transfer/types.ts` | 与 §4 DTO 对齐的 TS 类型 |
| `web/src/features/qy-transfer/constants.ts` | 状态色映射、`FAIL_CODE_I18N_MAP` |
| `web/src/features/qy-transfer/lib/transfer-form.ts` | `getQyTransferFormSchema(t)` — zod，含 min/max/整数校验 |
| `web/src/features/qy-transfer/hooks/use-qy-transfer.ts` | react-query：`useQuery(['qy-transfer-config'])`、`useMutation` 提交，`onSuccess` 里 `invalidateQueries(['qy-transfer-records'])` + `invalidateQueries(['userSelf'])` |
| `web/src/features/qy-transfer/components/qy-transfer-entry-card.tsx` | **挂进钱包页的入口卡片**（`TitledCard`，显示可转余额 + 今日剩余额度 + 「转账给用户」按钮 + 「划转记录」链接）。`config.enabled === false` 时 `return null` |
| `web/src/features/qy-transfer/components/qy-transfer-dialog.tsx` | 主表单弹窗（收款人输入 + 校验 + 金额 + 备注） |
| `web/src/features/qy-transfer/components/qy-transfer-confirm-dialog.tsx` | 二次确认弹窗（`ConfirmDialog` + `destructive`） |
| `web/src/features/qy-transfer/components/qy-transfer-records-table.tsx` | 流水表（`DataTablePage` + `useTableUrlState` + `useDataTable`，`manualPagination`） |
| `web/src/features/qy-transfer/components/qy-transfer-columns.tsx` | 列定义：时间 / 方向徽章 / 对手方（脱敏） / 金额 / 手续费 / 状态徽章 / 单号（`CopyButton`） / 备注 |

### 9.2 修改的原项目前端文件（**共 2 个，7 行**）

**① `web/src/features/wallet/index.tsx`（+2 行）** — 用户已拍板直接改原文件，不 fork。

- import 区（`:33` `import { SubscriptionPlansCard }` 之后）加 1 行：
  ```tsx
  import { QyTransferEntryCard } from '@/features/qy-transfer/components/qy-transfer-entry-card'
  ```
- `:350` `<AffiliateRewardsCard ... />` 之后、`:351` `</div>` 之前加 1 行：
  ```tsx
  <QyTransferEntryCard userQuota={user?.quota} onTransferSuccess={fetchUser} />
  ```
  `fetchUser`（`:113`）已存在，直接复用即可完成**成功后余额刷新** —— `WalletStatsCard` 的 `user` prop 随之更新。

> 只加 2 行、不改任何既有 JSX 结构，是刻意的：钱包页是上游改动最频繁的业务页，diff 面越小合并越安全。

**② `web/src/hooks/use-sidebar-data.ts`（+5 行）** — 在 `id: 'personal'` 分组（`:102-117`）的 `items` 里、`Wallet` 项（`:106-110`）之后插入：

```tsx
{
  title: t('qy_transfer_nav_title'),
  url: '/qy-transfer',
  icon: ArrowLeftRight,   // lucide-react，需在 import 区加入
},
```

副作用红利：⌘K 命令面板（`command-menu.tsx:51`）、折叠态、移动端抽屉自动继承，无需改动。`use-sidebar-config.ts:173-176` 对未映射 URL 默认可见，**不需要改**。

**③ `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json`** — 纯追加（`bun run i18n:sync` 补齐）。

**④ `web/src/routeTree.gen.ts`** — 插件自动重写，合并冲突时删除后重新 `bun run build`。

### 9.3 主要交互

1. 钱包页 → `QyTransferEntryCard` 显示「可划转余额 / 今日剩余 X 笔、$Y」→ 点「转账给用户」。
2. `QyTransferDialog`：
   - 收款人输入框（`placeholder` 随 `allow_lookup_by_username` 变：「用户 ID 或用户名」/「用户 ID」）+ 「校验」按钮。
   - 校验成功 → 显示绿色收款人卡片：`al***ce`（ID 1234）+ 脱敏邮箱；金额输入框解锁。
   - 校验失败 → 红色提示，金额输入框保持禁用。
   - 金额输入沿用 `TransferDialog`（`components/dialogs/transfer-file.tsx`）的模式：`type='number'`，`parseQuotaFromDollars` 转 quota，下方实时显示「实际转出 `formatQuota(total)`」。
   - 备注（可选，`Textarea`，200 字计数器）。
   - 「下一步」按钮：`disabled` 条件 = 未校验 / 金额越界 / 冷却中 / `blocked_reason != ''`。
3. `QyTransferConfirmDialog`（`destructive`）：
   - 大字展示：**收款人 `al***ce`（ID 1234）** / **转出 $XX.XX** / 手续费（>0 才显示）/ 转出后余额。
   - 红底警示区（`Alert variant='destructive'` + `TriangleAlert` 图标）。
   - `Checkbox`：「我已核对收款人信息，理解划转**不可撤销**」，未勾选则确认按钮禁用。
   - 确认按钮文案 `t('qy_transfer_confirm_action')`，`isLoading` 时禁用 + `Loader2` 转圈。
4. 成功 → `toast.success` 携带单号 → 关闭弹窗 → `fetchUser()` 刷新钱包余额 → `invalidateQueries(['qy-transfer-records'])`。
5. 失败 → `toast.error(t(FAIL_CODE_I18N_MAP[code] ?? 'qy_transfer_err_unknown'))`，**弹窗不关闭**，保留已填内容，`request_id` 不变（重试仍幂等）。

### 9.4 i18n key（`qy_` 下划线扁平键，**禁止点号**）

```jsonc
// zh.json（en.json 同 key 英文值）
"qy_transfer_nav_title": "余额划转",
"qy_transfer_entry_title": "转账给用户",
"qy_transfer_entry_desc": "将余额划转给平台内其他用户",
"qy_transfer_receiver_label": "收款人",
"qy_transfer_receiver_ph_both": "输入用户 ID 或完整用户名",
"qy_transfer_receiver_ph_id": "输入用户 ID",
"qy_transfer_receiver_verify": "校验收款人",
"qy_transfer_amount_label": "划转金额",
"qy_transfer_remark_label": "备注（可选）",
"qy_transfer_next": "下一步",

// 确认弹窗（不可逆警示，必须醒目）
"qy_transfer_confirm_title": "确认划转？",
"qy_transfer_confirm_warning": "划转一经确认将立即到账，且【无法撤销、无法退回】。请务必核对收款人 ID 与金额；若转错对象，平台无法为你追回。",
"qy_transfer_confirm_ack": "我已核对收款人信息，理解此操作不可撤销",
"qy_transfer_confirm_action": "确认划转",
"qy_transfer_confirm_to": "收款人",
"qy_transfer_confirm_amount": "转出金额",
"qy_transfer_confirm_fee": "手续费",
"qy_transfer_confirm_after": "划转后余额",

// 错误文案（对应后端 fail_code / blocked_reason）
"qy_transfer_err_self_transfer": "不能转账给自己",
"qy_transfer_err_not_found": "收款人不存在，请检查用户 ID 或用户名",
"qy_transfer_err_receiver_disabled": "该账号当前无法接收划转",
"qy_transfer_err_insufficient_quota": "余额不足，请调整划转金额",
"qy_transfer_err_receiver_overflow": "对方余额已达上限，无法接收本次划转",
"qy_transfer_err_below_minimum": "低于最小划转金额 {{min}}",
"qy_transfer_err_amount_overflow": "划转金额超出系统上限",
"qy_transfer_err_daily_quota_exceeded": "已达今日划转额度上限，请明日再试",
"qy_transfer_err_daily_count_exceeded": "已达今日划转次数上限，请明日再试",
"qy_transfer_err_receiver_daily_limit": "对方今日收款次数已达上限",
"qy_transfer_err_cooldown": "操作过于频繁，请 {{sec}} 秒后再试",
"qy_transfer_err_account_too_new": "账号注册未满 {{hours}} 小时，暂不能发起划转",
"qy_transfer_err_no_topup": "需完成至少一次充值后才能发起划转",
"qy_transfer_err_pending_exists": "你有一笔划转正在处理中，请稍后再试",
"qy_transfer_err_service_unavailable": "划转服务暂不可用，请稍后重试",
"qy_transfer_err_unknown": "划转失败，请稍后重试或联系客服",

// 记录页
"qy_transfer_records_title": "划转记录",
"qy_transfer_dir_out": "转出",
"qy_transfer_dir_in": "转入",
"qy_transfer_status_pending": "处理中",
"qy_transfer_status_success": "已完成",
"qy_transfer_status_failed": "已失败",
"qy_transfer_status_unknown": "待核实",
"qy_transfer_no": "划转单号",
"qy_transfer_counterparty": "对手方",
"qy_transfer_empty_title": "暂无划转记录",
"qy_transfer_empty_desc": "你还没有发起或收到任何余额划转",
"qy_transfer_disabled_notice": "管理员未开启余额划转功能"
```

> 若文案以常量形式出现（如 `FAIL_CODE_I18N_MAP` 的值），必须在 `web/src/i18n/static-keys.ts` 的 `STATIC_I18N_KEYS` 登记，否则 `i18n:sync` 会当成未使用 key 清掉。

### 9.5 划转记录查询页（双方可见）

- 路由 `/qy-transfer?tab=records`。
- 数据源 `GET /api/qy/transfer/self`，`WHERE from_user_id = me OR to_user_id = me` — **发起方与收款方看到的是同一条流水的两个视角**，`direction` 由后端按当前用户计算后下发，前端不做判断。
- 列：时间 / 方向徽章（转出红、转入绿） / 对手方（脱敏 + ID） / 金额（`formatQuota`） / 手续费 / 状态徽章 / 单号（`CopyButton`） / 备注。
- 筛选：方向、状态、时间范围；URL 驱动（`useTableUrlState`）。
- 移动端走 `MobileCardList`（`components/data-table` 已提供）。
- 空态：`EmptyState` + `qy_transfer_empty_title/desc`。
- 管理员额外看到 `全部 / 仅我` scope 切换，切到「全部」时打 `/api/qy/transfer/admin`。

---

## 10. 原项目改动清单（精确到行 + 冲突风险）

### 10.1 后端

| # | 文件:行号 | 插入的确切代码 | 行数 | 冲突风险 | 归属 |
|---|---|---|---|---|---|
| B1 | `main.go:31` 之后 | `	"github.com/QuantumNous/new-api/qianye"` | 1 | **低** | 架构共享 |
| B2 | `main.go:365` 之后（`service.StartAuthArtifactCleanup()` 与 `return nil` 之间） | ```if err := qianye.Init(); err != nil {```<br>```	common.SysError("failed to initialize qianye extension: " + err.Error())```<br>```	return err```<br>```}``` | 4 | **低** | 架构共享 |
| B3 | `main.go:195` 之后（`InjectGoogleAnalytics()` 与 `:198 router.SetRouter` 之间） | `	qianye.RegisterRoutes(server)` | 1 | **低** | 架构共享 |
| B4 | `main.go:152` 之后（`service.StartSystemTaskRunner()` 之后） | `	qianye.StartBackgroundTasks()` | 1 | **低** | 架构共享 |
| B5 | `model/qy_export.go` | **全新文件**（内容见 §3） | 0 行改动现有文件 | **无** | 架构共享（本模块新增 `QyLogDBSharesMainDB` / `QyCreateLogWithTx` 两个符号） |

> **B3 必须在 `main.go:198` `router.SetRouter` 之前**：`router/web-router.go:25-28` 会 `server.Use(gzip / GlobalWebRateLimit / Cache / static.Serve)`，Gin 的 `Use` 只影响其后注册的路由。注册晚了会被这四层全局中间件污染。

> **B2 必须在 `main.go:365` 位置**：晚于 `:287 godotenv.Load`（`QIANYE_CONFIG` 可用）、`:295 common.InitEnv`（`IsMasterNode` 已就绪，AutoMigrate 才能正确 gate）、`:307 model.InitDB`（`model.DB` 可用）、`:331 model.InitLogDB`（`QyLogDBSharesMainDB()` 才有正确答案）、`:337 InitRedisClient`。

**本需求（划转）独占的原项目后端改动 = 0 行。** B1–B5 全部是所有模块共用的架构地基，不重复计入 D3 的 ≤10 文件 / ≤40 行预算。

### 10.2 前端

| # | 文件:行号 | 改动 | 行数 | 冲突风险 |
|---|---|---|---|---|
| F1 | `web/src/features/wallet/index.tsx:33` 之后 | `import { QyTransferEntryCard } from '@/features/qy-transfer/components/qy-transfer-entry-card'` | 1 | **中**（该文件上游改动频繁，但仅 import 区追加，冲突易解） |
| F2 | `web/src/features/wallet/index.tsx:350` 之后 | `<QyTransferEntryCard userQuota={user?.quota} onTransferSuccess={fetchUser} />` | 1 | **中**（同上） |
| F3 | `web/src/hooks/use-sidebar-data.ts:110` 之后（`personal` 组 Wallet 项之后）+ import 区加 `ArrowLeftRight` | 菜单项对象 4 行 + import 1 行 | 5 | **中**（纯数组/import 追加） |
| F4 | `web/src/i18n/locales/*.json` ×7 | 追加 ~40 个 `qy_transfer_*` key | 追加 | **高频但纯追加** |
| F5 | `web/src/routeTree.gen.ts` | 插件自动重写 | 自动 | **高（可自动重生成）** — 合并流程文档写死「冲突即删除后重跑 build」 |

**前端合计：3 个原文件（F1/F2 同一文件），7 行手写 diff + i18n 追加。**

---

## 11. 我建议补充的（用户未提，但缺了会出事）

> 以下全部标注为**建议**，可按优先级取舍。

1. **【建议·高】`pending_exists` 硬闸门**。同一用户存在未结算 pending 时禁止发起新划转。理由：两阶段中间态叠加会让「余额」与「流水」的差值无法归因，人工对账时无法判断该退哪一笔。代价是极端情况下用户要等补偿任务跑完（最长 `reconcile_giveup_seconds`），可接受。

2. **【建议·高】管理员「待核实（unknown）」看板 + 告警**。`unknown` 是唯一需要人介入的状态，如果没有可见界面，它会变成静默积压的资金黑洞。在 `/api/qy/transfer/admin` 加 `status=unknown` 筛选；补偿任务每产生一条 `unknown` 就 `common.SysError` 一次。

3. **【建议·高】划转开关的紧急熔断**。`transfer.enabled` 是 YAML 启动级配置，改了要重启。建议额外在新库加一张 `qy_runtime_flags(key, value, updated_at)`，`enabled` 读「YAML && runtime_flag」，让管理员在发现刷量时**不重启**就能一键关停。这是资金类功能的运维底线。

4. **【建议·中】反向操作只走管理员**。不提供任何用户侧「撤销/退回」。若确需退回，走管理员在 `/qy-transfer/admin` 发起一笔**反向划转**（新单号，`remark` 引用原单号），保持账本只增不改。

5. **【建议·中】收款方通知**。收到划转时给收款方留一条主库 `logs`（本设计的 `buildInLog` 已覆盖）。若平台已有站内信/邮件能力，可追加；但**不要**在 relay 热路径外新增邮件依赖。

6. **【建议·中】新账号冷却 `min_account_age_hours`**。`users.created_at`（`model/user.go:104`）现成可读。24 小时门槛能挡掉绝大多数脚本批量注册套现，成本几乎为零。

7. **【建议·中】`/resolve` 固定延迟 + 慢速枚举告警**。见 §5。命中率异常低的用户自动进管理员观察名单。

8. **【建议·中】流水导出**。管理端提供 CSV 导出（复用前端 `DataTable` 的导出模式）。资金类功能的对账刚需。

9. **【建议·低】手续费预留**。`fee_rate` / `fee_quota` 列已建但默认 0。若未来要收费，需同时决定手续费的去向（销毁 / 转入 root 账号），当前设计是**销毁**（发起方扣 `amount+fee`，收款方只加 `amount`），最简单且不引入第三方账户。实施时须在管理端明确展示手续费总额，否则平台会不知道自己「凭空多了多少钱」。

10. **【建议·低】前端「最近转账对象」快捷入口**。从 `/api/qy/transfer/self` 取最近 5 个成功转出的对手方（脱敏 + ID），降低用户手输 ID 出错率 —— 这是「转错人不可撤销」风险的最有效缓解手段，比任何警示文案都管用。

---

## 12. 实施顺序建议

1. `model/qy_export.go`（B5）+ `qianye/db` 迁移 3 张表 → 单测：并发 100 笔 A↔B 互转，断言余额守恒、无死锁、无负数、无超发。
2. 后端 service 两阶段 + 补偿任务 → 故障注入测试：主库事务后 kill 进程，验证补偿任务把 pending 正确收敛。
3. Controller + 路由 + `/config` `/resolve` → Postman 验证幂等（同 `request_id` 提交 5 次，只扣 1 次）。
4. 前端 feature + 路由（纯新增）。
5. 最后才动 F1/F2/F3 三处原文件 —— 把与上游冲突的改动放在最后一步，便于 rebase。
