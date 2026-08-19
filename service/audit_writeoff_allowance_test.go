package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audit_writeoff_allowance_test.go —— 「平台核销一个重置周期最多送出去多少」。
//
// settleSubscriptionDelta 的注释曾经把「每张套餐每个重置周期至多核销一次」写成
// 结构性事实:核销之后 amount_used == amount_total,pickFundingSubscription 的
// `remain <= 0 → continue` 就再也不给这张套餐派预扣了。
//
// 这条推理漏掉了并发:N 路请求可以**各自先拿到预扣**(此时套餐还没满)、各自
// 超支、再各自走到核销那一步。核销笔数 = 在飞路数,平台白送的额度随并发线性
// 放大且没有上界 —— 实测 10 路把一张面值 10000 的套餐核销掉 40000(面值的 4 倍)。
//
// 现在名额由 model.ClaimSubscriptionWriteOff 在行锁里发,一个周期一份。

// TestWriteOffAllowanceIsOnePerResetPeriod 直接把「同一张已经到顶的套餐、
// 闸门连续说不许补收」这件事跑两遍 —— 那正是并发下会发生的形态。
func TestWriteOffAllowanceIsOnePerResetPeriod(t *testing.T) {
	truncate(t)

	const (
		userId         = 8101
		planId         = 8201
		subscriptionId = 8301
		startWallet    = 1_000_000
		amountTotal    = 10_000
		shortfall      = 4_000
	)
	seedUser(t, userId, startWallet)
	seedPlanRow(t, planId, "write-off allowance plan")
	// 套餐已经被在飞的那几路预扣占满:amount_used == amount_total。
	seedSubscriptionWithPlan(t, subscriptionId, userId, planId, amountTotal, amountTotal)

	calls := stubShortfallGate(t, false)
	// 运营报表(/admin/group-namespace/report 的 shortfall_write_offs)只该收到
	// **真的落定了**的那一笔。这个登记以前写在闸门里,于是名额用尽后照样扣了
	// 钱包的那些笔也被计成免单 —— 虚高的部分恰好等于向用户收到的钱。
	noted := stubWriteOffLedger(t)

	// 第一路:拿到本周期唯一的核销名额,钱包一分不动 —— 这是上一轮定下的口径。
	firstSplit, err := settleSubscriptionDelta(userId, "default", "plan-only-group", subscriptionId, shortfall)
	require.NoError(t, err)
	assert.Equal(t, int64(0), firstSplit.Applied, "套餐已经到顶,吃不下任何一分")
	assert.Equal(t, int64(shortfall), firstSplit.WrittenOff)
	assert.Equal(t, int64(0), firstSplit.WalletCharged)

	walletAfterFirst, err := model.GetUserQuota(userId, false)
	require.NoError(t, err)
	assert.Equal(t, startWallet, walletAfterFirst, "开关说不许动钱包,第一笔就必须一分不动")

	// 第二路:名额已经被用掉。平台不能无上限地送,只能按实际用量向钱包收。
	secondSplit, err := settleSubscriptionDelta(userId, "default", "plan-only-group", subscriptionId, shortfall)
	require.NoError(t, err)
	assert.Equal(t, int64(0), secondSplit.WrittenOff,
		"一个重置周期只发一份核销名额,否则并发数就是平台的损失倍数")
	assert.Equal(t, int64(shortfall), secondSplit.WalletCharged)

	walletAfterSecond, err := model.GetUserQuota(userId, false)
	require.NoError(t, err)
	assert.Equal(t, startWallet-shortfall, walletAfterSecond)

	// 闸门两次都被问到,而且问的是同一组坐标 —— 名额是在闸门**之后**才起作用的,
	// 它不改变判据本身。
	require.Len(t, *calls, 2)
	for _, call := range *calls {
		assert.Equal(t, userId, call.userId)
		assert.Equal(t, "default", call.userGroup)
		assert.Equal(t, "plan-only-group", call.modelGroup)
		assert.Equal(t, int64(shortfall), call.shortfall)
	}

	// 账面:两笔合计 8000,平台吃 4000、用户付 4000,一分都没有凭空消失。
	assert.Equal(t, int64(2*shortfall),
		firstSplit.WrittenOff+secondSplit.WrittenOff+firstSplit.WalletCharged+secondSplit.WalletCharged)

	// 运营看到的免单量必须**逐位等于**账面上真的免掉的那部分:一笔 4000,
	// 不是两笔 8000。闸门被问了两次,但只有一次真的免了单。
	require.Len(t, *noted, 1, "钱包出的那一笔不得进免单报表")
	assert.Equal(t, int64(shortfall), (*noted)[0].quota)
	assert.Equal(t, userId, (*noted)[0].userId)
	assert.Equal(t, "default", (*noted)[0].userGroup)
	assert.Equal(t, "plan-only-group", (*noted)[0].modelGroup)
}

