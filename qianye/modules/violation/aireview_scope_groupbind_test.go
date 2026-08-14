package violation

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_scope_groupbind_test.go —— 「启用中的作用域策略必须绑定分组」。
//
// 项目方原话:「强制绑定分组,全站模型还是太高了一点」。要挡的不是"这一格是空的",
// 是**一条策略能覆盖全站**这件事 —— 它有两种写法,而只挡一种等于没挡:
//
//	空名单        groupInScope 恒为真 = 全部分组。
//	exclude 名单  名单之外的全部分组,而且随着新分组的建立自动变宽。
//
// 这一组测试钉住四件事,每一件都对应一种真实的坏结果:
//
//  1. 两种写法在启用时都被拒(少挡一种 = 多点一次鼠标就能绕过)。
//  2. **停用**的档照旧能存下去 —— 否则列表上的一键启停(它走的正是这个 upsert)
//     会让一条存量的、正在生效的空作用域策略连"关掉"都做不到。一道本意是收缩
//     暴露面的校验,反过来堵死唯一能立刻收缩暴露面的动作。
//  3. 存量行不被改写、不被丢弃:热路径照旧匹配,但汇总表与启动日志都把它标出来。
//  4. 迁移出来的那一条(全局抽样率)属于第 3 类,而且能靠补齐分组复活。

// TestValidateAIScopeRequiresGroupBinding 是纯校验闸的表。
//
// base 是一条合法的启用策略;每一格只动与"绑定分组"有关的那两列。
func TestValidateAIScopeRequiresGroupBinding(t *testing.T) {
	base := func() AIScope {
		return AIScope{Name: "自助注册", Enabled: true, Priority: 100,
			GroupScope: "selfserve", GroupScopeMode: GroupScopeInclude,
			PreSampleRateBps: 0, AsyncSampleRateBps: 1000}
	}
	tests := []struct {
		name    string
		mutate  func(*AIScope)
		wantErr string
		why     string
	}{
		{
			name: "绑定了分组的启用策略:合法", mutate: func(*AIScope) {},
		},
		{
			name:    "启用 + 分组留空:拒",
			mutate:  func(s *AIScope) { s.GroupScope = "" },
			wantErr: "必须绑定用户分组",
			why: "空名单 = 匹配全站,这一档的抽样率会作用在所有用户身上 —— " +
				"把他们的请求内容发往第三方",
		},
		{
			name:    "启用 + 分组只有空白:拒(归一发生在校验之前)",
			mutate:  func(s *AIScope) { s.GroupScope = "   \n\t " },
			wantErr: "必须绑定用户分组",
			why: "只挡空串不挡空白,等于留了一个按几下空格就能绕过的闸 —— " +
				"而 compileScope 对两者的处理完全一样",
		},
		{
			name:    "启用 + 排除方向:拒",
			mutate:  func(s *AIScope) { s.GroupScopeMode = GroupScopeExclude },
			wantErr: "不能用「排除」方向",
			why: "排除 = 名单之外的全部分组,同样覆盖全站,而且会随时间变宽:" +
				"明天新建的分组会自动进入监控,没有人配置过这件事",
		},
		{
			name: "停用 + 分组留空:放行(存量行必须还关得掉)",
			mutate: func(s *AIScope) {
				s.Enabled = false
				s.GroupScope = ""
			},
			why: "列表上的一键启停走的正是这个 upsert,请求体带的 group_scope 仍然是空的 —— " +
				"无条件必填会让一条正在生效的空作用域策略连「关掉」都做不到",
		},
		{
			name: "停用 + 排除方向:放行(同上)",
			mutate: func(s *AIScope) {
				s.Enabled = false
				s.GroupScopeMode = GroupScopeExclude
			},
			why: "停用的档一次都不会被 scopeFor 看到,拦它挡不住任何东西",
		},
		{
			name: "免审名单(两个抽样率都是 0)仍然要绑分组",
			mutate: func(s *AIScope) {
				s.GroupScope = ""
				s.PreSampleRateBps, s.AsyncSampleRateBps = 0, 0
			},
			wantErr: "必须绑定用户分组",
			why: "一条 0% 的全站策略排在前面会把后面所有档全部遮住 —— " +
				"看起来像「什么都没配」,实际是「什么都不审」",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := base()
			tc.mutate(&row)
			err := validateAIScope(&row)
			if tc.wantErr != "" {
				require.Error(t, err, tc.why)
				assert.Contains(t, err.Error(), tc.wantErr, tc.why)
				return
			}
			assert.NoError(t, err, tc.why)
		})
	}
}

