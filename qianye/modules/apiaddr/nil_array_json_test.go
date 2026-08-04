package apiaddr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nil_array_json_test.go —— 空库下发的列表字段必须是 JSON `[]`,不能是 `null`。
//
// 与提现队列角标白屏(`Cannot read properties of null (reading 'find')`)同形:
// nil 切片被序列化成 `null`,前端对着它调 .map/.length 直接白屏。
// 触发条件只有一个 —— **结果集一行都没有**,而这恰恰是本功能最常见的初始状态:
// 站点刚升级、运营还没配任何 API 地址。密钥列表上的「复制链接信息」每次点开
// 都会读这个接口,空库一崩就是整页白屏。
//
// 先 seed 几行再断言 len 的测试永远碰不到这条路径(nil 切片的 len 也是 0),
// 所以这里必须跑在空库上。
func rawDataOf(t *testing.T, path string, h gin.HandlerFunc) map[string]json.RawMessage {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	c.Set("id", 1)
	h(c)

	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
	var body struct {
		Success bool                       `json:"success"`
		Data    map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success, "body=%s", res.Body.String())
	return body.Data
}

func TestUserListReturnsEmptyArrayOnEmptyDB(t *testing.T) {
	newTestDB(t)

	data := rawDataOf(t, "/api/qy/api-addresses", handleUserList)
	raw, ok := data["items"]
	require.True(t, ok, "响应里没有 items 字段,字段名改过了?")
	assert.Equal(t, "[]", string(raw),
		"一条地址都没配时必须下发空数组:前端据此静默回落到站点地址,"+
			"而 null 会让它在 .map 上崩掉整页")
}

func TestAdminListReturnsEmptyArrayOnEmptyDB(t *testing.T) {
	newTestDB(t)

	data := rawDataOf(t, "/api/qy/admin/api-addresses", adminList)
	raw, ok := data["items"]
	require.True(t, ok, "响应里没有 items 字段,字段名改过了?")
	assert.Equal(t, "[]", string(raw))
}

// 用户侧只下发已启用的地址,而且只下发白名单里的四个字段。
//
// # 为什么要断言"字段不在里面"
//
// Address 带着 created_by / updated_by(管理员的用户 id)与 enabled。
// 直接把管理端行下发给用户今天无害,但那个形状本身就是下一次泄漏的入口 ——
// 有人往 Address 上加一个内部字段时,它会自动出现在这个所有登录用户都能调的
// 接口里。userView 让新增字段默认不外发。
func TestUserListHidesDisabledRowsAndInternalFields(t *testing.T) {
	gdb := newTestDB(t)
	seedAddress(t, gdb, "主线路", "https://a.example.com", 10, true)
	seedAddress(t, gdb, "内部维护中", "https://b.example.com", 20, false)

	data := rawDataOf(t, "/api/qy/api-addresses", handleUserList)
	var items []map[string]json.RawMessage
	require.NoError(t, common.Unmarshal(data["items"], &items))
	require.Len(t, items, 1, "已停用的地址不该出现在用户侧")

	for _, hidden := range []string{"enabled", "created_by", "updated_by", "sort_order"} {
		_, present := items[0][hidden]
		assert.False(t, present, "用户侧不该下发 %q", hidden)
	}
	assert.Equal(t, `"https://a.example.com"`, string(items[0]["url"]))
}
