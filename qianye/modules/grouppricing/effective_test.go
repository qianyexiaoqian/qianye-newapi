package grouppricing

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// effective_test.go —— 「管理端显示的价」与「热路径真正乘的倍率」必须同口径。
//
// 这个缺陷不是假设:改动前 computeEffective 用的是
// ratio_setting.GetGroupRatio(模型分组)(单参数),而热路径
// relay/helper/price.go 的 HandleGroupRatio 在
// GetGroupGroupRatio(用户分组, 使用分组) 命中时会**整体替换**倍率。
// 本站已经配了若干条 GroupGroupRatio,所以那两个数字早就对不上了。
//
// 这条测试是防它第二次漂开的唯一办法:它不重新实现一份"正确答案",
// 而是直接把 computeEffective 的产物与 HandleGroupRatio 的产物对齐。
// 上游哪天改了倍率解析的分支,这里立刻变红。

// setGroupRatios 显式构造 ratio_setting 的状态并在用例结束后还原。
//
// 这两张表是进程级全局状态,不还原会让相邻用例互相污染,而污染的表现是
// "单独跑绿、一起跑红"—— 在一个决定扣多少钱的模块里,这种不确定性本身就是缺陷。
func setGroupRatios(t *testing.T, groupRatio, groupGroupRatio string) {
	t.Helper()
	prevGroup := ratio_setting.GroupRatio2JSONString()
	prevPair := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(prevGroup))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(prevPair))
	})
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(groupRatio))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(groupGroupRatio))
}

// hotPathGroupRatio 走上游真实的计价入口拿出这次请求实际会乘的分组倍率。
func hotPathGroupRatio(userGroup, usingGroup string) float64 {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{UserGroup: userGroup, UsingGroup: usingGroup}
	return relayhelper.HandleGroupRatio(c, info).GroupRatio
}

// TestEffectiveRatioMatchesHotPath:折算用的倍率必须逐位等于热路径乘的倍率。
func TestEffectiveRatioMatchesHotPath(t *testing.T) {
	// 分组名全用小写:computeEffective 会对**模型分组**走 normalizeGroup(折叠大小写,
	// 与规则表的存储口径一致),而热路径拿的是原样的 UsingGroup。大小写不同的
	// 情形是另一个独立缺陷(由 ratioWarning 的 BaseNearMiss 负责喊出来),
	// 不能混进这条同口径断言里当噪音。
	setGroupRatios(t,
		`{"default":1,"vip":0.5,"free":0}`,
		`{"paid":{"vip":0.3},"zero":{"vip":0},"other":{"default":2}}`)

	cases := []struct {
		name       string
		userGroup  string
		modelGroup string
		want       string
	}{
		{"有专属倍率:整体替换兜底值", "paid", "vip", "0.3"},
		{"专属倍率显式为 0:免费,不是「没配」", "zero", "vip", "0"},
		{"无专属倍率:回落模型分组的兜底倍率", "nobody", "vip", "0.5"},
		{"兜底口径(空用户分组)", "", "vip", "0.5"},
		{"兜底倍率本身为 0", "nobody", "free", "0"},
		{"模型分组不在倍率表:上游 fail-open 按 1.0", "nobody", "ghost", "1"},
		{"专属倍率配在别的模型分组上,不串味", "other", "vip", "0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeEffective(tc.userGroup, tc.modelGroup, "gpt-4o", ModeRatio, dec("1"))
			assert.Equal(t, tc.want, got.GroupRatio, "折算用的倍率不对")

			hot := normalizeDecimal(decimal.NewFromFloat(hotPathGroupRatio(tc.userGroup, tc.modelGroup)))
			assert.Equal(t, hot, got.GroupRatio,
				"管理端显示 %s、热路径实际乘 %s —— 这正是本轮要修的那个骗人的数字",
				got.GroupRatio, hot)
		})
	}
}