// writeOffNote 是一次"真的核销掉了"的登记。
type writeOffNote struct {
	userId                int
	userGroup, modelGroup string
	quota                 int64
}

// stubWriteOffLedger 截下核销登记,用来断言"报表上的数字 == 账面上真的免掉的钱"。
func stubWriteOffLedger(t *testing.T) *[]writeOffNote {
	t.Helper()
	original := QyNoteSubscriptionWriteOff
	notes := make([]writeOffNote, 0, 2)
	QyNoteSubscriptionWriteOff = func(userId int, userGroup, modelGroup string, quota int64) {
		notes = append(notes, writeOffNote{userId, userGroup, modelGroup, quota})
	}
	t.Cleanup(func() { QyNoteSubscriptionWriteOff = original })
	return &notes
}

// TestWriteOffAllowanceIsNotConsumedWhenTheWalletMayPay 反向:闸门允许补收时
// 一次核销都不该发生,名额必须原封不动地留着。
func TestWriteOffAllowanceIsNotConsumedWhenTheWalletMayPay(t *testing.T) {
	truncate(t)

	const (
		userId         = 8102
		planId         = 8202
		subscriptionId = 8302
		startWallet    = 1_000_000
		amountTotal    = 10_000
		shortfall      = 4_000
	)
	seedUser(t, userId, startWallet)
	seedPlanRow(t, planId, "wallet-may-pay plan")
	seedSubscriptionWithPlan(t, subscriptionId, userId, planId, amountTotal, amountTotal)
	stubShortfallGate(t, true)

	split, err := settleSubscriptionDelta(userId, "default", "shared-group", subscriptionId, shortfall)
	require.NoError(t, err)
	assert.Equal(t, int64(shortfall), split.WalletCharged)
	assert.Equal(t, int64(0), split.WrittenOff)

	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("id = ?", subscriptionId).First(&sub).Error)
	assert.Equal(t, 0, sub.WriteOffCount,
		"钱包补收得了的时候不该动核销名额 —— 否则一次正常补收就把这个周期的名额吃光了")
}

// TestWriteOffAllowanceIsNotConsumedByARefund 负差额(退款没能全额退进套餐)
// 既不补钱包也不核销,更不能吃掉名额。
func TestWriteOffAllowanceIsNotConsumedByARefund(t *testing.T) {
	truncate(t)

	const (
		userId         = 8103
		planId         = 8203
		subscriptionId = 8303
		startWallet    = 1_000_000
	)
	seedUser(t, userId, startWallet)
	seedPlanRow(t, planId, "refund plan")
	seedSubscriptionWithPlan(t, subscriptionId, userId, planId, 10_000, 0)
	calls := stubShortfallGate(t, false)

	split, err := settleSubscriptionDelta(userId, "default", "plan-only-group", subscriptionId, -5_000)
	require.NoError(t, err)
	assert.Equal(t, int64(0), split.WrittenOff)
	assert.Equal(t, int64(0), split.WalletCharged)
	assert.Empty(t, *calls, "退款方向根本不该问闸门")

	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("id = ?", subscriptionId).First(&sub).Error)
	assert.Equal(t, 0, sub.WriteOffCount)
}
