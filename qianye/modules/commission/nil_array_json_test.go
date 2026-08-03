package commission

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nil_array_json_test.go —— 空库下发的列表字段必须是 JSON `[]`,不能是 `null`。
//
// 与提现队列角标白屏(`Cannot read properties of null (reading 'find')`)同形:
// nil 切片被 common.Marshal 写成 `null`,前端对着它调 .find/.map 直接白屏。
// 触发条件只有一个 —— **结果集一行都没有**,所以这里必须跑在空库上:
// 先 seed 几行再断言 len 的测试永远碰不到这条路径(nil 切片的 len 也是 0)。

// 健康位复用 api_admin_config_test.go 里那一份 linkname:同一个包里对同一个
// 符号声明两次会在链接期报 "symbol redeclared"。
func useHealthyExtDB(t *testing.T) {
	t.Helper()
	prev := qyDBHealthy.Swap(true)
	t.Cleanup(func() { qyDBHealthy.Store(prev) })
}

func readCommissionData(t *testing.T, path string, h gin.HandlerFunc) map[string]json.RawMessage {
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

// 佣金流水管理端在空库下的 items 必须是 []。
//
// 这一处走 GORM Find,当前版本会替我们 MakeSlice 出非 nil 切片,
// 单独回滚 make 这条测试**不会**变红;显式 make 的意义是不把 JSON 契约
// 挂在 GORM 的内部实现细节上,守它的是 qianye/json_array_guard_test.go。
func TestCommissionAdminRecordsReturnEmptyArrayOnEmptyDB(t *testing.T) {
	newTestDB(t)
	useHealthyExtDB(t)
	useConfig(t, &config.Config{
		Enabled:    true,
		Commission: config.Commission{Enabled: true},
	})

	data := readCommissionData(t, "/api/qy/admin/commission/records", adminListRecords)
	raw, ok := data["items"]
	require.True(t, ok, "响应里没有 items 字段,字段名改过了?")
	assert.Equal(t, "[]", string(raw),
		"items 必须序列化成空数组;null 会让前端对着它调 .find/.map 时整页白屏")
}

// 有流水时 items 仍然带上那一行 —— 防止"把字段写死成 []"这种把测试变绿
// 却把功能改坏的修法。
func TestCommissionAdminRecordsStillListsExistingRow(t *testing.T) {
	gdb := newTestDB(t)
	useHealthyExtDB(t)
	useConfig(t, &config.Config{
		Enabled:    true,
		Commission: config.Commission{Enabled: true},
	})
	require.NoError(t, gdb.Create(&Accrual{
		AccrualNo: "AC-1", InviterId: 1, InviteeId: 2,
		SourceType: SourceTopup, SourceRef: "TP-1",
		BaseQuota: 1000, GrossAmount: decimal.NewFromInt(10),
		Status: StatusAccrued, CreatedAt: common.GetTimestamp(),
	}).Error)

	data := readCommissionData(t, "/api/qy/admin/commission/records", adminListRecords)
	var items []Accrual
	require.NoError(t, common.Unmarshal(data["items"], &items))
	require.Len(t, items, 1)
	assert.Equal(t, "AC-1", items[0].AccrualNo)
}
