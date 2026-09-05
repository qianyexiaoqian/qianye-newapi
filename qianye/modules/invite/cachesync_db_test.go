package invite

// cachesync_db_test.go —— 两把进程内缓存的跨节点失效。
//
// 被守的两条缺陷:
//
//	#1 管理员在 node A 改分组 / 换绑,node B 在最长 300s 里继续按旧关系、旧档位给星屑,
//	   而档位是**逐笔冻结**进流水的,事后追不回来。
//	#3 风控在 node A 按下拉黑,node B 在最长 60s 里继续给这条关系返星屑落账。
//
// 这里跑在一个进程里,所以"另一个节点"用**它写下的那一行失效流水**来表示:
// 生产里 node B 看到的就是这一行,除此之外它不可能知道 node A 做过什么。
// 判据一律打在"缓存里的值有没有跟上库",不是打在函数被调了几次上。

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useCacheSync 打开广播开关,并把写队列改成同步落库。
//
// 刻意不起 cacheSyncWriteLoop / cacheSyncPollLoop 两个协程:用例要断言的是
// "发布了什么、重放之后缓存变成什么",让协程去跑就只能靠 sleep 等它 ——
// 而按纪律,靠 sleep 的测试不算测试。
func useCacheSync(t *testing.T) {
	t.Helper()
	prev := cacheSyncOn.Swap(true)
	prevCursor := cacheSyncCursor.Swap(0)
	t.Cleanup(func() {
		cacheSyncOn.Store(prev)
		cacheSyncCursor.Store(prevCursor)
		for {
			select {
			case <-cacheSyncCh:
			default:
				return
			}
		}
	})
}

// flushCacheSync 把队列里待广播的失效同步写进库,走的是生产函数 writeInvalidation。
func flushCacheSync(t *testing.T) {
	t.Helper()
	for {
		select {
		case msg := <-cacheSyncCh:
			if cacheSyncOverflow.Swap(false) {
				writeInvalidation(cacheInvalidationMsg{Kind: cacheKindAll})
			}
			writeInvalidation(msg)
		default:
			return
		}
	}
}

func invalidationRows(t *testing.T, gdb *gorm.DB) []CacheInvalidation {
	t.Helper()
	var rows []CacheInvalidation
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	return rows
}

// seedForeignInvalidation 写一行"别的节点做的失效"。
func seedForeignInvalidation(t *testing.T, gdb *gorm.DB, kind string, target int64) {
	t.Helper()
	require.NoError(t, gdb.Create(&CacheInvalidation{
		Kind: kind, TargetId: target,
		Node:      "node-B:deadbeef",
		CreatedAt: common.GetTimestamp(),
	}).Error)
}

// TestEveryLocalInvalidationIsBroadcast 钉住两个失效函数都真的发了广播。
//
// 少接一个的后果不是"晚一点生效",而是那一类缓存在其余节点上**永远**只靠
// TTL 收敛,而 TTL 期间算出来的星屑是冻结的。
func TestEveryLocalInvalidationIsBroadcast(t *testing.T) {
	gdb := newTestDB(t)
	useCacheSync(t)

	invalidateInviter(4242)
	invalidateBlocked()
	flushCacheSync(t)

	rows := invalidationRows(t, gdb)
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Kind] = r.TargetId
		assert.Equal(t, lease.Holder(), r.Node, "必须记下是哪个进程发的")
		assert.NotZero(t, r.CreatedAt)
	}
	for _, kind := range []string{cacheKindInviter, cacheKindBlocked} {
		_, ok := got[kind]
		assert.True(t, ok, "%s 的失效没有广播出去,别的节点收不到", kind)
	}
	assert.EqualValues(t, 4242, got[cacheKindInviter], "邀请关系失效必须带上是谁")
}

// TestBroadcastIsOffUntilStarted 钉住零值口径:没启动通道时一行都不写。
// 单元测试、离线工具与扩展库尚未就绪的启动早期都落在这一支上。
func TestBroadcastIsOffUntilStarted(t *testing.T) {
	gdb := newTestDB(t)
	invalidateBlocked()
	flushCacheSync(t)
	assert.Empty(t, invalidationRows(t, gdb))
}

