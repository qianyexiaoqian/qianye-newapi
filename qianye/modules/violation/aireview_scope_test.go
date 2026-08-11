package violation

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_scope_test.go —— AI 审核作用域与分档抽样。
//
// 这里只钉三类不变量,每一类都对应一种"配置正确却不按预期跑"的失效:
//
//  1. **匹配顺序与兜底**:第一条匹配的策略说了算,都不匹配才落兜底。
//     配错了没有任何症状 —— 抽样本来就是概率性的,少抽到一半看起来像运气。
//  2. **作用域外零开销**:不在作用域内的请求连抽样函数都不许调用。
//     反过来(先抽中再丢弃)会让界面上的"10%"变成一个谁也算不出来的数。
//  3. **两个时机各摇各的**:转发前只对少数分组开、转发后全站铺,是这一功能
//     被要求存在的理由(前者加延迟,后者不加)。共用一枚骰子就表达不了。

// scopeRT 是构造运行期策略的测试夹具,走的是**生产的** compileScope,
// 不是在测试里另写一份归一逻辑 —— 后者会让"两份判据"这个缺陷刚好测不出来。
func scopeRT(name, modelScope, groupScope, mode string, pre, async int) *aiScopeRT {
	return &aiScopeRT{
		Name:         name,
		scopeMatcher: compileScope(modelScope, groupScope, mode),
		PreBps:       pre,
		AsyncBps:     async,
	}
}

// TestAIScopeSampleRatesFor 是作用域解析的表驱动主用例。
func TestAIScopeSampleRatesFor(t *testing.T) {
	// 现网最典型的一份配置:高风险自助分组重点看,内部对接分组一律免审,
	// 其余走兜底。三档同时存在,顺序决定一切。
	rt := &aiRuntime{
		SampleRateBps: 100, // 兜底 1%
		Scopes: []*aiScopeRT{
			scopeRT("内部对接·免审", "", "internal,batch", GroupScopeInclude, 0, 0),
			scopeRT("视觉模型·全量后审", "*-vision,gpt-4*", "", GroupScopeInclude, 0, 10000),
			scopeRT("自助注册·重点看", "", "selfserve", GroupScopeInclude, 5000, 5000),
			// 名单侧故意写成大写:管理端表单里 "VIP" 与 "vip" 都会被填进来,
			// 而分组列在 MySQL 与 PostgreSQL 上都是大小写不敏感的排序规则。
			// 编译时不归一名单,这一档就是一条"保存成功、界面正常、永不命中"的策略。
			scopeRT("大写名单", "", "VIP, Svip", GroupScopeInclude, 2000, 2000),
		},
	}

	tests := []struct {
		name      string
		model     string
		group     string
		wantPre   int
		wantAsync int
		why       string
	}{
		{"免审分组两个时机都是 0", "gpt-4o", "internal", 0, 0,
			"抽样率两个都填 0 是免审名单的字面写法,不是'没配'"},
		{"免审分组的大小写要折叠", "gpt-4o", "INTERNAL", 0, 0,
			"分组列是大小写不敏感的排序规则,不折叠就是一条永不命中的配置"},
		{"免审档排在前面,遮住后面的模型档", "gpt-4o", "batch", 0, 0,
			"gpt-4* 也匹配 gpt-4o,但免审档优先级更高 —— 第一条匹配的说了算"},
		{"模型后缀通配命中后审档", "qwen-vl-vision", "default", 0, 10000,
			"转发后审核不加延迟,可以对整个模型族全量铺"},
		{"模型前缀通配命中后审档", "gpt-4o-mini", "default", 0, 10000, ""},
		{"自助分组走重点档", "claude-3-opus", "selfserve", 5000, 5000, ""},
		{"名单侧的大写也要折叠", "claude-3-opus", "vip", 2000, 2000,
			"管理端填 VIP、用户实际分组是 vip —— 编译时不归一名单等于没归一"},
		{"名单与请求两侧大小写都不同", "claude-3-opus", "SVIP", 2000, 2000, ""},
		{"自助分组 + 视觉模型:模型档排在前面", "gpt-4-vision", "selfserve", 0, 10000,
			"两条都匹配时取 priority 更靠前的那条,不叠加 —— 叠加没有任何一种定义是运营预期的"},
		{"谁都不匹配 → 兜底", "claude-3-opus", "default", 100, 100,
			"兜底是一条策略都没有时的行为,也是这张表存在之前的行为"},
		{"空分组折进 default,同样走兜底", "claude-3-opus", "", 100, 100,
			"UsingGroup 为空的历史账号恰恰最可疑,不能因为空串就漏出作用域"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pre, async := rt.sampleRatesFor(tc.model, tc.group)
			assert.Equal(t, tc.wantPre, pre, tc.why)
			assert.Equal(t, tc.wantAsync, async, tc.why)
		})
	}
}

