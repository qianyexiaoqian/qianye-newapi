// Package grouppricing 实现「模型按分组单独定价」。
//
// ═══════════════════════════ 语义(已拍板,不可改)═══════════════════════════
//
//	最终扣费 = 分组级模型价 × 分组倍率          (配置了分组级价格的模型)
//	最终扣费 = 全局价       × 分组倍率          (没有配置的模型,与改动前逐位一致)
//
// 是**相乘**不是覆盖。相乘的代价是运营很难心算出最终价,因此管理端的每一条规则
// 都必须直接回显折算后的最终生效价(见 api_admin.go 的 effective 字段)——
// 那是这个方案的必要配套,不是可选装饰。
//
// ═══════════════════════════ 三条铁律 ═══════════════════════════
//
//  1. **默认影子模式**。ShadowMode=true 时完整算出"若启用会扣多少"、记录差额,
//     但 hook 返回原值,实际扣费一分不变。计费改错不可逆 —— 钱已经从用户账上
//     扣走了,没有任何补偿路径能把"多扣的那部分"精确还回去。
//
//  2. **读库失败只能回落成「无覆盖」**。少一个折扣是运营问题,按一份来历不明的
//     旧规则扣钱是资损问题。快照冷启动失败 → 空快照;刷新持续失败超过
//     max_stale_seconds → 主动丢弃快照回到"无覆盖"。绝不存在"读失败 ⇒ 有覆盖"的路径。
//
//  3. **规则值在写入与快照编译两处各校验一次**。手改数据库绕过管理接口是这套
//     系统最现实的攻击面,而规则值会被直接乘进每一笔账单。快照编译时校验不过的
//     行一律**跳过**(等于无覆盖),绝不"尽量用一下"。
//
// ═══════════════════════════ 覆盖了哪些计价路径 ═══════════════════════════
//
// 覆盖分两段:计价侧三个入口(relay/helper/price.go,共 5 个插入点,
// 见 relay/helper/qy_pricing_export.go),结算侧两个挂载点、三个调用点
// (service/qy_pricing_export.go)。
//
//	ModelPriceHelper        对话/文本/音频/实时,含 Claude、OpenAI、Gemini 等全部 relay 格式
//	ModelPriceHelperPerCall MJ / Task 按次计费
//	modelPriceHelperTiered  阶梯表达式计价
//
// 结算侧(service/text_quota.go、service/quota.go 的 PostWss/PostAudio)读的是
// relayInfo.PriceData.ModelRatio / ModelPrice,而 PriceData 就是上面三个函数的
// 产物,所以那几条路径不需要再挂钩子。**有两条例外路径**(阶梯计价、Task 差额重算),
// 它们都不读 PriceData;其中阶梯计价那条有两个调用点:
//
//	service/tiered_settle.go TryTieredSettle
//	                                          阶梯计价的结算。它从 snap.ExprString
//	                                          **重跑表达式**,只乘 snap.GroupRatio ——
//	                                          预扣侧那次 QyGroupTieredQuota 的乘数到这里
//	                                          已经不存在了。因此单独挂了第五处
//	                                          service.QyGroupTieredSettle(同一个实现体
//	                                          applyTieredQuota,两侧必须得出同一个数)。
//	                                          乘数作用在 ActualQuotaBeforeGroup 上再按
//	                                          billingexpr 的同一公式重算,而不是乘已取整的
//	                                          最终值 —— 后者会引入与预扣侧不同的第二次舍入。
//
//	service/tiered_settle.go refreshTieredBillingGroup
//	                                          第五处挂载点的**第二个调用点**(同一个变量,
//	                                          不是第六处):auto 重试切分组后重算预留额。
//	                                          它拿 snap.EstimatedQuotaBeforeGroup 乘新分组的
//	                                          倍率 —— 而分组级乘数和分组倍率一样是"当前分组"
//	                                          的属性,不重算就会把原分组的乘数带进新分组的
//	                                          预留额。影响面只到**预留额**(预扣少了会误判
//	                                          余额不足,多了会冻结用户额度),最终扣费仍由
//	                                          TryTieredSettle 按最终分组算对。
//	                                          配套约束:relay/helper 那一侧的
//	                                          EstimatedQuotaBeforeGroup 必须存**未乘乘数**的
//	                                          原始表达式结果,否则这里会二次相乘。
//
//	service/task_billing.go RecalculateTaskQuotaByTokens
//	                                          Task(视频/MJ 等)拿到实际 token 数后的
//	                                          差额重算。它跑在任务轮询协程上,不经过
//	                                          RelayInfo、也不经过 PriceData,上面三个
//	                                          挂载点全都够不到,因此单独挂了第四处
//	                                          service.QyGroupTaskRatio(实现体
//	                                          applyTaskRatio)。它存在的唯一理由是让
//	                                          预扣与结算同口径:预扣走
//	                                          ModelPriceHelperPerCall(已覆盖),结算若
//	                                          仍按全局倍率,分组折扣在任务类模型上等于
//	                                          不存在,而且是以**追扣**的形式发生。
//
// ⚠ 这一处**只认 ratio 口径**。给任务类模型配 price(按次)规则时,
// applyTaskRatio 刻意返回入参原值 —— 那条路径的预扣走的是分组级按次价,
// 而 RecalculateTaskQuotaByTokens 只在模型配了全局倍率、没有按次价时才会跑,
// 两者根本不在同一个分支上,在这里跟着改倍率只会制造出第二种不一致。
// **代价是这一档确实留着一个口径缺口**:如果同一个任务类模型既有全局倍率、
// 又被配了分组级 price 规则,预扣按分组按次价、结算按全局倍率重算,差额仍会追扣。
// 这个残留没有被消除,也没有任何管理端告警会提示它(effective.go 的
// modeMismatchWarning 只判「规则口径 vs 模型全局计费口径」,这个组合不触发它)——
// 给任务类模型配规则时请用 ratio 口径。
//
// ═══════════════════════════ 已知不覆盖范围(必须知道)═══════════════════════════
//
// 以下两处直接调用 ratio_setting,不经过 PriceData,因此**不受分组价影响**。
// 它们不是遗漏,是刻意不碰 —— 每一条都会额外消耗上游改动预算,而收益不成比例:
//
//	service/quota.go  PreWssConsumeQuota      Realtime 会话中途的增量预扣。
//	                                          它只是"够不够扣"的余额闸,真正的
//	                                          结算走 PostWssConsumeQuota(读 PriceData,
//	                                          已覆盖)。分组价更低时这道闸偏严,
//	                                          偏严不会多扣钱。
//	model/pricing.go                          模型广场的价格展示,与扣费无关。
//
// ═══════════════════════════ 为什么不覆盖补全倍率 ═══════════════════════════
//
// completion_ratio 决定输出 token 相对输入 token 的价格倍数。不覆盖它有两个理由:
//
//  1. 覆盖 model_ratio 已经等比例缩放了输入与输出两侧的价格,"这个分组按 X 折
//     付费"这个真实诉求已经满足;改 completion_ratio 改的是输入/输出的**相对**
//     价格结构,那是换一套定价模型,不是打折。
//  2. 更要命的是它破坏线性。实际扣费对 model_ratio 是严格线性的
//     (quota = 加权 token 数 × model_ratio × 分组倍率),所以影子模式可以用
//     "实际扣费 × (新值/旧值 - 1)"精确算出差额;而 completion_ratio 改变的是
//     加权 token 数本身的权重,同一个 factor 不再成立,差额只能估算。
//     影子模式的全部价值就是那个精确的差额数字。
package grouppricing

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Mod 是分组定价模块的注册入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "grouppricing" }

