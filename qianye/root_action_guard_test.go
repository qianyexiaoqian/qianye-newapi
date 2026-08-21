package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// root_action_guard_test.go —— 「哪些动作被提到了超级管理员」的唯一清单。
//
// # 为什么这份清单必须是断言,而不是文档
//
// 提档是**一行路由参数**。它没有返回值、没有调用者、删掉之后编译照过、
// 所有现有用例照绿 —— 与 actor_gate_guard_test.go 守的那批闸门是同一种形状,
// 而本仓已经反复出现过"判据装好了,下一个人加接口时忘了接"。
//
// 更麻烦的是**反方向**:提档很容易连坐。项目方要的是
//   - 兑换码只提"铸码",查/改/删照旧;
//   - 分组命名空间只提"写",读照旧;
//   - 抽奖只提"设定开奖结果",开活动/发布/封盘/取消/隐藏/删除/参与照旧。
//
// 所以这份表同时断言两件事:该提的提了(gate 对得上具体动作),
// **不该提的一个都没提**(同一个注册函数里其余路由的 gate 必须为空)。
// 只断言前一半的话,某天有人图省事把整组换成 RootAuth,这里依然全绿。

// rootGatedRoute 是一条"必须挂 RootActionGate(action)"的路由。
type rootGatedRoute struct {
	method string // GET / POST / PUT / DELETE
	// path 是**该注册调用里那个字符串字面量**,不是完整 URL:组前缀由外层
	// r.Group(...) 提供,拼全 URL 需要一整套前缀解析,而它并不能让断言更强。
	path string
	// action 是 middleware.RootOnlyAction 常量的 Go 标识符(不带包名)。
	action string
	// why 只出现在失败信息里,让人不必先读代码就知道这一档是为什么立的。
	why string
}

// rootGateSite 是一个路由注册函数(里面的某一个路由组),以及它里面全部该提档的路由。
type rootGateSite struct {
	file string
	fn   string
	// recv 是承载这些路由的那个 *gin.RouterGroup 变量名。
	//
	// 它必须写死:上游 SetApiRouter 一个函数里挂着几十个组,光靠 `POST "/"`
	// 认不出是兑换码还是令牌 —— 第一版就是这么撞上的。限定 recv 之后,
	// "不该提的一个都没提"这条断言的作用域也随之收敛到这一个组内,
	// 那正是判定连坐该看的范围。
	recv   string
	routes []rootGatedRoute
	// requireAllWrites 表示这个注册函数里**每一条写路由**都必须提档。
	// 只有"整个写侧提档"的面才置 true(分组命名空间);逐个动作提档的面
	// (兑换码、抽奖、提现)必须留 false,否则这条断言反而会逼着把不该提的
	// 也提上去 —— 那正是本文件要防的连坐。
	requireAllWrites bool
}