// TestAIScopeExcludeDirection 钉住 include / exclude 是严格互补的。
//
// 与 groupscope_test.go 对规则的那条同形:方向搞反的后果是"一批原本豁免的
// 分组突然开始被送审",而送审意味着他们的请求内容被发往第三方。
func TestAIScopeExcludeDirection(t *testing.T) {
	groups := []string{"default", "vip", "internal", "batch", ""}
	inc := &aiRuntime{Scopes: []*aiScopeRT{
		scopeRT("include", "", "internal,batch", GroupScopeInclude, 5000, 5000)}}
	exc := &aiRuntime{Scopes: []*aiScopeRT{
		scopeRT("exclude", "", "internal,batch", GroupScopeExclude, 5000, 5000)}}

	for _, g := range groups {
		incPre, _ := inc.sampleRatesFor("gpt-4o", g)
		excPre, _ := exc.sampleRatesFor("gpt-4o", g)
		// 兜底率是 0,所以"没匹配上"= 0,"匹配上"= 5000。两个方向必须恰好相反。
		assert.NotEqual(t, incPre, excPre,
			"分组 %q:include 与 exclude 必须严格互补,否则方向写反了不会有任何症状", g)
	}
}

// TestAIScopeSamplingZeroCost 是本轮最硬的一条:**作用域外零开销**。
//
// 断言的是"抽样函数一次都没被调用",而不是"没发出外部调用" —— 后者在
// "先抽中再丢弃"的实现下同样成立,而那种实现会让界面上的抽样率彻底失真:
// 运营填的 10% 会变成"作用域内的 10% 乘以一个谁也说不出来的数"。
//
// aiSampleRolls 的第一行就在 sampleAI 里,所以 delta == 0 字面意思就是
// "sampleAI 没被调用过"。
func TestAIScopeSamplingZeroCost(t *testing.T) {
	useGenerousScanBudget(t)
	rule, err := compile(Rule{
		Id: 91, Enabled: true, Mode: ModeEnforce, Phase: PhasePrompt,
		MatchType: MatchAIReview, Action: ActionBlock,
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		group     string
		model     string
		wantRolls int64
		wantCalls int64
		why       string
	}{
		{
			name:  "作用域外的请求:抽样函数调用次数为 0",
			group: "default", model: "gpt-4o",
			wantRolls: 0, wantCalls: 0,
			why: "兜底率 0 + 唯一一条策略只覆盖 selfserve → 这条请求连骰子都不该摇",
		},
		{
			name:  "作用域内的请求:抽样一次、外部调用一次",
			group: "selfserve", model: "gpt-4o",
			wantRolls: 1, wantCalls: 1,
			why: "只配了转发前时机,所以恰好一次抽样、一次调用",
		},
		{
			name:  "作用域内但模型不匹配:同样一次都不摇",
			group: "selfserve", model: "claude-3-opus",
			wantRolls: 0, wantCalls: 0,
			why: "模型作用域与分组作用域是 AND,漏掉任何一道闸都会让抽样率失真",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(okVerdict(false, "none", 0.1, 1, 1)))
			})
			rt := rtForServer(srv.URL, 2000)
			rt.SampleRateBps = 0 // 兜底 0:作用域外 = 完全不审
			rt.Scopes = []*aiScopeRT{
				scopeRT("自助注册", "gpt-4*", "selfserve", GroupScopeInclude, 10000, 0),
			}
			snap := &snapshot{promptRules: []*compiledRule{rule}, hasAIPrompt: true, ai: rt}
			c, info := newRelayTestCtx(t)
			in := scanInput{Model: tc.model, Group: tc.group, Text: "内容"}

			before := aiSampleRolls.Load()
			assert.NoError(t, aiPreReview(c, info, snap, in, in.Text))
			assert.Equal(t, tc.wantRolls, aiSampleRolls.Load()-before, tc.why)
			assert.Equal(t, tc.wantCalls, srv.calls.Load(), tc.why)
		})
	}
}

