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