// TestEffectiveRatioSourceAndSpread:来源标注与倍率跨度。
//
// 运营看到 0 的时候必须能一眼分清「我给这个用户分组配的 0」和「这个模型分组
// 兜底就是 0」。source + base_group_ratio 这两个字段就是那个区分。
func TestEffectiveRatioSourceAndSpread(t *testing.T) {
	setGroupRatios(t,
		`{"default":1,"vip":0.5}`,
		`{"paid":{"vip":0.3},"cheap":{"vip":0.1}}`)

	override := computeEffective("paid", "vip", "gpt-4o", ModeRatio, dec("1"))
	assert.Equal(t, "override", override.RatioSource)
	assert.Equal(t, "0.3", override.GroupRatio)
	assert.Equal(t, "0.5", override.BaseGroupRatio, "兜底值必须同时给出,否则看不出专属倍率改了什么")

	inherit := computeEffective("nobody", "vip", "gpt-4o", ModeRatio, dec("1"))
	assert.Equal(t, "inherit", inherit.RatioSource)
	assert.Equal(t, inherit.BaseGroupRatio, inherit.GroupRatio)

	// 跨度覆盖兜底值 ∪ 全部专属倍率:0.1 / 0.3 / 0.5。
	assert.Equal(t, "0.1", override.GroupRatioMin)
	assert.Equal(t, "0.5", override.GroupRatioMax)
	assert.False(t, override.Uniform,
		"存在不同倍率时 uniform 必须为 false —— 前端据此决定显示一个数还是一个区间")

	// 没有任何专属倍率的模型分组:跨度退化成一个点,列表可以照旧显示一个数。
	uniform := computeEffective("", "default", "gpt-4o", ModeRatio, dec("1"))
	assert.True(t, uniform.Uniform)
	assert.Equal(t, uniform.GroupRatioMin, uniform.GroupRatioMax)
}

// TestEffectiveByUserGroupRows:展开行的内容与顺序。
func TestEffectiveByUserGroupRows(t *testing.T) {
	setGroupRatios(t,
		`{"default":1,"vip":0.5}`,
		`{"paid":{"vip":0.3},"cheap":{"vip":0.1},"unrelated":{"default":2}}`)

	rows := effectiveByUserGroup("vip", "gpt-4o", ModeRatio, dec("2"))
	require.Len(t, rows, 3, "兜底行 + 两个真的配了专属倍率的用户分组;unrelated 配在别的模型分组上,不该出现")

	assert.Equal(t, "*", rows[0].UserGroup, "兜底口径必须排在第一行:它是覆盖人数最多的那一档")
	assert.Equal(t, "inherit", rows[0].Source)
	assert.Equal(t, "1", rows[0].RuleEffective, "2 × 0.5")

	assert.Equal(t, "cheap", rows[1].UserGroup, "明细行按分组名排序,顺序必须稳定")
	assert.Equal(t, "override", rows[1].Source)
	assert.Equal(t, "0.2", rows[1].RuleEffective, "2 × 0.1")

	assert.Equal(t, "paid", rows[2].UserGroup)
	assert.Equal(t, "0.6", rows[2].RuleEffective, "2 × 0.3")
}

// TestDeltaPercentIsRatioInvariant:涨跌幅与分组倍率无关。
//
// 涨跌幅 = 新值/旧值 − 1,分组倍率在这个比值里约掉了。展开成"每用户分组一行"
// 之后最容易发生的误解就是以为这一列也随倍率变;这条断言把它钉住,
// 顺便说明了 reconcile.go 的差额公式为什么对 GroupGroupRatio 免疫。
func TestDeltaPercentIsRatioInvariant(t *testing.T) {
	setGroupRatios(t, `{"vip":0.5}`, `{"paid":{"vip":0.3},"cheap":{"vip":0.1}}`)

	rows := effectiveByUserGroup("vip", "gpt-4o", ModeTiered, dec("2"))
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.Equal(t, rows[0].DeltaPercent, row.DeltaPercent,
			"用户分组 %s 的涨跌幅与兜底行不同 —— 倍率本该在比值里约掉", row.UserGroup)
	}
	// tiered 口径的全局基准恒为 1,所以 2 就是 +100%。
	assert.Equal(t, "100.00", rows[0].DeltaPercent)
}

