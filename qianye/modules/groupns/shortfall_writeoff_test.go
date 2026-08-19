package groupns

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortfall_writeoff_test.go —— 结算差额那一侧的钱包出资闸门。
//
// ═══════════ 它守的是「同一条规则,两个时刻」 ═══════════
//
// 套餐余额不够一次预扣额时按剩余额度**部分预扣**(model.pickFundingSubscription
// 的第二轮),真实花费超出那点余额时,差额撞到 amount_total 上限。此前那一段
// **无条件**落到钱包上 —— 也就是说 allow_wallet_overflow=false 的套餐照样让钱包
// 付了钱,那个开关在结算这条路径上从来没有被问过。enforce 档因此拦得住"下一次
// 请求",却拦不住"这一次请求的尾巴"。
//
// 修法不是给结算再写一条规则,而是让它问**同一个** walletFundingBlocked。
// 本文件第一条测试就是这句话的直接核实:把 ModelGroupFundingAllowed 的整张判据表
// 原样喂给 WalletMayCoverSubscriptionShortfall,两边必须逐行同号。
// 任何一边单独改判据都会让它红。

// TestShortfallWriteOffAnswersTheSameDecisionTable 把请求前那道闸门的整张判据表
// 原样搬到结算后这一道上。
//
// 两个入口共用 walletFundingBlocked,所以这张表不是"再抄一遍",而是**接缝断言**:
// 它钉死的是"结算这一侧没有自己的判据"。如果哪天有人给结算加一条特例
// (常见的诱惑:"反正请求已经跑完了,收了再说"),这条测试立刻红。
func TestShortfallWriteOffAnswersTheSameDecisionTable(t *testing.T) {
	for _, tc := range fundingGateTable() {
		t.Run(tc.name, func(t *testing.T) {
			newTestDB(t)
			nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
			useUpstreamGroups(t,
				map[string]string{"default": "默认", "共享池": "共享"},
				map[string]float64{"default": 1, "共享池": 1})
			usePlanUnlock(t, func(_ int, mg string, _ bool) (bool, bool, bool) {
				if mg != tc.modelGroup {
					return false, false, false
				}
				return tc.unlocked, tc.funded, tc.allowOverflow
			})

			requestTime, _ := ModelGroupFundingAllowed(7, "default", tc.modelGroup)
			settleTime := WalletMayCoverSubscriptionShortfall(7, "default", tc.modelGroup, 4900)

			assert.Equalf(t, tc.wantAllowed, settleTime,
				"结算差额能不能落钱包,判据必须与请求前那道闸门一致。理由:%s", tc.why)
			assert.Equal(t, requestTime, settleTime,
				"同一个人在同一笔请求的两个时刻拿到两种答案 —— 这正是判据分家的形状:"+
					"请求前拒绝了他,结算时又替他从钱包扣走一段(或者反过来,平台白吃一段)")
		})
	}
}

// TestShortfallWriteOffModeIsOnlyARolloutSwitch 锁住灰度档位在结算这一侧的语义,
// 与请求前那一侧逐字相同:off / shadow 一律照收,只有 enforce 才核销。
//
// 这一条不能省。核销是**真金白银的免单**,而它的开关必须与拒绝共用同一个档位 ——
// 否则会出现「enforce 拦住了新请求,但 gate 还没生效时的尾巴照样白送」这种
// 谁都没配出来的中间态。
func TestShortfallWriteOffModeIsOnlyARolloutSwitch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabled       bool
		mode          string
		wantMayCharge bool
	}{
		{"模块关闭", false, config.FundingGateEnforce, true},
		{"off(回滚档)", true, config.FundingGateOff, true},
		{"shadow(只记录)", true, config.FundingGateShadow, true},
		{"enforce(生效)", true, config.FundingGateEnforce, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestDB(t)
			nsConfig(t, tc.enabled, config.MissingRatioPolicyLegacyOne, tc.mode)
			useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
			usePlanUnlock(t, func(int, string, bool) (bool, bool, bool) {
				return true, false, false // 解锁了、已耗尽、且不许钱包续付
			})
			assert.Equal(t, tc.wantMayCharge,
				WalletMayCoverSubscriptionShortfall(7, "default", "反重力的哈基米", 4900))
		})
	}
}

