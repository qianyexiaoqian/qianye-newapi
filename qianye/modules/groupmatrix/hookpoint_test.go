package groupmatrix

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookpoint_test.go —— 用 AST 锁死上游的三个挂载点与它们的**位置**。
//
// # 为什么必须是 AST 断言
//
// 本扩展被审计出的缺陷全都不在算术里,而在调度层:纯函数写对了、单元测试全绿,
// 唯独没有人真的去调它,或者调的位置不对。这次改动的形状完全一样 ——
//
//	service/group.go 里那一行挂**早一步**(在自我补入之前),
//	「用户分组可以不包含它自己」就永远做不到,而所有单元测试照常全绿:
//	Resolve 的返回值是对的,只是上游在它之后又把 userGroup 补了回去。
//
// 这是项目方点名要的能力,而它的失效**完全静默**。只有把"那一行在哪儿"
// 本身变成断言,回滚才会让测试变红。

const (
	groupGoPath      = "../../../service/group.go"
	tokenGoPath      = "../../../controller/token.go"
	taskBillingPath  = "../../../service/task_billing.go"
	exportGoPath     = "../../../service/qy_usablegroup_export.go"
	hookImplFilePath = "hook.go"
)

// TestResolveHookIsTheLastStatementOfGetUserUsableGroups 是本文件最重要的一条。
func TestResolveHookIsTheLastStatementOfGetUserUsableGroups(t *testing.T) {
	file := parseFileOrFail(t, groupGoPath)
	fn := findFunc(t, file, "GetUserUsableGroups")

	var returns []*ast.ReturnStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok {
			returns = append(returns, r)
		}
		return true
	})
	require.Len(t, returns, 1,
		"GetUserUsableGroups 里出现了第二个 return —— 提前返回的那条路径会绕过 hook,"+
			"表现是「某些用户分组的收紧莫名其妙不生效」")

	call, ok := returns[0].Results[0].(*ast.CallExpr)
	require.True(t, ok, "唯一的 return 必须返回 QyResolveUsableGroups(...) 的结果")
	ident, ok := call.Fun.(*ast.Ident)
	require.True(t, ok)
	require.Equal(t, "QyResolveUsableGroups", ident.Name)
	require.Len(t, call.Args, 2, "hook 必须同时拿到 userGroup 与上游算好的 map")

	// 位置锁:必须排在**自我补入**之后。
	//
	// 上游最后一步是 `if _, ok := groupsCopy[userGroup]; !ok { groupsCopy[userGroup] = ... }`。
	// 挂在它之前,上游会在 hook 之后把 userGroup 补回去 —— 「一个用户分组可以不
	// 包含它自己」就永远做不到,而所有单元测试照常全绿。本站那条 `-:自己` 的规则
	// 至今无效正是同一个原因(那套 +:/-: 差分本身已整体下线,见
	// setting/ratio_setting/group_ratio.go;自我补入仍在,只是收窄成
	// 「名字必须是一个配了倍率的模型分组」,所以这条位置锁一个字都不能松)。
	selfInsertEnd := token.NoPos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		s, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		// 上游写的是 `if _, ok := groupsCopy[userGroup]; !ok { ... }`,
		// 判据在 Init 里而不是 Cond 里(Cond 只是 `!ok`)。
		if init, ok := s.Init.(*ast.AssignStmt); ok && len(init.Rhs) == 1 &&
			strings.Contains(exprText(t, groupGoPath, init.Rhs[0]), "groupsCopy[userGroup]") {
			selfInsertEnd = s.End()
		}
		return true
	})
	require.NotEqual(t, token.NoPos, selfInsertEnd,
		"找不到上游的自我补入分支 —— 上游改写了 GetUserUsableGroups,请重新确认 hook 的位置")
	assert.Greater(t, returns[0].Pos(), selfInsertEnd,
		"hook 必须排在自我补入之后:挂在前面时上游会把 userGroup 补回去,"+
			"「用户分组可以不包含它自己」永远做不到,而所有单元测试照常全绿")
}

