package audit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// retention_test.go —— 清理任务真的对数据库跑一遍。
//
// 这几条守的东西全都住在 WHERE 条件与循环结构里:"0 是不是被当成删光"、
// "低于下限能不能绕过"、"是不是退化成一条大 DELETE"。把 GORM mock 掉等于
// 把被测对象换成测试自己写的假设,所以这里开真的 sqlite 让它执行。
//
// 覆盖不到的那一半是"任务有没有被注册" —— 它在 qianye/bootstrap.go 里,
// 由 qianye/audit_prune_registration_test.go 用 AST 锁住。

const day = int64(86400)

func newAuditDB(t *testing.T) (*gorm.DB, *sqlRecorder) {
	t.Helper()
	rec := &sqlRecorder{Interface: gormlogger.Discard}
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: rec})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&qymodel.AuditLog{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb, rec
}

// sqlRecorder 把每条真正发给数据库的语句记下来。
//
// "是不是分批"这个性质在返回值上看不出来 —— 一条 `DELETE WHERE created_at < ?`
// 和一串按主键开窗的 DELETE 删掉的行数完全一样。只有语句本身能区分两者。
type sqlRecorder struct {
	gormlogger.Interface
	mu   sync.Mutex
	sqls []string
}

func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sqls = append(r.sqls, sql)
}

func (r *sqlRecorder) statements(kind string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.sqls {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s)), kind) {
			out = append(out, s)
		}
	}
	return out
}

func (r *sqlRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sqls = nil
}

// seedAges 按"距今多少天"批量插入审计行,返回 now。
func seedAges(t *testing.T, gdb *gorm.DB, now int64, agesInDays ...int64) {
	t.Helper()
	rows := make([]qymodel.AuditLog, 0, len(agesInDays))
	for _, age := range agesInDays {
		rows = append(rows, qymodel.AuditLog{
			TraceNo: fmt.Sprintf("T%d", age), Category: qymodel.AuditCategoryWithdraw,
			Action: "withdraw.approve", ActorType: qymodel.ActorAdmin,
			Result: qymodel.ResultOK, CreatedAt: now - age*day,
		})
	}
	require.NoError(t, gdb.CreateInBatches(&rows, 200).Error)
}

func countRows(t *testing.T, gdb *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, gdb.Model(&qymodel.AuditLog{}).Count(&n).Error)
	return n
}

// ── ② 0 不是"删光" ────────────────────────────────────────────────────
//
// 这是本文件最重要的一条。危险的写法只差一个 early return:
//
//	cutoff := now - int64(days)*86400     // days=0 ⇒ cutoff == now
//	... WHERE created_at < cutoff         // ⇒ 整张表
//
// 也就是说"永久保留"这个默认值会被实现成"每 6 小时把审计表清空一次",
// 而且是在所有现存部署上 —— 它们的 retention_days 全都是 0。
func TestPruneExpired_ZeroAndNegativeDaysDeleteNothing(t *testing.T) {
	for _, days := range []int{0, -1, -365} {
		t.Run(fmt.Sprintf("%d天", days), func(t *testing.T) {
			gdb, rec := newAuditDB(t)
			now := time.Now().Unix()
			// 全部是"按任何非零保留期都该删"的极老行,唯一拦住它们的只有 days<=0。
			seedAges(t, gdb, now, 10000, 5000, 3000, 800, 400)
			rec.reset()

			deleted, err := pruneExpired(context.Background(), gdb, &qymodel.AuditLog{}, days, now)
			require.NoError(t, err)
			// 先取语句快照再查行数 —— countRows 自己也会发一条 SELECT。
			deletes, selects := rec.statements("DELETE"), rec.statements("SELECT")

			assert.Zero(t, deleted, "days<=0 表示永久保留,一行都不能删")
			assert.EqualValues(t, 5, countRows(t, gdb))
			assert.Empty(t, deletes,
				"days<=0 时不该向数据库发出任何 DELETE —— 连一条删了 0 行的语句都不该发生")
			assert.Empty(t, selects,
				"days<=0 应当在扫描之前就返回,否则每 6 小时白扫一遍全表")
		})
	}
}

