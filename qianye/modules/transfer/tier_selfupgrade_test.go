package transfer

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tier_selfupgrade_test.go —— 「分档可以被用户自己买松」的两个缺口。
//
// 背景:分档按此刻的 users.group 解析,而 users.group 是用户自己花钱就能改的 ——
// 任何一个 upgrade_group 指向更松档、且允许余额支付的套餐(这正是用户组商品的
// 设计用途)都能改它。实测:
//
//	① 当天日额度/日笔数用满 → 花 0.01 美元买一个升组套餐 → 同一秒转 6000 万,
//	   而 qy_transfer_user_state 里当天的计数一个都不重置,只是上限换了一档;
//	② 注册 9 秒的新号被 new_account_freeze_hours=720 挡住 → 买同一个套餐 →
//	   换进一个该项为 0 的档 → 立刻放行,users.created_at 一个字节没变。
//
// 两处修法不同,因为两者卖的东西不同:
//
//	额度型门槛(单笔/日额度/日笔数/冷却/收款方入账笔数)分档放宽**就是**这个
//	功能的产品形态,不该禁;要挡的是「当天用满之后再换档」,所以按
//	「今天的计数是在哪一档下累起来的」取严。
//
//	新账号冻结期是一道反滥用身份闸门,它一旦能被分档放宽就等于能被一次购买
//	抹掉,所以它只许分档收紧(tightenOnlyKeys)。

func TestNewAccountFreezeCanOnlyBeTightenedByATier(t *testing.T) {
	global := tierGlobal() // NewAccountFreezeHours = 24

	cases := []struct {
		name     string
		override *int64
		want     int
	}{
		{"分档没覆盖:回落全局", nil, 24},
		{"分档想放宽到 0(=买一档不设冻结期):被顶回全局", i64(0), 24},
		{"分档想放宽到 1 小时:被顶回全局", i64(1), 24},
		{"分档收紧到 720 小时:照常生效", i64(720), 720},
		{"分档与全局相等:原样", i64(24), 24},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := tierSettings(GroupLimit{
				UserGroup: "vip", Enabled: true, NewAccountFreezeHours: tc.override,
			}).transferFor("vip")
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.NewAccountFreezeHours)
			// 反向:别的项一个都不能被这条「只许收紧」的规则连坐。
			assert.Equal(t, global.MinQuota, cfg.MinQuota)
			assert.Equal(t, global.MaxPerTxQuota, cfg.MaxPerTxQuota)
		})
	}

	t.Run("额度型门槛仍然可以被分档放宽 —— 那是这个功能要卖的东西", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: true,
			MaxPerTxQuota: i64(0), DailyMaxQuota: i64(0), DailyMaxCount: i64(0),
			CooldownSecs: i64(0), ReceiverDailyMaxInCount: i64(0),
		}).transferFor("vip")
		require.NoError(t, err)
		assert.Zero(t, cfg.MaxPerTxQuota, "0 = 这一档不设单笔上限")
		assert.Zero(t, cfg.DailyMaxQuota)
		assert.Zero(t, cfg.DailyMaxCount)
		assert.Zero(t, cfg.CooldownSecs)
		assert.Zero(t, cfg.ReceiverDailyMaxInCount)
	})
}

// stricterSetting 的两类语义:0 在上限型里是「不限」,不是「最严」。
func TestStricterSettingTreatsZeroAsUnlimitedOnCeilings(t *testing.T) {
	cases := []struct {
		key  string
		a, b int64
		want int64
	}{
		// 下限型:越大越严。
		{keyMinQuota, 1000, 5000, 5000},
		{keyCooldownSecs, 0, 60, 60},
		{keyNewAccountFreezeHours, 720, 0, 720},
		// 上限型:越小越严,但 0 = 不限,不能被当成最严。
		{keyMaxPerTxQuota, 0, 5000, 5000},
		{keyMaxPerTxQuota, 5000, 0, 5000},
		{keyMaxPerTxQuota, 5000, 60000000, 5000},
		{keyMaxPerTxQuota, 0, 0, 0},
		{keyDailyMaxCount, 2, 0, 2},
		{keyDailyMaxQuota, 0, 10000, 10000},
		{keyReceiverDailyMaxInCount, 50, 2, 2},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, stricterSetting(tc.key, tc.a, tc.b),
			"stricterSetting(%s, %d, %d)", tc.key, tc.a, tc.b)
	}
}

