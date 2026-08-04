package ticket

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// callAs 驱动一个 handler,身份与路径参数由调用方给定。
//
// 从 HTTP handler 进而不是直接调下层函数:本文件量的全是"前端拿着列表里的
// 那个值能不能取到详情""这个 ref 换个人请求会回什么",而这两件事的答案完全
// 取决于 handler 里的取参与鉴权顺序,绕过它就等于绕过被测对象。
func callAs(t *testing.T, userId int, method, path string, params gin.Params,
	body string, handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	c.Request = httptest.NewRequest(method, path, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", userId)
	c.Set("username", "u")
	handler(c)
	return rec
}

// bodyCode 取响应信封里的业务 code。
func bodyCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return env.Code
}

// TestUserTicketIsAddressableFromItsOwnList 是这一轮最贵那条缺陷的回归。
//
// 曾经的形状:列表走 toUserView,而 toUserView 把自增 id 抹成 0(刻意的 ——
// 用户端不下发主键);路由却收 :id。于是前端只能拿着 0 去请求,详情、追加回复、
// 关单三条路一起断,而管理端(保留真实 id)完全正常,自测极易漏掉。
// 更糟的是"未关闭工单数上限"默认 5,用户连自己关单的入口都没有,五张之后
// 彻底建不了新单。
//
// 因此这里量的是**闭环**:从列表拿到的那个对外标识,必须能原样取回详情。
// 断言不写成"id != 0"——那会把修复方向锁死成"把主键发出去",而正确的修法是
// 用户端一律按业务单号寻址。
func TestUserTicketIsAddressableFromItsOwnList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// 关掉回复冷却:这条用例量的是"能不能寻址",不是节流,而默认 10 秒会让
	// 同一轮里的追加回复被冷却拦下,把一次寻址失败伪装成节流生效。
	newEnv(t, `
  reply_cooldown_seconds: 0
`)

	rec := callAs(t, 7, http.MethodPost, "/ticket", nil,
		`{"title":"打不开控制台","body":"点进去一直转圈","priority":"normal"}`, handleCreate)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	list := callAs(t, 7, http.MethodGet, "/ticket/list", nil, "", handleList)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var page struct {
		Data struct {
			Items []struct {
				TicketNo string `json:"ticket_no"`
				Title    string `json:"title"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &page))
	require.Len(t, page.Data.Items, 1)
	no := page.Data.Items[0].TicketNo
	require.NotEmpty(t, no, "列表必须给出一个可寻址的对外标识")

	t.Run("详情", func(t *testing.T) {
		got := callAs(t, 7, http.MethodGet, "/ticket/"+no,
			gin.Params{{Key: "no", Value: no}}, "", handleGet)
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
		var env struct {
			Data struct {
				TicketNo string `json:"ticket_no"`
				Messages []struct {
					Body string `json:"body"`
				} `json:"messages"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(got.Body.Bytes(), &env))
		assert.Equal(t, no, env.Data.TicketNo)
		require.Len(t, env.Data.Messages, 1, "详情必须带出首条正文,否则用户看不到自己写了什么")
		assert.Equal(t, "点进去一直转圈", env.Data.Messages[0].Body)
	})

	t.Run("追加回复", func(t *testing.T) {
		got := callAs(t, 7, http.MethodPost, "/ticket/"+no+"/reply",
			gin.Params{{Key: "no", Value: no}},
			`{"body":"补充一句","attachment_refs":[]}`, handleReply)
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	})

	t.Run("关单", func(t *testing.T) {
		got := callAs(t, 7, http.MethodPost, "/ticket/"+no+"/close",
			gin.Params{{Key: "no", Value: no}}, "", handleClose)
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	})

	t.Run("别人的单号取不到", func(t *testing.T) {
		got := callAs(t, 8, http.MethodGet, "/ticket/"+no,
			gin.Params{{Key: "no", Value: no}}, "", handleGet)
		assert.Equal(t, http.StatusNotFound, got.Code)
		assert.Equal(t, "qy_tk_not_found", bodyCode(t, got))
	})
}

