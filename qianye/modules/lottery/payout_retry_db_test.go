package lottery

import (
	"context"
	"sync/atomic"
	"testing"
	_ "unsafe" // go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// payout_retry_db_test.go —— "一笔已经确定要发的钱,永远存在一条能把它发出去的路"。
//
// 收敛前这条不成立:出款的幂等键恒等于 payout_no,而 twophase 对一张 Failed 的
// 资金单只会返回 ErrOrderFailed。于是第一次失败之后,自动重试与管理端「重试」
// 按钮都只是在反复撞同一张死单 —— 用户中的奖永远发不出去,而系统里没有任何
// 路径能发出它。这一组用例锁住修复后的三条不变量:
//
//  1. 主库探针确认**没生效**时才换代次;代次一换,幂等键就换,重试才真的能出手。
//  2. 结果**不可判定**时绝不换代次(钱可能已经动了),预算耗尽转人工 ——
//     而不是停在一个再也不会被 worker 扫到的 paying 上。
//  3. 管理端重试按资金单的真实终态分支:已成功就直接收尾,不可判定就拒绝。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// newPayoutEnv 建一个装好扩展库句柄与配置的测试环境。
//
// 资金单表必须一起迁移:重试的分支判据就是"本代次的资金单现在是什么状态",
// 少了它测的就不是真实的判据。
func newPayoutEnv(t *testing.T, lot config.Lottery) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(tables()...))
	require.NoError(t, gdb.AutoMigrate(&qymodel.FundOrder{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	// outbox 显式关掉:探针查的是主库,而这一组用例根本没有主库。关掉之后
	// mainSideApplied 恒返回 false,正好对应"主库确认没生效"这一支判据 ——
	// 需要另一支的用例自己造一张 Success 单来表达。
	outboxOff := false
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Lottery:  lot,
		TwoPhase: config.TwoPhase{MainOutboxEnabled: &outboxOff},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

func seedPayout(t *testing.T, gdb *gorm.DB, actId int64, mutate func(*Payout)) *Payout {
	t.Helper()
	p := &Payout{
		PayoutNo: newPayoutNo(), ActId: actId, EntryId: 1, Kind: PayoutPrize,
		UserId: 9, AmountQuota: 500, Status: PayoutPlanned, CreatedAt: common.GetTimestamp(),
	}
	if mutate != nil {
		mutate(p)
	}
	require.NoError(t, gdb.Create(p).Error)
	return p
}

func reloadPayout(t *testing.T, gdb *gorm.DB, payoutNo string) *Payout {
	t.Helper()
	var p Payout
	require.NoError(t, gdb.Where("payout_no = ?", payoutNo).Take(&p).Error)
	return &p
}

// 探针确认主库没生效 → 换代次退避重试。
//
// 不换代次的话下一轮 Execute 会幂等命中同一张 Failed 单直接返回,
// MainApply 根本不会执行 —— 重试次数被白白烧光,最后转人工,而人工重试
// 走的还是同一个键。这一条是"钱永远发得出去"的地基。
func TestFailPayout_BumpsEpochWhenMainSideDidNotApply(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutPaying
		p.Attempts = 1
	})

	order := &qymodel.FundOrder{
		OrderNo: "LP-x", Kind: qymodel.KindLotteryPayout, Status: qymodel.StatusFailed,
		IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
		UserId: p.UserId, AmountQuota: p.AmountQuota, LastError: "余额上限",
	}
	require.NoError(t, gdb.Create(order).Error)

	failPayout(context.Background(), gdb, p, order, assert.AnError)

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutFailed, after.Status)
	assert.Equal(t, 1, after.Epoch, "探针说主库没动,必须换一个代次才可能真的重试")
	assert.NotEqual(t, payoutIdemKey(p), payoutIdemKey(after), "代次一换,幂等键必须跟着换")
	assert.Greater(t, after.NextAttemptAt, int64(0))
}

