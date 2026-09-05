package stardust

import (
	"context"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// ledger_check.go —— 星屑账本的自洽性体检(design §3.1 的三条恒等式 + 下线消费返的 I3)。
//
// # 为什么必须由服务端报,而不是留给谁去写 SQL
//
// 四条恒等式都不会自己喊疼:
//
//	I0  available == total_earned − total_spent + total_refunded + total_adjusted
//	I1  available == Σ qy_sd_ledger.amount(per user)
//	I2  Σ qy_sd_accrual.gross(settled) == Σ ledger.amount(kind=consume_rebate) + carry
//	I3  Σ qy_sd_invite_accrual.gross(settled) == Σ ledger.amount(kind=invite_consume) + invite_carry
//
// I0 由 kindColumn 的"一个 kind 只落一列"保证,I1 由账本只追加保证,I2 / I3 由结算的
// 余数结转保证 —— 前提是没有人绕过 Credit / Debit 直接改过表。直接 UPDATE 余额列、
// 删掉一行流水、手工改一个日桶的 status,接口全都照常 200,只有这里能看见。
// 所以报的是"哪几行对不上、差多少、最坏的是谁",让漂移在发生当天就有人看得见。
//
// I2 / I3 允许 |diff| < 1:gross 是 decimal 而账本只发整数,尚未结算吸收的零头就在 carry
// 里,两边的差不可能达到 1 —— 达到了就是某一次结算发错了整数。
//
// 只读:本文件不改任何一行。修复由人拿着流水行去做,体检只负责指出来。

// maxLedgerCheckUsers 是一次体检最多核对多少行余额。
// 超过就只报"这次没查全",而不是拖着体检接口跑一张全表扫描 —— 体检本身把站点拖慢,
// 是最没道理的一种故障。
const maxLedgerCheckUsers = 20000

func init() {
	adminRouteInstallers = append(adminRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/stardust/ledger-check", handleLedgerCheck)
	})
}