// 换档之后当天的额度不能跟着变松 —— 这是 ① 那条链的落点。
func TestTierOfTodayKeepsApplyingAfterAMidDayGroupChange(t *testing.T) {
	const now int64 = 1_700_000_000
	bucket := dayBucket(now)

	settings := tierSettings(
		// 紧档:单笔 5000、日额 10000、日笔数 2、冷却 300。
		// MinQuota 必须一起给:全局下限是 50 万,而这一档的单笔上限是 5000,
		// 不覆盖下限的话这一档本身就是「任何金额都不合法」,transferFor 会
		// fail-closed 报 errGroupLimitInvalid —— 那不是本用例要验的东西。
		GroupLimit{UserGroup: "lo", Enabled: true,
			MinQuota: i64(1_000), MaxPerTxQuota: i64(5_000), DailyMaxQuota: i64(10_000),
			DailyMaxCount: i64(2), CooldownSecs: i64(300)},
		// 松档:五项全 0 = 一道都不设。
		GroupLimit{UserGroup: "hi", Enabled: true,
			MinQuota: i64(0), MaxPerTxQuota: i64(0), DailyMaxQuota: i64(0),
			DailyMaxCount: i64(0), CooldownSecs: i64(0)},
	)

	t.Run("今天还没转出过:直接按新档,那是他买到的东西", func(t *testing.T) {
		cfg, err := settings.transferForSenderDay("hi", &UserState{DayBucket: bucket}, bucket)
		require.NoError(t, err)
		assert.Zero(t, cfg.MaxPerTxQuota)
		assert.Zero(t, cfg.DailyMaxCount)
	})

	t.Run("状态行还不存在:同上", func(t *testing.T) {
		cfg, err := settings.transferForSenderDay("hi", nil, bucket)
		require.NoError(t, err)
		assert.Zero(t, cfg.MaxPerTxQuota)
	})

	t.Run("今天在紧档下转过账、现在换到松档:今天继续按紧档取严", func(t *testing.T) {
		cfg, err := settings.transferForSenderDay("hi", &UserState{
			DayBucket: bucket, DayOutGroup: "lo", DayOutCount: 2, DayOutQuota: 10_000,
		}, bucket)
		require.NoError(t, err)
		assert.EqualValues(t, 5_000, cfg.MaxPerTxQuota, "6000 万那一笔必须还是转不出去")
		assert.EqualValues(t, 10_000, cfg.DailyMaxQuota)
		assert.Equal(t, 2, cfg.DailyMaxCount)
		assert.Equal(t, 300, cfg.CooldownSecs)
	})

	t.Run("方向反过来:今天在松档下转过账、现在掉进紧档 —— 立刻按紧档", func(t *testing.T) {
		cfg, err := settings.transferForSenderDay("lo", &UserState{
			DayBucket: bucket, DayOutGroup: "hi", DayOutCount: 1,
		}, bucket)
		require.NoError(t, err)
		assert.EqualValues(t, 5_000, cfg.MaxPerTxQuota, "收紧方向永远即时生效")
		assert.Equal(t, 2, cfg.DailyMaxCount)
	})

	t.Run("跨日之后重新开始:昨天那一档不再压着他", func(t *testing.T) {
		cfg, err := settings.transferForSenderDay("hi", &UserState{
			DayBucket: bucket - 1, DayOutGroup: "lo", DayOutCount: 2,
		}, bucket)
		require.NoError(t, err)
		assert.Zero(t, cfg.MaxPerTxQuota)
		assert.Zero(t, cfg.DailyMaxCount)
	})

	t.Run("没换档:与 transferFor 逐位相同", func(t *testing.T) {
		want, err := settings.transferFor("lo")
		require.NoError(t, err)
		got, err := settings.transferForSenderDay("lo", &UserState{
			DayBucket: bucket, DayOutGroup: "lo", DayOutCount: 1,
		}, bucket)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})
}

// 当天基准只由今天的第一笔定下,后面几笔不许覆盖 ——
// 覆盖的话「换到松档再转一笔」就把基准也换掉了,闸门等于没加。
func TestDayOutGroupIsPinnedByTheFirstTransferOfTheDay(t *testing.T) {
	const now int64 = 1_700_000_000
	bucket := dayBucket(now)

	sender := UserState{UserId: 1}
	receiver := UserState{UserId: 2}
	applyReservation(&sender, &receiver, "lo", 1_000, 1_000, now, bucket)
	assert.Equal(t, "lo", sender.DayOutGroup)

	applyReservation(&sender, &receiver, "hi", 1_000, 1_000, now, bucket)
	assert.Equal(t, "lo", sender.DayOutGroup, "第二笔换了档也不能改写今天的基准")

	// 跨日必须清:昨天在紧档转过账,不能压着今天。
	rollDay(&sender, bucket+1)
	assert.Empty(t, sender.DayOutGroup)
	assert.Zero(t, sender.DayOutCount)
}

