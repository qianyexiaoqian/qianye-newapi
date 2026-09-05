package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// activity_totals_db_test.go —— "活动上的合计必须等于真的发出去的钱"。
//
// 收敛前这条不成立:payout_quota / refund_quota 在 settling→finished 那一次 CAS
// 里被写成"当时已 paid 的合计"就再也不动了,而 finishIfDone 刻意把 held 当终态
// 放行 —— 一笔转人工的出款随后被管理端「重试」推成 paid,星屑真的增发出去了
// (账本上有流水行),活动上的数却停在收尾那一刻。held_quota 是实时 SUM,
// 补发成功后归零,于是同一笔钱从两个口子同时消失,管理端「本场收支」把一场
// 净亏的活动显示成净赚,连符号都是反的。

// reloadActivity 读回活动行。
func reloadActivity(t *testing.T, gdb *gorm.DB, id int64) *Activity {
	t.Helper()
	var a Activity
	require.NoError(t, gdb.Where("id = ?", id).Take(&a).Error)
	return &a
}

// 收尾之后才成功的出款(held → 重试 → worker 入账)必须补计进活动合计,且只计一次。
func TestRetriedPayoutKeepsFinishedActivityTotalsCurrent(t *testing.T) {
	cases := []struct {
		name       string
		kind       string
		amount     int64
		wantPayout int64
		wantRefund int64
	}{
		{"补发的派奖进 payout_quota", PayoutPrize, 109000, 27505 + 109000, 300},
		{"补发的退款进 refund_quota", PayoutRefund, 500, 27505, 300 + 500},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
			act := seedActivity(t, gdb, func(a *Activity) {
				a.Status = StatusFinished
				a.PoolQuota = 30000
				a.PayoutQuota = 27505
				a.RefundQuota = 300
				a.SettledAt = common.GetTimestamp()
			})
			p := seedPayout(t, gdb, act.Id, func(p *Payout) {
				p.Kind = tc.kind
				p.AmountQuota = tc.amount
				p.Status = PayoutHeld
				p.Attempts = 8
			})

			require.NoError(t, RetryPayout(context.Background(), p.PayoutNo))
			DrivePayouts(context.Background())
			require.Equal(t, PayoutPaid, reloadPayout(t, gdb, p.PayoutNo).Status)
			assert.Equal(t, tc.amount, stardustOf(t, gdb, p.UserId), "补发的钱必须真的到账")

			after := reloadActivity(t, gdb, act.Id)
			assert.Equal(t, tc.wantPayout, after.PayoutQuota)
			assert.Equal(t, tc.wantRefund, after.RefundQuota)

			// 同一笔被 worker 重复扫到时不许再加一次:paying→paid 的 CAS 只可能
			// 成功一次,paid 是终态;账本的幂等键也让第二次入账记不上。
			for i := 0; i < 2; i++ {
				DrivePayouts(context.Background())
			}
			again := reloadActivity(t, gdb, act.Id)
			assert.Equal(t, tc.wantPayout, again.PayoutQuota, "重复收尾不许重复计入")
			assert.Equal(t, tc.wantRefund, again.RefundQuota, "重复收尾不许重复计入")
			assert.Equal(t, tc.amount, stardustOf(t, gdb, p.UserId), "重复扫到不许重复入账")
		})
	}
}

// 收尾**之前**成功的出款不许在这里被计一遍:它们随后会进收尾那次聚合。
func TestPayoutBeforeFinishDoesNotDoubleCountTotals(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusSettling
		a.PoolQuota = 30000
		a.PayoutQuota = 137005 // reveal 写进来的计划口径
	})
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.AmountQuota = 109000
		p.Status = PayoutPaying
	})

	DrivePayouts(context.Background())

	after := reloadActivity(t, gdb, act.Id)
	assert.Equal(t, int64(137005), after.PayoutQuota,
		"活动还没收尾,合计由收尾那次聚合负责,这里加就会加两遍")
	assert.Equal(t, PayoutPaid, reloadPayout(t, gdb, p.PayoutNo).Status)
	assert.EqualValues(t, 109000, stardustOf(t, gdb, p.UserId))
}

// 「本场收支」的净值不许把手续费算两遍。
//
// platform_fee_quota 是从 pool 里切出来的那一块,income_quota 就是 pool,已经含它。
// 竞猜恒有 pool = payout + fee,所以加一遍的话真实净值(恰等于 fee)会被显示成
// 2×fee —— 误差 100%,而且亏损场会被少报一个 fee 的亏损。
func TestActivityNetQuotaCountsPlatformFeeOnce(t *testing.T) {
	cases := []struct {
		name string
		act  Activity
		held int64
		want int64
	}{
		{
			// 竞猜:SplitPool 断言 Σpay + fee == pool,平台真实所得就是 fee。
			name: "竞猜:pool = payout + fee",
			act:  Activity{PoolQuota: 37500, PayoutQuota: 35625, PlatformFeeQuota: 1875},
			want: 1875,
		},
		{
			// 双色球:fee = pool − ballPoolIn,同样是 pool 的一部分。
			name: "双色球:亏损场不许被少报亏损",
			act:  Activity{PoolQuota: 30000, PayoutQuota: 137005, PlatformFeeQuota: 12000},
			want: -107005,
		},
		{
			name: "退款与待人工出款都要扣",
			act:  Activity{PoolQuota: 10000, PayoutQuota: 3000, RefundQuota: 1000, PlatformFeeQuota: 500},
			held: 2000,
			want: 4000,
		},
		{
			// rank/prob 的 fee 恒为 0 —— 正因如此这个错误一直没显形。
			name: "无手续费的玩法不受影响",
			act:  Activity{PoolQuota: 10000, PayoutQuota: 12000},
			want: -2000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			act := tc.act
			assert.Equal(t, tc.want, activityNetQuota(&act, tc.held))
		})
	}
}

// 详情页与列表页对同一场活动必须给出同一个「净值」。
//
// 列表页(admin-lottery/index.tsx)算的是 pool − payout − refund;详情页用后端的
// net_quota。后端原先多加了一个 platform_fee_quota,于是同一场竞猜在列表页显示
// 1875、在详情页显示 3750,而 commit.go 的守恒断言判定错的是详情页。
func TestActivityNetQuotaAgreesWithTheListPageFormula(t *testing.T) {
	act := Activity{PoolQuota: 37500, PayoutQuota: 35625, RefundQuota: 0, PlatformFeeQuota: 1875}
	listPage := act.PoolQuota - act.PayoutQuota - act.RefundQuota
	assert.Equal(t, listPage, activityNetQuota(&act, 0),
		"held 为 0 时两个页面必须给出同一个数")
}
