package violation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safe / unsafe 两种形状必须原样认下来,而且 safe 那一档**不能**带类别 ——
// 带了会让 toVerdict 在未违规的分支上去解析类别,而那条路的结果没人读,
// 于是一个解析错误会一直不被发现。
func TestParseLlamaGuardAcceptsBothShapes(t *testing.T) {
	g, err := parseLlamaGuardVerdict("safe")
	require.NoError(t, err)
	assert.Equal(t, "safe", g.Safety)
	assert.Empty(t, g.Categories)
	assert.Empty(t, g.Unknown)

	g, err = parseLlamaGuardVerdict("unsafe\nS1,S10")
	require.NoError(t, err)
	assert.Equal(t, "unsafe", g.Safety)
	// S1 → violent、S10 → unethical_acts,且按 guardAllCategories 排序。
	assert.Equal(t, []string{GuardCatViolent, GuardCatUnethical}, g.Categories)
	assert.Empty(t, g.Unknown)
}

// 排版容忍面必须与文档一致:多认一种形状不要紧,少认一种的表现是
// "这个部署永远 bad_json" —— 而 bad_json 是**付过钱**的失败。
func TestParseLlamaGuardToleratesRealWorldFormatting(t *testing.T) {
	for _, s := range []string{
		"safe", " safe ", "SAFE", "Safe\n", "safe.", "\n\nsafe\n\n",
	} {
		g, err := parseLlamaGuardVerdict(s)
		require.NoErrorf(t, err, "%q 必须认成 safe", s)
		assert.Equalf(t, "safe", g.Safety, "%q", s)
	}
	// 码之间的分隔符:官方是逗号,实测不同部署会混用空格与换行。
	for _, s := range []string{
		"unsafe\nS1,S10", "unsafe\ns1, s10", "UNSAFE\nS1 S10",
		"unsafe\nS1\nS10", "unsafe\n S1 , S10 ", "unsafe\nS1;S10",
	} {
		g, err := parseLlamaGuardVerdict(s)
		require.NoErrorf(t, err, "%q 必须认成 unsafe", s)
		assert.Equalf(t, "unsafe", g.Safety, "%q", s)
		assert.Equalf(t, []string{GuardCatViolent, GuardCatUnethical}, g.Categories, "%q", s)
	}
}

// 形状不对必须**可见地失败**,而不是被前缀/包含匹配读成一个判定。
//
// 这一条挡的是一个具体的事故形状:渠道地址指到了一个通用模型,它回一段
// 客套话。做包含匹配的话 "that would be unsafe" 会被读成违规,于是这个
// 渠道变成一台随机拦截器 —— 而错误的方向是**拦截**,用户直接受影响。
func TestParseLlamaGuardRejectsAnythingElse(t *testing.T) {
	for _, s := range []string{
		"", "   ", "yes", "no", "safe and sound", "unsafe content detected",
		"I'm sorry, that would be unsafe.", "The answer is safe",
		"{\"safe\":true}", "S1", "1", "unsafely",
	} {
		_, err := parseLlamaGuardVerdict(s)
		assert.ErrorIsf(t, err, errLlamaGuardInvalidResponse, "%q 必须判为形状不对", s)
	}
}

// unsafe 却一个码都没给:**判定必须成立**。
//
// 反向的写法(当成解析失败)会把一票真实的违规判定变成 fail-open 放行,
// 而这正是本模块最不能接受的那个方向。
func TestParseLlamaGuardKeepsVerdictWhenNoCodes(t *testing.T) {
	for _, s := range []string{"unsafe", "unsafe\n", "unsafe\n\n  "} {
		g, err := parseLlamaGuardVerdict(s)
		require.NoErrorf(t, err, "%q", s)
		assert.Equalf(t, "unsafe", g.Safety, "%q", s)
		assert.Emptyf(t, g.Categories, "%q", s)

		v := g.toVerdict(guardPolicy{}, aiVocabulary{})
		assert.Truef(t, v.Violated, "%q:没有类别也必须判违规", s)
	}
}

// 表外的码要留痕而不是被静默丢弃 —— 它是"换代际/换微调版/渠道其实指向
// 别的模型"唯一的早期信号。判定同时必须照常成立。
func TestParseLlamaGuardKeepsUnknownCodes(t *testing.T) {
	g, err := parseLlamaGuardVerdict("unsafe\nS1,S99,S42")
	require.NoError(t, err)
	assert.Equal(t, []string{GuardCatViolent}, g.Categories)
	assert.Equal(t, []string{"s42", "s99"}, g.Unknown, "按字典序、已去重")

	v := g.toVerdict(guardPolicy{}, aiVocabulary{})
	assert.True(t, v.Violated)
	assert.Contains(t, v.Reason, "unknown=s42,s99")
}

