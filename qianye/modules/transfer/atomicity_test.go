package transfer

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// atomicity_test.go —— 「先扣款成功再给对方加,宁少勿多」的不变式与真并发回归。
//
// ═══════════════════ 项目方担心的到底是什么 ═══════════════════
//
// 原话:「划转前,你要先扣款成功后再给对方增加款项,不要出现漏洞,或者高并发的
// 情况下导致余额多增。宁少勿多,余额少了用户自然会发工单。」
//
// 「先扣后加」是**原子性**的直观说法。两条 UPDATE 都跑在主库的同一个事务里
// (twophase.applyOnMainDB 的 tx → applyQuotaTransfer(tx, ...)),同一个事务内的
// 先后顺序对最终可见状态没有影响 —— 要么两条一起提交,要么一起回滚。真正要防的
// 是**半截状态**:加款生效而扣款没生效。于是本文件钉的是三条:
//
//	① 同一个事务:扣款之后的任何一步失败,扣款必须一起消失(TestQuotaTransfer…HalfState);
//	② 宁少勿多:任何时刻 转出方减少额 >= 转入方增加额,差额恰好是手续费;
//	③ 高并发下 ① ② 仍然成立,五种形态各打一遍。
//
// ═══════════════════ SQLite 替身能证明什么、不能证明什么 ═══════════════════
//
// 本包的主库替身是 SQLite(见 newMainDB),那里 lockForUpdate 是空操作,写事务
// 由 SQLite 自己串行化。因此这几条并发用例**证明不了** MySQL 的行锁与加锁顺序
// 写对了 —— 它们证明的是另一件事,而那件事恰恰是项目方问的:无论几路并发、
// 无论多少路失败,**账面上永远不会出现"加了没扣"或者"加得比扣得多"**。
// 死锁顺序那一半由 applyQuotaTransfer / reserveRisk 里"按 user id 升序加锁"
// 的代码不变量顶着,交叉转账那一组用例是它的行为侧对照。

// atomicityGlobal 是本文件的全站门槛。手续费必须非零 ——
// 费率为 0 时"转出减少 == 转入增加",②那条不变式会退化成一句永真的等式,
// 而它要拦的恰恰是"转入比转出多"。
func atomicityGlobal() config.Transfer {
	cfg := createGlobal()
	cfg.MinQuota = 1_000
	cfg.FeeBps = 100     // 1%
	cfg.FeeMinQuota = 10 // 手续费下限,保证小额也带费
	cfg.DailyMaxCount = 0
	cfg.DailyMaxQuota = 0
	cfg.ReceiverDailyMaxInCount = 0
	return cfg
}

