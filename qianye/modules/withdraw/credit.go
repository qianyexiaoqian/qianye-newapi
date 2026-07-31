package withdraw

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"gorm.io/gorm"
)

// idemScopeCredit 是兑现动作的幂等域。与申请的 idemScope 分开:
// 一张单只能申请一次、也只能兑现一次,但那是两个独立的幂等事实。
const idemScopeCredit = "withdraw_credit"

// creditQuota 把佣金兑现为主库额度。
//
// 这是全模块唯一的跨库写入点,方向与划转相反:扩展库扣佣金 → 主库加额度。
// "主库加钱成功但扩展库扣减失败" = 同一笔佣金可被无限重复领取。因此每一步都有
// 精确的幂等锚点:
//
//	① 申请时已冻结佣金        —— 用户手里没有第二次发单的本金
//	② approved → paying CAS   —— 全集群唯一的跨库执行准入闸门
//	③ (idem_scope, idem_key)  —— 资金单唯一索引,重放直接命中原单
//	④ 主库 qy_fund_outbox     —— 唯一精确探针,判定主库到底动没动
//	⑤ paying → paid CAS       —— 只有赢下这一次 CAS 的路径才去核销佣金
//
// 回滚路径严格区分两类失败:主库【确定未生效】才退回佣金;结果不可判定一律
// 转人工(holdForReview),绝不自动退。
func creditQuota(ctx context.Context, w *Withdrawal) error {
	req := twophase.Request{
		Kind:        qymodel.KindWithdrawQuota,
		IdemScope:   idemScopeCredit,
		IdemKey:     w.WithdrawNo,
		UserId:      w.UserId,
		AmountQuota: w.Quota,
		RefType:     "withdraw",
		RefId:       w.WithdrawNo,

		LocalDetail: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			return startPaying(tx, w, o.OrderNo)
		},
		MainApply: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			return creditMainQuota(tx, w)
		},
		AfterCommit: func(o *qymodel.FundOrder) {
			afterCredit(w, o.OrderNo)
		},
		LocalCommit: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			return finishPaid(tx, w, o.OrderNo)
		},
	}
	// 指纹让幂等命中时能验出"同一个键、不同的资金要素"。这里的幂等键是服务端
	// 生成的 withdraw_no,正常路径下不可能换参重放;但资金单一旦被人手工改过
	// (或将来有人把幂等键改成客户端可影响的值),没有指纹就只能默默按原单返回。
	// 代价是一次 sha256,收益是这条路径永远不会变成 B4 那种"审计可伪造"。
	req.Fingerprint = req.Digest()

	order, err := twophase.Execute(ctx, req)
	if err == nil {
		return nil
	}

	// 只有"主库确定未生效"才允许退回佣金。pending / uncertain 都意味着钱可能
	// 已经动了,自动退回就是纯资损,必须交给对账任务与人工。
	if order != nil && order.Status == qymodel.StatusFailed {
		if mainSideApplied(order.OrderNo) {
			holdForReview(w, "资金单被判失败但主库探针显示已生效,需人工核对")
			return err
		}
		failWithdrawal(w, err)
	}
	return err
}

// mainSideApplied 用主库 outbox 探针判定资金变更是否真的生效。
//
// 主库事务在 commit 阶段断连时,GORM 会返回错误,但事务其实可能已经提交。
// 仅凭错误就退回佣金,等于"额度已经加了却把佣金原样还给用户"。
// 探针本身失败时一律按"可能已生效"处理:宁可让佣金多冻一会儿等人工,
// 也不能错退 —— 前者是体验问题,后者是可被反复利用的超发漏洞。
func mainSideApplied(orderNo string) bool {
	if !config.Get().TwoPhase.OutboxEnabled() {
		return false
	}
	applied, err := model.QyProbeFundOutbox(orderNo)
	if err != nil {
		common.SysError("qianye/withdraw: 单号 " + orderNo + " 探测主库 outbox 失败,按可能已生效处理: " + err.Error())
		return true
	}
	return applied
}

