package service

// qy_groupns_export.go —— 千夜扩展「用户分组 / 模型分组彻底分家」与上游 service 包
// 之间的唯一耦合面。纯新增文件,合并上游时冲突为 0。
//
// 三个 hook 都带 no-op 默认实现,因此扩展未启用(甚至整个 qianye 目录被删掉)时
// 行为与上游逐位一致,调用点无需 nil 判断。
//
// ══════════════════════════ 四条硬规则(实现方必须遵守)══════════════════════════
//
//  1. **本文件禁止 import 任何 qianye/* 包。** service 是被扩展依赖的下层包,
//     反向依赖会成环。实现体一律在 qianye.Init() 里注入。
//
//  2. **QyResolveDefaultModelGroup 与 QyGroupRatioMissingDenied 禁止查库、
//     禁止取互斥锁、禁止 context 等待。** 它们挂在 middleware/auth.go 上,
//     每一个空分组令牌的 relay 请求各调用一次。只允许 atomic.Pointer.Load 与 map 查找。
//
//  3. **赋值只在 qianye.Init() 发生一次**,早于 HTTP 监听与后台协程,
//     因此不存在并发读写窗口 —— 也正因如此,运行期禁止改写这三个变量。
//
//  4. **fail 方向一律倒向"和今天一样"。** 未启用、快照没加载、名字没登记 ——
//     全部必须退化成上游原行为,而不是拒绝。这三个 hook 里任何一个 fail-closed,
//     表现都是"一整组用户发不出请求"。

// 默认模型分组的三态。与 qianye/modules/groupns 的常量同值,两边共用同一组字面量。
const (
	// QyDefaultModeInherit:没配 → UsingGroup = users.group,**逐位等于今天**。
	QyDefaultModeInherit = "inherit"
	// QyDefaultModePin:配了一个具体的模型分组。
	QyDefaultModePin = "pin"
	// QyDefaultModeDeny:显式拒绝兜底(隔离组 / 封禁组)。
	QyDefaultModeDeny = "deny"
)

// QyResolveDefaultModelGroup 解析「这个用户分组在**没有令牌分组**时该用哪个模型分组」。
//
// ═══════════════ 它解决的是一个已经在线上发生的 503 ═══════════════
//
// 令牌没有指定分组时,上游 middleware/auth.go 让 UsingGroup = users.group,再拿它
// 去 abilities 选渠道。用户分组一旦不再兼作模型分组,就一个渠道都没有:
//
//	503 No available channel for model X under group <用户分组>
//
// 这条路径上今天既没有 hook,也没有 ContainsGroupRatio 校验 —— 上游那一行是
// `userGroup = userCache.Group` 的直接赋值,所以必须直接改上游。
//
// ─────────────────────── 返回值契约 ───────────────────────
//
//	(userGroup, QyDefaultModeInherit)  调用方一个字节都不改。**默认实现恒走这一档。**
//	(M,         QyDefaultModePin)      调用方按显式令牌分组的**同一套校验**放行到 M
//	("",        QyDefaultModeDeny)     调用方 403
//
// mode 取到未知值时调用方必须按 inherit 处理:未知配置退化成"今天的行为"。
//
// **实现方不做 pin 的合法性校验**(可选清单 + 分组倍率表)。那两道校验在调用点上
// 与显式令牌分组走同样的两行 —— 放到实现体里会让两条路径的判据分家,
// 而分家的表现是"令牌指定 X 能用、默认解析出 X 却被挡",同一个人两个答案。
var QyResolveDefaultModelGroup = func(userId int, userGroup string) (group string, mode string) {
	return userGroup, QyDefaultModeInherit
}

// QyDeclaredUserGroups 返回**已声明但可能还没有任何用户**的用户分组名。
//
// 它补上 `SELECT DISTINCT users.group` 的那个盲区:一个刚建出来的新档次天然还没有
// 人,而"为新档次创建套餐"恰恰是套餐 upgrade_group 最正常的用法。只认
// users.group 会让这件事在结构上不可能完成(见 controller/subscription.go 的
// validatePlanUserGroups)。
//
// 冷路径(管理端保存套餐时一次)。默认返回 nil ⇒ 逐位退化为只认 users.group。
var QyDeclaredUserGroups = func() []string { return nil }

// QyPinnedModelGroups 返回「模型分组 → 正 pin 着它的用户分组名」。
//
// ═══════════════ 它挡住的是一次"看起来完全无害"的删除 ═══════════════
//
// 保存 pin 时有三道闸门,但它们是**一次性**的。之后运营在「模型分组」页删掉那一行
// (页面文案明写「删除一行只会删掉它的兜底倍率,不会动渠道、令牌或任何一档人的
// 可用范围」,一个字都没提默认模型分组),保存 options 成功 —— 而被 pin 的那个
// 用户分组下的全部空分组令牌从下一次请求起变成 **HTTP 500**,比它们原来的 503
// 更难懂,而且绝大多数客户端 SDK 会把 500 当网关故障重试从而放大流量。
//
// 所以删除路径必须能反查 pin。这是 groupns 唯一一个被**上游写路径**消费的 hook。
//
// 冷路径(管理端保存 options 时调一次),允许读快照;默认返回 nil ⇒ 校验整段跳过。
var QyPinnedModelGroups = func() map[string][]string { return nil }

