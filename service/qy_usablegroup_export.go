package service

// qy_usablegroup_export.go —— 千夜扩展「用户分组的权威可选模型分组清单」与上游
// service 包之间的唯一耦合面。
//
// 这是纯新增文件,合并上游时冲突为 0。两个 hook 变量都带 no-op 默认实现,因此
// 扩展未启用(甚至整个 qianye 目录被删掉)时行为与上游逐位一致,调用点无需 nil 判断。
//
// ══════════════════════════ 四条硬规则(实现方必须遵守)══════════════════════════
//
//  1. **本文件禁止 import 任何 qianye/* 包。** service 是被扩展依赖的下层包,
//     反向依赖会成环。实现体一律在 qianye.Init() 里注入。
//
//  2. **实现体禁止调用 GetUserUsableGroups / GroupInUserUsableGroups /
//     IsUserSelectableGroup / GetUserAutoGroup / FilterUserTokenAutoGroups。**
//     QyResolveUsableGroups 就挂在 GetUserUsableGroups 的 return 上,回调进去
//     就是无限递归 —— 表现是整个进程栈溢出崩溃,而不是某个接口出错。
//
//  3. **实现体禁止查库、禁止取互斥锁、禁止 context 等待。**
//     QyResolveUsableGroups 挂在 middleware/auth.go 的令牌分组校验上,
//     每一个带令牌分组的 relay 请求调用一次。只允许 atomic.Pointer.Load
//     与 map 查找;任何一次 I/O 就是全站延迟。
//
//  4. **赋值只在 qianye.Init() 发生一次**,早于 HTTP 监听与后台协程,
//     因此不存在并发读写窗口 —— 也正因如此,运行期禁止改写这两个变量。

import "github.com/gin-gonic/gin"

// QyResolveUsableGroups 决定一个用户分组最终能选哪些模型分组。
//
// ─────────────────────── 为什么挂在函数最后一条语句上 ───────────────────────
//
// 上游 GetUserUsableGroups 的最后一步是「如果 userGroup 不在结果里,就把它补回去」
// (无条件自我补入)。挂在更早的位置,上游会在 hook 之后再把 userGroup 塞回来,
// 「一个用户分组可以不包含它自己」就永远做不到 —— 本站那条 `-:自己` 的
// GroupSpecialUsableGroup 规则至今无效,根因正是这一步。
//
// ─────────────────────── 逐位一致契约(实现方必须满足)───────────────────────
//
// 以下四种情况必须 `return upstream`,返回**同一个 map 指针**,不复制、不增删、
// 不重排 —— 指针相等是「未配置 = 上游」最强的可证形式,而 controller/pricing.go
// 会把这张 map 继续往下传,指针语义本来就是调用方依赖的:
//
//	(a) 扩展未启用 / 该功能开关关闭
//	(b) userGroup == ""(匿名口径:模型广场、可用率、价格表的匿名基准都依赖它)
//	(c) 该用户分组没有被扩展接管(未配置权威清单)
//	(d) 扩展的清单快照从未成功加载过
//
// 被接管时必须**新分配**一张 map 返回,绝不返回实现方内部快照持有的那一张:
// 调用方 delete 一个 key 就会污染全站鉴权,而且无法复现。
var QyResolveUsableGroups = func(userGroup string, upstream map[string]string) map[string]string {
	return upstream
}

// QyCheckTokenGroupChange 在**写入侧**校验令牌分组的可选性。
//
// 上游的不对称是结构性的:AddToken / UpdateToken 完全不校验 token.Group,
// 校验只存在于请求时(middleware/auth.go)。于是「保存得下、一发请求就 403」
// 的孤儿令牌可以被无限制地创建出来 —— 本站现存 545 条。
//
// oldGroup 是库里的旧值(新建时为空串),newGroup 是本次请求要写入的值。
// 返回非 nil 时调用方直接 common.ApiError 并 return。
//
// 实现方必须遵守的语义(缺一条都会把用户挡在门外):
//
//	newGroup == oldGroup   → nil。只校验真的改了分组的那一次,否则用户改一个
//	                          孤儿令牌的名字都会被挡,而他根本没碰分组。
//	newGroup == ""         → nil。回落用户分组永远合法,而且这是孤儿令牌唯一的自救出口。
//	newGroup == "auto"     → nil。auto 的合法性由既有的 setTokenAutoGroups 管。
//	任何内部错误 / panic   → nil(fail-open)。这条跑在用户建令牌的同步路径上,
//	                          挡住一次合法建号的代价大于放过一次非法分组 ——
//	                          读侧还有 middleware/auth.go 那道最后的闸。
//
// 读取侧的检查保持不动:写入侧方案永远不应该让人以为可以省掉最后那道闸。
var QyCheckTokenGroupChange = func(c *gin.Context, oldGroup, newGroup string) error {
	return nil
}

// QyPlaygroundGroupAllowed 判定 playground 的分组覆盖请求是否放行。
//
// ─────────────────────── 为什么这条必须单独挂一个 hook ───────────────────────
//
// /pg/chat/completions 走的是 UserAuth 而不是 TokenAuth,而 ContextKeyUsingGroup
// **只在 TokenAuth 里写入**。于是 middleware/distributor.go 在这条路径上拿到的
// usingGroup 恒为空串,上游那句
//
//	GroupInUserUsableGroups("", 请求分组) || 请求分组 == ""
//
// 实际比对的是**匿名口径**的全局白名单 —— QyResolveUsableGroups 对空用户分组
// 恒等返回上游(契约 (b)),权威清单在这条路径上一条都不生效。
// 结果是任何已登录用户都能在 playground 里把分组覆盖成站内任意仍在全局白名单里的
// 模型分组(本站有三个 ratio=0 的免费分组),而管理端显示他只能选自己那一档。
//
// 默认实现与上游那一行**逐位同义**(短路顺序一致、结果一致),所以扩展未启用时
// 行为一个字节都不变。实现方要做的只有一件事:把空的 usingGroup 换成
// **令牌所有者的用户分组**再判定。
//
// c 可能为 nil(理论上不会,但实现方必须自己防)。任何内部错误一律 fail-open,
// 回落上游语义 —— 挡住一次合法的 playground 请求不比放过一次覆盖更可接受,
// 而真正的最后一道闸在计费与渠道选择那一侧。
var QyPlaygroundGroupAllowed = func(c *gin.Context, usingGroup, requestedGroup string) bool {
	return GroupInUserUsableGroups(usingGroup, requestedGroup) || requestedGroup == usingGroup
}
