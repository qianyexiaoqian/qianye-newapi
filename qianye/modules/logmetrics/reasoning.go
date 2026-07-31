package logmetrics

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// 归一化档位。跨厂商统一分档必然是近似的 —— 这正是必须同时保留 Raw 与 Budget
// 原值的原因:档位只用于快速扫视与配色,tooltip 展示精确数字。
const (
	LevelNone    = "none"
	LevelMinimal = "minimal"
	LevelLow     = "low"
	LevelMedium  = "medium"
	LevelHigh    = "high"
	LevelMax     = "max"
	LevelAuto    = "auto"
)

// 数据来源。用于评估各厂商的覆盖率,也是排障时判断「探测走了哪条分支」的唯一线索。
const (
	SrcRelayInfo      = "relay_info"
	SrcClaudeThinking = "claude_thinking"
	SrcClaudeEffort   = "claude_effort"
	SrcGeminiBudget   = "gemini_budget"
	SrcGeminiLevel    = "gemini_level"
	SrcOAIReasoning   = "oai_reasoning"
	SrcOAIEffort      = "oai_effort"
	SrcQwen           = "qwen"
	SrcVendorThinking = "vendor_thinking"
	SrcOllamaThink    = "ollama_think"
)

// 数值预算(tokens)→ 档位的阈值,左开右闭。
//
// 依据:Claude 官方最低 budget_tokens 是 1024(低于此值 API 直接拒绝),
// 故 minimal 上界取 1024;Gemini 2.5 Flash 动态预算上限 24576、Pro 上限 32768,
// 故 high 上界取 32768,超过即 max。
//
// 刻意硬编码而非做成配置项:阈值必须前后端完全一致,多一个可配项就多一处漂移来源;
// 真需要精确值时前端读 Budget 原值即可。
const (
	BudgetMinimalMax = 1024
	BudgetLowMax     = 4096
	BudgetMediumMax  = 16384
	BudgetHighMax    = 32768
)

// maxRawJSONBytes 限制单个透传 JSON 字段的解析体积。
//
// reasoning / thinking / think 这些字段是客户端可控的 json.RawMessage,
// 理论上可以塞进整个 128MB 的请求体。真实取值都是几十字节的小对象,
// 超过这个量级一定不是正常用法,直接放弃探测比拖慢结算链路划算。
const maxRawJSONBytes = 4096

// Reasoning 是归一化后的推理强度。
type Reasoning struct {
	// Level 是统一档位,用于表格展示与配色。
	Level string
	// Raw 保留厂商原值。归一化必然丢信息,排障与 tooltip 都要能还原真相。
	Raw string
	// Budget 是数值型思考预算(tokens)。无数值口径时为 0;
	// Gemini 的动态预算 -1 原样保留,它与「预算为 0(不思考)」语义完全不同。
	Budget int
	// Src 记录数据来自哪条探测分支。
	Src string
}

// detectReasoning 按优先级取第一个命中的信号。
//
// relayInfo.ReasoningEffort 优先:它是 adaptor 在转换请求时算出来的结果,
// 已经考虑了模型名后缀、渠道差异等上下文,比直接读原始请求更准确,而且零解析开销。
// 只有它为空时才回退到原始请求 DTO 探测。
func detectReasoning(relayInfo *relaycommon.RelayInfo) *Reasoning {
	if relayInfo == nil {
		return nil
	}
	if r := fromEffort(relayInfo.ReasoningEffort, SrcRelayInfo); r != nil {
		return r
	}
	return fromRequest(relayInfo.Request)
}

// fromRequest 从客户端原始请求 DTO 探测思考参数。
//
// 这里读的是 relayInfo.Request,即 helper.GetAndValidateRequest 解析出来的原始对象。
// 各 relay handler(claude/compatible/gemini/responses)在改写前都会先 common.DeepCopy,
// 因此这个对象始终保持客户端传入的原貌,不会被 adaptor 的清空逻辑污染。
//
// 用类型断言而不是重新解析 body:body 探测需要访问 KeyBodyStorage、判空、
// 限体积、Seek 复位,并且此刻 CleanupBodyStorage 可能已经跑过;
// 而 DTO 就在手边,零 IO、零分配、零副作用。
func fromRequest(req dto.Request) *Reasoning {
	switch r := req.(type) {
	case *dto.ClaudeRequest:
		return fromClaudeRequest(r)
	case *dto.GeminiChatRequest:
		return fromGeminiRequest(r)
	case *dto.OpenAIResponsesRequest:
		return fromResponsesRequest(r)
	case *dto.GeneralOpenAIRequest:
		return fromOpenAIRequest(r)
	default:
		// 嵌入、音频、图片、rerank 等请求没有思考参数;
		// req 为 nil 接口时也落到这里。
		return nil
	}
}

