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

// 只读查询侧的默认实现必须给出**上游兜底分组**,而不是空串。
//
// 空串在 service.GetUserUsableGroups 里是「匿名口径」,与「新用户会落进
// default」是两个截然不同的答案 —— 模型广场的未登录展示直接消费这个返回值,
// 返回空串会让扩展禁用的站点重新退回"未登录看到空页面"。
func TestQyNewUserGroup_DefaultImplIsUpstreamDefault(t *testing.T) {
	assert.Equal(t, model.UpstreamDefaultUserGroup, model.QyNewUserGroup())
	assert.Equal(t, upstreamDefaultGroup, model.UpstreamDefaultUserGroup,
		"本模块的兜底常量与上游那一份必须是同一个字符串,抄成两份就会说两句话")
}

// TestNewUserGroupAgreesWithResolveNewUserGroup —— 只读查询侧与写入侧必须永远
// 给出同一个分组。
//
// 断言的右侧是**数据库里真正落下的值**(走 model.User 的真实插入路径),
// 左侧是模型广场未登录展示所消费的那个查询。两者分家的表现是:
// 访客在价格页上看到 A 的模型与倍率,注册完发现自己在 B ——
// 一次页面级的价格欺骗,而且两侧各自的单测都会是绿的。
func TestNewUserGroupAgreesWithResolveNewUserGroup(t *testing.T) {
	cases := []struct {
		name       string
		groupRatio string
		configured string
		want       string
	}{
		{
			name:       "未配置回落上游兜底分组",
			groupRatio: `{"default":1,"vip":0.8}`,
			configured: "",
			want:       upstreamDefaultGroup,
		},
		{
			name:       "配置生效",
			groupRatio: `{"default":1,"vip":0.8}`,
			configured: "vip",
			want:       "vip",
		},
		{
			name:       "配置的分组已被删掉时回落",
			groupRatio: `{"default":1}`,
			configured: "vip",
			want:       upstreamDefaultGroup,
		},
		{
			name:       "auto 永远不作为用户分组下发",
			groupRatio: `{"default":1,"auto":1}`,
			configured: autoGroup,
			want:       upstreamDefaultGroup,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extDB := newExtDB(t)
			mainDB := newMainDB(t)
			useGroupRatio(t, tc.groupRatio)
			installHook(t)
			if tc.configured != "" {
				seedDefaultGroup(t, extDB, tc.configured)
			}

			assert.Equal(t, tc.want, model.QyNewUserGroup(), "只读查询侧")
			assert.Equal(t, tc.want, insertUser(t, mainDB, "qy-agree-user", ""), "写入侧(库里真正落下的值)")
		})
	}
}

// 扩展库不可用时,只读查询侧必须与写入侧一起 fail-open 到上游兜底分组。
//
// 单独列一条是因为这条路径上两侧的代码不同:写入侧靠"返回空串 → 数据库列默认值",
// 查询侧没有数据库兜底,必须自己给出 upstreamDefaultGroup。
func TestNewUserGroupExtensionDBDownFallsBackToUpstreamDefault(t *testing.T) {
	extDB := newExtDB(t)
	newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8}`)
	installHook(t)
	seedDefaultGroup(t, extDB, "vip")
	require.Equal(t, "vip", model.QyNewUserGroup())

	qyDBHealthy.Store(false)
	resetCache()

	assert.Equal(t, upstreamDefaultGroup, model.QyNewUserGroup())
}
