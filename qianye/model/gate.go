package model

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
)

// gateKeyPrefix 把闸门锚点与 qy_kv 里的游标类键区分开。
const gateKeyPrefix = "gate:"

// LockGate 取一把以 name 为键的锚点行锁,用来把"先数一下再决定放不放行"
// 这类闸门串行化。**必须在事务里调用**,锁随事务提交/回滚释放。
//
// # 为什么不能直接给 COUNT 加 FOR UPDATE
//
// 原写法是 `SELECT COUNT(*) ... FOR UPDATE`,靠的是 MySQL InnoDB 在
// REPEATABLE READ 下会对扫过的索引范围加 next-key 锁,并发的 INSERT 因此排队。
// 那句话在另外两种方言上都不成立,而且失效方式完全不同:
//
//   - PostgreSQL:直接报 `FOR UPDATE is not allowed with aggregate functions`,
//     整条语句失败 —— 闸门后面的写入根本执行不了(响亮但功能坏掉);
//   - PostgreSQL 即便把聚合拆开写成 `SELECT id ... FOR UPDATE`,它在
//     READ COMMITTED 下**只锁已存在的行**,不阻止并发插入新行 ——
//     两个并发请求会双双读到旧计数并双双写入(安静且闸门失守);
//   - SQLite:没有行锁,FOR UPDATE 由驱动整段丢弃。
//
// 锚点行锁在三种方言上是同一个东西:一把普通的单行排他锁。
// 拿到它之后再数、再写,计数与写入之间不存在别人插队的窗口。
//
// 锚点行放在 qy_kv(地基表,主键就是 K),不为每个闸门新建一张只有一行的表。
func LockGate(tx *gorm.DB, name string) error {
	key := gateKeyPrefix + name
	// 先补行:锚点行不存在时 FOR UPDATE 会锁不到任何东西(不是报错),
	// 那样两个并发请求都拿不到锁、都往下走 —— 闸门在**首次使用**那一刻失守。
	seed := KV{K: key, V: "", UpdatedAt: common.GetTimestamp()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		return err
	}
	var row KV
	return db.LockForUpdate(tx).Where("k = ?", key).Take(&row).Error
}
