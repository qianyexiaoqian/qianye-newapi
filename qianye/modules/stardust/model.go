package stardust

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// model.go —— 本模块在扩展库里的七张表。
//
// 全部遵守 qianye/model/tables.go 的约定:qy_ 前缀硬编码在 TableName 里、时间戳一律
// int64 unix 秒并手工赋值、跨库无外键(user_id / plan_id 是主库的软引用)、
// decimal 列的 default 写满 scale、bool 列不写 default、索引名全库唯一
// (idx_qy_sd* / uk_qy_sd*,PostgreSQL 与 SQLite 的索引名是 schema 级的)。

// Balance 是每人一行的星屑余额,也是本模块**唯一的加锁点**(LockBalance)。
//
// 五个整数列由 kindColumn 的映射咬合成 I0(见 doc.go);它们都是**派生量**,
// 只能经 Credit / Debit 的条件 UPDATE 变动,任何直接 UPDATE 都会让 I1 当场失效。
type Balance struct {
	// UserId 是主库 users.id 的软引用。autoIncrement:false 写死:这一列永远由
	// 调用方给出,本库不分配任何用户 id。
	UserId int `json:"user_id" gorm:"primaryKey;autoIncrement:false"`

	// Available 是可用星屑,恒 ≥ 0(条件 UPDATE 断言)。
	Available int64 `json:"available" gorm:"not null;default:0"`
	// TotalEarned 是累计获得,**不含**手调。
	TotalEarned int64 `json:"total_earned" gorm:"not null;default:0"`
	// TotalSpent 是累计花掉的毛额,退款不回冲它(退款落 TotalRefunded)。
	TotalSpent int64 `json:"total_spent" gorm:"not null;default:0"`
	// TotalRefunded 是累计退回。
	TotalRefunded int64 `json:"total_refunded" gorm:"not null;default:0"`
	// TotalAdjusted 是管理员手调的累计**净额**,带符号:加 100 再减 30 是 70。
	TotalAdjusted int64 `json:"total_adjusted" gorm:"not null;default:0"`

	// Carry 是消费返结算的余数结转,落在 [0,1):日桶 gross 是 decimal,而账本只发整数,
	// 不足 1 星屑的零头留在这里等下一次结算 —— 否则小额消费的用户会一直拿到 0。
	Carry decimal.Decimal `json:"carry" gorm:"type:decimal(30,10);not null;default:0.0000000000"`
	// InviteCarry 是**下线消费返**结算的余数结转,与 Carry 分开:两条日结各自 floor、
	// 各自结转,混在一列里会让消费返的零头被邀请返那一笔顺手发出去,I2 / I3 两条
	// 恒等式就再也分不开谁欠谁。
	InviteCarry decimal.Decimal `json:"invite_carry" gorm:"type:decimal(30,10);not null;default:0.0000000000"`

	// HoldReason 是暂缓发放的原因(HoldOverdraft / HoldAccountRemoved /
	// HoldAccountDisabled),空串 = 正常;消费返与下线消费返共用这一列 ——
	// 两条日结对同一个人读的是同一份主库快照,判据相同;该用户名下的 held 桶归零时
	// 由结算清空。
	HoldReason string `json:"hold_reason" gorm:"type:varchar(32);not null;default:''"`

	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Balance) TableName() string { return "qy_sd_balance" }

// 消费返暂缓的三种原因,写进 Balance.HoldReason 与 Accrual.HoldReason。
const (
	HoldOverdraft       = "overdraft"
	HoldAccountRemoved  = "account_removed"
	HoldAccountDisabled = "account_disabled"
)

