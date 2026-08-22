package transfer

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// basegroup_test.go —— 「划转门槛只认用户组,套餐不许影响」的回归。
//
// 分两层:
//   - 判据层:model.QyBaseUserGroup 的四条边界(管理员手工改组 / 套餐到期回退 /
//     多张升组套餐 / 根本没有套餐);
//   - 接线层:两条上一轮实测跑通的攻击路径,从 create() 这个真正会动钱的入口
//     再打一遍,必须失败,并且**余额一分未动**。
//
// 接线层不可省:纯函数写对了、create() 没接上,是本仓头号缺陷形状 ——
// 把 create() 里的 baseUserGroup(senderNow) 改回 senderNow.Group,分档就在受理
// 校验、新账号冻结、日额度、冷却四处全部按付费档放行,而判据层的用例仍然全绿。

// seedSubscription 落一条升组订阅。
//
// 只填链根判定真正看的四列(user_id / status / upgrade_group / prev_user_group)。
// 其余列留零值是刻意的:判据不该依赖 end_time、amount_total 这些与"这条链站不站着"
// 无关的字段,一旦哪天依赖了,这里的零值会让它立刻显形。
func seedSubscription(t *testing.T, gdb *gorm.DB, id, userId int, status, upgrade, prev string) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.UserSubscription{
		Id: id, UserId: userId, PlanId: 1,
		Status: status, UpgradeGroup: upgrade, PrevUserGroup: prev,
	}).Error)
}

func TestBaseUserGroupStripsPlanGrantedGroups(t *testing.T) {
	mainDB := newMainDB(t)

	t.Run("根本没有套餐:原样返回 users.group —— 绝大多数人的行为一个字节不变", func(t *testing.T) {
		got, err := model.QyBaseUserGroup(11, "default")
		require.NoError(t, err)
		assert.Equal(t, "default", got)
	})

	t.Run("一张活着的升组套餐:剥回链根", func(t *testing.T) {
		seedSubscription(t, mainDB, 1, 21, "active", "vip", "default")
		got, err := model.QyBaseUserGroup(21, "vip")
		require.NoError(t, err)
		assert.Equal(t, "default", got, "vip 是买来的,门槛必须按买之前那一档算")
	})

	t.Run("套餐到期回退:那一行不再站着,基准组回到 users.group", func(t *testing.T) {
		// 到期扫描会把 status 打成 expired 并把人放回 default;
		// 若套餐配了 downgrade_group,users.group 就是运营指定的落点。
		seedSubscription(t, mainDB, 2, 22, "expired", "vip", "default")
		got, err := model.QyBaseUserGroup(22, "silver")
		require.NoError(t, err)
		assert.Equal(t, "silver", got,
			"到期之后人现在属于哪一组是运营(downgrade_group / prev)决定的,那就是他的用户组")
	})

	t.Run("多张升组套餐:取 id 最小的链根,答案唯一", func(t *testing.T) {
		// 跨组顶替:default → VIP(被顶掉,superseded,upgrade_group 已摘空)→ GOLD。
		// 两环携带同一个根,读哪一环都得到 default。
		seedSubscription(t, mainDB, 3, 23, model.SubscriptionStatusSuperseded, "", "default")
		seedSubscription(t, mainDB, 4, 23, "active", "gold", "default")
		got, err := model.QyBaseUserGroup(23, "gold")
		require.NoError(t, err)
		assert.Equal(t, "default", got)

		// 对照:把 superseded 那一环的链根写成付费组(升级之前落库的历史行形状),
		// 读到的就是那个付费组 —— 这是**已知残余**,由 day_out_group 取严与
		// tightenOnlyKeys 兜着,不是本函数能修的。写成用例是为了让它显性存在,
		// 将来谁去回填历史行,这一条会立刻告诉他口径变了。
		require.NoError(t, mainDB.Model(&model.UserSubscription{}).Where("id = ?", 3).
			Update("prev_user_group", "vip").Error)
		got, err = model.QyBaseUserGroup(23, "gold")
		require.NoError(t, err)
		assert.Equal(t, "vip", got)
	})

	t.Run("管理员手工改组:三个快照被摘空,链随之作废,管理员说了算", func(t *testing.T) {
		seedSubscription(t, mainDB, 5, 24, "active", "vip", "default")
		require.NoError(t, mainDB.Transaction(func(tx *gorm.DB) error {
			_, err := model.DetachUserGroupSubscriptionsTx(tx, 24)
			return err
		}))
		got, err := model.QyBaseUserGroup(24, "gold")
		require.NoError(t, err)
		assert.Equal(t, "gold", got, "运营决定就是用户组本身,不该被一条已被摘掉的订阅推翻")
	})

	t.Run("读失败必须如实上报,绝不回落 users.group", func(t *testing.T) {
		require.NoError(t, mainDB.Migrator().DropTable(&model.UserSubscription{}))
		t.Cleanup(func() {
			require.NoError(t, mainDB.AutoMigrate(&model.UserSubscription{}))
		})
		_, err := model.QyBaseUserGroup(21, "vip")
		require.Error(t, err, "回落 users.group 拿到的正是套餐给的付费组,等于把剥离静默关掉")
	})
}

