package groupns

// modelgroup_delete_test.go —— 「删一个模型分组」的联动与三道闸门。
//
// 这些用例保护的不是某段实现,而是**联动的完整性**:上游版本的删除只把一行从
// options.GroupRatio 里拿掉,其余十一处引用原样留着,而那些残留没有任何症状 ——
// 接口 200、界面上那一行消失了,只有事后才会发现「授权还在」「auto 顺序里还有它」
// 「有一档人的默认模型分组指着一个不存在的名字」。
//
// 因此断言逐项点名每一处引用。少断言一处 = 那一处将来被删掉时不会有任何测试变红。

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newMainTestDB 复用 usergroup_maindb_test.go 的主库脚手架,并补上 channels。
//
// channels 必须真的在场:影响面里 abilities 口径与 channels 口径是**两个数**,
// 而「删模型分组却留着渠道绑定」这条闸门判的正是两者的并集。只建 abilities
// 会让闸门在"缓存模式下真实路由由 channels 决定"这一档上恒为假。
func newMainTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := newMainDB(t)
	require.NoError(t, gdb.AutoMigrate(&model.Channel{}))
	return gdb
}

// useOptionSnapshot 装载 options 侧的四份分组配置并在用例结束后还原。
//
// 四份一起装是刻意的:删除必须**同时**改这四处,只装其中一两份的用例会在
// 另外两处被漏掉时照样全绿。
func useOptionSnapshot(t *testing.T, groupRatio, usable, autoGroups, crossRatio string) {
	t.Helper()
	// model.UpdateOptionsBulk 落库之后会把新值写进 common.OptionMap(那是
	// /api/option 回读与其它节点广播的来源)。测试绕过了 InitOptionMap,
	// 它是 nil —— 不建的话删除路径会在"写内存快照"这一步 panic,
	// 而那正是生产上真正会跑到的一步。
	prevOptionMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = prevOptionMap })

	prevRatio := ratio_setting.GroupRatio2JSONString()
	prevUsable := setting.UserUsableGroups2JSONString()
	prevAuto := setting.AutoGroups2JsonString()
	prevCross := ratio_setting.GroupGroupRatio2JSONString()

	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(groupRatio))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(usable))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(autoGroups))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(crossRatio))

	t.Cleanup(func() {
		_ = ratio_setting.UpdateGroupRatioByJSONString(prevRatio)
		_ = setting.UpdateUserUsableGroupsByJSONString(prevUsable)
		_ = setting.UpdateAutoGroupsByJsonString(prevAuto)
		_ = ratio_setting.UpdateGroupGroupRatioByJSONString(prevCross)
	})
}

// seedDeletableModelGroup 建一条登记行 + 一个声明了残留的假模块。
//
// 假模块而不是真的 import groupmatrix:那会成环(planentitlement→groupns),
// 而注册表存在的全部理由就是不让这个环出现。用假模块反而更严格 ——
// 它证明的是「注册表里的每一个处置都被调用了」,与具体是哪个模块无关。
func seedDeletableModelGroup(t *testing.T, gdb *gorm.DB, name string) *fakeResidue {
	t.Helper()
	require.NoError(t, gdb.Create(newModelGroup(name, 1, now())).Error)

	fake := &fakeResidue{target: name}
	ResetModelResiduesForTest()
	RegisterModelResidue(ModelResidueHandler{
		Module: "fake", Probe: fake.probe, Sweep: fake.sweep,
	})
	t.Cleanup(ResetModelResiduesForTest)
	return fake
}

type fakeResidue struct {
	target      string
	rows        int64
	disposition string
	swept       int
}

func (f *fakeResidue) probe(_ *gorm.DB, modelGroup string) ([]Residue, error) {
	if modelGroup != f.target {
		return nil, nil
	}
	d := f.disposition
	if d == "" {
		d = ResidueClean
	}
	return []Residue{{
		Module: "fake", Table: "fake_table", Label: "假模块的授权",
		Rows: f.rows, Disposition: d,
	}}, nil
}

