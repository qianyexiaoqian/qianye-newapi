package groupmatrix

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ratio_test.go —— 倍率格子的三态语义。
//
// null(未配置/继承)、0(显式免费)、正数(显式覆盖)必须能在
// 「前端 → DTO → options JSON → 上游内存快照」这一整条链路上往返而不失真。
//
// 这条链路上有两个经典的失真点,而它们的后果都是**钱**:
//
//	float64 + omitempty  运营填的 0 在序列化时消失 → 本该免费的分组变成兜底价
//	写 0 表示清除        继承被写成显式 0        → 本该按兜底价的分组变成免费

func ptr(v float64) *float64 { return &v }

// TestRatioCellRoundTripDistinguishesUnsetFromExplicitZero 是本文件的核心断言。
//
// 判据用的是上游 GetGroupGroupRatio 的**第二个返回值** —— 那个 bool 就是
// 「这个 key 在不在」,它天然区分零值与未配置,所以扩展侧不需要再造一个
// ratio_mode 枚举来补(造了就是第二个真相源)。
func TestRatioCellRoundTripDistinguishesUnsetFromExplicitZero(t *testing.T) {
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "free": 0.5, "paid": 2, "keep": 3})

	prev := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(prev) })

	m := ratioMatrix{"vip": {"keep": 3}}
	cells, err := normalizeCells([]Cell{
		{UserGroup: "vip", ModelGroup: "free", Action: ActionSetRatio, Ratio: ptr(0)},
		{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetRatio, Ratio: ptr(0.25)},
		{UserGroup: "vip", ModelGroup: "keep", Action: ActionClearRatio},
	})
	require.NoError(t, err)
	require.True(t, applyRatioCells(m, cells), "三个动作里有两个真的改了值")

	payload, err := marshalRatioMatrix(m)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(payload))

	ratio, ok := ratio_setting.GetGroupGroupRatio("vip", "free")
	assert.True(t, ok, "显式填的 0 必须作为一个存在的 key 落库 —— 丢了它,本该免费的分组会按兜底价扣钱")
	assert.Equal(t, float64(0), ratio)

	ratio, ok = ratio_setting.GetGroupGroupRatio("vip", "paid")
	assert.True(t, ok)
	assert.Equal(t, 0.25, ratio)

	_, ok = ratio_setting.GetGroupGroupRatio("vip", "keep")
	assert.False(t, ok, "clear_ratio 必须把 key 整个删掉(回落兜底),而不是写一个 0(变成免费)")

	_, ok = ratio_setting.GetGroupGroupRatio("vip", "never_configured")
	assert.False(t, ok)
}

// TestApplyRatioCellsReportsNoChangeWhenValuesAreIdentical 守的是"别做无谓的发布"。
//
// 每一次 publish 都会广播给所有节点并触发上游选项热更新。把一个值原样写回去
// 也算改动的话,矩阵页每次保存都会在审计里留下一条"改了倍率"——
// 而事后复盘时,一条假的倍率变更记录比没有记录更误导人。
func TestApplyRatioCellsReportsNoChangeWhenValuesAreIdentical(t *testing.T) {
	m := ratioMatrix{"vip": {"paid": 0.25}}
	cells, err := normalizeCells([]Cell{
		{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetRatio, Ratio: ptr(0.25)},
		{UserGroup: "vip", ModelGroup: "absent", Action: ActionClearRatio},
	})
	require.NoError(t, err)
	assert.False(t, applyRatioCells(m, cells),
		"原样写回同一个值、清除一个本来就不存在的 key,都不构成改动")
}

