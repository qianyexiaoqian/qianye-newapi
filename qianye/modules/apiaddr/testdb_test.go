package apiaddr

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 apiaddr 包的数据库脚手架。
//
// 为什么必须真跑数据库:本模块被要求守住的三条不变量 —— "编辑不能覆盖排序"、
// "并发重排要被拒"、"空库下发 [] 而不是 null" —— 一条都不在纯函数里,
// 而在 SQL 的列清单、事务里的全集比对与序列化行为里。只测 normalizeURL 的话,
// 把 Updates 换成 Save(会连 sort_order 一起写回)测试照样全绿。
//
// 生产的扩展库固定是 MySQL,这里跑 sqlite,因此断言一律只依赖跨库通用语义
// (带 WHERE 的条件 UPDATE、RowsAffected、事务回滚)。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

// qyDBHealthy 指向 db 包的健康标志。db.Available() 读它,而 audit.Write 又读
// db.Available() —— 不置位的话审计断言会全部变成"永远没写进去"的假绿。
//
//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// newTestDB 建一个扩展库测试实例并接到 db.Get()。
//
// qy_audit_logs 必须一起建:四个写接口的审计断言全靠它,表不存在时
// audit.Write 只会记一行日志然后返回,断言会变成"永远没写进去"的假绿。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_apiaddr.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(&Address{}, &qymodel.AuditLog{}))

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

// seedAddress 直接落一行,跳过 HTTP 层。
func seedAddress(t *testing.T, gdb *gorm.DB, name, addrURL string, order int, enabled bool) Address {
	t.Helper()
	now := common.GetTimestamp()
	row := Address{
		Name: name, URL: addrURL, SortOrder: order, Enabled: enabled,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, gdb.Create(&row).Error)
	return row
}

// call 驱动一个 handler,返回 recorder。actorId 落进 c.Set("id"),审计要用。
func call(t *testing.T, method, path, body string, params gin.Params, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, path, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", 9001)
	c.Set("username", "root")
	h(c)
	return res
}

// dataOf 解出信封里的 data。非 200 或 success:false 直接 fail。
func dataOf(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success, "body=%s", res.Body.String())
	return body.Data
}

// codeOf 解出失败信封里的业务 code。
func codeOf(t *testing.T, res *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.False(t, body.Success, "body=%s", res.Body.String())
	return body.Code
}

// auditActions 读出全部审计记录的 action + result,按写入顺序。
func auditActions(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action+":"+r.Result)
	}
	return out
}

// orderedIds 按展示顺序读出全部 id。
func orderedIds(t *testing.T, gdb *gorm.DB) []int {
	t.Helper()
	ids := make([]int, 0, 8)
	require.NoError(t, gdb.Model(&Address{}).Order(orderClause).Pluck("id", &ids).Error)
	return ids
}
