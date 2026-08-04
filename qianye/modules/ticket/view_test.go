package ticket

import (
	"testing"

	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 内部备注是本模块唯一一条"写错就会把客服的内部判断发给被投诉的那个用户"
// 的路径,所以它有两道防线,两道都要有断言:
//
//	① loadUserMessages 的 WHERE internal = false —— 数据根本不出库
//	② toUserView 的 continue          —— 即使调用方传了全量也不下发
//
// 只测其中一道等于承认另一道可以被删掉。
func TestUserVisibility(t *testing.T) {
	gdb := newEnv(t, "")
	tk := seedTicket(t, gdb, "T1", func(x *Ticket) {
		x.AssigneeId = 9
		x.AssigneeName = "root"
	})

	_, err := appendMessage(tk, replyInput{
		AuthorType: qymodel.ActorUser, AuthorId: 1, AuthorName: "alice", Body: "我的问题",
	}, nil)
	require.NoError(t, err)
	_, err = appendMessage(tk, replyInput{
		AuthorType: qymodel.ActorAdmin, AuthorId: 9, AuthorName: "root-admin", Body: "已处理",
	}, nil)
	require.NoError(t, err)
	_, err = appendMessage(tk, replyInput{
		AuthorType: qymodel.ActorAdmin, AuthorId: 9, AuthorName: "root-admin",
		Body: "这个人上周投诉过一次", Internal: true,
	}, nil)
	require.NoError(t, err)

	t.Run("SQL 层就滤掉内部备注", func(t *testing.T) {
		visible, err := loadUserMessages(tk.Id)
		require.NoError(t, err)
		require.Len(t, visible, 2)
		for _, m := range visible {
			assert.False(t, m.Internal)
			assert.NotContains(t, m.Body, "投诉过")
		}
	})

	t.Run("视图层再滤一次,并抹掉管理员姓名与指派信息", func(t *testing.T) {
		all, err := loadAllMessages(tk.Id)
		require.NoError(t, err)
		require.Len(t, all, 3, "管理端能看到全部三条")

		// 刻意把**全量**消息喂进用户视图:第二道防线的意义就在于调用方传错时它仍然挡得住。
		v := toUserView(tk, all, nil)
		require.Len(t, v.Messages, 2)
		for _, m := range v.Messages {
			assert.False(t, m.Internal)
			if m.AuthorType == qymodel.ActorAdmin {
				assert.Empty(t, m.AuthorName,
					"下发管理员真名等于让任何用户发一张工单就能拿到一个管理员账号名")
			}
		}
		assert.Zero(t, v.AssigneeId, "指派是内部排班,用户不需要也不该知道")
		assert.Empty(t, v.AssigneeName)
		assert.Zero(t, v.UserId, "用户视图不回显自增 id 与用户名")
		assert.Empty(t, v.Username)
		assert.Equal(t, tk.TicketNo, v.TicketNo, "对外寻址一律用单号")
	})

	t.Run("管理端视图保留真名与内部备注", func(t *testing.T) {
		all, err := loadAllMessages(tk.Id)
		require.NoError(t, err)
		v := toAdminView(tk, all, nil)
		require.Len(t, v.Messages, 3)
		assert.Equal(t, "root", v.AssigneeName)
		found := false
		for _, m := range v.Messages {
			if m.Internal {
				found = true
				assert.Equal(t, "root-admin", m.AuthorName, "内部要能回答这句话是谁说的")
			}
		}
		assert.True(t, found)
	})
}

// 视图里的每一个数组字段都必须是空切片而不是 nil。
//
// nil 切片序列化成 JSON null,前端对着它调 .map / .find 会整页白屏 ——
// 触发条件只有一个:**结果集一行都没有**(新工单、没有附件的消息)。
// 判据与 qianye/json_array_guard_test.go 同源。
func TestViewArraysAreNeverNil(t *testing.T) {
	gdb := newEnv(t, "")
	tk := seedTicket(t, gdb, "T1", nil)

	for name, v := range map[string]*ticketView{
		"用户列表项":  toUserView(tk, nil, nil),
		"管理端列表项": toAdminView(tk, nil, nil),
	} {
		assert.NotNil(t, v.Messages, "%s 的 messages 不能是 nil", name)
	}

	m := Message{Id: 1, AuthorType: qymodel.ActorUser, Body: "没有附件"}
	mv := toMessageView(&m, nil)
	assert.NotNil(t, mv.Attachments, "没有附件的消息,attachments 必须是 []")
}
