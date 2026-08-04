package ticket

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 自动关闭最容易变成的样子,是"把没人处理的工单悄悄抹掉"。
//
// 三条约束各有一个用例:
//   - 只碰 replied(在等用户),绝不碰 open / user_replied(在等客服);
//   - 按 last_reply_at 计时,不按 updated_at —— 后者会被改等级/改指派刷新;
//   - auto_close_days = 0 时整段不执行。
func TestAutoCloseStale(t *testing.T) {
	ctx := context.Background()

	statusOf := func(t *testing.T, gdb *gorm.DB, no string) string {
		t.Helper()
		var tk Ticket
		require.NoError(t, gdb.Where("ticket_no = ?", no).Take(&tk).Error)
		return tk.Status
	}

	t.Run("只关闭在等用户且已超期的工单", func(t *testing.T) {
		gdb := newEnv(t, "  auto_close_days: 7\n")
		now := common.GetTimestamp()
		stale := now - 8*86400
		seedTicket(t, gdb, "STALE", func(x *Ticket) { x.Status = StatusReplied; x.LastReplyAt = stale })
		seedTicket(t, gdb, "FRESH", func(x *Ticket) { x.Status = StatusReplied; x.LastReplyAt = now })
		// 这两张在等客服。放着不管是**客服的**问题,不是用户的 —— 自动关掉
		// 等于用一个后台任务把待办队列刷干净。
		seedTicket(t, gdb, "WAIT_OPEN", func(x *Ticket) { x.Status = StatusOpen; x.LastReplyAt = stale })
		seedTicket(t, gdb, "WAIT_USER", func(x *Ticket) { x.Status = StatusUserReplied; x.LastReplyAt = stale })

		autoCloseStale(ctx, gdb)

		assert.Equal(t, StatusClosed, statusOf(t, gdb, "STALE"))
		assert.Equal(t, StatusReplied, statusOf(t, gdb, "FRESH"))
		assert.Equal(t, StatusOpen, statusOf(t, gdb, "WAIT_OPEN"),
			"在等客服的单被自动关掉,等于把没人处理的工单悄悄抹掉")
		assert.Equal(t, StatusUserReplied, statusOf(t, gdb, "WAIT_USER"))

		var closed Ticket
		require.NoError(t, gdb.Where("ticket_no = ?", "STALE").Take(&closed).Error)
		assert.Equal(t, ClosedBySystem, closed.ClosedBy,
			"关单方必须记成 system:它没有 gin.Context,痕迹只有这一列")
		assert.NotZero(t, closed.ClosedAt, "关闭时刻是图片保留期的计时起点,不能为 0")
	})

	t.Run("按 last_reply_at 计时,不受改等级/改指派影响", func(t *testing.T) {
		gdb := newEnv(t, "  auto_close_days: 7\n")
		now := common.GetTimestamp()
		// updated_at 刚被刷新(比如客服刚调了一次等级),但用户确实 8 天没回话。
		seedTicket(t, gdb, "T1", func(x *Ticket) {
			x.Status = StatusReplied
			x.LastReplyAt = now - 8*86400
			x.UpdatedAt = now
		})

		autoCloseStale(ctx, gdb)
		assert.Equal(t, StatusClosed, statusOf(t, gdb, "T1"))
	})

	t.Run("auto_close_days = 0 时什么都不做", func(t *testing.T) {
		gdb := newEnv(t, "  auto_close_days: 0\n")
		seedTicket(t, gdb, "T1", func(x *Ticket) {
			x.Status = StatusReplied
			x.LastReplyAt = common.GetTimestamp() - 999*86400
		})

		autoCloseStale(ctx, gdb)
		assert.Equal(t, StatusReplied, statusOf(t, gdb, "T1"))
	})

	t.Run("last_reply_at 为 0 的行不参与判定", func(t *testing.T) {
		gdb := newEnv(t, "  auto_close_days: 7\n")
		seedTicket(t, gdb, "T1", func(x *Ticket) { x.Status = StatusReplied; x.LastReplyAt = 0 })

		autoCloseStale(ctx, gdb)
		assert.Equal(t, StatusReplied, statusOf(t, gdb, "T1"),
			"0 是「没有记录」而不是「1970 年」,按它算会把一批数据异常的单一次性关光")
	})
}
