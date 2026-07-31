package logmetrics

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadConfig 用一份临时 YAML 驱动真实的配置加载路径。
// 不 mock config:两个 hook 的开关判断本身就是要保护的行为。
func loadConfig(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
}

const bothColumnsOn = `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
log_metrics:
  show_reasoning_effort: true
  show_cache_ratio: true
`

func intPtr(v int) *int { return &v }

// ───────────────────────────── 归一化映射 ─────────────────────────────

func TestNormalizeEffort(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		// OpenAI / GPT-5 / DeepSeek V4 的枚举口径
		{"none", LevelNone},
		{"minimal", LevelMinimal},
		{"low", LevelLow},
		{"medium", LevelMedium},
		{"high", LevelHigh},
		{"xhigh", LevelMax},
		{"max", LevelMax},
		// 大小写与空白必须归一,上游模型名后缀解析出来的值不保证规范
		{"HIGH", LevelHigh},
		{"  Low  ", LevelLow},
		// 厂商自定义的开关式取值
		{"disabled", LevelNone},
		{"enabled", LevelMedium},
		{"auto", LevelAuto},
		{"dynamic", LevelAuto},
		// 未知口径保守居中:归 none 会让用户以为没花思考的钱
		{"turbo-ultra-9000", LevelMedium},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, NormalizeEffort(c.raw), "raw=%q", c.raw)
	}
}

// 分档阈值是左开右闭,边界值错一格会让整列的配色系统性偏移。
func TestLevelForBudget(t *testing.T) {
	cases := []struct {
		budget int
		want   string
	}{
		{-1, LevelAuto}, // Gemini 动态预算
		{-9999, LevelAuto},
		{0, LevelNone},
		{1, LevelMinimal},
		{BudgetMinimalMax, LevelMinimal},
		{BudgetMinimalMax + 1, LevelLow},
		{BudgetLowMax, LevelLow},
		{BudgetLowMax + 1, LevelMedium},
		{BudgetMediumMax, LevelMedium},
		{BudgetMediumMax + 1, LevelHigh},
		{BudgetHighMax, LevelHigh},
		{BudgetHighMax + 1, LevelMax},
		{math.MaxInt32, LevelMax},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, LevelForBudget(c.budget), "budget=%d", c.budget)
	}
}

// ───────────────────────────── 各厂商 usage/请求结构 → 归一化 ─────────────────────────────

