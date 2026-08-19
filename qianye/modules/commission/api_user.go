package commission

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// getSummary 返回"我的推广"看板数据。
//
// 刻意把未结算余数也下发(且用字符串,避免 JS 的 Number 精度丢失):
// 用户最常见的困惑是"我用了一整天怎么没佣金",让他看见 0.4271 正在累积,
// 比任何文案解释都有效。
func getSummary(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	userId := c.GetInt("id")
	gdb := db.Get()

	var bal Balance
	err := gdb.Where("user_id = ?", userId).Take(&bal).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		internalError(c, err)
		return
	}

	// 已解绑的关系不计入"我的下线数":那条关系已经不会再产生任何佣金,
	// 继续算进来会让用户以为自己还在从这个人身上挣钱。
	// 历史佣金仍然留在流水与余额里,那是另一回事。
	var inviteeCount int64
	if err := gdb.Model(&InviteRelation{}).
		Where("inviter_id = ? AND unbound_at = ?", userId, 0).
		Count(&inviteeCount).Error; err != nil {
		internalError(c, err)
		return
	}

	pendingMature, err := sumOutstanding(gdb, userId, true)
	if err != nil {
		internalError(c, err)
		return
	}
	earliestMature, err := earliestPendingMatureAt(gdb, userId)
	if err != nil {
		internalError(c, err)
		return
	}
	s := effective()
	cm := config.Get().Commission
	respond(c, gin.H{
		"invitee_count":        inviteeCount,
		"available_quota":      bal.AvailableQuota,
		"frozen_quota":         bal.FrozenQuota,
		"withdrawn_quota":      bal.WithdrawnQuota,
		"total_earned_quota":   bal.TotalEarnedQuota,
		"total_clawback_quota": bal.TotalClawbackQuota,
		"unsettled_amount":     bal.UnsettledAmount.String(),
		"pending_mature_quota": pendingMature.Floor().String(),
		"debt_blocked":         bal.DebtBlocked,
		"available_fiat":       bal.AvailableFiat.Round(2).String(),
		// 币种取**余额行上冻结的那一个**,不是当前配置。available_fiat 是按结算
		// 当时的汇率一笔笔累起来的,币种也必须来自同一时刻,否则运营改一次
		// withdraw.fiat_currency,历史金额一个字没动、标签全变了。
		// 空串是补上这一列之前的存量行(结算过一次就会被写上),只有这一种情况
		// 才回落到当前配置 —— 这些行本来就是按当前配置那条链路算出来的。
		"fiat_currency":   frozenFiatCurrency(bal.FiatCurrency),
		"last_settled_at": bal.LastSettledAt,
		// 比例对外一律是百分比字符串。旧的 *_bps 键继续下发是为了不打断
		// 已在跑的前端页面(它们自己再除以 100),等页面切到 *_percent 之后
		// 可以直接删掉这几行。
		//
		// 兑换码是第三档。下发的是**生效值**(没单独配时等于充值档)而不是
		// 配置值:用户端要回答的是"我下线用兑换码充值,我能拿几个点",
		// 空字符串在这里没有意义。redemption_follows_topup 保留那一位事实,
		// 界面据此在数字后面标一句"跟随充值档",省掉运营的一轮客服问答。
		//
		// 这里给的是**这个人自己**的档,不是全局默认档。费率按推广人自己的
		// 账号分组解析(见 grouprate.go 的口径),所以"我能拿几个点"对每个
		// 推广人是一个确定的数字 —— 旧版本这里只能给全局默认值,而那个数字
		// 对配了分组档的站点直接就是错的,推广人拿到手会以为平台少发了钱。
		//
		// group / group_matched / global_* 三组一起下发,是为了让界面能回答
		// "为什么是这个数":命中了 vip 档、还是没配所以回落到全局默认。
		// 只给一个数字的话,这个页面永远解释不了它自己。
		"rate": rateSummary(c, userId, s),
		// pending_earliest_mature_at 是**账本上的事实**:名下还没被结算吸收的
		// 计佣行里,最早的那个 mature_at。0 表示没有在途佣金。
		//
		// 它与下面 policy.payout_day_offset 的分工是这一版刻意定死的:
		//
		//	payout_day_offset          按**当前配置**算出来的 T+N,只对**此后**新产生的消费成立
		//	pending_earliest_mature_at 已经挣到手的那批钱**实际**什么时候成熟
		//
		// 成熟期是逐行冻结的(见 consumeIdemKey 的 h 段),运营今天把
		// holding_days 从 7 改成 0,昨天那一桶仍然按 7 天成熟。此前界面上只有
		// 前一个数,于是那句 "T+1 到账" 对一个昨天就挣到钱的用户是错的。
		// 补上后一个数之后,界面不必再拿配置去反算历史 —— 直接把账本上写着的
		// 那个时刻显示出来,改配置这件事就再也影响不到"我的钱什么时候到"。
		"pending_earliest_mature_at": earliestMature,
		"policy": gin.H{
			"holding_days":     s.HoldingDays,
			"min_settle_quota": s.MinSettleQuota,
			// settle_interval_seconds 现在是调度心跳,不是"多久发一次钱"。
			// 用户端不该再拿它推算到账时间,所以下面直接给出结论:
			//
			//	payout_day_offset = holding_days + 1
			//
			// 那个 +1 是"消费所在的那一天要整天结束才封板"的直接后果
			// (见 bucketMatureAt),不是四舍五入出来的:holding_days=0 也是
			// **次日**到账。不把它算好下发,前端就得自己复刻这条规则,
			// 而它一旦与后端算得不一样,界面上就是一句会被追问的错话。
			//
			// **它只对此后新产生的消费成立。** 成熟期逐行冻结,已经在途的那批钱
			// 按各自行上的 mature_at 发放 —— 那个事实由上面的
			// pending_earliest_mature_at 给出,界面必须两个一起显示。
			"settle_interval_seconds": cm.SettleIntervalSecs,
			"settle_daily":            true,
			"payout_day_offset":       payoutDayOffset(s.HoldingDays),
			"day_offset_minutes":      cm.DayOffsetMinutes,
			"exclude_redemption":      cm.ExcludeRedemptionAndManual,
			"exclude_subscription":    cm.ExcludeSubscriptionConsume,
		},
	})
}

