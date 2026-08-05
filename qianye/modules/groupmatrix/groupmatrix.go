// Package groupmatrix 实现「用户分组 × 模型分组」的权威可选清单。
//
// ═══════════════════════════ 正名(不改数据模型)═══════════════════════════
//
//	users.group                        →  用户分组
//	channels.group / abilities.group   →  模型分组
//
// 底层仍是同一个字符串命名空间,正名只发生在文案层。「把一个用户分组名同时当
// 模型分组用」在数据上仍然可能,保存时只能警告不能禁止 —— 这是既定方案的已知代价,
// 因此管理端界面必须**始终把两个轴分列显示**,绝不合并成一个下拉。
//
// ═══════════════════════════ 「大杂烩」的根源 ═══════════════════════════
//
// 上游 setting.userUsableGroups 是一份**全站共用**的白名单,对所有用户分组一视同仁。
// service.GetUserUsableGroups 在它之上用 GroupSpecialUsableGroup 做 +:X / -:X 增删,
// 最后**无条件把 userGroup 自己塞回去** —— 于是「把一个分组从它自己的清单里删掉」
// 这条规则永远无效(本站实测就有这么一条一直没生效的 `-:自己`)。
// 结果就是:只要在全局白名单里,谁都能选。
//
// ═══════════════════════════ 本模块做到的 ═══════════════════════════
//
// GetUserUsableGroups(userGroup) 的结果,在本模块启用、且该用户分组在扩展库里
// 有 scope 行、且 mode=enforce 时,**完全由 grants 决定**:
// 不叠加全局白名单、不应用 +:/-:、不无条件补自己。
//
// 未配置的用户分组保持上游原行为,而且是**逐位一致**(返回上游那一张 map 的指针)。
// 这是回退能力的基础,由 bitexact_test.go 与 hookpoint_test.go 两层钉死。
//
// ═══════════════════════════ 三条与直觉相反的事实 ═══════════════════════════
//
//  1. **倍率不在这个模块里。** 唯一真相源是上游 options.GroupGroupRatio。
//     扩展库只存成员资格,一个倍率字节都不存。理由见 store.go 顶部。
//
//  2. **快照陈旧时不丢弃,永久沿用最后一份成功快照。** 与 grouppricing 相反,
//     理由见 snapshot.go 的 activeSnapshot:陈旧的钱丢弃更安全,
//     陈旧的可见性保留更安全 —— 丢弃只能回落到上游宽松白名单,而那意味着
//     被收紧的用户重新可以把令牌指向 ratio=0 的免费分组。
//
//  3. **把用户分组 U 从它自己的清单里删掉,不会让 U 的用户发不出请求。**
//     middleware/auth.go 的 `if tokenGroup != ""` 让**空分组令牌**整段绕过检查,
//     直接用 users.group 当 UsingGroup。所以删除对角线只影响
//     「能不能把令牌显式钉在 U 上」。这句话必须出现在矩阵对角线格子的提示里,
//     否则运营会以为自己在做一件比实际严重得多的事。
//
// ═══════════════════════════ 射程之外(必须知道)═══════════════════════════
//
//   - 空分组令牌(本站 200 个)结构性免疫本模块的一切收紧。它们的 UsingGroup
//     恒等于 users.group;若那个分组不在 GroupRatio 里,上游 GetGroupRatio 会
//     **静默返回 1 并只写一条 SysLog**,按原价扣费且零告警。本模块只能做探测器
//     (orphans.go 的 users.group 检查),改不了那条 fail-open。
//
//   - service/task_billing.go 的 Task 差额结算原本写的是
//     `GetGroupGroupRatio(group, group)` —— 只命中矩阵**对角线**,而预扣走的是
//     (用户分组, 模型分组) 的交叉格。令牌做了分组覆盖且配了交叉倍率时,
//     Task 类模型的预扣与结算不同口径,差额以**追扣**落到用户头上。
//     本方案把交叉格从"偶尔配的例外"提升为主要机制,矩阵页还用「免费」这个
//     一等公民操作直接引导运营配出触发条件,因此**本轮已修**:第一个实参改成
//     所有者的 users.group。形状由 hookpoint_test.go 的
//     TestTaskBillingUsesCrossCellGroupRatio 锁住,grouppricing 那侧原来的
//     taskCrossCellWarning 已同步删除(留着一条不成立的告警和显示一个错误的
//     数字一样糟)。
//
//   - /pg/chat/completions 走 UserAuth 而不是 TokenAuth,ContextKeyUsingGroup
//     在那条链路上恒为空串,于是上游的 playground 分组覆盖校验实际比对的是
//     **匿名口径**的全局白名单,权威清单一条都不生效。本轮用
//     service.QyPlaygroundGroupAllowed 这个 hook 补上(默认实现与上游逐字同义),
//     实现体只做一件事:空 usingGroup 换成令牌所有者的用户分组再判定。
package groupmatrix

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Mod 是权威可选清单模块的注册入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "groupmatrix" }

