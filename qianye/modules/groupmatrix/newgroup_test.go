package groupmatrix

// newgroup_test.go —— 锁住「新分组默认全遮断」的边界。
//
// 这个默认唯一的严重失败方式是**遮断错了对象**:遮断一个既存分组会让那一档的
// 用户当场一个模型分组都选不了。下面每一个用例对应一条真实存在的触发路径,
// 没有一条是"跑一遍看看不报错"。

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useUserCounts 把「某个用户分组有多少在册用户」换成一张固定表。
//
// 生产实现查主库,而单测里 model.DB 恒为 nil —— 不给这个接缝的话每一个用例
// 都会停在"查不到人数 → 整轮跳过"上,全绿而实现体可以整段写反。
func useUserCounts(t *testing.T, counts map[string]int64) {
	t.Helper()
	prev := userCountOf
	userCountOf = func(userGroup string) (int64, bool) { return counts[userGroup], true }
	t.Cleanup(func() { userCountOf = prev })
}

// useUserCountsUnavailable 模拟主库不可用。
func useUserCountsUnavailable(t *testing.T) {
	t.Helper()
	prev := userCountOf
	userCountOf = func(string) (int64, bool) { return 0, false }
	t.Cleanup(func() { userCountOf = prev })
}

func ratioTable(names ...string) map[string]float64 {
	out := make(map[string]float64, len(names))
	for _, n := range names {
		out[n] = 1
	}
	return out
}

func scopeNames(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	var rows []Scope
	require.NoError(t, gdb.Order("user_group asc").Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.UserGroup)
	}
	return out
}

func seenRow(t *testing.T, gdb *gorm.DB, name string) Seen {
	t.Helper()
	var row Seen
	require.NoError(t, gdb.Where("user_group = ?", name).Take(&row).Error)
	return row
}

// 首轮对账必须把当时存在的全部分组登记成基线并**一个都不遮断**。
//
// 这是项目方那句「既存的 7 个用户分组保持原样」在代码里的位置。搞错这一条的
// 表现不是某个数字不对,而是全站用户在下一秒同时失去全部可选模型分组。
func TestNewGroupScanFirstRunBaselinesWithoutMasking(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil,
		ratioTable("default", "vip", "svip", "浅夜の自己人"))

	require.NoError(t, reconcileNewGroups(context.Background()))

	assert.Empty(t, scopeNames(t, gdb),
		"首轮基线绝不能建 scope 行 —— 既存分组一律由运营手动接管")
	var seen []Seen
	require.NoError(t, gdb.Find(&seen).Error)
	assert.Len(t, seen, 4)
	for _, row := range seen {
		assert.True(t, row.Baseline, "首轮登记的每一行都必须是基线")
		assert.False(t, row.AutoMasked)
	}
	assert.Zero(t, autoMaskedTotal.Load())
}

// 基线之后新出现的分组必须被自动接管成 enforce + 零 grant,而既存分组一个都不许动。
func TestNewGroupScanMasksOnlyFreshGroups(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{"default": 120, "vip": 8})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "vip"))
	require.NoError(t, reconcileNewGroups(context.Background()))
	require.Empty(t, scopeNames(t, gdb))

	// 运营在上游「系统设置-分组倍率」表单里加了一个 key。
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "vip", "内测组"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	assert.Equal(t, []string{"内测组"}, scopeNames(t, gdb),
		"只有新出现的分组该被接管,既存分组必须原样不动")

	var scope Scope
	require.NoError(t, gdb.Where("user_group = ?", "内测组").Take(&scope).Error)
	assert.Equal(t, ModeEnforce, scope.Mode,
		"shadow 只记录不阻断,遮断必须是 enforce 才真的生效")
	assert.False(t, scope.AllowAuto,
		"allow_auto 打开等于把 auto 伪分组注回可选 map,那不叫全遮断")

	var grantCount int64
	require.NoError(t, gdb.Model(&Grant{}).
		Where("user_group = ?", "内测组").Count(&grantCount).Error)
	assert.Zero(t, grantCount, "全遮断 = 零条可选模型分组")

	assert.True(t, seenRow(t, gdb, "内测组").AutoMasked)
	assert.False(t, seenRow(t, gdb, "default").AutoMasked)
	assert.Equal(t, int64(1), autoMaskedTotal.Load())

	// 快照必须已经跟上:遮断落了库但快照没刷新时,读侧仍按上游白名单放行,
	// 而管理端显示这一行已经是 enforce —— 又是一次「界面 A、线上 B」。
	snap, loaded := SnapshotView()
	require.True(t, loaded)
	require.Contains(t, snap.Scopes, "内测组")
	assert.Empty(t, snap.Grants["内测组"])
}

