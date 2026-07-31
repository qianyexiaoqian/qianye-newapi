package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audit_prune_registration_test.go —— 锁住 audit.prune 这个挂载点。
//
// # 为什么单独锁它
//
// audit.retention_days 之所以要修,是因为它是一个"定义了、有默认值、写进了
// 示例 YAML、却没有任何消费方"的空闸门。而修它的方式恰好会制造同一个形状的
// 下一代:retention.go 里的判定与分批全都写对了(qianye/service/audit/
// retention_test.go 已经逐条量过),但只要 bootstrap 里那行 lease.Run 被删掉
// 或被包进一个条件,配置项就重新变回空转 —— 而这一次自检登记表里已经写上了
// "消费方:qianye/service/audit/retention.go",告警不会再响。
//
// StartBackgroundTasks 本身没法用普通单测驱动(要 YAML、真实 MySQL、主库已初始化),
// 所以照 bootstrap_guard_test.go 的先例直接对源码断言。
func TestStartBackgroundTasksRegistersAuditPruneUnconditionally(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Recv == nil && d.Name.Name == "StartBackgroundTasks" {
			fn = d
		}
	}
	require.NotNil(t, fn, "bootstrap.go 里必须有 func StartBackgroundTasks()")

	// 只认函数体的**直接**语句:注册被包进 if 就等于"按启动那一刻的配置决定
	// 要不要接消费方",而 retention_days 是可以被 config_reload_seconds 热载的。
	// 那种写法下运维把 0 改成 400 不会有任何效果 —— 正是刚被修掉的那个形状。
	registered := false
	for _, stmt := range fn.Body.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok || len(call.Args) != 3 {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" {
			continue
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "lease" {
			continue
		}
		name, ok := call.Args[0].(*ast.BasicLit)
		if !ok || name.Value != `"audit.prune"` {
			continue
		}
		fnArg, ok := call.Args[2].(*ast.SelectorExpr)
		require.True(t, ok, "lease.Run(\"audit.prune\", ...) 的第三个参数必须是 audit.Prune")
		pkg, ok := fnArg.X.(*ast.Ident)
		assert.True(t, ok && pkg.Name == "audit" && fnArg.Sel.Name == "Prune",
			"audit.prune 这个租约名必须真的跑 audit.Prune,而不是别的函数")
		registered = true
	}

	assert.True(t, registered,
		"StartBackgroundTasks 必须无条件注册 lease.Run(\"audit.prune\", audit.PruneInterval, audit.Prune)。"+
			"少了这一行,audit.retention_days 就重新变成空转的配置项,"+
			"而 config/selfcheck.go 的登记表已经宣称它有消费方 —— 告警不会再响")
}
