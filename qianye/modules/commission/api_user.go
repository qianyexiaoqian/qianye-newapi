package commission

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// getSummary 返回"我的推广"看板里星辉佣金那一半。
//
// 刻意把未结算余数也下发(且用字符串,避免 JS 的 Number 精度丢失):
// 用户最常见的困惑是"我用了一整天怎么没佣金",让他看见 0.4271 正在累积,
// 比任何文案解释都有效。
//
// 金额一律是额度整数,前端用 QyAmountText 按站点展示单位(星辉)渲染 ——
// 本接口不出现任何法币字段。
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
	var inviteeCount int64
	if err := gdb.Model(&invite.InviteRelation{}).
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
	now := common.GetTimestamp()
	respond(c, gin.H{
		"invitee_count": inviteeCount,
		// 两段余额:可用(等着自动入账)/ 已入账(已经发成星屑)。
		//
		// D-15 时中间还有一段"在途"—— 佣金记星辉,入账要跨库,存在"已开单、主库
		// 还没落定"的中间态。D-16 入账是本地事务,那一段不再存在。
		"available":        bal.Available,
		"credited":         bal.Credited,
		"total_earned":     bal.TotalEarned,
		"total_clawback":   bal.TotalClawback,
		"unsettled_amount": bal.UnsettledAmount.String(),
		"pending_mature":   pendingMature.Floor().String(),
		"debt_blocked":     bal.DebtBlocked,
		"last_settled_at":  bal.LastSettledAt,
		"last_credited_at": bal.LastCreditedAt,
		// 自动入账:门槛与下一次最早开跑的时刻。可用余额 >= min_credit_stardust 且没有
		// 欠账时,next_credit_at 之后的那个周期就会开单,用户不必做任何事。
		"min_credit_stardust": s.MinCreditStardust,
		"next_credit_at":      nextCreditAt(now),
		// 比例对外一律是百分比字符串;*_bps 是同一个数字的万分比形式。
		//
		// 这里给的是**这个人自己**的档,不是全局默认档。费率按推广人自己的
		// 账号分组解析(见 grouprate.go 的口径),所以"我能拿几个点"对每个
		// 推广人是一个确定的数字。group / group_matched / global_* 三组一起下发,
		// 是为了让界面能回答"为什么是这个数":命中了 vip 档、还是回落到全局默认。
		"rate": rateSummary(c, userId, s),
		// pending_earliest_mature_at 是**账本上的事实**:名下还没被结算吸收的
		// 计佣行里,最早的那个 mature_at。0 表示没有在途佣金。
		//
		// 它与下面 policy.payout_day_offset 的分工:后者按**当前配置**算出来的 T+N,
		// 只对**此后**新产生的消费成立;前者是已经挣到手的那批钱**实际**什么时候成熟
		// (持有期逐行冻结,改配置影响不到它)。界面必须两个一起显示。
		"pending_earliest_mature_at": earliestMature,
		"policy": gin.H{
			"holding_days":        s.HoldingDays,
			"min_settle_stardust": s.MinSettleStardust,
			// payout_day_offset = holding_days + 1:消费所在的那一天要整天结束才封板
			// (见 bucketMatureAt),holding_days=0 也是**次日**结算进可用余额。
			"settle_interval_seconds": cm.SettleIntervalSecs,
			"settle_daily":            true,
			"payout_day_offset":       payoutDayOffset(s.HoldingDays),
			"credit_interval_seconds": cm.CreditIntervalSecs,
			"day_offset_minutes":      invite.DayOffsetMinutes(),
			"exclude_redemption":      cm.ExcludeRedemptionAndManual,
			"exclude_subscription":    cm.ExcludeSubscriptionConsume,
		},
	})
}

