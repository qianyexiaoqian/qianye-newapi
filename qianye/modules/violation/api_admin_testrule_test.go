package violation

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_testrule_test.go —— 规则试跑的回归。
//
// 试跑面板要回答的是"这条规则的逻辑对不对",所以它必须按规则真正会读的维度
// 收样本(上下文 / 上游正文 / 状态码 / 错误码 / 频率计数),而不是雷打不动地
// 问"模型 / 分组"。这里守住三件事:
//
//  1. 字段映射:每种 match_type × phase 组合问且只问它会读到的字段;
//  2. 三态结论:不在作用域 / 在作用域但没命中 / 命中,必须彼此可分 ——
//     折叠成一个布尔时,管理员分不清"规则写错了"和"我样本填得不对";
//  3. 逐字段对齐:线上 scanInput 新增一个字段而试跑没跟上,这里就变红。
//     这一条是本文件存在的主要理由 —— 那种漏字段的失效不会报错,只表现为
//     "试跑说不命中、线上却命中",而那正是这个面板最坏的失效方式。

// scanInputTestCoverage 是**线上判据字段 → 试跑输入标识**的完整映射。
//
// 它是第 3 条守卫的锚:scanInput 每加一个字段,都必须在这里给出它对应的试跑
// 输入,否则 TestScanInputFullyCoveredByRuleTest 立刻变红。
var scanInputTestCoverage = map[string]string{
	"Model":        TestInputModel,
	"Group":        TestInputGroup,
	"Text":         TestInputRequestText,
	"RateCount":    TestInputRateCount,
	"ErrCode":      TestInputErrorCode,
	"StatusCode":   TestInputStatusCode,
	"UpstreamText": TestInputUpstreamText,
	"RejectReason": TestInputRejectReason,
	"AI":           TestInputAIVerdict,
}

// legalRulePhases 是每种匹配方式被 ValidateRule 允许的阶段。
// 试跑字段映射的完备性只在这些组合上有意义:其余组合根本存不下来。
var legalRulePhases = map[string][]string{
	MatchKeyword:      {PhasePrompt, PhaseUpstreamErr, PhaseRejectReason},
	MatchRegex:        {PhasePrompt, PhaseUpstreamErr, PhaseRejectReason},
	MatchUpstreamText: {PhaseUpstreamErr, PhaseRejectReason},
	MatchErrorCode:    {PhaseUpstreamErr, PhaseRejectReason},
	MatchStatusCode:   {PhaseUpstreamErr, PhaseRejectReason},
	MatchRequestRate:  {PhasePrompt},
	MatchAIReview:     {PhasePrompt, PhasePostAsync},
}

// TestScanInputFullyCoveredByRuleTest 钉住"试跑输入必须与线上的 scanInput 逐字段对齐"。
//
// 三道断言各拦一种漏法:
//
//   - 反射遍历 scanInput → 新增字段没登记进映射表就红;
//   - 每个试跑标识都必须是 ruleTestReq 的一个 JSON 键 → 登记了却没有入参通道就红
//     (那等于界面填得进、后端收不到,静默失效);
//   - 每个试跑标识都必须被至少一种合法规则形态问到 → 有入参通道却没有任何规则
//     会被问这一格,说明字段映射漏了一档。
func TestScanInputFullyCoveredByRuleTest(t *testing.T) {
	t.Run("scanInput 每个字段都有对应的试跑输入", func(t *testing.T) {
		typ := reflect.TypeOf(scanInput{})
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			id, ok := scanInputTestCoverage[name]
			require.Truef(t, ok,
				"scanInput 新增了字段 %s 而试跑没跟上:线上会读它、试跑读不到,"+
					"这一维度的规则在面板里永远显示未命中", name)
			assert.NotEmptyf(t, id, "字段 %s 的试跑输入标识不能为空", name)
		}
	})

	t.Run("每个试跑输入都有请求体入口", func(t *testing.T) {
		keys := map[string]bool{}
		typ := reflect.TypeOf(ruleTestReq{})
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if tag != "" && tag != "-" {
				keys[tag] = true
			}
		}
		for field, id := range scanInputTestCoverage {
			assert.Truef(t, keys[id],
				"ruleTestReq 没有 json 键 %q(scanInput.%s):界面填得进、后端收不到", id, field)
		}
	})

	t.Run("每个试跑输入都被至少一种规则形态问到", func(t *testing.T) {
		asked := map[string]bool{}
		for matchType, phases := range legalRulePhases {
			for _, phase := range phases {
				for _, id := range ruleTestInputs(phase, matchType) {
					asked[id] = true
				}
			}
		}
		for field, id := range scanInputTestCoverage {
			assert.Truef(t, asked[id],
				"没有任何规则形态会被问到 %q(scanInput.%s):字段映射漏了一档", id, field)
		}
	})
}

