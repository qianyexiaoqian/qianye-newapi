package model

// qy_groupns_export_test.go —— 用户分组改写在主库这一侧的两条不变量。
//
// 这两条都是"改错了不会报错、只会安静地少改一批行"的形状:
//
//	分批    `WHERE id IN (…)` 的占位符个数在 SQLite 上有硬上限。不分批时,超过上限的
//	        那一次迁移直接报一条与分组毫无关系的驱动错误 —— 而它只在用户数够多的
//	        站点上出现,本地几十个测试用户永远撞不到。
//	软删除  users 是软删除表。漏掉 deleted_at IS NULL 会把早已注销的账号一起改回来,
//	        让一个 0 影响的历史分组重新"活"过来并计进影响面。

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestQyChunkIntsCoversEveryIdExactlyOnce(t *testing.T) {
	tests := []struct {
		name  string
		total int
		size  int
		want  int
	}{
		{"空清单", 0, 200, 0},
		{"不足一批", 7, 200, 1},
		{"恰好一批", 200, 200, 1},
		{"多出一个", 201, 200, 2},
		{"size 非法时回落默认值", 500, 0, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids := make([]int, tc.total)
			for i := range ids {
				ids[i] = i + 1
			}
			batches := qyChunkInts(ids, tc.size)
			assert.Len(t, batches, tc.want)

			seen := map[int]int{}
			for _, batch := range batches {
				for _, id := range batch {
					seen[id]++
				}
			}
			assert.Len(t, seen, tc.total, "每个 id 都必须恰好出现一次")
			for id, n := range seen {
				assert.Equalf(t, 1, n, "id %d 出现了 %d 次", id, n)
			}
		})
	}
}

// TestQyRewriteUserGroupTxSkipsDeletedUsersAndRewritesSnapshots 走一遍真实的事务。
//
// 断言里最容易被忽略的是最后一条:**注销账号不算**。上游 users 是软删除表,
// 把它们一起改回来会让一个早已 0 影响的历史分组重新出现在影响面里,
// 运营会以为那一档还有人在用。
func TestQyRewriteUserGroupTxSkipsDeletedUsersAndRewritesSnapshots(t *testing.T) {
	gdb := newQyTestMainDB(t)

	require.NoError(t, gdb.Create(&User{Id: 1, Username: "a", AffCode: "a", Group: "旧档"}).Error)
	require.NoError(t, gdb.Create(&User{Id: 2, Username: "b", AffCode: "b", Group: "旧档"}).Error)
	require.NoError(t, gdb.Create(&User{Id: 3, Username: "c", AffCode: "c", Group: "别的档"}).Error)
	// 一个注销账号,分组同样是旧档。
	require.NoError(t, gdb.Create(&User{Id: 4, Username: "d", AffCode: "d", Group: "旧档"}).Error)
	require.NoError(t, gdb.Delete(&User{}, 4).Error)

	require.NoError(t, gdb.Create(&UserSubscription{
		Id: 1, UserId: 1, PrevUserGroup: "旧档", UpgradeGroup: "旧档",
	}).Error)
	require.NoError(t, gdb.Create(&SubscriptionPlan{
		Id: 1, Title: "季卡", UpgradeGroup: "旧档",
	}).Error)

	// 删除路径不许静默改掉套餐卖的是什么。
	res, err := QyRewriteUserGroupTx("旧档", "新档", QyRewriteModeDelete)
	require.NoError(t, err)

	assert.EqualValues(t, 2, res.Users, "只有 2 个在册用户,注销的那个不算")
	assert.ElementsMatch(t, []int{1, 2}, res.UserIds,
		"UserIds 必须精确到人 —— 调用方要靠它逐个失效 Redis 里的旧分组缓存")
	assert.EqualValues(t, 2, res.Subscriptions, "prev_user_group 与 upgrade_group 各一次")
	assert.EqualValues(t, 0, res.Plans, "删除模式下一个套餐都不能动")
	assert.EqualValues(t, 0, res.Stragglers)

	var plan SubscriptionPlan
	require.NoError(t, gdb.First(&plan, 1).Error)
	assert.Equal(t, "旧档", plan.UpgradeGroup,
		"删除路径必须让套餐引用原样留着,由接口层硬拦 —— "+
			"一个卖着「升级到 X」的套餐被悄悄改成「升级到 Y」,买家付的钱和拿到的东西不再是同一件事")

	var revived User
	require.NoError(t, gdb.Unscoped().First(&revived, 4).Error)
	assert.Equal(t, "旧档", revived.Group, "注销账号的分组不能被改写")
}