var rootGateSites = []rootGateSite{
	{
		file: "router/api-router.go",
		fn:   "SetApiRouter",
		recv: "redemptionRoute",
		routes: []rootGatedRoute{
			{"POST", "/", "RootActionRedemptionCreate",
				"铸码是全站唯一一条凭空增发额度、且产物直接落进操作人自己那一桶的接口"},
		},
	},
	{
		file: "qianye/modules/groupns/groupns.go",
		fn:   "RegisterAdminRoutes",
		recv: "r",
		// 这一面是"写侧整体提档",因此不逐条列 —— requireAllWrites 会把
		// 每一条写路由都查一遍,包括以后新加的。
		requireAllWrites: true,
		routes: []rootGatedRoute{
			{"POST", "/backfill", "RootActionGroupNamespaceWrite", "回填登记表"},
			{"PUT", "/user-groups/:name/default", "RootActionGroupNamespaceWrite", "空分组令牌落进哪个池子"},
			{"POST", "/user-groups", "RootActionGroupNamespaceWrite", "新建用户分组"},
			{"PUT", "/user-groups/:name", "RootActionGroupNamespaceWrite", "改用户分组"},
			{"POST", "/user-groups/:name/rename", "RootActionGroupNamespaceWrite", "改名横跨两库改六张表"},
			{"DELETE", "/user-groups/:name", "RootActionGroupNamespaceWrite", "删除同上"},
			{"POST", "/user-groups/:name/migrate", "RootActionGroupNamespaceWrite", "把一整档人挪去别的分组"},
			{"PUT", "/model-groups/:name", "RootActionGroupNamespaceWrite", "模型分组启停/排序/备注"},
			{"DELETE", "/model-groups/:name", "RootActionGroupNamespaceWrite", "删模型分组会联动清理授权表"},
		},
	},
	{
		file: "qianye/modules/usergroup/usergroup.go",
		fn:   "RegisterAdminRoutes",
		recv: "g",
		routes: []rootGatedRoute{
			{"PUT", "/user-group/config", "RootActionUserGroupDefaultWrite",
				"新注册用户落进哪个分组,写一次影响的是全部未来账号"},
		},
	},
	{
		file: "qianye/modules/withdraw/module.go",
		fn:   "RegisterAdminRoutes",
		recv: "g",
		routes: []rootGatedRoute{
			{"GET", "/withdraw/:id/payee", "RootActionWithdrawPayeeReveal", "收款账号明文"},
			{"GET", "/withdraw/:id/proof", "RootActionWithdrawPayeeReveal", "打款凭证图片,与收款账号同属 PII"},
		},
	},
	{
		file: "qianye/modules/lottery/module.go",
		fn:   "RegisterAdminRoutes",
		recv: "g",
		routes: []rootGatedRoute{
			{"POST", "/lottery/activities/:act_no/guess-result", "RootActionLotteryResultSet",
				"竞猜结果是链下事实,是全站唯一一处管理员说了算的开奖口"},
		},
	},
}

// TestRootOnlyActionsAreGatedExactlyWhereDecided 逐条证明闸门接上了,
// 并且**只**接在该接的那几条上。
func TestRootOnlyActionsAreGatedExactlyWhereDecided(t *testing.T) {
	for _, site := range rootGateSites {
		t.Run(site.file+"::"+site.fn, func(t *testing.T) {
			routes := parseRouteGates(t, site.file, site.fn, site.recv)
			require.NotEmpty(t, routes,
				site.file+" 的 "+site.fn+" 里没解析出任何挂在 "+site.recv+
					" 上的路由注册,这份清单已经过期")

			want := map[string]rootGatedRoute{}
			for _, r := range site.routes {
				want[r.method+" "+r.path] = r
			}

			seen := map[string]bool{}
			for _, got := range routes {
				key := got.method + " " + got.path
				expected, ok := want[key]
				if ok {
					seen[key] = true
					assert.Equal(t, expected.action, got.action,
						key+" 必须挂 middleware.RootActionGate(middleware."+expected.action+
							");理由:"+expected.why)
					continue
				}
				assert.Empty(t, got.action,
					key+" 不在提档清单里却挂了闸门。提档是逐个动作决定的,"+
						"连坐会把 role=10 本来就该能做的事一起关掉 —— "+
						"要提就先把它写进 rootGateSites,连同理由")
				if site.requireAllWrites && got.method != "GET" {
					assert.NotEmpty(t, got.action,
						key+" 是写路由,而这一面(整个写侧)已经决定提到超级管理员;"+
							"新增写路由必须一并挂上闸门并登记进 rootGateSites")
				}
			}

			for key, r := range want {
				assert.True(t, seen[key],
					site.file+" 里找不到路由 "+key+",这份清单已经过期(理由:"+r.why+")")
			}
		})
	}
}

