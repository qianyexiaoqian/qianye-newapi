package planentitlement

// hook.go —— 注入给 service.QyPlanUnlockGroups / service.QyPlanUnlockedGroup 的实现体。
//
// ══════════════════════════ 本文件的硬约束 ══════════════════════════
//
//  1. **第一层快照零 I/O、零锁**。Resolve 挂在 middleware/auth.go 的令牌分组校验上,
//     每一个带令牌分组的 relay 请求调用一次。
//
//  2. **允许查库的只有第二层 per-user 缓存的未命中分支**,自带 50ms 硬超时,
//     而且必须先被"全站没有任何绑定"这一条短路挡住 —— 也就是说在运营配下第一条
//     绑定之前,本 hook 一次 I/O 都不会发生。这条例外只属于本文件,
//     不要拿它给 groupmatrix 那三个 hook 免责。
//
//  3. **禁止回调 service.GetUserUsableGroups 及其转调方**(GroupInUserUsableGroups /
//     IsUserSelectableGroup / QyUsableGroupsForUser)。QyUsableGroupsForUser 就是
//     "GetUserUsableGroups 之后再调本 hook",回调进去是无限递归。
//
//  4. **未配置时必须返回入参那一张 map 的指针**,不复制、不增删。
//     指针相等是"未配置 = 上游"最强的可证形式,由 bitexact_test.go 钉死。

import (
	"github.com/QuantumNous/new-api/setting"
)

// statusActive 是上游 user_subscriptions.status 的"生效中"取值。
//
// 与 model.PreConsumeUserSubscription 的候选查询同一个字面量:两处判据一旦分叉,
// 就会出现"看得见却扣不到"或"扣得到却看不见"。
const statusActive = "active"

// Resolve 是 service.QyPlanUnlockGroups 的实现体。
//
// 并集语义:先拿到上游(含 groupmatrix 权威清单)的结果,再把该用户持有的套餐
// 解锁的模型分组并进去。**套餐解锁与用户分组无关** —— 这就是项目方那句
// 「不绑定用户组」在代码里的落点。
//
// 五种情况原样返回 upstream(见 service/qy_usablegroup_export.go 的契约):
//
//	(a) 扩展未启用 / plan_entitlement.enabled == false
//	(b) userId <= 0(匿名口径)
//	(c) 快照从未加载成功
//	(d) 全站没有任何套餐配过绑定 —— 到这一步就返回,**不碰第二层、不查库**
//	(e) 该用户没有任何解锁
func Resolve(userId int, upstream map[string]string) map[string]string {
	if !enabled() {
		return upstream
	}
	// 刷新只做一次 atomic 比较,到期由 HotAsync 在别的 goroutine 上回源。
	maybeRefresh()

	if userId <= 0 {
		return upstream
	}
	s := activeSnapshot()
	if !s.AnyBindings() {
		// 全站零绑定:这是上线当天的状态,也是本功能"零行为变化"的结构性保证。
		return upstream
	}

	unlocked := unlockedSet(s, userId)
	if len(unlocked) == 0 {
		return upstream
	}

	// 必须新分配:调用方(controller/pricing.go 会把这张 map 继续往下传)
	// delete 一个 key 就会污染快照,而且无法复现。
	out := make(map[string]string, len(upstream)+len(unlocked))
	for k, v := range upstream {
		out[k] = v
	}
	for mg := range unlocked {
		if _, ok := out[mg]; ok {
			// 已经在上游结果里:沿用它那一份描述文案,连 value 都逐位一致。
			continue
		}
		out[mg] = setting.GetUsableGroupDescription(mg)
	}
	return out
}

// UnlockedGroup 是 service.QyPlanUnlockedGroup 的实现体:写入侧的单点判定。
//
// 与 Resolve 同源(同一份快照、同一份 per-user 缓存),因此两者不会在同一时刻
// 给出不同答案。任何一步不确定都返回 false —— 写入侧的 fail 方向是"把判定交回
// 原有逻辑",不是"放行"。
func UnlockedGroup(userId int, groupName string) bool {
	if !enabled() || userId <= 0 || groupName == "" || groupName == autoGroup {
		return false
	}
	s := activeSnapshot()
	if !s.AnyBindings() {
		return false
	}
	_, ok := unlockedSet(s, userId)[groupName]
	return ok
}

