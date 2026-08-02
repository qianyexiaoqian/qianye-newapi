package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// request_audit_registration_test.go —— 锁住请求台账中间件的挂载点。
//
// # 为什么必须单独锁它
//
// 中间件的全部价值来自"挂上去了"这一件事。脱敏写得再好、字段设计得再全,
// 只要 router.go 里那一行 root.Use(audit.Middleware()) 没了,
// qy_request_audits 就是一张永远为空的表 —— 而且不会有任何报错:
// 表在、模型在、清理任务在、config/selfcheck.go 的登记表还宣称
// audit.request_enabled 有消费方,于是启动自检也不会响。
//
// 这正是本仓反复出现的第一种缺陷形状(断链):写对了但没接上。
// 普通单测抓不到它 —— service/audit 的用例测的是中间件本身,
// 它们在中间件从未被挂载的情况下照样全绿。
//
// # 顺序也一并锁住
//
// 这一行必须在 UserAuth/AdminAuth **之前**。挂在认证之后,被 401/403 挡掉的
// 请求就整个不进台账 —— 而越权探测恰恰全是失败请求,是这张表最有价值的一半。
// 由于两者都是 root/子组上的 Use 调用,顺序在源码里就是位置关系,可以直接断言。
func TestRegisterRoutesMountsRequestAuditBeforeAuth(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Recv == nil && d.Name.Name == "RegisterRoutes" {
			fn = d
		}
	}
	require.NotNil(t, fn, "router.go 里必须有 func RegisterRoutes(engine *gin.Engine)")

	auditPos, userAuthPos, adminAuthPos := token.NoPos, token.NoPos, token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		use, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || use.Sel.Name != "Use" || len(call.Args) != 1 {
			return true
		}
		recv, _ := use.X.(*ast.Ident)
		inner, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		arg, ok := inner.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, _ := arg.X.(*ast.Ident)
		if pkg == nil || recv == nil {
			return true
		}
		switch {
		case pkg.Name == "audit" && arg.Sel.Name == "Middleware":
			assert.Equal(t, "root", recv.Name,
				"请求台账中间件必须挂在 /api/qy 根组上,否则只覆盖其中一部分子组")
			auditPos = call.Pos()
		case pkg.Name == "middleware" && arg.Sel.Name == "UserAuth":
			userAuthPos = call.Pos()
		case pkg.Name == "middleware" && arg.Sel.Name == "AdminAuth":
			adminAuthPos = call.Pos()
		}
		return true
	})

	require.NotEqual(t, token.NoPos, auditPos,
		"RegisterRoutes 必须调用 root.Use(audit.Middleware())。少了这一行,"+
			"qy_request_audits 会是一张永远为空的表,而且不会有任何报错 —— "+
			"表在、清理任务在、config/selfcheck.go 还宣称 audit.request_enabled 有消费方")
	require.NotEqual(t, token.NoPos, userAuthPos, "router.go 里必须有 middleware.UserAuth()")
	require.NotEqual(t, token.NoPos, adminAuthPos, "router.go 里必须有 middleware.AdminAuth()")

	assert.Less(t, int(auditPos), int(userAuthPos),
		"台账中间件必须在 UserAuth 之前挂载,否则被认证挡掉的 401 完全不进台账")
	assert.Less(t, int(auditPos), int(adminAuthPos),
		"台账中间件必须在 AdminAuth 之前挂载,否则被认证挡掉的 403 完全不进台账")
}
