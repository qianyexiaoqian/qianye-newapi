package withdraw

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// payout_debt_gate_test.go —— 放款侧的冲正欠账闸门。
//
// 缺陷形状:先提现把佣金冻住 → 下线退款触发冲正 → 冲正按设计只吃 available、
// 吃不到已冻的 frozen,差额挂成 unsettled 负结转并置 debt_blocked → 管理员
// 照常 approve + mark-paid 把钱发出去。四桶恒等式全程成立,没有任何一条对账
// 会变红,而平台净损 = 已放款金额。
//
// 改动前唯一的阻力是单据上那个徽标 —— 靠审核人自己看见并自行决定不放款。
// 现在 approve 与 mark-paid 两步各加一道硬闸;驳回 / 标记发放失败 / 用户撤单
// 一律**不**加,那三条是把佣金退回可用池的方向,正好是冲正能吃到的地方。

func setDebt(t *testing.T, env *reviewEnv, unsettled string, blocked bool) {
	t.Helper()
	require.NoError(t, env.ext.Model(&commission.Balance{}).
		Where("user_id = ?", 7).
		Updates(map[string]any{
			"debt_blocked":     blocked,
			"unsettled_amount": decimal.RequireFromString(unsettled),
		}).Error)
}

// 欠账的四种形态都必须挡住放款,且一分钱都不能动。
func TestPayoutIsBlockedWhilePayeeOwesAClawback(t *testing.T) {
	cases := []struct {
		name      string
		unsettled string
		blocked   bool
		wantBlock bool
	}{
		{"没有欠账:照常放款", "0", false, false},
		{"冲正吃不到 frozen,差额挂成负余数", "-20000000", true, true},
		{"只置了 debt_blocked 标记", "0", true, true},
		{"只有负余数、标记还没落下", "-1", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newReviewEnv(t)
			w := env.seedApproved(t, "WD-DEBT-PAY", config.WithdrawMethodQuota, 500000, 1000)
			setDebt(t, env, tc.unsettled, tc.blocked)

			res := callAdmin(t, handleAdminMarkPaid, w.Id,
				`{"payout_ref":"MANUAL-1","confirm_quota":500000}`)

			if !tc.wantBlock {
				require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
				assert.Equal(t, StatusPaid, env.status(t, w.Id))
				bal := env.balance(t)
				assert.EqualValues(t, 500000, bal.WithdrawnQuota, "没有欠账时该核销的还是要核销")
				return
			}

			require.Equal(t, http.StatusConflict, res.Code, "body=%s", res.Body.String())
			assert.Equal(t, "qy_wd_debt_blocked_payout", respCode(t, res))
			assert.Equal(t, StatusApproved, env.status(t, w.Id), "单据状态一步都不能往前走")
			bal := env.balance(t)
			assert.EqualValues(t, 500000, bal.FrozenQuota, "钱必须还冻在原处")
			assert.Zero(t, bal.WithdrawnQuota, "一分钱都不能核销出去")
			env.assertLedgerIdentity(t, 500000)
		})
	}
}

// approve 也要挡:它是这条链上更早的一步,放过去之后单据就进了待发放队列,
// 而队列本身没有第二道欠账判据。
func TestApproveIsBlockedWhilePayeeOwesAClawback(t *testing.T) {
	env := newReviewEnv(t)
	w := env.seedApproved(t, "WD-DEBT-APV", config.WithdrawMethodQuota, 500000, 1000)
	require.NoError(t, env.ext.Model(&Withdrawal{}).Where("id = ?", w.Id).
		Update("status", StatusPending).Error)
	setDebt(t, env, "-20000000", true)

	res := callAdmin(t, handleAdminApprove, w.Id, `{}`)

	require.Equal(t, http.StatusConflict, res.Code, "body=%s", res.Body.String())
	assert.Equal(t, "qy_wd_debt_blocked_payout", respCode(t, res))
	assert.Equal(t, StatusPending, env.status(t, w.Id))
}