// TestShortfallWriteOffCountsQuotaNotEvents 锁住核销计数的单位是**额度**。
//
// 运营要回答的问题是"这条规则这个月让我少收了多少",笔数回答不了它:
// 一笔 4900 与一笔 12 在拒绝计数里长得一模一样,而它们对账面的意义差 400 倍。
// 拒绝计数(enforceDenies,请求根本没跑)与核销额度是两件事,分桶也分开。
func TestShortfallWriteOffCountsQuotaNotEvents(t *testing.T) {
	newTestDB(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
	useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
	usePlanUnlock(t, func(int, string, bool) (bool, bool, bool) { return true, false, false })

	const key = "default→反重力的哈基米"
	before := ShortfallWriteOffs()[key]

	NoteShortfallWriteOff(7, "default", "反重力的哈基米", 4900)
	NoteShortfallWriteOff(8, "default", "反重力的哈基米", 12)

	assert.Equal(t, before+4912, ShortfallWriteOffs()[key],
		"核销的是额度不是次数:两笔 4900 与 12 加起来必须是 4912")
}

// TestTheGateItselfNeverCountsAWriteOff 是本轮修掉的那条口径缺陷的直接回归。
//
// 闸门只回答"规则允不允许钱包补收"。这一段最后到底由平台吃下,还要看紧接着
// 那次 model.ClaimSubscriptionWriteOff 有没有抢到本周期的核销名额 —— 抢不到的
// 那些笔扣的是**钱包**。早先闸门里就先把 shortfall 计进了免单量,于是管理端
// /admin/group-namespace/report 上的 shortfall_write_offs 虚高的部分恰好等于
// 已经向用户收到的钱(实测:真实核销 1900、钱包收了 1900,报表报 3800),
// 而并发越高、名额越早用完,虚高越多。它正是运营用来决定这条规则要不要继续开
// 的那个数字。
func TestTheGateItselfNeverCountsAWriteOff(t *testing.T) {
	newTestDB(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
	useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
	usePlanUnlock(t, func(int, string, bool) (bool, bool, bool) { return true, false, false })

	const key = "default→反重力的哈基米"
	before := ShortfallWriteOffs()[key]

	require.False(t, WalletMayCoverSubscriptionShortfall(7, "default", "反重力的哈基米", 4900),
		"判据本身没变:规则仍然说不许补收")
	assert.Equal(t, before, ShortfallWriteOffs()[key],
		"问一次闸门不等于免单了一笔 —— 名额抢不到时这一段是钱包出的")

	// 只有真的落定之后才算数。
	NoteShortfallWriteOff(7, "default", "反重力的哈基米", 4900)
	assert.Equal(t, before+4900, ShortfallWriteOffs()[key])
}

// TestShortfallWriteOffNeverFiresOnNonPositiveAmounts 守住退款方向。
//
// shortfall <= 0 意味着这一笔是**退款没能全额退进套餐**(例如套餐已被续期清零),
// 而不是"套餐没收够"。那笔钱当初未必是从钱包出的,往钱包里补就是凭空发钱;
// 反过来把它记成一次"核销"同样错 —— 平台什么都没让出去。
//
// 断言落在接缝被问的次数上:非正差额必须在问闸门之前就返回,连一次主库都不能查。
func TestShortfallWriteOffNeverFiresOnNonPositiveAmounts(t *testing.T) {
	newTestDB(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
	useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
	calls := 0
	usePlanUnlock(t, func(int, string, bool) (bool, bool, bool) {
		calls++
		return true, false, false
	})

	const key = "default→反重力的哈基米"
	before := ShortfallWriteOffs()[key]
	for _, shortfall := range []int64{0, -1, -4900} {
		assert.True(t, WalletMayCoverSubscriptionShortfall(7, "default", "反重力的哈基米", shortfall))
	}
	assert.Equal(t, 0, calls, "非正差额根本不该走到判据,更不该查主库")
	assert.Equal(t, before, ShortfallWriteOffs()[key], "退款方向不产生核销")
}