// baseGroupTiers 建起本文件接线层共用的两档门槛:
//
//	lo  基准档:单笔 5000、日额 10000、日笔数 2
//	hi  买来的那一档:五项全 0(一道都不设)
//
// 两档的差距必须大到"按哪一档算"只能有一种结论 —— 6000 万在 lo 下是硬拒,
// 在 hi 下是照常放行。
func baseGroupTiers(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "lo", Enabled: true,
		MinQuota: i64(1_000), MaxPerTxQuota: i64(5_000),
		DailyMaxQuota: i64(10_000), DailyMaxCount: i64(2),
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "hi", Enabled: true,
		MinQuota: i64(0), MaxPerTxQuota: i64(0),
		DailyMaxQuota: i64(0), DailyMaxCount: i64(0),
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	invalidateSettings()
}

// 攻击路径 ①(上一轮实测):当天额度用满 → 花 $0.01 买一档 → 同一秒转 6000 万。
//
// 与 TestCreateKeepsTodaysTierAfterASelfServeGroupUpgrade 的区别是本用例
// **刻意不给发起方任何当日计数**:day_out_group 那道闸门在这里是空的,
// 拦住这一笔的只能是"门槛认基准用户组"这条更根本的规则。
// 两条用例合起来才说明两道闸门各自独立作数。
func TestCreateResolvesTierOnTheBaseGroupNotThePlanGrantedGroup(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	// 发起方**此刻**在 users.group = "hi" 里,因为他刚买了一个升组套餐。
	seedMainUser(t, mainDB, 1, "hi", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)
	seedSubscription(t, mainDB, 1, 1, "active", "hi", "lo")
	baseGroupTiers(t, gdb)

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "base-tier-1",
	})
	require.Error(t, err, "买来的那一档不该参与门槛解析")
	assert.Same(t, errAmountOutOfRange, err,
		"门槛必须按链根 lo 的单笔上限 5000 算;读 users.group 会拿到 hi 的“不设上限”并放行这一笔")
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1), "被拒的划转不得动余额")
	assert.Equal(t, 0, quotaOf(t, mainDB, 2))

	// 日笔数同样按 lo 那一档:金额降到 lo 档内可以过,第三笔就该被拒。
	for i, key := range []string{"base-tier-2", "base-tier-3"} {
		require.NoError(t, callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 4_000, Confirm: true, ClientRequestId: key,
		}), "lo 档内的第 %d 笔应当放行", i+1)
	}
	err = callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 4_000, Confirm: true, ClientRequestId: "base-tier-4",
	})
	require.Error(t, err)
	assert.Same(t, errDailyCountExceeded, err, "日笔数必须按链根 lo 的 2 笔算")
	assert.Equal(t, 90_000_000-8_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 8_000, quotaOf(t, mainDB, 2))

	// 今天的档位基准必须记成**基准用户组**,不能记 users.group。
	// 记错了,transferForSenderDay 明天……不,今天下一笔就会拿"lo 的当前档"
	// 去和"hi 这个基准"取严 —— 两个不同口径的名字相比,取严的结论毫无意义。
	var st UserState
	require.NoError(t, gdb.First(&st, "user_id = ?", 1).Error)
	assert.Equal(t, "lo", st.DayOutGroup,
		"day_out_group 必须与门槛解析用的是同一个分组口径")
}