// 遮断只发生一次:运营撤销接管之后,对账不许把它改回去。
//
// 这是整个方案的可回退性所在。判据必须是登记簿而不是 scope 行 ——
// 用 scope 行做判据的实现在这个用例上必红。
func TestNewGroupScanNeverReMasksAfterOperatorUnmanages(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "内测组"))
	require.NoError(t, reconcileNewGroups(context.Background()))
	require.Equal(t, []string{"内测组"}, scopeNames(t, gdb))

	// 运营在矩阵页撤销接管(= 删 scope 行,grants 刻意保留)。
	require.NoError(t, gdb.Where("user_group = ?", "内测组").Delete(&Scope{}).Error)

	require.NoError(t, reconcileNewGroups(context.Background()))
	assert.Empty(t, scopeNames(t, gdb),
		"撤销接管之后再被自动遮断一次,等于回退能力不存在")

	// 幂等的另一半:什么都没变的一轮不许有任何写入。
	require.NoError(t, reconcileNewGroups(context.Background()))
	assert.Empty(t, scopeNames(t, gdb))
	assert.Equal(t, int64(1), autoMaskedTotal.Load(), "重复对账不许重复计数")
}

// 已经被手动接管的分组,自动遮断不许介入 —— 哪怕它还没进登记簿。
//
// 触发路径:运营在对账首次跑起来之前就手动接管并配好了清单。
// 用一条零 grant 的 enforce 行盖上去 = 一次静默的全量撤销。
func TestNewGroupScanKeepsManuallyManagedScope(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{"gpt4": "高级"}, nil, ratioTable("default", "gpt4"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{"gpt4": "高级"}, nil,
		ratioTable("default", "内测组", "gpt4"))
	require.NoError(t, gdb.Create(newScope("内测组", ModeShadow, true, "运营手配", 7, 100)).Error)
	require.NoError(t, gdb.Create(&Grant{
		UserGroup: "内测组", ModelGroup: "gpt4", CreatedAt: 100, UpdatedAt: 100,
	}).Error)

	require.NoError(t, reconcileNewGroups(context.Background()))

	var scope Scope
	require.NoError(t, gdb.Where("user_group = ?", "内测组").Take(&scope).Error)
	assert.Equal(t, ModeShadow, scope.Mode, "手动接管的 mode 不许被自动改写")
	assert.Equal(t, "运营手配", scope.Note)
	assert.True(t, scope.AllowAuto)

	var grantCount int64
	require.NoError(t, gdb.Model(&Grant{}).
		Where("user_group = ?", "内测组").Count(&grantCount).Error)
	assert.Equal(t, int64(1), grantCount, "已配好的清单绝不能被自动清空")
	assert.False(t, seenRow(t, gdb, "内测组").AutoMasked)
	assert.Zero(t, autoMaskedTotal.Load())
}

// 已经有用户在用的分组不是新分组 —— 那是既存分组被补登进倍率表。
//
// 本站的孤儿分组正是这个形状(users.group 有它,GroupRatio 没有)。
// 运营给它补上倍率的本意是"让它正常工作",遮断会当场打断一批在线用户。
func TestNewGroupScanSkipsGroupThatAlreadyHasUsers(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{"老用户组": 43})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "老用户组"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	assert.Empty(t, scopeNames(t, gdb),
		"给一个已有 43 名用户的分组补倍率,不该让这 43 个人当场失去全部模型分组")
	row := seenRow(t, gdb, "老用户组")
	assert.False(t, row.AutoMasked)
	assert.Contains(t, row.Reason, "43", "「当初为什么没遮断它」必须写在登记簿里")
}

