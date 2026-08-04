package lottery

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eligibility_rules_test.go —— 参与条件引擎的契约。
//
// 这里断言两类事实:
//   - 规范化 JSON 的稳定性:它就是 rules_text,进 rules_hash 与 commit_hash。
//     算出来的字节变了,所有历史活动的承诺都会复算失败。
//   - 判定的完整性:每一条被打开的条件都必须真的能拦下不满足的人,
//     而"没打开的条件不该拦人"同样重要 —— 误拒的用户完全无从判断问题在哪。

const testNow = int64(1_800_000_000)

func okSubject() *Subject {
	return &Subject{
		Status:      common.UserStatusEnabled,
		Role:        common.RoleCommonUser,
		Group:       "default",
		Quota:       100_000,
		UsedQuota:   500_000,
		CreatedAt:   testNow - 100*86400,
		HasEmail:    true,
		HasOAuth:    true,
		HasPayPass:  true,
		RecentSpend: 50_000,
	}
}

func codes(items []Missing) []string {
	out := make([]string, 0, len(items))
	for _, m := range items {
		out = append(out, m.Code)
	}
	return out
}

// 规范化 JSON 必须只由取值决定,不受运营勾选顺序影响。
// 否则同一组条件会算出两个不同的 rules_hash,而承诺是对外公布的。
func TestRulesCanonicalTextIsOrderIndependent(t *testing.T) {
	a := Rules{AllowGroups: []string{"vip", "default"}, DenyGroups: []string{"b", "a"}}.Normalize()
	b := Rules{AllowGroups: []string{"default", "vip"}, DenyGroups: []string{"a", "b"}}.Normalize()

	ta, err := a.CanonicalText()
	require.NoError(t, err)
	tb, err := b.CanonicalText()
	require.NoError(t, err)
	assert.Equal(t, ta, tb)
	assert.Equal(t, RulesHash(ta), RulesHash(tb))

	// 键必须按字节序升序且无空白 —— 这是外部验证脚本唯一能依赖的编码约定。
	assert.NotContains(t, ta, " ")
	assert.NotContains(t, ta, "\n")
	assert.Contains(t, ta, `"allow_groups":["default","vip"]`)

	// 重复项必须被去掉,否则同一个分组勾两次会算出不同的哈希。
	dup := Rules{AllowGroups: []string{"vip", "vip", " vip "}}.Normalize()
	assert.Equal(t, []string{"vip"}, dup.AllowGroups)
}

// 落库的 rules_text 必须能被原样还原成同一组判定条件 ——
// 否则"用发布时冻结的规则判定"这条口径在解析那一步就已经失守。
func TestParseRulesRoundTrips(t *testing.T) {
	src := Rules{
		AllowGroups: []string{"vip"}, MinAccountAgeDays: 7, MinQuota: 1000,
		MinUsedQuota: 2000, RecentSpendDays: 30, RecentSpendQuota: 5000,
		ExcludeViolation: true, MaxViolationHits: 3, ExcludeEverAutoBanned: true,
		RequireEmail: true, MaxEntriesPerUser: 2, CooldownSeconds: 60, DedupIp: true,
	}.Normalize()

	text, err := src.CanonicalText()
	require.NoError(t, err)
	got, err := ParseRules(text)
	require.NoError(t, err)
	assert.Equal(t, src, got)

	// 空 rules_text 是"没有任何条件",不是错误:草稿期允许先不填条件。
	empty, err := ParseRules("")
	require.NoError(t, err)
	assert.Empty(t, Evaluate(empty, okSubject(), 1, testNow))
}

// 尝试上限必须自动放宽到成功上限之上。缺了它,一个"余额刚好不够"的用户
// 会在第一次失败之后就被永久挡在门外,而失败条目并没有让他真的参与。
func TestNormalizeDerivesAttemptCapFromEntryCap(t *testing.T) {
	r := Rules{MaxEntriesPerUser: 2}.Normalize()
	assert.Equal(t, 5, r.MaxAttemptsPerUser)

	// 显式填了一个比成功上限还小的值是配置错误,抬到成功上限。
	r2 := Rules{MaxEntriesPerUser: 5, MaxAttemptsPerUser: 2}.Normalize()
	assert.Equal(t, 5, r2.MaxAttemptsPerUser)

	// "近 N 日消费"的两个字段必须成对出现,只填天数不填额度等于没开这道门槛。
	r3 := Rules{RecentSpendDays: 30}.Normalize()
	assert.Zero(t, r3.RecentSpendDays)
	r4 := Rules{RecentSpendQuota: 100}.Normalize()
	assert.Equal(t, 30, r4.RecentSpendDays)
}

// 硬规则不可配置:管理员与活动创建者一律不能参与。
//
// 掌握数据库读权限的人能提前看到种子,这是 commit-reveal 的固有弱点,
// 只能用"他们不能下场"来堵。它必须在**没有任何条件被打开**时也生效。
func TestEvaluateAlwaysExcludesAdminsAndCreator(t *testing.T) {
	none := Rules{}.Normalize()

	admin := okSubject()
	admin.Role = common.RoleAdminUser
	assert.Contains(t, codes(Evaluate(none, admin, 1, testNow)), MissAdmin)

	root := okSubject()
	root.Role = common.RoleRootUser
	assert.Contains(t, codes(Evaluate(none, root, 1, testNow)), MissAdmin)

	creator := okSubject()
	creator.IsCreator = true
	assert.Contains(t, codes(Evaluate(none, creator, 1, testNow)), MissCreator)

	disabled := okSubject()
	disabled.Status = common.UserStatusDisabled
	assert.Contains(t, codes(Evaluate(none, disabled, 1, testNow)), MissDisabled)

	assert.Empty(t, Evaluate(none, okSubject(), 1, testNow),
		"没有打开任何条件时,一个正常用户必须畅通无阻")
}

