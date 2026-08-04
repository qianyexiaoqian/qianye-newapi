package grouppricing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookpoint_test.go —— 用 AST 锁死上游 relay/helper/price.go 的挂载点。
//
// # 为什么必须是 AST 断言
//
// 本扩展被审计出的缺陷全都**不在算术里,而在调度层与配置消费层**:纯函数写对了、
// 单元测试全绿,唯独没有人真的去调它。这次改动的形状完全一样 —— 三个 hook 的
// 实现可以做得完美无缺,只要 price.go 里少插一行,那条计价路径就静默地按全局价
// 扣钱,而运营从管理界面上完全看不出来(规则列表照常显示,影子差额少一块)。
//
// 单元测试抓不到这个:它们直接调 applyModelRatio,根本不经过 price.go。
// 只有把"调用点确实存在"本身变成断言,回滚才会让测试变红。
//
// # 这里锁了四件事
//
//  1. 五个插入点一个都不少,各自在正确的函数里
//  2. QyGroupModelPrice 在 ModelPriceHelper 里必须排在 HandleGroupRatio **之后**
//     —— 上游原本先取价、后解析 auto 分组,顺序是反的,插错位置会读到未改写的
//     UsingGroup,分组价与分组倍率来自两个不同的分组
//  3. 每一个 ratio_setting.GetModelPrice / GetModelRatio 调用点都配了一个 hook,
//     数量必须相等 —— 上游将来新增一个取值口时这条会失败,逼人回来做决定
//  4. 三个计价入口用到的 ratio_setting 取值口集合没有变 —— 上游新增一个
//     价格来源(哪怕是新的倍率种类)时这条会失败

const priceGoPath = "../../../relay/helper/price.go"

// taskBillingGoPath 是第四个挂载点所在的文件。它不在 relay 计价链路上,
// 所以 priceGoPath 那份 AST 够不到它 —— 复核实测:把那一行调用整行删掉,
// 全仓测试全绿。
const taskBillingGoPath = "../../../service/task_billing.go"

// tieredSettleGoPath 是第五个挂载点所在的文件:阶梯计价的结算侧。
// 它从表达式重跑、不读 PriceData,所以计价链路那三个挂载点够不到它。
const tieredSettleGoPath = "../../../service/tiered_settle.go"

// pricingEntries 是上游的三个计价入口。它们的产物(PriceData)被结算侧直接使用,
// 所以覆盖这三个函数即覆盖全部扣费。
var pricingEntries = []string{"ModelPriceHelper", "ModelPriceHelperPerCall", "modelPriceHelperTiered"}

// TestHookPointsExistInEveryPricingEntry:五个插入点一个都不能少。
func TestHookPointsExistInEveryPricingEntry(t *testing.T) {
	calls := callsByFunc(t, priceGoPath)

	want := map[string][]string{
		"ModelPriceHelper":        {"QyGroupModelPrice", "QyGroupModelRatio"},
		"ModelPriceHelperPerCall": {"QyGroupModelPrice", "QyGroupModelRatio"},
		"modelPriceHelperTiered":  {"QyGroupTieredQuota"},
	}
	for fn, hooks := range want {
		require.Contains(t, calls, fn, "relay/helper/price.go 里找不到计价入口 %s —— 上游改了函数名?", fn)
		for _, h := range hooks {
			assert.Contains(t, calls[fn], h,
				"%s 里缺少 %s 挂载点:这条计价路径会静默地按全局价扣钱,而管理界面上看不出来", fn, h)
		}
	}
}

// TestModelPriceHookRunsAfterGroupResolution:顺序锁。
//
// ModelPriceHelper 的上游原始顺序是「先 GetModelPrice、后 HandleGroupRatio」,
// 而 HandleGroupRatio 才是把 auto 分组解析进 info.UsingGroup 的那一步。
// 把 QyGroupModelPrice 插在 GetModelPrice 紧后面(最自然的位置)会读到旧的
// UsingGroup —— 价格取一个分组、倍率取另一个分组,相乘出来的数字不对应任何真实定价。
func TestModelPriceHookRunsAfterGroupResolution(t *testing.T) {
	seq := callsByFunc(t, priceGoPath)["ModelPriceHelper"]
	groupIdx := indexOf(seq, "HandleGroupRatio")
	hookIdx := indexOf(seq, "QyGroupModelPrice")

	require.GreaterOrEqual(t, groupIdx, 0, "ModelPriceHelper 里找不到 HandleGroupRatio")
	require.GreaterOrEqual(t, hookIdx, 0, "ModelPriceHelper 里找不到 QyGroupModelPrice")
	assert.Greater(t, hookIdx, groupIdx,
		"QyGroupModelPrice 必须排在 HandleGroupRatio 之后:auto 分组要在那一步才被解析进 "+
			"info.UsingGroup,插在前面会按用户原分组取价、按实际分组取倍率")
}

