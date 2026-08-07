package transfer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rule 是构造一条启用规则的简写,单个用例只写自己关心的字段。
func rule(from, policy, to string) GroupRule {
	return GroupRule{FromGroup: from, Policy: policy, ToGroups: to, Enabled: true}
}

// useUserGroupRegistry 把**用户分组登记表**换成用例自己的清单。
//
// 划转规则与门槛分档的键都是 users.group,取值域因此来自 qy_user_groups
// (见 definedUserGroups 的缺陷回归),而不是分组倍率表 —— 后者是模型分组。
// 直接发布一份 groupns 快照,免得为了几个名字去装一个扩展库。
func useUserGroupRegistry(t *testing.T, names ...string) {
	t.Helper()
	rows := make([]groupns.UserGroup, 0, len(names))
	for _, name := range names {
		rows = append(rows, groupns.UserGroup{
			Name: name, Enabled: true, DefaultMode: groupns.DefaultModeInherit,
		})
	}
	groupns.SetSnapshotForTest(rows, nil)
	t.Cleanup(groupns.ResetForTest)
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

// TestGroupRuleNamesAreCaseFolded 锁定分组名的大小写口径。
//
// 缺陷:from_group / to_groups 都是 varchar(64) 且没有指定排序规则,而扩展库固定
// 是 MySQL —— AutoMigrate 建表时继承库默认排序规则(5.7 utf8mb4_general_ci、
// 8.0 utf8mb4_0900_ai_ci),两者都**大小写不敏感**;而 byGroup 是 Go map 的精确
// 匹配。一条 deny_all 配在 VIP 上、用户的 users.group 是 vip,map 查不到就落到
// 兜底规则,没有兜底就是完全不设防 —— 一道资金闸门静默失效,且规则列表里
// 那条 deny_all 还好端端地摆在那儿。
//
// 这条测试跑在 sqlite 上照样有意义,因为修复走的是"代码跟上存储"(全链路折叠
// 大小写)而不是 ALTER TABLE:判定与写入两侧都只产生小写键,与数据库的排序规则
// 无关,因此在任何数据库上都可断言。把 normalizeGroupName / parseGroupList 里的
// 折叠去掉,下面每一组断言都会翻面。
func TestGroupRuleNamesAreCaseFolded(t *testing.T) {
	// ① 最要命的一侧:禁止发起的闸门不能因为大小写变体而失效。
	blocked := buildGroupRuleSet([]GroupRule{rule("VIP", GroupPolicyDenyAll, "")})
	for _, from := range []string{"vip", "VIP", "Vip", "  vIp  "} {
		assert.ErrorIs(t, blocked.allowsGroup(from, "default"), errGroupSendBlocked,
			"deny_all 对 %q 失效了 —— 这条规则等于完全不设防", from)
	}
	assert.ErrorIs(t, enforceGroupPolicy(
		&model.User{Id: 1, Group: "vip"}, &model.User{Id: 2, Group: "default"}, blocked),
		errGroupSendBlocked,
		"判定入口读的是 users 行上的原始大小写,它必须和规则表对得上")

	// ② 黑名单里的大小写变体:漏掉就是静默放走本该被拦的资金。
	deny := buildGroupRuleSet([]GroupRule{rule("a", GroupPolicyDenyList, "B")})
	assert.ErrorIs(t, deny.allowsGroup("a", "b"), errGroupTargetDenied)
	assert.ErrorIs(t, deny.allowsGroup("A", "B"), errGroupTargetDenied)

	// ③ 白名单里的大小写变体:漏掉是反方向的错 —— 名单静默变窄,合法划转被拒。
	allow := buildGroupRuleSet([]GroupRule{rule("A", GroupPolicyAllowList, "B,C")})
	assert.NoError(t, allow.allowsGroup("a", "b"))
	assert.NoError(t, allow.allowsGroup("a", "C"))
	assert.ErrorIs(t, allow.allowsGroup("a", "d"), errGroupTargetDenied)

	// ④ @self 判"同组"用的是归一化后的两个值,不能因为大小写不同就变成跨组。
	self := buildGroupRuleSet([]GroupRule{rule("VIP", GroupPolicyAllowList, groupSelfToken)})
	assert.NoError(t, self.allowsGroup("vip", "VIP"))
	assert.ErrorIs(t, self.allowsGroup("vip", "default"), errGroupTargetDenied)

	// ⑤ 写入侧:from_group 与名单一起落成小写。有了这一步,"一个分组至多一条
	//    规则"这个唯一索引承诺在大小写不敏感与敏感的数据库上是同一个结论。
	row := GroupRule{FromGroup: " VIP ", Policy: GroupPolicyAllowList, ToGroups: "B, b ,C"}
	require.NoError(t, validateGroupRule(&row))
	assert.Equal(t, "vip", row.FromGroup)
	assert.Equal(t, "b,c", row.ToGroups, "只差大小写的两项必须被折叠成一项")

	// ⑥ 归一化后的规则必须能被判定端原样读回(与 …OutputStaysParseable 同理,
	//    但这里锁的是大小写往返)。
	set := buildGroupRuleSet([]GroupRule{{FromGroup: row.FromGroup, Policy: row.Policy,
		ToGroups: row.ToGroups, Enabled: true}})
	assert.NoError(t, set.allowsGroup("VIP", "B"))
	assert.ErrorIs(t, set.allowsGroup("VIP", "D"), errGroupTargetDenied)
}

// TestTransferGroupNameFollowsSharedContract 把本模块的分组名口径钉在共享实现上。
//
// 存在理由:commission 的费率表与本模块的规则表都以分组名为键,同仓一度有三份
// 各自演化的 normalizeGroup,其中出钱的那一份恰好是最宽松的(只 TrimSpace、
// 不折叠大小写、空串逃逸)。两个模块现在都只转调 qianye/groupname,这条断言
// 保证的是"没有人把它悄悄换回一份私有实现" —— 换回去的那一刻两个模块就会
// 再次分叉,而分叉的表现是一边放行一边拦截,极难归因。
func TestTransferGroupNameFollowsSharedContract(t *testing.T) {
	for _, in := range []string{"vip", "VIP", " Vip ", "", "   ", "DEFAULT", "*", groupSelfToken, "内部测试组"} {
		assert.Equal(t, groupname.Effective(in), normalizeGroupName(in),
			"分组名口径必须与 qianye/groupname 一致(输入 %q)", in)
	}
	assert.Equal(t, defaultGroupName, groupname.Default,
		"本模块的 default 常量必须与共享口径折叠出的空值一致")
}

// useGroupRatio 把分组倍率表换成用例自己的清单,并在结束后原样还原。
//
// 必须显式设置:倍率表有进程级默认值(default/vip/svip),依赖它会让"这个分组
// 到底定义没定义"取决于别的用例有没有动过全局状态。
func useGroupRatio(t *testing.T, jsonStr string) {
	t.Helper()
	prev := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(jsonStr))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(prev))
	})
}

