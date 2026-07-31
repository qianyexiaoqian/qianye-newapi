package transfer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rule 是构造一条启用规则的简写,单个用例只写自己关心的字段。
func rule(from, policy, to string) GroupRule {
	return GroupRule{FromGroup: from, Policy: policy, ToGroups: to, Enabled: true}
}

// TestGroupRuleSetPicksExactRuleThenFallback 锁定规则选取顺序。
//
// 顺序必须是"精确 > 兜底 > 不限制"且与扫描顺序无关:一个分组至多一条规则
// (from_group 唯一索引),否则"谁能转给谁"就取决于数据库返回行的次序,
// 而那是这套规则最不能有的性质。
func TestGroupRuleSetPicksExactRuleThenFallback(t *testing.T) {
	rows := []GroupRule{
		rule(groupWildcard, GroupPolicyDenyAll, ""),
		rule("vip", GroupPolicyAllowAll, ""),
		// 停用的规则视同不存在,因此 svip 会落到兜底规则上。
		{FromGroup: "svip", Policy: GroupPolicyAllowAll, Enabled: false},
	}
	set := buildGroupRuleSet(rows)

	cases := []struct {
		name       string
		from       string
		wantPolicy string
	}{
		{name: "精确命中", from: "vip", wantPolicy: GroupPolicyAllowAll},
		{name: "未覆盖的分组落到兜底", from: "default", wantPolicy: GroupPolicyDenyAll},
		{name: "停用的精确规则同样落到兜底", from: "svip", wantPolicy: GroupPolicyDenyAll},
		{name: "空分组名归一成 default", from: "", wantPolicy: GroupPolicyDenyAll},
		{name: "分组名两侧空白被忽略", from: "  vip  ", wantPolicy: GroupPolicyAllowAll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := set.ruleFor(tc.from)
			require.NotNil(t, got)
			assert.Equal(t, tc.wantPolicy, got.Policy)
		})
	}

	// 没有兜底规则时,未覆盖的分组必须是"不限制"而不是"拒绝"。
	noFallback := buildGroupRuleSet([]GroupRule{rule("vip", GroupPolicyAllowAll, "")})
	assert.Nil(t, noFallback.ruleFor("default"))
}

// TestGroupRuleEmptyTableMeansUnrestricted 是向后兼容的回归。
//
// 升级到本版本时规则表必然是空的。空表若被判成"拒绝",全站划转会在升级瞬间
// 全线失效;若被判成"读失败也不限制",扩展库抖一下限制就全没了。
// 这里只锁前一半,后一半由 loadGroupRules 的错误传播保证(见 grouprule.go)。
func TestGroupRuleEmptyTableMeansUnrestricted(t *testing.T) {
	for _, set := range []groupRuleSet{
		buildGroupRuleSet(nil),
		buildGroupRuleSet([]GroupRule{}),
		// 全部规则都被停用,等价于没有规则。
		buildGroupRuleSet([]GroupRule{{FromGroup: "vip", Policy: GroupPolicyDenyAll}}),
	} {
		assert.NoError(t, set.allowsGroup("vip", "default"))
		assert.NoError(t, set.allowsGroup("default", "vip"))
		assert.NoError(t, set.allowsGroup("", ""))
		assert.Equal(t, groupViewUnrestricted, describeGroupPolicy(set, "vip").Policy)
	}
}

