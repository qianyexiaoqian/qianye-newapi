package stardust

import (
	"strings"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
)

// api_admin_user_summary.go —— 「结算台 → 佣金用户 → 星屑总览」:每个用户的星屑按来源拆开。
//
// 项目方要求:显示所有用户的星屑数量,区分「自身返还」与「下游返还」。来源只能从流水
// 种类上分(余额行上的四个累计列按记账方向分,不按来源分),归类如下:
//
//	自身返还  consume_rebate / plan_buyer —— 他自己消费、自己买套餐返给他自己的
//	下游返还  invite_* / plan_inviter / commission_credit —— 因为他的下线而得到的
//	          (邀请返与佣金入账是两条线,但都是「下游给的」,这一列合在一起)
//	已花费    lot_stake / mall_order(流水是负数,这里取正)
//	其他      其余全部:活动派奖、两种退款、管理员手调(带符号,按净额)。
//	          新增的流水种类默认落在这里,不会凭空从总账里消失。
//
// 恒等式:可用 = 自身返还 + 下游返还 + 其他 − 已花费(I1 保证可用 == 流水之和)。

var (
	sdSelfEarnedKinds     = []string{string(KindConsumeRebate), string(KindPlanBuyer)}
	sdDownlineEarnedKinds = []string{
		string(KindInviteConsume), string(KindInviteTopup), string(KindInviteRedeem),
		string(KindInviteRegister), string(KindPlanInviter), string(KindCommissionCredit),
	}
	sdSpentKinds = []string{string(KindLotStake), string(KindMallOrder)}
)

// sdUserSummarySorts 是可排序列的白名单:前端传的是 key,SQL 里拼的永远是这里的常量。
var sdUserSummarySorts = map[string]string{
	"available": "available",
	"self":      "self_earned",
	"downline":  "downline_earned",
	"other":     "other_earned",
	"spent":     "spent",
}

type sdUserSummaryRow struct {
	UserId         int   `json:"user_id"`
	Available      int64 `json:"available"`
	SelfEarned     int64 `json:"self_earned"`
	DownlineEarned int64 `json:"downline_earned"`
	OtherEarned    int64 `json:"other_earned"`
	Spent          int64 `json:"spent"`
}

type sdUserSummaryView struct {
	sdUserSummaryRow
	Username string `json:"username"`
}

func init() {
	adminRouteInstallers = append(adminRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/stardust/user-summary", handleAdminUserSummary)
	})
}

// handleAdminUserSummary 分页返回每个有星屑余额行的用户的来源拆分,可按 keyword 检索、
// 按任一列降序排序(默认按可用星屑)。
func handleAdminUserSummary(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	orderCol, ok := sdUserSummarySorts[strings.TrimSpace(c.DefaultQuery("sort", "available"))]
	if !ok {
		respondErr(c, errBadRequest("未知的排序列"))
		return
	}

	balances := gdb.WithContext(ctx).Model(&Balance{})
	var ids []int
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		var err error
		if ids, err = keywordUserIds(ctx, c, kw); err != nil {
			respondErr(c, err)
			return
		}
		if len(ids) == 0 {
			respondOK(c, gin.H{"items": make([]sdUserSummaryView, 0), "total": 0, "page": page, "page_size": size})
			return
		}
		balances = balances.Where("user_id IN ?", ids)
	}
	var total int64
	if err := balances.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计余额行", err))
		return
	}

	// 一条聚合查询:余额行 LEFT JOIN 流水,按种类分桶求和。没有流水的余额行四列都是 0。
	notOther := append(append(append([]string{}, sdSelfEarnedKinds...), sdDownlineEarnedKinds...), sdSpentKinds...)
	q := gdb.WithContext(ctx).Table(Balance{}.TableName()+" AS b").
		Select(`b.user_id AS user_id, b.available AS available,
			COALESCE(SUM(CASE WHEN l.kind IN ? THEN l.amount ELSE 0 END), 0) AS self_earned,
			COALESCE(SUM(CASE WHEN l.kind IN ? THEN l.amount ELSE 0 END), 0) AS downline_earned,
			COALESCE(SUM(CASE WHEN l.kind IS NOT NULL AND l.kind NOT IN ? THEN l.amount ELSE 0 END), 0) AS other_earned,
			COALESCE(SUM(CASE WHEN l.kind IN ? THEN -l.amount ELSE 0 END), 0) AS spent`,
			sdSelfEarnedKinds, sdDownlineEarnedKinds, notOther, sdSpentKinds).
		Joins("LEFT JOIN " + Ledger{}.TableName() + " AS l ON l.user_id = b.user_id")
	if len(ids) > 0 {
		q = q.Where("b.user_id IN ?", ids)
	}
	rows := make([]sdUserSummaryRow, 0, size)
	if err := q.Group("b.user_id, b.available").
		Order(orderCol + " DESC, user_id DESC").
		Offset(httpq.Offset(page, size)).Limit(size).
		Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("汇总星屑来源", err))
		return
	}

	pageIds := make([]int, 0, len(rows))
	for _, r := range rows {
		pageIds = append(pageIds, r.UserId)
	}
	names, err := usernamesOf(ctx, pageIds)
	if err != nil {
		respondErr(c, err)
		return
	}
	items := make([]sdUserSummaryView, 0, len(rows))
	for _, r := range rows {
		items = append(items, sdUserSummaryView{sdUserSummaryRow: r, Username: names[r.UserId]})
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
