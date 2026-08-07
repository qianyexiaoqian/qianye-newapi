package planentitlement

import (
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/qianye/modules/groupmatrix"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unset_scope_identity_test.go —— 「没有任何 scope 配置时,与上游逐位一致」的证明。
//
// ═══════════ 为什么这条证明必须在这里、必须装两个 hook ═══════════
//
// 各模块自己的 bitexact 用例只装自己那一个 hook,证明的是「我这一个没碰它」。
// 而生产上 service.GetUserUsableGroups 的 return 上串着两层:
//
//	QyResolveUsableGroups  groupmatrix   范围替换
//	QyPlanUnlockGroups     planentitlement 套餐解锁并集(由 QyUsableGroupsForUser 追加)
//
// 「零配置 = 零行为变化」是这两层**合起来**的性质,而它恰好没有任何一个模块的
// 用例覆盖得到。本包能同时 import 两者(planentitlement 本来就依赖 groupmatrix),
// 所以这条证明住在这里。
//
// 断言分两层,缺一不可:
//
//	值逐位相同   与一份**照抄上游函数体**的参考实现比对(而不是与"我期望的样子"比)
//	指针恒等     hook 拿到什么就返回什么 —— 有人在恒等分支里顺手加一次 maps.Clone,
//	             值断言照常通过,而 controller/pricing.go 依赖的指针语义已经变了

// upstreamGetUserUsableGroups 是 service.GetUserUsableGroups 在**接入本扩展之前**
// 的函数体,逐行照抄(唯一的差别是最后那句 return 不经过 hook)。
//
// 照抄而不是调用真函数:真函数已经串上了 hook,拿它当参照系等于用被测对象证明
// 自己。这份副本一旦与上游漂移,TestUnsetScopeMatchesUpstreamBitForBit 里那些
// 带 +: / -: 差分的用例会先红 —— 那正是提醒"上游改了这段逻辑"的信号。
func upstreamGetUserUsableGroups(userGroup string) map[string]string {
	groupsCopy := setting.GetUserUsableGroupsCopy()
	if userGroup != "" {
		specialSettings, b := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Get(userGroup)
		if b {
			for specialGroup, desc := range specialSettings {
				if strings.HasPrefix(specialGroup, "-:") {
					delete(groupsCopy, strings.TrimPrefix(specialGroup, "-:"))
				} else if strings.HasPrefix(specialGroup, "+:") {
					groupsCopy[strings.TrimPrefix(specialGroup, "+:")] = desc
				} else {
					groupsCopy[specialGroup] = desc
				}
			}
		}
		if _, ok := groupsCopy[userGroup]; !ok {
			groupsCopy[userGroup] = "用户分组"
		}
	}
	return groupsCopy
}

// installBothUsableGroupHooks 装上生产 InstallHooks 装的那两个可选清单 hook。
func installBothUsableGroupHooks(t *testing.T) {
	t.Helper()
	prevResolve := service.QyResolveUsableGroups
	prevUnlock := service.QyPlanUnlockGroups
	service.QyResolveUsableGroups = groupmatrix.Resolve
	service.QyPlanUnlockGroups = Resolve
	t.Cleanup(func() {
		service.QyResolveUsableGroups = prevResolve
		service.QyPlanUnlockGroups = prevUnlock
	})
}

// TestUnsetScopeMatchesUpstreamBitForBit 是本轮"未设定范围 = 上游"的正式证明。
//
// 场景:扩展**已启用**、两层 hook **都已装上**、扩展库里**一条配置都没有**
// (没有 scope 行、没有 grant 行、没有套餐解锁绑定)。这正是本轮反转 default-deny
// 之后的默认状态,也是升级当天每一个部署的状态。
func TestUnsetScopeMatchesUpstreamBitForBit(t *testing.T) {
	gdb := newExtDB(t)
	newMainDB(t)
	require.NoError(t, gdb.AutoMigrate(groupmatrix.Mod{}.Tables()...))
	installBothUsableGroupHooks(t)
	setGroupRatios(t, `{"default":1,"vip":0.5,"pro":0.8,"lab":0}`)

	// 全局「用户可选分组」刻意**不含** pro / lab:本站真实形状就是这样。
	// 这一点是整条证明的关键 —— 若实现真的按"未设范围 = 全部模型分组"来做,
	// 下面的比对会立刻红在 pro / lab 上。
	prevUsable := setting.UserUsableGroups2JSONString()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(
		`{"default":"默认分组","vip":"VIP 分组"}`))
	t.Cleanup(func() { _ = setting.UpdateUserUsableGroupsByJSONString(prevUsable) })

	// +: / -: 差分也要在场:它们是上游那段逻辑里最容易被 hook 改写掉的部分。
	special := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
	prevSpecial := special.ReadAll()
	special.Set("agency", map[string]string{"+:pro": "代理专属", "-:vip": ""})
	t.Cleanup(func() {
		special.Clear()
		special.AddAll(prevSpecial)
	})

	// 扩展库是空的,但要真的**加载过一次**空快照:snapshot=nil 走的是另一条
	// fail-open 分支,只测它等于没测「配置为空」这一档。
	require.NoError(t, groupmatrix.InvalidateAndReload())
	require.NoError(t, reload())

	for _, userGroup := range []string{"", "default", "vip", "agency", "从来没配过的分组"} {
		t.Run("userGroup="+userGroup, func(t *testing.T) {
			want := upstreamGetUserUsableGroups(userGroup)

			assert.Equal(t, want, service.GetUserUsableGroups(userGroup),
				"零配置时 GetUserUsableGroups 必须与上游原实现逐位相同 —— "+
					"差一个 key 就是一批用户断服或一次分组泄漏")

			// 带身份的入口同样要逐位一致:用户没有任何套餐,解锁集合为空。
			assert.Equal(t, want, service.QyUsableGroupsForUser(7, userGroup),
				"没有任何套餐绑定时,带身份的入口必须与不带身份的结论逐位相同")
			assert.Equal(t, want, service.QyUsableGroupsForUser(0, userGroup),
				"匿名口径(userId<=0)必须与上游逐位相同")
		})
	}
}

// TestUnsetScopeKeepsTheUpstreamMapPointer 是上面那条的另一半。
//
// 值相等在功能上够用,但**指针相等**才能证明"两个 hook 都确实没碰它"。
// 有人哪天在恒等分支里顺手加一次 maps.Clone,值断言照常通过,而
// controller/pricing.go 继续往下传的那张 map 的指针语义已经变了。
func TestUnsetScopeKeepsTheUpstreamMapPointer(t *testing.T) {
	gdb := newExtDB(t)
	newMainDB(t)
	require.NoError(t, gdb.AutoMigrate(groupmatrix.Mod{}.Tables()...))
	installBothUsableGroupHooks(t)
	setGroupRatios(t, `{"default":1,"vip":0.5,"pro":0.8}`)
	require.NoError(t, groupmatrix.InvalidateAndReload())
	require.NoError(t, reload())

	in := map[string]string{"default": "默认分组", "vip": "VIP 分组"}
	for _, userGroup := range []string{"", "default", "从来没配过的分组"} {
		got := service.QyResolveUsableGroups(userGroup, in)
		assert.Equalf(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer(),
			"未设定范围时 QyResolveUsableGroups(%q) 必须返回入参那一张 map 的指针", userGroup)
	}
	for _, userId := range []int{0, 7, 1260} {
		got := service.QyPlanUnlockGroups(userId, in)
		assert.Equalf(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer(),
			"全站零解锁绑定时 QyPlanUnlockGroups(%d) 必须返回入参那一张 map 的指针", userId)
	}
}