// TestAIScopePhasesSampleIndependently 钉住两个时机各摇各的。
//
// 这是"转发前只对少数分组开、转发后全站铺"能成立的唯一前提。共用一枚骰子时,
// 两个时机会被绑成同一批请求 —— 转发后想要的覆盖率会被转发前的延迟顾虑拖低,
// 而那正是项目方要这个功能的原因(「AI审核基本都是后审核,秋后算账的」)。
//
// 同步侧发不发出外部调用是可观测的;异步侧走 guard.HotAsync,测试环境里作业
// 不执行,所以它的"被抽中"只体现在抽样次数上 —— 那恰好是这条用例要断言的量。
func TestAIScopePhasesSampleIndependently(t *testing.T) {
	useGenerousScanBudget(t)
	promptRule, err := compile(Rule{
		Id: 92, Enabled: true, Mode: ModeEnforce, Phase: PhasePrompt,
		MatchType: MatchAIReview, Action: ActionBlock,
	})
	require.NoError(t, err)
	asyncRule, err := compile(Rule{
		Id: 93, Enabled: true, Mode: ModeEnforce, Phase: PhasePostAsync,
		MatchType: MatchAIReview, Action: ActionRecord,
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		pre       int
		async     int
		hasPrompt bool
		hasAsync  bool
		wantRolls int64
		wantCalls int64
		why       string
	}{
		{
			name: "只开转发后:同步侧一次外部调用都不发",
			pre:  0, async: 10000, hasPrompt: true, hasAsync: true,
			wantRolls: 1, wantCalls: 0,
			why: "转发后审核不占用户的时间,这一档正是它存在的意义",
		},
		{
			name: "只开转发前:恰好一次抽样、一次调用",
			pre:  10000, async: 0, hasPrompt: true, hasAsync: true,
			wantRolls: 1, wantCalls: 1, why: "",
		},
		{
			name: "两个时机都开:摇两次骰子,但只发一次调用",
			pre:  10000, async: 10000, hasPrompt: true, hasAsync: true,
			wantRolls: 2, wantCalls: 1,
			why: "同时被抽中时异步侧复用同步的结论 —— 两次调用问的是同一段文本,第二次不会带来新信息",
		},
		{
			name: "配了转发前抽样率、却一条转发前规则都没有 → 不摇",
			pre:  10000, async: 0, hasPrompt: false, hasAsync: true,
			wantRolls: 0, wantCalls: 0,
			why: "没有规则消费结论时送审纯属烧钱,而且要给用户加一次外部调用的延迟",
		},
		{
			name: "配了转发后抽样率、却一条转发后规则都没有 → 不摇",
			pre:  0, async: 10000, hasPrompt: true, hasAsync: false,
			wantRolls: 0, wantCalls: 0, why: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(okVerdict(false, "none", 0.1, 1, 1)))
			})
			rt := rtForServer(srv.URL, 2000)
			rt.SampleRateBps = 0
			rt.Scopes = []*aiScopeRT{
				scopeRT("自助注册", "", "selfserve", GroupScopeInclude, tc.pre, tc.async),
			}
			snap := &snapshot{ai: rt, hasAIPrompt: tc.hasPrompt, hasAIAsync: tc.hasAsync}
			if tc.hasPrompt {
				snap.promptRules = []*compiledRule{promptRule}
			}
			if tc.hasAsync {
				snap.asyncRules = []*compiledRule{asyncRule}
			}
			c, info := newRelayTestCtx(t)
			in := scanInput{Model: "gpt-4o", Group: "selfserve", Text: "内容"}

			before := aiSampleRolls.Load()
			assert.NoError(t, aiPreReview(c, info, snap, in, in.Text))
			assert.Equal(t, tc.wantRolls, aiSampleRolls.Load()-before, tc.why)
			assert.Equal(t, tc.wantCalls, srv.calls.Load(), tc.why)
		})
	}
}

