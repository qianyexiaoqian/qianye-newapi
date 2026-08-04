package subscription

import (
	"github.com/QuantumNous/new-api/model"

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
	if err := q.Model(&model.UserSubscription{}).
		Select("plan_id, COUNT(DISTINCT user_id) AS holders").
		Where("plan_id IN ? AND status = ? AND end_time > ?", planIds, statusActive, now).
		Group("plan_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.PlanId] = r.Holders
	}
	return out, nil
}