// TestNormalizeCellsRejectsTheAmbiguousInputs 覆盖写入侧的语义闸门。
//
// 每一条拒绝都对应一种"看起来没问题、实际会静默改错钱或改错权限"的输入。
func TestNormalizeCellsRejectsTheAmbiguousInputs(t *testing.T) {
	cases := []struct {
		name string
		cell Cell
		why  string
	}{
		{"set_ratio 不带 ratio", Cell{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetRatio},
			"「传 null 表示清除」与「不传表示不动」必须分开,否则一次局部保存会清掉没在屏幕上的格子"},
		{"负倍率", Cell{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetRatio, Ratio: ptr(-1)},
			"负倍率会把扣费变成给用户充值"},
		{"倍率超上界", Cell{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetRatio, Ratio: ptr(1e18)},
			"倍率会被直接乘进账单,不设上界时中间结果会被推过 int32 并饱和"},
		{"授权 auto", Cell{UserGroup: "vip", ModelGroup: autoGroup, Action: ActionGrant},
			"auto 是伪分组,上游 IsUserSelectableGroup 显式拒绝它;放进清单只会让人误以为它有用"},
		{"空用户分组", Cell{ModelGroup: "paid", Action: ActionGrant},
			"空用户分组是匿名口径,它永远走上游,配了也不会被读到"},
		{"未知动作", Cell{UserGroup: "vip", ModelGroup: "paid", Action: "toggle"},
			"未知动作被静默忽略的话,运营会以为自己保存成功了"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeCells([]Cell{tc.cell})
			assert.Error(t, err, tc.why)
		})
	}
}

// TestNormalizeCellsIsOrderIndependent 守 draft_hash 的可比性。
//
// 指纹必须与前端点击顺序无关,否则运营调换两次点击的先后,预览拿到的
// draft_hash 与保存时算出来的就对不上,切 enforce 会被自己的闸门永远挡住。
func TestNormalizeCellsIsOrderIndependent(t *testing.T) {
	a, err := normalizeCells([]Cell{
		{UserGroup: "vip", ModelGroup: "b", Action: ActionGrant},
		{UserGroup: "svip", ModelGroup: "a", Action: ActionRevoke},
	})
	require.NoError(t, err)
	b, err := normalizeCells([]Cell{
		{UserGroup: "svip", ModelGroup: "a", Action: ActionRevoke},
		{UserGroup: "vip", ModelGroup: "b", Action: ActionGrant},
	})
	require.NoError(t, err)

	ha, err := hashCells(a)
	require.NoError(t, err)
	hb, err := hashCells(b)
	require.NoError(t, err)
	assert.Equal(t, ha, hb, "同一组动作换个提交顺序必须得到同一个 draft_hash")
}

// TestRatioMatrixHashIgnoresKeyOrder 守 base_ratio_hash 的可比性。
//
// 上游那一页每次保存都会重排 map 的键。用原始字符串做哈希会让"运营在上游页面
// 保存了一次但什么都没改"被误报成"数据被改过",而那条 409 的文案是
// 「请重新载入」—— 一个永远无法通过的循环。
func TestRatioMatrixHashIgnoresKeyOrder(t *testing.T) {
	a, err := hashRatioMatrix(ratioMatrix{
		"vip":  {"x": 1, "y": 2},
		"svip": {"z": 3},
	})
	require.NoError(t, err)
	b, err := hashRatioMatrix(ratioMatrix{
		"svip": {"z": 3},
		"vip":  {"y": 2, "x": 1},
	})
	require.NoError(t, err)
	assert.Equal(t, a, b)

	c, err := hashRatioMatrix(ratioMatrix{"vip": {"x": 1, "y": 2.5}, "svip": {"z": 3}})
	require.NoError(t, err)
	assert.NotEqual(t, a, c, "值变了指纹必须跟着变,否则这道闸等于不存在")
}

// TestCloneRatioMatrixIsDeep 守审计 before 快照的有效性。
//
// 浅拷的话 applyRatioCells 会把 before 一起改掉,审计里 before 与 after
// 一模一样 —— 一条看起来齐全、实际什么都没记的记录,比没有记录更难发现。
func TestCloneRatioMatrixIsDeep(t *testing.T) {
	m := ratioMatrix{"vip": {"paid": 1}}
	before := cloneRatioMatrix(m)
	cells, err := normalizeCells([]Cell{
		{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetRatio, Ratio: ptr(9)},
	})
	require.NoError(t, err)
	require.True(t, applyRatioCells(m, cells))

	assert.Equal(t, float64(1), before["vip"]["paid"], "before 快照必须留住改动前的值")
	assert.Equal(t, float64(9), m["vip"]["paid"])
}
