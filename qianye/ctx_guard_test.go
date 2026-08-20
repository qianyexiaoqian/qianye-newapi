package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctx_guard_test.go —— 用 AST 保证「ctx 必须一路透传到 GORM 调用」这条铁律有机器校验。
//
// # 为什么这条断言必须存在
//
// 这条约定在本仓库被写进注释至少 5 遍(commission/hook.go、commission/accrual.go、
// commission/settings.go、db/db.go、violation/ban.go),几十处非测试 WithContext 调用点
// 全部遵守 —— 唯独跨库资金主路径 twophase.Execute 和两份规则热刷新 reload 漏接,
// 而这三处恰恰是最需要它的。有约定、有先例、有 5 处注释,却零机器校验。
//
// 漏接的代价不是"这条语句慢一点":
//   - 语句级预算(hot_async_timeout_ms 3 秒 / cold_path_timeout_ms 3 秒)只对
//     WithContext(ctx) 的语句生效,漏接就只剩主库 innodb_lock_wait_timeout(50 秒)
//     与扩展库 DSN readTimeout(30 秒)兜底;
//   - 而"等一个空闲连接"这一段是在 ctx 上 select 的,context.Background() 下**无界**;
//   - 次生问题:db.WithOpProbe 只认 tx.Statement.Context,漏接的语句拿不到探针,
//     hotRunWithBudget 就只会在失败时 MarkFailure、成功时永不 MarkSuccess,
//     熔断的健康票被单向截断。
//
// # 这里锁了两件事
//
//  1. 参数锁:凡是签名里写了 `ctx context.Context` 的函数/闭包,函数体必须真的用到它。
//     这正是 M6(Execute 收 ctx 零引用)与 M7(HotAsync 闭包 `func(ctx ...) error {
//     return reload(false) }`)的原样形状,而 Go 编译器对未使用的**参数**一个字都不说。
//     确实不需要 ctx 的回调把参数写成 `_` —— 那是一次显式的、看得见的声明。
//  2. 句柄锁:在拿得到 ctx 的作用域里,由 db.Get() / model.DB 取到的句柄必须先
//     WithContext 再发起 GORM 终结调用,也不许在未 WithContext 前被交给别的函数。
//     第二半条是必须的:M6 的漏接恰恰是 `gdb := db.Get()` 之后原样传进
//     createOrLoadOrder —— 只看调用链上有没有 WithContext 是抓不到它的。
//
// 遍历复用 httpq_guard_test.go 的 forEachQianyeFile(含"扫到的文件太少"自检),
// 不再写第二份遍历器:那正是本仓库反复栽跟头的形状。它跳过 qianye/httpq(纯参数解析,
// 不碰数据库),这一点对这两条锁没有影响。

// gormTerminalCalls 是"真的会发一条语句给数据库"的那批方法。
// Model/Where/Select/Joins 这类链式构造器刻意不在表里:它们不执行,
// 把它们算进来只会让失败信息指向链条中段而不是终结点。
var gormTerminalCalls = map[string]bool{
	"Find": true, "First": true, "Take": true, "Last": true,
	"Create": true, "CreateInBatches": true, "Save": true,
	"Update": true, "Updates": true, "UpdateColumn": true, "UpdateColumns": true,
	"Delete": true, "Count": true, "Pluck": true, "Scan": true,
	"Row": true, "Rows": true, "Exec": true, "Transaction": true,
	"FirstOrCreate": true, "FirstOrInit": true,
}

