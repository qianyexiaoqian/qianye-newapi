package violation

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// rule_enabled_test.go —— 规则列表行内快速启停的回归。
//
// 这条路径守的是四件事,每一件都对应一种"接口 200、界面正常、防护其实没了"的形态:
//
//  1. **只写 enabled 一列。** 复用整体更新接口的话,前端那份十几秒前的规则拷贝会被
//     整个写回库,把这期间别人对 pattern / mode 的改动静默回滚掉。
//  2. **启用编译不过的规则必须被拒。** reloadCtx 对编译失败的规则是静默跳过的,
//     "启用成功、显示已启用、线上永不命中"没有任何可见信号。
//  3. **停用永远允许。** 把一条正在误伤的坏规则关掉是紧急出口,不能被校验挡住。
//  4. **内置规则被停用之后,重新导入不会把它重新打开。** 否则运营为了止损关掉一条
//     误杀规则,下一次点"导入内置规则包"就把它原样放回线上,而界面上没有任何提示。

// seedRule 直接落一条规则行,**绕过 ValidateRule**。
//
// 绕过是刻意的:测试里要造出"库里存着一条编译不过的规则"这种状态,而写接口本来
// 就拦得住它 —— 能产生这种行的是旧版本、滚动升级、DBA 手工插入,不是本接口。
func seedRule(t *testing.T, gdb *gorm.DB, row *Rule) *Rule {
	t.Helper()
	require.NoError(t, gdb.Create(row).Error)
	return row
}

func goodRule(enabled bool) *Rule {
	return &Rule{
		Name: "关键词规则", Enabled: enabled, Mode: ModeShadow,
		Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "越狱\n破限",
		Action: ActionRecord, FeeMode: FeeNone, GroupScopeMode: GroupScopeInclude,
		Priority: 100, CountWeight: 1, Severity: 1,
		CreatedAt: 1000, UpdatedAt: 1000, CreatedBy: 7, UpdatedBy: 7,
	}
}

func loadRule(t *testing.T, gdb *gorm.DB, id int64) Rule {
	t.Helper()
	var row Rule
	require.NoError(t, gdb.Where("id = ?", id).Take(&row).Error)
	return row
}

// TestSetRuleEnabledOnlyTouchesTheSwitch 是本文件最重要的一条。
//
// 它复现的是"用整体更新接口做启停"的实际后果:管理员 A 打开列表(拿到一份快照),
// 管理员 B 在这期间改窄了 pattern 并把 mode 从 enforce 调回 shadow,A 随后点了一下
// 行内的停用开关。只写一列时 B 的改动完好;整体写回时 B 的改动被无声抹掉。
func TestSetRuleEnabledOnlyTouchesTheSwitch(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	row := seedRule(t, gdb, goodRule(true))
	row.Mode = ModeEnforce
	require.NoError(t, gdb.Save(row).Error)

	// 管理员 B 的改动:改窄词表 + 把真实模式调回影子。
	require.NoError(t, gdb.Model(&Rule{}).Where("id = ?", row.Id).Updates(map[string]any{
		"pattern": "越狱", "mode": ModeShadow, "updated_by": 99,
	}).Error)

	got, changed, err := setRuleEnabled(gdb, row.Id, false, 7, 2000)
	require.NoError(t, err)
	require.True(t, changed)
	assert.False(t, got.Enabled)

	after := loadRule(t, gdb, row.Id)
	assert.False(t, after.Enabled)
	assert.Equal(t, "越狱", after.Pattern, "启停把别人改窄的词表写回去了 —— 一次没有人按下过的回滚")
	assert.Equal(t, ModeShadow, after.Mode, "启停把 mode 从影子改回了真实 —— 那是真的开始扣钱")
	// updated_at / updated_by 必须跟着走:它们是"谁在什么时候关的"在列表上的
	// 唯一可见投影,不写的话界面显示的更新时间会停在上一次编辑。
	assert.Equal(t, int64(2000), after.UpdatedAt)
	assert.Equal(t, 7, after.UpdatedBy)
}

// 返回值必须是库里的最新行,因为调用方拿它去写审计的 AfterSnap。
//
// CAS 只锁 enabled 一列,别人在这期间改掉 pattern / mode 时这次更新照样成功。
// 若返回入口处那份快照,审计里就会留下一份库里已经不存在的规则:事后追
// "关掉它的那一刻它是什么模式"会得到相反的答案 —— 而启停是一条完全无症状的
// 路径,审计是它唯一的证据。
func TestSetRuleEnabledReturnsTheRowAsItActuallyIs(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	row := seedRule(t, gdb, goodRule(true))
	row.Mode = ModeEnforce
	row.Pattern = "越狱\n破限"
	require.NoError(t, gdb.Save(row).Error)

	// 管理员 B 在 A 读到快照之后改窄词表并调回影子模式。
	require.NoError(t, gdb.Model(&Rule{}).Where("id = ?", row.Id).Updates(map[string]any{
		"pattern": "越狱", "mode": ModeShadow,
	}).Error)

	got, changed, err := setRuleEnabled(gdb, row.Id, false, 7, 2000)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, ModeShadow, got.Mode, "审计快照写的是一份库里已经不存在的模式")
	assert.Equal(t, "越狱", got.Pattern, "审计快照写的是一份库里已经不存在的词表")
	assert.False(t, got.Enabled)
}

