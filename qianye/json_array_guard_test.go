package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// json_array_guard_test.go —— 用 AST 保证「下发给前端的数组不会是 JSON null」。
//
// # 缺陷原样
//
// 提现审核页整页白屏,报 `Cannot read properties of null (reading 'find')`。
// 根因在 modules/withdraw/api_admin.go 的队列角标接口:
//
//	var rows []bucket                       // nil 切片
//	...Group("status").Scan(&rows)          // 库里没有 pending/approved/paying 单据
//	respondOK(c, gin.H{"buckets": rows})    // → {"buckets":null}
//
// 前端 `buckets.find(...)` 直接崩。触发条件只有一个:**结果集一行都没有** ——
// 新站点、筛选无命中、某个状态桶为空。项目方是在生产库副本上撞到的。
//
// # 为什么这条锁必须存在,而不是各处 make 一遍就完了
//
// 本仓库反复出现的失败形状是「同一概念的第 N 份拷贝各自漂移」。
// 这套响应信封 `{success,message,data}` 现在有**八份**独立实现
// (withdraw/transfer/paypass 的 respondOK,commission/violation/grouppricing/
// usergroup/availability 的 respond),每一份都是 `c.JSON(200, gin.H{...})`。
// 没有唯一的收口点,就不存在"在 respond 里统一规范化一次"这个选项 ——
// 只改其中一份,得到的是行为与另外七份不同的第九份拷贝。
//
// 反射式兜底(遍历 payload 把所有 nil 切片改写成空切片)另有一层实打实的风险:
// json.RawMessage 与 []byte 在反射下同样是 Kind() == Slice,把 nil 的
// json.RawMessage 换成非 nil 空值会让 Marshal 直接报错("unexpected end of
// JSON input"),把一个能用的 200 变成 500。为一个只在空结果时出现的显示问题
// 去动所有 200 响应的序列化路径,收益/风险不成比例。
//
// 所以走的是「每处显式 make + 一条结构性断言」:
// make 让每一处的意图看得见,这条断言负责让第 14 份拷贝在 CI 上变红。
//
// # 这条锁锁了什么
//
// 凡是用 `var x []T` 声明(即零值 nil)的切片,不许出现在任何一个响应函数的
// 实参表达式里 —— 无论是 `respond(c, gin.H{"items": x})`、
// `respond(c, someView{Cells: x})` 还是 `respond(c, x)`。
// 需要下发就显式 `x := make([]T, 0, n)`,那一行本身就是声明"这是给前端的数组"。
//
// 判据刻意是「声明方式」而不是「运行期是否为 nil」:后者静态分析看不见,
// 而前者恰好是缺陷的原样,并且强制每一处把意图写出来。
//
// # 这条锁**抓不到**什么(请勿把它当兜底网)
//
//   - 只看**同一个函数体内**的声明与使用。切片由别的函数返回时(本轮修的
//     transfer.listGroupRuleRows、controller 里 lease.List() 的错误路径)
//     它一个字都看不见 —— 那两处靠空库行为测试守住,见各模块的
//     nil_array_json_test.go。
//   - 响应函数按**名字**识别(responseFuncNames),给第九份信封起一个表里
//     没有的名字就能绕过。
//   - 不跟踪赋值:`var x []T` 之后 `x = make(...)` 再下发会被误报。
//     真遇上了,正确的修法是把 make 提到声明处,而不是放宽这条规则。
//   - []byte / []uint8 刻意不管:它们序列化成 base64 字符串而不是 JSON 数组,
//     nil 与空值的区别不会让前端崩,而 json.RawMessage 正是这个形状。
//
// 遍历复用 httpq_guard_test.go 的 forEachQianyeFile(含"扫到的文件太少"自检),
// 不再写第二份遍历器。

// responseFuncNames 是扩展里「把 data 交给 c.JSON 下发」的那批函数名。
//
// 八份信封拷贝的名字全在这里。此外还收了裸 `.JSON(` 选择器调用:
// 第九份拷贝很可能是直接在 handler 里写 c.JSON(200, gin.H{...}) —— 那是
// 同一个缺陷的另一种写法,不该因为它没经过 respond 就在视野外。
var responseFuncNames = map[string]bool{
	"respond":   true,
	"respondOK": true,
	"ok":        true,
}

// nilSliceNamesInFunc 收集函数体里用 `var x []T` 声明(无初值)的切片名。
//
// 不含 []byte:见文件头"抓不到什么"的第四条。
func nilSliceNamesInFunc(body *ast.BlockStmt) map[string]token.Pos {
	out := map[string]token.Pos{}
	ast.Inspect(body, func(n ast.Node) bool {
		decl, ok := n.(*ast.DeclStmt)
		if !ok {
			return true
		}
		gen, ok := decl.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return true
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Values) > 0 || vs.Type == nil {
				continue // 有初值的 var 不是 nil 切片
			}
			arr, ok := vs.Type.(*ast.ArrayType)
			if !ok || arr.Len != nil {
				continue // 数组不是切片,数组永远非 nil
			}
			if elt, ok := arr.Elt.(*ast.Ident); ok && (elt.Name == "byte" || elt.Name == "uint8") {
				continue
			}
			for _, name := range vs.Names {
				if name.Name != "_" {
					out[name.Name] = name.Pos()
				}
			}
		}
		return true
	})
	return out
}