// rateSummary 是"我现在走哪一档、为什么"这个问题的应答。
//
// 三次 resolveInviterPricing 而不是一次解析出分组再自己查三档:那样就要在
// 展示路径上复刻一份"取谁的分组、命中哪一档"的判定,而复刻品迟早会与计佣
// 路径漂移 —— 界面上写着 8%、账本按 5% 发钱,是本仓最忌讳的形状。走同一个
// 入口的代价只是两次额外的缓存命中(邀请关系与分组费率表都在进程内)。
//
// userId 在这里既是"看板的主人"也是"计佣时的上线",两者本来就是同一个人。
func rateSummary(c *gin.Context, userId int, s opSettings) gin.H {
	ctx := c.Request.Context()
	topup := resolveInviterPricing(ctx, userId, SourceTopup, s)
	consume := resolveInviterPricing(ctx, userId, SourceConsume, s)
	redemption := resolveInviterPricing(ctx, userId, SourceRedemption, s)
	return gin.H{
		"topup_percent":      config.FormatRatePercent(topup.Rate.Units),
		"consume_percent":    config.FormatRatePercent(consume.Rate.Units),
		"redemption_percent": config.FormatRatePercent(redemption.Rate.Units),
		"topup_bps":          topup.Rate.Units,
		"consume_bps":        consume.Rate.Units,
		"redemption_bps":     redemption.Rate.Units,
		// 「跟随充值档」这一位只在**本人这一档**上成立才该显示:分组单独配了
		// 兑换码档时,它跟随的是那一档,写"跟随充值档"就是句错话。判据是
		// 两档算出来的数字相等 —— 那正是界面上"跟随"要表达的全部意思。
		"redemption_follows_topup": redemption.Rate.Units == topup.Rate.Units,
		// group 是解析用的那个分组名(已归一化)。空串表示这次没能解析出
		// 账号分组(主库读失败),界面据此闭嘴而不是编一个分组名出来。
		"group": consume.Rate.Group,
		// group_matched 回答"为什么是这个数":true = 命中了你所在分组的档,
		// false = 你所在的分组没单独配,回落到了下面这组全局默认值。
		"group_matched":             consume.Rate.Matched,
		"global_topup_percent":      s.TopupRatePercent(),
		"global_consume_percent":    s.ConsumeRatePercent(),
		"global_redemption_percent": s.EffectiveRedemptionRatePercent(),
	}
}

// sumOutstanding 汇总"已计佣但尚未被结算吸收"的金额。
// unmaturedOnly 为真时只统计尚未过成熟期的部分。
//
// 句柄由调用方给:手工增减佣金要在**持有余额行锁的那个事务里**算这个数
// (见 api_admin_adjust.go 的可回收上限),自取 db.Get() 会绕开锁读到另一份快照。
func sumOutstanding(gdb *gorm.DB, inviterId int, unmaturedOnly bool) (decimal.Decimal, error) {
	q := gdb.Model(&Accrual{}).
		Where("inviter_id = ? AND status = ? AND settled_amount <> gross_amount",
			inviterId, StatusAccrued)
	if unmaturedOnly {
		q = q.Where("mature_at > ?", common.GetTimestamp())
	}
	var raw string
	if err := q.Select("COALESCE(SUM(gross_amount - settled_amount), 0)").Scan(&raw).Error; err != nil {
		db.MarkFailure(err)
		return decimal.Zero, err
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, nil
	}
	return d, nil
}

