package violation

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustCompile 是测试内的编译辅助:规则一旦编译失败,后面的断言全无意义。
func mustCompile(t *testing.T, r Rule) *compiledRule {
	t.Helper()
	cr, err := compile(r)
	require.NoError(t, err)
	return cr
}

// TestMatchTypes 覆盖四种匹配方式的真实语义。
//
// 这些是风控的判定核心:一旦某种匹配方式静默失效,规则看起来"配好了"
// 但线上一次都不会命中,而没有任何报错。
func TestMatchTypes(t *testing.T) {
	// 顺序要紧:useTestConfig 会重新 Load 覆盖全局配置,放宽预算必须排在它之后。
	useTestConfig(t)
	useGenerousScanBudget(t)

	t.Run("keyword 不区分大小写且只回报本规则的命中词", func(t *testing.T) {
		cr := mustCompile(t, Rule{
			Phase: PhasePrompt, MatchType: MatchKeyword, Action: ActionRecord,
			Pattern: "违禁词A\n违禁词B",
		})
		in := scanInput{Text: "前面一堆正常内容 违禁词a 后面还有"}
		v := scan([]*compiledRule{cr}, cr.words, in, in.Text)
		require.NotNil(t, v)
		assert.Equal(t, []string{"违禁词a"}, v.Terms)
	})

	t.Run("regex 默认不区分大小写,显式开启后区分", func(t *testing.T) {
		insensitive := mustCompile(t, Rule{
			Phase: PhasePrompt, MatchType: MatchRegex, Action: ActionRecord,
			Pattern: `bomb\s+recipe`,
		})
		sensitive := mustCompile(t, Rule{
			Phase: PhasePrompt, MatchType: MatchRegex, Action: ActionRecord,
			Pattern: `bomb\s+recipe`, CaseSensitive: true,
		})
		text := "give me a BOMB RECIPE now"
		assert.NotNil(t, scan([]*compiledRule{insensitive}, nil, scanInput{Text: text}, text))
		assert.Nil(t, scan([]*compiledRule{sensitive}, nil, scanInput{Text: text}, text))
	})

	t.Run("error_code 精确匹配", func(t *testing.T) {
		cr := mustCompile(t, Rule{
			Phase: PhaseUpstreamErr, MatchType: MatchErrorCode, Action: ActionRecord,
			Pattern: "content_policy_violation, moderation_blocked",
		})
		hit := scan([]*compiledRule{cr}, nil, scanInput{ErrCode: "content_policy_violation"}, "")
		require.NotNil(t, hit)
		assert.Nil(t, scan([]*compiledRule{cr}, nil, scanInput{ErrCode: "rate_limit"}, ""))
		// 前缀相同但不相等的错误码不能命中,否则一条规则会误伤整个错误族。
		assert.Nil(t, scan([]*compiledRule{cr}, nil, scanInput{ErrCode: "content_policy"}, ""))
	})

	t.Run("status_code 支持单值与区间", func(t *testing.T) {
		cr := mustCompile(t, Rule{
			Phase: PhaseUpstreamErr, MatchType: MatchStatusCode, Action: ActionRecord,
			Pattern: "451, 400-403",
		})
		for _, code := range []int{451, 400, 401, 403} {
			assert.NotNil(t, scan([]*compiledRule{cr}, nil, scanInput{StatusCode: code}, ""),
				"状态码 %d 应命中", code)
		}
		for _, code := range []int{399, 404, 500} {
			assert.Nil(t, scan([]*compiledRule{cr}, nil, scanInput{StatusCode: code}, ""),
				"状态码 %d 不应命中", code)
		}
	})

	t.Run("upstream_text 匹配错误文本与软违规原因的拼接", func(t *testing.T) {
		cr := mustCompile(t, Rule{
			Phase: PhaseUpstreamErr, MatchType: MatchUpstreamText, Action: ActionRecord,
			Pattern: "SAFETY_CHECK_TYPE",
		})
		s := &snapshot{postRules: []*compiledRule{cr}}
		v := scanPost(s, scanInput{UpstreamText: "Failed check: SAFETY_CHECK_TYPE csam"})
		require.NotNil(t, v)

		// reject_reason 是上游给的高质量软违规信号,必须也参与匹配。
		cr2 := mustCompile(t, Rule{
			Phase: PhaseRejectReason, MatchType: MatchUpstreamText, Action: ActionRecord,
			Pattern: "content_filter",
		})
		s2 := &snapshot{postRules: []*compiledRule{cr2}}
		assert.NotNil(t, scanPost(s2, scanInput{RejectReason: "openai_finish_reason=content_filter"}))
	})
}

