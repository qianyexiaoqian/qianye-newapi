package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groupns_hookpoint_guard_test.go —— 「写了但没接」的防线。
//
// 这个仓库反复出现的形状是:hook 变量定义好了、实现体写好了、测试全绿,
// 而上游那一行调用**根本没插进去**。表现是功能在管理端配得好好的、线上零效果,
// 而且没有任何东西会变红 —— 因为每一半单独看都是对的。
//
// 三个 hook 各自的调用点在这里逐个断言。

// TestGroupNamespaceHooksAreVacantByDefault 断言三个 hook 的默认实现是
// **上游语义**,而不是某个偷懒的常量。
//
// 用非平凡入参(而不是零值)是刻意的:恒等函数与"返回零值"、"返回常量"在零值输入上
// 无法区分,而后两者正是"有人赋了一个坏实现"的样子。
func TestGroupNamespaceHooksAreVacantByDefault(t *testing.T) {
	group, mode := service.QyResolveDefaultModelGroup(42, "浅夜の梦专属号池")
	assert.Equal(t, "浅夜の梦专属号池", group,
		"QyResolveDefaultModelGroup 的默认实现必须恒等返回入参 —— 它逐位等于上游那行 "+
			"`userGroup = userCache.Group`;返回别的东西意味着扩展没装时行为也变了")
	assert.Equal(t, service.QyDefaultModeInherit, mode)

	assert.False(t, service.QyGroupRatioMissingDenied("任何模型分组"),
		"QyGroupRatioMissingDenied 的默认实现必须恒 false —— 默认打开等于一次全站 403")

	allowed, reason := service.QyModelGroupFundingAllowed(42, "default", "反重力的哈基米")
	assert.True(t, allowed, "QyModelGroupFundingAllowed 的默认实现必须恒放行")
	assert.Empty(t, reason)
}

// TestAuthWiresDefaultModelGroupResolution 断言 middleware/auth.go 里那段接线**存在**,
// 而且 pin 分支跑了与显式令牌分组完全相同的两道校验。
//
// 少任何一道的后果是具体的:
//
//	少 QyUsableGroupsForUser  → 默认值成为绕过权威可选清单的后门,
//	                            运营能靠"设默认"把一个本不许选的分组塞给整组用户
//	少 ContainsGroupRatio     → 那一整组用户按 fail-open 的 1.0 倍静默计费
func TestAuthWiresDefaultModelGroupResolution(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	path := filepath.Join(root, "middleware", "auth.go")
	file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, perr)

	called := map[string]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			called[sel.Sel.Name]++
		}
		return true
	})

	assert.Positive(t, called["QyResolveDefaultModelGroup"],
		"middleware/auth.go 没有调用 service.QyResolveDefaultModelGroup —— "+
			"「用户分组的默认模型分组」整条链路是断的:管理端配得好好的,线上零效果,"+
			"空分组令牌继续撞 503")
	assert.Positive(t, called["QyGroupRatioMissingDenied"],
		"middleware/auth.go 没有调用 service.QyGroupRatioMissingDenied —— "+
			"严格模式的开关拨了也不会有任何反应")
	assert.GreaterOrEqual(t, called["QyUsableGroupsForUser"], 2,
		"QyUsableGroupsForUser 必须在**两个**分支上各出现一次。少了 pin 那一处,"+
			"「设默认」就成了绕过权威可选清单的后门")

	// ── 下面这一半必须**限定在 pin 分支内部**统计,不能按整个文件数 ──
	//
	// 按文件数是这条守卫此前的形状,而它保护不了自己声明要保护的东西:auth.go 里
	// ContainsGroupRatio 本来就出现 3 次(显式令牌分组、pin、最终检查),把 pin 那一处
	// 整段删掉之后 `>= 2` 仍然成立,守卫全绿而 pin 可以指向一个不在 GroupRatio 里的
	// 模型分组、按 fail-open 的 1.0 倍静默计费 —— 正是它注释里写明要防的那件事。
	// (已做变异验证:把 pin 分支的那个 if 改成 `if false {`,旧守卫通过。)
	pinCalls := callsInPinBranch(t, file)
	assert.Positive(t, pinCalls["QyUsableGroupsForUser"],
		"pin 分支里没有 QyUsableGroupsForUser —— 「设默认」成了绕过权威可选清单的后门")
	assert.Positive(t, pinCalls["ContainsGroupRatio"],
		"pin 分支里没有 ContainsGroupRatio —— pin 可以指向一个没有倍率的模型分组,"+
			"那一整组用户按 fail-open 的 1.0 倍静默计费")
	assert.Positive(t, pinCalls["QyNotePinRejected"],
		"pin 分支的校验失败路径没有回调 service.QyNotePinRejected —— "+
			"/admin/health 的 pin_rejects 恒为 0,一次整组 500 在健康面板上完全看不见")
}

