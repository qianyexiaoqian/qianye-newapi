package invite

import (
	"context"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm/clause"
)

// ───────────────────────── 邀请关系与风控快照 ─────────────────────────

// ensureRelation 在首次见到某个下线时落一行关系快照。
//
// 脱敏名在这里算好并缓存,列表页因此零主库访问 —— 邀请人永远拿不到
// 下线的真实用户名与邮箱。
func ensureRelation(ctx context.Context, inviterId, inviteeId int, rawName string, boundAt int64) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	gdb = gdb.WithContext(ctx)
	risk := ""
	blocked := false
	// 互邀环路:A 邀 B 且 B 邀 A,是最常见的双账号自刷手法。
	if peer, _, err := resolveInviter(ctx, inviterId); err == nil && peer.InviterId == inviteeId {
		risk = "reciprocal_invite"
		blocked = true
	}
	now := common.GetTimestamp()
	row := InviteRelation{
		InviteeId:  inviteeId,
		InviterId:  inviterId,
		MaskedName: truncate(MaskUsername(rawName), 64),
		InviteeRef: inviteeRef(inviteeId, refSalt()),
		BoundAt:    boundAt,
		RiskFlags:  risk,
		Blocked:    blocked,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	// 已存在就不动:管理员可能手工改过 Blocked,自动流程不该覆盖人的决定。
	if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		db.MarkFailure(err)
	}
	if blocked {
		invalidateBlocked()
	}
}

// blockedCacheSeconds 是拉黑名单快照的刷新周期。
//
// 刻意不起后台协程去刷:名单只在非热路径(异步 worker、日结任务、HTTP handler)
// 被读到,惰性刷新足够。
const blockedCacheSeconds = 60

var (
	blockedMu     sync.Mutex
	blockedSet    map[int]bool
	blockedLoaded int64
	// blockedEpoch 是快照的代次,每次失效自增。
	//
	// 查库放到临界区之外之后,"SELECT 返回"与"写回缓存"之间就出现了一个窗口:
	// 管理员在这中间拉黑了某人并调 invalidateBlocked(),在途的旧快照会把它
	// 静默盖掉,此后 60 秒继续给刷单账号返星屑。代次让写回方能发现
	// "我读的那一版已经作废了"并丢弃本次结果。
	blockedEpoch uint64
)

// blockedInvitees 返回被拉黑的邀请关系集合。
//
// 拉黑是极少数情形,整表拉进内存(每 60 秒刷一次)远比每次判定回一次库便宜。
//
// 持锁只做读/写快照,SELECT 在临界区之外发出,写回缓存时用代次判断这份快照
// 是否已被 invalidateBlocked 作废(拉黑一个下线之后还按旧快照继续给他上线
// 返星屑,正是这把锁要防的那件事)。
func blockedInvitees(ctx context.Context) map[int]bool {
	blockedMu.Lock()
	now := common.GetTimestamp()
	if blockedSet != nil && now-blockedLoaded < blockedCacheSeconds {
		cached := blockedSet
		blockedMu.Unlock()
		return cached
	}
	epoch := blockedEpoch
	snapshot := blockedSet
	blockedMu.Unlock()

	gdb := db.Get()
	if gdb == nil {
		if snapshot != nil {
			return snapshot
		}
		return map[int]bool{}
	}
	var ids []int
	if err := gdb.WithContext(ctx).Model(&InviteRelation{}).Where("blocked = ?", true).Pluck("invitee_id", &ids).Error; err != nil {
		db.MarkFailure(err)
		if snapshot != nil {
			return snapshot
		}
		return map[int]bool{}
	}
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	blockedMu.Lock()
	if blockedEpoch == epoch {
		blockedSet = m
		blockedLoaded = common.GetTimestamp()
	}
	blockedMu.Unlock()
	return m
}

// invalidateBlocked 失效本进程的拉黑快照,并广播给其它节点。
//
// 拉黑是「立刻停止给这条关系返星屑」的紧急开关,而它此前只在按下按钮的那一个
// 节点上生效 —— 其余节点最长 60 秒里继续给刷单账号全额返并落账,
// 落下的行没有任何下游复核。
func invalidateBlocked() {
	invalidateBlockedLocal()
	publishInvalidation(cacheKindBlocked, 0)
}

// invalidateBlockedLocal 只清本进程,供 cachesync 重放远端流水时使用。
func invalidateBlockedLocal() {
	blockedMu.Lock()
	blockedSet = nil
	blockedLoaded = 0
	blockedEpoch++
	blockedMu.Unlock()
}

// ───────────────────────── 通用小工具 ─────────────────────────

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	for len(string(r)) > max {
		r = r[:len(r)-1]
	}
	return string(r)
}

func itoa(v int) string { return strconv.Itoa(v) }
