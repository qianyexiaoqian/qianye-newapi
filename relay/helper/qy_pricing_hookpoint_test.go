package helper

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qy_pricing_hookpoint_test.go —— 用 AST 锁死本包 price.go 里三个计价挂载点的
// 位置与顺序。
//
// # 它为什么在这里,而不是在扩展模块里
//
// 这份断言原来住在 qianye/modules/grouppricing/hookpoint_test.go。那个模块已经
// 下线,但它断的东西**从来就不属于那个模块** —— 它断的是「上游 price.go 的这三个
// 插入点还在不在、顺序对不对」,而插入点归 relay/helper 自己。跟着模块一起删掉,
// 等于把一份用真金白银试出来的知识连同模块一起扔了。
//
// # 为什么必须是 AST 断言
//
// 计价接缝被审计出的缺陷全都**不在算术里,而在调度层**:hook 的实现可以做得完美
// 无缺,只要 price.go 里少插一行,那条计价路径就静默地按全局价扣钱,而运营从管理
// 界面上完全看不出来。数值型单测抓不到这个 —— 它们直接调纯函数,根本不经过
// price.go。只有把"调用点确实存在"本身变成断言,回滚才会让测试变红。
//
// # 接缝当前是空置的
//
// grouppricing 下线之后没有任何代码给这三个变量赋值(由
// qianye.TestPricingHooksAreDeliberatelyVacant 断言),它们是恒等函数,计价结果与
// 上游逐位一致。**接缝空置不等于接缝该删**:删掉它们要改本文件之外 5 行真上游
// 代码,而保留是 0 行。详见 qy_pricing_export.go 的包注释。

const qyPriceGoPath = "price.go"

// qyPricingEntries 是本包的三个计价入口。它们的产物(PriceData)被结算侧直接使用。
var qyPricingEntries = []string{"ModelPriceHelper", "ModelPriceHelperPerCall", "modelPriceHelperTiered"}

// TestQyHookPointsExistInEveryPricingEntry:三个入口上的五个插入点一个都不能少。
func TestQyHookPointsExistInEveryPricingEntry(t *testing.T) {
	calls := qyCallsByFunc(t, qyPriceGoPath)

	want := map[string][]string{
		"ModelPriceHelper":        {"QyGroupModelPrice", "QyGroupModelRatio"},
		"ModelPriceHelperPerCall": {"QyGroupModelPrice", "QyGroupModelRatio"},
		"modelPriceHelperTiered":  {"QyGroupTieredQuota"},
	}
	for fn, hooks := range want {
		require.Contains(t, calls, fn, "relay/helper/price.go 里找不到计价入口 %s —— 上游改了函数名?", fn)
		for _, h := range hooks {
			assert.Contains(t, calls[fn], h,
				"%s 里缺少 %s 挂载点:接缝一旦被摘掉,将来任何分组级计价都会静默地按全局价扣钱", fn, h)
		}
	}
}

// TestQyModelPriceHookRunsAfterGroupResolution:顺序锁。
//
// ModelPriceHelper 的上游原始顺序是「先 GetModelPrice、后 HandleGroupRatio」,
// 而 HandleGroupRatio 才是把 auto 分组解析进 info.UsingGroup 的那一步。
// 把 QyGroupModelPrice 插在 GetModelPrice 紧后面(最自然的位置)会读到旧的
// UsingGroup —— 价格取一个分组、倍率取另一个分组,相乘出来的数字不对应任何真实定价。
func TestQyModelPriceHookRunsAfterGroupResolution(t *testing.T) {
	seq := qyCallsByFunc(t, qyPriceGoPath)["ModelPriceHelper"]
	groupIdx := qyIndexOf(seq, "HandleGroupRatio")
	hookIdx := qyIndexOf(seq, "QyGroupModelPrice")

	require.GreaterOrEqual(t, groupIdx, 0, "ModelPriceHelper 里找不到 HandleGroupRatio")
	require.GreaterOrEqual(t, hookIdx, 0, "ModelPriceHelper 里找不到 QyGroupModelPrice")
	assert.Greater(t, hookIdx, groupIdx,
		"QyGroupModelPrice 必须排在 HandleGroupRatio 之后:auto 分组要在那一步才被解析进 "+
			"info.UsingGroup,插在前面会按用户原分组取价、按实际分组取倍率")
}

