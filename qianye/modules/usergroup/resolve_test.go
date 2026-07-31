package usergroup

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 未配置时行为必须与接入本模块之前逐字一致。
//
// 这是向后兼容的底线:已有部署升级上来、扩展启用、模块注册、hook 装上,
// 但没人去管理端配过默认分组 —— 新用户必须仍然落在 default。
//
// 断言的是**数据库里真正存下来的值**而不是 resolveNewUserGroup 的返回值:
// 兜底是 model.User 上的 gorm:"default:'default'" 做的,只有真的插一行才验得到。
func TestNewUserGroup_UnconfiguredKeepsUpstreamDefault(t *testing.T) {
	newExtDB(t)
	mainDB := newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":1}`)
	installHook(t)

	assert.Equal(t, upstreamDefaultGroup, insertUser(t, mainDB, "alice", ""))
}

// 配置了默认分组之后,新用户必须真的落进那个分组。
//
// 本测试是整条链路的验收点:它同时覆盖 ① 管理端写进 qy_settings 的值能被读到、
// ② hook 被 InstallHooks 注入、③ **上游 model/user.go 的 prepareForInsert 里
// 那一行同包调用还在**。把那一行删掉,本测试立刻失败,而任何只测
// resolveNewUserGroup 返回值的单测都不会有反应 —— 这正是本项目反复踩过的
// 「纯函数层做对了不算数」。
func TestNewUserGroup_AppliesConfiguredGroup(t *testing.T) {
	extDB := newExtDB(t)
	mainDB := newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8}`)
	installHook(t)

	seedDefaultGroup(t, extDB, "vip")

	assert.Equal(t, "vip", insertUser(t, mainDB, "bob", ""))
}

// 调用方已经显式指定分组时,扩展不得越权覆盖。
func TestNewUserGroup_ExplicitGroupWins(t *testing.T) {
	extDB := newExtDB(t)
	mainDB := newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8,"svip":0.5}`)
	installHook(t)

	seedDefaultGroup(t, extDB, "vip")

	assert.Equal(t, "svip", insertUser(t, mainDB, "carol", "svip"))
}

// 配置的分组事后被运营从倍率表里删掉时,必须回落到上游默认分组。
//
// 没有这道应用期再校验,一次分组重命名就会让此后所有新用户拿到一个不存在的
// 分组 —— 注册成功、充值成功、一个模型都调不通,而且没有任何报错指向这条配置。
func TestNewUserGroup_StaleConfiguredGroupFallsBack(t *testing.T) {
	extDB := newExtDB(t)
	mainDB := newMainDB(t)
	// 倍率表里只有 default:配置时存在的 vip 已经被删掉了。
	useGroupRatio(t, `{"default":1}`)
	installHook(t)

	seedDefaultGroup(t, extDB, "vip")

	assert.Equal(t, upstreamDefaultGroup, insertUser(t, mainDB, "dave", ""))
}

// auto 是令牌的自动分组,abilities 里不存在 group='auto' 的行。
// 即使运营手工把它塞进倍率表,也不能作为用户分组下发。
func TestNewUserGroup_AutoGroupIsNeverApplied(t *testing.T) {
	extDB := newExtDB(t)
	mainDB := newMainDB(t)
	useGroupRatio(t, `{"default":1,"auto":1}`)
	installHook(t)

	seedDefaultGroup(t, extDB, autoGroup)

	assert.Equal(t, upstreamDefaultGroup, insertUser(t, mainDB, "erin", ""))
}

// 扩展库不可用时必须 fail-open:注册照常进行,分组回落到上游默认值。
//
// hook 跑在用户创建的主库事务内部,任何「读不到就报错」的写法都会把扩展库的
// 抖动直接变成注册失败。
func TestNewUserGroup_ExtensionDBDownFailsOpen(t *testing.T) {
	extDB := newExtDB(t)
	mainDB := newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8}`)
	installHook(t)

	seedDefaultGroup(t, extDB, "vip")
	// 先确认配置本身是生效的,否则下面的断言可能因为别的原因而通过。
	require.Equal(t, "vip", insertUser(t, mainDB, "frank", ""))

	// 熔断打开 / 连接不健康。
	qyDBHealthy.Store(false)
	resetCache()

	assert.Equal(t, upstreamDefaultGroup, insertUser(t, mainDB, "grace", ""))
}

// hook 未安装(扩展禁用)时,model 侧的默认实现必须是恒等函数。
func TestQyResolveNewUserGroup_DefaultImplIsIdentity(t *testing.T) {
	assert.Equal(t, "", model.QyResolveNewUserGroup(""))
	assert.Equal(t, "vip", model.QyResolveNewUserGroup("vip"))
}