// 主库查不到人数时整轮跳过,而且**连登记簿都不写** —— 下一轮还要重判。
//
// 写了登记簿等于用一次"不知道"永久放弃对这个分组的处置。
func TestNewGroupScanDefersWhenUserCountUnavailable(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "内测组"))
	useUserCountsUnavailable(t)
	require.NoError(t, reconcileNewGroups(context.Background()))

	assert.Empty(t, scopeNames(t, gdb))
	var probe Seen
	assert.ErrorIs(t, gdb.Where("user_group = ?", "内测组").Take(&probe).Error,
		gorm.ErrRecordNotFound,
		"人数查不到时若把它登记下来,主库恢复之后这个分组就永远不会被处置了")

	// 主库恢复,同一个分组这一次必须被处置。
	useUserCounts(t, map[string]int64{})
	require.NoError(t, reconcileNewGroups(context.Background()))
	assert.Equal(t, []string{"内测组"}, scopeNames(t, gdb))
}

// 一轮冒出一批新分组 = 数据异常(倍率表被整体替换 / 登记簿被截断),
// 必须全部只登记不遮断,而不是一次遮断一片。
func TestNewGroupScanRefusesAvalanche(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	names := []string{"default", "a", "b", "c", "d", "e"}
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable(names...))
	require.NoError(t, reconcileNewGroups(context.Background()))

	assert.Empty(t, scopeNames(t, gdb))
	assert.Zero(t, autoMaskedTotal.Load())
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		assert.False(t, seenRow(t, gdb, n).AutoMasked)
	}

	// 已登记之后不再重复告警,也不会因为下一轮差集变小而"补遮断"。
	require.NoError(t, reconcileNewGroups(context.Background()))
	assert.Empty(t, scopeNames(t, gdb))
}

// 倍率表为空时整轮跳过。把空集当基线登记下来,下一轮全站真实分组都会变成"新分组"。
func TestNewGroupScanSkipsEmptyRatioTable(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, map[string]float64{})

	require.NoError(t, reconcileNewGroups(context.Background()))

	var seen []Seen
	require.NoError(t, gdb.Find(&seen).Error)
	assert.Empty(t, seen, "空倍率表不足以支撑一次基线登记")
	assert.Zero(t, lastScanAt.Load())
}

// 开关关掉时**仍然登记**,只是不遮断。
//
// 不登记的话,关闭期间新建的分组会攒成一批"从未见过"的名字;
// 运营某天把开关打开,它们会在同一轮里被一起遮断 —— 而那批分组早已有人在用。
// 开关的语义必须是"从现在起",不是"把这段时间补上"。
func TestNewGroupScanRecordsButDoesNotMaskWhenSwitchOff(t *testing.T) {
	gdb := newTestDB(t)
	useConfigWithNewGroupDeny(t, true, false)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "内测组"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	assert.Empty(t, scopeNames(t, gdb))
	assert.False(t, seenRow(t, gdb, "内测组").AutoMasked)

	// 把开关打开:刚才那个分组已经登记过,不许被"补遮断"。
	useConfigWithNewGroupDeny(t, true, true)
	require.NoError(t, reconcileNewGroups(context.Background()))
	assert.Empty(t, scopeNames(t, gdb),
		"开关打开只对此后新出现的分组生效,不能追溯关闭期间建的分组")
}

// 模块整体关掉(L1 kill switch)时,连登记簿都不维护。
func TestNewGroupScanNoopWhenModuleDisabled(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, false)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "内测组"))

	require.NoError(t, reconcileNewGroups(context.Background()))

	var seen []Seen
	require.NoError(t, gdb.Find(&seen).Error)
	assert.Empty(t, seen)
	assert.Empty(t, scopeNames(t, gdb))
}