// atomicityEnv 建起 create() 需要的两个库,并把用户按 quotas 播好种。
func atomicityEnv(t *testing.T, quotas map[int]int) (*gorm.DB, *gorm.DB) {
	t.Helper()
	gdb, mainDB := createEnv(t, atomicityGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	for id, q := range quotas {
		seedMainUser(t, mainDB, id, "default", q)
	}
	return gdb, mainDB
}

// ═════════════════════ ① 同一个事务:不许留半截 ═════════════════════

// TestQuotaTransferLeavesNoHalfStateWhenALaterStepFails 钉住「扣款不会独立生效」。
//
// 做法是在**同一个主库事务里**、applyQuotaTransfer 返回之后再失败一次 ——
// 这正是生产里真实存在的形状:twophase.applyOnMainDB 在资金变更之后还要发
// COMMIT,而 outbox 认领、连接断开都可能让整段回滚。此时扣款必须一起消失,
// 绝不能留下"转出方少了钱、转入方没多钱"这种半截。
//
// 这一条能杀掉的变异:把 applyQuotaTransfer 里任意一条 UPDATE 的句柄从 tx 换成
// model.DB(或者让它自己 Begin 一个内层事务再提交)—— 那时扣款会独立落库,
// 回滚只带走加款,账面上凭空少一笔钱。
func TestQuotaTransferLeavesNoHalfStateWhenALaterStepFails(t *testing.T) {
	newSettingsTestDB(t)
	useSettingsConfig(t, atomicityGlobal())
	mainDB := newMainDB(t)
	seedMainUser(t, mainDB, 1, "default", seedSenderQuota)
	seedMainUser(t, mainDB, 2, "default", seedReceiverQuota)

	acc := seedAccepted()
	boom := errors.New("模拟资金变更之后、COMMIT 之前的失败")
	var snap quotaSnapshot
	err := mainDB.Transaction(func(tx *gorm.DB) error {
		if err := applyQuotaTransfer(tx, acc, mainDBConfig(), groupRuleSet{}, &snap); err != nil {
			return err
		}
		// 到这里扣款与加款都已经发出去了。断言它们在**本事务内**确实可见,
		// 否则下面那条"回滚后没动"会退化成永真。
		var sender, receiver model.User
		require.NoError(t, tx.First(&sender, "id = ?", 1).Error)
		require.NoError(t, tx.First(&receiver, "id = ?", 2).Error)
		assert.Equal(t, seedSenderQuota-seedAmount-seedFee, sender.Quota, "事务内应当已经扣掉")
		assert.Equal(t, seedReceiverQuota+seedAmount, receiver.Quota, "事务内应当已经加上")
		return boom
	})
	require.ErrorIs(t, err, boom)

	assert.Equal(t, seedSenderQuota, quotaOf(t, mainDB, 1),
		"扣款必须与加款同生共死;扣款独立落库就是凭空少一笔钱")
	assert.Equal(t, seedReceiverQuota, quotaOf(t, mainDB, 2),
		"加款必须与扣款同生共死;加款独立落库就是凭空多一笔钱")
}

// ═════════════════════ ② 宁少勿多 ═════════════════════

// TestSenderDecreaseNeverBelowReceiverIncrease 是本轮的核心不变式。
//
//	任何一笔成功的划转:转出方减少额 == 转入方增加额 + 手续费,
//	因此 转出方减少额 >= 转入方增加额,相等只在手续费为 0 时出现。
//
// 断言写成"差额恰好等于手续费"而不是只写 >= :只写 >= 的话,把加款金额从
// acc.Amount 改成 0 也能通过,那是"少给了对方"——方向虽然保守,但它是缺陷。
func TestSenderDecreaseNeverBelowReceiverIncrease(t *testing.T) {
	cases := []struct {
		name        string
		amount, fee int64
	}{
		{"带手续费:转出比转入多出手续费", 100_000, 1_000},
		{"手续费为 0:转出与转入相等", 100_000, 0},
		{"手续费下限吃掉小额:仍然只多不少", 1_000, 999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mainDB := newMainDB(t)
			seedMainUser(t, mainDB, 1, "default", seedSenderQuota)
			seedMainUser(t, mainDB, 2, "default", seedReceiverQuota)

			acc := seedAccepted()
			acc.Amount, acc.Fee, acc.Total = tc.amount, tc.fee, tc.amount+tc.fee
			var snap quotaSnapshot
			require.NoError(t, applyQuotaTransfer(mainDB, acc, mainDBConfig(), groupRuleSet{}, &snap))

			out := int64(seedSenderQuota - quotaOf(t, mainDB, 1))
			in := int64(quotaOf(t, mainDB, 2) - seedReceiverQuota)
			assert.Equal(t, tc.amount+tc.fee, out, "转出方实扣 = 金额 + 手续费")
			assert.Equal(t, tc.amount, in, "转入方实收 = 金额,手续费不进对方口袋")
			assert.GreaterOrEqual(t, out, in, "宁少勿多:转入多于转出即为缺陷")
			assert.Equal(t, tc.fee, out-in, "差额必须恰好是手续费,不多也不少")

			// 快照是事后仲裁的唯一凭据,它也必须满足同一条不变式。
			assert.Equal(t, snap.FromBefore-snap.FromAfter, out)
			assert.Equal(t, snap.ToAfter-snap.ToBefore, in)
			assert.GreaterOrEqual(t, snap.FromBefore-snap.FromAfter, snap.ToAfter-snap.ToBefore)
		})
	}
}

