package ticket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 空库下的列表响应绝不能出现 JSON null。
//
// 这条缺陷本项目已经在生产库副本上撞到过一次:提现审核页整页白屏,报
// `Cannot read properties of null (reading 'find')`,根因是一个
// `var rows []bucket` 在结果集为空时被原样序列化成 null。
// 触发条件只有一个 —— **一行都没有**,而那正是新站点第一次打开工单页的样子。
//
// qianye/json_array_guard_test.go 用 AST 锁住"别再写 var x []T",
// 但它看不见跨函数返回的切片。这里从 HTTP handler 进,直接量最终的 JSON。
func TestEmptyDatabaseNeverReturnsJSONNull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newEnv(t, "")

	call := func(t *testing.T, path string, handler gin.HandlerFunc, params gin.Params) string {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, path, nil)
		c.Params = params
		c.Set("id", 1)
		c.Set("username", "alice")
		handler(c)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec.Body.String()
	}

	t.Run("用户端工单列表", func(t *testing.T) {
		body := call(t, "/ticket/list", handleList, nil)
		assert.NotContains(t, body, `"items":null`)
		assertArray(t, body, "items")
	})

	t.Run("管理端工单队列", func(t *testing.T) {
		body := call(t, "/admin/ticket", handleAdminList, nil)
		assert.NotContains(t, body, `"items":null`)
		assertArray(t, body, "items")
	})

	t.Run("管理端角标", func(t *testing.T) {
		// 这一条是原缺陷的原样:库里一张单都没有时 Scan 不给切片赋值。
		body := call(t, "/admin/ticket/stats", handleAdminStats, nil)
		assert.NotContains(t, body, `"buckets":null`)
		assertArray(t, body, "buckets")
	})

	t.Run("用户端配置里的等级与图片类型", func(t *testing.T) {
		body := call(t, "/ticket/config", handleGetConfig, nil)
		assertArray(t, body, "priorities")
		assertArray(t, body, "image_accept")
	})
}

// assertArray 断言 data 下的某个字段是 JSON 数组(可以为空,但不能是 null)。
func assertArray(t *testing.T, body, field string) {
	t.Helper()
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &env))
	raw, ok := env.Data[field]
	require.True(t, ok, "响应里没有 %q 字段: %s", field, body)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(string(raw)), "["),
		"%q 必须是 JSON 数组,实际是 %s —— null 会让前端对着它调 .map 直接白屏",
		field, string(raw))
}

// 工单详情在没有任何附件时,每条消息的 attachments 也必须是 []。
func TestTicketDetailAttachmentsAreAlwaysArrays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newEnv(t, "")
	tk := seedTicket(t, gdb, "T1", nil)
	_, err := appendMessage(tk, replyInput{
		AuthorType: "user", AuthorId: 1, AuthorName: "alice", Body: "纯文字,不带图",
	}, nil)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// 用户端按业务单号寻址(路由是 /ticket/:no):用户视图刻意不下发自增 id,
	// 用它做路径参数等于要求前端拿一个恒为 0 的值去请求。
	c.Request = httptest.NewRequest(http.MethodGet, "/ticket/"+tk.TicketNo, nil)
	c.Params = gin.Params{{Key: "no", Value: tk.TicketNo}}
	c.Set("id", 1)
	handleGet(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), `"attachments":null`)
	assert.Contains(t, rec.Body.String(), `"attachments":[]`)
}