// ledgerCheckReport 是体检结果。契约字段之外多 ok / error:读失败时明说"这次没查成",
// 而不是让健康面板 500 —— 排查故障时最不需要的就是"健康页也打不开"。
type ledgerCheckReport struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`

	CheckedUsers int `json:"checked_users"`
	DriftedUsers int `json:"drifted_users"`
	WorstUserId  int `json:"worst_user_id"`
	// WorstDrift 是最坏那个人四条恒等式里偏得最远的一条的差值(带符号),decimal 字符串。
	WorstDrift string `json:"worst_drift"`

	HeldRows      int64  `json:"held_rows"`
	OldestHeldDay string `json:"oldest_held_day"`
	// HeldAlert:最老的 held 桶距今 ≥ held_alert_days 天(且阈值 > 0)。
	HeldAlert bool `json:"held_alert"`
}

func handleLedgerCheck(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	respondOK(c, ledgerCheck(c.Request.Context()))
}

// ledgerCheck 逐用户核对 I0 / I1 / I2 / I3,并统计暂缓桶(两张日桶表)的积龄。
func ledgerCheck(ctx context.Context) ledgerCheckReport {
	rep := ledgerCheckReport{WorstDrift: "0"}
	gdb := db.Get()
	if gdb == nil {
		rep.Error = db.ErrNotReady.Error()
		return rep
	}

	var balances []Balance
	if err := gdb.WithContext(ctx).Limit(maxLedgerCheckUsers + 1).Find(&balances).Error; err != nil {
		db.MarkFailure(err)
		rep.Error = err.Error()
		return rep
	}
	if len(balances) > maxLedgerCheckUsers {
		rep.Error = "余额行数超过一次体检的上界 " + strconv.Itoa(maxLedgerCheckUsers) +
			",本次未核对;需要把体检改成离线任务"
		return rep
	}

	// I1 的右边:每人的流水总和。
	var ledgerSums []struct {
		UserId int
		Total  int64
	}
	if err := gdb.WithContext(ctx).Model(&Ledger{}).
		Select("user_id, COALESCE(SUM(amount), 0) AS total").Group("user_id").Scan(&ledgerSums).Error; err != nil {
		db.MarkFailure(err)
		rep.Error = err.Error()
		return rep
	}
	sumByUser := make(map[int]int64, len(ledgerSums))
	for _, r := range ledgerSums {
		sumByUser[r.UserId] = r.Total
	}

	// I2 的左边:每人已结算日桶的 gross 之和。SUM 的结果按字符串读回再转 decimal:
	// 读成 float64 会丢掉 decimal(30,10) 的精度,而这里比的正是零头。
	var settledGross []struct {
		UserId int
		Gross  string
	}
	if err := gdb.WithContext(ctx).Model(&Accrual{}).
		Select("user_id, COALESCE(SUM(gross), 0) AS gross").
		Where("status = ?", AccrualSettled).Group("user_id").Scan(&settledGross).Error; err != nil {
		db.MarkFailure(err)
		rep.Error = err.Error()
		return rep
	}
	grossByUser := make(map[int]decimal.Decimal, len(settledGross))
	for _, r := range settledGross {
		d, err := decimal.NewFromString(r.Gross)
		if err != nil {
			rep.Error = "日桶 gross 汇总值无法解析: " + r.Gross
			return rep
		}
		grossByUser[r.UserId] = d
	}

	// I2 的右边:每人消费返流水之和。
	var rebateSums []struct {
		UserId int
		Total  int64
	}
	if err := gdb.WithContext(ctx).Model(&Ledger{}).
		Select("user_id, COALESCE(SUM(amount), 0) AS total").
		Where("kind = ?", string(KindConsumeRebate)).Group("user_id").Scan(&rebateSums).Error; err != nil {
		db.MarkFailure(err)
		rep.Error = err.Error()
		return rep
	}
	rebateByUser := make(map[int]int64, len(rebateSums))
	for _, r := range rebateSums {
		rebateByUser[r.UserId] = r.Total
	}

	// I3:下线消费返与 I2 同形 —— 每个邀请人已结算日桶的 gross 之和 == 他名下
	// invite_consume 流水之和 + invite_carry。两条各自闭合,谁欠谁才分得开。
	var inviteGross []struct {
		InviterId int
		Gross     string
	}
	if err := gdb.WithContext(ctx).Model(&InviteAccrual{}).
		Select("inviter_id, COALESCE(SUM(gross), 0) AS gross").
		Where("status = ?", AccrualSettled).Group("inviter_id").Scan(&inviteGross).Error; err != nil {
		db.MarkFailure(err)
		rep.Error = err.Error()
		return rep
	}
	inviteGrossByUser := make(map[int]decimal.Decimal, len(inviteGross))
	for _, r := range inviteGross {
		d, err := decimal.NewFromString(r.Gross)
		if err != nil {
			rep.Error = "下线消费返日桶 gross 汇总值无法解析: " + r.Gross
			return rep
		}
		inviteGrossByUser[r.InviterId] = d
	}
	var inviteSums []struct {
		UserId int
		Total  int64
	}
	if err := gdb.WithContext(ctx).Model(&Ledger{}).
		Select("user_id, COALESCE(SUM(amount), 0) AS total").
		Where("kind = ?", string(KindInviteConsume)).Group("user_id").Scan(&inviteSums).Error; err != nil {
		db.MarkFailure(err)
		rep.Error = err.Error()
		return rep
	}
	inviteByUser := make(map[int]int64, len(inviteSums))
	for _, r := range inviteSums {
		inviteByUser[r.UserId] = r.Total
	}

	one := decimal.NewFromInt(1)
	worst := decimal.Zero
	drifted := 0
	seen := make(map[int]bool, len(balances))
	for _, b := range balances {
		seen[b.UserId] = true
		i0 := decimal.NewFromInt(b.Available - (b.TotalEarned - b.TotalSpent + b.TotalRefunded + b.TotalAdjusted))
		i1 := decimal.NewFromInt(b.Available - sumByUser[b.UserId])
		i2 := grossByUser[b.UserId].Sub(decimal.NewFromInt(rebateByUser[b.UserId])).Sub(b.Carry)
		i3 := inviteGrossByUser[b.UserId].Sub(decimal.NewFromInt(inviteByUser[b.UserId])).Sub(b.InviteCarry)
		if i0.IsZero() && i1.IsZero() && i2.Abs().LessThan(one) && i3.Abs().LessThan(one) {
			continue
		}
		drifted++
		for _, d := range []decimal.Decimal{i0, i1, i2, i3} {
			if d.Abs().GreaterThan(worst.Abs()) {
				worst, rep.WorstUserId = d, b.UserId
			}
		}
	}
	// 有流水却没有余额行的人:LockBalance 在第一次记账之前就建行,这种形状只能是
	// 有人删过余额行。按 available=0 记成 I1 漂移。
	for userId, total := range sumByUser {
		if seen[userId] || total == 0 {
			continue
		}
		drifted++
		if d := decimal.NewFromInt(-total); d.Abs().GreaterThan(worst.Abs()) {
			worst, rep.WorstUserId = d, userId
		}
	}
	rep.CheckedUsers = len(balances)
	rep.DriftedUsers = drifted
	rep.WorstDrift = worst.String()

	// 暂缓桶的积龄:消费返与下线消费返两张日桶表的 held 行一并计,最老的桶按
	// bucket_date 取(uk 与 idx_qy_sd*_status 都在),不做 MIN() 聚合 ——
	// 一次 ORDER BY LIMIT 1 三种数据库同一口径。
	var inviteHeld int64
	if err := gdb.WithContext(ctx).Model(&Accrual{}).Where("status = ?", AccrualHeld).
		Count(&rep.HeldRows).Error; err != nil {
		db.MarkFailure(err)
		rep.Error = err.Error()
		return rep
	}
	if err := gdb.WithContext(ctx).Model(&InviteAccrual{}).Where("status = ?", AccrualHeld).
		Count(&inviteHeld).Error; err != nil {
		db.MarkFailure(err)
		rep.Error = err.Error()
		return rep
	}
	rep.HeldRows += inviteHeld
	if rep.HeldRows > 0 {
		var oldest []Accrual
		if err := gdb.WithContext(ctx).Select("bucket_date").Where("status = ?", AccrualHeld).
			Order("bucket_date asc").Limit(1).Find(&oldest).Error; err != nil {
			db.MarkFailure(err)
			rep.Error = err.Error()
			return rep
		}
		if len(oldest) == 1 {
			rep.OldestHeldDay = oldest[0].BucketDate
		}
		var oldestInvite []InviteAccrual
		if err := gdb.WithContext(ctx).Select("bucket_date").Where("status = ?", AccrualHeld).
			Order("bucket_date asc").Limit(1).Find(&oldestInvite).Error; err != nil {
			db.MarkFailure(err)
			rep.Error = err.Error()
			return rep
		}
		if len(oldestInvite) == 1 && (rep.OldestHeldDay == "" || oldestInvite[0].BucketDate < rep.OldestHeldDay) {
			rep.OldestHeldDay = oldestInvite[0].BucketDate
		}
	}
	if alertDays := effectiveCtx(ctx).HeldAlertDays; alertDays > 0 && rep.OldestHeldDay != "" {
		if start, ok := invite.DayKeyStart(rep.OldestHeldDay); ok {
			ageDays := (invite.DayStart(common.GetTimestamp()) - start) / 86400
			rep.HeldAlert = ageDays >= int64(alertDays)
		}
	}
	rep.OK = true
	return rep
}