// TestInsufficientQuotaNeverCreditsTheReceiver 是失败方向的落点:
// 宁可"扣了没加"(用户发工单,可人工补),绝不能"加了没扣"。
//
// 余额差一分钱都不许放行 —— 那一分钱正是 WHERE quota >= ? 这道 CAS 的边界。
func TestInsufficientQuotaNeverCreditsTheReceiver(t *testing.T) {
	mainDB := newMainDB(t)
	acc := seedAccepted()
	// 刚好差 1:预检与 CAS 必须一致地拒绝。
	seedMainUser(t, mainDB, 1, "default", int(acc.Total)-1)
	seedMainUser(t, mainDB, 2, "default", seedReceiverQuota)

	var snap quotaSnapshot
	err := applyQuotaTransfer(mainDB, acc, mainDBConfig(), groupRuleSet{}, &snap)
	require.Error(t, err)
	assert.Same(t, errInsufficientQuota, err)
	assert.Equal(t, int(acc.Total)-1, quotaOf(t, mainDB, 1))
	assert.Equal(t, seedReceiverQuota, quotaOf(t, mainDB, 2), "扣款没成功就绝不能给对方加钱")

	// 对照:补上那 1 分钱就该成功 —— 少了它,上面的断言可能只是因为别的原因失败。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
		Update("quota", acc.Total).Error)
	require.NoError(t, applyQuotaTransfer(mainDB, acc, mainDBConfig(), groupRuleSet{}, &snap))
	assert.Equal(t, 0, quotaOf(t, mainDB, 1))
	assert.Equal(t, seedReceiverQuota+int(acc.Amount), quotaOf(t, mainDB, 2))
}

// ═════════════════════ ③ 五种并发形态 ═════════════════════

// transferShot 是并发用例里的一路请求。
type transferShot struct {
	from int
	req  createRequest
}

