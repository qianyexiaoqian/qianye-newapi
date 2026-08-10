package planentitlement

import (
	"context"
	"strconv"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"
)

// seams.go —— 本模块对同进程其它模块暴露的接缝。
//
// 全部是**只读**的:别的模块只能问"现在是什么状态",不能改本模块的任何东西。
// 写入只有一条路径:管理端的 PUT(见 api_admin.go)。

// balanceScopeEnforced 表示「余额使用范围(仅限/通用)此刻真的在扣费路径上生效」。
//
// ═══════════ 为什么需要这个握手,而不是默认它已经生效 ═══════════
//
// 判定本身(CandidateUsable)住在本包,但真正让它生效的那一步不在这里:
// 订阅侧要把它接到 model.QySubscriptionCandidateUsable 上,而那个 hook 的上游
// 挂载点是 PreConsumeUserSubscription 的候选循环。
//
// **接线现已完成**:InstallHooks 里那三行(两个赋值 + 本握手)由
// qianye/planentitlement_hookpoint_guard_test.go 的 AST 守卫钉死,上游那两处
// 由 TestBalanceScopeKeepsItsUpstreamWiring 钉死。握手本身保留,因为它守的是
// **部分接线**这个中间状态:
//
// 两条线之间如果只靠"应该已经接上了"这句话,就会出现本设计里最不能接受的一种
// 状态:管理端把某个套餐配成「仅限 G」,用户端据此显示「这笔余额在 H 上用不了」,
// 而扣费路径根本没有过滤,那笔钱照样从这张套餐里扣走 —— **界面在骗人**,
// 而且骗的正是"我的钱去哪了"这个问题。
//
// 所以这里让"配置"与"生效"变成同一个判据:没接上时 CandidateUsable 恒为 true,
// 于是展示侧也一律显示"可用" —— 两边同时说真话。接上的那一方调
// MarkBalanceScopeEnforced() 完成握手,而 /admin/health 会把
// 「有仅限套餐、但过滤没生效」标成需要关注(见 Health)。
var balanceScopeEnforced atomic.Bool

// MarkBalanceScopeEnforced 由订阅侧在把 CandidateUsable 接到
// model.QySubscriptionCandidateUsable 上之后调用,声明"过滤真的在扣费路径上了"。
//
// 只在 qianye.Init() 的 InstallHooks 阶段调用一次,早于任何 HTTP 请求。
func MarkBalanceScopeEnforced() { balanceScopeEnforced.Store(true) }

// BalanceScopeEnforced 供管理端与用户端下发"这个设置此刻算不算数"。
func BalanceScopeEnforced() bool { return balanceScopeEnforced.Load() }

// ═══════════ 已删除:WalletOverflowAllowedDespiteStrict ═══════════
//
// 它曾是 model.QyWalletOverflowAllowedDespiteStrict 的实现体,唯一存在的理由是解开
// 「一张『仅限 G + 不许钱包回退』的套餐,在请求模型分组 H 时既出不了资、又不许用
// 钱包」这个谁都没配出来的死锁。那个死锁来自上游的**用户级**聚合口径
// (任一活跃订阅 allow_wallet_overflow=0 就封锁全部钱包回退)。
//
// 该口径已经废除:allow_wallet_overflow 现在只对「纯靠套餐解锁的模型分组」生效,
// 而且只统计**解锁该模型分组的**订阅(见 UnlockFundingState)。范围不匹配的套餐
// 根本不参与取值,死锁在结构上不再存在,所以这个函数连同它的 hook 一起删掉,
// 而不是留一个"看起来还在生效"的分支。

// Enabled 回答「套餐解锁此刻整体是开着的吗」。
//
// groupmatrix 的矩阵页据此区分两种长得一样、含义完全相反的状态:
// 「解锁关掉了(已购用户当前拿不到他买的分组)」与「开着但一个绑定都没配」。
func Enabled() bool { return enabled() }

// UnlockedModelGroupPlans 返回「模型分组 → 解锁它的套餐标题」。
//
// ═══════════ 它服务的是矩阵页的可达性,而可达性直接是钱 ═══════════
//
// 考虑这条链:用户分组 A 设了范围、其清单**不含** G,且没有交叉倍率格 (A,G)。
// A 组的某个用户买了解锁 G 的套餐 —— 他现在可以用 G,按 GroupRatio[G] 计费。
// 而矩阵页的 A 行只渲染 A 的清单,(A,G) 这一格根本不存在于界面上。运营看那张
// 页面得到的结论是「A 组的人永远碰不到 G」,几周后他把 GroupRatio[G] 从 1.0
// 调到 3.0,A 组所有持有该套餐的用户当场按 3 倍扣费 —— 而站内没有任何一个页面
// 显示过 A 组的人可以到达 G。这不是数值漂移,是**可达性认知的漂移**。
//
// 契约(与 groupmatrix.PlanUnlockedModelGroups 的声明逐条对应):
//
//	冷路径专用    只被管理端 GET /group-matrix 调用。这里会查一次主库拿套餐标题,
//	              带冷路径超时;任何失败一律返回 nil(可达性少标一点,不影响扣费)。
//	返回值只读    每次都新分配,不返回快照内部持有的任何 map。
//	键必须是模型分组,且必然已在 options.GroupRatio 里 —— 快照编译期已经把
//	              指向已删分组的绑定剔除了(不变量 I4)。
func UnlockedModelGroupPlans() map[string][]string {
	s := Current()
	if !s.AnyBindings() {
		return nil
	}
	planIds := make([]int, 0, len(s.Bind))
	for id := range s.Bind {
		planIds = append(planIds, id)
	}
	titles := make(map[int]string, len(planIds))
	if model.DB != nil {
		ctx, cancel := guard.ColdContext(context.Background())
		defer cancel()
		plans := make([]model.SubscriptionPlan, 0, len(planIds))
		if err := model.DB.WithContext(ctx).Where("id IN ?", planIds).Find(&plans).Error; err != nil {
			// 标题查不到不等于绑定不存在,但一份没有名字的可达性说明
			// (「经套餐 ? 可达」)只会让运营更困惑。整条放弃,矩阵页少标一点。
			common.SysError("qianye/planentitlement: 读取套餐标题失败,本次矩阵页不标注套餐可达性: " + err.Error())
			return nil
		}
		for i := range plans {
			titles[plans[i].Id] = plans[i].Title
		}
	}

	out := make(map[string][]string, 8)
	for planId, groups := range s.Bind {
		title := titles[planId]
		if title == "" {
			// 套餐已被删除但绑定行还在(删除路径没清干净)。用一个能被搜索到的
			// 占位而不是空串:空串在界面上就是"经套餐 可达",看起来像渲染坏了。
			title = "#" + strconv.Itoa(planId)
		}
		for mg := range groups {
			out[mg] = append(out[mg], title)
		}
	}
	return out
}
