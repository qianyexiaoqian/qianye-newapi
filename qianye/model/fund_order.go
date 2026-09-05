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
	// Fingerprint 是本单资金要素(金额/费用/双方/业务引用)的摘要。
	//
	// 唯一索引只保证"同一个键不会被重复执行",它保证不了"重放的是同一个请求"。
	// client_request_id 由前端生成,换个金额、换个收款人复用同一个键,幂等命中
	// 会返回原单成功,调用方据此写下金额虚高的成功审计 —— 资金侧毫无痕迹,
	// 而审计表是事后仲裁的唯一凭据。指纹就是用来堵这个洞的。
	//
	// 可选:空字符串表示"未参与校验"。AutoMigrate 给历史行补的默认值就是空,
	// 尚未接入指纹的调用方也是空;两种情况都必须跳过校验而不是判为不匹配,
	// 否则升级瞬间所有历史单的幂等重放都会变成 409。
	Fingerprint string `json:"fingerprint" gorm:"type:varchar(64);not null;default:''"`

	UserId     int `json:"user_id" gorm:"not null;index:idx_qy_fund_user,priority:1"`
	PeerUserId int `json:"peer_user_id" gorm:"not null;default:0;index"`

	// AmountQuota 用 int64 承载,但跨库写入主库前必须校验不超过 common.MaxQuota
	// (全站额度换算的算术上界,不是列宽)。绝不静默截断。
	AmountQuota int64 `json:"amount_quota" gorm:"not null"`
	// FeeQuota 即使当前配置为 0 也要落库,否则日后改了费率,历史单将无法解释。
	FeeQuota int64 `json:"fee_quota" gorm:"not null;default:0"`

	// RefType / RefId 用于溯源:transfer:<id>、mall_order:<order_no>、commission_credit:<credit_no>。
	RefType string `json:"ref_type" gorm:"type:varchar(32);not null;default:''"`
	RefId   string `json:"ref_id" gorm:"type:varchar(64);not null;default:'';index:idx_qy_fund_ref"`

	// AfterCommitAt 是"提交后收尾(缓存失效 + 账本行)已被认领"的时间戳。
	//
	// 存在理由:提交后收尾有两条入口 —— 业务线程的 Request.AfterCommit,
	// 与补偿任务确认主库已生效后的 PostCommit 回调。两条都必须能跑,
	// 但账本行只能写一次(写两条,用户在账单里看到的就是被扣了两次钱)。
	// 谁 CAS 赢下这一列谁执行,另一方直接跳过。
	//
	// 零值 0 = 尚无人认领。历史行(升级前的成功单)迁移后同样是 0,但补偿任务
	// 只扫 pending / in_doubt,永远不会回头碰它们,因此不存在"升级后给存量成功单
	// 补写一批账本行"的风险。
	AfterCommitAt int64 `json:"after_commit_at" gorm:"not null;default:0"`

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
	StatusPending int8 = 0 // 扩展库已落单,主库的 COMMIT 尚未发出
	StatusSuccess int8 = 2 // 主库已生效且扩展库已回写
	// StatusFailed 表示主库**明确**未生效。
	//
	// 判据不是"Execute 返回了错误",而是"COMMIT 从来没有发出去过":
	// 事务开启失败、事务体返回业务错误(余额不足、用户禁用)、语句执行报错 ——
	// 这三种情况下主库不可能提交,回滚是确定且安全的。
	//
	// commit 阶段断连**不属于**这一态,它落 StatusInDoubt。把它也塞进 Failed
	// 正是"抽奖双发"的根因:Failed 一旦同时意味着"没开始"和"结局不明",
	// 每一个按 Status == Failed 回滚/重发的调用方都在拿一个歧义值做不可逆动作。
	StatusFailed int8 = 3
	// StatusUncertain 是资金系统必须有的"我不知道,交给人"出口。
	// 探针耗尽重试仍无法判定时进入此态,由管理员在对账台裁决,或由
	// twophase.ReprobeUncertain 在探针恢复后自动复判。
	StatusUncertain int8 = 4
	StatusReversed  int8 = 5 // 已被冲正(如退款回收佣金)
	// StatusInDoubt 表示**主库的 COMMIT 已经发出,但结果未知**。
	//
	// 典型来源:tx.Commit() 返回错误。数据库连接在 COMMIT 期间断掉时,
	// 服务端仍可能把这笔事务提交下去;客户端拿到的那个 error 不含任何信息。
	// database/sql 在 ctx 恰好于 COMMIT 期间取消时也会走到这里。
	//
	// 与 Pending 的区别是**证据强度**,不是流程位置:Pending 意味着 COMMIT
	// 还没发出(补偿任务需要等主库事务自己了结),InDoubt 意味着已经发出
	// (探针读到什么就是什么)。两者都由补偿任务推进,都绝不允许调用方回滚。
	//
	// 与 Uncertain 的区别是**出口**:InDoubt 由机器复判(outbox 探针),
	// Uncertain 是探针也判不出来之后交给人的那一档。
	StatusInDoubt int8 = 6
)

