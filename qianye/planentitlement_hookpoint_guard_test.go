package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planentitlement_hookpoint_guard_test.go —— 锁住「套餐解锁模型分组」在上游
// 代码里的四个挂载点。
//
// # 为什么这条锁必须存在
//
// 这四行是本功能与上游之间**全部**的耦合面,每处都是把
// service.GetUserUsableGroups(g) 原地换成 service.QyUsableGroupsForUser(userId, g):
//
//	middleware/auth.go     令牌分组校验(请求时唯一的闸门)
//	controller/group.go    令牌页的分组下拉
//	controller/pricing.go  价格表
//	controller/user.go     个人信息页的可用模型
//
// 它们全都是**上游合并的冲突文件**,而"合并时被冲回原样"是完全现实的事故 ——
// 而且它不会报错、不会告警,只会让付费用户安静地拿不到他买的模型分组:
//
//	auth.go 被冲回     解锁在热路径上零效果:下拉里选得到、一发请求就 403
//	group.go 被冲回    有权限但选不到:下拉里根本不出现那个分组
//	pricing/user 冲回  价格表与个人信息页少一批模型,而令牌照常能用
//
// 模块自己的用例一条都发现不了这件事:它们直接调 Resolve 或 service 的封装。
//
// # 这条锁抓不到什么
//
// 它只看"这个函数里有没有调这个封装",不看参数对不对(比如传了 0 而不是真实
// userId)。参数语义由 modules/planentitlement 自己的用例负责。
// 它的职责是防止整行被冲掉。
var planUnlockCallSites = []struct {
	file string
	fn   string
	why  string
}{
	{"../middleware/auth.go", "TokenAuth",
		"请求时唯一的分组闸门。这一行没了,套餐解锁在热路径上零效果 —— " +
			"用户在令牌页选得到那个分组,一发请求就「无权访问 X 分组」"},
	{"../controller/group.go", "GetUserGroups",
		"令牌页分组下拉的唯一数据源。这一行没了,用户买了套餐却在下拉里看不到那个分组," +
			"于是「有权限但选不到」"},
	{"../controller/pricing.go", "GetPricing",
		"价格表按可选分组裁剪。这一行没了,套餐解锁的分组在价格表里整段消失," +
			"用户没有任何入口能确认自己买到了什么"},
	{"../controller/user.go", "GetUserModels",
		"个人信息页的可用模型清单,口径必须与令牌页一致"},
}

func TestPlanUnlockKeepsItsUpstreamCallSites(t *testing.T) {
	for _, want := range planUnlockCallSites {
		t.Run(want.file, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), want.file, nil, 0)
			require.NoErrorf(t, err, "应当可解析 %s", want.file)

			var fn *ast.FuncDecl
			for _, decl := range file.Decls {
				d, ok := decl.(*ast.FuncDecl)
				if ok && d.Recv == nil && d.Name.Name == want.fn {
					fn = d
				}
			}
			require.NotNilf(t, fn, "%s 里找不到 func %s —— 上游改了函数名?"+
				"改名之后这张表必须跟着改,否则这条锁会变成永远绿的摆设", want.file, want.fn)

			found := false
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "QyUsableGroupsForUser" {
					found = true
				}
				return !found
			})

			assert.Truef(t, found,
				"%s 的 %s 里缺少 service.QyUsableGroupsForUser 挂载点。理由:%s。\n"+
					"这一行被冲掉不会报错、不会告警,只会让付费用户安静地拿不到他买的模型分组",
				want.file, want.fn, want.why)
		})
	}
}

