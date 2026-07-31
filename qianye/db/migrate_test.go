package db

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 自检用的两张假表。刻意不复用 qianye/model 里的真表:
// qianye/model 依赖 qianye/db,反向 import 会成环。
type qyMigrateTestFoundation struct{ ID int }

func (qyMigrateTestFoundation) TableName() string { return "qy_test_foundation" }

type qyMigrateTestModule struct{ ID int }

func (qyMigrateTestModule) TableName() string { return "qy_test_module" }

// useOfflineHandle 装一个全程不触网的 GORM 句柄。
//
// SkipInitializeWithVersion + DisableAutomaticPing 让 gorm.Open 不去连服务器;
// 自检只用它做 schema 解析(表名)与一次被 stub 掉的查询,因此完全不需要真实 MySQL。
func useOfflineHandle(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "u:p@tcp(qy-test-invalid-host.invalid:3306)/qy?parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	prev := handle.Load()
	handle.Store(gdb)
	t.Cleanup(func() { handle.Store(prev) })
	return gdb
}

// stubExistingTables 替换"扩展库里现在有哪些表"的读取方式。
func stubExistingTables(t *testing.T, existing []string, err error) {
	t.Helper()
	prev := tableLister
	tableLister = func(*gorm.DB) ([]string, error) { return existing, err }
	t.Cleanup(func() { tableLister = prev })
}

// asNode 固定本节点角色。测试进程里 common.InitEnv 从未跑过,
// IsMasterNode 的零值是 false(从节点),所以主节点分支必须显式设置,
// 否则"auto_migrate=false"那一条会被从节点分支抢先命中,测了个寂寞。
func asNode(t *testing.T, master bool) {
	t.Helper()
	prev := common.IsMasterNode
	common.IsMasterNode = master
	t.Cleanup(func() { common.IsMasterNode = prev })
}

// loadConfigWithAutoMigrate 让进程级配置进入 auto_migrate=false 的状态。
// 清理时改写同一个文件为 enabled: false 再加载一次,避免污染同包其他用例。
func loadConfigWithAutoMigrate(t *testing.T, autoMigrate bool) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	body := "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n  auto_migrate: false\n"
	if autoMigrate {
		body = "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n  auto_migrate: true\n"
	}
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
	t.Cleanup(func() {
		_ = os.WriteFile(p, []byte("enabled: false\n"), 0o600)
		_ = config.Load()
	})
}

// resetSchemaState 清掉 schema 降级态与后台复查的一次性开关。
//
// 这两个都是包级状态,不清的话用例之间会互相污染:前一个用例留下的缺表清单
// 会让后一个用例的 SchemaIncomplete() 断言"碰巧"通过。
func resetSchemaState(t *testing.T) {
	t.Helper()
	prevMissing := schemaMissing.Load()
	prevInterval := schemaRecheckInterval
	schemaMissing.Store(nil)
	schemaRecheckOnce = sync.Once{}
	t.Cleanup(func() {
		schemaMissing.Store(prevMissing)
		schemaRecheckInterval = prevInterval
		schemaRecheckOnce = sync.Once{}
	})
}

// stubAutoMigrate 固定"本节点这一轮到底有没有建表"的结论。
//
// 抢锁失败(多 master 并发启动)与"刚亲自跑完 DDL"这两条分支需要真实的
// 多节点 MySQL 才能自然到达,上一轮因此只能靠推理保证。
func stubAutoMigrate(t *testing.T, err error) {
	t.Helper()
	prev := runAutoMigrate
	runAutoMigrate = func(*gorm.DB, []any) error { return err }
	t.Cleanup(func() { runAutoMigrate = prev })
}

