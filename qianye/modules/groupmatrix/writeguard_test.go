package groupmatrix

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeguard_test.go —— 令牌写入侧校验的语义。
//
// 这一档的每一条规则都是为了不把用户挡在门外而存在的,所以它们比"能拦住非法分组"
// 更重要:拦不住的代价是多一条孤儿令牌(读侧还有最后一道闸),
// 挡错的代价是用户连自己的令牌都改不动。

func ctxWithUser(userId int) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Set("id", userId)
	return c
}

// TestCheckTokenGroupAlwaysAllowsTheEscapeHatches 覆盖前三条规则。
//
// 它们的共同点是"与清单无关,永远放行"。缺任何一条,某一类完全合法的操作
// 就会被挡死,而用户看到的只是一句"不能使用模型分组 X"。
func TestCheckTokenGroupAlwaysAllowsTheEscapeHatches(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "banned": 1})
	// vip 被接管且清单为空:任何分组都不该通过 —— 除了这三条豁免。
	seedScope(t, gdb, "vip", ModeEnforce, false)

	c := ctxWithUser(0) // 取不到用户 → 规则 6 也会放行,这里只测前三条
	assert.NoError(t, CheckTokenGroup(c, "banned", "banned"),
		"分组没变必须放行:否则用户改一个孤儿令牌的名字都会被挡,而他根本没碰分组")
	assert.NoError(t, CheckTokenGroup(c, "banned", ""),
		"置空必须放行:它是孤儿令牌唯一的自救出口")
	assert.NoError(t, CheckTokenGroup(c, "", "auto"),
		"auto 的合法性由上游 setTokenAutoGroups 管,这里不重复也不改变")
}

// useOwnerGroup 让测试能决定"令牌所有者属于哪个用户分组"。
//
// 生产路径走 model.GetUserCache,单测里没有主库,于是每一个用例都会停在
// fail-open 上 —— 那正是 writeguard 之前**唯一真正拦人的分支零覆盖**的原因:
// 七处断言全是 assert.NoError,把实现体整段写反也照样全绿。
func useOwnerGroup(t *testing.T, group string) {
	t.Helper()
	prev := ownerUserGroup
	ownerUserGroup = func(*gin.Context) (string, bool) { return group, true }
	t.Cleanup(func() { ownerUserGroup = prev })
}

// TestCheckTokenGroupRejectsUnlistedGroupUnderEnforce 覆盖规则 5 ——
// **写入侧唯一会真的拒绝的分支**。
//
// 它防的是这一类回归:把 `if _, granted := ...; granted { return nil }` 写成 `!granted`、
// 把 unmanaged 的提前返回挪到前面、或者让 ownerUserGroup 恒返回 false。
// 每一种的表现都一样:调用点还在(AST 守卫照样绿)、矩阵页照样显示已 enforce,
// 而用户继续能保存任意分组的令牌、继续在请求时收 403 —— 正是本轮要消灭的
// 「保存得下、一发请求就 403」,且这次连 AST 守卫都证明不了它没发生
// (AST 只能证明调用点在,证明不了实现体会拒绝)。
func TestCheckTokenGroupRejectsUnlistedGroupUnderEnforce(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "paid": 1, "free": 1})
	// vip 已 enforce,清单里只有 paid。
	seedScope(t, gdb, "vip", ModeEnforce, false, "paid")
	useOwnerGroup(t, "vip")

	c := ctxWithUser(7)

	err := CheckTokenGroup(c, "", "free")
	require.Error(t, err, "enforce 且不在清单里必须**拒绝** —— 这是写入侧闸门存在的全部理由")
	assert.Contains(t, err.Error(), "paid",
		"错误正文必须把当前可选清单一起给出来:只说「不许选 X」会让用户在下拉框里反复试错")

	assert.NoError(t, CheckTokenGroup(c, "", "paid"), "清单里的分组必须放行")

	// auto 与读侧同一个判据:AllowAuto=false 时读侧不会把 auto 注回可选 map,
	// 写侧放行只会造出一条永远发不出请求的新孤儿。
	autoErr := CheckTokenGroup(c, "", "auto")
	require.Error(t, autoErr,
		"allow_auto=false 时 auto 必须被拒:放行等于亲手造一条「保存得下、一发请求就 403」的令牌")

	// 空清单是合法配置(隔离组),文案必须说清楚而不是给一个空串。
	seedScope(t, gdb, "locked", ModeEnforce, false)
	useOwnerGroup(t, "locked")
	lockedErr := CheckTokenGroup(c, "", "paid")
	require.Error(t, lockedErr)
	assert.Contains(t, lockedErr.Error(), "可选清单为空")
}

// TestCheckTokenGroupAllowsAutoWhenScopeAllowsIt 是上一条的反面。
func TestCheckTokenGroupAllowsAutoWhenScopeAllowsIt(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1})
	seedScope(t, gdb, "vip", ModeEnforce, true)
	useOwnerGroup(t, "vip")

	assert.NoError(t, CheckTokenGroup(ctxWithUser(7), "", "auto"),
		"allow_auto=true 时读侧会把 auto 注回可选 map,写侧必须同步放行")
}