// TestAIScopeGroupUnboundIsOneJudgement 钉住写入闸、汇总表与启动巡检用的是同一个判据。
//
// 三处各写一遍 `GroupScope == ""` 时,漏掉 exclude 的那一处就是这条规则的缺口,
// 而缺口的表现是一条覆盖全站的策略在列表上显示得完全正常。
func TestAIScopeGroupUnboundIsOneJudgement(t *testing.T) {
	tests := []struct {
		name  string
		scope string
		mode  string
		want  bool
	}{
		{"绑了分组 + 包含:绑上了", "selfserve", GroupScopeInclude, false},
		{"空名单:没绑", "", GroupScopeInclude, true},
		{"只有空白:没绑", "  \n ", GroupScopeInclude, true},
		{"有名单但方向是排除:没绑(匹配的是名单之外的全部分组)", "internal", GroupScopeExclude, true},
		{"空名单 + 排除:没绑", "", GroupScopeExclude, true},
		{"方向留空(存量行的形状)按包含算", "selfserve", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, aiScopeGroupUnbound(tc.scope, tc.mode))
		})
	}

	t.Run("与写入闸的结论一致", func(t *testing.T) {
		// 判据与校验必须同进同退:aiScopeGroupUnbound 说"没绑"的每一种取值,
		// 启用时都必须被 validateAIScope 拒掉。两者分开漂移时,界面上会出现
		// 一条被标成"未绑定分组"、却能一键启用的策略。
		for _, tc := range tests {
			row := AIScope{Name: "x", Enabled: true, Priority: 100,
				GroupScope: tc.scope, GroupScopeMode: tc.mode,
				AsyncSampleRateBps: 1000}
			err := validateAIScope(&row)
			assert.Equal(t, tc.want, err != nil,
				"%s:判据说 unbound=%v,写入闸却给了 err=%v", tc.name, tc.want, err)
		}
	})
}

// TestSummarizeAIScopesFlagsUnboundGroups —— 存量行在管理端看得见。
//
// 存量的空作用域策略不会被自动停用(那是一次没人按下过的风控收缩),所以它
// **还在照常匹配全站**。列表上必须标出来:一条覆盖全站的策略与一条只盯一个
// 分组的策略在这张表上长得完全一样,而两者的成本与数据出境面差着整个站点。
func TestSummarizeAIScopesFlagsUnboundGroups(t *testing.T) {
	rows := []AIScope{
		{Id: 1, Name: "自助注册", Enabled: true, Priority: 10,
			GroupScope: "selfserve", GroupScopeMode: GroupScopeInclude},
		{Id: 2, Name: "存量全站档", Enabled: true, Priority: 20,
			GroupScopeMode: GroupScopeInclude},
		{Id: 3, Name: "存量排除档", Enabled: true, Priority: 30,
			GroupScope: "internal", GroupScopeMode: GroupScopeExclude},
		{Id: 4, Name: migratedAIScopeName, Enabled: false, Priority: 10000,
			GroupScopeMode: GroupScopeInclude},
	}
	got := summarizeAIScopes(rows)
	require.Len(t, got, 4)
	assert.False(t, got[0].GroupUnbound)
	assert.True(t, got[1].GroupUnbound, "空名单 = 全部分组")
	assert.True(t, got[2].GroupUnbound,
		"排除 = 名单之外的全部分组 —— 不标出来,这一格就是那道闸的公开绕法")
	assert.True(t, got[3].GroupUnbound,
		"迁移出来的那一条也是存量:停着,但要让人看得出它为什么开不起来")
}