// ctxDebtWhitelist 列出**已知欠账**的位置,键是 "相对路径:函数名"(不含行号 ——
// 行号会随着任何一次无关编辑失效,那样的白名单只会在下一次改动时变成噪音)。
//
// 这些不是误报,是同一条铁律在别处的同形漏接:ctx 只被用来做 `ctx.Err()` 的循环
// 检查,真正发给数据库的语句一条都没接上。本轮只收掉资金主路径(twophase)与两份
// 规则热刷新(violation / grouppricing 的 reload),其余按模块归属分批处理。
//
// **这张表只允许变短。** 新增条目等于承认又写了一处同形缺陷。
//
// 加白名单之前先问一句:这一步是不是"主库已定局之后的收尾写"?如果是,正确做法是
// context.WithoutCancel(ctx) + 独立预算(见 twophase.settleContext),而不是不接 ctx ——
// 不接 ctx 换来的不是"不会被取消",是"没有任何上界"。
var ctxDebtWhitelist = map[string]string{
	// —— lease 定时任务:ctx 只做 `ctx.Err()` 的循环检查,句柄原样传给子函数后裸发语句 ——
	"modules/availability/flush.go:runRollup": "gdb 传进 rollupStart/rollupHour 后裸发 SELECT/INSERT;归属 availability",
	"modules/availability/flush.go:runCleanup": "gdb 传进 deleteBefore 后裸发分批 DELETE(它只检查 ctx.Err());" +
		"归属 availability",
	"modules/commission/settle.go:repairStrandedAccruals": "自愈 UPDATE 未接 ctx;归属 commission",
	"modules/commission/topup_scan.go:runTopupScan": "model.DB 扫 top_ups 未接 ctx,而它的 ctx 来自 " +
		"lease.go 的 context.WithCancel(Background),本来就没有 deadline;归属 commission",
	"modules/transfer/reconcile.go:reconcile": "gdb 传进 syncStuckOrders/pruneLookupLogs 后裸发语句;归属 transfer",
	"modules/violation/guard.go:persist": "gdb 传进 maybeAutoBan → markBan 后裸发收尾 UPDATE。" +
		"这一处正是应当改成 context.WithoutCancel + 独立预算的形状;归属 violation(本轮只改 rules.go)",
	"modules/violation/tasks.go:runBanCompensate": "封禁补偿的三条语句未接 ctx;归属 violation",
	// withdraw 的自动到账链路(credit.go 的补偿收尾、reconcile.go 的 resumeApproved)
	// 已随「提现只做佣金扣除、由管理员手动发放」整条删除,两条豁免一并去掉。
	// 剩下的 reconcile 仍是把 gdb 传进 pruneExpiredPii 后裸发语句的形状。
	"modules/withdraw/reconcile.go:reconcile": "gdb 传进 pruneExpiredPii 后裸发语句;归属 withdraw",
}

// ctxParamWhitelist 列出"确实不需要 ctx"的回调。
//
// 正确写法是把参数直接写成 `_`(一次显式的、看得见的声明),这里登记的是
// 归属其他模块、本轮不动的文件。
var ctxParamWhitelist = map[string]string{
	"modules/availability/sample.go:onRelaySample": "HotAsync(\"availability.sample\") 是纯内存作业" +
		"(guard.syncSafeJobs 里登记的两条之一),确实用不到 ctx;正确写法是把参数改成 `_`,归属 availability",
}

// 参数锁:签名里写了名字的 ctx 必须真的被用到。
func TestNamedContextParamIsActuallyUsed(t *testing.T) {
	var offenders []string
	forEachQianyeFile(t, func(file *ast.File, fset *token.FileSet) {
		eachFuncDecl(file, func(fn *ast.FuncDecl) {
			key := funcKey(fset, fn)
			for _, bad := range unusedContextParams(fn) {
				if _, ok := ctxParamWhitelist[key]; ok {
					continue
				}
				offenders = append(offenders, at(fset, bad.pos)+" ("+key+"): "+bad.what)
			}
		})
	})

	assert.Empty(t, offenders,
		"以下函数/闭包收下了 ctx 却一个字节都没用。语句级预算只对 WithContext(ctx) 的语句生效,"+
			"漏接就只剩 innodb_lock_wait_timeout(50 秒)与 DSN readTimeout(30 秒)兜底,"+
			"而连接池等待在 Background 下无界。确实不需要 ctx 的回调请把参数写成 `_`: %v", offenders)
}