// TestQyRewriteUserGroupTxRewritesPlansOnRename 是上一条的另一半。
//
// 改名时套餐必须跟着改:不跟的话这个套餐从此把每一个买家送进一个不存在的分组,
// 而购买流程一个字都不会报错。
func TestQyRewriteUserGroupTxRewritesPlansOnRename(t *testing.T) {
	gdb := newQyTestMainDB(t)
	require.NoError(t, gdb.Create(&User{Id: 1, Username: "a", AffCode: "a", Group: "老名字"}).Error)
	require.NoError(t, gdb.Create(&SubscriptionPlan{
		Id: 1, Title: "季卡", UpgradeGroup: "老名字", DowngradeGroup: "老名字",
	}).Error)

	res, err := QyRewriteUserGroupTx("老名字", "新名字", QyRewriteModeRename)
	require.NoError(t, err)
	assert.EqualValues(t, 2, res.Plans, "upgrade_group 与 downgrade_group 各一次")

	var plan SubscriptionPlan
	require.NoError(t, gdb.First(&plan, 1).Error)
	assert.Equal(t, "新名字", plan.UpgradeGroup)
	assert.Equal(t, "新名字", plan.DowngradeGroup)
}

// TestQyRewriteUserGroupTxMigrateOnlyTouchesMovedUsersSnapshots 锁住纯迁移与
// 删除/改名的**唯一实质差别**。
//
// 纯迁移之后源分组仍然存在、仍然配置完整,所以一个不在这次迁移里的账号
// (早已升到别的档、prev_user_group 还记着源分组)的回落目标指着源分组仍然是对的。
// 按列值全表匹配会把他的降级目标静默改到迁移目标上 —— 一次没有出现在任何确认
// 弹窗里、也没有任何人决定过的档位变更,而它要等到订阅到期那天才显形。
func TestQyRewriteUserGroupTxMigrateOnlyTouchesMovedUsersSnapshots(t *testing.T) {
	gdb := newQyTestMainDB(t)

	require.NoError(t, gdb.Create(&User{Id: 1, Username: "a", AffCode: "a", Group: "旧档"}).Error)
	// 2 号早已升到 VIP,不在这次迁移里,但他的快照还记着旧档。
	require.NoError(t, gdb.Create(&User{Id: 2, Username: "b", AffCode: "b", Group: "VIP"}).Error)
	require.NoError(t, gdb.Create(&UserSubscription{Id: 1, UserId: 1, PrevUserGroup: "旧档"}).Error)
	require.NoError(t, gdb.Create(&UserSubscription{Id: 2, UserId: 2, PrevUserGroup: "旧档"}).Error)
	require.NoError(t, gdb.Create(&SubscriptionPlan{
		Id: 1, Title: "季卡", UpgradeGroup: "旧档",
	}).Error)

	res, err := QyRewriteUserGroupTx("旧档", "新档", QyRewriteModeMigrate)
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Users)
	assert.EqualValues(t, 1, res.Subscriptions, "只有被迁移的那个人的快照会动")
	assert.EqualValues(t, 0, res.Plans, "纯迁移一个套餐引用都不动 —— 分组名还在,套餐照常工作")

	var moved, untouched UserSubscription
	require.NoError(t, gdb.First(&moved, 1).Error)
	require.NoError(t, gdb.First(&untouched, 2).Error)
	assert.Equal(t, "新档", moved.PrevUserGroup)
	assert.Equal(t, "旧档", untouched.PrevUserGroup,
		"没有被迁移的人的回落目标必须原样留着 —— 源分组仍然存在,那个值不是孤儿")

	var plan SubscriptionPlan
	require.NoError(t, gdb.First(&plan, 1).Error)
	assert.Equal(t, "旧档", plan.UpgradeGroup)
}

func TestQyRewriteUserGroupTxRejectsEmptyTarget(t *testing.T) {
	newQyTestMainDB(t)
	_, err := QyRewriteUserGroupTx("旧档", "", QyRewriteModeDelete)
	require.Error(t, err,
		"目标为空会把一批用户的 group 清空,而空 group 与 default 在 middleware/auth.go 里并不等价")
}

// TestQyRewriteUserGroupTxRejectsUnknownMode 守住"模式必须显式给出"。
//
// 未知模式回落到某一条默认路径的表现是:调用方以为自己在做纯迁移,而实际发生的是
// 一次按列值全表匹配的快照改写 —— 那正是这个参数被引进来要区分的两件事。
func TestQyRewriteUserGroupTxRejectsUnknownMode(t *testing.T) {
	newQyTestMainDB(t)
	_, err := QyRewriteUserGroupTx("旧档", "新档", "")
	require.Error(t, err)
}

// newQyTestMainDB 建一个主库测试实例。
//
// initCol() 必须跑:它由 InitDB 调用,而测试直接替换 DB 句柄绕过了那一步,
// 于是 commonGroupCol 是空串,`SELECT ` + col 会发出语法错误的 SQL。
func newQyTestMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "main.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&User{}, &UserSubscription{}, &SubscriptionPlan{}))

	prev := DB
	DB = gdb
	initCol()
	t.Cleanup(func() {
		DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}