func (f *fakeResidue) sweep(_ *gorm.DB, modelGroup string) error {
	if modelGroup == f.target {
		f.swept++
	}
	return nil
}

// TestDeleteModelGroupClearsEveryOptionReference 是本轮的主断言。
//
// 项目方原话:「模型分组删除的分组,用户那边有的也要一并移除,这里包括:
// 全局(用户可选分组)」。这里把"一并移除"的四处 options 逐项钉住,
// 并且顺带证明**别的分组一个字节都不受影响** —— 一次删除顺手改掉另一个分组的
// 倍率是这条路径最坏的失败方式,而它在界面上完全看不出来。
func TestDeleteModelGroupClearsEveryOptionReference(t *testing.T) {
	gdb := newTestDB(t)
	newMainTestDB(t)
	nsConfig(t, true, "", "")
	syncHotAsync(t)
	useOptionSnapshot(t,
		`{"default":1,"待删分组":0.5,"留下的分组":2}`,
		`{"default":"默认分组","待删分组":"要删掉的","留下的分组":"别动我"}`,
		`["待删分组","留下的分组"]`,
		`{"vip":{"待删分组":0.3,"留下的分组":0.9},"只配了它的档":{"待删分组":0.1}}`)
	fake := seedDeletableModelGroup(t, gdb, "待删分组")

	res, err := DeleteModelGroup(context.Background(), model.DB, gdb, "待删分组",
		DeleteModelGroupOptions{}, 7)
	require.NoError(t, err)
	require.Nil(t, res.Partial)
	assert.Equal(t, 1, fake.swept, "注册表里的每一个 Sweep 都必须被调用一次")
	assert.ElementsMatch(t,
		[]string{"AutoGroups", "GroupGroupRatio", "GroupRatio", "UserUsableGroups"},
		res.RemovedFrom)

	assert.Equal(t, map[string]float64{"default": 1, "留下的分组": 2},
		ratio_setting.GetGroupRatioCopy(), "GroupRatio 里必须只少了被删的那一个键")
	assert.Equal(t, map[string]string{"default": "默认分组", "留下的分组": "别动我"},
		setting.RawUserUsableGroupsCopy(),
		"全局「用户可选分组」必须一并移除 —— 这是项目方点名的那一处")
	assert.Equal(t, []string{"留下的分组"}, setting.GetAutoGroups(),
		"auto 顺序里留着一个已删分组 = 每次 auto 都白试一轮")

	var cross map[string]map[string]float64
	require.NoError(t, common.UnmarshalJsonStr(ratio_setting.GroupGroupRatio2JSONString(), &cross))
	assert.Equal(t, map[string]map[string]float64{"vip": {"留下的分组": 0.9}}, cross,
		"交叉倍率的**内层**键要摘掉;摘完变成空行的外层键也不留 —— "+
			"留一个空对象只会让 options 里堆满永远不会被读到的空壳")

	var left int64
	require.NoError(t, gdb.Model(&ModelGroup{}).Where("name = ?", "待删分组").Count(&left).Error)
	assert.Zero(t, left, "登记行本身要删掉")
}