// 重复的码不得把同一个类别塞两遍 —— guardLabels 的契约是"已去重"。
func TestParseLlamaGuardDeduplicates(t *testing.T) {
	g, err := parseLlamaGuardVerdict("unsafe\nS1,S1,S9,S1")
	require.NoError(t, err)
	// S1 与 S9 都折进 violent,合起来只该出现一次。
	assert.Equal(t, []string{GuardCatViolent}, g.Categories)
}

// 1 代的 O 码是**另一套分类法**,不是 S 码的别名。这一条钉住那张独立表。
func TestParseLlamaGuardMapsLegacyOCodes(t *testing.T) {
	g, err := parseLlamaGuardVerdict("unsafe\nO2")
	require.NoError(t, err)
	assert.Equal(t, []string{GuardCatSexual}, g.Categories, "O2 = Sexual Content")
	assert.Empty(t, g.Unknown)

	// O1 是 Violence and Hate —— 与 S1 恰好同落 violent,但走的是另一张表。
	g, err = parseLlamaGuardVerdict("unsafe\nO1")
	require.NoError(t, err)
	assert.Equal(t, []string{GuardCatViolent}, g.Categories)
}

// 九类映射是有损的(S4 与 S12 都落 sexual),原始 S 码必须留在 Reason 里,
// 否则事后无法区分"儿童性剥削"与"成人性内容" —— 而这两者在研判上完全
// 不是一回事。
func TestLlamaGuardReasonKeepsRawCodes(t *testing.T) {
	g, err := parseLlamaGuardVerdict("unsafe\nS4")
	require.NoError(t, err)
	v := g.toVerdict(guardPolicy{}, aiVocabulary{})
	assert.Contains(t, v.Reason, "S4 Child Sexual Exploitation")
	assert.Contains(t, v.Reason, AIProtocolLlamaGuard+" safety=unsafe",
		"Reason 必须说明是哪条协议判的")
	assert.NotContains(t, v.Reason, AIProtocolQwen3Guard,
		"多协议共用 guardLabels 之后,不能再把所有判定都署名成 qwen3guard")
}

// 存量的 qwen3guard 判定的 Reason 必须**逐字节不变** —— Source 的零值
// 就是为此存在的。变了的话,历史明细与新明细在同一页里长得不一样。
func TestGuardSummaryKeepsLegacyPrefixForQwen3Guard(t *testing.T) {
	g, err := parseGuardVerdict("Safety: Unsafe\nCategories: Violent")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(g.summary(), "qwen3guard safety=unsafe"),
		"实际为 %q", g.summary())
}

// 协议归一与写入侧校验必须同时认它,否则要么保存不进去,要么保存进去了
// 运行期又被折回 json_prompt —— 后者正是"配了护栏模型却永远 bad_json"。
func TestLlamaGuardProtocolIsAcceptedEndToEnd(t *testing.T) {
	assert.Equal(t, AIProtocolLlamaGuard, normalizeAIProtocol(AIProtocolLlamaGuard))
	assert.Equal(t, AIProtocolLlamaGuard, normalizeAIProtocol(" llama_guard "))
	assert.True(t, aiProtocolValid(AIProtocolLlamaGuard))
	assert.False(t, aiProtocolValid("llamaguard"), "拼错的取值必须在写入侧当场 400")
	assert.True(t, isGuardProtocol(AIProtocolLlamaGuard))
	assert.True(t, guardProtocolHasCategories(AIProtocolLlamaGuard),
		"它一次给出多标签,九类启用清单对它是真的生效的")
	assert.False(t, guardProtocolHasCategories(AIProtocolGraniteGuardian),
		"二值协议给不出类别,留着那份清单等于给运营一个勾了不生效的开关")
}

// 组请求这一侧:护栏协议一律不发提示词。发了的话,那段文字会被当成
// 待分类内容的一部分,判准直接掉 —— 而现象只是"这个渠道判得不准"。
func TestLlamaGuardRequestSendsOnlyUserMessage(t *testing.T) {
	ch := &aiChannelRT{Model: "meta-llama/Llama-Guard-3-1B", Protocol: AIProtocolLlamaGuard}
	raw, err := aiRequestPayload(ch, "这一段提示词绝不能被发出去", "待审内容")
	require.NoError(t, err)

	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens int `json:"max_tokens"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "user", got.Messages[0].Role)
	assert.Equal(t, "待审内容", got.Messages[0].Content, "不包 <content> 标签")
	assert.Equal(t, llamaGuardMaxTokens, got.MaxTokens)
	assert.NotContains(t, string(raw), "绝不能被发出去")
}
