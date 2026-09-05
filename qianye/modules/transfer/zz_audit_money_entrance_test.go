package transfer

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// zz_audit_money_entrance_test.go —— 资金安全审计探针:金额边界与收款方状态。
//
// 全部从 create() 这个真正会动钱的入口打进去,并且断言主库两行的 quota。
// 「拒绝了」与「拒绝了且一分钱没动」是两回事:判定挪到两条 UPDATE 之后同样
// 会返回错误,而钱已经转走了。

// auditWideOpenGlobal 把除了 common.MaxQuota 那条算术硬顶之外的门槛全部关掉。
//
// 目的是让金额边界用例的失败原因唯一:任何一个被拒的金额都只能是撞上了
// 硬顶或者 amount<=0,而不是碰巧被 min_quota / max_per_tx_quota 挡下。
func auditWideOpenGlobal() config.Transfer {
	cfg := createGlobal()
	cfg.MinQuota = 1
	cfg.MaxPerTxQuota = 0 // 0 = 不设单笔上限
	cfg.DailyMaxQuota = 0
	cfg.DailyMaxCount = 0
	cfg.ReceiverDailyMaxInCount = 0
	return cfg
}

// auditEnv 建起 create() 需要的两个库,并按 quotas 播种主库用户。
func auditEnv(t *testing.T, global config.Transfer, quotas map[int]int) (*gorm.DB, *gorm.DB) {
	t.Helper()
	gdb, mainDB := createEnv(t, global)
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	for id, q := range quotas {
		seedMainUser(t, mainDB, id, "default", q)
	}
	return gdb, mainDB
}

// TestAuditAmountBoundsAtTheMoneyEntrance 把金额区间的每一条边界都从动钱入口打一遍。
//
// 最危险的两个取值是 math.MaxInt64 与 common.MaxQuota+1:前者会让任何一处
// 「先算 amount+fee 再判上界」的加法回绕成负数(扣款变加款),后者是
// twophase.validateAmount 与 validateCreate 共同守的那条算术硬顶。
// 断言到主库余额,而不是只看错误码 —— 校验被挪到扣款之后同样会返回错误。
func TestAuditAmountBoundsAtTheMoneyEntrance(t *testing.T) {
	const senderQuota = 90_000_000
	const receiverQuota = 12_345

	cases := []struct {
		name   string
		amount int64
	}{
		{name: "零", amount: 0},
		{name: "负一", amount: -1},
		{name: "int64 下界", amount: math.MinInt64},
		{name: "int64 上界", amount: math.MaxInt64},
		{name: "刚好越过额度换算硬顶", amount: int64(common.MaxQuota) + 1},
		{name: "恰好等于硬顶(余额不足,但绝不能溢出成加款)", amount: int64(common.MaxQuota)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb, mainDB := auditEnv(t, auditWideOpenGlobal(),
				map[int]int{1: senderQuota, 2: receiverQuota})

			err := callCreate(t, 1, createRequest{
				ToUserId: 2, Amount: tc.amount, Confirm: true,
				ClientRequestId: "audit-amount-bound",
			})
			require.Error(t, err, "越界金额必须被拒")

			assert.Equal(t, senderQuota, quotaOf(t, mainDB, 1),
				"被拒的划转不得扣走发起方一分钱")
			assert.Equal(t, receiverQuota, quotaOf(t, mainDB, 2),
				"被拒的划转不得给收款方加一分钱")

			var settled int64
			require.NoError(t, gdb.Model(&Order{}).
				Where("status = ?", statusSuccess).Count(&settled).Error)
			assert.Zero(t, settled, "越界金额不得留下一张成功的明细单")
		})
	}

	// 对照组:同一套环境下一个合法金额必须真的转走。少了它,上面每一条
	// 「余额没动」都可能只是因为这个环境根本转不通。
	t.Run("对照:合法金额确实成交", func(t *testing.T) {
		_, mainDB := auditEnv(t, auditWideOpenGlobal(),
			map[int]int{1: senderQuota, 2: receiverQuota})
		require.NoError(t, callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true,
			ClientRequestId: "audit-amount-control",
		}))
		assert.Equal(t, senderQuota-1_000_000, quotaOf(t, mainDB, 1))
		assert.Equal(t, receiverQuota+1_000_000, quotaOf(t, mainDB, 2))
	})
}

// TestAuditSelfTransferNeverReachesTheLedger 钉住自转在动钱入口被硬拒。
//
// 自转会让扣款与加款落在同一行上:先扣 amount+fee 再加 amount 的净效果是
// 「凭空销毁手续费」,而如果两条 UPDATE 的顺序或条件写反,它就是「凭空造钱」。
// 余额守恒的所有校验在同一行上都会失效,所以这条只能硬拒。
func TestAuditSelfTransferNeverReachesTheLedger(t *testing.T) {
	gdb, mainDB := auditEnv(t, auditWideOpenGlobal(), map[int]int{1: 90_000_000})

	err := callCreate(t, 1, createRequest{
		ToUserId: 1, Amount: 1_000_000, Confirm: true,
		ClientRequestId: "audit-self-transfer",
	})
	require.Error(t, err)
	assert.Same(t, errSelfTransfer, err)
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1), "自转必须一分钱都不动")

	var orders int64
	require.NoError(t, gdb.Model(&Order{}).Count(&orders).Error)
	assert.Zero(t, orders, "自转不得落任何明细单")
}