// TestRuleTestInputsFollowTheRule 是这次改动的正题:试跑问什么,由规则说了算。
func TestRuleTestInputsFollowTheRule(t *testing.T) {
	cases := []struct {
		name      string
		phase     string
		matchType string
		want      []string
	}{
		{
			name: "prompt 关键词只问请求上下文", phase: PhasePrompt, matchType: MatchKeyword,
			want: []string{TestInputRequestText, TestInputModel, TestInputGroup},
		},
		{
			name: "prompt 正则同样只问请求上下文", phase: PhasePrompt, matchType: MatchRegex,
			want: []string{TestInputRequestText, TestInputModel, TestInputGroup},
		},
		{
			name:  "频率规则只问计数,一个文本框都不该出现",
			phase: PhasePrompt, matchType: MatchRequestRate,
			want: []string{TestInputRateCount, TestInputModel, TestInputGroup},
		},
		{
			name:  "上游正文规则问上游正文 + 拒绝原因 + 状态码",
			phase: PhaseUpstreamErr, matchType: MatchUpstreamText,
			want: []string{TestInputUpstreamText, TestInputRejectReason, TestInputStatusCode,
				TestInputModel, TestInputGroup},
		},
		{
			name:  "上游阶段的关键词规则扫的是上游文本,不是请求上下文",
			phase: PhaseUpstreamErr, matchType: MatchKeyword,
			want: []string{TestInputUpstreamText, TestInputRejectReason, TestInputStatusCode,
				TestInputModel, TestInputGroup},
		},
		{
			name:  "软违规阶段同样两段文本都要问",
			phase: PhaseRejectReason, matchType: MatchRegex,
			want: []string{TestInputUpstreamText, TestInputRejectReason, TestInputStatusCode,
				TestInputModel, TestInputGroup},
		},
		{
			name:  "错误码规则问错误码,外加状态码作用域那一格",
			phase: PhaseUpstreamErr, matchType: MatchErrorCode,
			want: []string{TestInputErrorCode, TestInputStatusCode, TestInputModel, TestInputGroup},
		},
		{
			name:  "状态码规则只问一次状态码,不重复",
			phase: PhaseUpstreamErr, matchType: MatchStatusCode,
			want: []string{TestInputStatusCode, TestInputModel, TestInputGroup},
		},
		{
			// AI 规则读的是外部模型的结论,不是上下文本身。问一段文本等于把面板
			// 变成"我猜模型会怎么判",而那正是这条规则唯一无法在本地复现的一步。
			name:  "AI 审核规则问结论/类型/置信度,不问任何文本",
			phase: PhasePrompt, matchType: MatchAIReview,
			want: []string{TestInputAIVerdict, TestInputAICategory, TestInputAIConfidence,
				TestInputModel, TestInputGroup},
		},
		{
			// post_async 跑在异步 worker 上,手上没有上游响应。多摆一格填了也不
			// 生效的状态码输入,与这次改动要修的毛病同形。
			name:  "转发后异步阶段不问状态码",
			phase: PhasePostAsync, matchType: MatchAIReview,
			want: []string{TestInputAIVerdict, TestInputAICategory, TestInputAIConfidence,
				TestInputModel, TestInputGroup},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ruleTestInputs(tc.phase, tc.matchType))
		})
	}
}

