package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestNormalizeDSNAddsDriverTimeouts 固化 C3(b):DSN 必须带上驱动层的硬上界。
//
// 没有 readTimeout/writeTimeout 时,一条撞上行锁的语句会一直等到 MySQL 的
// innodb_lock_wait_timeout(默认 50 秒),连接被占死;没有 timeout 时,连接池
// 后续新建连接的 dial 完全没有超时(Init 那一次 PingContext 管不到它们)。
func TestNormalizeDSNAddsDriverTimeouts(t *testing.T) {
	cases := []struct {
		name     string
		cfg      config.Database
		contains []string
		absent   []string
	}{
		{
			name: "裸 DSN 补齐全部参数",
			cfg: config.Database{
				DSN:                   "u:p@tcp(127.0.0.1:3306)/qy",
				ConnectTimeoutSeconds: 5,
				ReadTimeoutSeconds:    30,
				WriteTimeoutSeconds:   30,
			},
			contains: []string{
				"parseTime=true", "charset=utf8mb4",
				"timeout=5s", "readTimeout=30s", "writeTimeout=30s",
			},
		},
		{
			name: "已有查询串时用 & 追加",
			cfg: config.Database{
				DSN:                   "u:p@tcp(h:3306)/qy?parseTime=true&charset=utf8mb4",
				ConnectTimeoutSeconds: 3,
				ReadTimeoutSeconds:    20,
				WriteTimeoutSeconds:   10,
			},
			contains: []string{"&timeout=3s", "&readTimeout=20s", "&writeTimeout=10s"},
			// 不得重复追加已有参数
			absent: []string{"parseTime=true&parseTime", "charset=utf8mb4&charset"},
		},
		{
			name: "运维显式写过的超时不被覆盖",
			cfg: config.Database{
				DSN:                   "u:p@tcp(h:3306)/qy?readTimeout=90s&writeTimeout=90s&timeout=9s",
				ConnectTimeoutSeconds: 5,
				ReadTimeoutSeconds:    30,
				WriteTimeoutSeconds:   30,
			},
			contains: []string{"readTimeout=90s", "writeTimeout=90s", "timeout=9s"},
			absent:   []string{"readTimeout=30s", "writeTimeout=30s", "timeout=5s"},
		},
		{
			name: "零值(未配置)回落到默认值而不是 0 秒",
			cfg:  config.Database{DSN: "u:p@tcp(h:3306)/qy"},
			// 驱动语义里 0 表示"永不超时",正是要消灭的状态
			contains: []string{"timeout=5s", "readTimeout=30s", "writeTimeout=30s"},
			absent:   []string{"timeout=0s", "readTimeout=0s", "writeTimeout=0s"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeDSN(tc.cfg)
			for _, want := range tc.contains {
				assert.Contains(t, got, want)
			}
			for _, bad := range tc.absent {
				assert.NotContains(t, got, bad)
			}
		})
	}
}

// TestOpProbeAttributesStatementsPerContext 固化 C4 的判据来源:
// "本次 hook 到底有没有真的访问扩展库"必须能被逐次、逐 ctx 地判定。
//
// 判据不能是全局计数器 —— 并发 worker 的语句会互相污染,一个纯内存作业会被
// 误判成访问过库,进而清零熔断的失败计数。
func TestOpProbeAttributesStatementsPerContext(t *testing.T) {
	ctxA, touchedA := WithOpProbe(context.Background())
	_, touchedB := WithOpProbe(context.Background())

	require.False(t, touchedA(), "尚未执行任何语句")
	require.False(t, touchedB(), "尚未执行任何语句")

	noteOp(&gorm.DB{Statement: &gorm.Statement{Context: ctxA}})

	assert.True(t, touchedA(), "在 ctxA 下跑过语句")
	assert.False(t, touchedB(), "ctxB 没跑过语句,不得被 ctxA 污染")
}