// 本节点建不出表时,缺表绝不能阻断主程序启动。
//
// 这是 M10 要守的东西。上一轮把 Migrate 拆成 autoMigrate + verifyTables 时,
// 只给"抢锁失败"加了豁免,而从节点在第一道门就 return nil,于是走进严格分支:
// 缺表 → bootstrap 返回 err → main.go 的 common.FatalLog → 整个 new-api 进程
// (含全部上游 relay 流量)退出。而从节点结构性地永远建不出表,它看到的缺表
// 恰恰就是"主节点此刻正在跑 DDL"这个秒级到分钟级的中间态,于是无限重启回退。
//
// 把这三条分支里的任意一条改回"返回缺表 error"(或让 autoMigrate 重新返回裸 nil、
// 从而落进 owner 严格分支),对应子用例立刻变红。
func TestMigrateNeverBlocksStartupWhenThisNodeCannotCreateTables(t *testing.T) {
	models := []any{&qyMigrateTestFoundation{}, &qyMigrateTestModule{}}

	t.Run("从节点缺表:降级而不是打死网关", func(t *testing.T) {
		useOfflineHandle(t)
		resetSchemaState(t)
		asNode(t, false)
		// 只建出了地基表,模块表被漏掉。
		stubExistingTables(t, []string{"qy_test_foundation"}, nil)

		require.NoError(t, Migrate(models...),
			"从节点建不出表,让整台网关退出既修不好 schema,又会把它打进重启循环")

		assert.True(t, SchemaIncomplete(), "缺表必须被记成降级态,而不是被咽掉")
		assert.Equal(t, []string{"qy_test_module"}, MissingTables(),
			"降级态必须点名缺哪张表 —— 运维拿到「有表缺失」四个字什么也做不了")
	})

	t.Run("auto_migrate=false 缺表:降级而不是打死网关", func(t *testing.T) {
		useOfflineHandle(t)
		resetSchemaState(t)
		asNode(t, true) // 主节点,唯一被跳过的理由是 auto_migrate=false
		loadConfigWithAutoMigrate(t, false)
		require.False(t, config.Get().Database.ShouldAutoMigrate())
		stubExistingTables(t, []string{"qy_test_module"}, nil)

		require.NoError(t, Migrate(models...),
			"表由 DBA 手工建,本节点同样无权建表,不该有资格让主程序退出")
		assert.Equal(t, []string{"qy_test_foundation"}, MissingTables())
	})

	t.Run("另一节点正持有迁移锁:降级而不是打死网关", func(t *testing.T) {
		useOfflineHandle(t)
		resetSchemaState(t)
		asNode(t, true)
		stubAutoMigrate(t, errMigrationInProgress)
		stubExistingTables(t, nil, nil)

		require.NoError(t, Migrate(models...))
		assert.Equal(t, []string{"qy_test_foundation", "qy_test_module"}, MissingTables(),
			"缺多张表时必须逐张列出")
	})

	t.Run("表齐全时不进降级态", func(t *testing.T) {
		useOfflineHandle(t)
		resetSchemaState(t)
		asNode(t, false)
		// 扩展库里多出无关的表不影响判定。
		stubExistingTables(t, []string{"qy_test_module", "users", "qy_test_foundation"}, nil)

		require.NoError(t, Migrate(models...))
		assert.False(t, SchemaIncomplete())
		assert.Nil(t, MissingTables())
	})
}

// 自检自身失败既不阻断启动,也不算"确认缺表"。
//
// db.Init 刚 Ping 成功过,此刻 information_schema 读不出来更像瞬时抖动。
// 把"不知道"当成"缺表"会让一次抖动把扩展打进降级态并刷屏,
// 反过来把它当成"表齐全"又会掩盖真实缺表 —— 两个方向都必须挡住。
func TestVerifyTablesTreatsAnUnreadableSchemaAsUnknown(t *testing.T) {
	models := []any{&qyMigrateTestFoundation{}, &qyMigrateTestModule{}}

	t.Run("无权建表的分支", func(t *testing.T) {
		useOfflineHandle(t)
		resetSchemaState(t)
		asNode(t, false)
		stubExistingTables(t, nil, errors.New("information_schema 暂时读不到"))

		require.NoError(t, Migrate(models...))
		assert.False(t, SchemaIncomplete(),
			"读不出表清单是「不知道」,不是「确认缺表」")
	})

	t.Run("刚跑完迁移的分支", func(t *testing.T) {
		gdb := useOfflineHandle(t)
		resetSchemaState(t)
		stubExistingTables(t, nil, errors.New("information_schema 暂时读不到"))

		assert.NoError(t, verifyTables(gdb, models),
			"自检自身失败不能让主程序起不来 —— 扩展绝不成为主程序的单点故障")
		assert.False(t, SchemaIncomplete())
	})
}

