package ticket

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserState 是工单模块【每个用户一行】的串行化点,本身不承载任何业务数据。
//
// # 为什么需要它
//
// 建单闸门与图片配额闸门都是"先 COUNT 再 INSERT"的形状。同事务 ≠ 串行化:
// 扩展库固定是 MySQL,默认 REPEATABLE READ,每个事务在自己的快照里读计数 ——
// 并发提交的 N 个请求全部读到同一个旧值,于是 N 个全部放行。实测 8 并发建单
// 8/8 成功,max_open_per_user=5 与 cooldown=60s 双双失效。
//
// 修法与 transfer 模块一致(qy_transfer_user_state):让【闸门判定】与
// 【业务行插入】共享同一把行锁,把同一个用户的并发请求排成一队。
//
// # 为什么是一张独立的表,而不是给业务表的 COUNT 加 FOR UPDATE
//
// `SELECT ... WHERE user_id = ? FOR UPDATE` 在 InnoDB 里锁的是二级索引上的
// 一段区间(next-key/gap lock)。而 gap 锁之间互不排斥:N 个事务可以同时持有
// 同一段 gap 锁,随后各自的 INSERT 都要申请 insert intention 锁,与对方手里的
// gap 锁冲突 —— 结果是死锁,而不是排队。锁一行【已存在的主键行】没有这个问题。
//
// # 为什么这一行不放计数
//
// transfer 的 UserState 里存了日限额计数,是因为那些计数没有别的来源。工单这
// 几道闸门数的都是 qy_tickets / qy_ticket_attachments 里的真实行,把它们再抄
// 一份到这里就是第二份真相,迟早与业务表漂移(本仓已经为"同一概念的第 N 份
// 拷贝"翻过好几次车)。这里只要一把锁。
type UserState struct {
	UserId int `json:"-" gorm:"primaryKey"`
	// UpdatedAt 只用于事后排查"这个用户最早什么时候进过工单模块",不参与任何判定。
	UpdatedAt int64 `json:"-" gorm:"not null;default:0"`
}

func (UserState) TableName() string { return "qy_ticket_user_state" }

// lockUserState 取得该用户在工单模块的排他行锁,行不存在则先补一行。
//
// ⚠️ 调用约定:**它必须是所在事务的第一条语句**,前面不能有任何普通 SELECT。
//
// 这不是洁癖。MySQL 的 REPEATABLE READ 快照建立在事务里的【第一次一致性读】
// 那一刻;加锁读(FOR UPDATE)与写语句都不建立快照。所以:
//
//	先加锁再 COUNT  → 快照晚于加锁 → 数得到上一个持锁者刚提交的行 ✅
//	先 COUNT 再加锁 → 快照早于加锁 → 拿到锁也只能看见旧值,闸门照样被绕过 ❌
//
// 后一种写法在 MySQL 8.0.28 上实测复现过:A 事务持锁期间插入并提交,
// B 事务若在加锁前先做过一次普通 COUNT,加锁成功之后再 COUNT 读到的仍是 0。
// 因此在这几条事务里往闸门前面插入任何一次普通查询,都会静默地把闸门废掉;
// concurrency_test.go 里的并发用例与 TestCreateLimitsLocksBeforeItReads
// 正是为了守住这一点(后者不需要 MySQL,任何机器上都会拦下这种改动)。
//
// 补行用 OnConflict{DoNothing}:GORM 的 MySQL 驱动把它译成
// `ON DUPLICATE KEY UPDATE user_id=user_id`,撞上已存在的行时拿的是**排他**锁,
// 而不是 INSERT IGNORE 那种共享锁,因此不会出现"两个事务各持共享锁、
// 又都要升级成排他锁"的死锁。这一点与 transfer 的 reserveRisk 完全一致。
func lockUserState(tx *gorm.DB, userId int) error {
	now := common.GetTimestamp()
	seed := UserState{UserId: userId, UpdatedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		return err
	}
	var s UserState
	return db.LockForUpdate(tx).Where("user_id = ?", userId).Take(&s).Error
}
