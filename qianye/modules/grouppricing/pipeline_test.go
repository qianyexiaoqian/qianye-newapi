package grouppricing

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	relaykittypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// pipeline_test.go —— 分组定价的**行为级**端到端回归。
//
// # 它补的是哪一段
//
// hook_test.go 直接调用 applyModelPrice / applyModelRatio / applyTieredQuota。
// 那三个确实是生产实现体,但它们要生效必须经过两段接线:
//
//	InstallHooks()  把三个函数赋给 relayhelper.QyGroupXxx 这三个全局变量
//	price.go        在五个位置调用那三个全局变量
//
// 第二段由 hookpoint_test.go 的 AST 断言守着,**第一段谁都没守**:把
// grouppricing.go 里 `relayhelper.QyGroupModelRatio = applyModelRatio` 这一行删掉,
// hook_test 照样全绿(它绕过变量直接调函数)、hookpoint_test 照样全绿
// (它只看 price.go 的源码),而全站分组价已经彻底失效。这正是本扩展反复出现的
// 那个形状 —— "调用了却没生效",只不过这次断链发生在赋值那一步。
//
// 因此本文件从上游真正的计价入口 relayhelper.ModelPriceHelper /
// ModelPriceHelperPerCall 驱动,断言产出的 PriceData 里的**钱**变了(真实模式)
// 或没变但差额被记下了(影子模式)。这条链路覆盖:price.go 调用点 → 全局变量 →
// InstallHooks 赋值 → hook 实现 → 规则快照 → 扩展库里的规则行。
//
// # 四个挂载点,一个都不能漏
//
// 第四个挂载点 service.QyGroupTaskRatio 不在 relay 计价链路上(它在 Task 的异步
// 差额结算里),前三条用例一条都碰不到它,曾经整段掏空全绿。它由
// TestTaskRatioHookVariableIsWiredAndApplies 单独驱动,
// 调用点本身则由 hookpoint_test.go 的 AST 断言锁住。

// pipelineModel 刻意用一个上游默认表里不存在的模型名:
// 它的全局倍率完全由本文件设定,不受上游默认定价表变化的影响。
const pipelineModel = "qy-grouppricing-pipeline-probe"

// pipelineGroup 取 vip:上游默认 group_ratio 里它是 1,
// 这样 PriceData 里的数字直接反映模型倍率,不掺入分组倍率的干扰。
const pipelineGroup = "vip"

// installHooksForTest 走真正的 InstallHooks,而不是自己给三个全局变量赋值。
//
// 自己赋值就把被测的那段接线绕过去了 —— 那正是本文件存在的理由。
func installHooksForTest(t *testing.T) {
	t.Helper()
	prevPrice := relayhelper.QyGroupModelPrice
	prevRatio := relayhelper.QyGroupModelRatio
	prevTiered := relayhelper.QyGroupTieredQuota
	prevTask := service.QyGroupTaskRatio
	t.Cleanup(func() {
		relayhelper.QyGroupModelPrice = prevPrice
		relayhelper.QyGroupModelRatio = prevRatio
		relayhelper.QyGroupTieredQuota = prevTiered
		service.QyGroupTaskRatio = prevTask
	})
	Mod{}.InstallHooks()
}

// useModelRatio 给探针模型设一个全局倍率,并在用例结束后原样还回去。
//
// 必须还原:ratio_setting 的表是整个测试进程共享的全局状态。
func useModelRatio(t *testing.T, name string, ratio float64) {
	t.Helper()
	before, err := common.Marshal(ratio_setting.GetModelRatioCopy())
	require.NoError(t, err)

	next := ratio_setting.GetModelRatioCopy()
	next[name] = ratio
	blob, err := common.Marshal(next)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(blob)))

	t.Cleanup(func() {
		_ = ratio_setting.UpdateModelRatioByJSONString(string(before))
	})
}

func newPricingContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

// replaceRule 把规则表换成唯一一条规则并重建快照。
func replaceRule(t *testing.T, gdb *gorm.DB, group, modelName, mode, value string) {
	t.Helper()
	resetCaches()
	require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
	seedRule(t, gdb, group, modelName, mode, value)
}

