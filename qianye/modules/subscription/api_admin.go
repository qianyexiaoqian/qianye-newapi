package subscription

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// maxCapacity 是总名额的上界。
//
// 存在的理由不是"谁会卖一亿份",而是别让一个手滑粘贴进来的天文数字变成
// 一个看起来像限量、实际等于不限的配置 —— 那种错误在页面上完全看不出来。
// 取一亿:比任何真实运营场景大好几个数量级,又远离 int 溢出。
const maxCapacity = 100000000

// planUsage 是一个套餐的名额占用与删除影响面。
//
// 一个接口同时回这两组数,而不是拆成"名额"与"影响面"两个:调用它的两个弹窗
// (编辑套餐、删除套餐)各自只用其中一半,但两组数必须来自**同一个时刻** ——
// 分两次请求的话,删除弹窗会拿着 A 时刻的占用数去做 B 时刻的删除判断。
type planUsage struct {
	PlanId int `json:"plan_id"`
	// Capacity 是全站总名额上限,0 = 不限。存在扩展库。
	Capacity int `json:"capacity"`
	// UsedSeats 是当前占用:持有该套餐 status='active' 订阅的**去重人数**,
	// 与 gateSeat 判定时用的是同一个口径,两边永远对得上。
	UsedSeats int64 `json:"used_seats"`
	// ActiveSubscriptions 是订阅**行数**,与 UsedSeats 不是同一个数:
	// 同一个人持有该套餐的两条 active 订阅时占 1 个名额、但会被作废 2 条。
	// 删除弹窗要报的是"多少条会被取消",所以两个都得回。
	ActiveSubscriptions int64 `json:"active_subscriptions"`
	PendingOrders       int64 `json:"pending_orders"`
	// MaxPurchasePerUser 一并回给前端,不是冗余:运营最容易把"每人限购次数"
	// 与"全站总名额"搞混,把两个数字摆在一起是让人一眼分清它们的最省事办法。
	MaxPurchasePerUser int `json:"max_purchase_per_user"`
}

// adminPlanUsage 返回一个套餐的名额占用与删除影响面。
//
// 占用是每次请求现算的,不读 seats.go 那份进程内缓存:那份缓存是给热路径闸门
// 用的,允许滞后 30 秒;管理端必须显示库里真正存着什么,否则运营刚在另一个节点
// 改完再回来看会以为没保存成功。这是管理端低频请求,现算的代价可以忽略。
//
// **是一个纯读接口,没有任何副作用。** 早先的版本在这里顺手清理"孤儿名额配置行"
// (主库已无对应套餐的残留行),那是错的:一个 GET 不该写库,更不该做删除 ——
// 主库读到不完整结果的那一刻(恢复中、读库未追上),存活套餐会被整批误判成孤儿,
// 全站限量配置被静默抹成"不限",而这条路径连审计都不写。孤儿行的清理留在删除
// 接口里(它天然知道自己在删哪个套餐,且本来就是幂等可重试的)。
func adminPlanUsage(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	planId, ok := planIdParam(c)
	if !ok {
		return
	}
	if model.DB == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	var plan model.SubscriptionPlan
	if err := model.DB.WithContext(ctx).Where("id = ?", planId).First(&plan).Error; err != nil {
		badRequest(c, "qy_subscription_plan_not_found", "套餐不存在")
		return
	}

	out := planUsage{PlanId: planId, MaxPurchasePerUser: plan.MaxPurchasePerUser}
	q := model.DB.WithContext(ctx)
	if err := q.Model(&model.UserSubscription{}).
		Where("plan_id = ? AND status = ?", planId, statusActive).
		Distinct("user_id").Count(&out.UsedSeats).Error; err != nil {
		internalError(c, err)
		return
	}
	if err := q.Model(&model.UserSubscription{}).
		Where("plan_id = ? AND status = ?", planId, statusActive).
		Count(&out.ActiveSubscriptions).Error; err != nil {
		internalError(c, err)
		return
	}
	if err := q.Model(&model.SubscriptionOrder{}).
		Where("plan_id = ? AND status = ?", planId, common.TopUpStatusPending).
		Count(&out.PendingOrders).Error; err != nil {
		internalError(c, err)
		return
	}

	row, err := readSeatRow(ctx, planId)
	if err != nil {
		internalError(c, err)
		return
	}
	out.Capacity = row.Capacity
	respond(c, out)
}

