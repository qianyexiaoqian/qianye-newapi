// Package planentitlement 实现「订阅套餐解锁模型分组」与「套餐余额的使用范围」。
//
// ═══════════════════════════ 需求原话与它的落点 ═══════════════════════════
//
//	「可以拥有多个套餐,可以用作解锁,新增模型组,**不绑定用户组**。」
//	「套餐若绑定了模型分组,那么余额你要加一个设置,
//	  这个余额是否可用于其他分组,或仅限于这个分组使用。」
//
// 两句话对应本包的两张表:
//
//	qy_plan_group_grants   套餐 → 解锁的模型分组(成员资格)
//	qy_plan_balance_policy 套餐 → 余额使用范围(universal / restricted)
//
// ═══════════════════════ 三件本包**不**做的事 ═══════════════════════
//
//  1. **不存倍率,一个字节都不存。** 倍率恒为 (用户分组, 模型分组) 的纯函数,
//     真相源只有上游 options.GroupGroupRatio / options.GroupRatio。于是本轮向
//     计费链路新增的倍率来源数是 0,三条复制的计费路径(relay/helper/price.go、
//     service/quota.go、service/task_billing.go)一行不改而天然全覆盖。
//     代价说清楚:用户分组 A 谈好的 (A,G)=0.5 不会因为 G 是"靠套餐拿到的"而失效。
//     这与项目方原话「按照模型分组 G 的模型分组倍率来使用」的字面表述有偏差,
//     偏差是刻意的 —— 反向实现要求计价时知道"这次可用性是谁给的",那就得把
//     解锁来源一路带进 HandleGroupRatio,等于再造一条解析路径。
//
//  2. **不改扣费顺序。** 「多个套餐按最先到期的先扣」已经是上游行为
//     (model.PreConsumeUserSubscription 的 `Order("end_time asc, id asc")`),
//     本包只做两件事:用回归测试把它钉死,以及在**我们自己的**用户接口里按同一个
//     顺序展示(上游那个展示函数按 end_time desc 排,与扣费顺序正好相反,
//     但那一行不改 —— 理由见 api_user.go 顶部)。
//
//  3. **解锁的模型分组不参与 auto 自动分组。** service/group.go 的
//     GetUserAutoGroup / FilterUserTokenAutoGroups 保持 group-keyed,上游 0 行。
//     理由是资金安全而不是能力取舍:relay/helper/price.go 在 auto 命中后会
//     **改写 relayInfo.UsingGroup**,而 UsingGroup 正是余额使用范围的判据。
//     让解锁分组能被 auto 选中,等于一次上游重试就能在用户完全没有指定的情况下
//     **改变这笔请求的出资来源**(从钱包变成套餐余额,或反过来),
//     而 auto 的候选来自另一条链路、界面上完全看不出来。
//
// ═══════════════════════ 与上游的耦合面(4 行,4 个文件)═══════════════════════
//
//	middleware/auth.go     令牌分组校验    —— 请求时唯一的闸门,不改则解锁零效果
//	controller/group.go    令牌页分组下拉  —— 唯一数据源,不改则「有权限但选不到」
//	controller/pricing.go  价格表
//	controller/user.go     个人信息页可用模型
//
// 四处全部是把 service.GetUserUsableGroups(g) 原地换成
// service.QyUsableGroupsForUser(userId, g),不新增 import、不新增行。
// hook 声明在纯新增文件 service/qy_usablegroup_export.go。
//
// ═══════════════════════ 上线当天为什么零变化 ═══════════════════════
//
// 两张表空表起步 = 没有任何套餐解锁任何分组、全部 universal = 上游今天的行为。
// 而且 Snapshot.AnyBindings() 为 false 时,权限解析在触碰 per-user 缓存之前就
// 返回上游那张 map 的**指针** —— 零行为变化与零新增 I/O 都是结构性的,
// 不依赖任何配置默认值。
package planentitlement

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/modules/groupmatrix"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Mod 是套餐解锁模块的注册入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "planentitlement" }

func (Mod) Tables() []any {
	return []any{
		&PlanGrant{},
		&PlanBalancePolicy{},
	}
}

// InstallHooks 注入两个挂载点并预热一次快照。
//
// 注入无条件发生(不看 enabled):两个实现体自己判 enabled(),这样
// plan_entitlement.enabled 就是真正的热载开关 —— 改 YAML 即生效,不必重启。
// 若只在启用时注入,关掉再打开就需要重启,而 kill switch 必须在最坏的时刻可用。
//
// 预热失败不阻塞启动:快照未加载 = 所有套餐按"无解锁 + 余额通用"处理,
// 与上游逐位一致。它是安全的方向,但会让已付款用户的解锁分组暂时消失,
// 所以 /admin/health 会一直显示 snapshot_loaded=false。
func (Mod) InstallHooks() {
	service.QyPlanUnlockGroups = Resolve
	service.QyPlanUnlockedGroup = UnlockedGroup

	// ── 余额使用范围的执行侧 ──────────────────────────────────────────────
	//
	// 这两个赋值与紧随其后的 MarkBalanceScopeEnforced() 是一个整体:前者让
	// 「仅限」真的在扣费路径上过滤候选,后者让展示侧敢于说「这笔余额在这里用不了」。
	// **少做任何一步,界面就会开始说假话** —— 管理端显示已经限制住了,而钱照样
	// 从那张套餐里扣。三行必须一起改,不要拆开。
	model.QySubscriptionCandidateUsable = CandidateUsable
	model.QyWalletOverflowAllowedDespiteStrict = WalletOverflowAllowedDespiteStrict
	MarkBalanceScopeEnforced()

	// 矩阵页的两个接缝。它们是**冷路径只读**的,与上面两个热路径 hook 无关,
	// 但必须同时注入:少注入一个,矩阵页就会把"经套餐可达"的格子显示成不可达,
	// 而运营正是在那张页面上改倍率的(见 seams.go 的说明)。
	groupmatrix.PlanUnlockEnabled = Enabled
	groupmatrix.PlanUnlockedModelGroups = UnlockedModelGroupPlans

	if !enabled() {
		return
	}
	if err := reload(); err != nil {
		common.SysError("qianye/planentitlement: 套餐解锁快照预热失败(本次启动后先按「无解锁」处理," +
			"已购套餐的解锁分组暂不生效,稍后自动重试): " + err.Error())
	}
}

// RegisterAdminRoutes 挂管理端接口。
//
// 前缀与 modules/subscription 的名额接口保持一致(/subscription/plans/:plan_id/…):
// 两者是同一个对象(套餐)的两组属性,前端只需要记住一个基址。参数名必须也叫
// plan_id —— gin 的路由树里同一位置出现两个不同参数名是**注册期 panic**,
// 表现是进程起不来而不是某个接口 404。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/subscription/plans/:plan_id/entitlement", adminGetEntitlement)
	// 改解锁绑定与余额使用范围会立刻改变「谁能用什么」与「钱从哪个池子扣」,
	// 按关键操作限流,成功与失败都写审计。
	g.PUT("/subscription/plans/:plan_id/entitlement", middleware.CriticalRateLimit(), adminPutEntitlement)
}

// RegisterUserRoutes 挂用户接口:我的套餐余额能用在什么上、哪一张会先被扣。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/subscription/entitlements", userEntitlements)
}

func init() { module.Register(Mod{}) }
