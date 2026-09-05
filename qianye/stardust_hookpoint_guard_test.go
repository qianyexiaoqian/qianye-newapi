package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stardust_hookpoint_guard_test.go —— 锁住星屑接进上游代码的七个挂载点。
//
// # 为什么这条锁必须存在
//
// 这七行是星屑与上游之间**全部**的耦合面,每处 1 行:
//
//	model/user.go            finishInsert / FinalizeOAuthUserCreation → QyOnUserRegistered
//	model/subscription.go    CompleteSubscriptionOrder / PurchaseSubscriptionWithBalance /
//	                         AdminBindSubscription → QyOnSubscriptionGranted
//	model/redemption.go      Redeem(套餐码分支)→ QyOnSubscriptionGranted
//	qianye/modules/invite/hook.go     onRedeemSuccess → AfterRedeemSuccess(无条件转发)
//
// 少一行的表现不是报错,是"管理端比例配得好好的,线上零效果":注册奖一个都不发、
// 买套餐一颗星屑都不返、兑换码返跟着消失。本仓的 groupns / planentitlement /
// subscription 三条 hookpoint 守卫存在的理由与此相同 —— "写了但没接"在这个仓库里
// 已经真实发生过不止一次。

// stardustHookSites 是必须存在挂载点的上游函数。路径相对 qianye/ 目录。
var stardustHookSites = []struct {
	file string
	fn   string
	hook string
	why  string
}{
	{"../model/user.go", "finishInsert", "QyOnUserRegistered",
		"密码注册 / 微信注册 / 管理员建号三条建号路径的收口点;没了它注册奖只在 OAuth 路径发"},
	{"../model/user.go", "FinalizeOAuthUserCreation", "QyOnUserRegistered",
		"OAuth 建号路径;没了它 GitHub/Discord/OIDC 注册的用户不给邀请人发注册奖"},
	{"../model/subscription.go", "CompleteSubscriptionOrder", "QyOnSubscriptionGranted",
		"支付网关回调路径;没了它在线付费买套餐不返"},
	{"../model/subscription.go", "PurchaseSubscriptionWithBalance", "QyOnSubscriptionGranted",
		"余额购买路径;没了它用余额买套餐不返"},
	{"../model/subscription.go", "AdminBindSubscription", "QyOnSubscriptionGranted",
		"管理员绑定路径;默认不返,但套餐上可配为返,没了它这项配置是死键"},
	{"../model/redemption.go", "Redeem", "QyOnSubscriptionGranted",
		"套餐兑换码路径;同上"},
}

func TestStardustHooksKeepTheirUpstreamCallSites(t *testing.T) {
	for _, want := range stardustHookSites {
		t.Run(want.file+"/"+want.fn, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), want.file, nil, 0)
			require.NoErrorf(t, err, "应当可解析 %s", want.file)

			fn := funcDeclNamed(file, want.fn)
			require.NotNilf(t, fn, "%s 里找不到 func %s —— 上游改了函数名?"+
				"改名之后这张表必须跟着改,否则这条锁会变成永远绿的摆设", want.file, want.fn)

			n := countCalls(fn, want.hook)
			assert.Equalf(t, 1, n,
				"%s 的 %s 里 %s 的调用次数必须恰好是 1(实际 %d)。理由:%s。\n"+
					"0 次 = 挂载点被冲掉;2 次 = 同一事件发两遍星屑",
				want.file, want.fn, want.hook, n, want.why)
		})
	}
}

// 注册奖那一行必须放在上游 `if inviterId != 0 && IsPaymentComplianceConfirmed()` 块
// **之外**、作为函数体的顶层语句:是否有邀请人、是否受合规门约束,全部由扩展侧自己判
// (design-15 D-G)。塞进 if 块里的话,合规门的归属就悄悄变成了上游代码,而那个块
// 里还有 QuotaForInviter > 0 这类与星屑无关的判定,漏进去就是"置 0 关闭上游注册奖 =
// 顺手关掉星屑注册奖"。
func TestUserRegisteredHookIsATopLevelStatement(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../model/user.go", nil, 0)
	require.NoError(t, err)
	for _, name := range []string{"finishInsert", "FinalizeOAuthUserCreation"} {
		fn := funcDeclNamed(file, name)
		require.NotNil(t, fn, name)
		found := false
		for _, stmt := range fn.Body.List {
			es, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := es.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "QyOnUserRegistered" {
				found = true
			}
		}
		assert.Truef(t, found, "%s 里 QyOnUserRegistered 必须是函数体的顶层语句,不能塞进任何 if 块", name)
	}
}

// 两个 hook 的默认实现必须是空函数体:扩展未安装时上游六条路径与接入前逐字一致。
func TestStardustHookDefaultsAreNoop(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../model/qy_stardust_export.go", nil, 0)
	require.NoError(t, err)
	for _, name := range []string{"QyOnUserRegistered", "QyOnSubscriptionGranted"} {
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok || len(spec.Names) != 1 || spec.Names[0].Name != name {
				return true
			}
			found = true
			require.Len(t, spec.Values, 1, "%s 必须有默认实现,否则未安装扩展时是 nil 直接 panic", name)
			lit, ok := spec.Values[0].(*ast.FuncLit)
			require.True(t, ok, "%s 的默认实现必须是就地的函数字面量", name)
			assert.Empty(t, lit.Body.List, "%s 的默认实现必须是空函数体(no-op)", name)
			return false
		})
		require.True(t, found, "model/qy_stardust_export.go 里必须声明 %s", name)
	}
}

// 星屑吃兑换码事件靠 invite.AfterRedeemSuccess 这个第二级转发槽。D-14 之前它住在
// commission 里,而且必须排在佣金自己的 `!cm.Enabled` 早退之前 —— 佣金一关星屑的
// 兑换码返就一起静默消失。现在 invite 的 onRedeemSuccess 只做一件事:无条件转发。
// 这条锁守的是"无条件"这三个字:任何人在转发前面加一道 `Enabled` 判断,都会把
// invite 关着(星屑仍开着)的部署里的兑换码返静默吃掉。
func TestAfterRedeemSuccessIsForwardedUnconditionally(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "modules/invite/hook.go", nil, 0)
	require.NoError(t, err)
	fn := funcDeclNamed(file, "onRedeemSuccess")
	require.NotNil(t, fn, "invite/hook.go 里找不到 onRedeemSuccess")

	var hookPos, gatePos token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if ident, ok := x.Fun.(*ast.Ident); ok && ident.Name == "AfterRedeemSuccess" && hookPos == token.NoPos {
				hookPos = x.Pos()
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "Enabled" && gatePos == token.NoPos {
				gatePos = x.Pos()
			}
		}
		return true
	})
	require.NotEqual(t, token.NoPos, hookPos, "onRedeemSuccess 里缺少 AfterRedeemSuccess 转发")
	assert.Equal(t, token.NoPos, gatePos,
		"onRedeemSuccess 不许读任何 Enabled 开关:invite 关着时星屑的兑换码返不能跟着消失")
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

// funcDeclNamed 不区分有无接收者:finishInsert 是 (*User) 的方法,其余是普通函数。
func funcDeclNamed(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func countCalls(fn *ast.FuncDecl, name string) int {
	n := 0
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == name {
				n++
			}
		case *ast.SelectorExpr:
			if f.Sel.Name == name {
				n++
			}
		}
		return true
	})
	return n
}
