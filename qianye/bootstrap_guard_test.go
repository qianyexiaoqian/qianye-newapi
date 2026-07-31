package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootstrap_guard_test.go —— 锁住 Init() 里那两个挂载点的存在。
//
// # 为什么必须用 AST
//
// 缺表自检的完整链路横跨两个包:db.Migrate 负责判定,bootstrap.Init 负责消费。
// db 包的测试能证明"判定写对了、后台循环接得上",但证明不了 Init 真的调了它 ——
// 而本仓库反复出现的失败形状第一名正是"纯函数写对了,调度层没接上"。
//
// Init 本身没法用普通单测驱动:它要求 YAML 配置、真实 MySQL、主库已初始化。
// 所以这里退而求其次,直接对源码断言两件事:
//
//  1. db.Migrate 的返回值仍然被 return 出去 —— 那是"本节点刚建过表却仍缺表"
//     这条 fail-fast 的唯一出口,删了它自检就只剩日志;
//  2. db.StartSchemaRecheck 被调用 —— 那是 M10 之后"缺表不再杀进程"所换来的
//     那一半可见性:降级态每分钟点名一次缺哪张表,并在表建出来后自动解除。
//     删了它,从节点缺表就退回成"启动日志里一行会被滚走的错误"。
func TestInitKeepsSchemaCheckMountPoints(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	require.NoError(t, err)

	var init *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "Init" {
			init = fn
		}
	}
	require.NotNil(t, init, "bootstrap.go 里必须有 func Init()")

	called := map[string]bool{}
	returnsMigrateErr := false
	ast.Inspect(init.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "db" {
			return true
		}
		called[sel.Sel.Name] = true
		return true
	})
	// Migrate 的 error 必须被冒泡出去:if err := db.Migrate(...); err != nil { return err }
	ast.Inspect(init.Body, func(n ast.Node) bool {
		stmt, ok := n.(*ast.IfStmt)
		if !ok || stmt.Init == nil {
			return true
		}
		assign, ok := stmt.Init.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Migrate" {
			return true
		}
		for _, s := range stmt.Body.List {
			if _, isReturn := s.(*ast.ReturnStmt); isReturn {
				returnsMigrateErr = true
			}
		}
		return true
	})

	assert.True(t, called["Migrate"], "Init 必须调用 db.Migrate")
	assert.True(t, returnsMigrateErr,
		"db.Migrate 的 error 必须 return 出去:那是「本节点刚建过表却仍缺表」的唯一出口")
	assert.True(t, called["StartSchemaRecheck"],
		"Init 必须调用 db.StartSchemaRecheck —— 缺表不再杀进程之后,"+
			"降级态的持续可见性与自愈全靠它,删掉它自检就退化成一行会被滚走的日志")
}
