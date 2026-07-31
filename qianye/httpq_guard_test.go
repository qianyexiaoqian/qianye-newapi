package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// httpq_guard_test.go —— 用 AST 保证「分页与查询参数解析」只有一份实现。
//
// # 为什么这组断言必须存在
//
// 本仓库反复出现的失败形状之一是「同一概念的第 N 份拷贝各自漂移」。
// 这套解析曾经有七份:controller、availability、violation、commission、
// grouppricing、transfer、withdraw。它们全部从第一份复制而来,然后停在了
// 各自被复制的那一刻 —— 后来给第一份加的整数上界、给第二份加的页码上界,
// 另外五份一份都没有跟上。
//
// 这不是靠"下次记得"能守住的:七份里没有一份是有人故意写错的,
// 每一份在被复制的当天都是对的。
//
// 更关键的是,普通单元测试抓不到第八份拷贝:新拷贝会带着自己的一份测试,
// 而且照样全绿 —— 它测的是它自己那份逻辑,不是全仓的一致性。
// 只有把"不存在第二份实现"本身变成断言,复制粘贴才会让测试变红。
//
// # 这里锁了四件事
//
//  1. 名字锁:历史上漂移过的那批函数名不允许在 httpq 之外再出现
//  2. offset 锁:每一个 .Offset(...) 的实参必须是 httpq.Offset(...)
//     —— 真正打给数据库的是这个乘积,而不是 page/size 两个中间变量
//  3. 解析锁:httpq 之外,同一个函数体里不许同时出现「从请求里取参」
//     与「strconv 的解析调用」
//  4. 切片锁:httpq 之外,切片表达式 x[lo:hi] 的边界不许来自乘法
//     —— 内存分页的 panic 来自 (page-1)*size 回绕成负数,而 .Offset()
//     那条锁只看得见走数据库的那一半
//
// # 这四条锁**抓不到**什么(请勿把它们当兜底网)
//
// 上一版这里写着「解析锁与名字无关,第八份拷贝叫什么都会被抓住」。
// 实测不成立:当时它只匹配选择器名 `Query`,`c.DefaultQuery` 写法三条锁全过。
// 一句测试没有兑现的承诺比没有注释更危险,所以这里逐条写清边界:
//
//   - 名字锁是**固定黑名单**。给新拷贝起一个表里没有的名字就能绕过。
//     它的职责只是"历史上那批名字不许回来",不是"识别所有分页函数"。
//   - 解析锁的粒度是**函数体**,判据是「取参调用」与「strconv 解析调用」
//     同时出现。把取参和解析拆到两个函数、或者自己手写十进制循环不调 strconv,
//     它都看不见。取参方法名见 ginRequestGetters(与 gin v1.9.1 的
//     *Context 逐个核对过),但**不校验接收者类型** —— 任何 `X.Query(...)`
//     都算,宁可误报也不放过。
//   - offset 锁只认选择器名 `Offset`,换个 ORM 或换个写法就在视野外。
//   - 切片锁只看**切片表达式** `x[lo:hi]`,不看单点下标 `x[i]`:
//     没有类型信息就分不出数组下标和 map 下标,而 map 下标在本仓遍地都是,
//     一起管会淹没在误报里。它的判据是"边界表达式里出现乘法",
//     把乘法挪进另一个函数(`names[offsetOf(p, s):]`)就绕过了。
//
// 也就是说:这四条锁覆盖的是**照着本仓既有写法自然写出的**第八份拷贝
// (取参 → strconv → 自己乘 → 切片 / Offset),不是一个对抗性的完备防线。

// sharedPkgDir 是唯一允许实现查询参数解析的目录(相对 qianye/)。
const sharedPkgDir = "httpq"

// forbiddenFuncNames 是历史上漂移过的那批函数名。
//
// 失败时不要把名字从这张表里删掉,也不要给函数改个名绕过去 ——
// 该做的是把实现挪进 qianye/httpq,然后在调用点转调它。
var forbiddenFuncNames = map[string]bool{
	"intQuery":       true,
	"queryInt":       true,
	"queryInt64":     true,
	"int64Query":     true,
	"queryIntParam":  true,
	"paginate":       true,
	"pagination":     true,
	"pageParams":     true,
	"pageParam":      true,
	"parsePage":      true,
	"parsePageParam": true,
}