// 刚亲自跑完 AutoMigrate 却仍然缺表,必须阻断启动。
//
// 这是非对称契约的另一半,不能被"从节点不阻断"顺手抹掉:AutoMigrate 刚返回 nil
// 却还缺表,是本节点自己的自相矛盾(模型漏登记进 allTables()、DDL 没在
// database.dsn 指向的库上生效),不是别人的中间态,多等一分钟也不会自愈。
func TestMigrateStillBlocksTheNodeThatJustRanTheMigration(t *testing.T) {
	models := []any{&qyMigrateTestFoundation{}, &qyMigrateTestModule{}}

	useOfflineHandle(t)
	resetSchemaState(t)
	asNode(t, true)
	stubAutoMigrate(t, nil) // 本节点刚亲自把 DDL 跑完
	stubExistingTables(t, []string{"qy_test_foundation"}, nil)

	err := Migrate(models...)

	require.Error(t, err, "自己刚建过的表却不存在,是本节点该修、也只有本节点能修的问题")
	assert.Contains(t, err.Error(), "qy_test_module")
	assert.NotContains(t, err.Error(), "qy_test_foundation",
		"已经存在的表不该出现在缺失清单里")
}

// 降级态必须能自愈:主节点补完 DDL 后,从节点不需要重启。
func TestSchemaRecheckClearsTheDegradedStateWhenTablesAppear(t *testing.T) {
	models := []any{&qyMigrateTestFoundation{}, &qyMigrateTestModule{}}

	useOfflineHandle(t)
	resetSchemaState(t)
	asNode(t, false)
	stubExistingTables(t, []string{"qy_test_foundation"}, nil)
	require.NoError(t, Migrate(models...))
	require.True(t, SchemaIncomplete())

	// 主节点的 DDL 还没跑完:复查仍然缺表,不能自行解除。
	assert.False(t, recheckSchema(models))
	assert.Equal(t, []string{"qy_test_module"}, MissingTables())

	// 表建出来了。
	stubExistingTables(t, []string{"qy_test_foundation", "qy_test_module"}, nil)
	assert.True(t, recheckSchema(models), "表齐全后必须报告完成,让后台循环停下来")
	assert.False(t, SchemaIncomplete())
	assert.Nil(t, MissingTables())
}

// 复查逻辑必须真的被后台循环驱动。
//
// 单测 recheckSchema 只能证明"函数写对了",挡不住本项目反复出现的断链形状:
// 纯函数正确、调度层没接上。这条用例走真实的 StartSchemaRecheck 入口,
// 并顺带钉住"schema 完整时不起协程"这个前置判断 —— 少了它,正常部署下
// 那个一次性开关会被空转的协程用掉,真出现缺表时反而再也起不来。
func TestStartSchemaRecheckIsDrivenByTheBackgroundLoop(t *testing.T) {
	models := []any{&qyMigrateTestFoundation{}, &qyMigrateTestModule{}}

	useOfflineHandle(t)
	resetSchemaState(t)
	asNode(t, false)

	// 第一次:schema 完整,StartSchemaRecheck 应当原地返回、不消耗一次性开关。
	// 周期先设成一小时,这样即便判断被删掉,那个不该存在的协程也不会来搅局。
	stubExistingTables(t, []string{"qy_test_foundation", "qy_test_module"}, nil)
	require.NoError(t, Migrate(models...))
	require.False(t, SchemaIncomplete())
	schemaRecheckInterval = time.Hour
	StartSchemaRecheck(models...)

	// 第二次:确认缺表,此时才该起循环。
	stubExistingTables(t, []string{"qy_test_foundation"}, nil)
	require.NoError(t, Migrate(models...))
	require.True(t, SchemaIncomplete())

	// 主节点把表补齐(先换掉再起协程,避免和循环并发改同一个变量)。
	stubExistingTables(t, []string{"qy_test_foundation", "qy_test_module"}, nil)
	schemaRecheckInterval = 5 * time.Millisecond
	StartSchemaRecheck(models...)

	require.Eventually(t, func() bool { return !SchemaIncomplete() },
		5*time.Second, 5*time.Millisecond,
		"后台循环没有把复查结果接回降级态 —— 缺表将永远不会自愈")
}

// 表名按精确匹配比对。
//
// GORM 生成的表名一律小写,而 MySQL 在 lower_case_table_names=0(Linux 默认)下
// 表名大小写敏感。放宽成忽略大小写会让 DBA 建出的 "QY_Test_Foundation" 通过自检,
// 而后续每一条查询都会因为找不到 "qy_test_foundation" 而失败 ——
// 那正是自检本该挡住的场景。
func TestMissingTablesComparesTableNamesExactly(t *testing.T) {
	gdb := useOfflineHandle(t)
	models := []any{&qyMigrateTestFoundation{}}

	assert.Equal(t, []string{"qy_test_foundation"},
		missingTables(gdb, models, []string{"QY_Test_Foundation"}),
		"大小写不同的表名不算存在")
	assert.Empty(t, missingTables(gdb, models, []string{"qy_test_foundation"}))
}