// rateSummary 是"我现在走哪一档、为什么"这个问题的应答。
//
// 三次 resolveInviterPricing 而不是一次解析出分组再自己查三档:那样就要在
// 展示路径上复刻一份"取谁的分组、命中哪一档"的判定,而复刻品迟早会与计佣
// 路径漂移 —— 界面上写着 8%、账本按 5% 发钱,是本仓最忌讳的形状。
//
// userId 在这里既是"看板的主人"也是"计佣时的上线",两者本来就是同一个人。
func rateSummary(c *gin.Context, userId int, s opSettings) gin.H {
	// 展示路径必须**闭嘴**:同一次页面刷新在这里解析三次,而降级计数器的全部
	// 用途是回答「哪段时间的佣金要复核」(口径与理由见 settings.go 的 silentDegradeCtx)。
	ctx := silentDegradeCtx(c.Request.Context())
	topup := resolveInviterPricing(ctx, userId, SourceTopup, s)
	consume := resolveInviterPricing(ctx, userId, SourceConsume, s)
	redemption := resolveInviterPricing(ctx, userId, SourceRedemption, s)
	return gin.H{
		"topup_percent":      config.FormatRatePercent(topup.Units),
		"consume_percent":    config.FormatRatePercent(consume.Units),
		"redemption_percent": config.FormatRatePercent(redemption.Units),
		"topup_bps":          topup.Units,
		"consume_bps":        consume.Units,
		"redemption_bps":     redemption.Units,
		// 「跟随充值档」这一位只在**本人这一档**上成立才该显示:判据是两档算出来的
		// 数字相等 —— 那正是界面上"跟随"要表达的全部意思。
		"redemption_follows_topup": redemption.Units == topup.Units,
		// group 是解析用的那个分组名(已归一化)。空串表示这次没能解析出
		// 账号分组(主库读失败),界面据此闭嘴而不是编一个分组名出来。
		"group": consume.Group,
		// group_matched 回答"为什么是这个数":true = 命中了你所在分组的档,
		// false = 你所在的分组没单独配,回落到了下面这组全局默认值。
		"group_matched":             consume.Matched,
		"global_topup_percent":      s.TopupRatePercent(),
		"global_consume_percent":    s.ConsumeRatePercent(),
		"global_redemption_percent": s.EffectiveRedemptionRatePercent(),
	}
}

// sumOutstanding 汇总"已计佣但尚未被结算吸收"的金额。
// unmaturedOnly 为真时只统计尚未过持有期的部分。
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
// 是"没有需要等的东西"。**没有在途佣金** 与 **在途的都已成熟** 对界面是同一句话
// ("下一次日结就发"),所以合成同一个值。
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
			// rate_percent 是本行冻结的费率(百分比);rate_bps 是同一个数字的万分比。
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

// relationRefs 用 invite 的关系快照给流水行补脱敏名与对外标识。
//
// 只下发脱敏名与不可逆的 ref。真实用户名、邮箱、user_id 一律不下发 ——
// 推广佣金不是获取他人隐私的授权。
func relationRefs(inviterId int, rows []Accrual) (map[int]invite.InviteRelation, error) {
	out := map[int]invite.InviteRelation{}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.InviteeId)
	}
	var rels []invite.InviteRelation
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

// creditView 是一条自动入账记录对用户端与管理端的形状。
type creditView struct {
	CreditNo string `json:"credit_no"`
	UserId   int    `json:"user_id"`
	// Amount 是这一笔入账的星屑数;LedgerNo 是星屑账本那一侧的流水号,
	// 拿着它去「星屑 → 流水」按号一查就是同一笔。
	Amount     int64  `json:"amount"`
	LedgerNo   string `json:"ledger_no"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at"`
	Remark     string `json:"remark"`
}

func creditViews(rows []Credit) []creditView {
	out := make([]creditView, 0, len(rows))
	for _, r := range rows {
		out = append(out, creditView{
			CreditNo: r.CreditNo, UserId: r.UserId, Amount: r.Amount, LedgerNo: r.LedgerNo,
			Status: r.Status, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, Remark: r.Remark,
		})
	}
	return out
}

// listCredits 返回我的自动入账记录。
func listCredits(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	userId := c.GetInt("id")
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Credit{}).Where("user_id = ?", userId)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []Credit
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": creditViews(rows), "total": total, "p": page, "page_size": size})
}
