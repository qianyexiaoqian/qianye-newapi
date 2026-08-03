package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// nil_array_json_test.go —— 空库下发的列表字段必须是 JSON `[]`,不能是 `null`。
//
// 提现审核页那次白屏(`Cannot read properties of null (reading 'find')`)的根因
// 是 nil 切片被 common.Marshal 写成 `null`。本文件覆盖 qianye/controller 这一侧
// 的同形位置,其中健康页的 leases 是**错误路径**上的那一份:
//
//	leases, _ := lease.List()   // 扩展库不可用时返回 (nil, err),错误被刻意吞掉
//	ok(c, gin.H{"leases": leases})
//
// 而"扩展库不可用"恰恰是运维最需要打开这个排障页的时刻 —— 它偏偏在这时白屏。
//
// 断言必须落在序列化后的 JSON 上:nil 切片的 len 也是 0,
// assert.Len(rows, 0) 对 nil 和空切片一视同仁地通过,把修复回滚照样全绿。

// newEmptyExtEnv 建一个测试库并接到 db.Get()。
//
// tables 为空时刻意**一张表都不建**:那是 lease.List() 会真的返回 error 的
// 唯一办法,也是健康页那处 nil 的实际来源。
func newEmptyExtEnv(t *testing.T, tables ...any) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	if len(tables) > 0 {
		require.NoError(t, gdb.AutoMigrate(tables...))
	}

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{Enabled: true})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

func readExtData(t *testing.T, path string, h gin.HandlerFunc) map[string]json.RawMessage {
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

func assertEmptyArrayField(t *testing.T, data map[string]json.RawMessage, field string) {
	t.Helper()
	raw, ok := data[field]
	require.True(t, ok, "响应里没有 %q 字段,字段名改过了?", field)
	assert.Equal(t, "[]", string(raw),
		"%q 必须序列化成空数组;null 会让前端对着它调 .find/.map 时整页白屏", field)
}

// 健康页的 leases 在**租约表读取失败**时也必须是 []。
//
// 这是本文件里唯一一处回滚就变红的断言(实测):把
// `leases, err := lease.List(); if err != nil || leases == nil { ... }`
// 改回 `leases, _ := lease.List()`,leases 是 nil,JSON 变成 `null`。
func TestAdminHealthLeasesIsArrayWhenLeaseTableUnreadable(t *testing.T) {
	newEmptyExtEnv(t) // 一张表都不建:lease.List() 必定报 no such table
	data := readExtData(t, "/api/qy/admin/health", AdminHealth)
	assertEmptyArrayField(t, data, "leases")
}

// 租约表存在但为空时同样是 [],并且表里有行时不会被误清空。
func TestAdminHealthLeasesReflectsTableContents(t *testing.T) {
	gdb := newEmptyExtEnv(t, &qymodel.TaskLease{})
	data := readExtData(t, "/api/qy/admin/health", AdminHealth)
	assertEmptyArrayField(t, data, "leases")

	require.NoError(t, gdb.Create(&qymodel.TaskLease{
		Name: "commission.settle", Holder: "node-1:1", Fence: 1,
	}).Error)
	data = readExtData(t, "/api/qy/admin/health", AdminHealth)
	var leases []qymodel.TaskLease
	require.NoError(t, common.Unmarshal(data["leases"], &leases))
	require.Len(t, leases, 1)
	assert.Equal(t, "commission.settle", leases[0].Name)
}

// 三个分页列表接口在空库下的 items 必须是 []。
//
// 这三处走 GORM Find,当前版本会替我们 MakeSlice 出非 nil 切片,
// 单独回滚 make 这条测试**不会**变红;显式 make 的意义是不把 JSON 契约
// 挂在 GORM 的内部实现细节上,守它的是 qianye/json_array_guard_test.go。
func TestExtAdminListsReturnEmptyArrayOnEmptyDB(t *testing.T) {
	cases := []struct {
		name    string
		table   any
		path    string
		handler gin.HandlerFunc
	}{
		{"资金单列表", &qymodel.FundOrder{}, "/api/qy/admin/fund-orders", AdminListFundOrders},
		{"审计台账", &qymodel.AuditLog{}, "/api/qy/admin/audit-logs", AdminListAuditLogs},
		{"请求留痕", &qymodel.RequestAudit{}, "/api/qy/admin/request-audits", AdminListRequestAudits},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newEmptyExtEnv(t, tc.table)
			data := readExtData(t, tc.path, tc.handler)
			assertEmptyArrayField(t, data, "items")
		})
	}
}
