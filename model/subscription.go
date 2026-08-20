package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/samber/hot"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Subscription duration units
const (
	SubscriptionDurationYear   = "year"
	SubscriptionDurationMonth  = "month"
	SubscriptionDurationDay    = "day"
	SubscriptionDurationHour   = "hour"
	SubscriptionDurationCustom = "custom"
	// SubscriptionDurationPermanent 永久有效:算不出结束时间,end_time 存 0。
	//
	// 判据见 SubscriptionActiveEndTimeSQL —— 到期扫描本来就写着
	// `end_time > 0 AND end_time <= ?`,所以 0 天然不会被扫到,不需要为永久档
	// 新增任何到期分支。
	SubscriptionDurationPermanent = "permanent"
)

// 订阅状态。仓库里此前一直用字面量("active" / "refunded"),这里只为新增的
// 那一档定义常量 —— 不把存量字面量一起改成常量是刻意的:那会碰到十几处与本次
// 改动无关的代码,而 diff 越大越难看出"这次到底改了什么"。
// SubscriptionSourceOrder 是 CreateUserSubscriptionFromPlanTx 的 source 里
// **钱已经收了**的那一档:支付网关回调 CompleteSubscriptionOrder。
//
// 它是资金安全的判据,不是日志分类。这一档下任何一条业务规则返回 error 都会
// 让整个事务回滚 —— 订单永久停在 pending、订阅发不出、top_ups 一行都没有,
// 而本仓没有定时任务会关掉它,也没有管理端补单或退款接口(AdminBindSubscription
// 走同一个函数,补发撞同一堵墙),只能手工改库。
//
// 判据集中在 isPaidSubscriptionSource,与 qianye/modules/subscription/gate.go §四
// 的名额闸门同一条口径。
const SubscriptionSourceOrder = "order"

// isPaidSubscriptionSource 报告这条创建路径是不是"钱已经收了"。
//
// 只有网关回调一档:余额购买("balance")那条路是同一个事务里先扣的 quota,
// 回滚会把扣款一起回滚,拒绝是安全的;兑换码与管理员发放同理(没有外部收款)。
func isPaidSubscriptionSource(source string) bool {
	return source == SubscriptionSourceOrder
}

const (
	// SubscriptionStatusSuperseded 被后买的用户组商品顶替掉。
	//
	// 与 expired 分开:expired 是"时间到了",superseded 是"用户自己买了别的组,
	// 剩余时间作废"。客服要能回答「我上个月买的 VIP 去哪了」,而两者的答案不同。
	//
	// 不删行同理:那条订阅是用户真的花过钱的,删掉之后没有任何地方查得到它。
	SubscriptionStatusSuperseded = "superseded"
)

// Subscription quota reset period
const (
	SubscriptionResetNever   = "never"
	SubscriptionResetDaily   = "daily"
	SubscriptionResetWeekly  = "weekly"
	SubscriptionResetMonthly = "monthly"
	SubscriptionResetCustom  = "custom"
)

var (
	ErrSubscriptionOrderNotFound      = errors.New("subscription order not found")
	ErrSubscriptionOrderStatusInvalid = errors.New("subscription order status invalid")
)

const (
	subscriptionPlanCacheNamespace     = "new-api:subscription_plan:v1"
	subscriptionPlanInfoCacheNamespace = "new-api:subscription_plan_info:v1"
)

var (
	subscriptionPlanCacheOnce     sync.Once
	subscriptionPlanInfoCacheOnce sync.Once

	subscriptionPlanCache     *cachex.HybridCache[SubscriptionPlan]
	subscriptionPlanInfoCache *cachex.HybridCache[SubscriptionPlanInfo]
)

func subscriptionPlanCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_TTL", 300)
	if ttlSeconds <= 0 {
		ttlSeconds = 300
	}
	return time.Duration(ttlSeconds) * time.Second
}

func subscriptionPlanInfoCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_TTL", 120)
	if ttlSeconds <= 0 {
		ttlSeconds = 120
	}
	return time.Duration(ttlSeconds) * time.Second
}

func subscriptionPlanCacheCapacity() int {
	capacity := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_CAP", 5000)
	if capacity <= 0 {
		capacity = 5000
	}
	return capacity
}

func subscriptionPlanInfoCacheCapacity() int {
	capacity := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_CAP", 10000)
	if capacity <= 0 {
		capacity = 10000
	}
	return capacity
}

