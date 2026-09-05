package groupmatrix

// scope_allow_auto_migration_test.go —— 启动时的「shadow → enforce」迁移里,
// allow_auto 的抬升必须只发生一次(在还有 shadow 行的那次启动),绝不能每次主节点
// 启动都把运营亲手关掉的 allow_auto 改回打开。
//
// allow_auto=false 有两种来路:shadow 行上它是从空白名单推出来的 artifact(该纠正);
// enforce 行上它是运营在编辑弹窗里显式关掉的决定(绝不能碰)。修复前那句
// `UPDATE ... SET allow_auto=true WHERE allow_auto=false` 不加任何一次性判据,
// 于是运营关掉的每一档在下一次重启后又被打开 —— 而 auto 会改写 relayInfo.UsingGroup,
// 影响这笔请求由钱包还是套餐余额出资,不是纯展示项。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gorm.io/gorm"
)

func loadScopeRow(t *testing.T, gdb *gorm.DB, userGroup string) Scope {
	t.Helper()
	var s Scope
	require.NoError(t, gdb.Where("user_group = ?", userGroup).Take(&s).Error)
	return s
}

// 运营在一个已生效(enforce)的分组上显式关掉 allow_auto,之后无论主节点重启多少次
// 都不能被改回打开。
func TestMigrateDoesNotRevertOperatorDisabledAllowAuto(t *testing.T) {
	gdb := newTestDB(t)
	seedScope(t, gdb, "vip", ModeEnforce, false, "gpt")
	require.False(t, loadScopeRow(t, gdb, "vip").AllowAuto, "前提:运营已把它关掉")

	// 连跑两次,模拟两次主节点启动。
	migrateShadowScopesToEnforce()
	migrateShadowScopesToEnforce()

	got := loadScopeRow(t, gdb, "vip")
	assert.False(t, got.AllowAuto,
		"运营在编辑弹窗里关掉的 allow_auto 不能被每次主节点启动改回打开")
	assert.Equal(t, ModeEnforce, got.Mode)
}

// shadow 行上的 allow_auto=false 是 artifact:首次迁移应把它抬成允许并迁成 enforce;
// 迁移之后运营再把它(现在是 enforce 行)关掉,后续启动不得再翻回。
func TestMigrateLiftsShadowArtifactAllowAutoExactlyOnce(t *testing.T) {
	gdb := newTestDB(t)
	seedScope(t, gdb, "gold", ModeShadow, false, "gpt")

	migrateShadowScopesToEnforce()
	first := loadScopeRow(t, gdb, "gold")
	assert.True(t, first.AllowAuto, "shadow artifact 首次迁移应把 allow_auto 抬成允许")
	assert.Equal(t, ModeEnforce, first.Mode, "shadow 应被迁成 enforce")

	// 迁移之后运营把它关掉 —— 现在它是 enforce 行,是运营的决定。
	require.NoError(t, gdb.Model(&Scope{}).Where("user_group = ?", "gold").
		Update("allow_auto", false).Error)
	migrateShadowScopesToEnforce()
	assert.False(t, loadScopeRow(t, gdb, "gold").AllowAuto,
		"迁移过一次之后,运营对 enforce 行的关闭决定不能再被启动流程翻回")
}
