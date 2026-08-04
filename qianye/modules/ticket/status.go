package ticket

// 工单状态。四个,不多不少。
//
//	open          待客服首次响应(刚建单)
//	replied       客服已回复,**在等用户**
//	user_replied  用户在客服回复之后又追加,**在等客服**
//	closed        已关闭(终态)
//
// open 与 user_replied 都是"在等客服",为什么不合并成一个 pending?
// 因为客服队列要能把它们分开看:open 是还没有人碰过的新单(首响时长的分母),
// user_replied 是已经在对话中、对方在等回音的单。合成一个之后,"今天有多少
// 新问题进来"这个每天都要回答的问题就再也算不出来了。
const (
	StatusOpen        = "open"
	StatusReplied     = "replied"
	StatusUserReplied = "user_replied"
	StatusClosed      = "closed"
)

// PriorityLow ... PriorityUrgent 是工单等级(需求原文的「等级」)。
//
// 白名单写死在代码里而不是配置里:每一档都对应前端的一种徽章色与队列排序权重,
// 配置里加一个 "p0" 而代码不认识它,结果是徽章渲染成灰色、排序权重取零 ——
// 一个看起来生效了、实际上什么都没做的等级。能配置的东西必须是代码已实现的东西。
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

// priorityRank 是队列排序权重,越大越靠前。
//
// 排序在 SQL 里做不了(varchar 的字典序是 high < low < normal < urgent,
// 恰好完全错乱),所以列表接口按 `priority = 'urgent' DESC, ...` 这种
// 多列表达式排会在三种数据库上写法各异。这里的做法是:SQL 只按时间排,
// 等级筛选走等值条件,权重只用于**前端展示排序之外**的判断(见 view.go)。
var priorityRank = map[string]int{
	PriorityLow: 0, PriorityNormal: 1, PriorityHigh: 2, PriorityUrgent: 3,
}

// knownPriorities 是筛选参数的白名单,同时也是建单时的合法取值集合。
// 由 priorityRank 派生而不是另写一份:两处各写一遍就是"同一概念的第二份拷贝"。
func knownPriority(p string) bool {
	_, ok := priorityRank[p]
	return ok
}

// Priorities 供 /ticket/config 下发给前端渲染等级选择器。
//
// 顺序即前端选项顺序,从低到高 —— 与 priorityRank 的取值一致,但这里必须是
// 一个显式切片:map 的遍历顺序在 Go 里是随机的,直接从 priorityRank 派生
// 会让选择器每次刷新都换一个顺序。
func Priorities() []string {
	return []string{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent}
}

// allowedTransitions 是状态机的唯一真相。缺席即非法。
//
// 三条设计判断,每一条都对应一个具体的坏结局:
//
//  1. **closed 只有管理员能打开**(closed → open,见 api_admin.go 的 reopen)。
//     用户侧不给出边:给了的话,"未关闭工单数上限"这道闸就能被"关掉再开回来"
//     绕过,而那是本模块最重要的一道闸。用户要继续问,新开一张单 ——
//     那才是客服队列想要的粒度。
//
//  2. **closed 之后不能追加消息**。这不是状态机的边,而是 reply.go 的前置判断,
//     但语义在这里:关单是一次"这件事到此为止"的双方共识,允许单方面在终态上
//     继续追加,等于让关单按钮什么都没关掉。
//
//  3. **open → open / replied → replied 不是合法边**。同一方连续追加不改变
//     "在等谁",因此那种情况根本不走状态机(见 appendMessage 里的 nextStatus)。
//     把它写成一条自环边只会让"状态没变"和"状态被非法地改成了自己"长得一样。
var allowedTransitions = map[string]map[string]bool{
	StatusOpen: {
		StatusReplied: true, // 客服首次回复
		StatusClosed:  true, // 用户或管理员关单
	},
	StatusReplied: {
		StatusUserReplied: true, // 用户追加
		StatusClosed:      true, // 用户或管理员关单;auto_close_days 到期也走这条
	},
	StatusUserReplied: {
		StatusReplied: true, // 客服再次回复
		StatusClosed:  true,
	},
	StatusClosed: {
		StatusOpen: true, // 仅管理员重开
	},
}

// canTransit 判断一次状态跃迁是否合法。
func canTransit(from, to string) bool { return allowedTransitions[from][to] }

// openStatuses 是"未关闭"的切片投影,供 SQL 的 `status IN ?` 使用。
//
// 由状态常量派生而不是各处各写一遍字面量:max_open_per_user 那道闸、
// 自动关闭扫描、管理端角标三处都要用它,漏掉一个状态的后果分别是
// "上限算少了""该关的没关""角标对不上"。
var openStatuses = []string{StatusOpen, StatusReplied, StatusUserReplied}

// knownStatus 是筛选参数的白名单,拒绝把任意字符串拼进 SQL 的可能。
func knownStatus(s string) bool {
	switch s {
	case StatusOpen, StatusReplied, StatusUserReplied, StatusClosed:
		return true
	default:
		return false
	}
}

// 关单发起方。ClosedBy 用它,不用 ActorType —— system 不是一种 actor 类型。
const (
	ClosedByUser   = "user"
	ClosedByAdmin  = "admin"
	ClosedBySystem = "system"
)
