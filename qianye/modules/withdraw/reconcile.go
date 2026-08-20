package withdraw

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
)

// reconcile 是提现模块的周期性收尾任务。
//
// # 它以前是什么
//
// 跨库兑现的对账:扫 approved 却没进兑现的单、扫 paying 迟迟不落终态的单,
// 依据主库 outbox 探针判定"钱到底动没动"。自动到账下线之后这两件事都不存在了 ——
// 本模块不再有任何跨库写入,也就没有任何"不可判定"的中间态需要收敛。
//
// # 它现在是什么
//
// 两件"到点才做一次"的收尾,共用同一把租约:
//
//	A. 待发放积压告警 —— 人工发放模型新引入的敞口。佣金在申请那一刻就离开了
//	   用户的可用池,如果管理员一直不发放也不驳回,用户就是"钱扣了、东西没拿到"。
//	   系统无法替他发钱,但必须让这件事**有声音**。
//	B. 保留期到期的 PII 清理 —— 提现单上的收款快照(Payee)、用户保存的收款方式
//	   (PayeeAccount,存的是同一份银行卡号密文)、以及明文访问审计(PiiAudit,
//	   保留期独立且更长)。三个面缺一不可,少一个保留期就是半个摆设。
//
// 本任务由 lease.Run 驱动,多节点不会双跑;即便双跑,每一步也都是幂等的。
func reconcile(ctx context.Context) {
	if !config.Get().Withdraw.Enabled {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	batch := config.Get().TwoPhase.BatchSize
	if batch <= 0 {
		batch = 200
	}

	// 一条聚合告警,不是逐单告警:积压是个总量问题,管理员一天没上线就可能有
	// 几十张单同时超时。逐单打一行只会把真正的新异常淹掉(本仓在"未知邀请人
	// 告警刷屏"上已经栽过一次),而运维需要知道的就一句话:有多少张、
	// 最老的那张等了多久。实时可见性由管理端队列角标承担,日志是第二条通道。
	if b := scanPayoutBacklog(ctx); b.Count > 0 {
		common.SysError(fmt.Sprintf(
			"qianye/withdraw: 有 %d 张提现单已通过审核但超过 %d 小时未标记发放"+
				"(合计 %d 额度,最久的已等待 %d 小时)。用户的佣金已经扣除,"+
				"请尽快发放或驳回退回",
			b.Count, config.Get().Withdraw.PayoutSLAHours, b.Quota, b.WaitedHours()))
	}
	pruneExpiredPii(ctx, gdb, batch)
}

// payoutBacklog 是「有多少笔佣金已经扣了、却还没人把钱发出去」。
type payoutBacklog struct {
	Count int64
	Quota int64
	// Oldest 是积压里最早那张单的审核通过时间,0 表示没有积压。
	Oldest int64
}

// WaitedHours 是最久的那张单已经等了多少小时。
func (b payoutBacklog) WaitedHours() int64 {
	if b.Oldest <= 0 {
		return 0
	}
	return (common.GetTimestamp() - b.Oldest) / 3600
}

// scanPayoutBacklog 统计超过发放时限的待发放单。
//
// 判据必须与管理端角标(handleAdminStats 的 payout_sla_breached)逐字一致:
// 日志说有 5 张、页面上写着 3 张,运维会认为其中一处坏了,然后两处都不信。
//
// 零值口径:PayoutSLAHours <= 0 表示彻底关掉发放时限,返回空积压(不是"全部
// 都算积压")—— 与 slaOf 的同一个判断同向。
//
// 租约丢失(ctx 取消)后本次扫描不得再产生结果:接管节点会重新扫一遍,这里再
// 喊一声只会重复。执行这条约束的是查询上的 WithContext(ctx) 本身,**不是**一句
// 额外的 ctx.Err() 前置判断 —— 那句判断只能挡住"进函数时就已经取消",挡不住
// 扫描过程中丢失租约,而且它挡住的两种情况 WithContext 全都挡住了。
// 写一句挡不住任何独有情况的检查,只会让下一个人以为真正的约束在那里。
func scanPayoutBacklog(ctx context.Context) payoutBacklog {
	hours := config.Get().Withdraw.PayoutSLAHours
	if hours <= 0 {
		return payoutBacklog{}
	}
	gdb := db.Get()
	if gdb == nil {
		return payoutBacklog{}
	}
	deadline := common.GetTimestamp() - int64(hours)*3600

	var row struct {
		Count  int64
		Quota  int64
		Oldest int64
	}
	err := gdb.WithContext(ctx).Model(&Withdrawal{}).
		Select("COUNT(*) AS count, COALESCE(SUM(quota), 0) AS quota, "+
			"COALESCE(MIN(reviewed_at), 0) AS oldest").
		// 判据是 reviewed_at 而不是 created_at:发放时限从"审核通过"起算,
		// 审核本身有它自己的 review_sla_hours。用 created_at 会把审核花掉的
		// 时间算进发放方的账上,两道时限互相污染就都失去意义。
		Where("status = ? AND reviewed_at > 0 AND reviewed_at < ?", StatusApproved, deadline).
		Scan(&row).Error
	if err != nil {
		db.MarkFailure(err)
		return payoutBacklog{}
	}
	return payoutBacklog{Count: row.Count, Quota: row.Quota, Oldest: row.Oldest}
}
