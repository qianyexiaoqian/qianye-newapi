package groupmatrix

// usergrouptable_test.go —— 「用户分组」那**一张**表的读接口。
//
// 项目方的原话是「一个列表框即可」,而上一轮为了同一张表要打三个请求
// (矩阵 / 用户分组登记 / 模型分组登记)。这一组用例守的是合并之后的两件事:
//
//	1. 一次调用给全六列(名称 / 注册用户数 / 充值倍率 / 可用模型分组 / 备注 / 每格配置)
//	2. 行轴上**只有用户分组**——模型分组的名字不再混进这张表
//
// 第 2 条是这一轮"一团糟"里最直接的一条:上一轮把 options.GroupRatio 的键
// (那是模型分组)并进了行轴,于是一张本该回答"站上有哪几档人"的表,
// 一半的行不是人。

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useTopupRatios(t *testing.T, m map[string]float64) {
	t.Helper()
	prev := common.TopupGroupRatio2JSONString()
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(mustJSON(t, m)))
	t.Cleanup(func() { _ = common.UpdateTopupGroupRatioByJSONString(prev) })
}

func useCrossRatios(t *testing.T, raw string) {
	t.Helper()
	prev := ratio_setting.GroupGroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(raw))
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(prev) })
}

func seedUserGroupRegistry(t *testing.T, gdb *gorm.DB, rows ...groupns.UserGroup) {
	t.Helper()
	for i := range rows {
		require.NoError(t, gdb.Create(&rows[i]).Error)
	}
}

func rowsByName(view *matrixView) map[string]userGroupRow {
	out := make(map[string]userGroupRow, len(view.UserGroups))
	for _, row := range view.UserGroups {
		out[row.Name] = row
	}
	return out
}

// assertWarns 断言 warnings 里有一条同时提到这两个词的话。
//
// 判据取"两个词都在同一句里"而不是逐字比对整句:整句会把测试钉在文案上,
// 而这里要守的是「这件事被说出来了」——一条只写进日志、界面上看不见的异常
// 与没有发现它是同一回事。
func assertWarns(t *testing.T, view *matrixView, must ...string) {
	t.Helper()
	for _, warn := range view.Warnings {
		hit := true
		for _, word := range must {
			if !strings.Contains(warn, word) {
				hit = false
				break
			}
		}
		if hit {
			return
		}
	}
	t.Fatalf("warnings 里没有同时提到 %v 的那一条,实际是: %v", must, view.Warnings)
}

