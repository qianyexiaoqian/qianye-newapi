package stardust

import (
	"context"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settle_invite.go —— 下线消费返的日结(design-15 §4.7,D-14)。
//
// 它与消费返(settle.go)在**同一次 run** 里跑、用同一份 logs 聚合、同一把日界,
// 但账本形状不同:消费返一天一人一桶,这里一天一对 (邀请人, 下线) 一桶;消费返按
// 消费者自己的分组取档,这里按**邀请人**的分组取档(D-02);余数各自结转
// (qy_sd_balance.carry / invite_carry),两条恒等式(I2 / I3)各自闭合。
//
// 每个邀请人的结算同样是一个扩展库事务:锁余额行 → 事务内 FOR UPDATE 重读候选桶 →
// floor(invite_carry + Σgross) → Credit(kind=invite_consume) → 逐桶 CAS 成 settled →
// 余数回写。暂缓判据(透支 / 注销 / 封禁)读的是**邀请人**的主库快照 —— 钱是发给他的。

const (
	inviteSettleRefType   = "sd_invite"
	inviteSettleIdemScope = "sd_invite"
)

// inviteIdemKey 是下线消费返给每个邀请人的流水幂等键:日结 sdinvite:<run_date>:<inviter_id>,
// 重跑 sdinviterr:<D>:<inviter_id>:<nonce>。与消费返的键分族,理由同 settleKey.idemKey。
func (k settleKey) inviteIdemKey(inviterId int) string {
	if k.Rerun {
		return "sdinviterr:" + k.Day + ":" + strconv.Itoa(inviterId) + ":" + k.Nonce
	}
	return "sdinvite:" + k.RunDate + ":" + strconv.Itoa(inviterId)
}

// inviteConsumeRateFor 解析下线消费返的档位:合规门 → 邀请人分组档的 invite_consume_bps
// → 回落全站 stardust.invite_consume_bps。分组表那一档不经 effective(),合规门要再判一次,
// 否则"分组表配了 10%"就是一条绕过合规门的路(与 inviteRateFor 同一条理由)。
func inviteConsumeRateFor(ctx context.Context, inviterGroup string) int {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return 0
	}
	if r, ok := groupRateFor(ctx, inviterGroup); ok && r.InviteConsumeBps != nil {
		return *r.InviteConsumeBps
	}
	return effective().InviteConsumeBps
}

// settleInviteDay 是下线消费返那一段:按 D 的 logs 聚合重算日桶,再按邀请人排空候选集。
//
// 结果写进 st.Invite*;重算失败或选人失败只记 Note 与 InviteFailed,不改消费返那一段
// 已经算好的数 —— 两段各自成败,合在一起才决定这一跑是 done 还是 partial。
func settleInviteDay(ctx context.Context, day string, key settleKey, rows []consumeAgg, st *settleStats) {
	if err := recomputeInviteDay(ctx, day, rows); err != nil {
		st.InviteFailed++
		st.Note = "重算 " + day + " 的下线消费返失败: " + err.Error()
		warnf("%s", st.Note)
		return
	}
	cursor := 0
	for rounds := 0; rounds < settleDrainMaxRounds; rounds++ {
		if ctx.Err() != nil {
			st.Aborted = true
			st.Note = "租约中途丢失,下线消费返队列未排空"
			return
		}
		ids, err := candidateInviters(ctx, day, cursor, settleUserBatch)
		if err != nil {
			st.InviteFailed++
			st.Note = "下线消费返选人失败: " + err.Error()
			warnf("%s", st.Note)
			return
		}
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				st.Aborted = true
				st.Note = "租约中途丢失,下线消费返队列未排空"
				return
			}
			out, err := settleInviter(ctx, id, day, key)
			switch {
			case err != nil:
				st.InviteFailed++
				warnf("邀请人 %d 的下线消费返结算失败(本日稍后重试): %v", id, err)
			case out.Held:
				st.InviteHeld++
			case out.Skipped:
				st.InviteSkipped++
			default:
				st.InviteProcessed++
				st.InviteGranted += out.Net
			}
		}
		cursor = ids[len(ids)-1]
	}
	st.InviteFailed++
	st.Note = "下线消费返排空达到轮次上界,剩余队列本日稍后重试"
	warnf("%s 的下线消费返排空达到轮次上界 %d,队列未排空", day, settleDrainMaxRounds)
}