// fireConcurrently 把 n 路 create() 卡在同一个起跑线上同时放出去。
//
// 没有 sleep、没有随机、没有大循环:起跑线用一个 close(chan) 完成,
// 断言全部落在"钱"上,与调度顺序无关。
func fireConcurrently(t *testing.T, shots []transferShot) []error {
	t.Helper()
	start := make(chan struct{})
	errs := make([]error, len(shots))
	var wg sync.WaitGroup
	for i := range shots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = callCreate(t, shots[i].from, shots[i].req)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func countNil(errs []error) int {
	n := 0
	for _, err := range errs {
		if err == nil {
			n++
		}
	}
	return n
}

// shot 造一路「id=from 转 amount 给 to」的请求,幂等键取 key。
func shot(from, to int, amount int64, key string) transferShot {
	return transferShot{from: from, req: createRequest{
		ToUserId: to, Amount: amount, Confirm: true, ClientRequestId: key,
	}}
}

// assertNoOvercredit 是所有并发形态共用的判决。
//
// 它**不数订单表**,只看主库两侧余额的净变化:成功笔数 k 由收款方的增加额反推,
// 再要求转出方的减少额恰好等于 k*(amount+fee)。这样任何一笔"加了没扣"都会
// 让两边算出的 k 对不上;"扣了没加"则表现为转出方多减,同样会被抓到。
//
// 最后再与"返回 nil 的路数"对齐:两者不一致意味着有一路在账面上动了钱却
// 报了错(或者反过来),那是必须有人看的事故,不能被"反正总账对得上"掩盖。
func assertNoOvercredit(t *testing.T, mainDB *gorm.DB, from, to int,
	beforeFrom, beforeTo int, amount, fee int64, errs []error) int {
	t.Helper()
	out := int64(beforeFrom - quotaOf(t, mainDB, from))
	in := int64(quotaOf(t, mainDB, to) - beforeTo)

	assert.GreaterOrEqual(t, out, in, "宁少勿多:转出方减少额必须 >= 转入方增加额")
	assert.GreaterOrEqual(t, quotaOf(t, mainDB, from), 0, "转出方余额不得被并发打成负数")
	require.Zero(t, in%amount, "转入方的增加额必须是整笔金额的倍数,%d 不是", in)
	k := in / amount
	assert.Equal(t, k*(amount+fee), out,
		"转出方实扣必须恰好等于 %d 笔 × (金额 %d + 手续费 %d)", k, amount, fee)
	assert.EqualValues(t, countNil(errs), k,
		"成功返回的路数与真正动了钱的笔数必须一致")
	// 记下这一轮的真实数字。断言已经在上面做完了,这一行只是让 -v 的输出里
	// 有一份"期望 == 实测"的可读账,排查偶发时不必再加日志。
	t.Logf("并发 %d 路:成功 %d;转出方 %d→%d(-%d),转入方 %d→%d(+%d)",
		len(errs), countNil(errs), beforeFrom, quotaOf(t, mainDB, from), out,
		beforeTo, quotaOf(t, mainDB, to), in)
	return int(k)
}

// 形态一:同一个转出方多路并发。
//
// 这是「高并发导致余额多增」最直白的那一种:同一个人同时发四笔。
// 未结算笔数闸门(PendingCount)会把绝大多数并发挡在受理阶段,但本用例不断言
// "只有一笔成功" —— 那是实现细节;断言的是**无论几笔成功,账都是平的**。
func TestConcurrentTransfersFromOneSender(t *testing.T) {
	const amount int64 = 1_000_000
	const fee int64 = 10_000 // 1% of amount
	_, mainDB := atomicityEnv(t, map[int]int{1: 90_000_000, 2: 0})

	shots := make([]transferShot, 0, 4)
	for i := 0; i < 4; i++ {
		shots = append(shots, shot(1, 2, amount, "conc-one-sender-"+strconv.Itoa(i)))
	}
	errs := fireConcurrently(t, shots)
	k := assertNoOvercredit(t, mainDB, 1, 2, 90_000_000, 0, amount, fee, errs)
	assert.GreaterOrEqual(t, k, 1, "四路并发里至少要有一路真的成交,否则本用例什么都没验")
}

// 形态二:同一个转入方多路并发(四个不同的转出方汇集到同一账号)。
//
// 这一路真正会在收款方那一行上撞车:主库 users 那一行、扩展库
// qy_transfer_user_state 那一行都被四路同时写。
func TestConcurrentTransfersIntoOneReceiver(t *testing.T) {
	const amount int64 = 1_000_000
	const fee int64 = 10_000
	quotas := map[int]int{9: 0}
	for id := 1; id <= 4; id++ {
		quotas[id] = 90_000_000
	}
	_, mainDB := atomicityEnv(t, quotas)

	shots := make([]transferShot, 0, 4)
	for id := 1; id <= 4; id++ {
		shots = append(shots, shot(id, 9, amount, "conc-one-receiver-"+strconv.Itoa(id)))
	}
	errs := fireConcurrently(t, shots)

	// 转出方有四个,总账要按四个人合起来算。
	totalOut := int64(0)
	for id := 1; id <= 4; id++ {
		totalOut += int64(90_000_000 - quotaOf(t, mainDB, id))
		assert.GreaterOrEqual(t, quotaOf(t, mainDB, id), 0)
	}
	in := int64(quotaOf(t, mainDB, 9))
	assert.GreaterOrEqual(t, totalOut, in, "宁少勿多:四个转出方合计减少额必须 >= 收款方增加额")
	require.Zero(t, in%amount)
	k := in / amount
	assert.Equal(t, k*(amount+fee), totalOut)
	assert.EqualValues(t, countNil(errs), k, "成功返回的路数与真正动了钱的笔数必须一致")
	assert.GreaterOrEqual(t, k, int64(1), "四路并发里至少要有一路真的成交")
	t.Logf("并发 %d 路:成功 %d;四个转出方合计 -%d,转入方 0→%d(+%d)",
		len(errs), countNil(errs), totalOut, quotaOf(t, mainDB, 9), in)
}

// 形态三:A→B 与 B→A 交叉。
//
// 两侧的加锁顺序(主库 applyQuotaTransfer、扩展库 reserveRisk)都写死成
// "按 user id 升序",反序就会形成死锁环。本用例是那条代码不变量的行为侧对照:
// 交叉并发必须在有限时间内全部返回,并且账是平的。
func TestConcurrentCrossTransfers(t *testing.T) {
	const amount int64 = 1_000_000
	const fee int64 = 10_000
	_, mainDB := atomicityEnv(t, map[int]int{1: 90_000_000, 2: 90_000_000})

	errs := fireConcurrently(t, []transferShot{
		shot(1, 2, amount, "cross-a-1"),
		shot(2, 1, amount, "cross-b-1"),
		shot(1, 2, amount, "cross-a-2"),
		shot(2, 1, amount, "cross-b-2"),
	})

	// 交叉时"某一方的减少额"不再是单向的,总量守恒要按**全站合计**算:
	// 手续费是被销毁的,所以两人余额之和减少的正是手续费总额。
	sum := int64(quotaOf(t, mainDB, 1) + quotaOf(t, mainDB, 2))
	burned := int64(180_000_000) - sum
	require.Zero(t, burned%fee, "全站余额只会因为手续费而减少,%d 不是手续费的倍数", burned)
	assert.EqualValues(t, countNil(errs), burned/fee,
		"成交笔数必须等于被销毁的手续费笔数 —— 多出来的余额只可能来自一笔没扣款的加款")
	assert.GreaterOrEqual(t, quotaOf(t, mainDB, 1), 0)
	assert.GreaterOrEqual(t, quotaOf(t, mainDB, 2), 0)
	assert.GreaterOrEqual(t, countNil(errs), 1, "交叉并发里至少要有一路真的成交")
	t.Logf("交叉并发 %d 路:成功 %d;两人余额 90000000/90000000 → %d/%d,销毁手续费 %d",
		len(errs), countNil(errs), quotaOf(t, mainDB, 1), quotaOf(t, mainDB, 2), burned)
}

// 形态四:转出方余额**刚好够一笔**,同时并发两笔。
//
// 这是 WHERE quota >= ? 那道 CAS 唯一真正要命的场景:两路都在锁外读到"够",
// 只有一路能写成功。第二路必须失败,绝不能把余额扣成负数、更不能给对方加两次。
func TestConcurrentTransfersWithExactlyEnoughForOne(t *testing.T) {
	const amount int64 = 1_000_000
	const fee int64 = 10_000
	_, mainDB := atomicityEnv(t, map[int]int{1: int(amount + fee), 2: 0})

	errs := fireConcurrently(t, []transferShot{
		shot(1, 2, amount, "exact-1"),
		shot(1, 2, amount, "exact-2"),
	})
	k := assertNoOvercredit(t, mainDB, 1, 2, int(amount+fee), 0, amount, fee, errs)
	assert.Equal(t, 1, k, "余额只够一笔,就只能成一笔")
	assert.Equal(t, 0, quotaOf(t, mainDB, 1))
	assert.EqualValues(t, amount, quotaOf(t, mainDB, 2))
}

// 形态五:同一个幂等键并发重放(用户狂点提交 / 前端重试)。
//
// 幂等键相同 ⇒ 至多扣一次。这一条与前四条不同:它的期望是**确定的**,
// 因为唯一索引 (idem_scope, idem_key) 在任何一种并发下都只允许一行落库。
func TestConcurrentReplayOfTheSameIdemKeyChargesOnce(t *testing.T) {
	const amount int64 = 1_000_000
	const fee int64 = 10_000
	_, mainDB := atomicityEnv(t, map[int]int{1: 90_000_000, 2: 0})

	shots := make([]transferShot, 0, 4)
	for i := 0; i < 4; i++ {
		shots = append(shots, shot(1, 2, amount, "replay-same-key"))
	}
	errs := fireConcurrently(t, shots)

	out := int64(90_000_000 - quotaOf(t, mainDB, 1))
	in := int64(quotaOf(t, mainDB, 2))
	assert.EqualValues(t, amount, in, "同一个幂等键无论并发几路,对方只能收到一笔")
	assert.EqualValues(t, amount+fee, out, "同一个幂等键无论并发几路,只能扣一次")
	assert.GreaterOrEqual(t, out, in)
	assert.GreaterOrEqual(t, countNil(errs), 1, "至少有一路要拿到成功")

	// 落库的资金单也只能有一张。
	var orders int64
	require.NoError(t, mustExtDB(t).Model(&Order{}).
		Where("from_user_id = ? AND to_user_id = ?", 1, 2).Count(&orders).Error)
	assert.EqualValues(t, 1, orders, "同一个幂等键只允许一张明细单")
	t.Logf("同键重放 %d 路:成功 %d;转出方 90000000→%d(-%d),转入方 0→%d(+%d),明细单 %d 张",
		len(errs), countNil(errs), quotaOf(t, mainDB, 1), out, quotaOf(t, mainDB, 2), in, orders)
}

// mustExtDB 取出本用例正在用的扩展库句柄。
func mustExtDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := qyDBHandle.Load()
	require.NotNil(t, gdb, "扩展库句柄未装配")
	return gdb
}