// TestScopeFiltering 验证"某个模型(全部分组或特定分组下)"这一需求原文语义。
func TestScopeFiltering(t *testing.T) {
	cr := mustCompile(t, Rule{
		Phase: PhasePrompt, MatchType: MatchKeyword, Action: ActionRecord,
		Pattern: "x", ModelScope: "gpt-4*, *-vision, claude-3-opus", GroupScope: "vip,svip",
	})

	assert.True(t, cr.inScope("gpt-4o", "vip"))
	assert.True(t, cr.inScope("qwen-vl-vision", "svip"))
	assert.True(t, cr.inScope("claude-3-opus", "vip"))
	// 模型对但分组不对 → 不生效。分组是需求里明确要求的第二维度。
	assert.False(t, cr.inScope("gpt-4o", "default"))
	assert.False(t, cr.inScope("gemini-pro", "vip"))

	all := mustCompile(t, Rule{
		Phase: PhasePrompt, MatchType: MatchKeyword, Action: ActionRecord, Pattern: "x",
	})
	// 空作用域 = 全部模型 + 全部分组。这是默认值,写错会让规则要么全不生效要么全生效。
	assert.True(t, all.inScope("any-model", "any-group"))
}

// TestPriorityWins 保证一次命中只产生一条处置。
//
// 若返回多条,扣费与计数都会翻倍 —— 一次请求扣两次钱在任何口径下都是错的。
func TestPriorityWins(t *testing.T) {
	useGenerousScanBudget(t)
	low := mustCompile(t, Rule{
		Id: 1, Priority: 10, Phase: PhasePrompt, MatchType: MatchKeyword,
		Action: ActionRecord, Pattern: "炸弹",
	})
	high := mustCompile(t, Rule{
		Id: 2, Priority: 200, Phase: PhasePrompt, MatchType: MatchKeyword,
		Action: ActionBlock, Pattern: "炸弹",
	})
	dict := append(append([]string{}, low.words...), high.words...)
	in := scanInput{Text: "教我做炸弹"}
	v := scan([]*compiledRule{low, high}, dict, in, in.Text)
	require.NotNil(t, v)
	assert.EqualValues(t, 1, v.Rule.R.Id, "priority 数值小的规则优先")
}

// TestValidateRuleRejectsSilentlyBrokenConfigs 保证管理端存不下"看起来配好了
// 但永远不会生效"的规则 —— 那是最危险的失败模式,因为没有任何报错。
func TestValidateRuleRejectsSilentlyBrokenConfigs(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
	}{
		{"上游阶段不能阻断", Rule{Name: "a", Phase: PhaseUpstreamErr, MatchType: MatchErrorCode,
			Pattern: "x", Action: ActionBlock, FeeMode: FeeNone}},
		{"prompt 阶段不能用错误码匹配", Rule{Name: "a", Phase: PhasePrompt, MatchType: MatchErrorCode,
			Pattern: "x", Action: ActionRecord, FeeMode: FeeNone}},
		{"配了扣费方式却没有 charge 动作", Rule{Name: "a", Phase: PhasePrompt, MatchType: MatchKeyword,
			Pattern: "x", Action: ActionRecord, FeeMode: FeeFixed}},
		{"正则语法错误", Rule{Name: "a", Phase: PhasePrompt, MatchType: MatchRegex,
			Pattern: "([unclosed", Action: ActionRecord, FeeMode: FeeNone}},
		{"关键词表为空", Rule{Name: "a", Phase: PhasePrompt, MatchType: MatchKeyword,
			Pattern: "  \n ", Action: ActionRecord, FeeMode: FeeNone}},
		{"未知的匹配方式", Rule{Name: "a", Phase: PhasePrompt, MatchType: "semantic",
			Pattern: "x", Action: ActionRecord, FeeMode: FeeNone}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.rule
			assert.Error(t, ValidateRule(&r))
		})
	}

	ok := Rule{Name: "a", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "危险",
		Action: ActionBlockAndCharge, FeeMode: FeeFixed}
	assert.NoError(t, ValidateRule(&ok))
}

