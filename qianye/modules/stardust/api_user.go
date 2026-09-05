package stardust

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/gin-gonic/gin"
)

// api_user.go —— 普通用户看自己的星屑:余额 / 流水 / 消费返日桶。三条都只读。
//
// 路由挂在 UserAuth 之后(module.go 的 RegisterUserRoutes),不挂 TokenAuth:
// API Key 是给机器用的、可批量分发,而这里能看到的是一个人的完整账目。
//
// 视图结构体(balanceView / ledgerView / accrualView)同时是管理端列表的基础形状:
// 管理端在它们外面各嵌一层 user_id(见 api_admin_ledger.go),契约里"管理端 items
// 同用户端 + user_id"就是这一层嵌入。两边共用同一份字段名,前端只需要一套类型。

func init() {
	userRouteInstallers = append(userRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/stardust/me", handleGetMe)
		g.GET("/stardust/ledger", handleListMyLedger)
		g.GET("/stardust/accruals", handleListMyAccruals)
	})
}

// balanceView 是余额行的下发形状。carry 是 decimal,按契约下发字符串。
type balanceView struct {
	Available     int64  `json:"available"`
	TotalEarned   int64  `json:"total_earned"`
	TotalSpent    int64  `json:"total_spent"`
	TotalRefunded int64  `json:"total_refunded"`
	TotalAdjusted int64  `json:"total_adjusted"`
	Carry         string `json:"carry"`
	HoldReason    string `json:"hold_reason"`
}

func newBalanceView(b Balance) balanceView {
	return balanceView{
		Available: b.Available, TotalEarned: b.TotalEarned, TotalSpent: b.TotalSpent,
		TotalRefunded: b.TotalRefunded, TotalAdjusted: b.TotalAdjusted,
		Carry: b.Carry.String(), HoldReason: b.HoldReason,
	}
}

// ledgerView 是一行流水的下发形状。刻意不带 id 与 idem_key:前者是主键、后者装着
// 调用方给的原文,两者对用户都没有意义,而 ledger_no 才是用户能拿去对账的单号。
type ledgerView struct {
	LedgerNo     string `json:"ledger_no"`
	Kind         string `json:"kind"`
	Amount       int64  `json:"amount"`
	BalanceAfter int64  `json:"balance_after"`
	RefType      string `json:"ref_type"`
	RefNo        string `json:"ref_no"`
	ActNo        string `json:"act_no"`
	PeerUserId   int    `json:"peer_user_id"`
	Remark       string `json:"remark"`
	CreatedAt    int64  `json:"created_at"`
}

func newLedgerView(l Ledger) ledgerView {
	return ledgerView{
		LedgerNo: l.LedgerNo, Kind: l.Kind, Amount: l.Amount, BalanceAfter: l.BalanceAfter,
		RefType: l.RefType, RefNo: l.RefNo, ActNo: l.ActNo, PeerUserId: l.PeerUserId,
		Remark: l.Remark, CreatedAt: l.CreatedAt,
	}
}

// accrualView 是一行消费返日桶的下发形状。gross 是 decimal,按契约下发字符串。
type accrualView struct {
	BucketDate   string `json:"bucket_date"`
	UserGroup    string `json:"user_group"`
	RateBps      int    `json:"rate_bps"`
	QuotaPerUnit int64  `json:"quota_per_unit"`
	BaseQuota    int64  `json:"base_quota"`
	Gross        string `json:"gross"`
	Status       string `json:"status"`
	HoldReason   string `json:"hold_reason"`
	LedgerId     int64  `json:"ledger_id"`
	ComputedAt   int64  `json:"computed_at"`
	SettledAt    int64  `json:"settled_at"`
}

func newAccrualView(a Accrual) accrualView {
	return accrualView{
		BucketDate: a.BucketDate, UserGroup: a.UserGroup, RateBps: a.RateBps,
		QuotaPerUnit: a.QuotaPerUnit, BaseQuota: a.BaseQuota, Gross: a.Gross.String(),
		Status: a.Status, HoldReason: a.HoldReason, LedgerId: a.LedgerId,
		ComputedAt: a.ComputedAt, SettledAt: a.SettledAt,
	}
}

