package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
)

// migrateLockName 是 MySQL 命名锁的键。GET_LOCK 是连接级的,
// 因此必须固定在同一条 *sql.Conn 上获取和释放。
const migrateLockName = "qy_schema_migrate"

// migrateLockTimeoutSeconds 是等待其他节点让出迁移锁的时长。
// 超时不是错误 —— 说明别的节点正在迁移,本节点跳过即可。
const migrateLockTimeoutSeconds = 30

// migrateTimeout 是整个迁移过程的预算。DDL 在大表上很慢,给足。
const migrateTimeout = 30 * time.Minute

// openMigrationConn 打开一条迁移专用连接。
//
// 独立于业务连接池,DSN 里去掉了读写超时(见 migrationDSN),
// 且只允许一条连接 —— GET_LOCK 是连接级的,多一条都可能让释放打错地方。
func openMigrationConn() (*sql.DB, error) {
	cfg := config.Get().Database
	sqlDB, err := sql.Open("mysql", migrationDSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("qianye: 打开迁移专用连接失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	return sqlDB, nil
}

// Migrate 执行扩展库的自动迁移。
//
// 两道门缺一不可:
//  1. common.IsMasterNode —— 但它只是 NODE_TYPE != "slave" 这个环境变量,
//     多个节点完全可能都被配成 master;
//  2. MySQL GET_LOCK —— 真正的跨节点互斥。没有它,多 master 并发 AutoMigrate
//     会在 MySQL 上互相锁表甚至死锁。
func Migrate(models ...any) error {
	if !common.IsMasterNode {
		common.SysLog("qianye: 从节点,跳过扩展库自动迁移")
		return nil
	}
	if !config.Get().Database.ShouldAutoMigrate() {
		common.SysLog("qianye: database.auto_migrate 为 false,跳过扩展库自动迁移")
		return nil
	}
	gdb := Get()
	if gdb == nil {
		return ErrNotReady
	}
	if len(models) == 0 {
		return nil
	}

	// 迁移必须走一条独立的、不受 readTimeout 约束的连接。
	//
	// 业务连接池的 DSN 里带 readTimeout(默认 30 秒),那是给热路径兜底用的:
	// 它是驱动层"每次读结果包"的硬 deadline,与 ctx 无关,也不区分语句类型。
	// 而迁移里有两类必然超过它的读:
	//   1. SELECT GET_LOCK(name, 30) —— 从节点抢锁时会阻塞满 30 秒,与 readTimeout
	//      恰好相等,谁先触发不确定。readTimeout 先到就变成错误,主程序 FatalLog,
	//      而正确行为是"另一节点在迁移,本节点跳过"后正常启动。
	//   2. 大表 ADD COLUMN —— 千万行的 DDL 超过 30 秒是常态。被驱动掐断后
	//      MySQL 的 DDL 不可回滚,表会停在半迁移态。
	//
	// 所以这里单开一个只有一条连接的 sql.DB,DSN 去掉读写超时。
	migDB, err := openMigrationConn()
	if err != nil {
		return err
	}
	defer migDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancel()

	// GET_LOCK / RELEASE_LOCK 必须打在同一条连接上,否则释放会作用到别的连接。
	conn, err := migDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("qianye: 获取迁移专用连接失败: %w", err)
	}
	defer conn.Close()

	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)",
		migrateLockName, migrateLockTimeoutSeconds).Scan(&got); err != nil {
		return fmt.Errorf("qianye: 获取迁移锁失败: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		common.SysLog("qianye: 另一节点正在执行扩展库迁移,本节点跳过")
		return nil
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", migrateLockName); err != nil {
			common.SysError("qianye: 释放迁移锁失败: " + err.Error())
		}
	}()

	if err := gdb.AutoMigrate(models...); err != nil {
		return fmt.Errorf("qianye: 扩展库自动迁移失败: %w", err)
	}
	common.SysLog(fmt.Sprintf("qianye: 扩展库迁移完成,共 %d 张表", len(models)))
	return nil
}

// TableCount 统计扩展库中 qy_ 前缀的表数量,供健康面板核对迁移结果。
func TableCount() int64 {
	gdb := Get()
	if gdb == nil {
		return 0
	}
	var n int64
	err := gdb.Raw(`SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name LIKE 'qy\_%'`).Scan(&n).Error
	if err != nil {
		MarkFailure(err)
		return 0
	}
	return n
}