// TestUserGroupTableCarriesEveryColumn 守"一次给全"。
//
// 每一列都是项目方逐条点名过的。少任何一列,前端就会去拼第二个接口,
// 而拼出来的那一行的人数、可用清单、备注来自三个不同时刻的快照 ——
// 运营照着它做的决定建立在一份从未同时存在过的状态上。
func TestUserGroupTableCarriesEveryColumn(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"paid": "白名单原文"},
		map[string]float64{"vip": 1, "paid": 0.5, "free": 0})
	// vip 配了一个真实折扣;legacy_zero 是**存量的 0** —— 写侧本轮起拒绝它,
	// 但库里可能已经有,而支付路径会把它抬回 1 收款。
	useTopupRatios(t, map[string]float64{"vip": 0.9, "legacy_zero": 0})
	useCrossRatios(t, `{}`)
	useModelGroupNote(t, map[string]string{})

	seedUserGroupRegistry(t, gdb,
		groupns.UserGroup{Name: "vip", DisplayName: "尊享档", Note: "这一档是年付用户",
			Enabled: true, SortOrder: 3, DefaultMode: groupns.DefaultModeInherit},
		groupns.UserGroup{Name: "brand_new", DisplayName: "刚建的", Note: "还没有人",
			Enabled: true, DefaultMode: groupns.DefaultModeInherit},
	)
	require.NoError(t, gdb.Create(newScope("vip", ModeEnforce, false, nil, "为什么给这一档设范围", 1, 1)).Error)
	require.NoError(t, gdb.Create(&Grant{
		UserGroup: "vip", ModelGroup: "paid", Note: "这一格专属备注", CreatedAt: 1, UpdatedAt: 1,
	}).Error)
	require.NoError(t, reload())

	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	rows := rowsByName(view)

	vip, ok := rows["vip"]
	require.True(t, ok)
	assert.Equal(t, "这一档是年付用户", vip.Note,
		"note 必须是**用户分组备注**(登记表),不是 scope 上那段接管说明")
	assert.Equal(t, "为什么给这一档设范围", vip.ScopeNote,
		"接管说明改叫 scope_note —— 两者主语不同,合用一个键会让前端拿到一段答非所问的文字")
	assert.Equal(t, "尊享档", vip.DisplayName)
	assert.True(t, vip.Registered)
	assert.Equal(t, []string{"paid"}, vip.ModelGroups,
		"「可用模型分组」是名称清单而不是个数,而且由后端算 —— "+
			"过滤条件(设了范围看清单、没设看上游)是后端才知道的事")

	require.NotNil(t, vip.TopupRatio,
		"配过的充值倍率必须以 *float64 下发:float64 + omitempty 会让 0 这类值在序列化时消失")
	assert.Equal(t, 0.9, *vip.TopupRatio)
	assert.Equal(t, "0.9", vip.TopupRatioEffective)

	// ── 存量 0:配置值与**收款值**必须分开显示 ────────────────────────────
	//
	// 五条支付路径读到 0 之后一律 `if ratio == 0 { ratio = 1 }`(见
	// effectiveTopupRatio 的注释)。把 0 原样印进"生效倍率"那一列就是骗人:
	// 界面写 0、收款按 1,而这个偏差此前不出现在任何一处。
	legacy, ok := rows["legacy_zero"]
	require.True(t, ok, "只在 TopupGroupRatio 里出现的名字必须在表上 —— "+
		"否则那条死配置永远没有入口可以改")
	require.NotNil(t, legacy.TopupRatio)
	assert.Equal(t, float64(0), *legacy.TopupRatio, "库里原值原样下发,不许静默改写")
	assert.Equal(t, "1", legacy.TopupRatioEffective,
		"生效值必须复刻支付路径:0 在那里被抬回 1")
	assertWarns(t, view, "legacy_zero", "充值倍率")

	fresh, ok := rows["brand_new"]
	require.True(t, ok, "刚建出来、还没有任何用户的分组必须在表上 —— "+
		"只认 users.group 会让「新建一档再把人挪进去」在结构上不可能完成")
	assert.Nil(t, fresh.TopupRatio, "没配过充值倍率必须是 null,不能写 0 冒充")
	assert.Equal(t, "1", fresh.TopupRatioEffective,
		"生效值仍然要给:上游缺键时回落 1 并写一条 SysError,那个 1 不是任何人配出来的")

	// 每格配置(勾选 / 倍率 / 备注)与行头在同一次响应里。
	var cell *cellView
	for i := range view.Cells {
		if view.Cells[i].UserGroup == "vip" && view.Cells[i].ModelGroup == "paid" {
			cell = &view.Cells[i]
		}
	}
	require.NotNil(t, cell)
	assert.True(t, cell.Granted)
	assert.Equal(t, "这一格专属备注", cell.Note)
	assert.Equal(t, "这一格专属备注", cell.EffectiveNote)
	assert.Equal(t, NoteSourceGrant, cell.NoteSource)

	// 模型分组那一张表的四列也在同一次响应里。
	var paid *modelGroupRow
	for i := range view.ModelGroups {
		if view.ModelGroups[i].Name == "paid" {
			paid = &view.ModelGroups[i]
		}
	}
	require.NotNil(t, paid)
	assert.Equal(t, "0.5", paid.BaseRatio)
	assert.True(t, paid.UserSelectable, "「用户可选」= 在全局白名单的键里")
	assert.Equal(t, "白名单原文", paid.UsableDescription)
	for _, mg := range view.ModelGroups {
		if mg.Name == "free" {
			assert.False(t, mg.UserSelectable,
				"不在白名单里的模型分组,「用户可选」必须是 false")
		}
	}
}

