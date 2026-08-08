package groupmatrix

// hook.go —— 注入给 service.QyResolveUsableGroups / QyCheckTokenGroupChange 的实现体。
//
// ══════════════════════════ 本文件的硬约束 ══════════════════════════
//
//  1. **零 I/O、零锁、零 context 等待。** Resolve 挂在 middleware/auth.go 的
//     令牌分组校验上,每一个带令牌分组的 relay 请求调用一次。
//     由 hookpoint_test.go 的 AST 守卫断言本文件不 import 任何数据库包。
//
//  2. **禁止回调上游的 service.GetUserUsableGroups / GroupInUserUsableGroups /
//     IsUserSelectableGroup。** Resolve 就挂在前者的 return 上,回调进去是无限递归。
//
//  3. **未配置时必须返回入参那一张 map 的指针**,不复制、不增删、不重排。
//     指针相等是"未配置 = 上游"最强的可证形式,由 bitexact_test.go 钉死。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/groupratio"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// Resolve 是 service.QyResolveUsableGroups 的实现体。
//
// 四种情况必须原样返回 upstream(见 service/qy_usablegroup_export.go 的契约):
//
//	(a) 扩展未启用 / group_matrix.enabled == false
//	(b) userGroup == ""(匿名口径)
//	(c) 该用户分组没有 scope 行(unmanaged / inherit)
//	(d) 快照从未成功加载过
//
// 另加一种:scope.mode == shadow —— 清单已配置但刻意不生效。
//
// 被接管且 enforce 时返回一张**新分配**的 map:绝不返回快照内部那一张,
// 调用方 delete 一个 key 就会污染全站鉴权,而且无法复现。
//
// ══════════ 按格备注为什么只在 enforce 这一档生效 ══════════
//
// 备注是"这一格"的属性,而"这一格存不存在"在 shadow / 未设范围两档里都还没成立:
// 那两档 return 的是 upstream 那一张 map 的**指针**,契约 (c) 与 bitexact_test 都
// 钉在这上面。为了一段文案去破坏"未生效 = 逐位等于上游"这条唯一可证的回退依据,
// 是拿回退能力换一个装饰品。shadow 的语义本来就是「配好了但一个字节都不生效」,
// 让它半生效(挡不住人、却改了文案)比不生效更难解释。
// 运营要预览按格备注,看管理端那一份解析结果即可(effective_note)。
func Resolve(userGroup string, upstream map[string]string) map[string]string {
	if !enabled() {
		return upstream
	}
	// 刷新只做一次 atomic 比较,到期由 HotAsync 在别的 goroutine 上回源。
	// 放在匿名判断之前:匿名请求同样应该让快照保持新鲜。
	maybeRefresh()

	if userGroup == "" {
		// 匿名口径。groupvis 的模型广场、availability、pricing 的匿名基准
		// 全部依赖这一条,改它等于同时动三个已经修好的泄漏面。
		return upstream
	}

	// ── 顺带补上一条上游够不到的信号(0 额外上游行)────────────────────────
	//
	// users.group 本身不在分组倍率表里时,上游 middleware/auth.go 的
	// `if tokenGroup != ""` 让**空分组令牌**整段绕过令牌分组校验,UsingGroup 直接
	// 等于这个孤儿名字;GetGroupRatio 查不到就静默返回 1,按原价扣费、零告警。
	// 那条路径上没有任何一处会去解析交叉倍率,所以 groupratio.Resolve 永远撞不到它。
	//
	// 我们已经在这个 hook 里、而且手上正好有那个名字,所以这是全仓成本最低的观测点。
	// 代价被钳到最小:命中(绝大多数请求)时只多一次与上游同源的 map 查找;
	// 未命中才进登记簿,而未命中意味着这一笔本来就在被错误计费。
	// 登记簿自带上限 + 首次必报 + 每 1000 次限频,近似名的计算是惰性的。
	if !ratio_setting.ContainsGroupRatio(userGroup) {
		groupratio.NoteMissingGroup(userGroup)
	}

	s := activeSnapshot()
	if s == nil {
		// 从未成功加载过。恒等返回 + 计数,健康面板据此出 snapshot_loaded=false。
		if n := fallbackRequests.Add(1); n == 1 || n%10000 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/groupmatrix: 可选清单快照尚未加载成功,累计 %d 次请求按上游全局白名单放行 —— "+
					"权威清单当前完全不生效,请检查扩展库健康状态", n))
		}
		return upstream
	}

	scope, ok := s.Scopes[userGroup]
	if !ok || scope.Mode != ModeEnforce {
		return upstream
	}

	grants := s.Grants[userGroup]
	notes := s.GrantNotes[userGroup]
	out := make(map[string]string, len(grants)+1)
	for mg := range grants {
		// ── 说明文案的阶梯,自上而下,第一条命中即止 ──────────────────
		//
		//	1. 按格备注 qy_group_grants.note   ← 只有本分支拿得到用户分组
		//	2. 模型分组备注 qy_model_groups.note
		//	3. options.UserUsableGroups[mg]    ← 上游原生 value
		//	4. 模型分组名
		//
		// 2~4 已经由 setting.QyDescribeGroup 统一持有(见 setting/qy_groupnote_export.go),
		// 所以这里只需要判第 1 级。全站唯一一处叠加按格备注的地方就是这里 ——
		// 三处调用点各写一遍 `if note != ""` 的表现是同一个分组在令牌页和
		// 模型广场上显示两段不同的说明,而那正是本轮在收敛的东西。
		if note := notes[mg]; note != "" {
			out[mg] = note
			continue
		}
		// 命中 upstream 时直接沿用它那一份 —— 这样"清单 = 上游结果 + 无按格备注"时
		// 连 value 都逐位一致,L4 的零变更断言才成立。
		if desc, ok := upstream[mg]; ok {
			out[mg] = desc
			continue
		}
		out[mg] = setting.GetUsableGroupDescription(mg)
	}
	if scope.AllowAuto {
		if desc, ok := upstream[autoGroup]; ok {
			out[autoGroup] = desc
		}
	}
	return out
}

