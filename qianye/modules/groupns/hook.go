package groupns

// hook.go —— 注入给 service.QyResolveDefaultModelGroup /
// service.QyGroupRatioMissingDenied / service.QyModelGroupFundingAllowed 的实现体。
//
// ══════════════════════════ 本文件的硬约束 ══════════════════════════
//
//  1. **零 I/O、零锁、零 context 等待。** ResolveDefaultModelGroup 挂在
//     middleware/auth.go 上,每一个**空分组令牌**的 relay 请求调用一次。
//     只允许 atomic.Pointer.Load 与 map 查找。
//
//  2. **登记表 fail-open。** 未登记 / 快照未加载 / 模块关闭 → 一律 inherit,
//     也就是逐位等于上游的 `userGroup = userCache.Group`。
//     漏登记一个名字必须表现为"和今天一样",而不是"这组人全部 403"。
//
//  3. **实现体不做 pin 的合法性校验。** 那两道校验(可选清单 + 分组倍率表)在
//     middleware/auth.go 的调用点上做,与显式令牌分组走**同样的两行**。
//     放在这里会让两条路径的判据分家,而分家的表现是"令牌指定 X 能用、
//     默认解析出 X 却被挡"——同一个人在两个入口得到两种答案。

import (
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/service"
)

// ResolveDefaultModelGroup 是 service.QyResolveDefaultModelGroup 的实现体。
//
// 契约(见 service/qy_groupns_export.go):
//
//	返回 (userGroup, "inherit")  = 逐位等于上游,调用方一个字节都不改
//	返回 (M, "pin")              = 调用方按显式令牌分组的同一套校验放行到 M
//	返回 ("", "deny")            = 调用方 403
//
// 下面每一条 return 都通向 inherit,只有"启用 + 快照在 + 有行 + mode 合法"
// 这一条窄路径才可能返回别的东西 —— 这就是"上线当天零变化"的结构性来源,
// 而不是某个配置默认值的副产品。
func ResolveDefaultModelGroup(userId int, userGroup string) (string, string) {
	if !defaultResolveOn() {
		return userGroup, DefaultModeInherit
	}
	// 刷新只做一次 atomic 比较,到期由 HotAsync 在别的 goroutine 上回源。
	maybeRefresh()

	if userGroup == "" {
		// 匿名 / 无身份口径。没有身份就没有"这个人的默认模型分组"这回事。
		return userGroup, DefaultModeInherit
	}

	s := activeSnapshot()
	if s == nil {
		// 从未成功加载过。恒等返回 + 计数,健康面板据此出 snapshot_loaded=false。
		if n := fallbackRequests.Add(1); n == 1 || n%10000 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/groupns: 分组登记快照尚未加载成功,累计 %d 次空分组令牌按 inherit 放行 —— "+
					"默认模型分组当前完全不生效,请检查扩展库健康状态", n))
		}
		return userGroup, DefaultModeInherit
	}

	ug, ok := s.UserGroups[userGroup]
	if !ok {
		// **未登记 ≠ 拒绝。** 登记表是给人看的表,不是准入闸门。
		return userGroup, DefaultModeInherit
	}
	switch ug.DefaultMode {
	case DefaultModePin:
		return ug.DefaultModelGroup, DefaultModePin
	case DefaultModeDeny:
		return "", DefaultModeDeny
	default:
		return userGroup, DefaultModeInherit
	}
}

