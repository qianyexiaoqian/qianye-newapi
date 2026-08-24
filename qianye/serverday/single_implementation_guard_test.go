package serverday

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// single_implementation_guard_test.go —— 「本地自然日只能有一份实现」。
//
// # 为什么需要这条
//
// 算本地午夜看上去是一行代码,于是它天然会被复制:
//
//	time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
//
// 而那一行在夏令时跳变日会给出**别的日子**的瞬间(America/Sao_Paulo
// 2018-11-04:本地午夜不存在,Go 归一化到前一天 23:00)。全时区全量实测
// (598 个时区 × 1990-2040 每一天,共 11 139 544 个样本):这一行与真正的
// 日界在 **853** 个 (时区, 日期) 组合上不同,差的那一小时属于前一天。
// 再配上 `start.AddDate(0, 0, 1)` 取次日,还能开出 end == start 的空窗口。
//
// 两处都不会报错。所以这条守卫盯的不是某一次错误,是**复制这件事本身**。
//
// # 为什么是 AST 而不是逐行字符串扫描
//
// 第一版是逐行扫描,要求 `time.Date(` 与 `0, 0, 0, 0,` 出现在**同一行**。
// 一个 gofmt 合法的多行写法就能整条绕过(实测 SURVIVED,`gofmt -l` 与
// `gofmt -d` 都干净):
//
//	return time.Date(
//		t.Year(), t.Month(), t.Day(),
//		0, 0, 0, 0,
//		time.Local,
//	).Unix()
//
// 而它与被守的那一行是**同一个夏令时缺陷**。判据换成 AST 之后,换行、注释、
// 中间插空格都不再影响判定 —— 判的是那个调用本身:`time.Date` 的第 4~7 个
// 实参字面量全是 0,且最后一个实参不是 time.UTC。
//
// time.UTC 那一支是纯日历算术(本包用它把「年月日」规整成合法日期),
// 不涉及任何偏移,不在判据里。测试文件同样不在判据里:那里构造某个具体时区
// 的具体时刻是本来就该做的事。

// knownSecondImplementations 是**已知的、尚未迁移**的第二份实现。
//
// # 键是"文件:函数",不是文件
//
// 第一版按**文件**豁免,于是往那个文件里再抄一份**全新的**手写午夜同样不会红
// (实测 SURVIVED)—— 与它自己写下的「多一处少一处都会让这条守卫变红」正相反,
// 那个文件因此成了一张可以无限追加的白名单。按位置豁免之后,
// 「历史遗留的那一处」与「今天新抄的那一处」重新分得开。
//
// 反向同样要守:登记项对应的那处违规必须**真的还在**。第一版的反向检查只做
// os.Stat 判文件在不在,于是那一处迁走之后登记会静默退化成一张永久的整文件
// 空白豁免(实测 SURVIVED)。现在改成"清单里的每一条都必须真的命中一次"。
//
// 迁移一处就从这里删一行。清单空了之后请连同这个变量一起删掉,
// 别把它留成长期豁免。
var knownSecondImplementations = map[string]string{
	"modules/lottery/spend.go:dayRange": "抽奖消费日桶 dayRange:同样是 time.Date(..., time.Local) + " +
		"AddDate(0,0,1),两个夏令时缺陷俱全。它按 YYYYMMDD 取区间而不是按时刻," +
		"迁移需要本包再导出一个按日历日期取区间的入口;本轮任务范围之外,未动。",
}

func TestLocalMidnightHasOneImplementation(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	offenders := make([]string, 0, 1)
	hit := map[string]bool{}
	scanned := 0

	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "serverday/") {
			return nil
		}
		scanned++

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}

		// 逐个顶层函数走,这样违规点能报出"文件:函数",而豁免也按这个粒度登记。
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			site := rel + ":" + fn.Name.Name
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isTimeDateCall(call) || !isLocalMidnightArgs(call.Args) {
					return true
				}
				if _, known := knownSecondImplementations[site]; known {
					hit[site] = true
					return true
				}
				offenders = append(offenders,
					site+" (line "+strconv.Itoa(fset.Position(call.Pos()).Line)+")")
				return true
			})
		}
		return nil
	})
	require.NoError(t, err)

	// 自检:遍历器被改坏(跳过了半棵树)时这条先红,而不是让下面那条静默全绿。
	assert.Greater(t, scanned, 100,
		"扫到的 Go 文件太少,遍历器八成被改坏了 —— 那样下面的断言就是假绿")

	assert.Empty(t, offenders,
		"以下位置自己算了一遍本地午夜。请改用 qianye/serverday —— "+
			"手写的那一行在夏令时跳变日会落到前一天,853 个 (时区, 日期) 组合实测不同,"+
			"而两边都不会报错:%v", offenders)

	// 反向:清单里的每一条都必须真的命中一次。命中不了说明那一处已经迁走
	// (或者改名了),而留着这条登记会让同名位置上新写的那一处被一起放行。
	for site, why := range knownSecondImplementations {
		assert.True(t, hit[site],
			"%s 已经不再有手写的本地午夜了,但仍登记在 knownSecondImplementations 里"+
				"(理由:%s)。请删掉这一行 —— 留着它等于给这个位置开一张永久豁免", site, why)
	}
}

// isTimeDateCall 判断这次调用是不是 time.Date(...)。
func isTimeDateCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Date" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}

// isLocalMidnightArgs 判断实参是不是「当天 0 点、且不是 UTC」。
//
// time.Date 的签名是 (year, month, day, hour, min, sec, nsec, loc):
// 第 4~7 个实参(下标 3..6)全是字面量 0 就是午夜;最后一个是 time.UTC 时
// 属于纯日历算术,不在判据里。
func isLocalMidnightArgs(args []ast.Expr) bool {
	if len(args) != 8 {
		return false
	}
	for _, arg := range args[3:7] {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.INT || lit.Value != "0" {
			return false
		}
	}
	if sel, ok := args[7].(*ast.SelectorExpr); ok {
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && sel.Sel.Name == "UTC" {
			return false
		}
	}
	return true
}