// TestDerivedHelpersStillFunnelThroughTheHook 守派生链。
//
// 8 个消费方全都经过 service.GetUserUsableGroups,所以挂一处即可覆盖全部。
// 但那是**上游当前的**实现细节:上游哪天把 IsUserSelectableGroup 改成直接读
// setting,一部分调用方就会绕过 hook,而收紧只在另一部分生效 ——
// 「界面能选、实际不能用」或者反过来。
func TestDerivedHelpersStillFunnelThroughTheHook(t *testing.T) {
	calls := callsByFunc(t, groupGoPath)
	assert.Contains(t, calls["GroupInUserUsableGroups"], "GetUserUsableGroups",
		"GroupInUserUsableGroups 必须经过 GetUserUsableGroups")
	assert.Contains(t, calls["IsUserSelectableGroup"], "GroupInUserUsableGroups",
		"IsUserSelectableGroup 必须经过 GroupInUserUsableGroups")
	assert.Contains(t, calls["GetUserAutoGroup"], "IsUserSelectableGroup",
		"GetUserAutoGroup 必须经过 IsUserSelectableGroup")
	assert.Contains(t, calls["FilterUserTokenAutoGroups"], "IsUserSelectableGroup",
		"FilterUserTokenAutoGroups 必须经过 IsUserSelectableGroup")

	for _, fn := range []string{"GroupInUserUsableGroups", "IsUserSelectableGroup",
		"GetUserAutoGroup", "FilterUserTokenAutoGroups"} {
		assert.NotContains(t, calls[fn], "GetUserUsableGroupsCopy",
			"%s 直接读了 setting 的全局白名单 —— 它会绕过权威清单,收紧只在一半路径上生效", fn)
	}
}

// TestTokenWriteGuardsAreAtTheRightPositions 锁住写入侧的两个插入点。
//
// UpdateToken 那一处必须落在 `if statusOnly != ""` 的 **else 分支内**:
// 那个分支天然跳过全部字段赋值,所以"用户禁用一个孤儿令牌"永远不会被挡。
// 这不是靠实现体判断出来的,是靠**位置**保证的 —— 比任何参数传递都可靠。
// 上游哪天把它挪出 else 分支,这条立刻红,否则用户会被挡得连禁用都做不到。
func TestTokenWriteGuardsAreAtTheRightPositions(t *testing.T) {
	file := parseFileOrFail(t, tokenGoPath)

	addSeq := callsByFunc(t, tokenGoPath)["AddToken"]
	hookIdx := indexOf(addSeq, "QyCheckTokenGroupChange")
	require.GreaterOrEqual(t, hookIdx, 0,
		"AddToken 里缺少 QyCheckTokenGroupChange —— 新建令牌时不校验分组,孤儿会继续增加")

	upd := findFunc(t, file, "UpdateToken")
	var elseBlock *ast.BlockStmt
	ast.Inspect(upd.Body, func(n ast.Node) bool {
		s, ok := n.(*ast.IfStmt)
		if !ok || s.Else == nil {
			return true
		}
		if !strings.Contains(exprText(t, tokenGoPath, s.Cond), "statusOnly") {
			return true
		}
		if b, ok := s.Else.(*ast.BlockStmt); ok {
			elseBlock = b
		}
		return true
	})
	require.NotNil(t, elseBlock,
		"UpdateToken 里找不到 `if statusOnly != \"\"` 的 else 分支 —— 上游重构了这个函数,"+
			"请重新确认写入侧校验的位置(挪出 else 会让用户连禁用孤儿令牌都做不到)")

	hookPos, assignPos := token.NoPos, token.NoPos
	ast.Inspect(elseBlock, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.CallExpr:
			if sel, ok := s.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "QyCheckTokenGroupChange" {
				hookPos = s.Pos()
			}
		case *ast.AssignStmt:
			if len(s.Lhs) == 1 && strings.Contains(exprText(t, tokenGoPath, s.Lhs[0]), "cleanToken.Group") {
				assignPos = s.Pos()
			}
		}
		return true
	})
	require.NotEqual(t, token.NoPos, hookPos,
		"UpdateToken 的 else 分支里缺少 QyCheckTokenGroupChange")
	require.NotEqual(t, token.NoPos, assignPos,
		"UpdateToken 的 else 分支里找不到 cleanToken.Group 的赋值")
	assert.Less(t, hookPos, assignPos,
		"校验必须排在 cleanToken.Group 赋值之前,否则旧值已经被覆盖,拿不到 oldGroup —— "+
			"「分组没变就放行」这条规则会当场失效,用户改个名字都会被挡")
}

