// Package groupns 把「用户分组」和「模型分组」**彻底分成两个东西**。
//
// ═══════════════════════════ 分家之前是什么样 ═══════════════════════════
//
// 上游用**同一个字符串命名空间**同时表达两种概念:
//
//	users.group / tokens.group          用户分组:这个人是谁
//	channels.group / abilities.group    模型分组:这次请求去哪个渠道池子
//	options.GroupRatio 的键             既是「有哪些分组」的事实清单,
//	                                    又是模型分组的兜底倍率
//
// 于是「浅夜の梦专属号池」这**一个名字**同时是 539 个用户的用户分组、
// 又是 76 行 abilities 的模型分组。在界面上改文案解决不了这件事。
//
// ═══════════════════════════ 本模块做了什么 ═══════════════════════════
//
//	登记表   qy_user_groups / qy_model_groups —— 两份**分开的**名单
//	默认解析 用户分组的 default_model_group,修「空分组令牌 503」
//	两个闸门 分组倍率缺失的严格模式、套餐耗尽后的钱包出资闸门
//
// ═══════════════════════════ 三条与直觉相反的事实 ═══════════════════════════
//
//  1. **登记表是 fail-open 的,它没有权力挡任何请求。**
//     abilities / users 里出现而登记表里没有的名字叫「未登记」,在管理端以告警行
//     出现,不遮断任何东西。漏登记一个名字 = 那一整组用户 503,而一张给人看的表
//     不该有让站点下线的能力。
//
//  2. **一个倍率字节都不存。** 真相源恒为 options.GroupRatio / GroupGroupRatio。
//     镜像列的同步失败表现为「管理端显示 A、热路径乘 B」——
//     与 groupmatrix / planentitlement 的判断逐字同源。
//
//  3. **一行 abilities、一行 channels.group 都不碰,路由链路零改动。**
//     这是本模块最主要的安全声明。改模型分组侧要连带重写 abilities(路由表)、
//     tokens.group、GroupRatio 键、GroupGroupRatio 内层键、Grant.ModelGroup、
//     PlanGrant.ModelGroup —— 血溅路由与计费两条链路;而且要穿过
//     model/channel.go 里「先提交 channels.group、再单独重建 abilities」那个非原子
//     窗口(修复前那个窗口能让 InitChannelCache 写 nil map 而 panic 致死整个进程,
//     见 model/channel_cache.go 的 buildGroup2Model2Channels)。
//
// ═══════════════════ 「默认模型分组」为什么是三态而不是空串 ═══════════════════
//
// 登记表对每个观测到的用户分组自动回填一行 ⇒ 行存在性不再携带信息 ⇒
// 空串必须同时表达「还没配」和「就是不给兜底」。那正是本仓已经栽过三次的
// 「零值与未配置不可区分」。所以用显式 default_mode 枚举承载,见 model.go。
//
// ═══════════════════════════ 射程之外(必须知道)═══════════════════════════
//
//   - **不做改名。** 存量 5 个同时是用户分组和模型分组的名字(legacy_dual)
//     长期挂着;唯一性只对管理端新建生效,从此重名集合只减不增。
//   - **不自动修 545 条孤儿令牌。** 它们今天撞 403「分组已被弃用」,之后表现必须
//     一致。静默把 403 变成能用,等于替运营做了一个改价决定。
package groupns

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// Mod 是分组命名空间模块的注册入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "groupns" }

func (Mod) Tables() []any { return []any{&UserGroup{}, &ModelGroup{}} }