// strconvParsers 是"把字符串变成数"的那批函数。
// Itoa/FormatInt 这类格式化函数刻意不在表里:它们不产生上界问题。
var strconvParsers = map[string]bool{
	"Atoi":       true,
	"ParseInt":   true,
	"ParseUint":  true,
	"ParseFloat": true,
}

// ginRequestGetters 是 gin *Context 上「从请求里取出原始字符串」的全部方法。
//
// 上一版这张表只有一个 "Query",于是 `strconv.Atoi(c.DefaultQuery("page","1"))`
// —— 也就是本仓 controller/channel.go 的既有写法 —— 从解析锁下面整个走过去了。
// 名单按 gin v1.9.1 的 *Context 方法列表逐个过了一遍:凡是返回请求侧字符串、
// 且调用方拿到后需要自己转成数字的,都在这里。
//
// ClientIP/RemoteIP/ContentType/FullPath 刻意不收:它们不是"参数",
// 收进来只会制造与分页无关的误报。
var ginRequestGetters = map[string]bool{
	"Param":            true,
	"Query":            true,
	"DefaultQuery":     true,
	"GetQuery":         true,
	"QueryArray":       true,
	"GetQueryArray":    true,
	"QueryMap":         true,
	"GetQueryMap":      true,
	"PostForm":         true,
	"DefaultPostForm":  true,
	"GetPostForm":      true,
	"PostFormArray":    true,
	"GetPostFormArray": true,
	"PostFormMap":      true,
	"GetPostFormMap":   true,
	"GetHeader":        true,
	"Cookie":           true,
}

// pathParamParseDebt 曾登记「自己解析 /:id 的第 N 份拷贝」这笔欠账。
//
// 四份(violation/grouppricing/transfer/withdraw)已全部迁到 httpq.PathInt64,
// **表刻意留空而不是删掉**:留着它,解析锁对 c.Param 就是零豁免的,
// 下一个想加豁免的人得先在这里写下自己的名字与理由;删掉它,
// 下一次有人碰上误报时更可能去放宽规则本身,那才是真正丢掉防线的走法。
//
// 校验口径是**子集**:名单里的函数消失(迁走)不会变红,名单外冒出新的会变红。
// key 用「文件路径#函数名」而不是行号 —— 行号会被同文件的任何其他改动推移。
var pathParamParseDebt = map[string]string{}

// TestNoPrivateQueryParamParserOutsideSharedPackage:名字锁。
//
// 这条只管「历史上那批名字不许回来」,不负责识别改了名的新拷贝 ——
// 那是解析锁和切片锁的职责。
func TestNoPrivateQueryParamParserOutsideSharedPackage(t *testing.T) {
	var offenders []string
	forEachQianyeFile(t, func(file *ast.File, fset *token.FileSet) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !forbiddenFuncNames[fn.Name.Name] {
				continue
			}
			offenders = append(offenders, at(fset, fn.Pos())+": func "+fn.Name.Name)
		}
	})

	assert.Empty(t, offenders,
		"以下位置又自己定义了一份查询参数解析/分页函数。这套逻辑必须只有 "+
			"qianye/httpq 一份实现 —— 曾经有过七份,而后加的整数上界与页码上界只有两份跟上了,"+
			"另外五份里 transfer 与 withdraw 是资金模块的用户端只读接口,"+
			"?p=184467440737095518 会让 (page-1)*size 溢出成负数直接喂进 Offset(): %v", offenders)
}

// TestEveryOffsetComesFromSharedHelper:offset 锁。
//
// page 与 size 被夹住并不等于安全 —— 真正打给数据库的是 (page-1)*size。
// 只要还有一个调用点自己写这段乘法,它就可能在拿到一个没夹过的 page 时溢出成负数:
// 轻则 SQL 报错 500,重则拿到非预期的结果集。这条断言让那段算术无处可写。
//
// 只管走数据库的那一半;内存里的 names[start:end] 由切片锁负责。
func TestEveryOffsetComesFromSharedHelper(t *testing.T) {
	var offenders []string
	forEachQianyeFile(t, func(file *ast.File, fset *token.FileSet) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Offset" || len(call.Args) != 1 {
				return true
			}
			// 允许的唯一形态:.Offset(httpq.Offset(page, size))
			if inner, ok := call.Args[0].(*ast.CallExpr); ok && isSelectorCall(inner, "httpq", "Offset") {
				return true
			}
			offenders = append(offenders,
				at(fset, call.Pos())+": Offset("+exprText(call.Args[0])+")")
			return true
		})
	})

	assert.Empty(t, offenders,
		"以下 Offset() 调用点自己算了 offset,而不是转调 httpq.Offset(page, size)。"+
			"真正打给数据库的是这个乘积,它必须只有一份带上界的实现: %v", offenders)
}