// TestExportedHookFileDoesNotDependOnTheExtension 守上游导出文件的依赖方向。
//
// service 是被扩展依赖的下层包,反向依赖会成环 —— 而成环的表现是编译失败,
// 那还算好的;更糟的是有人为了绕开环把实现直接写进上游文件,
// 于是"扩展删掉后行为逐位一致"这条承诺当场作废。
func TestExportedHookFileDoesNotDependOnTheExtension(t *testing.T) {
	file := parseFileOrFail(t, exportGoPath)
	for _, imp := range file.Imports {
		assert.NotContains(t, imp.Path.Value, "qianye",
			"service/qy_usablegroup_export.go 不得 import 任何 qianye 包(反向依赖成环)")
	}
}

// TestHookImplHasNoDatabaseImports 守热路径的零 I/O 约束。
//
// Resolve 挂在 middleware/auth.go 的令牌分组校验上,每个带令牌分组的 relay 请求
// 调用一次。实现体里任何一次查库/取锁/超时等待就是全站延迟,
// 而它不会以错误的形式表现出来 —— 只会表现为"网关变慢了"。
func TestHookImplHasNoDatabaseImports(t *testing.T) {
	file := parseFileOrFail(t, hookImplFilePath)
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, banned := range []string{"gorm.io", "qianye/db", "database/sql"} {
			assert.NotContains(t, path, banned,
				"hook.go 不得 import %q:Resolve 跑在 relay 热路径上,只允许 atomic load + map 查找", banned)
		}
	}
}

// qyGroupRatioHotPathAllowed 是 hook.go 允许调用的 qianye/groupratio 函数白名单。
//
// 只有一个:NoteMissingGroup(纯内存登记 + 一次进程内互斥锁,且只在 miss 分支发生)。
var qyGroupRatioHotPathAllowed = map[string]bool{"NoteMissingGroup": true}

// TestHookImplOnlyCallsMemoryOnlyGroupRatioHelpers 补上 import 白名单抓不到的那一半。
//
// 上面那条只看**直接** import 的包名,而 qianye/groupratio 不在 banned 列表里 ——
// 它是一个内存登记簿,所以 hook.go 引它是对的。问题在于同一个包里还住着 Scan(),
// 那是一条对 users 与 tokens 做全表 GROUP BY 的查询。下一个人为了"顺手在热路径上
// 报一下孤儿数"在 Resolve 里加一行 groupratio.Scan(ctx, false),import 守卫全绿,
// 而那条全表聚合会跑在每一个带令牌分组的 relay 请求上。
//
// 守卫存在的全部意义就是拦住这一类改动,所以这里按**函数名白名单**再守一次。
func TestHookImplOnlyCallsMemoryOnlyGroupRatioHelpers(t *testing.T) {
	file := parseFileOrFail(t, hookImplFilePath)
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "groupratio" {
			return true
		}
		assert.Truef(t, qyGroupRatioHotPathAllowed[sel.Sel.Name],
			"hook.go 调用了 groupratio.%s,而热路径只允许纯内存的 %v。\n"+
				"该包里同时住着 Scan() —— 一条对 users 与 tokens 的全表 GROUP BY;"+
				"把它放进 Resolve 等于让每一个带令牌分组的 relay 请求跑一次全表聚合",
			sel.Sel.Name, qyGroupRatioHotPathAllowed)
		return true
	})
}