// InstallHooks 注入三个挂载点并预热一次登记快照。
//
// 注入**无条件发生**(不看 enabled):三个实现体自己判 enabled(),
// 这样 group_namespace.enabled 就是真正的热载开关 —— 改 YAML 即生效,不必重启。
// 若只在启用时注入,关掉再打开就需要重启,而 kill switch 必须在最坏的时刻可用。
//
// 预热失败不阻塞启动:快照未加载 ⇒ 全部按 inherit ⇒ 逐位等于上游。
func (Mod) InstallHooks() {
	service.QyResolveDefaultModelGroup = ResolveDefaultModelGroup
	service.QyGroupRatioMissingDenied = GroupRatioMissingDenied
	service.QyModelGroupFundingAllowed = ModelGroupFundingAllowed
	service.QyNotePinRejected = NotePinRejected
	service.QyPinnedModelGroups = PinnedModelGroups
	service.QyDeclaredUserGroups = DeclaredUserGroups
	// 模型分组备注:它**覆盖** options.UserUsableGroups 里那段说明文案。
	// 优先级与理由见 setting/qy_groupnote_export.go;实现体见 modelgroup_api.go。
	setting.QyGroupNote = ModelGroupNote

	if !enabled() {
		return
	}
	if err := reload(); err != nil {
		common.SysError("qianye/groupns: 登记快照预热失败(不阻塞启动,当前按 inherit 放行): " + err.Error())
	}
}

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	r := g.Group("/group-namespace")
	{
		r.GET("/user-groups", adminListUserGroups)
		r.GET("/model-groups", adminListModelGroups)
		r.GET("/report", adminReport)
		// 删除/改名之前必须能看到影响面。只读,不限流 ——
		// 它是那两个写接口的**前置条件**,限流它等于让运营更倾向于跳过预览。
		r.GET("/user-groups/:name/impact", adminUserGroupImpact)
		// 写接口一律挂 CriticalRateLimit:它们决定"空分组令牌去哪个池子",
		// 一次误配影响一整组用户。
		r.POST("/backfill", middleware.CriticalRateLimit(), adminBackfill)
		r.PUT("/user-groups/:name/default", middleware.CriticalRateLimit(), adminSetDefaultModelGroup)
		// 生命周期三件套。删除与改名会横跨两个数据库改六张表,
		// 一次误点影响一整档人的可用分组与账单,限流档次与上面一致。
		r.POST("/user-groups", middleware.CriticalRateLimit(), adminCreateUserGroup)
		r.PUT("/user-groups/:name", middleware.CriticalRateLimit(), adminUpdateUserGroup)
		r.POST("/user-groups/:name/rename", middleware.CriticalRateLimit(), adminRenameUserGroup)
		r.DELETE("/user-groups/:name", middleware.CriticalRateLimit(), adminDeleteUserGroup)
		// 「一键迁移」是一个**独立动作**,不是删除的一个选项。
		//
		// 迁移那段逻辑此前只作为删除的副产品存在,而删除对 default 是硬拒的 ——
		// 于是「把这一档人挪走」这件事在界面上无路可走。同一份实现
		// (model.QyRewriteUserGroupTx)两个入口,不是两份实现。
		r.POST("/user-groups/:name/migrate", middleware.CriticalRateLimit(), adminMigrateUserGroup)

		// ── 模型分组这一侧 ────────────────────────────────────────────
		//
		// 只有备注/显示名/启停/排序与**删除联动**,刻意**没有新建与改名**:
		//   新建 一个模型分组是不是存在,由 options.GroupRatio 与 abilities 回答,
		//        在这里凭空登记一行只会造出一个既没有倍率也没有渠道的名字 ——
		//        那正是「预设混乱」的来源。新建仍然在上游「模型分组」页(加一行倍率)。
		//   改名 要连带重写 abilities(物化路由表)、channels.group(逗号串)、
		//        tokens.group、GroupRatio 键、GroupGroupRatio 内层键、两张授权表,
		//        而 model/channel.go 里「先提交 channels.group、再单独重建 abilities」
		//        那个非原子窗口在改名期间会让 InitChannelCache 读到半成状态。
		//        射程之外,见 groupns.go 的包注释。
		r.GET("/model-groups/:name/impact", adminModelGroupImpact)
		r.PUT("/model-groups/:name", middleware.CriticalRateLimit(), adminUpdateModelGroup)
		r.DELETE("/model-groups/:name", middleware.CriticalRateLimit(), adminDeleteModelGroup)
	}
}

