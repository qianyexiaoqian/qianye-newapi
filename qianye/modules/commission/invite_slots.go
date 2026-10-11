package commission

import (
	"context"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/shopspring/decimal"
)

// invite_slots.go —— 回答 invite 模块留给「下线带来多少」的三个槽(export.go 的 func 变量)。
//
// 下线充值 / 兑换码 / 消费带来的返还只走推广佣金这一条线(星屑侧那套按同一笔基数计返的
// invite_* 已下线),所以用户端「我的下线」、管理端关系列表与下线日消费报表里"带来多少 /
// 计了多少"的数字都由本包的计佣行回答。invite 不能 import 本包(会成环),由
// installHooks(consume.go)赋值。
//
// 作废(voided)的行不计:它们是人工判定"不该计"的佣金,算进来就是把已经撤销的钱
// 再报一次。冲正(clawback)行带负数照常相加,得到的是净额。

// pairCommissionTotals 按 (邀请人, 下线) 汇总计佣。读失败返回空表(调用方只是在渲染
// 一个回显数字),并 MarkFailure 让扩展库熔断照常起作用。
func pairCommissionTotals(ctx context.Context, pairs [][2]int) map[[2]int]invite.PairCommission {
	out := make(map[[2]int]invite.PairCommission, len(pairs))
	if len(pairs) == 0 {
		return out
	}
	gdb := db.Get()
	if gdb == nil {
		return out
	}
	wanted := make(map[[2]int]bool, len(pairs))
	inviters := make([]int, 0, len(pairs))
	seen := make(map[int]bool, len(pairs))
	for _, p := range pairs {
		wanted[p] = true
		if !seen[p[0]] {
			seen[p[0]] = true
			inviters = append(inviters, p[0])
		}
	}
	var rows []struct {
		InviterId   int
		InviteeId   int
		SourceType  string
		BaseQuota   int64
		LastDay     string
		GrossAmount decimal.Decimal
	}
	// 按邀请人取(idx_qy_ca_inviter 打头),再在内存里挑出要的那几对:一个邀请人名下的
	// 计佣行按 (下线, 来源) 聚合之后行数是下线数 × 3 的量级。
	if err := gdb.WithContext(ctx).Model(&Accrual{}).
		Select("inviter_id, invitee_id, source_type, COALESCE(SUM(base_quota), 0) AS base_quota, "+
			"MAX(bucket_date) AS last_day, COALESCE(SUM(gross_amount), 0) AS gross_amount").
		Where("inviter_id IN ? AND status <> ?", inviters, StatusVoided).
		Group("inviter_id, invitee_id, source_type").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		return out
	}
	for _, r := range rows {
		key := [2]int{r.InviterId, r.InviteeId}
		if !wanted[key] {
			continue
		}
		agg := out[key]
		agg.Gross = agg.Gross.Add(r.GrossAmount)
		if r.SourceType == SourceConsume {
			agg.ConsumeBaseQuota += r.BaseQuota
			if r.LastDay > agg.LastDay {
				agg.LastDay = r.LastDay
			}
		}
		out[key] = agg
	}
	return out
}

// consumeAccrualByInvitee 汇总 [startDay, endDay] 内每个下线的消费计佣:基数与毛额。
// 全部未作废的状态都算(accrued / settled / risk_hold):报表回答的是"这段消费有多少
// 计了佣金",不是"发出去了多少"。
func consumeAccrualByInvitee(ctx context.Context, startDay, endDay string, inviterId int) (map[int]invite.DayAccrual, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	q := gdb.WithContext(ctx).Model(&Accrual{}).
		Select("invitee_id, COALESCE(SUM(base_quota), 0) AS base_quota, COALESCE(SUM(gross_amount), 0) AS gross").
		Where("source_type = ? AND status <> ? AND bucket_date >= ? AND bucket_date <= ?",
			SourceConsume, StatusVoided, startDay, endDay)
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

// consumeAccrualByDay 汇总一个下线在 [startDay, endDay] 内逐日的消费计佣,按 YYYYMMDD 索引。
func consumeAccrualByDay(ctx context.Context, inviteeId int, startDay, endDay string) (map[string]invite.DayAccrual, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []struct {
		BucketDate string
		BaseQuota  int64
		Gross      decimal.Decimal
	}
	if err := gdb.WithContext(ctx).Model(&Accrual{}).
		Select("bucket_date, COALESCE(SUM(base_quota), 0) AS base_quota, COALESCE(SUM(gross_amount), 0) AS gross").
		Where("invitee_id = ? AND source_type = ? AND status <> ? AND bucket_date >= ? AND bucket_date <= ?",
			inviteeId, SourceConsume, StatusVoided, startDay, endDay).
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
