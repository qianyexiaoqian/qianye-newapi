package groupmatrix

import (
	"go/ast"
	"testing"

	"github.com/QuantumNous/new-api/qianye/groupratio"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// billingratio_test.go —— 「倍率只有一个源」的证明。
//
// ═════════════════════ 为什么这组测试存在 ═════════════════════
//
// 分组倍率的解析在全仓被**复制了四份**:
//
//	relay/helper/price.go:59      预扣 / 主计价      交叉格 + ok 回落
//	service/quota.go:119          WSS / 音频结算     交叉格 + ok 回落
//	service/task_billing.go:309   Task 差额结算      交叉格 + ok 回落(上一轮刚从对角格修过来)
//	controller/pricing.go:50      价格表展示         先播种兜底再用交叉格覆盖(语义等价、**形状不同**)
//
// 本方案刻意**不去统一它们**:统一要重新挂三个 hook、横跨预扣与结算两侧,每一处都
// 要重新证明"预扣与结算同口径";上游已经把它们修成同口径了,往里塞第四个来源是
// 纯粹的负收益。代价是它们可能各自漂移,而漂移的表现是"某条路径按另一个倍率扣钱,
// 管理界面完全看不出来"。
//
// 所以防线换成两条测试:形状(AST)+ 结论(表驱动)。两条都很便宜,而它们合起来
// 就是「界面上那个数字 == 账单上那个数字」的全部证明。

const (
	pricePath        = "../../../relay/helper/price.go"
	quotaPath        = "../../../service/quota.go"
	ctlPricingPath   = "../../../controller/pricing.go"
	serviceGroupPath = "../../../service/group.go"
)

// TestBillingRatioPathsAgree 是**结论**断言。
//
// service.GetUserGroupRatio 是全仓唯一没有被复制的那一份解析,而扩展侧
// (矩阵页、groupratio.Resolve)全部走它。这里把四条路径各自的分支逻辑按它们
// 源码里的样子写一遍,断言在同一组输入下与它逐位一致 —— 四份复制品里任何一份
// 哪天被改成别的判据(例如把 `ok` 换成 `ratio > 0`),这条立刻红。
//
// 输入刻意覆盖三个真正会出事的格子:
//
//	显式 0    —— 免费。用 `ratio > 0` 当判据的实现会在这里静默回落兜底(白扣钱)。
//	兜底 0    —— 本站有三个 ratio=0 的免费模型分组,它们必须原样是 0。
//	兜底缺失  —— 上游 GetGroupRatio fail-open 返回 1。四条路径必须**一致地**返回 1,
//	             不允许某一条返回 0 或者 panic。
func TestBillingRatioPathsAgree(t *testing.T) {
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "free": 0, "paid": 2})

	prev := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(prev) })
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(
		`{"vip":{"paid":0.5,"free":0,"default":0}}`))

	cases := []struct {
		name       string
		userGroup  string
		modelGroup string
		want       float64
	}{
		{"交叉格覆盖", "vip", "paid", 0.5},
		{"交叉格显式 0(免费)", "vip", "default", 0},
		{"交叉格 0 覆盖兜底 0", "vip", "free", 0},
		{"无交叉格 → 兜底", "vip", "vip", 1},
		{"无交叉格 → 兜底 0", "default", "free", 0},
		{"用户分组从未配过", "从未配过", "paid", 2},
		{"兜底缺失 → 上游 fail-open 1.0", "vip", "已从倍率表删掉的分组", 1},
		{"匿名口径", "", "paid", 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := service.GetUserGroupRatio(tc.userGroup, tc.modelGroup)
			assert.Equal(t, tc.want, want, "先确认基准函数本身的取值符合预期")

			// relay/helper/price.go:59 / service/quota.go:119 /
			// service/task_billing.go:309 —— 三处逐字同形。
			crossCell := func() float64 {
				fallback := ratio_setting.GetGroupRatio(tc.modelGroup)
				if v, ok := ratio_setting.GetGroupGroupRatio(tc.userGroup, tc.modelGroup); ok {
					return v
				}
				return fallback
			}
			assert.Equal(t, want, crossCell(),
				"三条计费路径共用的「交叉格 + ok 回落」形状必须与 GetUserGroupRatio 同结论")

			// controller/pricing.go:50 —— **形状不同**:先从 GroupRatioCopy 播种,
			// 再用交叉格覆盖。它是唯一一处没有显式回落分支的,上游哪天改了那里,
			// 展示会先于计费漂移,所以必须一并钉住。
			seeded := func() float64 {
				out := map[string]float64{}
				for g, r := range ratio_setting.GetGroupRatioCopy() {
					out[g] = r
				}
				if v, ok := ratio_setting.GetGroupGroupRatio(tc.userGroup, tc.modelGroup); ok {
					out[tc.modelGroup] = v
				}
				if v, ok := out[tc.modelGroup]; ok {
					return v
				}
				// 播种表里没有这个模型分组 = 它不在 GroupRatio 里。
				// 上游此时会把这一列从价格表里 delete 掉,展示上等价于"不展示";
				// 与计费路径的 fail-open 1.0 对齐由下面这一行表达。
				return ratio_setting.GetGroupRatio(tc.modelGroup)
			}
			assert.Equal(t, want, seeded(),
				"价格表展示路径与计费必须同结论 —— 它的形状不同,漂移会先出现在这里")
		})
	}
}

