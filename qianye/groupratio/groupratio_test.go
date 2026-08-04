package groupratio

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setGroupRatios 显式构造 ratio_setting 的状态并在用例结束后还原。
// 这两张表是进程级全局状态,不还原会让相邻用例互相污染。
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
	ResetObservedForTest()
}

// TestResolveMatchesUpstreamBranches:Resolve 的三种结果必须与上游的两个分支对齐。
//
// 判据是"金额来自 service.GetUserGroupRatio",而 Resolve 额外回答的只是
// "这个数字从哪来"。附加信息如果与金额不自洽,它就变成了第二个真相源。
func TestResolveMatchesUpstreamBranches(t *testing.T) {
	setGroupRatios(t,
		`{"default":1,"vip":0.5,"free":0}`,
		`{"paid":{"vip":0.3},"zero":{"vip":0}}`)

	cases := []struct {
		name        string
		userGroup   string
		modelGroup  string
		wantRatio   float64
		wantSource  string
		wantBase    float64
		wantMissing bool
	}{
		{"命中专属倍率", "paid", "vip", 0.3, SourceOverride, 0.5, false},
		{"专属倍率显式为 0", "zero", "vip", 0, SourceOverride, 0.5, false},
		{"未命中,回落兜底", "nobody", "vip", 0.5, SourceInherit, 0.5, false},
		{"兜底本身为 0", "nobody", "free", 0, SourceInherit, 0, false},
		{"匿名口径", "", "vip", 0.5, SourceInherit, 0.5, false},
		{"模型分组不在倍率表:上游 fail-open 返回 1", "nobody", "ghost", 1, SourceInherit, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.userGroup, tc.modelGroup)
			assert.Equal(t, tc.wantRatio, got.Ratio)
			assert.Equal(t, tc.wantSource, got.Source)
			assert.Equal(t, tc.wantBase, got.Base)
			assert.Equal(t, tc.wantMissing, got.BaseMissing)
		})
	}
}

// TestResolveDoesNotFoldCase:分组名精确匹配,绝不折叠大小写。
//
// 倍率侧 GetGroupGroupRatio / GetGroupRatio 都是精确 map 查找,而它们在 3 条
// 计费路径上,我们无权改。这边折叠、那边不折叠,就会造出"管理端显示 0.5、
// 实际按兜底 1.0 扣费、零告警"的新骗人数字 —— 正是本轮要消灭的形状。
// 近似项只作为 warning 列出,不替运营做决定(设计裁定 C4)。
func TestResolveDoesNotFoldCase(t *testing.T) {
	setGroupRatios(t, `{"VIP":0.5}`, `{}`)

	got := Resolve("", "vip")
	assert.True(t, got.BaseMissing, "vip 与 VIP 是两个分组,不许折叠")
	assert.Equal(t, float64(1), got.Ratio, "上游 fail-open 返回 1")
	assert.Equal(t, "VIP", got.BaseNearMiss, "近似项必须被点名,否则运营会以为「配了没生效」")

	exact := Resolve("", "VIP")
	assert.False(t, exact.BaseMissing)
	assert.Empty(t, exact.BaseNearMiss)
}

// TestObservedRegistersMisses:失配必须留下一个能被查询的信号。
//
// 上游对这件事的全部处理是一行 common.SysLog —— 日志会被滚走,没有界面、
// 没有计数器。这条登记簿就是"除 SysLog 之外的可观测信号"本身。
func TestObservedRegistersMisses(t *testing.T) {
	setGroupRatios(t, `{"default":1}`, `{}`)

	require.Empty(t, Observed(), "fixture 应当从干净状态开始")

	Resolve("", "ghost")
	Resolve("someone", "ghost")
	Resolve("", "default")

	observed := Observed()
	require.Len(t, observed, 1, "只有 ghost 是失配的;default 在倍率表里,不该被记")
	assert.Equal(t, "ghost", observed[0].Group)
	assert.Equal(t, int64(2), observed[0].Count, "重复命中要累加,否则看不出「已经影响了多少次」")
	assert.NotZero(t, observed[0].FirstSeen)
	assert.GreaterOrEqual(t, observed[0].LastSeen, observed[0].FirstSeen)
}

// TestScanWithoutMainDBIsDiagnosable:主库不可用时 Scan 必须给出可诊断的半份结果。
//
// 两件事同时成立才算合格:
//  1. 不返回 nil 切片 —— nil 会被序列化成 JSON null,前端对着 null 调 .map 白屏
//     (判据见 qianye/json_array_guard_test.go);
//  2. Error 非空 —— 否则一份空的 orphans 看起来就像"站点很干净",
//     而失败最可能发生的时刻正是最需要打开这一页的时刻。
func TestScanWithoutMainDBIsDiagnosable(t *testing.T) {
	setGroupRatios(t, `{"default":1,"vip":0.5}`, `{}`)

	res := Scan(context.Background(), true)
	assert.NotNil(t, res.Orphans, "下发给前端的数组不许是 nil")
	assert.Empty(t, res.Orphans)
	assert.NotEmpty(t, res.Error, "扫描没跑成必须说出来,不能沉默返回一份空结果")
	assert.Equal(t, 2, res.DefinedGroups)
	assert.NotZero(t, res.At)
}

// TestHealthAlwaysHasObservedArray:/admin/health 那一段的形状。
//
// last_scan 缺省表示"本进程还没扫过",与"扫过且没问题"必须能被分开 ——
// 所以刻意不用一个空结果去冒充,而是整个字段不出现。
func TestHealthAlwaysHasObservedArray(t *testing.T) {
	setGroupRatios(t, `{"default":1}`, `{}`)

	h := Health()
	observed, ok := h["observed"].([]Miss)
	require.True(t, ok, "observed 必须是切片而不是 nil interface")
	assert.NotNil(t, observed)
	assert.NotContains(t, h, "last_scan", "还没扫过就不该有 last_scan")

	Scan(context.Background(), true)
	assert.Contains(t, Health(), "last_scan")
}
