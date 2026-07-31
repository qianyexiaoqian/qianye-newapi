package twophase

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// execute_ctx_test.go —— M6 的回归防线:Execute 的 ctx 必须真的抵达每一条 GORM 语句,
// 而且必须在"正向"与"收尾"两条路径上按不同口径使用。
//
// 缺陷原样:Execute(ctx, req) 的 ctx 在整个函数体内一次都没被引用,三个调用方
// 专门构造的 guard.ColdContext(3 秒) 预算完全落空;并发划转的行锁会一直等到主库
// innodb_lock_wait_timeout(50 秒)。
//
// 为什么这三条用例缺一不可:
//   - 只测"正向路径接上了 ctx",会诱导人把 ctx 一路直传到底 —— 那会引入一个更坏的
//     新缺陷:markFailed 恰恰常常是因为预算耗尽才被触发的,用同一个已过期的 ctx
//     去写 failed 会立刻 DeadlineExceeded,单据永远停在 pending,而 transfer 的
//     releaseOnFailure 只在 Status == Failed 时才回滚风控预占。
//   - 只测"收尾扛得住取消",把 ctx 整个丢掉(回到缺陷原样)照样绿。
//
// 所以三条一起钉:正向必须被取消打断、主库事务必须带上调用方 ctx、收尾必须写得进去。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

type ctxMarkerKey struct{}

// newExecuteEnv 把 db.Get()、model.DB 与配置快照都换成测试用的 sqlite,
// 让 Execute 能被端到端地跑一遍。
//
// 端到端是必须的:M6 断在 Execute 的函数体里(它拿到 ctx 却没往下传),
// 只驱动 markFailed/markSuccess 这些下游函数,永远看不见那一层断链。
func newExecuteEnv(t *testing.T) (ext *gorm.DB, main *gorm.DB) {
	t.Helper()

	open := func(name string) *gorm.DB {
		dsn := filepath.Join(t.TempDir(), name) +
			"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
		gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
		require.NoError(t, err)
		sqlDB, err := gdb.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return gdb
	}

	ext = open("qy_ext.db")
	require.NoError(t, ext.AutoMigrate(&qymodel.FundOrder{}))
	main = open("main.db")

	prevHandle := qyDBHandle.Swap(ext)
	prevHealthy := qyDBHealthy.Swap(true)
	prevMain := model.DB
	model.DB = main

	falseVal := false
	prevCfg := qyConfig.Swap(&config.Config{
		// 审计写入会去碰 qy_audit_logs;本文件只关心 ctx 的去向,关掉它以免
		// 表不存在的报错淹没真正的断言。
		Audit: config.Audit{Enabled: &falseVal},
		// 关掉 outbox:探针表属于主库对账的另一条链路,与 ctx 透传无关。
		TwoPhase: config.TwoPhase{MainOutboxEnabled: &falseVal, PendingGraceSeconds: 60},
		Runtime:  config.Runtime{ColdPathTimeoutMs: 3000},
	})

	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		model.DB = prevMain
	})
	return ext, main
}

func newExecuteRequest(idemKey string) Request {
	return Request{
		Kind:        qymodel.KindTransfer,
		IdemScope:   "transfer",
		IdemKey:     idemKey,
		UserId:      5,
		PeerUserId:  7,
		AmountQuota: 100,
	}
}

// 正向路径:阶段一的扩展库写入必须带上调用方预算。
//
// 用已取消的 ctx 驱动 —— 接上了 ctx 就一定在 Create 上失败;漏接则 sqlite 照常插入
// 成功、Execute 一路跑到底。这是 M6 最直接的判据。
func TestExecute_ForwardPathIsBoundByCallerContext(t *testing.T) {
	ext, _ := newExecuteEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	order, err := Execute(ctx, newExecuteRequest("canceled-before-start"))

	require.Error(t, err, "调用方预算已耗尽时,阶段一的落单必须失败而不是照常写库")
	assert.ErrorIs(t, err, context.Canceled,
		"错误必须是 ctx 取消,说明 WithContext 真的挂到了这条语句上")
	assert.Nil(t, order)

	var cnt int64
	require.NoError(t, ext.Model(&qymodel.FundOrder{}).Count(&cnt).Error)
	assert.Zero(t, cnt, "ctx 已取消却仍落了单,说明扩展库句柄没接调用方 ctx")
}

