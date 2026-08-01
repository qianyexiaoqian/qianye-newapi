package paypass

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// no_exemption_test.go —— 把裁决 1 的那句话钉成机器可校验的断言。
//
//	「联系人只是方便快速填写表,不是因为是联系人就可以绕过支付密码的验证。」
//
// 这里有两条互补的锁,缺一不可:
//
//  1. **行为锁**:对一个被标成"联系人"的收款人发起划转、不带支付密码,必须被拒。
//     它守的是 Require 本身 —— 只要有人在 gate.go 里加一句"是联系人就放行",
//     这条立刻变红。
//  2. **结构锁**:全仓禁掉 `if <含 contact 的条件> { <提到支付密码的分支> }` 这个形状。
//     它守的是**调用点** —— 集成者完全可以不碰 paypass 包,只在 transfer 的
//     handler 里包一个 `if !isContact { paypass.Require(...) }`,行为锁在
//     paypass 包内是看不见那一行的。
//
// 只有行为锁 = 调用点可以绕过;只有结构锁 = 换个不含 "contact" 的变量名就绕过。
// 两条一起,才覆盖得住"照着本仓既有写法自然写出来"的那种豁免。

// ─────────────────────────── 行为锁 ───────────────────────────

// contactMarkers 枚举"这个收款人是联系人"在 gin 上下文里可能长的样子。
//
// 逐一试过去而不是只试一种:集成者会用哪个键名不由本模块决定,
// 而这条断言要保证的是**无论哪种写法,结论都一样**。
var contactMarkers = []string{
	"qy_recipient_is_contact", "is_contact", "isContact", "contact", "from_contact",
}

// 对联系人发起划转、不带支付密码,必须被拒。
//
// 回滚验证的靶子:在 Require 里加一句
// `if c.GetBool("qy_recipient_is_contact") { return true }`,这条必须变红。
func TestContactIsNotAnExemption(t *testing.T) {
	gdb := newTestDB(t)
	gin.SetMode(gin.TestMode)
	const userId = 7200
	setPassword(t, gdb, userId, goodPassword)

	for _, marker := range contactMarkers {
		t.Run(marker, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost,
				"/api/qy/transfer?from_contact=1&contact_id=42", nil)
			c.Set("id", userId)
			// 把"是联系人"这件事用尽可能多的方式喊出来。
			c.Set(marker, true)
			c.Set("contact_id", 42)

			assert.False(t, Require(c, userId, ""),
				"收款人是联系人时未带支付密码竟然放行了 —— 联系人只负责填表,不构成任何豁免")
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Contains(t, rec.Body.String(), errPayPwdRequired.Code)
		})
	}
}

// 联系人 + 错误密码同样必须被拒,并且照常计入错误次数。
//
// 单测"空密码被拒"是不够的:一个"联系人跳过校验但仍然要求字段非空"的实现
// 会让上面那条通过,而它其实什么都没验。
func TestContactWithWrongPasswordStillCounts(t *testing.T) {
	gdb := newTestDB(t)
	gin.SetMode(gin.TestMode)
	const userId = 7210
	setPassword(t, gdb, userId, goodPassword)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/qy/transfer?from_contact=1", nil)
	c.Set("id", userId)
	c.Set("qy_recipient_is_contact", true)

	assert.False(t, Require(c, userId, "not-the-password"))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), errPayPwdWrong.Code)
	assert.Equal(t, 1, rowOf(t, gdb, userId).FailCount,
		"联系人路径没有计入错误次数 —— 那等于给了一条不受锁定策略约束的试密通道")
}

// 正确密码在联系人路径上照常放行:这条防的是"为了让上面两条变绿而把闸门焊死"。
func TestContactWithCorrectPasswordPasses(t *testing.T) {
	gdb := newTestDB(t)
	gin.SetMode(gin.TestMode)
	const userId = 7220
	setPassword(t, gdb, userId, goodPassword)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/qy/transfer", nil)
	c.Set("id", userId)
	c.Set("qy_recipient_is_contact", true)

	assert.True(t, Require(c, userId, goodPassword))
	assert.False(t, c.IsAborted())
}

// ─────────────────────────── 结构锁 ───────────────────────────

var (
	// contactIdentRe 匹配"联系人"这个概念在标识符里的样子:isContact、
	// contactId、Contacts、recipient.IsContact 的选择器名……
	contactIdentRe = regexp.MustCompile(`(?i)contact`)
	// payPwdIdentRe 匹配"支付密码"这个概念在标识符里的样子。
	payPwdIdentRe = regexp.MustCompile(`(?i)(paypass|paypwd|pay_?password)`)
)