// 对照组:同一个人、同样在 users.group = "hi" 里,但那一档**不是买来的**
// (名下没有任何站着的升组链)⇒ hi 照常生效。
//
// 少了这一组,上面那条断言可能只是因为"凡是 hi 一律拒"而通过。
func TestCreateHonoursAGroupTheUserGenuinelyBelongsTo(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	seedMainUser(t, mainDB, 1, "hi", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)
	// 管理员手工把他放进 hi(摘干净的订阅在库里长这样:三个快照全空)。
	seedSubscription(t, mainDB, 1, 1, "active", "", "")
	baseGroupTiers(t, gdb)

	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "base-control-1",
	}), "没有站着的升组链时 users.group 就是他的用户组,hi 那一档必须照常生效")
	assert.Equal(t, 30_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 60_000_000, quotaOf(t, mainDB, 2))
}

// 攻击路径 ②(上一轮实测):注册几百秒的号被新账号冻结期挡住 → 买同一个套餐 → 放行。
//
// 本用例把**全局** new_account_freeze_hours 设成 0,于是 tightenOnlyKeys
// (「分档只许把冻结期收紧」)在这里退化成空操作 —— 拦住这一笔的只能是
// "门槛认基准用户组"。这样两道闸门的功劳不会互相冒领。
func TestNewAccountFreezeSurvivesASelfServeGroupUpgrade(t *testing.T) {
	global := createGlobal()
	global.NewAccountFreezeHours = 0 // 让 tightenOnlyKeys 无事可做
	gdb, mainDB := createEnv(t, global)
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))

	now := common.GetTimestamp()
	seedMainUser(t, mainDB, 1, "hi", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)
	// 注册 496 秒(实测那条链里的数字)。GORM 的 BeforeCreate 会盖上当前时间,
	// 所以这一列必须显式改回去。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
		Update("created_at", now-496).Error)
	seedSubscription(t, mainDB, 1, 1, "active", "hi", "lo")

	// lo(链根)配 720 小时冻结期;hi(买来的)显式配 0。
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "lo", Enabled: true, NewAccountFreezeHours: i64(720),
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "hi", Enabled: true, NewAccountFreezeHours: i64(0),
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	invalidateSettings()

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "freeze-bypass-1",
	})
	require.Error(t, err)
	assert.Same(t, errAccountTooNew, err,
		"冻结期必须按链根 lo 的 720 小时算;读 users.group 会拿到 hi 的 0 并立刻放行")
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 0, quotaOf(t, mainDB, 2))

	// 对照:把那条升组订阅摘掉(等同管理员手工把他放进 hi)⇒ hi 的 0 生效。
	require.NoError(t, mainDB.Transaction(func(tx *gorm.DB) error {
		_, e := model.DetachUserGroupSubscriptionsTx(tx, 1)
		return e
	}))
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "freeze-bypass-2",
	}), "运营亲手放进 hi 的账号就该按 hi 那一档,否则这一档等于配不出来")
	assert.Equal(t, 89_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 1_000_000, quotaOf(t, mainDB, 2))
}

