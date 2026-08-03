package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qy_hookpoint_test.go —— 用 AST 锁死 controller/relay.go 里 QyPreRelayGuard 的位置。
//
// # 为什么必须有这条
//
// controller/relay.go 是上游合并的高频冲突文件:每次同步上游都会有人在 Relay()
// 里增删几行(本次同步 upstream/main 就在重试循环里插入了
// PrepareTieredBillingForSelectedGroup)。而 QyPreRelayGuard 的位置有两条硬约束,
// 之前**完全没有任何测试保护** —— 合并时把它挪到任一侧,防蒸馏规则会静默失效,
// 而接口照常返回 200,运营从任何界面上都看不出来。
//
// 两条约束:
//
//  1. 必须在 GenRelayInfo **之后**。relayInfo.IsStream 是在 GenRelayInfo 里才确定的,
//     防蒸馏规则(按是否流式区分限制)直接依赖它。提前调用会拿到零值 IsStream,
//     规则对所有流式请求失效。
//
//  2. 必须在 PreConsumeBilling **之前**。守卫的作用是拦截请求;放到预扣费之后,
//     被拦截的请求已经扣过钱了,要靠退款路径补回来 —— 而退款失败就是真实漏钱。
//
// 单元测试抓不到这个:QyPreRelayGuard 的实现体可以完美无缺地通过它自己的所有测试,
// 只要 Relay() 里这一行的位置不对,整条规则就是死的。只有把"调用点的相对位置"
// 本身变成断言,合并事故才会让测试变红。

const relayGoPath = "relay.go"

// TestPreRelayGuardSitsBetweenRelayInfoAndPreConsume 锁死上面那两条约束。
func TestPreRelayGuardSitsBetweenRelayInfoAndPreConsume(t *testing.T) {
	seq := qyCallsByFunc(t, relayGoPath)["Relay"]
	require.NotEmpty(t, seq, "controller/relay.go 里找不到 Relay() —— 上游改了函数名?")

	guardIdx := qyIndexOf(seq, "QyPreRelayGuard")
	genIdx := qyIndexOf(seq, "GenRelayInfo")
	preConsumeIdx := qyIndexOf(seq, "PreConsumeBilling")

	require.GreaterOrEqual(t, guardIdx, 0,
		"Relay() 里缺少 QyPreRelayGuard 挂载点:防蒸馏规则整个失效,而接口照常返回 200")
	require.GreaterOrEqual(t, genIdx, 0, "Relay() 里找不到 GenRelayInfo")
	require.GreaterOrEqual(t, preConsumeIdx, 0, "Relay() 里找不到 PreConsumeBilling")

	assert.Greater(t, guardIdx, genIdx,
		"QyPreRelayGuard 必须排在 GenRelayInfo 之后:relayInfo.IsStream 在那一步才确定, "+
			"提前调用会拿到零值,防蒸馏规则对所有流式请求静默失效")
	assert.Less(t, guardIdx, preConsumeIdx,
		"QyPreRelayGuard 必须排在 PreConsumeBilling 之前:放到预扣费之后, "+
			"被拦截的请求已经扣过钱,只能靠退款补回来,退款失败即漏钱")
}

// TestPostRelayGuardStillMounted:失败路径上的收尾守卫也不能在合并里被丢掉。
func TestPostRelayGuardStillMounted(t *testing.T) {
	seq := qyCallsByFunc(t, relayGoPath)["Relay"]
	assert.GreaterOrEqual(t, qyIndexOf(seq, "QyPostRelayGuard"), 0,
		"Relay() 里缺少 QyPostRelayGuard:失败请求不再进入违规计数/收尾统计")
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

// qyCallsByFunc 返回每个顶层函数体内按源码顺序出现的被调用函数名。
// 与 qianye/modules/grouppricing/hookpoint_test.go 的同名辅助保持同一形状:
// 只取选择器的 Sel,使同包变量调用与跨包调用能用同一张表断言。
func qyCallsByFunc(t *testing.T, path string) map[string][]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
	require.NoError(t, err, "应当可解析: %s", path)

	out := map[string][]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var seq []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				seq = append(seq, f.Name)
			case *ast.SelectorExpr:
				seq = append(seq, f.Sel.Name)
			}
			return true
		})
		out[fn.Name.Name] = seq
	}
	return out
}

func qyIndexOf(seq []string, name string) int {
	for i, s := range seq {
		if s == name {
			return i
		}
	}
	return -1
}
