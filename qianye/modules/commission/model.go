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
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/shopspring/decimal"
)

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
	BaseMoney decimal.Decimal `json:"base_money" gorm:"type:decimal(18,6);not null;default:0.000000"`

	// RateUnits 是计佣当刻冻结的费率,单位是"百分比 × 100"(10.25% = 1025)。
	//
	// 列名保持 rate_bps 不变:数值口径与万分比完全一致(百分比 × 100 就是
	// 万分之一),改名只能换来一次资金账本表的数据迁移和一批需要同步改的
	// 历史查询,换不来任何东西。对外(YAML、管理端、前端)一律是百分比。
	RateUnits int `json:"rate_bps" gorm:"column:rate_bps;not null;default:0"`

	// RateGroup 是计佣当刻冻结的【推广人(上线)】分组。
	//
	// 为什么按上线的分组而不是被推广人的分组,见 grouprate.go 与 pricing.go 的口径说明。
	// 冻结的理由与 UsdRate 完全一样:分组费率是运营可随时改的,不冻结的话
	// 事后没有任何办法解释"这条 2 月的佣金为什么是 8% 而不是现在的 5%"。
	//
	// 空串表示计佣时读不到该上线的分组信息(pricing.go 的降级标记 ——
	// "读不到这个人"与"这个人在 default 组"是两件事,账本上必须分得开);
	// 费率是否命中了分组规则要看 RateUnits 与当时的全局默认是否一致,
	// 本列只负责记录事实。
	//
	// **这一列在库里是双语义的**:2026-08-18 之前落的行冻结的是【下线】分组
	// (那时费率按下线分组判定),之后的行是【上线】分组。存量行不迁移、不改写
	// —— 冻结值是"当时按什么算的"这个事实。对账时先看 created_at 落在哪一侧。
	RateGroup string `json:"rate_group" gorm:"type:varchar(64);not null;default:''"`

	// GrossAmount 是不截断的精确佣金,GrossAmount - SettledAmount 即待结算增量。
	// 用增量而非 status 翻转来驱动结算,日聚合行才能"边增长边结算"。
	GrossAmount   decimal.Decimal `json:"gross_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`
	SettledAmount decimal.Decimal `json:"settled_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`

	// CappedAmount 是单笔封顶(commission.max_per_order_quota)从这一行累计削掉的金额。
	//
	// 有了它,被削过的行才重新可复算:
	//
	//	base_quota × rate_bps / 10000 == gross_amount + capped_amount
	//
	// 没有它的时候,"触顶少发"与"费率被人改错"在账面上完全同形,而封顶是同模块
	// 唯一一处不留痕的截断(结算 int32 触顶写 Remark、充值换算触顶打告警)。
	//
	// 零值口径:0 = **这一行没有被封顶削减过**,不是"未知"。两类行会是 0——
	// 从来没触顶的行(绝大多数),以及本次修复上线之前就已经被削过的存量行。
	// 后者刻意不回填:账本 append-only,金额列的历史值是冻结事实,补一个
	// 事后算出来的数字进去等于把"我们改写过它"这件事抹掉。存量被削行仍然
	// 对不平,那正是它当时被写坏的样子。
	CappedAmount decimal.Decimal `json:"capped_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`

	// UsdRate 冻结计佣当时的汇率。USDExchangeRate 是管理员可随时热改的全局变量,
	// 不冻结的话历史对账永远对不上。
	UsdRate decimal.Decimal `json:"usd_rate" gorm:"type:decimal(18,8);not null;default:0.00000000"`

	Status    string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_ca_inviter,priority:2;index:idx_qy_ca_scan,priority:1"`
	RiskFlags string `json:"risk_flags" gorm:"type:varchar(255);not null;default:''"`
	// MatureAt 是成熟时间(裁定 C23)。结算只吸收已成熟的行,这样
	// qy_commission_balance.available_quota 天然全部可提,提现模块无需再判时间维度。
	MatureAt int64 `json:"mature_at" gorm:"not null;default:0;index:idx_qy_ca_scan,priority:2"`
	// BucketDate 是消费日聚合的自然日(yyyymmdd,UTC)。非消费来源为空。
	//
	// 类型是 varchar(8) 而不是 char(8),这一处**不是**风格问题:本列的合法取值
	// 里包含空串(充值来源不填桶),而定长 CHAR 在 PostgreSQL 上会把空串补成
	// 8 个空格再原样读出来 —— MySQL 的 CHAR 在读取时会剥掉尾随空格,于是同一份
	// 代码在两种方言上得到 "" 与 "        " 两个不同的值。它会顺着
	// api_user.go 的 bucket_date 出到接口上,也会让任何 Go 端的 == "" 判断分叉。
	// 扩展库不再使用定长 CHAR,判据见 qianye/schema_crossdb_test.go。
	BucketDate string `json:"bucket_date" gorm:"type:varchar(8);not null;default:''"`

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
	UnsettledAmount decimal.Decimal `json:"unsettled_amount" gorm:"type:decimal(30,10);not null;default:0.0000000000"`

	AvailableQuota     int64 `json:"available_quota" gorm:"not null;default:0"`
	FrozenQuota        int64 `json:"frozen_quota" gorm:"not null;default:0"`
	WithdrawnQuota     int64 `json:"withdrawn_quota" gorm:"not null;default:0"`
	TotalEarnedQuota   int64 `json:"total_earned_quota" gorm:"not null;default:0"`
	TotalClawbackQuota int64 `json:"total_clawback_quota" gorm:"not null;default:0"`

	// AvailableFiat 按计佣时刻的冻结汇率折算,提现模块直接读它,
	// 禁止用当前 USDExchangeRate 反算。
	AvailableFiat decimal.Decimal `json:"available_fiat" gorm:"type:decimal(18,6);not null;default:0.000000"`
	FiatCurrency  string          `json:"fiat_currency" gorm:"type:varchar(8);not null;default:''"`

	DebtBlocked bool `json:"debt_blocked" gorm:"not null"`

	// DailyCapWindowStart / DailyCapGranted 是日封顶(max_daily_quota_per_inviter)
	// 的窗口状态,与余额在同一把行锁下变更。
	//
	// # 为什么不能像以前那样现算
	//
	// 「今日已发」曾经是 `SUM(granted_quota) WHERE created_at >= dayStart(now)`,
	// 而 dayStart 的日界完全由 commission.day_offset_minutes 决定。那个值不进
	// 幂等键、不进结算行、不参与任何身份:改一次配置重启(或灰度期间两个节点
	// 取值不同),窗口起点就整体平移,已发的结算行整批掉出窗口,SUM 读到 0,
	// **当天的日封顶原地满血复活**。日封顶是单个推广人「一天最多拿多少」的
	// 唯一总量闸门,击穿它等于当天风控失效,而结算行本身完全自洽、没有任何
	// 计数器会响。
	//
	// 现在窗口起点被**记在余额行上**,新窗口只在「距上一个窗口起点已满 24 小时」
	// 时才开。日界怎么挪都不会凭空造出第二个窗口;偏移往前挪时最坏是当天那一份
	// 封顶晚几个小时才开(钱留在 unsettled 里,下一跑照发),方向是保守的那一边。
	//
	// # 零值口径
	//
	// DailyCapWindowStart == 0 = 本列上线前的存量行(以及从没结算过的人)。
	// 此时按旧口径查一次结算行补出「今天已发多少」,否则升级当天每个人的封顶
	// 都会白白多出一份。DailyCapGranted 只累加发放,回收(clawback)不减 ——
	// 封顶限的是「一天最多发出去多少」,不是净额。
	DailyCapWindowStart int64 `json:"daily_cap_window_start" gorm:"not null;default:0"`
	DailyCapGranted     int64 `json:"daily_cap_granted" gorm:"not null;default:0"`

	// 这里曾经有一个 InviteeCount(下线数)。它**从来没有任何写入方** ——
	// 设计稿里写着"计佣路径顺手维护",实现从未落地,于是全库每一个真正有下线的
	// 推广人在这一列上都是 0(实测 658 行里 24 个非零值与真实下线数无关,
	// 是灌库夹具留下的)。管理端「佣金余额总览」把它原样下发,而同一个数字在
	// 用户端与「佣金总表」页是现算的 —— 三个页面两套口径,其中一套恒为 0。
	//
	// 现在下线数一律现算(hydrateBalanceViews / api_admin_users.go 同一份口径:
	// qy_invite_relation 里 unbound_at = 0 的行数)。
	//
	// 字段从模型里摘掉而不是 DropColumn:SQLite 上 GORM 的 DropColumn 会静默失败,
	// 而一个没人读也没人写的列留在表里是无害的。

	LastSettledAt int64 `json:"last_settled_at" gorm:"not null;default:0"`
	CreatedAt     int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt     int64 `json:"updated_at" gorm:"not null;default:0;index:idx_qy_cb_updated"`
}