// earliestPendingMatureAt 返回名下最早成熟的那笔在途佣金的成熟时刻,没有则 0。
//
// 口径与结算的取数完全一致(settle.go 的第一路按 MIN(mature_at) 排队):
// status = accrued 且 settled_amount <> gross_amount。手工增减与冲正写的是
// mature_at = 0 的立即成熟行,它们会让这个数变成 0 —— 那正确:0 在这里的语义
// 是"没有需要等的东西",而立即成熟的行确实不用等。零值口径因此是唯一的:
// **没有在途佣金** 与 **在途的都已成熟** 对界面是同一句话("下一次日结就发"),
// 所以合成同一个值,前端不需要区分。
func earliestPendingMatureAt(gdb *gorm.DB, inviterId int) (int64, error) {
	var raw *int64
	if err := gdb.Model(&Accrual{}).
		Where("inviter_id = ? AND status = ? AND settled_amount <> gross_amount",
			inviterId, StatusAccrued).
		Select("MIN(mature_at)").Scan(&raw).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	if raw == nil {
		return 0, nil
	}
	return *raw, nil
}

// listInvitees 返回已邀请用户列表。
//
// 只下发脱敏名与不可逆的 ref。真实用户名、邮箱、user_id、IP、下线用了哪些
// 模型一律不下发 —— 邀请返佣不是获取他人隐私的授权。
func listInvitees(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	userId := c.GetInt("id")
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()

	var total int64
	if err := gdb.Model(&InviteRelation{}).
		Where("inviter_id = ? AND unbound_at = ?", userId, 0).
		Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []InviteRelation
	if err := gdb.Where("inviter_id = ? AND unbound_at = ?", userId, 0).
		Order("bound_at desc, invitee_id desc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}

	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.InviteeId)
	}
	totals, err := aggregateByInvitee(userId, ids)
	if err != nil {
		internalError(c, err)
		return
	}

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		agg := totals[r.InviteeId]
		items = append(items, gin.H{
			"ref":              r.InviteeRef,
			"masked_name":      r.MaskedName,
			"bound_at":         r.BoundAt,
			"total_base_quota": agg.BaseQuota,
			"total_commission": agg.Gross.String(),
			"blocked":          r.Blocked,
		})
	}
	respond(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

type inviteeAggregate struct {
	InviteeId int             `gorm:"column:invitee_id"`
	BaseQuota int64           `gorm:"column:base_quota"`
	Gross     decimal.Decimal `gorm:"column:gross"`
}

// aggregateByInvitee 按下线汇总累计基数与累计佣金。
//
// 现算而不是维护计数器列:计数器一旦与明细漂移就再也对不回去,
// 而这里的数据量是"当前页的下线数",聚合成本可忽略。
func aggregateByInvitee(inviterId int, inviteeIds []int) (map[int]inviteeAggregate, error) {
	out := map[int]inviteeAggregate{}
	if len(inviteeIds) == 0 {
		return out, nil
	}
	var rows []inviteeAggregate
	err := db.Get().Model(&Accrual{}).
		Select("invitee_id, COALESCE(SUM(base_quota),0) AS base_quota, COALESCE(SUM(gross_amount),0) AS gross").
		Where("inviter_id = ? AND invitee_id IN ? AND status <> ?", inviterId, inviteeIds, StatusVoided).
		Group("invitee_id").Scan(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	for _, r := range rows {
		out[r.InviteeId] = r
	}
	return out, nil
}

// listRecords 返回我的佣金流水。
func listRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	userId := c.GetInt("id")
	page, size := httpq.Paginate(c, listPaging)

	q := db.Get().Model(&Accrual{}).Where("inviter_id = ?", userId)
	if v := c.Query("source_type"); v != "" {
		q = q.Where("source_type = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := httpq.Int64(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := httpq.Int64(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []Accrual
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}

	refs, err := relationRefs(userId, rows)
	if err != nil {
		internalError(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		rel := refs[r.InviteeId]
		items = append(items, gin.H{
			"accrual_no":  r.AccrualNo,
			"source_type": r.SourceType,
			// 下线的订单号属于下线的隐私,只给能和客服对上号的后 4 位。
			"source_ref":          maskRef(r.SourceRef),
			"invitee_ref":         rel.InviteeRef,
			"invitee_masked_name": rel.MaskedName,
			"base_quota":          r.BaseQuota,
			// rate_percent 是本行冻结的费率(百分比);rate_bps 是同一个数字的
			// 旧口径,留给尚未切换的前端页面。
			"rate_percent":   config.FormatRatePercent(r.RateUnits),
			"rate_bps":       r.RateUnits,
			"gross_amount":   r.GrossAmount.String(),
			"settled_amount": r.SettledAmount.String(),
			"status":         r.Status,
			"mature_at":      r.MatureAt,
			"bucket_date":    r.BucketDate,
			"created_at":     r.CreatedAt,
		})
	}
	respond(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

func relationRefs(inviterId int, rows []Accrual) (map[int]InviteRelation, error) {
	out := map[int]InviteRelation{}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.InviteeId)
	}
	var rels []InviteRelation
	if err := db.Get().Where("inviter_id = ? AND invitee_id IN ?", inviterId, ids).
		Find(&rels).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	for _, r := range rels {
		out[r.InviteeId] = r
	}
	return out, nil
}