// TestTaskBillingUsesCrossCellGroupRatio 钉死 Task 差额结算的**交叉格**形状。
//
// 原缺陷:那里写的是 GetGroupGroupRatio(group, group) —— 两个实参是同一个标识符,
// 只命中分组倍率矩阵的对角线;而预扣走 relay/helper/price.go 的 HandleGroupRatio,
// 用的是 (UserGroup, UsingGroup) 交叉格。于是令牌做了分组覆盖且配了交叉倍率时,
// Task 类模型(视频 / MJ)的预扣与结算不同口径,差额以**追扣**落到用户头上 ——
// 正是 AGENTS.md「预扣与结算必须同口径」直指的情形。
//
// 本轮三条计费路径合并成 ratio_setting.ResolveGroupRatio,断言随之改成它,
// 但守的东西一个字都没变:**第一个实参必须是所有者的用户分组,两者不能同名**。
//
// 第二个实参必须是 task.Group 那一列(变量名 group),而不是重新解析出来的默认
// 模型分组:task.Group 在提交时就落库了,它就是这条异步链路的 pin。
// 结算时重新解析等于让运营在提交与结算之间改一次配置就能改变这一笔的价。
func TestTaskBillingUsesCrossCellGroupRatio(t *testing.T) {
	file := parseFileOrFail(t, taskBillingPath)

	var args []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ResolveGroupRatio" || len(call.Args) != 2 {
			return true
		}
		a, aok := call.Args[0].(*ast.Ident)
		b, bok := call.Args[1].(*ast.Ident)
		if aok && bok {
			args = []string{a.Name, b.Name}
		}
		return true
	})
	require.Len(t, args, 2,
		"service/task_billing.go 里找不到 ResolveGroupRatio(标识符, 标识符) —— "+
			"Task 差额结算的倍率来源变了,请重新确认预扣与结算是否仍然同口径")
	assert.NotEqual(t, args[0], args[1],
		"ResolveGroupRatio 的两个实参又变成同一个标识符(%q)—— 对角线缺陷回归了:"+
			"预扣按 (用户分组, 模型分组) 交叉格,结算按对角格,差额会以追扣落到用户头上", args[0])
	assert.Equal(t, "userGroup", args[0],
		"第一个实参必须是**所有者的用户分组**(users.group),与 relay/helper/price.go "+
			"的 HandleGroupRatio(relayInfo.UserGroup, ...) 同口径")
	assert.Equal(t, "group", args[1],
		"第二个实参必须是 task.Group 那一列(变量 group)—— 结算永不重新解析默认模型分组,"+
			"否则提交与结算之间改一次配置就会改变这一笔的价")
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

// astFset 是本文件全部解析共用的 FileSet。
//
// 共用是必须的:exprText 要拿 ast.Expr 的位置去源码里取字节,而位置只在
// 产生它的那个 FileSet 里有意义。每次 NewFileSet 会让偏移量看起来正常、
// 取出来的却是另一段代码 —— 断言随之变成随机通过。
var (
	astFset = token.NewFileSet()
	astSrc  = map[string][]byte{}
)

func parseFileOrFail(t *testing.T, path string) *ast.File {
	t.Helper()
	p := filepath.FromSlash(path)
	src, ok := astSrc[p]
	if !ok {
		src = readFileOrFail(t, path)
		astSrc[p] = src
	}
	file, err := parser.ParseFile(astFset, p, src, 0)
	require.NoError(t, err, "应当可解析: %s", path)
	return file
}

func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(path))
	require.NoError(t, err, "应当可读取: %s", path)
	return b
}

func findFunc(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	require.FailNowf(t, "找不到函数", "%s —— 上游改了函数名?", name)
	return nil
}

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

func indexOf(seq []string, name string) int {
	for i, s := range seq {
		if s == name {
			return i
		}
	}
	return -1
}

// exprText 把表达式还原成源码文本。用文本比对而不是逐个 AST 节点匹配,
// 是因为上游随时可能把 `groupsCopy[userGroup]` 换个变量名 ——
// 那时应当让断言失败并逼人回来确认,而不是悄悄匹配不上。
func exprText(t *testing.T, path string, e ast.Expr) string {
	t.Helper()
	src := astSrc[filepath.FromSlash(path)]
	start := astFset.Position(e.Pos()).Offset
	end := astFset.Position(e.End()).Offset
	if start < 0 || end > len(src) || start >= end {
		return ""
	}
	return string(src[start:end])
}