// 收款方口径的闸门同样要剥离:不剥的话,攻击者只要给汇集账号买一档
// receiver_daily_max_in_count 最松的套餐,这道闸门就等于不存在 ——
// 而它存在的唯一理由就是拦「一堆小号汇集到同一账号」。
func TestReceiverGateResolvesOnTheReceiverBaseGroup(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	seedMainUser(t, mainDB, 1, "default", 90_000_000)
	// 汇集账号买了一档「每天不限入账笔数」的套餐,链根是 vip(每天 1 笔)。
	seedMainUser(t, mainDB, 2, "unlimited", 0)
	seedSubscription(t, mainDB, 1, 2, "active", "unlimited", "vip")
	seedUserState(t, gdb, 2, 1) // 今天已经收过 1 笔

	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "vip", Enabled: true, ReceiverDailyMaxInCount: i64(1),
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "unlimited", Enabled: true, ReceiverDailyMaxInCount: i64(0),
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	invalidateSettings()

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "recv-base-1",
	})
	require.Error(t, err)
	assert.Same(t, errReceiverDailyInExceeded, err,
		"收款方那一档必须按它的基准用户组解析;读 users.group 会拿到买来的“不限”并放行")
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 0, quotaOf(t, mainDB, 2))
}

// TestLimitsEchoesTheBaseGroupTier 钉住回显与判定同口径。
//
// 断链的表现极其具体:handleGetLimits 只要还按 users.group 解析,刚买完套餐的人
// 就会在界面上看到「不限单笔」,提交时按链根那一档被拒 —— 而他看不出问题出在哪。
func TestLimitsEchoesTheBaseGroupTier(t *testing.T) {
	gdb := newSettingsTestDB(t)
	mainDB := newMainDB(t)
	useSettingsConfig(t, createGlobal())
	// callTransferHandler 固定以用户 1 的身份调用。
	seedMainUser(t, mainDB, 1, "hi", 90_000_000)
	seedSubscription(t, mainDB, 1, 1, "active", "hi", "lo")
	baseGroupTiers(t, gdb)

	rec := callTransferHandler(t, http.MethodGet, "/api/qy/transfer/limits", "", handleGetLimits)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got struct {
		Data struct {
			MaxPerTxQuota int64 `json:"max_per_tx_quota"`
			DailyMaxQuota int64 `json:"daily_max_quota"`
			DailyMaxCount int   `json:"daily_max_count"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &got))
	assert.EqualValues(t, 5_000, got.Data.MaxPerTxQuota,
		"回显必须是链根 lo 那一档;按 users.group 解析会回显 hi 的“不限”")
	assert.EqualValues(t, 10_000, got.Data.DailyMaxQuota)
	assert.Equal(t, 2, got.Data.DailyMaxCount)
}

// TestLimitsEchoesTodaysTighterTier 是回显链路上的第二半:当日取严。
//
// 用户的基准组是松档 hi(名下没有任何升组链),但**今天的计数是在紧档 lo 下累的**
// —— create() 会按两档取严,回显必须给出同一个答案。少了这一段,用户会看到
// 「还能转 6000 万」,提交时被 lo 的 5000 拒掉。
func TestLimitsEchoesTodaysTighterTier(t *testing.T) {
	gdb := newSettingsTestDB(t)
	mainDB := newMainDB(t)
	useSettingsConfig(t, createGlobal())
	seedMainUser(t, mainDB, 1, "hi", 90_000_000)
	baseGroupTiers(t, gdb)
	require.NoError(t, gdb.Create(&UserState{
		UserId: 1, DayBucket: dayBucket(common.GetTimestamp()),
		DayOutGroup: "lo", DayOutCount: 1, DayOutQuota: 4_000,
		UpdatedAt: common.GetTimestamp(),
	}).Error)

	rec := callTransferHandler(t, http.MethodGet, "/api/qy/transfer/limits", "", handleGetLimits)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got struct {
		Data struct {
			MaxPerTxQuota       int64 `json:"max_per_tx_quota"`
			DailyMaxCount       int   `json:"daily_max_count"`
			RemainingDailyQuota int64 `json:"remaining_daily_quota"`
			RemainingDailyCount int   `json:"remaining_daily_count"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &got))
	assert.EqualValues(t, 5_000, got.Data.MaxPerTxQuota, "回显必须与 create() 一样按两档取严")
	assert.Equal(t, 2, got.Data.DailyMaxCount)
	assert.EqualValues(t, 6_000, got.Data.RemainingDailyQuota, "10000 - 今天已用的 4000")
	assert.Equal(t, 1, got.Data.RemainingDailyCount)
}

