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

// grouptier_current_test.go —— 「划转门槛按用户此刻的 users.group 分档,查不到走
// 全局兜底」这条简单规则的回归。
//
// 项目方原话:「关于划转,不是说不看套餐给的组,是以用户当前的用户组使用的对应
// 配置或兜底,不要搞那么复杂。」上一轮那层「基准用户组剥离」(名下还站着升组链
// 就取链根、已到期套餐的 downgrade_group 落点再取严)整体拆掉,本文件是它的替身。
//
// 分两层:
//
//	判据层  transferFor 的四态(没配 / 配了 / 显式 0 / 负数)与「停用 = 没配」;
//	接线层  从 create() 这个真正会动钱的入口打,断言到主库两行的 quota。
//
// 接线层不可省:纯函数写对了、create() 没接上,是本仓头号缺陷形状 —— 把 create()
// 里的 `settings.transferForSenderDay(senderNow.Group, ...)` 换成
// `settings.Transfer`,分档就在受理校验、新账号冻结、日额度、冷却、收款方闸门
// 五处全部失效,而判据层的用例仍然全绿。

// TestTierResolvesOnTheCurrentGroupWithGlobalFallback 钉住四态。
//
// 「没配」与「配成 0」必须是两件事,这是本模块用指针列承载三态的全部理由;
// 第四态(负数)只会由手工 UPDATE 落进库里,而它的方向是**放宽** ——
// risk.go 的守卫一律写成 `cfg.DailyMaxCount > 0`,一个 -1 会让那道闸门不成立。
func TestTierResolvesOnTheCurrentGroupWithGlobalFallback(t *testing.T) {
	global := tierGlobal() // 单笔 5000 万 / 日额 1 亿 / 日笔数 10 / 冷却 60

	t.Run("这一档没配:整份回落全局兜底", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: true, MaxPerTxQuota: i64(7_000),
		}).transferFor("someone-else")
		require.NoError(t, err)
		assert.Equal(t, global, cfg, "别人那一档一个字节都不该漏到这个分组上")
	})

	t.Run("站点一档都没配:回落全局兜底", func(t *testing.T) {
		cfg, err := tierSettings().transferFor("vip")
		require.NoError(t, err)
		assert.Equal(t, global, cfg)
	})

	t.Run("分组名为空串:回落全局兜底,不是报错", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: true, MaxPerTxQuota: i64(7_000),
		}).transferFor("")
		require.NoError(t, err)
		assert.Equal(t, global, cfg)
	})

	t.Run("这一档配了 n:用 n,没覆盖的项仍旧回落全局", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: true,
			MaxPerTxQuota: i64(7_000_000), DailyMaxCount: i64(3),
		}).transferFor("vip")
		require.NoError(t, err)
		assert.EqualValues(t, 7_000_000, cfg.MaxPerTxQuota)
		assert.Equal(t, 3, cfg.DailyMaxCount)
		assert.Equal(t, global.DailyMaxQuota, cfg.DailyMaxQuota, "没覆盖的项走兜底")
		assert.Equal(t, global.CooldownSecs, cfg.CooldownSecs)
	})

	t.Run("这一档显式配 0:这道闸门对这一档不设,不是回落全局", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: true,
			MinQuota: i64(0), MaxPerTxQuota: i64(0), DailyMaxQuota: i64(0),
			DailyMaxCount: i64(0), CooldownSecs: i64(0), ReceiverDailyMaxInCount: i64(0),
		}).transferFor("vip")
		require.NoError(t, err)
		assert.Zero(t, cfg.MaxPerTxQuota)
		assert.Zero(t, cfg.DailyMaxQuota)
		assert.Zero(t, cfg.DailyMaxCount)
		assert.Zero(t, cfg.CooldownSecs)
		assert.Zero(t, cfg.ReceiverDailyMaxInCount)
	})

	t.Run("这一档被停用:整档视同不存在,回落全局", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: false, MaxPerTxQuota: i64(7_000_000),
		}).transferFor("vip")
		require.NoError(t, err)
		assert.Equal(t, global, cfg)
	})

	// 负数只可能由手工 UPDATE 落进 qy_transfer_group_limits(写入侧 settingBounds
	// 的下界是 0)。**必须失败关闭**:每一个负数在判定端都会被
	// `cfg.X > 0` 那类守卫读成「这道闸门不设」,方向是放宽。
	negatives := []struct {
		name string
		row  GroupLimit
	}{
		{"日笔数 -1", GroupLimit{UserGroup: "vip", Enabled: true, DailyMaxCount: i64(-1)}},
		{"冷却 -1", GroupLimit{UserGroup: "vip", Enabled: true, CooldownSecs: i64(-1)}},
		{"收款方入账笔数 -1", GroupLimit{UserGroup: "vip", Enabled: true, ReceiverDailyMaxInCount: i64(-1)}},
		{"冻结期 -1", GroupLimit{UserGroup: "vip", Enabled: true, NewAccountFreezeHours: i64(-1)}},
		{"单笔上限 -1", GroupLimit{UserGroup: "vip", Enabled: true, MaxPerTxQuota: i64(-1)}},
		{"日额度 -1", GroupLimit{UserGroup: "vip", Enabled: true, DailyMaxQuota: i64(-1)}},
		{"超上界", GroupLimit{UserGroup: "vip", Enabled: true, DailyMaxCount: i64(100_001)}},
	}
	for _, tc := range negatives {
		t.Run("越界取值("+tc.name+"):这一档失败关闭", func(t *testing.T) {
			_, err := tierSettings(tc.row).transferFor("vip")
			require.Error(t, err)
			assert.Same(t, errGroupLimitInvalid, err)
			// 失败面只有这一档:别的分组照常。
			other, err := tierSettings(tc.row).transferFor("default")
			require.NoError(t, err)
			assert.Equal(t, global, other)
		})
	}
}

