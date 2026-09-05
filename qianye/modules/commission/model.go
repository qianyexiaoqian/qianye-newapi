// Package commission 实现推广佣金的账本、触发、结算与自动入账(D-16:以**星屑**结算)。
//
// 本包的账本以星屑记账:计佣那一刻就按冻结的 stardust.quota_per_unit 把消费/充值
// 额度折成星屑,持有期满结算进可用余额,达到门槛后由 autocredit.go 自动入账进
// qy_sd_balance.available —— 没有申请、没有审核、没有法币、没有提现,也**不碰主库额度**。
//
// # D-16 相对 D-15 少掉的那一整层
//
// D-15 的佣金记的是「星辉」(主库 users.quota 的展示名),自动入账因此是一次**跨库**
// 转账,必须走两阶段资金单(KindCommissionCredit)、冻结列、探针、人工裁决与对账。
// 改记星屑之后佣金与星屑同在扩展库,一次入账就是一个本地事务 —— 那一整层连同
// qy_commission_freeze 表、balance.frozen_quota 列、credit 的 held 态一起退役。
//
// 三条铁律,违反任何一条都会造成资损或用户投诉:
//
//  1. 佣金金额一律用 decimal 全精度累计,只在"落成整数星屑"那一刻才取整。
//     单次对话的消费常见 10~500 额度,折成星屑再乘 5% 是 0.000001 量级,
//     任何一步提前取整都会大量归零 —— 用户用了一整天却看到 0 佣金,而钱确实
//     被平台吞了。
//  2. 消费计佣挂在 relay 结算路径上,禁止在该线程里查库。邀请关系走进程内缓存
//     (含负缓存,由 modules/invite 提供),真正的写入交给 guard.HotAsync。
//  3. 每一笔充值/消费的计佣都必须幂等。三条触发路径(扫描、hook、人工重扫)
//     任意重叠都不能重复发钱,靠 (idem_scope, idem_key) 唯一索引兜住。
//
// 依赖方向:commission → invite(邀请关系、日界、充值口径)、commission → stardust
// (刻度与入账)。stardust 不得 import commission —— 那会成环。
package commission

import "github.com/shopspring/decimal"

// 计佣来源。取值同时作为 idem_scope,这样"按来源查"直接命中唯一索引前缀。
const (
	SourceTopup      = "topup"
	SourceRedemption = "redemption"
	SourceConsume    = "consume"
	SourceClawback   = "clawback"
	// SourceManual 是管理员手工增减佣金落下的账目行(见 api_admin_adjust.go)。
	//
	// 它必须是一条 accrual 而不是直接改 qy_commission_balance 的某一列:
	// 余额行上的每一分钱都由「Σ计佣 − Σ已结算 = 未结算」这条恒等式解释,
	// 绕过账本直接改列会让这条式子当场失效,而结算流水里没有任何一行能解释差额。
	//
	// 这一路没有下线(invitee_id 落 0)也没有费率(rate_bps 落 0):
	// 手工调整既不来自某一笔消费/充值,也不按任何比例算出来。
	SourceManual = "manual"
)

// 计佣行状态。
//
// 刻意不设 pending_review:宽松口径下逐笔人工审核在消费计佣场景不可行
// (行数 = 下线数 × 天数)。风控命中走 risk_hold 进人工队列,是例外而非常态。
const (
	StatusAccrued  = "accrued"   // 已计佣,等待成熟与结算
	StatusSettled  = "settled"   // 已被结算完全吸收(仅用于不会再增长的行)
	StatusRiskHold = "risk_hold" // 风控命中,需人工放行
	StatusVoided   = "voided"    // 人工作废,不参与结算
)

