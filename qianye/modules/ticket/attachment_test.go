package ticket

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// bindAttachments 的四个 WHERE 条件是越权与重复认领的唯一防线。
//
// 判定必须是那一条带条件的 UPDATE,而不是"先查出来看看是不是本人的、是不是
// 还没用过、再更新" —— 先读后写在并发下必然出现同一张图被两条消息同时认领。
// 所以这里逐条验证:换个人不行、已绑过不行、已清理不行。
func TestBindAttachments(t *testing.T) {
	bind := func(t *testing.T, gdb *gorm.DB, tk *Ticket, authorId int, refs ...string) error {
		t.Helper()
		m := &Message{TicketId: tk.Id, AuthorId: authorId}
		require.NoError(t, gdb.Create(m).Error)
		return bindAttachments(gdb, tk, m, refs)
	}

	t.Run("本人的未绑定图片可以挂上去", func(t *testing.T) {
		gdb := newEnv(t, "")
		tk := seedTicket(t, gdb, "T1", nil)
		a := seedAttachment(t, gdb, "R1", func(x *Attachment) { x.UserId = 1 })

		require.NoError(t, bind(t, gdb, tk, 1, a.Ref))

		var reloaded Attachment
		require.NoError(t, gdb.Where("ref = ?", "R1").Take(&reloaded).Error)
		assert.Equal(t, tk.Id, reloaded.TicketId)
		assert.NotZero(t, reloaded.MessageId)
		assert.Equal(t, tk.TicketNo, reloaded.TicketNo)
	})

	t.Run("别人上传的图片不能被我认领", func(t *testing.T) {
		gdb := newEnv(t, "")
		tk := seedTicket(t, gdb, "T1", nil)
		seedAttachment(t, gdb, "R1", func(x *Attachment) { x.UserId = 2 })

		assert.ErrorIs(t, bind(t, gdb, tk, 1, "R1"), errImageNotFound)

		var reloaded Attachment
		require.NoError(t, gdb.Where("ref = ?", "R1").Take(&reloaded).Error)
		assert.Zero(t, reloaded.TicketId, "被拒绝的绑定不该改动那一行")
	})

	t.Run("同一张图不能被两条消息认领", func(t *testing.T) {
		gdb := newEnv(t, "")
		tk := seedTicket(t, gdb, "T1", nil)
		a := seedAttachment(t, gdb, "R1", nil)

		require.NoError(t, bind(t, gdb, tk, 1, a.Ref))
		assert.ErrorIs(t, bind(t, gdb, tk, 1, a.Ref), errImageNotFound)
	})

	t.Run("已清理的图片不能再被引用", func(t *testing.T) {
		gdb := newEnv(t, "")
		tk := seedTicket(t, gdb, "T1", nil)
		seedAttachment(t, gdb, "R1", func(x *Attachment) { x.PurgedAt = common.GetTimestamp() })

		assert.ErrorIs(t, bind(t, gdb, tk, 1, "R1"), errImageNotFound)
	})
}

// acceptRefs 是"单条消息附几张图"这道闸,以及图片总开关的第一道拦截。
func TestAcceptRefs(t *testing.T) {
	t.Run("超过单条上限即拒绝", func(t *testing.T) {
		loadTicketConfig(t, "  image_max_per_message: 2\n")
		_, err := acceptRefs([]string{"a", "b", "c"})
		assert.ErrorIs(t, err, errImageTooMany)
	})

	t.Run("重复的 ref 先去重再判上限", func(t *testing.T) {
		loadTicketConfig(t, "  image_max_per_message: 2\n")
		// 去重放在绑定之前,否则第二次绑定会因为条件不再成立而报"图片不存在" ——
		// 一个由客户端重复输入引起、却指向服务端状态的误导性错误。
		refs, err := acceptRefs([]string{"a", "a", "b"})
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, refs)
	})

	t.Run("站点关掉图片时带 ref 即拒绝", func(t *testing.T) {
		loadTicketConfig(t, "  image_enabled: false\n")
		_, err := acceptRefs([]string{"a"})
		assert.ErrorIs(t, err, errImageDisabled)
	})

	t.Run("站点关掉图片但不带 ref 时照常放行", func(t *testing.T) {
		loadTicketConfig(t, "  image_enabled: false\n")
		refs, err := acceptRefs(nil)
		require.NoError(t, err)
		assert.Empty(t, refs, "关掉图片不该影响纯文字工单")
	})
}

