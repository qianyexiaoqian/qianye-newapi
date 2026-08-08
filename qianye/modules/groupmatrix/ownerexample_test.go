package groupmatrix

// ownerexample_test.go —— 项目方原话里那个例子,逐字走一遍。
//
// 「假设当前有:模型分组 1 2 3 兜底倍率都是 0.5,用户分组 1 2 3
//   用户分组1:模型分组1 倍率0.9、模型分组2 倍率0.2、模型分组3没填写,默认兜底倍率0.5
//   用户分组2:模型分组1 倍率0」
//
// 这个例子把本轮全部倍率规则压在一屏里:每一格可以单独填、不填 = 用模型分组的
// 兜底、填 0 = 显式免费。而「不填」与「填 0」在一个 float64 零值上完全同形 ——
// 本仓已经栽过三次,所以它值一条端到端用例,而不是只在 DTO 层断言指针非空。
//
// 走的是**真实的三段**:normalizeCells(DTO 归一)→ applyRatioCells + 发布
// (落 options.GroupGroupRatio)→ groupratio.Resolve(计费路径读的那一份解析)。
// 只断言中间任何一段都会漏掉真正容易断的接缝。

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/groupratio"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// republish 走 marshalRatioMatrix + 上游的整表装载,与 publishRatioMatrix 的
// 落点完全相同,只是跳过 model.UpdateOption 那一次落库(单测里没有主库)。
// 这条链上真正会失真的是**序列化与装载**,而它们两条路径共用。
func republish(t *testing.T, m ratioMatrix) error {
	t.Helper()
	payload, err := marshalRatioMatrix(m)
	require.NoError(t, err)
	return ratio_setting.UpdateGroupGroupRatioByJSONString(payload)
}

