package groupmatrix

// autoorder_test.go —— 按用户分组的默认 auto 顺序,以及它的两处同步。
//
// 项目方要求两件事:
//  1. 默认 auto 顺序按**用户分组**单独配,不再是全站一份
//  2. 管理员删除 / 取消授权某个模型分组时,这份顺序要跟着同步

import (
	"testing"

	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoOrderCodecDropsDuplicatesAndBlanks 编解码的两条不变式。
//
// 重复项会让 auto 在同一个池子上试两次(第二次必然同样失败),空串会变成一个
// 查不到任何渠道的分组名 —— 两者都只是在故障转移时白白多烧一次上游超时。
func TestAutoOrderCodecDropsDuplicatesAndBlanks(t *testing.T) {
	assert.Equal(t, "a,b", joinAutoOrder([]string{"a", "b", "a", "", "  ", "b"}))
	assert.Equal(t, "", joinAutoOrder(nil))
	assert.Equal(t, []string{"a", "b"}, splitAutoOrder("a, b ,"))
	assert.Nil(t, splitAutoOrder("   "), "空串解成 nil;由 UserAutoGroups 再决定它对外是什么")
}

// TestUserAutoGroupsHasNoGlobalFallback 钉住「没有兜底」这条拍板口径。
//
// nil 只留给「这一档根本没有 scope 行」(未接管 = 上游口径)。一旦有 scope 行,
// 配了什么就是什么:没配 auto 顺序 = 没有 auto 分组,**不回落全站清单**。
//
// 回落全局的实际后果说不通:全局清单里的分组未必在这一档的授权清单里,
// 回落之后还要被逐个滤掉,运营看到的是「我没配,它却按另一份我看不见的清单在转」。
func TestUserAutoGroupsHasNoGlobalFallback(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"vip": 1, "paid": 1, "free": 1})

	// 没有 scope 行 → nil(调用方回落全局清单)
	assert.Nil(t, UserAutoGroups("vip"), "未设范围必须回 nil")

	// 有 scope 行、allow_auto 开、auto_order 空 → **空切片**,不是 nil
	seedScope(t, gdb, "vip", ModeEnforce, true, "paid", "free")
	syncHotAsync(t)
	require.NoError(t, reload())
	unset := UserAutoGroups("vip")
	require.NotNil(t, unset, "接管了就是权威的:没配 auto 顺序 = 没有 auto 分组,不回落全站清单")
	assert.Empty(t, unset)

	// allow_auto 关 → 空切片(明确不试任何分组),**不是** nil
	require.NoError(t, gdb.Model(&Scope{}).Where("user_group = ?", "vip").
		Update("allow_auto", false).Error)
	syncHotAsync(t)
	require.NoError(t, reload())
	got := UserAutoGroups("vip")
	require.NotNil(t, got, "禁用 auto 必须回空切片而不是 nil —— 回 nil 会让这个开关被全局清单绕过")
	assert.Empty(t, got)
}

// TestUserAutoGroupsReturnsConfiguredOrder 配了就按配的顺序来。
func TestUserAutoGroupsReturnsConfiguredOrder(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"vip": 1, "paid": 1, "free": 1})

	seedScope(t, gdb, "vip", ModeEnforce, true, "paid", "free")
	require.NoError(t, gdb.Model(&Scope{}).Where("user_group = ?", "vip").
		Update("auto_order", "free,paid").Error)
	syncHotAsync(t)
	require.NoError(t, reload())

	assert.Equal(t, []string{"free", "paid"}, UserAutoGroups("vip"),
		"顺序就是语义:auto 从上往下试到第一个可用的分组为止")
}

// TestRevokeGrantDropsItFromAutoOrder 取消授权时顺序要跟着同步。
//
// 不同步的话运行期仍然安全(GetUserAutoGroup 按当前权限过滤),但管理端会把
// 那一项继续显示在顺序里 —— 运营看到「A → 已撤销的 B → C」,实际执行「A → C」。
func TestRevokeGrantDropsItFromAutoOrder(t *testing.T) {
	gdb := newTestDB(t)
	seedScope(t, gdb, "vip", ModeEnforce, true, "paid", "free")
	require.NoError(t, gdb.Model(&Scope{}).Where("user_group = ?", "vip").
		Update("auto_order", "paid,free").Error)

	require.NoError(t, dropFromAutoOrder(gdb, "vip", "paid"))

	var scope Scope
	require.NoError(t, gdb.Where("user_group = ?", "vip").Take(&scope).Error)
	assert.Equal(t, "free", scope.AutoOrder)
}

// TestSweepModelGroupDropsItFromEveryAutoOrder 删模型分组时全表同步。
//
// 用一对有前缀关系的名字:天真的 SQL 字符串替换会把「浅梦号池测试2」里的
// 「浅梦号池测试」一起换掉,而分组名之间存在前缀关系完全正常。
func TestSweepModelGroupDropsItFromEveryAutoOrder(t *testing.T) {
	gdb := newTestDB(t)
	seedScope(t, gdb, "vip", ModeEnforce, true, "paid")
	seedScope(t, gdb, "free", ModeEnforce, true, "paid")
	require.NoError(t, gdb.Model(&Scope{}).Where("user_group = ?", "vip").
		Update("auto_order", "浅梦号池测试,浅梦号池测试2,paid").Error)
	require.NoError(t, gdb.Model(&Scope{}).Where("user_group = ?", "free").
		Update("auto_order", "paid").Error)

	require.NoError(t, sweepAutoOrder(gdb, "浅梦号池测试"))

	var vip, free Scope
	require.NoError(t, gdb.Where("user_group = ?", "vip").Take(&vip).Error)
	require.NoError(t, gdb.Where("user_group = ?", "free").Take(&free).Error)
	assert.Equal(t, "浅梦号池测试2,paid", vip.AutoOrder,
		"前缀相同的另一个分组绝不能被一起摘掉")
	assert.Equal(t, "paid", free.AutoOrder, "没有这一项的档必须原样不动")
}

// TestGetUserAutoGroupPrefersPerGroupOrder 端到端:上游解析走的是这一档的顺序。
//
// 这一条穿过 service.QyUserAutoGroups 那个挂载点,证明"按用户分组配"真的接上了 ——
// 只测 UserAutoGroups 本身证明不了这一点(hook 没注入时它照样返回正确结果)。
func TestGetUserAutoGroupPrefersPerGroupOrder(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"vip": 1, "paid": 1, "free": 1})

	seedScope(t, gdb, "vip", ModeEnforce, true, "paid", "free")
	require.NoError(t, gdb.Model(&Scope{}).Where("user_group = ?", "vip").
		Update("auto_order", "free,paid").Error)
	syncHotAsync(t)
	require.NoError(t, reload())

	// 两个 hook 都要接:GetUserAutoGroup 会对每个候选跑 IsUserSelectableGroup,
	// 而那条链路走的是 QyResolveUsableGroups。只接 QyUserAutoGroups 的话,
	// 候选会被"这一档什么都不能选"逐个滤掉,结果恒为空 —— 那是夹具没搭对,
	// 不是被测行为出错。
	prevAuto, prevResolve := service.QyUserAutoGroups, service.QyResolveUsableGroups
	service.QyUserAutoGroups = UserAutoGroups
	service.QyResolveUsableGroups = Resolve
	t.Cleanup(func() {
		service.QyUserAutoGroups = prevAuto
		service.QyResolveUsableGroups = prevResolve
	})

	assert.Equal(t, []string{"free", "paid"}, service.GetUserAutoGroup("vip"),
		"上游必须用这一档配的顺序,而不是全站那份")
}
