package stardust

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// api_invite_user.go —— 「我的推广」:邀请人看自己的下线与因他们得到的星屑(D-14)。
//
// 四条都挂在 /api/qy/invite/* 下,但住在本包而不是 invite 包:它们读的全是星屑账本
// (流水、下线消费返日桶、分组档),而 invite 不能 import 本包。闸门是 FlagInvite ——
// 邀请功能关掉时推广页整页 404,这是契约定的。
//
// 隐私边界与旧版一致:下线只以脱敏名出现,真实用户名 / 邮箱一律不下发;下线 id 按
// 契约下发(前端要用它下钻某个下线的逐日明细)。

// invitePaging 是这四条列表的分页口径:?p= / ?page_size=(契约与 invite 的管理端同形)。
var invitePaging = httpq.Spec{}

// maxPendingInvitees 是"我的下线今天花了多少"一次最多看多少个下线:超过就不算了
// (下发 0)—— 一个几千下线的推广人每刷新一次页面就扫一遍 logs,不值。
const maxPendingInvitees = 500

func init() {
	userRouteInstallers = append(userRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/invite/summary", handleInviteSummary)
		g.GET("/invite/invitees", handleInviteInvitees)
		g.GET("/invite/records", handleInviteRecords)
		g.GET("/invite/invitee-daily", handleInviteInviteeDaily)
	})
}

// inviteTotals 按 kind 汇总一个邀请人的邀请类流水。
func inviteTotals(ctx context.Context, userId int) (map[string]int64, int64, error) {
	gdb := db.Get()
	var rows []struct {
		Kind  string
		Total int64
	}
	if err := gdb.WithContext(ctx).Model(&Ledger{}).
		Select("kind, COALESCE(SUM(amount), 0) AS total").
		Where("user_id = ? AND kind IN ?", userId, inviteKinds).
		Group("kind").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, 0, err
	}
	out := make(map[string]int64, len(inviteKinds))
	for _, k := range inviteKinds {
		out[k] = 0
	}
	var all int64
	for _, r := range rows {
		out[r.Kind] = r.Total
		all += r.Total
	}
	return out, all, nil
}

// inviterGroupOf 读邀请人自己的账号分组(users.group),给"我现在走哪一档"用。
// 读不到时回空串,由 groupname.Effective 折成 default —— 界面上写的档要与账本按同一条
// 路径算出来,而账本那一侧(InviteeEligible)读不到上线时也不按任何分组档发。
func inviterGroupOf(ctx context.Context, userId int) string {
	if model.DB == nil {
		return ""
	}
	var rows []struct{ Group string }
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("group").Where("id = ?", userId).Limit(1).Scan(&rows).Error; err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].Group
}

