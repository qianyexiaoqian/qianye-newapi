package transfer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contacts_isolation_test.go —— 用 AST **双向**钉死裁决 1 的那条硬约束。
//
// # 约束原文
//
//	「联系人只是方便快速填写表,不是因为是联系人就可以绕过支付密码的验证。」
//	「实现上禁止出现任何形如 `if isContact { skipPayPassword }` 的分支。」
//
// # 为什么必须是结构断言,而不是只写一条注释或一条行为用例
//
// 行为用例("对联系人发起划转不带支付密码 → 必须被拒")是必要的,但它只覆盖
// 当前这一条豁免形状。真正会发生的事是:半年后有人为了"体验优化"在
// service.go 里加一句 `if isSavedContact(from, to) { return nil }`,
// 而所有既有用例都不会碰到那条分支 —— 它们构造的收款人根本不在联系人簿里。
// 那样的缺陷只会在生产上被人发现。
//
// 所以这里锁的是**更强的性质**:动钱路径与联系人代码之间没有任何符号级联系。
// 这条性质与"用哪种豁免写法"无关,任何形式的耦合都会让它变红。
//
// # 两个方向都要锁
//
//	方向一(主要):动钱/风控/验密路径的文件里不许出现 Contact 相关标识符。
//	              挡的是 `if isContact { skipPayPassword }`。
//	方向二(反向):联系人文件里不许出现动钱/验密相关标识符。
//	              挡的是"顺手在添加联系人时把钱转了"或"联系人接口自己去
//	              读一遍支付密码状态"——后者会把验密点从一处变成两处,
//	              而裁决 1 明确要求验密点**只有一处**。
//
// # 这条锁抓不到什么(别当兜底网)
//
//   - 它按**标识符名字**匹配,把符号改名成 `buddy` / `favourite` 就绕过了。
//     它挡的是"照着既有命名自然写出来的耦合",不是对抗性的完备防线。
//   - 它不看注释:动钱路径的注释里写"联系人不构成豁免"是**应该**的。
//   - 跨包耦合(比如别的模块 import transfer 之后自己拼)不在视野内。

// contactAwareFiles 是**唯一**允许出现 Contact 相关标识符的非测试文件。
//
// 这张表只允许因为"联系人功能自身被拆成更多文件"而变长,绝不允许因为
// "某个动钱路径的文件报错了所以加进来"而变长 —— 那正好是这条锁要挡的事。
var contactAwareFiles = map[string]string{
	"contacts.go":     "联系人簿本体",
	"api_contacts.go": "联系人簿的用户端接口",
	"module.go":       "模块注册:AutoMigrate 的表清单与四条路由",
}

// fundPathSymbols 是联系人文件里不许出现的标识符片段(小写比较)。
//
// 取的是本包动钱、风控与验密路径上真实存在的符号名:
//   - twophase / applyquota:跨库资金主状态机与主库扣款
//   - reserverisk / settledetail:风控预占与结算
//   - validatecreate / acceptedrequest / computefee:受理与计费
//   - paypwd / paypassword:支付密码策略(settings.go 的 PayPasswordPolicy /
//     PayPwdMaxAttempts / PayPwdLockMinutes)
var fundPathSymbols = []string{
	"twophase",
	"applyquota",
	"reserverisk",
	"settledetail",
	"validatecreate",
	"acceptedrequest",
	"computefee",
	"paypwd",
	"paypassword",
}

// TestContactSymbolsNeverLeakIntoFundPath 是方向一:动钱路径不许认识"联系人"。
func TestContactSymbolsNeverLeakIntoFundPath(t *testing.T) {
	var offenders []string
	scanned := forEachTransferSourceFile(t, func(name string, file *ast.File, fset *token.FileSet) {
		if _, ok := contactAwareFiles[name]; ok {
			return
		}
		for _, hit := range symbolHits(file, fset, []string{"contact"}) {
			offenders = append(offenders, name+" "+hit)
		}
	})
	require.GreaterOrEqual(t, len(scanned), 10,
		"只扫到 %d 个文件,遍历大概率写坏了 —— 一条永远为真的断言比没有断言更危险", len(scanned))

	assert.Empty(t, offenders,
		"裁决 1:联系人只是把收款人字段填好,不是信任凭据。动钱/风控/验密路径的文件里"+
			"出现联系人标识符,意味着某处判定开始区分\"是不是联系人\" —— 而那正是"+
			"`if isContact { skipPayPassword }` 的形状。请把这段逻辑挪回 contacts.go,"+
			"或者删掉它: %v", offenders)
}

// TestContactCodeNeverTouchesFundPath 是方向二:联系人代码不许认识"动钱"。
func TestContactCodeNeverTouchesFundPath(t *testing.T) {
	var offenders []string
	forEachTransferSourceFile(t, func(name string, file *ast.File, fset *token.FileSet) {
		// module.go 是模块注册文件,它当然要认识动钱路由与 twophase 补偿回调。
		if name != "contacts.go" && name != "api_contacts.go" {
			return
		}
		for _, hit := range symbolHits(file, fset, fundPathSymbols) {
			offenders = append(offenders, name+" "+hit)
		}
	})

	assert.Empty(t, offenders,
		"联系人接口不得触碰动钱路径,也不得自己去读支付密码状态:裁决 1 要求验密点"+
			"**只有一处**(划转的执行入口)。多一处就多一份会漂移的口径: %v", offenders)
}

// TestContactAwareFilesAllExist 是白名单自检。
//
// 键写错、或者文件被改名之后忘了改这里,那条豁免就会静默失配 ——
// 失配的方向恰好是"看起来还在管着,其实什么都没管"。
func TestContactAwareFilesAllExist(t *testing.T) {
	for name, why := range contactAwareFiles {
		_, err := os.Stat(name)
		assert.NoError(t, err, "白名单条目 %q(%s)对应的文件不存在:"+
			"要么名字写错了,要么文件已改名 —— 两种情况下这条豁免都已失效", name, why)
	}
}

// symbolHits 返回文件里命中任一片段的标识符与字符串字面量位置。
//
// 同时看字符串字面量,是因为耦合也可以是隐式的:
// `c.GetBool("is_contact")` 里一个 Contact 标识符都没有。
// 注释刻意不看:动钱路径**应该**有注释写清"联系人不构成豁免"。
func symbolHits(file *ast.File, fset *token.FileSet, needles []string) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		var text, kind string
		switch v := n.(type) {
		case *ast.Ident:
			text, kind = v.Name, "标识符"
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return true
			}
			text, kind = v.Value, "字符串"
		default:
			return true
		}
		lower := strings.ToLower(text)
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				out = append(out, fset.Position(n.Pos()).String()+" "+kind+" "+text)
				break
			}
		}
		return true
	})
	sort.Strings(out)
	return out
}

// forEachTransferSourceFile 遍历本包的全部非测试 .go 文件,返回扫到的文件名。
func forEachTransferSourceFile(t *testing.T, visit func(name string, file *ast.File, fset *token.FileSet)) []string {
	t.Helper()
	entries, err := filepath.Glob("*.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	var scanned []string
	for _, path := range entries {
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, err, "解析 %s 失败", name)
		scanned = append(scanned, name)
		visit(name, f, fset)
	}
	return scanned
}
