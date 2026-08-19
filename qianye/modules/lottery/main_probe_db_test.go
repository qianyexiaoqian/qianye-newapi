package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// main_probe_db_test.go —— "一张 Failed 的资金单绝不等于主库没动钱"。
//
// markFailed 对**任何** mainErr 都把单置成 Failed,包括 commit 阶段断连
// (事务其实已经提交)。本模块有三处依赖这个判据做不可逆动作:
//
//	failPayout / RetryPayout → 换代次重开单 = 再给用户加一次钱
//	releaseEntryOnFailure    → 回滚预占     = 宣布这笔钱没扣过
//	convergeExcluded         → 判 failed    = 参与费永久退不回来
//
// 三处都必须只在 twophase.MainNotApplied 这一个明确取值上动手。

// newProbeMainDB 接一个只装 outbox 探针表的主库。
//
// 探针查的是主库,不接主库就只能把探针关掉,而"探针关掉"恰恰是被判定为
// 不可判定的那一支 —— 用它当测试前提就等于测不到真实分支。
func newProbeMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.QyFundOutbox{}))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// seedOutboxRow 落一行主库探针,表示"这一笔的主库事务确实提交过"。
func seedOutboxRow(t *testing.T, main *gorm.DB, orderNo string) {
	t.Helper()
	require.NoError(t, main.Create(&model.QyFundOutbox{
		OrderNo: orderNo, Kind: qymodel.KindLotteryPayout,
		UserId: 9, Amount: 500, CreatedAt: common.GetTimestamp(),
	}).Error)
}

// 探针说主库已经加过钱 → 自动重试必须转人工,绝不换代次。
//
// 换代次就是换幂等键,下一轮 DrivePayouts 会拿一个全新单号重新 creditMainQuota,
// 同一笔奖金发第二次,而且 payout=paid、fund_order=success、零 flag。
func TestFailPayout_HoldsWhenProbeSaysMainApplied(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutPaying
		p.Attempts = 1
	})
	order := &qymodel.FundOrder{
		OrderNo: "LP-ambig", Kind: qymodel.KindLotteryPayout, Status: qymodel.StatusFailed,
		IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
		UserId: p.UserId, AmountQuota: p.AmountQuota, CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, gdb.Create(order).Error)
	seedOutboxRow(t, model.DB, order.OrderNo)

	failPayout(context.Background(), gdb, p, order, assert.AnError)

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.Equal(t, 0, after.Epoch, "主库已经加过钱,换代次就是第二次发钱")
}

// 探针整体关掉 → 判不出来,同样必须转人工。
//
// 旧实现在这里返回"确定没生效",于是任何一张 Failed 单都会被换代次重开,
// 只要主库那一次真的提交过就是无声超发 —— 不需要等保留期。
func TestFailPayout_HoldsWhenProbeUnavailable(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	off := false
	cfg := *qyConfig.Load()
	cfg.TwoPhase.MainOutboxEnabled = &off
	prev := qyConfig.Swap(&cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })

	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutPaying
		p.Attempts = 1
	})
	order := &qymodel.FundOrder{
		OrderNo: "LP-noprobe", Kind: qymodel.KindLotteryPayout, Status: qymodel.StatusFailed,
		IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
		UserId: p.UserId, AmountQuota: p.AmountQuota, CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, gdb.Create(order).Error)

	failPayout(context.Background(), gdb, p, order, assert.AnError)

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.Equal(t, 0, after.Epoch, "探针不可用时不许猜'主库没动'")
}

// 管理端重试 + 探针说主库已加钱 → 409,且保留期清理跑过之后仍然是 409。
//
// F1 的完整链:PruneOutbox 曾经按 created_at 一刀切,把这张 Failed 单的
// 探针行删掉,同一条 retry 请求就从 409 变成 200,10 秒内钱发第二次。
func TestRetryPayout_StaysManualAfterOutboxPrune(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutHeld
		p.Attempts = 8
	})
	// 保留期是 30 天,把单与探针行都造成 40 天前 —— 正是清理任务的目标。
	old := common.GetTimestamp() - 40*86400
	require.NoError(t, gdb.Create(&qymodel.FundOrder{
		OrderNo: "LP-aged", Kind: qymodel.KindLotteryPayout, Status: qymodel.StatusFailed,
		IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
		UserId: p.UserId, AmountQuota: p.AmountQuota, CreatedAt: old, UpdatedAt: old,
	}).Error)
	require.NoError(t, model.DB.Create(&model.QyFundOutbox{
		OrderNo: "LP-aged", Kind: qymodel.KindLotteryPayout,
		UserId: p.UserId, Amount: p.AmountQuota, CreatedAt: old,
	}).Error)

	require.ErrorIs(t, RetryPayout(context.Background(), p.PayoutNo), errPayoutNeedsManual)

	// 跑真实的清理任务,而不是手工 DELETE。
	twophase.PruneOutbox(context.Background())

	var probes int64
	require.NoError(t, model.DB.Model(&model.QyFundOutbox{}).
		Where("order_no = ?", "LP-aged").Count(&probes).Error)
	assert.EqualValues(t, 1, probes, "非 Success 单的探针行是唯一证据,保留期到了也不能删")

	require.ErrorIs(t, RetryPayout(context.Background(), p.PayoutNo), errPayoutNeedsManual)
	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.Equal(t, 0, after.Epoch, "清理任务不得把'不许重发'降级成'可以重发'")
}