// callsInPinBranch 只统计 `case service.QyDefaultModePin:` 这一个分支体内部的调用。
func callsInPinBranch(t *testing.T, file *ast.File) map[string]int {
	t.Helper()
	out := map[string]int{}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range clause.List {
			sel, ok := expr.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "QyDefaultModePin" {
				continue
			}
			found = true
			for _, stmt := range clause.Body {
				ast.Inspect(stmt, func(m ast.Node) bool {
					call, ok := m.(*ast.CallExpr)
					if !ok {
						return true
					}
					if s, ok := call.Fun.(*ast.SelectorExpr); ok {
						out[s.Sel.Name]++
					}
					return true
				})
			}
		}
		return true
	})
	require.True(t, found,
		"middleware/auth.go 里找不到 `case service.QyDefaultModePin:` —— pin 这一档整段没了")
	return out
}

// TestBillingSessionWiresFundingGate 断言钱包出资闸门接在**钱包出资决策**上,
// 而且 tryWallet 是订阅出不了资之后的唯一去处。
//
// ═══════════ 这条守卫的理由在本轮换了 ═══════════
//
// 旧理由是「四种计费偏好(wallet_only / wallet_first / subscription_first 的回落 /
// subscription_only 的非订阅分支)全部经由 tryWallet」。计费偏好已经整个去掉,
// 扣费顺序写死为套餐优先,于是形状变成:**只有一条 tryWallet 路径**。
//
// 两半都要钉住,少任何一半闸门都会静默失效:
//
//	闸门在 tryWallet 里     挂在别处就漏掉钱包出资这个唯一的判定时机
//	tryWallet 被调用 ≥ 2 次 一次是"无活跃订阅"的直达,一次是订阅预扣失败后的回落。
//	                        少了后者,「有权限的人」在套餐用尽时会被直接 403 ——
//	                        那正是本轮要修的那条缺陷,而它不会让任何别的测试变红
func TestBillingSessionWiresFundingGate(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	path := filepath.Join(root, "service", "billing_session.go")
	file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, perr)

	gateInWallet := false
	tryWalletCalls := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "tryWallet" {
				tryWalletCalls++
			}
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		id, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || id.Name != "tryWallet" {
			return true
		}
		fn, ok := assign.Rhs[0].(*ast.FuncLit)
		if !ok {
			return true
		}
		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "QyModelGroupFundingAllowed" {
				gateInWallet = true
			}
			return true
		})
		return true
	})

	assert.True(t, gateInWallet,
		"service/billing_session.go 的 tryWallet 闭包里找不到 QyModelGroupFundingAllowed —— "+
			"钱包出资闸门没有接上,或者被挪到了别的位置")
	assert.GreaterOrEqual(t, tryWalletCalls, 2,
		"tryWallet 必须在**两个**位置被调用:无活跃订阅的直达,以及订阅预扣失败后的回落。"+
			"只剩一个说明「套餐出不了资就走钱包」这条回落被删掉了 —— "+
			"用户分组本来就含该模型分组的人会在套餐用尽时被直接 403")
}