func (Mod) Tables() []any {
	return []any{
		&Scope{},
		&Grant{},
		&WriteDeny{},
		&Seen{},
	}
}

// StartTasks 启动「新分组默认全遮断」的对账任务。
//
// ══════════════ 为什么这一条必须走 lease.Run,而快照刷新不能 ══════════════
//
// 两者的判断刚好相反,别照抄隔壁:
//
//	快照刷新  是**每个节点各自持有**的进程内缓存。lease.Run 只会让一个节点跑,
//	          其余节点的清单永远陈旧,所以它走请求驱动的 CAS(见 snapshot.go)。
//	新分组对账 是**写库**。多节点同时跑会对同一个新分组各建一次 scope 行、
//	          各写一条审计 —— 唯一索引挡得住重复行,挡不住重复审计与重复日志,
//	          而"这个分组被自动遮断了 N 次"会让复盘的人以为发生了 N 次事件。
//
// 未启用时不注册任务:与 InstallHooks 的无条件注入不同,后台任务不是 kill
// switch 的一部分,而 lease 表里多一行永远不会被续约的租约只是噪声。
// new_group_default_deny 关掉时**仍然注册** —— 那一档只登记不遮断,
// 而登记恰恰是它必须继续做的事(见 reconcileNewGroups 的说明)。
func (Mod) StartTasks() {
	if !enabled() {
		return
	}
	lease.Run("groupmatrix.new_group_scan",
		time.Duration(newGroupScanInterval())*time.Second, runNewGroupScan)
}

// InstallHooks 注入两个挂载点并预热一次清单快照。
//
// 预热是必要的,但它的失败方向与 grouppricing 相反:那边空快照 = 无覆盖(安全),
// 这边快照未加载 = **恒等返回上游**(宽松)。因此预热失败不阻塞启动,
// 但 /admin/health 会一直显示 snapshot_loaded=false,并且写侧校验同步放行 ——
// 一个状态同时控制读写两侧,不允许 enforce 从写侧先漏进来。
//
// 注入无条件发生(不看 enabled):两个实现体自己判 enabled(),
// 这样 group_matrix.enabled 就是真正的热载开关 —— 改 YAML 即生效,不必重启。
// 若只在启用时注入,关掉再打开就需要重启,而 kill switch 必须在最坏的时刻可用。
func (Mod) InstallHooks() {
	service.QyResolveUsableGroups = Resolve
	service.QyCheckTokenGroupChange = CheckTokenGroup
	service.QyPlaygroundGroupAllowed = PlaygroundGroupAllowed

	if !enabled() {
		return
	}
	if err := reload(); err != nil {
		common.SysError("qianye/groupmatrix: 可选清单快照预热失败(本次启动后先按上游全局白名单放行," +
			"权威清单暂不生效,稍后自动重试): " + err.Error())
	}
}

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/group-matrix", adminGetMatrix)
	// orphans 是常驻基线,不只在保存流程里闪现:545 这个数字必须在运营按保存
	// 之前就摆在那里并标明「与本次改动无关」,否则第一次预览的巨大数字会让人
	// 直接放弃这个功能。
	g.GET("/group-matrix/orphans", adminOrphans)

	// preview 是纯只读试算,但它要扫 tokens/users/logs,挂搜索级限流。
	g.POST("/group-matrix/preview", middleware.SearchRateLimit(), adminPreview)

	// 写接口改的是"谁能发出请求"与"按什么倍率扣钱",一律挂关键操作限流,全部写审计。
	crit := middleware.CriticalRateLimit()
	g.PUT("/group-matrix", crit, adminPutMatrix)
	g.PUT("/group-matrix/scope/:user_group", crit, adminPutScope)
	g.POST("/group-matrix/repair-token", crit, adminRepairToken)
}

func init() { module.Register(Mod{}) }