// 方案 3 的已知代价,写成断言而不是注释:**新增一个"模型分组"同样会被遮断。**
//
// options.GroupRatio 的键集合同时是用户分组清单与模型分组清单,扩展侧没有任何
// 信息能把"我加的是一条渠道分组"和"我加的是一档用户"分开。遮断是安全的那一侧
// (新键的在册人数为 0),但它会在矩阵页多出一行「新分组·待配置」——
// 这件事必须被测试钉住,否则将来有人会把它当成缺陷"修"掉,
// 而修掉的方式只能是猜,猜错的方向是漏掉真正的新用户分组。
func TestNewGroupScanAlsoMasksNewlyAddedModelGroupName(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	// 运营的本意只是新增一个渠道分组 claude-max,并给它配一个倍率。
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "claude-max"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	assert.Equal(t, []string{"claude-max"}, scopeNames(t, gdb),
		"同一个命名空间里,「新增模型分组」与「新增用户分组」在数据上无法区分 —— "+
			"按在册 0 人的那一侧处置(遮断)是安全方向,但它必须是被写下来的决定")
}

// auto 是伪分组,永远不是一个用户分组:它既不进登记簿,也不会被接管。
func TestNewGroupScanIgnoresAutoPseudoGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", autoGroup))
	require.NoError(t, reconcileNewGroups(context.Background()))

	assert.Empty(t, scopeNames(t, gdb))
	var probe Seen
	assert.ErrorIs(t, gdb.Where("user_group = ?", autoGroup).Take(&probe).Error,
		gorm.ErrRecordNotFound)
}

