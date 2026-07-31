package transfer

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// loadRetainConfig 加载一份只关心保留期的配置。
//
// 必须走真正的 config.Load() 而不是直接塞结构体:被测缺陷正是
// "YAML 里的键根本没人读",绕过加载路径就等于把缺陷本身绕过去了。
func loadRetainConfig(t *testing.T, days int) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	yaml := `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
transfer:
  enabled: true
  lookup_log_retain_days: ` + strconv.Itoa(days) + "\n"
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
}

// newLookupLogDB 建一个只承载解析日志表的内存库。
// 清理条件写在 WHERE 里,只有真让数据库执行一遍才算验证过。
func newLookupLogDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&LookupLog{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

func seedLookupLog(t *testing.T, gdb *gorm.DB, identifier string, ageDays int64) {
	t.Helper()
	require.NoError(t, gdb.Create(&LookupLog{
		UserId:     1,
		Identifier: identifier,
		ByType:     "id",
		CreatedAt:  common.GetTimestamp() - ageDays*86400,
	}).Error)
}

func remainingLookupLogs(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	var out []string
	require.NoError(t, gdb.Model(&LookupLog{}).Order("id asc").Pluck("identifier", &out).Error)
	return out
}

// OLD-1:transfer.lookup_log_retain_days 有定义、有默认值、写进了示例 YAML,
// 清理任务却用包内常量 lookupLogRetainDays = 30 —— 运维为合规改成 7、
// 为风控改成 90 都完全无效,而这张表记的是"谁查过谁的收款人",
// 属于可关联到个人的行为日志。
//
// 保留期取 7 天、样本取 10 天前:沿用常量 30 的实现会把 10 天前那条留下来。
func TestPruneLookupLogs_HonoursConfiguredRetention(t *testing.T) {
	loadRetainConfig(t, 7)
	gdb := newLookupLogDB(t)

	seedLookupLog(t, gdb, "beyond-7d", 10)
	seedLookupLog(t, gdb, "within-7d", 3)

	pruneLookupLogs(context.Background(), gdb)

	assert.Equal(t, []string{"within-7d"}, remainingLookupLogs(t, gdb),
		"保留期必须取自配置:配成 7 天时 10 天前的解析日志必须被清掉")
}

// 反方向同样要成立 —— 配成 90 天时,常量 30 会把 40 天前那条误删。
// 少了这一半,一个"永远删光"的实现也能通过上面那个用例。
func TestPruneLookupLogs_LongerRetentionKeepsOlderRows(t *testing.T) {
	loadRetainConfig(t, 90)
	gdb := newLookupLogDB(t)

	seedLookupLog(t, gdb, "within-90d", 40)
	seedLookupLog(t, gdb, "beyond-90d", 100)

	pruneLookupLogs(context.Background(), gdb)

	assert.Equal(t, []string{"within-90d"}, remainingLookupLogs(t, gdb),
		"保留期配成 90 天时,40 天前的解析日志不该被清掉")
}

// 保留期填负数表示关掉清理(填 0 会被 applyDefaults 补成 30,这是刻意的:
// 少配一个键不该让行为日志永久留存);失去租约(ctx 取消)必须立刻停手,
// 否则会与接管节点双跑。
func TestPruneLookupLogs_SkipsWhenDisabledOrCancelled(t *testing.T) {
	gdb := newLookupLogDB(t)
	seedLookupLog(t, gdb, "ancient", 400)

	loadRetainConfig(t, -1)
	pruneLookupLogs(context.Background(), gdb)
	assert.Equal(t, []string{"ancient"}, remainingLookupLogs(t, gdb), "保留期为负数时不该清理")

	loadRetainConfig(t, 7)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	pruneLookupLogs(cancelled, gdb)
	assert.Equal(t, []string{"ancient"}, remainingLookupLogs(t, gdb), "失去租约后不该继续写库")
}