// recomputeInviteDay 重算 D 的下线消费返日桶:DELETE 当天 computed 行,再按每个消费者
// 解析上线、按上线分组取档、DoNothing 插入。
//
// 与消费返不同,档位与 gross 在**这里**就冻结而不是在结算事务里:一个邀请人名下有几十个
// 下线,每个下线一行,档位是邀请人的 —— 同一跑里对同一个邀请人只解析一次分组即可,
// 放进事务只会让每一行都重算一遍。(U,D) 已存在的(held / settled)跳过不重算,
// 已 held 的桶 gross 冻结于首次计算值,rerun 不追溯。bps=0 时不写行。
func recomputeInviteDay(ctx context.Context, day string, rows []consumeAgg) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	qpu := QuotaPerUnit()
	now := common.GetTimestamp()
	// 同一跑里同一个邀请人的档位只解析一次:分组档缓存在进程内,但 InviteeEligible
	// 每次都要回一次(可能已缓存的)主库拿上线分组,几十个下线共用一份结果就够了。
	type inviterRate struct {
		group string
		bps   int
	}
	rates := map[int]inviterRate{}
	buckets := make([]InviteAccrual, 0, len(rows))
	for _, r := range rows {
		if r.BaseQuota <= 0 {
			continue
		}
		match, err := invite.InviteeEligible(ctx, r.UserId)
		if err != nil {
			return fmt.Errorf("解析下线 %d 的邀请关系: %w", r.UserId, err)
		}
		if !match.Eligible() {
			continue
		}
		rate, ok := rates[match.InviterId]
		if !ok {
			rate = inviterRate{
				group: groupname.Effective(match.InviterGroup),
				bps:   inviteConsumeRateFor(ctx, match.InviterGroup),
			}
			rates[match.InviterId] = rate
		}
		if rate.bps <= 0 {
			continue
		}
		buckets = append(buckets, InviteAccrual{
			InviterId: match.InviterId, InviteeId: r.UserId, BucketDate: day,
			BaseQuota: r.BaseQuota, RateGroup: rate.group, Bps: rate.bps, QuotaPerUnit: qpu,
			Gross: grossOf(r.BaseQuota, rate.bps, qpu), Status: AccrualComputed,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("bucket_date = ? AND status = ?", day, AccrualComputed).
			Delete(&InviteAccrual{}).Error; err != nil {
			return err
		}
		if len(buckets) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "inviter_id"}, {Name: "invitee_id"}, {Name: "bucket_date"}},
			DoNothing: true,
		}).CreateInBatches(&buckets, 500).Error
	})
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("重算 "+day+" 的下线消费返日桶", err)
	}
	return nil
}

// candidateInviters 取一页候选邀请人:D 的 computed 桶 ∪ 任意日期的 held 桶,
// 按 inviter_id 去重、键集游标递增。第二路同样不能省(见 candidateUsers)。
func candidateInviters(ctx context.Context, day string, after, limit int) ([]int, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	ids := make([]int, 0, limit)
	err := gdb.WithContext(ctx).Model(&InviteAccrual{}).Distinct().
		Where("inviter_id > ?", after).
		Where("(bucket_date = ? AND status = ?) OR status = ?", day, AccrualComputed, AccrualHeld).
		Order("inviter_id asc").Limit(limit).Pluck("inviter_id", &ids).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return ids, nil
}