func TestDetectReasoningByVendor(t *testing.T) {
	cases := []struct {
		name       string
		info       *relaycommon.RelayInfo
		wantLevel  string
		wantBudget int
		wantSrc    string
		wantRaw    string
	}{
		{
			name:      "relayInfo 已算出的 effort 优先于原始请求",
			info:      &relaycommon.RelayInfo{ReasoningEffort: "high", Request: &dto.ClaudeRequest{Thinking: &dto.Thinking{BudgetTokens: intPtr(1024)}}},
			wantLevel: LevelHigh,
			wantSrc:   SrcRelayInfo,
			wantRaw:   "high",
		},
		{
			name:       "Claude thinking.budget_tokens",
			info:       &relaycommon.RelayInfo{Request: &dto.ClaudeRequest{Thinking: &dto.Thinking{Type: "enabled", BudgetTokens: intPtr(24576)}}},
			wantLevel:  LevelHigh,
			wantBudget: 24576,
			wantSrc:    SrcClaudeThinking,
			wantRaw:    "budget:24576",
		},
		{
			name:      "Claude thinking.type=disabled 是明确的不思考",
			info:      &relaycommon.RelayInfo{Request: &dto.ClaudeRequest{Thinking: &dto.Thinking{Type: "disabled"}}},
			wantLevel: LevelNone,
			wantSrc:   SrcClaudeThinking,
			wantRaw:   "disabled",
		},
		{
			name:      "Claude output_config.effort(Opus 4.6+)",
			info:      &relaycommon.RelayInfo{Request: &dto.ClaudeRequest{OutputConfig: json.RawMessage(`{"effort":"max"}`)}},
			wantLevel: LevelMax,
			wantSrc:   SrcClaudeEffort,
			wantRaw:   "max",
		},
		{
			name: "Gemini 数值 thinkingBudget",
			info: &relaycommon.RelayInfo{Request: &dto.GeminiChatRequest{
				GenerationConfig: dto.GeminiChatGenerationConfig{
					ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingBudget: intPtr(2048)},
				},
			}},
			wantLevel:  LevelLow,
			wantBudget: 2048,
			wantSrc:    SrcGeminiBudget,
			wantRaw:    "budget:2048",
		},
		{
			name: "Gemini 动态预算 -1 必须保留原值而非归零",
			info: &relaycommon.RelayInfo{Request: &dto.GeminiChatRequest{
				GenerationConfig: dto.GeminiChatGenerationConfig{
					ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingBudget: intPtr(-1)},
				},
			}},
			wantLevel:  LevelAuto,
			wantBudget: -1,
			wantSrc:    SrcGeminiBudget,
			wantRaw:    "budget:-1",
		},
		{
			name: "Gemini thinkingLevel 枚举",
			info: &relaycommon.RelayInfo{Request: &dto.GeminiChatRequest{
				GenerationConfig: dto.GeminiChatGenerationConfig{
					ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "low"},
				},
			}},
			wantLevel: LevelLow,
			wantSrc:   SrcGeminiLevel,
			wantRaw:   "low",
		},
		{
			name: "Gemini 只有 includeThoughts 不算强度信号",
			info: &relaycommon.RelayInfo{Request: &dto.GeminiChatRequest{
				GenerationConfig: dto.GeminiChatGenerationConfig{
					ThinkingConfig: &dto.GeminiThinkingConfig{IncludeThoughts: true},
				},
			}},
		},
		{
			name:      "OpenAI Responses reasoning.effort",
			info:      &relaycommon.RelayInfo{Request: &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "minimal"}}},
			wantLevel: LevelMinimal,
			wantSrc:   SrcOAIReasoning,
			wantRaw:   "minimal",
		},
		{
			name:      "通用 OpenAI 兼容渠道的 reasoning_effort(上游不落库的缺口)",
			info:      &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{ReasoningEffort: "medium"}},
			wantLevel: LevelMedium,
			wantSrc:   SrcOAIEffort,
			wantRaw:   "medium",
		},
		{
			name:      "OpenRouter reasoning.effort",
			info:      &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Reasoning: json.RawMessage(`{"effort":"high"}`)}},
			wantLevel: LevelHigh,
			wantSrc:   SrcOAIReasoning,
			wantRaw:   "high",
		},
		{
			name:       "OpenRouter reasoning.max_tokens 走数值分档",
			info:       &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Reasoning: json.RawMessage(`{"max_tokens":512}`)}},
			wantLevel:  LevelMinimal,
			wantBudget: 512,
			wantSrc:    SrcOAIReasoning,
			wantRaw:    "budget:512",
		},
		{
			name:       "Qwen thinking_budget",
			info:       &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{ThinkingBudget: json.RawMessage(`8192`)}},
			wantLevel:  LevelMedium,
			wantBudget: 8192,
			wantSrc:    SrcQwen,
			wantRaw:    "budget:8192",
		},
		{
			name:      "Qwen enable_thinking=false 压过 thinking_budget",
			info:      &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{EnableThinking: json.RawMessage(`false`), ThinkingBudget: json.RawMessage(`30000`)}},
			wantLevel: LevelNone,
			wantSrc:   SrcQwen,
			wantRaw:   "enable_thinking:false",
		},
		{
			name:      "Qwen enable_thinking=true 且无预算时按默认档",
			info:      &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{EnableThinking: json.RawMessage(`true`)}},
			wantLevel: LevelMedium,
			wantSrc:   SrcQwen,
			wantRaw:   "enable_thinking:true",
		},
		{
			name:      "doubao/zhipu thinking.type",
			info:      &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{THINKING: json.RawMessage(`{"type":"disabled"}`)}},
			wantLevel: LevelNone,
			wantSrc:   SrcVendorThinking,
			wantRaw:   "disabled",
		},
		{
			name:      "ollama think 布尔",
			info:      &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Think: json.RawMessage(`true`)}},
			wantLevel: LevelMedium,
			wantSrc:   SrcOllamaThink,
			wantRaw:   "think:true",
		},
		{
			name:      "ollama think 字符串档位",
			info:      &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Think: json.RawMessage(`"high"`)}},
			wantLevel: LevelHigh,
			wantSrc:   SrcOllamaThink,
			wantRaw:   "high",
		},
		{
			name: "普通请求不产生推理数据",
			info: &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Model: "gpt-4o"}},
		},
		{
			name: "typed-nil 请求不得 panic",
			info: &relaycommon.RelayInfo{Request: (*dto.ClaudeRequest)(nil)},
		},
		{
			name: "nil relayInfo 不得 panic",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectReasoning(c.info)
			if c.wantLevel == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, c.wantLevel, got.Level)
			assert.Equal(t, c.wantBudget, got.Budget)
			assert.Equal(t, c.wantSrc, got.Src)
			assert.Equal(t, c.wantRaw, got.Raw)
		})
	}
}