// TestLegacyUnboundScopeStillMatches —— 存量行不被静默丢弃。
//
// 让运行期跳过"不合规"的行是很诱人的一步(库里干净了、界面上也不刺眼),
// 而它的后果是一条正在生效的风控被一次升级悄悄关掉:抽样率照旧显示在界面上,
// 成本页第二天掉下去,而"为什么掉"没有任何地方写着。
func TestLegacyUnboundScopeStillMatches(t *testing.T) {
	gdb := newAIWiringDB(t)
	require.NoError(t, gdb.Create(&AIScope{
		Id: 1, Name: "存量全站档", Enabled: true, Priority: 100,
		GroupScope: "", GroupScopeMode: GroupScopeInclude,
		PreSampleRateBps: 300, AsyncSampleRateBps: 300,
	}).Error)

	scopes, err := buildAIScopes(gdb)
	require.NoError(t, err)
	require.Len(t, scopes, 1,
		"热路径不认识「合不合规」,它只认作用域 —— 在这里过滤等于静默关掉一条风控")

	rt := &aiRuntime{Scopes: scopes}
	_, pre, async := rt.scopeFor("gpt-4o", "谁都没配过的分组")
	assert.Equal(t, 300, pre, "存量行照旧匹配,一个字节都没改")
	assert.Equal(t, 300, async)
}

// TestUnboundGroupScopeRowsReportsBothStates —— 启动期巡检把两种状态分开。
//
// 启用中的那些正在按全站匹配(要立刻处理),停用的那些只是开不起来。
// 两者混成一个数字时,运维分不清"我现在有没有在全站送审"。
func TestUnboundGroupScopeRowsReportsBothStates(t *testing.T) {
	gdb := newAIWiringDB(t)
	rows := []AIScope{
		{Id: 1, Name: "自助注册", Enabled: true, Priority: 10,
			GroupScope: "selfserve", GroupScopeMode: GroupScopeInclude},
		{Id: 2, Name: "存量全站档", Enabled: true, Priority: 20,
			GroupScopeMode: GroupScopeInclude},
		{Id: 3, Name: "两侧带空白的存量行", Enabled: false, Priority: 30,
			GroupScope: "  ", GroupScopeMode: GroupScopeInclude},
		{Id: 4, Name: "存量排除档", Enabled: false, Priority: 40,
			GroupScope: "internal", GroupScopeMode: GroupScopeExclude},
	}
	for i := range rows {
		require.NoError(t, gdb.Create(&rows[i]).Error)
	}

	got, err := unboundGroupScopeRows(context.Background(), gdb)
	require.NoError(t, err)
	require.Len(t, got, 3, "绑好的那一条不该出现在清单里,否则这份日志全是噪声")

	ids := make([]int64, 0, len(got))
	enabled := make([]int64, 0, 1)
	for _, r := range got {
		ids = append(ids, r.Id)
		if r.Enabled {
			enabled = append(enabled, r.Id)
		}
	}
	assert.Equal(t, []int64{2, 3, 4}, ids, "按 priority 升序,与列表页同一个顺序")
	assert.Equal(t, []int64{2}, enabled,
		"只有它正在按全站匹配 —— 这一条与另外两条对运维是完全不同的下一步")

	t.Run("巡检不改任何一行", func(t *testing.T) {
		var after []AIScope
		require.NoError(t, gdb.Order("id asc").Find(&after).Error)
		require.Len(t, after, 4)
		for i := range after {
			assert.Equal(t, rows[i].Enabled, after[i].Enabled,
				"自动停用等于一次升级悄悄关掉一条正在生效的风控")
			assert.Equal(t, rows[i].GroupScope, after[i].GroupScope,
				"自动补上「全部分组」正是项目方要避免的全站匹配,只是换了种写法")
		}
	})
}

