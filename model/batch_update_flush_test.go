package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batch_update_flush_test.go —— BATCH_UPDATE_ENABLED 下退出前必须把队列刷进库。
//
// 缺陷形态:开了这个开关之后,用户额度增减、令牌额度增减、used_quota、
// request_count 全部只落在进程内存的 map 里,由 InitBatchUpdater 每
// BATCH_UPDATE_INTERVAL 秒(默认 5)刷一次。优雅退出只调了 SaveQuotaDataCache()
// (那是看板缓存,与额度队列无关),于是 SIGTERM/崩溃时最后一个窗口内的**全站**
// 扣费与退款被静默丢弃,一行日志都没有 —— 而消费日志照写不误,logs 记的钱与
// 用户实际被扣的钱从此对不上。仓库自带的 docker-compose.yml 就默认开着它。

func TestFlushBatchUpdatesPersistsQueuedQuotaChanges(t *testing.T) {
	const userId = 9701
	require.NoError(t, DB.Create(&User{Id: userId, Username: "batch_flush_user", Quota: 100_000}).Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM users WHERE id = ?", userId) })

	prev := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() {
		common.BatchUpdateEnabled = prev
		// 队列是包级状态,跑完必须排空,否则会漏进别的用例。
		batchUpdate()
	})

	require.NoError(t, DecreaseUserQuota(userId, 30_000, false))
	require.NoError(t, IncreaseUserQuota(userId, 5_000, false))

	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, 100_000, user.Quota,
		"前提:开了批量更新之后这些扣减确实只在内存里 —— 否则这条用例证明不了任何事")

	FlushBatchUpdates()

	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, 75_000, user.Quota,
		"退出前刷一次之后,排队的扣费与退款必须一分不差地落库")
}

func TestFlushBatchUpdatesIsANoOpWhenBatchingIsOff(t *testing.T) {
	const userId = 9702
	require.NoError(t, DB.Create(&User{Id: userId, Username: "batch_off_user", Quota: 100_000}).Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM users WHERE id = ?", userId) })

	prev := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	t.Cleanup(func() { common.BatchUpdateEnabled = prev })

	require.NoError(t, DecreaseUserQuota(userId, 30_000, false))
	FlushBatchUpdates()

	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, 70_000, user.Quota, "关着批量更新时扣减本来就直接落库,刷新不该重复扣一遍")
}

// TestShutdownFlushesTheBatchUpdateQueue 钉住调用点本身。
//
// 只测 FlushBatchUpdates 能刷是不够的:缺陷不是"刷不动",而是"退出时没人调它"。
func TestShutdownFlushesTheBatchUpdateQueue(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../main.go", nil, 0)
	require.NoError(t, err)

	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "FlushBatchUpdates" {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "model" {
			found = true
		}
		return true
	})
	assert.True(t, found,
		"main.go 的退出路径必须调 model.FlushBatchUpdates();少了它,"+
			"最后一个刷新窗口内全站的扣费与退款会在每次重启时被静默丢弃")
}
