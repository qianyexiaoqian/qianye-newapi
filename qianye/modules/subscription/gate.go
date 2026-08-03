package subscription

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

// 上游 CreateUserSubscriptionFromPlanTx 的 source 取值,以及预检模式自己的标记。
//
// sourceOrder 单独拎出来是**资金安全的判据**而不是日志分类:它意味着调用点是
// 支付回调,用户的钱已经付掉了。判定见 gateSeat 的 §四。
const (
	sourceOrder    = "order"
	sourcePrecheck = "precheck"
)

// gateSeat 是 model.QyGateSubscriptionSeat 的实现:全站总名额闸门。
//
// ═══════════════════════ 一、它跑在什么地方 ═══════════════════════
//
// 两种模式,靠 tx 是否为 nil 区分:
//
//	tx != nil  强一致模式。跑在 model.CreateUserSubscriptionFromPlanTx 内部,
//	           也就是订阅创建的主库事务里。三条生产路径全部经过它:
//	           支付回调(source="order")、管理员绑定("admin")、余额购买("balance")。
//	tx == nil  预检模式(source="precheck")。跑在四个支付网关的下单 HTTP handler
//	           里,纯体验层,目的是别让用户"付完钱才被告知没名额"。
//
// ═══════════════ 二、跨库判定:变的那一半必须留在主库 ═══════════════
//
// 名额判定要两个数:上限(capacity)和占用(used)。
//
//	capacity 在扩展库(另一个 MySQL 实例)。它是运营配置,一天可能改零次。
//	used     在主库 user_subscriptions。它每成交一单就变一次。
//
// 关键取舍:**只有 capacity 走跨库,used 一定要用调用方的 tx 去数。**
// 用别的连接数 used 的话,同一个事务里"先数后插"的自洽性就没了 —— 数的时候
// 看不见自己刚插的行(甚至看不见同一批重试里刚插的行),重复购买会被漏放行。
// 反过来,capacity 是慢变量,缓存 30 秒完全可以接受(见下面的残余风险 R2)。
//
// 所以本函数在事务内**只做主库查询**,扩展库那一次读走的是 seats.go 的进程内
// 缓存,绝大多数调用连一次网络往返都没有。
//
// ═══════════════════ 三、能不能完全防住超卖?不能 ═══════════════════
//
// 说清楚为止,不含糊:
//
// R1 —— 并发超卖(主要残余风险)。
//
//	两个购买事务同时到达闸门,各自 COUNT 出 used = capacity-1,都判定通过,
//	都插入 → 实际占用 capacity+1。因为 COUNT 是一致性读(非锁定读),
//	它不会阻塞对方,也不会看见对方未提交的行。
//
//	为什么不加锁把它彻底堵死 —— 两条能想到的路都比病更糟:
//
//	  (a) 把 COUNT 改成锁定读(SELECT ... FOR UPDATE)。
//	      PostgreSQL 直接拒绝聚合查询上的 FOR UPDATE(报 "FOR UPDATE is not
//	      allowed with aggregate functions"),而 AGENTS.md 要求三种数据库同时
//	      可用,所以这条在跨库层面就不成立。退一步说,即便只在 MySQL 上开,
//	      它会锁住 plan_id 索引上的整段区间 —— 而 PreConsumeUserSubscription
//	      在 relay 热路径上对同一批 user_subscriptions 行加 FOR UPDATE。
//	      拿购买去和每一次模型调用抢行锁,换来的是热路径死锁。
//
//	  (b) 在 subscription_plans 那一行上加 FOR UPDATE,把同套餐的购买串行化。
//	      这条会**造出一个死锁环**:余额购买的加锁顺序是 users → plans
//	      (PurchaseSubscriptionWithBalance 先 lockForUpdate 用户行,再进闸门),
//	      而支付回调是 plans → users(闸门在前,getUserGroupByIdTx 在后)。
//	      两条路径互为反向,MySQL 会回滚其中一个 —— 被回滚的若是支付回调,
//	      结果就是"钱收了、订阅发不出、订单卡在 pending",比多卖一个名额严重得多。
//
//	超卖的量级:溢出上限 = 同一套餐的购买事务中,COUNT 与 COMMIT 窗口相互重叠的
//	并发数。那个窗口是一次 INSERT 的宽度(毫秒级),而套餐购买是人操作出来的
//	(支付回调、后台绑定),现实中溢出 0~2 个。
//
//	为什么可以接受:溢出的后果是业务性的(限量套餐多进来一两个人),不是资损 ——
//	没有任何一笔钱被算错、被多扣或被少扣。而且它是**可见且自愈**的:管理端列表
//	的 used 是实时数出来的,超了会直接显示成 used > capacity;下一次购买因为
//	COUNT 已经超过上限而被正确拒绝,不会持续扩大。
//
// R2 —— capacity 的缓存滞后(次要)。
//
//	运营在 A 节点把名额从 200 调到 50,B 节点最多 30 秒内仍按 200 放行
//	(cacheSeconds)。写入节点自己是立刻生效的(storeCapacity 写穿缓存)。
//	上界明确、会自动收敛,且"调小名额"这个动作本身通常发生在已经卖满之后,
//	此时 used >= 新上限,两个数字都拦得住。
//
// R3 —— 扩展库不可用时放行(刻意的)。
//
//	读不到 capacity 就当"不限"。这是本仓的铁律:扩展绝不能拖垮主业务。
//	反过来 fail-closed 的代价是灾难性的 —— 闸门也跑在支付回调里,扩展库打个嗝
//	就会让一批已付款的订单永远发不出订阅。
//
// R4 —— "active" 的口径包含已过期但尚未被后台任务标记的订阅。
//
//	占用口径由项目方钉死为 status='active'(不叠加 end_time > now)。上游的
//	ExpireDueSubscriptions 是后台批量把到期订阅改成 expired 的,存在分钟级延迟,
//	这段时间里那些人仍然占着名额。这是刻意的:闸门与管理端页面用同一个口径,
//	两边永远对得上;而"少放一个人进来"远好过"名额已经被算回收了、其实人还在用"。
//
// ═════════ 四、支付回调永不拒绝(这条比上面所有取舍都重要)═════════
//
// source == "order" 时,即便判定出**确实满员**也必须放行。
//
// 这一条极容易被漏掉,因为满员是"闸门正常工作"的结果,看起来正是该拒绝的时候。
// 但 source=="order" 的调用点是 CompleteSubscriptionOrder,它在把订单写成
// success **之前**创建订阅。这里返回错误 → 整个事务回滚 → 订单仍是 pending、
// 钱已经收了、订阅发不出;网关按重试策略反复回调,每一次都撞同一条死路,
// 而且没有任何退款或降级路径。
//
// 也就是说,fail-closed 在这一档制造出来的,正是本模块删除套餐那一侧拼命要
// 堵住的"后果 2"的同一个形状。R3(扩展库不可用时放行)讲的是"读不到就放行",
// 这一条讲的是"读到了、判满了,在这条路径上仍然放行" —— 两者缺一不可。
//
// 那名额还怎么守住?靠下单前的预检(四个网关 handler 里的 tx==nil 调用):
// 用户在收银台之前就被拦下,钱还没出。预检与回调之间的那段时间里名额被别人占掉
// 的话,这一单会溢出 —— 溢出的后果是业务性的(多进来一个人),而拒绝的后果是
// 资损性的(卡死一笔已付款订单)。两者不在一个量级。溢出在管理端页面上可见
// (used > capacity),下一笔购买会被正确拒绝,不会持续扩大。
func gateSeat(tx *gorm.DB, plan *model.SubscriptionPlan, userId int, source string, inErr error) (out error) {
	// 入参 err 优先级最高:上游那些错误(套餐不存在、周期算不出来)语义上先于
	// 名额判定,吞掉它们会把"套餐不存在"变成"名额已满"这种指错方向的报错。
	out = inErr
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf(
				"qianye/subscription: 名额闸门发生 panic(已拦截,购买流程按放行处理): %v", r))
			out = inErr
		}
	}()
	if inErr != nil {
		return inErr
	}
	if plan == nil || plan.Id <= 0 || userId <= 0 {
		return nil
	}
	planId := plan.Id

	// 预检模式下停用的套餐直接放行:调用点紧随其后就是上游自己的
	// `if !plan.Enabled { "套餐未启用" }`。这里抢先报"名额已满"的话,一个
	// 已下架且恰好卖满的套餐会让用户和客服去追一个不存在的名额问题,
	// 真实原因(已下架)被盖住。强一致模式不做这个判断 —— 管理员绑定停用套餐
	// 是上游允许的动作,名额该拦还是要拦。
	if source == sourcePrecheck && !plan.Enabled {
		return nil
	}

	capacity := capacityOf(planId)
	if capacity <= 0 {
		return nil
	}

	// 强一致模式直接用调用方的事务句柄,**刻意不套超时 ctx**:ctx 到期时
	// go-sql-driver 无法与服务端重新同步协议,会把连接标记为坏连接,而那条连接
	// 正是调用方事务所在的连接 —— 整个购买事务随之报 "driver: bad connection"。
	// 一个本该 fail-open 的名额判定就变成了把已付款的支付回调打挂。事务内的语句
	// 本来就受调用方事务的生命周期约束(上游其余语句同样没有单独预算)。
	// 真正需要硬上界的是跨库那一跳,它在 seats.go 的 readAllSeats 里有自己的 ctx。
	//
	// 预检模式没有事务可以毒化,给它一个预算是纯收益。
	q := tx
	if q == nil {
		if model.DB == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), loadBudget())
		defer cancel()
		q = model.DB.WithContext(ctx)
	}

	// 第一问:这个人是不是已经占着这个套餐的名额了?
	//
	// 占用口径是**去重人数**,所以老用户续订/再买一份不额外消耗名额。
	// 这一问必须在总数判定之前:名额卖满之后,已经在里面的人续订应当照常成功,
	// 否则"限量套餐"会变成"限量且到期即出局",而那不是运营要的东西。
	var mine int64
	if err := q.Model(&model.UserSubscription{}).
		Where("plan_id = ? AND user_id = ? AND status = ?", planId, userId, statusActive).
		Count(&mine).Error; err != nil {
		return failOpen(planId, err)
	}
	if mine > 0 {
		return nil
	}

	// 第二问:全站还剩位置吗?COUNT(DISTINCT user_id) 是项目方钉死的口径 ——
	// 同一个人的多条 active 订阅只占 1 个名额。
	var used int64
	if err := q.Model(&model.UserSubscription{}).
		Where("plan_id = ? AND status = ?", planId, statusActive).
		Distinct("user_id").
		Count(&used).Error; err != nil {
		return failOpen(planId, err)
	}
	if used < int64(capacity) {
		return nil
	}
	if source == sourceOrder {
		// 已付款,只能放行(见 §四)。这条日志是运营事后核对"哪几单溢出了名额"
		// 的唯一线索,不限频:它的触发上界是超卖笔数,本来就该逐笔看见。
		common.SysError(fmt.Sprintf(
			"qianye/subscription: 套餐 %d 的全站名额已满(上限 %d 人),但用户 %d 的这一单已完成支付,"+
				"按放行处理以免订单卡死在 pending;实际占用将超出上限,请在管理端确认",
			planId, capacity, userId))
		return nil
	}
	return fmt.Errorf("该套餐全站名额已满(上限 %d 人),暂时无法购买", capacity)
}

// failOpen 把一次数不出来的主库查询按"放行"处理并告警。
//
// 为什么放行而不是拒绝:数不出来说明主库此刻有问题,而主库有问题时整个购买
// 事务本来就会失败 —— 由它自己的错误去失败,比让用户看到一句指错方向的
// "名额已满"要好。
//
// 不做日志限频:这条路径的触发频率上界等于购买频率(人操作出来的),
// 主库真的坏了的话,这几行日志远不是最吵的那个。限频反而会在真正需要
// 逐笔核对"哪几单是被放过去的"时把证据抹掉。
func failOpen(planId int, err error) error {
	common.SysError(fmt.Sprintf(
		"qianye/subscription: 统计套餐 %d 的名额占用失败,本次购买按放行处理: %v", planId, err))
	return nil
}
