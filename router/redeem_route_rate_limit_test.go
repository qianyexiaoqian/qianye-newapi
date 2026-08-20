package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redeem_route_rate_limit_test.go —— 兑换码入口必须同时挂两把限流。
//
// # 为什么这条测试只能是这种形状
//
// 限流的**机制**由 middleware/redeem_rate_limit_test.go 证明:那两条用例各自
// 构造一批 IP / 一批账号,证明少了哪一把就能被线性放大。但它们证明不了
// 「线上那条路由真的挂了这两把」—— 中间件从路由上被摘掉之后,
// 那两条用例照样全绿,gin 也不会有任何抱怨。
//
// 而 gin 注册完之后拿不回一条路由的处理链:engine.Routes() 只给出最后一个
// handler 的名字。要在运行时走到限流层又得先过 UserAuth(会话链),而伪造一个
// 真会话需要把 model.DB、UserSession 表和 cookie 签名整套搭起来 —— 那测的
// 已经不是这件事了。所以判据落在注册处本身,与 qianye/audit_coverage_guard_test.go
// 守审计埋点用的是同一种形状:它防的是「整段被顺手删掉」,不是证明限流算得对。

func TestRedeemRouteCarriesBothRateLimiters(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "api-router.go", nil, 0)
	require.NoError(t, err)

	// 找 selfRoute.POST("/topup", ...) 这一次注册。路径拼出来是 /api/user/self/topup,
	// 也就是前端兑换框调的那条(controller.TopUp → model.Redeem)。
	var middlewares []string
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "POST" {
			return true
		}
		path, ok := call.Args[0].(*ast.BasicLit)
		if !ok || path.Value != `"/topup"` {
			return true
		}
		found = true
		for _, arg := range call.Args[1:] {
			inner, ok := arg.(*ast.CallExpr)
			if !ok {
				continue
			}
			if fn, ok := inner.Fun.(*ast.SelectorExpr); ok {
				middlewares = append(middlewares, fn.Sel.Name)
			}
		}
		return false
	})

	require.True(t, found, `api-router.go 里必须有一次 POST("/topup", ...) 注册 —— `+
		`路径改了就把这条判据一起改,别让它静默失效`)
	assert.Contains(t, middlewares, "CriticalRateLimit",
		"按出口 IP 的桶:挡住一个人拿一个号狂试")
	assert.Contains(t, middlewares, "UserCriticalRateLimit",
		"按账号的桶:挡住一个人换一批代理狂试。只有 IP 那一把时,代理池的规模"+
			"直接等于猜测次数的放大倍数,而爆破兑换码需要的恰恰只是一个能登录的账号")
}
