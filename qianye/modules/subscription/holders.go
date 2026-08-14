package subscription

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"gorm.io/gorm"
)

// planHolderRow 是一次分组计数的一行。只在本文件内部使用,不出包。
type planHolderRow struct {
	PlanId  int   `gorm:"column:plan_id"`
	Holders int64 `gorm:"column:holders"`
}

// activeHolders 数出一批套餐当前的名额占用:**已激活且尚未到期**的去重人数。
//
// # 为什么这条 SQL 只许有一份
//
// 闸门(gate.go 的 gateSeat)、套餐详情(adminPlanUsage)、列表页(adminPlansUsage)
// 三处全部从这里取数。口径漂移是这类功能最隐蔽的缺陷形状:页面显示"还剩 1 个名额",
// 用户点下去却被拒,或者反过来 —— 两侧各写各的 COUNT 就一定会漂。
//
// # 两个条件缺一不可
//
//	status = 'active'   订阅本身有效。cancelled / expired 不占名额。
//	end_time > now      尚未到期。
//
// end_time 这一条是后补的,补它的理由不是洁癖:上游把到期订阅改成 expired 的
// ExpireDueSubscriptions 是**每分钟一批的后台任务,而且只在 master 节点上跑**
// (service/subscription_reset_task.go 里的 IsMasterNode 判断直接 return)。
// 没有 master 的部署里它压根不跑,到期订阅会永远停在 'active' —— 只看 status 的话,
// 一个限量套餐会被这些早已作废的行永久占满、再也卖不出去,而管理端显示的占用数
// 同样永远是错的,且看不出错在哪。
//
// 补上它不会把"还在用的人"算漏:end_time > now 正是上游自己判定"这条订阅可用"
// 的条件(model/subscription.go 的 GetAllActiveUserSubscriptions、
// PreConsumeUserSubscription 用的都是这一条),所以一条到期但尚未被清扫的订阅
// **确实已经用不了了**,它不该继续占着名额。
//
// COUNT(DISTINCT user_id) 是项目方钉死的口径:同一个人持有该套餐的多条订阅
// 只占 1 个名额。
//
// now 由调用方传进来而不是在这里取:闸门跑在购买事务内部,必须与调用方看到的是
// 同一个时刻;而三处调用统一用 common.GetTimestamp()(应用时钟)而不是
// model.GetDBTimestamp()——后者会在**调用方事务之外**另开一条连接发一次查询,
// 在连接数紧张时能把持有事务的那一方直接堵死(testdb_test.go 里就踩过)。
// 两个时钟的偏差量级是秒,而这个数字的用途是"还剩几个名额",秒级偏差无意义。
func activeHolders(q *gorm.DB, planIds []int, now int64) (map[int]int64, error) {
	out := make(map[int]int64, len(planIds))
	if q == nil || len(planIds) == 0 {
		return out, nil
	}
	rows := make([]planHolderRow, 0, len(planIds))
	if err := holdingSubscriptions(q, planIds, now).
		Select("plan_id, COUNT(DISTINCT user_id) AS holders").
		Group("plan_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.PlanId] = r.Holders
	}
	return out, nil
}

// holdingSubscriptions 是「哪些订阅行正占着名额」这个判据的**唯一**表达式。
//
// 抽出来的理由与 activeHolders 只许有一份是同一条:管理端现在既显示占用人数,
// 又能点开看"具体是哪些人"。这两个视图一旦各写一份 WHERE,迟早会出现
// "列表说 7 个人、点开只列出 5 行"—— 而这是最难向运营解释的一类缺陷:
// 两个数字都由本系统给出、都言之凿凿,却没有任何一处能说明差在哪。
//
// 让它们同源之后,差异在结构上就不可能存在:计数与列表是同一个 WHERE 的
// 两种投影,连 now 都由调用方一次取好往下传(见下面 adminPlanHolders)。
func holdingSubscriptions(q *gorm.DB, planIds []int, now int64) *gorm.DB {
	return q.Model(&model.UserSubscription{}).
		Where("plan_id IN ? AND status = ? AND "+model.SubscriptionActiveEndTimeSQL, planIds, statusActive, now)
}