// TestNoStrconvOnRequestParamOutsideSharedPackage:解析锁。
//
// 判据是「同一个函数体里既有请求取参、又有 strconv 的解析调用」。
// strconv.Atoi 的上界是 MaxInt64,184467440737095518 能被它成功解析 ——
// 上界必须是解析的一部分,而那份带上界的解析在 httpq 里。
//
// 取参口是 ginRequestGetters 整张表,不是单个 "Query":上一版就是因为
// 只匹配 "Query",让 `strconv.Atoi(c.DefaultQuery("page","1"))` 完整通过。
// 边界见文件头「抓不到什么」一节。
func TestNoStrconvOnRequestParamOutsideSharedPackage(t *testing.T) {
	var offenders []string
	forEachQianyeFile(t, func(file *ast.File, fset *token.FileSet) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var parsed bool
			var getter string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strconv" && strconvParsers[sel.Sel.Name] {
					parsed = true
				}
				if ginRequestGetters[sel.Sel.Name] && getter == "" {
					getter = sel.Sel.Name
				}
				return true
			})
			if !parsed || getter == "" {
				continue
			}
			key := filepath.ToSlash(fset.Position(fn.Pos()).Filename) + "#" + fn.Name.Name
			if _, known := pathParamParseDebt[key]; known {
				continue
			}
			offenders = append(offenders,
				at(fset, fn.Pos())+": func "+fn.Name.Name+"(c."+getter+" + strconv)")
		}
	})

	assert.Empty(t, offenders,
		"以下函数自己把请求参数喂给了 strconv —— 请求参数的解析必须走 qianye/httpq"+
			"(它的上界是解析的一部分,而 strconv.Atoi 的上界是 MaxInt64):%v\n"+
			"路径 ID 用 httpq.PathInt64,查询参数用 httpq.Int / httpq.Int64 / httpq.Paginate。",
		offenders)
}

// TestNoHandRolledPageSliceOutsideSharedPackage:切片锁。
//
// offset 锁只看得见走数据库的那一半。内存分页的那一半长这样:
//
//	start := (page - 1) * pageSize
//	pageModels := names[start:end]
//
// ?page=184467440737095518 被 strconv.Atoi 成功解析后,(page-1)*50 回绕成
// -9223372036854775766,`start < len(names)` 对负数**成立**,于是
// names[负数:…] 直接 panic —— 一个登录用户可达的只读看板端点被打成 500。
// 这正是收敛前 availability 的真实状态,也是复核变异用过的那份代码。
//
// 判据:切片表达式的任一边界,顺着本函数内的赋值链回溯后含乘法。
// 内存分页只能来自 httpq.Slice(它返回的是切片值,不是切片表达式,天然不触发)。
func TestNoHandRolledPageSliceOutsideSharedPackage(t *testing.T) {
	var offenders []string
	forEachQianyeFile(t, func(file *ast.File, fset *token.FileSet) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			defs := localAssignments(fn.Body)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				slice, ok := n.(*ast.SliceExpr)
				if !ok {
					return true
				}
				for _, bound := range []ast.Expr{slice.Low, slice.High, slice.Max} {
					if bound == nil {
						continue
					}
					if !derivesFromMultiplication(bound, defs, map[string]bool{}) {
						continue
					}
					offenders = append(offenders, at(fset, slice.Pos())+": func "+
						fn.Name.Name+" —— "+exprText(slice.X)+"[…"+exprText(bound)+"…]")
				}
				return true
			})
		}
	})
	sort.Strings(offenders)

	assert.Empty(t, offenders,
		"以下切片表达式的边界是自己乘出来的 —— 页内切片必须走 httpq.Slice(items, page, size)。"+
			"(page-1)*size 回绕成负数后,`start >= len(items)` 这类保护对负数不成立,"+
			"names[负数:…] 直接 panic:%v", offenders)
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

