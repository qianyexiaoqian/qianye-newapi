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
	service.QyWalletMayCoverSubscriptionShortfall = WalletMayCoverSubscriptionShortfall
	service.QyNoteSubscriptionWriteOff = NoteShortfallWriteOff
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
		// 写接口一律挂 CriticalRateLimit + RootActionGate:它们决定
		// "空分组令牌去哪个池子",一次误配横跨两个数据库改六张表,影响一整档人
		// 的可用分组与账单,因此写侧整体提到超级管理员。
		//
		// **读侧刻意不连坐**:上面那四条 GET 仍是 role>=10。影响面预览是这些写
		// 动作的前置条件(限制它只会让人更倾向于跳过预览),而分组名本身在模型
		// 广场上就是公开的 —— 把读也关掉只会让 role=10 的分组矩阵页塌成空表,
		// 那与"坏了"长得一模一样。
		// 闸门排在限流**之前**:限流桶的键是 mark + 客户端 IP + 路由,与身份无关,
		// 于是被拒的越权尝试同样会消耗这一格。一个 role=10 对着同一条路由连点
		// 20 次(或一个重试脚本)就能把这条路由对**该来源 IP 上的所有人**锁死
		// 20 分钟 —— 包括超管自己。实测:role=10 连打 21 次 PUT /user-group/config
		// 得到 19 个 403 + 2 个 429,紧接着超管从同一个 IP 打同一条路由直接 429。
		// 装到反代后面时全体管理员共用同一个来源 IP,这一格就是全站唯一的一格。
		// 反过来,闸门在前时被拒的尝试一格桶都不消耗,而它们仍然逐条写审计。
		root := middleware.RootActionGate(middleware.RootActionGroupNamespaceWrite)
		r.POST("/backfill", root, middleware.CriticalRateLimit(), adminBackfill)
		r.PUT("/user-groups/:name/default", root, middleware.CriticalRateLimit(), adminSetDefaultModelGroup)
		// 生命周期三件套。删除与改名会横跨两个数据库改六张表,
		// 一次误点影响一整档人的可用分组与账单,限流档次与上面一致。
		r.POST("/user-groups", root, middleware.CriticalRateLimit(), adminCreateUserGroup)
		r.PUT("/user-groups/:name", root, middleware.CriticalRateLimit(), adminUpdateUserGroup)
		r.POST("/user-groups/:name/rename", root, middleware.CriticalRateLimit(), adminRenameUserGroup)
		r.DELETE("/user-groups/:name", root, middleware.CriticalRateLimit(), adminDeleteUserGroup)
		// 「一键迁移」是一个**独立动作**,不是删除的一个选项。
		//
		// 迁移那段逻辑此前只作为删除的副产品存在,而删除对 default 是硬拒的 ——
		// 于是「把这一档人挪走」这件事在界面上无路可走。同一份实现
		// (model.QyRewriteUserGroupTx)两个入口,不是两份实现。
		r.POST("/user-groups/:name/migrate", root, middleware.CriticalRateLimit(), adminMigrateUserGroup)

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
		r.PUT("/model-groups/:name", root, middleware.CriticalRateLimit(), adminUpdateModelGroup)
		r.DELETE("/model-groups/:name", root, middleware.CriticalRateLimit(), adminDeleteModelGroup)
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