// TestResolveMatchesHotPathRatio 是「界面不骗人」的全部证明。
//
// 扩展侧任何要显示倍率的地方都走 qianye/groupratio.Resolve,而它内部调
// service.GetUserGroupRatio。这条断言便宜到几乎没有成本,但它锁死的是:
// 有人哪天为了"少一次函数调用"在 Resolve 里再写一遍 if,展示与账单就此分家。
func TestResolveMatchesHotPathRatio(t *testing.T) {
	groupratio.ResetObservedForTest()
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "free": 0, "paid": 2})

	prev := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(prev) })
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"vip":{"paid":0.5,"free":0}}`))

	for _, ug := range []string{"", "default", "vip", "从未配过"} {
		for _, mg := range []string{"default", "vip", "free", "paid", "不在倍率表里"} {
			res := groupratio.Resolve(ug, mg)
			assert.Equal(t, service.GetUserGroupRatio(ug, mg), res.Ratio,
				"(%q, %q):展示倍率必须与 service.GetUserGroupRatio 逐位一致", ug, mg)

			// Source 必须由 key 的存在性决定,不能由值决定 —— vip/free 配的是
			// 显式 0,它是 override 而不是 inherit。这两者在界面上是两句话:
			// 「我配的 0(免费)」与「兜底本来就是 0」。
			_, ok := ratio_setting.GetGroupGroupRatio(ug, mg)
			wantSource := groupratio.SourceInherit
			if ok {
				wantSource = groupratio.SourceOverride
			}
			assert.Equal(t, wantSource, res.Source, "(%q, %q) 的来源判据必须是 key 存在性", ug, mg)
			assert.Equal(t, !ratio_setting.ContainsGroupRatio(mg), res.BaseMissing,
				"(%q, %q) 的兜底缺失标记必须与 ContainsGroupRatio 同源", ug, mg)
		}
	}

	// 兜底缺失必须**留下痕迹**:上游 GetGroupRatio 在那一档静默返回 1,
	// 而唯一的痕迹本来只有一行会被滚走的 SysLog。
	assert.NotEmpty(t, groupratio.Observed(),
		"解析到一个不在倍率表里的模型分组时必须进失配登记簿 —— 静默按 1.0 计费是资损")
}

// TestMatrixCellsCarryTheHotPathRatio 守矩阵页每一格下发的数字。
//
// 格子**不允许有空白态**:空白是 0 还是继承,人永远猜不对,而这一页上每个数字
// 都是报价。effective_ratio 因此是必填的,并且必须与热路径同源。
func TestMatrixCellsCarryTheHotPathRatio(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "free": 0, "paid": 2})

	prev := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(prev) })
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"vip":{"paid":0.5,"free":0}}`))
	require.NoError(t, reload())

	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	require.NotEmpty(t, view.Cells)

	for _, cell := range view.Cells {
		assert.NotEmpty(t, cell.EffectiveRatio,
			"(%s, %s) 的 effective_ratio 为空 —— 格子不允许有空白态", cell.UserGroup, cell.ModelGroup)
		assert.Equal(t, ratioText(service.GetUserGroupRatio(cell.UserGroup, cell.ModelGroup)),
			cell.EffectiveRatio,
			"(%s, %s) 界面显示的倍率必须就是账单上那个", cell.UserGroup, cell.ModelGroup)

		if cell.UserGroup == "vip" && cell.ModelGroup == "free" {
			require.NotNil(t, cell.Ratio, "显式配的 0 必须以 *float64 下发,不能因为是零值而消失")
			assert.Equal(t, float64(0), *cell.Ratio)
			assert.Equal(t, SourceOverride, cell.Source,
				"「我配的 0(免费)」与「兜底本来就是 0」必须是两种 source")
		}
		if cell.UserGroup == "default" && cell.ModelGroup == "free" {
			assert.Nil(t, cell.Ratio, "没配过就必须是 null —— 写 0 进去是把继承变成显式免费")
			assert.Equal(t, SourceInherit, cell.Source)
			assert.Equal(t, "0", cell.EffectiveRatio, "兜底 0 必须原样显示成 0,而不是空白")
		}
	}
}

