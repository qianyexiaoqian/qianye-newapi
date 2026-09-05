package transfer

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// zz_audit_transfer_refund_test.go —— 资金审计:退还风控预占时的「跨日」判定。
//
// 被审计的不变量(来自 undoReservation / refundReservation 的注释本身):
//
//	「预占之后若已跨日,日计数早被 rollDay 清零,再减会把今天的额度凭空放大」
//
// refundReservation 只算了**一个** sameDay,取自**发起方**那一行的 DayBucket:
//
//	sameDay := sender.DayBucket == dayBucket(row.CreatedAt)
//	undoReservation(sender, receiver, ..., sameDay, now)
//
// 然后把这**同一个**判定用在了收款方的 DayInCount 上(risk.go undoReservation):
//
//	if sameDay {
//	    sender.DayOutQuota  -= total
//	    sender.DayOutCount  -= 1
//	    receiver.DayInCount -= 1   // ← 用的是 sender 的 sameDay
//	}
//
// 但发起方与收款方的自然日计数**各自独立滚动**:收款方的 DayBucket 由它自己
// 参与的任何一笔划转(作为收方或发方)在 reserveRisk 里 rollDay 推进,与发起方
// 那一笔失败单毫不相干。于是当发起方**没跨日**、收款方**已跨日**时:
//
//   - 发起方角度 sameDay = true(它的 DayBucket 仍停在建单那天);
//   - 收款方的 DayInCount 却是**新的一天**里新收到的那些笔,与这张失败单无关
//     (旧那天的计数早被 rollDay 清零了);
//   - undoReservation 照样把它 -1 —— 收款方今天凭空多出一个「可接收笔数」名额。
//
// receiver_daily_max_in_count 正是「一堆小号汇集到同一账号」这条洗号路径唯一的
// 闸门(见 evaluateRisk)。把它的当日计数错误地减 1,就是在**放宽**这道资金闸门。

// seedRefundState 直接落一行风控状态,字段全给,避免零值把断言搅浑。
func seedRefundState(t *testing.T, gdb *gorm.DB, s UserState) {
	t.Helper()
	require.NoError(t, gdb.Create(&s).Error)
}

// seedPendingRiskHeldOrder 落一张「已预占、待结算」的明细单,建单时间可控。
func seedPendingRiskHeldOrder(t *testing.T, gdb *gorm.DB, orderNo string, from, to int, amount, fee, createdAt int64) {
	t.Helper()
	require.NoError(t, gdb.Create(&Order{
		OrderNo:    orderNo,
		FromUserId: from,
		ToUserId:   to,
		Amount:     amount,
		FeeQuota:   fee,
		Status:     statusPending,
		RiskHeld:   true,
		CreatedAt:  createdAt,
	}).Error)
}

// TestFailedRefundLoosensReceiverDailyInboundGateWhenReceiverCrossedDay 复现放宽方向。
//
// 时序:
//
//	① 发起方 S 在【前天】发起一笔到收款方 R,占用 R 当天的 1 个入账名额,
//	   随后这笔卡在 pending/uncertain(主库 COMMIT 断连等)。
//	② R 是个活跃的汇集账号,在【今天】又收了 5 笔(它自己的 DayBucket 滚到今天,
//	   DayInCount=5,前天那 1 笔早被 rollDay 清掉)。S 期间没再参与任何划转,
//	   DayBucket 仍停在前天。
//	③ 对账任务在【今天】把 S 那张前天的失败单收敛为 failed → refundReservation。
//
// 正确语义:R 今天的 5 个入账名额与前天那张失败单无关,退还预占**不该动** R 今天的
// DayInCount(前天那 1 笔的名额早已随 rollDay 消失)。
//
// 实测:refundReservation 拿 S 的 sameDay(=true,S 没跨日)去减 R 的 DayInCount,
// 把 5 减成 4 —— R 今天凭空多出一个入账名额,洗号闸门被放宽。
func TestFailedRefundLoosensReceiverDailyInboundGateWhenReceiverCrossedDay(t *testing.T) {
	gdb := newSettingsTestDB(t)

	now := common.GetTimestamp()
	twoDaysAgo := now - 2*86400
	bucketOld := dayBucket(twoDaysAgo)
	bucketToday := dayBucket(now)
	require.NotEqual(t, bucketOld, bucketToday, "前置:两天必须落在不同的自然日桶")

	const sender, receiver = 1, 2
	const amount, fee int64 = 1_000, 0

	// 发起方 S:DayBucket 停在前天(它自建单后没再参与任何划转),预占仍握着。
	seedRefundState(t, gdb, UserState{
		UserId: sender, DayBucket: bucketOld,
		DayOutGroup: "default", DayOutQuota: amount + fee, DayOutCount: 1,
		LifetimeOutQuota: amount + fee, PendingCount: 1, UpdatedAt: twoDaysAgo,
	})
	// 收款方 R:已滚到今天,今天新收了 5 笔(与前天那张失败单毫无关系)。
	seedRefundState(t, gdb, UserState{
		UserId: receiver, DayBucket: bucketToday,
		DayInCount: 5, LifetimeInQuota: 10 * amount, UpdatedAt: now,
	})

	seedPendingRiskHeldOrder(t, gdb, "TR-refund-loosen", sender, receiver, amount, fee, twoDaysAgo)

	// 对账把前天的失败单收敛:这一步就是 reconcile → applyFundOrderStatus 的 failed 分支。
	require.NoError(t, settleDetail("TR-refund-loosen", settlement{
		status: statusFailed, failCode: "qy_main_not_applied", failReason: "主库未生效",
	}))

	var r UserState
	require.NoError(t, gdb.First(&r, "user_id = ?", receiver).Error)

	assert.Equal(t, 5, r.DayInCount,
		"收款方已跨日,其今天的入账计数与前天那张失败单无关,退还预占绝不该把它减 1 —— "+
			"减了就是凭空放宽 receiver_daily_max_in_count 这道洗号闸门(修复前会被误减成 4)")

	// 对照:发起方**没跨日**,它自己那笔预占该正常退还,DayOutCount 回到 0。
	// 这一条确认失败单本身确实走到了退还逻辑,排除「什么都没发生」。
	var s UserState
	require.NoError(t, gdb.First(&s, "user_id = ?", sender).Error)
	assert.Equal(t, 0, s.DayOutCount, "发起方没跨日,它的当日转出计数应被正常退还到 0")
	assert.Equal(t, 0, s.PendingCount, "失败单必须释放未结算笔数")
}

