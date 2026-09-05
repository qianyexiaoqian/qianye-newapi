package stardust

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settle_invite_test.go —— 下线消费返(D-14 / design-15 §4.7)的日结 e2e。
//
// 与消费返跑在同一次 runSettle 里,但要钉住的是它**独有**的几条:
//
//	① 一个邀请人名下几个下线的消费合成**一行** invite_consume 流水,档位按**邀请人**的分组;
//	② 余数结转在 invite_carry,与本人消费返的 carry 互不串账;
//	③ 暂缓看的是邀请人的账号状态,held 桶在重跑时按冻结的 gross 补发;
//	④ 拉黑的关系、分组档 0、合规未确认:一行日桶都不写。
//
// 主库用户 id 取 501–530(邀请关系缓存是进程级的,见 tasks_helpers_test.go)。

// inviteAccrualsOfDay 按 (邀请人, 下线) 索引某一天的下线消费返日桶。
func inviteAccrualsOfDay(t *testing.T, env apiEnv, day string) map[[2]int]InviteAccrual {
	t.Helper()
	var rows []InviteAccrual
	require.NoError(t, env.ext.Where("bucket_date = ?", day).Find(&rows).Error)
	out := make(map[[2]int]InviteAccrual, len(rows))
	for _, r := range rows {
		out[[2]int{r.InviterId, r.InviteeId}] = r
	}
	return out
}

// ledgerOfKind 取一个用户某一种 kind 的全部流水。
func ledgerOfKind(t *testing.T, env apiEnv, userId int, kind Kind) []Ledger {
	t.Helper()
	out := make([]Ledger, 0, 2)
	for _, l := range ledgerOf(t, env.ext, userId) {
		if l.Kind == string(kind) {
			out = append(out, l)
		}
	}
	return out
}