// TestGroupMatrixCoversGroupsReferencedByRules 是 96-audit-r3 transfer #1 的回归。
//
// # 缺陷原样
//
// 规则能成功创建(HTTP 200 落库)、判定也真的生效,但矩阵的取值域只有
// definedGroups 那三个来源(users.group 默认值 / 分组倍率表 / 可选分组白名单),
// 规则表自身的 from_group 与 to_groups 完全不参与。于是给一个不在倍率表里的
// 分组配规则之后:
//   - 它不成行 —— 管理员在矩阵里找不到自己刚配的那条规则;
//   - 它不成列 —— 别的分组"能不能转给它"这一格根本不存在;
//   - 更糟的是,若该分组恰好是别人白名单里唯一的目标,那一行会显示成
//     "谁都转不了",而实际判定是放行的。
//
// 矩阵是这一页存在的理由("矩阵在上"是页面注释里写死的),取值域漏掉规则自己
// 引用的分组,等于让管理员照着一张看不见那条规则的图做决定。
//
// 把 knownGroups 改回只读 definedUserGroups(不并规则表),下面每一组断言都会翻面。
func TestGroupMatrixCoversGroupsReferencedByRules(t *testing.T) {
	// 站点登记过的用户分组里刻意**不含** qingxin 与 agent:它们只存在于规则表里,
	// 正是勘察实测的那条 `清芯 → allow_list[agent]`。
	useUserGroupRegistry(t, "default", "vip")

	rows := []GroupRule{
		rule("qingxin", GroupPolicyAllowList, "agent"),
		// 停用的规则同样要贡献取值域:运营要能在矩阵里先看清"启用之后会怎样"。
		{FromGroup: "legacy", Policy: GroupPolicyDenyAll, Enabled: false},
	}
	known := knownGroups(rows)

	assert.Contains(t, known, "qingxin", "规则的发起分组必须进入矩阵取值域")
	assert.Contains(t, known, "agent", "白名单里的目标分组必须进入矩阵取值域")
	assert.Contains(t, known, "legacy", "停用规则引用的分组同样要能在矩阵里看到")
	assert.Contains(t, known, "default")
	assert.Contains(t, known, "vip")
	assert.NotContains(t, known, groupWildcard, "通配符不是分组名")
	assert.NotContains(t, known, groupSelfToken, "@self 不是分组名")

	set := buildGroupRuleSet(rows)
	byFrom := map[string]groupMatrixRow{}
	for _, row := range buildGroupMatrix(set, known) {
		byFrom[row.FromGroup] = row
	}

	// ① 行:规则的发起分组必须有自己的一行,且策略是它自己的策略。
	row, ok := byFrom["qingxin"]
	require.True(t, ok, "规则的发起分组在矩阵里连一行都没有 —— 页面看起来就是规则没保存上")
	assert.Equal(t, GroupPolicyAllowList, row.Policy)

	// ② 列:白名单里的目标必须成为一列,并且这一格显示为放行。
	assert.Equal(t, []string{"agent"}, row.ToGroups,
		"矩阵里 qingxin 那一行必须正好列出 agent,而不是空成『谁都转不了』")

	// ③ 矩阵与真实判定必须逐格一致 —— 取值域变完整不能顺手改动任何判定。
	for _, from := range known {
		allowed := map[string]bool{}
		for _, to := range byFrom[from].ToGroups {
			allowed[to] = true
		}
		for _, to := range known {
			assert.Equal(t, set.allowsGroup(from, to) == nil, allowed[to],
				"矩阵格 %s→%s 与真实判定不一致", from, to)
		}
	}
	// 判定端本来就是放行的(勘察实测过的那一条),矩阵现在终于说了同一件事。
	assert.NoError(t, set.allowsGroup("qingxin", "agent"))
}

