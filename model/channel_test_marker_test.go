package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// channel_test_marker_test.go —— 钉住"渠道可用性测试不返佣"这条硬排除的两端。
//
// # 这条排除为什么需要一个守卫
//
// 判据写在 qianye/modules/stardust/settle.go 的 likeChannelTest 里,标记打在
// controller/channel-test.go 的 buildTestLogOther 里。两端隔着两个模块,
// 中间没有任何编译期依赖:把标记那一行删掉,`go build` 通过、渠道测试照常 200、
// 日志照常写,唯一的变化是**每一次渠道测试都开始给管理员(与他的上线)发星屑**。
//
// 在标记出现之前,这条排除靠的是 `TokenName == "模型测试"` 这个中文字面量。
// 它长得像一句可以随手改、可以做 i18n 的界面文案,而改掉它的后果与删标记相同。
//
// 因此这里断的是"两端还连着"这件事本身:
//   - 写日志那一侧必须真的把标记打进 other;
//   - 两侧都不许再出现裸的中文字面量(必须走 model 的常量)。
//
// 它抓不到什么:它不看排除的语义对不对(那由 stardust 自己的用例负责),
// 也不保证除渠道测试外没有别的路径写这个键。

const (
	channelTestControllerFile = "../controller/channel-test.go"
	stardustSettleFile        = "../qianye/modules/stardust/settle.go"
)

// TestChannelTestLogCarriesExclusionMarker 确认写日志那一侧真的打了标记。
func TestChannelTestLogCarriesExclusionMarker(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, channelTestControllerFile, nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Recv == nil && d.Name.Name == "buildTestLogOther" {
			fn = d
		}
	}
	require.NotNil(t, fn,
		"controller/channel-test.go 里必须有 func buildTestLogOther —— 改名了就把这条守卫一起改")

	// 上游 rc.33 起 other 是 model.LogOther,写法从 `other[key] = true` 变成
	// `other.SetPublic(key, true)`,所以判据认的是「常量名出现在某次调用的实参里」
	// 而不是某个下标赋值的左值。要抓的东西没变:那一行必须还在。
	marked := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range call.Args {
			sel, ok := arg.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "ChannelTestLogOtherKey" {
				marked = true
			}
		}
		return true
	})
	assert.True(t, marked,
		"buildTestLogOther 必须往 other 里写 model.ChannelTestLogOtherKey："+
			"少了这一行,qianye/modules/stardust 的日结硬排除只剩中文文案兜底,"+
			"每次渠道测试都会按分组档给管理员返星屑、再给他的上线返一份")
}

// TestChannelTestTokenNameGoesThroughConstant 确认判据两端不再各写一份中文字面量。
func TestChannelTestTokenNameGoesThroughConstant(t *testing.T) {
	for _, path := range []string{channelTestControllerFile, stardustSettleFile} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)

			// 注释里引用这个字面量是合理的(本仓的注释就在解释它),
			// 因此只看代码行。
			for i, line := range strings.Split(string(raw), "\n") {
				code := line
				if idx := strings.Index(code, "//"); idx >= 0 {
					code = code[:idx]
				}
				assert.NotContainsf(t, code, `"`+ChannelTestTokenName+`"`,
					"%s:%d 出现了裸的 %q —— 必须用 model.ChannelTestTokenName,"+
						"否则两端会各自漂移,而漂移的后果是星屑排除静默失效",
					path, i+1, ChannelTestTokenName)
			}
		})
	}
}
