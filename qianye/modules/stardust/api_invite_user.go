package stardust

import (
	"context"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// api_invite_user.go —— 「我的推广」里星屑账本那一半:下线名单与因他们直接得到的星屑。
//
// 三条都挂在 /api/qy/invite/* 下,但住在本包而不是 invite 包:它们读的是星屑账本,
// 而 invite 不能 import 本包。闸门是 FlagInvite —— 邀请功能关掉时推广页整页 404。
//
// 下线充值 / 兑换码 / 消费带来的返还只走推广佣金(/api/qy/commission/*),这里不再有
// 自己的比例、昨日已返、今日待返基数 —— 两条线各报一套"下线返"正是要消灭的冲突统计。
// 「我的下线」那一列"带来多少佣金"由佣金侧经 invite.PairCommissionTotals 回答。
//
// 隐私边界与旧版一致:下线只以脱敏名出现,真实用户名 / 邮箱一律不下发。

// invitePaging 是这几条列表的分页口径:?p= / ?page_size=(契约与 invite 的管理端同形)。
var invitePaging = httpq.Spec{}

func init() {
	userRouteInstallers = append(userRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/invite/summary", handleInviteSummary)
		g.GET("/invite/invitees", handleInviteInvitees)
		g.GET("/invite/records", handleInviteRecords)
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
// 读不到时回空串,由 groupname.Effective 折成 default。
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

// handleInviteSummary 是推广页星屑那一半的概览:下线数、注册奖 / 套餐返上线的累计,
// 以及已下线的星屑侧下线返留下的历史合计(legacy,> 0 才值得界面提一句)。
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
	var legacy int64
	for _, k := range retiredInviteKinds {
		legacy += totals[k]
	}

	s := effectiveCtx(ctx)
	respondOK(c, gin.H{
		"invitee_count": inviteeCount,
		"blocked_count": blockedCount,
		"totals": gin.H{
			"invite_register": totals[string(KindInviteRegister)],
			"plan_inviter":    totals[string(KindPlanInviter)],
			"legacy":          legacy,
			"all":             all,
		},
		// 注册奖不按分组分档;group 留着给界面说明"我是哪一档的推广人"(佣金按它取档)。
		"rate": gin.H{
			"group":                    groupname.Effective(inviterGroupOf(ctx, me)),
			"invite_register_stardust": s.InviteRegisterStardust,
		},
		"compliance_confirmed": operation_setting.IsPaymentComplianceConfirmed(),
		"day_offset_minutes":   invite.DayOffsetMinutes(),
	})
}

// handleInviteInvitees 分页列出我的下线(绑定中的)。
//
// 每个下线两个数分开下发,不相加:commission_gross 是佣金(含尚未结算进余额的,
// decimal 字符串),total_stardust 是星屑账本上因他直接得到的(注册奖 / 套餐返 / 历史)。
// 两者口径不同(一个还在持有期里、一个已经到账),加成一个数就再也解释不了。
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

	pairs := make([][2]int, 0, len(rels))
	for _, r := range rels {
		pairs = append(pairs, [2]int{me, r.InviteeId})
	}
	commission := invite.PairCommissionTotals(ctx, pairs)
	totals := pairRewardTotals(ctx, pairs)

	items := make([]gin.H, 0, len(rels))
	for _, r := range rels {
		key := [2]int{me, r.InviteeId}
		comm := commission[key]
		items = append(items, gin.H{
			"user_id":          r.InviteeId,
			"username_masked":  r.MaskedName,
			"bound_at":         r.BoundAt,
			"blocked":          r.Blocked,
			"last_active_day":  comm.LastDay,
			"total_base_quota": comm.ConsumeBaseQuota,
			"commission_gross": comm.Gross.String(),
			"total_stardust":   totals[key],
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleInviteRecords 分页返回我的邀请类流水,可按 kind 筛(只认邀请类的五种;
// 其中三种已下线,只剩历史行)。
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
	return slices.Contains(inviteKinds, kind)
}