// TestUnknownRuleGroupsIsWarningNotGate 锁定"未知分组"是软告警而不是硬闸门。
//
// 两个方向都要钉住:
//   - 报得出来 —— 打错一个字母的分组名会静默变成一条永不命中的规则,
//     不告警运营就只能靠肉眼对拼写;
//   - 拦不下来 —— 历史分组(倍率表里已删、users 里还有人挂着)恰恰是最需要
//     限制转出的那批账号。把 unknownRuleGroups 的结论接进 validateGroupRule
//     当拒绝条件,运营就会在最需要配置的时刻配不进去。
func TestUnknownRuleGroupsIsWarningNotGate(t *testing.T) {
	useUserGroupRegistry(t, "default", "vip")

	rows := []GroupRule{
		rule("vip", GroupPolicyAllowList, "default,agent"),
		rule("qingxin", GroupPolicyDenyAll, ""),
		rule(groupWildcard, GroupPolicyAllowList, groupSelfToken),
	}
	assert.Equal(t, []string{"agent", "qingxin"}, unknownRuleGroups(rows),
		"只有站点没定义过的分组才该被标黄;通配符与 @self 不是分组名")

	// 全部分组都定义过时必须是**空切片而不是 nil**:前端拿到 null 还要各自兜底。
	clean := unknownRuleGroups([]GroupRule{rule("vip", GroupPolicyAllowList, "default")})
	require.NotNil(t, clean)
	assert.Empty(t, clean)

	// 闸门方向:同一条规则必须照样能通过写入校验。
	row := GroupRule{FromGroup: "qingxin", Policy: GroupPolicyAllowList, ToGroups: "agent"}
	require.NoError(t, validateGroupRule(&row),
		"未知分组是告警不是拒绝 —— 历史分组必须仍然能配规则")
	assert.Equal(t, "qingxin", row.FromGroup)
	assert.Equal(t, "agent", row.ToGroups)
}

