package planentitlement

import (
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolve_test.go —— 「未配置时与上游逐位一致」+「配置之后并集正确」。
//
// 前一半是回退能力的全部证明:两张表空着的时候,本模块必须一个字节都不改
// (而且**一次 I/O 都不发**)。后一半是需求本身:多个套餐的解锁取并集,
// 与用户分组无关。

// falseP 让用例能显式关掉那个 *bool 开关。
func falseP() *bool { b := false; return &b }

// TestQyPlanUnlockDefaultIsIdentity 是**指针恒等**断言。
//
// 值相等在功能上够用,但指针相等才能证明"hook 确实没碰它"。有人哪天在恒等分支
// 里顺手加一次 maps.Clone,值断言照常通过,而 controller/pricing.go 依赖的指针
// 语义已经变了。
func TestQyPlanUnlockDefaultIsIdentity(t *testing.T) {
	gdb := newExtDB(t)
	newMainDB(t)
	setGroupRatios(t, `{"default":1,"vip":0.5,"pro":0.8}`)

	cases := []struct {
		name   string
		userId int
		setup  func(t *testing.T)
	}{
		{"(a) 功能开关显式关闭", 7, func(t *testing.T) {
			cfg := *qyConfig.Load()
			cfg.PlanEntitlement.Enabled = falseP()
			prev := qyConfig.Swap(&cfg)
			t.Cleanup(func() { qyConfig.Store(prev) })
			putGrant(t, gdb, 1, "pro")
			require.NoError(t, reload())
		}},
		{"(b) 匿名口径(userId <= 0)", 0, func(t *testing.T) {
			putGrant(t, gdb, 1, "pro")
			require.NoError(t, reload())
		}},
		{"(c) 全站零绑定", 7, func(t *testing.T) {
			require.NoError(t, reload())
		}},
		{"(d) 快照从未加载", 7, func(t *testing.T) {
			current.Store(nil)
		}},
		{"(e) 该用户没有任何活跃订阅", 7, func(t *testing.T) {
			putGrant(t, gdb, 1, "pro")
			require.NoError(t, reload())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, gdb.Exec("DELETE FROM qy_plan_group_grants").Error)
			resetCaches()
			tc.setup(t)

			in := map[string]string{"default": "默认分组"}
			got := Resolve(tc.userId, in)
			assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer(),
				"这一档必须原样返回入参那一张 map,不得复制、不得增删")
		})
	}
}

// TestZeroBindingsCostsNoDatabaseRead 钉死「上线当天零新增 I/O」。
//
// 这不是性能洁癖:本 hook 挂在 middleware/auth.go 上,每一个带令牌分组的请求
// 调用一次。如果"全站零绑定"这条短路没了,那就是给主库凭空加上与 relay QPS
// 等量的订阅查询,而功能本身一个人都没在用。
//
// 判据用第二层缓存的 miss 计数:它只在真的要回源时才自增。
func TestZeroBindingsCostsNoDatabaseRead(t *testing.T) {
	newExtDB(t)
	newMainDB(t)
	setGroupRatios(t, `{"default":1}`)
	require.NoError(t, reload())

	in := map[string]string{"default": "默认分组"}
	for i := 0; i < 20; i++ {
		Resolve(1000+i, in)
	}
	assert.Zero(t, userCacheMiss.Load(),
		"全站没有任何套餐配过绑定时,解析必须在触碰 per-user 缓存之前就返回 —— "+
			"一次主库查询都不该发生")
}

