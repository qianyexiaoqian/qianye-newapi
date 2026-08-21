package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 两条中继路径的「免费模型」分支都必须仍然过一次余额闸。
//
// 判据落在源码上是因为这两处只有接线可测：闸门本身的行为由
// service.TestRejectOverdrawnFreeModelCall 覆盖，而"纯函数改对了、调度层没接上"
// 正是本项目反复出现的形状 —— 删掉调用点之后单测全绿，缺陷原样复活。
// gin 注册之后拿不回处理链，运行时又要先过鉴权与真实上游，所以判据只能落在
// 源码上（与 topup_tokens_mode_test.go 的 TestRequestEpayNormalizesBeforePricing
// 同形）。
func TestFreeModelBranchesStillCheckTheWalletGate(t *testing.T) {
	cases := []struct {
		name string
		file string
		fn   string
	}{
		{"同步中继", "relay.go", "Relay"},
		{"异步任务", "../relay/relay_task.go", "RelayTaskSubmit"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tc.file, nil, 0)
			require.NoError(t, err)

			var target *ast.FuncDecl
			for _, d := range file.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if ok && fn.Name.Name == tc.fn {
					target = fn
				}
			}
			require.NotNil(t, target, "%s 里没有 %s,这份守卫已经过期", tc.file, tc.fn)

			found := false
			ast.Inspect(target, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "RejectOverdrawnFreeModelCall" {
					found = true
				}
				return true
			})
			assert.True(t, found,
				"免费模型分支跳过的只能是预扣;余额闸一旦一起跳过,"+
					"已经欠费的账号就能无限次调用并无限次记账(内置工具附加费与"+
					"ModelRatio/ModelPrice 完全解耦)")
		})
	}
}
