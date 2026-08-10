package violation

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upstream_rule_test.go —— 上游返回错误的违规判据。
//
// 覆盖两件事:
//   - 状态码作用域是一道与匹配方式**正交**的 AND 闸(这是"status_code + 正文"
//     成为一条规则的全部机制);
//   - 内置的上游拒绝条目能在项目方给的那条真实响应上命中,并且不会在别的
//     状态码上命中。
//
// 项目方给的原文(status_code=400):
//
//	This content was flagged for possible cybersecurity risk. If this seems
//	wrong, try rephrasing your request. To get authorized for security work,
//	join the Trusted Access for Cyber program

// anthropicCyberBody 是项目方从生产环境贴出来的那条上游拒绝正文。
//
// 用它包一层 RelayErrorHandler 实际会拼出来的形状(`bad response status code
// 400, message: ..., body: {...}`),因为线上喂给 scanPost 的就是这个串,
// 而不是干净的一句话。只测干净版本会漏掉"正文被包在 JSON 里"这一层。
const anthropicCyberBody = `bad response status code 400, message: This content was flagged ` +
	`for possible cybersecurity risk. If this seems wrong, try rephrasing your request. ` +
	`To get authorized for security work, join the Trusted Access for Cyber program, ` +
	`body: {"type":"error","error":{"type":"invalid_request_error","message":"This content ` +
	`was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your ` +
	`request. To get authorized for security work, join the Trusted Access for Cyber program"}}`

// TestStatusScopeIsAndedWithMatchType 是状态码作用域的核心判据。
//
// 没有这道 AND 闸,项目方那条拒绝只能拆成两条规则(一条判 400、一条判正文),
// 而两条规则会各自命中、各自计数、各自扣费 —— 一次上游拒绝算成两次违规。
func TestStatusScopeIsAndedWithMatchType(t *testing.T) {
	useGenerousScanBudget(t)

	rule := Rule{
		Id: 1, Name: "上游拒绝-网络安全风险", Enabled: true, Mode: ModeEnforce,
		Phase: PhaseUpstreamErr, MatchType: MatchUpstreamText,
		Pattern: "flagged for possible cybersecurity risk", StatusScope: "400",
		Action: ActionRecord, FeeMode: FeeNone,
	}
	cr, err := compile(rule)
	require.NoError(t, err)

	cases := []struct {
		name   string
		status int
		text   string
		want   bool
	}{
		{"状态码与正文都命中", 400, anthropicCyberBody, true},
		{"正文命中但状态码不在作用域(5xx)", 500, anthropicCyberBody, false},
		{"正文命中但状态码不在作用域(429)", 429, anthropicCyberBody, false},
		{"状态码命中但正文不含关键词", 400, "bad response status code 400, message: invalid model", false},
		{"两边都不命中", 502, "upstream timeout", false},
		{"状态码为 0(本地错误,还没发给上游)", 0, anthropicCyberBody, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := scanInput{StatusCode: tc.status, UpstreamText: tc.text}
			v := scan([]*compiledRule{cr}, nil, in, scanPostText(in))
			assert.Equal(t, tc.want, v != nil && v.Rule != nil)
		})
	}
}

// TestStatusScopeAcceptsListsAndRanges 覆盖作用域语法。
//
// 语法与 MatchStatusCode 的 pattern 共用 parseStatusRange。这条用例的价值在于
// 钉住"共用"这件事本身:两处各写一个解析器时,管理员在两个格子里写同一串
// "400-499" 会得到不同结果,而界面上看不出任何区别。
func TestStatusScopeAcceptsListsAndRanges(t *testing.T) {
	useGenerousScanBudget(t)

	cases := []struct {
		name   string
		scope  string
		status int
		want   bool
	}{
		{"单个状态码命中", "400", 400, true},
		{"单个状态码不命中", "400", 403, false},
		{"逗号列表命中第二项", "400,403", 403, true},
		{"区间下界", "400-499", 400, true},
		{"区间上界", "400-499", 499, true},
		{"区间外", "400-499", 500, false},
		{"列表与区间混用", "429,500-599", 503, true},
		{"空作用域 = 不限", "", 500, true},
		{"带空白的列表", " 400 , 403 ", 403, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr, err := compile(Rule{
				Id: 1, Name: "t", Enabled: true, Mode: ModeEnforce,
				Phase: PhaseUpstreamErr, MatchType: MatchUpstreamText,
				Pattern: "denied", StatusScope: tc.scope,
				Action: ActionRecord, FeeMode: FeeNone,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, cr.statusInScope(tc.status))
		})
	}
}