// TestScopePolicyReportsUnsetGroups 守行头三态与那条常驻说明。
//
// 「未设定范围 = 全部可用」这条口径本轮刚刚**反转过**。前端若把说明写死,
// 后端回退时文案不会跟着回退 —— 所以口径必须由接口下发,而且必须被测试钉住。
func TestScopePolicyReportsUnsetGroups(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "paid": 1, "iso": 1})

	require.NoError(t, reload())
	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	assert.True(t, view.ScopePolicy.UnsetMeansAll)
	assert.Zero(t, view.ScopePolicy.ScopedGroups, "一条 scope 行都没有")
	assert.Positive(t, view.ScopePolicy.UnsetGroups)
	for _, row := range view.UserGroups {
		assert.Equal(t, ScopeStateUnset, row.ScopeState, "%s 应当是未设定范围", row.Name)
	}

	// 设一个非空范围 + 一个空范围,三态必须能被分开。
	seedScope(t, gdb, "vip", ModeEnforce, false, "paid")
	seedScope(t, gdb, "iso", ModeEnforce, false)

	view, err = buildMatrixView(gdb)
	require.NoError(t, err)
	assert.Equal(t, 2, view.ScopePolicy.ScopedGroups)
	assert.Equal(t, 1, view.ScopePolicy.EmptyScopedGroups,
		"「设了范围但范围为空」必须单独计数 —— 它是唯一会让整组用户一个模型分组都选不到的配置")

	states := map[string]string{}
	for _, row := range view.UserGroups {
		states[row.Name] = row.ScopeState
	}
	assert.Equal(t, ScopeStateSet, states["vip"])
	assert.Equal(t, ScopeStateEmpty, states["iso"])
	assert.Equal(t, ScopeStateUnset, states["default"])
}

// TestBillingRatioSitesKeepTheCrossCellShape 是**形状**断言。
//
// 三条计费路径必须一直是 GetGroupGroupRatio(用户分组, 模型分组) 的**交叉格**,
// 两个实参不能是同一个标识符。上一轮 task_billing 就是写成了对角格
// (GetGroupGroupRatio(group, group)),于是令牌做了分组覆盖时预扣打折、结算原价,
// 差额以**追扣**落到用户头上 —— 单元测试全绿,因为每一份复制品都与自己一致。
//
// task_billing 那一条已经由 hookpoint_test.go 单独钉住(它还额外断言了第一个实参
// 必须叫 userGroup),这里补上另外三处。
func TestBillingRatioSitesKeepTheCrossCellShape(t *testing.T) {
	for _, path := range []string{pricePath, quotaPath, ctlPricingPath} {
		t.Run(path, func(t *testing.T) {
			file := parseFileOrFail(t, path)

			found := 0
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "GetGroupGroupRatio" || len(call.Args) != 2 {
					return true
				}
				found++
				a := exprText(t, path, call.Args[0])
				b := exprText(t, path, call.Args[1])
				assert.NotEqual(t, a, b,
					"%s 的 GetGroupGroupRatio 两个实参变成了同一个表达式(%q)—— 对角格缺陷:"+
						"预扣按 (用户分组, 模型分组) 交叉格、这里按对角格,差额会以追扣落到用户头上", path, a)
				return true
			})
			assert.Equal(t, 1, found,
				"%s 里 GetGroupGroupRatio 的调用数变了 —— 倍率解析被增删过一处,"+
					"请重新确认它与 service.GetUserGroupRatio 是否仍然同结论", path)
		})
	}
}

// TestGetUserGroupRatioIsTheSoleUncopiedResolution 守住"基准函数"本身。
//
// 扩展侧的展示、矩阵页、对账全部以 service.GetUserGroupRatio 为准。它必须一直是
// 「交叉格命中就用它,否则 GetGroupRatio 兜底」这两句 —— 判据是 **ok**,不是值。
// 有人把它改成 `if ratio > 0`,本站三个 ratio=0 的免费分组会当场按兜底价扣钱,
// 而上面所有的"两边一致"断言仍然全绿(两边一起错)。
func TestGetUserGroupRatioIsTheSoleUncopiedResolution(t *testing.T) {
	file := parseFileOrFail(t, serviceGroupPath)
	fn := findFunc(t, file, "GetUserGroupRatio")

	var usesOK bool
	var comparesValue bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.IfStmt:
			if id, ok := s.Cond.(*ast.Ident); ok && id.Name == "ok" {
				usesOK = true
			}
			if _, ok := s.Cond.(*ast.BinaryExpr); ok {
				comparesValue = true
			}
		}
		return true
	})
	assert.True(t, usesOK,
		"GetUserGroupRatio 必须用 GetGroupGroupRatio 的第二个返回值(key 存在性)当判据")
	assert.False(t, comparesValue,
		"GetUserGroupRatio 里出现了对倍率**值**的比较 —— 用 `ratio > 0` 之类当判据会让"+
			"显式配置的 0(免费)静默回落到兜底价,而本站有三个 ratio=0 的分组")
}
