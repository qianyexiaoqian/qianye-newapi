package ticket

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 四道防滥用闸门。
//
// 这个文件存在的理由是 config/selfcheck.go 里记着的 C1:withdraw 的四项风控限额
// 曾经定义齐全、校验齐全、写进示例 YAML,而 create 流程一条都没查 —— 运维改完
// 配置、重启、日志一切正常,闸门却是空的。工单的四道闸是同一个形状,
// 所以每一道都要有"配了就真的拦"和"配 0 就真的不拦"两条断言。
//
// 后一条同样重要:注释向运维承诺了"0 = 不限制",而实现里少写一个 `> 0` 判断
// 的表现是填 0 的站点一张单都建不出来。

func TestCheckCreateLimits(t *testing.T) {
	t.Run("冷却:两次建单间隔不足即拒绝", func(t *testing.T) {
		gdb := newEnv(t, "  cooldown_seconds: 60\n  max_open_per_user: 0\n  daily_max_count: 0\n")
		now := common.GetTimestamp()
		seedTicket(t, gdb, "T1", func(x *Ticket) { x.CreatedAt = now - 30 })

		assert.ErrorIs(t, checkCreateLimits(gdb, 1, now), errCooldown)
		// 满了 60 秒就该放行。冷却是"等一会儿",不是"今天别来了"。
		assert.NoError(t, checkCreateLimits(gdb, 1, now+31))
	})

	t.Run("冷却:0 表示不限制", func(t *testing.T) {
		gdb := newEnv(t, "  cooldown_seconds: 0\n  max_open_per_user: 0\n  daily_max_count: 0\n")
		now := common.GetTimestamp()
		seedTicket(t, gdb, "T1", func(x *Ticket) { x.CreatedAt = now })
		assert.NoError(t, checkCreateLimits(gdb, 1, now))
	})

	t.Run("冷却只看本人的上一张单", func(t *testing.T) {
		gdb := newEnv(t, "  cooldown_seconds: 60\n  max_open_per_user: 0\n  daily_max_count: 0\n")
		now := common.GetTimestamp()
		seedTicket(t, gdb, "T1", func(x *Ticket) { x.UserId = 2; x.CreatedAt = now })
		assert.NoError(t, checkCreateLimits(gdb, 1, now),
			"别人刚建了一张单,不该把我拦住")
	})

	t.Run("未关闭数:只数未关闭的,已关闭的不占额度", func(t *testing.T) {
		gdb := newEnv(t, "  cooldown_seconds: 0\n  max_open_per_user: 2\n  daily_max_count: 0\n")
		now := common.GetTimestamp()
		seedTicket(t, gdb, "T1", func(x *Ticket) { x.Status = StatusOpen })
		seedTicket(t, gdb, "T2", func(x *Ticket) { x.Status = StatusReplied })
		assert.ErrorIs(t, checkCreateLimits(gdb, 1, now), errOpenLimit)

		// 关掉一张就该重新放行 —— 这正是这道闸的出口,没有它用户会被永久锁死。
		require.NoError(t, gdb.Model(&Ticket{}).Where("ticket_no = ?", "T1").
			Updates(map[string]any{"status": StatusClosed, "closed_at": now}).Error)
		assert.NoError(t, checkCreateLimits(gdb, 1, now))
	})

	t.Run("未关闭数:user_replied 同样计入", func(t *testing.T) {
		gdb := newEnv(t, "  cooldown_seconds: 0\n  max_open_per_user: 1\n  daily_max_count: 0\n")
		seedTicket(t, gdb, "T1", func(x *Ticket) { x.Status = StatusUserReplied })
		assert.ErrorIs(t, checkCreateLimits(gdb, 1, common.GetTimestamp()), errOpenLimit)
	})

	t.Run("未关闭数:0 表示不限制", func(t *testing.T) {
		gdb := newEnv(t, "  cooldown_seconds: 0\n  max_open_per_user: 0\n  daily_max_count: 0\n")
		for _, no := range []string{"T1", "T2", "T3", "T4", "T5", "T6"} {
			seedTicket(t, gdb, no, nil)
		}
		assert.NoError(t, checkCreateLimits(gdb, 1, common.GetTimestamp()))
	})

	t.Run("日限:按最近 24 小时滑动窗口,窗口外的不计入", func(t *testing.T) {
		gdb := newEnv(t, "  cooldown_seconds: 0\n  max_open_per_user: 0\n  daily_max_count: 2\n")
		now := common.GetTimestamp()
		// 窗口内两张(已关闭也算:日限管的是"提交了多少",不是"挂着多少")。
		seedTicket(t, gdb, "T1", func(x *Ticket) { x.CreatedAt = now - 100; x.Status = StatusClosed })
		seedTicket(t, gdb, "T2", func(x *Ticket) { x.CreatedAt = now - 200 })
		assert.ErrorIs(t, checkCreateLimits(gdb, 1, now), errDailyLimit)

		// 把其中一张推到 25 小时前,窗口就该重新有位置。
		require.NoError(t, gdb.Model(&Ticket{}).Where("ticket_no = ?", "T1").
			Update("created_at", now-25*3600).Error)
		assert.NoError(t, checkCreateLimits(gdb, 1, now))
	})

	t.Run("日限:0 表示不限制", func(t *testing.T) {
		gdb := newEnv(t, "  cooldown_seconds: 0\n  max_open_per_user: 0\n  daily_max_count: 0\n")
		now := common.GetTimestamp()
		for _, no := range []string{"T1", "T2", "T3"} {
			seedTicket(t, gdb, no, func(x *Ticket) { x.CreatedAt = now })
		}
		assert.NoError(t, checkCreateLimits(gdb, 1, now))
	})
}