// TestRatioWarningOnMissingGroup:模型分组不在倍率表时必须出告警。
//
// 这是上游 GetGroupRatio 那条 fail-open 在管理端的落点:它返回 1、只写一行会被
// 滚走的 SysLog,而这条告警是运营唯一能看见的信号。
func TestRatioWarningOnMissingGroup(t *testing.T) {
	setGroupRatios(t, `{"default":1,"VIP":0.5}`, `{}`)

	missing := computeEffective("", "ghost", "gpt-4o", ModeRatio, dec("1"))
	assert.Contains(t, missing.RatioWarning, "fail-open",
		"模型分组不在倍率表里,必须明说上游会按 1.0 倍静默计费")

	// normalizeGroup 会把规则里的模型分组折叠成小写,而倍率表里存的是 "VIP" ——
	// 倍率侧是精确匹配,二者是两个分组。这种情形不折叠、只告警(设计裁定 C4)。
	nearMiss := computeEffective("", "VIP", "gpt-4o", ModeRatio, dec("1"))
	assert.Contains(t, nearMiss.RatioWarning, "大小写",
		"仅大小写不同的近似项必须被点名,否则运营会以为「配了没生效」")

	fine := computeEffective("", "default", "gpt-4o", ModeRatio, dec("1"))
	assert.Empty(t, fine.RatioWarning)
}

// TestNoStaleTaskCrossCellWarning:Task 交叉格告警必须已经消失。
//
// 上游 service/task_billing.go 的差额结算本轮改成了按 (users.group, task.Group)
// 查交叉格,与预扣(HandleGroupRatio 的 (UserGroup, UsingGroup))同口径。
// 那条告警因此不再成立 —— 一条不成立的告警和一个错误的数字一样糟:运营会为它
// 放弃一整类配置。源码形状锁见 hookpoint_test.go 的
// TestTaskSettlementUsesCrossCellGroupRatio。
func TestNoStaleTaskCrossCellWarning(t *testing.T) {
	setGroupRatios(t, `{"vip":0.5}`, `{"paid":{"vip":0.3},"vip":{"vip":0.5}}`)

	cross := computeEffective("paid", "vip", "gpt-4o", ModeRatio, dec("1"))
	assert.NotContains(t, cross.RatioWarning, "Task",
		"交叉格不该再出 Task 差额告警:上游那条对角线缺陷已经修好")
	assert.Equal(t, "0.3", cross.GroupRatio,
		"交叉格的倍率必须是专属倍率本身")

	rows := effectiveByUserGroup("vip", "gpt-4o", ModeRatio, dec("1"))
	for _, row := range rows {
		assert.Empty(t, row.Warning, "展开行同样不该再带 Task 告警")
	}
}

// TestEffectiveResolvesRatioByRealGroupRatioKey 守本轮修掉的一条**头条数字骗人**的路径。
//
// 规则表的键是折叠过的(rules.go 的 normalizeGroup),倍率表是精确 map 查找。
// 两边直接用同一个折叠名时,含大写的模型分组在倍率侧整个落空:
// 头条倍率退化成 fail-open 的 1.0、overriddenUserGroups 永远查不到那个分组,
// 于是 Uniform 算成 true —— 界面断言「这个价对所有用户分组成立」,
// 而真正在打折的那一档根本不在展开行里。
func TestEffectiveResolvesRatioByRealGroupRatioKey(t *testing.T) {
	setGroupRatios(t, `{"default":1,"VIP":0.3}`, `{"paid":{"VIP":0.1}}`)

	// 规则里存的是折叠后的 "vip",倍率表里是 "VIP"。
	e := computeEffective("paid", "VIP", "gpt-4o", ModeRatio, dec("1"))
	assert.Equal(t, "0.1", e.GroupRatio,
		"必须命中 (paid, VIP) 的专属倍率,而不是 fail-open 的 1")
	assert.Equal(t, "0.3", e.BaseGroupRatio)
	assert.False(t, e.Uniform,
		"配了专属倍率就不该断言「这个价对所有用户分组成立」")
	assert.Contains(t, e.RatioWarning, "大小写",
		"两处写法不一致必须点名,否则运营不知道这个数字来自另一个名字")

	rows := effectiveByUserGroup("VIP", "gpt-4o", ModeRatio, dec("1"))
	require.Len(t, rows, 2, "兜底行 + paid 一行")
	assert.Equal(t, wildcardUserGroup, rows[0].UserGroup)
	assert.Equal(t, "0.3", rows[0].GroupRatio)
	assert.Equal(t, "paid", rows[1].UserGroup)
	assert.Equal(t, "0.1", rows[1].GroupRatio)
}