// 句柄锁:ctx 作用域里的 db.Get() / model.DB 必须先 WithContext。
func TestExtDBHandleIsBoundToContextWhereAvailable(t *testing.T) {
	var offenders []string
	forEachQianyeFile(t, func(file *ast.File, fset *token.FileSet) {
		eachFuncDecl(file, func(fn *ast.FuncDecl) {
			key := funcKey(fset, fn)
			if _, ok := ctxDebtWhitelist[key]; ok {
				return
			}
			for _, bad := range unboundHandleUses(fn) {
				offenders = append(offenders, at(fset, bad.pos)+" ("+key+"): "+bad.what)
			}
		})
	})

	assert.Empty(t, offenders,
		"以下位置在拿得到 ctx 的作用域里用了没接 ctx 的 GORM 句柄。"+
			"主库已定局之后的收尾写不是例外 —— 那种步骤要用 context.WithoutCancel(ctx) + "+
			"独立预算(见 twophase.settleContext),而不是不接 ctx: %v", offenders)
}

// 白名单里的每一条都必须指向一个真实存在的函数。
//
// 这是第二道自检:键写错(路径拼错、函数改名后忘了改这里)会让那条豁免静默失配,
// 而失配的方向恰好是"看起来还在管着,其实什么都没管"。顺带也挡住"函数已经修好了
// 却把豁免留在表里"—— 那会让这张只允许变短的表悄悄变成永久免死金牌。
func TestCtxGuardWhitelistEntriesStillExist(t *testing.T) {
	existing := map[string]bool{}
	forEachQianyeFile(t, func(file *ast.File, fset *token.FileSet) {
		eachFuncDecl(file, func(fn *ast.FuncDecl) { existing[funcKey(fset, fn)] = true })
	})

	for _, table := range []map[string]string{ctxDebtWhitelist, ctxParamWhitelist} {
		for key := range table {
			assert.True(t, existing[key],
				"白名单条目 %q 找不到对应函数:要么键写错了,要么函数已经改名/删除 —— "+
					"两种情况下这条豁免都已经失效,请删掉它或改对", key)
		}
	}
}

func eachFuncDecl(file *ast.File, visit func(*ast.FuncDecl)) {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			visit(fn)
		}
	}
}

// funcKey 是白名单的键:相对路径 + 函数名。刻意不含行号 —— 行号会被任何一次
// 无关编辑冲掉,而白名单一旦静默失配,这两条锁就退化成永真断言。
func funcKey(fset *token.FileSet, fn *ast.FuncDecl) string {
	return filepath.ToSlash(fset.Position(fn.Pos()).Filename) + ":" + fn.Name.Name
}

// ─────────────────────────────── 分析器 ───────────────────────────────

type finding struct {
	pos  token.Pos
	what string
}

// unusedContextParams 找出所有"收下具名 ctx 却没用"的函数与闭包。
func unusedContextParams(root ast.Node) []finding {
	var out []finding
	ast.Inspect(root, func(n ast.Node) bool {
		var params *ast.FieldList
		var body *ast.BlockStmt
		what := "闭包"
		switch v := n.(type) {
		case *ast.FuncDecl:
			if v.Body == nil {
				return true
			}
			params, body, what = v.Type.Params, v.Body, "func "+v.Name.Name
		case *ast.FuncLit:
			params, body = v.Type.Params, v.Body
		default:
			return true
		}
		for _, name := range contextParamNames(params) {
			if identUsedIn(body, name) {
				continue
			}
			out = append(out, finding{n.Pos(), what + "(" + name + " context.Context) 未使用 ctx"})
		}
		return true
	})
	return out
}