// readSeatRow 读一个套餐的名额配置行。没有配过时返回零值行,不是错误。
//
// 刻意不复用 seats.go 的进程内缓存,理由同 adminPlanUsage:管理端要看库里的真值。
func readSeatRow(ctx context.Context, planId int) (PlanSeat, error) {
	gdb := db.Get()
	if gdb == nil {
		return PlanSeat{}, db.ErrNotReady
	}
	var rows []PlanSeat
	if err := gdb.WithContext(ctx).Where("plan_id = ?", planId).Limit(1).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return PlanSeat{}, err
	}
	if len(rows) == 0 {
		return PlanSeat{}, nil
	}
	return rows[0], nil
}

// planIdParam 解析并校验路径上的 plan_id,失败时已经写好响应。
func planIdParam(c *gin.Context) (int, bool) {
	planId64, ok := httpq.PathInt64(c, "plan_id")
	if !ok || planId64 <= 0 || planId64 > math.MaxInt32 {
		badRequest(c, "qy_invalid_param", "套餐 ID 非法")
		return 0, false
	}
	return int(planId64), true
}

type putSeatRequest struct {
	// Capacity 用指针:0 是合法值(取消限量),不能与"字段没传"混为一谈。
	Capacity *int `json:"capacity"`
}

// adminPutSeat 设置一个套餐的全站总名额。
//
// 必写审计:这一项决定这个套餐还能不能卖出去,而且改小之后是立刻生效的。
// 成功与失败各一条 —— 写失败时库里到底变没变是不确定的(连接被掐、部分提交),
// 而"有人在这一刻试图把名额改成 0"这个事实本身与成功的那次同等重要。
func adminPutSeat(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	planId, ok := planIdParam(c)
	if !ok {
		return
	}

	var req putSeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.Capacity == nil {
		badRequest(c, "qy_invalid_param", "缺少 capacity")
		return
	}
	capacity := *req.Capacity
	if capacity < 0 || capacity > maxCapacity {
		badRequest(c, "qy_subscription_seat_invalid",
			fmt.Sprintf("总名额必须介于 0 和 %d 之间,0 表示不限", maxCapacity))
		return
	}
	if model.DB == nil {
		internalError(c, db.ErrNotReady)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	// 套餐必须真实存在。不校验的话,一个手滑打错的 plan_id 会写出一行永远不会被
	// 任何人读到的配置,而页面上完全看不出来 —— 运营只会以为"限量设了但没生效"。
	var plan model.SubscriptionPlan
	if err := model.DB.WithContext(ctx).Where("id = ?", planId).First(&plan).Error; err != nil {
		badRequest(c, "qy_subscription_plan_not_found", "套餐不存在")
		return
	}

	row, err := readSeatRow(ctx, planId)
	if err != nil {
		internalError(c, err)
		return
	}
	before := row.Capacity
	// 值没变就不写库、不写审计。少了这条短路,运营每保存一次套餐表单都会往
	// 扩展库 upsert 一次并留下一条没有实际改动的审计,事后查"谁把名额改小了"
	// 得从一堆空改动里人工筛。
	if before == capacity {
		respond(c, gin.H{"plan_id": planId, "capacity": capacity, "changed": false})
		return
	}

	if err := writeCapacity(ctx, planId, capacity, c.GetInt("id")); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "subscription.plan_seat.update",
			Result: qymodel.ResultFail,
			Reason: fmt.Sprintf("设置套餐 %d(%s)的全站总名额失败: %v", planId, plan.Title, err),
			Before: describeCapacity(before),
			After:  describeCapacity(capacity),
		})
		internalError(c, err)
		return
	}

	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "subscription.plan_seat.update",
		Result: qymodel.ResultOK,
		Reason: fmt.Sprintf("设置套餐 %d(%s)的全站总名额", planId, plan.Title),
		Before: describeCapacity(before),
		After:  describeCapacity(capacity),
	})
	respond(c, gin.H{"plan_id": planId, "capacity": capacity, "changed": true})
}

// describeCapacity 把名额渲染成审计里读得懂的一句话。
//
// 直接写 "0" 的话,审计详情里 0 与"没配过"长得一模一样,而这两件事在事故复盘
// 时的含义完全相反(一个是主动取消限量,一个是从来没限过)。
func describeCapacity(capacity int) string {
	if capacity <= 0 {
		return "不限"
	}
	return fmt.Sprintf("%d 人", capacity)
}

// ───────────────────────────── 响应辅助 ─────────────────────────────
//
// 与扩展其余部分保持同一个信封:{success, message, data},失败时额外带 code
// 供前端做 i18n 映射,不把中文写死在前端。

func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func badRequest(c *gin.Context, code, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": code, "message": msg})
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/subscription: 接口处理失败: " + err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}