func fromClaudeRequest(r *dto.ClaudeRequest) *Reasoning {
	if r == nil {
		return nil
	}
	if t := r.Thinking; t != nil {
		// type=disabled 是客户端明确表达的「不要思考」,与「没传 thinking」不同,
		// 值得记下来 —— 用户排查成本时想知道自己到底关没关。
		if strings.EqualFold(strings.TrimSpace(t.Type), "disabled") {
			return &Reasoning{Level: LevelNone, Raw: "disabled", Src: SrcClaudeThinking}
		}
		if t.BudgetTokens != nil {
			return fromBudget(*t.BudgetTokens, SrcClaudeThinking)
		}
		if t.Type != "" {
			return fromEffort(t.Type, SrcClaudeThinking)
		}
	}
	// Claude Opus 4.6+ 用 output_config.effort 表达强度,与 thinking 并存。
	if len(r.OutputConfig) > 0 && len(r.OutputConfig) <= maxRawJSONBytes {
		var cfg dto.OutputConfigForEffort
		if err := common.Unmarshal(r.OutputConfig, &cfg); err == nil {
			return fromEffort(cfg.Effort, SrcClaudeEffort)
		}
	}
	return nil
}

func fromGeminiRequest(r *dto.GeminiChatRequest) *Reasoning {
	if r == nil {
		return nil
	}
	tc := r.GenerationConfig.ThinkingConfig
	if tc == nil {
		return nil
	}
	// 数值预算优先于枚举 level:两者并存时 Gemini 以预算为准,
	// 而且预算是更精确的信息。
	if tc.ThinkingBudget != nil {
		return fromBudget(*tc.ThinkingBudget, SrcGeminiBudget)
	}
	return fromEffort(tc.ThinkingLevel, SrcGeminiLevel)
	// 刻意不把 IncludeThoughts 单独当作信号:它只控制「是否返回思考摘要」,
	// 与思考强度无关,当成 auto 上报会让没有配置预算的普通请求全部亮起档位。
}

func fromResponsesRequest(r *dto.OpenAIResponsesRequest) *Reasoning {
	if r == nil {
		return nil
	}
	if r.Reasoning != nil {
		if v := fromEffort(r.Reasoning.Effort, SrcOAIReasoning); v != nil {
			return v
		}
	}
	return fromQwenThinking(r.EnableThinking, r.ThinkingBudget)
}

func fromOpenAIRequest(r *dto.GeneralOpenAIRequest) *Reasoning {
	if r == nil {
		return nil
	}
	// 通用 OpenAI 兼容渠道的主要缺口:上游只在 o 系列 / GPT-5 分支里
	// 把 reasoning_effort 回填进 relayInfo,其余渠道原样透传但不落日志。
	if v := fromEffort(r.ReasoningEffort, SrcOAIEffort); v != nil {
		return v
	}
	if v := fromOpenRouterReasoning(r.Reasoning); v != nil {
		return v
	}
	if v := fromQwenThinking(r.EnableThinking, r.ThinkingBudget); v != nil {
		return v
	}
	if v := fromVendorThinking(r.THINKING); v != nil {
		return v
	}
	return fromOllamaThink(r.Think)
}