// TestCheckTokenGroupFailsOpenWithoutOwner 守规则 6 与规则 7。
//
// 取不到用户分组时必须放行。这条跑在建令牌的同步路径上,
// 挡住一次合法建号的代价大于放过一次非法分组。
func TestCheckTokenGroupFailsOpenWithoutOwner(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1})
	seedScope(t, gdb, "vip", ModeEnforce, false)

	assert.NoError(t, CheckTokenGroup(nil, "", "vip"), "没有 context 时必须放行")
	assert.NoError(t, CheckTokenGroup(ctxWithUser(0), "", "vip"), "拿不到用户 id 时必须放行")
}

// TestCheckTokenGroupIsGatedByTheSameSwitchAsTheReadSide 守的是
// 「一个状态同时控制读写两侧」。
//
// 不允许 enforce 从写侧先漏进来:那会造出"读侧还没收紧、写侧已经开始拒绝"的
// 中间态,用户被挡住却查不到任何原因(他的令牌明明还能用)。
func TestCheckTokenGroupIsGatedByTheSameSwitchAsTheReadSide(t *testing.T) {
	newTestDB(t)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1})

	useConfig(t, false) // 功能开关关闭
	require.NoError(t, reload())
	assert.NoError(t, CheckTokenGroup(ctxWithUser(1), "", "vip"),
		"功能开关关闭时写侧必须与读侧一样完全放行")

	useConfig(t, true)
	current.Store(nil) // 快照从未加载
	assert.NoError(t, CheckTokenGroup(ctxWithUser(1), "", "vip"),
		"快照未加载时读侧恒等返回,写侧必须同步放行")
}

// TestShadowModeRecordsWriteDenyWithoutBlocking 守影子期的**唯一**可归因证据源。
//
// 影子的全部意义是"配好了、先看看会挡掉谁"。不记录就没有证据,而没有证据的
// 影子期只是把 enforce 推迟了几天,并没有降低任何风险。
//
// 同时断言它**不阻断**:影子期挡住用户建令牌,就等于 enforce 从写侧提前生效了。
func TestShadowModeRecordsWriteDenyWithoutBlocking(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "paid": 1})
	seedScope(t, gdb, "vip", ModeShadow, false, "default")

	// 影子期的记录必须能归因到具体的 (用户分组, 模型分组),否则数字没法用。
	require.NoError(t, upsertWriteDeny(context.Background(), "vip", "paid", 42))
	require.NoError(t, upsertWriteDeny(context.Background(), "vip", "paid", 43))

	denies := listWriteDenies(gdb)
	require.Len(t, denies, 1, "同一个组合必须折叠成一行累加,而不是每次写一行")
	assert.EqualValues(t, 2, denies[0].Count)
	assert.Equal(t, "vip", denies[0].UserGroup)
	assert.Equal(t, "paid", denies[0].ModelGroup)
	assert.EqualValues(t, 43, denies[0].SampleUserId, "样本用户取最近一次,便于抽查")
	assert.Positive(t, denies[0].FirstSeen)
}

// TestUpstreamHookVariablesAreWiredByInstallHooks 守的是"实现写好了但没接上"。
//
// 这是本仓反复出现的第一种缺陷形状。两个 hook 的实现可以做得完美无缺,
// 只要 InstallHooks 里少一行赋值,整套收紧就完全不生效,而管理端矩阵
// 看起来配得好好的、列表页也完全正常。
func TestUpstreamHookVariablesAreWiredByInstallHooks(t *testing.T) {
	prevResolve, prevCheck := service.QyResolveUsableGroups, service.QyCheckTokenGroupChange
	t.Cleanup(func() {
		service.QyResolveUsableGroups = prevResolve
		service.QyCheckTokenGroupChange = prevCheck
	})
	service.QyResolveUsableGroups = func(_ string, u map[string]string) map[string]string { return u }
	service.QyCheckTokenGroupChange = func(*gin.Context, string, string) error { return nil }

	useConfig(t, false) // 关闭时也必须注入,否则 enabled 就不是热载开关了
	Mod{}.InstallHooks()

	assert.Equal(t,
		reflect.ValueOf(Resolve).Pointer(),
		reflect.ValueOf(service.QyResolveUsableGroups).Pointer(),
		"service.QyResolveUsableGroups 没有接上本模块的 Resolve —— 整套收紧完全不生效,"+
			"而管理端矩阵看起来配得好好的")
	assert.Equal(t,
		reflect.ValueOf(CheckTokenGroup).Pointer(),
		reflect.ValueOf(service.QyCheckTokenGroupChange).Pointer(),
		"service.QyCheckTokenGroupChange 没有接上 —— 令牌写入侧继续不校验,孤儿会继续增加")
}
