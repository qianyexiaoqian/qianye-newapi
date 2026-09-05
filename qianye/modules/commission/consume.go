package commission

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// consume.go —— 三条事件型计佣入口(消费日聚合 / 兑换码 / 任务补扣与退款)。
//
// hook 体跑在 relay 结算线程或用户请求线程上,必须 O(1) 无 I/O:只读 config 快照
// 与 invite 的负缓存,所有查库都在 guard.HotAsync 的 worker 里,ctx 透传到每个
// GORM 调用。邀请关系判定走 invite.InviteeEligible —— 与星屑侧同一份判据(拉黑 /
// 互邀 / invite.enabled),两条线对"这个人能不能给上线返"的回答永远一致。

// consumeEvent 是从 relay 线程传出去的最小快照。
//
// 刻意不把 model.RecordConsumeLogParams 整个带走:它内含 Other map,
// 调用方在 hook 返回后仍可能读写它,跨 goroutine 持有等于数据竞争。
type consumeEvent struct {
	InviteeId int
	Quota     int64
	At        int64
}

// installHooks 把实现注入上游 model 包与 invite 的转发槽。
// 调用时机在 qianye.Init(),早于任何 HTTP 请求与后台协程。
func installHooks() {
	model.QyOnConsumeLog = onConsumeLog
	model.QyOnTaskBillingLog = onTaskBillingLog
	// 兑换码事件的单槽变量由 invite 占着,再转发给星屑与本包各自的槽。
	invite.AfterRedeemSuccessCommission = onRedeemSuccess
	// 本包两把进程内缓存的跨节点失效走 invite 的通道。
	invite.OnInvalidation(cacheKindSettings, func(int) { invalidateSettingsLocal() })
	invite.OnInvalidation(cacheKindGroupRate, func(int) { invalidateGroupRatesLocal() })
}

// onConsumeLog 跑在 relay 结算线程上,必须是 O(1) 且无 I/O。
//
// 允许做的事只有三件:读原子配置、读内存缓存、往有界队列投递。
// 任何数据库访问都必须发生在 guard.HotAsync 的 worker 里。
func onConsumeLog(c *gin.Context, userId int, params model.RecordConsumeLogParams) {
	cm := config.Get().Commission
	if !cm.Enabled || params.Quota <= 0 {
		return
	}
	if hardExcluded(params) {
		return
	}
	if cm.ExcludeSubscriptionConsume && isSubscriptionConsume(params.Other) {
		return
	}
	// 负缓存命中即到此为止 —— 绝大多数用户没有邀请人,这是最热的分支,
	// 全程一次 map 查找,不产生任何后续工作。
	if invite.KnownWithoutInviter(userId) {
		return
	}

	ev := consumeEvent{
		InviteeId: userId,
		Quota:     int64(params.Quota),
		At:        common.GetTimestamp(),
	}
	consumeEvents.Add(1)
	guard.HotAsync("commission.consume", func(ctx context.Context) error {
		return accrueConsume(ctx, ev)
	})
}

// hardExcluded 是没有开关的排除项。
//
// 违规扣费产生的消费日志绝不能计佣:那笔钱是罚款不是消费,给邀请人分成
// 等于"下线违规、上线获利"。这属于逻辑错误而非口径偏好,因此不设开关。
func hardExcluded(p model.RecordConsumeLogParams) bool {
	if p.Other.PublicBool("violation_fee") {
		return true
	}
	// 渠道可用性测试跑在管理员账号上,不是真实消费。
	//
	// 首选判据是写日志那一侧打的显式标记(与 violation_fee 同形)。
	// TokenName 那条是兜底:它靠一个会被当成文案去改的中文字面量,
	// 单独用它的话改文案就等于静默恢复计佣。两处共用 model 的常量。
	if p.Other.PublicBool(model.ChannelTestLogOtherKey) {
		return true
	}
	if p.TokenId == 0 && p.TokenName == model.ChannelTestTokenName {
		return true
	}
	return false
}

// isSubscriptionConsume 判断这笔消费扣的是订阅额度而非钱包余额。
//
// 不能用 other["wallet_quota_deducted"] 反推:该键只在订阅分支被写入,
// 钱包分支根本不写,取零值会把所有钱包消费误判成订阅消费。
func isSubscriptionConsume(other *model.LogOther) bool {
	return other.PublicString("billing_source") == "subscription"
}