// TestTieredHookRunsBeforeGroupRatioMultiplication:阶梯路径的顺序锁。
//
// 分组乘数必须乘在"乘分组倍率之前"的 quota 上,才能得到
// 表达式结果 × 乘数 × 分组倍率 这个与另外两条路径一致的相乘形状。
func TestTieredHookRunsBeforeGroupRatioMultiplication(t *testing.T) {
	seq := callsByFunc(t, priceGoPath)["modelPriceHelperTiered"]
	hookIdx := indexOf(seq, "QyGroupTieredQuota")
	roundIdx := indexOf(seq, "QuotaRoundStrict")

	require.GreaterOrEqual(t, hookIdx, 0)
	require.GreaterOrEqual(t, roundIdx, 0, "modelPriceHelperTiered 里找不到额度换算调用")
	assert.Less(t, hookIdx, roundIdx,
		"分组乘数必须在乘分组倍率、换算成额度之前应用")
}

// TestEveryUpstreamPriceSourceHasAHook:数量锁。
//
// 这是"一个都不能漏"的机械保证。上游将来在这三个入口里再加一处
// GetModelPrice / GetModelRatio(例如新增一种回退价格来源),这条立刻失败,
// 逼人回来判断新的取值口要不要也走分组价 —— 而不是让它悄悄成为一条漏网路径。
func TestEveryUpstreamPriceSourceHasAHook(t *testing.T) {
	calls := callsByFunc(t, priceGoPath)
	total := map[string]int{}
	for _, fn := range pricingEntries {
		for _, c := range calls[fn] {
			total[c]++
		}
	}
	assert.Equal(t, total["GetModelPrice"], total["QyGroupModelPrice"],
		"三个计价入口里 GetModelPrice 的调用点有 %d 处,QyGroupModelPrice 只挂了 %d 处 —— "+
			"没挂上的那一处会按全局价扣钱", total["GetModelPrice"], total["QyGroupModelPrice"])
	assert.Equal(t, total["GetModelRatio"], total["QyGroupModelRatio"],
		"三个计价入口里 GetModelRatio 的调用点有 %d 处,QyGroupModelRatio 只挂了 %d 处",
		total["GetModelRatio"], total["QyGroupModelRatio"])
}

// TestPricingEntriesUseOnlyKnownRatioSources:取值口集合锁。
//
// 这三个入口当前只从下面这些 ratio_setting 取值口拿价格。集合一旦变化就说明
// 上游引入了新的价格来源,必须有人判断它要不要走分组价 —— 本扩展已经四次栽在
// "没有人去判断"上。失败时不要直接改这张表,先想清楚新来源该怎么处理。
//
// GetCompletionRatio 等辅助倍率刻意留在表里但不挂 hook,理由见包注释
// (覆盖它会破坏影子差额的线性折算)。
func TestPricingEntriesUseOnlyKnownRatioSources(t *testing.T) {
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

	file := parseFileOrFail(t, priceGoPath)
	seen := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		// HandleGroupRatio 是被三个入口共用的分组倍率解析,一并纳入。
		if !ok || fn.Body == nil || (!contains(pricingEntries, fn.Name.Name) && fn.Name.Name != "HandleGroupRatio") {
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
		"上游计价入口用到的 ratio_setting 取值口集合变了。新增的那一个是不是也该走分组价?"+
			"想清楚再改这张表 —— 本扩展已经四次栽在「没有人去判断」上")
}

