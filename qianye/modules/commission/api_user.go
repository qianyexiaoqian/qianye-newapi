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
		"fiat_currency":        config.Get().Withdraw.FiatCurrency,
		"last_settled_at":      bal.LastSettledAt,
		// 比例对外一律是百分比字符串。旧的 *_bps 键继续下发是为了不打断
		// 已在跑的前端页面(它们自己再除以 100),等页面切到 *_percent 之后
		// 可以直接删掉这两行。
		//
		// 兑换码是第三档。下发的是**生效值**(没单独配时等于充值档)而不是
		// 配置值:用户端要回答的是"我下线用兑换码充值,我能拿几个点",
		// 空字符串在这里没有意义。redemption_follows_topup 保留那一位事实,
		// 界面据此在数字后面标一句"跟随充值档",省掉运营的一轮客服问答。
		//
		// 与充值/消费两档一样,这里给的是**全局**档:分组差异化费率按下线的
		// 分组逐笔解析,邀请人事先并不知道下一个下线落在哪个分组。
		"rate": gin.H{
			"topup_percent":            s.TopupRatePercent(),
			"consume_percent":          s.ConsumeRatePercent(),
			"redemption_percent":       s.EffectiveRedemptionRatePercent(),
			"topup_bps":                s.TopupRateUnits,
			"consume_bps":              s.ConsumeRateUnits,
			"redemption_bps":           s.EffectiveRedemptionRateUnits(),
			"redemption_follows_topup": s.RedemptionRateUnits == nil,
		},
		"policy": gin.H{
			"holding_days":            s.HoldingDays,
			"min_settle_quota":        s.MinSettleQuota,
			"settle_interval_seconds": cm.SettleIntervalSecs,
			"exclude_redemption":      cm.ExcludeRedemptionAndManual,
			"exclude_subscription":    cm.ExcludeSubscriptionConsume,
		},
	})
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