// handleGetMe 返回我的余额、昨日结算摘要与下一次结算时刻。
//
// 余额行缺失时返回零值行而**不插库**:读接口不该有副作用,而每个从没拿过星屑的
// 用户打开一次页面就建一行,会让余额表塞满永远为 0 的行。行由第一次 Credit /
// Debit 经 LockBalance 建出。
//
// next_settle_at 由后端算:日界与偏移量都是服务端配置(invite.day_offset_minutes
// 与 stardust.settle_delay_minutes),前端没有任何一个能自己算对它的输入。
func handleGetMe(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	me := c.GetInt("id")
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	bal := Balance{UserId: me}
	var balRows []Balance
	if err := gdb.WithContext(ctx).Where("user_id = ?", me).Limit(1).Find(&balRows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("读取余额", err))
		return
	}
	if len(balRows) == 1 {
		bal = balRows[0]
	}

	// "昨天"按结算日界算,不按 UTC 自然日:昨天的桶就是下一次结算要结的那一个。
	now := common.GetTimestamp()
	cfg := config.Get().Stardust
	nextSettleAt := invite.NextDayStart(now) + int64(cfg.SettleDelayMinutes)*60
	yesterdayKey := invite.DayKey(invite.DayStart(now) - 1)

	var accRows []Accrual
	if err := gdb.WithContext(ctx).Where("user_id = ? AND bucket_date = ?", me, yesterdayKey).
		Limit(1).Find(&accRows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("读取昨日日桶", err))
		return
	}
	var yesterday any
	if len(accRows) == 1 {
		a := accRows[0]
		// settled_amount 取流水行上真正发出去的整数,而不是 gross 取整:两者差着 carry
		// 那一截余数,用户看到的"到账"必须与流水页上那一行逐字相同。
		settled := int64(0)
		if a.LedgerId > 0 {
			var led []Ledger
			if err := gdb.WithContext(ctx).Select("amount").Where("id = ?", a.LedgerId).
				Limit(1).Find(&led).Error; err != nil {
				db.MarkFailure(err)
				respondErr(c, wrapInternal("读取昨日结算流水", err))
				return
			}
			if len(led) == 1 {
				settled = led[0].Amount
			}
		}
		yesterday = gin.H{
			"bucket_date":    a.BucketDate,
			"base_quota":     a.BaseQuota,
			"gross":          a.Gross.String(),
			"status":         a.Status,
			"settled_amount": settled,
			"hold_reason":    a.HoldReason,
			"rate_bps":       a.RateBps,
			"quota_per_unit": a.QuotaPerUnit,
		}
	}

	var heldCount int64
	if err := gdb.WithContext(ctx).Model(&Accrual{}).
		Where("user_id = ? AND status = ?", me, AccrualHeld).Count(&heldCount).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计暂缓日桶", err))
		return
	}

	respondOK(c, gin.H{
		"name":               effectiveCtx(ctx).Name,
		"quota_per_unit":     QuotaPerUnit(),
		"balance":            newBalanceView(bal),
		"next_settle_at":     nextSettleAt,
		"yesterday":          yesterday,
		"pending_held_count": heldCount,
	})
}

// handleListMyLedger 分页返回我的流水,可按 kind 筛。
//
// kind 先过 kindColumn:未知的 kind 直接 400,而不是拼进 WHERE 返回一页空表 ——
// 空表的表现是"这个人没有这类流水",与"你拼错了枚举"必须能分开。
func handleListMyLedger(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	me := c.GetInt("id")
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&Ledger{}).Where("user_id = ?", me)
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		if _, err := kindColumn(Kind(kind)); err != nil {
			respondErr(c, errBadRequest("未知的流水种类: "+kind))
			return
		}
		q = q.Where("kind = ?", kind)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计我的流水", err))
		return
	}
	rows := make([]Ledger, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询我的流水", err))
		return
	}
	items := make([]ledgerView, 0, len(rows))
	for _, r := range rows {
		items = append(items, newLedgerView(r))
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// handleListMyAccruals 分页返回我的消费返日桶,最近的桶在前。
func handleListMyAccruals(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	me := c.GetInt("id")
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&Accrual{}).Where("user_id = ?", me)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计我的日桶", err))
		return
	}
	rows := make([]Accrual, 0, size)
	if err := q.Order("bucket_date desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询我的日桶", err))
		return
	}
	items := make([]accrualView, 0, len(rows))
	for _, r := range rows {
		items = append(items, newAccrualView(r))
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
