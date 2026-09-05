package model

// qy_stardust_export.go —— 星屑模块专用的 hook 声明。
//
// 与 qy_export.go / qy_invite_export.go 一样是纯新增文件。
// 铁律:本文件禁止 import 任何 qianye/* 包。model 是底层包,扩展依赖它,
// 反向依赖会成环。实现由 qianye.Init() 注入,运行期禁止改写。

// QyOnUserRegistered 在一个新账号**建号完成之后**触发(两条注册路径各一处:
// finishInsert 与 FinalizeOAuthUserCreation),载荷是新账号 id 与注册时解析到的
// 邀请人 id(0 = 没有邀请人)。
//
// 它放在上游"邀请奖励"那个 `if inviterId != 0 && IsPaymentComplianceConfirmed()`
// 块**之外**、无条件调用:上游只多一行、不多一个分支,守卫最简单;是否有邀请人、
// 是否受支付合规门约束,全部由扩展侧自己判。管理员建号经 FinishInsert(0) 也到达
// 同一行,inviterId=0 自然不发。
//
// 默认实现 no-op,因此扩展未安装时两条注册路径与接入前逐字一致。
var QyOnUserRegistered = func(userId int, inviterId int) {}

// QySubscriptionGrant 是"一份订阅刚刚发出去"这个事实的最小快照。
type QySubscriptionGrant struct {
	UserId         int
	PlanId         int
	SubscriptionId int
	// Source 是上游既有的来源标记:order / balance / admin / redemption。
	Source string
	// TradeNo 是 order / balance 两条付费路径的订单号(subscription_orders.trade_no),
	// 其余来源为空。扩展侧拿它做幂等键。
	TradeNo string
	// RedemptionId 只在 source=redemption 时非 0。
	RedemptionId int
	// PriceAmount 是套餐售价(美元),Money 是 order 路径的实付金额快照(其余来源为 0)。
	PriceAmount float64
	Money       float64
	// Renewed 表示这次没有新建订阅行,而是续期 / 转永久 / 永久组已持有(钱已收、货没变)。
	// 判据是"返回的订阅行早于本次事务开始就存在";同一秒内建行的误判无害 —— 它只是标记。
	Renewed bool
}

// QyOnSubscriptionGranted 在四条购买路径的**事务提交之后**触发:支付回调
// (CompleteSubscriptionOrder)、余额购买(PurchaseSubscriptionWithBalance)、
// 管理员绑定(AdminBindSubscription)、套餐兑换码(Redeem)。
//
// 为什么不是扫表:同组续期只 UPDATE 既有行的 end_time 并返回同一条订阅
// (applyUserGroupPurchaseRulesTx),user_subscriptions 上没有新行也没有状态跃迁,
// 任何 id 低水位都看不到它;而 subscription_orders 与 user_subscriptions 之间又没有
// 引用列。只有事件本身知道"这次发的是哪条、是不是续期"。
//
// 只能在事务提交之后调用;默认实现 no-op。
var QyOnSubscriptionGranted = func(g QySubscriptionGrant) {}

// qySubscriptionGrant 在四个调用点把散落的量收成一份快照,让每处只多一行。
func qySubscriptionGrant(sub *UserSubscription, plan *SubscriptionPlan, source, tradeNo string,
	redemptionId int, money float64, startedAt int64) QySubscriptionGrant {
	g := QySubscriptionGrant{Source: source, TradeNo: tradeNo, RedemptionId: redemptionId, Money: money}
	if plan != nil {
		g.PlanId = plan.Id
		g.PriceAmount = plan.PriceAmount
	}
	if sub != nil {
		g.UserId = sub.UserId
		g.SubscriptionId = sub.Id
		g.Renewed = sub.CreatedAt < startedAt
		if g.PlanId == 0 {
			g.PlanId = sub.PlanId
		}
	}
	return g
}