// TestUnlockIsUnionAcrossPlansAndIgnoresUserGroup 是需求本身:
// 「可以拥有多个套餐,可以用作解锁,新增模型组,不绑定用户组」。
//
// 三件事一起断言:
//   - 多个套餐的解锁取**并集**(成员资格是幂等的,不存在"谁优先");
//   - 上游给的键一个都不能少,而且描述文案原样保留;
//   - 与用户分组无关 —— 同一个 userId 无论上游给的是哪一档,解锁项都会并进去。
func TestUnlockIsUnionAcrossPlansAndIgnoresUserGroup(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"vip":0.5,"pro":0.8,"lab":0.1}`)

	seedPlan(t, mainDB, 1, "套餐A")
	seedPlan(t, mainDB, 2, "套餐B")
	putGrant(t, gdb, 1, "pro")
	putGrant(t, gdb, 1, "lab")
	putGrant(t, gdb, 2, "pro") // 与套餐A 重叠:并集里只出现一次
	putGrant(t, gdb, 3, "vip") // 用户没买这个套餐
	require.NoError(t, reload())

	seedSubscription(t, mainDB, 11, 7, 1, 3600, 1000, 0)
	seedSubscription(t, mainDB, 12, 7, 2, 7200, 1000, 0)

	in := map[string]string{"default": "默认分组"}
	got := Resolve(7, in)

	assert.Equal(t, map[string]string{
		"default": "默认分组",
		"pro":     "pro",
		"lab":     "lab",
	}, got, "解锁必须是多个套餐的并集,且不动上游已有的键值")
	assert.Equal(t, map[string]string{"default": "默认分组"}, in,
		"绝不能就地改写调用方那张 map")

	// 没买任何套餐的人一个解锁都拿不到 —— 解锁挂在人身上,不是挂在分组上。
	assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(Resolve(8, in)).Pointer())
}

// TestUnlockNeverLeaksAutoOrMissingGroups 守两条写入侧与编译期的硬约束在
// **解析结果**上真的成立。
//
// 手改数据库绕过接口是这套系统最现实的攻击面,所以这两条不能只在写入侧把关:
//
//	auto      它不是模型分组,放进可选清单会让上游 IsUserSelectableGroup 与
//	          auto 候选链路各说各话;
//	已删分组  上游 GetGroupRatio 找不到时**静默返回 1**,按原价扣费且零告警,
//	          而 middleware/auth.go 紧随其后的 ContainsGroupRatio 又会 403 掉它 ——
//	          配置看起来是通的,实际一定不生效。
func TestUnlockNeverLeaksAutoOrMissingGroups(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)

	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	putGrant(t, gdb, 1, autoGroup)
	putGrant(t, gdb, 1, "已经被删掉的分组")
	require.NoError(t, reload())
	seedSubscription(t, mainDB, 11, 7, 1, 3600, 1000, 0)

	got := Resolve(7, map[string]string{"default": "默认分组"})
	assert.Equal(t, map[string]string{"default": "默认分组", "pro": "pro"}, got)

	s, ok := SnapshotView()
	require.True(t, ok)
	assert.Equal(t, []string{"plan:1/已经被删掉的分组"}, s.Dropped,
		"指向已删模型分组的绑定必须被剔除并留下痕迹,而不是静默消失")
}

// TestExpiredSubscriptionUnlocksNothing 钉死解锁与扣费用**同一个判据**。
//
// 上游 PreConsumeUserSubscription 的候选是 status='active' AND end_time > now。
// 解锁若用另一套判据,就会出现"看得见却扣不到"或"扣得到却看不见" ——
// 两种都是说不清的工单,而且它们出现的时刻正好是用户套餐刚到期那一天。
func TestExpiredSubscriptionUnlocksNothing(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	require.NoError(t, reload())

	// end_time 已过但 status 仍是 active:没有 master 节点的部署里这种行永久存在
	// (ExpireDueSubscriptions 只在 master 上跑)。上游扣费同样取不到它。
	seedSubscription(t, mainDB, 11, 7, 1, -60, 1000, 0)

	in := map[string]string{"default": "默认分组"}
	assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(Resolve(7, in)).Pointer(),
		"到期(哪怕还没被清扫成 expired)的订阅一律不解锁任何分组")
}

// TestUnlockedGroupMatchesResolve 守读写两侧同源。
//
// 写入侧(groupmatrix.CheckTokenGroup)问的是单个分组,读侧问的是整张清单。
// 两者一旦用不同的判据,用户就会遇到"能用却存不下"——本仓已经修过一次的形状。
func TestUnlockedGroupMatchesResolve(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8,"lab":0.1}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	require.NoError(t, reload())
	seedSubscription(t, mainDB, 11, 7, 1, 3600, 1000, 0)

	resolved := Resolve(7, map[string]string{"default": "默认分组"})
	for _, g := range []string{"pro", "lab", "default", autoGroup, ""} {
		_, inResolved := resolved[g]
		unlocked := UnlockedGroup(7, g)
		if g == "default" || g == autoGroup || g == "" {
			// 上游本来就给的键、auto、空串都不算"套餐解锁"。
			assert.False(t, unlocked, "%q 不该被判成套餐解锁", g)
			continue
		}
		assert.Equal(t, inResolved, unlocked,
			"%q 在读侧与写侧的结论必须一致", g)
	}
	assert.False(t, UnlockedGroup(0, "pro"), "匿名身份不可能持有套餐")
}

// TestDisabledSwitchRevokesUnlockOnBothSides 守 kill switch 两侧同时生效。
//
// 只关掉读侧的话,用户存得下令牌却发不出请求;只关掉写侧则相反。
// 一个开关必须同时控制两侧,不允许其中一侧先漏出来。
func TestDisabledSwitchRevokesUnlockOnBothSides(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	require.NoError(t, reload())
	seedSubscription(t, mainDB, 11, 7, 1, 3600, 1000, 0)

	require.True(t, UnlockedGroup(7, "pro"))

	cfg := *qyConfig.Load()
	cfg.PlanEntitlement = config.PlanEntitlement{
		Enabled: falseP(), CacheSeconds: 30, UserCacheSeconds: 60, UserMaxStaleSeconds: 300,
	}
	prev := qyConfig.Swap(&cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })

	in := map[string]string{"default": "默认分组"}
	assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(Resolve(7, in)).Pointer())
	assert.False(t, UnlockedGroup(7, "pro"))
}

// TestServiceWrapperAppliesUnlockAfterUpstream 证明并集叠在**替换之后**。
//
// service.QyUsableGroupsForUser 的顺序是「上游 + 权威清单 → 再并入套餐解锁」。
// 顺序反了的话,一个被权威清单收紧的用户分组会把套餐解锁一起抹掉 ——
// 用户付了钱却拿不到,而且没有任何报错。
func TestServiceWrapperAppliesUnlockAfterUpstream(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	require.NoError(t, reload())
	seedSubscription(t, mainDB, 11, 7, 1, 3600, 1000, 0)

	// 模拟一个"权威清单把该用户分组收紧到只剩 default"的上游结果。
	prevResolve := service.QyResolveUsableGroups
	service.QyResolveUsableGroups = func(_ string, _ map[string]string) map[string]string {
		return map[string]string{"default": "默认分组"}
	}
	prevUnlock := service.QyPlanUnlockGroups
	service.QyPlanUnlockGroups = Resolve
	t.Cleanup(func() {
		service.QyResolveUsableGroups = prevResolve
		service.QyPlanUnlockGroups = prevUnlock
	})

	got := service.QyUsableGroupsForUser(7, "被收紧的分组")
	assert.Contains(t, got, "default")
	assert.Contains(t, got, "pro",
		"套餐解锁必须叠在权威清单之后 —— 这就是「不绑定用户组」在解析顺序上的落点")
}