// QyNotePinRejected 由 middleware/auth.go 在 pin 的两道运行时校验失败时回调一次。
//
// ═══════════════ 为什么这个回调必须存在 ═══════════════
//
// pin 的合法性在保存接口上被三道闸门硬拦过一次,但那是**一次性**的:之后任何一次
// 「模型分组页删掉那一行的兜底倍率」或「矩阵页撤销该用户分组对它的授权」都能把一个
// 已生效的 pin 变成运行时 500,而那两个页面对 pin 一无所知(它们不 import groupns)。
// 表现是被 pin 的那个用户分组下**全部空分组令牌**从下一次请求起 500。
//
// 没有这个回调的话,/admin/health 的 group_namespace.pin_rejects 恒为 0、
// needs_attention 恒为 false —— 健康面板对一次整组下线毫无表示,唯一线索是后端
// SysError 日志。近乎不可达的错误一旦发生,没有计数器就等于没有发生。
//
// 默认空实现;禁止查库、禁止取锁(它跑在鉴权路径上,虽然只在失败分支)。
var QyNotePinRejected = func(userGroup, modelGroup, reason string) {}

// QyGroupRatioMissingDenied 回答「最终 UsingGroup 不在分组倍率表里时,要不要直接 403」。
//
// ═══════════════ 为什么严格模式落在鉴权处而不是计费处 ═══════════════
//
// 计费处报错时上游 token 已经烧掉了 —— 钱花了、请求失败、两头空。
// 鉴权处拒绝时请求还没花一分钱。这是「资金安全优先于可用性」的直接推论。
//
// 默认恒 false ⇒ 逐位等于上游:静默按 fail-open 的 1.0 计费。那不是被接受的现状,
// 而是被**降级**的现状 —— 现在每一笔都带 other.admin_info.group_ratio_missing 标记,
// 可定位、可重算、可补差(见 setting/ratio_setting/qy_ratio_export.go)。
// 翻到拒绝之前必须先把标记的计数清零。
//
// 刻意**只覆盖空令牌分组那一档**:显式令牌分组已经被上游的
// `if !ContainsGroupRatio(tokenGroup)` 挡过了(403「分组 %s 已被弃用」),
// 在同一个位置再判一次只会制造两条文案不同、结论相同的路径。
var QyGroupRatioMissingDenied = func(modelGroup string) bool { return false }

// QyModelGroupFundingAllowed 判定一个模型分组此刻**还有没有资格由钱包出资**。
//
// ═══════════ 只有一个调用点:钱包出资那一处 ═══════════
//
// service/billing_session.go 的 tryWallet 闭包第一行。权限层(middleware/auth.go)
// **不**调用它 —— 那里问的是"有没有资格用这个分组",由
// QyUsableGroupsForUser 独立回答,余额与运营开关都与它无关。
// 曾经有一个 requireFunding 布尔留给权限层,自始至终没有过生产调用方,本轮删掉:
// 一个没有调用方的参数会让规格比行为长,而规格长的那一半没有测试能钉住。
//
// 判据(顺序即语义):
//
//	contains(U, M)                            → 放行,**不看 allow_wallet_overflow**
//	不含 + 没有任何套餐解锁 M                 → 放行
//	不含 + 套餐解锁 M + 那些套餐还有余额      → 放行
//	不含 + 套餐解锁 M + 已耗尽 + 任一 O=true  → 放行
//	不含 + 套餐解锁 M + 已耗尽 + 全部 O=false → 拒绝(受 funding_gate_mode 灰度控制)
//
// 第一条是本轮的核心:一个用户分组本身就含 M 的人,本来根本不需要套餐就能用 M,
// 拿"套餐额度用完"去拦他没有道理。allow_wallet_overflow(运营在套餐上勾的
// 「额度用尽后不允许使用钱包余额」)只对**纯靠套餐解锁**的模型分组生效,而且只统计
// 解锁 M 的那些订阅 —— 用户级聚合会让一张与 M 无关的套餐把 M 上的钱包出资一起封掉。
//
// ─────────────────────── 余额与运营开关必须是主库权威的 ───────────────────────
//
// 后四条的判据此前来自 planentitlement 的 per-user 缓存(默认 60s 新鲜期),而订阅是在
// relay 的扣费事务里被耗尽的、运营改 allow_wallet_overflow 也不让该缓存失效 ——
// 没有任何一处会为这两件事做失效。危险的是**放行**那个方向:套餐刚耗尽的那一分钟里
// 缓存仍说"还有余额",闸门放行,钱包替一个运营明令禁止钱包续付的分组付了钱。
// 因此实现体只用缓存回答"M 是不是套餐解锁的"(零 I/O 短路),一旦成立就回一次主库
// 拿真实余额与开关,放行与拒绝两个方向都是。
//
// ─────────────────────── 调用方必须遵守 ───────────────────────
//
// 拒绝时返回的错误**不得**使用 types.ErrorCodeInsufficientUserQuota:
// 「你没有这个分组的出资资格」与「你余额不足」是两件事,客户端要能分辨 ——
// 前者充值钱包解决不了。约定用 types.ErrorCodeAccessDenied。
//
// ─────────────────────── fail 方向:一律放行 ───────────────────────
//
// 快照未加载、开关未开、余额或运营开关读不到 —— 全部 (true, "")。fail-closed 会让
// 付费用户在缓存抖动时突然失去分组,而那种失败在日志里没有形状,只表现为
// "少数人偶发 403"。
var QyModelGroupFundingAllowed = func(userId int, userGroup, modelGroup string) (allowed bool, reason string) {
	return true, ""
}