func TestRunSettlePaysInviteConsumeByInviterGroupAndHoldsRiskyInviters(t *testing.T) {
	env := newTaskEnv(t, func(s *config.Stardust) { s.InviteConsumeBps = 1000 })
	withCompliance(t, true)
	ctx := context.Background()
	now := common.GetTimestamp()
	runDate, day, ready := settleTargetDay(now, 0)
	require.True(t, ready)
	start, ok := invite.DayKeyStart(day)
	require.True(t, ok)
	at := start + 3600

	// vip 档 20%;zero 档显式 0(启用)。default 走全站 10%。
	require.NoError(t, env.ext.Create(&[]GroupRate{
		{UserGroup: "vip", InviteConsumeBps: bpsPtr(2000), Enabled: true},
		{UserGroup: "zero", InviteConsumeBps: bpsPtr(0), Enabled: true},
	}).Error)
	invalidateGroupRates()

	// 501(vip)名下 502 / 503;501 自己也消费,用来证明两条 carry 不串。
	seedMainUser(t, env.main, model.User{Id: 501, Group: "vip", Quota: 100})
	seedMainUser(t, env.main, model.User{Id: 502, InviterId: 501, Quota: 100})
	seedMainUser(t, env.main, model.User{Id: 503, InviterId: 501, Quota: 100})
	// 504(default)名下 505。
	seedMainUser(t, env.main, model.User{Id: 504, Quota: 100})
	seedMainUser(t, env.main, model.User{Id: 505, InviterId: 504, Quota: 100})
	// 506 透支,名下 507 消费正常:507 自己的消费返照发,506 的下线返暂缓。
	seedMainUser(t, env.main, model.User{Id: 506, Quota: -1})
	seedMainUser(t, env.main, model.User{Id: 507, InviterId: 506, Quota: 100})
	// 508 ↔ 509 互邀:首次解析就被自动拉黑(拉黑名单缓存是进程级的,直接往表里塞一行
	// 绕不过它 —— 生产里的人工拉黑走 invite 的管理端接口,那条路会做失效)。
	seedMainUser(t, env.main, model.User{Id: 508, InviterId: 509, Quota: 100})
	seedMainUser(t, env.main, model.User{Id: 509, InviterId: 508, Quota: 100})
	// 510 在 zero 档,名下 511。
	seedMainUser(t, env.main, model.User{Id: 510, Group: "zero", Quota: 100})
	seedMainUser(t, env.main, model.User{Id: 511, InviterId: 510, Quota: 100})

	require.NoError(t, env.main.Create(&[]model.Log{
		consumeLog(501, at, 750_000, ""),
		consumeLog(502, at, 3_000_000, ""),
		consumeLog(502, at+1, 500_000, `{"violation_fee":true}`), // 排除项与消费返同口径
		consumeLog(503, at, 1_250_000, ""),
		consumeLog(505, at, 5_000_000, ""),
		consumeLog(507, at, 5_000_000, ""),
		consumeLog(509, at, 5_000_000, ""),
		consumeLog(511, at, 5_000_000, ""),
	}).Error)

	runSettle(ctx)

	run := settleRunOf(t, env.ext, runDate)
	require.NotNil(t, run)
	assert.Equal(t, SettleRunDone, run.Status)
	assert.Equal(t, 2, run.InviteProcessed, "501 与 504 各结一笔")
	assert.Equal(t, 1, run.InviteHeld, "506 透支")
	assert.Equal(t, 0, run.InviteFailed)
	assert.EqualValues(t, 2, run.InviteGranted)

	acc := inviteAccrualsOfDay(t, env, day)
	require.Len(t, acc, 4, "(501,502) (501,503) (504,505) (506,507);拉黑与 0 档不写行: %v", acc)

	// ① 501:vip 20%。502 基数 3M → 6 单位 → 1.2;503 基数 1.25M → 2.5 单位 → 0.5;合计 1.7 → 发 1。
	a502, a503 := acc[[2]int{501, 502}], acc[[2]int{501, 503}]
	assert.Equal(t, AccrualSettled, a502.Status)
	assert.EqualValues(t, 3_000_000, a502.BaseQuota, "违规扣费不进基数")
	assert.Equal(t, "vip", a502.RateGroup, "档位按邀请人的分组")
	assert.Equal(t, 2000, a502.Bps)
	assert.EqualValues(t, 500_000, a502.QuotaPerUnit)
	assert.True(t, a502.Gross.Equal(decimal.RequireFromString("1.2")), a502.Gross.String())
	assert.True(t, a503.Gross.Equal(decimal.RequireFromString("0.5")), a503.Gross.String())
	assert.Empty(t, a502.HoldReason)

	l501 := ledgerOfKind(t, env, 501, KindInviteConsume)
	require.Len(t, l501, 1, "两个下线合成一行流水")
	assert.EqualValues(t, 1, l501[0].Amount)
	assert.Equal(t, inviteSettleIdemScope, l501[0].IdemScope)
	assert.Equal(t, "sdinvite:"+runDate+":501", l501[0].IdemKey)
	assert.Equal(t, inviteSettleRefType, l501[0].RefType)
	assert.Equal(t, runDate, l501[0].RefNo)
	assert.Equal(t, 2000, l501[0].RateBps)
	assert.Equal(t, "vip", l501[0].RateGroup)
	assert.EqualValues(t, 4_250_000, l501[0].BaseQuota, "流水上冻结的是两个下线的基数之和")
	assert.Zero(t, l501[0].PeerUserId, "按邀请人汇总发,不指向某一个下线")
	assert.Equal(t, l501[0].LedgerNo, a502.LedgerNo)
	assert.Equal(t, l501[0].LedgerNo, a503.LedgerNo)

	// ② 501 自己的消费返:750k → 1.5 → 发 1、carry 0.5;invite_carry 0.7。两条各自结转。
	own := ledgerOfKind(t, env, 501, KindConsumeRebate)
	require.Len(t, own, 1)
	assert.EqualValues(t, 1, own[0].Amount)
	b501 := balanceOf(t, env.ext, 501)
	require.NotNil(t, b501)
	assert.EqualValues(t, 2, b501.Available)
	assert.EqualValues(t, 2, b501.TotalEarned, "invite_consume 落在 total_earned")
	assert.True(t, b501.Carry.Equal(decimal.RequireFromString("0.5")), b501.Carry.String())
	assert.True(t, b501.InviteCarry.Equal(decimal.RequireFromString("0.7")), b501.InviteCarry.String())

	// 504:全站 10%。505 基数 5M → 10 单位 → 1.0 → 发 1、invite_carry 0。
	a505 := acc[[2]int{504, 505}]
	assert.Equal(t, "default", a505.RateGroup)
	assert.Equal(t, 1000, a505.Bps)
	assert.True(t, a505.Gross.Equal(decimal.NewFromInt(1)), a505.Gross.String())
	l504 := ledgerOfKind(t, env, 504, KindInviteConsume)
	require.Len(t, l504, 1)
	assert.EqualValues(t, 1, l504[0].Amount)
	assert.Empty(t, ledgerOfKind(t, env, 504, KindConsumeRebate), "504 自己没消费")

	// ③ 506 透支:桶 held、不 Credit,余额行记原因;507 自己的消费返不受影响。
	a507 := acc[[2]int{506, 507}]
	assert.Equal(t, AccrualHeld, a507.Status)
	assert.Equal(t, HoldOverdraft, a507.HoldReason)
	assert.Empty(t, a507.LedgerNo)
	assert.True(t, a507.Gross.Equal(decimal.NewFromInt(1)), a507.Gross.String())
	assert.Empty(t, ledgerOf(t, env.ext, 506))
	b506 := balanceOf(t, env.ext, 506)
	require.NotNil(t, b506)
	assert.Equal(t, HoldOverdraft, b506.HoldReason)
	assert.Len(t, ledgerOfKind(t, env, 507, KindConsumeRebate), 1, "下线自己的消费返与上线的状态无关")

	// ④ 拉黑与 0 档:没有桶、没有流水;下线自己的消费返照发。
	assert.Empty(t, ledgerOf(t, env.ext, 508))
	assert.Empty(t, ledgerOf(t, env.ext, 510))
	assert.Len(t, ledgerOfKind(t, env, 509, KindConsumeRebate), 1)
	assert.Len(t, ledgerOfKind(t, env, 511, KindConsumeRebate), 1)

	// 体检:I3 靠 invite_carry 闭合;held 行把两张日桶表都数进去。
	rep := ledgerCheck(ctx)
	assert.True(t, rep.OK)
	assert.Zero(t, rep.DriftedUsers, "worst=%d/%s", rep.WorstUserId, rep.WorstDrift)
	assert.EqualValues(t, 1, rep.HeldRows)
	assert.Equal(t, day, rep.OldestHeldDay)

	// 506 余额转正,同时把全站比例改成 50%:held 桶按冻结的 gross 补发 1,不是 5。
	require.NoError(t, env.main.Model(&model.User{}).Where("id = ?", 506).Update("quota", 0).Error)
	useConfig(t, stardustConfig(func(s *config.Stardust) { s.QuotaPerUnit, s.InviteConsumeBps = 500_000, 5000 }))

	stats, err := rerunDay(ctx, day)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.InviteSettled)
	assert.Equal(t, 0, stats.InviteHeld)
	assert.Equal(t, 0, stats.InviteFailed)
	assert.EqualValues(t, 1, stats.InviteGranted)
	assert.Equal(t, 0, stats.Settled, "消费返那一段早已全部 settled,重跑不动它")
	l506 := ledgerOfKind(t, env, 506, KindInviteConsume)
	require.Len(t, l506, 1)
	assert.EqualValues(t, 1, l506[0].Amount)
	assert.True(t, strings.HasPrefix(l506[0].IdemKey, "sdinviterr:"+day+":506:"), l506[0].IdemKey)
	a507 = inviteAccrualsOfDay(t, env, day)[[2]int{506, 507}]
	assert.Equal(t, AccrualSettled, a507.Status)
	assert.Equal(t, l506[0].LedgerNo, a507.LedgerNo)
	assert.Empty(t, a507.HoldReason)
	assert.Empty(t, balanceOf(t, env.ext, 506).HoldReason)
	assert.Len(t, ledgerOfKind(t, env, 501, KindInviteConsume), 1, "已 settled 的邀请人不受重跑影响")

	// 再跑一次:什么都不该再发。
	stats, err = rerunDay(ctx, day)
	require.NoError(t, err)
	assert.Equal(t, RerunStats{}, stats)
	rep = ledgerCheck(ctx)
	assert.True(t, rep.OK)
	assert.Zero(t, rep.DriftedUsers)
	assert.Zero(t, rep.HeldRows)
}