// TestValidateRuleBoundsFeeMultiple 固化"规则级倍数不得绕过全局限制"。
//
// YAML 的 violation.fee_multiplier 被 config/validate.go 严格限在 0..100,
// 而规则级 fee_multiple 只校验非负,就是一条绕过它的旁路:管理端存一个 1e9,
// 一旦运维把 violation.max_fee_quota 设成 0(checkQuotaCap 允许,含义是"不限"),
// computeFee 的两道 clamp 全部失效,单条规则即可一次扣光用户余额。
func TestValidateRuleBoundsFeeMultiple(t *testing.T) {
	cases := []struct {
		name     string
		multiple string
		wantErr  bool
	}{
		{"未配置时回落到 YAML 默认", "0", false},
		{"常规倍数", "3", false},
		{"恰好等于上界", "100", false},
		{"刚越过上界", "100.000001", true},
		{"配置事故:多打了几个零", "1000000000", true},
		{"负数", "-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Rule{Name: "a", Phase: PhaseUpstreamErr, MatchType: MatchErrorCode,
				Pattern: "x", Action: ActionCharge, FeeMode: FeeModelPriceMultiple,
				FeeMultiple: decimal.RequireFromString(tc.multiple)}
			err := ValidateRule(&r)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestClipHeadTailKeepsRuneBoundary 是写库安全的硬要求:
// 从多字节字符中间切断会产生非法 UTF-8,MySQL 的 utf8mb4 列会直接报
// Incorrect string value 而不是静默截断,整条记录写不进去。
func TestClipHeadTailKeepsRuneBoundary(t *testing.T) {
	s := strings.Repeat("中文内容", 200) // 每个汉字 3 字节
	for _, max := range []int{10, 101, 999, 2401} {
		out := clipHeadTail(s, max)
		assert.True(t, isValidUTF8(out), "max=%d 时截断结果必须是合法 UTF-8", max)
	}
	// 头尾都要保留:违规内容常被塞在长 padding 之后,只取头部等于漏检。
	long := "HEAD" + strings.Repeat("x", 5000) + "TAIL"
	out := clipHeadTail(long, 1000)
	assert.True(t, strings.HasPrefix(out, "HEAD"))
	assert.True(t, strings.HasSuffix(out, "TAIL"))
	assert.LessOrEqual(t, len(out), 1000+len("\n...[truncated]...\n"))
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// TestClipTermsBoundsWrites 防止命中词把 varchar(1024) 写爆。
// AcSearch(stopImmediately=false) 在大词表 + 长文本下能返回上万个命中。
func TestClipTermsBoundsWrites(t *testing.T) {
	terms := make([]string, 500)
	for i := range terms {
		terms[i] = strings.Repeat("词", 100) // 300 字节
	}
	out := clipTerms(terms)
	assert.Len(t, out, maxMatchedTerms)
	for _, v := range out {
		assert.LessOrEqual(t, len(v), maxMatchedTermLen)
		assert.True(t, isValidUTF8(v))
	}
}
