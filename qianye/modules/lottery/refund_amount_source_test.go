package lottery

import (
	"context"
	"testing"

	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 退款金额的权威来源是**资金单**，不是 qy_lot_entry.amount。
//
// 退款按 entry.amount 出款，而这笔钱真实收了多少写在
// qy_fund_orders.amount_quota 上（entry.order_no 就是锚点，同库可读）。
// 同一处篡改在开奖路径被 revealActivity 明确拒绝（roster_drift → 停手挂起），
// 在取消/流局路径却原样变成主库真金：实测把一条 2500 的参与改成 900000，
// 管理员一按取消就退出 900000，活动行上 refund_quota 与 pool_quota 当场自相矛盾
// 也没人拦。而取消/流局恰恰是「出事之后的止损动作」，四种流局与取消共用它。
func TestRefundAmountComesFromTheFundOrderNotTheEntryRow(t *testing.T) {
	t.Run("两者一致时按原额退", func(t *testing.T) {
		gdb := newFundTestDB(t)
		act := seedActivity(t, gdb, nil)
		e := seedPendingEntry(t, gdb, act, 11, 2500)

		amount, ok := refundAmountOf(context.Background(), gdb, act.Id, e)
		require.True(t, ok)
		assert.Equal(t, int64(2500), amount)
	})

	t.Run("明细被改大时按资金单退,并落一条 refund_drift", func(t *testing.T) {
		gdb := newFundTestDB(t)
		act := seedActivity(t, gdb, nil)
		e := seedPendingEntry(t, gdb, act, 12, 2500)

		// 扩展库单表一次 UPDATE —— 这正是 revealActivity 的注释里点名要防的那种。
		require.NoError(t, gdb.Model(&Entry{}).Where("id = ?", e.Id).
			Update("amount", 900000).Error)
		e.Amount = 900000

		amount, ok := refundAmountOf(context.Background(), gdb, act.Id, e)
		require.True(t, ok, "止损动作不能整场停手,否则所有人的本金一起被冻住")
		assert.Equal(t, int64(2500), amount,
			"必须按资金单的真实金额退;按明细退就是扩展库一次 UPDATE 变成主库净增发")

		var flags []Flag
		require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagRefundDrift).Find(&flags).Error)
		require.Len(t, flags, 1, "金额对不上必须留痕,否则事后无从发现")
	})

	t.Run("资金单不存在时不退款:没有证据证明钱收过", func(t *testing.T) {
		gdb := newFundTestDB(t)
		act := seedActivity(t, gdb, nil)
		e := seedPendingEntry(t, gdb, act, 13, 2500)
		require.NoError(t, gdb.Where("order_no = ?", e.OrderNo).
			Delete(&qymodel.FundOrder{}).Error)

		_, ok := refundAmountOf(context.Background(), gdb, act.Id, e)
		assert.False(t, ok, "读不到资金单就不能凭空发钱")

		var flags []Flag
		require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagRefundDrift).Find(&flags).Error)
		require.Len(t, flags, 1)
	})
}