// 全仓禁掉 `if <条件里有 contact> { <分支里提到支付密码> }`。
//
// 两个方向都会被抓到,因为它们是同一个缺陷:
//
//	if isContact  { skipPayPassword() }       // 是联系人就跳过
//	if !isContact { paypass.Require(...) }    // 不是联系人才验
//
// 判据刻意不区分正负条件:一旦"是不是联系人"与"要不要验支付密码"出现在同一个
// if 上,这个实现就已经把两件无关的事绑在一起了,无论哪个方向都要人来看一眼。
func TestNoContactBasedPayPasswordBranchInRepo(t *testing.T) {
	var offenders []string
	forEachQianyeSource(t, func(file *ast.File, fset *token.FileSet) {
		for _, bad := range contactGatedPayPasswordBranches(file) {
			offenders = append(offenders,
				filepath.ToSlash(fset.Position(bad).Filename)+":"+
					strconv.Itoa(fset.Position(bad).Line))
		}
	})

	assert.Empty(t, offenders,
		"以下位置把「是不是联系人」和「要不要验支付密码」写进了同一个 if。"+
			"裁决 1:联系人只是方便快速填写表,不构成任何豁免 —— 验密点只有划转执行入口一处,"+
			"且不接受任何按收款人身份分流的条件: %v", offenders)
}

// contactGatedPayPasswordBranches 找出该文件里所有"联系人条件 + 支付密码分支"的 if。
func contactGatedPayPasswordBranches(root ast.Node) []token.Pos {
	var out []token.Pos
	ast.Inspect(root, func(n ast.Node) bool {
		stmt, ok := n.(*ast.IfStmt)
		if !ok || stmt.Cond == nil {
			return true
		}
		if !mentionsIdent(stmt.Cond, contactIdentRe) {
			return true
		}
		if mentionsIdent(stmt.Body, payPwdIdentRe) ||
			(stmt.Else != nil && mentionsIdent(stmt.Else, payPwdIdentRe)) {
			out = append(out, stmt.Pos())
		}
		return true
	})
	return out
}

// mentionsIdent 判断子树里是否出现了匹配 re 的标识符。
// 只看标识符(含选择器名),不看注释与字符串字面量 —— 本文件自己的注释里
// 「联系人」满天飞,把注释算进来这条锁第一个就抓自己。
func mentionsIdent(node ast.Node, re *regexp.Regexp) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && re.MatchString(id.Name) {
			found = true
		}
		return !found
	})
	return found
}

// 分析器本身必须真的认得出这些形状。
//
// 没有它,分析器写坏(正则打错、Inspect 起点写错)会让上面那条断言
// "全绿但一个字节没读" —— 本仓最常见的假回归形状。
// 用例里既有该报的,也有**不该报**的:只测阳性会漏掉"什么都报"这种写法。
func TestContactBranchAnalyzerDetectsKnownShapes(t *testing.T) {
	const src = `package p

func skipWhenContact(c *gin.Context, isContact bool) {
	if isContact {
		return
	}
	paypass.Require(c, 1, "")
}

func inlineSkip(isContact bool) {
	if isContact {
		skipPayPassword()
	}
}

func negatedForm(recipient rec) {
	if !recipient.IsContact {
		paypass.Require(nil, 1, "")
	}
}

func elseBranchForm(isContact bool) {
	if isContact {
		noop()
	} else {
		payPwdVerify()
	}
}

func contactWithoutPayPassword(isContact bool) {
	if isContact {
		fillRecipientField()
	}
}

func payPasswordWithoutContact(ok bool) {
	if !ok {
		paypass.Require(nil, 1, "")
	}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", src, 0)
	require.NoError(t, err)

	flagged := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if len(contactGatedPayPasswordBranches(fn)) > 0 {
			flagged[fn.Name.Name] = true
		}
	}
	assert.Equal(t, map[string]bool{
		"inlineSkip": true, "negatedForm": true, "elseBranchForm": true,
	}, flagged,
		"结构锁的分析器认错了形状 —— 认不出就是永真断言,认过头就是把无关代码报成缺陷")

	// skipWhenContact 是这条锁**抓不到**的形状(early return 之后才调 Require,
	// 两者不在同一个 if 里)。如实记下来,免得有人把这条锁当成完备防线:
	// 行为锁与代码评审仍然是必需的。
	assert.False(t, flagged["skipWhenContact"],
		"early-return 形状不在本锁射程内,这是已知边界,不是回归")
}

// forEachQianyeSource 遍历 qianye/ 下所有非测试 .go 文件。
//
// 起点是相对本包的 ../..(即 qianye/),而不是仓库根:结构锁只管扩展自己的代码,
// 上游文件不会调用 paypass。
func forEachQianyeSource(t *testing.T, visit func(*ast.File, *token.FileSet)) {
	t.Helper()
	fset := token.NewFileSet()
	root := filepath.Join("..", "..")
	var seen int
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		seen++
		visit(f, fset)
		return nil
	}))
	// 遍历本身也要被断言:起点写错会让上面那条"通过"而一个字节都没读到。
	require.Greater(t, seen, 50, "扫到的 .go 文件太少,遍历起点可能不对")
}
