package channelops

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

// store.go —— 直接打在上游主库 channels / abilities 上的三条语句。
//
// 全部走 model.DB.WithContext(ctx):语句级预算只对接了 ctx 的语句生效,
// 漏接换来的不是"不会被取消",是"没有任何上界"(见 qianye/ctx_guard_test.go)。

// channelColumns 是本模块要用到的列。
//
// 显式列清单而不是 SELECT *:channels 里有 key(密钥明文)、setting、
// param_override 这些既敏感又大的列,而这里只需要判重复状态和取个名字。
// 少读一列就少一次把密钥带进内存/日志的机会。
//
// balance_updated_time 必须在列清单里:少读它的那一版把
// 「balance 已经是 0、但 balance_updated_time 还留着旧时间戳」判成了
// "本来就是干净的"(结构体零值恒等于库里的 0),于是页面上那句
// 「最近更新于 …:余额 0」永远清不掉 —— 而它正是重置余额要消灭的假话。
var channelColumns = []string{
	"id", "name", "status", "used_quota", "balance", "balance_updated_time",
}

// loadChannel 按 id 读一行。行不存在时返回 errChannelGone。
func loadChannel(ctx context.Context, id int) (*model.Channel, error) {
	var row model.Channel
	err := model.DB.WithContext(ctx).
		Select(channelColumns).
		Where("id = ?", id).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errChannelGone
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// deleteChannelWithAbilities 在**同一个事务**里删掉渠道行与它的全部 abilities 行。
//
// # 为什么不能调上游的 (*Channel).Delete()
//
// 那个方法是两条独立语句:先 `DB.Delete(channel)`,成功之后再
// `channel.DeleteAbilities()`。第二条失败时第一条已经提交 —— 物化路由表
// abilities 里就留下了一批 channel_id 指向已删渠道的行。上游的
// model.BatchDeleteChannels 反而是对的(它用了事务),但它把整批放进同一个
// 事务,做不到"部分成功"。
//
// 所以这里取两者各自正确的那一半:单条原子(事务)+ 批次可部分成功(每条一个事务)。
//
// RowsAffected == 0 说明读到写之间行被别人删掉了。报 not_found 而不是静默成功:
// 后者会让管理员以为是自己删掉的,而真正发生的是两个人同时在删。
func deleteChannelWithAbilities(ctx context.Context, id int) error {
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).Delete(&model.Channel{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errChannelGone
		}
		return tx.Where("channel_id = ?", id).Delete(&model.Ability{}).Error
	})
}

// resetOutcome 是一次重置在库里**真正**产生的效果。
//
// Changed 与两个金额分开:「勾了余额、余额本来就是 0、但时间戳还挂着」是
// Changed=true 而 Balance=0 的一档,拿金额去反推"有没有改动"会把它误判成跳过。
type resetOutcome struct {
	Changed bool
	// UsedQuota 是这一次抹掉的已用额度(清零前的值)。它是审计里
	// 「各自清掉多少」那一栏的唯一来源,必须与库里真正被覆盖掉的值逐字相等。
	UsedQuota int64
	// Balance 是这一次抹掉的上游余额展示值。
	Balance float64
}

// resetChannelCounters 在**一个事务 + 一把行锁**里读出清零前的值、按勾选项清零,
// 并返回这一次真正抹掉了多少。
//
// # 为什么必须在锁里重读,而不能用 runBatch 那次预读的值
//
// runBatch 先读一次行是为了拿渠道名与判存在。把那次读到的 used_quota 直接
// 当作"清掉了多少"写进审计,在两个管理员同时清同一批时会说假话:
//
//	A 读到 used_quota=290275000 → B 读到 290275000 → A 清零 → B 清零(库里已是 0)
//	→ 两条审计各自宣称抹掉了 2.9 亿,合计 5.8 亿,而实际只抹掉了 2.9 亿。
//
// 事后按审计做成本核算的减法会因此凭空多出一倍。锁内重读之后 B 读到的是 0,
// 它落的是 skipped、金额 0 —— 两条审计合起来仍然等于真正被抹掉的那一笔。
//
// FOR UPDATE 由 model.QyLockForUpdate 生成:MySQL/PostgreSQL 上是真锁,
// SQLite 上是空操作(方言不支持),而 SQLite 的写事务本身是全库互斥的,
// 三种数据库上"读到的值就是被覆盖掉的值"这条都成立。
//
// 行在读到之后消失(另一个管理员删了它)返回 errChannelGone,而不是静默成功:
// 后者会让管理员以为自己清掉了一笔,而那个渠道连同它的统计一起已经不在了。
func resetChannelCounters(ctx context.Context, id int, resetUsedQuota, resetBalance bool) (resetOutcome, error) {
	var out resetOutcome
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out = resetOutcome{}

		var row model.Channel
		err := model.QyLockForUpdate(tx).
			Select(channelColumns).
			Where("id = ?", id).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errChannelGone
		}
		if err != nil {
			return err
		}

		updates := make(map[string]any, 3)
		if resetUsedQuota && row.UsedQuota != 0 {
			updates["used_quota"] = 0
		}
		// 余额清零连 balance_updated_time 一起清:留着旧时间戳等于宣称
		// "刚刚查过,余额就是 0",而那是一句假话。时间戳单独残留时也要清,
		// 所以判据是两者之一非零,而不是只看金额。
		if resetBalance && (row.Balance != 0 || row.BalanceUpdatedTime != 0) {
			updates["balance"] = 0
			updates["balance_updated_time"] = 0
		}
		if len(updates) == 0 {
			return nil
		}

		res := tx.Model(&model.Channel{}).Where("id = ?", id).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errChannelGone
		}

		out.Changed = true
		if _, ok := updates["used_quota"]; ok {
			out.UsedQuota = row.UsedQuota
		}
		if _, ok := updates["balance"]; ok {
			out.Balance = row.Balance
		}
		return nil
	})
	if err != nil {
		return resetOutcome{}, err
	}
	return out, nil
}