// TestPlanUnlockDefaultImplementationsAreIdentity 把两个 hook 的**恒等契约**
// 变成断言。
//
// 上游那四行没有任何 nil 判断,靠的就是默认实现恒等:
//
//	QyPlanUnlockGroups   必须原样返回入参那张 map(不是新建一张空的,也不是 nil)
//	QyPlanUnlockedGroup  必须返回 false(它只用于**放宽**写入侧判定,
//	                     默认返回 true 会让 groupmatrix 的写入校验整段失效)
//
// 默认实现一旦不再恒等,没装扩展的部署会在完全没有任何配置的情况下改变行为,
// 而这两个方向恰好一个是断服、一个是把收紧整段放开。
func TestPlanUnlockDefaultImplementationsAreIdentity(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../service/qy_usablegroup_export.go", nil, 0)
	require.NoError(t, err)

	bodies := map[string]*ast.FuncLit{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		name := spec.Names[0].Name
		if name != "QyPlanUnlockGroups" && name != "QyPlanUnlockedGroup" {
			return true
		}
		lit, ok := spec.Values[0].(*ast.FuncLit)
		require.Truef(t, ok, "%s 的默认实现必须是就地的函数字面量,便于一眼看出它是恒等的", name)
		bodies[name] = lit
		return true
	})
	require.Len(t, bodies, 2, "两个 hook 变量都必须带默认实现,否则未安装扩展时是 nil 直接 panic")

	unlock := bodies["QyPlanUnlockGroups"]
	require.Len(t, unlock.Body.List, 1, "默认实现必须只有一条 return upstream")
	ret, ok := unlock.Body.List[0].(*ast.ReturnStmt)
	require.True(t, ok)
	require.Len(t, ret.Results, 1)
	ident, ok := ret.Results[0].(*ast.Ident)
	assert.True(t, ok && ident.Name == "upstream",
		"QyPlanUnlockGroups 的默认实现必须返回**入参那一张 map**:"+
			"返回新建的 map 会破坏 controller/pricing.go 依赖的指针语义,返回 nil 是全站断服")

	checked := bodies["QyPlanUnlockedGroup"]
	require.Len(t, checked.Body.List, 1)
	ret, ok = checked.Body.List[0].(*ast.ReturnStmt)
	require.True(t, ok)
	require.Len(t, ret.Results, 1)
	lit, ok := ret.Results[0].(*ast.Ident)
	assert.True(t, ok && lit.Name == "false",
		"QyPlanUnlockedGroup 的默认实现必须是 false:它只用于**放宽**写入侧判定,"+
			"默认 true 会让 groupmatrix 的令牌分组写入校验整段失效")
}

// balanceScopeWiring 是「余额使用范围」从配置变成扣费行为的**完整**接线。
//
// # 少任何一根线,界面就开始说假话
//
// 判定住在 qianye/modules/planentitlement,执行在上游的扣费路径上。三根线:
//
//	model/subscription.go 的候选循环   「仅限」的套餐在模型分组对不上时被跳过
//	model/subscription.go 的钱包回退   被跳过的套餐无权禁止钱包回退
//	planentitlement.InstallHooks       两个赋值 + MarkBalanceScopeEnforced 握手
//
// 握手那一步不是形式:没有它,展示侧(管理端弹窗、用户端余额卡片)读到的
// CandidateUsable 恒为 true,于是**配置与展示同时说真话**;有了它,展示侧才敢
// 说「这笔余额在这里用不了」。所以断掉赋值却留下握手,或反过来,都会造出
// 「管理端显示已经限制住了,而钱照样从那张套餐里扣」这个最不能接受的状态。
var balanceScopeWiring = []struct {
	file string
	fn   string
	call string
	why  string
}{
	{"../model/subscription.go", "PreConsumeUserSubscription", "QySubscriptionCandidateUsable",
		"候选循环里的范围过滤。这一行没了,「仅限 G」的套餐余额会被任意模型分组花掉," +
			"而管理端与用户端都显示它已经被限制住了"},
	{"../model/subscription.go", "UserActiveSubscriptionsAllowWalletOverflow", "QyWalletOverflowAllowedDespiteStrict",
		"被范围跳过的套餐无权禁止钱包回退。这一行没了,持有「仅限 G + 不许回退」套餐的用户" +
			"在别的模型分组上会既扣不到套餐余额、也不许用钱包 —— 一个谁都没有配出来的死锁"},
}

func TestBalanceScopeKeepsItsUpstreamWiring(t *testing.T) {
	for _, want := range balanceScopeWiring {
		t.Run(want.fn, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), want.file, nil, 0)
			require.NoErrorf(t, err, "应当可解析 %s", want.file)

			var fn *ast.FuncDecl
			for _, decl := range file.Decls {
				d, ok := decl.(*ast.FuncDecl)
				if ok && d.Recv == nil && d.Name.Name == want.fn {
					fn = d
				}
			}
			require.NotNilf(t, fn, "%s 里找不到 func %s —— 上游改了函数名?"+
				"改名之后这张表必须跟着改,否则这条锁会变成永远绿的摆设", want.file, want.fn)

			found := false
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == want.call {
					found = true
				}
				return !found
			})
			assert.Truef(t, found, "%s 的 %s 里缺少 %s。理由:%s",
				want.file, want.fn, want.call, want.why)
		})
	}
}