// TestAIScopesReachAnySampling 钉住"兜底率为 0 但策略非 0"必须整体生效。
//
// 旧判据是 `SampleRateBps <= 0 → 整份 AI 配置不生效`。它与"只盯高风险分组"
// 这个最典型的用法直接冲突:兜底 0 + 一条 50% 的策略会被整个关掉,
// 而界面上策略配得好好的、线上一次都不跑、零报错。
func TestAIScopesReachAnySampling(t *testing.T) {
	tests := []struct {
		name     string
		fallback int
		scopes   []*aiScopeRT
		want     bool
	}{
		{"兜底非 0 → 生效", 100, nil, true},
		{"兜底 0 且一条策略都没有 → 不生效", 0, nil, false},
		{"兜底 0 + 转发前 50% 的策略 → 生效", 0,
			[]*aiScopeRT{scopeRT("高风险", "", "selfserve", GroupScopeInclude, 5000, 0)}, true},
		{"兜底 0 + 只配转发后的策略 → 生效", 0,
			[]*aiScopeRT{scopeRT("后审", "", "", GroupScopeInclude, 0, 1000)}, true},
		{"兜底 0 + 全部策略都是 0 → 不生效", 0,
			[]*aiScopeRT{scopeRT("免审", "", "internal", GroupScopeInclude, 0, 0)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, aiScopesReachAnySampling(tc.fallback, tc.scopes))
		})
	}
}

// TestValidateAIScope 是写入闸。每一条拒绝都对应一种"存得下去、线上不对"的取值。
func TestValidateAIScope(t *testing.T) {
	base := func() AIScope {
		return AIScope{Name: "自助注册", Enabled: true, Priority: 100,
			GroupScope: "selfserve", GroupScopeMode: GroupScopeInclude,
			PreSampleRateBps: 5000, AsyncSampleRateBps: 5000}
	}
	tests := []struct {
		name    string
		mutate  func(*AIScope)
		wantErr bool
		check   func(*testing.T, AIScope)
	}{
		{"合法配置", func(*AIScope) {}, false, nil},
		{"名称为空", func(s *AIScope) { s.Name = "  " }, true, nil},
		{"名称过长", func(s *AIScope) { s.Name = string(make([]rune, 65)) }, true, nil},
		{"方向取值非法", func(s *AIScope) { s.GroupScopeMode = "both" }, true, nil},
		{"抽样率越界(转发前)", func(s *AIScope) { s.PreSampleRateBps = 10001 }, true, nil},
		{"抽样率越界(转发后)", func(s *AIScope) { s.AsyncSampleRateBps = -1 }, true, nil},
		{"优先级越界", func(s *AIScope) { s.Priority = 10001 }, true, nil},
		{"两个抽样率都是 0 是合法的免审名单", func(s *AIScope) {
			s.PreSampleRateBps, s.AsyncSampleRateBps = 0, 0
		}, false, nil},
		{"方向留空回落 include", func(s *AIScope) { s.GroupScopeMode = "" }, false,
			func(t *testing.T, s AIScope) {
				assert.Equal(t, GroupScopeInclude, s.GroupScopeMode,
					"空串必须归一成 include —— 它是这一列出现之前的唯一语义")
			}},
		{"名称与作用域两侧的空白要去掉", func(s *AIScope) {
			s.Name, s.GroupScope = "  自助注册  ", "  selfserve  "
		}, false, func(t *testing.T, s AIScope) {
			assert.Equal(t, "自助注册", s.Name)
			assert.Equal(t, "selfserve", s.GroupScope)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := base()
			tc.mutate(&row)
			err := validateAIScope(&row)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.check != nil {
				tc.check(t, row)
			}
		})
	}
}