// TestUserGroupTableTellsTheTruthAboutEnforcement 守两个"隐式规则"的可见性。
//
// 项目方的口径是「用户分组若配置了可用模型分组则用户只能选这些分组;
// 若没有配置,则按模型分组自己的『用户可选』」。实现上有两处这句话没覆盖到的地方,
// 而两处都会让界面显示成一件事、线上是另一件事:
//
//	shadow 的清单   配过、但一个字节都不生效(读侧逐位返回上游)
//	自我补入        没设清单时,与自己同名且配了倍率的模型分组会被上游补进可选集,
//	                它绕过了「用户可选」开关
//
// 两者都不改行为(改哪一个都会让存量 legacy_dual 那几档人当场 403),
// 但都必须下发成显式字段 —— 一条隐式规则不写在界面上,就等于没有规则。
func TestUserGroupTableTellsTheTruthAboutEnforcement(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	// legacy_dual 同时是用户分组与模型分组(在 GroupRatio 里),但**不在白名单**。
	useUpstreamGroups(t,
		map[string]string{"paid": "白名单原文"},
		map[string]float64{"paid": 1, "legacy_dual": 1, "shadowed": 1})
	// plain 是一档纯粹的用户分组:没设清单,也没有一个同名的模型分组。
	// 它是自我补入这条例外的**反例**,少了它那条断言在"managed 分支根本不跑"
	// 的情况下也会通过。
	useTopupRatios(t, map[string]float64{"legacy_dual": 1, "shadowed": 1, "plain": 1})
	useCrossRatios(t, `{}`)
	useModelGroupNote(t, map[string]string{})

	require.NoError(t, gdb.Create(newScope("shadowed", ModeShadow, false, nil, "", 1, 1)).Error)
	require.NoError(t, gdb.Create(&Grant{
		UserGroup: "shadowed", ModelGroup: "paid", CreatedAt: 1, UpdatedAt: 1}).Error)
	require.NoError(t, reload())

	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	rows := rowsByName(view)

	// 存量遗留行:mode 写着 shadow(迁移没跑到的窗口),而读侧自 shadow 下线起
	// 一个字都不看 mode —— 这份清单此刻正在限制人。管理端必须说同一句话:
	// 画成「尚未生效」会让运营以为这一档还没被收紧,而他手上正有人在 403。
	shadowed := rows["shadowed"]
	assert.Equal(t, ScopeStateSet, shadowed.ScopeState, "确实配过清单")
	assert.True(t, shadowed.ScopeEnforced,
		"有 scope 行就是生效,与 Resolve 同一个谓词 —— 回头判 mode 会让界面撒谎")

	dual := rows["legacy_dual"]
	assert.True(t, dual.SelfInserted,
		"这一档能选到与自己同名的模型分组,靠的是上游的自我补入而不是「用户可选」开关 —— "+
			"隐式规则必须显式下发,否则运营永远解释不了它为什么能选到一个不在白名单里的分组")
	plain := rows["plain"]
	assert.False(t, plain.SelfInserted,
		"plain 没有一个同名的模型分组,自我补入那一步对它什么都不做 —— "+
			"这一条是上面那条断言的反例:少了它,标志位恒为 true 也能全绿")
	assert.Equal(t, []string{"paid"}, plain.ModelGroups,
		"没设清单的那一档只按「用户可选」开关(= 全局白名单的键)算 —— "+
			"legacy_dual 与 shadowed 虽然都在 options.GroupRatio 里,但不在白名单里,"+
			"所以它们不出现在 plain 的可用清单上。"+
			"「没设清单 = 全部可用」这句话的准确含义是「**本页不限制**」,不是「全部模型分组」")
	assert.Equal(t, []string{"legacy_dual", "paid"}, dual.ModelGroups,
		"legacy_dual 比 plain 多出来的那一项就是自我补入 —— 它是这条规则唯一的例外")
}

