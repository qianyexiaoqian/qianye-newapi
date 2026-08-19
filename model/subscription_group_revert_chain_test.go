package model

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useChainDB 把本文件的用例接到一个**允许多条连接**的临时库上。
//
// 本包的 TestMain 把 DB 限成 MaxOpenConns(1)。CreateUserSubscriptionFromPlanTx
// 在自己的事务里调 GetDBTimestamp(),而后者走全局 DB 句柄另取一条连接 ——
// 一条连接的库上那一步会永远等下去。这不是被测代码的问题(生产库连接池不止一条),
// 而是 fixture 的限制,所以这里换一个文件库、放开连接数。
func useChainDB(t *testing.T) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "chain.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&User{}, &SubscriptionPlan{}, &UserSubscription{}))
	prev, prevLog := DB, LOG_DB
	DB, LOG_DB = gdb, gdb
	t.Cleanup(func() {
		DB, LOG_DB = prev, prevLog
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
}

// subscription_group_revert_chain_test.go —— 到期回退必须回到「这条升组链开始
// 之前」那个分组,而不是链上的某一环。
//
// 两条被实测出来的路径都是**永久把付费用户组免费送出去**:
//
//   - 跨组顶替:default → VIP → GOLD。GOLD 那一行记的 prev 是 VIP(刚刚被作废、
//     剩余时间不折算不退款的那一档),GOLD 到期后人被放回 VIP,而此刻他名下
//     一条会改组的订阅都没有了 —— 再没有任何任务会碰他的分组。
//   - 续费:同一个「升组+送额度」套餐买第二次时 prev 留空,而到期扫描取的正是
//     end_time 最大的那一行,判到空就放弃回退。第一行上记着的 default 从不被读。
//
// 两条都不需要管理员、不需要越权接口,就是最普通的购买与续费。

func seedGroupPlan(t *testing.T, id int, upgradeGroup string, noQuota bool, durationSeconds int) *SubscriptionPlan {
	t.Helper()
	plan := &SubscriptionPlan{
		Id:            id,
		Title:         "plan-" + upgradeGroup,
		Enabled:       true,
		NoQuota:       noQuota,
		UpgradeGroup:  upgradeGroup,
		DurationUnit:  SubscriptionDurationCustom,
		CustomSeconds: int64(durationSeconds),
	}
	if !noQuota {
		plan.TotalAmount = 10_000
	}
	require.NoError(t, DB.Create(plan).Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM subscription_plans WHERE id = ?", id) })
	return plan
}

func seedGroupUser(t *testing.T, id int, group string) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id: id, Username: "chain-user", Group: group, Status: 1,
	}).Error)
	t.Cleanup(func() {
		DB.Exec("DELETE FROM users WHERE id = ?", id)
		DB.Exec("DELETE FROM user_subscriptions WHERE user_id = ?", id)
	})
}

func userGroupOf(t *testing.T, userId int) string {
	t.Helper()
	var u User
	require.NoError(t, DB.Where("id = ?", userId).First(&u).Error)
	return u.Group
}

func buyPlan(t *testing.T, userId int, plan *SubscriptionPlan) *UserSubscription {
	t.Helper()
	var sub *UserSubscription
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		created, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, "balance")
		if err != nil {
			return err
		}
		sub = created
		return nil
	}))
	return sub
}

// expireEverything 把这个人所有订阅的到期时间推到过去,再跑一次真实的到期扫描。
func expireEverything(t *testing.T, userId int) {
	t.Helper()
	require.NoError(t, DB.Model(&UserSubscription{}).
		Where("user_id = ? AND end_time > 0", userId).
		Update("end_time", time.Now().Unix()-10).Error)
	_, err := ExpireDueSubscriptions(200)
	require.NoError(t, err)
}