// TestBalanceScopeInstallsBothHooksAndTheHandshake 断言接线的三行是一个整体。
//
// 拆开做的后果不对称,所以三行都要在同一个函数里:
//
//	只赋值不握手  过滤真的在扣费路径上跑,但展示侧仍然显示「可用」——
//	              用户看到"余额还在、还能用",实际那笔请求走了钱包
//	只握手不赋值  展示侧开始声称「这笔余额在这里用不了」,而扣费路径没有过滤 ——
//	              界面在骗人,骗的正是"我的钱去哪了"
func TestBalanceScopeInstallsBothHooksAndTheHandshake(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(),
		"modules/planentitlement/planentitlement.go", nil, 0)
	require.NoError(t, err)

	var install *ast.FuncDecl
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name.Name == "InstallHooks" {
			install = d
		}
	}
	require.NotNil(t, install, "planentitlement 必须有 InstallHooks")

	assigned := map[string]bool{}
	handshake := false
	ast.Inspect(install, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok {
					assigned[sel.Sel.Name] = true
				}
			}
		case *ast.CallExpr:
			if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "MarkBalanceScopeEnforced" {
				handshake = true
			}
		}
		return true
	})

	assert.True(t, assigned["QySubscriptionCandidateUsable"],
		"InstallHooks 必须把 CandidateUsable 接到 model.QySubscriptionCandidateUsable 上,"+
			"否则「仅限」在扣费路径上完全空转")
	assert.True(t, assigned["QyWalletOverflowAllowedDespiteStrict"],
		"InstallHooks 必须接上钱包回退的复核,否则「仅限 + 不许回退」的组合会把用户锁死")
	assert.True(t, handshake,
		"InstallHooks 必须调用 MarkBalanceScopeEnforced():没有这次握手,展示侧会一直"+
			"显示「未生效」,而过滤其实已经在扣费路径上跑了")
}

// planUnlockReadSurfaces 是扩展**自己**的可用分组读取面。
//
// # 为什么这一条与上面那张上游表分开
//
// 上面那张表守的是"上游合并把行冲掉";这一张守的是另一种失败:扩展自己新写的
// 页面顺手调了不带身份的 service.GetUserUsableGroups。表现不是 403,而是
// **同一个人在同一个站点的不同页面得到互相矛盾的答案** —— 令牌页选得到 pro、
// 可用率页一条 pro 的数据都看不到。这类不一致没有任何报错,只会变成
// "为什么我买了套餐还是看不到"的工单。
//
// 判据刻意是「不得出现 GetUserUsableGroups」而不是「必须出现 QyUsableGroupsForUser」:
// 前者能抓到"又新加了一处忘了带 userId",后者只能抓到"这一处被改回去了"。
var planUnlockReadSurfaces = []struct {
	file string
	why  string
}{
	{"modules/availability/api.go",
		"可用率页的分组可见性。不带 userId 时套餐解锁的分组整段消失,而令牌页里选得到它"},
	{"modules/groupvis/groupvis.go",
		"模型广场与模型性能的分组可见性,口径必须与令牌页一致"},
}

func TestPlanUnlockReadSurfacesCarryTheUserId(t *testing.T) {
	for _, want := range planUnlockReadSurfaces {
		t.Run(want.file, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), want.file, nil, 0)
			require.NoErrorf(t, err, "应当可解析 %s", want.file)

			bare := false
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "GetUserUsableGroups" {
					bare = true
				}
				return !bare
			})
			assert.Falsef(t, bare,
				"%s 里出现了不带用户身份的 service.GetUserUsableGroups。理由:%s。\n"+
					"请改用 service.QyUsableGroupsForUser(userId, userGroup) —— "+
					"userId <= 0 时它逐位等价于原函数,匿名口径一个字节不变",
				want.file, want.why)
		})
	}
}
