package paypass

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// handle 取出一个已绑定 ctx 的扩展库句柄。
//
// 全模块只有这一个取句柄的地方:db.Get() 返回 nil 与"忘了 WithContext"是本仓
// 反复出现的两个漏点,收成一处之后调用方只可能拿到已绑定的句柄。
func handle(ctx context.Context) (*gorm.DB, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	return gdb.WithContext(ctx), nil
}

// loadRow 读出一个用户的支付密码行。第二个返回值表示行是否存在。
//
// 行不存在不是错误:从未设置过支付密码的用户就是没有这一行,零值即正确答案。
func loadRow(ctx context.Context, gdb *gorm.DB, userId int) (PayPassword, bool, error) {
	var row PayPassword
	err := gdb.WithContext(ctx).Where("user_id = ?", userId).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PayPassword{UserId: userId}, false, nil
		}
		db.MarkFailure(err)
		return PayPassword{}, false, err
	}
	return row, true, nil
}

// claimFirstPassword 落下首次设置的支付密码,已存在时返回 false。
//
// # 为什么要 CAS 而不是"先查再写"
//
// 先 SELECT 判断"没设过"再 INSERT,两步之间存在窗口:同一用户并发提交两个不同的
// 密码时,两次都会通过判断,最后落库的是后到的那个,而用户以为生效的是先提交的
// 那个 —— 首次设置是一次性动作,弄错了就得走邮箱找回。
//
// 两步都是原子的:
//  1. INSERT ... DO NOTHING —— 行不存在时一次成功;并发时只有一个能拿到 1 行
//  2. UPDATE ... WHERE hash 为空串 —— 行存在但被管理员清空过(见 clearPasswordByAdmin),
//     同样只有一个并发者能把它从空改成非空
func claimFirstPassword(ctx context.Context, gdb *gorm.DB, userId int, hash string, now int64) (bool, error) {
	row := PayPassword{
		UserId: userId, Algo: algoBcrypt, Hash: hash,
		SetAt: now, ChangedAt: now, UpdatedAt: now,
	}
	res := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	if res.RowsAffected == 1 {
		return true, nil
	}

	res = gdb.WithContext(ctx).Model(&PayPassword{}).
		Where("user_id = ? AND hash = ?", userId, "").
		Updates(map[string]any{
			"algo": algoBcrypt, "hash": hash,
			"set_at": now, "changed_at": now, "updated_at": now,
			"fail_count": 0, "locked_until": 0,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// replacePassword 用新哈希替换旧哈希。oldHash 非空时作为 CAS 条件。
//
// CAS 的意义:改密流程是"验旧密 → 写新密"两步,中间用户可能在另一个设备上
// 又改了一次。带上旧哈希做条件,后到的那次会 RowsAffected = 0 而不是把
// 别的设备刚设好的密码悄悄盖掉。
//
// 一并清零 fail_count / locked_until:密码都换了,旧密码累积的错误次数
// 不该继续压着新密码 —— 否则用户刚通过邮箱找回,一登录就发现还是锁着的。
func replacePassword(ctx context.Context, gdb *gorm.DB, userId int, oldHash, newHash string, now int64) (bool, error) {
	q := gdb.WithContext(ctx).Model(&PayPassword{}).Where("user_id = ?", userId)
	if oldHash != "" {
		q = q.Where("hash = ?", oldHash)
	}
	res := q.Updates(map[string]any{
		"algo": algoBcrypt, "hash": newHash,
		"changed_at": now, "updated_at": now,
		"fail_count": 0, "locked_until": 0,
	})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	if res.RowsAffected == 1 {
		return true, nil
	}
	if oldHash != "" {
		return false, nil
	}
	// 无 CAS 条件却没改到行 —— 说明这个用户还没有行(例如管理员重置后
	// 用户直接走了邮箱找回)。补一次插入,让找回流程不必先去"设置"一遍。
	return claimFirstPassword(ctx, gdb, userId, newHash, now)
}

// clearPasswordByAdmin 清空支付密码,但**保留行**。
//
// # 为什么是清空而不是删行,更不是由管理员代设一个新密码
//
//   - 代设新密码意味着管理员在某个时刻知道用户的支付密码,这条路径一旦存在,
//     "支付密码只有本人知道"这个前提就不成立了,事后也无法自证不是管理员动的钱。
//     清空之后用户下一次划转会被拦下并被要求重新设置,管理员全程不接触密码。
//   - 保留行是为了保住 set_at 与 changed_at:审计要能回答"这个账号的支付密码
//     在什么时候被重置过几次"。删行会把这段历史一起删掉。
func clearPasswordByAdmin(ctx context.Context, gdb *gorm.DB, userId int, now int64) error {
	res := gdb.WithContext(ctx).Model(&PayPassword{}).
		Where("user_id = ?", userId).
		Updates(map[string]any{
			"algo": "", "hash": "",
			"fail_count": 0, "locked_until": 0,
			"changed_at": now, "updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	return nil
}

// unlockByAdmin 提前解除锁定,不动密码本身。
func unlockByAdmin(ctx context.Context, gdb *gorm.DB, userId int, now int64) error {
	res := gdb.WithContext(ctx).Model(&PayPassword{}).
		Where("user_id = ?", userId).
		Updates(map[string]any{
			"fail_count": 0, "locked_until": 0, "updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	return nil
}