// TestModelPriceHelperAppliesGroupRatioOverride 是主计价入口(对话/文本/音频/实时)的
// 端到端回归:同一次调用,规则命中与不命中必须算出不同的预扣额度。
//
// 基线不是"关掉模块",而是"规则挂在另一个分组上" —— 两次调用走的是逐行相同的
// 代码路径,唯一的差别就是这条规则该不该命中本次请求。
func TestModelPriceHelperAppliesGroupRatioOverride(t *testing.T) {
	useConfig(t, true, false) // 真实模式
	syncHotAsync(t)
	gdb := newTestDB(t)
	useModelRatio(t, pipelineModel, 2)
	installHooksForTest(t)

	c := newPricingContext(t)
	const promptTokens = 1000

	replaceRule(t, gdb, "some-other-group", pipelineModel, ModeRatio, "0.5")
	base, err := relayhelper.ModelPriceHelper(c,
		relayInfo(pipelineGroup, pipelineGroup, pipelineModel),
		promptTokens, &relaykittypes.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, float64(2), base.ModelRatio, "基线必须是全局倍率")
	require.Positive(t, base.QuotaToPreConsume, "基线预扣额度必须非零,否则下面的比值断言无意义")

	replaceRule(t, gdb, pipelineGroup, pipelineModel, ModeRatio, "0.5")
	got, err := relayhelper.ModelPriceHelper(c,
		relayInfo(pipelineGroup, pipelineGroup, pipelineModel),
		promptTokens, &relaykittypes.TokenCountMeta{})
	require.NoError(t, err)

	assert.Equal(t, 0.5, got.ModelRatio,
		"分组价必须经由 relay/helper 的真实计价入口生效 —— hook 实现对了但没接上变量时,这里仍是 2")
	assert.Equal(t, base.QuotaToPreConsume/4, got.QuotaToPreConsume,
		"倍率 2 → 0.5,预扣额度必须缩到四分之一;数字没变就说明这条链路上有一段没接上")
}

// TestModelPriceHelperShadowModeChargesUpstreamPriceButRecordsDiff 锁定影子模式的两半,
// 同样从上游入口驱动。
//
// 只断言"扣费没变"是不够的:hook 根本没接上时扣费同样不变,这条断言会变成永真。
// 因此必须同时断言影子差额被记了下来 —— 那条记录只可能由跑在真实计价链路上的
// hook 产生。
func TestModelPriceHelperShadowModeChargesUpstreamPriceButRecordsDiff(t *testing.T) {
	useConfig(t, true, true) // 影子模式
	syncHotAsync(t)
	gdb := newTestDB(t)
	useModelRatio(t, pipelineModel, 2)
	installHooksForTest(t)

	replaceRule(t, gdb, pipelineGroup, pipelineModel, ModeRatio, "0.5")
	got, err := relayhelper.ModelPriceHelper(newPricingContext(t),
		relayInfo(pipelineGroup, pipelineGroup, pipelineModel),
		1000, &relaykittypes.TokenCountMeta{})
	require.NoError(t, err)

	assert.Equal(t, float64(2), got.ModelRatio, "影子模式下真实扣费必须一分不变")
	assert.Equal(t, 2000, got.QuotaToPreConsume, "1000 token × 全局倍率 2 × 分组倍率 1")

	buckets := drainToBuckets(t)
	require.Len(t, buckets, 1, "影子差额必须由真实计价链路上的 hook 记下来")
	assert.Equal(t, pipelineGroup, buckets[0].GroupName)
	assert.Equal(t, pipelineModel, buckets[0].ModelName)
	assert.Equal(t, ModeRatio, buckets[0].Mode)
	assert.Equal(t, "2", buckets[0].OldValue)
	assert.Equal(t, "0.5", buckets[0].NewValue)
}