// TestListGroupCandidatesCarriesMetadata 锁定下拉候选带着运营下判断需要的元数据。
//
// 裸 datalist 只提示、不校验、不告警。换成带元数据的下拉之后,"这个分组倍率
// 多少 / 底下还有没有启用的渠道 / 是不是公开可选"这三件事必须真的下发,
// 否则换掉 datalist 只是换了个外观。
func TestListGroupCandidatesCarriesMetadata(t *testing.T) {
	mainDB := newMainDB(t)
	require.NoError(t, mainDB.AutoMigrate(&model.Ability{}))
	useGroupRatio(t, `{"default":1,"vip":0.8,"ghost":1}`)

	require.NoError(t, mainDB.Create(&model.Ability{
		Group: "vip", Model: "gpt-4o", ChannelId: 1, Enabled: true,
	}).Error)
	require.NoError(t, mainDB.Create(&model.Ability{
		Group: "default", Model: "gpt-4o", ChannelId: 2, Enabled: false,
	}).Error)

	options, probeOK := listGroupCandidates()
	require.True(t, probeOK)

	byName := map[string]groupCandidate{}
	for _, o := range options {
		byName[o.Name] = o
	}
	require.Contains(t, byName, "vip")
	require.NotNil(t, byName["vip"].Ratio, "名字逐字相同的分组必须带出它真实的兜底倍率")
	assert.InDelta(t, 0.8, *byName["vip"].Ratio, 1e-9)
	assert.True(t, byName["vip"].HasChannels)
	assert.False(t, byName["default"].HasChannels, "禁用的 ability 不算可用渠道")
	assert.False(t, byName["ghost"].HasChannels)
	// 候选清单只列站点定义过的分组:软告警要靠"下拉里有的就是定义过的"这个前提。
	assert.NotContains(t, byName, "agent")

	// 名字必须与落库口径一致,否则运营选一项保存后再回来会发现它不在下拉里。
	useGroupRatio(t, `{"VIP":0.8}`)
	folded, _ := listGroupCandidates()
	names := []string{}
	for _, o := range folded {
		names = append(names, o.Name)
	}
	assert.Contains(t, names, "vip")
	assert.NotContains(t, names, "VIP")
	// 折叠过大小写的名字**不得**带出倍率:倍率侧 GetGroupRatio 是精确 map 查找,
	// 请求走 "vip" 时它查不到 GroupRatio["VIP"],会 fail-open 按 1.0 扣费。
	// 界面显示 0.8、实扣 1.0 正是这份清单要消灭的骗人数字。
	for _, o := range folded {
		if o.Name == "vip" {
			assert.Nil(t, o.Ratio,
				"大小写被折叠过的名字上挂了一个查不到的倍率 —— 界面 0.8、实扣 1.0")
		}
	}
}

// TestGroupCandidateShapeMatchesUserGroupModule 挡住"同一概念的第 N 份拷贝各自漂移"。
//
// 「带元数据的分组下拉」在本模块与 usergroup 各有一份实现(候选范围口径不同,
// 那边要排除 auto 伪分组)。字段名一旦分叉,前端就要为同一件事写两套渲染,
// 而这正是本仓库已经出现过六组的那个缺陷形状。
//
// 这里锁的是**跨模块的 JSON 契约**,不是实现细节:任何一侧改名,这条断言变红。
func TestGroupCandidateShapeMatchesUserGroupModule(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "usergroup", "groups.go"))
	require.NoError(t, err)

	mine := reflect.TypeOf(groupCandidate{})
	require.Equal(t, 4, mine.NumField())
	for i := 0; i < mine.NumField(); i++ {
		tag := mine.Field(i).Tag.Get("json")
		require.NotEmpty(t, tag)
		assert.Contains(t, string(src), `json:"`+tag+`"`,
			"字段 %q 在 usergroup 的 groupOption 上找不到同名 JSON 字段 —— "+
				"两份『分组下拉』已经开始漂移,前端会被迫为同一件事写两套渲染", tag)
	}
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

