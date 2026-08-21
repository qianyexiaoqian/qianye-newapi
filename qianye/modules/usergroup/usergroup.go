// Package usergroup 让运营指定「新注册用户落进哪个分组」。
//
// # 上游现状
//
// 上游没有这个设置。model.User 的 Group 字段只靠 gorm:"default:'default'"
// 兜底:四条建号路径(密码注册、微信注册、OAuth 注册、管理员建号)全都不给
// Group 赋值,GORM 在 INSERT 时把零值列整个省掉,于是数据库默认值生效。
// 想把新用户放进别的分组,只能建号之后逐个手工改,或者去改数据库默认值。
//
// # 实现方式
//
// 上游改动只有一行:model/user.go 的 prepareForInsert 首行调用同包 hook
// model.QyResolveNewUserGroup(见 model/qy_usergroup_export.go)。选这个位置
// 是因为 User.Insert 与 User.InsertWithTx 都经过它,一行覆盖全部四条路径。
//
// # 三条不可动摇的安全约束
//
//  1. **未配置时必须与上游逐字一致**。没有配置行时 hook 原样返回入参(空串),
//     users.group 仍是零值,仍由数据库默认值兜底成 default。已有部署升级上来
//     不需要做任何事,行为不变。
//
//  2. **目标分组必须真实存在**。分组不是实体表,它只是散落在 GroupRatio、
//     abilities.group 等几处的字符串 key。把默认分组设成一个不存在的名字,
//     新用户就匹配不到任何 abilities 行 —— 注册成功、充值成功、但一个模型都
//     调不通,而且没有任何报错指向这个配置。因此保存时硬校验,**应用时再校验
//     一次**:分组可能在配置之后被运营从倍率表里删掉,只在保存时把关拦不住
//     这种漂移。
//
//  3. **绝不能阻塞注册**。hook 跑在用户创建的主库事务内部,扩展库一旦卡住就会
//     连带把主库事务拖住。因此读取有硬超时、有进程内缓存、失败即 fail-open。
package usergroup

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-gonic/gin"
)

// Mod 是新用户默认分组模块的注册入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "usergroup" }

// Tables 为空:配置只有一项,直接放共享的 qy_settings(地基表),
// 不值得为一行数据单开一张表。

// InstallHooks 无条件注入 hook,并预热一次配置缓存。
//
// 无条件注入是安全的:未配置时 resolveNewUserGroup 原样返回入参。反过来,
// 把注入包在「已配置」判断里会引入一个更糟的状态 —— 运营在管理端配好之后
// 必须重启进程才生效,而重启前后行为不同这件事在日志里完全看不出来。
//
// 预热的目的是让进程起来后的**第一次**注册就拿到正确分组:缓存冷的时候
// resolveNewUserGroup 会 fail-open 成上游默认值,那次注册就被放进 default 了。
// 预热失败不阻塞启动 —— 扩展库连不上时注册必须照常进行。
func (Mod) InstallHooks() {
	model.QyResolveNewUserGroup = resolveNewUserGroup
	// 只读查询侧。写入侧(上面那一行)与查询侧必须同时注入:只注入一半的表现是
	// 「新用户注册进 A,而未登录访客在模型广场上看到的是 default 的模型与价格」。
	model.QyNewUserGroup = newUserGroup
	warmDefaultGroup()
}

// RegisterAdminRoutes 挂管理端读写接口。
//
// 不新增 guard.Flag:本功能没有 YAML 开关(「是否启用」等价于「有没有配置
// 默认分组」),用 FlagCore 即「扩展已启用且扩展库可用」正是这里需要的语义。
// 多定义一个恒为 true 的开关只会多出一个没人消费的配置项。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/user-group/config", adminGetConfig)
	// 改这一项会决定此后**所有**新用户能不能用模型 —— 写一次影响的是全部
	// 未来账号,而不是某一个人,因此按关键操作限流并提到超级管理员。
	// 读不连坐:role=10 必须能看见当前值,否则连"为什么新用户没模型"都查不了。
	g.PUT("/user-group/config", middleware.CriticalRateLimit(),
		middleware.RootActionGate(middleware.RootActionUserGroupDefaultWrite), adminPutConfig)
}

func init() { module.Register(Mod{}) }
