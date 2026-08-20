package model

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// pgDSNEnv 是这组回归专用的 PostgreSQL DSN。
//
// 与 qianye/modules/{ticket,withdraw} 的 QY_TEST_MYSQL_DSN 同义:被测对象正是
// **方言本身**,用 SQLite 顶替就等于没测。没配就跳过,CI 不因此变红。
const pgDSNEnv = "QY_TEST_PG_DSN"

// newPostgresTestDB 把 model 包的主库句柄换成一个真实 PostgreSQL 连接。
//
// 每次跑都建一个独立 schema 并把 search_path 指过去,跑完整个 DROP 掉:
// 这组用例会 AutoMigrate + 写行,不能污染调用方给的库,也不能因为上一次
// 跑剩的行让 ON CONFLICT 的判据失真。
//
// InitCol() 必须跑:它由 InitDB 调用,而这里直接替换 DB 句柄绕过了那一步,
// 于是 commonGroupCol 会是空串,拼出来的 SQL 直接语法错误。
func newPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(pgDSNEnv))
	if dsn == "" {
		t.Skipf("PostgreSQL 方言回归需要真实 PG(被测对象就是方言),已跳过。\n"+
			"运行方式:%s='postgres://user:pass@127.0.0.1:5432/db?sslmode=disable' "+
			"go test ./model/ -run Postgres", pgDSNEnv)
	}

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err, "连接 PostgreSQL 失败")
	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	schema := fmt.Sprintf("qy_pg_dialect_%d", os.Getpid())
	require.NoError(t, gdb.Exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE").Error)
	require.NoError(t, gdb.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, gdb.Exec("SET search_path TO "+schema).Error)

	prevDB, prevType := DB, common.MainDatabaseType()
	DB = gdb
	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	InitCol()
	t.Cleanup(func() {
		_ = gdb.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		DB = prevDB
		common.SetMainDatabaseType(prevType)
		InitCol()
		_ = sqlDB.Close()
	})
	return gdb
}

