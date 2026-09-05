package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 退款金额的权威来源是**账本流水**,不是 qy_lot_entry.amount。
//
// 退款若按 entry.amount 出款,而这笔钱真实收了多少写在 qy_sd_ledger.amount 上
// (entry.order_no = ledger_no 就是锚点,同库可读)。同一处篡改在开奖路径被
// revealActivity 明确拒绝(roster_drift → 停手挂起),在取消/流局路径却会原样
// 变成账本上的净增发:实测把一条 2500 的参与改成 900000,管理员一按取消就退出
// 900000。而取消/流局恰恰是「出事之后的止损动作」,四种流局与取消共用它。
func TestRefundAmountComesFromTheLedgerNotTheEntryRow(t *testing.T) {
	t.Run("两者一致时按原额退", func(t *testing.T) {
		gdb := newFundTestDB(t)
		act := seedActivity(t, gdb, nil)
		e := seedSuccessEntry(t, gdb, act, 11, 2500)
		require.NotEmpty(t, e.OrderNo, "票上必须带着扣款那一行流水的 ledger_no")

		amount, ok := refundAmountOf(context.Background(), gdb, act.Id, e)
		require.True(t, ok)
		assert.Equal(t, int64(2500), amount)
	})

	t.Run("明细被改大时按流水退,并落一条 refund_drift", func(t *testing.T) {
		gdb := newFundTestDB(t)
		act := seedActivity(t, gdb, nil)
		e := seedSuccessEntry(t, gdb, act, 12, 2500)

		// 业务表单次 UPDATE —— 这正是 revealActivity 的注释里点名要防的那种。
		require.NoError(t, gdb.Model(&Entry{}).Where("id = ?", e.Id).
			Update("amount", 900000).Error)
		e.Amount = 900000

		amount, ok := refundAmountOf(context.Background(), gdb, act.Id, e)
		require.True(t, ok, "止损动作不能整场停手,否则所有人的本金一起被冻住")
		assert.Equal(t, int64(2500), amount,
			"必须按流水的真实金额退;按明细退就是业务表一次 UPDATE 变成账本净增发")

		var flags []Flag
		require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagRefundDrift).Find(&flags).Error)
		require.Len(t, flags, 1, "金额对不上必须留痕,否则事后无从发现")
	})

	t.Run("流水不存在时不退款:没有证据证明钱收过", func(t *testing.T) {
		gdb := newFundTestDB(t)
		act := seedActivity(t, gdb, nil)
		e := seedSuccessEntry(t, gdb, act, 13, 2500)
		require.NoError(t, gdb.Where("ledger_no = ?", e.OrderNo).
			Delete(&stardust.Ledger{}).Error)

		_, ok := refundAmountOf(context.Background(), gdb, act.Id, e)
		assert.False(t, ok, "读不到流水就不能凭空发钱")

		var flags []Flag
		require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagRefundDrift).Find(&flags).Error)
		require.Len(t, flags, 1)
	})

	t.Run("票指向的流水不是本人的扣款时不退款", func(t *testing.T) {
		gdb := newFundTestDB(t)
		act := seedActivity(t, gdb, nil)
		e := seedSuccessEntry(t, gdb, act, 14, 2500)
		// 把票上的锚点换成同一场活动里**别人**的扣款流水:金额一样,人不一样。
		other := seedSuccessEntry(t, gdb, act, 15, 2500)
		e.OrderNo = other.OrderNo

		_, ok := refundAmountOf(context.Background(), gdb, act.Id, e)
		assert.False(t, ok, "拿别人的扣款流水当自己的凭据,退款必须拒绝")
	})

	t.Run("票指向的是一行入账而不是扣款时不退款", func(t *testing.T) {
		gdb := newFundTestDB(t)
		act := seedActivity(t, gdb, nil)
		e := seedSuccessEntry(t, gdb, act, 16, 2500)
		// 种子那一行是 manual 入账,金额同样是 2500 —— 只看数字它"对得上"。
		var seed stardust.Ledger
		require.NoError(t, gdb.Where("user_id = ? AND kind = ?", 16, string(stardust.KindManual)).
			Take(&seed).Error)
		e.OrderNo = seed.LedgerNo

		_, ok := refundAmountOf(context.Background(), gdb, act.Id, e)
		assert.False(t, ok, "只有 lot_stake 那一行才证明钱收过,入账行不算")
	})
}
