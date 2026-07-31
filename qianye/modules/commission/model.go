// Package commission 实现邀请返佣的账本、触发与结算。
//
// 三条铁律,违反任何一条都会造成资损或用户投诉:
//
//  1. 佣金金额一律用 decimal 全精度累计,只在"落成整数额度"那一刻才取整。
//     quota 是 int32,单次对话常见 10~500,5% 佣金按整数截断会大量归零 ——
//     用户用了一整天却看到 0 佣金,而钱确实被平台吞了。
//  2. 消费返佣挂在 relay 结算路径上,禁止在该线程里查库。邀请关系走进程内缓存
//     (含负缓存),真正的写入交给 guard.HotAsync。
//  3. 每一笔充值/消费的返佣都必须幂等。三条触发路径(扫描、hook、人工重扫)
//     任意重叠都不能重复发钱,靠 (idem_scope, idem_key) 唯一索引兜住。
package commission

import (
	"github.com/shopspring/decimal"
)

// 计佣来源。取值同时作为 idem_scope,这样"按来源查"直接命中唯一索引前缀。
const (
	SourceTopup      = "topup"
	SourceRedemption = "redemption"
	SourceConsume    = "consume"
	SourceClawback   = "clawback"
)

// 计佣行状态。
//
// 刻意不设 pending_review:宽松口径下逐笔人工审核在消费返佣场景不可行
// (行数 = 下线数 × 天数)。风控命中走 risk_hold 进人工队列,是例外而非常态。
const (
	StatusAccrued  = "accrued"   // 已计佣,等待成熟与结算
	StatusSettled  = "settled"   // 已被结算完全吸收(仅用于不会再增长的行)
	StatusRiskHold = "risk_hold" // 风控命中,需人工放行
	StatusVoided   = "voided"    // 人工作废,不参与结算
)

