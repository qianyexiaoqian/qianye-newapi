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

	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// GET_LOCK / RELEASE_LOCK 必须打在同一条连接上,否则释放会作用到别的连接。
	conn, err := sqlDB.Conn(ctx)
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
