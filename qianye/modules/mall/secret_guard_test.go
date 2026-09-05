package mall

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

// secret_guard_test.go —— 用 AST 钉死"兑换码密文与收货地址密文只能在封装函数里出现"
// (照 lottery/secret_guard_test.go)。
//
// qy_ml_code_stock.code_cipher 存的是发给用户的实际第三方卡密,qy_ml_order 的
// address_cipher / contact_cipher 存的是收货地址与联系方式(PII)。泄漏的形状全都是
// "看起来无害的一行":管理端列表顺手整行返回、排障时加一句 %+v、审计快照直接塞整行。
// 这些都不会让任何普通测试变红,所以把"只有封装函数能碰这几列"本身变成断言。
//
// 它只看**字段名出现在哪个函数里**,不看那个函数有没有把值传出去;它的职责是防止
// 这几列扩散到第三个地方,不是证明封装函数本身正确。

// codeSecretFields 只允许在 sealCode / openCode 里出现。
var codeSecretFields = map[string]bool{
	"CodeCipher": true,
	"CodeNonce":  true,
	"KeyVersion": true,
}

// addressSecretFields 只允许在 sealAddress / openAddress 里出现。
var addressSecretFields = map[string]bool{
	"AddressCipher":     true,
	"AddressNonce":      true,
	"ContactCipher":     true,
	"ContactNonce":      true,
	"AddressKeyVersion": true,
}

// secretAllowedFuncs 是唯一允许触碰上面那些字段的函数。
//
// 数据库列名(address_cipher 等)另有一处豁免:保留期清理的置空更新必须用列名写 map,
// 它就在 pruneAddresses 里(reconcile.go),与"清空"这件事同一屏。
var secretAllowedFuncs = map[string]bool{
	"sealCode":       true,
	"openCode":       true,
	"sealAddress":    true,
	"openAddress":    true,
	"pruneAddresses": true,
}

func TestSecretFieldsStayInsideTheirWrappers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.Greater(t, len(files), 8, "扫到的文件太少,遍历八成写错了")

	fset := token.NewFileSet()
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		// model.go 是这几列的**声明处**,天然要提到它们。
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
			if secretAllowedFuncs[fn.Name.Name] {
				return true
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				// 唯一的豁免:把整行**作为实参**交给封装函数,整棵调用子树直接跳过。
				if call, ok := inner.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && secretAllowedFuncs[id.Name] {
						return false
					}
				}
				sel, ok := inner.(*ast.SelectorExpr)
				if !ok || (!codeSecretFields[sel.Sel.Name] && !addressSecretFields[sel.Sel.Name]) {
					return true
				}
				assert.Failf(t, "密文列泄漏到了封装函数之外",
					"%s 的 %s 里出现了 %s。兑换码三列只允许在 sealCode / openCode 里出现,"+
						"地址五列只允许在 sealAddress / openAddress(以及 pruneAddresses 的置空更新)里出现,"+
						"其余任何地方(列表 DTO、日志、%%+v、审计快照)都不行。",
					path, fn.Name.Name, sel.Sel.Name)
				return false
			})
			return true
		})
	}
	require.Greater(t, scanned, 8, "扫到的非测试文件太少,遍历八成写错了")
}

// 用户端与管理端的**每一份视图**都不许带密文列或明文字段名:orderView / adminOrderView
// 是列表接口的唯一出口,这里再从 JSON 键的角度钉一次。
func TestOrderViewsNeverCarrySecrets(t *testing.T) {
	o := &Order{OrderNo: "ML-x", Kind: KindPhysical, Status: StatusPaid}
	for name, view := range map[string]map[string]any{
		"user":  userOrderView(o),
		"admin": adminOrderView(o, "u"),
	} {
		for k := range view {
			lk := strings.ToLower(k)
			assert.Falsef(t, strings.Contains(lk, "cipher") || strings.Contains(lk, "nonce") ||
				lk == "address" || lk == "contact" || lk == "code",
				"%s 视图下发了 %s:码与地址只走各自写审计的揭示接口", name, k)
		}
	}
}
