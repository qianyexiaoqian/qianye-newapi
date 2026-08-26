package transfer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// 分组改名/删除之后,划转的门槛分档缓存必须在**事务提交之后**失效。
//
// 这一处此前完全没有:sweepResidue 改写/删除 qy_transfer_group_limits
// (日额度、单笔上限、新账号冻结期 —— 一整套资金闸门)之后既不刷本进程缓存,
// 也不推版本号,而跨节点收敛的唯一触发点正是 invalidateSettings 里的
// bumpSettingsVersion。表现是改名之后 ≤60 秒内这一档静默退化成全站兜底门槛:
// 在跑着的演示站上实测,一档 daily_max_quota=1000000 的分组改名之后,
// 同一秒 4,000,000 的划转从「被拒」变成「成功,钱真的动了」;
// 另一档 720 小时反套现冻结期改名之后,一个注册 7 秒的新号立刻转走 4,000,000。
func TestTransferResidueInvalidatesTierCacheAfterCommit(t *testing.T) {
	gdb := newResidueCacheDB(t)
	prev := qyConfig.Swap(&config.Config{Enabled: true, Transfer: baseConfig()})
	t.Cleanup(func() { qyConfig.Store(prev) })

	daily := int64(1_000_000)
	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "lo", DailyMaxQuota: &daily, Enabled: true,
	}).Error)

	// 预热:改名前这一档确实生效。
	before := tierDailyMax(t, "lo")
	require.EqualValues(t, 1_000_000, before, "预热失败:这一档本来就没生效")

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "lo", "lo2", true)
	}))
	// Sweep 自己不许刷缓存 —— 刷在事务里会让并发读把未提交的旧行填回来。
	assert.EqualValues(t, 1_000_000, tierDailyMax(t, "lo"),
		"sweepResidue 不许在事务内刷缓存")

	groupns.CommitResidues("lo", "lo2", true)

	assert.EqualValues(t, 1_000_000, tierDailyMax(t, "lo2"),
		"改名之后新名字必须立刻拿到这一档,而不是等 60 秒 TTL 才收敛")
	assert.EqualValues(t, baseConfig().DailyMaxQuota, tierDailyMax(t, "lo"),
		"旧名字必须回落全站兜底")
}

func tierDailyMax(t *testing.T, group string) int64 {
	t.Helper()
	s, ok, err := resolveSettings(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	cfg, err := s.transferFor(group)
	require.NoError(t, err)
	return cfg.DailyMaxQuota
}

func newResidueCacheDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(&GroupRule{}, &GroupLimit{}, &SettingsVersion{}, &qymodel.Setting{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	invalidateSettings()
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		invalidateSettings()
		_ = sqlDB.Close()
	})
	return gdb
}