// TestUserImageAuthorization 量用户取图这条路上的三条判定。
//
// 图片本体走的是一条**独立**接口,不经过详情投影 —— 于是"详情里滤掉了内部
// 备注"这件事对它一点用都没有。三条各对应一个具体的坏结局。
func TestUserImageAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	get := func(t *testing.T, userId int, ref string) *httptest.ResponseRecorder {
		t.Helper()
		return callAs(t, userId, http.MethodGet, "/ticket/images/"+ref,
			gin.Params{{Key: "ref", Value: ref}}, "", handleGetImage)
	}

	// seedMessage 造一条消息并把一张图挂上去。
	seedBound := func(t *testing.T, gdb *gorm.DB, tk *Ticket, ref string, internal bool) {
		t.Helper()
		m := &Message{
			TicketId: tk.Id, TicketNo: tk.TicketNo, AuthorType: qymodel.ActorAdmin,
			AuthorId: 99, Internal: internal, Body: "x", CreatedAt: common.GetTimestamp(),
		}
		require.NoError(t, gdb.Create(m).Error)
		seedAttachment(t, gdb, ref, func(a *Attachment) {
			a.UserId = 99
			a.TicketId = tk.Id
			a.MessageId = m.Id
			a.TicketNo = tk.TicketNo
		})
	}

	t.Run("内部备注的附图不下发给工单所属用户", func(t *testing.T) {
		gdb := newEnv(t, "")
		tk := seedTicket(t, gdb, "TK-A", func(x *Ticket) { x.UserId = 7 })
		seedBound(t, gdb, tk, "R-note", true)
		seedBound(t, gdb, tk, "R-reply", false)

		// 对外回复的截图当然要看得见 —— 收紧过头会让客服贴的图变成破图。
		assert.Equal(t, http.StatusOK, get(t, 7, "R-reply").Code)

		// 内部备注的正文在 SQL 层被滤掉了,附图必须走同一条判定:客服贴在
		// 备注里的可能是风控画像或别人的对账截图,ref 一旦从旁路泄漏
		// (误粘贴、日志、审计导出)就是直接 200 拿到本体。
		rec := get(t, 7, "R-note")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "qy_tk_image_not_found", bodyCode(t, rec))
	})

	t.Run("已清理的图片不向陌生人确认它存在过", func(t *testing.T) {
		gdb := newEnv(t, "")
		tk := seedTicket(t, gdb, "TK-B", func(x *Ticket) { x.UserId = 7 })
		seedBound(t, gdb, tk, "R-purged", false)
		require.NoError(t, gdb.Model(&Attachment{}).Where("ref = ?", "R-purged").
			Update("purged_at", common.GetTimestamp()).Error)

		// 攻击者拿到的必须与"这个 ref 从来不存在"完全一样,否则两者可区分 =
		// 存在性预言机,而本函数的注释正是承诺不给这条信息。
		stranger := get(t, 8, "R-purged")
		missing := get(t, 8, "R-never-existed")
		assert.Equal(t, missing.Code, stranger.Code)
		assert.Equal(t, bodyCode(t, missing), bodyCode(t, stranger))

		// 工单所属用户仍然要收到 410:"传过但按保留期清理了"与"从来没有这张"
		// 对他是两件事,合并会让他以为自己当初根本没传。
		owner := get(t, 7, "R-purged")
		assert.Equal(t, http.StatusGone, owner.Code)
		assert.Equal(t, "qy_tk_image_purged", bodyCode(t, owner))
	})
}

// uploadImage 走真实的 multipart 请求驱动上传 handler。
func uploadImage(t *testing.T, userId int) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", "shot.png")
	require.NoError(t, err)
	_, err = fw.Write(pngBytes)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/ticket/images", body)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	c.Set("id", userId)
	c.Set("username", "u")
	handleUploadImage(c)
	return rec
}

