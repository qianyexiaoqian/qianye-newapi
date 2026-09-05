package transfer

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// zz_audit_idempotency_test.go —— 资金安全审计探针:幂等键的四条边界。
//
// 幂等键是「这一次点击到底代表哪一笔钱」的唯一判据。它的失效方向有两个,
// 而且两个都是资损:
//
//	识别不出重放 → 同一笔钱被扣两次(用户 / 前端重试)
//	把两笔不同的请求归并成一笔 → 第二笔被静默吞掉,或者第一笔的结论被
//	                             拿去回应第二笔(换金额/换收款人重放)
//
// 全部从 create() 这个真正会动钱的入口打进去,断言主库余额与扩展库两张表。

// auditOrderByNo 读一张明细单。
func auditOrderByNo(t *testing.T, gdb *gorm.DB, orderNo string) Order {
	t.Helper()
	var row Order
	require.NoError(t, gdb.Where("order_no = ?", orderNo).First(&row).Error)
	return row
}

func auditOnlyOrder(t *testing.T, gdb *gorm.DB) Order {
	t.Helper()
	var rows []Order
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 1, "本用例期望恰好一张明细单")
	return rows[0]
}

func auditUserState(t *testing.T, gdb *gorm.DB, userId int) UserState {
	t.Helper()
	var st UserState
	require.NoError(t, gdb.Where("user_id = ?", userId).First(&st).Error)
	return st
}

// TestAuditIdemKeyReplayWithDifferentFundingFactsIsRejected 复现「换参重放」。
//
// 唯一索引只保证"不重复执行",保证不了"重放的是同一个请求"。攻击形态:
// 先用 key=K 正常转 100 万给 2 号,再用同一个 K 提交「转 5000 万给 3 号」——
// 幂等命中会直接返回原单成功。指纹是唯一能识破它的判据。
//
// 断言分两层:① 第二次必须 409;② 第三方账号一分钱都没收到,而且原单的
// 状态/失败码没有被这次伪造请求污染 —— 审计表是事后仲裁的唯一凭据。
func TestAuditIdemKeyReplayWithDifferentFundingFactsIsRejected(t *testing.T) {
	const senderQuota = 90_000_000
	const key = "audit-idem-swap"

	t.Run("换金额", func(t *testing.T) {
		gdb, mainDB := auditEnv(t, auditWideOpenGlobal(),
			map[int]int{1: senderQuota, 2: 0, 3: 0})
		require.NoError(t, callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: key,
		}))
		original := auditOnlyOrder(t, gdb)

		err := callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 50_000_000, Confirm: true, ClientRequestId: key,
		})
		require.Error(t, err)
		assert.Same(t, errIdemKeyConflict, err,
			"同一个 client_request_id 换了金额,必须 409 而不是返回原单成功")

		assert.Equal(t, senderQuota-1_000_000, quotaOf(t, mainDB, 1),
			"换金额重放不得再扣一次")
		assert.Equal(t, 1_000_000, quotaOf(t, mainDB, 2))

		after := auditOrderByNo(t, gdb, original.OrderNo)
		assert.Equal(t, statusSuccess, after.Status, "原单不得被伪造请求推离终态")
		assert.Empty(t, after.FailCode, "原单的失败码不得被这次被拒的请求污染")
		assert.EqualValues(t, 1_000_000, after.Amount, "原单金额必须还是原来那一笔")
	})

	t.Run("换收款人", func(t *testing.T) {
		gdb, mainDB := auditEnv(t, auditWideOpenGlobal(),
			map[int]int{1: senderQuota, 2: 0, 3: 0})
		require.NoError(t, callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: key,
		}))

		err := callCreate(t, 1, createRequest{
			ToUserId: 3, Amount: 1_000_000, Confirm: true, ClientRequestId: key,
		})
		require.Error(t, err)
		assert.Same(t, errIdemKeyConflict, err)
		assert.Zero(t, quotaOf(t, mainDB, 3), "第三方账号绝不能凭一次换收款人的重放收到钱")
		assert.Equal(t, senderQuota-1_000_000, quotaOf(t, mainDB, 1))

		var orders int64
		require.NoError(t, gdb.Model(&Order{}).Count(&orders).Error)
		assert.EqualValues(t, 1, orders, "被拒的换参重放不得留下第二张明细单")
	})
}

// TestAuditIdemKeyIsCaseFoldedAtTheMoneyEntrance 钉住大小写折叠。
//
// 幂等键"相不相等"最终由列的排序规则说了算:MySQL 的库默认排序规则大小写
// 不敏感,PostgreSQL 与 SQLite 按字节比较。不折叠的话同一份代码在两种官方
// 支持的方言上给出**相反的资金结果** —— 本包的测试库正是按字节比较的 SQLite,
// 所以折叠一旦被去掉,这条用例会立刻在这里变红(扣两次)。
func TestAuditIdemKeyIsCaseFoldedAtTheMoneyEntrance(t *testing.T) {
	const senderQuota = 90_000_000

	t.Run("同一笔钱换个大小写重发只扣一次", func(t *testing.T) {
		gdb, mainDB := auditEnv(t, auditWideOpenGlobal(),
			map[int]int{1: senderQuota, 2: 0})

		require.NoError(t, callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true,
			ClientRequestId: "AUDIT-Fold-Key-1",
		}))
		// 同样的资金要素,只有键的大小写不同:这是同一笔的重放。
		err := callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true,
			ClientRequestId: "audit-fold-key-1",
		})
		require.NoError(t, err, "同一个键的重放应当原样返回原单结论")

		assert.Equal(t, senderQuota-1_000_000, quotaOf(t, mainDB, 1),
			"大小写不同的同一个键绝不能扣第二次")
		assert.Equal(t, 1_000_000, quotaOf(t, mainDB, 2))
		auditOnlyOrder(t, gdb) // 恰好一张单
	})

	t.Run("换大小写再换金额同样按冲突处理", func(t *testing.T) {
		gdb, mainDB := auditEnv(t, auditWideOpenGlobal(),
			map[int]int{1: senderQuota, 2: 0})

		require.NoError(t, callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true,
			ClientRequestId: "AUDIT-Fold-Key-2",
		}))
		err := callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 9_000_000, Confirm: true,
			ClientRequestId: "audit-fold-key-2",
		})
		require.Error(t, err)
		assert.Same(t, errIdemKeyConflict, err)
		assert.Equal(t, senderQuota-1_000_000, quotaOf(t, mainDB, 1))
		auditOnlyOrder(t, gdb)
	})
}

