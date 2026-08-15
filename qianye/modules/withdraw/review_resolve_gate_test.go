package withdraw

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件守一条边:**人工裁决只能作用在补偿链路标记为 hold 的单上**。
//
// paying 不是"等人裁决"的状态,而是每一笔正在执行中的到账单都会经过的状态:
// startPaying 与资金单落库同事务提交,之后整个 applyOnMainDB → AfterCommit →
// markSuccess 期间它都是 paying 且 reconcile_state 为空。扩展库回写失败时资金单
// 停在 pending,这张单会在非 hold 的 paying 上一直待到补偿探针跑完退避阶梯 ——
// 那恰恰是运维会去翻"卡住的单"的时刻。
//
// 只判 status 的话,对一张主库已经加完额度的在途单裁一次 failed:
// UnfreezeForWithdraw 把佣金退回可用池,随后的 finishPaid CAS 落空、静默返回 nil,
// 补偿任务照样把资金单推成 success。用户既拿到了站内额度,又保住了可以再提一次
// 的佣金,而佣金侧的 available+frozen+withdrawn == earned-clawback 恒等式仍然成立。
//
// 因此断言必须落在**佣金余额的实际数值**上:只看返回码的话,把闸门删掉之后
// 状态码固然会变,但"佣金被退回了多少"这件事没有任何测试盯着。

// seedPayingCredited 造出"主库已加额度、扩展库还没回写"的中间态:
// 提现单停在 paying、佣金仍在 frozen、主库 users.quota 已经含这笔钱。
func (e *reviewEnv) seedPayingCredited(t *testing.T, no, reconcileState string, quota int64) *Withdrawal {
	t.Helper()
	now := common.GetTimestamp()
	w := &Withdrawal{
		WithdrawNo:      no,
		OrderNo:         no + "-FO",
		IdemScope:       idemScope,
		IdemKey:         idemKeyOf(7, "seed-"+no),
		UserId:          7,
		Method:          "quota",
		Status:          StatusPaying,
		ReconcileState:  reconcileState,
		Quota:           quota,
		SettleStartedAt: now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, e.ext.Create(w).Error)
	require.NoError(t, e.ext.Create(&commission.Balance{
		UserId: 7, FrozenQuota: quota, AvailableQuota: 0,
		AvailableFiat: decimal.Zero, UnsettledAmount: decimal.Zero,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, e.ext.Create(&commission.FreezeRecord{
		RefNo: no, Action: commission.FreezeActionFreeze,
		UserId: 7, Quota: quota, Fiat: decimal.Zero, CreatedAt: now,
	}).Error)
	// 主库这一侧已经生效:额度已加、outbox 已登记。
	require.NoError(t, e.main.Create(&model.User{
		Id: 7, Username: "u7", Quota: int(quota), Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, e.main.Create(&model.QyFundOutbox{
		OrderNo: w.OrderNo, Kind: "withdraw_quota", UserId: 7, Amount: quota, CreatedAt: now,
	}).Error)
	return w
}

func TestResolveOnlyAcceptsHoldOrders(t *testing.T) {
	const quota int64 = 20000

	t.Run("非 hold 的在途 paying 单必须被拒且一分钱不动", func(t *testing.T) {
		env := newReviewEnv(t, nil)
		w := env.seedPayingCredited(t, "WD-LIVE", "", quota)

		res := callAdmin(t, handleAdminResolve, w.Id,
			`{"decision":"failed","evidence":"主库看起来没加额度"}`)

		assert.Equal(t, "qy_wd_illegal_transition", respCode(t, res))
		assert.Equal(t, StatusPaying, env.status(t, w.Id), "被拒的裁决不得改状态")

		bal := env.balance(t)
		assert.EqualValues(t, quota, bal.FrozenQuota,
			"佣金必须仍在 frozen —— 退回可用池就意味着主库那笔额度是白送的")
		assert.EqualValues(t, 0, bal.AvailableQuota)
		assert.EqualValues(t, quota, env.mainQuota(t), "主库额度不得被改动")
	})

	t.Run("hold 单裁决 failed:退回佣金,主库额度由人工另行处理", func(t *testing.T) {
		env := newReviewEnv(t, nil)
		w := env.seedPayingCredited(t, "WD-HOLD-F", ReconcileHold, quota)

		res := callAdmin(t, handleAdminResolve, w.Id,
			`{"decision":"failed","evidence":"已核对主库未加额度"}`)

		require.Equal(t, 200, res.Code, res.Body.String())
		assert.Equal(t, StatusFailed, env.status(t, w.Id))
		bal := env.balance(t)
		assert.EqualValues(t, 0, bal.FrozenQuota)
		assert.EqualValues(t, quota, bal.AvailableQuota, "拒付后佣金必须原样回到可用池")
	})

	t.Run("hold 单裁决 paid:冻结额结转为已提现", func(t *testing.T) {
		env := newReviewEnv(t, nil)
		w := env.seedPayingCredited(t, "WD-HOLD-P", ReconcileHold, quota)

		res := callAdmin(t, handleAdminResolve, w.Id,
			`{"decision":"paid","evidence":"已核对主库确已加额度"}`)

		require.Equal(t, 200, res.Code, res.Body.String())
		assert.Equal(t, StatusPaid, env.status(t, w.Id))
		bal := env.balance(t)
		assert.EqualValues(t, 0, bal.FrozenQuota)
		assert.EqualValues(t, 0, bal.AvailableQuota)
		assert.EqualValues(t, quota, bal.WithdrawnQuota)
	})
}