// Accrual 是计佣明细,账本的最小单位。只增不改(金额列除外),负额行表示冲正。
//
// 消费计佣按 (被邀请人, 自然日) 聚合成一行而不是每次请求一行:relay QPS 量级
// 会把表撑爆,而日聚合的行数上界 = 活跃下线数 × 天数,与 QPS 无关,同时保留了
// "这天这个下线消费了多少、返了多少"的可审计粒度。
type Accrual struct {
	Id        int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	AccrualNo string `json:"accrual_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_ca_no"`

	// IdemScope + IdemKey 是唯一幂等键。
	// 充值 = topup:<trade_no>;兑换码 = redemption:<id>;
	// 消费 = consume:<inviteeId>:<yyyymmdd>:…;冲正 = clawback:<...>。
	IdemScope string `json:"idem_scope" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_ca_idem,priority:1"`
	IdemKey   string `json:"idem_key" gorm:"type:varchar(96);not null;uniqueIndex:uk_qy_ca_idem,priority:2"`

	InviterId int `json:"inviter_id" gorm:"not null;index:idx_qy_ca_inviter,priority:1"`
	InviteeId int `json:"invitee_id" gorm:"not null;index:idx_qy_ca_invitee"`

	SourceType string `json:"source_type" gorm:"type:varchar(24);not null;default:''"`
	// SourceRef 是可展示的来源引用(trade_no / 兑换码 id / task_id),下发前脱敏。
	SourceRef string `json:"source_ref" gorm:"type:varchar(128);not null;default:''"`

	// BaseQuota 用 int64:日聚合会把整天基数累加,int32 在重度用户上会溢出。
	BaseQuota int64 `json:"base_quota" gorm:"not null;default:0"`
	// BaseMoney 是充值路径的原始付款金额,只作展示与对账。必须单独存 ——
	// common.QuotaPerUnit 是可改的全局变量,从 quota 反算付款金额,
	// 只要它被调过一次,历史订单的对账就全错。
	BaseMoney decimal.Decimal `json:"base_money" gorm:"type:decimal(18,6);not null;default:0.000000"`

	// RateUnits 是计佣当刻冻结的费率,单位万分比(10.25% = 1025)。列名 rate_bps。
	RateUnits int `json:"rate_bps" gorm:"column:rate_bps;not null;default:0"`

	// QuotaPerUnit 是计佣当刻冻结的刻度:1 星屑 = 多少额度。
	//
	// 必须逐行冻结,理由与 RateUnits / RateGroup 完全相同:它是运营可改的全局值
	// (stardust.quota_per_unit),不冻结就再也没有办法解释"这条 2 月的计佣为什么
	// base_quota 是 500000 而 gross 是 0.05" —— 复算会拿今天的刻度去除昨天的基数。
	//
	// 与 stardust 自己的日桶(qy_sd_accrual.quota_per_unit)是同一条纪律、同一个来源。
	QuotaPerUnit int64 `json:"quota_per_unit" gorm:"not null;default:0"`

	// RateGroup 是计佣当刻冻结的【推广人(上线)】分组。
	//
	// 为什么按上线的分组而不是被推广人的分组,见 grouprate.go 与 pricing.go 的口径说明。
	// 冻结的理由:分组费率是运营可随时改的,不冻结的话事后没有任何办法解释
	// "这条 2 月的佣金为什么是 8% 而不是现在的 5%"。
	//
	// 空串表示计佣时读不到该上线的分组信息(pricing.go 的降级标记 ——
	// "读不到这个人"与"这个人在 default 组"是两件事,账本上必须分得开)。
	RateGroup string `json:"rate_group" gorm:"type:varchar(64);not null;default:''"`

	// GrossAmount 是不截断的精确佣金(**星屑**),GrossAmount - SettledAmount 即待结算增量。
	// 用增量而非 status 翻转来驱动结算,日聚合行才能"边增长边结算"。
	GrossAmount   decimal.Decimal `json:"gross_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`
	SettledAmount decimal.Decimal `json:"settled_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`

	// CappedAmount 是单笔封顶(commission.max_per_order_stardust)从这一行累计削掉的星屑。
	//
	// 有了它,被削过的行才重新可复算:
	//
	//	base_quota × rate_bps / 10000 / quota_per_unit == gross_amount + capped_amount   (I3)
	//
	// **适用范围只有正额计佣行**:source_type ∈ {consume, topup, redemption}。
	// manual 的 rate_bps 恒为 0、clawback 的 base_quota 是负数的幂等指纹且金额
	// 可被等比冲正与 remaining 削减 —— 那两类按设计就不满足 I3。逐条理由写在
	// accrual.go 的 capGross 上。
	CappedAmount decimal.Decimal `json:"capped_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`

	Status    string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_ca_inviter,priority:2;index:idx_qy_ca_scan,priority:1"`
	RiskFlags string `json:"risk_flags" gorm:"type:varchar(255);not null;default:''"`
	// MatureAt 是成熟时间。结算只吸收已成熟的行,这样 qy_commission_balance.available
	// 天然全部可入账,自动入账不必再判时间维度。
	MatureAt int64 `json:"mature_at" gorm:"not null;default:0;index:idx_qy_ca_scan,priority:2"`
	// BucketDate 是消费日聚合的日键(yyyymmdd,invite.day_offset_minutes 口径)。非消费来源为空。
	//
	// 类型是 varchar(8) 而不是 char(8):本列的合法取值里包含空串,而定长 CHAR 在
	// PostgreSQL 上会把空串补成 8 个空格再原样读出来,同一份代码在两种方言上得到
	// 两个不同的值。扩展库不使用定长 CHAR,判据见 qianye/schema_crossdb_test.go。
	BucketDate string `json:"bucket_date" gorm:"type:varchar(8);not null;default:''"`

	RefAccrualId int64 `json:"ref_accrual_id" gorm:"not null;default:0;index:idx_qy_ca_ref"`
	SettlementId int64 `json:"settlement_id" gorm:"not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0;index:idx_qy_ca_created"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (Accrual) TableName() string { return "qy_commission_accrual" }

// Balance 是邀请人的佣金余额,单位星屑。
//
// 三个整数列受同一条恒等式约束(I2):
//
//	available + credited == total_earned − total_clawback
//
// 结算同时加 available 与 total_earned;冲正同时减 available、加 total_clawback;
// 自动入账把 available 直接搬进 credited(autocredit.go)。
//
// D-15 时这里还有第四列 frozen_quota —— 佣金记星辉,入账是一次跨库转账,必须
// 有"已开单、主库还没落定"的中间态。D-16 佣金改记星屑之后入账是本地事务,
// 要么全成要么全不成,中间态不再存在,那一列连同 qy_commission_freeze 表一并删除。
//
// UnsettledAmount(未结算余数)与 Available 必须在同一个事务、同一把行锁下
// 原子变更,所以刻意不拆成两张表 —— 拆开只会引入第二次加锁与新的中间态。
// 余数的审计轨迹由 Settlement 的 carry_before/carry_after 承载。
type Balance struct {
	UserId int `json:"user_id" gorm:"primaryKey"`

	// UnsettledAmount 承载所有不足 1 额度的零头。可以为负 —— 那表示冲正金额
	// 超过了可回收余额,即欠账,此时暂停自动入账,未来佣金自动优先抵扣。
	UnsettledAmount decimal.Decimal `json:"unsettled_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`

	// Available 是已结算、等着自动入账的星屑。
	Available int64 `json:"available" gorm:"not null;default:0"`
	// Credited 是已经入账进 qy_sd_balance.available 的累计星屑。
	Credited      int64 `json:"credited" gorm:"not null;default:0"`
	TotalEarned   int64 `json:"total_earned" gorm:"not null;default:0"`
	TotalClawback int64 `json:"total_clawback" gorm:"not null;default:0"`

	DebtBlocked bool `json:"debt_blocked" gorm:"not null"`

	// DailyCapWindowStart / DailyCapGranted 是日封顶(max_daily_stardust_per_inviter)
	// 的窗口状态,与余额在同一把行锁下变更。
	//
	// 「今日已发」不现算:日界由 invite.day_offset_minutes 决定,改一次配置窗口起点
	// 就整体平移,已发的结算行整批掉出窗口,**当天的日封顶原地满血复活**。窗口起点
	// 记在余额行上,新窗口只在「距上一个窗口起点已满 24 小时」时才开。
	//
	// DailyCapWindowStart == 0 = 从没结算过的人。DailyCapGranted 只累加发放,
	// 回收(clawback)不减 —— 封顶限的是「一天最多发出去多少」,不是净额。
	DailyCapWindowStart int64 `json:"daily_cap_window_start" gorm:"not null;default:0"`
	DailyCapGranted     int64 `json:"daily_cap_granted" gorm:"not null;default:0"`

	LastSettledAt int64 `json:"last_settled_at" gorm:"not null;default:0"`
	// LastCreditedAt 是最近一次自动入账落定的时刻,给用户端"上次入账"用。
	LastCreditedAt int64 `json:"last_credited_at" gorm:"not null;default:0"`
	CreatedAt      int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt      int64 `json:"updated_at" gorm:"not null;default:0;index:idx_qy_cb_updated"`
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
	DeltaAmount  decimal.Decimal `json:"delta_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`
	CarryBefore  decimal.Decimal `json:"carry_before" gorm:"type:decimal(30,10);not null;default:0.0000000000"`
	CarryAfter   decimal.Decimal `json:"carry_after" gorm:"type:decimal(30,10);not null;default:0.0000000000"`

	Granted   int64 `json:"granted" gorm:"not null;default:0"`
	Reclaimed int64 `json:"reclaimed" gorm:"not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0;index:idx_qy_cs_user,priority:2"`
}