// ═══════════ 两阶段四态与「结果不明时不许给对方加钱」 ═══════════

// TestUncertainMainSideNeverReleasesTheReservation 钉住四态里最要命的那一支。
//
// releaseOnFailure 只在「资金单已 failed **且** 主库探针明确说没生效」时才退还
// 风控预占。探针关掉、探针报错、探针行缺失一律归入"可能已生效",此时把预占退还
// 等于"钱已经转走却把额度原样还给用户"—— 那是唯一会造成超发的方向,
// 也是「宁少勿多」在失败路径上的落点。
func TestUncertainMainSideNeverReleasesTheReservation(t *testing.T) {
	gdb, mainDB := atomicityEnv(t, map[int]int{1: 90_000_000, 2: 0})
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "uncertain-seed",
	}))

	var row Order
	require.NoError(t, gdb.First(&row, "from_user_id = ?", 1).Error)
	// 把这一笔按"业务线程还没结算"的形状摆回去,再让它走一次失败收尾。
	require.NoError(t, gdb.Model(&Order{}).Where("order_no = ?", row.OrderNo).
		Updates(map[string]any{"status": statusPending, "risk_held": true}).Error)
	require.NoError(t, gdb.Model(&UserState{}).Where("user_id = ?", 1).
		Update("pending_count", 1).Error)

	markUncertainAfterConflict(row.OrderNo)

	var st UserState
	require.NoError(t, gdb.First(&st, "user_id = ?", 1).Error)
	assert.EqualValues(t, 1_010_000, st.DayOutQuota,
		"结果不明时绝不退还日累计 —— 退了就是把可能已经转走的钱重新发一遍额度")
	assert.Equal(t, 1, st.DayOutCount)
	assert.Equal(t, 1, st.PendingCount, "未结算笔数同样不许释放,它是禁止叠加第二笔的那道闸门")

	var after Order
	require.NoError(t, gdb.First(&after, "order_no = ?", row.OrderNo).Error)
	assert.Equal(t, statusUncertain, after.Status)
	assert.True(t, after.RiskHeld, "预占必须仍然握着,等人工裁决")
	// 对照:主库两行一分未动 —— markUncertainAfterConflict 只改扩展库的展示状态。
	assert.Equal(t, 90_000_000-1_010_000, quotaOf(t, mainDB, 1))
	assert.EqualValues(t, 1_000_000, quotaOf(t, mainDB, 2))
}