// Ledger 是流水:只追加,每一次余额变动恰好一行。
//
// 它是本模块的**证据表**:永不随活动 / 订单 / 用户删除而删除,也不注册任何清理任务。
// 争议时能回答"这个人的星屑是怎么来的、怎么没的"的只有它。
type Ledger struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// LedgerNo 由 NewLedgerNo 生成:SD + UTC 时间 + 序列 + 随机,≤ 32 字符。
	LedgerNo string `json:"ledger_no" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_sdl_no"`
	UserId   int    `json:"user_id" gorm:"not null;index:idx_qy_sdl_user"`
	// Kind 是流水种类(Kind* 常量),决定它落在余额行的哪一列(kindColumn)。
	Kind string `json:"kind" gorm:"type:varchar(32);not null"`
	// Amount 带符号:Credit 为正、Debit 为负。I1 就是对这一列求和。
	Amount int64 `json:"amount" gorm:"not null;default:0"`
	// BalanceAfter 是写入这一行之后的 available 快照,链式核对用。
	BalanceAfter int64 `json:"balance_after" gorm:"not null;default:0"`

	// IdemScope + IdemKey 是唯一幂等键(§3.2 表列出了每种 kind 的键形状)。
	// IdemKey 超过 96 字节时经 sha256 折叠(normalizeIdemKey),保证单射。
	// IdemKey 不下发:它装的是调用方给的 client_request_id 之类的原文。
	IdemScope string `json:"idem_scope" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_sdl_idem,priority:1"`
	IdemKey   string `json:"-" gorm:"type:varchar(96);not null;uniqueIndex:uk_qy_sdl_idem,priority:2"`

	// RefType / RefNo 是关联单据:lot_entry / 票号、lot_payout / 出款号、
	// mall_order / 订单号、sd_settle / run_date、topup / 充值单号……
	RefType string `json:"ref_type" gorm:"type:varchar(32);not null;default:''"`
	RefNo   string `json:"ref_no" gorm:"type:varchar(64);not null;default:'';index:idx_qy_sdl_ref"`
	// ActNo 在 lot_* 行上冗余活动号(其余空串):活动删除之后仍能按活动归拢流水 ——
	// 本表永不随活动删除,而活动行删掉之后 RefNo 里的票号就再也 JOIN 不回活动了。
	ActNo string `json:"act_no" gorm:"type:varchar(32);not null;default:'';index:idx_qy_sdl_act"`
	// PeerUserId 是邀请返时的下线 id;其余 0。
	PeerUserId int `json:"peer_user_id" gorm:"not null;default:0"`

	// RateBps / RateGroup / BaseQuota 是冻结的比例、分组与基数(适用的 kind):
	// 比例与分组表是运营可随时改的,不冻结就没法解释一条三个月前的流水。
	// RateGroup 对 groupns 的残留处置是 keep(见 residue.go)。
	RateBps   int    `json:"rate_bps" gorm:"not null;default:0"`
	RateGroup string `json:"rate_group" gorm:"type:varchar(64);not null;default:''"`
	BaseQuota int64  `json:"base_quota" gorm:"not null;default:0"`

	// Remark 是手调事由等;OperatorId 是手调的操作人,系统 0。
	Remark     string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	OperatorId int    `json:"operator_id" gorm:"not null;default:0"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;default:0"`
}

func (Ledger) TableName() string { return "qy_sd_ledger" }

