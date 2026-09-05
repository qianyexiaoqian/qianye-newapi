package apiaddr

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// surface_filter_db_test.go —— 「展示位置」的服务端过滤契约。
//
// 过滤刻意放在服务端(handleUserList 的 surface 参数)而不是让每个前端消费方
// 自己滤:后者是同一条规则的 N 份拷贝,新增消费方忘了过滤时没有任何东西会红。
// 这组用例锁三件事:各位置只拿到自己的行 + 空名单兜底、不带参数 = 全量
// (旧版前端升级期间的行为),未知位置名直接 400 而不是静默放行。

func seedSurfacedAddress(t *testing.T, gdb *gorm.DB, name, addrURL string, order int, surfaces string) Address {
	t.Helper()
	row := seedAddress(t, gdb, name, addrURL, order, true)
	require.NoError(t, gdb.Model(&Address{}).Where("id = ?", row.Id).
		Update("surfaces", surfaces).Error)
	row.Surfaces = surfaces
	return row
}

// userListNamesOn 以指定 surface 参数调用用户侧列表,返回按展示顺序排好的名称。
func userListNamesOn(t *testing.T, rawQuery string) []string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/api-addresses"+rawQuery, nil)
	c.Set("id", 1)
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

func TestUserListFiltersBySurfaceWithUnboundFallback(t *testing.T) {
	gdb := newTestDB(t)
	seedSurfacedAddress(t, gdb, "全域", "https://all.example.com", 10, "")
	seedSurfacedAddress(t, gdb, "仅卡片", "https://console.example.com", 20, "console")
	seedSurfacedAddress(t, gdb, "仅复制", "https://picker.example.com", 30, "picker")
	seedSurfacedAddress(t, gdb, "双写", "https://both.example.com", 40, "console,picker")

	assert.Equal(t, []string{"全域", "仅卡片", "双写"},
		userListNamesOn(t, "?surface=console"),
		"控制台卡片 = 不限位置的行 + 点名了 console 的行")
	assert.Equal(t, []string{"全域", "仅复制", "双写"},
		userListNamesOn(t, "?surface=picker"),
		"复制链接 = 不限位置的行 + 点名了 picker 的行")
	assert.Equal(t, []string{"全域", "仅卡片", "仅复制", "双写"},
		userListNamesOn(t, ""),
		"不带参数 = 不做位置过滤 —— 升级期间的旧版前端看到全量,与从前一致")
	assert.Equal(t, []string{"全域", "仅卡片", "双写"},
		userListNamesOn(t, "?surface=Console"),
		"参数侧折叠大小写,与存储侧同一套口径")
}

func TestUserListRejectsUnknownSurface(t *testing.T) {
	newTestDB(t)

	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/qy/api-addresses?surface=sidebar", nil)
	c.Set("id", 1)
	handleUserList(c)

	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Equal(t, errSurfaceInvalid.Code, codeOf(t, res),
		"未知位置名多半是新前端的拼写错误,静默当成不过滤会把限定位置的地址下发到别处")
}