// 阶段二:主库事务同样必须带上调用方 ctx。
//
// 不接的后果是同一个收款账号的并发划转在行锁上一直等到 innodb_lock_wait_timeout
// (MySQL 默认 50 秒),整段时间钉住一条主库连接。这里用 ctx 上的标记值断言,
// 因为 sqlite 复现不出行锁排队。
func TestExecute_MainTransactionCarriesCallerContext(t *testing.T) {
	newExecuteEnv(t)

	ctx := context.WithValue(context.Background(), ctxMarkerKey{}, "caller")

	var seen any
	req := newExecuteRequest("main-tx-ctx")
	req.MainApply = func(tx *gorm.DB, o *qymodel.FundOrder) error {
		seen = tx.Statement.Context.Value(ctxMarkerKey{})
		return nil
	}

	_, err := Execute(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, "caller", seen,
		"主库事务的 ctx 必须来自调用方 —— model.DB.Transaction 不接 WithContext 时,"+
			"连接池取连接与行锁等待都没有任何上界")
}

// 收尾路径:主库那一侧已经定局之后,调用方的取消绝不能把回写也打掉。
//
// 这是"修复自身引入新缺陷"的防线:ctx 一路直传到底看起来更整齐,但会让每一笔
// 超时的划转都停在 pending 变成人工单(失败侧),或者钱已经动了却显示未完成(成功侧)。
func TestExecute_SettlementSurvivesCallerCancellation(t *testing.T) {
	t.Run("主库失败后仍能写下 failed", func(t *testing.T) {
		ext, _ := newExecuteEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		req := newExecuteRequest("settle-failed")
		req.MainApply = func(tx *gorm.DB, o *qymodel.FundOrder) error {
			// 真实形状:预算就是在主库这一步耗尽的,随后 Execute 才去写 failed。
			cancel()
			return errors.New("主库锁等待超时")
		}

		order, err := Execute(ctx, req)
		require.Error(t, err)
		require.NotNil(t, order)

		var got qymodel.FundOrder
		require.NoError(t, ext.Where("order_no = ?", order.OrderNo).Take(&got).Error)
		assert.Equal(t, qymodel.StatusFailed, got.Status,
			"调用方 ctx 已取消,失败回写仍必须落库:调用方只在 Status == Failed 时"+
				"才回滚风控预占,停在 pending 就是一张人工单")
		assert.Equal(t, qymodel.StatusFailed, order.Status,
			"内存状态必须与库一致,调用方的回滚判定完全依赖它")
	})

	t.Run("主库提交后仍能写下 success", func(t *testing.T) {
		ext, _ := newExecuteEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		localRan := false
		req := newExecuteRequest("settle-success")
		req.AfterCommit = func(o *qymodel.FundOrder) {
			// 主库已提交、钱已经动了,预算恰好在这一刻耗尽。
			cancel()
		}
		req.LocalCommit = func(tx *gorm.DB, o *qymodel.FundOrder) error {
			localRan = true
			return nil
		}

		order, err := Execute(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, order)

		var got qymodel.FundOrder
		require.NoError(t, ext.Where("order_no = ?", order.OrderNo).Take(&got).Error)
		assert.Equal(t, qymodel.StatusSuccess, got.Status,
			"钱已经动了,回写 success 不能被调用方的取消打断,否则单据要等补偿任务才收敛")
		assert.True(t, localRan, "业务副作用与 success 回写同事务,必须一起落地")
	})
}

// 收尾用的是"切断取消链 + 独立预算",不是"干脆不接 ctx"。
//
// 不接 ctx 等于把上界交给 innodb_lock_wait_timeout(50 秒),而收尾要的只是
// "不被调用方取消"。这条断言让两者不会被混为一谈。
func TestSettleContext_DetachesCancellationButKeepsDeadline(t *testing.T) {
	newExecuteEnv(t)

	parent, cancel := context.WithCancel(
		context.WithValue(context.Background(), ctxMarkerKey{}, "caller"))
	cancel()

	ctx, done := settleContext(parent)
	defer done()

	assert.NoError(t, ctx.Err(), "调用方已取消,收尾 ctx 不得跟着失效")
	_, hasDeadline := ctx.Deadline()
	assert.True(t, hasDeadline, "收尾必须仍有一个独立的上界,不能退化成 Background")
	assert.Equal(t, "caller", ctx.Value(ctxMarkerKey{}),
		"链路追踪等 ctx 值必须保留,切断的只是取消信号")
}
