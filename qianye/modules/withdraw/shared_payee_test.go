package withdraw

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// shared_payee_test.go —— 共用收款账号的标记必须**一张不漏**。
//
// # 被测的缺陷(实测到打款)
//
// 标记此前是在建单那一刻、落库之前算出来的:数 `payee_digest = ? AND user_id <> ?`。
// 于是同一张卡上时间最早的那张单数到的永远是 0,永久不带标记。管理端队列默认
// `id asc` 先进先出,`risk_only=true` 更是把没标记的那张整个过滤掉 ——
// 团伙里第一个人(往往是金额最大的主号)恰好排在最前、没有任何风控提示地被
// 审核通过;等第二张单亮红灯时,第一笔钱已经打出去了。
//
// 实测:三张同指纹单 50/51/52,只有 50 干净,而 50 正是被 approve → mark-paid
// 走完的那张。

const testDigest = "202e886699911505aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// submitFiatWith 用给定指纹提交一张单,返回落库后的单。
func submitFiatWith(t *testing.T, gdb *gorm.DB, userId int, digest, idem string) *Withdrawal {
	t.Helper()
	seedFiatBalance(t, gdb, userId, 50000000, "850", "CNY")

	w := fiatOrder(userId, 50000000, idem)
	w.PayeeDigest = digest
	w.PayeeChannel = "bank"
	w.PayeeMasked = "CMB ****6789 / Z********"
	acc := acceptedRequest{IdemKey: idem, Method: config.WithdrawMethodFiat, Quota: 50000000}
	replay, err := submitInTx(gdb, w, nil, acc, ledgerCfg(), "u")
	require.NoError(t, err)
	require.Nil(t, replay)
	return w
}

func riskFlagsOf(t *testing.T, gdb *gorm.DB, no string) string {
	t.Helper()
	var w Withdrawal
	require.NoError(t, gdb.Where("withdraw_no = ?", no).Take(&w).Error)
	return w.RiskFlags
}

// 第二个人用同一张卡提交时,**先提交的那张也要被补上标记**。
func TestMarkSharedPayee_BackfillsTheEarliestOrder(t *testing.T) {
	gdb := newTestDB(t)

	first := submitFiatWith(t, gdb, 101, testDigest, "idem-a")
	assert.Empty(t, first.RiskFlags, "只有一个人用这张卡时不该标红(家庭共用是真实场景)")

	second := submitFiatWith(t, gdb, 102, testDigest, "idem-b")
	assert.Equal(t, RiskSharedPayee, second.RiskFlags)
	assert.Equal(t, RiskSharedPayee, riskFlagsOf(t, gdb, first.WithdrawNo),
		"最早那张单必须被回补 —— 它正是 FIFO 队列里排最前、最先被打款的那张")

	// 第三张进来时,前两张已经带标记,不该被重复写。
	third := submitFiatWith(t, gdb, 103, testDigest, "idem-c")
	assert.Equal(t, RiskSharedPayee, third.RiskFlags)
}

// 回补不许改 updated_at:补一个风控标记不是"对这张单的处理",
// 把一批历史单顶到列表最前面会让管理端的时间线整个失真。
func TestMarkSharedPayee_BackfillDoesNotTouchUpdatedAt(t *testing.T) {
	gdb := newTestDB(t)

	first := submitFiatWith(t, gdb, 111, testDigest, "idem-a")
	var before Withdrawal
	require.NoError(t, gdb.Where("withdraw_no = ?", first.WithdrawNo).Take(&before).Error)
	// 人为把时间推到过去,回补之后它必须原样不动。
	require.NoError(t, gdb.Model(&Withdrawal{}).Where("withdraw_no = ?", first.WithdrawNo).
		UpdateColumn("updated_at", before.UpdatedAt-86400).Error)

	submitFiatWith(t, gdb, 112, testDigest, "idem-b")

	var after Withdrawal
	require.NoError(t, gdb.Where("withdraw_no = ?", first.WithdrawNo).Take(&after).Error)
	assert.Equal(t, RiskSharedPayee, after.RiskFlags)
	assert.Equal(t, before.UpdatedAt-86400, after.UpdatedAt, "回补不该顶起 updated_at")
}

// 已经终态的历史单同样要被补上:那张卡上走过哪些单,是事后追查的全部用途,
// 而 `risk_only=true` 是唯一能把它们捞出来的过滤器。
func TestMarkSharedPayee_BackfillsTerminalOrders(t *testing.T) {
	gdb := newTestDB(t)

	paid := seedWithdrawal(t, gdb, "WD-paid-old", func(w *Withdrawal) {
		w.UserId = 201
		w.IdemKey = idemKeyOf(201, "old")
		w.Method = config.WithdrawMethodFiat
		w.Status = StatusPaid
		w.PayeeDigest = testDigest
	})

	submitFiatWith(t, gdb, 202, testDigest, "idem-new")

	assert.Equal(t, RiskSharedPayee, riskFlagsOf(t, gdb, paid.WithdrawNo),
		"已打款的那张也要能被 risk_only 捞出来 —— 钱已经出去了,追查才刚开始")

	// risk_only=true 的那条 WHERE 必须真的能同时捞到两张。
	var flagged int64
	require.NoError(t, gdb.Model(&Withdrawal{}).Where("risk_flags <> ''").Count(&flagged).Error)
	assert.EqualValues(t, 2, flagged)
}

// 同一个人自己用同一张卡提交多次不算共用。
func TestMarkSharedPayee_SameUserIsNotShared(t *testing.T) {
	gdb := newTestDB(t)

	seedFiatBalance(t, gdb, 301, 50000000, "850", "CNY")

	// 同一个人的两张单,同一张卡。第二张不该把第一张标红。
	var orders []*Withdrawal
	for _, idem := range []string{"idem-a", "idem-b"} {
		w := fiatOrder(301, 10000000, idem)
		w.PayeeDigest = testDigest
		acc := acceptedRequest{IdemKey: idem, Method: config.WithdrawMethodFiat, Quota: 10000000}
		_, err := submitInTx(gdb, w, nil, acc, ledgerCfg(), "u")
		require.NoError(t, err)
		orders = append(orders, w)
	}

	assert.Empty(t, orders[1].RiskFlags)
	assert.Empty(t, riskFlagsOf(t, gdb, orders[0].WithdrawNo))
}

// 别的指纹不受牵连:回补的 WHERE 必须带指纹条件,否则一次共用会把全表标红。
func TestMarkSharedPayee_OtherDigestsUntouched(t *testing.T) {
	gdb := newTestDB(t)

	other := seedWithdrawal(t, gdb, "WD-other-card", func(w *Withdrawal) {
		w.UserId = 401
		w.IdemKey = idemKeyOf(401, "other")
		w.Method = config.WithdrawMethodFiat
		w.PayeeDigest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	})
	quotaOrder := seedWithdrawal(t, gdb, "WD-quota", func(w *Withdrawal) {
		w.UserId = 402
		w.IdemKey = idemKeyOf(402, "quota")
	})

	submitFiatWith(t, gdb, 403, testDigest, "idem-a")
	submitFiatWith(t, gdb, 404, testDigest, "idem-b")

	assert.Empty(t, riskFlagsOf(t, gdb, other.WithdrawNo))
	assert.Empty(t, riskFlagsOf(t, gdb, quotaOrder.WithdrawNo),
		"quota 单没有收款人,指纹为空,永远不该被这条回补扫到")
}