// TestFailedRefundOverchargesReceiverInboundWhenSenderCrossedDay 守住相反方向(收紧)。
//
// 同一个根因的另一面:发起方 S 在预占之后**被动跨了日**(它作为别人的收款方,
// 在新的一天里被 reserveRisk rollDay 推进了 DayBucket —— PendingCount 只挡住 S
// 主动发起,挡不住别人转给 S)。此时 S 相对失败单 sameDay=false;修复前
// undoReservation 用这一个判定一刀切,**跳过**收款方 R 的 DayInCount 退还,可 R
// 明明**没跨日**(仍停在建单那天)、那张失败单占掉的入账名额还实实在在压在它当天
// 的计数里,不退 = 一张失败的划转永久吃掉 R 一个名额。
//
// 这一侧方向保守(收紧闸门),不是资损,但它和上面那条是同一个缺陷:一个 sameDay
// 判定被强加到两个独立滚动的自然日计数上。修复后两方各判各的,这里退还 R 的名额。
func TestFailedRefundOverchargesReceiverInboundWhenSenderCrossedDay(t *testing.T) {
	gdb := newSettingsTestDB(t)

	now := common.GetTimestamp()
	orderDay := now - 2*86400
	bucketOrder := dayBucket(orderDay)
	bucketToday := dayBucket(now)
	require.NotEqual(t, bucketOrder, bucketToday, "前置:建单那天与今天必须落在不同的自然日桶")

	const sender, receiver = 3, 4
	const amount, fee int64 = 1_000, 0

	// 发起方 S:预占之后被动跨日到今天(作为别人的收方被 rollDay 推进,PendingCount
	// 只挡 S 主动发起、挡不住别人转给 S),DayBucket 已滚到今天、当日转出计数被清零。
	seedRefundState(t, gdb, UserState{
		UserId: sender, DayBucket: bucketToday,
		DayOutGroup: "", DayOutQuota: 0, DayOutCount: 0,
		LifetimeOutQuota: amount + fee, PendingCount: 1, UpdatedAt: now,
	})
	// 收款方 R:**没跨日**,仍停在建单那天(自建单后一直没再参与划转),它当天的
	// DayInCount=3 里实实在在含着那张失败单占的 1 个名额。
	seedRefundState(t, gdb, UserState{
		UserId: receiver, DayBucket: bucketOrder,
		DayInCount: 3, LifetimeInQuota: 10 * amount, UpdatedAt: orderDay,
	})

	// 失败单建于那一天:发起方相对它已跨日(sameDay=false),收款方相对它没跨日
	// (sameDay=true)—— 正是"一个 sameDay 判定按发起方口径同时决定两方"会出错的另一面。
	seedPendingRiskHeldOrder(t, gdb, "TR-refund-tighten", sender, receiver, amount, fee, orderDay)

	require.NoError(t, settleDetail("TR-refund-tighten", settlement{
		status: statusFailed, failCode: "qy_main_not_applied", failReason: "主库未生效",
	}))

	var r UserState
	require.NoError(t, gdb.First(&r, "user_id = ?", receiver).Error)
	assert.Equal(t, 2, r.DayInCount,
		"收款方没跨日,那张失败单占的入账名额还压在它当天的计数里,退还预占必须把 DayInCount 从 3 减到 2;"+
			"修复前 refundReservation 用发起方(已跨日)的 sameDay=false 一刀切,跳过了收款方的退还,名额被永久吃掉")
}