// Accrual 是消费返的日桶:某人某天消费站内余额换算出的应返星屑。
//
// 一天一人一行,(user_id, bucket_date) 唯一。status 从 computed 走到 settled 或 held;
// 已 held 的桶 gross 冻结于首次计算值,rerun 不追溯。
type Accrual struct {
	Id     int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId int   `json:"user_id" gorm:"not null;uniqueIndex:uk_qy_sda_bucket,priority:1"`
	// BucketDate 是 YYYYMMDD,日界读 invite.day_offset_minutes(D-C)。
	// varchar(8) 而不是 char(8):定长 CHAR 在 PostgreSQL 上会把空串补成空格再原样读出
	// (qianye/schema_crossdb_test.go)。
	BucketDate string `json:"bucket_date" gorm:"type:varchar(8);not null;uniqueIndex:uk_qy_sda_bucket,priority:2"`
	// UserGroup 是结算那一刻读到的 users.group,**一律以 groupname.Effective 归一化后
	// 的形式存储**(冻结的值必须就是判定用的值);对 groupns 的残留处置是 keep。
	UserGroup string `json:"user_group" gorm:"type:varchar(64);not null;default:''"`
	// RateBps / QuotaPerUnit 是冻结的比例与刻度;BaseQuota 是 Σ(type=2) 的消费额度,天然 ≥ 0。
	RateBps      int   `json:"rate_bps" gorm:"not null;default:0"`
	QuotaPerUnit int64 `json:"quota_per_unit" gorm:"not null;default:0"`
	BaseQuota    int64 `json:"base_quota" gorm:"not null;default:0"`
	// Gross = base_quota × rate_bps / 10000 / quota_per_unit,全精度;整数化在结算时做。
	Gross decimal.Decimal `json:"gross" gorm:"type:decimal(30,10);not null;default:0.0000000000"`
	// Status ∈ AccrualComputed / AccrualSettled / AccrualHeld。
	Status string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_sda_status"`
	// LedgerId 是结算后指向的流水行;net=0 的桶 settled 且 ledger_id=0。
	LedgerId   int64  `json:"ledger_id" gorm:"not null;default:0"`
	HoldReason string `json:"hold_reason" gorm:"type:varchar(32);not null;default:''"`
	ComputedAt int64  `json:"computed_at" gorm:"not null;default:0"`
	SettledAt  int64  `json:"settled_at" gorm:"not null;default:0"`
}

func (Accrual) TableName() string { return "qy_sd_accrual" }

// 日桶状态。
const (
	AccrualComputed = "computed"
	AccrualSettled  = "settled"
	AccrualHeld     = "held"
)