// handleInviteSummary 是推广页的概览。
func handleInviteSummary(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	ctx := c.Request.Context()
	me := c.GetInt("id")
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	var inviteeCount, blockedCount int64
	if err := gdb.WithContext(ctx).Model(&invite.InviteRelation{}).
		Where("inviter_id = ? AND unbound_at = ?", me, 0).Count(&inviteeCount).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计下线", err))
		return
	}
	if err := gdb.WithContext(ctx).Model(&invite.InviteRelation{}).
		Where("inviter_id = ? AND unbound_at = ? AND blocked = ?", me, 0, true).Count(&blockedCount).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计被拉黑的下线", err))
		return
	}

	totals, all, err := inviteTotals(ctx, me)
	if err != nil {
		respondErr(c, wrapInternal("汇总邀请返流水", err))
		return
	}

	// 昨天:按日界算,昨天的桶就是刚结完(或正等着结)的那一批。
	now := common.GetTimestamp()
	yesterday := invite.DayKey(invite.DayStart(now) - 1)
	var yRows []InviteAccrual
	if err := gdb.WithContext(ctx).Where("inviter_id = ? AND bucket_date = ?", me, yesterday).
		Find(&yRows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("读取昨日下线消费返", err))
		return
	}
	var yBase int64
	yHeld := 0
	ledgerNos := map[string]bool{}
	for _, r := range yRows {
		yBase += r.BaseQuota
		if r.Status == AccrualHeld {
			yHeld++
		}
		if r.LedgerNo != "" {
			ledgerNos[r.LedgerNo] = true
		}
	}
	// granted 取流水行上真正发出去的整数,而不是 gross 取整:两者差着 invite_carry
	// 那一截余数,用户看到的"到账"必须与流水页上那一行逐字相同。
	var yGranted int64
	if len(ledgerNos) > 0 {
		nos := make([]string, 0, len(ledgerNos))
		for no := range ledgerNos {
			nos = append(nos, no)
		}
		if err := gdb.WithContext(ctx).Model(&Ledger{}).
			Select("COALESCE(SUM(amount), 0)").Where("ledger_no IN ?", nos).Scan(&yGranted).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("读取昨日结算流水", err))
			return
		}
	}

	pendingRows, _, err := pendingTodayInviteeConsume(ctx, me, now)
	if err != nil {
		respondErr(c, wrapInternal("汇总下线今日消费", err))
		return
	}
	var pendingBase int64
	for _, r := range pendingRows {
		pendingBase += r.BaseQuota
	}

	group := inviterGroupOf(ctx, me)
	s := effectiveCtx(ctx)
	respondOK(c, gin.H{
		"invitee_count": inviteeCount,
		"blocked_count": blockedCount,
		"totals": gin.H{
			"invite_consume":  totals[string(KindInviteConsume)],
			"invite_topup":    totals[string(KindInviteTopup)],
			"invite_redeem":   totals[string(KindInviteRedeem)],
			"invite_register": totals[string(KindInviteRegister)],
			"plan_inviter":    totals[string(KindPlanInviter)],
			"all":             all,
		},
		"yesterday": gin.H{
			"base_quota": yBase,
			"granted":    yGranted,
			"held":       yHeld,
		},
		"pending_today_base_quota": pendingBase,
		// 档位按**我自己**的分组解析(D-02),与发放路径同一条判定(inviteRateFor /
		// inviteConsumeRateFor);合规未确认时三档比例都是 0,界面据 compliance_confirmed 解释。
		"rate": gin.H{
			"group":                    groupname.Effective(group),
			"invite_consume_bps":       inviteConsumeRateFor(ctx, group),
			"invite_topup_bps":         firstOf(inviteRateFor(ctx, group, inviteTopupRate)),
			"invite_redeem_bps":        firstOf(inviteRateFor(ctx, group, inviteRedeemRate)),
			"invite_register_stardust": s.InviteRegisterStardust,
		},
		"compliance_confirmed": operation_setting.IsPaymentComplianceConfirmed(),
		"day_offset_minutes":   invite.DayOffsetMinutes(),
	})
}

// firstOf 只取 inviteRateFor 的比例那一半,分组名在 rate.group 里已经给过了。
func firstOf(bps int, _ string) int { return bps }

// pendingTodayInviteeConsume 是"我的下线今天到现在各花了多少"——明天日结的基数,
// 按下线一行。第二个返回值为假表示下线太多、这一次没扫。
//
// 口径与日结逐字相同(aggregateDayConsume 的排除项),只是限定在我名下绑定中、
// 没被拉黑的下线。"没扫"与"扫了、都是 0"必须由调用方分得开:推广页把两者
// 一起显示成 0,而「明日预计到账」要把前者说成"下线过多,未统计"。
func pendingTodayInviteeConsume(ctx context.Context, me int, now int64) ([]consumeAgg, bool, error) {
	gdb := db.Get()
	ids := make([]int, 0, 64)
	if err := gdb.WithContext(ctx).Model(&invite.InviteRelation{}).
		Where("inviter_id = ? AND unbound_at = ? AND blocked = ?", me, 0, false).
		Limit(maxPendingInvitees+1).Pluck("invitee_id", &ids).Error; err != nil {
		db.MarkFailure(err)
		return nil, false, err
	}
	if len(ids) > maxPendingInvitees {
		return nil, false, nil
	}
	if len(ids) == 0 {
		return nil, true, nil
	}
	start := invite.DayStart(now)
	rows, err := aggregateDayConsume(ctx, start, start+secondsPerDay, ids)
	if err != nil {
		return nil, true, err
	}
	return rows, true, nil
}