// strictestTransfer 只动可分档的七项,别的字段一个都不许被搅动。
func TestStrictestTransferLeavesNonTierableFieldsAlone(t *testing.T) {
	a := tierGlobal()
	b := tierGlobal()
	b.FeeBps = 9999
	b.FeeMinQuota = 9999
	b.RecipientLookup = config.RecipientLookupID
	b.MaxPerTxQuota = 7

	out := strictestTransfer(a, b)
	assert.EqualValues(t, 7, out.MaxPerTxQuota)
	assert.Equal(t, a.FeeBps, out.FeeBps, "手续费不可分档,也不参与取严")
	assert.Equal(t, a.FeeMinQuota, out.FeeMinQuota)
	assert.Equal(t, a.Enabled, out.Enabled)
}

// TestCreateKeepsTodaysTierAfterASelfServeGroupUpgrade 是这条链的**接线**回归。
//
// 纯函数写对了、create() 没接上,是本仓的头号缺陷形状:分档会在受理校验、
// 日额度、日笔数、冷却四处全部失效,而整包测试仍然全绿。所以这一条必须从
// create() 这个真正会动钱的入口进,并且断言到扩展库的观测面。
//
// 复现的就是实测那条链:紧档里当天用满 → 花钱换到松档 → 同一秒再转一笔。
func TestCreateKeepsTodaysTierAfterASelfServeGroupUpgrade(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	// 成功那一路会走到主库 outbox 登记(twophase 的 applyOnMainDB),
	// 缺这张表的话最后一组对照会以「no such table」失败而不是以断言失败。
	require.NoError(t, mainDB.AutoMigrate(&model.QyFundOutbox{}))
	// 发起方**此刻**已经在松档里(模拟刚买完升组套餐)。
	seedMainUser(t, mainDB, 1, "hi", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)

	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "lo", Enabled: true,
		MinQuota: i64(1_000), MaxPerTxQuota: i64(5_000),
		DailyMaxQuota: i64(10_000), DailyMaxCount: i64(2),
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "hi", Enabled: true,
		MinQuota: i64(0), MaxPerTxQuota: i64(0),
		DailyMaxQuota: i64(0), DailyMaxCount: i64(0),
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()

	// 今天的两笔是在紧档 "lo" 下转出去的,额度已经用满。
	require.NoError(t, gdb.Create(&UserState{
		UserId: 1, DayBucket: dayBucket(common.GetTimestamp()),
		DayOutGroup: "lo", DayOutCount: 2, DayOutQuota: 10_000,
		UpdatedAt: common.GetTimestamp(),
	}).Error)

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "upgrade-bypass-1",
	})
	require.Error(t, err, "换到松档也不该在今天放行这一笔")
	assert.Same(t, errAmountOutOfRange, err,
		"今天是在 lo 那一档下累的计数,单笔上限必须还是 lo 的 5000")
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1), "被拒的划转不得动余额")
	assert.Equal(t, 0, quotaOf(t, mainDB, 2))

	// 日笔数同样还压着他:金额降到 lo 档内也照样过不去。
	err = callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 5_000, Confirm: true, ClientRequestId: "upgrade-bypass-2",
	})
	require.Error(t, err)
	assert.Same(t, errDailyCountExceeded, err)
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1))

	// 对照:同一个人、同样在松档里,但**今天还没转出过** ⇒ 松档照常生效。
	// 少了这一组,上面两条断言可能只是因为「换档之后一律拒」而通过。
	require.NoError(t, gdb.Model(&UserState{}).Where("user_id = ?", 1).
		Updates(map[string]any{"day_out_group": "", "day_out_count": 0, "day_out_quota": 0}).Error)
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 60_000_000, Confirm: true, ClientRequestId: "upgrade-bypass-3",
	}), "今天还没转过账的人换档就该拿到松档 —— 那是他买到的东西")
	assert.Equal(t, 30_000_000, quotaOf(t, mainDB, 1))
	assert.Equal(t, 60_000_000, quotaOf(t, mainDB, 2))

	// 这一笔本身把今天的基准钉在 "hi" 上,落库可查。
	var st UserState
	require.NoError(t, gdb.First(&st, "user_id = ?", 1).Error)
	assert.Equal(t, "hi", st.DayOutGroup)
}