// TestValidateRuleRejectsStatusScopeOnPromptPhase 挡的是一条"永不命中"的规则。
//
// prompt 阶段没有上游响应,状态码恒为 0。允许保存的话,管理员会得到一条
// 保存成功、界面正常、线上一次都不会命中的规则 —— 而这类失效没有任何报错。
func TestValidateRuleRejectsStatusScopeOnPromptPhase(t *testing.T) {
	base := Rule{
		Name: "t", Mode: ModeShadow, MatchType: MatchKeyword, Pattern: "foo",
		Action: ActionRecord, FeeMode: FeeNone,
	}

	prompt := base
	prompt.Phase = PhasePrompt
	prompt.StatusScope = "400"
	assert.Error(t, ValidateRule(&prompt), "prompt 阶段配状态码作用域 = 一条永不命中的规则")

	promptNoScope := base
	promptNoScope.Phase = PhasePrompt
	assert.NoError(t, ValidateRule(&promptNoScope))

	upstream := base
	upstream.Phase = PhaseUpstreamErr
	upstream.MatchType = MatchUpstreamText
	upstream.Pattern = "denied"
	upstream.StatusScope = "400"
	assert.NoError(t, ValidateRule(&upstream))

	bad := upstream
	bad.StatusScope = "4xx"
	assert.Error(t, ValidateRule(&bad), "非法状态码必须在保存时报错,而不是在快照编译时被静默跳过")
}

