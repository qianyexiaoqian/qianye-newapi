package violation

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStripInlineBinary 是证据留存的容量闸门。
//
// 一条含 10 张 1MB base64 图片的请求归档下来就是 10MB;按 1000 次违规/天算
// 是 300GB/月。base64 必须在入库之前就被换成描述符,而不是靠截断兜底 ——
// 截断只会把图片切一半存进去,既没用又照样占空间。
func TestStripInlineBinary(t *testing.T) {
	t.Run("data URI 被替换为描述符且不含原始数据", func(t *testing.T) {
		blob := strings.Repeat("QUJDRA", 200) // 1200 字符
		text := "看这张图 data:image/png;base64," + blob + " 然后回答"
		out, n := stripInlineBinary(text)

		assert.Equal(t, 1, n)
		assert.NotContains(t, out, blob, "原始 base64 绝不能留在归档里")
		assert.Contains(t, out, "image/png")
		assert.Contains(t, out, "sha256:")
		assert.Contains(t, out, "看这张图")
		assert.Contains(t, out, "然后回答")
		assert.Less(t, len(out), len(text)/2)
	})

	t.Run("裸的超长 base64 串同样被剥离", func(t *testing.T) {
		blob := strings.Repeat("A", minBase64Run+10)
		out, n := stripInlineBinary("prefix " + blob + " suffix")
		assert.Equal(t, 1, n)
		assert.NotContains(t, out, blob)
	})

	t.Run("正常文本不被误伤", func(t *testing.T) {
		text := "这是一段普通的中文提示词,里面有 base64 这个词但没有二进制数据。"
		out, n := stripInlineBinary(text)
		assert.Equal(t, 0, n)
		assert.Equal(t, text, out)
	})
}

// TestRedact 验证脱敏。归档内容是用户输入的原始文本,属于个人数据 ——
// 脱敏在写入前执行,数据库里因此从来不存在未脱敏的原文。
func TestRedact(t *testing.T) {
	in := "联系 alice@example.com 或 13800138000,密钥 sk-abcdefghijklmnopqrstuvwx,Authorization: Bearer abcdefghijklmnopqrst"
	out, stats := redact(in)

	assert.NotContains(t, out, "alice@example.com")
	assert.NotContains(t, out, "13800138000")
	assert.NotContains(t, out, "sk-abcdefghijklmnopqrstuvwx")
	assert.NotContains(t, out, "abcdefghijklmnopqrst")
	assert.Contains(t, out, "«email»")
	assert.Contains(t, out, "«phone»")
	require.NotNil(t, stats)
	assert.EqualValues(t, 1, stats["email"])

	// 顺序敏感:身份证必须先于银行卡处理,否则 18 位身份证会被当成卡号,
	// redact_stats 的口径就错了。
	_, idStats := redact("身份证 11010119900307123X 卡号 6222021234567890123")
	assert.EqualValues(t, 1, idStats["id_card_cn"])
	assert.EqualValues(t, 1, idStats["bank_card"])

	out2, stats2 := redact("完全干净的一句话")
	assert.Equal(t, "完全干净的一句话", out2)
	assert.Nil(t, stats2)
}

// TestBuildEvidenceTruncatesAndRoundTrips 验证三层闸门:剥离 → 截断 → 压缩,
// 以及归档能被原样读回(读不回来的证据等于没存)。
func TestBuildEvidenceTruncatesAndRoundTrips(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  evidence_max_bytes: 1024\n")

	huge := strings.Repeat("违规内容片段", 5000) // 约 90KB
	rec := &Record{RecNo: "vr_test_1", RequestId: "req-1", ModelName: "gpt-4o", CreatedAt: 100}
	cr := mustCompile(t, Rule{Id: 7, Phase: PhasePrompt, MatchType: MatchKeyword,
		Action: ActionRecord, Pattern: "违规内容"})
	v := &verdict{Rule: cr, Terms: []string{"违规内容"}, Snippet: "违规内容片段"}

	p := buildEvidence(rec, scanInput{Text: huge}, v, nil)
	require.NotNil(t, p)

	assert.True(t, p.Truncated)
	assert.EqualValues(t, len(huge), p.OriginBytes)
	// 压缩前必须已经被截到配置上限附近,而不是把 90KB 原样压缩后入库。
	assert.Less(t, p.RawBytes, int64(4096))
	assert.Less(t, p.StoredBytes, p.RawBytes)

	text, err := decodeEvidence(p)
	require.NoError(t, err)
	assert.Contains(t, text, "违规内容")
	assert.Contains(t, text, "gpt-4o")
}

// TestDescribeFilesKeepsNoBinary 保证多模态输入只留描述符。
func TestDescribeFilesKeepsNoBinary(t *testing.T) {
	blob := strings.Repeat("Z", 4096)
	files := []*types.FileMeta{
		types.NewImageFileMeta(types.NewBase64FileSource("data:image/jpeg;base64,"+blob, "image/jpeg"), "high"),
		types.NewImageFileMeta(types.NewURLFileSource("https://cdn.example.com/a.png?sig=secret-token"), "low"),
	}
	out := describeFiles(files)

	assert.NotContains(t, out, blob, "二进制绝不能进描述符")
	assert.NotContains(t, out, "secret-token", "签名 URL 的 query 里常带临时凭证,必须剥掉")
	assert.Contains(t, out, "https://cdn.example.com/a.png")
	assert.Contains(t, out, "sha256")
	assert.Less(t, len(out), 1024)
}