// CheckTokenGroup 是 service.QyCheckTokenGroupChange 的实现体。
//
// 规则按顺序判定,任何一条命中即返回 —— 顺序本身就是语义,别重排:
//
//  1. 分组没变      → 放行。只校验真的改了分组的那一次,否则用户改一个孤儿令牌
//     的名字都会被挡,而他根本没碰分组。那是把"将来会 403"
//     换成"现在就改不动",不是改进。
//  2. 新分组为空    → 放行。回落用户分组永远合法,而且这是孤儿令牌唯一的自救出口。
//  3. 未启用 / 写侧开关关 / 快照未加载 / unmanaged / shadow → 放行(shadow 下记一条影子拒绝)。
//     **一个状态同时控制读写两侧的严格性**,不允许 enforce 从写侧先漏进来。
//  4. 新分组 = auto → 由 scope.AllowAuto 决定,**与读侧同一个判据**。
//     这一条刻意排在 scope 查找之后:早期版本把 auto 放在最前面无条件豁免,
//     于是 enforce + AllowAuto=false 时令牌保存得下、每次请求却被读侧
//     (Resolve 只在 AllowAuto 时才把 auto 注回)以「无权访问 auto 分组」挡掉 ——
//     一条永远发不出请求的新孤儿,正是这道闸门存在的理由。
//  5. enforce 且不在清单里 → 拒绝。
//  6. 任何内部错误 / panic → 放行(fail-open)。
func CheckTokenGroup(c *gin.Context, oldGroup, newGroup string) (err error) {
	// panic 一律吞掉并放行:这条跑在用户建令牌的同步路径上,挡住一次合法建号的
	// 代价大于放过一次非法分组 —— 读侧还有 middleware/auth.go 那道最后的闸。
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf(
				"qianye/groupmatrix: 令牌分组写入校验 panic(已拦截并放行): %v", r))
			err = nil
		}
	}()

	if newGroup == oldGroup || newGroup == "" {
		return nil
	}
	if !enabled() || !cfg().WriteGuardOn() {
		return nil
	}
	s := activeSnapshot()
	if s == nil {
		return nil
	}
	userGroup, ok := ownerUserGroup(c)
	if !ok {
		return nil
	}
	scope, managed := s.Scopes[userGroup]
	if !managed {
		return nil
	}
	if newGroup == autoGroup {
		// 与读侧 Resolve 的 allow_auto 分支同一个判据。放行则 auto 会被注回可选 map,
		// 拒绝则读侧同样拒绝 —— 两侧永远说同一句话。
		if scope.AllowAuto {
			return nil
		}
	} else if _, granted := s.Grants[userGroup][newGroup]; granted {
		return nil
	} else if service.QyPlanUnlockedGroup(c.GetInt("id"), newGroup) {
		// 套餐解锁是 **per-user** 的,不在 s.Grants 这份 per-group 清单里。
		// 少了这一条,读侧(QyUsableGroupsForUser 并入解锁)放行、写侧却拒绝 ——
		// 用户买了套餐、页面上看得到那个分组、保存令牌时被挡下来。
		// 两侧口径分叉是本仓已经修过一次的形状,不要再造一次。
		//
		// 判据与读侧同源(同一份 planentitlement 快照与 per-user 缓存),
		// 默认实现恒返回 false —— 订阅侧没接入时这一行行为中性。
		return nil
	}
	if scope.Mode != ModeEnforce {
		// 影子期:只记录,不阻断。这一档是唯一**可归因**的影子拒绝来源 ——
		// 读侧那个挂载点拿不到被查询的 key(见 WriteDeny 的注释)。
		recordWriteDeny(userGroup, newGroup, c.GetInt("id"))
		return nil
	}
	if newGroup == autoGroup {
		return fmt.Errorf("用户分组 %q 当前不允许使用 auto 自动分组(接管设置里的「允许 auto 伪分组」是关闭的)。"+
			"当前可选:%s", userGroup, describeGrants(s.Grants[userGroup]))
	}
	// 错误正文必须把"可以选什么"一起给出来:只说"不许选 X"会让用户在下拉框里
	// 反复试错,而下拉框的候选来自另一条链路,两者不一致时他永远试不出来。
	return fmt.Errorf("用户分组 %q 不能使用模型分组 %q。当前可选:%s",
		userGroup, newGroup, describeGrants(s.Grants[userGroup]))
}