// contextParamNames 返回参数表里所有具名的 context.Context 参数名(跳过 `_`)。
func contextParamNames(params *ast.FieldList) []string {
	if params == nil {
		return nil
	}
	var out []string
	for _, f := range params.List {
		sel, ok := f.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Context" {
			continue
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "context" {
			continue
		}
		for _, name := range f.Names {
			if name.Name != "_" {
				out = append(out, name.Name)
			}
		}
	}
	return out
}

func identUsedIn(body ast.Node, name string) bool {
	used := false
	ast.Inspect(body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			used = true
		}
		return !used
	})
	return used
}

// unboundHandleUses 找出 fn 里"在 ctx 作用域内、未经 WithContext"的句柄用法。
func unboundHandleUses(fn *ast.FuncDecl) []finding {
	// ctx 可能来自函数签名,也可能来自闭包参数
	//(guard.HotAsync / lease.Run 的回调就是后者)。
	ctxScopes := map[ast.Node]bool{}
	if len(contextParamNames(fn.Type.Params)) > 0 {
		ctxScopes[fn.Body] = true
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok && len(contextParamNames(lit.Type.Params)) > 0 {
			ctxScopes[lit.Body] = true
		}
		return true
	})
	if len(ctxScopes) == 0 {
		return nil
	}

	// handles 记录由 db.Get() 绑定出来的句柄名;rebound 记录哪些被
	// `x = x.WithContext(...)` 重新绑定过 —— 那之后它整段都带着 ctx。
	handles, rebound := map[string]bool{}, map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		lhs, ok := as.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		kind, chain := classifyChain(as.Rhs[0], handles)
		if kind == "" {
			return true
		}
		if containsWithContext(chain) {
			rebound[lhs.Name] = true
			return true
		}
		// 只把"光秃秃的 db.Get()"记成句柄。`res := db.Get()....Updates(...)` 这种
		// 已经发过语句的返回值不算 —— 它本身会被终结调用那一条规则报出来,
		// 再按句柄报一遍只会让同一处缺陷刷出四行噪音,把真正的位置埋掉。
		if kind == "db.Get()" && len(chain) == 0 {
			handles[lhs.Name] = true
		}
		return true
	})

	var out []finding
	walkWithParent(fn.Body, nil, ctxScopes[fn.Body], ctxScopes, func(n, parent ast.Node) {
		switch v := n.(type) {
		case *ast.CallExpr:
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok || !gormTerminalCalls[sel.Sel.Name] {
				return
			}
			kind, chain := classifyChain(v, nil)
			if kind == "" || containsWithContext(chain) {
				return
			}
			out = append(out, finding{v.Pos(), kind + " ... " + sel.Sel.Name + "() 未 WithContext"})
		case *ast.Ident:
			if !handles[v.Name] || rebound[v.Name] {
				return
			}
			// 允许的用法只有两种:nil 判断,以及绑定语句本身。
			switch p := parent.(type) {
			case *ast.BinaryExpr, *ast.AssignStmt:
				return
			case *ast.SelectorExpr:
				if p.Sel.Name == "WithContext" {
					return
				}
			}
			out = append(out, finding{v.Pos(), "句柄 " + v.Name + " 未 WithContext 就被使用"})
		}
	})
	return out
}

// walkWithParent 带着父节点与"是否已进入 ctx 作用域"递归,只对作用域内的节点回调。
func walkWithParent(node, parent ast.Node, in bool, scopes map[ast.Node]bool, visit func(n, parent ast.Node)) {
	if node == nil {
		return
	}
	if scopes[node] {
		in = true
	}
	if in {
		visit(node, parent)
	}
	first := true
	ast.Inspect(node, func(child ast.Node) bool {
		if first {
			first = false
			return true // node 自身,继续往下走一层
		}
		if child == nil {
			return false
		}
		walkWithParent(child, node, in, scopes, visit)
		return false // 只处理直接子节点,更深的层由上一行的递归负责
	})
}