// startPaying 是跨库执行的准入闸门。
//
// 它跑在 twophase 的 LocalDetail 里,与资金单的创建同事务:CAS 失败即代表
// 别的节点(或对账任务)已经在推进这张单,整个事务回滚,连资金单都不会产生。
// 这保证主库加钱操作在全集群最多被执行一次。
func startPaying(tx *gorm.DB, w *Withdrawal, orderNo string) error {
	return applyTransition(tx, w, transition{
		From:      StatusApproved,
		To:        StatusPaying,
		Action:    ActionSettleStart,
		ActorType: qymodel.ActorSystem,
		Detail:    common.MapToJsonStr(map[string]any{"order_no": orderNo, "quota": w.Quota}),
		Updates: map[string]any{
			"order_no":          orderNo,
			"settle_started_at": common.GetTimestamp(),
		},
	})
}

// creditMainQuota 在主库事务内给用户加额度。
//
// 硬性约束(违反即资损):
//   - 绝不调用 model.IncreaseUserQuota:它没有事务、没有溢出校验,
//     且在 db=false 时会进批量队列 —— 钱什么时候到账将无法确定
//   - 绝不 tx.Save(&user):全字段覆盖会把并发的其他变更冲掉
//   - 绝不触碰 auth_version / 调 user.Update():那会吊销用户的全部会话
//   - 加款必须带上限条件:users.quota 是 int32,上游全无溢出校验
func creditMainQuota(tx *gorm.DB, w *Withdrawal) error {
	headroom := int64(common.MaxQuota) - w.Quota

	var u model.User
	// First 自带软删除过滤。绝不加 Unscoped:给已注销账号加额度等于把钱
	// 转进一个再也取不出来的账户。
	if err := model.QyLockForUpdate(tx).Where("id = ?", w.UserId).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errUserUnavailable
		}
		return err
	}
	// 锁内复检:申请到兑现之间可能隔了几天,用户可能已被封禁。
	if u.Status != common.UserStatusEnabled {
		return errUserUnavailable
	}
	if int64(u.Quota) > headroom {
		return errQuotaOverflow
	}

	res := tx.Model(&model.User{}).
		Where("id = ? AND quota <= ?", w.UserId, headroom).
		Update("quota", gorm.Expr("quota + ?", w.Quota))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errQuotaOverflow
	}
	return nil
}

// afterCredit 处理主库提交之后的收尾。此时钱已经动了,任何失败都不能回滚。
func afterCredit(w *Withdrawal, orderNo string) {
	// 直接失效而非增量更新缓存:提现是低频高价值操作,失效回源最稳;
	// 增量更新会与并发的消费扣费互相覆盖。
	if err := model.InvalidateUserCache(w.UserId); err != nil {
		common.SysError("qianye/withdraw: 失效用户缓存失败(额度已入账): " + err.Error())
	}
	// 同时写一条主库账本日志:用户在原项目"日志"页就能看到到账记录,
	// 这是零前端改动的兜底通知。
	model.QyRecordLedgerLog(w.UserId, model.LogTypeTopup,
		fmt.Sprintf("佣金提现到账 %d 额度 [%s]", w.Quota, w.WithdrawNo), orderNo,
		map[string]any{
			"qy_module":      "withdraw",
			"qy_withdraw_no": w.WithdrawNo,
			"qy_quota":       w.Quota,
		})
}

