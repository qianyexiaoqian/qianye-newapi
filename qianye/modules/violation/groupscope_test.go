package violation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groupscope_test.go —— 分组作用域的两件事:大小写口径,以及豁免名单。
//
// 大小写那一条是**既存缺陷的回归**:compile 只做了 splitList、inScope 拿
// info.UsingGroup 原样比较,整个 violation 包 grep 不到 groupname 的 import。
// 后果与 commission / transfer 各自栽过一次的完全同形 —— 管理端配 "VIP"、
// 用户实际分组是 "vip",规则保存成功、界面正常、线上一次都不命中。

func scopedRule(scope, mode string) Rule {
	return Rule{
		Name: "s", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "x",
		Mode: ModeShadow, Action: ActionRecord, FeeMode: FeeNone,
		GroupScope: scope, GroupScopeMode: mode,
	}
}

// TestGroupScopeFoldsCase 固化"分组名的比较口径由 groupname 唯一决定"。
func TestGroupScopeFoldsCase(t *testing.T) {
	t.Run("管理端配大写,用户分组是小写", func(t *testing.T) {
		cr := mustCompile(t, scopedRule("VIP, SVIP", GroupScopeInclude))
		assert.True(t, cr.inScope("gpt-4o", "vip"),
			"扩展库与主库的分组列都是大小写不敏感排序规则,只有 Go 侧是精确匹配")
		assert.True(t, cr.inScope("gpt-4o", "Svip"))
		assert.False(t, cr.inScope("gpt-4o", "default"))
	})

	t.Run("管理端配小写,用户分组带空白与大写", func(t *testing.T) {
		cr := mustCompile(t, scopedRule("vip", GroupScopeInclude))
		assert.True(t, cr.inScope("gpt-4o", "  VIP "))
	})

	t.Run("空分组折叠成 default", func(t *testing.T) {
		// users.group 留空的历史账号在业务上就是默认分组的用户,
		// 而它们恰恰是最可疑的那批。不折叠就没有任何规则盖得住它们。
		cr := mustCompile(t, scopedRule("default", GroupScopeInclude))
		assert.True(t, cr.inScope("gpt-4o", ""))
	})
}

// TestGroupScopeExcludeMode 固化豁免名单(黑名单半边)。
//
// 它存在的根因是频率判据会误伤合法的高频非流式用户:embedding 批处理、
// 批量分类、agent 工具循环、结构化输出流水线。没有豁免就只能把整条规则关掉。
func TestGroupScopeExcludeMode(t *testing.T) {
	cr := mustCompile(t, scopedRule("batch, INTERNAL", GroupScopeExclude))

	assert.False(t, cr.inScope("gpt-4o", "batch"), "名单内的分组必须被豁免")
	assert.False(t, cr.inScope("gpt-4o", "internal"), "豁免名单同样要折叠大小写")
	assert.True(t, cr.inScope("gpt-4o", "default"), "名单外的分组全部生效")
	assert.True(t, cr.inScope("gpt-4o", "vip"))

	t.Run("include 与 exclude 在同一份名单上恰好互补", func(t *testing.T) {
		// 两个方向必须由同一列推导。开第二列 GroupExclude 的话,
		// 两张能互相矛盾的名单必然漂移,而"哪张说了算"没有自解释的答案。
		inc := mustCompile(t, scopedRule("batch, INTERNAL", GroupScopeInclude))
		for _, g := range []string{"batch", "internal", "default", "vip", ""} {
			assert.NotEqual(t, inc.inScope("gpt-4o", g), cr.inScope("gpt-4o", g),
				"分组 %q 在两种方向下的结论必须相反", g)
		}
	})

	t.Run("名单为空时两个方向都表示全部生效", func(t *testing.T) {
		empty := mustCompile(t, scopedRule("", GroupScopeExclude))
		assert.True(t, empty.inScope("gpt-4o", "anything"))
	})
}

// TestGroupScopeModeValidation 保证非法的方向存不下去。
func TestGroupScopeModeValidation(t *testing.T) {
	for _, mode := range []string{"", GroupScopeInclude, GroupScopeExclude} {
		r := scopedRule("vip", mode)
		assert.NoError(t, ValidateRule(&r), "mode=%q 应当合法(空串按 include 处理)", mode)
	}
	// 抄 transfer 那套四值策略枚举的话,这两个值会被当成合法输入,
	// 而它们在这里各自等价于"空 include"和"把规则停用"。
	for _, mode := range []string{"allow_all", "deny_list", "Include"} {
		r := scopedRule("vip", mode)
		require.Error(t, ValidateRule(&r), "mode=%q 必须被拒", mode)
	}
}

// TestRuleUpsertNormalizesGroupScopeMode 固化写入侧的归一。
//
// 名单为空时方向必须落回 include:两个等价状态并存,界面上就会出现一个
// 看得见、点得动、却什么都不改变的开关。
func TestRuleUpsertNormalizesGroupScopeMode(t *testing.T) {
	cases := []struct {
		name  string
		scope string
		mode  string
		want  string
	}{
		{"空名单 + exclude 折回 include", "", GroupScopeExclude, GroupScopeInclude},
		{"空名单 + 空方向", "", "", GroupScopeInclude},
		{"非空名单保留 exclude", "batch", GroupScopeExclude, GroupScopeExclude},
		{"大小写与空白归一", "batch", "  EXCLUDE ", GroupScopeExclude},
		{"未传方向默认 include", "batch", "", GroupScopeInclude},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ruleUpsertReq{
				Name: "r", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "x",
				Action: ActionRecord, FeeMode: FeeNone,
				GroupScope: tc.scope, GroupScopeMode: tc.mode,
			}
			var row Rule
			require.NoError(t, req.apply(&row))
			assert.Equal(t, tc.want, row.GroupScopeMode)
		})
	}
}