// settleInviter 结算一个邀请人名下的全部候选桶(一个扩展库事务)。
//
// 暂缓判据与消费返逐字相同,只是看的是**邀请人**的快照:钱发给谁,就看谁的账号状态。
// 发放:floor(invite_carry + Σgross),net > 0 才写流水;net=0 的桶同样 settled、
// ledger_no 留空,零头进 invite_carry。任一桶 CAS 落空整笔回滚。
func settleInviter(ctx context.Context, inviterId int, day string, key settleKey) (settleOutcome, error) {
	snap, err := loadUserSnapshot(ctx, inviterId)
	if err != nil {
		return settleOutcome{}, err
	}
	reason := snap.holdReason()

	gdb := db.Get()
	if gdb == nil {
		return settleOutcome{}, db.ErrNotReady
	}
	var out settleOutcome
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bal, err := LockBalance(tx, inviterId)
		if err != nil {
			return err
		}
		var buckets []InviteAccrual
		if err := db.LockForUpdate(tx).
			Where("inviter_id = ? AND ((bucket_date = ? AND status = ?) OR status = ?)",
				inviterId, day, AccrualComputed, AccrualHeld).
			Order("bucket_date asc, id asc").Find(&buckets).Error; err != nil {
			return err
		}
		if len(buckets) == 0 {
			out.Skipped = true
			return nil
		}
		now := common.GetTimestamp()
		if reason != "" {
			out.Held = true
			return holdInviteBuckets(tx, inviterId, buckets, reason, now)
		}

		sum := decimal.Zero
		var baseTotal int64
		bps, group := 0, ""
		for _, b := range buckets {
			sum = sum.Add(b.Gross)
			baseTotal += b.BaseQuota
			// 流水上冻结的档位取最近一天那一桶的:held 桶跨着几天时它们的档位可能不同,
			// 一行流水只能记一个,取最新的那个是"这一次按什么算"的最接近答案。
			bps, group = b.Bps, b.RateGroup
		}
		total := bal.InviteCarry.Add(sum)
		netInt, clamp := common.QuotaFromDecimalChecked(total.Floor())
		if clamp != nil {
			warnf("邀请人 %d 的下线消费返换算触顶: %s", inviterId, clamp.Error())
		}
		net := int64(netInt)
		if net < 0 {
			return fmt.Errorf("stardust: 邀请人 %d 的下线消费返净额为负(%d),拒绝落账", inviterId, net)
		}
		ledgerNo := ""
		if net > 0 {
			res, err := Credit(tx, Posting{
				UserId: inviterId, Kind: KindInviteConsume, Amount: net,
				IdemScope: inviteSettleIdemScope, IdemKey: key.inviteIdemKey(inviterId),
				RefType: inviteSettleRefType, RefNo: key.RunDate,
				RateBps: bps, RateGroup: group, BaseQuota: baseTotal,
				Remark: "下线消费返 " + day,
			})
			if err != nil {
				return err
			}
			if !res.Inserted {
				// 键已被占用 = 这一次运行已经给他发过;桶却还是 computed/held,说明两次
				// 运行交错。绝不能把桶标成 settled 而不发钱,整笔回滚等下一次。
				return fmt.Errorf("stardust: 幂等键 %s 已存在(流水 %s),本次下线消费返整笔回滚",
					key.inviteIdemKey(inviterId), res.LedgerNo)
			}
			ledgerNo = res.LedgerNo
		}
		carryAfter := total.Sub(decimal.NewFromInt(net))
		for _, b := range buckets {
			res := tx.Model(&InviteAccrual{}).Where("id = ? AND status = ?", b.Id, b.Status).
				Updates(map[string]any{
					"status": AccrualSettled, "ledger_no": ledgerNo, "hold_reason": "", "updated_at": now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return fmt.Errorf("stardust: 下线消费返日桶 %d 的结算 CAS 失败(状态已被并发改动),本批回滚", b.Id)
			}
		}
		if err := tx.Model(&Balance{}).Where("user_id = ?", inviterId).Updates(map[string]any{
			"invite_carry": carryAfter, "hold_reason": "", "updated_at": now,
		}).Error; err != nil {
			return err
		}
		out.Net = net
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return settleOutcome{}, err
	}
	return out, nil
}

// holdInviteBuckets 把一个邀请人的候选桶全部标成 held(不 Credit),余额行的 hold_reason 同步。
//
// 档位与 gross 在重算时就已冻结,这里只改状态与原因。computed 桶断言 RowsAffected,
// 已 held 的桶只刷新原因、不断言(值没变时 MySQL 报 0 行,那不是并发冲突)。
func holdInviteBuckets(tx *gorm.DB, inviterId int, buckets []InviteAccrual, reason string, now int64) error {
	for _, b := range buckets {
		res := tx.Model(&InviteAccrual{}).Where("id = ? AND status = ?", b.Id, b.Status).
			Updates(map[string]any{"status": AccrualHeld, "hold_reason": reason, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if b.Status == AccrualComputed && res.RowsAffected != 1 {
			return fmt.Errorf("stardust: 下线消费返日桶 %d 的暂缓 CAS 失败(状态已被并发改动),本批回滚", b.Id)
		}
	}
	return tx.Model(&Balance{}).Where("user_id = ?", inviterId).Updates(map[string]any{
		"hold_reason": reason, "updated_at": now,
	}).Error
}