// 被自动遮断的行必须在矩阵回显里带上来历与「待配置」标记。
//
// 不下发这两个字段,运营看到的就是一个莫名其妙已经 enforce、清单还空着的分组,
// 而他确信自己没做过这件事 —— 那正是这个默认最容易被当成故障的时刻。
func TestMatrixViewSurfacesAutoMaskedRows(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{"gpt4": "高级"}, nil,
		ratioTable("default", "gpt4"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{"gpt4": "高级"}, nil,
		ratioTable("default", "gpt4", "内测组"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	require.True(t, view.NewGroupPolicy.Enabled)
	assert.Equal(t, 1, view.NewGroupPolicy.PendingSetup)

	var row *userGroupRow
	for i := range view.UserGroups {
		if view.UserGroups[i].Name == "内测组" {
			row = &view.UserGroups[i]
		}
	}
	require.NotNil(t, row)
	assert.True(t, row.AutoMasked)
	assert.True(t, row.PendingSetup)
	assert.Equal(t, ModeEnforce, row.Mode)
	assert.NotZero(t, row.AutoMaskedAt)

	// 配上一个模型分组之后,「待配置」必须消失,而「来历」必须留着:
	// 长期挂着的提示等于没有提示,而抹掉来历会让复盘查不到是谁把它变 enforce 的。
	require.NoError(t, gdb.Create(&Grant{
		UserGroup: "内测组", ModelGroup: "gpt4",
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	view, err = buildMatrixView(gdb)
	require.NoError(t, err)
	assert.Zero(t, view.NewGroupPolicy.PendingSetup)
	for _, r := range view.UserGroups {
		if r.Name != "内测组" {
			continue
		}
		assert.True(t, r.AutoMasked, "来历是历史事实,配好之后也不该被抹掉")
		assert.False(t, r.PendingSetup)
	}
}

// 「开关开着、发现了、却刻意没遮断」必须在矩阵回显里说出来。
//
// 这一档是这套默认里唯一"运营以为发生了、实际没发生"的组合,而它原本在界面上
// 完全没有形状:页面顶部常驻写着「新分组默认全遮断:已开启」,这一行看起来与
// 任何一个未接管的分组一模一样,登记簿又永不重判 —— 于是它此后**永远**不会被
// 补遮断,唯一的痕迹只有扩展库里的一列 reason,而那一列运营查不到。
//
// 触发它的是最自然的运营顺序:在分组倍率表单里加一个分组,然后立刻把几个人
// 划进去;60 秒后对账跑到时人数已经 > 0。
func TestMatrixViewSurfacesPolicySkippedRows(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{"gpt4": "高级"}, nil,
		ratioTable("default", "gpt4"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	// 新分组出现时已经有 3 个人在用 —— 判定为「既存分组补登倍率」,不遮断。
	useUserCounts(t, map[string]int64{"内测组": 3})
	useUpstreamGroups(t, map[string]string{"gpt4": "高级"}, nil,
		ratioTable("default", "gpt4", "内测组"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	require.Empty(t, scopeNames(t, gdb), "有人在用的分组不该被遮断")
	assert.True(t, seenRow(t, gdb, "内测组").Declined,
		"登记簿必须记下「开关开着、而我刻意没遮断它」")

	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	require.True(t, view.NewGroupPolicy.Enabled)
	assert.Equal(t, 1, view.NewGroupPolicy.PolicySkipped)
	assert.Zero(t, view.NewGroupPolicy.PendingSetup,
		"它没有被遮断,所以不是「待配置」——两种落差方向相反,不能混成一个计数")

	var row *userGroupRow
	for i := range view.UserGroups {
		if view.UserGroups[i].Name == "内测组" {
			row = &view.UserGroups[i]
		}
	}
	require.NotNil(t, row)
	assert.True(t, row.PolicySkipped)
	assert.Contains(t, row.PolicySkippedReason, "3",
		"只给一个布尔的话,运营下一步该做什么完全没有线索")

	// 运营自己接管这一行之后提示必须消失:那时的状态是他配的,不再有预期落差。
	require.NoError(t, gdb.Create(newScope("内测组", ModeEnforce, false, "手动接管",
		1, common.GetTimestamp())).Error)
	view, err = buildMatrixView(gdb)
	require.NoError(t, err)
	assert.Zero(t, view.NewGroupPolicy.PolicySkipped)
}

// 雪崩闸门拦下的那一批同样是「开关开着却没遮断」,同样要上界面。
//
// 它们比人数那一档更危险:登记簿永不重判,所以这 5 个分组从此按上游全局白名单
// 放行,而运营看到的仍然是「已开启」。
func TestAvalancheBatchIsReportedAsPolicySkipped(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{}, nil,
		ratioTable("default", "a", "b", "c", "d", "e"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	require.Empty(t, scopeNames(t, gdb))
	view, err := buildMatrixView(gdb)
	require.NoError(t, err)
	assert.Equal(t, 5, view.NewGroupPolicy.PolicySkipped)
}

// 只差大小写的分组名不能把对账变成一个每周期重来一次的循环。
//
// 扩展库固定是 MySQL,qy_group_seen 的 varchar 主键走默认的 utf8mb4_general_ci:
// 在库看来 "VIP" 与 "vip" 是同一行。Go 侧若按精确匹配算差集,VIP 会每一轮都被
// 判成 fresh、回查却命中 vip 那一行,于是它永远写不进登记簿;走到遮断分支时
// 则是 Create 撞主键、事务回滚,变成每周期一条 SysError 且永远不自愈。
//
// 折叠之后的正确行为:VIP 不被当成新分组(因此不遮断),而且**反复对账是稳定的** ——
// 不新增 scope 行、不新增登记簿行、不报错。
func TestCaseVariantOfSeenGroupDoesNotLoopForever(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	useUserCounts(t, map[string]int64{})
	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "vip"))
	require.NoError(t, reconcileNewGroups(context.Background()))

	useUpstreamGroups(t, map[string]string{}, nil, ratioTable("default", "vip", "VIP"))
	for i := 0; i < 3; i++ {
		require.NoError(t, reconcileNewGroups(context.Background()),
			"第 %d 轮对账不该报错 —— 报错的那一版每 60 秒重来一次且永远不自愈", i+1)
	}

	assert.Empty(t, scopeNames(t, gdb),
		"只差大小写的名字在库看来就是已登记的那一行,不该被当成新分组遮断")
	var rows []Seen
	require.NoError(t, gdb.Find(&rows).Error)
	assert.Len(t, rows, 2, "登记簿应当只有首轮基线的 default 与 vip,反复对账不新增行")
}