// UnsettledStatuses 是补偿任务负责推进的两个未定局状态。
//
// 抽成函数而不是让 compensate.go 各处手写 []int8{Pending, InDoubt}:
// 这张列表决定了"哪些单还会被自动收敛",漏掉一处 CAS 就意味着那一档
// 状态的单永远停在原地。它是一个稳定的业务概念,不是为了缩短调用方。
func UnsettledStatuses() []int8 { return []int8{StatusPending, StatusInDoubt} }

// IsUnsettled 表示该状态仍在补偿任务的收敛范围内。
func IsUnsettled(s int8) bool { return s == StatusPending || s == StatusInDoubt }

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
	case StatusInDoubt:
		return "in_doubt"
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
	KindTransfer = "transfer"
	// KindCommissionCredit 是 D-15 的「佣金自动入账」走两阶段时的资金单:扩展库扣
	// available(转 frozen)→ 主库 IncreaseUserQuota(佣金那时记的是「星辉」= 主库额度)
	// → 扩展库 frozen 转 credited。单号前缀 CC。
	//
	// 自 D-16 起佣金改记**星屑**,佣金账本与星屑账本同在扩展库,一次入账就是一个本地
	// 事务,**不再产生这种单**,也没有 Resolver 注册它;常量保留只为让历史单据在资金单页
	// 与方向 / 单位判定里仍能被读出来,不是兼容路径(与下面 KindLotteryEntry 同一处置)。
	KindCommissionCredit = "commission_credit"
	// KindCommissionSettle / KindCommissionRevers / KindWithdrawQuota / KindWithdrawFiat 是
	// D-14 之前佣金结算 / 冲正与提现兑现走两阶段时的四种资金单。结算与冲正现在只在扩展库
	// 单事务里完成(D-15 恢复的账本不再为它们开资金单),提现模块已整体删除,
	// **不再产生这四种单**,也没有 Resolver 注册它们;常量保留只为让历史单据在资金单页
	// 与方向 / 单位判定里仍能被读出来,不是兼容路径。
	KindCommissionSettle = "commission_settle"
	KindCommissionRevers = "commission_reverse"
	KindWithdrawQuota    = "withdraw_quota"
	KindWithdrawFiat     = "withdraw_fiat"
	KindViolationFee     = "violation_fee"
	// KindLotteryEntry / KindLotteryPayout 是 v2.0.0 之前抽奖走两阶段时的两种资金单
	// (参与扣主库额度 / 派奖·赔付·退款加主库额度)。自 v2.0.0 起抽奖只动扩展库
	// 星屑账本、单事务,**不再产生这两种单**,也没有 Resolver 注册它们;常量保留
	// 只为让历史单据在资金单页与方向 / 单位判定里仍能被读出来,不是兼容路径。
	KindLotteryEntry  = "lottery_entry"
	KindLotteryPayout = "lottery_payout"
	// KindMallPlan 是星屑商城用星屑兑换套餐:扩展库扣星屑 → 主库发订阅 → 扩展库标完成。
	//
	// **单位例外**:这一 Kind 的 FundOrder.AmountQuota / qy_fund_outbox.amount /
	// 审计 AmountQuota 装的是**星屑数**,不是额度 —— 主库那一侧动的是 user_subscriptions
	// 与 subscription_orders,不是 users.quota,资金单上唯一有意义的金额就是用户付了
	// 多少星屑。twophase 的告警文案按 Kind 带单位(amountUnit),积压告警按 kind 分组求和,
	// 对账台前端按 row.kind === 'mall_plan' 切星屑展示。
	KindMallPlan = "mall_plan"
)

// KindCode 返回单号中使用的两字母类型码。
func KindCode(kind string) string {
	switch kind {
	case KindTransfer:
		return "TR"
	case KindCommissionCredit:
		return "CC"
	case KindCommissionSettle:
		return "CM"
	case KindCommissionRevers:
		return "RV"
	case KindWithdrawQuota, KindWithdrawFiat:
		return "WD"
	case KindViolationFee:
		return "VF"
	case KindLotteryEntry:
		return "LE"
	case KindLotteryPayout:
		return "LP"
	case KindMallPlan:
		return "MP"
	default:
		return "XX"
	}
}