// TestTransferGroupNamespaceIsUserGroupsNotModelGroups 是本轮那条命名空间缺陷的回归。
//
// # 缺陷原样
//
// 判定端从第一天起就是用户分组:enforceGroupPolicy 收的是 model.User.Group
// (users.group),残留处置也注册在 groupns.RegisterResidue(用户分组那一侧)。
// 但**取值域**当时来自 ratio_setting.GetGroupRatioCopy() 与
// setting.GetUserUsableGroupsCopy() —— 用户分组与模型分组分家之后,那两份
// 都是模型分组(渠道池子的兜底倍率键 / 令牌能挑哪个池子的白名单)。
//
// 于是:运营从下拉里挑一个"分组"配限制,配出来的是一条永不命中的规则
// (没有任何用户的 users.group 等于那个名字);矩阵的行与列也全是模型分组,
// 与真正的判定对不上号;而真正的用户分组反而会被软告警标成"站点没定义过"。
//
// 判定一个字节都没受影响 —— 这正是它能一直藏着的原因。
//
// 把 definedUserGroups 换回读倍率表,下面每一组断言都会翻面。
func TestTransferGroupNamespaceIsUserGroupsNotModelGroups(t *testing.T) {
	// 模型分组那一侧有 pool_a,用户分组那一侧有 vip,两边刻意不重名。
	useGroupRatio(t, `{"default":1,"pool_a":0.5}`)
	useUserGroupRegistry(t, "default", "vip")

	defined, ok := definedUserGroups()
	require.True(t, ok, "登记表已发布快照,探测必须成功")
	assert.True(t, defined["vip"], "用户分组必须进入取值域")
	assert.True(t, defined[defaultGroupName], "default 恒在:它是 users.group 的列默认值")
	assert.False(t, defined["pool_a"],
		"模型分组绝不能被当成用户分组 —— 那正是让运营配出一条永不命中的规则的那一步")

	// 下拉候选同上:两页(分组限制、门槛分档)填的都是 users.group。
	names := map[string]bool{}
	options, probeOK := listUserGroupCandidates()
	require.True(t, probeOK)
	for _, o := range options {
		names[o.Name] = true
	}
	assert.True(t, names["vip"])
	assert.True(t, names[defaultGroupName])
	assert.False(t, names["pool_a"])

	// 软告警的方向:引用了用户分组不告警,引用了模型分组的名字要告警。
	assert.Empty(t, unknownRuleGroups([]GroupRule{rule("vip", GroupPolicyAllowList, "default")}))
	assert.Equal(t, []string{"pool_a"},
		unknownRuleGroups([]GroupRule{rule("vip", GroupPolicyAllowList, "pool_a")}),
		"模型分组的名字出现在划转规则里,恰恰是最该被标黄的情况")
}

// TestUnknownGroupWarningIsSuppressedWhenRegistryIsUnreadable 锁住假警报那一侧。
//
// 登记表读不到时每一个名字都会被算成"未登记"。整页标黄比不标黄更糟:
// 运营会以为自己配的所有规则都失效了,而实际它们全都在生效。
func TestUnknownGroupWarningIsSuppressedWhenRegistryIsUnreadable(t *testing.T) {
	// 不发布快照、也不接扩展库 ⇒ 探测失败。
	groupns.ResetForTest()

	defined, ok := definedUserGroups()
	assert.False(t, ok, "读不到登记表时必须如实说'不确定'")
	assert.True(t, defined[defaultGroupName], "即便如此 default 也必须在")

	rows := []GroupRule{rule("vip", GroupPolicyAllowList, "agent")}
	assert.Empty(t, unknownRuleGroups(rows),
		"探测失败时必须整块收起软告警,而不是把每个名字都标成未登记")
	assert.Empty(t, unknownTierGroups([]GroupLimit{{UserGroup: "vip"}}))
}
