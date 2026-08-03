package withdraw

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
// # 缺陷原样
//
// 提现审核页整页白屏:`Cannot read properties of null (reading 'find')`。
// handleAdminStats 里 `var rows []bucket` + `.Scan(&rows)`,库里没有
// pending/approved/paying 单据时 GORM 的 db.Scan() 先 rows.Next() 再决定要不要
// ScanRows —— 一行都没有就**根本不碰 dest**,rows 保持 nil,
// common.Marshal 把 nil 切片写成 `null`,前端 buckets.find(...) 直接崩。
//
// # 为什么断言必须落在序列化后的 JSON 上
//
// 这类缺陷只在**空结果**时出现,而 nil 切片的 len 也是 0 —— 断言
// `assert.Len(t, rows, 0)` 或 `assert.Empty(t, rows)` 对 nil 和空切片
// 一视同仁地通过,把修复整段回滚照样全绿。唯一能分辨的是那 6 个字节:
// `null` 还是 `[]`。所以这里一律用 json.RawMessage 取原文比对。
//
// 同理,测试必须显式覆盖"库里一行都没有"。先 seed 几行再断言的测试
// 永远碰不到这条路径。

// newEmptyAdminEnv 建一个**一行数据都没有**的提现库并接到 db.Get()。
//
// 与 pagination_handler_test.go 的 newListEnv 刻意分开:那一份会 seed 5 行,
// 而这里要的恰恰是空库。共用一份再加参数只会让两个测试的意图互相遮蔽。
func newEmptyAdminEnv(t *testing.T) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Withdrawal{}, &PiiAudit{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Withdraw: config.Withdraw{Enabled: true},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
}

// readListData 跑一个管理端 handler 并把信封里的 data 解成"字段名 → JSON 原文"。
func readListData(t *testing.T, path string, h gin.HandlerFunc) map[string]json.RawMessage {
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

// assertEmptyJSONArray 断言这个字段的 JSON 原文正好是 `[]`。
//
// 刻意不写成 "不是 null":`{}`、`0`、字符串同样会让前端的 .find/.map 崩,
// 而把期望写死成 `[]` 才能在字段类型被改坏时也变红。
func assertEmptyJSONArray(t *testing.T, data map[string]json.RawMessage, field string) {
	t.Helper()
	raw, ok := data[field]
	require.True(t, ok, "响应里没有 %q 字段,字段名改过了?", field)
	assert.Equal(t, "[]", string(raw),
		"%q 必须序列化成空数组;null 会让前端对着它调 .find/.map 时整页白屏", field)
}

// 提现管理端三个列表字段在空库下都必须是 []。
//
// buckets 是线上炸掉的那一处(Scan 路径,回滚 make 立刻变红);
// items 两处走 Find 路径,GORM 当前会替我们 MakeSlice 出非 nil 切片,
// 因此单独回滚它们这条测试**不会**变红 —— 那是 GORM 的内部实现细节,
// 显式 make 是为了不把 JSON 契约挂在别人的实现细节上,守它的是
// qianye/json_array_guard_test.go 那条结构性断言。
func TestAdminListEndpointsReturnEmptyArrayOnEmptyDB(t *testing.T) {
	t.Run("队列角标 buckets", func(t *testing.T) {
		newEmptyAdminEnv(t)
		data := readListData(t, "/api/qy/admin/withdraw/stats", handleAdminStats)
		assertEmptyJSONArray(t, data, "buckets")
	})

	t.Run("审核队列 items", func(t *testing.T) {
		newEmptyAdminEnv(t)
		data := readListData(t, "/api/qy/admin/withdraw/records", handleAdminList)
		assertEmptyJSONArray(t, data, "items")
	})

	t.Run("明文访问记录 items", func(t *testing.T) {
		newEmptyAdminEnv(t)
		data := readListData(t, "/api/qy/admin/withdraw/pii-audits", handleAdminPiiAudits)
		assertEmptyJSONArray(t, data, "items")
	})
}

// 有数据时 buckets 仍然是数组、并且真的带上了那一桶 —— 防止"把字段写死成 []"
// 这种把测试变绿却把功能改坏的修法。
func TestAdminStatsStillReportsBucketsWhenRowsExist(t *testing.T) {
	newEmptyAdminEnv(t)
	seedWithdrawal(t, qyDBHandle.Load(), "WD-bucket-1", nil)

	data := readListData(t, "/api/qy/admin/withdraw/stats", handleAdminStats)
	var buckets []struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
	}
	require.NoError(t, common.Unmarshal(data["buckets"], &buckets))
	require.Len(t, buckets, 1)
	assert.Equal(t, StatusPending, buckets[0].Status)
	assert.EqualValues(t, 1, buckets[0].Count)
}