// TestRegisterOpProbeFiresOnRealStatements 保证探针真的挂得上去。
//
// registerOpProbe 的失败会让 db.Init 返回 error、整个扩展启动即炸,而六个锚点名
// (gorm:create/query/update/delete/row/raw)是 GORM 内部约定,升级依赖时可能改名。
// 用 SkipInitializeWithVersion + DryRun 走完整的回调链,全程不需要真实 MySQL。
func TestRegisterOpProbeFiresOnRealStatements(t *testing.T) {
	// SkipInitializeWithVersion + DisableAutomaticPing = 全程零网络访问。
	gdb, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "u:p@tcp(qy-test-invalid-host.invalid:3306)/qy?parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	require.NoError(t, registerOpProbe(gdb), "六个回调锚点必须都存在,否则 db.Init 启动即失败")

	dry := gdb.Session(&gorm.Session{DryRun: true})

	t.Run("带探针 ctx 的查询被记到", func(t *testing.T) {
		ctx, touched := WithOpProbe(context.Background())
		var rows []map[string]any
		dry.WithContext(ctx).Table("qy_commission_accrual").Where("inviter_id = ?", 1).Find(&rows)
		assert.True(t, touched())
	})

	t.Run("没有探针的 ctx 不受影响", func(t *testing.T) {
		var rows []map[string]any
		assert.NotPanics(t, func() {
			dry.WithContext(context.Background()).Table("qy_commission_accrual").Find(&rows)
		})
	})
}

// TestNoteOpIgnoresStatementsWithoutProbe 保证探针对未接 ctx 的语句是无害的:
// GORM 回调跑在每一条语句上,任何 nil 解引用都会打爆整个扩展库访问。
func TestNoteOpIgnoresStatementsWithoutProbe(t *testing.T) {
	assert.NotPanics(t, func() { noteOp(nil) })
	assert.NotPanics(t, func() { noteOp(&gorm.DB{}) })
	assert.NotPanics(t, func() { noteOp(&gorm.DB{Statement: &gorm.Statement{}}) })
	assert.NotPanics(t, func() {
		noteOp(&gorm.DB{Statement: &gorm.Statement{Context: context.Background()}})
	})
}

// TestMarkProbeHealthyOnlyResetsOnRecovery 固化 C4 的另一半:
// 健康探测只在"不健康 → 健康"这一次转变时清零失败计数。
//
// 修复前 probe() 每 15 秒 Ping 成功就无条件 failStreak.Store(0),而 Ping 只验证
// TCP 与握手。扩展库"可达但查询慢"时,业务侧每积累一次超时都会被下一次探测
// 抹掉,连续失败数永远到不了阈值,熔断在唯一重要的场景下永远打不开。
func TestMarkProbeHealthyOnlyResetsOnRecovery(t *testing.T) {
	t.Run("healthy 期间不得清零业务侧的失败计数", func(t *testing.T) {
		healthy.Store(true)
		failStreak.Store(3)
		openUntil.Store(0)

		assert.False(t, markProbeHealthy(), "没有发生状态转变")
		assert.EqualValues(t, 3, failStreak.Load(), "Ping 成功不能抹掉业务侧的连续失败")
	})

	t.Run("由不健康恢复时清零全部熔断状态", func(t *testing.T) {
		healthy.Store(false)
		failStreak.Store(5)
		openUntil.Store(1 << 40)

		assert.True(t, markProbeHealthy(), "发生了 不健康 → 健康 的转变")
		assert.True(t, healthy.Load())
		assert.EqualValues(t, 0, failStreak.Load())
		assert.EqualValues(t, 0, openUntil.Load())
	})
}