// TestGroupRuleAllowsGroup 覆盖四种策略 × 名单形态的判定结果。
//
// 这是整个需求的判定核心:判错一格,要么放走一笔本该被拦的资金,
// 要么把一个合法分组整体锁死。
func TestGroupRuleAllowsGroup(t *testing.T) {
	cases := []struct {
		name    string
		rows    []GroupRule
		from    string
		to      string
		wantErr *bizError
	}{
		// ── 用户原话的形态:A 组只能转给 B、C 组 ──
		{
			name: "白名单内放行(第一项)",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, "b,c")},
			from: "a", to: "b",
		},
		{
			name: "白名单内放行(第二项)",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, "b,c")},
			from: "a", to: "c",
		},
		{
			name: "白名单外拒绝",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, "b,c")},
			from: "a", to: "d", wantErr: errGroupTargetDenied,
		},
		{
			name: "白名单不含自己时组内互转也被拒",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, "b,c")},
			from: "a", to: "a", wantErr: errGroupTargetDenied,
		},
		{
			name: "规则只约束发起方,反向不受影响",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, "b")},
			from: "b", to: "a",
		},

		// ── 全体不限制 ──
		{
			name: "allow_all 放行任意目标",
			rows: []GroupRule{rule("a", GroupPolicyAllowAll, "")},
			from: "a", to: "从未出现过的新分组",
		},

		// ── 禁止发起 ──
		{
			name: "deny_all 拒绝一切目标",
			rows: []GroupRule{rule("a", GroupPolicyDenyAll, "")},
			from: "a", to: "b", wantErr: errGroupSendBlocked,
		},
		{
			name: "deny_all 连同组也拒绝",
			rows: []GroupRule{rule("a", GroupPolicyDenyAll, "")},
			from: "a", to: "a", wantErr: errGroupSendBlocked,
		},

		// ── 黑名单 ──
		{
			name: "黑名单内拒绝",
			rows: []GroupRule{rule("a", GroupPolicyDenyList, "b")},
			from: "a", to: "b", wantErr: errGroupTargetDenied,
		},
		{
			name: "黑名单外放行",
			rows: []GroupRule{rule("a", GroupPolicyDenyList, "b")},
			from: "a", to: "c",
		},

		// ── @self:两种常见形态 ──
		{
			name: "只能转给同组:同组放行",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, groupSelfToken)},
			from: "a", to: "a",
		},
		{
			name: "只能转给同组:跨组拒绝",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, groupSelfToken)},
			from: "a", to: "b", wantErr: errGroupTargetDenied,
		},
		{
			name: "禁止组内互转:同组拒绝",
			rows: []GroupRule{rule("a", GroupPolicyDenyList, groupSelfToken)},
			from: "a", to: "a", wantErr: errGroupTargetDenied,
		},
		{
			name: "禁止组内互转:跨组放行",
			rows: []GroupRule{rule("a", GroupPolicyDenyList, groupSelfToken)},
			from: "a", to: "b",
		},
		{
			name: "兜底规则上的 @self 按各自分组解析",
			rows: []GroupRule{rule(groupWildcard, GroupPolicyAllowList, groupSelfToken)},
			from: "vip", to: "vip",
		},
		{
			name: "兜底规则上的 @self 不放行跨组",
			rows: []GroupRule{rule(groupWildcard, GroupPolicyAllowList, groupSelfToken)},
			from: "vip", to: "default", wantErr: errGroupTargetDenied,
		},
		{
			name: "@self 与具名分组可以并存",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, groupSelfToken+",b")},
			from: "a", to: "a",
		},

		// ── 归一化 ──
		{
			name: "空分组名按 default 判定",
			rows: []GroupRule{rule("default", GroupPolicyAllowList, "vip")},
			from: "", to: "vip",
		},
		{
			name: "收款方空分组名同样按 default 判定",
			rows: []GroupRule{rule("vip", GroupPolicyAllowList, "default")},
			from: "vip", to: "",
		},
		{
			name: "名单里的空白被忽略",
			rows: []GroupRule{rule("a", GroupPolicyAllowList, " b , c ")},
			from: "a", to: "c",
		},

		// ── 兜底与精确并存 ──
		{
			name: "精确规则覆盖兜底规则",
			rows: []GroupRule{
				rule(groupWildcard, GroupPolicyDenyAll, ""),
				rule("vip", GroupPolicyAllowList, "default"),
			},
			from: "vip", to: "default",
		},
		{
			name: "未被精确覆盖的分组仍走兜底",
			rows: []GroupRule{
				rule(groupWildcard, GroupPolicyDenyAll, ""),
				rule("vip", GroupPolicyAllowList, "default"),
			},
			from: "default", to: "vip", wantErr: errGroupSendBlocked,
		},

		// ── 被改坏的数据 ──
		{
			name: "未知策略一律拒绝",
			rows: []GroupRule{rule("a", "someone_hand_edited_the_db", "b")},
			from: "a", to: "b", wantErr: errGroupTargetDenied,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := buildGroupRuleSet(tc.rows).allowsGroup(tc.from, tc.to)
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Same(t, tc.wantErr, err)
		})
	}
}