// TestCreateUsesTheGroupTheUserIsInRightNow 是接线层的主用例。
//
// 同一个人、同一笔金额,只有 users.group 不同,结论必须相反 ——
// 并且这两个人名下都**站着一条升组链**(seedStandingUpgrade),用来钉死
// 「套餐给的组照样算数」这条已经拍板的口径:上一轮的剥离层要是被谁加回来,
// 第二组断言会立刻翻面。
func TestCreateUsesTheGroupTheUserIsInRightNow(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	currentGroupTiers(t, gdb)

	// 1 号在紧档 lo,2 号收钱;3 号在松档 hi(而且是买来的)。
	seedMainUser(t, mainDB, 1, "lo", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)
	seedMainUser(t, mainDB, 3, "hi", 90_000_000)
	seedStandingUpgrade(t, mainDB, 1, 3, "hi", "lo")

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "cur-lo-1",
	})
	require.Error(t, err, "lo 那一档的单笔上限是 5000")
	assert.Same(t, errAmountOutOfRange, err)
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1), "被拒的划转不得动余额")
	assert.Equal(t, 0, quotaOf(t, mainDB, 2))

	require.NoError(t, callCreate(t, 3, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "cur-hi-1",
	}), "hi 那一档不设单笔上限 —— 哪怕这一档是买来的,项目方要的就是按当前分组算")
	assert.Equal(t, 30_000_000, quotaOf(t, mainDB, 3))
	assert.Equal(t, 60_000_000, quotaOf(t, mainDB, 2))

	// 今天的档位基准记的必须是**当前分组**(归一化后),否则明天之前的每一笔
	// 都会拿两个不同口径的名字去取严,结论毫无意义。
	var st UserState
	require.NoError(t, gdb.First(&st, "user_id = ?", 3).Error)
	assert.Equal(t, "hi", st.DayOutGroup)
}

// TestCreateFollowsAGroupChangeImmediately —— 换组立刻生效(当天还没转出过时)。
//
// 这是简单规则里最容易被缓存或快照悄悄破坏的一条:门槛快照每次 create() 现取,
// 分组每次 create() 现读。中间不许有任何"这个人属于哪一档"的记忆。
func TestCreateFollowsAGroupChangeImmediately(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	currentGroupTiers(t, gdb)
	seedMainUser(t, mainDB, 1, "lo", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "switch-1",
	})
	require.Error(t, err)
	assert.Same(t, errAmountOutOfRange, err)

	// 换到松档。当天一笔都还没转出去 ⇒ day_out_group 为空 ⇒ 新档完全生效。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
		Update("group", "hi").Error)
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "switch-2",
	}), "换组必须立刻生效,不许有任何分档记忆")
	assert.Equal(t, 30_000_000, quotaOf(t, mainDB, 1))

	// 反向:掉回紧档,同样立刻生效(收紧方向永远即时)。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
		Update("group", "lo").Error)
	err = callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "switch-3",
	})
	require.Error(t, err)
	assert.Same(t, errAmountOutOfRange, err)
	assert.Equal(t, 30_000_000, quotaOf(t, mainDB, 1), "被拒的那两笔一分未动")
}

