package violation

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这一条是整个 RiskName 特性的**地基**:零值必须等于这一列加入之前的行为。
//
// 实测依据写在 aireview_granite.go 文件头 —— "不发 system" 与 system="harm"
// 在每一个样本上结果相同。所以空串 → 不发 system,存量渠道升级后逐字节无变化。
// 反过来(空串被当成某个具体风险)会让每一个已经在跑的 Granite 渠道在升级
// 那一秒静默换掉判定口径,而界面上一切正常。
func TestGraniteEmptyRiskSendsNoSystemMessage(t *testing.T) {
	for _, risk := range []string{"", "   ", graniteRiskHarm, "HARM", "无效的风险名"} {
		ch := &aiChannelRT{
			Model:    "ibm/granite3.1-guardian:2b",
			Protocol: AIProtocolGraniteGuardian,
			RiskName: risk,
		}
		raw, err := aiRequestPayload(ch, "提示词不该被发出去", "待审内容")
		require.NoErrorf(t, err, "%q", risk)

		msgs := payloadMessages(t, raw)
		require.Lenf(t, msgs, 1, "%q:harm 档必须只发一条 user 消息", risk)
		assert.Equalf(t, "user", msgs[0].Role, "%q", risk)
		assert.Equalf(t, "待审内容", msgs[0].Content, "%q", risk)
	}
}

// 选了具体风险时,风险名要**原样**作为 system 发出去。
//
// 它不是提示词而是风险槽:模板对 system 做的是精确匹配(见文件头引的那段
// Modelfile)。多一个字、翻译成中文、或者拼成 HF 那边的 `jailbreaking`,
// 都会落回 harm 档 —— 而那是一次**静默**的口径改变。
func TestGraniteRiskGoesOutAsBareSystemMessage(t *testing.T) {
	for _, risk := range []string{"jailbreak", "violence", "sexual_content", "social_bias"} {
		ch := &aiChannelRT{
			Model:    "ibm/granite3.1-guardian:2b",
			Protocol: AIProtocolGraniteGuardian,
			RiskName: risk,
		}
		raw, err := aiRequestPayload(ch, "提示词不该被发出去", "待审内容")
		require.NoErrorf(t, err, "%q", risk)

		msgs := payloadMessages(t, raw)
		require.Lenf(t, msgs, 2, "%q", risk)
		assert.Equalf(t, "system", msgs[0].Role, "%q", risk)
		assert.Equalf(t, risk, msgs[0].Content, "%q:必须是**裸的**风险名,不能加任何修饰", risk)
		assert.Equalf(t, "user", msgs[1].Role, "%q", risk)
		assert.Equalf(t, "待审内容", msgs[1].Content, "%q", risk)
		assert.NotContainsf(t, string(raw), "提示词不该被发出去", "%q", risk)
	}
}

// 另外两条护栏协议一条 system 都不能发 —— 它们的模板没有风险槽,
// 那段文字会被当成待分类内容的一部分。
func TestOtherGuardProtocolsNeverSendSystem(t *testing.T) {
	for _, proto := range []string{AIProtocolQwen3Guard, AIProtocolLlamaGuard} {
		ch := &aiChannelRT{Model: "m", Protocol: proto, RiskName: "jailbreak"}
		raw, err := aiRequestPayload(ch, "p", "待审内容")
		require.NoErrorf(t, err, proto)
		msgs := payloadMessages(t, raw)
		require.Lenf(t, msgs, 1, "%s 不得发 system", proto)
		assert.Equal(t, "user", msgs[0].Role)
	}
}

// 类别来自**问题**而不是回答:模型只回一个词,而我们知道这一次问的是
// 哪个风险。这是 Granite 能给出类别的唯一依据。
func TestGraniteVerdictCarriesCategoryFromRisk(t *testing.T) {
	for risk, want := range map[string]string{
		"jailbreak":          GuardCatJailbreak,
		"violence":           GuardCatViolent,
		"sexual_content":     GuardCatSexual,
		"social_bias":        GuardCatUnethical,
		"profanity":          GuardCatUnethical,
		"unethical_behavior": GuardCatUnethical,
		graniteRiskHarm:      "",
	} {
		v := graniteVerdict(true, risk)
		assert.Truef(t, v.Violated, "%q", risk)
		assert.Equalf(t, want, v.Category, "%q", risk)
		assert.Equalf(t, guardConfidenceUnsafe, v.Confidence, "%q", risk)
		assert.Containsf(t, v.Reason, "risk="+normalizeGraniteRisk(risk),
			"%q:Reason 必须说明这一次问的是哪个风险,否则事后无从复盘", risk)

		// 判 No 时不得带类别 —— 未违规的分支上类别不参与任何判定,
		// 带上去只会让明细页显示一个从未成立过的类型。
		assert.Emptyf(t, graniteVerdict(false, risk).Category, "%q", risk)
		assert.Falsef(t, graniteVerdict(false, risk).Violated, "%q", risk)
	}
}