// 退钱方向的两个决定一律不受欠账影响 —— 挡住它们等于把欠账用户的单据永久
// 钉死在队列里,而它们恰恰是让冲正吃得到那笔钱的唯一出口。
func TestRejectAndFailStayOpenWhilePayeeOwesAClawback(t *testing.T) {
	t.Run("驳回", func(t *testing.T) {
		env := newReviewEnv(t)
		w := env.seedApproved(t, "WD-DEBT-REJ", config.WithdrawMethodQuota, 500000, 1000)
		require.NoError(t, env.ext.Model(&Withdrawal{}).Where("id = ?", w.Id).
			Update("status", StatusPending).Error)
		setDebt(t, env, "-20000000", true)

		res := callAdmin(t, handleAdminReject, w.Id, `{"reason":"存在欠账"}`)

		require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
		assert.Equal(t, StatusRejected, env.status(t, w.Id))
		bal := env.balance(t)
		assert.EqualValues(t, 500000, bal.AvailableQuota, "钱退回可用池,下一轮结算才吃得到")
		assert.Zero(t, bal.FrozenQuota)
	})

	t.Run("标记发放失败", func(t *testing.T) {
		env := newReviewEnv(t)
		w := env.seedApproved(t, "WD-DEBT-FAIL", config.WithdrawMethodQuota, 500000, 1000)
		setDebt(t, env, "-20000000", true)

		res := callAdmin(t, handleAdminFail, w.Id, `{"reason":"打款退回"}`)

		require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
		bal := env.balance(t)
		assert.EqualValues(t, 500000, bal.AvailableQuota)
		assert.Zero(t, bal.FrozenQuota)
	})
}

// 读不出账本状态时按拦下处理。这是一道资金闸门,「读不到」不等于「没有欠账」。
func TestPayoutIsBlockedWhenTheLedgerCannotBeRead(t *testing.T) {
	env := newReviewEnv(t)
	w := env.seedApproved(t, "WD-DEBT-DOWN", config.WithdrawMethodQuota, 500000, 1000)
	// 把佣金余额表拿掉:LoadDebtStatuses 会报错,而不是回一个空结果。
	require.NoError(t, env.ext.Migrator().DropTable(&commission.Balance{}))

	res := callAdmin(t, handleAdminMarkPaid, w.Id,
		`{"payout_ref":"MANUAL-2","confirm_quota":500000}`)

	require.Equal(t, http.StatusServiceUnavailable, res.Code, "body=%s", res.Body.String())
	assert.Equal(t, "qy_wd_debt_status_unknown", respCode(t, res))
	assert.Equal(t, StatusApproved, env.status(t, w.Id))
}

// 被拦下来的放款要留痕:事后必须能回答「这笔钱当时为什么没发」。
func TestBlockedPayoutWritesAFailedAudit(t *testing.T) {
	env := newReviewEnv(t)
	w := env.seedApproved(t, "WD-DEBT-AUDIT", config.WithdrawMethodQuota, 500000, 1000)
	setDebt(t, env, "-20000000", true)

	res := callAdmin(t, handleAdminMarkPaid, w.Id,
		`{"payout_ref":"MANUAL-3","confirm_quota":500000}`)
	require.Equal(t, http.StatusConflict, res.Code)

	var rows []qymodel.AuditLog
	require.NoError(t, env.ext.Where("trace_no = ?", w.WithdrawNo).Find(&rows).Error)
	require.NotEmpty(t, rows, "被拦下的放款必须写审计")
	found := false
	for _, r := range rows {
		if r.Action == "withdraw.payout" && r.Result == qymodel.ResultFail {
			found = true
			assert.Equal(t, 7, r.TargetUserId)
			assert.Contains(t, r.Reason, "欠账")
		}
	}
	assert.True(t, found, "缺一条 withdraw.payout 的 fail 审计: %+v", rows)
	_ = common.GetTimestamp
}
