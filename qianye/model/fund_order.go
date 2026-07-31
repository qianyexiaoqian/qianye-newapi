package model

// FundOrder 是所有跨库资金操作的统一状态机。
//
// 划转、佣金兑现、提现到账、佣金冲正全部共用这一张表 —— 因此补偿任务只需要一个,
// 不必每个业务模块各写一套。各模块的业务明细放自己的表,以 OrderNo 软关联。
//
// 为什么需要它:资金的最终落点(users.quota)在主库,记账在扩展库,两者不能放进
// 同一个事务。中间态必须有地方记录,否则"主库已扣款但扩展库回写失败"就成了无人
// 知晓的悬案。
type FundOrder struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// OrderNo 是跨库唯一关联键,会同时写进主库 outbox 与 logs.request_id,
	// 是事后对账的唯一锚点。
	OrderNo string `json:"order_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_fund_no"`
	// Kind 区分业务类型,取值见 kind.go。补偿任务据此回调对应模块。
	Kind   string `json:"kind" gorm:"type:varchar(32);not null;index:idx_qy_fund_kind_status,priority:1"`
	Status int8   `json:"status" gorm:"not null;default:0;index:idx_qy_fund_kind_status,priority:2"`

	// IdemScope + IdemKey 是幂等的唯一保证。重复提交、补偿重扫、支付回调重放
	// 都会命中这个唯一索引,直接返回已有单而不是重复动钱。
	IdemScope string `json:"idem_scope" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_fund_idem,priority:1"`
	IdemKey   string `json:"idem_key" gorm:"type:varchar(96);not null;uniqueIndex:uk_qy_fund_idem,priority:2"`

	UserId     int `json:"user_id" gorm:"not null;index:idx_qy_fund_user,priority:1"`
	PeerUserId int `json:"peer_user_id" gorm:"not null;default:0;index"`

	// AmountQuota 用 int64 承载,但跨库写入主库前必须校验不超过 common.MaxQuota
	// (users.quota 是 int32)。绝不静默截断。
	AmountQuota int64 `json:"amount_quota" gorm:"not null"`
	// FeeQuota 即使当前配置为 0 也要落库,否则日后改了费率,历史单将无法解释。
	FeeQuota int64 `json:"fee_quota" gorm:"not null;default:0"`

	// RefType / RefId 用于溯源:topup:<trade_no>、log:<id>、withdraw:<id>。
	// 佣金冲正靠它找回原单。
	RefType string `json:"ref_type" gorm:"type:varchar(32);not null;default:''"`
	RefId   string `json:"ref_id" gorm:"type:varchar(64);not null;default:'';index:idx_qy_fund_ref"`

	// Attempts / NextProbeAt 支撑补偿任务的指数退避,防止一条坏单反复打爆主库。
	Attempts    int    `json:"attempts" gorm:"not null;default:0"`
	NextProbeAt int64  `json:"next_probe_at" gorm:"not null;default:0;index:idx_qy_fund_probe"`
	LastError   string `json:"last_error" gorm:"type:varchar(512);not null;default:''"`
	// NodeName 用于多节点部署下定位是哪台机器留下的中间态。
	NodeName string `json:"node_name" gorm:"type:varchar(160);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_fund_user,priority:2"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;index:idx_qy_fund_updated"`
	// SettledAt 与 UpdatedAt 分开,便于财务按结算时间(而非最后修改时间)对账。
	SettledAt int64 `json:"settled_at" gorm:"not null;default:0"`
}

func (FundOrder) TableName() string { return "qy_fund_orders" }

// 资金单状态。
//
// 刻意留出 1 号位不用:GORM 的 int8 零值是 0,把 Pending 定为 0 可以让
// "插入时忘记赋值"退化成最安全的状态,而不是意外变成 Success。
const (
	StatusPending int8 = 0 // 扩展库已落单,主库尚未确认
	StatusSuccess int8 = 2 // 主库已生效且扩展库已回写
	StatusFailed  int8 = 3 // 主库明确未生效(余额不足、用户禁用等)
	// StatusUncertain 是资金系统必须有的"我不知道,交给人"出口。
	// 探针耗尽重试仍无法判定时进入此态,只能由管理员在对账台裁决。
	StatusUncertain int8 = 4
	StatusReversed  int8 = 5 // 已被冲正(如退款回收佣金)
)

// StatusName 返回状态的稳定英文标识,供 API 与前端 i18n 使用。
// 不返回中文:自然语言留给前端按 key 渲染。
func StatusName(s int8) string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusSuccess:
		return "success"
	case StatusFailed:
		return "failed"
	case StatusUncertain:
		return "uncertain"
	case StatusReversed:
		return "reversed"
	default:
		return "unknown"
	}
}

// IsTerminal 表示该状态不会再自动变化。
func IsTerminal(s int8) bool {
	return s == StatusSuccess || s == StatusFailed || s == StatusReversed
}

// 业务类型。两字母代码用于生成单号前缀。
const (
	KindTransfer         = "transfer"
	KindCommissionSettle = "commission_settle"
	KindCommissionRevers = "commission_reverse"
	KindWithdrawQuota    = "withdraw_quota"
	KindWithdrawFiat     = "withdraw_fiat"
	KindViolationFee     = "violation_fee"
)

// KindCode 返回单号中使用的两字母类型码。
func KindCode(kind string) string {
	switch kind {
	case KindTransfer:
		return "TR"
	case KindCommissionSettle:
		return "CM"
	case KindCommissionRevers:
		return "RV"
	case KindWithdrawQuota, KindWithdrawFiat:
		return "WD"
	case KindViolationFee:
		return "VF"
	default:
		return "XX"
	}
}