// 运行期容忍脏值(折回 harm),写入侧当场 400。两者故意不同 ——
// 理由与 normalizeAIProtocol / aiProtocolValid 那一对完全相同。
func TestGraniteRiskNormalizeIsLenientAndValidateIsStrict(t *testing.T) {
	assert.Equal(t, graniteRiskHarm, normalizeGraniteRisk(""))
	assert.Equal(t, graniteRiskHarm, normalizeGraniteRisk("DBA 手抖写进来的值"))
	assert.Equal(t, "jailbreak", normalizeGraniteRisk(" JailBreak "))

	assert.True(t, graniteRiskValid(""), "空串 = 零值档,必须放行")
	for _, r := range graniteRiskOrder {
		assert.Truef(t, graniteRiskValid(r), "%q", r)
	}
	assert.False(t, graniteRiskValid("jailbreaking"),
		"HF 那边的拼写在这条端点上落回 harm —— 必须当场 400 而不是静默换口径")
	assert.False(t, graniteRiskValid("function_calling"),
		"Agentic 档需要 tools 角色,本模块给不出")
	assert.False(t, graniteRiskValid("groundedness"),
		"RAG 档需要 context 角色,本模块给不出")
	assert.False(t, graniteRiskValid("answer_relevance"))
	assert.False(t, graniteRiskValid("context_relevance"))
}

// 每一个放行的风险名都必须在映射表里有一条 —— 否则写入侧放它进来、
// 运行期又查不到类别,表现是"选了风险却永远落兜底类型"。
func TestGraniteRiskOrderCoversTheMap(t *testing.T) {
	assert.Len(t, graniteRiskOrder, len(graniteRisks))
	for _, r := range graniteRiskOrder {
		_, ok := graniteRisks[r]
		assert.Truef(t, ok, "%q 出现在下拉框里却不在映射表里", r)
	}
}

// 写入侧:风险名只在 Granite 渠道上留存,其他协议上必须清空。
// 留着一个被忽略的取值,下一个人会照着界面回显去查"为什么没生效"。
func TestValidateAIChannelClearsRiskOnOtherProtocols(t *testing.T) {
	for _, proto := range []string{AIProtocolJSONPrompt, AIProtocolQwen3Guard, AIProtocolLlamaGuard} {
		ch := &AIChannel{
			Name: "n", BaseUrl: "http://127.0.0.1:11434", Model: "m",
			Protocol: proto, RiskName: "jailbreak", Weight: 1, TimeoutMs: 3000,
		}
		require.NoErrorf(t, validateAIChannel(ch), proto)
		assert.Emptyf(t, ch.RiskName, "%s 上必须清空风险名", proto)
	}

	ch := &AIChannel{
		Name: "n", BaseUrl: "http://127.0.0.1:11434", Model: "m",
		Protocol: AIProtocolGraniteGuardian, RiskName: " JailBreak ",
		Weight: 1, TimeoutMs: 3000,
	}
	require.NoError(t, validateAIChannel(ch))
	assert.Equal(t, "jailbreak", ch.RiskName, "归一成小写去空白后落库")
}

// 九类启用清单对 Llama Guard 是真的生效的,不能跟着 Granite 一起被清掉。
// 清掉的表现是运营勾了类别、保存后勾选消失,而且判定的置信度降档失效。
func TestValidateAIChannelKeepsCategoriesForLlamaGuard(t *testing.T) {
	ch := &AIChannel{
		Name: "n", BaseUrl: "http://127.0.0.1:11434", Model: "m",
		Protocol: AIProtocolLlamaGuard, GuardCategories: GuardCatViolent,
		GuardControversial: GuardControversialUnsafe, GuardElevate: GuardCatJailbreak,
		Weight: 1, TimeoutMs: 3000,
	}
	require.NoError(t, validateAIChannel(ch))
	assert.Equal(t, GuardCatViolent, ch.GuardCategories, "它一次给出多标签,这份子集有意义")
	assert.Empty(t, ch.GuardControversial, "Llama Guard 只有 safe/unsafe 两态")
	assert.Empty(t, ch.GuardElevate, "Elevate 只在 Controversial 档里被读")
}

func payloadMessages(t *testing.T, raw []byte) []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
} {
	t.Helper()
	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	return got.Messages
}