// TestEveryRootOnlyActionConstantIsWired 守"常量声明 == 接线清单"。
//
// 少了会让 middleware 里躺着一个谁都没用的档位常量(界面上就是"这个动作
// 明明说了只有超管能做,实际 role=10 一点就过"),多了会让某条路由挂着一个
// 从未被审视过的动作名。两个方向都只在这里能被发现。
func TestEveryRootOnlyActionConstantIsWired(t *testing.T) {
	declared := parseRootActionConstants(t)
	require.NotEmpty(t, declared, "middleware/root_action.go 里没解析出任何 RootOnlyAction 常量")

	wired := map[string]bool{}
	for _, site := range rootGateSites {
		for _, r := range site.routes {
			wired[r.action] = true
		}
	}

	sort.Strings(declared)
	for _, name := range declared {
		assert.True(t, wired[name],
			"middleware."+name+" 声明了却没有任何一条路由在用它;"+
				"要么把它接上,要么删掉 —— 留一个空档位等于对外承诺了一道并不存在的闸门")
	}
	for name := range wired {
		assert.Contains(t, declared, name,
			"rootGateSites 引用了 middleware."+name+",但 middleware/root_action.go 里没有这个常量")
	}
}

// parsedRoute 是一条被解析出来的路由注册。action 为空表示没挂闸门。
type parsedRoute struct {
	method string
	path   string
	action string
}

// parseRouteGates 从 file 的 fn 里解析出全部路由注册,并识别每一条挂没挂
// middleware.RootActionGate。
//
// 闸门允许两种写法,因为两种在真实代码里都更可读:
//   - 直接内联 middleware.RootActionGate(middleware.RootActionX);
//   - 先 `root := middleware.RootActionGate(...)` 再在多条路由上复用。
//
// 第二种必须认,否则这条守卫会逼着九条路由各写一遍同一个调用。
func parseRouteGates(t *testing.T, file, fn, recv string) []parsedRoute {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, repoFile(file), nil, 0)
	require.NoError(t, err, file+" 解析失败")

	decl := findFuncDecl(parsed, fn)
	require.NotNil(t, decl, file+" 里找不到 "+fn)

	// 第一遍:收集 `x := middleware.RootActionGate(middleware.ActionY)` 的变量名。
	gateVars := map[string]string{}
	ast.Inspect(decl, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		name, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		if action := rootGateAction(assign.Rhs[0]); action != "" {
			gateVars[name.Name] = action
		}
		return true
	})

	methods := map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	var routes []parsedRoute
	ast.Inspect(decl, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !methods[sel.Sel.Name] || len(call.Args) < 2 {
			return true
		}
		group, ok := sel.X.(*ast.Ident)
		if !ok || group.Name != recv {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		path, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		route := parsedRoute{method: sel.Sel.Name, path: path}
		for _, arg := range call.Args[1:] {
			if action := rootGateAction(arg); action != "" {
				route.action = action
				break
			}
			if ident, ok := arg.(*ast.Ident); ok {
				if action := gateVars[ident.Name]; action != "" {
					route.action = action
					break
				}
			}
		}
		routes = append(routes, route)
		return true
	})
	return routes
}

// rootGateAction 从表达式里取出 RootActionGate 的那个动作常量名,不是就返回 ""。
func rootGateAction(expr ast.Expr) string {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return ""
	}
	switch f := call.Fun.(type) {
	case *ast.Ident:
		if f.Name != "RootActionGate" {
			return ""
		}
	case *ast.SelectorExpr:
		if f.Sel.Name != "RootActionGate" {
			return ""
		}
	default:
		return ""
	}
	switch a := call.Args[0].(type) {
	case *ast.Ident:
		return a.Name
	case *ast.SelectorExpr:
		return a.Sel.Name
	}
	return ""
}

// parseRootActionConstants 读出 middleware/root_action.go 里全部
// `X RootOnlyAction = "..."` 常量的 Go 标识符。
func parseRootActionConstants(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, repoFile("middleware/root_action.go"), nil, 0)
	require.NoError(t, err, "middleware/root_action.go 解析失败")

	var names []string
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || value.Type == nil {
				continue
			}
			typeName, ok := value.Type.(*ast.Ident)
			if !ok || typeName.Name != "RootOnlyAction" {
				continue
			}
			for _, name := range value.Names {
				names = append(names, name.Name)
			}
		}
	}
	return names
}
