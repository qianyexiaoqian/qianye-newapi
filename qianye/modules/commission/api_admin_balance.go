package commission

// api_admin_balance.go —— 佣金余额总览。
//
// # 这张表的三个星屑列是什么关系
//
// qy_commission_balance 上的三个整数列不是三个独立的数字,它们受同一条恒等式约束:
//
//	可用(available) + 已入账(credited)
//	    = 累计已结算(total_earned) − 累计冲正(total_clawback)
//
// 三条写入路径全都按这条恒等式成对改列:结算(settle.go)同时加 available 与
// total_earned;冲正同时减 available、加 total_clawback;自动入账(autocredit.go)
// 把 available 直接搬进 credited。所以列表页必须把这条式子显式摊给运营看 ——
// 只给一个"可用"数字,"为什么这个人有佣金却还没入账"这个问题永远要靠翻代码回答。
//
// D-15 时这里还有第四列「在途(frozen)」:佣金记星辉,入账要跨库,存在"已开单、
// 主库还没落定"的中间态。D-16 佣金改记星屑之后入账是扩展库里的本地事务,那一列
// 连同它背后的资金单、探针与人工裁决一起退役(autocredit.go 的文件头写了全过程)。
//
// 本文件只读:没有任何"直接改余额列"的接口 —— 手工增减走 manual 计佣行
// (api_admin_adjust.go),入账由系统自动完成。

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// balanceSortOrders 是列表排序的白名单。
//
// 一律带 user_id 做次级排序:仅按金额排时,金额相同的行在 MySQL 里没有稳定
// 顺序,翻页会漏行也会重复行。
var balanceSortOrders = map[string]string{
	"available": "available desc, user_id asc",
	"credited":  "credited desc, user_id asc",
	"earned":    "total_earned desc, user_id asc",
	"updated":   "updated_at desc, user_id asc",
	"user":      "user_id asc",
}

const defaultBalanceSort = "available"

// balanceView 是一行佣金余额对管理端的形状。
//
// DerivedAvailable / LedgerDrift 是刻意冗余的:它们能由前四列算出来,但让**后端**
// 算并下发,前端就不会出现第二份算法。
type balanceView struct {
	UserId   int    `json:"user_id"`
	Username string `json:"username"`
	// UserResolved 为假表示主库里读不到这个 id(账号已删,或这一次主库读失败)。
	UserResolved bool `json:"user_resolved"`

	Available     int64 `json:"available"`
	Credited      int64 `json:"credited"`
	TotalEarned   int64 `json:"total_earned"`
	TotalClawback int64 `json:"total_clawback"`

	// DerivedAvailable = 已结算 − 已冲正 − 已入账,即恒等式给出的可用。
	DerivedAvailable int64 `json:"derived_available"`
	// LedgerDrift = 实际可用 − 派生可用。非 0 就是账本坏了,必须先查清楚再动。
	LedgerDrift int64 `json:"ledger_drift"`

	// 余数用字符串下发:decimal(30,10) 到了 JS 的 Number 里会丢位。
	UnsettledAmount string `json:"unsettled_amount"`

	DebtBlocked bool `json:"debt_blocked"`
	// InviteeCount 一律由 hydrateBalanceViews 现算(qy_invite_relation 里
	// unbound_at = 0 的行数),与「佣金总表」页同一份口径。
	InviteeCount   int   `json:"invitee_count"`
	LastSettledAt  int64 `json:"last_settled_at"`
	LastCreditedAt int64 `json:"last_credited_at"`
	UpdatedAt      int64 `json:"updated_at"`
}

func newBalanceView(b Balance) balanceView {
	derived := b.TotalEarned - b.TotalClawback - b.Credited
	return balanceView{
		UserId:           b.UserId,
		Available:        b.Available,
		Credited:         b.Credited,
		TotalEarned:      b.TotalEarned,
		TotalClawback:    b.TotalClawback,
		DerivedAvailable: derived,
		LedgerDrift:      b.Available - derived,
		UnsettledAmount:  b.UnsettledAmount.String(),
		DebtBlocked:      b.DebtBlocked,
		LastSettledAt:    b.LastSettledAt,
		LastCreditedAt:   b.LastCreditedAt,
		UpdatedAt:        b.UpdatedAt,
	}
}

