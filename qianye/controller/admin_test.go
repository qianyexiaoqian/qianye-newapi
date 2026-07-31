package controller

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D3:人工裁决理由必须有长度上限,并且是在写库之前拒绝。
//
// 没有上限时,超长理由会在 MySQL 严格模式下报 1406 Data too long,而
// serverError 只回"处理失败,请稍后重试" —— 管理员不知道原因,原样重试仍然失败,
// 这笔资金单就永远停在 uncertain。uncertain 不会被补偿任务收敛(它只扫 pending),
// 人工裁决是它唯一的出口,堵死这个出口等于把单据永久卡死。
func TestCheckResolveReason(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantCode string
		wantOut  string
	}{
		{"空理由", "", "qy_reason_required", ""},
		{"纯空白视同未填", "   \t\n ", "qy_reason_required", ""},
		{"正常理由去首尾空白", "  主库探针确认已到账  ", "", "主库探针确认已到账"},
		{"刚好 200 个汉字", strings.Repeat("拒", 200), "", strings.Repeat("拒", 200)},
		{"201 个汉字超限", strings.Repeat("拒", 201), "qy_reason_too_long", ""},
		{"超长 ASCII", strings.Repeat("a", 201), "qy_reason_too_long", ""},
		{"刚好 200 个 ASCII", strings.Repeat("a", 200), "", strings.Repeat("a", 200)},
		{"超大请求体走字节剪枝", strings.Repeat("拒", 100000), "qy_reason_too_long", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, code, msg := checkResolveReason(tc.raw)
			assert.Equal(t, tc.wantCode, code)
			assert.Equal(t, tc.wantOut, got)
			if tc.wantCode == "" {
				assert.Empty(t, msg)
			} else {
				assert.NotEmpty(t, msg, "错误必须带可读提示,否则管理员不知道该改什么")
			}
		})
	}
}

// 校验通过的理由拼上前缀后仍要能安全落进 varchar(512):
// 200 个汉字 = 600 字节,按字节卡的列上会被截断,截断点必须是 rune 边界。
func TestResolveReasonFitsLastErrorColumn(t *testing.T) {
	reason, code, _ := checkResolveReason(strings.Repeat("拒", maxResolveReasonRunes))
	require.Empty(t, code)

	stored := audit.Truncate("人工裁决: "+reason, 512)
	assert.LessOrEqual(t, len(stored), 512)
	assert.True(t, utf8.ValidString(stored),
		"落库前的截断必须是 rune 安全的,否则 utf8mb4 列会以 1366 拒绝整行")
	assert.True(t, strings.HasPrefix(stored, "人工裁决: "),
		"裁决标记必须保留,否则事后分不清是人改的还是程序改的")
}