// finishPaid 收尾:标记已到账并核销佣金。
//
// 幂等锚点在 CAS 上:RowsAffected == 0 说明别的路径(业务线程 / 补偿任务 /
// 对账任务)已经收过尾,直接返回成功。佣金核销只在赢下 CAS 的那一次执行,
// 因此绝不会重复扣减 frozen。
func finishPaid(tx *gorm.DB, w *Withdrawal, orderNo string) error {
	now := common.GetTimestamp()
	res := tx.Model(&Withdrawal{}).
		Where("id = ? AND status = ?", w.Id, StatusPaying).
		Updates(map[string]any{
			"status":          StatusPaid,
			"paid_at":         now,
			"updated_at":      now,
			"reconcile_state": "",
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}

	if err := commission.SettleFrozen(tx, w.UserId, w.Quota, w.WithdrawNo); err != nil {
		return err
	}
	w.Status = StatusPaid
	w.PaidAt = now
	return writeEvent(tx, w, transition{
		From:      StatusPaying,
		To:        StatusPaid,
		Action:    ActionSettleDone,
		ActorType: qymodel.ActorSystem,
		Detail:    common.MapToJsonStr(map[string]any{"order_no": orderNo, "quota": w.Quota}),
	})
}

// failWithdrawal 在主库确定性失败后退回佣金。
// 只在"主库确定未生效"时调用 —— 判定逻辑见 creditQuota。
func failWithdrawal(w *Withdrawal, cause error) {
	reason := truncate(cause.Error(), 512)
	err := db.Get().Transaction(func(tx *gorm.DB) error {
		if err := applyTransition(tx, w, transition{
			From:      StatusPaying,
			To:        StatusFailed,
			Action:    ActionFail,
			ActorType: qymodel.ActorSystem,
			Reason:    reason,
			Updates:   map[string]any{"fail_reason": reason},
		}); err != nil {
			return err
		}
		return commission.UnfreezeForWithdraw(tx, w.UserId, w.Quota, w.WithdrawNo)
	})
	if err != nil && !errors.Is(err, errStatusConflict) {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 单号 " + w.WithdrawNo + " 退回佣金失败,已交由对账任务处理: " + err.Error())
	}
}

// holdForReview 把单据转入人工裁决。
//
// 资金系统必须有"我不知道,交给人"这个合法出口。自动判失败会把佣金退回可用池,
// 若主库其实已加额度,用户就白拿了一笔 —— 而这是可以被主动构造的套利面
// (在兑现瞬间打满主库连接让事务超时)。人工兜底最坏只是运营成本。
func holdForReview(w *Withdrawal, reason string) {
	marked := false
	err := db.Get().Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Withdrawal{}).
			Where("id = ? AND status = ? AND reconcile_state <> ?", w.Id, StatusPaying, ReconcileHold).
			Updates(map[string]any{
				"reconcile_state": ReconcileHold,
				"updated_at":      common.GetTimestamp(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		marked = true
		w.ReconcileState = ReconcileHold
		// hold 附着在 paying 上,不是独立状态,因此事件的 from/to 都是 paying。
		return writeEvent(tx, w, transition{
			From: StatusPaying, To: StatusPaying, Action: ActionHold,
			ActorType: qymodel.ActorSystem, Reason: truncate(reason, 512),
		})
	})
	if err != nil {
		db.MarkFailure(err)
		return
	}
	if !marked {
		// 已经在 hold 里了。对账任务每个周期都会重新扫到它,这里再告警一次
		// 只会把日志刷满,反而淹没真正的新异常。持续可见性由管理端
		// /admin/withdraw/stats 的 reconcile_hold 计数承担。
		return
	}
	common.SysError(fmt.Sprintf("qianye/withdraw: 提现单 %s 已转人工裁决(用户 %d,额度 %d): %s",
		w.WithdrawNo, w.UserId, w.Quota, reason))
}

// resolveAfterCompensation 是补偿任务确认主库已生效后的收尾回调。
// 必须幂等:同一张单可能被补偿多次,幂等由 finishPaid 的 CAS 保证。
func resolveAfterCompensation(ctx context.Context, order *qymodel.FundOrder) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return db.Get().Transaction(func(tx *gorm.DB) error {
		var w Withdrawal
		if err := tx.Where("withdraw_no = ?", order.RefId).Take(&w).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// 有资金单却没有业务明细,是不该出现的孤儿数据。返回 nil 让补偿
				// 任务把资金单推进终态而不是无限重试,异常本身靠告警交给人。
				common.SysError("qianye/withdraw: 资金单 " + order.OrderNo +
					" 找不到对应提现单 " + order.RefId)
				return nil
			}
			return err
		}
		return finishPaid(tx, &w, order.OrderNo)
	})
}

// shouldAutoCredit 判断审核通过后是否立即自动到账。
func shouldAutoCredit(w *Withdrawal) bool {
	return w.Method == config.WithdrawMethodQuota && config.Get().Withdraw.AutoCredit()
}