func TestGroupRevertLandsOnTheGroupHeldBeforeTheUpgradeChain(t *testing.T) {
	t.Run("跨组顶替:GOLD 到期必须回 default,不是回被顶掉的 VIP", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9401, "default")
		vip := seedGroupPlan(t, 9401, "chain-vip", true, 3600)
		gold := seedGroupPlan(t, 9402, "chain-gold", true, 3600)

		first := buyPlan(t, 9401, vip)
		require.Equal(t, "default", first.PrevUserGroup)
		require.Equal(t, "chain-vip", userGroupOf(t, 9401))

		second := buyPlan(t, 9401, gold)
		assert.Equal(t, "default", second.PrevUserGroup,
			"新行必须继承链根,记成 chain-vip 就等于把一个付费组永久送出去")
		require.Equal(t, "chain-gold", userGroupOf(t, 9401))

		expireEverything(t, 9401)
		assert.Equal(t, "default", userGroupOf(t, 9401))

		// 再扫一遍不能把人又挪走(回退必须是幂等的终态)。
		_, err := ExpireDueSubscriptions(200)
		require.NoError(t, err)
		assert.Equal(t, "default", userGroupOf(t, 9401))
	})

	t.Run("续费:同一个升组套餐买两次,全部到期后必须回 default", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9402, "default")
		// 带额度 ⇒ 走不到纯商品的续期分支,会真的新建第二行。
		vip := seedGroupPlan(t, 9403, "chain-vip2", false, 3600)

		first := buyPlan(t, 9402, vip)
		require.Equal(t, "default", first.PrevUserGroup)
		second := buyPlan(t, 9402, vip)
		require.NotEqual(t, first.Id, second.Id, "带额度的套餐第二次购买必须新建一行")
		assert.Equal(t, "default", second.PrevUserGroup,
			"第二行 prev 留空的话,到期扫描取到的正是它,判空即放弃回退")

		expireEverything(t, 9402)
		assert.Equal(t, "default", userGroupOf(t, 9402))
	})

	t.Run("只买一次仍然照旧回退(不能因为这次修复改掉正常路径)", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9403, "silver")
		vip := seedGroupPlan(t, 9404, "chain-vip3", true, 3600)

		sub := buyPlan(t, 9403, vip)
		assert.Equal(t, "silver", sub.PrevUserGroup, "没有链时链根就是买之前那一刻")
		require.Equal(t, "chain-vip3", userGroupOf(t, 9403))

		expireEverything(t, 9403)
		assert.Equal(t, "silver", userGroupOf(t, 9403))
	})
}

// TestNewPurchaseOnTopOfABrokenLegacyChainInheritsTheRealRoot 覆盖库里**已经**
// 有一条坏链的人再买一次的情形。
//
// 这是链根必须取 id 最小那一条(而不是最近那一条)的唯一理由:坏链上,最近那一环
// 记着的正是被顶掉的付费组,继承它等于把缺陷原样传给新行。修复上线之后新建的
// 每一环都携带同一个根,那时取哪一环都一样 —— 只有和历史坏行混在一起时才分得出。
func TestNewPurchaseOnTopOfABrokenLegacyChainInheritsTheRealRoot(t *testing.T) {
	useChainDB(t)
	seedGroupUser(t, 9421, "broken-gold")
	now := time.Now().Unix()
	// 老逻辑留下的两行:vip 记着真正的链根 default,gold 记着被顶掉的 vip。
	require.NoError(t, DB.Create(&UserSubscription{
		Id: 9421, UserId: 9421, PlanId: 1, Status: SubscriptionStatusSuperseded,
		UpgradeGroup: "broken-vip", PrevUserGroup: "default",
		StartTime: now - 200, EndTime: now - 150,
	}).Error)
	require.NoError(t, DB.Create(&UserSubscription{
		Id: 9422, UserId: 9421, PlanId: 2, Status: "active",
		UpgradeGroup: "broken-gold", PrevUserGroup: "broken-vip",
		StartTime: now - 150, EndTime: now + 3600,
	}).Error)

	plat := seedGroupPlan(t, 9421, "broken-plat", true, 3600)
	sub := buyPlan(t, 9421, plat)
	assert.Equal(t, "default", sub.PrevUserGroup,
		"必须取链根 default;取最近那一环会继承坏值 broken-vip,缺陷就这样传下去")

	expireEverything(t, 9421)
	assert.Equal(t, "default", userGroupOf(t, 9421))
}

