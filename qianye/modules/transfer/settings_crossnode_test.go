package transfer

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settings_crossnode_test.go —— 门槛缓存的跨节点收敛。
//
// invalidateSettings() 只清**本进程**的缓存。多节点部署里,运营在 A 节点收紧
// 某一档的单笔上限之后,B 节点最长 settingsCacheSeconds(60 秒)仍按旧值放行,
// 而且零告警 —— 实测过一笔在库里明写单笔上限 1000 的情况下走掉 5000 的划转,
// 同一时刻 /transfer/limits 还在向用户回显旧值(前后端口径一致地都是错的)。
//
// 对照:分组**规则**那一半刻意不缓存,理由写在 loadGroupRules 上 ——
// 「多节点口径不一致比多一次查库严重得多」。同一道闸门的两半不能采用相反的
// 新鲜度策略,而缓存的恰好是决定钱能走多少的那一半。

// writeSettingRowRaw 直接写 qy_settings,**不**调用 invalidateSettings。
//
// 不能复用 putSetting:那个 helper 结尾会调 invalidateSettings(),等于让
// 「另一个节点」伸手清了本进程的缓存 —— 而那正是这条缺陷里唯一不会发生的事。
func writeSettingRowRaw(t *testing.T, gdb *gorm.DB, key, value string) {
	t.Helper()
	require.NoError(t, gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "updated_at"}),
	}).Create(&qymodel.Setting{
		Scope: settingScope, K: key, V: value, UpdatedAt: common.GetTimestamp(),
	}).Error)
}

// otherNodeSaves 模拟另一个节点保存了一次门槛:直接写库 + 自增版本号,
// 但**不**调用本进程的 invalidateSettings —— 那正是跨节点的真实形状。
func otherNodeSaves(t *testing.T, gdb *gorm.DB, mutate func()) {
	t.Helper()
	mutate()
	now := common.GetTimestamp()
	var ver SettingsVersion
	if err := gdb.Where("id = ?", 1).Take(&ver).Error; err != nil {
		require.NoError(t, gdb.Create(&SettingsVersion{Id: 1, Version: 1, UpdatedAt: now}).Error)
		return
	}
	require.NoError(t, gdb.Model(&SettingsVersion{}).Where("id = ?", 1).
		Updates(map[string]any{"version": ver.Version + 1, "updated_at": now}).Error)
}

// openVersionCheckWindow 让下一次 resolveSettings 立刻去比对版本号。
//
// 时间窗是 settingsVersionCheckSeconds(2 秒)。测试里绝不 sleep 等它 ——
// 直接把「上次比对时刻」清零,语义上等价于「2 秒过去了」。
func openVersionCheckWindow() {
	settingsMu.Lock()
	settingsVersionChecked = 0
	settingsMu.Unlock()
}

func TestSettingsSnapshotFollowsAnotherNodesSave(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, tierGlobal())
	invalidateSettings()

	ctx := context.Background()
	first, ok, err := resolveSettings(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 50_000_000, first.Transfer.MaxPerTxQuota)

	t.Run("另一个节点收紧了全站单笔上限:本节点必须跟上", func(t *testing.T) {
		otherNodeSaves(t, gdb, func() {
			writeSettingRowRaw(t, gdb, keyMaxPerTxQuota, "1000000")
		})
		openVersionCheckWindow()

		got, ok, err := resolveSettings(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		assert.EqualValues(t, 1_000_000, got.Transfer.MaxPerTxQuota,
			"版本号已经变了,本节点不能再按旧快照放行 60 秒")
	})

	t.Run("另一个节点收紧了某一档:分档也要跟上", func(t *testing.T) {
		otherNodeSaves(t, gdb, func() {
			require.NoError(t, gdb.Create(&GroupLimit{
				UserGroup: "vip", Enabled: true,
				MinQuota: i64(1), MaxPerTxQuota: i64(500),
				CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
			}).Error)
		})
		openVersionCheckWindow()

		snap, ok, err := resolveSettings(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		cfg, err := snap.transferFor("vip")
		require.NoError(t, err)
		assert.EqualValues(t, 500, cfg.MaxPerTxQuota)
	})

	// 反向对照:版本号没变时必须继续吃缓存。少了这一组,上面两条可能只是因为
	// 「每次都全量重载」而通过 —— 那等于把缓存整个删掉,而不是让它收敛。
	t.Run("版本号没变:即使库里被人绕过版本号改了,也仍然吃缓存", func(t *testing.T) {
		writeSettingRowRaw(t, gdb, keyMaxPerTxQuota, "2000000")
		openVersionCheckWindow()

		got, ok, err := resolveSettings(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		assert.EqualValues(t, 1_000_000, got.Transfer.MaxPerTxQuota,
			"版本号是唯一的收敛信号;它没动就不该重载")
	})

	// 本进程自己保存时,除了清本地缓存,还必须把版本号推上去 ——
	// 否则别的节点永远收不到信号,这条链只对「改动发生在别处」有效。
	t.Run("本节点保存会把版本号推上去", func(t *testing.T) {
		var before SettingsVersion
		require.NoError(t, gdb.Where("id = ?", 1).Take(&before).Error)

		invalidateSettings()

		var after SettingsVersion
		require.NoError(t, gdb.Where("id = ?", 1).Take(&after).Error)
		assert.Greater(t, after.Version, before.Version)
	})
}

// TestSettingsVersionIsReadBeforeTheRows 钉住两次读的**先后**。
//
// 版本号必须在拉门槛行**之前**读。反过来的话,两者之间的一次写入会让本节点
// 拿到旧数据、却配上新版本号 —— 此后 60 秒的版本比对全部命中"没变",
// 于是这次收紧在本节点上整整 60 秒不生效,而且这一次错过之后再也没有第二次
// 信号(号已经被记成新的了)。
//
// 用 GORM 的查询后置回调制造那一次交错:它在"门槛行已经读完、版本号还没读"
// 的缝隙里模拟另一个节点保存一次。不是随机并发 —— 交错点是确定的。
func TestSettingsVersionIsReadBeforeTheRows(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, tierGlobal())
	invalidateSettings()

	ctx := context.Background()
	_, ok, err := resolveSettings(ctx)
	require.NoError(t, err)
	require.True(t, ok)

	fired := false
	const cbName = "qy_transfer_test_interleave"
	require.NoError(t, gdb.Callback().Query().After("gorm:query").Register(cbName, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != (qymodel.Setting{}).TableName() {
			return
		}
		fired = true
		// 另一个节点就在这一刻保存了一次收紧。
		otherNodeSaves(t, gdb, func() {
			writeSettingRowRaw(t, gdb, keyMaxPerTxQuota, "1000000")
		})
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(cbName) })

	// 先让它重载一次:这一次读到的门槛行是交错发生**之前**的旧值。
	invalidateSettings()
	fired = false
	_, ok, err = resolveSettings(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, fired, "交错回调没被触发,这条用例没有测到任何东西")

	// 关键断言:下一次版本比对必须发现号变了并重载。
	// 版本号若是在拉行之后读的,这里会读到"号没变",旧值再吃 60 秒。
	openVersionCheckWindow()
	got, ok, err := resolveSettings(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.EqualValues(t, 1_000_000, got.Transfer.MaxPerTxQuota,
		"版本号必须先于门槛行读出;否则这次收紧会被记成'已经是最新的'并静默丢失")
}