// TestModelPriceHelperPerCallAppliesPriceOverride 覆盖 MJ / Task 的按次计价入口。
//
// 这条路径与上一条走的是不同的函数、不同的插入点(QyGroupModelPrice 在
// ModelPriceHelperPerCall 里),只测 ModelPriceHelper 覆盖不到它。
func TestModelPriceHelperPerCallAppliesPriceOverride(t *testing.T) {
	useConfig(t, true, false)
	syncHotAsync(t)
	gdb := newTestDB(t)
	// 给探针模型配上全局倍率,这样"未命中"那一侧走的是按量计费分支而不是报错分支。
	useModelRatio(t, pipelineModel, 2)
	installHooksForTest(t)

	c := newPricingContext(t)

	replaceRule(t, gdb, "some-other-group", pipelineModel, ModePrice, "0.02")
	base, err := relayhelper.ModelPriceHelperPerCall(c,
		relayInfo(pipelineGroup, pipelineGroup, pipelineModel))
	require.NoError(t, err)
	require.False(t, base.UsePrice, "基线:没有按次价,回落到按量计费")

	replaceRule(t, gdb, pipelineGroup, pipelineModel, ModePrice, "0.02")
	got, err := relayhelper.ModelPriceHelperPerCall(c,
		relayInfo(pipelineGroup, pipelineGroup, pipelineModel))
	require.NoError(t, err)

	assert.True(t, got.UsePrice, "分组级按次价必须把计费口径切成按次")
	assert.Equal(t, 0.02, got.ModelPrice)
	assert.Equal(t, common.QuotaFromFloat(0.02*common.QuotaPerUnit), got.Quota)
	assert.NotEqual(t, base.Quota, got.Quota, "按次价覆盖必须改变实际预扣额度")
}

// TestTieredHookVariableIsWiredAndApplies 覆盖阶梯计价那个挂载点。
//
// 阶梯入口需要一整套表达式配置才能从 ModelPriceHelper 驱动起来,代价与收益不成
// 比例;但被测的断链点是"InstallHooks 有没有给这个全局变量赋值",所以直接调用
// price.go 真正调用的那个变量即可 —— 与 hook_test.go 里直接调 applyTieredQuota
// 的关键差别就在这里:少了那行赋值,这条会红,那条不会。
func TestTieredHookVariableIsWiredAndApplies(t *testing.T) {
	t.Run("真实模式下乘数生效", func(t *testing.T) {
		useConfig(t, true, false)
		syncHotAsync(t)
		gdb := newTestDB(t)
		installHooksForTest(t)
		replaceRule(t, gdb, pipelineGroup, pipelineModel, ModeTiered, "0.5")

		assert.Equal(t, float64(50),
			relayhelper.QyGroupTieredQuota(relayInfo(pipelineGroup, pipelineGroup, pipelineModel), 100),
			"price.go 调的是这个变量:没被 InstallHooks 赋值时它是恒等函数,结果会是 100")
	})

	t.Run("影子模式下不改扣费", func(t *testing.T) {
		useConfig(t, true, true)
		syncHotAsync(t)
		gdb := newTestDB(t)
		installHooksForTest(t)
		replaceRule(t, gdb, pipelineGroup, pipelineModel, ModeTiered, "0.5")

		assert.Equal(t, float64(100),
			relayhelper.QyGroupTieredQuota(relayInfo(pipelineGroup, pipelineGroup, pipelineModel), 100))
		require.Len(t, drainToBuckets(t), 1, "影子模式必须留下差额记录,否则等于 hook 压根没跑")
	})
}