// TestTaskSettlementHookPointExists:第四个挂载点的调用点锁。
//
// RecalculateTaskQuotaByTokens 是 Task 类模型(视频、MJ)拿到实际 token 数之后的
// 差额重算。它直接读 ratio_setting、不经过 PriceData,所以 relay/helper 的三个
// 挂载点全都够不到;少了这一行,预扣按分组折扣价、结算按全局倍率,差额以**追扣**
// 形式落到用户头上 —— 正是 AGENTS.md「预扣与结算必须同口径」直指的情形。
//
// 顺序也一并锁住:分组要先从 task.Group 解析(为空时回落 users.group)才有意义,
// 把 hook 提到那段之前会用空分组去查规则,永远查不到,等于这一行不存在。
func TestTaskSettlementHookPointExists(t *testing.T) {
	seq := callsByFunc(t, taskBillingGoPath)["RecalculateTaskQuotaByTokens"]
	require.NotEmpty(t, seq,
		"service/task_billing.go 里找不到 RecalculateTaskQuotaByTokens —— 上游改了函数名?")

	hookIdx := indexOf(seq, "QyGroupTaskRatio")
	require.GreaterOrEqual(t, hookIdx, 0,
		"RecalculateTaskQuotaByTokens 里缺少 QyGroupTaskRatio 挂载点:"+
			"任务类模型的分组折扣会在差额结算时被追扣回全局价,而管理界面上看不出来")

	fallbackIdx := indexOf(seq, "GetUserById")
	require.GreaterOrEqual(t, fallbackIdx, 0,
		"RecalculateTaskQuotaByTokens 里找不到分组回落(task.Group 为空时读 users.group)")
	assert.Greater(t, hookIdx, fallbackIdx,
		"QyGroupTaskRatio 必须排在分组解析之后:提前调用会拿空分组去查规则,永远查不到")
}

// TestTaskSettlementUsesCrossCellGroupRatio 钉死 Task 差额结算的**交叉格**形状。
//
// 修复前是 GetGroupGroupRatio(group, group):两个实参同一个标识符,只命中分组倍率
// 矩阵的对角线。而预扣走 relay/helper/price.go 的 HandleGroupRatio,用的是
// (UserGroup, UsingGroup) 交叉格。令牌做了分组覆盖且配了交叉倍率时,Task 类模型
// (视频 / MJ)的预扣与结算不同口径,差额以**追扣**形式落到用户头上 ——
// 正是 AGENTS.md「预扣与结算必须同口径」直指的情形。
//
// 本轮已修:第一个实参改成所有者的 users.group。effective.go 里那条
// taskCrossCellWarning 随之删除 —— 留着一条不再成立的告警,和显示一个错误的
// 数字一样糟。这条断言防的是它被改回去。
func TestTaskSettlementUsesCrossCellGroupRatio(t *testing.T) {
	file := parseFileOrFail(t, taskBillingGoPath)

	args := make([]string, 0, 2)
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "RecalculateTaskQuotaByTokens" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "GetGroupGroupRatio" {
				return true
			}
			found = true
			for _, arg := range call.Args {
				if id, ok := arg.(*ast.Ident); ok {
					args = append(args, id.Name)
					continue
				}
				args = append(args, "<非标识符>")
			}
			return true
		})
	}

	require.True(t, found,
		"service/task_billing.go 的 RecalculateTaskQuotaByTokens 里找不到 GetGroupGroupRatio —— "+
			"Task 差额结算的倍率来源变了,请重新确认预扣与结算是否仍然同口径")
	require.Len(t, args, 2)
	assert.NotEqual(t, args[0], args[1],
		"GetGroupGroupRatio 的两个实参又是同一个标识符(%q)—— 对角线缺陷回归:"+
			"预扣按交叉格、结算按对角格,差额会以追扣落到用户头上", args[0])
	assert.Equal(t, "userGroup", args[0],
		"第一个实参必须是所有者的 users.group,与 HandleGroupRatio(relayInfo.UserGroup, ...) 同口径")
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