// PlanUnlockFundingState 回答关于「模型分组 M 与该用户的套餐」的三件事:
//
//	unlocked      M 是不是该用户某张活跃套餐解锁的
//	funded        那些能为 M 出资的套餐里,还有没有一张有余额
//	allowOverflow 解锁 M 的那些活跃订阅里,有没有一张 allow_wallet_overflow=true
//
// allowOverflow **只统计解锁 M 的订阅**。用户级聚合(任一活跃订阅 O=0 就封锁)
// 会让一张与 M 毫无关系的套餐把 M 上的钱包出资一起封掉,那是本轮要废掉的行为。
//
// ── authoritative:unlocked 可以读缓存,funded/allowOverflow 必须回主库 ──
//
//	false  读 per-user 缓存(零 I/O)。只够回答 unlocked,也就是"这一档归不归
//	       钱包出资闸门管"。绝大多数请求在这一问上就短路了。
//	true   先回一次主库再作答。凡是要拿 funded/allowOverflow 下判断都必须用它。
//
// 缓存最长可以是 user_cache_seconds(默认 60s)之前的,而 funded 会在 relay 的
// 扣费事务里悄悄变假(订阅被耗尽),allowOverflow 会被运营在管理端改掉 —— 两者
// 都没有任何一处让本缓存失效。两个方向都会错,而危险的是**放行**那个方向:
// 套餐刚耗尽的那一分钟里缓存仍说"还有余额",闸门放行,钱包替一个运营明令禁止
// 钱包续付的分组付了钱。反方向是运营刚放开、用户还要再吃一分钟 403。
//
// 抽成注入接缝而不是直接 import planentitlement:与
// groupmatrix.PlanUnlockEnabled 同一个手法。两个模块互不 import,注入在
// qianye/modules/planentitlement 的 InstallHooks 里发生一次。
//
// 默认实现返回 (false, false, false) ⇒ unlocked=false ⇒ 闸门恒放行。
// 订阅侧没接入时这条路径行为中性。
var PlanUnlockFundingState = func(userId int, modelGroup string, authoritative bool) (unlocked, funded, allowOverflow bool) {
	return false, false, false
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

// enforceDenies 是 enforce 档**真的拒了人**的进程内计数。
//
// 影子档有计数而生效档没有,是这个闸门此前最大的盲区:调用方带
// ErrOptionWithNoRecordErrorLog(不进 error log 表),被拒的请求也不写消费日志 ——
// 一次真实拒绝在数据库里一行痕迹都没有。翻到 enforce 之后这个盲区从"理论问题"
// 变成"上线即生效",所以两档都要能数得出来。
var enforceDenies = map[string]int64{}

func noteEnforcedFundingDeny(userId int, userGroup, modelGroup string) {
	key := userGroup + "→" + modelGroup
	guard.HotAsync("groupns.enforce_funding_deny", func(context.Context) error {
		shadowMu.Lock()
		enforceDenies[key]++
		n := enforceDenies[key]
		shadowMu.Unlock()
		if n == 1 || n%100 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/groupns: [生效] 用户 %d 的 %s 被钱包出资闸门拒绝(403 AccessDenied),累计 %d 次 —— "+
					"该模型分组纯靠套餐解锁、套餐额度已用尽,且套餐设置了不允许钱包续付。"+
					"要放开请把该套餐的 allow_wallet_overflow 打开,或把 funding_gate_mode 调回 shadow",
				userId, key, n))
		}
		return nil
	})
}

// EnforcedFundingDenies 返回生效档拒绝计数的快照,供健康面板使用。
func EnforcedFundingDenies() map[string]int64 {
	shadowMu.Lock()
	defer shadowMu.Unlock()
	out := make(map[string]int64, len(enforceDenies))
	for k, v := range enforceDenies {
		out[k] = v
	}
	return out
}

// writeOffQuota 是 enforce 档**核销掉**的额度总量(按 用户分组→模型分组 分桶)。
//
// 它与 enforceDenies 计的是两件不同的事,不能合并:
//
//	enforceDenies  请求**没跑**,平台一分钱没花,用户吃 403。
//	writeOffQuota  请求**跑完了**,上游 token 已经烧掉,套餐扣到上限为止,
//	               剩下这一段谁也不出 —— 平台自己吃下。
//
// 后者是真金白银的免单,所以计的是**额度**而不是次数:运营要看的是"这个月因为
// 这条规则少收了多少",次数回答不了。
//
// 只登记**真的落定了**的核销:闸门说"不得补收"之后还要去领核销名额
// (model.ClaimSubscriptionWriteOff,每张套餐每个重置周期一份),没领到的那些笔
// 最后扣的是钱包。早先在闸门里就先记一笔,于是这个数字虚高的部分恰好等于
// 已经向用户收到的钱,而并发越高虚高越多 —— 那是运营用来决定这条规则要不要
// 继续开的数字。
var writeOffQuota = map[string]int64{}

func noteShortfallWriteOff(userId int, userGroup, modelGroup string, shortfall int64) {
	key := userGroup + "→" + modelGroup
	// 刻意同步执行、不走 HotAsync:调用方紧接着就要按返回值决定钱的去向,
	// 而这一段跑在结算路径上、量极小(核销名额每张套餐每个重置周期只有一份,
	// 由 model.ClaimSubscriptionWriteOff 发放)。
	shadowMu.Lock()
	writeOffQuota[key] += shortfall
	total := writeOffQuota[key]
	shadowMu.Unlock()
	common.SysError(fmt.Sprintf(
		"qianye/groupns: [生效] 用户 %d 的 %s 结算差额 %d 被核销(钱包不补收),该键累计 %d —— "+
			"该模型分组纯靠套餐解锁、套餐额度已用尽,且套餐设置了不允许钱包续付。"+
			"请求已经跑完,这一段由平台承担;要改成照收请把该套餐的 allow_wallet_overflow 打开",
		userId, key, shortfall, total))
}

// ShortfallWriteOffs 返回已核销额度的快照,供健康面板使用。
func ShortfallWriteOffs() map[string]int64 {
	shadowMu.Lock()
	defer shadowMu.Unlock()
	out := make(map[string]int64, len(writeOffQuota))
	for k, v := range writeOffQuota {
		out[k] = v
	}
	return out
}