// TestLegacyRowsWithoutAChainRootStillRevert 覆盖**升级之前**就落在库里的行。
//
// 那些行不会被回填(快照表事后改写等于篡改"当时记了什么"),所以回退这一侧
// 必须能自己把链走回去。这里直接按老逻辑造出那两种坏行。
func TestLegacyRowsWithoutAChainRootStillRevert(t *testing.T) {
	t.Run("顶替链的历史行:prev 记着已经被顶掉的付费组", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9411, "legacy-gold")
		now := time.Now().Unix()
		require.NoError(t, DB.Create(&UserSubscription{
			Id: 9411, UserId: 9411, PlanId: 1, Status: SubscriptionStatusSuperseded,
			UpgradeGroup: "legacy-vip", PrevUserGroup: "default",
			StartTime: now - 100, EndTime: now - 50,
		}).Error)
		require.NoError(t, DB.Create(&UserSubscription{
			Id: 9412, UserId: 9411, PlanId: 2, Status: "active",
			UpgradeGroup: "legacy-gold", PrevUserGroup: "legacy-vip", // ← 坏值
			StartTime: now - 50, EndTime: now + 3600,
		}).Error)

		expireEverything(t, 9411)
		assert.Equal(t, "legacy-vip", userGroupOf(t, 9411),
			"历史行的 prev 是冻结事实,不回填 —— 它记着什么就回哪里")
	})

	t.Run("续费的历史行:第二行 prev 为空,必须走回第一行的 default", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9412, "legacy-vip2")
		now := time.Now().Unix()
		require.NoError(t, DB.Create(&UserSubscription{
			Id: 9413, UserId: 9412, PlanId: 1, Status: "active",
			UpgradeGroup: "legacy-vip2", PrevUserGroup: "default",
			StartTime: now - 100, EndTime: now + 3600,
		}).Error)
		require.NoError(t, DB.Create(&UserSubscription{
			Id: 9414, UserId: 9412, PlanId: 1, Status: "active",
			UpgradeGroup: "legacy-vip2", PrevUserGroup: "", // ← 老逻辑留空的那一行
			StartTime: now - 50, EndTime: now + 7200,
		}).Error)

		expireEverything(t, 9412)
		assert.Equal(t, "default", userGroupOf(t, 9412),
			"end_time 最大的那一行 prev 为空,不能就此放弃回退")
	})

	t.Run("坏链 + 续费:兜底必须挑链根,不是链上离得最近那一环", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9413, "legacy-gold3")
		now := time.Now().Unix()
		// default → vip(记对了) → gold(记成 vip,坏值) → gold 续费(prev 为空)
		require.NoError(t, DB.Create(&UserSubscription{
			Id: 9415, UserId: 9413, PlanId: 1, Status: SubscriptionStatusSuperseded,
			UpgradeGroup: "legacy-vip3", PrevUserGroup: "default",
			StartTime: now - 300, EndTime: now - 250,
		}).Error)
		require.NoError(t, DB.Create(&UserSubscription{
			Id: 9416, UserId: 9413, PlanId: 2, Status: "active",
			UpgradeGroup: "legacy-gold3", PrevUserGroup: "legacy-vip3",
			StartTime: now - 250, EndTime: now + 3600,
		}).Error)
		require.NoError(t, DB.Create(&UserSubscription{
			Id: 9417, UserId: 9413, PlanId: 2, Status: "active",
			UpgradeGroup: "legacy-gold3", PrevUserGroup: "",
			StartTime: now - 100, EndTime: now + 7200,
		}).Error)

		expireEverything(t, 9413)
		assert.Equal(t, "default", userGroupOf(t, 9413),
			"挑最近那一环会落到 legacy-vip3 —— 那正是被顶掉、用户并没有为之付过第二次钱的档")
	})
}