// PlaygroundGroupAllowed 是 service.QyPlaygroundGroupAllowed 的实现体。
//
// 它存在的唯一理由是 /pg/chat/completions 走 UserAuth,而 ContextKeyUsingGroup
// 只在 TokenAuth 里写入 —— 上游那条判定拿到的 usingGroup 恒为空串,于是走的是
// 匿名口径的全局白名单,权威清单一条都不生效(见 service 侧那个变量的注释)。
//
// 这里做的事只有一件:usingGroup 为空时换成**令牌所有者的用户分组**再判定。
// 其余语义与上游逐位一致(含 requestedGroup == usingGroup 这条短路)。
// 拿不到所有者时回落上游语义 —— fail-open,与写侧规则 6 同一个方向。
func PlaygroundGroupAllowed(c *gin.Context, usingGroup, requestedGroup string) bool {
	effective := usingGroup
	userId := 0
	if c != nil {
		userId = c.GetInt("id")
	}
	if effective == "" && enabled() {
		if owner, ok := ownerUserGroup(c); ok {
			effective = owner
		}
	}
	// 第三个短路是套餐解锁:GroupInUserUsableGroups 是 group-keyed 的,
	// 而套餐挂在人身上。不补这一条,买了套餐的用户在 playground 里选不到他
	// 已经付过钱的分组,而令牌链路上选得到 —— 同一个人在两个入口得到两种答案。
	// 默认实现恒返回 false,所以订阅侧没接入时这一行行为中性。
	return service.GroupInUserUsableGroups(effective, requestedGroup) ||
		requestedGroup == usingGroup ||
		service.QyPlanUnlockedGroup(userId, requestedGroup)
}

// describeGrants 把清单渲染成一句人话。空清单必须明说,不能显示成空字符串 ——
// 「一个都不许用」是合法配置,而空字符串看起来像是接口出错了。
func describeGrants(set map[string]struct{}) string {
	if len(set) == 0 {
		return "(该用户分组的可选清单为空,只能把令牌分组留空以回落到用户分组本身)"
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "、")
}

// ownerUserGroup 取令牌所有者的用户分组。走用户缓存,不查库。
//
// 取不到时返回 false,调用方一律放行(规则 6):
// 这条路径上"拿不准"必须倒向放行,读侧还有最后一道闸。
//
// 抽成变量只为让测试能覆盖**唯一真正拦人的那条分支**(规则 5:enforce 且不在
// 清单里 → 返回 error)。没有这个接缝,单测里 model.GetUserCache 永远拿不到用户,
// 于是每一个用例都停在 fail-open 上 —— 全绿,而实现体可以整段写反。
// 生产路径必须是下面这个默认实现。
var ownerUserGroup = func(c *gin.Context) (string, bool) {
	if c == nil {
		return "", false
	}
	userId := c.GetInt("id")
	if userId <= 0 {
		return "", false
	}
	u, err := model.GetUserCache(userId)
	if err != nil || u == nil {
		return "", false
	}
	return u.Group, true
}

// recordWriteDeny 累加一条影子期写入拒绝。
//
// 走 HotAsync 而不是同步写:这是观测数据,绝不该让一次扩展库抖动
// 卡住用户建令牌的请求。丢一条影子样本不影响任何判断。
func recordWriteDeny(userGroup, modelGroup string, userId int) {
	hotAsync("groupmatrix.write_deny", func(ctx context.Context) error {
		return upsertWriteDeny(ctx, userGroup, modelGroup, userId)
	})
}