// TestImageUserQuotaCountsBytesAlreadyInsideTickets 量单人磁盘总量闸。
//
// pendingUploadMax 只管"未绑定"那一档:图片一旦随消息提交,那个计数立刻归零。
// 而自动关闭只作用于 replied —— 一个每次收到客服回复就再补一句的账号可以让
// 自己的工单永远不进入可清理状态,它的图片因此永远不会被保留期扫到。
// 于是没有这道闸时,单个账号可以在宿主机上长期钉住数 GiB 且没有任何后台任务
// 能回收它们,而磁盘写满会拖垮整个进程。
func TestImageUserQuotaCountsBytesAlreadyInsideTickets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newEnv(t, `
  image_max_bytes: 1024
  image_user_quota_bytes: 1024
`)

	// 已经随消息提交的图片(ticket_id > 0):pending 计数看不到它,配额必须看到。
	tk := seedTicket(t, gdb, "TK-Q", func(x *Ticket) { x.UserId = 7 })
	seedAttachment(t, gdb, "R-bound", func(a *Attachment) {
		a.UserId = 7
		a.TicketId = tk.Id
		a.MessageId = 1
		a.Size = 1024
	})

	rec := uploadImage(t, 7)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "qy_tk_image_quota", bodyCode(t, rec))

	t.Run("配额只算自己的", func(t *testing.T) {
		other := uploadImage(t, 8)
		assert.Equal(t, http.StatusOK, other.Code, other.Body.String())
	})

	t.Run("已清理的字节不再占配额", func(t *testing.T) {
		require.NoError(t, gdb.Model(&Attachment{}).Where("ref = ?", "R-bound").
			Update("purged_at", common.GetTimestamp()).Error)
		again := uploadImage(t, 7)
		assert.Equal(t, http.StatusOK, again.Code, again.Body.String())
	})
}

// TestDiscardPendingUpload 量"移除一张还没提交的图"这条出口。
//
// 没有它,前端移除只删本地那一项,服务端那条未绑定的行会一直占着
// pendingUploadMax 的名额:两次"选了又不发"之后,用户在 24 小时孤儿清理到期
// 之前再也传不了图,而收到的提示是"请先完成当前这条消息"——
// 一个他根本无法执行的下一步。
func TestDiscardPendingUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("丢弃自己的未提交图片,行与文件一起消失", func(t *testing.T) {
		gdb := newEnv(t, "")
		a := seedAttachment(t, gdb, "R1", func(x *Attachment) { x.UserId = 7 })

		require.NoError(t, discardPendingUpload(7, "R1"))

		var cnt int64
		require.NoError(t, gdb.Model(&Attachment{}).Where("ref = ?", "R1").Count(&cnt).Error)
		assert.Zero(t, cnt, "名额必须真的还回去,只删文件不删行等于什么都没做")
		assert.False(t, attachmentExistsOnDisk(t, a.StoredName))
	})

	t.Run("别人的图片丢不掉", func(t *testing.T) {
		gdb := newEnv(t, "")
		seedAttachment(t, gdb, "R1", func(x *Attachment) { x.UserId = 9 })
		assert.ErrorIs(t, discardPendingUpload(7, "R1"), errImageNotFound)
	})

	t.Run("已经进了对话的图片丢不掉", func(t *testing.T) {
		gdb := newEnv(t, "")
		tk := seedTicket(t, gdb, "TK-D", func(x *Ticket) { x.UserId = 7 })
		a := seedAttachment(t, gdb, "R1", func(x *Attachment) {
			x.UserId = 7
			x.TicketId = tk.Id
			x.MessageId = 1
		})

		// 消息是 append-only 的,它的附图同样是"当时到底附了什么"的一部分。
		assert.ErrorIs(t, discardPendingUpload(7, "R1"), errImageNotFound)
		assert.True(t, attachmentExistsOnDisk(t, a.StoredName))
	})
}

// TestAdminAssignRejectsNonPositiveAssignee 量指派入参的下界。
//
// 负数原样写库的结果:assignee_id 非 0(前端显示"已指派"、负责人名为空),
// 而队列筛选只认 assignee_id > 0 —— 谁都搜不到它,也没人认为它还没人认领。
// 这正是"一张被静默丢弃的工单",只是走的是负数而不是普通用户 id 那条路,
// 脚本传错符号即可触发,不需要恶意。
func TestAdminAssignRejectsNonPositiveAssignee(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newEnv(t, "")
	tk := seedTicket(t, gdb, "TK-N", nil)

	rec := callAs(t, 1, http.MethodPost, "/admin/ticket/1/assign",
		gin.Params{{Key: "id", Value: "1"}}, `{"assignee_id":-1}`, handleAdminAssign)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "qy_tk_assignee_invalid", bodyCode(t, rec))

	var reloaded Ticket
	require.NoError(t, gdb.Where("id = ?", tk.Id).Take(&reloaded).Error)
	assert.Zero(t, reloaded.AssigneeId, "被拒绝的指派一个字段都不该写进去")
}