// InviteAccrual 是**下线消费返**的日桶:某个邀请人名下某个下线某天的站内消费,
// 按邀请人自己的分组档换算出的应返星屑(D-14:邀请收益只有星屑)。
//
// 一天一对 (inviter, invitee) 一行;status 与 Accrual 同一套(computed → settled / held)。
// 基数口径与消费返完全相同(同一次 logs 聚合),档位按**邀请人**分组(D-02),
// bps=0 时不写行。已 held 的桶 gross 冻结于首次计算值,rerun 不追溯。
type InviteAccrual struct {
	Id        int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	InviterId int   `json:"inviter_id" gorm:"not null;uniqueIndex:uk_qy_sdia_bucket,priority:1"`
	InviteeId int   `json:"invitee_id" gorm:"not null;uniqueIndex:uk_qy_sdia_bucket,priority:2;index:idx_qy_sdia_invitee"`
	// BucketDate 是 YYYYMMDD,日界读 invite.day_offset_minutes。varchar(8) 而不是 char(8),
	// 理由同 Accrual.BucketDate。
	BucketDate string `json:"bucket_date" gorm:"type:varchar(8);not null;uniqueIndex:uk_qy_sdia_bucket,priority:3"`
	// BaseQuota 是下线当日 type=2 的消费额度(排除口径与消费返相同)。
	BaseQuota int64 `json:"base_quota" gorm:"not null;default:0"`
	// RateGroup 是结算那一刻读到的**邀请人**分组(groupname.Effective 归一化后),
	// 对 groupns 的残留处置是 keep —— 它是"这一笔当时按哪个分组算的"这个事实。
	RateGroup string `json:"rate_group" gorm:"type:varchar(64);not null;default:''"`
	// Bps / QuotaPerUnit 是冻结的比例与刻度。
	Bps          int   `json:"bps" gorm:"column:bps;not null;default:0"`
	QuotaPerUnit int64 `json:"quota_per_unit" gorm:"not null;default:0"`
	// Gross = base_quota × bps / 10000 / quota_per_unit,全精度;整数化在结算时按邀请人汇总做。
	Gross decimal.Decimal `json:"gross" gorm:"type:decimal(30,10);not null;default:0.0000000000"`
	// Status ∈ AccrualComputed / AccrualSettled / AccrualHeld。
	Status     string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_sdia_status"`
	HoldReason string `json:"hold_reason" gorm:"type:varchar(32);not null;default:''"`
	// LedgerNo 是结算后指向的流水单号;net=0 的桶 settled 且为空串。
	LedgerNo  string `json:"ledger_no" gorm:"type:varchar(32);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (InviteAccrual) TableName() string { return "qy_sd_invite_accrual" }

// GroupRate 是按用户分组的比例覆盖;没有这一行(或 Enabled=false)的分组回落全站默认。
//
// 四个比例都是指针三态:nil = 本组没单独配这一档,回落全站;0 是显式的"这一档不返"。
// 哨兵值(-1 之类)迟早会有人当成比例算,所以用可空列。
type GroupRate struct {
	// UserGroup 是主库 users.group 的取值,**一律以 groupname.Normalize 归一化后的形式存储**:
	// 扩展库的 varchar 列在 MySQL 上继承大小写不敏感的排序规则,而 Go 侧是精确匹配,
	// 不折叠就会出现"管理端写 VIP、库里改的是 vip 那一行"的三方错位(见 groupname 包)。
	UserGroup string `json:"user_group" gorm:"type:varchar(64);primaryKey"`

	ConsumeBps       *int `json:"consume_bps" gorm:"column:consume_bps"`
	InviteTopupBps   *int `json:"invite_topup_bps" gorm:"column:invite_topup_bps"`
	InviteRedeemBps  *int `json:"invite_redeem_bps" gorm:"column:invite_redeem_bps"`
	InviteConsumeBps *int `json:"invite_consume_bps" gorm:"column:invite_consume_bps"`

	// Enabled 为 false 时整行回落全站,不等于零比例。不写 default 标签:
	// MySQL 与 PostgreSQL 对布尔默认值的归一化不同,会让 AutoMigrate 每次启动都发 ALTER。
	Enabled bool `json:"enabled" gorm:"not null"`

	OperatorId int   `json:"operator_id" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (GroupRate) TableName() string { return "qy_sd_group_rate" }

// PlanReward 是套餐上的星屑返还定义:扩展库 per-plan 附表,不给主库 subscription_plans 加列。
// 套餐删除时由 subscription 模块级联删掉这一行。
type PlanReward struct {
	// PlanId 是主库 subscription_plans.id 的软引用;autoIncrement:false 的理由同 Balance.UserId。
	PlanId int `json:"plan_id" gorm:"primaryKey;autoIncrement:false"`
	// BuyerBps / InviterBps:0 = 不返;10000 = 按售价 1:1。
	BuyerBps   int `json:"buyer_bps" gorm:"not null;default:0"`
	InviterBps int `json:"inviter_bps" gorm:"not null;default:0"`
	// Sources 是逗号分隔的触发来源,取值只能是 planRewardSources 这个闭集的子集
	// (NormalizeSources 负责校验与归一化),默认 DefaultPlanRewardSources。
	Sources    string `json:"sources" gorm:"type:varchar(64);not null;default:''"`
	OperatorId int    `json:"operator_id" gorm:"not null;default:0"`
	UpdatedAt  int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (PlanReward) TableName() string { return "qy_sd_plan_reward" }

// 套餐返的触发来源闭集:订单购买 / 余额购买 / 管理员授予 / 兑换码。
const (
	PlanSourceOrder      = "order"
	PlanSourceBalance    = "balance"
	PlanSourceAdmin      = "admin"
	PlanSourceRedemption = "redemption"

	// DefaultPlanRewardSources 是没配过 sources 时的口径:只有真花了钱的两条路返。
	DefaultPlanRewardSources = "order,balance"
)

var planRewardSources = map[string]bool{
	PlanSourceOrder: true, PlanSourceBalance: true, PlanSourceAdmin: true, PlanSourceRedemption: true,
}

// NormalizeSources 把用户输入的来源串归一成"去重、排序、逗号分隔"的存储形式。
//
// 空串归成 DefaultPlanRewardSources;任何不在闭集里的词都返回 false(调用方回
// qy_sd_bad_source),而不是静默丢掉那个词 —— 丢掉的表现是运营以为配了 admin,
// 实际管理员授予的套餐一颗星屑都不返。
func NormalizeSources(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return DefaultPlanRewardSources, true
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		if p == "" {
			continue
		}
		if !planRewardSources[p] {
			return "", false
		}
		seen[p] = true
	}
	if len(seen) == 0 {
		return "", false
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return strings.Join(out, ","), true
}

// SettleRun 是一日一结算的运行记录,
// 一天一行,run_date 唯一。
//
// 既是"今天这一次跑过了没有"的状态(run_date 唯一),也是健康面板要看的那几个数。
type SettleRun struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// RunDate 是结算日界口径下的"今天"(invite.DayKey),不是 UTC 自然日;
	// 它结的是前一天的桶(TargetDate)。
	RunDate string `json:"run_date" gorm:"type:varchar(8);not null;uniqueIndex:uk_qy_sdr_date"`
	// TargetDate 是这一次结算的桶日 D = run_date 的前一天。单独存一列而不是现算:
	// 日界偏移被调整过之后,"这一跑结的是哪一天"必须是当时的事实而不是现在的推算。
	TargetDate string `json:"target_date" gorm:"type:varchar(8);not null;default:''"`
	// Status ∈ SettleRunRunning / SettleRunDone / SettleRunPartial。
	Status string `json:"status" gorm:"type:varchar(16);not null;default:''"`
	// Holder 是最后一次尝试的持有者(节点名:随机后缀),与租约表同一口径。
	Holder   string `json:"holder" gorm:"type:varchar(200);not null;default:''"`
	Attempts int    `json:"attempts" gorm:"not null;default:0"`

	StartedAt  int64 `json:"started_at" gorm:"not null;default:0"`
	FinishedAt int64 `json:"finished_at" gorm:"not null;default:0"`
	// HeartbeatAt 在排空过程中逐轮刷新,是"持有者还活着"的唯一证据。
	HeartbeatAt int64 `json:"heartbeat_at" gorm:"not null;default:0"`

	Rounds    int `json:"rounds" gorm:"not null;default:0"`
	Processed int `json:"processed" gorm:"not null;default:0"`
	Failed    int `json:"failed" gorm:"not null;default:0"`
	// Held 是这一跑被暂缓的用户数(账号透支 / 已删 / 已禁用)。
	Held int `json:"held" gorm:"not null;default:0"`
	// Granted 是这一跑发出去的星屑总数。
	Granted int64 `json:"granted" gorm:"not null;default:0"`
	// InviteProcessed / InviteHeld / InviteFailed / InviteGranted 是同一跑里
	// **下线消费返**那一段的四个数,按邀请人计。与上面四个分开记:两条日结的
	// 候选集不同(一个按消费者、一个按邀请人),合在一起哪个数都说不清。
	InviteProcessed int   `json:"invite_processed" gorm:"not null;default:0"`
	InviteHeld      int   `json:"invite_held" gorm:"not null;default:0"`
	InviteFailed    int   `json:"invite_failed" gorm:"not null;default:0"`
	InviteGranted   int64 `json:"invite_granted" gorm:"not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (SettleRun) TableName() string { return "qy_sd_settle_run" }

// 运行状态。partial 与 done 的区别只有一个:partial 今天还会被重试。
const (
	SettleRunRunning = "running"
	SettleRunDone    = "done"
	SettleRunPartial = "partial"
)

// Tables 是本模块交给 AutoMigrate 的全部模型。
//
// Ledger 是永不清理的证据表;Balance / Accrual / InviteAccrual / GroupRate / PlanReward
// 是账目与配置;SettleRun 是运行记录。本模块不注册任何清理任务。
func Tables() []any {
	return []any{&Balance{}, &Ledger{}, &Accrual{}, &InviteAccrual{}, &GroupRate{}, &PlanReward{}, &SettleRun{}}
}

// ─────────────────────────── kind → 累计列 ───────────────────────────

// 流水种类。取值同时是 qy_sd_ledger.kind 的字面量,改任何一个都是一次数据迁移。
const (
	KindConsumeRebate  Kind = "consume_rebate"  // 消费返日结,+
	KindInviteConsume  Kind = "invite_consume"  // 下线消费返上线(日结),+
	KindInviteTopup    Kind = "invite_topup"    // 下线充值返上线,+
	KindInviteRedeem   Kind = "invite_redeem"   // 下线用兑换码返上线,+
	KindInviteRegister Kind = "invite_register" // 下线注册奖,+
	KindPlanBuyer      Kind = "plan_buyer"      // 买套餐返买家,+
	KindPlanInviter    Kind = "plan_inviter"    // 买套餐返上线,+
	// KindCommissionCredit 是推广佣金账本的自动入账(D-16)。
	//
	// 它与上面四个 invite_* 是**两条线**:invite_* 由本模块自己的 hook / 日结当场发,
	// 佣金那条线先在 qy_commission_* 里计佣、过持有期、按门槛攒够,才落成这一行。
	// 两条线的基数可以重叠(同一笔下线消费),重叠与否是运营的配置选择,不是本表的事 ——
	// 但流水上必须分得开,否则"这 10 星屑是邀请返还是佣金"永远答不上来。
	KindCommissionCredit Kind = "commission_credit" // 推广佣金自动入账,+
	KindLotPrize         Kind = "lot_prize"         // 活动派奖,+
	KindLotStake         Kind = "lot_stake"         // 报名 / 投注 / 转一次,−
	KindMallOrder        Kind = "mall_order"        // 商城下单,−
	KindLotRefund        Kind = "lot_refund"        // 活动退款,+
	KindMallRefund       Kind = "mall_refund"       // 商城退款,+
	KindManual           Kind = "manual"            // 管理员手调,±
)

// 余额行上的四个累计列名,与 Balance 的 gorm 列名逐字一致。
const (
	colTotalEarned   = "total_earned"
	colTotalSpent    = "total_spent"
	colTotalRefunded = "total_refunded"
	colTotalAdjusted = "total_adjusted"
)

// kindColumn 回答"这种流水落在余额行的哪一列"(design §3.2)。
//
// 一个 kind 只落一列是 I0 闭合的全部依据:表在这里只有一份,ledger-check 复算
// 与 Credit / Debit 写入都必须查同一张表。未知 kind 返回 ErrBadKind。
func kindColumn(kind Kind) (string, error) {
	switch kind {
	case KindConsumeRebate, KindInviteConsume, KindInviteTopup, KindInviteRedeem, KindInviteRegister,
		KindPlanBuyer, KindPlanInviter, KindLotPrize, KindCommissionCredit:
		return colTotalEarned, nil
	case KindLotStake, KindMallOrder:
		return colTotalSpent, nil
	case KindLotRefund, KindMallRefund:
		return colTotalRefunded, nil
	case KindManual:
		return colTotalAdjusted, nil
	}
	return "", fmt.Errorf("%w: %q", ErrBadKind, string(kind))
}

// postingColumn 在 kindColumn 之上再校验**方向**:获得类 / 退回类只能 Credit,
// 花掉类只能 Debit,只有 manual 两个方向都收。
//
// 少了这一层,一次 Credit(kind=lot_stake) 会把钱加进 available 却记到 total_spent,
// I0 当场不成立,而流水行本身看起来完全正常。
func postingColumn(kind Kind, debit bool) (string, error) {
	col, err := kindColumn(kind)
	if err != nil {
		return "", err
	}
	switch col {
	case colTotalSpent:
		if !debit {
			return "", fmt.Errorf("%w: %q 只能扣减", ErrBadKind, string(kind))
		}
	case colTotalEarned, colTotalRefunded:
		if debit {
			return "", fmt.Errorf("%w: %q 只能入账", ErrBadKind, string(kind))
		}
	}
	return col, nil
}