// callsByFunc 返回每个顶层函数体内按源码顺序出现的被调用函数名。
//
// 只取名字(选择器取 Sel):三个 hook 是同包变量、GetModelRatio 是跨包调用,
// 统一成名字之后一张表就能同时断言两类调用点。
func callsByFunc(t *testing.T, path string) map[string][]string {
	t.Helper()
	file := parseFileOrFail(t, path)

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

func parseFileOrFail(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
	require.NoError(t, err, "应当可解析: %s", path)
	return file
}

func indexOf(seq []string, name string) int {
	for i, s := range seq {
		if s == name {
			return i
		}
	}
	return -1
}

func contains(list []string, v string) bool {
	return indexOf(list, v) >= 0
}

// TestTieredSettleHookPointExists 锁住第五个挂载点:service/tiered_settle.go。
//
// 为什么单靠 pipeline_test 不够:那条断言的是"InstallHooks 给变量赋了值",
// 而这条断言的是"结算函数真的调了那个变量"。回滚验证实测过 ——
// 把 TryTieredSettle 里的调用换成恒等表达式(即缺陷原样),
// 全仓 go test 一条都不红,只有这条 AST 断言能抓到。
//
// 生产后果:阶梯计价的分组折扣在结算时丢失,预扣按折扣价、结算按原价,
// 差额以追扣落到用户头上。
func TestTieredSettleHookPointExists(t *testing.T) {
	seq := callsByFunc(t, tieredSettleGoPath)["TryTieredSettle"]
	require.NotEmpty(t, seq,
		"service/tiered_settle.go 里找不到 TryTieredSettle —— 上游改了函数名?")

	hookIdx := indexOf(seq, "QyGroupTieredSettle")
	require.GreaterOrEqual(t, hookIdx, 0,
		"TryTieredSettle 里缺少 QyGroupTieredSettle 挂载点:"+
			"阶梯计价的分组折扣会在结算时丢失,差额以追扣形式落到用户头上")

	computeIdx := indexOf(seq, "ComputeTieredQuotaWithRequest")
	require.GreaterOrEqual(t, computeIdx, 0,
		"TryTieredSettle 里找不到 ComputeTieredQuotaWithRequest —— 结算路径变了?")
	assert.Greater(t, hookIdx, computeIdx,
		"乘数必须作用在表达式重跑之后:提前调用拿到的不是 ActualQuotaBeforeGroup")

	roundIdx := indexOf(seq, "QuotaRoundChecked")
	require.GreaterOrEqual(t, roundIdx, 0,
		"乘数作用后必须按 billingexpr 同一公式重算 after-group,否则两侧舍入口径不同")
	assert.Greater(t, roundIdx, hookIdx,
		"重算必须排在乘数之后")
}

// TestTieredRetryReservationHookPointExists 锁住第五个挂载点的**第二个调用点**。
//
// refreshTieredBillingGroup 跑在 auto 重试切分组之后,拿
// snap.EstimatedQuotaBeforeGroup 乘新分组的倍率重算预留额。分组级乘数和分组倍率
// 一样是"当前分组"的属性,这里不重算就会把原分组的乘数带进新分组的预留额。
//
// 为什么单靠 TestTieredSettleHookPointExists 不够:那条锁的是 TryTieredSettle
// (最终扣费),这条锁的是预留额。两者是两个函数、两条后果 ——
// 最终扣费一直是对的,错的是预扣多了(冻结用户额度)或少了(误判余额不足)。
func TestTieredRetryReservationHookPointExists(t *testing.T) {
	seq := callsByFunc(t, tieredSettleGoPath)["refreshTieredBillingGroup"]
	require.NotEmpty(t, seq,
		"service/tiered_settle.go 里找不到 refreshTieredBillingGroup —— 上游改了函数名?")

	hookIdx := indexOf(seq, "QyGroupTieredSettle")
	require.GreaterOrEqual(t, hookIdx, 0,
		"refreshTieredBillingGroup 里缺少 QyGroupTieredSettle 挂载点:"+
			"auto 重试切分组后预留额仍按原分组的乘数算")

	roundIdx := indexOf(seq, "QuotaRoundStrict")
	require.GreaterOrEqual(t, roundIdx, 0,
		"refreshTieredBillingGroup 里找不到额度换算调用 —— 预留额换算路径变了?")
	assert.Greater(t, roundIdx, hookIdx,
		"乘数必须在乘分组倍率、换算成额度之前应用")
}

// TestTieredSnapshotStoresUnmultipliedQuota 锁住上面那条修复的配套前提。
//
// refreshTieredBillingGroup 之所以能"按当前分组重算乘数",前提是快照里
// EstimatedQuotaBeforeGroup 存的是**未乘乘数**的原始表达式结果。
// 若 price.go 把 QyGroupTieredQuota 的返回值写回同一个变量再存进快照
// (改动前的写法),两处就会各乘一次,同一个分组下预留额被平方级放大。
//
// 这条断言的是源码形状而不是数值:数值侧由
// relay/helper/qy_tiered_group_switch_test.go 端到端覆盖,
// 但那份测试看不出"哪个变量被存进了快照",而这正是缺陷的落点。
func TestTieredSnapshotStoresUnmultipliedQuota(t *testing.T) {
	file := parseFileOrFail(t, priceGoPath)

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