// TestQyTieredHookRunsBeforeGroupRatioMultiplication:阶梯路径的顺序锁。
//
// 分组乘数必须乘在"乘分组倍率之前"的 quota 上,才能得到
// 表达式结果 × 乘数 × 分组倍率 这个与另外两条路径一致的相乘形状。
func TestQyTieredHookRunsBeforeGroupRatioMultiplication(t *testing.T) {
	seq := qyCallsByFunc(t, qyPriceGoPath)["modelPriceHelperTiered"]
	hookIdx := qyIndexOf(seq, "QyGroupTieredQuota")
	roundIdx := qyIndexOf(seq, "QuotaRoundStrict")

	require.GreaterOrEqual(t, hookIdx, 0)
	require.GreaterOrEqual(t, roundIdx, 0, "modelPriceHelperTiered 里找不到额度换算调用")
	assert.Less(t, hookIdx, roundIdx,
		"分组乘数必须在乘分组倍率、换算成额度之前应用")
}

// TestQyEveryUpstreamPriceSourceHasAHook:数量锁。
//
// 上游将来在这三个入口里再加一处 GetModelPrice / GetModelRatio(例如新增一种
// 回退价格来源),这条立刻失败,逼人回来判断新的取值口要不要也走接缝 ——
// 而不是让它悄悄成为一条漏网路径。
func TestQyEveryUpstreamPriceSourceHasAHook(t *testing.T) {
	calls := qyCallsByFunc(t, qyPriceGoPath)
	total := map[string]int{}
	for _, fn := range qyPricingEntries {
		for _, c := range calls[fn] {
			total[c]++
		}
	}
	assert.Equal(t, total["GetModelPrice"], total["QyGroupModelPrice"],
		"三个计价入口里 GetModelPrice 的调用点有 %d 处,QyGroupModelPrice 只挂了 %d 处",
		total["GetModelPrice"], total["QyGroupModelPrice"])
	assert.Equal(t, total["GetModelRatio"], total["QyGroupModelRatio"],
		"三个计价入口里 GetModelRatio 的调用点有 %d 处,QyGroupModelRatio 只挂了 %d 处",
		total["GetModelRatio"], total["QyGroupModelRatio"])
}

// TestQyPricingEntriesUseOnlyKnownRatioSources:取值口集合锁。
//
// 这三个入口当前只从下面这些 ratio_setting 取值口拿价格。集合一旦变化就说明
// 上游引入了新的价格来源,必须有人判断它要不要也走接缝。失败时不要直接改这张表,
// 先想清楚新来源该怎么处理。
//
// GetCompletionRatio 等辅助倍率刻意留在表里但不挂 hook:覆盖它会破坏
// "实际扣费对覆盖值线性"这一性质。
func TestQyPricingEntriesUseOnlyKnownRatioSources(t *testing.T) {
	known := []string{
		"GetAudioCompletionRatio",
		"GetAudioRatio",
		"GetCacheRatio",
		"GetCompletionRatio",
		"GetCreateCacheRatio",
		"GetDefaultModelPriceMap",
		"GetGroupGroupRatio",
		"GetGroupRatio",
		"GetImageRatio",
		"GetModelPrice",
		"GetModelRatio",
	}

	file := qyParseFileOrFail(t, qyPriceGoPath)
	seen := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		// HandleGroupRatio 是被三个入口共用的分组倍率解析,一并纳入。
		if !ok || fn.Body == nil || (!qyContains(qyPricingEntries, fn.Name.Name) && fn.Name.Name != "HandleGroupRatio") {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "ratio_setting" {
				seen[sel.Sel.Name] = true
			}
			return true
		})
	}

	got := make([]string, 0, len(seen))
	for k := range seen {
		got = append(got, k)
	}
	sort.Strings(got)
	assert.Equal(t, known, got,
		"上游计价入口用到的 ratio_setting 取值口集合变了。新增的那一个是不是也该走接缝?想清楚再改这张表")
}