// 迁移必须走一条不受 readTimeout 约束的连接。
//
// 业务 DSN 的 readTimeout 默认 30 秒,恰好等于 GET_LOCK 的等待上限,也远小于
// 大表 ADD COLUMN 的耗时。共用连接会让从节点抢锁时被驱动切断而启动失败,
// 也会把大表 DDL 掐死在半迁移态(MySQL 的 DDL 不可回滚)。
func TestMigrationDSNDisablesDriverReadTimeout(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Database
	}{
		{
			name: "默认值",
			cfg:  config.Database{DSN: "u:p@tcp(h:3306)/d"},
		},
		{
			name: "配置了显式超时",
			cfg:  config.Database{DSN: "u:p@tcp(h:3306)/d", ReadTimeoutSeconds: 30, WriteTimeoutSeconds: 30},
		},
		{
			name: "运维在 DSN 里手写了超时",
			cfg:  config.Database{DSN: "u:p@tcp(h:3306)/d?readTimeout=15s&writeTimeout=15s"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			business := normalizeDSN(tc.cfg)
			assert.Contains(t, business, "readTimeout=",
				"业务连接必须保留 readTimeout 作为连接级兜底")

			mig := migrationDSN(tc.cfg)
			assert.Contains(t, mig, "readTimeout=0",
				"迁移连接必须关闭 readTimeout,否则 GET_LOCK 等待与大表 DDL 会被驱动切断")
			assert.Contains(t, mig, "writeTimeout=0")
			assert.NotContains(t, mig, "readTimeout=30s")
			assert.NotContains(t, mig, "readTimeout=15s")
			// 其余参数不能在覆盖过程中被破坏。
			assert.Contains(t, mig, "parseTime=true")
			assert.Contains(t, mig, "charset=utf8mb4")
		})
	}
}

// 迁移预算必须显著大于抢锁等待,否则从节点会在正常的"别人在迁移"场景下超时。
func TestMigrateTimeoutExceedsLockWait(t *testing.T) {
	assert.Greater(t, migrateTimeout, time.Duration(migrateLockTimeoutSeconds)*time.Second*2,
		"迁移总预算必须远大于 GET_LOCK 的等待上限,否则抢锁本身就会耗尽预算")
}

// 调用方自己设的预算到期不是数据库的健康信号,绝不能计入熔断。
//
// 热路径预算只有几百毫秒,把它喂给熔断意味着一次尾延迟抖动就能攒够连续失败数,
// 把整个扩展熔断数十秒,期间所有热路径事件被直接丢弃 —— 判据被自己的预算污染。
func TestContextDeadlineIsNotAConnectionFailure(t *testing.T) {
	assert.False(t, isConnLevelError(context.DeadlineExceeded),
		"ctx 超时是我们自己设的预算,不是连接故障")
	assert.False(t, isConnLevelError(context.Canceled),
		"ctx 取消同理")
	assert.False(t, isConnLevelError(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)),
		"包装过的 ctx 超时同样不算")

	// 驱动层真正的连接故障仍必须被识别 —— 包括 readTimeout 触发的 i/o timeout。
	assert.True(t, isConnLevelError(errors.New("invalid connection")))
	assert.True(t, isConnLevelError(errors.New("dial tcp: i/o timeout")))
	assert.True(t, isConnLevelError(errors.New("connection refused")))
	// 业务错误依旧不计入。
	assert.False(t, isConnLevelError(errors.New("Error 1062: Duplicate entry")))
}

// 迁移专用连接池必须容得下"锁 + DDL"两条连接。
//
// GET_LOCK 是连接级的,Migrate 会一直持有那条连接直到函数返回;AutoMigrate 再从
// 同一个池要一条跑 DDL。池上限设成 1 会让 DDL 永远等那条被锁占着的连接 ——
// 进程静默卡死在迁移阶段,数据库端一条语句都看不到,日志停在"已连接"之后没有下文。
// 这个死锁编译和单测都发现不了,只有真跑起来才暴露,所以用一条结构性断言钉住。
func TestMigrationPoolLeavesRoomForBothLockAndDDL(t *testing.T) {
	for _, dsn := range []string{
		"u:p@tcp(127.0.0.1:3306)/qy_test",
		"postgres://u:p@127.0.0.1:5432/qy_test?sslmode=disable",
	} {
		sqlDB, err := openMigrationConnWith(config.Database{DSN: dsn})
		require.NoError(t, err, dsn)

		stats := sqlDB.Stats()
		assert.GreaterOrEqual(t, stats.MaxOpenConnections, 2,
			"迁移池至少要 2 条连接:一条被迁移锁占着,另一条跑 DDL(%s)", dsn)
		require.NoError(t, sqlDB.Close())
	}
}