// holderRow 是下钻列表的一行:一个**人**在该套餐下的占用汇总。
type holderRow struct {
	UserId int `gorm:"column:user_id"`
	// Status 恒为 statusActive —— 它被放进 GROUP BY 而不是写死成常量:
	// 写死的常量在口径改动时不会跟着变(比如以后允许某种宽限期状态也占名额),
	// 而从库里带出来的值永远与 WHERE 一致。同时它也满足 MySQL 的
	// ONLY_FULL_GROUP_BY —— 非聚合列必须出现在 GROUP BY 里。
	Status string `gorm:"column:status"`
	// Subscriptions 是这个人在该套餐下**正占着名额**的订阅条数。
	// 同一个人可能买了多条(续费、补买),它们合起来只占 1 个名额,
	// 所以这个数与"人数"不是同一个口径,必须分别显示。
	Subscriptions int64 `gorm:"column:subscriptions"`
	// FirstStart / LastEnd 是这个人在该套餐下的最早开始与最晚到期。
	// 取聚合而不是任取一行:多条订阅时"他从什么时候开始用、能用到什么时候"
	// 正是这两个端点,而任取一行给出的是一个无法解释的中间值。
	FirstStart int64 `gorm:"column:first_start"`
	LastEnd    int64 `gorm:"column:last_end"`
}

// activeHolderPage 按人聚合地列出某个套餐当前的名额占用者,一页。
//
// 与 activeHolders 共用 holdingSubscriptions,因此**行数天然等于那个人数**:
// COUNT(DISTINCT user_id) 与 GROUP BY user_id 是同一个集合的两种问法。
//
// ORDER BY 写成聚合表达式而不是 SELECT 里的别名:别名排序在 MySQL/PostgreSQL/
// SQLite 上都能用,但三者对"别名与列名同名时先看谁"的规则并不一致,而这里
// 恰好有 end_time / start_time 这样的同源列名。写表达式没有这层歧义。
// 第二排序键 user_id 不是装饰:只按到期时间排的话,同一秒到期的多个人在两页
// 之间的相对顺序由数据库自由决定,翻页时会出现"有人重复出现、有人从没出现过"。
//
// 收 (page, size) 而不是收算好的 (offset, limit):打给数据库的是 (page-1)*size
// 这个乘积,它在整个扩展里只许有 httpq.Offset 一份带上界的实现
// (判据见 qianye/httpq_guard_test.go 的 TestEveryOffsetComesFromSharedHelper)。
// 让调用方把乘法算好再传进来,等于在这里开了第二个接受任意 int 的入口 ——
// 调用方今天夹了,下一个调用方不夹时守卫已经不会报警了。
func activeHolderPage(q *gorm.DB, planId int, now int64, page, size int) ([]holderRow, error) {
	rows := make([]holderRow, 0, size)
	if q == nil || size <= 0 {
		return rows, nil
	}
	err := holdingSubscriptions(q, []int{planId}, now).
		Select("user_id, status, COUNT(*) AS subscriptions, " +
			"MIN(start_time) AS first_start, MAX(end_time) AS last_end").
		Group("user_id, status").
		Order("MAX(end_time) DESC, user_id DESC").
		Offset(httpq.Offset(page, size)).Limit(size).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// holderIdentity 是主库 users 里与"这个人是谁"直接相关的最小字段集。
type holderIdentity struct {
	Id       int    `gorm:"column:id"`
	Username string `gorm:"column:username"`
	// Deleted 由 deleted_at 是否有值决定,见 lookupHolders 的说明。
	Deleted gorm.DeletedAt `gorm:"column:deleted_at"`
}

// lookupHolders 把占用者的 user_id 换成用户名。
//
// # 为什么必须 Unscoped
//
// users 是软删除表(model.User 有 gorm.DeletedAt)。上游删除用户时**不会**动
// user_subscriptions —— 一条 active 且未到期的订阅会继续占着名额,而它的主人
// 在用户管理里已经查不到。默认作用域会让这一行查不出用户名,如果再顺手用
// JOIN 写成一条 SQL,这个人会直接从列表里消失:于是"当前人数 7"点开只有 6 行,
// 而缺的那一个恰恰是最需要被看见的异常(名额被一个已删除账号永久占着)。
//
// 所以这里不 JOIN、单独查、并且 Unscoped:列表的行数由 activeHolderPage 决定,
// 用户名只是往上贴的注解,贴不上就贴不上,行不会少。
//
// 返回值只含 id 与用户名。email / quota / IP / 分组一律不查:管理端确实有权
// 看到用户名与 ID(与 commission 管理端列表同口径 —— 脱敏只针对"用户看别的
// 用户"这个方向),但"能看"不等于"顺手都下发一遍",多下发的每一个字段都是
// 一个日后会被截图外传的隐私面。用户名 + ID 已经足够跳到用户管理页做后续动作。
func lookupHolders(q *gorm.DB, userIds []int) (map[int]holderIdentity, error) {
	out := make(map[int]holderIdentity, len(userIds))
	if q == nil || len(userIds) == 0 {
		return out, nil
	}
	rows := make([]holderIdentity, 0, len(userIds))
	if err := q.Unscoped().Model(&model.User{}).
		Select("id, username, deleted_at").
		Where("id IN ?", userIds).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Id] = r
	}
	return out, nil
}