// TestAuditReceiverStateGatesAtTheMoneyEntrance 覆盖收款方的三种非法状态。
//
// 软删账号那一条最要紧:给已注销账号加款等于把钱转进一个再也取不出来的账户。
// lockUserForUpdate 的 First 自带 deleted_at IS NULL 过滤,加一个 Unscoped
// 就会让这条断言翻面。
func TestAuditReceiverStateGatesAtTheMoneyEntrance(t *testing.T) {
	const senderQuota = 90_000_000

	t.Run("收款人不存在", func(t *testing.T) {
		_, mainDB := auditEnv(t, auditWideOpenGlobal(), map[int]int{1: senderQuota})
		err := callCreate(t, 1, createRequest{
			ToUserId: 4242, Amount: 1_000_000, Confirm: true,
			ClientRequestId: "audit-recv-missing",
		})
		require.Error(t, err)
		assert.Same(t, errReceiverNotFound, err)
		assert.Equal(t, senderQuota, quotaOf(t, mainDB, 1))
	})

	t.Run("收款人已被停用", func(t *testing.T) {
		_, mainDB := auditEnv(t, auditWideOpenGlobal(),
			map[int]int{1: senderQuota, 2: 777})
		require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 2).
			Update("status", common.UserStatusDisabled).Error)

		err := callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true,
			ClientRequestId: "audit-recv-disabled",
		})
		require.Error(t, err)
		assert.Same(t, errReceiverDisabled, err)
		assert.Equal(t, senderQuota, quotaOf(t, mainDB, 1))
		assert.Equal(t, 777, quotaOf(t, mainDB, 2))
	})

	t.Run("收款人已注销(软删)", func(t *testing.T) {
		_, mainDB := auditEnv(t, auditWideOpenGlobal(),
			map[int]int{1: senderQuota, 2: 777})
		require.NoError(t, mainDB.Where("id = ?", 2).Delete(&model.User{}).Error)

		err := callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true,
			ClientRequestId: "audit-recv-deleted",
		})
		require.Error(t, err)
		assert.Same(t, errReceiverNotFound, err,
			"软删账号必须表现为不存在,而不是一个能收钱的账户")
		assert.Equal(t, senderQuota, quotaOf(t, mainDB, 1))

		// 直接回读那一行(绕过软删过滤),确认钱确实没进去。
		var gone model.User
		require.NoError(t, mainDB.Unscoped().First(&gone, "id = ?", 2).Error)
		assert.Equal(t, 777, gone.Quota, "绝不能给已注销账号加款")
	})

	t.Run("发起方已被停用", func(t *testing.T) {
		_, mainDB := auditEnv(t, auditWideOpenGlobal(),
			map[int]int{1: senderQuota, 2: 777})
		require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
			Update("status", common.UserStatusDisabled).Error)

		err := callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true,
			ClientRequestId: "audit-sender-disabled",
		})
		require.Error(t, err)
		assert.Same(t, errSenderDisabled, err)
		assert.Equal(t, senderQuota, quotaOf(t, mainDB, 1))
		assert.Equal(t, 777, quotaOf(t, mainDB, 2))
	})
}

// TestAuditReceiverOverflowNeverDebitsTheSender 钉住收款方余额上界这道闸门。
//
// 上游对 users.quota 的加款全无溢出校验。加款条件里的
// `quota <= MaxQuota - amount` 一旦被去掉,收款方那一行会越过算术上界,
// 而发起方的钱**已经扣掉了** —— 那是纯资损。因此这里断言的重点是
// 「发起方一分钱没少」,而不只是返回了错误。
func TestAuditReceiverOverflowNeverDebitsTheSender(t *testing.T) {
	const amount int64 = 1_000_000
	const senderQuota = 90_000_000
	// 收款方再多收 1 就会越过硬顶。
	nearTop := int(int64(common.MaxQuota) - amount + 1)

	_, mainDB := auditEnv(t, auditWideOpenGlobal(),
		map[int]int{1: senderQuota, 2: nearTop})

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: amount, Confirm: true,
		ClientRequestId: "audit-recv-overflow",
	})
	require.Error(t, err)
	assert.Same(t, errReceiverOverflow, err)
	assert.Equal(t, senderQuota, quotaOf(t, mainDB, 1),
		"收款方装不下时绝不能先把发起方的钱扣掉")
	assert.Equal(t, nearTop, quotaOf(t, mainDB, 2))

	// 对照:恰好装得下的那一笔必须成交,否则上面的断言可能只是因为
	// 这个环境里任何划转都转不通。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 2).
		Update("quota", int64(common.MaxQuota)-amount).Error)
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: amount, Confirm: true,
		ClientRequestId: "audit-recv-overflow-ok",
	}))
	assert.EqualValues(t, int64(common.MaxQuota), quotaOf(t, mainDB, 2))
	assert.Equal(t, senderQuota-int(amount), quotaOf(t, mainDB, 1))
}