// TestBuiltinCybersecurityRuleMatchesProjectPayload 是端到端的形状:
// 内置目录里的那一条,在项目方给的真实响应上必须命中。
//
// 这条用例是内置条目与真实载荷之间唯一的连接。没有它,模式串写错一个词
// (或上游改了一个词)只会表现为"这条规则从来没有命中过",而"从来没有命中过"
// 在影子模式下与"没有人违规"看起来完全一样。
func TestBuiltinCybersecurityRuleMatchesProjectPayload(t *testing.T) {
	useGenerousScanBudget(t)

	// key 拼错时必须直接红:静默跳过会让整个用例空转通过。
	b, ok := builtinByKey("upstream.cybersecurity_refusal")
	require.True(t, ok, "内置目录里没有 upstream.cybersecurity_refusal")
	require.Equal(t, PhaseUpstreamErr, b.Phase)
	require.Equal(t, "400", b.StatusScope, "不钉状态码的话,5xx 正文里偶然出现同一串词也会算违规")

	rule := b.toRule(1000, 7)
	rule.Id = 1
	// 内置条目一律落影子(toRule 钉死),这条用例要验的是**匹配**,
	// 所以显式切成 enforce —— 影子与否不影响 scan 的结论,但写出来能让
	// 下一个人一眼看到这里测的不是模式。
	rule.Mode = ModeEnforce
	cr, err := compile(*rule)
	require.NoError(t, err)

	cases := []struct {
		name   string
		status int
		text   string
		want   bool
	}{
		{"项目方给的原始响应", 400, anthropicCyberBody, true},
		{"只有 message 那一句", 400,
			"This content was flagged for possible cybersecurity risk.", true},
		{"上游改写了前半句,后半句的项目名仍在", 400,
			`{"error":{"message":"Request blocked. To get authorized for security work, join the Trusted Access for Cyber program"}}`, true},
		{"大小写变体", 400,
			"THIS CONTENT WAS FLAGGED FOR POSSIBLE CYBERSECURITY RISK", true},
		{"同一段文字出现在 502 网关错误里", 502, anthropicCyberBody, false},
		{"普通的 400(参数错误)", 400,
			`{"error":{"message":"model: claude-x not found"}}`, false},
		{"提到 cybersecurity 但不是这条拒绝", 400,
			`{"error":{"message":"cybersecurity is a valid topic"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := scanInput{StatusCode: tc.status, UpstreamText: tc.text}
			v := scan([]*compiledRule{cr}, cr.words, in, scanPostText(in))
			assert.Equal(t, tc.want, v != nil && v.Rule != nil)
		})
	}
}

// TestBuiltinUpstreamRulesAreSafeByConstruction 钉住这一类内置条目的三条不变量。
//
// 它们不是风格问题,每一条都对应一个具体的事故形状:
//   - 必须挂上游阶段:挂 prompt 阶段的话永远拿不到上游响应,规则静默失效;
//   - 必须有状态码作用域:不限状态码的正文规则会在 5xx / 网关超时正文上求值;
//   - 必须落影子、只记录、不扣费:导入不该让任何人立刻被扣钱或被封。
func TestBuiltinUpstreamRulesAreSafeByConstruction(t *testing.T) {
	found := 0
	for _, b := range builtinCatalog {
		if b.Category != CatUpstream {
			// 反过来也要成立:非 upstream 类不得配状态码作用域,
			// 那是一条 ValidateRule 会直接拒绝保存的规则。
			assert.Emptyf(t, b.StatusScope,
				"内置规则 %s 不属于 %s 类,却配了状态码作用域", b.Key, CatUpstream)
			continue
		}
		found++
		assert.Equalf(t, PhaseUpstreamErr, b.Phase,
			"内置规则 %s 属于上游拒绝类,却不挂在上游阶段 —— 它永远拿不到上游响应", b.Key)
		assert.NotEmptyf(t, b.StatusScope,
			"内置规则 %s 没有状态码作用域:5xx 与网关超时的正文也会参与匹配", b.Key)

		rule := b.toRule(1000, 7)
		assert.Equalf(t, ModeShadow, rule.Mode, "内置规则 %s 必须落影子", b.Key)
		assert.Equalf(t, ActionRecord, rule.Action, "内置规则 %s 必须只记录", b.Key)
		assert.Equalf(t, FeeNone, rule.FeeMode, "内置规则 %s 不得自带定价", b.Key)
		assert.NoErrorf(t, ValidateRule(rule), "内置规则 %s 过不了写入校验,导入时会整条失败", b.Key)
	}
	require.NotZero(t, found, "上游拒绝类一条内置规则都没有 —— 项目方要的那条判据不存在")
}

// TestRecordSnippetIsRedacted 守的是"落库前脱敏"。
//
// 上游错误正文经常把请求内容原样回抄,而 match_snippet 是管理端列表**直接整行
// 返回**的一列。此前只有归档证据走 redact,这一列是原文直存。
func TestRecordSnippetIsRedacted(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantHas    string
		wantAbsent string
	}{
		{"邮箱", "rejected: contact alice@example.com for access", "«email»", "alice@example.com"},
		{"API 密钥", "denied with sk-abcdefghijklmnop0123456789", "«apikey»", "sk-abcdefghijklmnop0123456789"},
		{"手机号", "blocked for 13800138000", "«phone»", "13800138000"},
		{"Bearer 令牌", "auth failed: Bearer abcdefghijklmnop0123", "«bearer»", "abcdefghijklmnop0123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSnippet(tc.in)
			assert.Contains(t, got, tc.wantHas)
			assert.NotContains(t, got, tc.wantAbsent,
				"命中片段会被管理端列表整行返回,原文落库等于把用户的个人数据复制一份")
		})
	}

	t.Run("不含敏感数据的正文原样保留", func(t *testing.T) {
		s := "This content was flagged for possible cybersecurity risk."
		assert.Equal(t, s, redactSnippet(s),
			"脱敏不得改写正常正文 —— 管理员要靠它判断违规,改花了就没法研判")
	})
}

// TestNewRecordRedactsUpstreamSnippet 钉的是**生产调用点**,不是脱敏函数本身。
//
// 上面那条只证明 redactSnippet 会替换;它证明不了 newRecord 真的调用了它。
// 两者的差别在一次变异里就暴露了:把 newRecord 里的 redactSnippet 去掉,
// 只测函数的那条用例照旧全绿,而线上从此原文落库。
// 落库路径上的脱敏必须由落库路径上的断言守住。
func TestNewRecordRedactsUpstreamSnippet(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		UserId: 42, OriginModelName: "claude-sonnet-4", UsingGroup: "vip", RequestId: "req-cyber",
	}
	v := &verdict{
		Rule:  &compiledRule{R: Rule{Id: 9, Name: "上游拒绝-网络安全风险", PublicReason: "请求被上游安全策略拒绝"}},
		Terms: []string{"flagged for possible cybersecurity risk"},
		// 上游把请求里的联系方式原样回抄 —— 这正是 A.4 要防的形状。
		Snippet: "flagged for possible cybersecurity risk; reported by alice@example.com key sk-abcdefghijklmnop0123456789",
	}
	in := scanInput{StatusCode: 400, UpstreamText: v.Snippet}

	rec := newRecord(c, info, PhaseUpstreamErr, in, v, false, "", false)
	assert.NotContains(t, rec.MatchSnippet, "alice@example.com",
		"上游正文里的邮箱必须在落库前被替换:match_snippet 是管理端列表整行返回的一列")
	assert.NotContains(t, rec.MatchSnippet, "sk-abcdefghijklmnop0123456789")
	assert.Contains(t, rec.MatchSnippet, "flagged for possible cybersecurity risk",
		"脱敏不得吃掉命中证据本身,否则管理员没法研判")
	assert.LessOrEqual(t, len(rec.MatchSnippet), 2048, "落库前必须截断到列宽以内")
}

// TestEvidenceRedactsUpstreamBodyNotJustPrompt 是本轮实测抓到的那个缺陷的回归。
//
// # 现场
//
// 桩上游把整个请求体原样回抄进 error.message(不少真实上游就是这么做的),
// 归档下来的 qy_violation_payload.body 里出现了完整的邮箱、手机号与 API 密钥,
// 而同一行的 `redacted` 列写着 false、`redact_stats` 是空的。
//
// # 根因
//
// buildEvidence 只对 in.Text(prompt)调 redact。上游阶段 in.Text 恒为空,
// 用户数据全部走 doc.Up["error_message"] 与 doc.Hit["snippet"] 两条路进库,
// 两条都没有经过脱敏。也就是说**上游阶段的归档等于零脱敏**,
// 而那两列还在如实地报告"这份证据里没有个人数据"—— 比不脱敏更糟。
//
// # 为什么断言要打在 Payload 上而不是 redact() 上
//
// redact() 本来就是对的,一个字都不用改。错的是"哪些字段流过它",
// 而那件事只有把整条 buildEvidence 跑一遍、再把压缩后的 blob 解开来看才验得到。
func TestEvidenceRedactsUpstreamBodyNotJustPrompt(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  evidence_max_bytes: 8192\n")

	const echoed = `This content was flagged for possible cybersecurity risk. ` +
		`| offending input: {"messages":[{"role":"user","content":` +
		`"reach me at alice@example.com or 13800138000, key sk-abcdefghijklmnop0123456789"}]}`

	rec := &Record{
		RequestId: "req-cyber", ModelName: "claude-sonnet-4",
		UsingGroup: "vip", RelayFormat: "openai", Phase: PhaseUpstreamErr,
		CreatedAt: 1000,
	}
	in := scanInput{
		StatusCode:   400,
		ErrCode:      "unknown_error",
		UpstreamText: echoed,
		RejectReason: "flagged: bob@example.com",
	}
	v := &verdict{
		Rule:    &compiledRule{R: Rule{Id: 9, Name: "上游拒绝-网络安全风险"}},
		Terms:   []string{"flagged for possible cybersecurity risk"},
		Snippet: echoed,
	}

	payload := buildEvidence(rec, in, v, nil)
	require.NotNil(t, payload)

	body, err := decodeEvidence(payload)
	require.NoError(t, err)

	for _, secret := range []string{
		"alice@example.com",
		"bob@example.com",
		"13800138000",
		"sk-abcdefghijklmnop0123456789",
	} {
		assert.NotContainsf(t, body, secret,
			"上游回抄的 %q 原文落进了归档 —— 上游阶段 in.Text 恒为空,"+
				"只脱敏 prompt 等于这一阶段完全没有脱敏", secret)
	}
	assert.Contains(t, body, "«email»")
	assert.Contains(t, body, "«phone»")
	assert.Contains(t, body, "«apikey»")

	// 证据本体必须留下:脱敏不能把管理员研判所需的命中上下文一起吃掉。
	assert.Contains(t, body, "flagged for possible cybersecurity risk")

	// 两列必须如实反映刚才发生的替换。它们为假/为空时,管理员会认为
	// 这份证据里本来就没有个人数据 —— 那正是这次缺陷最有欺骗性的部分。
	assert.True(t, payload.Redacted, "发生过替换就必须标 redacted=true")
	// 统计的键名取自 redactors 表(api_key / phone_cn),不是替换占位符的写法。
	// 断言写占位符的话这一条会在正确的实现上误报,下一个人只会把它删掉。
	assert.Contains(t, payload.RedactStats, "email")
	assert.Contains(t, payload.RedactStats, "phone_cn")
	assert.Contains(t, payload.RedactStats, "api_key")

	// 上游阶段 in.Text 恒为空,origin_bytes 只数它的话恒为 0 ——
	// 而管理端拿这一列判断"证据够不够用"。
	assert.Positive(t, payload.OriginBytes,
		"上游阶段的 origin_bytes 必须把上游正文算进去,否则这一列恒为 0")
}

// TestEvidenceRedactStatsAreSummedAcrossFields 钉住统计的合并口径。
//
// 四段自由文本共用 Payload 上唯一一列 redact_stats。每段各写一份的话,
// 先写的会被后写的覆盖 —— 而覆盖之后「这份证据里替换掉了几个邮箱」这个数是错的,
// 且错得不可见(它仍然是一个看起来合理的正整数)。
func TestEvidenceRedactStatsAreSummedAcrossFields(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  evidence_max_bytes: 8192\n")

	rec := &Record{RequestId: "req-1", Phase: PhaseUpstreamErr, CreatedAt: 1000}
	in := scanInput{
		StatusCode:   400,
		UpstreamText: "a@x.com and b@x.com",
		RejectReason: "c@x.com",
	}
	v := &verdict{
		Rule:    &compiledRule{R: Rule{Id: 1, Name: "r"}},
		Snippet: "d@x.com",
	}

	payload := buildEvidence(rec, in, v, nil)
	require.NotNil(t, payload)
	assert.Contains(t, payload.RedactStats, `"email":4`,
		"四段文本里的 4 个邮箱必须累加成 4,而不是被最后一段覆盖成 1")
}
