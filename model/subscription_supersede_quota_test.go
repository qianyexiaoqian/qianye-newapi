package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// subscription_supersede_quota_test.go —— 跨组顶替**不许销毁用户已经付过钱的余额**。
//
// ═══════════ 被修掉的洞 ═══════════
//
// applyUserGroupPurchaseRulesTx 的异组作废分支只按 `upgrade_group <> '' AND
// status='active'` 选行,不看这条订阅带不带余额。买家新买的那件是纯商品,
// 但被顶掉的可以是一张「升组 + 送额度」的付费订阅 —— status 被写成 superseded、
// end_time 推到当下,里面没花完的余额一起消失。
//
// 实测:持有 amount_total=1,000,000 / used=10,000 的 VIP 订阅,买一件几块钱的
// 纯商品(no_quota, upgrade_group=svip)之后,那 990,000 直接作废、不可撤销、
// 没有任何退款路径,下单预览还只字未提额度。
//
// ═══════════ 修完的口径 ═══════════
//
// 项目方 2026-08-14 拍板那条规则的原话是「A 组没到期时买 B 组 → A 组**剩余时间**
// 直接作废」,而「用户组商品」的定义就是"纯商品,没有余额"。所以作废的是
// **组与时间**,不是钱:
//
//	纯商品        → 照旧 superseded + end_time=now
//	带额度的订阅  → 只摘掉改组身份(upgrade_group/downgrade_group 清空),
//	                status 与 end_time 一个字节不动,余额继续出资到原到期日

func useSupersedeDB(t *testing.T) {
	t.Helper()
	dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "supersede.db")) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	require.NoError(t, gdb.AutoMigrate(
		&User{}, &Log{}, &SubscriptionPlan{}, &UserSubscription{}, &SubscriptionPreConsumeRecord{},
	))
	prev, prevLog := DB, LOG_DB
	DB, LOG_DB = gdb, gdb
	t.Cleanup(func() {
		DB, LOG_DB = prev, prevLog
		_ = sqlDB.Close()
	})
}

func subscriptionById(t *testing.T, id int) *UserSubscription {
	t.Helper()
	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", id).First(&sub).Error)
	return &sub
}

// TestCrossGroupPurchaseKeepsPaidQuota 是这条洞的正面用例。
func TestCrossGroupPurchaseKeepsPaidQuota(t *testing.T) {
	useSupersedeDB(t)
	const userId = 77_001
	seedGroupUser(t, userId, "default")

	vipPlan := seedGroupPlan(t, 77_101, "vip", false, 30*24*3600)
	vip := buyPlan(t, userId, vipPlan)
	require.Equal(t, "vip", userGroupOf(t, userId))
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", vip.Id).
		Update("amount_used", 1_000).Error)
	vipEndBefore := subscriptionById(t, vip.Id).EndTime

	// 几块钱的纯商品,目标是另一个组。
	svipPlan := seedGroupPlan(t, 77_102, "svip", true, 7*24*3600)
	buyPlan(t, userId, svipPlan)

	after := subscriptionById(t, vip.Id)
	assert.Equal(t, "active", after.Status,
		"带额度的订阅不许被作废 —— 里面的钱是用户真的付过的")
	assert.Equal(t, vipEndBefore, after.EndTime, "有效期不许被推到当下")
	assert.Equal(t, int64(10_000-1_000), after.AmountTotal-after.AmountUsed,
		"未花完的额度必须原样留着")
	assert.Empty(t, after.UpgradeGroup, "被顶替的是它的**改组身份**:不再决定用户组")
	assert.Equal(t, "svip", userGroupOf(t, userId), "用户组归新买的那件")

	// 余额真的还能用:这才是"没被销毁"的唯一硬证据。
	res, err := PreConsumeUserSubscription("supersede-keeps-quota", userId, "gpt-test", 0, 500, "default")
	require.NoError(t, err, "被摘掉改组身份的订阅必须还能继续出资")
	assert.Equal(t, vip.Id, res.UserSubscriptionId)
	assert.Equal(t, int64(500), res.PreConsumed)
}