// 客户端可控的透传字段体积无上界,超限必须直接放弃探测而不是拖慢结算链路。
func TestOversizedRawFieldIsSkipped(t *testing.T) {
	huge := make([]byte, maxRawJSONBytes+64)
	for i := range huge {
		huge[i] = ' '
	}
	copy(huge, []byte(`{"effort":"high"}`))

	got := detectReasoning(&relaycommon.RelayInfo{
		Request: &dto.GeneralOpenAIRequest{Reasoning: json.RawMessage(huge)},
	})
	assert.Nil(t, got)
}

// ───────────────────────────── 缓存分母与边界 ─────────────────────────────

func TestComputeCacheBasis(t *testing.T) {
	cases := []struct {
		name        string
		prompt      int
		read        int
		write       int
		claude      bool
		wantTotal   int
		wantRead    int
		wantWrite   int
		wantAnomaly bool
	}{
		{
			// OpenAI 语义:prompt_tokens 已含 cached_tokens,加回去会重复计数
			name:   "openai 语义分母就是 prompt_tokens",
			prompt: 10000, read: 4000, write: 0,
			wantTotal: 10000, wantRead: 4000,
		},
		{
			// Anthropic 语义:input_tokens 不含 cache read / cache creation
			name:   "anthropic 语义必须把缓存加回分母",
			prompt: 2000, read: 6000, write: 1000, claude: true,
			wantTotal: 9000, wantRead: 6000, wantWrite: 1000,
		},
		{
			name:   "全零输入不产生任何分母,前端据分子为 0 判定 0%",
			prompt: 0, read: 0, write: 0,
		},
		{
			name:      "无缓存的普通请求",
			prompt:    1234,
			wantTotal: 1234,
		},
		{
			// 上游 usage 自相矛盾:直接钳到 100% 而不留痕会掩盖上游 bug
			name:   "分子大于分母时抬高分母并标记异常",
			prompt: 100, read: 5000,
			wantTotal: 5000, wantRead: 5000, wantAnomaly: true,
		},
		{
			// OpenRouter+Claude 路径对 PromptTokens 连做减法,可能下溢
			name:   "负数一律归零并标记异常",
			prompt: -50, read: -1, write: -1,
			wantAnomaly: true,
		},
		{
			name:   "分母溢出 int32 时饱和并标记异常",
			prompt: math.MaxInt32, read: math.MaxInt32, write: math.MaxInt32, claude: true,
			wantTotal: math.MaxInt32, wantRead: math.MaxInt32, wantWrite: math.MaxInt32,
			wantAnomaly: true,
		},
		{
			name:   "分子恰好等于分母是合法的 100%,不算异常",
			prompt: 0, read: 3000, write: 0, claude: true,
			wantTotal: 3000, wantRead: 3000,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeCacheBasis(c.prompt, c.read, c.write, c.claude)
			assert.Equal(t, c.wantTotal, got.InputTotal, "InputTotal")
			assert.Equal(t, c.wantRead, got.CacheRead, "CacheRead")
			assert.Equal(t, c.wantWrite, got.CacheWrite, "CacheWrite")
			assert.Equal(t, c.wantAnomaly, got.Anomaly, "Anomaly")
			// 无论输入多畸形,分子都不得超过分母 —— 否则前端会算出 >100%
			assert.LessOrEqual(t, got.CacheRead, got.InputTotal)
		})
	}
}

// ───────────────────────────── hook 写入行为 ─────────────────────────────

