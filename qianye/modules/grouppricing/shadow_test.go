package grouppricing

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainToBuckets 把内存热桶落库后读出来,按维度排序,供断言。
func drainToBuckets(t *testing.T) []ShadowBucket {
	t.Helper()
	flushShadow()
	var rows []ShadowBucket
	require.NoError(t, qyDBHandle.Load().
		Order("group_name asc, model_name asc, mode asc, old_value asc").
		Find(&rows).Error)
	return rows
}

// TestHotAsyncDefaultsToGuard 锁定影子记录走的是 guard.HotAsync。
//
// hotAsync 是为了可测才存在的一层间接。它一旦被改成同步落库,relay 线程就会
// 直接等扩展库 —— 那正是 guard 这一整套东西要防的事,而且改完之后
// 本包所有测试仍然全绿(测试本来就把它换成同步)。所以必须单独锁一次默认值。
func TestHotAsyncDefaultsToGuard(t *testing.T) {
	assert.Equal(t,
		reflect.ValueOf(guard.HotAsync).Pointer(),
		reflect.ValueOf(hotAsync).Pointer(),
		"影子记录必须走 guard.HotAsync:它自带有界队列、panic 拦截与超时,"+
			"直接落库会把扩展库的延迟搬到 relay 线程上")
}

// TestShadowJobIsSyncSafe 锁定 "grouppricing.shadow" 被登记为纯内存作业。
//
// 不登记的话,队列高水位时这些样本会被静默丢弃,对账数字在高峰期系统性偏小 ——
// 而偏小的结论恰恰会让人放心地关掉影子模式。
func TestShadowJobIsSyncSafe(t *testing.T) {
	assert.True(t, stringLiteralsIn(t, "../../guard/guard.go")["grouppricing.shadow"],
		`qianye/guard/guard.go 的 syncSafeJobs 必须登记 "grouppricing.shadow",`+
			`否则队列高水位时影子样本会被丢弃,对账数字会偏小`)
}

// TestObserveAggregatesByDimension 锁定内存聚合的维度。
//
// (旧值, 新值) 必须进键:同一小时内规则被改过,两段区间的折算系数不同,
// 合并成一行就再也算不出差额了。
func TestObserveAggregatesByDimension(t *testing.T) {
	useConfig(t, true, true)
	newTestDB(t)

	base := shadowKey{
		BucketTs:  alignBucket(common.GetTimestamp()),
		GroupName: "vip", ModelName: "gpt-4o", Mode: ModeRatio,
		OldValue: "2", NewValue: "1", Exact: true,
	}
	observe(base, "req-1")
	observe(base, "req-2")

	changed := base
	changed.NewValue = "0.5"
	observe(changed, "req-3")

	rows := drainToBuckets(t)
	require.Len(t, rows, 2, "规则值不同的两段必须分行")
	byNew := map[string]ShadowBucket{}
	for _, r := range rows {
		byNew[r.NewValue] = r
	}
	assert.Equal(t, int64(2), byNew["1"].Requests)
	assert.Equal(t, "req-2", byNew["1"].SampleRequestId)
	assert.Equal(t, int64(1), byNew["0.5"].Requests)
}

// TestFlushAccumulatesAcrossRounds 锁定累加 upsert:分批落盘的结果必须与
// 一次落盘完全相同。多节点、多轮 flush 都靠这个语义合并到同一行。
func TestFlushAccumulatesAcrossRounds(t *testing.T) {
	useConfig(t, true, true)
	newTestDB(t)

	key := shadowKey{
		BucketTs:  alignBucket(common.GetTimestamp()),
		GroupName: "vip", ModelName: "gpt-4o", Mode: ModeRatio,
		OldValue: "2", NewValue: "1", Exact: true,
	}
	observe(key, "req-1")
	require.Len(t, drainToBuckets(t), 1)

	observe(key, "req-2")
	observe(key, "req-3")
	rows := drainToBuckets(t)
	require.Len(t, rows, 1, "同一维度必须合并到同一行,而不是新增一行")
	assert.Equal(t, int64(3), rows[0].Requests)
}

// TestFlushRestoresCountsOnError 锁定落库失败时计数被退回内存。
//
// 直接丢弃会让对账数字在数据库抖动时静默偏小,而对账数字偏小的后果是
// "看起来影响不大,可以切了"。
func TestFlushRestoresCountsOnError(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)

	key := shadowKey{
		BucketTs:  alignBucket(common.GetTimestamp()),
		GroupName: "vip", ModelName: "gpt-4o", Mode: ModeRatio,
		OldValue: "2", NewValue: "1", Exact: true,
	}
	observe(key, "req-1")
	observe(key, "req-2")

	require.NoError(t, gdb.Migrator().DropTable(&ShadowBucket{}))
	flushShadow()
	assert.Equal(t, int64(1), shadowFlushFail.Load())

	require.NoError(t, gdb.AutoMigrate(&ShadowBucket{}))
	rows := drainToBuckets(t)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(2), rows[0].Requests, "失败那一轮的计数必须原样退回内存")
}

// TestShadowGCDeletesOnlyExpired 锁定清理只删过期行。
func TestShadowGCDeletesOnlyExpired(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	fresh := &ShadowBucket{BucketTs: now - 86400, GroupName: "vip", ModelName: "a", Mode: ModeRatio, OldValue: "2", NewValue: "1", Exact: true, Requests: 1}
	old := &ShadowBucket{BucketTs: now - 200*86400, GroupName: "vip", ModelName: "b", Mode: ModeRatio, OldValue: "2", NewValue: "1", Exact: true, Requests: 1}
	require.NoError(t, gdb.Create(fresh).Error)
	require.NoError(t, gdb.Create(old).Error)

	runShadowGC(context.Background())

	var rows []ShadowBucket
	require.NoError(t, gdb.Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "a", rows[0].ModelName)
}

// stringLiteralsIn 返回一个源文件里出现过的全部字符串字面量。
//
// 与 qianye/config/selfcheck_test.go 同一套手法:只看字面量、不做类型解析。
// 作业名是跨包的字符串常量,除了这样对一次,没有别的办法证明两边写的是同一个。
func stringLiteralsIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err, "文件应当可解析: %s", path)

	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out[s] = true
			}
		}
		return true
	})
	return out
}
