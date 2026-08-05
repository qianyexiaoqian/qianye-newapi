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

// secret_guard_test.go —— 用 AST 钉死"文本奖的明文只能在两个函数里出现"。
//
// # 为什么必须是这种形状的测试
//
// Payout.Secret* 三列存的是中奖者手里那串兑换码。它与 Seed 同一档:
// **没被 SELECT 出来、没被拼进结构体的东西才泄不了。** 而泄漏的形状全都是
// "看起来无害的一行":
//
//   - 管理端列表顺手整行返回 model(本仓已经有过这种先例);
//   - 排障时给某个 handler 加一句 %+v;
//   - 审计快照图省事直接塞整行;
//   - 证据链里"稳妥起见"放一个哈希 —— 而 proof 是**匿名可访问**的,
//     act_no / tier / 域前缀全都印在同一份文件里,一个 8 位兑换码约 40 位熵,
//     离线爆破是笔记本几分钟的事。
//
// 这些都不会让任何普通测试变红:接口照常 200,业务照常跑。所以把
// "只有 sealPrizeSecret / openPrizeSecret 能碰这三列"本身变成断言。
//
// # 它抓不到什么
//
// 它只看**字段名出现在哪个函数里**,不看那个函数有没有把值传出去。
// 一个在 openPrizeSecret 里 SysError 打印明文的写法能骗过它。
// 它的职责是防止这三列扩散到第三个地方,不是证明那两个函数本身正确。

// prizeSecretFields 是只允许在两个封装函数里出现的字段名。
var prizeSecretFields = map[string]bool{
	"SecretNonce":      true,
	"SecretCipher":     true,
	"SecretKeyVersion": true,
}

// prizeSecretAllowedFuncs 是唯一允许触碰上面那三个字段的函数。
//
// 数据库列名(secret_nonce 等)另有一处豁免:fulfill 的那条 CAS 更新必须用
// 列名写 map,它就在 handleFulfillPrize 里,与封装函数同一个文件、同一屏。
var prizeSecretAllowedFuncs = map[string]bool{
	"sealPrizeSecret": true,
	"openPrizeSecret": true,
	// archivePrizeSecret 把一份即将被顶替的密文整体搬进只增不改的履历。
	// 它必须读那三列(要搬的就是它们),而"搬进另一张同样 json:\"-\" 的表"
	// 与封装函数是同一档动作:明文既不出包、也不进任何下发路径。
	"archivePrizeSecret": true,
}

func TestPrizeSecretFieldsStayInsideTheirTwoWrappers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.Greater(t, len(files), 10, "扫到的文件太少,遍历八成写错了")

	fset := token.NewFileSet()
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		// model.go 是这三列的**声明处**,天然要提到它们。
		if path == "model.go" {
			continue
		}
		scanned++

		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoErrorf(t, err, "解析 %s 失败", path)

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			if prizeSecretAllowedFuncs[fn.Name.Name] {
				return true
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				// 唯一的豁免:把整行 Payout 上的这三列**作为实参**交给封装函数。
				// 整棵调用子树直接跳过 —— 判据简单到没人能误读,而这正是
				// 一条护栏该有的样子。
				if call, ok := inner.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && prizeSecretAllowedFuncs[id.Name] {
						return false
					}
				}
				sel, ok := inner.(*ast.SelectorExpr)
				if !ok || !prizeSecretFields[sel.Sel.Name] {
					return true
				}
				assert.Failf(t, "文本奖的明文列泄漏到了封装函数之外",
					"%s 的 %s 里出现了 %s。"+
						"这三列只允许在 sealPrizeSecret / openPrizeSecret 里出现,"+
						"以及由这两个函数的调用点作为实参传入 —— "+
						"其余任何地方(列表 DTO、日志、%%+v、审计快照、证据链)都不行。",
					path, fn.Name.Name, sel.Sel.Name)
				return false
			})
			return true
		})
	}
	require.Greater(t, scanned, 10, "扫到的非测试文件太少,遍历八成写错了")
}

// 兑换码**一个比特都不能进匿名证据链**,连哈希都不行。
//
// proof 端点没有认证(见 api_proof.go 顶部),而兑换码的熵撑不住一次离线爆破。
// 这条断言盯的是 api_proof.go 里的字段名与哈希调用。
func TestProofNeverCarriesPrizeSecret(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "api_proof.go", nil, 0)
	require.NoError(t, err)

	banned := map[string]bool{
		"Secret": true, "SecretNonce": true, "SecretCipher": true,
		"SecretKeyVersion": true, "FulfillNote": true,
		"sealPrizeSecret": true, "openPrizeSecret": true, "maskSecret": true,
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			assert.Falsef(t, banned[v.Sel.Name],
				"api_proof.go 引用了 %s —— 证据链是匿名可访问的,"+
					"兑换码与履行备注一个字节都不能进去", v.Sel.Name)
		case *ast.Ident:
			assert.Falsef(t, banned[v.Name],
				"api_proof.go 引用了 %s —— 证据链是匿名可访问的,"+
					"兑换码与履行备注一个字节都不能进去", v.Name)
		}
		return true
	})

	// 证据链里关于文本奖能公开的只有一个布尔:有没有这件事,不是这件事是什么。
	src := readFileForGuard(t, "api_proof.go")
	assert.Contains(t, src, "Fulfilled bool",
		"文本奖在证据链里只应暴露一个 fulfilled 布尔")
}

func readFileForGuard(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	require.NoError(t, err)
	var sb strings.Builder
	ast.Inspect(f, func(n ast.Node) bool {
		if fld, ok := n.(*ast.Field); ok && len(fld.Names) == 1 {
			if id, ok := fld.Type.(*ast.Ident); ok {
				sb.WriteString(fld.Names[0].Name + " " + id.Name + "\n")
			}
		}
		return true
	})
	return sb.String()
}