// TestDeleteModelGroupIsBlockedByPinnedUserGroups 钉住整条链上**最危险**的那一处。
//
// 被 pin 的模型分组一旦消失,那一整档人的空分组令牌下一次请求全部 500
// (middleware/auth.go 的 pin 校验失败),而它们此前是好的。自动重置成 inherit
// 看起来更"贴心",但那会让这批令牌直接回到 503 —— 正是 pin 当初要修的东西。
// 所以这一条是**不可覆盖**的硬拦。
func TestDeleteModelGroupIsBlockedByPinnedUserGroups(t *testing.T) {
	gdb := newTestDB(t)
	newMainTestDB(t)
	nsConfig(t, true, "", "")
	syncHotAsync(t)
	useOptionSnapshot(t, `{"被钉住的分组":1}`, `{}`, `[]`, `{}`)
	seedDeletableModelGroup(t, gdb, "被钉住的分组")
	seedUserGroup(t, gdb, "某一档人", DefaultModePin, "被钉住的分组")

	impact, err := ProbeModelGroupImpact(context.Background(), model.DB, gdb, "被钉住的分组")
	require.NoError(t, err)
	assert.Equal(t, []string{"某一档人"}, impact.PinnedByUserGroups)
	require.NotEmpty(t, impact.Blockers)

	// 两道可覆盖闸门都勾上也没用:硬拦不接受覆盖。
	_, err = DeleteModelGroup(context.Background(), model.DB, gdb, "被钉住的分组",
		DeleteModelGroupOptions{ForceHasRoute: true, ForceOrphanTokens: true}, 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qy_modelgroup_blocked")

	var still int64
	require.NoError(t, gdb.Model(&ModelGroup{}).Where("name = ?", "被钉住的分组").Count(&still).Error)
	assert.EqualValues(t, 1, still, "被拒绝的删除必须一个字节都没改")
	assert.True(t, ratio_setting.ContainsGroupRatio("被钉住的分组"),
		"被拒绝的删除必须没有碰 options")
}

// TestDeleteModelGroupNeedsForceWhenChannelsOrTokensRemain 钉住两道**可覆盖**闸门。
//
// 两者分开而不是合成一个确认:后果方向完全不同 —— 一个是留下一批「有渠道但谁都
// 选不到」的僵尸路由(而且它一旦从倍率表消失就按 fail-open 的 1.0 静默计费),
// 另一个是一批令牌当场变孤儿。合成一个勾选框会让运营在只关心其中一件事的时候
// 顺手确认掉另一件。
func TestDeleteModelGroupNeedsForceWhenChannelsOrTokensRemain(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := newMainTestDB(t)
	nsConfig(t, true, "", "")
	syncHotAsync(t)
	useOptionSnapshot(t, `{"还在用的分组":1}`, `{}`, `[]`, `{}`)
	seedDeletableModelGroup(t, gdb, "还在用的分组")

	require.NoError(t, mainDB.Create(&model.Ability{
		Group: "还在用的分组", Model: "gpt-x", ChannelId: 1, Enabled: true,
	}).Error)
	require.NoError(t, mainDB.Create(&model.Channel{
		Id: 1, Name: "c1", Group: "还在用的分组", Status: common.ChannelStatusEnabled,
	}).Error)
	require.NoError(t, mainDB.Create(&model.User{
		Id: 11, Username: "u1", Password: "p", Group: "default", Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, mainDB.Create(&model.Token{
		Id: 21, UserId: 11, Name: "t1", Key: "k1",
		Group: "还在用的分组", Status: common.TokenStatusEnabled,
	}).Error)

	impact, err := ProbeModelGroupImpact(context.Background(), model.DB, gdb, "还在用的分组")
	require.NoError(t, err)
	assert.True(t, impact.HasRoute)
	assert.EqualValues(t, 1, impact.AbilityRows)
	assert.EqualValues(t, 1, impact.EnabledChannels)
	assert.EqualValues(t, 1, impact.Tokens)
	assert.EqualValues(t, 1, impact.TokenOwners)
	assert.True(t, impact.NeedsForceHasRoute)
	assert.True(t, impact.NeedsForceOrphanTokens)

	_, err = DeleteModelGroup(context.Background(), model.DB, gdb, "还在用的分组",
		DeleteModelGroupOptions{}, 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qy_modelgroup_has_route")

	_, err = DeleteModelGroup(context.Background(), model.DB, gdb, "还在用的分组",
		DeleteModelGroupOptions{ForceHasRoute: true}, 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qy_modelgroup_orphan_tokens",
		"勾了渠道那一道不应该把令牌那一道一起放过去")

	res, err := DeleteModelGroup(context.Background(), model.DB, gdb, "还在用的分组",
		DeleteModelGroupOptions{ForceHasRoute: true, ForceOrphanTokens: true}, 7)
	require.NoError(t, err)
	require.Nil(t, res.Partial)

	// 渠道与路由表**一行都不动**,这是本方案对 channels/abilities 的明确取舍。
	var abilities, channels int64
	require.NoError(t, mainDB.Model(&model.Ability{}).Count(&abilities).Error)
	require.NoError(t, mainDB.Model(&model.Channel{}).Count(&channels).Error)
	assert.EqualValues(t, 1, abilities,
		"abilities 是由 channels 物化出来的,单独删它下一次渠道保存就会重建回来")
	assert.EqualValues(t, 1, channels,
		"channels.group 是逗号串,摘掉一段是在改渠道配置,不能作为删名字的副作用")

	// 令牌同样不动:替用户改令牌分组等于替他改了计费分组。
	var token model.Token
	require.NoError(t, mainDB.First(&token, 21).Error)
	assert.Equal(t, "还在用的分组", token.Group,
		"孤儿令牌保持原样 —— 它们撞 403 的表现必须与今天一致,由运营/用户显式处理")
}

// TestDeleteModelGroupBlocksOnDeclaredBlockResidue 证明注册表的 block 处置真的会拦人。
//
// 它是各模块表达「删掉之后会让别的功能指向一个死名字,而正确的新值只有运营知道」
// 的唯一手段(典型:余额范围=仅限、且这是唯一解锁项的套餐)。没有这条用例,
// 某个模块把 block 写成 clean 之后不会有任何东西变红。
func TestDeleteModelGroupBlocksOnDeclaredBlockResidue(t *testing.T) {
	gdb := newTestDB(t)
	newMainTestDB(t)
	nsConfig(t, true, "", "")
	syncHotAsync(t)
	useOptionSnapshot(t, `{"有人挡着":1}`, `{}`, `[]`, `{}`)
	fake := seedDeletableModelGroup(t, gdb, "有人挡着")
	fake.rows, fake.disposition = 3, ResidueBlock

	_, err := DeleteModelGroup(context.Background(), model.DB, gdb, "有人挡着",
		DeleteModelGroupOptions{ForceHasRoute: true, ForceOrphanTokens: true}, 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qy_modelgroup_blocked")
	assert.Zero(t, fake.swept, "被拦下的删除不能已经清过残留")
}

// TestProbeModelGroupImpactLabelsProvenance 钉住项目方那句「预设有点混乱」的解法。
//
// 三种来源对应三种完全不同的处置,合成一个"是否有效"的布尔就把它们抹平了:
//
//	只有倍率没有路由 → 用户选得到、一发请求必然 503
//	只有路由没有倍率 → 走到它的请求正在按 fail-open 的 1.0 静默计费(资金泄漏)
//	两者都没有       → 一个纯粹的名字,最安全的删除对象
func TestProbeModelGroupImpactLabelsProvenance(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := newMainTestDB(t)
	nsConfig(t, true, "", "")
	syncHotAsync(t)
	useOptionSnapshot(t, `{"有倍率没路由":1}`, `{}`, `[]`, `{}`)
	require.NoError(t, gdb.Create(newModelGroup("有倍率没路由", 1, now())).Error)
	require.NoError(t, gdb.Create(newModelGroup("有路由没倍率", 1, now())).Error)
	require.NoError(t, gdb.Create(newModelGroup("空壳", 1, now())).Error)
	require.NoError(t, mainDB.Create(&model.Ability{
		Group: "有路由没倍率", Model: "m", ChannelId: 1, Enabled: true,
	}).Error)

	for name, want := range map[string][]string{
		"有倍率没路由": {SourceRatio},
		"有路由没倍率": {SourceRoute},
		"空壳":     {SourceRegistryOnly},
	} {
		impact, err := ProbeModelGroupImpact(context.Background(), model.DB, gdb, name)
		require.NoError(t, err)
		assert.Equal(t, want, impact.Sources, "模型分组 %q 的来源标注", name)
	}
}