// TestUpsertAIScopeGroupBindingGate 是**接口层**的那道闸。
//
// 必须走 handler:界面上的每一次保存与每一次启停都从这里过,而"能不能把一条
// 存量的空作用域策略关掉"这个问题只有在这一层才答得出来。
func TestUpsertAIScopeGroupBindingGate(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantMsg    string
		why        string
	}{
		{
			name: "新建一条启用的空作用域策略:400",
			body: `{"name":"全站后审","enabled":true,"priority":100,` +
				`"group_scope":"","group_scope_mode":"include",` +
				`"pre_sample_rate_bps":0,"async_sample_rate_bps":1000}`,
			wantStatus: http.StatusBadRequest, wantMsg: "必须绑定用户分组",
			why: "这正是项目方点名要挡的那一条:一条策略靠空作用域匹配全站",
		},
		{
			name: "新建一条启用的排除档:400",
			body: `{"name":"除了内部都审","enabled":true,"priority":100,` +
				`"group_scope":"internal","group_scope_mode":"exclude",` +
				`"pre_sample_rate_bps":0,"async_sample_rate_bps":1000}`,
			wantStatus: http.StatusBadRequest, wantMsg: "排除",
			why: "排除是同一件事的另一种写法,只挡空名单等于没挡",
		},
		{
			name: "新建一条绑好分组的启用策略:200",
			body: `{"name":"自助注册","enabled":true,"priority":100,` +
				`"group_scope":"selfserve","group_scope_mode":"include",` +
				`"pre_sample_rate_bps":0,"async_sample_rate_bps":1000}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "存草稿(停用)时不要求分组:200",
			body: `{"name":"待补齐","enabled":false,"priority":100,` +
				`"group_scope":"","group_scope_mode":"include",` +
				`"pre_sample_rate_bps":0,"async_sample_rate_bps":1000}`,
			wantStatus: http.StatusOK,
			why:        "停用的档一次都不会被 scopeFor 看到;它想生效时还要再过一次这道闸",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newAIScopeChannelEnv(t)
			c, rec := aiScopeCtx(t, http.MethodPut, "/violation/ai-review/scopes", tc.body)
			adminUpsertAIScope(c)

			assert.Equal(t, tc.wantStatus, rec.Code, tc.why)
			if tc.wantMsg != "" {
				assert.Contains(t, rec.Body.String(), tc.wantMsg, tc.why)
			}
			var n int64
			require.NoError(t, gdb.Model(&AIScope{}).Count(&n).Error)
			if tc.wantStatus == http.StatusOK {
				assert.EqualValues(t, 1, n)
				return
			}
			assert.EqualValues(t, 0, n, "被闸挡下的策略一行都不该落库")

			var audits []qymodel.AuditLog
			require.NoError(t, gdb.Find(&audits).Error)
			require.Len(t, audits, 1,
				"失败也要留痕:「我配了三次都没保存上」只能靠失败审计回答")
			assert.Equal(t, qymodel.ResultFail, audits[0].Result)
		})
	}
}

// TestUpsertAIScopeCanStillDisableLegacyUnboundRow 是这道闸唯一一个不能挡的动作。
//
// 列表上的一键启停走的正是 upsert,请求体是整条策略原样回传、只翻 enabled ——
// 也就是说它带着的 group_scope 仍然是空的。无条件必填时这一次提交会 400,
// 于是一条正在全站送审的存量策略**关不掉**:一道本意是收缩暴露面的校验,
// 反过来堵死唯一能立刻收缩暴露面的动作。
func TestUpsertAIScopeCanStillDisableLegacyUnboundRow(t *testing.T) {
	gdb := newAIScopeChannelEnv(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&AIScope{
		Id: 7, Name: "存量全站档", Enabled: true, Priority: 100,
		GroupScope: "", GroupScopeMode: GroupScopeInclude,
		PreSampleRateBps: 500, AsyncSampleRateBps: 500,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	off := `{"id":7,"name":"存量全站档","enabled":false,"priority":100,` +
		`"group_scope":"","group_scope_mode":"include",` +
		`"pre_sample_rate_bps":500,"async_sample_rate_bps":500}`
	c, rec := aiScopeCtx(t, http.MethodPut, "/violation/ai-review/scopes", off)
	adminUpsertAIScope(c)
	require.Equal(t, http.StatusOK, rec.Code,
		"关掉一条存量的空作用域策略必须一直做得到 —— 它是唯一能立刻收缩暴露面的动作")

	var row AIScope
	require.NoError(t, gdb.Where("id = ?", 7).Take(&row).Error)
	assert.False(t, row.Enabled)
	assert.Equal(t, 500, row.PreSampleRateBps,
		"关掉不等于清空:那个数字还要留着,补齐分组之后原样可用")

	t.Run("再想把它开回来就必须先补分组", func(t *testing.T) {
		on := `{"id":7,"name":"存量全站档","enabled":true,"priority":100,` +
			`"group_scope":"","group_scope_mode":"include",` +
			`"pre_sample_rate_bps":500,"async_sample_rate_bps":500}`
		c, rec := aiScopeCtx(t, http.MethodPut, "/violation/ai-review/scopes", on)
		adminUpsertAIScope(c)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "必须绑定用户分组")

		var after AIScope
		require.NoError(t, gdb.Where("id = ?", 7).Take(&after).Error)
		assert.False(t, after.Enabled, "被拒的那一次不能留下任何一半的写入")
	})
}