// TestRuleTestOutcomeThreeStates 是三态的表驱动:同一条规则,只换样本,
// 就必须能分别落到"不在作用域""在作用域但没命中""命中"这三格上。
func TestRuleTestOutcomeThreeStates(t *testing.T) {
	useGenerousScanBudget(t)
	// AI 类型的归一读的是快照里的类型闭集(与线上同一份),零值快照会把
	// 每一个类型都折进兜底,于是这一批用例断言的就不再是它们要测的东西。
	installSeedVocabSnapshot(t)

	cases := []struct {
		name      string
		rule      Rule
		req       ruleTestReq
		outcome   string
		scopeFail string
		terms     []string
		blanks    []string
	}{
		{
			name:    "关键词命中",
			rule:    Rule{Name: "kw", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "违禁词"},
			req:     ruleTestReq{RequestText: strptr("前面正常内容 违禁词 后面")},
			outcome: TestOutcomeMatched, terms: []string{"违禁词"},
			blanks: []string{},
		},
		{
			name:    "关键词在作用域内但没命中",
			rule:    Rule{Name: "kw", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "违禁词"},
			req:     ruleTestReq{RequestText: strptr("一段完全正常的话")},
			outcome: TestOutcomeNoMatch, blanks: []string{},
		},
		{
			name: "样本本来会命中,但分组作用域把它挡在门外",
			rule: Rule{Name: "kw", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "违禁词",
				GroupScope: "vip", GroupScopeMode: GroupScopeInclude},
			req:       ruleTestReq{RequestText: strptr("违禁词"), Group: "default"},
			outcome:   TestOutcomeOutOfScope,
			scopeFail: TestScopeFailGroup,
			blanks:    []string{},
		},
		{
			name: "模型作用域挡住时报的是模型这一道闸",
			rule: Rule{Name: "kw", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "违禁词",
				ModelScope: "gpt-4*"},
			req:       ruleTestReq{RequestText: strptr("违禁词"), Model: "claude-3-opus"},
			outcome:   TestOutcomeOutOfScope,
			scopeFail: TestScopeFailModel,
			blanks:    []string{},
		},
		{
			name: "状态码作用域挡住时报的是状态码这一道闸",
			rule: Rule{Name: "up", Phase: PhaseUpstreamErr, MatchType: MatchUpstreamText,
				Pattern: "cybersecurity risk", StatusScope: "400"},
			req: ruleTestReq{
				UpstreamText: strptr("This content was flagged for possible cybersecurity risk"),
				StatusCode:   500,
			},
			outcome:   TestOutcomeOutOfScope,
			scopeFail: TestScopeFailStatus,
			blanks:    []string{},
		},
		{
			name: "状态码对上之后,同一段正文就命中了",
			rule: Rule{Name: "up", Phase: PhaseUpstreamErr, MatchType: MatchUpstreamText,
				Pattern: "cybersecurity risk", StatusScope: "400"},
			req: ruleTestReq{
				UpstreamText: strptr("This content was flagged for possible cybersecurity risk"),
				StatusCode:   400,
			},
			outcome: TestOutcomeMatched,
			terms:   []string{"cybersecurity risk"},
			blanks:  []string{},
		},
		{
			name: "上游正文留空时,面板必须点名说是这一格没填",
			rule: Rule{Name: "up", Phase: PhaseUpstreamErr, MatchType: MatchUpstreamText,
				Pattern: "cybersecurity risk"},
			req:     ruleTestReq{UpstreamText: strptr(""), StatusCode: 400},
			outcome: TestOutcomeNoMatch,
			blanks:  []string{TestInputUpstreamText},
		},
		{
			name: "只填了拒绝原因也算填了文本 —— 线上就是拼在一起扫的",
			rule: Rule{Name: "soft", Phase: PhaseRejectReason, MatchType: MatchKeyword,
				Pattern: "content_filter"},
			req: ruleTestReq{
				UpstreamText: strptr(""),
				RejectReason: "openai_finish_reason=content_filter",
				StatusCode:   400,
			},
			outcome: TestOutcomeMatched,
			terms:   []string{"content_filter"},
			blanks:  []string{},
		},
		{
			name: "错误码规则:错误码没填就直说,别让人去改模式串",
			rule: Rule{Name: "ec", Phase: PhaseUpstreamErr, MatchType: MatchErrorCode,
				Pattern: "content_policy_violation"},
			req:     ruleTestReq{StatusCode: 400},
			outcome: TestOutcomeNoMatch,
			blanks:  []string{TestInputErrorCode},
		},
		{
			name: "错误码规则:填对就命中",
			rule: Rule{Name: "ec", Phase: PhaseUpstreamErr, MatchType: MatchErrorCode,
				Pattern: "content_policy_violation"},
			req:     ruleTestReq{ErrorCode: "content_policy_violation", StatusCode: 400},
			outcome: TestOutcomeMatched,
			terms:   []string{"content_policy_violation"},
			blanks:  []string{},
		},
		{
			name:    "频率规则:计数没到阈值",
			rule:    Rule{Name: "rate", Phase: PhasePrompt, MatchType: MatchRequestRate, Pattern: "60"},
			req:     ruleTestReq{RateCount: 59},
			outcome: TestOutcomeNoMatch, blanks: []string{},
		},
		{
			name:    "频率规则:计数到阈值就命中,而且一个字节的用户内容都不该被截进证据",
			rule:    Rule{Name: "rate", Phase: PhasePrompt, MatchType: MatchRequestRate, Pattern: "60"},
			req:     ruleTestReq{RateCount: 60, RequestText: strptr("随便一段上下文")},
			outcome: TestOutcomeMatched, terms: []string{"req_rate 60>=60/60s"}, blanks: []string{},
		},
		{
			name:    "频率规则:计数留空时点名说是计数没填",
			rule:    Rule{Name: "rate", Phase: PhasePrompt, MatchType: MatchRequestRate, Pattern: "60"},
			req:     ruleTestReq{RateCount: 0},
			outcome: TestOutcomeNoMatch, blanks: []string{TestInputRateCount},
		},
		{
			name: "AI 规则:没送审就是不命中,而且要点名是结论那一格没填",
			rule: Rule{Name: "ai", Phase: PhasePrompt, MatchType: MatchAIReview, Pattern: "sexual"},
			req:  ruleTestReq{},
			// 空结论 = 没抽中 / 审核失败 / 审核超时,线上一律放行。试跑必须能重现
			// 这一档,否则"失败即放行"这条最重要的性质在面板里根本测不出来。
			outcome: TestOutcomeNoMatch,
			blanks:  []string{TestInputAIVerdict},
		},
		{
			name: "AI 规则:判合规不命中",
			rule: Rule{Name: "ai", Phase: PhasePrompt, MatchType: MatchAIReview, Pattern: "sexual"},
			req: ruleTestReq{AIVerdict: OutcomeClean, AICategory: "sexual",
				AIConfidence: "0.99"},
			outcome: TestOutcomeNoMatch,
			blanks:  []string{},
		},
		{
			name: "AI 规则:判违规但类型不在白名单里,不命中",
			rule: Rule{Name: "ai", Phase: PhasePrompt, MatchType: MatchAIReview, Pattern: "sexual"},
			req: ruleTestReq{AIVerdict: OutcomeViolation, AICategory: "violence",
				AIConfidence: "0.99"},
			outcome: TestOutcomeNoMatch,
			blanks:  []string{},
		},
		{
			name: "AI 规则:置信度差 0.08 就不命中 —— 这正是管理员要来试跑验证的那个问题",
			rule: Rule{Name: "ai", Phase: PhasePrompt, MatchType: MatchAIReview,
				Pattern: "sexual", AIMinConfidence: decimal.RequireFromString("0.8")},
			req: ruleTestReq{AIVerdict: OutcomeViolation, AICategory: "sexual",
				AIConfidence: "0.72"},
			outcome: TestOutcomeNoMatch,
			blanks:  []string{},
		},
		{
			name: "AI 规则:置信度过线就命中,命中词带类型与置信度",
			rule: Rule{Name: "ai", Phase: PhasePrompt, MatchType: MatchAIReview,
				Pattern: "sexual", AIMinConfidence: decimal.RequireFromString("0.8")},
			req: ruleTestReq{AIVerdict: OutcomeViolation, AICategory: "sexual",
				AIConfidence: "0.81"},
			outcome: TestOutcomeMatched,
			terms:   []string{"ai:sexual@0.81"},
			blanks:  []string{},
		},
		{
			name: "旧版 sample_text 仍然同时灌进两段文本(兼容)",
			rule: Rule{Name: "up", Phase: PhaseUpstreamErr, MatchType: MatchUpstreamText,
				Pattern: "denied"},
			req:     ruleTestReq{Sample: "upstream says denied", StatusCode: 400},
			outcome: TestOutcomeMatched,
			terms:   []string{"denied"},
			blanks:  []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.rule
			row.Enabled = true
			cr, err := compile(row)
			require.NoError(t, err)

			in := tc.req.input()
			outcome, scopeFail, v := ruleTestOutcome(cr, row.Phase, in)
			assert.Equal(t, tc.outcome, outcome)
			assert.Equal(t, tc.scopeFail, scopeFail)
			assert.Equal(t, tc.blanks,
				ruleTestBlanks(in, ruleTestInputs(row.Phase, row.MatchType)))
			if tc.terms == nil {
				assert.True(t, v == nil || v.Rule == nil, "不该命中却给出了判定结果")
				return
			}
			require.NotNil(t, v)
			require.NotNil(t, v.Rule)
			assert.Equal(t, tc.terms, v.Terms)
			if row.MatchType == MatchRequestRate {
				assert.Empty(t, v.Snippet,
					"频率命中与文本无关,截一段上下文当证据等于把用户内容抄进管理端列表")
			}
		})
	}
}