// handleInviteInvitees 分页列出我的下线(绑定中的),带累计基数与累计星屑。
func handleInviteInvitees(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	ctx := c.Request.Context()
	me := c.GetInt("id")
	page, size := httpq.Paginate(c, invitePaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&invite.InviteRelation{}).Where("inviter_id = ? AND unbound_at = ?", me, 0)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计下线", err))
		return
	}
	rels := make([]invite.InviteRelation, 0, size)
	if err := q.Order("bound_at desc, invitee_id desc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rels).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询下线", err))
		return
	}

	ids := make([]int, 0, len(rels))
	pairs := make([][2]int, 0, len(rels))
	for _, r := range rels {
		ids = append(ids, r.InviteeId)
		pairs = append(pairs, [2]int{me, r.InviteeId})
	}
	type inviteeAgg struct {
		InviteeId int
		BaseQuota int64
		LastDay   string
	}
	aggs := map[int]inviteeAgg{}
	if len(ids) > 0 {
		var rows []inviteeAgg
		if err := gdb.WithContext(ctx).Model(&InviteAccrual{}).
			Select("invitee_id, COALESCE(SUM(base_quota), 0) AS base_quota, MAX(bucket_date) AS last_day").
			Where("inviter_id = ? AND invitee_id IN ?", me, ids).
			Group("invitee_id").Scan(&rows).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("汇总下线消费", err))
			return
		}
		for _, r := range rows {
			aggs[r.InviteeId] = r
		}
	}
	totals := pairRewardTotals(ctx, pairs)

	items := make([]gin.H, 0, len(rels))
	for _, r := range rels {
		agg := aggs[r.InviteeId]
		items = append(items, gin.H{
			"user_id":          r.InviteeId,
			"username_masked":  r.MaskedName,
			"bound_at":         r.BoundAt,
			"blocked":          r.Blocked,
			"last_active_day":  agg.LastDay,
			"total_base_quota": agg.BaseQuota,
			"total_stardust":   totals[[2]int{me, r.InviteeId}],
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleInviteRecords 分页返回我的邀请类流水,可按 kind 筛(只认邀请类的五种)。
func handleInviteRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	ctx := c.Request.Context()
	me := c.GetInt("id")
	page, size := httpq.Paginate(c, invitePaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&Ledger{}).Where("user_id = ?", me)
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		if !isInviteKind(kind) {
			respondErr(c, errBadRequest("未知的邀请返种类: "+kind))
			return
		}
		q = q.Where("kind = ?", kind)
	} else {
		q = q.Where("kind IN ?", inviteKinds)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计邀请返流水", err))
		return
	}
	rows := make([]Ledger, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询邀请返流水", err))
		return
	}

	// 下线的脱敏名从关系快照取:invite_consume 是按邀请人汇总发的(peer 恒 0),拿不到就给 0 / 空。
	peerIds := make([]int, 0, len(rows))
	for _, r := range rows {
		if r.PeerUserId > 0 {
			peerIds = append(peerIds, r.PeerUserId)
		}
	}
	masked := map[int]string{}
	if len(peerIds) > 0 {
		var rels []invite.InviteRelation
		if err := gdb.WithContext(ctx).Select("invitee_id", "masked_name").
			Where("invitee_id IN ?", peerIds).Find(&rels).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("读取下线脱敏名", err))
			return
		}
		for _, rel := range rels {
			masked[rel.InviteeId] = rel.MaskedName
		}
	}
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{
			"ledger_no":      r.LedgerNo,
			"kind":           r.Kind,
			"amount":         r.Amount,
			"invitee_id":     r.PeerUserId,
			"invitee_masked": masked[r.PeerUserId],
			"ref_no":         r.RefNo,
			"remark":         r.Remark,
			"created_at":     r.CreatedAt,
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

func isInviteKind(kind string) bool {
	for _, k := range inviteKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// handleInviteInviteeDaily 是某一天我名下每个下线的消费返明细(?day=YYYYMMDD,默认昨天)。
func handleInviteInviteeDaily(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	ctx := c.Request.Context()
	me := c.GetInt("id")
	day := strings.TrimSpace(c.Query("day"))
	if day == "" {
		day = invite.DayKey(invite.DayStart(common.GetTimestamp()) - 1)
	}
	if _, ok := invite.DayKeyStart(day); !ok {
		respondErr(c, errBadRequest("day 必须是 YYYYMMDD"))
		return
	}
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	rows := make([]InviteAccrual, 0, 32)
	if err := gdb.WithContext(ctx).Where("inviter_id = ? AND bucket_date = ?", me, day).
		Order("base_quota desc, invitee_id asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询下线日消费返", err))
		return
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.InviteeId)
	}
	masked := map[int]string{}
	if len(ids) > 0 {
		var rels []invite.InviteRelation
		if err := gdb.WithContext(ctx).Select("invitee_id", "masked_name").
			Where("invitee_id IN ?", ids).Find(&rels).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("读取下线脱敏名", err))
			return
		}
		for _, rel := range rels {
			masked[rel.InviteeId] = rel.MaskedName
		}
	}
	var totalBase int64
	totalGross := decimal.Zero
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		totalBase += r.BaseQuota
		totalGross = totalGross.Add(r.Gross)
		items = append(items, gin.H{
			"invitee_id":     r.InviteeId,
			"invitee_masked": masked[r.InviteeId],
			"base_quota":     r.BaseQuota,
			"bps":            r.Bps,
			"gross":          r.Gross.String(),
			"status":         r.Status,
		})
	}
	respondOK(c, gin.H{
		"day":              day,
		"items":            items,
		"total_base_quota": totalBase,
		"total_gross":      totalGross.String(),
	})
}
