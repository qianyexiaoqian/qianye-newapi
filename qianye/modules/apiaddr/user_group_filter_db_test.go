package apiaddr

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// user_group_filter_db_test.go —— 「按用户分组显示、默认地址兜底」的端到端契约。
//
// 这组不变量不在纯函数里:visibleToUserGroup 单测全绿的情况下,handler 忘了调它、
// 或 updateAddress 的列清单漏了 user_groups(表现为管理端保存分组"成功"但库里
// 纹丝不动),用户看到的仍是错的。所以必须打到 handler + 真库。

// seedGroupedAddress 落一行绑定了适用分组的已启用地址。
//
// 不改 seedAddress 的签名:既有测试全部走"未绑定"形态,那恰好就是升级前
// 存量行的形态 —— 保持原样本身就是一层回归覆盖。
func seedGroupedAddress(t *testing.T, gdb *gorm.DB, name, addrURL string, order int, userGroups string) Address {
	t.Helper()
	row := seedAddress(t, gdb, name, addrURL, order, true)
	require.NoError(t, gdb.Model(&Address{}).Where("id = ?", row.Id).
		Update("user_groups", userGroups).Error)
	row.UserGroups = userGroups
	return row
}

// userListNamesFor 以指定用户分组调用用户侧列表,返回按展示顺序排好的名称。
//
// c.Set("group", …) 模拟的是 UserAuth 的 setDashboardAuthContext 写入 ——
// 生产里这个值必已存在,测试里空串则对应"历史行分组为空串"的账号。
func userListNamesFor(t *testing.T, group string) []string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/api-addresses", nil)
	c.Set("id", 1)
	c.Set("group", group)
	handleUserList(c)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Items []userView `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success, "body=%s", res.Body.String())
	names := make([]string, 0, len(body.Data.Items))
	for _, item := range body.Data.Items {
		names = append(names, item.Name)
	}
	return names
}

// 分组可见性的完整语义在同一份数据上过一遍:专属线路只给绑定的分组,
// 未绑定的行对所有分组可见 —— 它就是没配专属线路的分组的默认兜底。
func TestUserListFiltersByUserGroupWithUnboundFallback(t *testing.T) {
	gdb := newTestDB(t)
	seedGroupedAddress(t, gdb, "主线路", "https://main.example.com", 10, "")
	seedGroupedAddress(t, gdb, "VIP 专线", "https://vip.example.com", 20, "vip,svip")
	seedGroupedAddress(t, gdb, "内测线", "https://beta.example.com", 30, "beta")

	assert.Equal(t, []string{"主线路", "VIP 专线"}, userListNamesFor(t, "vip"),
		"vip 用户 = 默认线路 + 自己分组的专属线路")
	assert.Equal(t, []string{"主线路"}, userListNamesFor(t, "default"),
		"default 分组没配专属线路,只看到默认兜底")
	assert.Equal(t, []string{"主线路"}, userListNamesFor(t, "enterprise"),
		"从没配过的分组同样落到默认兜底,而不是空列表或报错")
	assert.Equal(t, []string{"主线路", "VIP 专线"}, userListNamesFor(t, " VIP "),
		"用户分组带空白/大小写差异也必须命中 —— 判定侧与写入侧同一套折叠")
	assert.Equal(t, []string{"主线路"}, userListNamesFor(t, ""),
		"users.group 为空串的历史账号按 default 算,不该看到别的分组的专线")
}

// 某个分组被过滤到一条不剩时,items 仍是 `[]` 而不是 null ——
// 前端据此静默回落站点地址(nil_array_json_test.go 锁的是空库,这里锁的是
// "有数据但全被滤掉"的新路径:make 必须发生在过滤之前)。
func TestUserListFilteredToNothingStillReturnsEmptyArray(t *testing.T) {
	gdb := newTestDB(t)
	seedGroupedAddress(t, gdb, "VIP 专线", "https://vip.example.com", 10, "vip")

	data := rawDataOf(t, "/api/qy/api-addresses", handleUserList)
	raw, ok := data["items"]
	require.True(t, ok, "响应里没有 items 字段,字段名改过了?")
	assert.Equal(t, "[]", string(raw),
		"default 用户看不到 vip 专线时必须下发空数组,null 会让前端 .map 白屏")
}

// 新建时提交的适用分组要归一化后入库:大小写折叠、去空白、去重。
// 折叠必须发生在入库前 —— 存了 "VIP" 判定侧永远比不中。
func TestAdminCreatePersistsNormalizedUserGroups(t *testing.T) {
	gdb := newTestDB(t)

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses",
		`{"name":"VIP 专线","url":"https://vip.example.com","user_groups":" VIP, svip ,vip"}`,
		nil, adminCreate)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	var row Address
	require.NoError(t, gdb.Take(&row).Error)
	assert.Equal(t, "vip,svip", row.UserGroups)
}

// 编辑必须真的把 user_groups 写进库。
//
// # 这条断言守的是什么
//
// updateAddress 用的是显式列清单(为了不让编辑覆盖排序),新列漏在清单外时
// 的表现是:响应体里带着新分组、审计里也带着,唯独库里没变 —— 管理端怎么看
// 都是"改成功了",用户侧却永远按旧名单过滤。
func TestAdminUpdateWritesUserGroupsColumn(t *testing.T) {
	gdb := newTestDB(t)
	row := seedGroupedAddress(t, gdb, "VIP 专线", "https://vip.example.com", 10, "vip")

	res := call(t, http.MethodPut, "/api/qy/admin/api-addresses/1",
		`{"name":"VIP 专线","url":"https://vip.example.com","user_groups":"svip"}`,
		idParams(strconv.Itoa(row.Id)), adminUpdate)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	var after Address
	require.NoError(t, gdb.Where("id = ?", row.Id).Take(&after).Error)
	assert.Equal(t, "svip", after.UserGroups)

	// 编辑时不带 user_groups = 清空回"所有分组可见"。这与 remark 的缺省语义
	// 一致(整行提交),断言钉住它,免得有人把它当成"缺省保持原样"来依赖。
	res = call(t, http.MethodPut, "/api/qy/admin/api-addresses/1",
		`{"name":"VIP 专线","url":"https://vip.example.com"}`,
		idParams(strconv.Itoa(row.Id)), adminUpdate)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
	require.NoError(t, gdb.Where("id = ?", row.Id).Take(&after).Error)
	assert.Equal(t, "", after.UserGroups)
}

// 非法分组名挡在纯输入校验层:400 + 不写审计(那是打字过程,不是碰库的失败)。
func TestAdminCreateRejectsInvalidUserGroupWithoutAudit(t *testing.T) {
	gdb := newTestDB(t)

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses",
		`{"name":"VIP 专线","url":"https://vip.example.com","user_groups":"vip 2"}`,
		nil, adminCreate)
	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Equal(t, errGroupInvalid.Code, codeOf(t, res))
	assert.Empty(t, auditActions(t, gdb), "输入框里的打字过程不该进审计")
}