func (Mod) Tables() []any {
	return []any{
		&Rule{},
		&RuleVersion{},
		&ShadowBucket{},
	}
}

// InstallHooks 注入五个挂载点(relay/helper 的三个计价点 + service 的两个结算点),
// 并预热一次规则快照。
//
// 预热是必要的:第一个请求到来时快照若还是空的,那次请求就按全局价计费。
// 影子模式下这只是少一条记录,真实模式下就是一次计错的账。失败不阻塞启动 ——
// 空快照的语义是"无覆盖",是安全的那一侧。
func (Mod) InstallHooks() {
	if !config.Get().GroupPricing.Enabled {
		return
	}
	relayhelper.QyGroupModelPrice = applyModelPrice
	relayhelper.QyGroupModelRatio = applyModelRatio
	relayhelper.QyGroupTieredQuota = applyTieredQuota
	// 阶梯计价的结算侧走另一条路径(从表达式重跑,不读 PriceData),
	// 少这一行会让分组折扣在结算时丢失,差额以追扣落到用户头上。
	service.QyGroupTieredSettle = applyTieredQuota
	// 第四处在结算侧,不在计价链路上:Task 的 token 差额重算不经过 PriceData。
	// 少这一行,给任务类模型配的分组折扣会在差额结算时被追扣回全局价。
	service.QyGroupTaskRatio = applyTaskRatio

	if err := reload(true); err != nil {
		common.SysError("qianye/grouppricing: 规则快照预热失败(本次启动后的请求先按全局价计费,稍后自动重试): " + err.Error())
	}
}

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/group-pricing/rules", adminListRules)
	g.GET("/group-pricing/shadow/summary", adminShadowSummary)

	// 写接口改的是每一笔请求的扣费公式,一律挂关键操作限流,并且全部写审计。
	crit := middleware.CriticalRateLimit()
	g.POST("/group-pricing/rules", crit, adminCreateRule)
	g.PUT("/group-pricing/rules/:id", crit, adminUpdateRule)
	g.DELETE("/group-pricing/rules/:id", crit, adminDeleteRule)
	// preview 是只读试算,不改任何状态,但它是运营在输入框里边打边看的接口,
	// 挂搜索级限流而不是关键操作限流。
	g.POST("/group-pricing/preview", middleware.SearchRateLimit(), adminPreview)
}

func (Mod) StartTasks() {
	if !config.Get().GroupPricing.Enabled {
		return
	}
	// flush 刻意不加租约:每个节点持有自己的内存影子计数,必须各自落库,
	// 累加 upsert + 唯一索引保证多节点结果被正确合并到同一行。
	startShadowFlush()
	// 清理是全局唯一工作,必须走租约:common.IsMasterNode 只是个环境变量,
	// 多节点都配成 master 时会并发删同一批行。
	lease.Run("grouppricing.shadow_gc", shadowGCInterval, runShadowGC)
}

func init() { module.Register(Mod{}) }