// TestRuleTestRequestSeparatesTextChannels 钉住指针字段的语义:
// **缺字段**回落到旧样本框,**显式空串**就是空。
//
// 折叠成普通 string 的后果不是少一个分支:新面板给上游规则只填 upstream_text 时,
// request_text 会被 sample_text 偷偷灌满,于是一条 prompt 规则用上游样本也"命中" ——
// 一个凭空捏造的绿灯。
func TestRuleTestRequestSeparatesTextChannels(t *testing.T) {
	installSeedVocabSnapshot(t)
	t.Run("缺字段回落到 sample_text", func(t *testing.T) {
		var req ruleTestReq
		require.NoError(t, json.Unmarshal([]byte(`{"sample_text":"abc"}`), &req))
		in := req.input()
		assert.Equal(t, "abc", in.Text)
		assert.Equal(t, "abc", in.UpstreamText)
	})

	t.Run("显式空串不被 sample_text 顶上", func(t *testing.T) {
		var req ruleTestReq
		require.NoError(t, json.Unmarshal(
			[]byte(`{"sample_text":"abc","request_text":"","upstream_text":"up"}`), &req))
		in := req.input()
		assert.Equal(t, "", in.Text, "显式空的请求上下文被 sample_text 灌满了")
		assert.Equal(t, "up", in.UpstreamText)
	})

	t.Run("全部维度都能从请求体抵达线上判据", func(t *testing.T) {
		var req ruleTestReq
		require.NoError(t, json.Unmarshal([]byte(`{
			"request_text":"ctx","upstream_text":"up","reject_reason":"soft",
			"status_code":429,"error_code":"content_policy_violation",
			"rate_count":7,"model":"gpt-4o","group":"vip",
			"ai_verdict":"violation","ai_category":"sexual","ai_confidence":"0.9"}`), &req))
		assert.Equal(t, scanInput{
			Model: "gpt-4o", Group: "vip", Text: "ctx", RateCount: 7,
			ErrCode: "content_policy_violation", StatusCode: 429,
			UpstreamText: "up", RejectReason: "soft",
			AI: &aiOutcome{Outcome: OutcomeViolation, Violated: true,
				Category: "sexual", Confidence: clampConfidence(0.9)},
		}, req.input())
	})
}

