package stardust

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/shopspring/decimal"
)

// invite_stats.go —— 回答 invite 模块留的三个槽(export.go 的 func 变量):
// 按 (邀请人, 下线) 汇总的星屑、下线消费返按下线 / 按天的汇总。
//
// invite 不能 import 本包(qianye/module_import_guard_test.go),它的管理端关系列表
// 与下线日消费报表要的这几个数只有星屑账本答得出,所以由本包在 InstallHooks 里赋值。

func init() {
	hookInstallers = append(hookInstallers, func() {
		invite.PairRewardTotals = pairRewardTotals
		invite.InviteAccrualByInvitee = inviteAccrualByInvitee
		invite.InviteAccrualByDay = inviteAccrualByDay
	})
}

// inviteKinds 是"邀请人因下线得到"的全部流水种类:用户端「我的推广」与管理端关系列表
// 的星屑口径都取它。
var inviteKinds = []string{
	string(KindInviteConsume), string(KindInviteTopup), string(KindInviteRedeem),
	string(KindInviteRegister), string(KindPlanInviter),
}

// pairRewardTotals 按 (user_id=邀请人, peer_user_id=下线) 汇总邀请类流水。
//
// invite_consume 那一行是按邀请人汇总发的,peer_user_id 恒 0 —— 它**不在**这里:
// 下线消费返按下线拆开只能看日桶(inviteAccrualTotalsByInvitee),这里把两者相加。
// 读失败返回空表而不是错误:调用方要么已经改完了主库,要么只是在渲染一个列表,
// 都不该为一个回显数字失败。
func pairRewardTotals(ctx context.Context, pairs [][2]int) map[[2]int]int64 {
	out := make(map[[2]int]int64, len(pairs))
	if len(pairs) == 0 {
		return out
	}
	gdb := db.Get()
	if gdb == nil {
		return out
	}
	wanted := make(map[[2]int]bool, len(pairs))
	invitees := make([]int, 0, len(pairs))
	seen := make(map[int]bool, len(pairs))
	for _, p := range pairs {
		wanted[p] = true
		if !seen[p[1]] {
			seen[p[1]] = true
			invitees = append(invitees, p[1])
		}
	}
	var rows []struct {
		UserId     int
		PeerUserId int
		Total      int64
	}
	if err := gdb.WithContext(ctx).Model(&Ledger{}).
		Select("user_id, peer_user_id, COALESCE(SUM(amount), 0) AS total").
		Where("peer_user_id IN ? AND kind IN ?", invitees, inviteKinds).
		Group("user_id, peer_user_id").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		return out
	}
	for _, r := range rows {
		key := [2]int{r.UserId, r.PeerUserId}
		if wanted[key] {
			out[key] += r.Total
		}
	}
	// 下线消费返:已结算日桶的 gross 向下取整。零头在 invite_carry 里等下一次发,
	// 列表上的数宁可略小于账本也不能虚报。
	var accs []struct {
		InviterId int
		InviteeId int
		Gross     string
	}
	if err := gdb.WithContext(ctx).Model(&InviteAccrual{}).
		Select("inviter_id, invitee_id, COALESCE(SUM(gross), 0) AS gross").
		Where("invitee_id IN ? AND status = ?", invitees, AccrualSettled).
		Group("inviter_id, invitee_id").Scan(&accs).Error; err != nil {
		db.MarkFailure(err)
		return out
	}
	for _, a := range accs {
		key := [2]int{a.InviterId, a.InviteeId}
		if !wanted[key] {
			continue
		}
		d, err := decimal.NewFromString(a.Gross)
		if err != nil {
			continue
		}
		out[key] += int64(common.QuotaFromDecimal(d.Floor()))
	}
	return out
}

// inviteAccrualByInvitee 汇总 [startDay, endDay] 内每个下线的下线消费返基数与毛额。
// 全部状态都算(computed / settled / held):报表回答的是"这段消费有多少进了日桶",
// 不是"发出去了多少"。
func inviteAccrualByInvitee(ctx context.Context, startDay, endDay string, inviterId int) (map[int]invite.DayAccrual, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	q := gdb.WithContext(ctx).Model(&InviteAccrual{}).
		Select("invitee_id, COALESCE(SUM(base_quota), 0) AS base_quota, COALESCE(SUM(gross), 0) AS gross").
		Where("bucket_date >= ? AND bucket_date <= ?", startDay, endDay)
	if inviterId > 0 {
		q = q.Where("inviter_id = ?", inviterId)
	}
	var rows []struct {
		InviteeId int
		BaseQuota int64
		Gross     decimal.Decimal
	}
	if err := q.Group("invitee_id").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	out := make(map[int]invite.DayAccrual, len(rows))
	for _, r := range rows {
		out[r.InviteeId] = invite.DayAccrual{BaseQuota: r.BaseQuota, Gross: r.Gross}
	}
	return out, nil
}

// inviteAccrualByDay 汇总一个下线在 [startDay, endDay] 内逐日的下线消费返,按 YYYYMMDD 索引。
func inviteAccrualByDay(ctx context.Context, inviteeId int, startDay, endDay string) (map[string]invite.DayAccrual, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []struct {
		BucketDate string
		BaseQuota  int64
		Gross      decimal.Decimal
	}
	if err := gdb.WithContext(ctx).Model(&InviteAccrual{}).
		Select("bucket_date, COALESCE(SUM(base_quota), 0) AS base_quota, COALESCE(SUM(gross), 0) AS gross").
		Where("invitee_id = ? AND bucket_date >= ? AND bucket_date <= ?", inviteeId, startDay, endDay).
		Group("bucket_date").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	out := make(map[string]invite.DayAccrual, len(rows))
	for _, r := range rows {
		out[r.BucketDate] = invite.DayAccrual{BaseQuota: r.BaseQuota, Gross: r.Gross}
	}
	return out, nil
}