// TestCrossGroupPurchaseStillVoidsPureProducts 纯商品那一档规则原样成立。
//
// 没有它,上一个用例可以靠"把整个顶替分支删掉"来通过 —— 而那会让用户同时
// 拥有两个互斥的付费用户组。
func TestCrossGroupPurchaseStillVoidsPureProducts(t *testing.T) {
	useSupersedeDB(t)
	const userId = 77_002
	seedGroupUser(t, userId, "default")

	goldPlan := seedGroupPlan(t, 77_201, "gold", true, 30*24*3600)
	gold := buyPlan(t, userId, goldPlan)
	require.Equal(t, "gold", userGroupOf(t, userId))

	svipPlan := seedGroupPlan(t, 77_202, "svip", true, 7*24*3600)
	buyPlan(t, userId, svipPlan)

	after := subscriptionById(t, gold.Id)
	assert.Equal(t, SubscriptionStatusSuperseded, after.Status,
		"纯商品只有时间,规则原样成立:立刻作废")
	assert.LessOrEqual(t, after.EndTime, GetDBTimestamp(), "end_time 必须被推到当下")
	assert.Equal(t, "svip", userGroupOf(t, userId))
}

// TestSupersededQuotaSubscriptionStillCarriesTheChainRoot 摘身份不许丢链根。
//
// prev_user_group 记的是「这条升组链开始之前」那个分组。摘掉 upgrade_group 之后
// 如果链根也跟着丢,新买的那件会把"回退到 vip"记进自己的快照 —— 而用户此刻
// 根本不再拥有 vip,到期后被永久留在一个他没买过的付费组里。
// (这正是上一轮修掉的 prev_user_group 链根缺陷的同一个形状。)
func TestSupersededQuotaSubscriptionStillCarriesTheChainRoot(t *testing.T) {
	useSupersedeDB(t)
	const userId = 77_003
	seedGroupUser(t, userId, "default")

	vipPlan := seedGroupPlan(t, 77_301, "vip", false, 30*24*3600)
	vip := buyPlan(t, userId, vipPlan)
	require.Equal(t, "default", subscriptionById(t, vip.Id).PrevUserGroup)

	svipPlan := seedGroupPlan(t, 77_302, "svip", true, 7*24*3600)
	svip := buyPlan(t, userId, svipPlan)
	assert.Equal(t, "default", subscriptionById(t, svip.Id).PrevUserGroup,
		"新订阅的回退目标必须是链根 default,不是刚被顶掉的 vip")

	expireEverything(t, userId)
	assert.Equal(t, "default", userGroupOf(t, userId),
		"链上最后一件到期之后,人必须回到买第一件之前的那个组")
}

// TestAdminGroupChangeAlsoClearsDetachedChainRoots 管理员手工改组必须连带清掉
// 那些"只剩链根"的行。
//
// 顶替留下的行 upgrade_group 已经空了,DetachUserGroupSubscriptionsTx 若仍只按
// upgrade_group 选行就会漏掉它们 —— 管理员刚设好的组会在下一次购买时被那个
// 旧链根覆写回去。
func TestAdminGroupChangeAlsoClearsDetachedChainRoots(t *testing.T) {
	useSupersedeDB(t)
	const userId = 77_004
	seedGroupUser(t, userId, "default")

	vipPlan := seedGroupPlan(t, 77_401, "vip", false, 30*24*3600)
	vip := buyPlan(t, userId, vipPlan)
	svipPlan := seedGroupPlan(t, 77_402, "svip", true, 7*24*3600)
	buyPlan(t, userId, svipPlan)
	require.Equal(t, "default", subscriptionById(t, vip.Id).PrevUserGroup)
	require.Empty(t, subscriptionById(t, vip.Id).UpgradeGroup)

	var detached int64
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		n, err := DetachUserGroupSubscriptionsTx(tx, userId)
		detached = n
		return err
	}))
	assert.Equal(t, int64(2), detached, "两条都该被摘:一条还带着 upgrade_group,一条只剩链根")
	assert.Empty(t, subscriptionById(t, vip.Id).PrevUserGroup,
		"管理员说了算:旧链根必须一起清掉,否则它会在下一次购买时把管理员的操作覆写")
}
