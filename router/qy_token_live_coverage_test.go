package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// distributeCall 是「这条链真的会往上游转发」的判据。
// 只有过了 Distribute 的请求才会选渠道、发上游、计费。
const distributeCall = "middleware.Distribute"

const tokenLiveStatsCall = "middleware.QyTokenLiveStats"

// tokenLiveCoverageExemptions 是允许「转发但不计数」的链,每条都要写清理由。
var tokenLiveCoverageExemptions = map[string]string{
	// playground 走 UserAuth 而不是 TokenAuth,压根没有 token id 可记;
	// 「API 密钥」页那一列只讲这把 key 被怎么用,与站内试玩无关。
	"SetRelayRouter/playgroundRouter": "UserAuth 链,没有令牌身份",
}

// TestEveryRelayEntryCountsTokenLiveStats 守「新增的转发入口不会忘记挂
// QyTokenLiveStats」。
//
// # 为什么值得有这一条
//
// 上游 rc.33 把视频与厂商原生路由整体搬了家:/v1/video/generations 挪进新的
// videoSharedRouter、kling / jimeng / suno / doubao 从 video-router 搬进插件路由、
// 又新增了 /v1/tasks/** 与宿主协议端点。这四处**都是新写的链**,一条都没有带上
// 这一列 —— 而合并时编译、go vet、全量测试无一报错:计数少了不影响任何断言,
// 只是「API 密钥」页上视频与任务流量整段消失,而看页面的人会以为自己没发过请求。
//
// 判据钉在 Distribute 上而不是路径前缀上:路径会随上游重构漂移(这次就漂了),
// 而「过了 Distribute 就是转发」是这套架构里不会变的那一条。
func TestEveryRelayEntryCountsTokenLiveStats(t *testing.T) {
	root, err := filepath.Abs(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	// key = "<函数名>/<接收者变量名>",value = 这条链上出现过的中间件名字
	chains := map[string]map[string]bool{}
	// 子分组继承父分组的中间件:`child := parent.Group("")` 之后,child 上的请求
	// 一样会先跑完 parent 的那几条。不跟这条边就会把 relayV1Router 下面的
	// wsRouter / httpRouter 误报成漏挂。
	parents := map[string]string{}
	scanned := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(root, name), nil, 0)
		require.NoError(t, parseErr, name)
		scanned++

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					// child := parent.Group(...)
					if len(node.Lhs) != 1 || len(node.Rhs) != 1 {
						return true
					}
					child, isIdent := node.Lhs[0].(*ast.Ident)
					if !isIdent {
						return true
					}
					call, isCall := node.Rhs[0].(*ast.CallExpr)
					if !isCall {
						return true
					}
					sel, isSel := call.Fun.(*ast.SelectorExpr)
					if !isSel || sel.Sel.Name != "Group" {
						return true
					}
					parent, isIdent := sel.X.(*ast.Ident)
					if !isIdent {
						return true
					}
					parents[fn.Name.Name+"/"+child.Name] = fn.Name.Name + "/" + parent.Name
				case *ast.CallExpr:
					// group.Use(...) / group.POST(path, handlers...)
					sel, isSel := node.Fun.(*ast.SelectorExpr)
					if !isSel {
						return true
					}
					receiver, isIdent := sel.X.(*ast.Ident)
					if !isIdent {
						return true
					}
					key := fn.Name.Name + "/" + receiver.Name
					for _, arg := range node.Args {
						recordChainMiddleware(chains, key, arg)
					}
				case *ast.CompositeLit:
					// []gin.HandlerFunc{...} —— 宿主协议端点那几条就是这么写的
					array, isArray := node.Type.(*ast.ArrayType)
					if !isArray {
						return true
					}
					if elemName(array.Elt) != "gin.HandlerFunc" {
						return true
					}
					key := fn.Name.Name + "/[]gin.HandlerFunc"
					for _, elt := range node.Elts {
						recordChainMiddleware(chains, key, elt)
					}
				}
				return true
			})
		}
	}

	require.Positive(t, scanned, "一个 router 源文件都没扫到,守卫失效了")

	// inherited 顺着 Group 边向上找:自己没挂,父分组挂了也算挂了。
	var inherited func(key, middleware string, depth int) bool
	inherited = func(key, middleware string, depth int) bool {
		if depth > 8 {
			return false
		}
		if chains[key][middleware] {
			return true
		}
		parent, ok := parents[key]
		if !ok {
			return false
		}
		return inherited(parent, middleware, depth+1)
	}

	offenders := make([]string, 0, 2)
	relaying := 0
	for key := range chains {
		if !inherited(key, distributeCall, 0) {
			continue
		}
		relaying++
		if inherited(key, tokenLiveStatsCall, 0) {
			continue
		}
		if _, exempt := tokenLiveCoverageExemptions[key]; exempt {
			continue
		}
		offenders = append(offenders, key)
	}
	sort.Strings(offenders)

	require.Positive(t, relaying, "没有扫到任何带 Distribute 的链,判据失效了")
	assert.Emptyf(t, offenders,
		"以下转发链没有挂 %s:\n  %s\n"+
			"过了 Distribute 就是一次真实的上游转发,「API 密钥」页的当前并发 / 近 1 分钟\n"+
			"两列靠它统计。少挂一条不会让任何测试变红,只会让那一段流量在页面上消失。\n"+
			"挂载位置紧跟 TokenAuth、排在限流之前(被 429 挡掉的请求也要计入);\n"+
			"确有理由不计的写进 tokenLiveCoverageExemptions 并说明。",
		tokenLiveStatsCall, strings.Join(offenders, "\n  "))
}

// recordChainMiddleware 把一个实参里出现的 middleware.Xxx() 记到这条链上。
// 实参可能是 middleware.Foo() 本身,也可能是 middleware.Wrap(middleware.Foo())。
func recordChainMiddleware(chains map[string]map[string]bool, key string, arg ast.Expr) {
	ast.Inspect(arg, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := elemName(call.Fun)
		if !strings.HasPrefix(name, "middleware.") {
			return true
		}
		if chains[key] == nil {
			chains[key] = map[string]bool{}
		}
		chains[key][name] = true
		return true
	})
}

func elemName(expr ast.Expr) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name + "." + sel.Sel.Name
}
