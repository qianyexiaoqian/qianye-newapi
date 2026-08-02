package audit

import (
	"sync/atomic"
	"testing"
	_ "unsafe" // for go:linkname

	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// request_test.go —— 异步写入器的可观测性与落库。
//
// 参照实现(sub2api)的队列满即丢弃、没有任何计数,那等于给自己留了一个
// **无法察觉**的盲区:队列一旦长期打满,台账会系统性缺失最繁忙那段时间的
// 记录,而那恰恰是最需要它的时候。这里守的就是"丢了必须能被看见"。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// useExtensionDB 把扩展库句柄指到一个内存 sqlite,并在用例结束后还原。
func useExtensionDB(t *testing.T, cfg *config.Config) *gorm.DB {
	t.Helper()
	gdb, _ := newAuditDB(t)
	require.NoError(t, gdb.AutoMigrate(&qymodel.RequestAudit{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(cfg)
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
	})
	return gdb
}

// 一批记录必须真的落进 qy_request_audits。
//
// 这一条看着平淡,但它是整条链路上唯一会因为"模型没登记进 FoundationTables"
// 或"表名写错"而变红的地方 —— 那两种错都不会有编译错误,只会让台账永远为空。
func TestFlushRequestBatch_PersistsRows(t *testing.T) {
	gdb := useExtensionDB(t, &config.Config{Enabled: true})

	batch := []*qymodel.RequestAudit{
		{Action: "transfer.create", Method: "POST", Path: "/api/qy/transfer",
			StatusCode: 200, Success: true, ActorUserId: 7, CreatedAt: 1},
		{Action: "withdraw.payees.delete", Method: "DELETE", Path: "/api/qy/withdraw/payees/:ref",
			StatusCode: 403, Success: false, ActorUserId: 9, CreatedAt: 2},
	}
	writtenBefore := requestWritten.Load()
	left := flushRequestBatch(batch)

	assert.Empty(t, left, "flush 之后必须清空批次,否则同一批会被反复写")
	assert.Equal(t, writtenBefore+2, requestWritten.Load())

	var rows []qymodel.RequestAudit
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, "transfer.create", rows[0].Action)
	assert.True(t, rows[0].Success)
	assert.False(t, rows[1].Success)
	assert.Equal(t, 403, rows[1].StatusCode)
}

// 队列满时必须丢弃并计数,而不是阻塞调用方。
//
// 调用方是跑在用户请求线程上的 HTTP 中间件:让它等一个已经写不动的数据库,
// 就是把台账的故障传染给业务本身。
func TestRecord_DropsWhenQueueIsFullAndCountsIt(t *testing.T) {
	useExtensionDB(t, &config.Config{Enabled: true})
	// 先把 worker 起起来,它会捕获**当前**这个 channel;随后把包级变量换成一个
	// 容量 1 的队列,worker 就不会来消费它。没有这一步,"填满"与 worker 消费
	// 之间是竞态,用例会时红时绿 —— 而时红时绿的用例最终一定会被人删掉。
	startRequestWriter()
	original := requestQueue
	requestQueue = make(chan *qymodel.RequestAudit, 1)
	t.Cleanup(func() { requestQueue = original })

	before := requestDropped.Load()
	Record(&qymodel.RequestAudit{Action: "fits"})
	require.Len(t, requestQueue, 1)
	assert.Equal(t, before, requestDropped.Load(), "容量之内的记录不该被丢")

	Record(&qymodel.RequestAudit{Action: "overflow"})
	assert.Equal(t, before+1, requestDropped.Load(),
		"队列满时必须丢弃并计数,而不是阻塞 HTTP 线程")

	stats := RequestQueueStats()
	assert.Equal(t, before+1, stats["dropped"],
		"丢弃计数必须出现在健康接口里 —— 静默丢弃的台账等于没有台账")
	for _, key := range []string{"capacity", "pending", "submitted", "written", "failed"} {
		assert.Containsf(t, stats, key, "健康接口缺少 %s", key)
	}
}

// audit.request_enabled=false 时一条都不入队。
//
// 这个开关的存在意义就是"扩展库写入吃紧时先关掉台账,保住资金审计",
// 关了却照样入队等于开关是假的。
func TestRecord_RespectsRequestEnabledSwitch(t *testing.T) {
	off := false
	useExtensionDB(t, &config.Config{Enabled: true, Audit: config.Audit{RequestEnabled: &off}})
	// 同上:换掉包级队列,让 worker 不来消费,断言才与调度无关。
	startRequestWriter()
	original := requestQueue
	requestQueue = make(chan *qymodel.RequestAudit, 4)
	t.Cleanup(func() { requestQueue = original })

	before := requestSubmitted.Load()
	Record(&qymodel.RequestAudit{Action: "should.not.enqueue"})
	assert.Equal(t, before, requestSubmitted.Load(),
		"开关关闭时 Record 必须在入队之前返回")

	// 反证:同一条记录在开关打开时是会被收下的,否则上面那条断言可能只是
	// 因为别的原因恒成立(假回归)。
	on := true
	qyConfig.Store(&config.Config{Enabled: true, Audit: config.Audit{RequestEnabled: &on}})
	Record(&qymodel.RequestAudit{Action: "should.enqueue"})
	assert.Equal(t, before+1, requestSubmitted.Load())
}

// Record(nil) 不得 panic:一个纯观测设施绝不该有能力打死网关。
func TestRecord_NilRowIsIgnored(t *testing.T) {
	assert.NotPanics(t, func() { Record(nil) })
}