// TestAdminTestRuleResponseShape 走一次真实 handler:上面的表跑的是内部函数,
// 而"界面拿到的到底是什么"只存在于 handler 拼的那个响应体里。
func TestAdminTestRuleResponseShape(t *testing.T) {
	useGenerousScanBudget(t)
	// guard.RequireAPI 要求扩展库可用。试跑本身一行 SQL 都不跑,但它挡在接口最前面,
	// 不接一个句柄就只能测到 503 —— 那测的是 guard,不是试跑。
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	prevHandle := qyDBHandleForCtxTest.Swap(gdb)
	prevHealthy := qyDBHealthyForJSONTest.Swap(true)
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prevHandle)
		qyDBHealthyForJSONTest.Store(prevHealthy)
	})

	body := `{"rule":{"name":"up","enabled":true,"mode":"shadow","phase":"upstream_err",
		"match_type":"upstream_text","pattern":"cybersecurity risk","status_scope":"400",
		"action":"record","fee_mode":"none"},
		"upstream_text":"This content was flagged for possible cybersecurity risk",
		"status_code":403}`

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/violation/rules/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Set("id", 1)
	c.Set("username", "qy-admin")

	adminTestRule(c)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Outcome     string   `json:"outcome"`
			ScopeOK     bool     `json:"scope_ok"`
			ScopeFail   string   `json:"scope_fail"`
			Matched     bool     `json:"matched"`
			Inputs      []string `json:"inputs"`
			BlankInputs []string `json:"blank_inputs"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.Success)

	// 403 不在 status_scope=400 里:正文明明写着规则要找的那句话,结论也必须是
	// "不在作用域",而且要指名道姓说是状态码那一道闸 —— 只回 matched=false 的话,
	// 管理员会去改一个本来就对的模式串。
	assert.Equal(t, TestOutcomeOutOfScope, resp.Data.Outcome)
	assert.Equal(t, TestScopeFailStatus, resp.Data.ScopeFail)
	assert.False(t, resp.Data.ScopeOK)
	assert.False(t, resp.Data.Matched)
	assert.Equal(t, []string{TestInputUpstreamText, TestInputRejectReason,
		TestInputStatusCode, TestInputModel, TestInputGroup}, resp.Data.Inputs)
	assert.Empty(t, resp.Data.BlankInputs)
}

func strptr(s string) *string { return &s }
