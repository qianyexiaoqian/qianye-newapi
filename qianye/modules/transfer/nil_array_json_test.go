package transfer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// nil_array_json_test.go —— 空库下发的列表字段必须是 JSON `[]`,不能是 `null`。
//
// 与提现队列角标白屏(`Cannot read properties of null (reading 'find')`)同形:
// nil 切片被 common.Marshal 写成 `null`,前端对着它调 .find/.map 直接白屏。
//
// 分组规则那一处的切片来自 listGroupRuleRows() 的**返回值**,
// qianye/json_array_guard_test.go 的静态锁只看得见同一个函数体内的声明,
// 看不见跨函数返回 —— 那条边界只能由这里的空库行为断言守住。
//
// 断言必须落在序列化后的 JSON 上:nil 切片的 len 也是 0,
// assert.Len(rows, 0) 对 nil 和空切片一视同仁地通过。

func newEmptyTransferEnv(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Order{}, &GroupRule{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: config.Transfer{Enabled: true},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

func readTransferData(t *testing.T, path string, h gin.HandlerFunc) map[string]json.RawMessage {
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

func assertTransferArrayEmpty(t *testing.T, data map[string]json.RawMessage, field string) {
	t.Helper()
	raw, ok := data[field]
	require.True(t, ok, "响应里没有 %q 字段,字段名改过了?", field)
	assert.Equal(t, "[]", string(raw),
		"%q 必须序列化成空数组;null 会让前端对着它调 .find/.map 时整页白屏", field)
}

// assertTransferIsArray 只要求"是 JSON 数组",用于取值不由扩展库决定的字段。
func assertTransferIsArray(t *testing.T, data map[string]json.RawMessage, field string) {
	t.Helper()
	raw, ok := data[field]
	require.True(t, ok, "响应里没有 %q 字段,字段名改过了?", field)
	require.NotEmpty(t, raw)
	assert.Equal(t, byte('['), raw[0],
		"%q 必须是 JSON 数组,实际是 %s", field, string(raw))
}

func TestTransferAdminListsReturnEmptyArrayOnEmptyDB(t *testing.T) {
	t.Run("划转流水", func(t *testing.T) {
		newEmptyTransferEnv(t)
		data := readTransferData(t, "/api/qy/admin/transfer/records", handleAdminListRecords)
		assertTransferArrayEmpty(t, data, "items")
	})

	t.Run("分组规则", func(t *testing.T) {
		newEmptyTransferEnv(t)
		data := readTransferData(t, "/api/qy/admin/transfer/group-rules", handleAdminListGroupRules)
		// items 是本轮改的那一处:规则表为空 → 必须正好是 []。
		assertTransferArrayEmpty(t, data, "items")
		assertTransferArrayEmpty(t, data, "unknown_groups")
		// known_groups / matrix 的取值域来自站点分组设置而不是规则表,
		// 空库下也可能非空,所以只钉住"它是数组、不是 null" ——
		// 前端的规则矩阵会同时消费这三个字段,任何一个是 null 都是整页白屏。
		assertTransferIsArray(t, data, "known_groups")
		assertTransferIsArray(t, data, "matrix")
		assertTransferIsArray(t, data, "group_options")
	})
}

// 有规则时 items 仍然带上那一行 —— 防止"把字段写死成 []"这种把测试变绿
// 却把功能改坏的修法。
func TestTransferGroupRulesStillListsExistingRow(t *testing.T) {
	gdb := newEmptyTransferEnv(t)
	require.NoError(t, gdb.Create(&GroupRule{
		FromGroup: "vip", Policy: GroupPolicyAllowList, ToGroups: "vip", Enabled: true,
	}).Error)

	data := readTransferData(t, "/api/qy/admin/transfer/group-rules", handleAdminListGroupRules)
	var items []GroupRule
	require.NoError(t, common.Unmarshal(data["items"], &items))
	require.Len(t, items, 1)
	assert.Equal(t, "vip", items[0].FromGroup)
}