// TestEnforceGroupPolicyReadsLiveUserGroups 锁定判定入口取的是 users 行上的分组。
//
// 两个调用点(受理阶段与主库锁内)都必须把"这一刻读到的用户行"整个交进来。
// 一旦有人改成传字符串,就会有一处把缓存里的旧分组喂进来 —— 而分组恰恰是
// 管理员随时会改的字段,那正是"提交时合规、落账时已不合规"要拦的东西。
func TestEnforceGroupPolicyReadsLiveUserGroups(t *testing.T) {
	rules := buildGroupRuleSet([]GroupRule{rule("vip", GroupPolicyAllowList, "vip")})

	sender := &model.User{Id: 1, Group: "vip"}
	receiver := &model.User{Id: 2, Group: "vip"}
	require.NoError(t, enforceGroupPolicy(sender, receiver, rules))

	// 管理员在受理与落账之间把收款方调出了 vip:同一份规则必须给出不同结论。
	receiver.Group = "default"
	assert.Same(t, errGroupTargetDenied, enforceGroupPolicy(sender, receiver, rules))

	// 发起方被降级后,连同组的那条白名单也不再适用于他。
	sender.Group = "default"
	receiver.Group = "vip"
	assert.NoError(t, enforceGroupPolicy(sender, receiver, rules), "default 未被任何规则覆盖")

	assert.Same(t, errInvalidParam, enforceGroupPolicy(nil, receiver, rules))
	assert.Same(t, errInvalidParam, enforceGroupPolicy(sender, nil, rules))
}

// TestGroupPolicyIsEnforcedAtBothStagesOfCreate 是"调度层真的接上了"的防线。
//
// 本扩展反复出现的失败模式是:纯函数写对了、测试也绿了,但没有任何调用点消费它
// (审计里的 C1/C2/OLD-1/OLD-2 全是这个形状)。分组规则一旦只剩下 grouprule.go
// 里那个漂亮的判定函数,划转链路会一路放行而单元测试全绿。
//
// 因此这里直接解析 service.go,确认受理阶段与主库锁内两处都调了 enforceGroupPolicy
// —— 少任何一处这条测试就失败:
//   - 少了 loadParties 那处:用户要等到扣款事务里才被拒,白吃一次风控预占;
//   - 少了 applyQuotaTransfer 那处:提交后改分组的窗口完全不设防,而那才是
//     真正会让钱转到不该去的地方的一条路径。
func TestGroupPolicyIsEnforcedAtBothStagesOfCreate(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	require.NoError(t, err)

	callers := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "enforceGroupPolicy" {
				callers[fn.Name.Name] = true
			}
			return true
		})
	}

	assert.True(t, callers["loadParties"],
		"受理阶段没有判分组:用户要等到扣款事务里才被拒,白吃一次冷却与风控预占")
	assert.True(t, callers["applyQuotaTransfer"],
		"主库行锁内没有复判分组:提交之后、落账之前改分组的窗口完全不设防")
}

// TestDescribeGroupPolicyTellsUserWhereTheyCanSend 锁定下发给用户的结论。
//
// allow_all 与"没有规则"必须都收敛成 unrestricted:让用户区分这两者没有意义,
// 只会多一种要翻译、且解释不清的文案。
func TestDescribeGroupPolicyTellsUserWhereTheyCanSend(t *testing.T) {
	cases := []struct {
		name        string
		rows        []GroupRule
		from        string
		wantPolicy  string
		wantAllowed []string
		wantDenied  []string
	}{
		{
			name: "没有规则", from: "vip", wantPolicy: groupViewUnrestricted,
		},
		{
			name: "allow_all 对用户等价于不限制",
			rows: []GroupRule{rule("vip", GroupPolicyAllowAll, "")},
			from: "vip", wantPolicy: groupViewUnrestricted,
		},
		{
			name: "deny_all",
			rows: []GroupRule{rule("vip", GroupPolicyDenyAll, "")},
			from: "vip", wantPolicy: groupViewBlocked,
		},
		{
			name: "白名单原样下发",
			rows: []GroupRule{rule("vip", GroupPolicyAllowList, "default,svip")},
			from: "vip", wantPolicy: GroupPolicyAllowList,
			wantAllowed: []string{"default", "svip"},
		},
		{
			// @self 必须换成真实分组名:给用户看 "@self" 等于没说。
			name: "白名单里的 @self 解析成自己的分组名",
			rows: []GroupRule{rule(groupWildcard, GroupPolicyAllowList, groupSelfToken+",svip")},
			from: "vip", wantPolicy: GroupPolicyAllowList,
			wantAllowed: []string{"vip", "svip"},
		},
		{
			name: "黑名单下发被禁分组",
			rows: []GroupRule{rule("vip", GroupPolicyDenyList, groupSelfToken)},
			from: "vip", wantPolicy: GroupPolicyDenyList,
			wantDenied: []string{"vip"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := describeGroupPolicy(buildGroupRuleSet(tc.rows), tc.from)
			assert.Equal(t, tc.wantPolicy, got.Policy)
			assert.Equal(t, normalizeGroupName(tc.from), got.MyGroup)
			// 切片必须非 nil,否则前端每处都要再兜一次 null。
			require.NotNil(t, got.AllowedGroups)
			require.NotNil(t, got.DeniedGroups)
			if tc.wantAllowed == nil {
				assert.Empty(t, got.AllowedGroups)
			} else {
				assert.Equal(t, tc.wantAllowed, got.AllowedGroups)
			}
			if tc.wantDenied == nil {
				assert.Empty(t, got.DeniedGroups)
			} else {
				assert.Equal(t, tc.wantDenied, got.DeniedGroups)
			}
		})
	}
}

