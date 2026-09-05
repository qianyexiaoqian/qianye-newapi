package lottery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seed_guard_test.go —— 用 AST 钉死"种子只在三个地方被触碰"。
//
// # 为什么必须是这种形状的测试
//
// qy_lot_seed.seed 是整套承诺-揭示协议里唯一在揭示前保密的分量。泄漏一次就永久
// 毁掉那一场的公正性 —— 而泄漏的形状全都是"看起来无害的一行"(整行返回 model、
// 排障时的一句 %+v、审计快照塞整行),没有任何普通测试会因此变红。model.go 的
// 硬规则此前写着"由 seed_guard_test.go 守住",而那个文件并不存在;转盘把种子的
// 读点从冷路径扩到了**每一次转动**(loadSeedForSpin,由普通用户触发),暴露面
// 比现状大,正是把这条护栏真正立起来的时候(decisions.md D-13 的代价那一条)。
//
// # 它守住的三条
//
//  1. 元素非空的 Seed{...} 复合字面量只允许出现在 handleCreateActivity —— 种子与
//     两个盐只在创建那一刻生成,没有第二个写入点。零元素的 &Seed{} 作 Model /
//     AutoMigrate / 删除清单放行。
//  2. `.Seed` 选择器(字段名纯名字匹配)只允许出现在 loadSeedForReveal 与
//     loadSeedForSpin 两个读点里。api_proof.go 的文档字段因此叫 RevealedSeed
//     (JSON 键仍是 seed):纯名字匹配比 go/types 便宜,而且没有误报的第三种形状。
//  3. loadSeedForSpin 的调用者只能是转动事务函数 spinTx,而且在 wheel.go 里
//     种子只以 `seedHex` 这一个名字出现两次:从 loadSeedForSpin 接过来、交给
//     WheelTicket —— 中间不许有任何别的用法(日志、快照、回执)。
//
// # 它抓不到什么
//
// 它只看**名字与位置**,不看值有没有被传出去:在 loadSeedForSpin 里 SysError 打印
// 种子能骗过它。它的职责是防止种子扩散到第四个地方,不是证明那三个函数本身正确。

// seedTouchers 是允许出现 `.Seed` 选择器的两个读点。
var seedTouchers = map[string]bool{
	"loadSeedForReveal": true,
	"loadSeedForSpin":   true,
}

// parseNonTestFiles 解析包内全部非测试文件。
func parseNonTestFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.Greater(t, len(files), 10, "扫到的文件太少,遍历八成写错了")
	fset := token.NewFileSet()
	out := make(map[string]*ast.File, len(files))
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoErrorf(t, err, "解析 %s 失败", path)
		out[path] = f
	}
	require.Greater(t, len(out), 10, "扫到的非测试文件太少,遍历八成写错了")
	return out
}

// eachFunc 把文件里每个顶层函数的名字与函数体配成对,供"这个节点落在哪个
// 函数里"的判定。方法与普通函数同等对待(接收者名不参与)。
func eachFunc(f *ast.File, visit func(name string, body *ast.BlockStmt)) {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		visit(fn.Name.Name, fn.Body)
	}
}

// isSeedType 识别 Seed / *Seed / lottery.Seed 这几种写法的复合字面量类型。
func isSeedType(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == "Seed"
	case *ast.StarExpr:
		return isSeedType(v.X)
	case *ast.SelectorExpr:
		return v.Sel.Name == "Seed"
	}
	return false
}

func TestSeedIsOnlyConstructedAtCreation(t *testing.T) {
	for path, f := range parseNonTestFiles(t) {
		eachFunc(f, func(name string, body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isSeedType(lit.Type) || len(lit.Elts) == 0 {
					return true
				}
				assert.Equalf(t, "handleCreateActivity", name,
					"%s 的 %s 里构造了一个带字段的 Seed{...}。种子与两个盐只在创建那一刻由 "+
						"handleCreateActivity 生成,任何第二个写入点都是一条能覆盖承诺随机源的路。", path, name)
				return true
			})
		})
	}
}

func TestSeedFieldIsReadOnlyByTheTwoLoaders(t *testing.T) {
	found := map[string]bool{}
	for path, f := range parseNonTestFiles(t) {
		eachFunc(f, func(name string, body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Seed" {
					return true
				}
				found[name] = true
				assert.Truef(t, seedTouchers[name],
					"%s 的 %s 里出现了 .Seed 选择器。种子只允许在 loadSeedForReveal(冷路径)与 "+
						"loadSeedForSpin(转动事务)里被读出来;别处一律用 loadSalts 那个只取盐的投影。"+
						"证据链文档的 Go 字段叫 RevealedSeed,JSON 键才是 seed。", path, name)
				return true
			})
		})
	}
	// 反向:两个读点都必须真的还在读它。留一行没有对应实现的允许项,等于把读点
	// 改名之后这条护栏还照绿。
	for name := range seedTouchers {
		assert.Truef(t, found[name], "%s 已经不读 Seed 字段了 —— 读点改名了,还是这一行该清掉?", name)
	}
}

func TestSeedForSpinOnlyFeedsTheWheelTicketInsideSpinTx(t *testing.T) {
	files := parseNonTestFiles(t)

	// ③-a:loadSeedForSpin 的调用者只能是 spinTx,而且只有一处。
	callers := map[string]int{}
	for _, f := range files {
		eachFunc(f, func(name string, body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "loadSeedForSpin" {
					callers[name]++
				}
				return true
			})
		})
	}
	assert.Equal(t, map[string]int{"spinTx": 1}, callers,
		"loadSeedForSpin 是唯一的热路径种子读点,只能由转动事务函数 spinTx 调用一次")

	// ③-b:在 wheel.go 里,种子以 `seedHex` 这一个名字出现,且恰好两次 —— 一次是
	// 从 loadSeedForSpin 接过来(赋值左侧),一次是作为 WheelTicket 的第一个实参。
	// `seed` 这个裸名字在整个文件里一次都不许出现(它是最容易被顺手打进日志的那个)。
	wheel, ok := files["wheel.go"]
	require.True(t, ok, "转盘的实现应在 wheel.go")
	var seedIdents, seedHexIdents, fedToTicket int
	insideSpinTx := 0
	eachFunc(wheel, func(name string, body *ast.BlockStmt) {
		ast.Inspect(body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.Ident:
				switch v.Name {
				case "seed":
					seedIdents++
				case "seedHex":
					seedHexIdents++
					if name == "spinTx" {
						insideSpinTx++
					}
				}
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "WheelTicket" && len(v.Args) > 0 {
					if arg, ok := v.Args[0].(*ast.Ident); ok && arg.Name == "seedHex" {
						fedToTicket++
					}
				}
			}
			return true
		})
	})
	assert.Zero(t, seedIdents, "wheel.go 里不许出现裸的 seed 标识符 —— 那是最容易被顺手打进日志的那个名字")
	assert.Equal(t, 2, seedHexIdents, "wheel.go 里 seedHex 必须恰好出现两次:接过来、交给 WheelTicket")
	assert.Equal(t, 2, insideSpinTx, "seedHex 只能出现在 spinTx 里")
	assert.Equal(t, 1, fedToTicket, "seedHex 的唯一用途是作为 WheelTicket 的密钥实参")
}
