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
//     **唯一的例外是 QyPlanUnlockGroups**,它的解锁集合挂在用户身上、只能查主库。
//     那条例外的完整边界(缓存、硬超时、两段式失败方向)写在它自己的注释里,
//     实现方不得反过来拿它给其它 hook 免责。
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

// QyPlanUnlockGroups 把「这个用户持有的订阅套餐额外解锁的模型分组」并入可选清单。
//
// ───────────────────── 为什么必须把 userId 送进来 ─────────────────────
//
// GetUserUsableGroups(userGroup) 在签名处就把用户身份丢掉了,而套餐挂在**人**
// 身上、且项目方明确要求「不绑定用户组」。要么把身份传进来,要么放弃这条需求 ——
// 没有第三条路:goroutine-local 不存在;把解锁写进 users.group 直接违反
// 「不绑定用户组」;写成 per-group 的清单表达不了 per-user。
//
// 传 userId 而不是 *gin.Context 是刻意的:全部调用点都已经持有用户 id,传 c 会
// 诱使实现体去读请求状态,并让这个函数在非 HTTP 路径(结算、对账、管理端预览)
// 上不可用、不可单测;而排障时「这一笔为什么放行了」必须能由一个显式身份回答。
//
// ─────────────────────── 解析顺序:范围是替换,套餐是并集 ───────────────────────
//
//	usable(userId, userGroup) = QyResolveUsableGroups(userGroup, 上游) ∪ 套餐解锁
//
// 并集叠在替换**之后**:用户买了套餐就一定拿得到,与他在哪个用户分组里无关。
// 这就是「不绑定用户组」在解析顺序上的落点。
//
// ─────────────────────── 逐位一致契约(实现方必须满足)───────────────────────
//
// 以下情况必须 `return upstream`,返回**同一个 map 指针**:
//
//	(a) 扩展未启用 / 本功能开关关闭
//	(b) userId <= 0(匿名口径:模型广场、可用率、价格表的匿名基准全靠这一条)
//	(c) 全站没有任何套餐配过解锁绑定 —— 这一条让「上线当天零行为变化、零新增 I/O」
//	    成为结构性事实,而不是某个配置默认值的副产品
//	(d) 该用户没有任何解锁
//
// 有解锁时必须**新分配**一张 map(拷贝 upstream 再并入解锁项),绝不返回实现方
// 内部快照持有的那一张,也不得改写 upstream —— 调用方 delete 一个 key 就会污染
// 全站鉴权,而且无法复现。解锁项的描述文案一律走 setting.GetUsableGroupDescription,
// 与上游同源;**auto 永远不会出现在解锁集合里**。
//
// ─────────────────────── 它的契约比同文件的另外三个**弱** ───────────────────────
//
// 本文件顶部第 3 条(禁止查库)对本变量**不适用**:套餐是 per-user 数据,不可能
// 全量塞进进程内快照。实现方允许在「第二层缓存未命中」这一条冷分支上查一次主库,
// 但必须自带硬超时(50ms 量级),且必须先被 (c) 短路挡住 —— 也就是说没有任何
// 套餐配过绑定时,这个 hook 一次 I/O 都不会发生。这一点必须写在实现体的注释里,
// 不得默默沿用「禁止查库」那一条(它会让下一个人以为这里也是零 I/O)。
//
// 未命中且加载失败时的方向是 **fail-closed**(视为无解锁):无法区分「没有套餐」
// 与「读失败」,失败时放行等于让付费解锁变成免费。代价是付费用户偶发 403,
// 因此实现方必须出一个健康面板计数器,并把「加载失败」与「确实无权」在日志里分开 ——
// 这条路径的表现就是「少数用户偶发 403」,分不开就永远查不出来。
var QyPlanUnlockGroups = func(userId int, upstream map[string]string) map[string]string {
	return upstream
}

// QyPlanUnlockedGroup 判定某个模型分组是不是**由该用户持有的套餐**解锁的。
//
// 它服务的是**写入侧**:QyCheckTokenGroupChange 的实现体按「用户分组 → 可选清单」
// 判定,而套餐解锁是 per-user 的,那份清单里没有它。不接这一条,读侧放行、写侧
// 拒绝,用户会遇到「能用却存不下」——两侧口径分叉是本仓已经修过一次的形状。
//
// 判据必须与 QyPlanUnlockGroups 完全同源(同一份快照、同一份缓存),否则两个
// 函数会在同一时刻给出不同的答案。fail 方向与写侧一致:任何内部错误一律
// return false(把判定交回原有逻辑),绝不因为读不到就放行。
var QyPlanUnlockedGroup = func(userId int, groupName string) bool {
	return false
}

// QyUsableGroupsForUser 是「带用户身份」的可选清单入口。
//
// 它不是 hook,是一个普通函数:先走上游 + 权威清单(QyResolveUsableGroups 挂在
// GetUserUsableGroups 的 return 上),再并入套餐解锁。调用点只需把手上已有的
// userId 传进来,顺序由这里保证,不必每处各拼一次 —— 拼错一次的表现是
// 「某个页面看得到、另一个页面看不到」。
//
// userId <= 0 时逐位等价于 GetUserUsableGroups(userGroup)(见上面的契约 (b))。
func QyUsableGroupsForUser(userId int, userGroup string) map[string]string {
	return QyPlanUnlockGroups(userId, GetUserUsableGroups(userGroup))
}

// 刻意**没有** QyIsSelectableGroupForUser。
//
// IsUserSelectableGroup 在全仓只剩三个调用点,而它们**全部**是 auto 自动分组的
// 候选筛选(service/group.go 的 GetUserAutoGroup / FilterUserTokenAutoGroups,
// 以及 controller/token.go 的 setTokenAutoGroups 写入校验)。
//
// 套餐解锁的模型分组**不参与 auto**,这是拍板口径,理由在资金侧:
// relay/helper/price.go 里 auto 命中后会**改写 relayInfo.UsingGroup**,而 UsingGroup
// 正是套餐余额「仅限/通用」过滤的判据。若解锁分组能被 auto 选中,一次上游重试就会
// 在用户完全没有指定的情况下改变这笔请求的**出资来源**(钱包 ↔ 套餐余额),
// 而 auto 的候选来自另一条链路、界面上完全看不出来。
//
// 因此给这三处任何一处套上 per-user 版本都是错的:token.go 那一处若放宽,
// 用户能把解锁分组**存进** auto 列表,而读侧的 FilterUserTokenAutoGroups 会在
// 每次请求时把它悄悄滤掉 —— 「存下了但永远不生效」,比直接拒绝更难查。
//
// 普通令牌分组(token.Group,非 auto)的写入侧由 QyCheckTokenGroupChange 负责,
// 它已经能拿到 *gin.Context 里的用户 id,不需要新的上游改动。

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