// TestBuildGroupMatrixMatchesTheRealVerdict 锁定管理端矩阵与真实判定同源。
//
// 矩阵是管理员判断"当前谁能转给谁"的唯一依据。它一旦与 allowsGroup 分家,
// 管理端看到的"能转"就会与用户实际遭遇的拒绝对不上 —— 那比没有矩阵更危险,
// 因为它会让人放心地配错。
func TestBuildGroupMatrixMatchesTheRealVerdict(t *testing.T) {
	known := []string{"default", "svip", "vip"}
	rows := []GroupRule{
		rule(groupWildcard, GroupPolicyDenyList, groupSelfToken),
		rule("vip", GroupPolicyAllowList, "default,"+groupSelfToken),
		rule("svip", GroupPolicyDenyAll, ""),
	}
	set := buildGroupRuleSet(rows)
	matrix := buildGroupMatrix(set, known)
	require.Len(t, matrix, len(known))

	byFrom := map[string]groupMatrixRow{}
	for _, row := range matrix {
		byFrom[row.FromGroup] = row
	}

	// default 走兜底(禁止组内互转)。
	assert.Equal(t, GroupPolicyDenyList, byFrom["default"].Policy)
	assert.Equal(t, []string{"svip", "vip"}, byFrom["default"].ToGroups)
	// vip 有专属白名单,@self 已经在矩阵里体现成 vip 自己。
	assert.Equal(t, GroupPolicyAllowList, byFrom["vip"].Policy)
	assert.Equal(t, []string{"default", "vip"}, byFrom["vip"].ToGroups)
	// svip 被整体禁止转出。
	assert.Equal(t, GroupPolicyDenyAll, byFrom["svip"].Policy)
	assert.Empty(t, byFrom["svip"].ToGroups)

	// 逐格与 allowsGroup 对齐:矩阵不能自己另算一套。
	for _, from := range known {
		allowed := map[string]bool{}
		for _, to := range byFrom[from].ToGroups {
			allowed[to] = true
		}
		for _, to := range known {
			assert.Equal(t, buildGroupRuleSet(rows).allowsGroup(from, to) == nil, allowed[to],
				"矩阵格 %s→%s 与真实判定不一致", from, to)
		}
	}

	// 没有任何规则覆盖时必须标成 unrestricted 且列出全部已知分组,
	// 否则管理员会以为自己已经限制住了。
	open := buildGroupMatrix(buildGroupRuleSet(nil), known)
	for _, row := range open {
		assert.Equal(t, groupViewUnrestricted, row.Policy)
		assert.Equal(t, int64(0), row.RuleId)
		assert.Equal(t, known, row.ToGroups)
	}
}