// 功能关闭时 other 必须逐字节不变:这正是「老日志无 qy_ 键 → 前端显示 —」
// 这条降级链路的起点。一旦这里漏写,老日志会被误判成新日志并算出错误百分比。
func TestHooksAreNoOpWhenDisabled(t *testing.T) {
	loadConfig(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
log_metrics:
  show_reasoning_effort: false
  show_cache_ratio: false
`)

	other := map[string]interface{}{"cache_tokens": 4000, "prompt_tokens": 10000}
	AttachReasoning(nil, &relaycommon.RelayInfo{ReasoningEffort: "high"}, other)
	AttachCacheBasis(other, 10000, 4000, 0, false)

	assert.Equal(t, map[string]interface{}{"cache_tokens": 4000, "prompt_tokens": 10000}, other)
}

// 扩展整体未启用(没有配置文件)时同样必须零痕迹。
func TestHooksAreNoOpWhenExtensionDisabled(t *testing.T) {
	loadConfig(t, "enabled: false\n")

	other := map[string]interface{}{}
	AttachReasoning(nil, &relaycommon.RelayInfo{ReasoningEffort: "high"}, other)
	AttachCacheBasis(other, 10000, 4000, 0, true)
	assert.Empty(t, other)
}

// 水位线必须无条件写入,哪怕这次请求根本没用思考模型 ——
// 它标记的是「这条日志可判定」,不是「这条日志有推理数据」。
func TestVersionWatermarkIsAlwaysWritten(t *testing.T) {
	loadConfig(t, bothColumnsOn)

	other := map[string]interface{}{}
	AttachReasoning(nil, &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Model: "gpt-4o"}}, other)

	assert.Equal(t, LogVersion, other[KeyVer])
	assert.NotContains(t, other, KeyReasoning)
}

func TestAttachReasoningWritesNormalizedPayload(t *testing.T) {
	loadConfig(t, bothColumnsOn)

	other := map[string]interface{}{}
	AttachReasoning(nil, &relaycommon.RelayInfo{
		Request: &dto.ClaudeRequest{Thinking: &dto.Thinking{Type: "enabled", BudgetTokens: intPtr(24576)}},
	}, other)

	payload, ok := other[KeyReasoning].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, LevelHigh, payload["level"])
	assert.Equal(t, 24576, payload["budget"])
	assert.Equal(t, SrcClaudeThinking, payload["src"])
	// Raw 必须保留原值:归一化丢掉的精确预算要能在 tooltip 里还原
	assert.Equal(t, "budget:24576", payload["raw"])
}

// qy_semantic 无条件双向写入,补上游 usage_semantic 只写 anthropic 单边的缺口。
// 缺失从此只意味着一件事:本 hook 没跑过。
func TestAttachCacheBasisWritesSemanticBothWays(t *testing.T) {
	loadConfig(t, bothColumnsOn)

	openAI := map[string]interface{}{}
	AttachCacheBasis(openAI, 10000, 4000, 0, false)
	assert.Equal(t, SemanticOpenAI, openAI[KeySemantic])
	assert.Equal(t, 10000, openAI[KeyInputTotal])
	assert.Equal(t, 4000, openAI[KeyCacheRead])
	assert.NotContains(t, openAI, KeyCacheWrite)
	assert.NotContains(t, openAI, KeyCacheAnomaly)

	claude := map[string]interface{}{}
	AttachCacheBasis(claude, 2000, 6000, 1000, true)
	assert.Equal(t, SemanticAnthropic, claude[KeySemantic])
	assert.Equal(t, 9000, claude[KeyInputTotal])
	assert.Equal(t, 6000, claude[KeyCacheRead])
	assert.Equal(t, 1000, claude[KeyCacheWrite])
}

// 同一组 usage 在两种语义下必须得出不同的分母 —— 这是本模块存在的全部理由。
// 若两者相同,说明语义判别被短路了,老日志的误算风险会重新出现。
func TestSemanticChangesDenominator(t *testing.T) {
	openAI := computeCacheBasis(2000, 6000, 1000, false)
	claude := computeCacheBasis(2000, 6000, 1000, true)
	assert.NotEqual(t, openAI.InputTotal, claude.InputTotal)
	assert.Equal(t, 6000, openAI.InputTotal) // 分子大于 prompt,被抬高
	assert.True(t, openAI.Anomaly)           // 且必须标记异常
	assert.Equal(t, 9000, claude.InputTotal)
	assert.False(t, claude.Anomaly)
}

func TestAttachCacheBasisMarksAnomaly(t *testing.T) {
	loadConfig(t, bothColumnsOn)

	other := map[string]interface{}{}
	AttachCacheBasis(other, 100, 5000, 0, false)
	assert.Equal(t, true, other[KeyCacheAnomaly])
	assert.Equal(t, 5000, other[KeyInputTotal])
}