// TestPostgresOnConflictTargetsHaveMatchingUniqueIndex 锁住一条只有 PostgreSQL 会
// 违反的约束:`ON CONFLICT (col...)` 要求存在一条**列集合完全匹配**的唯一索引,
// 否则 PG 直接报 "there is no unique or exclusion constraint matching the
// ON CONFLICT specification"。
//
// 为什么这条必须在 PG 上测:MySQL 渲染成 `ON DUPLICATE KEY UPDATE`,它的语法里
// **根本没有冲突目标列表**,Columns 写错写漏都不影响执行 —— 而生产部署正是 MySQL,
// 于是列集合与 uniqueIndex tag 的漂移可以一路带到线上都不报错。
//
// 而这两个函数在换到 PG 之前**一行测试都没有**(实查:UpsertPerfMetric、
// QyClaimFundOutbox 在全仓 _test.go 里各 0 处引用),所以漂移既没有夹具挡,
// 也没有方言挡。变异验证:把 Columns 里的 {Name:"group"} 去掉,PG 当场报
// SQLSTATE 42P10,本用例 KILLED。
func TestPostgresOnConflictTargetsHaveMatchingUniqueIndex(t *testing.T) {
	gdb := newPostgresTestDB(t)
	require.NoError(t, gdb.AutoMigrate(&PerfMetric{}, &QyFundOutbox{}))

	t.Run("perf_metric_upsert_accumulates", func(t *testing.T) {
		// OnConflict 打在 (model_name, "group", bucket_ts) 上 —— 三列复合唯一索引,
		// 其中 group 还是 PG 的保留字。DoUpdates 用的是 perf_metrics.x + ? 自增表达式,
		// 所以第二次写入的正确结果是**累加**而不是覆盖。
		base := &PerfMetric{ModelName: "gpt-4o", Group: "default", BucketTs: 1787097600}
		first := *base
		first.RequestCount, first.SuccessCount = 3, 2
		require.NoError(t, UpsertPerfMetric(&first))

		second := *base
		second.RequestCount, second.SuccessCount = 5, 4
		require.NoError(t, UpsertPerfMetric(&second))

		var got PerfMetric
		require.NoError(t, gdb.Where("model_name = ? AND "+commonGroupCol+" = ? AND bucket_ts = ?",
			base.ModelName, base.Group, base.BucketTs).First(&got).Error)
		assert.EqualValues(t, 8, got.RequestCount, "两次 upsert 的 request_count 应当累加")
		assert.EqualValues(t, 6, got.SuccessCount, "两次 upsert 的 success_count 应当累加")

		var rows int64
		require.NoError(t, gdb.Model(&PerfMetric{}).Count(&rows).Error)
		assert.EqualValues(t, 1, rows, "同一 (model,group,bucket) 只能有一行")
	})

	t.Run("fund_outbox_claim_is_idempotent", func(t *testing.T) {
		// 资金单去重靠 ON CONFLICT (order_no) DO NOTHING + RowsAffected:
		// 第一次抢到返回 true,第二次必须返回 false —— 调用方据此**跳过资金变更**。
		// 若 PG 因索引不匹配而报错,或 DoNothing 在该方言下把 RowsAffected 记成 1,
		// 同一笔单就会被扣两次钱。
		row := &QyFundOutbox{OrderNo: "qy-pg-claim-1", Kind: "transfer", UserId: 7, Amount: 500}
		claimed, err := QyClaimFundOutbox(gdb, row)
		require.NoError(t, err)
		assert.True(t, claimed, "首次登记应当抢到单号")

		dup := &QyFundOutbox{OrderNo: "qy-pg-claim-1", Kind: "transfer", UserId: 7, Amount: 500}
		claimed, err = QyClaimFundOutbox(gdb, dup)
		require.NoError(t, err, "重复单号必须靠 DO NOTHING 吞掉,而不是报唯一键冲突")
		assert.False(t, claimed, "重复登记必须返回 false,否则同一笔资金会被处理两次")

		var rows int64
		require.NoError(t, gdb.Model(&QyFundOutbox{}).Where("order_no = ?", "qy-pg-claim-1").Count(&rows).Error)
		assert.EqualValues(t, 1, rows)
	})
}

// TestPostgresLockForUpdateAndReservedWordsReachPG 验证两条拼接层的方言判据在
// **真的发到 PG 的语句里**成立,而不只是在 Go 侧的字符串里成立。
//
//   - lockForUpdate 在 PG 上必须真的加 FOR UPDATE。它对 SQLite 返回原样 tx,
//     所以"锁没加上"在 SQLite 夹具里同样是隐形的 —— 而这条锁正是余额扣减的
//     并发正确性前提。
//   - commonGroupCol / commonKeyCol 必须发双引号:group / key 都是 PG 保留字,
//     裸列名会是语法错误。
func TestPostgresLockForUpdateAndReservedWordsReachPG(t *testing.T) {
	gdb := newPostgresTestDB(t)
	require.NoError(t, gdb.AutoMigrate(&Ability{}))

	assert.Equal(t, `"group"`, commonGroupCol, "PG 下 group 必须双引号")
	assert.Equal(t, `"key"`, commonKeyCol, "PG 下 key 必须双引号")

	// DryRun 渲染的是**方言层**的最终 SQL,与真正发出去的一致。
	stmt := lockForUpdate(gdb.Session(&gorm.Session{DryRun: true}).Model(&Ability{})).
		Where(commonGroupCol+" = ?", "default").Find(&[]Ability{}).Statement
	sql := stmt.SQL.String()
	assert.Contains(t, sql, "FOR UPDATE", "PG 上 lockForUpdate 必须发出 FOR UPDATE")
	assert.Contains(t, sql, `"group"`, "保留字列必须以双引号出现在最终 SQL 里")

	// 再真的发一次:DryRun 只证明拼得出来,执行才证明 PG 认这条语句。
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		var out []Ability
		return lockForUpdate(tx.Model(&Ability{})).
			Where(commonGroupCol+" = ?", "default").Find(&out).Error
	}), "带 FOR UPDATE 的保留字查询必须能在 PG 上真正执行")
}