func getSubscriptionPlanCache() *cachex.HybridCache[SubscriptionPlan] {
	subscriptionPlanCacheOnce.Do(func() {
		ttl := subscriptionPlanCacheTTL()
		subscriptionPlanCache = cachex.NewHybridCache[SubscriptionPlan](cachex.HybridCacheConfig[SubscriptionPlan]{
			Namespace: cachex.Namespace(subscriptionPlanCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SubscriptionPlan]{},
			Memory: func() *hot.HotCache[string, SubscriptionPlan] {
				return hot.NewHotCache[string, SubscriptionPlan](hot.LRU, subscriptionPlanCacheCapacity()).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return subscriptionPlanCache
}

func getSubscriptionPlanInfoCache() *cachex.HybridCache[SubscriptionPlanInfo] {
	subscriptionPlanInfoCacheOnce.Do(func() {
		ttl := subscriptionPlanInfoCacheTTL()
		subscriptionPlanInfoCache = cachex.NewHybridCache[SubscriptionPlanInfo](cachex.HybridCacheConfig[SubscriptionPlanInfo]{
			Namespace: cachex.Namespace(subscriptionPlanInfoCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SubscriptionPlanInfo]{},
			Memory: func() *hot.HotCache[string, SubscriptionPlanInfo] {
				return hot.NewHotCache[string, SubscriptionPlanInfo](hot.LRU, subscriptionPlanInfoCacheCapacity()).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return subscriptionPlanInfoCache
}

func subscriptionPlanCacheKey(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

func InvalidateSubscriptionPlanCache(planId int) {
	if planId <= 0 {
		return
	}
	cache := getSubscriptionPlanCache()
	_, _ = cache.DeleteMany([]string{subscriptionPlanCacheKey(planId)})
	infoCache := getSubscriptionPlanInfoCache()
	_ = infoCache.Purge()
}

// Subscription plan
type SubscriptionPlan struct {
	Id int `json:"id"`

	Title    string `json:"title" gorm:"type:varchar(128);not null"`
	Subtitle string `json:"subtitle" gorm:"type:varchar(255);default:''"`

	// Display money amount (follow existing code style: float64 for money)
	PriceAmount float64 `json:"price_amount" gorm:"type:decimal(10,6);not null;default:0"`
	Currency    string  `json:"currency" gorm:"type:varchar(8);not null;default:'USD'"`

	DurationUnit  string `json:"duration_unit" gorm:"type:varchar(16);not null;default:'month'"`
	DurationValue int    `json:"duration_value" gorm:"type:int;not null;default:1"`
	CustomSeconds int64  `json:"custom_seconds" gorm:"type:bigint;not null;default:0"`

	Enabled   bool `json:"enabled" gorm:"default:true"`
	SortOrder int  `json:"sort_order" gorm:"type:int;default:0"`

	// SaleStartAt / SaleEndAt 是**发售时间窗**:这个套餐在哪一段时间里可以被买。
	//
	// ═══════════ 0 是「不限制」,不是 1970 ═══════════
	//
	// 判据不是约定俗成,而是这两张表上既有的口径:
	//
	//	user_subscriptions.end_time = 0        永久有效(SubscriptionActiveEndTimeSQL)
	//	user_subscriptions.next_reset_time = 0 不重置(calcNextResetTime 的 never 档)
	//	user_subscriptions.last_reset_time = 0 从未重置过
	//
	// 全仓没有任何一处把这几列的 0 当成"1970-01-01"去比较 —— 到期扫描写的是
	// `end_time > 0 AND end_time <= ?`,前半句就是为了把 0 摘出去。所以在这两列上
	// 继续用 0 表示"不限"是与既有语义一致的,而不是又发明一个新约定。
	//
	// 但**方向不对称**,这是唯一需要小心的地方:
	//
	//	SaleStartAt = 0 天然安全 —— `now >= 0` 恒真,不写特判也是"随时可买"。
	//	SaleEndAt   = 0 天然危险 —— `now < 0` 恒假,不写特判就变成"所有套餐一律停售"。
	//
	// 所以 PlanSaleWindowError 里那句 `endAt != PlanSaleWindowUnlimited &&` 是
	// 必需的,和 SubscriptionActiveEndTimeSQL 里的 `end_time = 0 OR` 是同一件事。
	//
	// ═══════════ 窗口是左闭右开 [start, end) ═══════════
	//
	// 与 SubscriptionActiveEndTimeSQL 的 `end_time > ?` 同口径:到了停售那一秒
	// 就已经停售。于是 start == end 是一个空窗口(永远买不了),校验里直接拒绝。
	//
	// ═══════════ 与 Enabled 是「与」的关系 ═══════════
	//
	// 可购买 = Enabled && 在窗口内。Enabled 是运营手动的上下架开关,时间窗是
	// 到点自动上下架 —— 两者任何一个说"不",就是不。或的关系说不通:那意味着
	// 一个被手动下架的套餐会在开售时间到达时自己重新上架。
	SaleStartAt int64 `json:"sale_start_at" gorm:"type:bigint;not null;default:0"`
	SaleEndAt   int64 `json:"sale_end_at" gorm:"type:bigint;not null;default:0"`

	AllowBalancePay *bool `json:"allow_balance_pay"`

	// Allow falling back to wallet balance after subscription quota is exhausted (empty = true)
	AllowWalletOverflow *bool `json:"allow_wallet_overflow"`

	StripePriceId         string `json:"stripe_price_id" gorm:"type:varchar(128);default:''"`
	CreemProductId        string `json:"creem_product_id" gorm:"type:varchar(128);default:''"`
	WaffoPancakeProductId string `json:"waffo_pancake_product_id" gorm:"type:varchar(128);default:''"`

	// Max purchases per user (0 = unlimited)
	MaxPurchasePerUser int `json:"max_purchase_per_user" gorm:"type:int;default:0"`

	// Upgrade user group after purchase (empty = no change)
	UpgradeGroup string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`

	// Downgrade user group on expiry (empty = revert to the group held before purchase)
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`

	// Total quota (amount in quota units, 0 = unlimited)
	TotalAmount int64 `json:"total_amount" gorm:"type:bigint;not null;default:0"`

	// NoQuota 把这个套餐变成**纯商品**:不带任何订阅余额,只负责改用户组。
	//
	// ═══════ 为什么不能用 TotalAmount = 0 表达「没有余额」═══════
	//
	// 因为 0 的语义是**不限额度**:预扣那一段写着 `if sub.AmountTotal > 0` 才
	// 检查余额,0 直接跳过检查。也就是说拿 0 当"没有余额"来卖用户组商品,
	// 换来的是给每个买家一份**无限订阅余额**。这是资损口子,不是显示问题。
	//
	// 两者是真正不同的三态,必须用独立字段区分:
	//
	//	NoQuota=true            纯商品,压根不参与出资
	//	NoQuota=false, Total=0  不限额度
	//	NoQuota=false, Total>0  有限额度
	//
	// 平凡 bool(默认 false)是刻意的:存量套餐全都是带余额的,而
	// AGENTS.md 禁止给 bool 加 gorm default —— MySQL 与 PostgreSQL 对布尔默认值
	// 的归一化不同,会让 AutoMigrate 每次重启都发一条 ALTER TABLE。
	NoQuota bool `json:"no_quota" gorm:"not null"`

	// Quota reset period for plan
	QuotaResetPeriod        string `json:"quota_reset_period" gorm:"type:varchar(16);default:'never'"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds" gorm:"type:bigint;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

// SubscriptionActiveEndTimeSQL 是「这条订阅此刻仍然有效」在 end_time 上的**唯一判据**。
//
// ═════════════════ end_time = 0 表示永久有效 ═════════════════
//
// 永久档(SubscriptionDurationPermanent)算不出结束时间,存 0。选 0 而不是"存一个
// 很大的数"是因为到期扫描本来就写着 `end_time > 0 AND end_time <= ?` —— 0 天然
// 不会被扫到,不需要为永久档新增任何到期分支;而"很大的数"只是把问题推到那个数
// 真的到来的那一天,且在此之前一直显示成一个假的到期日。
//
// ═════════════════ 为什么必须只有一处定义 ═════════════════
//
// 「仍然有效」这个判据在本仓有 12 个调用点(升级订阅探测、可用额度、套餐解锁的
// 模型分组、名额闸门、持有人清单…)。手写 `end_time > ?` 的那些会把永久订阅
// 判成已过期,而每一处的表现都不一样:有的是额度用不了、有的是解锁的模型分组
// 突然消失、有的是名额没被占住。这类缺陷不会一起暴露,只会一个一个地被发现。
//
// subscription_active_predicate_test.go 扫源码钉死:除到期扫描外,任何地方都不许
// 再手写 end_time 的有效性判据。
const SubscriptionActiveEndTimeSQL = "(end_time = 0 OR end_time > ?)"

// PlanSaleWindowUnlimited 是发售/停售两列上「不限制」的取值。
//
// 具名而不是散落的裸 0:这两列的 0 与 price_amount 的 0、total_amount 的 0
// 语义完全不同(那两个 0 分别是"免费"与"不限额度"),读代码的人不该靠上下文猜。
const PlanSaleWindowUnlimited int64 = 0

// PlanSaleWindowMaxUnix 是发售/停售时间的上界:9999-12-31T23:59:59Z。
//
// 存在的理由不是"谁会把套餐定在一万年后开售",而是别让一个手滑粘进来的
// 毫秒时间戳(13 位,约 1.7e12,合公元 55000 年)变成一个看起来像配好了、
// 实际永远不开售的套餐 —— 那种错误在管理端页面上完全看不出来,因为
// JavaScript 的 `new Date(1.7e12 * 1000)` 直接是 Invalid Date,渲染成空白。
const PlanSaleWindowMaxUnix int64 = 253402300799

var (
	// ErrPlanNotOnSaleYet / ErrPlanSaleEnded 是两句**不同**的话,不能合并成
	// 一句"该套餐当前不可购买":前者用户再等等就买得到,后者永远买不到了。
	// 客服要能只看报错就回答"还要等多久"还是"别等了"。
	ErrPlanNotOnSaleYet = errors.New("该套餐尚未开售,请等待开售时间")
	// 「续费」两个字必须写进这句话里。停售之后老用户来续,得到的报错如果只说
	// "已停售",他的合理理解是"我买过的那份也没了" —— 而事实恰恰相反。
	ErrPlanSaleEnded = errors.New("该套餐已停售,不再接受新购买与续费;已购买的订阅不受影响,有效期照常")
)

// ValidatePlanSaleWindow 校验管理端提交的发售时间窗。
//
// 两条:不许为负、不许超过上界;以及**两端都设了的时候,停售必须晚于发售**。
// 只设一端是合法的(只设发售 = 到点开卖、永不停售;只设停售 = 立刻开卖、到点停),
// 所以 endAt <= startAt 这条判断必须先排除掉任意一端为 0 的情况 —— 否则
// "只设停售时间"会被算成 `endAt <= 0` 而被误拒。
func ValidatePlanSaleWindow(startAt, endAt int64) error {
	for _, v := range []int64{startAt, endAt} {
		if v < 0 {
			return errors.New("发售/停售时间不能为负数")
		}
		if v > PlanSaleWindowMaxUnix {
			return errors.New("发售/停售时间超出可用范围(应为秒级 Unix 时间戳)")
		}
	}
	if startAt != PlanSaleWindowUnlimited && endAt != PlanSaleWindowUnlimited && endAt <= startAt {
		// 相等也拒:窗口是左闭右开,[X, X) 是一个永远买不到的空窗口,
		// 而管理端上它看起来像是"这一刻开售、这一刻停售",完全不像配错了。
		return errors.New("停售时间必须晚于发售时间")
	}
	return nil
}

// PlanSaleWindowError 是「此刻能不能**新买**这个套餐」在时间窗上的唯一判据。
//
// ═══════════════ 它挡的是新购买,不是已购订阅 ═══════════════
//
// 本函数只被购买入口调用(余额购买、四个支付网关的下单、兑换码、购买预览)。
// 它**不出现在**任何一条读取已有订阅的路径上 —— 停售不作废任何人已经买到手的
// 订阅,那条订阅照常有效到它自己的 end_time。这一点在测试里单独钉住
// (TestSaleWindowDoesNotTouchPurchasedSubscriptions)。
//
// ═══════════════ 「续费」算不算新购买:算 ═══════════════
//
// 本仓压根没有独立的续费接口。用户所谓的"续费"就是**再买一次同一个套餐**,
// 走的是与首次购买逐字相同的那几条路径。唯一形似续费的是
// applyUserGroupPurchaseRulesTx 的同组延期分支,而那条分支跑在
// CreateUserSubscriptionFromPlanTx 内部 —— 那时余额已经扣了、订单已经付了,
// 在那里拒绝等于收了钱不给货(与 gate.go §四 同一个形状)。
//
// 所以判断只能落在付款之前,而付款之前分不出"这是续费还是首购"。既然只能二选一,
// 选"算新购买":停售的语义是把商品下架,如果续费不受限,一个月付的套餐会被
// 老用户无限期续下去,运营永远退不掉它 —— 那时"停售"这个功能等于不存在,
// 只能回头去关 enabled(而那正是时间窗要替代的手工动作)。
//
// 界面文案必须写明这一条,不能让用户自己猜:见 qy_plan_sale_ended_hint。
//
// ═══════════════ 已付款的回调不走这里 ═══════════════
//
// CompleteSubscriptionOrder(支付回调)刻意不调用本函数,与它同样不理会
// plan.Enabled 是同一个判断:那一刻用户的钱已经付掉了,拒绝只会让订单永久卡在
// pending,而没有任何退款路径。
//
// ─────────────── 残余风险的真实宽度:不是"一次支付的时长" ───────────────
//
// 这里曾经写着"窗口宽度等于一次支付的时长"。那是错的:pending 订单在本仓侧
// 没有任何上界(epay 的下单参数里没有时间戳,签名也就无从校验新鲜度;演示站
// 主库里最老的 pending 单挂了近三个月),所以「在售时下单 → 停售之后再付款」
// 这条路的窗口宽度取决于第三方网关还认不认那张付款单,与本仓无关。
//
// 于是这条残余风险按两件事分别处理:
//
//	卖出去的到底是什么  由 SubscriptionOrder.PlanSnapshot 钉死 —— 陈旧订单成交时
//	                    发的是**下单那一刻**的套餐,运营就地改价/加量不会顺着
//	                    这些订单流出去。这一半是资金问题,已经堵住。
//	运营视图            ExpireStalePendingSubscriptionOrders 把超龄 pending 打成
//	                    expired,让"还有多少单可能成交"变成一个看得见的数。
//	                    它**不**拒绝发货(见 CompleteSubscriptionOrder),
//	                    因为收了钱不发货比多卖一单严重得多。
//
// 剩下真正没堵的只有"多卖一单"本身(限量套餐的名额会因此溢出),那是业务性的,
// 与名额闸门对 source=="order" 的处理同一条口径。
//
// 管理员绑定(AdminBindSubscription)同样不走这里,与 gate.go 对停用套餐的处理
// 同口径:管理员手工发放是上游允许的动作,时间窗是面向买家的闸门。
func PlanSaleWindowError(plan *SubscriptionPlan, now int64) error {
	if plan == nil {
		return nil
	}
	if plan.SaleStartAt != PlanSaleWindowUnlimited && now < plan.SaleStartAt {
		return ErrPlanNotOnSaleYet
	}
	// `!= PlanSaleWindowUnlimited` 不能省:0 表示永不停售,而 `now < 0` 恒假,
	// 省掉它就是把全站每一个没配停售时间的套餐一起下架。
	if plan.SaleEndAt != PlanSaleWindowUnlimited && now >= plan.SaleEndAt {
		return ErrPlanSaleEnded
	}
	return nil
}

func (p *SubscriptionPlan) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	p.CreatedAt = now
	p.UpdatedAt = now
	return nil
}

func (p *SubscriptionPlan) BeforeUpdate(tx *gorm.DB) error {
	p.UpdatedAt = common.GetTimestamp()
	return nil
}

// PureProductAmountTotal 是纯商品落库时的额度:**1 个 quota 单位**,不是 0。
//
// ═══════════ 为什么不是 0 ═══════════
//
// 0 的语义是**不限额度** —— 预扣那一句 `if sub.AmountTotal > 0` 会直接跳过余额
// 检查。纯商品一旦以 0 落库,任何漏掉 NoQuota 判断的路径都会把它当成一份
// 永远花不完的余额。1 让这条订阅在**数据层面**就是"几乎没有钱":
// remain(1) < 任何一次真实请求的预扣额,于是必然被跳过、落到钱包。
//
// 项目方原话:「没有额度的你就设定一个很小的数值 0.000001 这种」。
// 单位是 quota 而不是美元(int64),所以能存的最小正值就是 1 —— 按本站
// 500000 quota = 1 美元换算,它是 0.000002 美元,比那个数还小。
//
// ═══════════ 它与 NoQuota 是两道闸,不是二选一 ═══════════
//
//	NoQuota      运营面的概念,也是出资查询里那条 SQL 过滤(纯商品压根不被选中)
//	本常量       数据面的兜底,万一将来有路径绕过了那条过滤,它拿到的也只有 1
//
// 只留前者:漏一处过滤就退化成无限余额。只留后者:运营得知道"1"是个魔法数,
// 而且零成本请求(倍率 0 的模型)真的会把这 1 个单位吃掉一次。两道都留最省心。
const PureProductAmountTotal int64 = 1

// planAmountTotal 给出这个套餐落库时应该写入的订阅额度。
func planAmountTotal(plan *SubscriptionPlan) int64 {
	if plan.NoQuota {
		return PureProductAmountTotal
	}
	return plan.TotalAmount
}

func (p *SubscriptionPlan) NormalizeDefaults() {
	if p.AllowBalancePay == nil {
		p.AllowBalancePay = common.GetPointer(true)
	}
	if p.AllowWalletOverflow == nil {
		p.AllowWalletOverflow = common.GetPointer(true)
	}
}

// Subscription order (payment -> webhook -> create UserSubscription)
type SubscriptionOrder struct {
	Id     int     `json:"id"`
	UserId int     `json:"user_id" gorm:"index"`
	PlanId int     `json:"plan_id" gorm:"index"`
	Money  float64 `json:"money"`

	TradeNo         string `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	Status          string `json:"status"`
	CreateTime      int64  `json:"create_time"`
	CompleteTime    int64  `json:"complete_time"`

	ProviderPayload string `json:"provider_payload" gorm:"type:text"`

	// PlanSnapshot 是**下单那一刻**的套餐整行 JSON。
	//
	// ═══════════ 为什么订单必须自带套餐 ═══════════
	//
	// 支付回调按 order.PlanId **现读**套餐,而下单与付款之间隔着用户的付款时长
	// (最短几十秒,最长没有上界 —— pending 订单不会自己消失)。这段时间里运营
	// 就地编辑同一个 plan_id 的价格/额度/时长/升级组是完全合法的运营动作,
	// 于是订单只快照了 Money、货却按新内容发:实测「下单时 1 元 / 1000 额度」
	// 的订单在套餐被改成「199 元 / 9,000,000 额度 / 12 个月 / vip」之后回调,
	// 用户花 1 元拿到了 9,000,000 额度。反方向(降价或减量)则是用户按旧的
	// 高价付款、拿到缩水的货。
	//
	// 快照让「付了多少钱」与「买到什么」出自同一时刻 —— 订单是一份合同,
	// 合同的内容不该在对方付款的路上被改。
	//
	// ═══════════ 空串是什么 ═══════════
	//
	// 空串 = 本列上线之前创建的存量订单(以及余额购买这种下单即成交、
	// 中间没有任何时间差的路径)。此时回落到按 plan_id 现读,即改动前的行为。
	// 不给它一个"默认套餐"是刻意的:读不出内容的订单只能问库,猜出来的合同
	// 比现读更危险。
	PlanSnapshot string `json:"-" gorm:"type:text"`
}

// SubscriptionPlanSnapshot 把下单那一刻的套餐序列化成订单要带走的那份合同。
//
// 序列化失败时返回空串而不是报错:快照缺失只会退回"按 plan_id 现读"这条
// 改动前就在跑的路径,而为了一次 Marshal 失败拒绝创建订单,等于把一个
// 可降级的问题升级成用户买不了东西。
func SubscriptionPlanSnapshot(plan *SubscriptionPlan) string {
	if plan == nil || plan.Id == 0 {
		return ""
	}
	buf, err := common.Marshal(plan)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to snapshot subscription plan %d into order: %v", plan.Id, err))
		return ""
	}
	return string(buf)
}

// subscriptionOrderPlan 给出这张订单**应当按哪一份套餐**发货。
//
// 优先用下单时的快照;快照缺失/解析不出/plan_id 对不上时回落到现读。
// plan_id 对不上必须回落而不是报错:那说明这一行的快照写错了对象,
// 按它发货比按现读更离谱。
func subscriptionOrderPlan(order *SubscriptionOrder) (*SubscriptionPlan, error) {
	if order == nil {
		return nil, errors.New("invalid subscription order")
	}
	if strings.TrimSpace(order.PlanSnapshot) != "" {
		var snapshot SubscriptionPlan
		if err := common.UnmarshalJsonStr(order.PlanSnapshot, &snapshot); err != nil || snapshot.Id != order.PlanId {
			common.SysError(fmt.Sprintf(
				"subscription order %s carries an unusable plan snapshot (plan_id=%d, err=%v); falling back to the live plan",
				order.TradeNo, order.PlanId, err))
		} else {
			return &snapshot, nil
		}
	}
	return GetSubscriptionPlanById(order.PlanId)
}

func (o *SubscriptionOrder) Insert() error {
	if o.CreateTime == 0 {
		o.CreateTime = common.GetTimestamp()
	}
	return DB.Create(o).Error
}

func (o *SubscriptionOrder) Update() error {
	return DB.Save(o).Error
}

func GetSubscriptionOrderByTradeNo(tradeNo string) *SubscriptionOrder {
	if tradeNo == "" {
		return nil
	}
	var order SubscriptionOrder
	if err := DB.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		return nil
	}
	return &order
}

// User subscription instance
type UserSubscription struct {
	Id     int `json:"id"`
	UserId int `json:"user_id" gorm:"index;index:idx_user_sub_active,priority:1"`
	PlanId int `json:"plan_id" gorm:"index"`

	AmountTotal int64 `json:"amount_total" gorm:"type:bigint;not null;default:0"`
	AmountUsed  int64 `json:"amount_used" gorm:"type:bigint;not null;default:0"`

	// NoQuota 是购买那一刻从套餐拍下的快照:这条订阅是不是纯商品(不带余额)。
	//
	// 快照而不是每次回查套餐:运营事后把套餐从"纯商品"改成"带余额",不该让
	// 已经卖出去的那批订阅凭空长出一份余额;反过来同理。这与本表其它几个
	// 快照字段(upgrade_group / downgrade_group / allow_wallet_overflow)同一口径。
	NoQuota bool `json:"no_quota" gorm:"not null"`

	StartTime int64  `json:"start_time" gorm:"bigint"`
	EndTime   int64  `json:"end_time" gorm:"bigint;index;index:idx_user_sub_active,priority:3"`
	Status    string `json:"status" gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"` // active/expired/cancelled

	Source string `json:"source" gorm:"type:varchar(32);default:'order'"` // order/admin

	LastResetTime int64 `json:"last_reset_time" gorm:"type:bigint;default:0"`
	NextResetTime int64 `json:"next_reset_time" gorm:"type:bigint;default:0;index"`

	UpgradeGroup  string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`
	PrevUserGroup string `json:"prev_user_group" gorm:"type:varchar(64);default:''"`

	// Downgrade target group on expiry (snapshot from plan; empty = revert to PrevUserGroup)
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`

	// Whether wallet fallback is allowed after this subscription's quota is exhausted (snapshot from plan)
	AllowWalletOverflow bool `json:"allow_wallet_overflow"`

	// WriteOffCount 是本重置周期内平台已经为这张套餐核销过几次结算差额。
	//
	// 核销发生在「套餐扣到 amount_total 为止、剩下的差额又不许由钱包补收」时。
	// service/funding_source.go 曾把「至多核销一次」当成结构性事实写进注释:
	// 核销之后 amount_used == amount_total,pickFundingSubscription 的
	// `remain <= 0 → continue` 会让这张套餐再也拿不到预扣。这句话只在**串行**下
	// 成立 —— N 路并发各自拿到一份预扣、各自超支、各自核销,核销笔数 = 在飞路数,
	// 总额随并发线性放大(实测 10 路把一张面值 10000 的套餐核销掉 40000)。
	//
	// 于是把它从"假设"变成"闩":每个重置周期只发一次核销名额,由
	// ClaimSubscriptionWriteOff 在行锁里发放,随 amount_used 一起归零。
	WriteOffCount int `json:"write_off_count" gorm:"not null;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func (s *UserSubscription) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	s.CreatedAt = now
	s.UpdatedAt = now
	return nil
}

func (s *UserSubscription) BeforeUpdate(tx *gorm.DB) error {
	s.UpdatedAt = common.GetTimestamp()
	return nil
}

type SubscriptionSummary struct {
	Subscription *UserSubscription `json:"subscription"`
}

type SubscriptionResetResult struct {
	PlanId           int    `json:"plan_id"`
	MatchedCount     int    `json:"matched_count"`
	ResetCount       int    `json:"reset_count"`
	UserCount        int    `json:"user_count"`
	AdvanceResetTime bool   `json:"advance_reset_time"`
	PlanTitle        string `json:"-"`
	AffectedUserIds  []int  `json:"-"`
}

func calcPlanEndTime(start time.Time, plan *SubscriptionPlan) (int64, error) {
	if plan == nil {
		return 0, errors.New("plan is nil")
	}
	// 永久档要排在 duration_value 校验之前:它压根不用填时长,
	// 排在后面的话运营必须为一个"永久"的商品编一个大于 0 的月数。
	if plan.DurationUnit == SubscriptionDurationPermanent {
		return 0, nil
	}
	if plan.DurationValue <= 0 && plan.DurationUnit != SubscriptionDurationCustom {
		return 0, errors.New("duration_value must be > 0")
	}
	switch plan.DurationUnit {
	case SubscriptionDurationYear:
		return start.AddDate(plan.DurationValue, 0, 0).Unix(), nil
	case SubscriptionDurationMonth:
		return start.AddDate(0, plan.DurationValue, 0).Unix(), nil
	case SubscriptionDurationDay:
		return start.Add(time.Duration(plan.DurationValue) * 24 * time.Hour).Unix(), nil
	case SubscriptionDurationHour:
		return start.Add(time.Duration(plan.DurationValue) * time.Hour).Unix(), nil
	case SubscriptionDurationCustom:
		if plan.CustomSeconds <= 0 {
			return 0, errors.New("custom_seconds must be > 0")
		}
		return start.Add(time.Duration(plan.CustomSeconds) * time.Second).Unix(), nil
	default:
		return 0, fmt.Errorf("invalid duration_unit: %s", plan.DurationUnit)
	}
}

func NormalizeResetPeriod(period string) string {
	switch strings.TrimSpace(period) {
	case SubscriptionResetDaily, SubscriptionResetWeekly, SubscriptionResetMonthly, SubscriptionResetCustom:
		return strings.TrimSpace(period)
	default:
		return SubscriptionResetNever
	}
}

func calcNextResetTime(base time.Time, plan *SubscriptionPlan, endUnix int64) int64 {
	if plan == nil {
		return 0
	}
	period := NormalizeResetPeriod(plan.QuotaResetPeriod)
	if period == SubscriptionResetNever {
		return 0
	}
	var next time.Time
	switch period {
	case SubscriptionResetDaily:
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, 1)
	case SubscriptionResetWeekly:
		// Align to next Monday 00:00
		weekday := int(base.Weekday()) // Sunday=0
		// Convert to Monday=1..Sunday=7
		if weekday == 0 {
			weekday = 7
		}
		daysUntil := 8 - weekday
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, daysUntil)
	case SubscriptionResetMonthly:
		// Align to first day of next month 00:00
		next = time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, base.Location()).
			AddDate(0, 1, 0)
	case SubscriptionResetCustom:
		if plan.QuotaResetCustomSeconds <= 0 {
			return 0
		}
		next = base.Add(time.Duration(plan.QuotaResetCustomSeconds) * time.Second)
	default:
		return 0
	}
	if endUnix > 0 && next.Unix() > endUnix {
		return 0
	}
	return next.Unix()
}

func GetSubscriptionPlanById(id int) (*SubscriptionPlan, error) {
	return getSubscriptionPlanByIdTx(nil, id)
}

func getSubscriptionPlanByIdTx(tx *gorm.DB, id int) (*SubscriptionPlan, error) {
	if id <= 0 {
		return nil, errors.New("invalid plan id")
	}
	key := subscriptionPlanCacheKey(id)
	if key != "" {
		if cached, found, err := getSubscriptionPlanCache().Get(key); err == nil && found {
			cached.NormalizeDefaults()
			return &cached, nil
		}
	}
	var plan SubscriptionPlan
	query := DB
	if tx != nil {
		query = tx
	}
	if err := query.Where("id = ?", id).First(&plan).Error; err != nil {
		return nil, err
	}
	plan.NormalizeDefaults()
	_ = getSubscriptionPlanCache().SetWithTTL(key, plan, subscriptionPlanCacheTTL())
	return &plan, nil
}

func CountUserSubscriptionsByPlan(userId int, planId int) (int64, error) {
	if userId <= 0 || planId <= 0 {
		return 0, errors.New("invalid userId or planId")
	}
	var count int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", userId, planId).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func getUserGroupByIdTx(tx *gorm.DB, userId int) (string, error) {
	if userId <= 0 {
		return "", errors.New("invalid userId")
	}
	if tx == nil {
		tx = DB
	}
	var group string
	if err := lockForUpdate(tx).Model(&User{}).Where("id = ?", userId).Select(commonGroupCol).Find(&group).Error; err != nil {
		return "", err
	}
	return group, nil
}

// userGroupBeforeUpgradeChainTx 回答:这个人在「当前这条升组链」开始之前属于哪个分组。
//
// ═══════════════════════ 为什么不能直接用"买之前那一刻的分组" ═══════════════════════
//
// prev_user_group 是到期(或被管理员作废)时把人放回去的那个目标。早先它记的是
// 「创建这条订阅的那一刻用户在哪个组」,于是名下已经有一条会改组的订阅时,它记下的
// 是**上一条订阅刚刚给他的那个付费组**。两条真实路径因此把付费分组永久送了出去:
//
//   - 跨组顶替:default → 买 VIP(prev=default)→ 买 GOLD。GOLD 那一行记的 prev 是
//     VIP —— 而 VIP 那条订阅在同一个事务里已经被作废、剩余时间不折算不退款。
//     GOLD 到期后人被放回 VIP,此刻他名下一条会改组的订阅都没有了,再没有任何
//     任务会碰他的分组。先买贵的再买便宜的,等便宜的到期就白拿贵的那一档。
//
//   - 续费:default → 买"升组+送额度"的 VIP(prev=default)→ 同一个套餐再买一次。
//     第二次购买时用户已经在 VIP 里,老逻辑于是把 prev 留空;而到期扫描取的是
//     end_time 最大的那条 expired 行,正好是这条空 prev 的行,判到 prev=="" 就
//     直接放弃回退 —— 第一行上明明记着 default 却从不被读。所有订阅到期之后
//     用户永久留在 VIP。续费是最常见的动作,而 downgrade_group 留空是管理端的默认档。
//
// ═══════════════════════ 判据 ═══════════════════════
//
// 只看**还站着的**那条链(active 与 superseded):它们要么正在给用户撑着某个组,
// 要么是刚刚在这个事务里被顶掉的前一档。expired 的行刻意不看 —— 那条链已经
// 走完、人已经被放回去了,当前分组本身就是新的起点(比如上一条订阅带着显式的
// downgrade_group 把人放到了 silver,他现在就是 silver,不该再回 default)。
//
// 取 id 最小的那一条(链根)。链上每一环从此都携带同一个根,所以到期时读哪一环
// 都得到同一个答案。
//
// 返回 fallback(调用方传当前分组)表示"没有正在站着的链",此时买之前那一刻
// 就是链的起点 —— 与改动前完全一致。
// 判据只看 prev_user_group,**不要求** upgrade_group 仍然非空:跨组顶替时
// 带额度的那条订阅是被"摘掉改组身份"(upgrade_group 清空、余额与有效期保留)
// 而不是被作废的,它照样是这条链的根。prev_user_group 只会由
// CreateUserSubscriptionFromPlanTx 在 upgrade_group 非空时写入,所以
// 「prev 非空」本身已经蕴含了"这一行曾经负责改组";而管理员手工改组那条路
// (DetachUserGroupSubscriptionsTx)会把 prev 一并清空,链根随之作废 —— 那正是
// 「管理员说了算」的意思。
func userGroupBeforeUpgradeChainTx(tx *gorm.DB, userId int, fallback string) string {
	var rows []UserSubscription
	err := tx.Where("user_id = ? AND prev_user_group <> '' AND status IN (?, ?)",
		userId, "active", SubscriptionStatusSuperseded).
		Order("id asc").Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return fallback
	}
	return strings.TrimSpace(rows[0].PrevUserGroup)
}

// legacyPrevUserGroupTx 是给**升级之前**就落库的那些行准备的兜底。
//
// userGroupBeforeUpgradeChainTx 只能让今后新建的行携带正确的链根;库里已经存在的
// 行(顶替链里记着付费组的、续费那条 prev 为空的)不会被回填 —— 那是一张
// 快照表,事后改写它等于篡改"当时记了什么"这个事实。所以回退这一侧必须能
// 自己把链走回去:选中的那一行 prev 为空时,回落到该用户所有已经不再活跃的
// 会改组订阅里 id 最小、prev 非空的那一条。
//
// 只在没有显式 downgrade_group、且用户当前确实还坐在那条订阅给的组里时才会走到
// 这里(两个判据都在调用方),所以它至多把人放回一个更早的免费档,不会把人
// 提到任何他没买过的组。
func legacyPrevUserGroupTx(tx *gorm.DB, userId int) string {
	var rows []UserSubscription
	err := tx.Where("user_id = ? AND upgrade_group <> '' AND prev_user_group <> '' AND status <> ?",
		userId, "active").
		Order("id asc").Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return ""
	}
	return strings.TrimSpace(rows[0].PrevUserGroup)
}

// alignGroupToSurvivingUpgrade 在一条升组订阅失效之后，把用户组对齐到**幸存的**
// 那条升组订阅所给的分组。
//
// 原先两处（到期扫描 ExpireDueSubscriptions、管理端作废
// downgradeUserGroupForSubscriptionTx）都是「名下还有别的活跃升组订阅就
// `return nil` 保持当前分组」—— 判据问的是「还有没有别人」，而不是「剩下的那位
// 给的是哪个组」。当前分组恰恰是刚刚失效的那条给的，于是：
//
//	先买便宜档的长周期（qy-cs-g，2 小时）→ 再买贵档的最短周期（0.5 倍率，70 秒）
//	→ 70 秒后贵档到期 → 用户原地停在贵档，把整整 2 小时按五折用完
//
// 更糟的是它不是暂时的：等便宜那条也到期时，取 end_time 最大的那条 expired 行
// 走 `currentGroup != upgradeGroup` 分支直接放弃回退，此后**再没有任何任务会碰
// 他的分组**，永久停在高档位。实测两行全部 expired 之后 users.group 仍是贵档。
//
// 触发只需用户自己控制的两个变量（购买顺序 + 周期长短），不需要管理员、不需要
// 竞态、不需要越权接口；前提只是站点配了两个及以上「带额度 + 改用户组」的套餐
// （applyUserGroupPurchaseRulesTx 对 !NoQuota 直接放行，跨组顶替不生效，
// 所以两条不同目标组的订阅可以合法并存）。
//
// 返回新的分组名；空串表示不需要改。
func alignGroupToSurvivingUpgrade(tx *gorm.DB, userId int, currentGroup string, surviving *UserSubscription) (string, error) {
	target := strings.TrimSpace(surviving.UpgradeGroup)
	if target == "" || target == currentGroup {
		return "", nil
	}
	if err := tx.Model(&User{}).Where("id = ?", userId).
		Update("group", target).Error; err != nil {
		return "", err
	}
	return target, nil
}

func downgradeUserGroupForSubscriptionTx(tx *gorm.DB, sub *UserSubscription, now int64) (string, error) {
	if tx == nil || sub == nil {
		return "", errors.New("invalid downgrade args")
	}
	downgradeGroup := strings.TrimSpace(sub.DowngradeGroup)
	upgradeGroup := strings.TrimSpace(sub.UpgradeGroup)
	// Nothing to do if neither an explicit downgrade target nor an upgrade snapshot exists.
	if downgradeGroup == "" && upgradeGroup == "" {
		return "", nil
	}
	currentGroup, err := getUserGroupByIdTx(tx, sub.UserId)
	if err != nil {
		return "", err
	}
	// 名下还有别的活跃升组订阅时,分组要对齐到**那一条**给的组,而不是原样保留
	// 当前组 —— 当前组正是刚刚被作废的这条给的。见 alignGroupToSurvivingUpgrade。
	var activeSub UserSubscription
	activeQuery := tx.Where("user_id = ? AND status = ? AND "+SubscriptionActiveEndTimeSQL+" AND id <> ? AND upgrade_group <> ''",
		sub.UserId, "active", now, sub.Id).
		Order("end_time desc, id desc").
		Limit(1).
		Find(&activeSub)
	if activeQuery.Error == nil && activeQuery.RowsAffected > 0 {
		return alignGroupToSurvivingUpgrade(tx, sub.UserId, currentGroup, &activeSub)
	}
	// Determine the downgrade target: an explicit downgrade group takes precedence,
	// otherwise revert to the group held before purchase (legacy behavior).
	target := downgradeGroup
	if target == "" {
		// Legacy behavior: only revert when the subscription actually elevated the user.
		if currentGroup != upgradeGroup {
			return "", nil
		}
		target = strings.TrimSpace(sub.PrevUserGroup)
		if target == "" {
			// 升级之前落的行:prev 为空(或记着已经被顶掉的付费组)。走回链根,
			// 否则这个人就被永久留在一个付费分组里,而且没有任何任务会再碰他。
			target = legacyPrevUserGroupTx(tx, sub.UserId)
		}
	}
	if target == "" || target == currentGroup {
		return "", nil
	}
	if err := tx.Model(&User{}).Where("id = ?", sub.UserId).
		Update("group", target).Error; err != nil {
		return "", err
	}
	return target, nil
}

// applyUserGroupPurchaseRulesTx 执行「用户组商品」的三条购买规则(项目方 2026-08-14 拍板)。
//
// 只对 upgrade_group 非空的套餐生效 —— 普通套餐一个字节都不受影响,
// 这是本函数唯一的启用条件,也是它可以被安全加进既有购买路径的理由。
//
// ═══════════════════════ 三条规则 ═══════════════════════
//
//	同一目标用户组 + 已有的那条是永久 → 拒绝。他已经永久拥有了,再卖一次是骗钱。
//	同一目标用户组 + 已有的那条有时效 → **延期**,不新建订阅。
//	不同目标用户组                     → 旧的那条**直接作废**,剩余时间不折算、不退款。
//
// 第三条是不可逆的,所以购买入口必须在下单**之前**就告诉用户会顶掉什么
// (见 PreviewUserGroupPurchase)。这里只负责执行,不负责征求同意 ——
// 把确认塞进事务里会让"用户没点确认"变成一次事务回滚,而那时钱可能已经收了。
//
// ═══════════════ 「作废」作废的是组与时间,不是钱 ═══════════════
//
// 项目方拍板那条规则的原话是「A 组没到期时买 B 组 → A 组**剩余时间**直接作废」,
// 说的是**用户组商品**(定义就是"纯商品,没有余额")。但选行 SQL 只看
// `upgrade_group <> ” AND status='active'`,被顶掉的完全可以是一张
// 「升组 + 送额度」的付费订阅 —— 那条路上 status 被写成 superseded、end_time
// 推到当下,里面**没花完的余额一起消失**(实测:一件几块钱的纯商品把一张
// ¥1296 套餐剩下的 9.9 亿 quota 原地销毁,没有退款、没有告知、不可撤销)。
//
// 所以顶替按订阅带不带额度分两种落法:
//
//	纯商品(no_quota)      → 照旧 superseded + end_time=now。它只有时间,规则原样成立。
//	带额度的订阅           → 只**摘掉它的改组身份**(upgrade_group/downgrade_group 清空),
//	                        status 与 end_time 一个字节不动。用户组权益立刻让位给新买的那件
//	                        (这就是"作废"),而他花钱买的那笔余额继续出资到原到期日。
//
// 摘身份而不是删身份:prev_user_group 必须留着,它是这条升组链的**链根**,
// 紧接着新建的那条订阅要靠它算到期回退目标(见 userGroupBeforeUpgradeChainTx,
// 那条查询因此不再要求 upgrade_group 非空)。丢了链根 = 新订阅把"回退到 vip"
// 记进快照,而用户根本不再拥有 vip。
//
// ═══════════════════════ 返回值语义 ═══════════════════════
//
//	(nil, nil)  没有触发任何规则,调用方继续正常新建订阅
//	(sub, nil)  已经延期了既有订阅,调用方**不要再新建**
//	(nil, err)  拒绝购买
//
// source 是资金安全判据:已付款的那一档(见 isPaidSubscriptionSource)一条
// 拒绝也不许返回 —— 拒绝会让整个事务回滚,订单永久停在 pending,钱收了货发不出。
func applyUserGroupPurchaseRulesTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string) (*UserSubscription, error) {
	target := strings.TrimSpace(plan.UpgradeGroup)
	if target == "" {
		return nil, nil
	}
	// ── 只有**纯商品**才适用这套规则 ──
	//
	// 带额度的「升组 + 送额度」套餐(最自然的 VIP 会员配置)不能走续期分支:
	// 那条分支只改 end_time,不新建行、不加 amount_total、不清 amount_used。
	// 而 PurchaseSubscriptionWithBalance 是**先扣钱**再进来的,续期分支不回滚那笔扣款 ——
	// 于是用户第二次付了全款,买到的是零可用额度(实测:两笔各 50000 quota 的订单,
	// 订阅仍停在 amount_used = amount_total 的用尽状态)。
	//
	// 纯商品没有这个问题:它本来就不带额度,续期改的确实只有有效期。
	// 带额度的套餐一律走下面的正常新建路径,由 MaxPurchasePerUser 去限购。
	if !plan.NoQuota {
		return nil, nil
	}
	now := GetDBTimestamp()

	var actives []UserSubscription
	if err := lockForUpdate(tx).
		Where("user_id = ? AND status = ? AND upgrade_group <> '' AND "+SubscriptionActiveEndTimeSQL,
			userId, "active", now).
		Order("id asc").
		Find(&actives).Error; err != nil {
		return nil, err
	}

	for i := range actives {
		existing := &actives[i]
		if strings.TrimSpace(existing.UpgradeGroup) != target {
			continue
		}
		// ── 同一个用户组 ──
		if existing.EndTime == 0 {
			if !isPaidSubscriptionSource(source) {
				return nil, errors.New("你已经永久拥有该用户组,无需重复购买")
			}
			// 钱已经收了。永久组没有任何可以"再给一点"的东西(延期到永久之后
			// 还是永久),但拒绝会让订单永久卡在 pending —— 那是收了钱、货发不出、
			// 也没有人能补单或退款。把既有那条原样交回去,订单照常成交、
			// top_ups 落行(收入与返佣不会凭空少一笔),同时留下这条日志给运营去退款。
			//
			// 下单前本来就该被拦住:四个网关 handler 在创建订单之前调
			// PreviewUserGroupPurchase,action=="reject" 直接不让下单。走到这里
			// 说明是那之后的竞态(或管理员/兑换码在中间把永久组发了出去)。
			common.SysError(fmt.Sprintf(
				"user %d already owns user group %q permanently but a paid subscription order (source=%s, plan %d) arrived; completing the order without granting anything — refund it manually",
				userId, target, source, plan.Id))
			return existing, nil
		}
		if plan.DurationUnit == SubscriptionDurationPermanent {
			// 时效档升永久:直接把它变成永久,而不是新开一条。
			// 新开一条的话两条都活着,到期回退那一步要处理"永久的还在、时效的到期了",
			// 而它算出来的结论正好是"保持当前组"——对,但多一条永远查不清的僵尸订阅。
			if err := tx.Model(&UserSubscription{}).Where("id = ?", existing.Id).
				Updates(map[string]any{"end_time": 0, "updated_at": now}).Error; err != nil {
				return nil, err
			}
			existing.EndTime = 0
			return existing, nil
		}
		// 时效 + 时效:从**原到期时间**往后接,不是从现在往后接。
		// 从现在接会把用户已经买过、还没用完的那段时间吃掉。
		base := time.Unix(existing.EndTime, 0)
		if existing.EndTime < now {
			base = time.Unix(now, 0)
		}
		newEnd, err := calcPlanEndTime(base, plan)
		if err != nil {
			return nil, err
		}
		if err := tx.Model(&UserSubscription{}).Where("id = ?", existing.Id).
			Updates(map[string]any{"end_time": newEnd, "updated_at": now}).Error; err != nil {
			return nil, err
		}
		existing.EndTime = newEnd
		return existing, nil
	}

	// ── 走到这里说明没有同组的,那么所有异组的都要被顶掉 ──
	//
	// 作废用 status=superseded 而不是删行:那条订阅是用户真的花钱买过的,
	// 删掉之后客服无法回答"我上个月买的 VIP 去哪了"。同时把 end_time 推到当下,
	// 让它在任何"仍然有效"的判据下都立刻失效。
	//
	// 刻意**不**触发它的到期回退:回退会把用户的组降回旧的 prev_user_group,
	// 而紧接着新订阅又要把组改成 target —— 中间那一瞬用户处在一个谁都没配过的
	// 状态,而且两次写 users.group 会在审计里留下一条看不懂的往返。
	//
	// 带额度的订阅只摘身份、不动 status/end_time(理由见函数头):作废的是
	// 用户组权益,不是用户已经付过钱的余额。
	for i := range actives {
		existing := &actives[i]
		update := map[string]any{
			"status":     SubscriptionStatusSuperseded,
			"end_time":   now,
			"updated_at": now,
		}
		if !existing.NoQuota {
			update = map[string]any{
				"upgrade_group":   "",
				"downgrade_group": "",
				"updated_at":      now,
			}
		}
		if err := tx.Model(&UserSubscription{}).Where("id = ?", existing.Id).
			Updates(update).Error; err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// DetachUserGroupSubscriptionsTx 在**管理员手动改用户组**时,把那些"负责改组"的
// 订阅从到期回退中摘出来。
//
// ═══════════════════════ 为什么必须摘 ═══════════════════════
//
// 到期回退任务会按订阅的 downgrade_group / prev_user_group 把用户的组改回去。
// 管理员刚手工把某个人设成 A,而他名下还挂着一条会在下周到期、回退目标是 B 的
// 订阅 —— 到了那天,任务会把管理员这次的操作**无声地覆盖掉**,而没有任何人
// 记得那条订阅的存在。项目方拍板:管理员改组时清掉那条记录,并在操作前提醒。
//
// 摘的方式是把 upgrade_group / downgrade_group / prev_user_group 三个快照清空,
// **不动 status 与 end_time**:
//
//	订阅本身还活着(它可能还带着余额,那是用户花钱买的,不能一起作废)
//	只是它不再对"这个人属于哪个用户组"负任何责任
//
// downgradeUserGroupForSubscriptionTx 的第一条判断正是
// 「downgrade_group 与 upgrade_group 都空 → 什么都不做」,所以清空之后它天然
// 不再参与回退,不需要在回退那侧再加一个分支。
//
// 返回被摘掉的条数,供调用方写审计与提示。
func DetachUserGroupSubscriptionsTx(tx *gorm.DB, userId int) (int64, error) {
	if tx == nil || userId <= 0 {
		return 0, nil
	}
	// 选行同时看 prev_user_group:跨组顶替留下的那些行 upgrade_group 已经被摘空、
	// 但 prev_user_group 还留着当链根(见 userGroupBeforeUpgradeChainTx)。
	// 漏掉它们的话,管理员刚手工设好的组会在下一次购买时被那个旧链根覆写。
	res := tx.Model(&UserSubscription{}).
		Where("user_id = ? AND (upgrade_group <> '' OR prev_user_group <> '')", userId).
		Updates(map[string]any{
			"upgrade_group":   "",
			"downgrade_group": "",
			"prev_user_group": "",
			"updated_at":      GetDBTimestamp(),
		})
	return res.RowsAffected, res.Error
}

// CountUserGroupSubscriptions 数一个用户名下还有几条"负责改组"的订阅。
//
// 给管理端在**保存之前**做提醒用:改组会把它们全部摘掉,而那是不可逆的。
// 只读、不锁 —— 它跑在管理员点开用户编辑页的路径上。
func CountUserGroupSubscriptions(userId int) (int64, error) {
	if userId <= 0 {
		return 0, nil
	}
	var count int64
	err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND upgrade_group <> ''", userId).
		Count(&count).Error
	return count, err
}

// UserGroupPurchasePreview.Action 的四个取值。
//
// UserGroupPurchaseActionReject 是唯一一个**下单入口必须自己处理**的:
// 它对应 applyUserGroupPurchaseRulesTx 里那条「你已经永久拥有该用户组」的拒绝,
// 而那条拒绝跑在收款之后的事务里 —— 四个网关 handler 不在下单前问一遍的话,
// 用户会先付款、再撞上它,订单永久停在 pending。
const (
	UserGroupPurchaseActionNew       = "new"
	UserGroupPurchaseActionExtend    = "extend"
	UserGroupPurchaseActionSupersede = "supersede"
	UserGroupPurchaseActionReject    = "reject"
)

// UserGroupPurchasePreview 是下单**之前**要告诉用户的后果。
type UserGroupPurchasePreview struct {
	// TargetGroup 买完之后他会在哪个用户组。
	TargetGroup string `json:"target_group"`
	// Action ∈ {"new", "extend", "supersede", "reject"}
	Action string `json:"action"`
	// SupersededGroups 会被顶掉的用户组名(Action = supersede 时非空)。
	SupersededGroups []string `json:"superseded_groups,omitempty"`
	// Message 给用户看的原话。
	Message string `json:"message"`
}

// PreviewUserGroupPurchase 在下单前算出这次购买会发生什么。
//
// ═══════════ 为什么必须有这一步 ═══════════
//
// 跨组购买会把旧组剩余的时间**直接作废**,不折算、不退款(项目方拍板)。
// 这是不可逆的,所以它必须在用户付钱**之前**就写在屏幕上。
//
// 刻意与执行分开:把确认塞进购买事务里,会让"用户没点确认"变成一次事务回滚,
// 而那时候钱可能已经收了。预览是只读的,执行是事务的,两者判据同源
// (都看 upgrade_group 与 SubscriptionActiveEndTimeSQL),但生命周期不同。
//
// 判据同源但不共用一个函数:执行那侧必须在事务里带行锁,而预览不能锁 ——
// 一个用户刷一下商品页就锁住自己所有订阅行是不可接受的。
func PreviewUserGroupPurchase(userId int, plan *SubscriptionPlan) (*UserGroupPurchasePreview, error) {
	if plan == nil {
		return nil, errors.New("invalid plan")
	}
	target := strings.TrimSpace(plan.UpgradeGroup)
	out := &UserGroupPurchasePreview{TargetGroup: target, Action: "new"}
	if target == "" {
		out.Action = "new"
		return out, nil
	}
	now := common.GetTimestamp()
	var actives []UserSubscription
	if err := DB.Where("user_id = ? AND status = ? AND upgrade_group <> '' AND "+SubscriptionActiveEndTimeSQL,
		userId, "active", now).Find(&actives).Error; err != nil {
		return nil, err
	}

	for i := range actives {
		if strings.TrimSpace(actives[i].UpgradeGroup) != target {
			continue
		}
		if actives[i].EndTime == 0 {
			out.Action = "reject"
			out.Message = "你已经永久拥有「" + target + "」,无需重复购买。"
			return out, nil
		}
		out.Action = "extend"
		if plan.DurationUnit == SubscriptionDurationPermanent {
			out.Message = "你已拥有「" + target + "」,本次购买会把它变为永久有效。"
		} else {
			out.Message = "你已拥有「" + target + "」,本次购买会在现有到期时间之后继续顺延。"
		}
		return out, nil
	}

	keepsQuota := false
	for i := range actives {
		out.SupersededGroups = append(out.SupersededGroups, strings.TrimSpace(actives[i].UpgradeGroup))
		if !actives[i].NoQuota {
			keepsQuota = true
		}
	}
	if len(out.SupersededGroups) > 0 {
		out.Action = "supersede"
		out.Message = "你当前的「" + strings.Join(out.SupersededGroups, "、") +
			"」尚未到期。购买本商品会立即顶替它,**剩余时间直接作废、不折算也不退款,且不可撤销**。"
		// 被顶掉的里面有带额度的订阅时必须补这一句 —— 上一句只说了"时间",
		// 而用户最担心的恰恰是钱。执行侧的口径见 applyUserGroupPurchaseRulesTx:
		// 带额度的订阅只被摘掉改组身份,余额与有效期原样保留。
		if keepsQuota {
			out.Message += "其中带额度的套餐**余额不会被清空**:它只是不再决定你的用户组,剩余额度仍可继续使用到原到期时间。"
		}
		return out, nil
	}
	out.Message = "购买后你的用户组将变更为「" + target + "」。"
	return out, nil
}

func CreateUserSubscriptionFromPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string) (*UserSubscription, error) {
	if tx == nil {
		return nil, errors.New("tx is nil")
	}
	if plan == nil || plan.Id == 0 {
		return nil, errors.New("invalid plan")
	}
	if userId <= 0 {
		return nil, errors.New("invalid user id")
	}
	if plan.MaxPurchasePerUser > 0 {
		var count int64
		if err := tx.Model(&UserSubscription{}).
			Where("user_id = ? AND plan_id = ?", userId, plan.Id).
			Count(&count).Error; err != nil {
			return nil, err
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			// 已付款的那一档只能放行,与名额闸门 §四 同一条口径:这里返回错误
			// → 整个事务回滚 → 订单永久停在 pending、钱已经收了、货发不出,
			// 而本仓既没有定时任务会关掉它,也没有管理端补单或退款接口
			// (AdminBindSubscription 走的是同一个函数,补发也撞同一堵墙)。
			//
			// 限购是**下单前**的闸门(四个网关 handler 各自的 CountUserSubscriptionsByPlan
			// 预检),不是收款后的闸门。预检与付款之间被别人/别的路径占满是竞态,
			// 溢出一份的后果是业务性的,卡死一笔已付款订单的后果是资损性的。
			if !isPaidSubscriptionSource(source) {
				return nil, errors.New("已达到该套餐购买上限")
			}
			common.SysError(fmt.Sprintf(
				"subscription plan %d hit its per-user purchase limit (%d) for user %d, but the payment was already taken (source=%s); granting it anyway to avoid a stuck paid order",
				plan.Id, plan.MaxPurchasePerUser, userId, source))
		}
	}
	nowUnix := GetDBTimestamp()
	now := time.Unix(nowUnix, 0)
	endUnix, err := calcPlanEndTime(now, plan)
	err = QyGateSubscriptionSeat(tx, plan, userId, source, err)
	if err != nil {
		return nil, err
	}
	// 用户组商品的三条购买规则。只作用于"会改用户组"的套餐,
	// 普通套餐(upgrade_group 为空)完全不受影响。
	//
	// **必须排在 QyGateSubscriptionSeat 之后。** 早先它排在前面,于是同组续期那条
	// 分支会在名额闸门跑之前 return —— 结果是「先买同组的不限量档、再买限量档」
	// 可以绕开全站名额:第二笔走进续期分支、直接返回,闸门根本没执行,而订单表里
	// 留下一条限量套餐的成功订单、订阅表里却没有对应的行(收入归因也落错了套餐)。
	if extended, err := applyUserGroupPurchaseRulesTx(tx, userId, plan, source); err != nil {
		return nil, err
	} else if extended != nil {
		// 同组续期:没有产生新订阅,直接把延长后的那条返回给调用方。
		return extended, nil
	}
	resetBase := now
	nextReset := calcNextResetTime(resetBase, plan, endUnix)
	lastReset := int64(0)
	if nextReset > 0 {
		lastReset = now.Unix()
	}
	upgradeGroup := strings.TrimSpace(plan.UpgradeGroup)
	prevGroup := ""
	if upgradeGroup != "" {
		currentGroup, err := getUserGroupByIdTx(tx, userId)
		if err != nil {
			return nil, err
		}
		// 回退目标必须是「这条升组链开始之前」那个分组,不是「买这一次之前」那个。
		// 两者只在用户名下已经有一条会改组的订阅时才不同,而那正好是两种最常见的
		// 动作:跨组顶替(先买 VIP 再买 GOLD)与续费(同一个升组套餐买第二次)。
		prevGroup = userGroupBeforeUpgradeChainTx(tx, userId, currentGroup)
		if currentGroup != upgradeGroup {
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("group", upgradeGroup).Error; err != nil {
				return nil, err
			}
		}
		if prevGroup == upgradeGroup {
			// 链根就是目标组本身(理论上到不了,靠上面的继承已经排除)。
			// 留空比留一个"回退到你已经在的组"更诚实。
			prevGroup = ""
		}
	}
	allowWalletOverflow := true
	if plan.AllowWalletOverflow != nil {
		allowWalletOverflow = *plan.AllowWalletOverflow
	}
	sub := &UserSubscription{
		UserId:              userId,
		PlanId:              plan.Id,
		AmountTotal:         planAmountTotal(plan),
		NoQuota:             plan.NoQuota,
		AmountUsed:          0,
		StartTime:           now.Unix(),
		EndTime:             endUnix,
		Status:              "active",
		Source:              source,
		LastResetTime:       lastReset,
		NextResetTime:       nextReset,
		UpgradeGroup:        upgradeGroup,
		PrevUserGroup:       prevGroup,
		DowngradeGroup:      strings.TrimSpace(plan.DowngradeGroup),
		AllowWalletOverflow: allowWalletOverflow,
		CreatedAt:           common.GetTimestamp(),
		UpdatedAt:           common.GetTimestamp(),
	}
	if err := tx.Create(sub).Error; err != nil {
		return nil, err
	}
	return sub, nil
}

func refreshSubscriptionUserGroupCache(userId int, operation string) {
	// 返佣模块把这个人的账号分组缓存在自己的进程内表里,而那个分组决定他作为
	// 推广人时的返佣费率与法币折算比例。它必须在 RefreshUserGroupCache **之前**
	// 通知:后者在没开 Redis 的部署上第一行就 return,把通知写在它后面等于
	// 单机部署永远收不到这个事件。
	QyOnUserGroupChanged(userId)
	if err := RefreshUserGroupCache(userId); err != nil {
		common.SysError(fmt.Sprintf("failed to refresh user group cache after %s for user %d: %v", operation, userId, err))
	}
}

// Complete a subscription order (idempotent). Creates a UserSubscription snapshot from the plan.
// expectedPaymentProvider guards against cross-gateway callback attacks (empty skips the check).
// actualPaymentMethod updates the order's PaymentMethod to reflect the real payment type used (empty skips update).
func CompleteSubscriptionOrder(tradeNo string, providerPayload string, expectedPaymentProvider string, actualPaymentMethod string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	var logUserId int
	var logPlanTitle string
	var logMoney float64
	var logPaymentMethod string
	var upgradeGroup string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status == common.TopUpStatusSuccess {
			return nil
		}
		// expired 也照常发货。
		//
		// 超龄的 pending 订单会被 ExpireStalePendingSubscriptionOrders 打成
		// expired,那只是**给运营看的**状态(这一单大概率是被丢弃的收银台),
		// 不是"这笔钱我们不认了"。真的收到一条签名合法的回调,说明用户确实
		// 把钱付了 —— 此时拒绝就是收了钱不发货,而本仓没有任何退款路径。
		if order.Status != common.TopUpStatusPending && order.Status != common.TopUpStatusExpired {
			return ErrSubscriptionOrderStatusInvalid
		}
		if order.Status == common.TopUpStatusExpired {
			common.SysError(fmt.Sprintf(
				"subscription order %s was already marked expired (created at %d) but a payment callback arrived; shipping it anyway",
				order.TradeNo, order.CreateTime))
		}
		// 按**下单那一刻**的套餐发货,而不是现读。见 SubscriptionOrder.PlanSnapshot。
		plan, err := subscriptionOrderPlan(&order)
		if err != nil {
			return err
		}
		if !plan.Enabled {
			// still allow completion for already purchased orders
		}
		// 锁定用户行：并发完成同一用户的不同订单（包括多实例部署下）时，
		// 使 CreateUserSubscriptionFromPlanTx 的 MaxPurchasePerUser 检查按用户串行。
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", order.UserId).First(&userRow).Error; err != nil {
			return err
		}
		subscription, err := CreateUserSubscriptionFromPlanTx(tx, order.UserId, plan, SubscriptionSourceOrder)
		if err != nil {
			return err
		}
		if subscription.PrevUserGroup != "" {
			upgradeGroup = strings.TrimSpace(subscription.UpgradeGroup)
		}
		if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
			return err
		}
		order.Status = common.TopUpStatusSuccess
		order.CompleteTime = common.GetTimestamp()
		if providerPayload != "" {
			order.ProviderPayload = providerPayload
		}
		if actualPaymentMethod != "" && order.PaymentMethod != actualPaymentMethod {
			order.PaymentMethod = actualPaymentMethod
		}
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		logUserId = order.UserId
		logPlanTitle = plan.Title
		logMoney = order.Money
		logPaymentMethod = order.PaymentMethod
		return nil
	})
	if err != nil {
		return err
	}
	if upgradeGroup != "" && logUserId > 0 {
		refreshSubscriptionUserGroupCache(logUserId, "subscription payment completion")
	}
	if logUserId > 0 {
		msg := fmt.Sprintf("订阅购买成功，套餐: %s，支付金额: %.2f，支付方式: %s", logPlanTitle, logMoney, logPaymentMethod)
		RecordLog(logUserId, LogTypeTopup, msg)
	}
	return nil
}

func upsertSubscriptionTopUpTx(tx *gorm.DB, order *SubscriptionOrder) error {
	if tx == nil || order == nil {
		return errors.New("invalid subscription order")
	}
	now := common.GetTimestamp()
	var topup TopUp
	if err := tx.Where("trade_no = ?", order.TradeNo).First(&topup).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			topup = TopUp{
				UserId:        order.UserId,
				Amount:        0,
				Money:         order.Money,
				TradeNo:       order.TradeNo,
				PaymentMethod: order.PaymentMethod,
				CreateTime:    order.CreateTime,
				CompleteTime:  now,
				Status:        common.TopUpStatusSuccess,
			}
			return tx.Create(&topup).Error
		}
		return err
	}
	topup.Money = order.Money
	if topup.PaymentMethod == "" {
		topup.PaymentMethod = order.PaymentMethod
	} else if topup.PaymentMethod != order.PaymentMethod {
		return ErrPaymentMethodMismatch
	}
	if topup.CreateTime == 0 {
		topup.CreateTime = order.CreateTime
	}
	topup.CompleteTime = now
	topup.Status = common.TopUpStatusSuccess
	return tx.Save(&topup).Error
}

func ExpireSubscriptionOrder(tradeNo string, expectedPaymentProvider string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status != common.TopUpStatusPending {
			return nil
		}
		order.Status = common.TopUpStatusExpired
		order.CompleteTime = common.GetTimestamp()
		return tx.Save(&order).Error
	})
}

// SubscriptionOrderPendingTTL 是一张 pending 订阅订单在被打成 expired 之前
// 能挂多久(秒)。
//
// ═══════════ 为什么必须有这个上界 ═══════════
//
// 在此之前**没有任何一处**会关掉 pending 的订阅订单:ExpireSubscriptionOrder
// 只在 epay 拉起支付失败与 Stripe 会话过期时被调到,没有定时任务、管理端也
// 没有订单接口。于是「打开收银台、没付钱」的订单永久堆积(演示站主库里
// 78 条,最早的近 3 个月),运营看不出哪些还活着,而"这个套餐已停售"这句话
// 在这堆订单面前是不成立的。
//
// ═══════════ 它不是"这笔钱不认了" ═══════════
//
// expired 只改变**运营视图**:CompleteSubscriptionOrder 仍然接受 expired 的
// 订单并照常发货(见那里的注释)。所以这个 TTL 定短了也不会吃掉任何人的钱,
// 最坏只是让一张真的会被付款的单子先显示成过期。默认 72 小时。
//
// <= 0 关闭清扫(留给不希望后台动订单表的部署)。
func SubscriptionOrderPendingTTL() int64 {
	return int64(common.GetEnvOrDefault("SUBSCRIPTION_ORDER_PENDING_TTL", 72*3600))
}

// ExpireStalePendingSubscriptionOrders 把创建时间早于 now-ttl 的 pending
// 订阅订单打成 expired,返回本批处理条数。
func ExpireStalePendingSubscriptionOrders(ttlSeconds int64, limit int) (int, error) {
	if ttlSeconds <= 0 {
		return 0, nil
	}
	if limit <= 0 {
		limit = 200
	}
	cutoff := common.GetTimestamp() - ttlSeconds
	var orders []SubscriptionOrder
	if err := DB.Where("status = ? AND create_time > 0 AND create_time < ?", common.TopUpStatusPending, cutoff).
		Order("id asc").
		Limit(limit).
		Find(&orders).Error; err != nil {
		return 0, err
	}
	if len(orders) == 0 {
		return 0, nil
	}
	ids := make([]int, 0, len(orders))
	for i := range orders {
		ids = append(ids, orders[i].Id)
	}
	// 按 id + status 更新:并发的支付回调可能刚好把其中一条改成 success,
	// 那一条就不该被这批扫描碰到。
	res := DB.Model(&SubscriptionOrder{}).
		Where("id IN ? AND status = ?", ids, common.TopUpStatusPending).
		Updates(map[string]any{
			"status":        common.TopUpStatusExpired,
			"complete_time": common.GetTimestamp(),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// Admin bind (no payment). Creates a UserSubscription from a plan.
func AdminBindSubscription(userId int, planId int, sourceNote string) (string, error) {
	if userId <= 0 || planId <= 0 {
		return "", errors.New("invalid userId or planId")
	}
	plan, err := GetSubscriptionPlanById(planId)
	if err != nil {
		return "", err
	}
	groupChanged := false
	err = DB.Transaction(func(tx *gorm.DB) error {
		// 与 CompleteSubscriptionOrder 一致：先锁用户行，再做购买次数检查。
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&userRow).Error; err != nil {
			return err
		}
		subscription, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, "admin")
		if err == nil {
			groupChanged = subscription.PrevUserGroup != ""
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if groupChanged {
		refreshSubscriptionUserGroupCache(userId, "admin subscription creation")
		return fmt.Sprintf("用户分组将升级到 %s", plan.UpgradeGroup), nil
	}
	return "", nil
}

func calcSubscriptionBalanceQuota(priceAmount float64) (int, error) {
	if priceAmount <= 0 {
		return 0, nil
	}
	if common.QuotaPerUnit <= 0 {
		return 0, errors.New("额度单位配置错误")
	}
	quota := decimal.NewFromFloat(priceAmount).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Ceil()
	return common.QuotaFromDecimalStrict(quota)
}

// PurchaseSubscriptionWithBalance creates a subscription by deducting the user's wallet quota.
func PurchaseSubscriptionWithBalance(userId int, planId int) error {
	if userId <= 0 || planId <= 0 {
		return errors.New("invalid userId or planId")
	}

	var logPlanTitle string
	var logMoney float64
	var chargedQuota int
	var upgradeGroup string
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		if !plan.Enabled {
			return errors.New("套餐未启用")
		}
		// 发售时间窗排在扣款之前(下面才 lockForUpdate 用户行并扣 quota)。
		// 排在扣款之后的话,拒绝会让整个事务回滚 —— 结果虽然也对,但用户看到的
		// 是"扣款失败"而不是"未开售/已停售",而这两句要做的下一步完全不同。
		if err := PlanSaleWindowError(plan, common.GetTimestamp()); err != nil {
			return err
		}
		if plan.PriceAmount < 0 {
			return errors.New("套餐价格不能为负数")
		}
		if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
			return errors.New("该套餐不允许使用余额兑换")
		}

		requiredQuota, err := calcSubscriptionBalanceQuota(plan.PriceAmount)
		if err != nil {
			return err
		}

		var user User
		if err := lockForUpdate(tx).Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		if requiredQuota > 0 && user.Quota < requiredQuota {
			return errors.New("余额不足")
		}
		if requiredQuota > 0 {
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("quota", gorm.Expr("quota - ?", requiredQuota)).Error; err != nil {
				return err
			}
		}

		subscription, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, PaymentMethodBalance)
		if err != nil {
			return err
		}

		now := common.GetTimestamp()
		tradeNo := fmt.Sprintf("SUBBALUSR%dNO%s%d", userId, common.GetRandomString(6), time.Now().UnixNano())
		order := &SubscriptionOrder{
			UserId:          userId,
			PlanId:          plan.Id,
			Money:           plan.PriceAmount,
			TradeNo:         tradeNo,
			PaymentMethod:   PaymentMethodBalance,
			PaymentProvider: PaymentProviderBalance,
			Status:          common.TopUpStatusSuccess,
			CreateTime:      now,
			CompleteTime:    now,
			ProviderPayload: fmt.Sprintf("charged_quota=%d", requiredQuota),
		}
		if err := tx.Create(order).Error; err != nil {
			return err
		}

		logPlanTitle = plan.Title
		logMoney = plan.PriceAmount
		chargedQuota = requiredQuota
		if subscription.PrevUserGroup != "" {
			upgradeGroup = strings.TrimSpace(subscription.UpgradeGroup)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if chargedQuota > 0 {
		if err := cacheDecrUserQuota(userId, int64(chargedQuota)); err != nil {
			common.SysLog("failed to decrease user quota cache after subscription balance purchase: " + err.Error())
		}
	}
	if upgradeGroup != "" {
		refreshSubscriptionUserGroupCache(userId, "subscription balance purchase")
	}
	msg := fmt.Sprintf("使用余额购买订阅成功，套餐: %s，支付金额: %.2f，扣除额度: %d", logPlanTitle, logMoney, chargedQuota)
	RecordLog(userId, LogTypeTopup, msg)
	return nil
}

// GetAllActiveUserSubscriptions returns all active subscriptions for a user.
func GetAllActiveUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var subs []UserSubscription
	err := DB.Where("user_id = ? AND status = ? AND "+SubscriptionActiveEndTimeSQL, userId, "active", now).
		Order("end_time desc, id desc").
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

// HasActiveUserSubscription returns whether the user has any active subscription.
// This is a lightweight existence check to avoid heavy pre-consume transactions.
func HasActiveUserSubscription(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var count int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND status = ? AND "+SubscriptionActiveEndTimeSQL, userId, "active", now).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ═══════════ 已删除:UserActiveSubscriptionsAllowWalletOverflow ═══════════
//
// 它的口径是**用户级**聚合:「只要有一条活跃订阅 allow_wallet_overflow=false,
// 就不许回落钱包」。那让一张与本次模型分组毫无关系的套餐把钱包出资一起封掉,
// 也让「用户分组本来就含这个模型分组」的人在套餐用尽后被 403。
//
// 现在 allow_wallet_overflow 只对「纯靠套餐解锁的模型分组」生效,而且只统计
// **解锁该模型分组的**订阅;判定住在钱包出资闸门内部
// (service.QyModelGroupFundingAllowed / qianye/modules/groupns)。

// GetAllUserSubscriptions returns all subscriptions (active and expired) for a user.
func GetAllUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	var subs []UserSubscription
	err := DB.Where("user_id = ?", userId).
		Order("end_time desc, id desc").
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

func buildSubscriptionSummaries(subs []UserSubscription) []SubscriptionSummary {
	if len(subs) == 0 {
		return []SubscriptionSummary{}
	}
	result := make([]SubscriptionSummary, 0, len(subs))
	for _, sub := range subs {
		subCopy := sub
		result = append(result, SubscriptionSummary{
			Subscription: &subCopy,
		})
	}
	return result
}

// AdminInvalidateUserSubscription marks a user subscription as cancelled and ends it immediately.
func AdminInvalidateUserSubscription(userSubscriptionId int) (string, error) {
	if userSubscriptionId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	now := common.GetTimestamp()
	cacheGroup := ""
	downgradeGroup := ""
	var userId int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
			return err
		}
		userId = sub.UserId
		if err := tx.Model(&sub).Updates(map[string]interface{}{
			"status":     "cancelled",
			"end_time":   now,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
		if err != nil {
			return err
		}
		if target != "" {
			cacheGroup = target
			downgradeGroup = target
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if cacheGroup != "" && userId > 0 {
		refreshSubscriptionUserGroupCache(userId, "admin subscription update")
	}
	if downgradeGroup != "" {
		return fmt.Sprintf("用户分组将回退到 %s", downgradeGroup), nil
	}
	return "", nil
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func AdminDeleteUserSubscription(userSubscriptionId int) (string, error) {
	if userSubscriptionId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	now := common.GetTimestamp()
	cacheGroup := ""
	downgradeGroup := ""
	var userId int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
			return err
		}
		userId = sub.UserId
		target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
		if err != nil {
			return err
		}
		if target != "" {
			cacheGroup = target
			downgradeGroup = target
		}
		if err := tx.Where("id = ?", userSubscriptionId).Delete(&UserSubscription{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if cacheGroup != "" && userId > 0 {
		refreshSubscriptionUserGroupCache(userId, "admin subscription deletion")
	}
	if downgradeGroup != "" {
		return fmt.Sprintf("用户分组将回退到 %s", downgradeGroup), nil
	}
	return "", nil
}

func resetUserSubscriptionTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64, advanceResetTime bool) error {
	if tx == nil || sub == nil || plan == nil {
		return errors.New("invalid reset args")
	}
	sub.AmountUsed = 0
	sub.WriteOffCount = 0
	if advanceResetTime {
		nextReset := calcNextResetTime(time.Unix(now, 0), plan, sub.EndTime)
		sub.NextResetTime = nextReset
		if nextReset > 0 {
			sub.LastResetTime = now
		} else {
			sub.LastResetTime = 0
		}
	}
	return tx.Save(sub).Error
}

func buildSubscriptionResetResult(plan *SubscriptionPlan, subs []UserSubscription, advanceResetTime bool) *SubscriptionResetResult {
	userIds := make([]int, 0, len(subs))
	seenUsers := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if _, ok := seenUsers[sub.UserId]; ok {
			continue
		}
		seenUsers[sub.UserId] = struct{}{}
		userIds = append(userIds, sub.UserId)
	}
	return &SubscriptionResetResult{
		PlanId:           plan.Id,
		MatchedCount:     len(subs),
		ResetCount:       len(subs),
		UserCount:        len(userIds),
		AdvanceResetTime: advanceResetTime,
		PlanTitle:        plan.Title,
		AffectedUserIds:  userIds,
	}
}

func adminResetUserSubscriptionsByPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, now int64, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if tx == nil || plan == nil {
		return nil, errors.New("invalid reset args")
	}
	var subs []UserSubscription
	if err := lockForUpdate(tx).
		Where("user_id = ? AND plan_id = ? AND status = ? AND "+SubscriptionActiveEndTimeSQL, userId, plan.Id, "active", now).
		Order("end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, errors.New("该用户没有有效的此套餐订阅")
	}
	for i := range subs {
		if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
			return nil, err
		}
	}
	return buildSubscriptionResetResult(plan, subs, advanceResetTime), nil
}

func adminResetPlanSubscriptionsTx(tx *gorm.DB, plan *SubscriptionPlan, now int64, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if tx == nil || plan == nil {
		return nil, errors.New("invalid reset args")
	}
	var subs []UserSubscription
	if err := lockForUpdate(tx).
		Where("plan_id = ? AND status = ? AND "+SubscriptionActiveEndTimeSQL, plan.Id, "active", now).
		Order("user_id asc, end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	for i := range subs {
		if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
			return nil, err
		}
	}
	return buildSubscriptionResetResult(plan, subs, advanceResetTime), nil
}

func AdminResetUserSubscriptionsByPlan(userId int, planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if userId <= 0 || planId <= 0 {
		return nil, errors.New("invalid userId or planId")
	}
	var result *SubscriptionResetResult
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		result, err = adminResetUserSubscriptionsByPlanTx(tx, userId, plan, now, advanceResetTime)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func AdminResetPlanSubscriptions(planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if planId <= 0 {
		return nil, errors.New("invalid planId")
	}
	var result *SubscriptionResetResult
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		result, err = adminResetPlanSubscriptionsTx(tx, plan, now, advanceResetTime)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type SubscriptionPreConsumeResult struct {
	UserSubscriptionId int
	PreConsumed        int64
	AmountTotal        int64
	AmountUsedBefore   int64
	AmountUsedAfter    int64
}

// ExpireDueSubscriptions marks expired subscriptions and handles group downgrade.
func ExpireDueSubscriptions(limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := GetDBTimestamp()
	var subs []UserSubscription
	if err := DB.Where("status = ? AND end_time > 0 AND end_time <= ?", "active", now).
		Order("end_time asc, id asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	expiredCount := 0
	userIds := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if sub.UserId > 0 {
			userIds[sub.UserId] = struct{}{}
		}
	}
	for userId := range userIds {
		cacheGroup := ""
		err := DB.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&UserSubscription{}).
				Where("user_id = ? AND status = ? AND end_time > 0 AND end_time <= ?", userId, "active", now).
				Updates(map[string]interface{}{
					"status":     "expired",
					"updated_at": common.GetTimestamp(),
				})
			if res.Error != nil {
				return res.Error
			}
			expiredCount += int(res.RowsAffected)

			// 名下还有活跃的升组订阅时,分组对齐到**那一条**给的组。
			// 见 alignGroupToSurvivingUpgrade:原先在这里 return nil 保留当前组,
			// 而当前组正是刚刚到期的那条给的。
			var activeSub UserSubscription
			activeQuery := tx.Where("user_id = ? AND status = ? AND "+SubscriptionActiveEndTimeSQL+" AND upgrade_group <> ''",
				userId, "active", now).
				Order("end_time desc, id desc").
				Limit(1).
				Find(&activeSub)
			if activeQuery.Error == nil && activeQuery.RowsAffected > 0 {
				survivorGroup, err := getUserGroupByIdTx(tx, userId)
				if err != nil {
					return err
				}
				aligned, err := alignGroupToSurvivingUpgrade(tx, userId, survivorGroup, &activeSub)
				if err != nil {
					return err
				}
				cacheGroup = aligned
				return nil
			}

			currentGroup, err := getUserGroupByIdTx(tx, userId)
			if err != nil {
				return err
			}
			// 显式降级目标优先:它是运营在套餐上直接指定的落点,与当前分组无关。
			var lastExpired UserSubscription
			expiredQuery := tx.Where("user_id = ? AND status = ? AND downgrade_group <> ''",
				userId, "expired").
				Order("end_time desc, id desc").
				Limit(1).
				Find(&lastExpired)
			target := ""
			if expiredQuery.Error == nil && expiredQuery.RowsAffected > 0 {
				target = strings.TrimSpace(lastExpired.DowngradeGroup)
			}
			if target == "" {
				// 取**真正把这个人放到当前分组的那一条**,而不是 end_time 最大的
				// 那一条。
				//
				// 原先取 end_time 最大的那一行,再用 `currentGroup != upgradeGroup`
				// 一票否决。两条目标组不同的升组订阅并存时(带额度的套餐不走跨组
				// 顶替,可以合法并存),先到期的那条把人留在了它的组,而 end_time
				// 最大的那条给的是另一个组 —— 判据当场不成立,回退被放弃,此后再没有
				// 任何任务会碰他的分组,人**永久**停在一个付费组里。
				if strings.TrimSpace(currentGroup) == "" {
					return nil
				}
				var setter UserSubscription
				setterQuery := tx.Where("user_id = ? AND status = ? AND upgrade_group = ?",
					userId, "expired", currentGroup).
					Order("end_time desc, id desc").
					Limit(1).
					Find(&setter)
				if setterQuery.Error != nil || setterQuery.RowsAffected == 0 {
					return nil
				}
				prevGroup := strings.TrimSpace(setter.PrevUserGroup)
				if prevGroup == "" {
					// 同一个升组套餐买第二次时,老逻辑把 prev 留空(那一刻用户已经在
					// 目标组里)。走回链根,否则同样是永久留在升级分组。
					prevGroup = legacyPrevUserGroupTx(tx, userId)
				}
				if prevGroup == "" {
					return nil
				}
				target = prevGroup
			}
			if target == "" || target == currentGroup {
				return nil
			}
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("group", target).Error; err != nil {
				return err
			}
			cacheGroup = target
			return nil
		})
		if err != nil {
			return expiredCount, err
		}
		if cacheGroup != "" {
			refreshSubscriptionUserGroupCache(userId, "subscription expiration")
		}
	}
	return expiredCount, nil
}

// SubscriptionPreConsumeRecord stores idempotent pre-consume operations per request.
type SubscriptionPreConsumeRecord struct {
	Id                 int    `json:"id"`
	RequestId          string `json:"request_id" gorm:"type:varchar(64);uniqueIndex"`
	UserId             int    `json:"user_id" gorm:"index"`
	UserSubscriptionId int    `json:"user_subscription_id" gorm:"index"`
	PreConsumed        int64  `json:"pre_consumed" gorm:"type:bigint;not null;default:0"`
	Status             string `json:"status" gorm:"type:varchar(32);index"` // consumed/refunded
	CreatedAt          int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt          int64  `json:"updated_at" gorm:"bigint;index"`
}

func (r *SubscriptionPreConsumeRecord) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	r.CreatedAt = now
	r.UpdatedAt = now
	return nil
}

func (r *SubscriptionPreConsumeRecord) BeforeUpdate(tx *gorm.DB) error {
	r.UpdatedAt = common.GetTimestamp()
	return nil
}

func maybeResetUserSubscriptionWithPlanTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64) error {
	if tx == nil || sub == nil || plan == nil {
		return errors.New("invalid reset args")
	}
	if sub.NextResetTime > 0 && sub.NextResetTime > now {
		return nil
	}
	if NormalizeResetPeriod(plan.QuotaResetPeriod) == SubscriptionResetNever {
		return nil
	}
	baseUnix := sub.LastResetTime
	if baseUnix <= 0 {
		baseUnix = sub.StartTime
	}
	base := time.Unix(baseUnix, 0)
	next := calcNextResetTime(base, plan, sub.EndTime)
	advanced := false
	for next > 0 && next <= now {
		advanced = true
		base = time.Unix(next, 0)
		next = calcNextResetTime(base, plan, sub.EndTime)
	}
	if !advanced {
		if sub.NextResetTime == 0 && next > 0 {
			sub.NextResetTime = next
			sub.LastResetTime = base.Unix()
			return tx.Save(sub).Error
		}
		return nil
	}
	sub.AmountUsed = 0
	sub.WriteOffCount = 0
	sub.LastResetTime = base.Unix()
	sub.NextResetTime = next
	return tx.Save(sub).Error
}

// PreConsumeUserSubscription pre-consumes from any active subscription total quota.
func PreConsumeUserSubscription(requestId string, userId int, modelName string, quotaType int, amount int64, usingGroup string) (*SubscriptionPreConsumeResult, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	now := GetDBTimestamp()

	returnValue := &SubscriptionPreConsumeResult{}

	err := DB.Transaction(func(tx *gorm.DB) error {
		var existing SubscriptionPreConsumeRecord
		query := tx.Where("request_id = ?", requestId).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if existing.Status == "refunded" {
				return errors.New("subscription pre-consume already refunded")
			}
			var sub UserSubscription
			if err := tx.Where("id = ?", existing.UserSubscriptionId).First(&sub).Error; err != nil {
				return err
			}
			returnValue.UserSubscriptionId = sub.Id
			returnValue.PreConsumed = existing.PreConsumed
			returnValue.AmountTotal = sub.AmountTotal
			returnValue.AmountUsedBefore = sub.AmountUsed
			returnValue.AmountUsedAfter = sub.AmountUsed
			return nil
		}

		var subs []UserSubscription
		if err := lockForUpdate(tx).
			// no_quota 的订阅是**纯商品**,不参与出资 —— 必须在 SQL 里就排除掉。
			//
			// 放到循环里再 continue 是不够的:这条查询带 FOR UPDATE,把纯商品行
			// 一起锁进来会让"改用户组的商品"和"扣钱"互相排队,而它们本无关系。
			// 更要紧的是下面那句 `if sub.AmountTotal > 0` —— 纯商品的 AmountTotal
			// 是 0,而 0 的语义是**不限额度**,漏掉它等于给每个买家一份无限余额。
			Where("user_id = ? AND status = ? AND no_quota = ? AND "+SubscriptionActiveEndTimeSQL,
				userId, "active", false, now).
			Order("end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return errors.New("no active subscription")
		}
		if len(subs) == 0 {
			return errors.New("no active subscription")
		}
		// 先把候选筛一遍并把该重置的重置掉,再选人。
		//
		// 选人分两轮(见下面 pickFundingSubscription),而重置是**写操作**,
		// 放在选人循环里会被跑两遍;分开之后每条候选只重置一次。
		usable := make([]UserSubscription, 0, len(subs))
		for _, candidate := range subs {
			sub := candidate
			// 「余额仅限绑定的模型分组」的套餐,在本次请求的模型分组对不上时跳过。
			// 跳过 ≠ 余额不足:余额还在,只是用不了(见 QySubscriptionCandidateUsable)。
			if !QySubscriptionCandidateUsable(sub.PlanId, usingGroup) {
				continue
			}
			plan, err := getSubscriptionPlanByIdTx(tx, sub.PlanId)
			if err != nil {
				// 套餐行已被删掉,而用户身上这条订阅还指着它。
				//
				// 必须跳过这一条,不能 return:一条脏数据会让整个事务失败,而
				// 失败的错误码不是「余额不足」,service/billing_session.go 里的
				// 钱包回落判断因此不成立 —— 表现是这个用户**所有**请求全部被打挂,
				// 纯钱包付费也救不回来、换任何模型分组也救不回来。
				//
				// 跳过之后这条订阅只是不再出资(它本来也定不出价:重置周期、
				// 额度上限、绑定分组全在被删掉的那一行里),用户回落到钱包,
				// 与「这个套餐已经用完了」是同一种表现。
				if errors.Is(err, gorm.ErrRecordNotFound) {
					common.SysError(fmt.Sprintf(
						"user subscription %d (user %d) points at deleted plan %d; skipping it for funding",
						sub.Id, userId, sub.PlanId))
					continue
				}
				return err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
				return err
			}
			usable = append(usable, sub)
		}

		picked, consume := pickFundingSubscription(usable, amount)
		if picked == nil {
			return fmt.Errorf("subscription quota insufficient, need=%d", amount)
		}
		{
			sub := *picked
			usedBefore := sub.AmountUsed
			record := &SubscriptionPreConsumeRecord{
				RequestId:          requestId,
				UserId:             userId,
				UserSubscriptionId: sub.Id,
				PreConsumed:        consume,
				Status:             "consumed",
			}
			if err := tx.Create(record).Error; err != nil {
				var dup SubscriptionPreConsumeRecord
				if err2 := tx.Where("request_id = ?", requestId).First(&dup).Error; err2 == nil {
					if dup.Status == "refunded" {
						return errors.New("subscription pre-consume already refunded")
					}
					returnValue.UserSubscriptionId = sub.Id
					returnValue.PreConsumed = dup.PreConsumed
					returnValue.AmountTotal = sub.AmountTotal
					returnValue.AmountUsedBefore = sub.AmountUsed
					returnValue.AmountUsedAfter = sub.AmountUsed
					return nil
				}
				return err
			}
			sub.AmountUsed += consume
			if err := tx.Save(&sub).Error; err != nil {
				return err
			}
			returnValue.UserSubscriptionId = sub.Id
			returnValue.PreConsumed = consume
			returnValue.AmountTotal = sub.AmountTotal
			returnValue.AmountUsedBefore = usedBefore
			returnValue.AmountUsedAfter = sub.AmountUsed
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	return returnValue, nil
}

// pickFundingSubscription 从候选里挑出这一次由谁出资、出多少。
//
// 两轮,顺序不能换 —— 两轮合起来才既保住既有语义又修掉尾数被困死的问题:
//
//	第一轮 找一张能**整额**覆盖本次预扣额的(候选已按最先到期在前排好)。
//	       这一轮就是改动之前的全部行为:余额不够的那张顺延给下一张。
//	第二轮 一张都覆盖不了时,取最先到期且还有余额的那张,按剩余额度部分预扣。
//
// 为什么需要第二轮:筛候选用的是**预扣估算额**,而预扣额是真实花费的几十到
// 上百倍。只有第一轮的话,每张余额型套餐用到尾巴时都会留下「一次预扣额 − 1」
// 的残额 —— 那笔钱既花不掉(后续请求的预扣额只会更大),也没有任何提示,
// 用户看到的是「套餐还有余额,却在扣钱包」;整张 amount_total 小于一次预扣额
// 的套餐更是从头到尾一次都出不了资。实测阈值精确落在预扣额上:残 3048 走套餐,
// 残 3047 走钱包。
//
// 部分预扣不会少收钱:不够的那部分在结算时由 SettleUserSubscriptionDelta 夹到
// amount_total、差额落钱包 —— 那条「撞上限则钱包补收」的路径本来就存在,
// 这里只是让它提前一步开始工作,不新增任何一种结算形态。
//
// amount_total <= 0 是**不限量**(不是零额度),这样的候选在第一轮就整额命中。
func pickFundingSubscription(usable []UserSubscription, amount int64) (*UserSubscription, int64) {
	for _, allowPartial := range []bool{false, true} {
		for i := range usable {
			sub := &usable[i]
			if sub.AmountTotal <= 0 {
				return sub, amount
			}
			remain := sub.AmountTotal - sub.AmountUsed
			if remain <= 0 {
				continue
			}
			if remain < amount {
				if !allowPartial {
					continue
				}
				return sub, remain
			}
			return sub, amount
		}
	}
	return nil, 0
}

// RefundSubscriptionPreConsume is idempotent and refunds pre-consumed subscription quota by requestId.
func RefundSubscriptionPreConsume(requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var record SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).
			Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == "refunded" {
			return nil
		}
		if record.PreConsumed <= 0 {
			record.Status = "refunded"
			return tx.Save(&record).Error
		}
		// 必须走 tx 版本:用全局 DB 另开事务会让退款先独立提交,
		// 外层回滚时钱已经退了而幂等记录还是 consumed,重试即重复退款。
		if err := postConsumeUserSubscriptionDeltaTx(tx, record.UserSubscriptionId, -record.PreConsumed); err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			// 订阅行已经被管理端硬删掉了(AdminDeleteUserSubscription 不理会
			// 在途的预扣记录)。预扣额随行一起消失,没有任何东西可退 ——
			// 但记录必须落成 refunded,否则 refundWithRetry 会为一件永远不会
			// 成功的事重试 3 次,并把这条记录永久卡在 consumed。
			common.SysError(fmt.Sprintf(
				"subscription %d for pre-consume record %s is gone; marking the record refunded without returning quota",
				record.UserSubscriptionId, record.RequestId))
		}
		record.Status = "refunded"
		return tx.Save(&record).Error
	})
}

// ResetDueSubscriptions resets subscriptions whose next_reset_time has passed.
func ResetDueSubscriptions(limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := GetDBTimestamp()
	var subs []UserSubscription
	if err := DB.Where("next_reset_time > 0 AND next_reset_time <= ? AND status = ?", now, "active").
		Order("next_reset_time asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	resetCount := 0
	for _, sub := range subs {
		subCopy := sub
		plan, err := getSubscriptionPlanByIdTx(nil, sub.PlanId)
		if err != nil || plan == nil {
			continue
		}
		err = DB.Transaction(func(tx *gorm.DB) error {
			var locked UserSubscription
			if err := lockForUpdate(tx).
				Where("id = ? AND next_reset_time > 0 AND next_reset_time <= ?", subCopy.Id, now).
				First(&locked).Error; err != nil {
				return nil
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &locked, plan, now); err != nil {
				return err
			}
			resetCount++
			return nil
		})
		if err != nil {
			return resetCount, err
		}
	}
	return resetCount, nil
}

// CleanupSubscriptionPreConsumeRecords removes old idempotency records to keep table small.
func CleanupSubscriptionPreConsumeRecords(olderThanSeconds int64) (int64, error) {
	if olderThanSeconds <= 0 {
		olderThanSeconds = 7 * 24 * 3600
	}
	cutoff := GetDBTimestamp() - olderThanSeconds
	res := DB.Where("updated_at < ?", cutoff).Delete(&SubscriptionPreConsumeRecord{})
	return res.RowsAffected, res.Error
}

type SubscriptionPlanInfo struct {
	PlanId    int
	PlanTitle string
}

func GetSubscriptionPlanInfoByUserSubscriptionId(userSubscriptionId int) (*SubscriptionPlanInfo, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	cacheKey := fmt.Sprintf("sub:%d", userSubscriptionId)
	if cached, found, err := getSubscriptionPlanInfoCache().Get(cacheKey); err == nil && found {
		return &cached, nil
	}
	var sub UserSubscription
	if err := DB.Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
		return nil, err
	}
	plan, err := getSubscriptionPlanByIdTx(nil, sub.PlanId)
	if err != nil {
		return nil, err
	}
	info := &SubscriptionPlanInfo{
		PlanId:    sub.PlanId,
		PlanTitle: plan.Title,
	}
	_ = getSubscriptionPlanInfoCache().SetWithTTL(cacheKey, *info, subscriptionPlanInfoCacheTTL())
	return info, nil
}

// SettleUserSubscriptionDelta applies a settlement delta to a subscription and
// reports how much of it actually landed.
//
// PostConsumeUserSubscriptionDelta refuses the whole write when the new used
// amount would exceed amount_total. That is right for a *reservation* (an
// over-budget request must be rejected up front), but wrong for settlement:
// the request has already been served, so refusing the write silently drops
// the uncollected part. The consume log still recorded the full charge, the
// wallet was never touched, and the difference was simply lost (measured:
// billed 14,566, collected 4,200).
//
// So settlement clamps to the subscription's hard cap and returns the applied
// amount; the caller is responsible for collecting the shortfall from the
// wallet. amount_total stays a real ceiling and no money goes missing.
// ClaimSubscriptionWriteOff 领取本重置周期内这张套餐的核销名额。
//
// 返回 true 表示这一次差额可以由平台核销;返回 false 表示这个周期的名额已经
// 被别的并发请求用掉了,调用方必须给这笔钱另找去处(见
// service/funding_source.go)。名额随 amount_used 一起在重置时归零。
//
// 用行锁而不是条件 UPDATE:SQLite 上 lockForUpdate 是空操作,所以额外用
// `WHERE id = ? AND write_off_count = ?` 的 CAS 兜一层,三种数据库上都只会有
// 一个调用方拿到 RowsAffected == 1。
func ClaimSubscriptionWriteOff(userSubscriptionId int) (bool, error) {
	if userSubscriptionId <= 0 {
		return false, errors.New("invalid userSubscriptionId")
	}
	claimed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).
			First(&sub).Error; err != nil {
			return err
		}
		if sub.WriteOffCount > 0 {
			return nil
		}
		res := tx.Model(&UserSubscription{}).
			Where("id = ? AND write_off_count = ?", userSubscriptionId, sub.WriteOffCount).
			Updates(map[string]any{
				"write_off_count": sub.WriteOffCount + 1,
				"updated_at":      common.GetTimestamp(),
			})
		if res.Error != nil {
			return res.Error
		}
		claimed = res.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// SettleUserSubscriptionDelta 把一次结算差额落到订阅上,返回**真正落进去**的那部分。
//
// ═══════════ 订阅行已经不在了:落 0,不报错 ═══════════
//
// 管理端的「删除」按钮(AdminDeleteUserSubscription)是硬删行,而且不理会
// subscription_pre_consume_records 里那条在途记录。请求在途时按下它,结算这一步
// 就会 record not found —— 而这个错误一路冒到 service.SettleBilling 的调用方,
// 那里只打一行日志就继续把**全额**写进消费日志。实测:同一笔消费,对照组收
// 25,010,删行组收 **0**(其中本该由钱包补收的 20,010 一分没扣),两条日志的
// quota 都写 25,010,令牌余额也停在预扣值。
//
// 订阅行没了,套餐这一侧确实无处可落 —— 但那不代表这一笔不用收:撞到
// amount_total 上限之后本来就该由钱包补收(或按闸门核销),而那一段与被删掉的
// 行毫无关系。所以这里回报 applied=0 让调用方照常把整段差额交给钱包出资闸门,
// 而不是把整个结算链一起打断。
//
// **只对 delta > 0(还要再收钱)这个方向宽容。** delta < 0 是把钱退回套餐,
// 行没了就是真的退不回去,而那是"用户少拿钱"的方向 —— 必须原样报错,让调用方
// 保留它的重试/人工对账标记(见 service.RefundTaskQuota:退款失败时不清 task.Quota)。
// 两个方向都宽容的话,一次删行会把一笔本该人工跟进的退款静默记成"已退"。
func SettleUserSubscriptionDelta(userSubscriptionId int, delta int64) (applied int64, err error) {
	if userSubscriptionId <= 0 {
		return 0, errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return 0, nil
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).
			First(&sub).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) && delta > 0 {
				common.SysError(fmt.Sprintf(
					"user subscription %d disappeared before settlement (delta=%d); settling it as applied=0 so the shortfall still reaches the wallet",
					userSubscriptionId, delta))
				applied = 0
				return nil
			}
			return err
		}
		newUsed := sub.AmountUsed + delta
		if newUsed < 0 {
			newUsed = 0
		}
		if sub.AmountTotal > 0 && newUsed > sub.AmountTotal {
			newUsed = sub.AmountTotal
		}
		applied = newUsed - sub.AmountUsed
		if applied == 0 {
			return nil
		}
		sub.AmountUsed = newUsed
		return tx.Save(&sub).Error
	})
	if err != nil {
		return 0, err
	}
	return applied, nil
}

// Update subscription used amount by delta (positive consume more, negative refund).
func PostConsumeUserSubscriptionDelta(userSubscriptionId int, delta int64) error {
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		return postConsumeUserSubscriptionDeltaTx(tx, userSubscriptionId, delta)
	})
}

// postConsumeUserSubscriptionDeltaTx 在**调用方的事务**里改订阅已用量。
//
// 存在的理由是 RefundSubscriptionPreConsume:它此前在自己的事务里直接调
// PostConsumeUserSubscriptionDelta,而后者用的是全局 DB 句柄、另开一个事务。
// 退款因此先独立提交,外层再把幂等记录标成 refunded —— 外层那一步一旦失败
// (连接被 kill、主备切换、代理超时,以及 SQLite 上**必现**的 SQLITE_BUSY:
// 内层提交让外层的读快照失效),外层回滚回滚不掉内层已提交的额度返还,
// 而 record.Status 仍是 consumed,service/funding_source.go 的 refundWithRetry
// 会把整件事重试 3 次,每次都再退一遍(实测一笔 3000 的预扣被退成 9000)。
// 内外同事务之后,退款与幂等标记要么一起成立要么一起不成立;
// 顺带也消掉了「外层持记录锁、内层用另一条连接去拿订阅行锁」的锁序倒挂。
func postConsumeUserSubscriptionDeltaTx(tx *gorm.DB, userSubscriptionId int, delta int64) error {
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return nil
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).
		Where("id = ?", userSubscriptionId).
		First(&sub).Error; err != nil {
		return err
	}
	newUsed := sub.AmountUsed + delta
	if newUsed < 0 {
		newUsed = 0
	}
	if sub.AmountTotal > 0 && newUsed > sub.AmountTotal {
		return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, sub.AmountTotal)
	}
	sub.AmountUsed = newUsed
	return tx.Save(&sub).Error
}
