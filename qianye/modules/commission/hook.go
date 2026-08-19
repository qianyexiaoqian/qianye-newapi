package commission

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// consumeEvent 是从 relay 线程传出去的最小快照。
//
// 刻意不把 model.RecordConsumeLogParams 整个带走:它内含 Other map,
// 调用方在 hook 返回后仍可能读写它,跨 goroutine 持有等于数据竞争。
type consumeEvent struct {
	InviteeId int
	Quota     int64
	At        int64
}

// installHooks 把实现注入上游 model 包的 hook 变量。
// 调用时机在 qianye.Init(),早于任何 HTTP 请求与后台协程。
func installHooks() {
	model.QyOnConsumeLog = onConsumeLog
	model.QyOnRedeemSuccess = onRedeemSuccess
	model.QyOnTaskBillingLog = onTaskBillingLog
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
	if e, ok := peekInviter(userId); ok && e.InviterId == 0 {
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
// 违规扣费产生的消费日志绝不能返佣:那笔钱是罚款不是消费,给邀请人分成
// 等于"下线违规、上线获利"。这属于逻辑错误而非口径偏好,因此不设开关。
func hardExcluded(p model.RecordConsumeLogParams) bool {
	if v, ok := p.Other["violation_fee"].(bool); ok && v {
		return true
	}
	// 渠道可用性测试跑在管理员账号上,不是真实消费。
	//
	// 首选判据是写日志那一侧打的显式标记(与 violation_fee 同形)。
	// TokenName 那条是兜底:它靠一个会被当成文案去改、去做 i18n 的中文字面量,
	// 单独用它的话改文案就等于静默恢复返佣。两处共用 model 的常量,
	// 并由 model/channel_test_marker_test.go 钉住"标记必须被打上"。
	if v, ok := p.Other[model.ChannelTestLogOtherKey].(bool); ok && v {
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
func isSubscriptionConsume(other map[string]interface{}) bool {
	s, _ := other["billing_source"].(string)
	return s == "subscription"
}

// accrueConsume 在后台 worker 里完成邀请关系解析与日聚合写入。
//
// ctx 由 guard 注入(hot_path_timeout_ms),必须一路透传到每一个 GORM 调用:
// 少接一处,那一处就会一直等到 MySQL 的 innodb_lock_wait_timeout(默认 50 秒),
// 把 worker 全部占满 —— guard 承诺的 200ms 上界只对接了 ctx 的语句成立。
func accrueConsume(ctx context.Context, ev consumeEvent) error {
	e, fromSource, err := resolveInviter(ctx, ev.InviteeId)
	if err != nil {
		warnUnknownInviter(ev.InviteeId, err)
		return err
	}
	if e.InviterId == 0 {
		return nil
	}
	// 自我邀请是数据异常而非策略选择,直接拒绝且不产生任何行。
	if e.InviterId == ev.InviteeId {
		accrualSkipped.Add(1)
		return nil
	}
	if fromSource {
		ensureRelation(ctx, e.InviterId, ev.InviteeId, e.InviteeName, e.InviteeCreated)
	}
	if blockedInvitees(ctx)[ev.InviteeId] {
		accrualSkipped.Add(1)
		return nil
	}

	s := effective()
	// 绑定成熟期:防"注册即充值→拿佣金→退款"的一次性套利账号。
	if s.MinInviteeAgeHours > 0 && e.InviteeCreated > 0 &&
		ev.At-e.InviteeCreated < int64(s.MinInviteeAgeHours)*3600 {
		accrualSkipped.Add(1)
		return nil
	}
	// 费率按【下线的账号分组】(users.group)解析,并连同分组一起冻结进这一行。
	//
	// 这是选定的口径:佣金档位跟人走,不跟单次请求走 —— "这个下线是 VIP 客户,
	// 所以他的消费按 8% 返" 是运营能直接讲清楚的规则,四条来源(消费、充值、
	// 兑换码、任务补扣)也因此共用同一个档位,对账时一个下线一天只对应一个费率。
	//
	// 已知的代价:令牌可以指定分组、auto 分组还会逐请求变化,所以本次请求的
	// **计价分组**可能与账号分组不同。上下线串通时,下线把令牌指到低毛利分组
	// (平台按薄利收钱)、佣金仍按账号分组的高费率算,能占到差价。需要事后排查时,
	// 主库 logs 表本身就按请求记了实际计价分组,与本表按 (下线, 日期) join 即可
	// 比对 —— 不需要在扩展库再存一份。
	rate := resolveRate(ctx, e.InviteeGroup, SourceConsume, s)
	gross := capGross(calcGross(ev.Quota, rate.Units), s.MaxPerOrderQuota)
	if gross.IsZero() {
		return nil
	}
	day := bucketDate(ev.At)
	// 法币折算比例按【上线的账号分组】解析,与费率那一档**相反**(费率按下线)。
	// 两个口径为什么必须分开,见 fiatrate.go 的文件头:费率跟毛利走,折算比例
	// 跟收款人走 —— available_fiat 是上线的钱,提现单也是上线在提。
	//
	// 与费率一样在这里冻结进 accrual 行,因此改比例只影响此后的计佣与结算,
	// 已经算进 available_fiat 的绝对值不会被重算。
	fiat := inviterFiatRate(ctx, e.InviterId, s)
	_, err = writeAccrual(ctx, accrualInput{
		SourceType: SourceConsume,
		IdemKey:    consumeIdemKey(e.InviterId, ev.InviteeId, day, rate, fiat),
		InviterId:  e.InviterId,
		InviteeId:  ev.InviteeId,
		BaseQuota:  ev.Quota,
		RateUnits:  rate.Units,
		RateGroup:  rate.Group,
		Gross:      gross,
		UsdRate:    fiat.Rate,
		MatureAt:   bucketMatureAt(day, s.HoldingDays),
		BucketDate: day,
		Status:     StatusAccrued,
		Accumulate: true,
	})
	return err
}

// onRedeemSuccess 处理兑换码充值返佣。
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
		// 兑换码没有付款金额,法币域留空。
		return accrueOneShot(ctx, userId, int64(quota), decimal.Zero, SourceRedemption,
			redemptionIdemKey(redemptionId), "RD"+itoa(redemptionId))
	})
}

// onTaskBillingLog 处理异步任务的差额补扣与退款。
//
// 任务的首次扣费走 RecordConsumeLog(已由 onConsumeLog 覆盖),这里只处理
// 结算后的增量:LogTypeConsume 是补扣(继续返佣),LogTypeRefund 是退款(冲正)。
func onTaskBillingLog(params model.RecordTaskBillingLogParams) {
	cm := config.Get().Commission
	if !cm.Enabled || params.Quota <= 0 {
		return
	}
	userId, quota := params.UserId, int64(params.Quota)
	taskId, _ := params.Other["task_id"].(string)

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
	e, fromSource, err := resolveInviter(ctx, inviteeId)
	if err != nil {
		warnUnknownInviter(inviteeId, err)
		return err
	}
	if e.InviterId == 0 || e.InviterId == inviteeId {
		return nil
	}
	if fromSource {
		ensureRelation(ctx, e.InviterId, inviteeId, e.InviteeName, e.InviteeCreated)
	}
	if blockedInvitees(ctx)[inviteeId] {
		accrualSkipped.Add(1)
		return nil
	}
	s := effective()
	// 与消费路径同口径:按下线的账号分组解析费率(见 accrueConsume 处的说明)。
	//
	// 幂等键在这里是订单号,不掺费率 —— 一笔充值无论如何只能返一次佣,
	// 费率变了也不能变成两行。
	rate := resolveRate(ctx, e.InviteeGroup, sourceType, s)
	gross := capGross(calcGross(baseQuota, rate.Units), s.MaxPerOrderQuota)
	if gross.IsZero() {
		return nil
	}
	now := common.GetTimestamp()
	// 法币折算比例按上线分组解析并冻结,与消费路径同口径(见 accrueConsume)。
	// 这一路的幂等键是订单号,不掺比例 —— 一笔充值无论如何只能返一次佣,
	// 比例变了也不能变成两行。
	fiat := inviterFiatRate(ctx, e.InviterId, s)
	_, err = writeAccrual(ctx, accrualInput{
		SourceType: sourceType,
		IdemKey:    idemKey,
		SourceRef:  sourceRef,
		InviterId:  e.InviterId,
		InviteeId:  inviteeId,
		BaseQuota:  baseQuota,
		BaseMoney:  baseMoney,
		RateUnits:  rate.Units,
		RateGroup:  rate.Group,
		Gross:      gross,
		UsdRate:    fiat.Rate,
		MatureAt:   now + int64(s.HoldingDays)*86400,
		Status:     StatusAccrued,
	})
	return err
}
