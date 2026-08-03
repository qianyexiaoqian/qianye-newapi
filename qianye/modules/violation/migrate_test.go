package violation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateRuleModeForcesEveryLegacyRuleToShadow 是本轮唯一的资损防线回归。
//
// 现网 YAML 里 violation.shadow_mode = true,而叠加语义取更保守者胜 —— 也就是说
// **线上每一条规则实际上都在影子跑**,不管它自己的 dry_run 是 true 还是 false。
// 删掉全局层之后,如果按 dry_run 逐条翻译(dry_run=false → enforce),那批从未
// 真正执行过的规则会在部署完成的那一秒同时开始扣费与封号。
//
// 所以迁移策略是"一律 shadow,由运营逐条确认后再转真实"。这条测试就是那句话的
// 可执行版本:**任何** mode 为空的行,迁移后必须是 shadow。
func TestMigrateRuleModeForcesEveryLegacyRuleToShadow(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	gdb := newBuiltinRuleDB(t)
	ctx := context.Background()

	base := func(name, mode string) *Rule {
		return &Rule{
			Name: name, Mode: mode, Enabled: true, Priority: 1,
			Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "x",
			Action: ActionRecord, FeeMode: FeeNone, GroupScopeMode: GroupScopeInclude,
			CreatedAt: 1, UpdatedAt: 1,
		}
	}
	// 三种历史形态:没有 mode 的旧行、已经迁移过的 shadow、
	// 以及运营已经确认过并切成真实执行的那一条。
	legacy := base("旧行-空 mode", ModeShadow)
	require.NoError(t, gdb.Create(legacy).Error)
	require.NoError(t, gdb.Create(base("已迁移", ModeShadow)).Error)
	require.NoError(t, gdb.Create(base("运营已确认", ModeEnforce)).Error)
	// 软删的行也要迁:管理端复核申诉时会读到它,空 mode 在界面上是渲染不出来的
	// 第三种状态。
	deleted := base("软删的旧行", ModeShadow)
	require.NoError(t, gdb.Create(deleted).Error)
	require.NoError(t, gdb.Delete(deleted).Error)

	// 空 mode 必须用裸 SQL 造:Rule.Mode 带 gorm default tag,GORM 会把零值字段
	// 从 INSERT 里剔掉、让数据库填默认值 —— 也就是说**用 Create 根本造不出一行
	// 空 mode**。这个事实本身就是第一层兜底(AutoMigrate 的 ADD COLUMN DEFAULT
	// 会把现网历史行全部回填成 shadow);这条 UPDATE 造的是绕过 AutoMigrate 的
	// 那些部署:关掉 auto_migrate、DBA 手工建表、滚动升级期间旧节点插的行。
	require.NoError(t, gdb.Exec(
		"UPDATE qy_violation_rule SET mode = '' WHERE id IN (?, ?)", legacy.Id, deleted.Id).Error)

	n, err := migrateRuleMode(ctx, gdb)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "两条空 mode 的行(含软删那条)必须被迁")

	var rows []Rule
	require.NoError(t, gdb.Unscoped().Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 4)
	assert.Equal(t, ModeShadow, rows[0].Mode, "空 mode 必须变成影子,而不是按 dry_run 翻译成真实执行")
	assert.Equal(t, ModeShadow, rows[1].Mode)
	assert.Equal(t, ModeEnforce, rows[2].Mode,
		"迁移不得动运营已经确认过的规则 —— 那会把一条正在生效的风控悄悄关掉")
	assert.Equal(t, ModeShadow, rows[3].Mode, "软删的行也要迁")

	t.Run("重复执行是幂等的", func(t *testing.T) {
		again, err := migrateRuleMode(ctx, gdb)
		require.NoError(t, err)
		assert.EqualValues(t, 0, again, "第二次运行不该再改任何行")

		var live Rule
		require.NoError(t, gdb.Where("name = ?", "运营已确认").Take(&live).Error)
		assert.Equal(t, ModeEnforce, live.Mode)
	})
}

// TestUnmigratedRuleStillRunsAsShadow 是迁移之外的第二层兜底。
//
// 迁移可能没跑到(关掉 auto_migrate、DBA 手工建表、滚动升级中的旧节点刚插了一行)。
// 运行期必须自己兜住,而且方向只能是"不扣费不封号"。两层都指向同一个方向,
// 漏掉任何一层都不会造成误扣费 —— 这条测试锁的就是"第二层还在"。
func TestUnmigratedRuleStillRunsAsShadow(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	isolateBreaker(t)

	on, reason := effectiveShadow(&compiledRule{R: Rule{Id: 1, Mode: ""}})
	assert.True(t, on, "迁移没跑到的行必须在运行期按影子处理")
	assert.Equal(t, ShadowReasonRuleMode, reason)
}
