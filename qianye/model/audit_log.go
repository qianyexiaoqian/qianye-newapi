package model

import "github.com/shopspring/decimal"

// AuditLog 是扩展所有资金操作与人工决策的统一审计流水。
//
// 只追加,不更新不删除(除按 retention_days 归档)。任何影响资金计算或对外承诺的
// 配置变更也必须写这里 —— 否则"为什么这笔按 5% 算、那笔按 8% 算"事后无法自证。
type AuditLog struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// TraceNo 串起一笔资金的全生命周期(申请 → 审核 → 到账 → 冲正),
	// 通常等于 FundOrder.OrderNo 或业务单号。
	TraceNo string `json:"trace_no" gorm:"type:varchar(64);not null;default:'';index:idx_qy_audit_trace"`

	// Category ∈ {fund, transfer, commission, withdraw, violation, config, admin}
	Category string `json:"category" gorm:"type:varchar(32);not null;index:idx_qy_audit_cat,priority:1"`
	// Action 是稳定的英文标识(如 withdraw.approve),不存自然语言 —— 前端按
	// qy_audit_<action> 做 i18n 渲染,与上游 RecordOperationAuditLog 的思路一致。
	Action string `json:"action" gorm:"type:varchar(64);not null"`

	// ActorType ∈ {user, admin, system}。补偿任务与结算任务写 system,
	// 必须能与人工操作区分,否则事故复盘时分不清是人干的还是程序干的。
	ActorType   string `json:"actor_type" gorm:"type:varchar(16);not null"`
	ActorUserId int    `json:"actor_user_id" gorm:"not null;default:0;index:idx_qy_audit_actor,priority:1"`
	ActorName   string `json:"actor_name" gorm:"type:varchar(64);not null;default:''"`

	TargetUserId int `json:"target_user_id" gorm:"not null;default:0;index:idx_qy_audit_target,priority:1"`

	AmountQuota int64           `json:"amount_quota" gorm:"not null;default:0"`
	AmountFiat  decimal.Decimal `json:"amount_fiat" gorm:"type:decimal(18,6);not null;default:0.000000"`
	Currency    string          `json:"currency" gorm:"type:varchar(8);not null;default:''"`
	// FrozenRate 冻结佣金产生当时的汇率。USDExchangeRate 是管理员可随时修改的
	// 全局变量,不冻结的话历史对账永远对不上。
	FrozenRate decimal.Decimal `json:"frozen_rate" gorm:"type:decimal(18,8);not null;default:0.00000000"`

	Result string `json:"result" gorm:"type:varchar(16);not null"` // ok | fail | pending
	Reason string `json:"reason" gorm:"type:varchar(512);not null;default:''"`
	// Before/AfterSnap 是 JSON 快照,按 audit.snapshot_max_bytes 截断。
	BeforeSnap string `json:"before_snap" gorm:"type:text"`
	AfterSnap  string `json:"after_snap" gorm:"type:text"`

	IP        string `json:"ip" gorm:"type:varchar(64);not null;default:''"`
	UserAgent string `json:"user_agent" gorm:"type:varchar(256);not null;default:''"`
	RequestId string `json:"request_id" gorm:"type:varchar(64);not null;default:'';index"`
	NodeName  string `json:"node_name" gorm:"type:varchar(160);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_audit_cat,priority:2;index:idx_qy_audit_actor,priority:2;index:idx_qy_audit_target,priority:2"`
}

func (AuditLog) TableName() string { return "qy_audit_logs" }

// 审计分类。
const (
	AuditCategoryFund       = "fund"
	AuditCategoryTransfer   = "transfer"
	AuditCategoryCommission = "commission"
	AuditCategoryWithdraw   = "withdraw"
	AuditCategoryViolation  = "violation"
	AuditCategoryConfig     = "config"
	AuditCategoryAdmin      = "admin"
	AuditCategoryLottery    = "lottery"
	AuditCategoryTicket     = "ticket"
)

// 操作者类型。
const (
	ActorUser   = "user"
	ActorAdmin  = "admin"
	ActorSystem = "system"
)

// 审计结果。
const (
	ResultOK      = "ok"
	ResultFail    = "fail"
	ResultPending = "pending"
)