// TestQyTieredSnapshotStoresUnmultipliedQuota 锁住快照存的是**未乘分组乘数**的额度。
//
// refreshTieredBillingGroup 之所以能"按当前分组重算乘数",前提是快照里
// EstimatedQuotaBeforeGroup 存的是原始表达式结果。若 price.go 把
// QyGroupTieredQuota 的返回值写回同一个变量再存进快照,两处就会各乘一次,
// 同一个分组下预留额被平方级放大。
//
// 这条断言的是源码形状而不是数值:数值侧由 qy_tiered_group_switch_test.go
// 端到端覆盖,但那份测试看不出"哪个变量被存进了快照",而这正是缺陷的落点。
func TestQyTieredSnapshotStoresUnmultipliedQuota(t *testing.T) {
	file := qyParseFileOrFail(t, qyPriceGoPath)

	var hookAssignedTo []string
	var snapshotField string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "modelPriceHelperTiered" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			// 收集 `x := QyGroupTieredQuota(...)` / `x = QyGroupTieredQuota(...)` 的左值
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
				if call, ok := as.Rhs[0].(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "QyGroupTieredQuota" {
						for _, lhs := range as.Lhs {
							if lid, ok := lhs.(*ast.Ident); ok {
								hookAssignedTo = append(hookAssignedTo, lid.Name)
							}
						}
					}
				}
			}
			// 找 BillingSnapshot 字面量里 EstimatedQuotaBeforeGroup 的取值变量
			if kv, ok := n.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "EstimatedQuotaBeforeGroup" {
					if v, ok := kv.Value.(*ast.Ident); ok {
						snapshotField = v.Name
					}
				}
			}
			return true
		})
	}

	require.NotEmpty(t, snapshotField,
		"modelPriceHelperTiered 里找不到 BillingSnapshot.EstimatedQuotaBeforeGroup 的赋值 —— 快照结构变了?")
	require.NotEmpty(t, hookAssignedTo,
		"modelPriceHelperTiered 里 QyGroupTieredQuota 的返回值没有被赋给任何变量 —— "+
			"若它被内联进了别的表达式,请手工确认它没有流进快照")
	assert.NotContains(t, hookAssignedTo, snapshotField,
		"EstimatedQuotaBeforeGroup 存的是 %q,而它正是 QyGroupTieredQuota 的返回值。"+
			"快照必须存未乘分组乘数的原始额度,否则 refreshTieredBillingGroup 会再乘一次",
		snapshotField)
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

// qyCallsByFunc 返回每个顶层函数体内按源码顺序出现的被调用函数名。
//
// 只取名字(选择器取 Sel):三个 hook 是同包变量、GetModelRatio 是跨包调用,
// 统一成名字之后一张表就能同时断言两类调用点。
func qyCallsByFunc(t *testing.T, path string) map[string][]string {
	t.Helper()
	file := qyParseFileOrFail(t, path)

	out := map[string][]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var seq []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				seq = append(seq, f.Name)
			case *ast.SelectorExpr:
				seq = append(seq, f.Sel.Name)
			}
			return true
		})
		out[fn.Name.Name] = seq
	}
	return out
}

func qyParseFileOrFail(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err, "应当可解析: %s", path)
	return file
}

func qyIndexOf(seq []string, name string) int {
	for i, s := range seq {
		if s == name {
			return i
		}
	}
	return -1
}

func qyContains(list []string, v string) bool {
	return qyIndexOf(list, v) >= 0
}
