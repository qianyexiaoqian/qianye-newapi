package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// payout_indoubt_test.go —— 抽奖双发的根因在**出款侧**的落点。
//
// 双发的形状:主库事务在 COMMIT 阶段断连,钱其实发出去了,而资金单被写成 Failed。
// failPayout 读到 Failed 就走"换代次退避重试"那一支 —— 换代次 = 换幂等键 =
// 对主库再加一次钱。上一轮堵的是 ProbeMainSide 那道复核;这一轮堵的是**源头**:
// 结局不明的单根本不该被写成 Failed。
//
// 这一组用例把出款侧在四种资金单状态下的分支逐个钉死。in_doubt 与 pending 必须
// 落在同一支(保持 paying、不换代次),而不是掉进 Failed 那一支。

// 出款侧对四种资金单状态的分支表。
//
// epoch 是判据里最要命的那一位:它一变,幂等键就变,下一轮 Execute 会对主库
// **再发一次钱**。只有"探针确认主库确实没动"才允许它动。
func TestFailPayout_BranchByFundOrderStatus(t *testing.T) {
	cases := []struct {
		name string
		// orderStatus 是本代次资金单的状态;-1 表示压根没拿到单据。
		orderStatus int8
		// hasOutbox 决定探针会答"已生效"还是"没生效"。
		hasOutbox  bool
		wantStatus string
		wantEpoch  int
	}{
		{
			name:        "failed + 探针说没动 → 换代次重试",
			orderStatus: qymodel.StatusFailed,
			wantStatus:  PayoutFailed,
			wantEpoch:   1,
		},
		{
			name:        "failed + 探针说动过 → 转人工,绝不重发",
			orderStatus: qymodel.StatusFailed,
			hasOutbox:   true,
			wantStatus:  PayoutHeld,
			wantEpoch:   0,
		},
		{
			// 这一行就是双发的原位置。收敛前 commit 断连会让资金单变成 Failed,
			// 于是走上面第一支换代次重发;现在它落 in_doubt,必须原地不动。
			name:        "in_doubt(commit 断连)→ 保持 paying,绝不换代次",
			orderStatus: qymodel.StatusInDoubt,
			wantStatus:  PayoutPaying,
			wantEpoch:   0,
		},
		{
			name:        "pending → 保持 paying,交补偿任务",
			orderStatus: qymodel.StatusPending,
			wantStatus:  PayoutPaying,
			wantEpoch:   0,
		},
		{
			name:        "uncertain → 保持 paying,等人裁决",
			orderStatus: qymodel.StatusUncertain,
			wantStatus:  PayoutPaying,
			wantEpoch:   0,
		},
		{
			name:        "连单据都没拿到 → 保持 paying",
			orderStatus: -1,
			wantStatus:  PayoutPaying,
			wantEpoch:   0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
			act := seedActivity(t, gdb, nil)
			p := seedPayout(t, gdb, act.Id, func(p *Payout) {
				p.Status = PayoutPaying
				p.Attempts = 1
			})

			var order *qymodel.FundOrder
			if tc.orderStatus >= 0 {
				order = &qymodel.FundOrder{
					OrderNo: "LP-" + p.PayoutNo, Kind: qymodel.KindLotteryPayout,
					Status: tc.orderStatus, IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
					UserId: p.UserId, AmountQuota: p.AmountQuota,
				}
				require.NoError(t, gdb.Create(order).Error)
				if tc.hasOutbox {
					seedOutboxRow(t, model.DB, order.OrderNo)
				}
			}

			failPayout(context.Background(), gdb, p, order, assert.AnError)

			after := reloadPayout(t, gdb, p.PayoutNo)
			assert.Equal(t, tc.wantStatus, after.Status)
			assert.Equal(t, tc.wantEpoch, after.Epoch,
				"代次一变就是对主库再发一次钱,只有'确定没动'才允许")
		})
	}
}

// 管理端「重试」按钮的分支表。与 failPayout 同一条判据,不能各说各的。
func TestRetryPayout_BranchByFundOrderStatus(t *testing.T) {
	cases := []struct {
		name        string
		orderStatus int8
		wantErr     bool
		wantStatus  string
		wantEpoch   int
	}{
		{"success → 直接收尾", qymodel.StatusSuccess, false, PayoutPaid, 0},
		{"failed(探针说没动)→ 换代次重排", qymodel.StatusFailed, false, PayoutPlanned, 1},
		{"in_doubt → 拒绝,此刻重开单就是赌一把", qymodel.StatusInDoubt, true, PayoutHeld, 0},
		{"pending → 拒绝", qymodel.StatusPending, true, PayoutHeld, 0},
		{"uncertain → 拒绝", qymodel.StatusUncertain, true, PayoutHeld, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
			act := seedActivity(t, gdb, nil)
			p := seedPayout(t, gdb, act.Id, func(p *Payout) { p.Status = PayoutHeld })
			require.NoError(t, gdb.Create(&qymodel.FundOrder{
				OrderNo: "LP-r-" + p.PayoutNo, Kind: qymodel.KindLotteryPayout,
				Status: tc.orderStatus, IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
				UserId: p.UserId, AmountQuota: p.AmountQuota,
			}).Error)

			err := RetryPayout(context.Background(), p.PayoutNo)
			if tc.wantErr {
				require.ErrorIs(t, err, errPayoutNeedsManual)
			} else {
				require.NoError(t, err)
			}

			after := reloadPayout(t, gdb, p.PayoutNo)
			assert.Equal(t, tc.wantStatus, after.Status)
			assert.Equal(t, tc.wantEpoch, after.Epoch)
		})
	}
}

// 参与侧(扣费)的回滚闸门:只有 Failed 才允许宣布"这笔钱没扣过"。
//
// in_doubt 走回滚的后果是用户被白扣一次参与费:明细标成 failed、计数被释放,
// 而主库那一侧的额度已经少了,模块内再没有任何路径会去补。
func TestReleaseEntryOnFailure_BranchByFundOrderStatus(t *testing.T) {
	cases := []struct {
		name        string
		orderStatus int8
		wantStatus  string
	}{
		{"failed → 回滚预占", qymodel.StatusFailed, EntryFailed},
		{"in_doubt → 不回滚", qymodel.StatusInDoubt, EntryPending},
		{"pending → 不回滚", qymodel.StatusPending, EntryPending},
		{"uncertain → 不回滚", qymodel.StatusUncertain, EntryPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newPayoutEnv(t, config.Lottery{Enabled: true})
			act := seedActivity(t, gdb, nil)
			entry := &Entry{
				EntryNo: newEntryNo(), ActId: act.Id, UserId: 9, Amount: 100,
				Status: EntryPending, CreatedAt: common.GetTimestamp(),
			}
			require.NoError(t, gdb.Create(entry).Error)
			order := &qymodel.FundOrder{
				OrderNo: "LE-" + entry.EntryNo, Kind: qymodel.KindLotteryEntry,
				Status: tc.orderStatus, IdemScope: "lottery_entry", IdemKey: entry.EntryNo,
				UserId: entry.UserId, AmountQuota: entry.Amount, RefId: entry.EntryNo,
			}
			require.NoError(t, gdb.Create(order).Error)

			releaseEntryOnFailure(context.Background(), order, entry, assert.AnError)

			var got Entry
			require.NoError(t, gdb.Where("entry_no = ?", entry.EntryNo).Take(&got).Error)
			assert.Equal(t, tc.wantStatus, got.Status,
				"只有'主库确定没扣'才允许把参与标成 failed 并放回名额")
		})
	}
}