// 图片清理的两条到期口径,以及它们各自**不**该碰的东西。
//
// 这是本模块最容易写错的一段:把两条口径挂在同一个开关上,运维填 0 关掉保留期
// 的同时会打开一个磁盘泄漏;按上传时刻而不是关闭时刻计时,会在一张还在争议中
// 的工单上销毁物证。
func TestPruneAttachments(t *testing.T) {
	ctx := context.Background()
	now := common.GetTimestamp()

	t.Run("孤儿图片过窗口即清理", func(t *testing.T) {
		gdb := newEnv(t, "  image_retention_days: 0\n")
		a := seedAttachment(t, gdb, "R1", func(x *Attachment) {
			x.CreatedAt = now - attachmentOrphanSeconds - 10
		})
		fresh := seedAttachment(t, gdb, "R2", func(x *Attachment) { x.CreatedAt = now })

		pruneAttachments(ctx, gdb)

		assert.False(t, attachmentExistsOnDisk(t, a.StoredName), "过窗口的孤儿必须从磁盘上消失")
		assert.True(t, attachmentExistsOnDisk(t, fresh.StoredName), "刚上传的不该被碰")
		// 元数据行留着并标记 purged_at:它是"这条消息当时附过一张图"的证据。
		var reloaded Attachment
		require.NoError(t, gdb.Where("ref = ?", "R1").Take(&reloaded).Error)
		assert.NotZero(t, reloaded.PurgedAt)
	})

	t.Run("保留期只管已关闭的工单", func(t *testing.T) {
		gdb := newEnv(t, "  image_retention_days: 30\n")
		openTk := seedTicket(t, gdb, "T1", func(x *Ticket) { x.Status = StatusUserReplied })
		closedTk := seedTicket(t, gdb, "T2", func(x *Ticket) {
			x.Status = StatusClosed
			x.ClosedAt = now - 31*86400
		})
		live := seedAttachment(t, gdb, "R1", func(x *Attachment) {
			x.TicketId = openTk.Id
			x.CreatedAt = now - 400*86400 // 上传于一年多前,但工单还开着
		})
		stale := seedAttachment(t, gdb, "R2", func(x *Attachment) {
			x.TicketId = closedTk.Id
			x.CreatedAt = now - 31*86400
		})

		pruneAttachments(ctx, gdb)

		assert.True(t, attachmentExistsOnDisk(t, live.StoredName),
			"工单还开着就绝不能清图 —— 那是还在争议中的物证")
		assert.False(t, attachmentExistsOnDisk(t, stale.StoredName))
	})

	t.Run("关闭时间未到保留期的不清", func(t *testing.T) {
		gdb := newEnv(t, "  image_retention_days: 30\n")
		tk := seedTicket(t, gdb, "T1", func(x *Ticket) {
			x.Status = StatusClosed
			x.ClosedAt = now - 10*86400
		})
		a := seedAttachment(t, gdb, "R1", func(x *Attachment) {
			x.TicketId = tk.Id
			x.CreatedAt = now - 400*86400
		})

		pruneAttachments(ctx, gdb)
		assert.True(t, attachmentExistsOnDisk(t, a.StoredName),
			"计时起点是关闭时刻,不是上传时刻")
	})

	t.Run("retention_days = 0 关掉保留期,但孤儿仍要清", func(t *testing.T) {
		gdb := newEnv(t, "  image_retention_days: 0\n")
		tk := seedTicket(t, gdb, "T1", func(x *Ticket) {
			x.Status = StatusClosed
			x.ClosedAt = now - 999*86400
		})
		kept := seedAttachment(t, gdb, "R1", func(x *Attachment) { x.TicketId = tk.Id })
		orphan := seedAttachment(t, gdb, "R2", func(x *Attachment) {
			x.CreatedAt = now - attachmentOrphanSeconds - 10
		})

		pruneAttachments(ctx, gdb)

		assert.True(t, attachmentExistsOnDisk(t, kept.StoredName), "0 = 永久保留")
		assert.False(t, attachmentExistsOnDisk(t, orphan.StoredName),
			"孤儿不受保留期开关控制,否则填 0 就打开了一个磁盘泄漏")
	})

	t.Run("ctx 取消时立即停手", func(t *testing.T) {
		gdb := newEnv(t, "  image_retention_days: 30\n")
		a := seedAttachment(t, gdb, "R1", func(x *Attachment) {
			x.CreatedAt = now - attachmentOrphanSeconds - 10
		})
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()

		pruneAttachments(cancelled, gdb)
		assert.True(t, attachmentExistsOnDisk(t, a.StoredName))
	})
}