func TestSetRuleEnabledIsIdempotent(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	row := seedRule(t, gdb, goodRule(true))

	_, changed, err := setRuleEnabled(gdb, row.Id, false, 7, 2000)
	require.NoError(t, err)
	require.True(t, changed)

	// 第二次点同一个方向:什么都没发生。changed=false 是调用方决定"要不要写审计、
	// 要不要 bump 版本号"的唯一依据 —— 每次重复点击都写一条"改过"的审计,
	// 会让事后翻日志的人数不清到底改了几次。
	got, changed, err := setRuleEnabled(gdb, row.Id, false, 8, 3000)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.False(t, got.Enabled)

	after := loadRule(t, gdb, row.Id)
	assert.Equal(t, int64(2000), after.UpdatedAt, "无变化的调用不该刷新更新时间")
	assert.Equal(t, 7, after.UpdatedBy)
}

func TestSetRuleEnabledRefusesToEnableAnUncompilableRule(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	bad := goodRule(false)
	bad.MatchType = MatchRegex
	bad.Pattern = "(未闭合" // regexp.Compile 会失败
	row := seedRule(t, gdb, bad)

	_, changed, err := setRuleEnabled(gdb, row.Id, true, 7, 2000)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errRuleWontCompile),
		"必须是可辨认的编译错误,接口才能翻成 400 而不是 500")
	assert.False(t, changed)
	// 库里必须仍是停用。留一个"报错了但其实打开了"的状态,比直接失败更坏:
	// 界面显示失败,线上却多了一条被 reloadCtx 静默跳过的规则。
	assert.False(t, loadRule(t, gdb, row.Id).Enabled)
}

func TestSetRuleEnabledAlwaysAllowsDisabling(t *testing.T) {
	// 停用是误伤时的紧急出口。一条编译不过的规则在库里是启用态(旧版本写进去的、
	// DBA 手工插的),如果停用也要过编译闸,管理员就只剩"删掉它"这一条路 ——
	// 而删除是软删且不可撤销地丢掉列表上下文。
	gdb := newBuiltinRuleDB(t)
	bad := goodRule(true)
	bad.MatchType = MatchRegex
	bad.Pattern = "(未闭合"
	row := seedRule(t, gdb, bad)

	got, changed, err := setRuleEnabled(gdb, row.Id, false, 7, 2000)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, got.Enabled)
	assert.False(t, loadRule(t, gdb, row.Id).Enabled)
}

func TestSetRuleEnabledReportsMissingRule(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	_, _, err := setRuleEnabled(gdb, 4040, false, 7, 2000)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound),
		"接口靠这个错误回 404;认不出来就会变成 500,把「id 打错了」报成服务端故障")
}

// TestDisabledBuiltinRuleStaysDisabledAcrossReimport 回答本轮的第 4 个问题。
//
// 场景:运营导入了内置防护包,某条规则误伤严重,他在列表里把它**停用**止损。
// 一周后他为了拿新版模式串再点一次"导入内置规则包"(勾不勾"同时升级"都算)。
//
// 如果那次导入把 enabled 打回 true,那就是一次没有人按下过的重新上线,而且是
// 静默的:导入结果只会说"已是最新 / 已升级",不会说"顺便帮你重新打开了"。
// 现在的实现里,已存在的行只被 UPDATE pattern / case_sensitive 与四列元数据,
// enabled 一个字节都不动 —— 这条测试就是把那个边界钉住。
func TestDisabledBuiltinRuleStaysDisabledAcrossReimport(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	current := builtinCatalog[0]
	require.Equal(t, importCreated, importOne(gdb, current, nil, false, 1000, 7).Action)

	rows, err := loadBuiltinRows(gdb)
	require.NoError(t, err)
	imported := rows[current.Key]
	require.NotNil(t, imported)
	require.True(t, imported.Enabled, "内置规则导入出来默认是启用的影子规则")

	// 运营止损:停用它。
	_, changed, err := setRuleEnabled(gdb, imported.Id, false, 9, 2000)
	require.NoError(t, err)
	require.True(t, changed)

	// 造一版"目录里出了新版本"。不依赖真实目录里恰好存在 v2 的规则:
	// 那样这条测试会随着目录内容时灵时不灵,而它要守的边界与目录内容无关。
	newer := current
	newer.Version = current.Version + 1
	newer.Pattern = current.Pattern + `|(?:zzz_new_branch)`

	// 不勾"同时升级":跳过。勾了:真的替换模式串。两条路都不许碰 enabled。
	skipped := importOne(gdb, newer, imported, false, 3000, 5)
	require.Equal(t, importSkipped, skipped.Action, skipped.Reason)
	assert.False(t, loadRule(t, gdb, imported.Id).Enabled,
		"不升级的导入把一条被运营停用的内置规则重新打开了")

	rows, err = loadBuiltinRows(gdb)
	require.NoError(t, err)
	pristine := rows[current.Key]
	require.NotNil(t, pristine)

	upgraded := importOne(gdb, newer, pristine, true, 4000, 5)
	require.Equal(t, importUpgraded, upgraded.Action, upgraded.Reason)

	after := loadRule(t, gdb, imported.Id)
	// 先证明升级分支真的跑到了 —— 否则下面那条 Enabled 断言只是在断言"什么都没发生"。
	require.Equal(t, newer.Pattern, after.Pattern, "升级分支没有真的替换模式串")
	assert.False(t, after.Enabled,
		"「同时升级」把一条被运营停用的内置规则重新打开了 —— 那是一次没有人按下过的"+
			"重新上线,而导入结果里只会说「已升级」,不会说「顺便帮你重新打开了」")
}
