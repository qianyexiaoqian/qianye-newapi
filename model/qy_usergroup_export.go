package model

// qy_usergroup_export.go —— 「新用户默认分组」的 hook 声明。
//
// 与 qy_export.go / qy_commission_export.go 一样是纯新增文件,合并上游时冲突为 0。
// 单独成文件是为了让并行开发的各个扩展模块各写各的,不去争同一个文件。
//
// 铁律:本文件禁止 import 任何 qianye/* 包。model 是底层包,扩展依赖它,
// 反向依赖会成环。实现由 qianye.Init() 注入。

// QyResolveNewUserGroup 决定新建用户落库时的 users.group 取值。
//
// # 为什么挂在 prepareForInsert
//
// 全仓一共四条建号路径 —— 密码注册(controller/user.go Register)、微信注册
// (controller/wechat.go)、OAuth 注册(controller/oauth.go 的两个分支)、
// 管理员建号(controller/user.go CreateUser)—— 分别走 User.Insert 与
// User.InsertWithTx,而这两者共用 prepareForInsert。把 hook 挂在那里,
// 一行就覆盖全部四条路径,不会漏掉任何一条,也不会随上游新增建号入口而失效。
//
// # 契约
//
// 入参是调用方已经决定好的分组,返回值是最终落库的分组。
//
//   - 默认实现是恒等函数,因此上游调用点不需要 nil 判断,扩展未启用时
//     users.group 保持零值,由 model.User 上的 gorm:"default:'default'"
//     兜底成 default —— 与接入本 hook 之前的行为逐字一致。
//   - 实现方必须遵守「入参非空即原样返回」:调用方显式指定了分组(未来上游
//     可能让管理员建号时选分组)时,扩展不得越权覆盖。
//   - 实现方必须自行校验目标分组真实存在。返回一个不存在的分组会让新用户
//     匹配不到任何 abilities 行,表现为「注册成功但一个模型都调不通」。
//   - 实现体位于用户创建的主库事务内部,因此必须有硬超时并且 fail-open:
//     读不到扩展库时原样返回入参,绝不能阻塞注册。
//
// 赋值时机为 qianye.Init(),早于任何 HTTP 请求与后台协程,因此不存在并发
// 读写窗口 —— 也正因如此,运行期禁止改写本变量。
var QyResolveNewUserGroup = func(group string) string { return group }

// UpstreamDefaultUserGroup 是 model.User.Group 上 gorm:"default:'default'"
// 兜底出来的那个值。
//
// 它不是"某个默认配置",而是**数据库列默认值**的字面量:不给 Group 赋值时
// GORM 会把该列整个从 INSERT 里省掉,于是新用户落进的就是这个名字。
// 抄成第二份字符串的表现是"页面上说新用户进 A、库里进 B",所以凡是需要
// 回答"没配置时新用户在哪一档"的地方都必须引用它。
const UpstreamDefaultUserGroup = "default"

// QyNewUserGroup 回答一个**只读**的事实问题:此刻注册一个新用户,他会落进
// 哪一个用户分组。
//
// # 为什么不能拿 QyResolveNewUserGroup("") 当查询用
//
// 那个 hook 是**写入侧**的决策函数,它跑在用户创建事务里,语义是"把这一次
// 建号的分组定下来"。今天它恰好是幂等的,但它的契约里没有任何一条禁止将来
// 把新用户轮流分到几个分组里去 —— 一旦有人那么做,拿它当查询就会在每次
// 打开模型广场时消耗掉一个名额,而且从调用点完全看不出来。
// 读写分成两个变量,是让"看一眼"永远不可能变成"动一下"。
//
// # 契约
//
//   - **绝不返回空串。** 未配置、配置失效、扩展未启用,一律回落
//     UpstreamDefaultUserGroup —— 因为那三种情况下新用户确实落进 default。
//     返回空串会让调用方拿到"匿名口径"(见 service/qy_usablegroup_export.go
//     的契约 (b)),那是另一个完全不同的答案。
//   - 必须与 QyResolveNewUserGroup 同源:同一份配置、同一份缓存、同一道
//     "目标分组是否仍然存在"的校验。两者分家的表现是模型广场展示 A 的价格、
//     新用户注册进 B —— 一次可见的价格欺骗。
//   - 允许有 I/O(实现方带缓存与硬超时),因此**禁止**在 relay 热路径上调用。
//     现有调用点只有模型广场那一路(未登录访客的展示口径)。
var QyNewUserGroup = func() string { return UpstreamDefaultUserGroup }