// TestValidateGroupRule 锁定写入侧的归一化与拒绝条件。
func TestValidateGroupRule(t *testing.T) {
	cases := []struct {
		name     string
		in       GroupRule
		wantErr  bool
		wantFrom string
		wantTo   string
	}{
		{
			name:     "白名单去空白去重并归一成逗号分隔",
			in:       GroupRule{FromGroup: " vip ", Policy: GroupPolicyAllowList, ToGroups: " b , c \n b "},
			wantFrom: "vip", wantTo: "b,c",
		},
		{
			name:     "兜底规则的星号原样保留",
			in:       GroupRule{FromGroup: groupWildcard, Policy: GroupPolicyDenyAll},
			wantFrom: groupWildcard, wantTo: "",
		},
		{
			// 名单在这两种策略下没有含义,留着会让人以为它还算数。
			name:     "allow_all 清空名单",
			in:       GroupRule{FromGroup: "vip", Policy: GroupPolicyAllowAll, ToGroups: "b,c"},
			wantFrom: "vip", wantTo: "",
		},
		{
			name:     "@self 被保留",
			in:       GroupRule{FromGroup: "vip", Policy: GroupPolicyDenyList, ToGroups: groupSelfToken},
			wantFrom: "vip", wantTo: groupSelfToken,
		},
		{name: "发起分组为空", in: GroupRule{Policy: GroupPolicyAllowAll}, wantErr: true},
		{
			name: "策略非法",
			in:   GroupRule{FromGroup: "vip", Policy: "whatever"}, wantErr: true,
		},
		{
			// 空白名单 == 禁止发起,但写成 allow_list 看起来像"漏填了",
			// 必须逼运营显式选 deny_all。
			name: "空白名单被拒",
			in:   GroupRule{FromGroup: "vip", Policy: GroupPolicyAllowList}, wantErr: true,
		},
		{
			name: "空黑名单被拒",
			in:   GroupRule{FromGroup: "vip", Policy: GroupPolicyDenyList}, wantErr: true,
		},
		{
			// 分组名里的逗号会让规则读回来时裂成两条,白名单因此静默变宽。
			name: "分组名含逗号被拒",
			in:   GroupRule{FromGroup: "a,b", Policy: GroupPolicyAllowAll}, wantErr: true,
		},
		{
			name: "分组名含空格被拒",
			in:   GroupRule{FromGroup: "vip", Policy: GroupPolicyAllowList, ToGroups: "a b"}, wantErr: true,
		},
		{
			name: "分组名超长被拒",
			in: GroupRule{FromGroup: strings.Repeat("x", maxGroupNameLen+1),
				Policy: GroupPolicyAllowAll}, wantErr: true,
		},
		{
			// 名单里的通配符与 allow_all/deny_all 语义重叠,同一件事两种写法
			// 意味着读规则的人必须两处对照才敢下结论。
			name: "名单里的通配符被拒",
			in:   GroupRule{FromGroup: "vip", Policy: GroupPolicyAllowList, ToGroups: groupWildcard}, wantErr: true,
		},
		{
			name: "名单条数超上限被拒",
			in: GroupRule{FromGroup: "vip", Policy: GroupPolicyAllowList,
				ToGroups: strings.Repeat("g,", maxToGroupEntries) + "last"}, wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.in
			err := validateGroupRule(&row)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantFrom, row.FromGroup)
			assert.Equal(t, tc.wantTo, row.ToGroups)
		})
	}
}

// TestValidateGroupRuleOutputStaysParseable 归一化后的名单必须能被判定端原样读回。
//
// 校验与判定用的是同一个 parseGroupList,但归一化写的是 strings.Join。
// 两者一旦不配套(比如哪天改成分号分隔),白名单会在写入后静默变成空名单。
func TestValidateGroupRuleOutputStaysParseable(t *testing.T) {
	row := GroupRule{
		FromGroup: "vip", Policy: GroupPolicyAllowList,
		ToGroups: "default\nsvip;" + groupSelfToken, Enabled: true,
	}
	require.NoError(t, validateGroupRule(&row))

	set := buildGroupRuleSet([]GroupRule{row})
	assert.NoError(t, set.allowsGroup("vip", "default"))
	assert.NoError(t, set.allowsGroup("vip", "svip"))
	assert.NoError(t, set.allowsGroup("vip", "vip"), "@self 必须在往返之后依然生效")
	assert.Same(t, errGroupTargetDenied, set.allowsGroup("vip", "other"))
}

// TestGroupBlockedReasonsAreDistinct 前端要按 code/reason 分流两种截然不同的处置:
// "换个收款人也许可以"与"换谁都不行"。两者塌缩成一个取值,用户只会不停重试。
func TestGroupBlockedReasonsAreDistinct(t *testing.T) {
	assert.NotEqual(t, blockedGroupDenied, blockedGroupBlocked)
	assert.NotEqual(t, errGroupTargetDenied.Code, errGroupSendBlocked.Code)
	// code 一旦发布就不能改:前端按 code 映射 i18n。
	assert.Equal(t, "qy_transfer_group_denied", errGroupTargetDenied.Code)
	assert.Equal(t, "qy_transfer_group_blocked", errGroupSendBlocked.Code)
}
