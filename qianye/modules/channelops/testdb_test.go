package channelops

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 channelops 的数据库脚手架。
//
// 为什么必须真跑数据库:本模块被要求守住的三条不变量 ——
// "第 7 条失败不牵连前 6 条已经删掉的"、"删渠道必须同时删 abilities"、
// "已经是目标状态不算失败" —— 一条都不在纯函数里,而在事务边界、
// 两张表的联动与 UpdateChannelStatus 的返回值语义里。
// 只测 normalizeIds 的话,把事务换成两条裸语句测试照样全绿。
//
// 生产的主库可能是 SQLite / MySQL / PostgreSQL,这里跑 sqlite,
// 因此断言一律只依赖跨库通用语义(条件 DELETE/UPDATE、RowsAffected、事务)。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// newQyDB 建扩展库(只用来放审计)并接到 db.Get()。
//
// qy_audit_logs 必须建:审计断言全靠它,表不存在时 audit.Write 只记一行日志
// 就返回,断言会退化成"永远没写进去"的假绿。
func newQyDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_channelops.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(&qymodel.AuditLog{}))

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

// newMainDB 建上游主库(channels + abilities)并接到 model.DB。
//
// MemoryCacheEnabled / RedisEnabled 一并关掉:InitChannelCache 与
// UpdateChannelStatus 在打开时会去解引用 common.RDB,而测试里它是 nil。
func newMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "main.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&model.Channel{}, &model.Ability{}))

	prev := model.DB
	prevMem := common.MemoryCacheEnabled
	prevRedis := common.RedisEnabled
	model.DB = gdb
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = prev
		common.MemoryCacheEnabled = prevMem
		common.RedisEnabled = prevRedis
		_ = sqlDB.Close()
	})
	return gdb
}

// seedChannel 往主库插一个渠道,并给它一行 abilities(物化路由表)。
func seedChannel(t *testing.T, gdb *gorm.DB, id int, name string, status int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.Channel{
		Id: id, Name: name, Status: status,
		Group: "default", Models: "gpt-4o",
	}).Error)
	require.NoError(t, gdb.Create(&model.Ability{
		Group: "default", Model: "gpt-4o", ChannelId: id,
		Enabled: status == common.ChannelStatusEnabled,
	}).Error)
}

// call 驱动一个 handler,返回 recorder。actorId 落进 c.Set("id"),审计要用。
func call(t *testing.T, path, body string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 9001)
	c.Set("username", "root")
	h(c)
	return res
}

// resultOf 解出信封里的 batchResult。非 200 或 success:false 直接 fail。
func resultOf(t *testing.T, res *httptest.ResponseRecorder) batchResult {
	t.Helper()
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
	var body struct {
		Success bool        `json:"success"`
		Data    batchResult `json:"data"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success, "body=%s", res.Body.String())
	// 三档必须凑齐总数。这条恒等式是前端唯一能信的东西 ——
	// 少了它,"20 选中、18 成功"就回答不了剩下 2 个要不要处理。
	require.Equal(t, body.Data.Total,
		body.Data.Succeeded+body.Data.Skipped+body.Data.Failed,
		"total 必须等于三档之和: %+v", body.Data)
	require.Len(t, body.Data.Items, body.Data.Total)
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

// itemFor 取出指定渠道 id 的那一条结果。
func itemFor(t *testing.T, res batchResult, id int) itemResult {
	t.Helper()
	for _, item := range res.Items {
		if item.Id == id {
			return item
		}
	}
	require.Failf(t, "结果里找不到渠道", "id=%d, items=%+v", id, res.Items)
	return itemResult{}
}

// auditRows 读出全部审计记录的 action + result,按写入顺序。
func auditRows(t *testing.T, gdb *gorm.DB) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	return rows
}