func (Balance) TableName() string { return "qy_commission_balance" }

// frozenFiatCurrency 给出一行法币余额到底是哪一种钱。
//
// AvailableFiat 是按每次结算当时的汇率一笔笔累起来的,币种必须来自同一时刻 ——
// 拿当前的 withdraw.fiat_currency 去顶替,等于运营改一次配置,全部历史金额
// 一个数字没动、标签却全变了。
//
// 空串只出现在 FiatCurrency 这一列被真正写入之前的存量行上(结算过一次就会
// 被补上)。那些行本来就是按当时的全局配置算出来的,所以回落到当前配置是
// 它们唯一正确的答案;而 config 侧已经有一条闸门保证这个值与汇率来源自洽。
func frozenFiatCurrency(frozen string) string {
	if frozen != "" {
		return frozen
	}
	return config.Get().Withdraw.FiatCurrency
}

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

	GrantedQuota   int64 `json:"granted_quota" gorm:"not null;default:0"`
	ReclaimedQuota int64 `json:"reclaimed_quota" gorm:"not null;default:0"`

	UsdRateWeighted decimal.Decimal `json:"usd_rate_weighted" gorm:"type:decimal(18,8);not null;default:0.00000000"`
	FiatDelta       decimal.Decimal `json:"fiat_delta" gorm:"type:decimal(18,6);not null;default:0.000000"`

	Remark    string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0;index:idx_qy_cs_user,priority:2"`
}