// 消息条数上限是"详情接口还打得开"这条底线,越界一次就再也回不去
// (它是 append-only 表,删不掉)。这里验证它真的在事务里拦,而不是只写在配置里。
func TestAppendMessage_MessageLimit(t *testing.T) {
	gdb := newEnv(t, "  max_messages_per_ticket: 2\n")
	tk := seedTicket(t, gdb, "T1", nil)

	in := replyInput{AuthorType: "user", AuthorId: 1, AuthorName: "alice", Body: "第一条"}
	_, err := appendMessage(tk, in, nil)
	require.NoError(t, err)
	_, err = appendMessage(tk, in, nil)
	require.NoError(t, err)

	_, err = appendMessage(tk, in, nil)
	assert.ErrorIs(t, err, errMessageLimit)

	var cnt int64
	require.NoError(t, db.Get().Model(&Message{}).Where("ticket_id = ?", tk.Id).Count(&cnt).Error)
	assert.EqualValues(t, 2, cnt, "被上限拦下的那条不该留下任何行")
}

// 内部备注不能刷新 last_reply_*。
//
// 那两列是自动关闭与客服队列排序的唯一依据:让一条用户看不见的备注去刷新它们,
// 等于把一张真的没人管的单伪装成刚刚处理过 —— 它会沉到队列底部,
// 而自动关闭的计时也跟着被推迟。
func TestAppendMessage_InternalNoteDoesNotTouchLastReply(t *testing.T) {
	gdb := newEnv(t, "")
	before := common.GetTimestamp() - 3600
	tk := seedTicket(t, gdb, "T1", func(x *Ticket) {
		x.Status = StatusReplied
		x.LastReplyAt = before
		x.LastReplyBy = "admin"
	})

	_, err := appendMessage(tk, replyInput{
		AuthorType: "admin", AuthorId: 9, AuthorName: "root",
		Body: "这个人上周投诉过一次", Internal: true,
	}, nil)
	require.NoError(t, err)

	var reloaded Ticket
	require.NoError(t, gdb.Where("id = ?", tk.Id).Take(&reloaded).Error)
	assert.Equal(t, before, reloaded.LastReplyAt, "内部备注不该刷新最后回复时间")
	assert.Equal(t, StatusReplied, reloaded.Status, "内部备注不该改变状态")
	assert.Equal(t, 1, reloaded.MessageCount, "但它确实占一条消息额度")
}

// 首响时刻只写一次。它是"首次人工响应有多快"的原始数据,被后续回复覆盖之后
// 这个指标就永远算不出来了。
func TestAppendMessage_FirstRepliedAtIsWrittenOnce(t *testing.T) {
	gdb := newEnv(t, "")
	tk := seedTicket(t, gdb, "T1", nil)

	_, err := appendMessage(tk, replyInput{
		AuthorType: "admin", AuthorId: 9, AuthorName: "root", Body: "第一次回复",
	}, nil)
	require.NoError(t, err)
	first := tk.FirstRepliedAt
	require.NotZero(t, first)

	// 用户追加一句,客服再回一次。
	_, err = appendMessage(tk, replyInput{
		AuthorType: "user", AuthorId: 1, AuthorName: "alice", Body: "还有一个问题",
	}, nil)
	require.NoError(t, err)
	_, err = appendMessage(tk, replyInput{
		AuthorType: "admin", AuthorId: 9, AuthorName: "root", Body: "第二次回复",
	}, nil)
	require.NoError(t, err)

	var reloaded Ticket
	require.NoError(t, gdb.Where("id = ?", tk.Id).Take(&reloaded).Error)
	assert.Equal(t, first, reloaded.FirstRepliedAt)
}

// 关单是幂等的:两个标签页各点一次是常态,第二次报"状态冲突"只会让用户
// 以为出了问题。重开则严格 —— 只有已关闭的单能被重开。
func TestCloseAndReopen(t *testing.T) {
	gdb := newEnv(t, "")
	tk := seedTicket(t, gdb, "T1", nil)

	require.NoError(t, closeTicket(tk, ClosedByUser))
	assert.Equal(t, StatusClosed, tk.Status)
	assert.NotZero(t, tk.ClosedAt)
	assert.NoError(t, closeTicket(tk, ClosedByUser), "重复关闭必须幂等")

	require.NoError(t, reopenTicket(tk))
	assert.Equal(t, StatusOpen, tk.Status)
	assert.Zero(t, tk.ClosedAt, "重开必须把关闭时刻清掉,否则图片保留期会按一个陈旧的时刻计时")
	assert.Empty(t, tk.ClosedBy)

	assert.ErrorIs(t, reopenTicket(tk), errIllegalTransition,
		"重开一张本来就开着的单没有意义,不该悄悄成功")
}