// TestTaskRatioHookVariableIsWiredAndApplies 覆盖第四个挂载点:Task 异步差额结算。
//
// 这是四个挂载点里唯一一个不在 relay 计价链路上的 —— 它跑在任务轮询协程上,
// 拿不到 RelayInfo,也不经过 PriceData,因此上面三条用例一条都碰不到它。
// 复核实测:把 hook.go 的 `return rule.ValueFloat` 改成 `return ratio`、
// 把 grouppricing.go 的赋值行删掉、把 task_billing.go 的调用行删掉,
// 三个环节任改其一,全仓测试都是全绿。
//
// 生产后果:给视频/MJ 这类任务模型配了 ratio 分组折扣时,预扣走
// ModelPriceHelperPerCall 按折扣价,结算 RecalculateTaskQuotaByTokens 按全局倍率
// 重算,差额以**追扣**形式补上 —— 用户先看到便宜的预扣,再被补一刀。
//
// 与 hook_test.go 直接调 applyTaskRatio 的关键差别:这里调的是 task_billing.go
// 真正调用的那个变量 service.QyGroupTaskRatio,少了 InstallHooks 里那行赋值,
// 这条会红,那条不会。
func TestTaskRatioHookVariableIsWiredAndApplies(t *testing.T) {
	const globalRatio = 2

	t.Run("真实模式下按分组倍率结算", func(t *testing.T) {
		useConfig(t, true, false)
		syncHotAsync(t)
		gdb := newTestDB(t)
		installHooksForTest(t)
		replaceRule(t, gdb, pipelineGroup, pipelineModel, ModeRatio, "0.5")

		assert.Equal(t, 0.5,
			service.QyGroupTaskRatio(pipelineGroup, pipelineModel, globalRatio),
			"task_billing.go 调的是这个变量:没被 InstallHooks 赋值、或实现体退化成返回入参时,结果会是 2 —— "+
				"预扣按 5 折、结算按全局价,差额以追扣形式落到用户头上")
	})

	t.Run("规则挂在别的分组时不生效", func(t *testing.T) {
		useConfig(t, true, false)
		syncHotAsync(t)
		gdb := newTestDB(t)
		installHooksForTest(t)
		replaceRule(t, gdb, "some-other-group", pipelineModel, ModeRatio, "0.5")

		assert.Equal(t, float64(globalRatio),
			service.QyGroupTaskRatio(pipelineGroup, pipelineModel, globalRatio),
			"基线:同一条代码路径,唯一差别是这条规则该不该命中本分组")
	})

	t.Run("影子模式下不改结算金额", func(t *testing.T) {
		useConfig(t, true, true)
		syncHotAsync(t)
		gdb := newTestDB(t)
		installHooksForTest(t)
		replaceRule(t, gdb, pipelineGroup, pipelineModel, ModeRatio, "0.5")

		assert.Equal(t, float64(globalRatio),
			service.QyGroupTaskRatio(pipelineGroup, pipelineModel, globalRatio),
			"影子模式必须与上游逐位一致:这条路径拿不到 RelayInfo,不落影子差额,只能一分不改")
	})

	t.Run("模块关闭时是恒等函数", func(t *testing.T) {
		useConfig(t, false, false)
		syncHotAsync(t)
		gdb := newTestDB(t)
		installHooksForTest(t)
		replaceRule(t, gdb, pipelineGroup, pipelineModel, ModeRatio, "0.5")

		assert.Equal(t, float64(globalRatio),
			service.QyGroupTaskRatio(pipelineGroup, pipelineModel, globalRatio))
	})

	// price 口径刻意不生效:预扣那一侧走的是按次价,根本不进入本函数所在的
	// token 重算分支(RecalculateTaskQuotaByTokens 只在 hasRatioSetting 时才跑),
	// 这里跟着改倍率反而会制造出预扣与结算的第二种不一致。
	// 这是一个残留的不一致口径,包注释里如实写着,不能被"顺手支持一下"抹掉。
	t.Run("price 口径的规则不改倍率", func(t *testing.T) {
		useConfig(t, true, false)
		syncHotAsync(t)
		gdb := newTestDB(t)
		installHooksForTest(t)
		replaceRule(t, gdb, pipelineGroup, pipelineModel, ModePrice, "0.02")

		assert.Equal(t, float64(globalRatio),
			service.QyGroupTaskRatio(pipelineGroup, pipelineModel, globalRatio),
			"按次价规则不得被当成倍率乘进 token 重算 —— 那是第二种预扣/结算不一致")
	})
}

// TestDisabledModuleLeavesUpstreamPricingUntouched:总开关关掉时,
// 上游计价入口必须与没有本扩展时逐位相同 —— InstallHooks 直接不赋值。
func TestDisabledModuleLeavesUpstreamPricingUntouched(t *testing.T) {
	useConfig(t, false, false)
	syncHotAsync(t)
	gdb := newTestDB(t)
	useModelRatio(t, pipelineModel, 2)
	installHooksForTest(t)
	replaceRule(t, gdb, pipelineGroup, pipelineModel, ModeRatio, "0.5")

	got, err := relayhelper.ModelPriceHelper(newPricingContext(t),
		relayInfo(pipelineGroup, pipelineGroup, pipelineModel),
		1000, &relaykittypes.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, float64(2), got.ModelRatio)
	assert.Equal(t, 2000, got.QuotaToPreConsume)
	assert.Equal(t, float64(100),
		relayhelper.QyGroupTieredQuota(relayInfo(pipelineGroup, pipelineGroup, pipelineModel), 100))
	assert.Equal(t, float64(2), service.QyGroupTaskRatio(pipelineGroup, pipelineModel, 2))
}