// 管理端重试 + 探针不可用 → 409,绝不换代次。
func TestRetryPayout_RefusesWhenProbeUnavailable(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	off := false
	cfg := *qyConfig.Load()
	cfg.TwoPhase.MainOutboxEnabled = &off
	prev := qyConfig.Swap(&cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })

	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) { p.Status = PayoutHeld })
	require.NoError(t, gdb.Create(&qymodel.FundOrder{
		OrderNo: "LP-retry-noprobe", Kind: qymodel.KindLotteryPayout, Status: qymodel.StatusFailed,
		IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
		UserId: p.UserId, AmountQuota: p.AmountQuota, CreatedAt: common.GetTimestamp(),
	}).Error)

	require.ErrorIs(t, RetryPayout(context.Background(), p.PayoutNo), errPayoutNeedsManual)
	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.Equal(t, 0, after.Epoch)
}

// ───────────────────── 报名侧:回滚预占的同一条判据 ─────────────────────

func seedPendingEntryRow(t *testing.T, gdb *gorm.DB, act *Activity, orderNo string) *Entry {
	t.Helper()
	now := common.GetTimestamp()
	e := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: orderNo, Seq: 1,
		UserId: 9, Amount: 1000, Status: EntryPending, OrderNo: orderNo,
		CreatedAt: now,
	}
	require.NoError(t, gdb.Create(e).Error)
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("pending_count", 1).Error)
	return e
}

func entryById(t *testing.T, gdb *gorm.DB, id int64) *Entry {
	t.Helper()
	var e Entry
	require.NoError(t, gdb.Where("id = ?", id).Take(&e).Error)
	return &e
}

// 回滚预占等于宣布"这笔钱没扣过"。探针不可用 / 探针说已扣,都必须拒绝回滚。
func TestReleaseEntryOnFailure_RollsBackOnlyWhenProbeSaysNotApplied(t *testing.T) {
	cases := []struct {
		name     string
		outboxOn bool
		hasRow   bool
		want     string
	}{
		{name: "探针说没扣 → 回滚", outboxOn: true, want: EntryFailed},
		{name: "探针说已扣 → 不回滚", outboxOn: true, hasRow: true, want: EntryPending},
		{name: "探针不可用 → 不回滚", outboxOn: false, want: EntryPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newPayoutEnv(t, config.Lottery{Enabled: true})
			if !tc.outboxOn {
				off := false
				cfg := *qyConfig.Load()
				cfg.TwoPhase.MainOutboxEnabled = &off
				prev := qyConfig.Swap(&cfg)
				t.Cleanup(func() { qyConfig.Store(prev) })
			}
			act := seedActivity(t, gdb, nil)
			e := seedPendingEntryRow(t, gdb, act, "LE-rb")
			if tc.hasRow {
				seedOutboxRow(t, model.DB, "LE-rb")
			}
			order := &qymodel.FundOrder{
				OrderNo: "LE-rb", Kind: qymodel.KindLotteryEntry, Status: qymodel.StatusFailed,
				RefId: e.EntryNo, UserId: e.UserId, AmountQuota: e.Amount,
				CreatedAt: common.GetTimestamp(),
			}
			releaseEntryOnFailure(context.Background(), order, e, assert.AnError)

			assert.Equal(t, tc.want, entryById(t, gdb, e.Id).Status)
		})
	}
}

// ───────────────────── convergeExcluded 的三条分支 ─────────────────────

func seedExcludedEntry(t *testing.T, gdb *gorm.DB, act *Activity, orderNo string, orderStatus int8) *Entry {
	t.Helper()
	now := common.GetTimestamp()
	e := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: orderNo, Seq: 1,
		UserId: 9, Amount: 1000, Status: EntryExcluded, OrderNo: orderNo,
		CreatedAt: now, SettledAt: now,
	}
	require.NoError(t, gdb.Create(e).Error)
	require.NoError(t, gdb.Create(&qymodel.FundOrder{
		OrderNo: orderNo, Kind: qymodel.KindLotteryEntry, Status: orderStatus,
		IdemScope: "lottery_entry", IdemKey: orderNo,
		UserId: e.UserId, AmountQuota: e.Amount, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return e
}

func payoutsOf(t *testing.T, gdb *gorm.DB, actId int64) []Payout {
	t.Helper()
	var rows []Payout
	require.NoError(t, gdb.Where("act_id = ?", actId).Find(&rows).Error)
	return rows
}

// 资金单被判 Failed,但主库探针说这笔参与费真的扣过 → 必须登记退款。
//
// 旧实现在这里闭眼判 failed:钱不退、无退款计划、活动随即 finished,
// runSettle 从此不扫,模块内没有任何补登退款的接口 —— 用户的参与费被静默吞掉。
func TestConvergeExcluded_RefundsWhenProbeSaysMainApplied(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, ExcludedManualAfterSeconds: 1})
	act := seedActivity(t, gdb, nil)
	e := seedExcludedEntry(t, gdb, act, "LE-ambig", qymodel.StatusFailed)
	seedOutboxRow(t, model.DB, "LE-ambig")

	convergeExcluded(context.Background(), gdb, act)

	var after Entry
	require.NoError(t, gdb.Where("id = ?", e.Id).Take(&after).Error)
	assert.Equal(t, EntryRefunded, after.Status, "钱真的扣了就必须退,不能判失败")

	plans := payoutsOf(t, gdb, act.Id)
	require.Len(t, plans, 1, "必须留下一条退款计划,worker 才会真的把钱退回去")
	assert.Equal(t, PayoutRefund, plans[0].Kind)
	assert.EqualValues(t, 1000, plans[0].AmountQuota)

	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagEntryStuck).
		Find(&flags).Error)
	assert.Len(t, flags, 1, "资金单与主库对不上,必须同时留一条红点给人复核")
}