// localAssignments 收集函数体内「标识符 → 它被赋过的表达式」。
//
// 一个标识符可能被赋值多次,因此值是切片:回溯时任何一次赋值命中都算命中,
// 宁可误报也不放过 —— 漏报的代价是一个可被查询参数打崩的线上端点。
func localAssignments(body *ast.BlockStmt) map[string][]ast.Expr {
	defs := map[string][]ast.Expr{}
	bind := func(lhs []ast.Expr, rhs []ast.Expr) {
		for i, l := range lhs {
			ident, ok := l.(*ast.Ident)
			if !ok || ident.Name == "_" {
				continue
			}
			switch {
			case len(rhs) == len(lhs):
				defs[ident.Name] = append(defs[ident.Name], rhs[i])
			case len(rhs) == 1:
				// a, b := f(...) —— 两个名字都挂到同一个右值上。
				defs[ident.Name] = append(defs[ident.Name], rhs[0])
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			bind(v.Lhs, v.Rhs)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, 0, len(v.Names))
			for _, name := range v.Names {
				lhs = append(lhs, name)
			}
			bind(lhs, v.Values)
		}
		return true
	})
	return defs
}

// derivesFromMultiplication 判断一个切片边界表达式是否(顺着本函数内的赋值链)
// 来自一次乘法。
//
// 只认乘法,不认加减:`len(s)-1`、`i+1` 是到处都在写的合法边界,
// 把它们收进来这条锁会淹死在误报里;而分页 offset 的形状里必然有一次乘法。
//
// 不下钻普通函数调用的实参:`foo(a*b)` 的返回值与 a*b 没有必然关系,
// 下钻只会制造误报。代价是把乘法藏进另一个函数就能绕过 —— 见文件头的边界说明。
func derivesFromMultiplication(e ast.Expr, defs map[string][]ast.Expr, seen map[string]bool) bool {
	switch v := e.(type) {
	case *ast.BinaryExpr:
		if v.Op == token.MUL {
			return true
		}
		return derivesFromMultiplication(v.X, defs, seen) ||
			derivesFromMultiplication(v.Y, defs, seen)
	case *ast.ParenExpr:
		return derivesFromMultiplication(v.X, defs, seen)
	case *ast.UnaryExpr:
		return derivesFromMultiplication(v.X, defs, seen)
	case *ast.Ident:
		if seen[v.Name] {
			return false
		}
		seen[v.Name] = true
		for _, rhs := range defs[v.Name] {
			if derivesFromMultiplication(rhs, defs, seen) {
				return true
			}
		}
		return false
	}
	return false
}

// forEachQianyeFile 遍历 qianye/ 下所有非测试 .go 文件,跳过共享包自身。
//
// 回调同时拿到 AST 与位置表,是为了让失败信息给得出"哪个文件第几行" ——
// 否则一条 %v 里全是函数名,看到了也没法直接跳过去改。
func forEachQianyeFile(t *testing.T, visit func(file *ast.File, fset *token.FileSet)) {
	t.Helper()
	fset := token.NewFileSet()
	var seen int
	require.NoError(t, filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == sharedPkgDir {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		seen++
		visit(file, fset)
		return nil
	}))
	// 遍历本身也要被断言:WalkDir 起点写错(或包被挪走)会让上面四条
	// 全部"通过",而它们一个字节都没读到 —— 正是本项目最常见的假回归形状。
	require.Greater(t, seen, 50, "扫到的 .go 文件太少,遍历起点可能不对")
}

// at 把位置渲染成 "modules/xxx/api.go:123",失败信息里可以直接点过去。
func at(fset *token.FileSet, pos token.Pos) string {
	return filepath.ToSlash(fset.Position(pos).Filename) + ":" +
		strconv.Itoa(fset.Position(pos).Line)
}

func isSelectorCall(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.BinaryExpr:
		return exprText(v.X) + " " + v.Op.String() + " " + exprText(v.Y)
	case *ast.ParenExpr:
		return "(" + exprText(v.X) + ")"
	case *ast.BasicLit:
		return v.Value
	case *ast.CallExpr:
		return exprText(v.Fun) + "(...)"
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	default:
		return "<expr>"
	}
}