// 每条规则都用**恰好被 okSubject 满足**的阈值来配,再由 tweak 把用户推到
// 门槛之下。这样同一份规则能同时断言两件事:不满足的被拦下,满足的不被误报。
// 后者与前者同样重要 —— 误拒的用户完全无从判断问题出在哪。
func TestEvaluateReportsEachUnmetCondition(t *testing.T) {
	cases := []struct {
		name  string
		rules Rules
		tweak func(*Subject)
		want  string
	}{
		{"分组白名单", Rules{AllowGroups: []string{"default"}},
			func(s *Subject) { s.Group = "other" }, MissGroup},
		{"分组黑名单", Rules{DenyGroups: []string{"banned"}},
			func(s *Subject) { s.Group = "banned" }, MissGroup},
		{"账号年龄", Rules{MinAccountAgeDays: 50},
			func(s *Subject) { s.CreatedAt = testNow - 10*86400 }, MissAccountAge},
		{"余额门槛", Rules{MinQuota: 100_000},
			func(s *Subject) { s.Quota = 99_999 }, MissBalance},
		{"累计消费", Rules{MinUsedQuota: 500_000},
			func(s *Subject) { s.UsedQuota = 499_999 }, MissUsedQuota},
		{"近 N 日消费", Rules{RecentSpendDays: 30, RecentSpendQuota: 50_000},
			func(s *Subject) { s.RecentSpend = 49_999 }, MissRecentSpend},
		{"有违规记录", Rules{ExcludeViolation: true},
			func(s *Subject) { s.HasViolation = true }, MissViolation},
		{"违规次数超限", Rules{MaxViolationHits: 2},
			func(s *Subject) { s.ViolationHits = 3 }, MissViolationCnt},
		{"曾被自动封禁", Rules{ExcludeEverAutoBanned: true},
			func(s *Subject) { s.EverAutoBanned = true }, MissEverBanned},
		{"未绑定邮箱", Rules{RequireEmail: true},
			func(s *Subject) { s.HasEmail = false }, MissEmail},
		{"未绑定第三方账号", Rules{RequireOAuth: true},
			func(s *Subject) { s.HasOAuth = false }, MissOAuth},
		{"未设置支付密码", Rules{RequirePayPassword: true},
			func(s *Subject) { s.HasPayPass = false }, MissPayPassword},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := tc.rules.Normalize()

			assert.NotContainsf(t, codes(Evaluate(rules, okSubject(), 1, testNow)), tc.want,
				"条件「%s」在满足时仍然报了缺失", tc.name)

			s := okSubject()
			tc.tweak(s)
			got := codes(Evaluate(rules, s, 1, testNow))
			assert.Containsf(t, got, tc.want,
				"条件「%s」没有拦下不满足的用户;实际缺失项 %v", tc.name, got)
		})
	}
}

// 参与费单独判:它是活动的资金要素而不是参与条件,而"余额够不够扣这一笔"
// 必须与"余额门槛"分成两条 —— 否则用户看到的提示会是一个他改不了的数字。
func TestEvaluateSeparatesStakeFromBalanceThreshold(t *testing.T) {
	none := Rules{}.Normalize()
	s := okSubject()

	assert.Empty(t, Evaluate(none, s, s.Quota, testNow), "余额刚好够扣时必须放行")
	assert.Equal(t, []string{MissStake}, codes(Evaluate(none, s, s.Quota+1, testNow)))
}

// 黑名单优先于白名单:同时命中两边时必须被拒。
// 反过来会让"临时拉黑某个分组"这个最常用的操作静默失效。
func TestGroupAllowedPrefersDeny(t *testing.T) {
	r := Rules{AllowGroups: []string{"vip"}, DenyGroups: []string{"vip"}}.Normalize()
	assert.False(t, GroupAllowed(r, "vip"))

	assert.True(t, GroupAllowed(Rules{}.Normalize(), "anything"),
		"两张名单都为空才是「不限制」")
	assert.False(t, GroupAllowed(Rules{AllowGroups: []string{"vip"}}.Normalize(), "default"))
}

// 资格快照必须记下判定输入。主库 users 没有历史版本:用户报名之后被降组、
// 被扣光余额、被封禁,事后就再也无法回答"他当时到底符不符合条件"。
func TestSnapshotKeepsTheInputsThatDecidedTheOutcome(t *testing.T) {
	s := okSubject()
	s.Group = "vip"
	s.RecentSpend = 12345

	text := SnapshotJSON(s)
	require.NotEmpty(t, text)

	var back Subject
	require.NoError(t, common.UnmarshalJsonStr(text, &back))
	assert.Equal(t, "vip", back.Group)
	assert.EqualValues(t, 12345, back.RecentSpend)
	assert.Equal(t, s.Quota, back.Quota)
	assert.Equal(t, s.UsedQuota, back.UsedQuota)
	assert.Equal(t, s.CreatedAt, back.CreatedAt)
}