// TestCreateFallsBackToGlobalWhenTheGroupHasNoTier —— 「没配走兜底」的接线层。
//
// 用户坐在一个**站点根本没有为它配过门槛**的分组里,判定必须落到全局兜底那一份,
// 而不是落到别人那一档、更不是报错。
func TestCreateFallsBackToGlobalWhenTheGroupHasNoTier(t *testing.T) {
	global := createGlobal()
	global.MaxPerTxQuota = 20_000_000 // 全局兜底:单笔 2000 万
	gdb, mainDB := createEnv(t, global)
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	currentGroupTiers(t, gdb)
	seedMainUser(t, mainDB, 1, "nobody-configured-this", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "fallback-1",
	})
	require.Error(t, err, "全局兜底的单笔上限是 2000 万")
	assert.Same(t, errAmountOutOfRange, err)

	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 10_000_000, Confirm: true, ClientRequestId: "fallback-2",
	}), "兜底之内的金额必须照常放行,不能被别人那一档的 5000 压着")
	assert.Equal(t, 80_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 10_000_000, quotaOf(t, mainDB, 2))
}

// TestReceiverGateResolvesOnTheReceiversCurrentGroup ——
// receiver_daily_max_in_count 按**收款方**此刻的分组解析,不是发起方那一档。
//
// 按发起方解析等于把闸门的上限交给被它约束的那一方去挑:一堆 default 小号往一个
// 配了「每天 1 笔」的汇集账号转钱,每一笔都按 default 那一档放行,而这道闸门
// 存在的唯一理由正是拦这件事。
func TestReceiverGateResolvesOnTheReceiversCurrentGroup(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	seedMainUser(t, mainDB, 1, "default", 90_000_000)
	seedMainUser(t, mainDB, 2, "vip", 0)
	seedUserState(t, gdb, 2, 1) // 汇集账号今天已经收过 1 笔

	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "vip", Enabled: true, ReceiverDailyMaxInCount: i64(1),
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	invalidateSettings()

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "recv-cur-1",
	})
	require.Error(t, err)
	assert.Same(t, errReceiverDailyInExceeded, err,
		"按发起方(default)那一档解析会拿到全站的 20 并放行")
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 0, quotaOf(t, mainDB, 2))

	// 对照:收款方换到一个没配这一项的分组 ⇒ 回落全局的 20 ⇒ 放行。
	// 少了这一组,上面那条断言可能只是因为"凡是收款方有状态行就拒"而通过。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 2).
		Update("group", "default").Error)
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "recv-cur-2",
	}))
	assert.Equal(t, 89_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 1_000_000, quotaOf(t, mainDB, 2))
}

// TestLimitsEchoesTheTierCreateWillEnforce 钉住回显与判定同源。
//
// 上一轮出过「两个口径混在一份响应里」的报告,所以这一条不只断言数字,
// 而是**同一套种子数据下先看回显、再打 create()**,两边必须给出同一个结论。
func TestLimitsEchoesTheTierCreateWillEnforce(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	currentGroupTiers(t, gdb)
	// callTransferHandler 固定以用户 1 的身份调用。
	seedMainUser(t, mainDB, 1, "lo", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)

	echoed := readLimits(t)
	assert.EqualValues(t, 5_000, echoed.MaxPerTxQuota, "回显必须是 lo 那一档")
	assert.EqualValues(t, 10_000, echoed.DailyMaxQuota)
	assert.Equal(t, 2, echoed.DailyMaxCount)

	// 回显说 5000 是上限 ⇒ 5001 必须真的被拒,5000 必须真的能过。
	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: echoed.MaxPerTxQuota + 1, Confirm: true, ClientRequestId: "echo-1",
	})
	require.Error(t, err)
	assert.Same(t, errAmountOutOfRange, err)
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: echoed.MaxPerTxQuota, Confirm: true, ClientRequestId: "echo-2",
	}), "回显给出的上限必须是真的能转过去的那个数")

	// 换到松档 hi 之后再看一次。
	//
	// 今天已经在 lo 下转过一笔了 ⇒ 当日取严那一位仍然作数 ⇒ 回显的还是 lo 那一档。
	// 这是这条简单规则下唯一一处"换组不立刻完全生效"的地方(见
	// transferForSenderDay),而回显与 create() 同源意味着这个**不那么好看**的
	// 结论也必须如实显示出来 —— 显示成 hi 的"不限"才是真正的坑。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
		Update("group", "hi").Error)
	after := readLimits(t)
	assert.EqualValues(t, 5_000, after.MaxPerTxQuota, "今天的计数是在 lo 下累的")
	assert.EqualValues(t, 10_000, after.DailyMaxQuota)
	assert.Equal(t, 2, after.DailyMaxCount)
	assert.EqualValues(t, 5_000, after.RemainingDailyQuota, "10000 - 今天已用的 5000")
	assert.Equal(t, 1, after.RemainingDailyCount)

	// 同源验证:回显说还剩 1 笔 5000 ⇒ 这一笔必须真的转得出去,再下一笔必须被拒。
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: after.RemainingDailyQuota, Confirm: true, ClientRequestId: "echo-3",
	}))
	err = callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000, Confirm: true, ClientRequestId: "echo-4",
	})
	require.Error(t, err)
	assert.Same(t, errDailyCountExceeded, err, "回显说只剩 1 笔,第 2 笔就该被拒")
	assert.Equal(t, 10_000, quotaOf(t, mainDB, 2), "收款方拿到的正是回显承诺的那两笔")
}