// DeclaredUserGroups 是 service.QyDeclaredUserGroups 的实现体:登记表里的全部
// 用户分组名(包含那些还没有任何用户的)。
//
// 冷路径,允许 make 一张切片。快照没加载时返回 nil ⇒ 调用方退化为只认 users.group。
func DeclaredUserGroups() []string {
	if !enabled() {
		return nil
	}
	s := activeSnapshot()
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.UserGroups))
	for name := range s.UserGroups {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// PinnedModelGroups 是 service.QyPinnedModelGroups 的实现体:
// 「模型分组 → 正 pin 着它的用户分组名」。
//
// 冷路径(管理端保存 options 时一次),所以允许读快照并 make 一张 map ——
// 它与本文件头「零 I/O」那条约束不冲突:那条约束管的是挂在 relay 热路径上的
// ResolveDefaultModelGroup,不是这个。
//
// 返回 nil 表示"无法回答"(模块关、解析关、快照没加载),调用方必须按**放行**
// 处理:一个查不到 pin 的时刻不该把管理员的正常保存动作卡死。
func PinnedModelGroups() map[string][]string {
	if !defaultResolveOn() {
		return nil
	}
	s := activeSnapshot()
	if s == nil {
		return nil
	}
	out := map[string][]string{}
	for name, ug := range s.UserGroups {
		if ug.DefaultMode == DefaultModePin && ug.DefaultModelGroup != "" {
			out[ug.DefaultModelGroup] = append(out[ug.DefaultModelGroup], name)
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}

// NotePinRejected 由调用点在 pin 校验失败时回调一次,只为让健康面板能看到它。
//
// 校验本身留在 middleware/auth.go(见文件头第 3 条),但"这个状态发生过"必须能被
// 看到:保存时的硬闸门让这条路径近乎不可达,而近乎不可达的错误一旦发生,
// 没有计数器就等于没有发生。
func NotePinRejected(userGroup, modelGroup, reason string) {
	n := pinRejects.Add(1)
	if n == 1 || n%100 == 0 {
		common.SysError(fmt.Sprintf(
			"qianye/groupns: 用户分组 %q 的默认模型分组 %q 校验失败(%s),累计 %d 次 —— "+
				"这是**配置自相矛盾**:保存时已经硬拦过 has_route 与可选清单,"+
				"运行时还能撞到说明有人绕过接口改了库,或者那个模型分组之后被删掉了。"+
				"请把该用户分组改回 inherit 或换一个仍然有渠道的模型分组",
			userGroup, modelGroup, reason, n))
	}
}

// GroupRatioMissingDenied 是 service.QyGroupRatioMissingDenied 的实现体:
// 「最终 UsingGroup 不在分组倍率表里时,是否在鉴权处直接 403」。
//
// ─────────────────── 为什么严格模式落在鉴权处而不是计费处 ───────────────────
//
// 计费处报错时上游 token 已经烧掉了 —— 钱花了、请求失败、两头空。鉴权处拒绝时
// 请求还没花一分钱。这是"资金安全优先于可用性"在响应码位置上的直接推论。
//
// 默认 legacy_one ⇒ 恒返回 false ⇒ 逐位等于上游(静默按 1.0 计费,但现在每一笔
// 都带 admin_info.group_ratio_missing 标记,可定位、可重算、可补差)。
// 翻到 deny 的前提见模块注释:登记簿计数连续为零,且
// `(abilities enabled 行的模型分组) ∖ (GroupRatio 键)` 差集为空。
func GroupRatioMissingDenied(modelGroup string) bool {
	if !enabled() || modelGroup == "" {
		return false
	}
	return cfg().MissingRatioPolicy == config.MissingRatioPolicyDeny
}

// ModelGroupFundingAllowed 是 service.QyModelGroupFundingAllowed 的实现体:
// 「这个模型分组此刻还有没有资格由**钱包**出资」。
//
// ══════════ 判据:成员资格优先,allow_wallet_overflow 只管纯解锁分组 ══════════
//
//	contains(U, M)                        → 放行。**不看 allow_wallet_overflow。**
//	                                        这个人本来就不需要套餐就能用 M,
//	                                        拿"套餐额度用完"去拦他没有道理。
//	不含 且 没有任何套餐解锁 M            → 放行(fail-open,理由见下)
//	不含 且 套餐解锁 M 且 那些套餐还有余额 → 放行
//	不含 且 套餐解锁 M 且 已耗尽 且 O=1   → 放行(运营显式允许钱包续付)
//	不含 且 套餐解锁 M 且 已耗尽 且 O=0   → 按 funding_gate_mode 决定
//
// O 即解锁 M 的那些活跃订阅上的 allow_wallet_overflow,取值口径是**任一为真即放行**,
// 而且**只看解锁 M 的套餐**。旧口径是用户级聚合(任一活跃订阅 O=0 就封锁全部钱包
// 出资),那会让一张与 M 毫无关系的套餐把 M 上的钱包出资一起封掉。
//
// funding_gate_mode 在这里是**纯粹的灰度开关**,不是第二条独立规则:
// off = 整条规则不生效(回滚用),shadow = 只记录不阻断,enforce = 生效。
// 让它携带独立语义会造出「O=0 但 gate=off,到底扣不扣钱包」这种谁都没配出来的格子。
//
// ══════════ 「归不归本闸门管」读缓存,判据本身回主库 ══════════
//
// 后三条的判据来自 planentitlement 的 per-user 缓存(默认 60s 新鲜期)。那份缓存
// 没有人为「订阅被扣空」与「运营改 allow_wallet_overflow」做失效 —— 前者发生在
// relay 的扣费事务里,后者发生在管理端。两个方向都会错,而**危险的是放行那个方向**:
// 套餐刚耗尽的那一分钟里缓存仍说"还有余额",闸门直接放行,钱包替一个运营明令
// 禁止钱包续付的分组付了钱;反方向则是运营刚把 O 改成 1、用户还要再吃一分钟 403。
//
// 所以只有第一问(unlocked:这一档归不归本闸门管)走缓存 —— 它是全站绝大多数
// 请求的短路出口,必须零 I/O。一旦 unlocked 成立,余额与运营开关**一律回主库**,
// 放行与拒绝两个方向都是。只在"即将拒绝"时才确认是不够的:放行那条路径根本
// 走不到那一步。
//
// ══════════ fail 方向:任何不确定一律放行 ══════════
//
// 快照未加载、开关未开、余额读不到、拒绝前的回源失败 —— 全部返回 (true, "")。
// fail-closed 会让付费用户在缓存抖动时突然失去分组,而那种失败在日志里没有形状。
func ModelGroupFundingAllowed(userId int, userGroup, modelGroup string) (bool, string) {
	if !enabled() || cfg().FundingGateMode == config.FundingGateOff {
		return true, ""
	}
	if userId <= 0 || modelGroup == "" || modelGroup == autoGroup {
		return true, ""
	}

	// 第一项:纯用户分组口径的成员资格。零 I/O(进程内快照 + map 查找)。
	// 刻意**不**走 QyUsableGroupsForUser:那个函数会把套餐解锁并进来,
	// 于是第一项与第二项塌成同一件事,后面的判定永远不会生效。
	if _, ok := service.GetUserUsableGroups(userGroup)[modelGroup]; ok {
		return true, ""
	}

	// 第一问走缓存,零 I/O。它只用来回答"这一档归不归本闸门管":
	// unlocked=false ⇒ 不归 ⇒ 放行,全站绝大多数请求在这里就结束了。
	if unlocked, _, _ := PlanUnlockFundingState(userId, modelGroup, false); !unlocked {
		// M 不是套餐解锁的。这一档**不由本闸门拒绝** —— 上游 middleware/auth.go
		// 已经在鉴权处挡过一次了,能走到计费的请求说明它在某种口径下是被允许的。
		// 越权拒绝会把一次口径差异变成一次线上故障。
		return true, ""
	}

	// ── 剩下的判据一律由主库当场回答,放行与拒绝**两个方向都是** ──
	//
	// 缓存里的 funded/allowOverflow 最长可以是 user_cache_seconds(默认 60s)之前的,
	// 而这两件事都没有人为它们做失效:funded 在 relay 的扣费事务里变假(订阅被这一笔
	// 或上一笔扣空),allowOverflow 由运营在管理端随手改。两个方向都会错,而且**危险的
	// 是放行那个方向** —— 套餐刚耗尽的那一分钟里,缓存仍说"还有余额",闸门直接放行,
	// 钱包替一个运营明令禁止钱包续付的分组付了钱。只在"即将拒绝"时才确认是不够的:
	// 那条路径根本走不到。
	//
	// 代价可控:走到这里要同时满足「M 纯靠套餐解锁」+「用户持有活跃套餐」+
	// 「订阅刚刚没能为这一笔出资」(否则不会来钱包这条路)。这是一次带索引的单用户
	// 查询,自带 50ms 硬超时与 singleflight 去重。这道闸门管的是钱,不能拿一份
	// 一分钟前的余额作数。
	unlocked, funded, allowOverflow := PlanUnlockFundingState(userId, modelGroup, true)
	if !unlocked || funded || allowOverflow {
		return true, ""
	}

	if cfg().FundingGateMode == config.FundingGateShadow {
		// 影子期:只记录,不阻断。数字稳定之后再翻 enforce。
		noteShadowFundingDeny(userId, userGroup, modelGroup)
		return true, ""
	}
	// enforce 档是**真会拒人**的档位,必须留下痕迹:调用方带
	// ErrOptionWithNoRecordErrorLog,被拒的请求也不写消费日志,数据库里一行都没有。
	noteEnforcedFundingDeny(userId, userGroup, modelGroup)
	return false, fmt.Sprintf(
		"模型分组 %q 由您持有的套餐解锁,该套餐额度已用尽,而您的用户分组 %q 不包含它;"+
			"该套餐同时设置了「额度用尽后不允许使用钱包余额」。"+
			"请续费套餐,或把令牌分组切换到您的用户分组可用的分组",
		modelGroup, userGroup)
}

// autoGroup 是上游的伪分组名。它不是任何池子,永远不参与出资判定。
const autoGroup = "auto"