func TestOwnerExampleCrossRatioEndToEnd(t *testing.T) {
	// 三个模型分组兜底都是 0.5;三个用户分组各自也要在倍率表里有一席
	// (上游 middleware 用 ContainsGroupRatio 判"分组是否被弃用")。
	useUpstreamGroups(t, map[string]string{},
		map[string]float64{
			"模型分组1": 0.5, "模型分组2": 0.5, "模型分组3": 0.5,
			"用户分组1": 1, "用户分组2": 1, "用户分组3": 1,
		})
	prev := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(prev) })
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{}`))

	// ── 第一步:用户分组1 填 0.9 / 0.2,模型分组3 一个字都不填 ──────────
	m, _, err := loadRatioMatrix()
	require.NoError(t, err)
	cells, err := normalizeCells([]Cell{
		{UserGroup: "用户分组1", ModelGroup: "模型分组1", Action: ActionSetRatio, Ratio: ptr(0.9)},
		{UserGroup: "用户分组1", ModelGroup: "模型分组2", Action: ActionSetRatio, Ratio: ptr(0.2)},
	})
	require.NoError(t, err)
	require.True(t, applyRatioCells(m, cells))
	require.NoError(t, republish(t, m))

	// 库里只有两个键。"没填写"必须**不写键**,而不是写一个 0 或 null ——
	// 上游 GetGroupGroupRatio 靠键在不在返回 (ratio, false),写 0 就是显式免费。
	assert.Equal(t, `{"用户分组1":{"模型分组1":0.9,"模型分组2":0.2}}`,
		ratio_setting.GroupGroupRatio2JSONString(),
		"模型分组3 没填写 —— 它绝不能出现在库里")

	_, ok := ratio_setting.GetGroupGroupRatio("用户分组1", "模型分组3")
	assert.False(t, ok, "没填写 = 没有这个键")

	// 计费路径读到的三个数。
	assert.Equal(t, 0.9, groupratio.Resolve("用户分组1", "模型分组1").Ratio)
	assert.Equal(t, 0.2, groupratio.Resolve("用户分组1", "模型分组2").Ratio)
	assert.Equal(t, 0.5, groupratio.Resolve("用户分组1", "模型分组3").Ratio,
		"没填写的那一格必须走模型分组自己的兜底倍率 0.5")
	// 别的用户分组一格都没配,三个都走兜底。
	assert.Equal(t, 0.5, groupratio.Resolve("用户分组2", "模型分组1").Ratio)
	assert.Equal(t, 0.5, groupratio.Resolve("用户分组3", "模型分组2").Ratio)

	// ── 第二步:把 用户分组1 × 模型分组1 改成 0 ─────────────────────────
	//
	// 这是整条链上最容易断的一步。0 与"删掉这个键"的差价是全额,而 float64 的
	// 零值让两者在 JSON 序列化、DTO 反序列化、map 赋值三处都可能同形。
	m, _, err = loadRatioMatrix()
	require.NoError(t, err)
	cells, err = normalizeCells([]Cell{
		{UserGroup: "用户分组1", ModelGroup: "模型分组1", Action: ActionSetRatio, Ratio: ptr(0)},
	})
	require.NoError(t, err)
	require.True(t, applyRatioCells(m, cells))
	require.NoError(t, republish(t, m))

	assert.Equal(t, `{"用户分组1":{"模型分组1":0,"模型分组2":0.2}}`,
		ratio_setting.GroupGroupRatio2JSONString(),
		"键必须还在、值是 0 —— 键被删掉的表现是这一格从免费静默回到 0.5")

	ratio, ok := ratio_setting.GetGroupGroupRatio("用户分组1", "模型分组1")
	require.True(t, ok, "显式 0 必须是一个**存在的键**")
	assert.Equal(t, float64(0), ratio)
	assert.Equal(t, float64(0), groupratio.Resolve("用户分组1", "模型分组1").Ratio,
		"计费路径读到的就是 0 —— 这一格真的免费")
	assert.Equal(t, ratio_setting.GroupRatioSourceOverride,
		groupratio.Resolve("用户分组1", "模型分组1").Source,
		"来源是交叉格而不是兜底:两者今天都可能是 0,但它们回答的不是同一个问题")

	// ── 第三步:对照组 —— clear_ratio 才是"删键回落兜底" ─────────────────
	m, _, err = loadRatioMatrix()
	require.NoError(t, err)
	cells, err = normalizeCells([]Cell{
		{UserGroup: "用户分组1", ModelGroup: "模型分组1", Action: ActionClearRatio},
	})
	require.NoError(t, err)
	require.True(t, applyRatioCells(m, cells))
	require.NoError(t, republish(t, m))

	_, ok = ratio_setting.GetGroupGroupRatio("用户分组1", "模型分组1")
	assert.False(t, ok)
	assert.Equal(t, 0.5, groupratio.Resolve("用户分组1", "模型分组1").Ratio,
		"删键之后回落到兜底 0.5 —— 这正是它与「填 0」相反的那一半")
}

// TestOwnerExampleTopupRatioRejectsZero 是同一个例子在**充值倍率**上的分岔。
//
// 交叉倍率的 0 是显式免费(上面那条用例钉住了);充值倍率的 0 不是 ——
// 五处支付路径读到 0 之后一律按 1 收款(见 effectiveTopupRatio)。
// 同一个数字在两侧含义不同这件事本身没法消掉,能消掉的是"界面上说它免费"。
func TestOwnerExampleTopupRatioRejectsZero(t *testing.T) {
	prev := common.TopupGroupRatio2JSONString()
	t.Cleanup(func() { _ = common.UpdateTopupGroupRatioByJSONString(prev) })
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"用户分组1":0}`))

	assert.Equal(t, float64(0), common.GetTopupGroupRatio("用户分组1"),
		"库里确实存着 0")
	assert.Equal(t, float64(1), effectiveTopupRatio(common.GetTopupGroupRatio("用户分组1")),
		"而收款按 1 —— 管理端那一列显示的必须是这个数,不是库里那个 0")
	assert.Equal(t, effectiveTopupRatio(common.GetTopupGroupRatio("从没配过的档")),
		effectiveTopupRatio(common.GetTopupGroupRatio("用户分组1")),
		"「配 0」与「没配过」收同样的钱 —— 所以 0 不是一个能表达意图的值,写侧拒绝它")
}