// TestInviteConsumeWritesNothingWithoutComplianceOrRate 钉住"一行都不写"的三种情形:
// 合规未确认(分组表配了也不算)、全站 0 且分组无档。消费返那一段照常。
func TestInviteConsumeWritesNothingWithoutComplianceOrRate(t *testing.T) {
	env := newTaskEnv(t, func(s *config.Stardust) { s.InviteConsumeBps = 1000 })
	ctx := context.Background()
	now := common.GetTimestamp()
	_, day, _ := settleTargetDay(now, 0)
	start, ok := invite.DayKeyStart(day)
	require.True(t, ok)

	require.NoError(t, env.ext.Create(&GroupRate{UserGroup: "vip", InviteConsumeBps: bpsPtr(9000), Enabled: true}).Error)
	invalidateGroupRates()
	seedMainUser(t, env.main, model.User{Id: 521, Group: "vip", Quota: 100})
	seedMainUser(t, env.main, model.User{Id: 522, InviterId: 521, Quota: 100})
	require.NoError(t, env.main.Create(&[]model.Log{consumeLog(522, start+10, 5_000_000, "")}).Error)

	withCompliance(t, false)
	runSettle(ctx)
	assert.Empty(t, inviteAccrualsOfDay(t, env, day), "合规未确认:分组档配了 90% 也不算")
	assert.Empty(t, ledgerOf(t, env.ext, 521))
	assert.Len(t, ledgerOfKind(t, env, 522, KindConsumeRebate), 1, "消费返不受合规门约束")

	// 合规确认了,但全站 0、分组也没档:仍然一行不写。用重跑走同一条重算路径。
	withCompliance(t, true)
	useConfig(t, stardustConfig(func(s *config.Stardust) { s.QuotaPerUnit, s.InviteConsumeBps = 500_000, 0 }))
	require.NoError(t, env.ext.Where("user_group = ?", "vip").Delete(&GroupRate{}).Error)
	invalidateGroupRates()
	stats, err := rerunDay(ctx, day)
	require.NoError(t, err)
	assert.Equal(t, RerunStats{}, stats)
	assert.Empty(t, inviteAccrualsOfDay(t, env, day))
	assert.Empty(t, ledgerOf(t, env.ext, 521))
}