// unlockedSet 求该用户所有活跃套餐解锁的模型分组的**并集**。
//
// ═════════════ 多套餐 / 多解锁的冲突口径:并集,且没有冲突可言 ═════════════
//
// 成员资格是幂等的:两个套餐都解锁 G,用户拿到的还是 G,没有"谁优先"的问题。
// 真正会打架的东西(倍率)本模块一个字节都不存 —— 倍率恒为
// (用户分组, 模型分组) 的纯函数,与"这个分组是靠哪个套餐拿到的"无关。
// 所以"两个套餐对同一个分组给了不同的价"这个问题在这里**不存在**,
// 而不是被某条优先级规则解决掉了。
//
// 余额的使用范围(balance_scope)是**每个套餐各自**的属性,落在出资候选过滤上
// (见 Snapshot.CandidateUsable),同样不产生跨套餐的冲突:一条订阅能不能出资
// 只看它自己那个套餐的 scope 与绑定。
//
// 返回的 map 可能为空(用户没订阅 / 他的套餐都没配绑定 / 读取失败)。
// 调用方必须把"空"与"读取失败"一视同仁地当作**无解锁**处理(fail-closed),
// 失败已经由 activePlanIds 内部计数与告警。
func unlockedSet(s *Snapshot, userId int) map[string]struct{} {
	planIds, ok := activePlanIds(userId)
	if !ok || len(planIds) == 0 {
		return nil
	}
	var out map[string]struct{}
	for _, planId := range planIds {
		for mg := range s.Bind[planId] {
			if out == nil {
				out = make(map[string]struct{}, 4)
			}
			out[mg] = struct{}{}
		}
	}
	return out
}

// FundedUnlockState 回答两个问题:这个模型分组是不是该用户的套餐解锁的、
// 以及那些解锁它的套餐里**还有没有一个是有余额的**。
//
// 它服务的是 qianye/modules/groupns 的「套餐耗尽闸门」(挂在钱包出资决策上)。
// 两个返回值必须一起给:只给 unlocked 的话,调用方无法把"套餐还在供着"与
// "套餐已经耗尽、这一笔要改由钱包出"分开,而那正是闸门唯一要判的事。
//
// **零额外查询**:解锁集合与余额都来自同一份 per-user 缓存条目。
//
// fail 方向:任何不确定(未启用 / 零绑定 / 快照未加载 / 缓存读不到)一律返回
// (false, false) —— unlocked=false 会让闸门整体放行(见 groupns.ModelGroupFundingAllowed),
// 因此这个方向对付费用户是安全的。
func FundedUnlockState(userId int, modelGroup string) (unlocked bool, funded bool) {
	if !enabled() || userId <= 0 || modelGroup == "" || modelGroup == autoGroup {
		return false, false
	}
	s := activeSnapshot()
	if !s.AnyBindings() {
		return false, false
	}
	planIds, ok := activePlanIds(userId)
	if !ok || len(planIds) == 0 {
		return false, false
	}
	for _, planId := range planIds {
		if _, bound := s.Bind[planId][modelGroup]; bound {
			unlocked = true
			break
		}
	}
	if !unlocked {
		return false, false
	}
	fundedIds, fundedOK := activeFundedPlanIds(userId)
	if !fundedOK {
		// 读不到余额:按"还有余额"处理。挡住一次合法请求比放过一次超额出资更贵 ——
		// 后者还有 model.PreConsumeUserSubscription 的事务内校验兜底。
		return true, true
	}
	// ── funded 的判据是 CandidateUsable,**不是** Bind ──
	//
	// 订阅出资的真实判据是 model.PreConsumeUserSubscription 候选循环里的
	// QySubscriptionCandidateUsable(= Snapshot.CandidateUsable):余额使用范围为
	// 「通用」(非 ScopeRestricted)的套餐可以为**任何**模型分组出资,它根本不需要
	// 绑定 M。只在 Bind[planId][M] 命中的套餐里找余额,会把
	// 「P1 解锁 M 但已用尽 + P2 通用余额充足」这种再正常不过的组合判成"耗尽",
	// 而闸门返回的 AccessDenied 又不会触发 wallet_first 的 trySubscription 回落 ——
	// 一条本来能付款的路径被直接砍掉,而 P2 完全付得起这一笔。
	for _, planId := range fundedIds {
		if s.CandidateUsable(planId, modelGroup) {
			return true, true
		}
	}
	return true, false
}
