package model

import (
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// search_case_pg_test.go —— 管理端检索的 LIKE 分支必须打真 PostgreSQL 才验得到。
//
// 为什么不能只靠默认的 sqlite 用例:sqlite 的 `LIKE` 对 ASCII **本来就**
// 大小写不敏感(它的 `=` 才是逐字节比),所以把 `LOWER(col) LIKE ?` 改回
// `col LIKE ?`,sqlite 上一切照旧、测试全绿 —— 那正是这条缺陷能藏住的原因。
// 三种受支持主库在这一条上的真实分布是:
//
//	MySQL       ci 排序规则  → LIKE 大小写不敏感
//	SQLite      默认         → LIKE 对 ASCII 大小写不敏感、`=` 敏感
//	PostgreSQL  ——           → LIKE 与 `=` **都**大小写敏感
//
// 也就是说 PostgreSQL 是唯一一种「运营搜 qy-pg-u1 看不到 Qy-Pg-U1」的部署,
// 而接口仍然返回 200 + 空列表,与「这个人不存在」不可分辨。
//
// 设 QY_TEST_PG_DSN 指向一次性库即可,不设就干净 SKIP。
func TestAdminSearchFoldsCaseOnPostgres(t *testing.T) {
	dsn := os.Getenv("QY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 QY_TEST_PG_DSN,跳过(只有 PostgreSQL 的 LIKE 是大小写敏感的)")
	}

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&User{}, &Channel{}, &Log{}))

	prevDB, prevLogDB := DB, LOG_DB
	prevMain, prevLog := common.MainDatabaseType(), common.LogDatabaseType()
	DB, LOG_DB = gdb, gdb
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	InitCol()
	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM users WHERE username LIKE 'Qy-Pgcase%'`)
		gdb.Exec(`DELETE FROM channels WHERE name LIKE 'Qy-Pgcase%'`)
		gdb.Exec(`DELETE FROM logs WHERE username LIKE 'Qy-Pgcase%'`)
		DB, LOG_DB = prevDB, prevLogDB
		common.SetDatabaseTypes(prevMain, prevLog)
		InitCol()
	})

	hashed, err := common.Password2Hash("pg-case-pass-1")
	require.NoError(t, err)
	require.NoError(t, gdb.Exec(`DELETE FROM users WHERE username LIKE 'Qy-Pgcase%'`).Error)
	require.NoError(t, gdb.Exec(`DELETE FROM channels WHERE name LIKE 'Qy-Pgcase%'`).Error)
	require.NoError(t, gdb.Exec(`DELETE FROM logs WHERE username LIKE 'Qy-Pgcase%'`).Error)

	require.NoError(t, DB.Create(&User{
		Username: "Qy-Pgcase-User", Password: hashed, Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
		AffCode: "aff-qy-pgcase", DisplayName: "Qy-Pgcase-Display",
		Email: "Qy-Pgcase-User@example.test",
	}).Error)
	priority := int64(0)
	require.NoError(t, DB.Create(&Channel{
		Id: 990772, Name: "Qy-Pgcase-Channel", Priority: &priority, Models: "Qy-Pgcase-Model",
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		Id: 880002, UserId: 1, CreatedAt: common.GetTimestamp(), Type: LogTypeConsume,
		Username: "Qy-Pgcase-User", ModelName: "Qy-Pgcase-Model",
	}).Error)

	// ① 先证明这个库确实是 LIKE 大小写敏感的,否则本用例什么也没验到。
	var rawHits int64
	require.NoError(t, DB.Model(&User{}).
		Where("username LIKE ?", "%qy-pgcase-user%").Count(&rawHits).Error)
	require.Zero(t, rawHits, "这不是一个 LIKE 大小写敏感的库,本用例证明不了任何事")

	t.Run("用户检索", func(t *testing.T) {
		for _, keyword := range []string{"Qy-Pgcase-User", "qy-pgcase-user", "QY-PGCASE-USER"} {
			users, total, err := SearchUsers(keyword, "", nil, nil, 0, 10)
			require.NoError(t, err)
			assert.Equalf(t, int64(1), total, "keyword=%q", keyword)
			require.Len(t, users, 1)
			assert.Equal(t, "Qy-Pgcase-User", users[0].Username)
		}
		// display_name 与 email 两列同样折叠。
		for _, keyword := range []string{"qy-pgcase-display", "QY-PGCASE-USER@EXAMPLE.TEST"} {
			_, total, err := SearchUsers(keyword, "", nil, nil, 0, 10)
			require.NoError(t, err)
			assert.Equalf(t, int64(1), total, "keyword=%q", keyword)
		}
	})

	t.Run("渠道检索", func(t *testing.T) {
		for _, keyword := range []string{"Qy-Pgcase-Channel", "qy-pgcase-channel", "QY-PGCASE-CHANNEL"} {
			channels, err := SearchChannels(keyword, "", "", true)
			require.NoError(t, err)
			require.Lenf(t, channels, 1, "keyword=%q", keyword)
			assert.Equal(t, 990772, channels[0].Id)
		}
		// models 列(按模型名过滤)同样折叠。
		channels, err := SearchChannels("qy-pgcase-channel", "", "QY-PGCASE-MODEL", true)
		require.NoError(t, err)
		assert.Len(t, channels, 1)
	})

	t.Run("日志过滤", func(t *testing.T) {
		for _, tc := range []struct{ username, modelName string }{
			{"Qy-Pgcase-User", "Qy-Pgcase-Model"},
			{"qy-pgcase-user", "QY-PGCASE-MODEL"},
			{"QY-PGCASE-%", "qy-pgcase-%"}, // LIKE 分支
		} {
			logs, total, err := GetAllLogs(LogTypeConsume, 0, 0, tc.modelName, tc.username,
				"", 0, 10, 0, "", "", "", false)
			require.NoError(t, err)
			assert.Equalf(t, int64(1), total, "username=%q model=%q", tc.username, tc.modelName)
			require.Len(t, logs, 1)
			assert.Equal(t, 880002, logs[0].Id)
		}
	})
}