// accrueConsume 在后台 worker 里完成邀请关系解析与日聚合写入。
//
// ctx 由 guard 注入(hot_path_timeout_ms),必须一路透传到每一个 GORM 调用:
// 少接一处,那一处就会一直等到 MySQL 的 innodb_lock_wait_timeout(默认 50 秒),
// 把 worker 全部占满 —— guard 承诺的 200ms 上界只对接了 ctx 的语句成立。
func accrueConsume(ctx context.Context, ev consumeEvent) error {
	match, err := invite.InviteeEligible(ctx, ev.InviteeId)
	if err != nil {
		invite.WarnUnknownInviter(ev.InviteeId, err)
		return err
	}
	if !match.Eligible() {
		if match.InviterId > 0 {
			accrualSkipped.Add(1)
		}
		return nil
	}

	s := effective()
	// 绑定成熟期:防"注册即充值→拿佣金→退款"的一次性套利账号。
	if s.MinInviteeAgeHours > 0 && match.InviteeCreated > 0 &&
		ev.At-match.InviteeCreated < int64(s.MinInviteeAgeHours)*3600 {
		accrualSkipped.Add(1)
		return nil
	}
	// 费率按【上线(推广人)自己的账号分组】(users.group)解析,由 resolveInviterPricing
	// 一处取分组(口径与理由见 grouprate.go 与 pricing.go 的文件头),连同分组一起
	// 冻结进这一行。**下线换分组不改动上线的费率**:费率是推广人自己的等级属性。
	rate := resolveInviterPricing(ctx, match.InviterId, SourceConsume, s)
	// 刻度与费率一样是运营可改的全局值,同样在这一刻冻结进行 —— 复算时拿今天的
	// 刻度去除昨天的基数,得到的是一个谁也解释不了的数。
	qpu := stardust.QuotaPerUnit()
	gross, capped := capGross(calcGross(ev.Quota, rate.Units, qpu), s.MaxPerOrderStardust)
	if gross.IsZero() {
		return nil
	}
	day := bucketDate(ev.At)
	_, err = writeAccrual(ctx, accrualInput{
		SourceType:   SourceConsume,
		IdemKey:      consumeIdemKey(match.InviterId, ev.InviteeId, day, rate, s.HoldingDays),
		InviterId:    match.InviterId,
		InviteeId:    ev.InviteeId,
		BaseQuota:    ev.Quota,
		RateUnits:    rate.Units,
		RateGroup:    rate.Group,
		QuotaPerUnit: qpu,
		Gross:        gross,
		Capped:       capped,
		MatureAt:     bucketMatureAt(day, s.HoldingDays),
		BucketDate:   day,
		Status:       StatusAccrued,
		Accumulate:   true,
	})
	return err
}

// onRedeemSuccess 处理兑换码充值计佣(invite.AfterRedeemSuccessCommission)。
//
// 跑在用户的兑换请求线程上而非 relay 上,但仍走 HotAsync:扩展库抖动
// 不应该让用户的兑换请求变慢,更不应该让它失败。
func onRedeemSuccess(userId int, redemptionId int, quota int) {
	cm := config.Get().Commission
	if !cm.Enabled || quota <= 0 {
		return
	}
	if cm.ExcludeRedemptionAndManual {
		return
	}
	guard.HotAsync("commission.redeem", func(ctx context.Context) error {
		// 兑换码没有付款金额,金额域留空。
		return accrueOneShot(ctx, userId, int64(quota), decimal.Zero, SourceRedemption,
			redemptionIdemKey(redemptionId), "RD"+itoa(redemptionId))
	})
}

// onTaskBillingLog 处理异步任务的差额补扣与退款。
//
// 任务的首次扣费走 RecordConsumeLog(已由 onConsumeLog 覆盖),这里只处理
// 结算后的增量:LogTypeConsume 是补扣(继续计佣),LogTypeRefund 是退款(冲正)。
func onTaskBillingLog(params model.RecordTaskBillingLogParams) {
	cm := config.Get().Commission
	if !cm.Enabled || params.Quota <= 0 {
		return
	}
	userId, quota := params.UserId, int64(params.Quota)
	taskId := params.Other.PublicString("task_id")

	switch params.LogType {
	case model.LogTypeConsume:
		ev := consumeEvent{
			InviteeId: userId,
			Quota:     quota,
			At:        common.GetTimestamp(),
		}
		guard.HotAsync("commission.task_consume", func(ctx context.Context) error {
			return accrueConsume(ctx, ev)
		})
	case model.LogTypeRefund:
		if !cm.RefundClawback {
			return
		}
		// 幂等键必须在投递前定死:worker 重试时要复用同一个键,
		// 否则一次退款会被冲正两次。
		key := clawbackIdemKey(taskId, userId, quota)
		guard.HotAsync("commission.task_refund", func(ctx context.Context) error {
			return clawback(ctx, userId, quota, key, taskId, "task refund")
		})
	}
}

// accrueOneShot 处理"一笔订单一条计佣行"的来源(充值、兑换码)。
func accrueOneShot(ctx context.Context, inviteeId int, baseQuota int64, baseMoney decimal.Decimal,
	sourceType, idemKey, sourceRef string) error {
	if baseQuota <= 0 {
		return nil
	}
	match, err := invite.InviteeEligible(ctx, inviteeId)
	if err != nil {
		invite.WarnUnknownInviter(inviteeId, err)
		return err
	}
	if !match.Eligible() {
		if match.InviterId > 0 {
			accrualSkipped.Add(1)
		}
		return nil
	}
	s := effective()
	// 与消费路径同口径:按**上线**的账号分组解析(见 accrueConsume 处的说明)。
	//
	// 幂等键在这里是订单号,不掺费率 —— 一笔充值/兑换无论如何只能计一次佣,
	// 费率变了也不能变成两行。
	rate := resolveInviterPricing(ctx, match.InviterId, sourceType, s)
	qpu := stardust.QuotaPerUnit()
	gross, capped := capGross(calcGross(baseQuota, rate.Units, qpu), s.MaxPerOrderStardust)
	if gross.IsZero() {
		return nil
	}
	now := common.GetTimestamp()
	_, err = writeAccrual(ctx, accrualInput{
		SourceType:   sourceType,
		IdemKey:      idemKey,
		SourceRef:    sourceRef,
		InviterId:    match.InviterId,
		InviteeId:    inviteeId,
		BaseQuota:    baseQuota,
		BaseMoney:    baseMoney,
		RateUnits:    rate.Units,
		RateGroup:    rate.Group,
		QuotaPerUnit: qpu,
		Gross:        gross,
		Capped:       capped,
		MatureAt:     now + int64(s.HoldingDays)*secondsPerDay,
		Status:       StatusAccrued,
	})
	return err
}