// 探针确认主库确实没动钱 → 判失败,不登记退款(退一笔从没收过的钱是假账)。
func TestConvergeExcluded_FailsWhenProbeSaysMainNotApplied(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, ExcludedManualAfterSeconds: 1})
	act := seedActivity(t, gdb, nil)
	e := seedExcludedEntry(t, gdb, act, "LE-clean", qymodel.StatusFailed)

	convergeExcluded(context.Background(), gdb, act)

	var after Entry
	require.NoError(t, gdb.Where("id = ?", e.Id).Take(&after).Error)
	assert.Equal(t, EntryFailed, after.Status)
	assert.Empty(t, payoutsOf(t, gdb, act.Id), "主库没扣过钱就不能凭空登记一笔退款")
}

// 探针判不出来 → 既不判失败也不退款,留在 excluded 等下一轮。
//
// 留在 excluded 是刻意的:finishIfDone 把 excluded 计入未结算,活动因此
// 不会收尾,没有人会被静默吞掉。
func TestConvergeExcluded_HoldsWhenProbeUnavailable(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, ExcludedManualAfterSeconds: 1})
	off := false
	cfg := *qyConfig.Load()
	cfg.TwoPhase.MainOutboxEnabled = &off
	prev := qyConfig.Swap(&cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })

	act := seedActivity(t, gdb, nil)
	e := seedExcludedEntry(t, gdb, act, "LE-unknown", qymodel.StatusFailed)
	require.NoError(t, gdb.Model(&Entry{}).Where("id = ?", e.Id).
		Update("created_at", common.GetTimestamp()-3600).Error)

	convergeExcluded(context.Background(), gdb, act)

	var after Entry
	require.NoError(t, gdb.Where("id = ?", e.Id).Take(&after).Error)
	assert.Equal(t, EntryExcluded, after.Status, "判不出来就不许判失败 —— 那是永久吞钱")
	assert.Empty(t, payoutsOf(t, gdb, act.Id))

	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagEntryStuck).
		Find(&flags).Error)
	assert.Len(t, flags, 1, "超过人工阈值必须落红点,否则没人知道有笔钱悬着")
}

// ───────────────────── 竞猜金额的口径 ─────────────────────

// 显式的负数金额必须 400,不能静默回落成单注额。
//
// "没填 amount"(int64 零值)才是回落分支,那是前端当前唯一在走的路径;
// 一个明确写着 -5 的请求被当成"按单注额下注"并真的扣钱,是替用户下了
// 一笔他没打算下的注,而参与是不可逆消费。
func TestAcceptAmount_GuessBranch(t *testing.T) {
	act := &Activity{Kind: KindGuess, StakeQuota: 1000}
	prev := qyConfig.Swap(&config.Config{
		Enabled: true,
		Lottery: config.Lottery{Enabled: true, MaxStakeQuota: 5_000_000},
	})
	t.Cleanup(func() { qyConfig.Store(prev) })

	cases := []struct {
		name    string
		in      EntryInput
		want    int64
		wantErr error
	}{
		{name: "缺字段 → 回落单注额", in: EntryInput{OptNo: 1}, want: 1000},
		{name: "显式 0 → 回落单注额", in: EntryInput{OptNo: 1, Amount: 0}, want: 1000},
		{name: "正常自选额", in: EntryInput{OptNo: 1, Amount: 2500}, want: 2500},
		{name: "负数 → 拒绝", in: EntryInput{OptNo: 1, Amount: -5}, wantErr: errBadAmount},
		{name: "int64 下界 → 拒绝", in: EntryInput{OptNo: 1, Amount: -1 << 62}, wantErr: errBadAmount},
		{name: "超单笔上限 → 拒绝", in: EntryInput{OptNo: 1, Amount: 6_000_000}, wantErr: errBadAmount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := acceptAmount(act, tc.in)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Zero(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