// TestRemoteRebindReachesThisNode 是 #1 的本体:另一个节点换了绑定,本节点必须在
// 重放之后立刻认新上线,而不是等 300 秒 TTL。
func TestRemoteRebindReachesThisNode(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useCacheSync(t)
	mainDB := useMainDB(t, &model.User{})
	ctx := context.Background()

	seedUser(t, mainDB, 9101, "old", 0, 1000)
	seedUser(t, mainDB, 9102, "new", 0, 1000)
	seedUser(t, mainDB, 9103, "down", 9101, 2000)
	match, err := InviteeEligible(ctx, 9103)
	require.NoError(t, err)
	require.Equal(t, 9101, match.InviterId, "前提:本节点的缓存里是旧上线")

	// 另一个节点把指向改到新上线并广播。主库由那个节点改,本节点只看得到流水。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 9103).Update("inviter_id", 9102).Error)
	seedForeignInvalidation(t, gdb, cacheKindInviter, 9103)
	match, err = InviteeEligible(ctx, 9103)
	require.NoError(t, err)
	assert.Equal(t, 9101, match.InviterId, "重放之前当然还是旧上线 —— 这正是缺陷窗口")

	applyRemoteInvalidations()

	match, err = InviteeEligible(ctx, 9103)
	require.NoError(t, err)
	assert.Equal(t, 9102, match.InviterId, "重放之后必须认新上线,否则这段时间的星屑会发给旧上线")
	assert.NotZero(t, cacheSyncCursor.Load(), "游标必须推进,否则每轮都重放同一批")
}

// TestRemoteBlockReachesThisNode 是 #3 的本体:另一个节点按下拉黑,
// 本节点必须立刻停止给这条关系返。
func TestRemoteBlockReachesThisNode(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useCacheSync(t)
	ctx := context.Background()

	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 9002, InviterId: 9001, Blocked: false,
		BoundAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.Empty(t, blockedInvitees(ctx), "前提:本节点手里是一份 blocked=0 的快照")

	require.NoError(t, gdb.Model(&InviteRelation{}).Where("invitee_id = ?", 9002).
		UpdateColumn("blocked", true).Error)
	seedForeignInvalidation(t, gdb, cacheKindBlocked, 0)
	require.Empty(t, blockedInvitees(ctx), "重放之前仍是旧快照")

	applyRemoteInvalidations()

	assert.True(t, blockedInvitees(ctx)[9002],
		"拉黑必须跨节点立刻生效 —— 这 60 秒正是刷单账号还在拿全额返还的时间")
}

// TestOwnBroadcastIsNotReplayed 钉住"自己写的行不重放":重放自己的失效只会
// 把刚建好的缓存再清一次,凭空给主库加一份读压力。
func TestOwnBroadcastIsNotReplayed(t *testing.T) {
	gdb := newTestDB(t)
	useCacheSync(t)

	invalidateBlocked()
	flushCacheSync(t)
	require.Len(t, invalidationRows(t, gdb), 1)

	before := cacheSyncApplied.Load()
	applyRemoteInvalidations()
	assert.Equal(t, before, cacheSyncApplied.Load(), "自己发的那一行不该被重放")
	assert.NotZero(t, cacheSyncCursor.Load(), "但游标必须越过它")
}

// TestUnknownKindPurgesEverything 钉住滚动升级期间的口径:认不出来的类别
// 一律全清。静默忽略等于在升级窗口里留一段没人知道的错价窗口。
func TestUnknownKindPurgesEverything(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useCacheSync(t)
	ctx := context.Background()

	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 9012, InviterId: 9011, BoundAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.Empty(t, blockedInvitees(ctx))

	require.NoError(t, gdb.Model(&InviteRelation{}).Where("invitee_id = ?", 9012).
		UpdateColumn("blocked", true).Error)
	seedForeignInvalidation(t, gdb, "some_future_kind", 0)
	applyRemoteInvalidations()

	assert.True(t, blockedInvitees(ctx)[9012])
}

// TestOverflowFallsBackToPurgeAll 钉住队列溢出的降级路径:宁可让所有节点
// 多读一次库,也不能丢掉一次失效。
func TestOverflowFallsBackToPurgeAll(t *testing.T) {
	gdb := newTestDB(t)
	useCacheSync(t)

	cacheSyncOverflow.Store(true)
	invalidateBlocked()
	flushCacheSync(t)

	kinds := map[string]bool{}
	for _, r := range invalidationRows(t, gdb) {
		kinds[r.Kind] = true
	}
	assert.True(t, kinds[cacheKindAll], "溢出之后必须补一条全清")
}

// TestPruneKeepsRecentInvalidations 钉住清理不许删掉还没被慢节点读到的行。
func TestPruneKeepsRecentInvalidations(t *testing.T) {
	gdb := newTestDB(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&CacheInvalidation{
		Kind: cacheKindBlocked, Node: "node-B:1", CreatedAt: now - cacheSyncRetentionSec - 60,
	}).Error)
	require.NoError(t, gdb.Create(&CacheInvalidation{
		Kind: cacheKindBlocked, Node: "node-B:1", CreatedAt: now - 60,
	}).Error)

	pruneInvalidations()

	rows := invalidationRows(t, gdb)
	require.Len(t, rows, 1, "只删超过保留期的那一行")
	assert.Equal(t, now-60, rows[0].CreatedAt)
}
