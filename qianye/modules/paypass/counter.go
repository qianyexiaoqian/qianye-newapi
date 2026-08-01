package paypass

import (
	"context"

	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

// counter.go —— 错误计数与锁定。整个文件只解决一件事:**并发绕过**。
//
// # 反面教材(绝不能这么写)
//
//	row := load(userId)            // 读
//	row.FailCount++                // 改
//	save(row)                      // 写
//
// 同一账号并发打 N 个请求时,N 个协程读到同一个旧值,各自 +1 后写回 —— 计数只涨 1。
// 阈值 5 次的锁定就此形同虚设:攻击者只要保持并发,永远到不了第 5 次。
// 这不是理论风险,它是"读-改-写"在任何隔离级别下的默认行为。
//
// # 这里怎么做
//
// 三条独立的、各自原子的语句,不需要事务,也不需要行锁:
//
//	① UPDATE ... SET fail_count = fail_count + 1        —— 数据库侧自增,N 个并发就是 +N
//	② SELECT fail_count                                 —— 读回自己这一次落在第几
//	③ UPDATE ... SET locked_until = ? WHERE locked_until < ?  —— 只延长、不缩短
//
// ①是不可能丢计数的:自增在数据库里完成,不经过应用内存。
// ③的 `locked_until < ?` 让多个并发失败者同时判定"该锁了"时不会互相把锁**缩短**
// (后到的那个若无条件覆盖,晚一点算出的截止时间反而可能更早)。
// ②读到的值只用来决定要不要执行③,即使它读到的是别的并发请求推高后的值也无妨 ——
// 那只意味着锁被更早地加上,方向是安全的那一边。
//
// # 为什么不用 CASE WHEN 一条语句搞定
//
// 写成 `SET fail_count = fail_count + 1, locked_until = CASE WHEN fail_count + 1 >= ? ...`
// 在 MySQL 与 SQLite/PostgreSQL 上语义**不同**:MySQL 的 UPDATE 赋值是从左到右求值的,
// 右边的表达式看到的是**已经被前一个赋值改过**的 fail_count;而 SQLite 与 PostgreSQL
// 一律用行的旧值。同一条 SQL 在生产(MySQL)与测试(SQLite)上差一个计数 ——
// 那正是"测试全绿、线上错一次"的经典形状。拆成三条就没有这个歧义。

// clearExpiredLock 在锁定期已过时把计数清零。
//
// 必须清 fail_count 而不是只清 locked_until:留着一个已经 >= 阈值的计数,
// 用户解锁后第一次输错就会立刻被重新锁上,等于锁定时长偷偷变成了无限。
//
// WHERE 里带 `locked_until <> 0 AND locked_until <= now` 让这条语句是幂等的:
// 并发的多个请求最多有一个真的改到行,其余 RowsAffected = 0。
func clearExpiredLock(ctx context.Context, gdb *gorm.DB, userId int, now int64) error {
	err := gdb.WithContext(ctx).Model(&PayPassword{}).
		Where("user_id = ? AND locked_until <> 0 AND locked_until <= ?", userId, now).
		Updates(map[string]any{
			"fail_count":   0,
			"locked_until": 0,
			"updated_at":   now,
		}).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// noteFailure 记一次输错,必要时加锁。返回加锁后的截止时间(未加锁时为 0)。
//
// 三条语句刻意不放进一个事务:它们各自原子,而事务只会让并发的失败者互相
// 等行锁 —— 在一个正被暴力破解的账号上,那意味着连接池被这些请求占满。
// 中途失败的后果也可以接受:①成功②失败 = 计数已经涨了但这次没加锁,
// 下一次失败会补上;①失败 = 这一次没被计数,但攻击者无法**选择性地**让①失败。
func noteFailure(ctx context.Context, gdb *gorm.DB, userId int, now int64, p policy) (int64, error) {
	// ① 原子自增。这一条是整套防并发绕过的核心,绝不能退化成读-改-写。
	err := gdb.WithContext(ctx).Model(&PayPassword{}).
		Where("user_id = ?", userId).
		Updates(map[string]any{
			"fail_count": gorm.Expr("fail_count + 1"),
			"updated_at": now,
		}).Error
	if err != nil {
		db.MarkFailure(err)
		return 0, err
	}

	// ② 读回当前计数。
	var row PayPassword
	if err := gdb.WithContext(ctx).Where("user_id = ?", userId).First(&row).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	if row.FailCount < p.MaxAttempts {
		return 0, nil
	}

	// ③ 加锁,只延长不缩短。
	until := now + p.lockSeconds()
	err = gdb.WithContext(ctx).Model(&PayPassword{}).
		Where("user_id = ? AND locked_until < ?", userId, until).
		Updates(map[string]any{
			"locked_until": until,
			"updated_at":   now,
		}).Error
	if err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	return until, nil
}

// clearFailures 在验密成功后把计数与锁定清零。
//
// WHERE 带 `(fail_count <> 0 OR locked_until <> 0)`:绝大多数验密是一次就过的,
// 不加这个条件等于每笔划转都白写一行。
func clearFailures(ctx context.Context, gdb *gorm.DB, userId int, now int64) error {
	err := gdb.WithContext(ctx).Model(&PayPassword{}).
		Where("user_id = ? AND (fail_count <> 0 OR locked_until <> 0)", userId).
		Updates(map[string]any{
			"fail_count":   0,
			"locked_until": 0,
			"updated_at":   now,
		}).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}