// fromOpenRouterReasoning 解析 OpenRouter 的 reasoning 对象:
// {"effort":"high"} / {"max_tokens":2048} / {"enabled":true} / {"exclude":true}。
func fromOpenRouterReasoning(raw json.RawMessage) *Reasoning {
	if !parsableObject(raw) {
		return nil
	}
	var payload struct {
		Effort    string `json:"effort"`
		MaxTokens *int   `json:"max_tokens"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := common.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	if v := fromEffort(payload.Effort, SrcOAIReasoning); v != nil {
		return v
	}
	if payload.MaxTokens != nil {
		return fromBudget(*payload.MaxTokens, SrcOAIReasoning)
	}
	if payload.Enabled != nil {
		return boolReasoning(*payload.Enabled, "reasoning.enabled", SrcOAIReasoning)
	}
	return nil
}

// fromQwenThinking 解析 Qwen 的 enable_thinking / thinking_budget 组合。
//
// 先看开关:enable_thinking=false 时即便带了预算也不会思考,
// 反过来按预算上报会得到一个「高强度思考」的假象。
func fromQwenThinking(enable, budget json.RawMessage) *Reasoning {
	switch strings.TrimSpace(string(enable)) {
	case "false":
		return &Reasoning{Level: LevelNone, Raw: "enable_thinking:false", Src: SrcQwen}
	case "true":
		if b, ok := rawTokenBudget(budget); ok {
			return fromBudget(b, SrcQwen)
		}
		return &Reasoning{Level: LevelMedium, Raw: "enable_thinking:true", Src: SrcQwen}
	}
	if b, ok := rawTokenBudget(budget); ok {
		return fromBudget(b, SrcQwen)
	}
	return nil
}

// fromVendorThinking 解析 doubao / zhipu 的 thinking 对象:{"type":"enabled"|"disabled"|"auto"}。
func fromVendorThinking(raw json.RawMessage) *Reasoning {
	if !parsableObject(raw) {
		return nil
	}
	var payload struct {
		Type string `json:"type"`
	}
	if err := common.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return fromEffort(payload.Type, SrcVendorThinking)
}

// fromOllamaThink 解析 ollama 的 think 字段:true / false / "low"|"medium"|"high"。
func fromOllamaThink(raw json.RawMessage) *Reasoning {
	trimmed := strings.TrimSpace(string(raw))
	switch trimmed {
	case "", "null":
		return nil
	case "true":
		return boolReasoning(true, "think", SrcOllamaThink)
	case "false":
		return boolReasoning(false, "think", SrcOllamaThink)
	}
	if len(trimmed) > maxRawJSONBytes {
		return nil
	}
	var level string
	if err := common.Unmarshal(raw, &level); err != nil {
		return nil
	}
	return fromEffort(level, SrcOllamaThink)
}

// ───────────────────────────── 归一化 ─────────────────────────────

// NormalizeEffort 把各厂商的枚举口径归一到统一档位。
func NormalizeEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "none", "off", "no", "false", "disable", "disabled", "nothinking":
		return LevelNone
	case "minimal", "min", "very_low", "very-low":
		return LevelMinimal
	case "low":
		return LevelLow
	case "medium", "mid", "moderate", "default", "standard", "enabled", "enable", "true":
		return LevelMedium
	case "high":
		return LevelHigh
	case "xhigh", "x-high", "extra_high", "max", "maximum", "highest", "ultra":
		return LevelMax
	case "auto", "dynamic", "adaptive":
		return LevelAuto
	default:
		// 未知口径保守居中。归成 none 会让用户以为没在花思考的钱,
		// 归成 max 又会引发不必要的成本恐慌;Raw 原值一并留存,tooltip 里还原真相。
		return LevelMedium
	}
}

// LevelForBudget 把数值预算映射为档位。
func LevelForBudget(budget int) string {
	switch {
	case budget < 0:
		// Gemini 用 -1 表示「由模型自行决定」,成本不可预估,单列一档。
		// 其余负数是畸形输入,同样按不可预估处理,不要归成 none。
		return LevelAuto
	case budget == 0:
		return LevelNone
	case budget <= BudgetMinimalMax:
		return LevelMinimal
	case budget <= BudgetLowMax:
		return LevelLow
	case budget <= BudgetMediumMax:
		return LevelMedium
	case budget <= BudgetHighMax:
		return LevelHigh
	default:
		return LevelMax
	}
}

func fromEffort(raw, src string) *Reasoning {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return &Reasoning{Level: NormalizeEffort(raw), Raw: raw, Src: src}
}

func fromBudget(budget int, src string) *Reasoning {
	budget = clampBudget(budget)
	return &Reasoning{
		Level:  LevelForBudget(budget),
		Raw:    "budget:" + strconv.Itoa(budget),
		Budget: budget,
		Src:    src,
	}
}

func boolReasoning(on bool, field, src string) *Reasoning {
	if on {
		// 开了思考但没给预算时,各厂商默认值大多落在 medium 区间。
		return &Reasoning{Level: LevelMedium, Raw: field + ":true", Src: src}
	}
	return &Reasoning{Level: LevelNone, Raw: field + ":false", Src: src}
}

// clampBudget 把预算钳进可安全落库的范围。
//
// 值来自客户端 JSON,可能是 1e300 或 -999。负数一律折叠成 -1(动态预算),
// 上界钳到 int32 —— 只做边界钳制,不做任何比例换算,这里不涉及金额。
func clampBudget(budget int) int {
	if budget < 0 {
		return -1
	}
	if budget > maxTokenCount {
		return maxTokenCount
	}
	return budget
}

// rawTokenBudget 从 json.RawMessage 里取一个 token 预算。
//
// 用 float64 而不是整型解析:客户端可能传 4096.0 或 1e5,
// 整型解析会直接失败;预算量级远小于 2^53,float64 精确表示无损。
func rawTokenBudget(raw json.RawMessage) (int, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || len(trimmed) > maxRawJSONBytes {
		return 0, false
	}
	var f float64
	if err := common.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	// 先在 float64 域内钳到 int32 范围再转换,避免超范围转换的未定义结果。
	if f > maxTokenCount {
		return maxTokenCount, true
	}
	if f < 0 {
		return -1, true
	}
	return int(f), true
}

// parsableObject 判断一个透传字段是否值得解析成对象。
func parsableObject(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > maxRawJSONBytes {
		return false
	}
	return common.GetJsonType(raw) == "object"
}