// 结果不可判定 + 重试预算耗尽 → 转人工,绝不留在 paying。
//
// 留在 paying 的后果是这一行同时掉出 worker 的扫描范围(attempts 已满)
// 与红点的统计范围(只数 held):一笔谁都不知道的丢单,而活动因为它永远
// 收不了尾。
func TestFailPayout_HoldsWhenUndecidableAndBudgetExhausted(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 3})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutPaying
		p.Attempts = 3
	})

	// order == nil 就是"连单据都没拿到"的不可判定形状(金额越界、库熔断)。
	failPayout(context.Background(), gdb, p, nil, assert.AnError)

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.Equal(t, 0, after.Epoch, "不可判定时钱可能已经动了,绝不能换代次重开单")

	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagPayoutStuck).Find(&flags).Error)
	assert.Len(t, flags, 1, "转人工必须同时落一条红点,否则没人知道有笔钱卡住了")
}

// 不可判定但预算还没耗尽 → 保持 paying,交补偿任务。
func TestFailPayout_KeepsPayingWhileBudgetRemains(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutPaying
		p.Attempts = 1
	})

	failPayout(context.Background(), gdb, p, nil, assert.AnError)

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutPaying, after.Status)
	assert.Equal(t, 0, after.Epoch)
}

// 管理端重试:本代次的资金单已经成功 → 直接收尾,绝不再发一次。
func TestRetryPayout_SettlesWhenOrderAlreadySucceeded(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) { p.Status = PayoutHeld })
	require.NoError(t, gdb.Create(&qymodel.FundOrder{
		OrderNo: "LP-ok", Kind: qymodel.KindLotteryPayout, Status: qymodel.StatusSuccess,
		IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
		UserId: p.UserId, AmountQuota: p.AmountQuota,
	}).Error)

	require.NoError(t, RetryPayout(context.Background(), p.PayoutNo))

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutPaid, after.Status, "钱其实已经到账,重试只能收尾")
	assert.Equal(t, 0, after.Epoch, "已经成功的单绝不能换代次 —— 那就是第二次发钱")
}

// 管理端重试:本代次的资金单被判失败且主库没动 → 换代次重排。
func TestRetryPayout_BumpsEpochOnFailedOrder(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutHeld
		p.Attempts = 8
	})
	require.NoError(t, gdb.Create(&qymodel.FundOrder{
		OrderNo: "LP-bad", Kind: qymodel.KindLotteryPayout, Status: qymodel.StatusFailed,
		IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
		UserId: p.UserId, AmountQuota: p.AmountQuota,
	}).Error)

	require.NoError(t, RetryPayout(context.Background(), p.PayoutNo))

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutPlanned, after.Status)
	assert.Equal(t, 1, after.Epoch)
	assert.Equal(t, 0, after.Attempts)
}

// 管理端重试:资金单还没落定 → 拒绝。此刻重开单就是赌一把。
func TestRetryPayout_RefusesWhileOrderUndecided(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) { p.Status = PayoutHeld })
	require.NoError(t, gdb.Create(&qymodel.FundOrder{
		OrderNo: "LP-pending", Kind: qymodel.KindLotteryPayout, Status: qymodel.StatusPending,
		IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
		UserId: p.UserId, AmountQuota: p.AmountQuota,
	}).Error)

	err := RetryPayout(context.Background(), p.PayoutNo)
	require.ErrorIs(t, err, errPayoutNeedsManual)

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.Equal(t, 0, after.Epoch)
}

// 补偿任务在一笔已经转人工的出款上确认主库已生效 → 必须能收尾。
//
// 收敛前 markPayoutPaid 只认 paying,于是这一行会永远停在 held:钱到账了,
// 红点却永不消失,而下一个看到红点的人无从判断它到底发没发出去。
func TestMarkPayoutPaid_ClosesHeldButNeverPlanned(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true})
	act := seedActivity(t, gdb, nil)
	held := seedPayout(t, gdb, act.Id, func(p *Payout) { p.Status = PayoutHeld })
	planned := seedPayout(t, gdb, act.Id, func(p *Payout) { p.EntryId = 2 })

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markPayoutPaid(tx, held.PayoutNo)
	}))
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markPayoutPaid(tx, planned.PayoutNo)
	}))

	assert.Equal(t, PayoutPaid, reloadPayout(t, gdb, held.PayoutNo).Status)
	assert.Equal(t, PayoutPlanned, reloadPayout(t, gdb, planned.PayoutNo).Status,
		"从未执行过的出款被记成已到账,等于系统认为钱给过了而用户永远收不到")
}