// Accrual 是计佣明细,账本的最小单位。只增不改(金额列除外),负额行表示冲正。
//
// 消费返佣按 (被邀请人, 自然日) 聚合成一行而不是每次请求一行:relay QPS 量级
// 会把表撑爆,而日聚合的行数上界 = 活跃下线数 × 天数,与 QPS 无关,同时保留了
// "这天这个下线消费了多少、返了多少"的可审计粒度。
type Accrual struct {
	Id        int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	AccrualNo string `json:"accrual_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_ca_no"`

	// IdemScope + IdemKey 是唯一幂等键(裁定 C11 统一双列结构)。
	// 充值 = topup:<trade_no>;兑换码 = redemption:<id>;
	// 消费 = consume:<inviteeId>:<yyyymmdd>;冲正 = clawback:<...>。
	IdemScope string `json:"idem_scope" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_ca_idem,priority:1"`
	IdemKey   string `json:"idem_key" gorm:"type:varchar(96);not null;uniqueIndex:uk_qy_ca_idem,priority:2"`

	InviterId int `json:"inviter_id" gorm:"not null;index:idx_qy_ca_inviter,priority:1"`
	InviteeId int `json:"invitee_id" gorm:"not null;index:idx_qy_ca_invitee"`

	SourceType string `json:"source_type" gorm:"type:varchar(24);not null;default:''"`
	// SourceRef 是可展示的来源引用(trade_no / 兑换码 id / task_id),下发前脱敏。
	SourceRef string `json:"source_ref" gorm:"type:varchar(128);not null;default:''"`

	// BaseQuota 用 int64:日聚合会把整天基数累加,int32 在重度用户上会溢出。
	BaseQuota int64 `json:"base_quota" gorm:"not null;default:0"`
	// BaseMoney 是充值路径的原始付款金额。必须单独存 ——
	// common.QuotaPerUnit 是可改的全局变量,从 quota 反算付款金额,
	// 只要它被调过一次,历史订单的法币对账就全错。
	BaseMoney decimal.Decimal `json:"base_money" gorm:"type:decimal(18,6);not null;default:0"`
	RateBps   int             `json:"rate_bps" gorm:"not null;default:0"`

	// GrossAmount 是不截断的精确佣金,GrossAmount - SettledAmount 即待结算增量。
	// 用增量而非 status 翻转来驱动结算,日聚合行才能"边增长边结算"。
	GrossAmount   decimal.Decimal `json:"gross_amount" gorm:"type:decimal(30,10);not null;default:0"`
	SettledAmount decimal.Decimal `json:"settled_amount" gorm:"type:decimal(30,10);not null;default:0"`

	// UsdRate 冻结计佣当时的汇率。USDExchangeRate 是管理员可随时热改的全局变量,
	// 不冻结的话历史对账永远对不上。
	UsdRate decimal.Decimal `json:"usd_rate" gorm:"type:decimal(18,8);not null;default:0"`

	Status    string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_ca_inviter,priority:2;index:idx_qy_ca_scan,priority:1"`
	RiskFlags string `json:"risk_flags" gorm:"type:varchar(255);not null;default:''"`
	// MatureAt 是成熟时间(裁定 C23)。结算只吸收已成熟的行,这样
	// qy_commission_balance.available_quota 天然全部可提,提现模块无需再判时间维度。
	MatureAt int64 `json:"mature_at" gorm:"not null;default:0;index:idx_qy_ca_scan,priority:2"`
	// BucketDate 是消费日聚合的自然日(yyyymmdd,UTC)。非消费来源为空。
	BucketDate string `json:"bucket_date" gorm:"type:char(8);not null;default:''"`

	RefAccrualId int64 `json:"ref_accrual_id" gorm:"not null;default:0;index:idx_qy_ca_ref"`
	SettlementId int64 `json:"settlement_id" gorm:"not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0;index:idx_qy_ca_created"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (Accrual) TableName() string { return "qy_commission_accrual" }

// Balance 是邀请人的佣金余额。
//
// UnsettledAmount(未结算余数)与 AvailableQuota 必须在同一个事务、同一把行锁下
// 原子变更,所以刻意不拆成两张表 —— 拆开只会引入第二次加锁与新的中间态。
// 余数的审计轨迹由 Settlement 的 carry_before/carry_after 承载。
type Balance struct {
	UserId int `json:"user_id" gorm:"primaryKey"`

	// UnsettledAmount 承载所有不足 1 额度的零头。可以为负 —— 那表示冲正金额
	// 超过了可回收余额,即欠账,此时禁止提现,未来佣金自动优先抵扣。
	UnsettledAmount decimal.Decimal `json:"unsettled_amount" gorm:"type:decimal(30,10);not null;default:0"`

	AvailableQuota     int64 `json:"available_quota" gorm:"not null;default:0"`
	FrozenQuota        int64 `json:"frozen_quota" gorm:"not null;default:0"`
	WithdrawnQuota     int64 `json:"withdrawn_quota" gorm:"not null;default:0"`
	TotalEarnedQuota   int64 `json:"total_earned_quota" gorm:"not null;default:0"`
	TotalClawbackQuota int64 `json:"total_clawback_quota" gorm:"not null;default:0"`

	// AvailableFiat 按计佣时刻的冻结汇率折算,提现模块直接读它,
	// 禁止用当前 USDExchangeRate 反算。
	AvailableFiat decimal.Decimal `json:"available_fiat" gorm:"type:decimal(18,6);not null;default:0"`
	FiatCurrency  string          `json:"fiat_currency" gorm:"type:varchar(8);not null;default:''"`

	InviteeCount int  `json:"invitee_count" gorm:"not null;default:0"`
	DebtBlocked  bool `json:"debt_blocked" gorm:"not null"`

	LastSettledAt int64 `json:"last_settled_at" gorm:"not null;default:0"`
	CreatedAt     int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt     int64 `json:"updated_at" gorm:"not null;default:0;index:idx_qy_cb_updated"`
}

func (Balance) TableName() string { return "qy_commission_balance" }

// Settlement 是一次结算批次,同时也是余数的完整审计轨迹。
//
// CarryBefore/CarryAfter 让"这一批为什么只发了 3 而不是 3.47"可以事后自证。
type Settlement struct {
	Id       int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	SettleNo string `json:"settle_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_cs_no"`
	UserId   int    `json:"user_id" gorm:"not null;index:idx_qy_cs_user,priority:1"`

	AccrualCount int             `json:"accrual_count" gorm:"not null;default:0"`
	DeltaAmount  decimal.Decimal `json:"delta_amount" gorm:"type:decimal(30,10);not null;default:0"`
	CarryBefore  decimal.Decimal `json:"carry_before" gorm:"type:decimal(30,10);not null;default:0"`
	CarryAfter   decimal.Decimal `json:"carry_after" gorm:"type:decimal(30,10);not null;default:0"`

	GrantedQuota   int64 `json:"granted_quota" gorm:"not null;default:0"`
	ReclaimedQuota int64 `json:"reclaimed_quota" gorm:"not null;default:0"`

	UsdRateWeighted decimal.Decimal `json:"usd_rate_weighted" gorm:"type:decimal(18,8);not null;default:0"`
	FiatDelta       decimal.Decimal `json:"fiat_delta" gorm:"type:decimal(18,6);not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0;index:idx_qy_cs_user,priority:2"`
}

func (Settlement) TableName() string { return "qy_commission_settlement" }

// InviteRelation 是邀请关系快照。
//
// 存在的意义是"已邀请用户列表"这个页面:脱敏名在服务端算好并缓存在这里,
// 列表页零主库访问,也就不存在把真实用户名/邮箱漏给邀请人的可能。
//
// 项目没有独立的邀请绑定时间列(users.inviter_id 是注册时一次性写入),
// 所以 BoundAt 只能取 users.created_at。
type InviteRelation struct {
	InviteeId int `json:"invitee_id" gorm:"primaryKey"`
	InviterId int `json:"inviter_id" gorm:"not null;index:idx_qy_ir_inviter"`

	MaskedName string `json:"masked_name" gorm:"type:varchar(64);not null;default:''"`
	// InviteeRef 是对外唯一标识,绝不下发 user_id —— 那等于把用户 id 空间
	// 暴露给任何一个邀请人。
	InviteeRef string `json:"invitee_ref" gorm:"type:varchar(16);not null;uniqueIndex:uk_qy_ir_ref"`

	BoundAt int64 `json:"bound_at" gorm:"not null;default:0"`

	// 刻意不在这里维护"累计基数/累计佣金"两个计数器:那需要每条消费事件
	// 多写一次库,而且一旦与 accrual 漂移就再也对不回去。列表页按
	// inviter_id 聚合 qy_commission_accrual 即可,数据永远自洽。

	RiskFlags string `json:"risk_flags" gorm:"type:varchar(255);not null;default:''"`
	// Blocked 由管理员设置,置位后该关系不再计佣。已发放的佣金不回收。
	Blocked bool `json:"blocked" gorm:"not null"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (InviteRelation) TableName() string { return "qy_invite_relation" }

// 冻结动作。提现模块的每一步都以 (RefNo, Action) 为幂等键。
const (
	FreezeActionFreeze   = "freeze"
	FreezeActionUnfreeze = "unfreeze"
	FreezeActionSettle   = "settle"
)

// FreezeRecord 记录提现引起的额度冻结/解冻/兑现。
//
// 提现模块的接口会被重试(HTTP 超时、补偿任务重跑),没有这张表就无法区分
// "第二次调用"和"第二笔提现",冻结额度会被重复扣减。
type FreezeRecord struct {
	Id     int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	RefNo  string `json:"ref_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_cf,priority:1"`
	Action string `json:"action" gorm:"type:varchar(16);not null;uniqueIndex:uk_qy_cf,priority:2"`

	UserId int   `json:"user_id" gorm:"not null;index:idx_qy_cf_user"`
	Quota  int64 `json:"quota" gorm:"not null;default:0"`
	// Fiat 是本次冻结从 AvailableFiat 里扣走的法币金额。
	// 必须原样记下来:解冻时若按"当前均价"回补,提现审核期间新结算进来的
	// 佣金会把均价拉偏,退回的钱就和当初冻走的对不上了。
	Fiat      decimal.Decimal `json:"fiat" gorm:"type:decimal(18,6);not null;default:0"`
	CreatedAt int64           `json:"created_at" gorm:"not null;default:0"`
}

func (FreezeRecord) TableName() string { return "qy_commission_freeze" }