// TestGroupLimitRowMatchesTransferFor —— 管理端列表与判定端同源。
//
// 两份合并逻辑漂移的表现是「列表说这一档冻结期是 0、用户提交时按全局的 24 小时
// 被拒」,运营在界面上看不到任何异常。把 buildGroupLimitRow 里的 mergeTier 换回
// 自己摊开的那个循环(不带 tightenOnlyKeys),第一组断言立刻翻面。
func TestGroupLimitRowMatchesTransferFor(t *testing.T) {
	cases := []struct {
		name string
		row  GroupLimit
	}{
		{"想把冻结期放宽到 0:被顶回全局,来源也必须写 global", GroupLimit{
			UserGroup: "vip", Enabled: true, NewAccountFreezeHours: i64(0)}},
		{"把冻结期收紧到 720:照常生效,来源是 group", GroupLimit{
			UserGroup: "vip", Enabled: true, NewAccountFreezeHours: i64(720)}},
		{"额度型显式 0:照常生效", GroupLimit{
			UserGroup: "vip", Enabled: true, MaxPerTxQuota: i64(0), DailyMaxCount: i64(0)}},
		{"一项都没覆盖:全部走兜底", GroupLimit{UserGroup: "vip", Enabled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tierSettings(tc.row)
			want, err := s.transferFor("vip")
			require.NoError(t, err)
			got := buildGroupLimitRow(s, tc.row)
			require.True(t, got.Valid)
			for _, key := range tierableKeys {
				w, _ := transferSettingValue(want, key)
				assert.Equal(t, w, got.Effective[key], "%s 的生效值两端必须一致", key)
				src := tierSourceGlobal
				if g, _ := transferSettingValue(s.Transfer, key); w != g {
					src = tierSourceGroup
				}
				assert.Equal(t, src, got.Sources[key], "%s 的来源必须说实话", key)
			}
		})
	}

	t.Run("越界取值:列表必须标成 invalid,不能显示一份不会生效的组合", func(t *testing.T) {
		row := GroupLimit{UserGroup: "vip", Enabled: true, DailyMaxCount: i64(-1)}
		s := tierSettings(row)
		_, err := s.transferFor("vip")
		require.Same(t, errGroupLimitInvalid, err)
		assert.False(t, buildGroupLimitRow(s, row).Valid)
	})
}

// currentGroupTiers 建起本文件接线层共用的两档门槛:
//
//	lo  紧档:单笔 5000、日额 10000、日笔数 2
//	hi  松档:五项全 0(一道都不设)
//
// 两档的差距必须大到"按哪一档算"只能有一种结论 —— 6000 万在 lo 下是硬拒,
// 在 hi 下是照常放行。
func currentGroupTiers(t *testing.T, gdb *gorm.DB) {
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

// seedStandingUpgrade 落一条**还站着**的升组订阅。
//
// 存在理由只有一个:证明它现在**不影响**门槛解析。上一轮的剥离层正是靠这几列
// 把 users.group 换成 prev_user_group 的,留着这个种子,剥离层一旦被加回来,
// TestCreateUsesTheGroupTheUserIsInRightNow 的第二组断言就会红。
func seedStandingUpgrade(t *testing.T, gdb *gorm.DB, id, userId int, upgrade, prev string) {
	t.Helper()
	require.NoError(t, gdb.AutoMigrate(&model.UserSubscription{}))
	require.NoError(t, gdb.Create(&model.UserSubscription{
		Id: id, UserId: userId, Status: "active",
		UpgradeGroup: upgrade, PrevUserGroup: prev,
	}).Error)
}

type limitsEcho struct {
	MinQuota            int64 `json:"min_quota"`
	MaxPerTxQuota       int64 `json:"max_per_tx_quota"`
	DailyMaxQuota       int64 `json:"daily_max_quota"`
	DailyMaxCount       int   `json:"daily_max_count"`
	RemainingDailyQuota int64 `json:"remaining_daily_quota"`
	RemainingDailyCount int   `json:"remaining_daily_count"`
}

func readLimits(t *testing.T) limitsEcho {
	t.Helper()
	rec := callTransferHandler(t, http.MethodGet, "/api/qy/transfer/limits", "", handleGetLimits)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Data limitsEcho `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &got))
	return got.Data
}