// ── ③ 下限不可绕过 ───────────────────────────────────────────────────
//
// 加载期的 validateAudit 已经会拒绝启动(见 config/audit_retention_test.go)。
// 这里守的是防御纵深:热载、手工塞 current 快照、或者将来某个"管理端改配置"
// 的接口绕过 Load,都不该能让一个没经过校验的保留期真的动手删证据。
func TestPruneExpired_RefusesBelowFloorEvenIfConfigWasBypassed(t *testing.T) {
	for _, days := range []int{1, 7, 30, 180, config.MinAuditRetentionDays - 1} {
		t.Run(fmt.Sprintf("%d天", days), func(t *testing.T) {
			gdb, rec := newAuditDB(t)
			now := time.Now().Unix()
			seedAges(t, gdb, now, 10000, 5000, 400)
			rec.reset()

			deleted, err := pruneExpired(context.Background(), gdb, &qymodel.AuditLog{}, days, now)
			require.NoError(t, err)
			deletes := rec.statements("DELETE") // countRows 之前取,它自己也发语句

			assert.Zero(t, deleted)
			assert.EqualValues(t, 3, countRows(t, gdb),
				"低于硬下限时宁可不删:少删是磁盘问题,多删是仲裁凭据没了")
			assert.Empty(t, deletes)
		})
	}
}

