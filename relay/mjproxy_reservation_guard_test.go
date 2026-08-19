package relay

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mjproxy_reservation_guard_test.go —— Midjourney 的两个提交入口必须在调用上游
// **之前**原子预留额度。
//
// 缺陷形态:两处都是 `model.GetUserQuota(...)` 读一个余额快照 → 判一次
// `userQuota-priceData.Quota < 0` → 最长 60 秒的上游调用 → defer 里才真扣钱。
// 这是教科书式的 check-then-act:并发请求全部通过同一个快照。实测一张图的余额
// 并发发出 8 张(users.quota 与 tokens.remain_quota 双双扣到 -350000),40 路
// 扣到 -1,950,000;swap_face 同样 20 路扣到 -475000。/mj 路由组连限流中间件都
// 没有,没有任何东西兜底。
//
// 这条守卫是源码级的,因为真正的行为测试需要一个完整的 MJ 上游 + 渠道 + 令牌
// 链路;而缺陷本身是**结构性**的 —— 只要"读后再用"那一行回来,或者预留跑到了
// 上游调用后面,缺陷就原样复活。守卫直接钉住这个结构。

func mjHandlerBody(t *testing.T, funcName string) (*token.FileSet, *ast.FuncDecl) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mjproxy_handler.go", nil, parser.ParseComments)
	require.NoError(t, err)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == funcName {
			return fset, fn
		}
	}
	t.Fatalf("函数 %s 不在 mjproxy_handler.go 里了 —— 守卫必须跟着改名走", funcName)
	return nil, nil
}

// callOffsets 返回 pkg.Fn(...) 这种调用在函数体内出现的位置(按源码顺序)。
func callOffsets(fset *token.FileSet, fn *ast.FuncDecl, pkg, name string) []int {
	var offsets []int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != pkg {
			return true
		}
		offsets = append(offsets, fset.Position(call.Pos()).Offset)
		return true
	})
	return offsets
}

func TestMidjourneySubmitReservesQuotaBeforeCallingUpstream(t *testing.T) {
	for _, funcName := range []string{"RelayMidjourneySubmit", "RelaySwapFace"} {
		t.Run(funcName, func(t *testing.T) {
			fset, fn := mjHandlerBody(t, funcName)

			reserve := callOffsets(fset, fn, "model", "TryReserveUserQuota")
			require.NotEmpty(t, reserve,
				"必须走 model.TryReserveUserQuota 那把 `WHERE quota >= ?` 的原子闩,"+
					"读余额再判断挡不住并发")

			reserveToken := callOffsets(fset, fn, "model", "TryReserveTokenQuota")
			require.NotEmpty(t, reserveToken,
				"令牌额度是「这把 key 最多花多少」的硬约束,同样要预留,否则子令牌上限形同虚设")

			upstream := callOffsets(fset, fn, "service", "DoMidjourneyHttpRequest")
			require.Len(t, upstream, 1)

			assert.Less(t, reserve[0], upstream[0],
				"预留必须发生在上游调用之前 —— 上游最长 60 秒,那正是并发窗口")
			assert.Less(t, reserveToken[0], upstream[0],
				"令牌预留同样要在上游调用之前")

			// 失败出口必须把预留退回去,否则被拒的提交会白扣用户的钱。
			refund := callOffsets(fset, fn, "model", "IncreaseUserQuota")
			assert.NotEmpty(t, refund, "没有走到结算的出口必须退还预留")
		})
	}
}

func TestMidjourneyHandlersNoLongerCheckThenAct(t *testing.T) {
	// 不带 ParseComments 重新解析,再把函数体打印回源码 —— 注释里解释这个缺陷是
	// 应该的,守卫只能看代码。
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mjproxy_handler.go", nil, 0)
	require.NoError(t, err)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "RelayMidjourneySubmit" && fn.Name.Name != "RelaySwapFace") {
			continue
		}
		var sb strings.Builder
		require.NoError(t, printer.Fprint(&sb, fset, fn))
		assert.NotContains(t, strings.ReplaceAll(sb.String(), " ", ""), "userQuota-priceData.Quota<0",
			"%s:「读余额快照再判断」是缺陷本身 —— 并发请求全部通过同一个快照,"+
				"一张图的余额能换任意多张图", fn.Name.Name)
	}
}
