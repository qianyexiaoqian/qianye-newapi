// Package transfer 实现用户之间的余额划转。
//
// 钱在原项目主库的 users.quota,记账在扩展库,两者不可能放进同一个事务,
// 因此资金主状态机统一走 twophase(qy_fund_orders + 主库 qy_fund_outbox 探针)。
// 本包的 qy_transfer_orders 只是业务明细:谁转给谁、当时双方余额多少、备注是什么 ——
// 这些资金单不关心,但用户账单与事后争议必须有。
//
// 风控(单笔上下限、日累计额度与笔数、冷却、新账号冻结)必须与"落 pending 单"
// 处在同一个扩展库事务内、并对用户状态行加锁:日累计若用 SELECT SUM 现算,
// 两笔并发请求会同时读到旧值、同时通过校验,限额形同虚设。
package transfer

// 明细状态。与 qy_fund_orders 的 int8 状态机分开是刻意的:
// 资金单描述"主库动没动钱",明细描述"这笔业务对用户呈现成什么样"。
// 取值统一用裁定文档 C12 的口径,"结果不可判定"一律叫 uncertain。
const (
	statusPending   = "pending"
	statusSuccess   = "success"
	statusFailed    = "failed"
	statusUncertain = "uncertain"
)

// Order 是一笔划转的业务明细。
//
// 与主库无外键(跨库不可能有),from/to 用户名是冗余快照:
// 主库用户可以改名、可以软删,事后查流水必须还原"当时是谁"。
type Order struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// OrderNo 由 twophase.NewOrderNo 生成,是与 qy_fund_orders、主库 outbox、
	// logs.request_id 三处对账的唯一锚点。
	OrderNo string `json:"order_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_tr_no"`

	FromUserId int `json:"from_user_id" gorm:"not null;index:idx_qy_tr_from,priority:1"`
	ToUserId   int `json:"to_user_id" gorm:"not null;index:idx_qy_tr_to,priority:1"`

	FromUsername string `json:"from_username" gorm:"type:varchar(64);not null;default:''"`
	ToUsername   string `json:"to_username" gorm:"type:varchar(64);not null;default:''"`

	// Amount 是收款方实收额度;FeeQuota 是额外从发起方扣的手续费(不进收款方)。
	// 发起方实扣 = Amount + FeeQuota。分两列存而不是存"总额 + 费率",
	// 是因为费率随时可改,事后按当前费率重算必然对不上账。
	Amount   int64 `json:"amount" gorm:"not null"`
	FeeQuota int64 `json:"fee_quota" gorm:"not null;default:0"`

	Status     string `json:"status" gorm:"type:varchar(16);not null;default:'pending';index:idx_qy_tr_status,priority:1"`
	FailCode   string `json:"fail_code" gorm:"type:varchar(48);not null;default:''"`
	FailReason string `json:"-" gorm:"type:varchar(255);not null;default:''"`

	Remark string `json:"remark" gorm:"type:varchar(200);not null;default:''"`

	// 余额快照是争议仲裁的唯一凭据:主库 users 表没有历史版本,
	// 事后无法回答"扣款那一刻余额到底是多少"。
	FromQuotaBefore int64 `json:"from_quota_before" gorm:"not null;default:0"`
	FromQuotaAfter  int64 `json:"from_quota_after" gorm:"not null;default:0"`
	ToQuotaBefore   int64 `json:"to_quota_before" gorm:"not null;default:0"`
	ToQuotaAfter    int64 `json:"to_quota_after" gorm:"not null;default:0"`

	// 批量套现与盗号划转的排查全靠这两列。UserAgent 不下发给普通用户。
	ClientIp  string `json:"client_ip" gorm:"type:varchar(64);not null;default:''"`
	UserAgent string `json:"-" gorm:"type:varchar(255);not null;default:''"`

	// RiskHeld 表示风控计数仍处于"预占且可撤销"的状态。
	// 它是结算幂等的开关:成功与失败都靠对它做 CAS 来保证同一笔只结算一次,
	// 否则补偿任务与业务线程会把日累计重复退还。
	RiskHeld bool `json:"-" gorm:"not null"`

	// LedgerWritten 标记主库账本日志(logs)是否已写。
	// 崩溃恢复时补偿任务据此决定要不要补写,避免用户看到两条重复的余额变动记录。
	LedgerWritten bool `json:"-" gorm:"not null"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_tr_from,priority:2;index:idx_qy_tr_to,priority:2;index:idx_qy_tr_status,priority:2"`
	SettledAt int64 `json:"settled_at" gorm:"not null;default:0"`
}

func (Order) TableName() string { return "qy_transfer_orders" }

// UserState 每个用户一行,是风控限额的串行化点。
//
// 存在理由:日累计如果每次都 SELECT SUM(...) FROM qy_transfer_orders 现算,
// 并发请求之间存在 TOCTOU 窗口。把"读计数 → 判限额 → 写计数 → 落单"
// 全部放进同一个事务并对本行 FOR UPDATE,才真正串行化。
type UserState struct {
	UserId int `json:"user_id" gorm:"primaryKey"`

	// DayBucket 形如 20260730(服务器本地自然日)。与 day_* 字段配套:
	// 读到 bucket 不是今天就地重置,省掉一个定时清零任务 —— 定时任务
	// 一旦漏跑,限额就会在跨日后继续沿用昨天的计数。
	DayBucket int32 `json:"day_bucket" gorm:"not null;default:0"`

	DayOutQuota int64 `json:"day_out_quota" gorm:"not null;default:0"`
	DayOutCount int   `json:"day_out_count" gorm:"not null;default:0"`
	DayInCount  int   `json:"day_in_count" gorm:"not null;default:0"`

	LastOutAt int64 `json:"last_out_at" gorm:"not null;default:0"`

	LifetimeOutQuota int64 `json:"lifetime_out_quota" gorm:"not null;default:0"`
	LifetimeInQuota  int64 `json:"lifetime_in_quota" gorm:"not null;default:0"`

	// PendingCount 未结算笔数。>0 时禁止发起新划转:两阶段中间态叠加会让
	// "余额"与"流水"的差额无法归因,人工对账时根本判断不出该退哪一笔。
	PendingCount int `json:"-" gorm:"not null;default:0"`

	UpdatedAt int64 `json:"-" gorm:"not null;default:0"`
}

func (UserState) TableName() string { return "qy_transfer_user_state" }

// LookupLog 记录每一次收款人解析。
//
// 存在理由:解析接口天然可被用来枚举用户。限流只能限速率,发现不了
// "每天都不超限但持续一个月扫库"的慢速枚举,这张表提供事后追溯的依据。
// 保留期由本模块的后台任务清理。
type LookupLog struct {
	Id     int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId int   `json:"user_id" gorm:"not null;index:idx_qy_tr_lk_user,priority:1"`
	// Identifier 原样保存用户输入 —— 只有原值才能判断"同一个人是否在按序号遍历"。
	Identifier string `json:"identifier" gorm:"type:varchar(64);not null;default:''"`
	ByType     string `json:"by_type" gorm:"type:varchar(8);not null;default:''"` // id | email
	Hit        bool   `json:"hit" gorm:"not null"`
	ClientIp   string `json:"client_ip" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;index:idx_qy_tr_lk_user,priority:2"`
}

func (LookupLog) TableName() string { return "qy_transfer_lookup_logs" }