// StartTasks 启动登记回填(一次立刻 + 之后周期跟进)。
func (Mod) StartTasks() {
	if !enabled() || !cfg().AutoBackfillOn() {
		return
	}
	// 启动后立刻回填一次,**刻意不走租约**。
	//
	// lease.Run 的第一次触发在一个完整周期之后,而两张登记表在那之前是空的 ——
	// 管理端三页会显示"一个分组都没有",那与"回填坏了"长得一模一样。
	// 不加租约的代价只是多节点各扫一次表(几百行、一次),而回填本身是幂等的
	// (只新增 + OnConflict DoNothing),并发跑不会互相覆盖。
	gopool.Go(func() { runBackfill(context.Background()) })

	// 之后按周期跟进新出现的分组名。这一档走租约:它是全局唯一工作,
	// 多节点同时跑只是 N 份全表扫描,没有额外收益。
	lease.Run("groupns.backfill", 6*time.Hour, runBackfill)
}

func runBackfill(ctx context.Context) {
	res, err := Backfill(ctx, model.DB, db.Get(), 0)
	if err != nil {
		common.SysError("qianye/groupns: 登记回填失败: " + err.Error())
		return
	}
	if res.UserGroupsAdded == 0 && res.ModelGroupsAdded == 0 {
		return
	}
	common.SysLog(fmt.Sprintf(
		"qianye/groupns: 登记回填新增 %d 个用户分组、%d 个模型分组(全部 default_mode=inherit,零行为变化)",
		res.UserGroupsAdded, res.ModelGroupsAdded))
	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupns: 回填后重载快照失败: " + err.Error())
	}
}

func init() { module.Register(Mod{}) }

// ─────────────────────── 套餐余额:与 planentitlement 之间的接缝 ───────────────────────

// planUnlockState 回答「这个模型分组是不是该用户的套餐解锁的、那些套餐还有没有余额」。
//
// 抽成注入接缝而不是直接 import planentitlement:与
// groupmatrix.PlanUnlockEnabled 同一个手法。两个模块互不 import,注入在
// qianye/modules/planentitlement 的 InstallHooks 里发生一次。
//
// 默认实现返回 (false, false) ⇒ ModelGroupFundingAllowed 的第二项永远不成立
// ⇒ 闸门恒放行。订阅侧没接入时这条路径行为中性。
var PlanFundedUnlock = func(userId int, modelGroup string) (unlocked bool, funded bool) {
	return false, false
}

// shadowDenies 是影子期「本可拒绝」的进程内计数。
//
// 刻意只放进程内、不落库:它的用途只有一个 —— 回答"翻 enforce 安全吗"。
// 落库会需要一张表、一套清理、一次并发写,而那些成本买不到更多的判断力。
var (
	shadowMu     sync.Mutex
	shadowDenies = map[string]int64{}
)

func noteShadowFundingDeny(userId int, userGroup, modelGroup string) {
	key := userGroup + "→" + modelGroup
	// 这条跑在计费路径上,但只在"套餐已耗尽 + 用户分组不含它"这一档,量极小。
	// 用 guard.HotAsync 派发以免任何一次 map 竞争影响请求。
	guard.HotAsync("groupns.shadow_funding_deny", func(context.Context) error {
		shadowMu.Lock()
		shadowDenies[key]++
		n := shadowDenies[key]
		shadowMu.Unlock()
		if n == 1 || n%100 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/groupns: [影子] 用户 %d 的 %s 本可被套餐耗尽闸门拒绝,累计 %d 次 —— "+
					"当前 funding_gate_mode=shadow,请求照常放行。数字稳定后再翻 enforce",
				userId, key, n))
		}
		return nil
	})
}

// ShadowFundingDenies 返回影子期计数的快照,供健康面板使用。
func ShadowFundingDenies() map[string]int64 {
	shadowMu.Lock()
	defer shadowMu.Unlock()
	out := make(map[string]int64, len(shadowDenies))
	for k, v := range shadowDenies {
		out[k] = v
	}
	return out
}