// TestReplayingOneIdemKeyDoesNotAmplifyTheArbitrationLedger 守仲裁台账的行数口径。
//
// qy_audit_logs 被本模块定义为「事后仲裁的唯一凭据」,而 create() 的失败分支
// 自辩说「行数与资金单一一对应」。实测那句话在**幂等重放**上不成立:
//
//	① 一笔失败单每重试一次就多一行 transfer.create/fail(线上同一个 key 连打
//	   8 次落了 8 行,而 qy_fund_orders 只有 1 张单);
//	② 在飞重放更糟:6 路并发同一个 key,4 路成交返回 ok、2 路撞上 ErrInProgress
//	   写下 fail —— 同一个单号下同时躺着 result=ok 与 result=fail「划转被拒」,
//	   而这一笔真的成交了。按单号查证时仲裁表给出两条口径相反的记录。
//
// 而前端的重试就是沿用同一个 client_request_id(transfer-form.tsx 的 requestId
// 只在打开弹窗与成功之后 renew),所以这是正常路径,不是攻击形态。
//
// 判据:同一个 client_request_id 无论被重放多少次,该单号下的 transfer.create
// 审计行数**恒等于 1**,且它的 result 与这一笔真实的结局一致。
func TestReplayingOneIdemKeyDoesNotAmplifyTheArbitrationLedger(t *testing.T) {
	t.Run("失败单重放:只留原始那一行 fail", func(t *testing.T) {
		gdb, mainDB := createEnv(t, createGlobal())
		require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
		// 余额刚好差 1:受理校验会过(金额在门槛内),失败发生在主库锁内的
		// 扣款 CAS 上 —— 那时资金单已经落库,正是这条审计分支唯一会走到的形状。
		seedMainUser(t, mainDB, 1, "default", 999_999)
		seedMainUser(t, mainDB, 2, "default", 0)

		const key = "replay-fail"
		first := callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: key,
		})
		require.Error(t, first)
		for i := 0; i < 5; i++ {
			require.Error(t, callCreate(t, 1, createRequest{
				ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: key,
			}), "重放必须继续被拒")
		}

		var rows []qymodel.AuditLog
		require.NoError(t, gdb.Where("action = ?", "transfer.create").Find(&rows).Error)
		assert.Len(t, rows, 1,
			"6 次尝试(1 次原始 + 5 次重放)只对应 1 张资金单,仲裁表也只能有 1 行")
		if len(rows) == 1 {
			assert.Equal(t, qymodel.ResultFail, rows[0].Result)
		}
		var orders int64
		require.NoError(t, gdb.Model(&qymodel.FundOrder{}).Count(&orders).Error)
		assert.EqualValues(t, 1, orders, "重放不该产生第二张资金单")
	})

	t.Run("成功单重放:那个单号下不许出现一行 fail", func(t *testing.T) {
		gdb, mainDB := createEnv(t, createGlobal())
		require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
		seedMainUser(t, mainDB, 1, "default", 90_000_000)
		seedMainUser(t, mainDB, 2, "default", 0)

		const key = "replay-ok"
		require.NoError(t, callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: key,
		}))
		// 同一个 key 再打三次:幂等命中返回原单(可能是成功、也可能撞上在飞态)。
		for i := 0; i < 3; i++ {
			_ = callCreate(t, 1, createRequest{
				ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: key,
			})
		}

		var rows []qymodel.AuditLog
		require.NoError(t, gdb.Where("action = ?", "transfer.create").Find(&rows).Error)
		require.Len(t, rows, 1, "一笔成交的划转在仲裁表里只能有一行")
		assert.Equal(t, qymodel.ResultOK, rows[0].Result,
			"这一笔真的成交了,同一个单号下绝不能同时躺着一条 result=fail")
		// 钱只动一次。
		assert.Equal(t, 90_000_000-1_000_000, quotaOf(t, mainDB, 1))
		assert.Equal(t, 1_000_000, quotaOf(t, mainDB, 2))
	})
}