// adminListBalances 分页列出全平台佣金余额。
//
// 只按 user_id / username 精确定位,不做模糊搜索:模糊搜索要么在扩展库里
// LIKE 一个不存在的用户名列,要么再往主库里塞一份 LIKE 转义实现。
func adminListBalances(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, listPaging)

	userId := httpq.Int(c, "user_id", 0)
	if name := strings.TrimSpace(c.Query("username")); name != "" {
		resolved, err := findUserIdByName(ctx, name)
		if err != nil {
			internalError(c, err)
			return
		}
		if resolved == 0 || (userId > 0 && userId != resolved) {
			// 用户名查无此人(或与同时给出的 user_id 互相矛盾)。回空页而不是
			// 忽略这个条件 —— 忽略掉筛选条件返回全表,看起来与"这个人排在第一页"一模一样。
			respond(c, gin.H{
				"items": []balanceView{}, "total": 0, "p": page, "page_size": size,
				"totals": gin.H{"available": 0, "credited": 0},
			})
			return
		}
		userId = resolved
	}

	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	q := gdb.WithContext(ctx).Model(&Balance{})
	if userId > 0 {
		q = q.Where("user_id = ?", userId)
	}
	if c.Query("debt_only") == "true" {
		q = q.Where("debt_blocked = ?", true)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		internalError(c, err)
		return
	}

	// 合计跟着同一组筛选走。对账问的就是"这批人一共还挂着多少可用、已经入账了
	// 多少",逐页心算是不可行的。
	var sums struct {
		Available int64
		Credited  int64
	}
	if err := q.Session(&gorm.Session{}).
		Select("COALESCE(SUM(available), 0) AS available, " +
			"COALESCE(SUM(credited), 0) AS credited").
		Scan(&sums).Error; err != nil {
		db.MarkFailure(err)
		internalError(c, err)
		return
	}

	order := balanceSortOrders[c.Query("sort")]
	if order == "" {
		order = balanceSortOrders[defaultBalanceSort]
	}
	var rows []Balance
	if err := q.Order(order).Offset(httpq.Offset(page, size)).Limit(size).
		Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		internalError(c, err)
		return
	}

	respond(c, gin.H{
		"items":     hydrateBalanceViews(ctx, rows),
		"total":     total,
		"p":         page,
		"page_size": size,
		"totals": gin.H{
			"available": sums.Available,
			"credited":  sums.Credited,
		},
	})
}

// findUserIdByName 精确解析用户名到主库 id。查无此人返回 0(不是错误)。
func findUserIdByName(ctx context.Context, name string) (int, error) {
	if model.DB == nil {
		return 0, db.ErrNotReady
	}
	var row struct{ Id int }
	err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id").Where("username = ?", name).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.Id, nil
}

// hydrateBalanceViews 回主库补用户名。
//
// 主库读失败时整页标 UserResolved=false 而不是把名字留空:分不清"这个账号没了"
// 和"这次没读到"就等于在盲改。
func hydrateBalanceViews(ctx context.Context, rows []Balance) []balanceView {
	views := make([]balanceView, 0, len(rows))
	for _, r := range rows {
		views = append(views, newBalanceView(r))
	}
	if len(views) == 0 || model.DB == nil {
		return views
	}

	ids := make([]int, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.UserId)
	}
	var users []model.User
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		common.SysError("qianye/commission: 回读佣金余额对应主库用户失败: " + err.Error())
		return views
	}
	names := make(map[int]string, len(users))
	for _, u := range users {
		names[u.Id] = u.Username
	}
	downlines := downlineCounts(ctx, ids)
	for i := range views {
		if name, ok := names[views[i].UserId]; ok {
			views[i].Username = name
			views[i].UserResolved = true
		}
		views[i].InviteeCount = downlines[views[i].UserId]
	}
	return views
}

// resolvedBalanceView 是单行版的 hydrateBalanceViews。
//
// 手工增减佣金那条路(以及它的审计前后快照)必须用它而不是 newBalanceView:
// 后者是从**扩展库**的余额行造出来的形状,username 恒为空串、user_resolved 恒为
// false,而 user_resolved=false 有明确语义("主库里读不到这个 id")。
func resolvedBalanceView(ctx context.Context, b Balance) balanceView {
	views := hydrateBalanceViews(ctx, []Balance{b})
	if len(views) == 0 {
		return newBalanceView(b)
	}
	return views[0]
}

// downlineCounts 数每个上线名下还在绑的下线条数。
//
// 与 api_admin_users.go 同一份口径(qy_invite_relation 里 unbound_at = 0)。
// 读不到就返回空表:少一个展示数字远好于让整页余额打不开。
func downlineCounts(ctx context.Context, inviterIds []int) map[int]int {
	out := map[int]int{}
	gdb := db.Get()
	if gdb == nil || len(inviterIds) == 0 {
		return out
	}
	var rows []struct {
		InviterId int
		Cnt       int
	}
	if err := gdb.WithContext(ctx).Model(&invite.InviteRelation{}).
		Select("inviter_id, COUNT(*) AS cnt").
		Where("inviter_id IN ? AND unbound_at = 0", inviterIds).
		Group("inviter_id").Scan(&rows).Error; err != nil {
		common.SysError("qianye/commission: 统计佣金余额对应下线数失败: " + err.Error())
		return out
	}
	for _, r := range rows {
		out[r.InviterId] = r.Cnt
	}
	return out
}

// currentBalance 尽力回读余额行,读不到就回零值。
// 只用于失败路径的 after 快照:那条快照回答"库里现在到底是什么",
// 读不到本身也是一种回答,不该因此把审计整条丢掉。
func currentBalance(ctx context.Context, userId int) Balance {
	bal, err := currentBalanceRow(ctx, userId)
	if err != nil {
		return Balance{UserId: userId}
	}
	return bal
}

// registerBalanceRoutes 挂载余额总览。列表做两次全表聚合,挂搜索限流。
func registerBalanceRoutes(g *gin.RouterGroup) {
	g.GET("/commission/balances", middleware.SearchRateLimit(), adminListBalances)
}