// seedExpiredWithDowngrade 落一条**已到期**、带显式降级落点的升组订阅。
//
// 这正是 downgrade_group 那条绕行路径在库里留下的形状:链已经全部 expired
// (于是 standingUpgradeChainRootTx 一条都查不到),而 users.group 被到期回退
// 写成了运营在套餐上填的那个落点。
func seedExpiredWithDowngrade(t *testing.T, gdb *gorm.DB, id, userId int, upgrade, prev, downgrade string) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.UserSubscription{
		Id: id, UserId: userId, PlanId: 1,
		Status: "expired", UpgradeGroup: upgrade,
		PrevUserGroup: prev, DowngradeGroup: downgrade,
	}).Error)
}

// TestBaseUserGroupsKeepsThePrePlanTierAfterAnExplicitDowngrade 守 downgrade_group
// 这条**慢一个套餐周期**的绕行路径。
//
// 剥离"还站着的链"只堵掉了「买了立刻用」。到期之后那一行是 expired、链不再站着,
// 而 users.group 已经被到期回退写成了运营在套餐上填的落点 —— 用户从此永久坐在
// 一个他从来没有过的档上,而这一档可以比他原来那一档松几个数量级。
//
// 备份库实测:qy-dz-lo(单笔 5000 / 日额 10000 / 日笔数 3)的用户买一张
// downgrade_group='default' 的 60 秒套餐,到期后 /limits 变成
// 50000000/200000000/20 —— 单笔上限 8000 倍。成本是套餐价 + 一个套餐周期。
//
// 取严而不是"直接用链根":运营把落点配成一个**更严**的档(到期后关小黑屋)
// 同样是正当配置,只认链根会把那种降级也一起撤销掉。
func TestBaseUserGroupsKeepsThePrePlanTierAfterAnExplicitDowngrade(t *testing.T) {
	mainDB := newMainDB(t)

	t.Run("坐在到期套餐的降级落点上:链根一并作数", func(t *testing.T) {
		seedExpiredWithDowngrade(t, mainDB, 31, 41, "hi", "lo", "default")
		got, err := model.QyBaseUserGroups(41, "default")
		require.NoError(t, err)
		assert.Equal(t, []string{"default", "lo"}, got,
			"default 是套餐留下的落点,不是他自己的身份;买之前那一档必须继续作数")
	})

	t.Run("没填 downgrade_group:行为一个字节不变", func(t *testing.T) {
		// 到期回落到 prev_user_group,users.group 本来就等于链根。
		seedExpiredWithDowngrade(t, mainDB, 32, 42, "hi", "lo", "")
		got, err := model.QyBaseUserGroups(42, "lo")
		require.NoError(t, err)
		assert.Equal(t, []string{"lo"}, got)
	})

	t.Run("落点与链根相同:不重复列出", func(t *testing.T) {
		seedExpiredWithDowngrade(t, mainDB, 33, 43, "hi", "lo", "lo")
		got, err := model.QyBaseUserGroups(43, "lo")
		require.NoError(t, err)
		assert.Equal(t, []string{"lo"}, got)
	})

	t.Run("管理员后来把他挪走:落点判据不成立,管理员说了算", func(t *testing.T) {
		seedExpiredWithDowngrade(t, mainDB, 34, 44, "hi", "lo", "default")
		// 管理员改组走 Detach,三个快照一起清空。
		require.NoError(t, mainDB.Transaction(func(tx *gorm.DB) error {
			_, err := model.DetachUserGroupSubscriptionsTx(tx, 44)
			return err
		}))
		got, err := model.QyBaseUserGroups(44, "gold")
		require.NoError(t, err)
		assert.Equal(t, []string{"gold"}, got)
	})

	t.Run("当前分组不是任何到期套餐的落点:不许把链根拖回来", func(t *testing.T) {
		// 这一条守的是**过度收紧**的方向。判据必须是「users.group 恰好等于某条
		// expired 行的 downgrade_group」;少了这个前提,任何买过一次套餐的人都会被
		// 永久钉在他最早那一档 —— 包括管理员后来正当挪过组的人,而那正是
		// 「管理员说了算」这条边界要保住的东西。
		seedExpiredWithDowngrade(t, mainDB, 35, 46, "hi", "lo", "")
		got, err := model.QyBaseUserGroups(46, "silver")
		require.NoError(t, err)
		assert.Equal(t, []string{"silver"}, got,
			"silver 不是任何一张到期套餐的降级落点,链根 lo 不该被拖回来")
	})

	t.Run("根本没有套餐:只有 users.group", func(t *testing.T) {
		got, err := model.QyBaseUserGroups(45, "default")
		require.NoError(t, err)
		assert.Equal(t, []string{"default"}, got)
	})

	t.Run("读失败如实上报", func(t *testing.T) {
		require.NoError(t, mainDB.Migrator().DropTable(&model.UserSubscription{}))
		t.Cleanup(func() {
			require.NoError(t, mainDB.AutoMigrate(&model.UserSubscription{}))
		})
		_, err := model.QyBaseUserGroups(41, "default")
		require.Error(t, err)
	})
}