// TestSummarizeAIScopes 钉住「一眼看出谁在被监控」那张表的两条性质:
// 兜底恒在最后一行,以及被遮住的行必须被标出来。
//
// 遮蔽标记不是装饰:一条被遮住的策略与一条配错了作用域的策略在列表上长得
// 一模一样,而两者的下一步完全不同(调优先级 vs 改作用域)。
func TestSummarizeAIScopes(t *testing.T) {
	rows := []AIScope{
		{Id: 1, Name: "免审", Enabled: true, Priority: 10, GroupScope: "internal"},
		{Id: 2, Name: "全站后审", Enabled: true, Priority: 20},
		{Id: 3, Name: "高风险", Enabled: true, Priority: 30, GroupScope: "selfserve"},
		{Id: 4, Name: "停用的档", Enabled: false, Priority: 40},
	}
	got := summarizeAIScopes(rows, 100)

	require.Len(t, got, 5, "汇总表 = 全部策略行 + 一行兜底")
	assert.False(t, got[0].Shadowed, "有作用域的第一条不会被遮住")
	assert.False(t, got[1].Shadowed, "作用域为空的那一条自己不被遮住")
	assert.True(t, got[2].Shadowed,
		"排在'全站'档后面的策略永远匹配不到 —— 不标出来,它看起来和配错作用域一模一样")
	assert.False(t, got[3].Shadowed, "停用的档不参与判定,标成被遮住只会制造噪声")

	last := got[4]
	assert.True(t, last.Fallback, "兜底恒在最后一行:界面顺序必须等于热路径的判定顺序")
	assert.Equal(t, 100, last.PreBps)
	assert.Equal(t, 100, last.AsyncBps)
	assert.True(t, last.Shadowed, "有'全站'档时兜底再也用不到,改它没有用")

	t.Run("没有全站档时谁都不被遮住", func(t *testing.T) {
		got := summarizeAIScopes([]AIScope{
			{Id: 1, Name: "高风险", Enabled: true, GroupScope: "selfserve"},
		}, 100)
		require.Len(t, got, 2)
		assert.False(t, got[0].Shadowed)
		assert.False(t, got[1].Shadowed)
		assert.True(t, got[1].Fallback)
	})

	t.Run("停用的全站档遮不住任何人", func(t *testing.T) {
		got := summarizeAIScopes([]AIScope{
			{Id: 1, Name: "停用的全站档", Enabled: false},
			{Id: 2, Name: "高风险", Enabled: true, GroupScope: "selfserve"},
		}, 100)
		require.Len(t, got, 3)
		assert.False(t, got[1].Shadowed, "停用的档一个请求都收不到")
		assert.False(t, got[2].Shadowed)
	})
}

// TestScopeMatcherSharedWithRules 钉住"作用域只有一份判据"。
//
// 规则的 group_scope / model_scope 与 AI 审核策略的同名列必须逐字节同义。
// 两份实现的失效形状在本模块出现过两次(groupname 大小写):症状是
// "界面上配好了、线上从不生效",而且没有任何报错指向它。
func TestScopeMatcherSharedWithRules(t *testing.T) {
	const (
		modelScope = "gpt-4*,*-vision"
		groupScope = "VIP, Svip"
	)
	rule, err := compile(Rule{
		Id: 94, Enabled: true, Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "x",
		ModelScope: modelScope, GroupScope: groupScope, GroupScopeMode: GroupScopeInclude,
	})
	require.NoError(t, err)
	scope := scopeRT("同一份作用域", modelScope, groupScope, GroupScopeInclude, 1, 1)

	cases := []struct{ model, group string }{
		{"gpt-4o", "vip"}, {"gpt-4o", "VIP"}, {"qwen-vl-vision", "svip"},
		{"claude-3-opus", "vip"}, {"gpt-4o", "default"}, {"gpt-4o", ""},
	}
	for _, tc := range cases {
		assert.Equal(t, rule.inScope(tc.model, tc.group), scope.inScope(tc.model, tc.group),
			"模型 %q / 分组 %q:规则与 AI 作用域策略必须给出同一个答案", tc.model, tc.group)
	}
}