// TestAuditExactBalanceBoundaryWithFee 把「余额刚好不够 / 刚好够」这条边界
// 连同手续费一起从动钱入口打一遍。
//
// 差一分钱必须拒,而且必须是**连风控预占都退回来**的拒 —— 否则用户被扣掉
// 一次日额度却什么都没转成,而他自己看不出为什么下一笔也发不出去。
func TestAuditExactBalanceBoundaryWithFee(t *testing.T) {
	const amount int64 = 1_000_000
	cfg := auditWideOpenGlobal()
	cfg.FeeBps = 100 // 1%
	const fee int64 = amount / 100
	total := amount + fee

	gdb, mainDB := auditEnv(t, cfg, map[int]int{1: int(total) - 1, 2: 0})

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: amount, Confirm: true,
		ClientRequestId: "audit-exact-short",
	})
	require.Error(t, err)
	assert.Same(t, errInsufficientQuota, err)
	assert.Equal(t, int(total)-1, quotaOf(t, mainDB, 1))
	assert.Zero(t, quotaOf(t, mainDB, 2), "扣款没成功就绝不能给对方加钱")

	// 失败之后风控预占必须原路退还:未结算笔数归零、当日转出计数归零。
	// 不退还的话这个人会被自己的失败单永久顶住(PendingCount>0 直接拒新单)。
	var st UserState
	require.NoError(t, gdb.Where("user_id = ?", 1).First(&st).Error)
	assert.Zero(t, st.PendingCount, "失败的划转必须释放未结算笔数,否则用户被自己顶死")
	assert.Zero(t, st.DayOutCount, "失败的划转不得吃掉当日笔数")
	assert.Zero(t, st.DayOutQuota, "失败的划转不得吃掉当日额度")

	// 补上那一分钱,同一个人换一个 client_request_id 必须成交。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
		Update("quota", total).Error)
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: amount, Confirm: true,
		ClientRequestId: "audit-exact-enough",
	}))
	assert.Zero(t, quotaOf(t, mainDB, 1))
	assert.EqualValues(t, amount, quotaOf(t, mainDB, 2),
		"手续费只从发起方扣,收款方实收永远是 amount")
}

// TestAuditFeeIsChargedExactlyAsRecorded 钉住手续费的"账实相符"。
//
// 手续费是**被销毁**的:发起方扣 amount+fee,收款方只加 amount,不进第三方账户。
// 于是全站余额会因为每一笔划转而减少 fee —— 这个数字必须与落库的
// qy_transfer_orders.fee_quota 逐位相同,否则事后没有任何凭据能解释那笔差额
// (费率随时可改,按当前费率重算必然对不上账)。
//
// 三组取值分别覆盖:向上进位(0.5 → 1)、向下舍去、以及费率算出来低于
// fee_min_quota 时被抬到下限。
func TestAuditFeeIsChargedExactlyAsRecorded(t *testing.T) {
	cases := []struct {
		name    string
		amount  int64
		bps     int
		minFee  int64
		wantFee int64
	}{
		{name: "半分进位到 1", amount: 15_000, bps: 1, wantFee: 2}, // 1.5 → 2
		{name: "不足半分舍去", amount: 12_345, bps: 1, wantFee: 1},  // 1.2345 → 1
		{name: "低于下限抬到下限", amount: 1_000, bps: 100, minFee: 5_000, wantFee: 5_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := auditWideOpenGlobal()
			cfg.FeeBps = tc.bps
			cfg.FeeMinQuota = tc.minFee

			const senderQuota = 90_000_000
			gdb, mainDB := auditEnv(t, cfg, map[int]int{1: senderQuota, 2: 0})
			require.NoError(t, callCreate(t, 1, createRequest{
				ToUserId: 2, Amount: tc.amount, Confirm: true,
				ClientRequestId: "audit-fee-exact",
			}))

			row := auditOnlyOrder(t, gdb)
			assert.Equal(t, tc.wantFee, row.FeeQuota, "落库的手续费必须是这一笔真正算出来的那个数")

			debited := int64(senderQuota - quotaOf(t, mainDB, 1))
			credited := int64(quotaOf(t, mainDB, 2))
			assert.Equal(t, tc.amount+row.FeeQuota, debited,
				"发起方实扣必须恰好等于 金额 + 落库的手续费")
			assert.Equal(t, tc.amount, credited, "收款方实收永远是 amount,手续费不进它的账")
			assert.Equal(t, row.FeeQuota, debited-credited,
				"被销毁的那部分必须恰好等于 fee_quota —— 差额没有第三个去处")

			// 余额快照是争议仲裁的唯一凭据,必须与主库实际发生的变动一致。
			assert.Equal(t, int64(senderQuota), row.FromQuotaBefore)
			assert.Equal(t, int64(senderQuota)-tc.amount-row.FeeQuota, row.FromQuotaAfter)
			assert.Zero(t, row.ToQuotaBefore)
			assert.Equal(t, tc.amount, row.ToQuotaAfter)
		})
	}
}