// TestUserGroupTableExcludesModelGroups 守行轴口径:观测 ∪ 登记 ∪ 以用户分组为键的配置。
//
// options.GroupRatio 的键是**模型分组**,它们不再出现在这张表上;
// 而 GroupGroupRatio 的外层键与 TopupGroupRatio 的键确实是用户分组,
// 它们必须留下 —— 那两处是**只能从这张表进入**的配置,不显示就永远清不掉。
func TestUserGroupTableExcludesModelGroups(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{},
		map[string]float64{"pool_a": 1, "pool_b": 1, "vip": 1})
	useTopupRatios(t, map[string]float64{"topup_only": 1.2})
	useCrossRatios(t, `{"cross_only":{"pool_a":0.3}}`)
	useModelGroupNote(t, map[string]string{})

	seedUserGroupRegistry(t, gdb, groupns.UserGroup{
		Name: "vip", Enabled: true, DefaultMode: groupns.DefaultModeInherit})
	require.NoError(t, gdb.Create(newScope("scoped_only", ModeEnforce, false, nil, "", 1, 1)).Error)
	require.NoError(t, reload())

	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	rows := rowsByName(view)

	for _, name := range []string{"vip", "scoped_only", "cross_only", "topup_only"} {
		assert.Contains(t, rows, name,
			"%s 是一个用户分组(登记 / 设过范围 / 配过交叉倍率 / 配过充值倍率),必须在表上", name)
	}
	assert.NotContains(t, rows, "pool_a",
		"pool_a 只在 options.GroupRatio 里 —— 那张表的键是模型分组,不该出现在用户分组表上")
	assert.NotContains(t, rows, "pool_b")

	// 列轴不受影响:模型分组仍然是 GroupRatio 的键。
	names := make([]string, 0, len(view.ModelGroups))
	for _, mg := range view.ModelGroups {
		names = append(names, mg.Name)
	}
	assert.Subset(t, names, []string{"pool_a", "pool_b"},
		"列轴口径不变:模型分组仍然由 options.GroupRatio 定义")
}

// TestUsableColumnSurvivesGrantsOutsideTheRatioTable 守「可用模型分组」这一列
// **不受列轴约束**。
//
// 列轴是 options.GroupRatio 的键派生的。一份清单完全可能引用一个已经从倍率表里
// 消失的模型分组(运营在「模型分组」页删了一行,而上游删除不做任何引用检查)。
// 早先这一列在列轴循环内侧回填,于是这种行被画成「一个都没有」,而且三个徽章
// 一个都不满足 —— 整格只剩五个字。
//
// 运营读到的结论是「这一档人一个池子都用不了」,真实原因却是「授权指向了两个
// 已经消失的模型分组」。两件事的处置完全相反:前者去勾选,后者去把模型分组加回
// 倍率表。所以这一列必须如实列出名字,并由 warnings 给出那条【需要处理】。
func TestUsableColumnSurvivesGrantsOutsideTheRatioTable(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	// 倍率表里只剩 alive。清单里那两个名字都已经从倍率表消失。
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"alive": 1})
	useTopupRatios(t, map[string]float64{})
	useCrossRatios(t, `{}`)
	useModelGroupNote(t, map[string]string{})

	seedScope(t, gdb, "vip", ModeEnforce, false, "已被删掉的池子", "另一个消失的池子")

	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	row, ok := rowsByName(view)["vip"]
	require.True(t, ok)

	assert.Equal(t, []string{"另一个消失的池子", "已被删掉的池子"}, row.ModelGroups,
		"清单里的名字必须原样列出来 —— 画成「一个都没有」会把"+
			"「授权指向了已消失的模型分组」误导成「这一档人什么都用不了」")
	assert.Equal(t, ScopeStateSet, row.ScopeState)
	assert.True(t, row.ScopeEnforced)
	assertWarns(t, view, "vip", "已被删掉的池子", "已从分组倍率表消失")
}

// TestUsableColumnExcludesAutoPseudoGroup 守 auto 不混进这一列。
//
// auto 是伪分组,由 allow_auto 单独表达。把它列进「可用模型分组」会让运营
// 把它当成一个真的渠道池子去核对渠道数与倍率,而它两样都没有。
func TestUsableColumnExcludesAutoPseudoGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"paid": "", autoGroup: ""},
		map[string]float64{"paid": 1, "unmanaged": 1})
	useTopupRatios(t, map[string]float64{"unmanaged": 0.8})
	useCrossRatios(t, `{}`)
	useModelGroupNote(t, map[string]string{})

	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	row, ok := rowsByName(view)["unmanaged"]
	require.True(t, ok)
	assert.NotContains(t, row.ModelGroups, autoGroup)
	assert.Contains(t, row.ModelGroups, "paid",
		"没设清单的那一档列的是上游此刻的实际可选集合")
}