func (Settlement) TableName() string { return "qy_commission_settlement" }

// InviteRelation 是邀请关系快照。
//
// # 它不是权威,users.inviter_id 才是
//
// 计佣链路上"谁是这个人的邀请人"永远只问主库的 users.inviter_id
// (resolveInviter → peekInviter),本表只是**懒建**的展示快照:
// ensureRelation 在某个下线第一次产生佣金时才写这一行。因此本表的行数
// 天然少于真实的绑定数(备份库实测:users 里 375 条绑定,本表 8 行),
// 拿它当"AFF 关系列表"的数据源会漏掉绝大多数关系。
//
// 存在的意义是"已邀请用户列表"这个页面:脱敏名在服务端算好并缓存在这里,
// 列表页零主库访问,也就不存在把真实用户名/邮箱漏给邀请人的可能。
//
// 项目没有独立的邀请绑定时间列(users.inviter_id 是注册时一次性写入),
// 所以自动建出来的行 BoundAt 只能取 users.created_at;管理员手工绑定的行
// 取绑定发生的那一刻 —— 两者都是"这条关系是什么时候成立的"这个事实。
type InviteRelation struct {
	InviteeId int `json:"invitee_id" gorm:"primaryKey"`
	InviterId int `json:"inviter_id" gorm:"not null;index:idx_qy_ir_inviter"`

	MaskedName string `json:"masked_name" gorm:"type:varchar(64);not null;default:''"`
	// InviteeRef 是对外唯一标识,绝不下发 user_id —— 那等于把用户 id 空间
	// 暴露给任何一个邀请人。
	InviteeRef string `json:"invitee_ref" gorm:"type:varchar(16);not null;uniqueIndex:uk_qy_ir_ref"`

	BoundAt int64 `json:"bound_at" gorm:"not null;default:0"`

	// UnboundAt 是管理员解绑这条关系的时刻,0 表示仍然绑定中。
	//
	// 解绑**不删除本行**,理由是历史佣金要保留:qy_commission_accrual 里那些
	// 已经产生的计佣行都指着这个 invitee_id,删掉快照会让它们在流水页上失去
	// 脱敏名与 invitee_ref,而账本行本身仍然在那里 —— 那是最糟的组合
	// (钱还在账上,但谁也说不清是谁挣的)。
	//
	// 真正让这条关系停止计佣的是主库 users.inviter_id 被清零;本列只是把
	// "曾经绑过、什么时候解的"这个事实留在扩展库里,供管理端反查。
	UnboundAt int64 `json:"unbound_at" gorm:"not null;default:0;index:idx_qy_ir_unbound"`

	// 刻意不在这里维护"累计基数/累计佣金"两个计数器:那需要每条消费事件
	// 多写一次库,而且一旦与 accrual 漂移就再也对不回去。列表页按
	// inviter_id 聚合 qy_commission_accrual 即可,数据永远自洽。

	// RiskFlags 是**自动风控**写的标记(目前只有 reciprocal_invite:互邀环路)。
	// 它由 ensureRelation 在建快照时算出,此后不该被任何人工动作覆盖 ——
	// 覆盖掉的话 AFF 关系页上那个徽标显示的就是人写的话,而"这条关系是系统
	// 自动判定为互刷的"这个事实再也看不到了。人工停止/恢复计佣的事由写
	// BlockReason,两列各管各的。
	RiskFlags string `json:"risk_flags" gorm:"type:varchar(255);not null;default:''"`
	// Blocked 由管理员设置,置位后该关系不再计佣。已发放的佣金不回收。
	Blocked bool `json:"blocked" gorm:"not null"`
	// BlockReason 是管理员最近一次停止/恢复计佣填的事由。
	//
	// 零值语义:空串 = 没有填过事由(或这条关系从未被人工停过),**不是**
	// "事由是空的"。它与 Blocked 是两件事 —— 恢复计佣时事由照样留下,
	// 因为"为什么恢复"与"为什么停"同样是事后要查的。
	BlockReason string `json:"block_reason" gorm:"type:varchar(255);not null;default:''"`

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
	Fiat      decimal.Decimal `json:"fiat" gorm:"type:decimal(18,6);not null;default:0.000000"`
	CreatedAt int64           `json:"created_at" gorm:"not null;default:0"`
}

func (FreezeRecord) TableName() string { return "qy_commission_freeze" }
