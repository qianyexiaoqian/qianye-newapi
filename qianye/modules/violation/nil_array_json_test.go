package violation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

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
// 与提现队列角标白屏的是同一个缺陷形状,而本模块的统计页命中得更早:
// adminStats 的 by_rule / by_model 走 `.Scan(&rows)`,GORM 的 db.Scan()
// 在结果集一行都没有时**根本不碰 dest**,于是新站点第一次打开违规统计页
// 拿到的就是 `{"by_rule":null,"by_model":null}`。
//
// 断言必须落在序列化后的 JSON 上:nil 切片的 len 也是 0,
// assert.Len(rows, 0) 对 nil 和空切片一视同仁地通过,把修复回滚照样全绿。

// 句柄复用 rules_ctx_test.go 里那一份 linkname:同一个包里对同一个符号
// 声明两次会在链接期报 "symbol redeclared"。

//go:linkname qyDBHealthyForJSONTest github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthyForJSONTest atomic.Bool

//go:linkname qyConfigForJSONTest github.com/QuantumNous/new-api/qianye/config.current
var qyConfigForJSONTest atomic.Pointer[config.Config]

// newEmptyViolationEnv 建一个**一行数据都没有**的违规库并接到 db.Get()。
func newEmptyViolationEnv(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Rule{}, &Record{}, &Ban{}, &Appeal{}, &Counter{}))

	prevHandle := qyDBHandleForCtxTest.Swap(gdb)
	prevHealthy := qyDBHealthyForJSONTest.Swap(true)
	prevCfg := qyConfigForJSONTest.Swap(&config.Config{
		Enabled:   true,
		Violation: config.Violation{Enabled: true},
	})
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prevHandle)
		qyDBHealthyForJSONTest.Store(prevHealthy)
		qyConfigForJSONTest.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

func readViolationData(t *testing.T, path string, h gin.HandlerFunc) map[string]json.RawMessage {
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

func assertJSONArrayIsEmpty(t *testing.T, data map[string]json.RawMessage, field string) {
	t.Helper()
	raw, ok := data[field]
	require.True(t, ok, "响应里没有 %q 字段,字段名改过了?", field)
	assert.Equal(t, "[]", string(raw),
		"%q 必须序列化成空数组;null 会让前端对着它调 .find/.map 时整页白屏", field)
}

// 违规管理端每一个列表字段在空库下都必须是 []。
//
// by_rule / by_model 是 Scan 路径 —— 把它们回滚成 `var byRule, byModel []bucket`
// 这两条子测试立刻变红(实测)。其余四处走 Find 路径,当前 GORM 版本会替我们
// MakeSlice,单独回滚不会变红;显式 make 的意义是不把 JSON 契约挂在 GORM 的
// 内部实现细节上,守它的是 qianye/json_array_guard_test.go 那条结构性断言。
func TestViolationAdminListsReturnEmptyArrayOnEmptyDB(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
		fields  []string
	}{
		{"统计页的两个分布桶", "/api/qy/admin/violation/stats", adminStats, []string{"by_rule", "by_model"}},
		{"规则列表", "/api/qy/admin/violation/rules", adminListRules, []string{"items"}},
		{"命中记录", "/api/qy/admin/violation/records", adminListRecords, []string{"items"}},
		{"封禁列表", "/api/qy/admin/violation/bans", adminListBans, []string{"items"}},
		{"申诉列表", "/api/qy/admin/violation/appeals", adminListAppeals, []string{"items"}},
		{"计数器列表", "/api/qy/admin/violation/counters", adminListCounters, []string{"items"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newEmptyViolationEnv(t)
			data := readViolationData(t, tc.path, tc.handler)
			for _, f := range tc.fields {
				assertJSONArrayIsEmpty(t, data, f)
			}
		})
	}
}

// 有数据时统计桶仍然带上真实分布 —— 防止"把字段写死成 []"这种把测试变绿
// 却把功能改坏的修法。
func TestViolationStatsStillReportsBucketsWhenRowsExist(t *testing.T) {
	gdb := newEmptyViolationEnv(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&Record{
		UserId: 1, RuleName: "测试规则", ModelName: "gpt-4o",
		Status: RecordActive, CreatedAt: now,
	}).Error)

	data := readViolationData(t, "/api/qy/admin/violation/stats", adminStats)
	var byRule []struct {
		Key string `json:"key"`
		Cnt int64  `json:"cnt"`
	}
	require.NoError(t, common.Unmarshal(data["by_rule"], &byRule))
	require.Len(t, byRule, 1)
	assert.Equal(t, "测试规则", byRule[0].Key)
	assert.EqualValues(t, 1, byRule[0].Cnt)
}