// TestCreateRefusesTheTierBoughtViaAnExpiredPlansDowngrade 是上面那条的接线层:
// 从真正会动钱的 create() 入口再打一遍。
//
// 判据层写对了、create() 没接上,是本仓头号缺陷形状 —— 把 create() 里的
// transferForBase 换回 transferFor(groups[0]),下面这一笔 6000 万立刻放行,
// 而判据层的用例仍然全绿。
func TestCreateRefusesTheTierBoughtViaAnExpiredPlansDowngrade(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	// 他此刻在 hi 里,而 hi 是**已到期套餐的降级落点**,不是他自己的档。
	seedMainUser(t, mainDB, 1, "hi", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)
	seedExpiredWithDowngrade(t, mainDB, 51, 1, "gold", "lo", "hi")
	baseGroupTiers(t, gdb)

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "dg-tier-1",
	})
	require.Error(t, err, "到期落点不该把 lo 那一档的单笔上限抹掉")
	assert.Same(t, errAmountOutOfRange, err)
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1), "被拒的划转不得动余额")
	assert.Equal(t, 0, quotaOf(t, mainDB, 2))

	// lo 档之内照常放行 —— 不是"凡是这个人一律拒"。
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 4_000, Confirm: true, ClientRequestId: "dg-tier-2",
	}))
	assert.Equal(t, 90_000_000-4_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 4_000, quotaOf(t, mainDB, 2))
}

// TestCreateStillHonoursAStricterDowngradeLanding 是取严方向的反向用例。
//
// 运营把落点配成一个**更严**的档(到期之后关进小黑屋)同样是正当配置。
// 只认链根会把这种降级一起撤销掉 —— 那是把一道运营刚刚落下的闸门抬起来。
func TestCreateStillHonoursAStricterDowngradeLanding(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	// 买之前是 hi(不设上限),到期后运营把他放进 lo(单笔 5000)。
	seedMainUser(t, mainDB, 1, "lo", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)
	seedExpiredWithDowngrade(t, mainDB, 61, 1, "gold", "hi", "lo")
	baseGroupTiers(t, gdb)

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "dg-strict-1",
	})
	require.Error(t, err, "运营配的更严的落点必须作数,不能被链根撤销")
	assert.Same(t, errAmountOutOfRange, err)
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1))
}