func (Settlement) TableName() string { return "qy_commission_settlement" }

// 自动入账记录的状态。
//
// 只剩一个 done。D-15 的 pending / failed / held 三态描述的都是**跨库**转账的中间
// 与失败形状(资金单在途、探针确认主库未动、进人工裁决);D-16 入账是扩展库里的一个
// 本地事务,佣金余额的搬运与星屑流水的写入在同一次提交里,不存在"这一半成了那一半
// 没成"。留着那三个状态只会让运营在对账台上等一个永远不会出现的行。
const (
	CreditStatusDone = "done" // 已入账:available → credited,星屑流水已写
)

// Credit 是一次自动入账(qy_commission_credit)。一行 = 一笔进星屑余额的钱。
type Credit struct {
	Id       int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	CreditNo string `json:"credit_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_cc_no"`
	UserId   int    `json:"user_id" gorm:"not null;index:idx_qy_cc_user,priority:1"`
	// Amount 是这一笔入账的星屑数。
	Amount int64 `json:"amount" gorm:"not null;default:0"`
	// LedgerNo 是星屑账本那一侧的流水号(qy_sd_ledger.ledger_no,kind=commission_credit)。
	//
	// 它取代了 D-15 的 fund_order_no:两边都是"另一侧那一笔的锚点",区别是资金单要靠
	// 探针与裁决才能知道对面到底动没动,而流水号是同一个事务里写下的 —— 拿着它去星屑
	// 流水里一定找得到,找不到就说明有人绕过本模块改过账。
	LedgerNo   string `json:"ledger_no" gorm:"type:varchar(32);not null;default:'';index:idx_qy_cc_ledger"`
	Status     string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_cc_status"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;default:0;index:idx_qy_cc_user,priority:2"`
	FinishedAt int64  `json:"finished_at" gorm:"not null;default:0"`
	Remark     string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
}

func (Credit) TableName() string { return "qy_commission_credit" }