// TestAuditIdemKeyIsScopedPerUser 钉住幂等键的用户前缀。
//
// 两个互不相识的用户完全可能生成同一个 client_request_id(前端用的是本地
// 随机串,不是全局序列)。前缀一旦丢掉,后发的那一笔会被当成重复提交静默
// 吞掉 —— 用户以为转了,钱没动,而且他还会从响应里看到**别人那张单**的单号。
func TestAuditIdemKeyIsScopedPerUser(t *testing.T) {
	const shared = "audit-shared-request-id"
	gdb, mainDB := auditEnv(t, auditWideOpenGlobal(),
		map[int]int{1: 90_000_000, 2: 90_000_000, 9: 0})

	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 9, Amount: 1_000_000, Confirm: true, ClientRequestId: shared,
	}))
	require.NoError(t, callCreate(t, 2, createRequest{
		ToUserId: 9, Amount: 2_000_000, Confirm: true, ClientRequestId: shared,
	}), "另一个用户用同一个 client_request_id 是两笔全新的划转,不是重放")

	assert.Equal(t, 90_000_000-1_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 90_000_000-2_000_000, quotaOf(t, mainDB, 2))
	assert.Equal(t, 3_000_000, quotaOf(t, mainDB, 9),
		"两个人各自的划转都必须真的到账")

	var orders int64
	require.NoError(t, gdb.Model(&Order{}).Count(&orders).Error)
	assert.EqualValues(t, 2, orders, "两个用户的同名请求必须是两张单")
}

// TestAuditReplayingAFailedKeyRefundsTheAllowanceExactlyOnce 钉住失败单重放的幂等。
//
// 失败的划转要把风控预占原路退还(当日额度、当日笔数、收款方入账笔数、
// 未结算笔数)。退还必须**恰好一次**:每重放一次就退一次的话,用户只要
// 拿一个注定失败的键反复打,就能把当天已经用掉的额度一点点"洗"回来。
//
// 判据刻意建立在一笔**成功**划转留下的计数之上:失败单退还之后计数应当
// 恰好回到那一笔成功的水位,多退一次就会掉到水位以下(clampNonNegative
// 只保证不为负,拦不住"多退")。
func TestAuditReplayingAFailedKeyRefundsTheAllowanceExactlyOnce(t *testing.T) {
	gdb, mainDB := auditEnv(t, auditWideOpenGlobal(), map[int]int{1: 1_000_000, 2: 0})

	// ① 一笔成功的划转,定下计数水位。
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 400_000, Confirm: true, ClientRequestId: "audit-fail-seed-ok",
	}))
	sender := auditUserState(t, gdb, 1)
	require.Equal(t, 1, sender.DayOutCount)
	require.EqualValues(t, 400_000, sender.DayOutQuota)

	// ② 一笔余额不足的划转:预占之后在主库那一步失败,必须原路退还。
	const failKey = "audit-fail-replay"
	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 900_000, Confirm: true, ClientRequestId: failKey,
	})
	require.Error(t, err)
	assert.Same(t, errInsufficientQuota, err)

	afterFail := auditUserState(t, gdb, 1)
	require.Equal(t, 1, afterFail.DayOutCount, "失败单必须把当日笔数退回成功那一笔的水位")
	require.EqualValues(t, 400_000, afterFail.DayOutQuota)
	require.Zero(t, afterFail.PendingCount)

	// ③ 同一个键重放若干次:每一次都必须是"这笔此前已失败",而且**不再退第二次**。
	for i := 0; i < 5; i++ {
		replayErr := callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 900_000, Confirm: true, ClientRequestId: failKey,
		})
		require.Error(t, replayErr, "第 "+strconv.Itoa(i)+" 次重放")
		assert.ErrorIs(t, replayErr, twophase.ErrOrderFailed)
	}

	final := auditUserState(t, gdb, 1)
	assert.Equal(t, 1, final.DayOutCount,
		"重放失败单不得把当日笔数越退越低 —— 那等于凭空刷回已经用掉的额度")
	assert.EqualValues(t, 400_000, final.DayOutQuota,
		"重放失败单不得把当日额度越退越低")
	assert.EqualValues(t, 400_000, final.LifetimeOutQuota)
	assert.Zero(t, final.PendingCount)

	receiver := auditUserState(t, gdb, 2)
	assert.Equal(t, 1, receiver.DayInCount,
		"收款方的入账笔数同样只该被退还一次")

	// 钱始终只动过成功的那一笔。
	assert.Equal(t, 600_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 400_000, quotaOf(t, mainDB, 2))
}
