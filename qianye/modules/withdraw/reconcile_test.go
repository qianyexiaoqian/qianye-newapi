package withdraw

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A1(a):对账扫描绝不能再碰已经转人工的单。
//
// holdForReview 只写 reconcile_state='hold',单据的 status 仍然是 paying。
// 扫描条件里漏掉这一条时,下一轮(默认 60 秒后)会重新命中同一张单,而
// failWithdrawal 的 CAS 条件正是 `From: paying`,必然成功 —— 人工裁决被自动
// 流程无声推翻:主库额度已经加过,佣金又回到可用池,同一笔钱可以再提一次。
func TestScanStalePaying_SkipsHoldOrders(t *testing.T) {
	gdb := newTestDB(t)
	now := common.GetTimestamp()
	stale := now - 60

	seedWithdrawal(t, gdb, "WD-hold", func(w *Withdrawal) {
		w.Status = StatusPaying
		w.OrderNo = "FO-hold"
		w.SettleStartedAt = stale - 10
		w.ReconcileState = ReconcileHold
	})
	seedWithdrawal(t, gdb, "WD-stale", func(w *Withdrawal) {
		w.Status = StatusPaying
		w.OrderNo = "FO-stale"
		w.SettleStartedAt = stale - 10
	})
	seedWithdrawal(t, gdb, "WD-fresh", func(w *Withdrawal) {
		w.Status = StatusPaying
		w.OrderNo = "FO-fresh"
		w.SettleStartedAt = now // 还在宽限期内,交给业务线程自己收尾
	})
	seedWithdrawal(t, gdb, "WD-approved", func(w *Withdrawal) {
		w.Status = StatusApproved
		w.SettleStartedAt = stale - 10
	})
	// settle_started_at = 0 的 paying 单是数据异常,不该被自动收尾。
	seedWithdrawal(t, gdb, "WD-nostart", func(w *Withdrawal) {
		w.Status = StatusPaying
		w.OrderNo = "FO-nostart"
	})

	rows, err := scanStalePaying(gdb, stale, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"WD-stale"}, withdrawNos(rows))
}

// A1(b):资金单是 failed【不等于】主库确定没动。
//
// twophase.markFailed 在业务线程拿到确定性错误时就会置 failed,而主库事务完全
// 可能是在 commit 阶段断连的 —— 服务端已提交、客户端收到 error。那条路径从来
// 没探过针,所以对账必须自己再探一次 outbox(划转模块在同一分支就是这么做的)。
// 少了这一步,退回佣金 = 主库额度已加、佣金也还给用户的双份到账。
func TestDecideSettle(t *testing.T) {
	cases := []struct {
		name        string
		status      int8
		mainApplied bool
		want        settleAction
		wantProbed  bool
	}{
		{"资金单成功即收尾", qymodel.StatusSuccess, false, actionFinish, false},
		{"失败且探针确认未生效才退款", qymodel.StatusFailed, false, actionRefund, true},
		{"失败但探针显示已生效必须转人工", qymodel.StatusFailed, true, actionHold, true},
		{"不可判定一律转人工", qymodel.StatusUncertain, false, actionHold, false},
		{"仍在推进中不做任何判断", qymodel.StatusPending, false, actionWait, false},
		{"已冲正不归对账处理", qymodel.StatusReversed, false, actionWait, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			got := decideSettle(tc.status, func() bool {
				probed = true
				return tc.mainApplied
			})
			assert.Equal(t, tc.want, got)
			// 探针要查主库 outbox,只有 Failed 分支需要它。
			// 其余分支多探一次是每轮对账都白付一次跨库查询。
			assert.Equal(t, tc.wantProbed, probed, "主库探针的调用时机不符")
		})
	}
}

// 转人工的理由是管理员裁决时看到的第一句话,两种 hold 的成因完全不同:
// 一种是"探针说钱动了但单据说没动",另一种是"补偿任务已经放弃判定"。
// 混成同一句会让裁决的人查错方向。
func TestHoldReason_DistinguishesCause(t *testing.T) {
	assert.Contains(t,
		holdReason(qymodel.FundOrder{Status: qymodel.StatusFailed}),
		"主库探针显示已生效")
	assert.Contains(t,
		holdReason(qymodel.FundOrder{Status: qymodel.StatusUncertain, LastError: "探针耗尽"}),
		"探针耗尽")
	// 没有原始错误时也必须给一句人话,不能留空。
	assert.NotEmpty(t, holdReason(qymodel.FundOrder{Status: qymodel.StatusUncertain}))
}
