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
var channelColumns = []string{"id", "name", "status", "used_quota", "balance"}

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

// resetChannelCounters 把指定的统计列清零,返回受影响行数。
//
// updates 由调用方按勾选项组装,这里不做业务判断 —— "该不该清 balance"
// 是 api_admin.go 的事,这一层只负责把它写进去。
func resetChannelCounters(ctx context.Context, id int, updates map[string]any) (int64, error) {
	res := model.DB.WithContext(ctx).
		Model(&model.Channel{}).
		Where("id = ?", id).
		Updates(updates)
	return res.RowsAffected, res.Error
}