// 达到下限就必须真的删,否则这个配置项等于只能填 0。
//
// 顺带钉死边界:cutoff 那一刻的行属于保留期内(用 < 而不是 <=)。
// 保留期边界上多留一行是无害的,少留一行是删早了。
func TestPruneExpired_DeletesOnlyRowsStrictlyOlderThanCutoff(t *testing.T) {
	gdb, _ := newAuditDB(t)
	now := time.Now().Unix()
	days := config.MinAuditRetentionDays
	seedAges(t, gdb, now,
		int64(days)+1, // 过期
		int64(days),   // 正好落在 cutoff 上 ⇒ 保留
		int64(days)-1, // 保留
		0,             // 刚写的
	)

	deleted, err := pruneExpired(context.Background(), gdb, &qymodel.AuditLog{}, days, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	var kept []qymodel.AuditLog
	require.NoError(t, gdb.Order("created_at asc").Find(&kept).Error)
	require.Len(t, kept, 3)
	cutoff := now - int64(days)*day
	assert.EqualValues(t, cutoff, kept[0].CreatedAt,
		"created_at == cutoff 的行必须留下:边界只能往多留的方向倒")
}

// ── ④ 必须按主键开窗分批,不能退化成一条大 DELETE ─────────────────────
//
// 退化的代价不在这张表上:一条 DELETE 干掉上百万行会长时间持锁、撑爆 binlog,
// 把同一个扩展库上的资金补偿与提现对账一起拖死。而且 qy_audit_logs 上没有
// created_at 的单列索引,裸 `WHERE created_at < ?` 是一次全表扫描。
func TestPruneExpired_DeletesInPrimaryKeyWindowsNotOneBigDelete(t *testing.T) {
	gdb, rec := newAuditDB(t)
	now := time.Now().Unix()
	const total = pruneScanSize*2 + 137 // 跨三个窗口,且最后一窗不满
	ages := make([]int64, 0, total)
	for i := 0; i < total; i++ {
		ages = append(ages, int64(config.MinAuditRetentionDays)+1+int64(i))
	}
	seedAges(t, gdb, now, ages...)
	rec.reset()

	deleted, err := pruneExpired(context.Background(), gdb, &qymodel.AuditLog{}, config.MinAuditRetentionDays, now)
	require.NoError(t, err)
	// 语句快照必须在 countRows 之前取:那一条 SELECT count(*) 也会被记进来。
	deletes, selects := rec.statements("DELETE"), rec.statements("SELECT")

	assert.EqualValues(t, total, deleted)
	assert.Zero(t, countRows(t, gdb))

	assert.Len(t, deletes, 3,
		"%d 行 / 每窗 %d 行 应当发出 3 条 DELETE;变成 1 条说明分批被拆掉了",
		total, pruneScanSize)
	for _, sql := range deletes {
		assert.Contains(t, sql, "IN (",
			"每条 DELETE 都必须按一批具体主键删,而不是给数据库一个开放区间")
		assert.NotContains(t, sql, "created_at",
			"DELETE 里出现 created_at 意味着退回成 `DELETE WHERE created_at < ?` —— "+
				"没有索引可走,且一条语句的影响行数无上界")
		assert.LessOrEqual(t, strings.Count(sql, ",")+1, pruneScanSize,
			"单条 DELETE 的主键个数不得超过 %d", pruneScanSize)
	}

	for _, sql := range selects {
		assert.Contains(t, sql, "LIMIT", "扫描必须开窗,否则一次把整张表拉进内存")
		assert.Contains(t, sql, "id >", "扫描必须按主键推进游标,否则每一窗都是全表扫描")
	}
}

// 表尾之后必须停:窗口没填满/末行已进入保留期 都是终止条件。
//
// 少了这两个 break,每一轮都会把 pruneMaxWindows 个空窗扫满 ——
// 一张只有几百行的表也要发 200 条 SELECT,每 6 小时一次。
func TestPruneExpired_StopsScanningOnceInsideRetentionWindow(t *testing.T) {
	gdb, rec := newAuditDB(t)
	now := time.Now().Unix()
	days := config.MinAuditRetentionDays
	seedAges(t, gdb, now, int64(days)+10, int64(days)+5, 1, 0)
	rec.reset()

	deleted, err := pruneExpired(context.Background(), gdb, &qymodel.AuditLog{}, days, now)
	require.NoError(t, err)
	selects := rec.statements("SELECT") // countRows 之前取

	assert.EqualValues(t, 2, deleted)
	assert.EqualValues(t, 2, countRows(t, gdb))
	assert.Len(t, selects, 1, "一窗就看到了保留期内的行,不该再扫第二窗")
}

// ── ⑤ 两张审计表都必须被清理 ──────────────────────────────────────────
//
// qy_request_audits 加进来的时候,pruneExpired 里的 `&qymodel.AuditLog{}` 是
// 硬编码的。只加表、不加清理目标,是本仓最典型的断链形状:表建出来了、
// 中间件在写、保留期配置也在,但那张表永远只增不减,而且没有任何报错。
//
// 断言直接对着 pruneTargets:它是唯一决定"哪些表会被清"的地方,
// 逐表跑一遍 pruneExpired 反而验不到"某张表压根没进列表"。
func TestPruneTargets_CoversEveryAuditTable(t *testing.T) {
	gdb, _ := newAuditDB(t)
	require.NoError(t, gdb.AutoMigrate(&qymodel.RequestAudit{}))
	now := time.Now().Unix()
	days := config.MinAuditRetentionDays
	old := now - int64(days+1)*day

	seedAges(t, gdb, now, int64(days)+1, 0)
	require.NoError(t, gdb.CreateInBatches(&[]qymodel.RequestAudit{
		{Action: "transfer.create", CreatedAt: old},
		{Action: "transfer.create", CreatedAt: now},
	}, 10).Error)

	labels := make([]string, 0, len(pruneTargets))
	for _, target := range pruneTargets {
		labels = append(labels, target.label)
		deleted, err := pruneExpired(context.Background(), gdb, target.model, days, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted, "%s 应当只删掉那一行过期记录", target.label)
	}
	assert.ElementsMatch(t, []string{"qy_audit_logs", "qy_request_audits"}, labels,
		"每一张受 audit.retention_days 管辖的表都必须登记进 pruneTargets,"+
			"否则它只增不减,而且不会有任何报错")

	// 另一半断链:模型没登记进 FoundationTables 时,表压根不会被建出来。
	// 那种情况下 AutoMigrate 不报错、编译不报错,只有第一次写入才失败,
	// 而写入是 fail-open 的(只 SysError)—— 台账会永远为空。
	registered := make(map[string]bool, len(qymodel.FoundationTables()))
	for _, m := range qymodel.FoundationTables() {
		named, ok := m.(interface{ TableName() string })
		require.Truef(t, ok, "地基表 %T 必须实现 TableName()(表名硬编码为 qy_ 前缀)", m)
		registered[named.TableName()] = true
	}
	for _, target := range pruneTargets {
		assert.Truef(t, registered[target.label],
			"%s 在 pruneTargets 里,却没有登记进 model.FoundationTables() —— 表根本不会被建出来",
			target.label)
	}

	var reqLeft int64
	require.NoError(t, gdb.Model(&qymodel.RequestAudit{}).Count(&reqLeft).Error)
	assert.EqualValues(t, 1, reqLeft)
	assert.EqualValues(t, 1, countRows(t, gdb))
}

// 租约丢失后必须立刻停手:继续删就是与接管节点双跑。
func TestPruneExpired_StopsWhenLeaseContextIsCancelled(t *testing.T) {
	gdb, rec := newAuditDB(t)
	now := time.Now().Unix()
	seedAges(t, gdb, now, 10000, 9000, 8000)
	rec.reset()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deleted, err := pruneExpired(ctx, gdb, &qymodel.AuditLog{}, config.MinAuditRetentionDays, now)
	require.NoError(t, err)
	deletes := rec.statements("DELETE") // countRows 之前取

	assert.Zero(t, deleted)
	assert.EqualValues(t, 3, countRows(t, gdb), "ctx 已取消时一行都不该被删")
	assert.Empty(t, deletes)
}