// isResponseCall 判断这个调用是不是"把 data 下发给前端"。
func isResponseCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return responseFuncNames[fn.Name]
	case *ast.SelectorExpr:
		return fn.Sel.Name == "JSON"
	default:
		return false
	}
}

// nilSliceInResponseOffenders 返回 "文件:行 变量名" 形式的违规点。
func nilSliceInResponseOffenders(file *ast.File, fset *token.FileSet) []string {
	var offenders []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		nilSlices := nilSliceNamesInFunc(fn.Body)
		if len(nilSlices) == 0 {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isResponseCall(call) {
				return true
			}
			for _, arg := range call.Args {
				ast.Inspect(arg, func(inner ast.Node) bool {
					// 嵌套调用的实参不算"下发":真正被序列化的是那个调用的
					// 返回值,而不是喂给它的切片。commission 的
					// respond(c, gin.H{"items": hydrateBalanceViews(ctx, rows)})
					// 正是这个形状 —— hydrate 返回的是 make 出来的新切片。
					// 不切断这条路径,这条锁会逼着后来人去放宽规则本身。
					if _, nested := inner.(*ast.CallExpr); nested {
						return false
					}
					id, ok := inner.(*ast.Ident)
					if !ok {
						return true
					}
					if _, bad := nilSlices[id.Name]; bad {
						offenders = append(offenders,
							at(fset, id.Pos())+" 变量 "+id.Name)
					}
					return true
				})
			}
			return true
		})
	}
	return offenders
}

// TestNoNilSliceReachesResponse:下发给前端的数组必须是显式初始化的切片。
//
// 把修复回滚(把任意一处 `x := make([]T, 0, n)` 改回 `var x []T`)
// 这条测试立刻变红 —— 实测过,这是它与"塞几行数据再断言 len"那类测试的区别:
// 后者在空库缺陷上永远绿,因为 nil 切片的 len 也是 0。
func TestNoNilSliceReachesResponse(t *testing.T) {
	var offenders []string
	forEachQianyeFile(t, func(file *ast.File, fset *token.FileSet) {
		offenders = append(offenders, nilSliceInResponseOffenders(file, fset)...)
	})
	assert.Empty(t, offenders,
		"这些 nil 切片会被序列化成 JSON null 而不是 [],前端对着 null 调 .find/.map 会整页白屏;"+
			"下发给前端的数组请改成 x := make([]T, 0, n)")
}

// TestNilSliceAnalyzerDetectsKnownShapes 是上面那条锁自己的回归测试。
//
// 必须有:分析器写错(比如只认 gin.H 字面量、或者漏了多名 var 声明)时,
// 全仓扫描照样返回空列表、照样全绿 —— 那正是本项目反复栽跟头的"假回归"形状。
func TestNilSliceAnalyzerDetectsKnownShapes(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			// 线上炸掉的那一处的原样形状。
			name: "gin.H 里的 nil 切片",
			src: `package p
func h(c *C) {
	type bucket struct{ S string }
	var rows []bucket
	_ = rows
	respondOK(c, gin.H{"buckets": rows})
}`,
			want: true,
		},
		{
			name: "一条 var 声明多个 nil 切片",
			src: `package p
func h(c *C) {
	var byRule, byModel []bucket
	_ = byRule
	respond(c, gin.H{"by_rule": byRule, "by_model": byModel})
}`,
			want: true,
		},
		{
			// availability 那种"响应体是结构体而不是 gin.H"的写法。
			name: "结构体字段里的 nil 切片",
			src: `package p
func h(c *C) {
	var rows []Bucket
	respond(c, matrixResponse{Cells: rows})
}`,
			want: true,
		},
		{
			name: "直接把 nil 切片当 data",
			src: `package p
func h(c *C) {
	var rows []Rule
	ok(c, rows)
}`,
			want: true,
		},
		{
			name: "绕过 respond 直接 c.JSON",
			src: `package p
func h(c *C) {
	var rows []Rule
	c.JSON(200, gin.H{"items": rows})
}`,
			want: true,
		},
		{
			name: "显式 make 的切片放行",
			src: `package p
func h(c *C) {
	rows := make([]Rule, 0, 20)
	respond(c, gin.H{"items": rows})
}`,
			want: false,
		},
		{
			name: "nil 切片只在内部用、不下发时放行",
			src: `package p
func h(c *C) {
	var rows []Rule
	out := map[int]Rule{}
	for _, r := range rows {
		out[r.Id] = r
	}
	respond(c, gin.H{"items": out})
}`,
			want: false,
		},
		{
			// 切片被交给别的函数加工时,下发的是那个函数的返回值。
			// commission 的余额列表就是这个形状。
			name: "作为嵌套调用的入参时放行",
			src: `package p
func h(c *C) {
	var rows []Balance
	respond(c, gin.H{"items": hydrateBalanceViews(ctx, rows)})
}`,
			want: false,
		},
		{
			// json.RawMessage 是 []byte 的别名形状:改成非 nil 反而会让
			// Marshal 报错,所以这条锁必须放它过去。
			name: "字节切片放行",
			src: `package p
func h(c *C) {
	var raw []byte
	respond(c, gin.H{"blob": raw})
}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "probe.go", tc.src, 0)
			require.NoError(t, err)
			got := nilSliceInResponseOffenders(file, fset)
			if tc.want {
				assert.NotEmpty(t, got, "分析器漏掉了一个已知的缺陷形状")
				return
			}
			assert.Empty(t, got, "分析器误报")
		})
	}
}