// classifyChain 把 a.B().C() 拆成"根是什么"与"链上依次调了哪些方法"。
// handles 非 nil 时,以已知句柄标识符为根的链也会被识别(用于 x = x.WithContext(...))。
func classifyChain(e ast.Expr, handles map[string]bool) (kind string, chain []string) {
	root, chain := unwindChain(e)
	if sel, ok := root.(*ast.SelectorExpr); ok {
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "model" && sel.Sel.Name == "DB" {
			return "model.DB", chain
		}
	}
	if id, ok := root.(*ast.Ident); ok {
		if id.Name == "db" && len(chain) > 0 && chain[0] == "Get" {
			return "db.Get()", chain[1:]
		}
		if handles[id.Name] && len(chain) > 0 {
			return "句柄 " + id.Name, chain
		}
	}
	return "", chain
}

// unwindChain 把 a.B().C().D() 拆成(根表达式 a,方法名 [B C D])。
func unwindChain(e ast.Expr) (ast.Expr, []string) {
	var chain []string
	for {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return e, chain
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return e, chain
		}
		chain = append([]string{sel.Sel.Name}, chain...)
		e = sel.X
	}
}

func containsWithContext(chain []string) bool {
	for _, m := range chain {
		if m == "WithContext" {
			return true
		}
	}
	return false
}

// ─────────────────────────────── 自检 ───────────────────────────────

// 分析器本身必须真的认得出这些形状。
//
// 没有它,分析器写坏(例如 unwindChain 的根判定失手)会让上面两条断言
// "全绿但一个字节没读" —— 这正是本仓库最常见的假回归形状。
// 用例里既有该报的,也有**不该报**的:只测阳性会漏掉"什么都报"这种写法。
func TestCtxGuardAnalyzerDetectsKnownShapes(t *testing.T) {
	const src = `package p

import (
	"context"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
)

func bareChain(ctx context.Context) {
	db.Get().Where("id = ?", 1).Find(nil)
}

func bareMainDB(ctx context.Context) {
	model.DB.Transaction(nil)
}

func escapingHandle(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	helper(gdb)
}

func closureGetsCtx() {
	run(func(ctx context.Context) error {
		return db.Get().Find(nil).Error
	})
}

func boundChain(ctx context.Context) {
	db.Get().WithContext(ctx).Find(nil)
}

func reboundHandle(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	gdb = gdb.WithContext(ctx)
	helper(gdb)
	gdb.Find(nil)
}

func boundMainDB(ctx context.Context) {
	model.DB.WithContext(ctx).Transaction(nil)
}

func noCtxAtAll() {
	gdb := db.Get()
	helper(gdb)
	db.Get().Find(nil)
}

func ignoresCtx(ctx context.Context) {}

func explicitlyDeclines(_ context.Context) {}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", src, 0)
	require.NoError(t, err)

	// 句柄锁
	flagged := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if len(unboundHandleUses(fn)) > 0 {
			flagged[fn.Name.Name] = true
		}
	}
	assert.Equal(t,
		map[string]bool{"bareChain": true, "bareMainDB": true, "escapingHandle": true, "closureGetsCtx": true},
		flagged,
		"句柄锁的分析器认错了形状 —— 认不出就是永真断言,认过头就是把 WithContext 正确的写法也报成缺陷")

	// 参数锁
	var ignored []string
	for _, f := range unusedContextParams(file) {
		ignored = append(ignored, f.what)
	}
	assert.Contains(t, ignored, "func ignoresCtx(ctx context.Context) 未使用 ctx")
	assert.Contains(t, ignored, "闭包(ctx context.Context) 未使用 ctx",
		"闭包参数同样要被认出来 —— M7 的原样形状就是 HotAsync 的闭包丢掉 ctx")
	for _, ok := range []string{"boundChain", "reboundHandle", "boundMainDB", "explicitlyDeclines"} {
		for _, got := range ignored {
			assert.NotContains(t, got, ok, "%s 用了(或显式声明不用)ctx,不该被参数锁报出来", ok)
		}
	}
}
