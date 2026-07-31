package commission

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/samber/hot"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// inviterEntry 是一条邀请关系的缓存值。
//
// InviterId == 0 表示"这个用户没有邀请人",这是必须被缓存的正常值 ——
// 绝大多数用户都是这种情况,不做负缓存的话每次消费都要回一次主库,
// 等于给主库加上与 relay QPS 等量的读压力。
type inviterEntry struct {
	InviterId      int
	InviteeName    string
	InviteeCreated int64
}

const inviterCacheCapacity = 200000

var (
	inviterCacheOnce sync.Once
	inviterCache     *hot.HotCache[int, inviterEntry]
	inviterSF        singleflight.Group

	cacheHit  = newCounter()
	cacheMiss = newCounter()
)

func getInviterCache() *hot.HotCache[int, inviterEntry] {
	inviterCacheOnce.Do(func() {
		ttl := config.Get().Commission.InviterCacheSecs
		if ttl <= 0 {
			ttl = 300
		}
		// 刻意不用 pkg/cachex.HybridCache:它在启用 Redis 时会走网络,
		// 等于给每一次消费加一次往返。邀请关系是注册时一次性写入、之后
		// 几乎不变的数据,per-node 内存缓存 + TTL 完全够用。
		inviterCache = hot.NewHotCache[int, inviterEntry](hot.LRU, inviterCacheCapacity).
			WithTTL(time.Duration(ttl) * time.Second).
			WithJanitor().
			Build()
	})
	return inviterCache
}

// peekInviter 只查缓存,绝不回源。这是 relay 线程唯一允许调用的形式。
func peekInviter(userId int) (inviterEntry, bool) {
	e, found, err := getInviterCache().Get(userId)
	if err != nil || !found {
		cacheMiss.Add(1)
		return inviterEntry{}, false
	}
	cacheHit.Add(1)
	return e, true
}

// resolveInviter 解析邀请关系,必要时回主库。只能在后台 worker 里调用。
//
// 第二个返回值表示"本次确实回源了",调用方据此决定是否需要补建关系快照 ——
// 这样关系快照的维护成本是"每个下线每个 TTL 一次",而不是每条消费事件一次。
//
// singleflight 防缓存击穿:某个热门邀请人的上千个下线同时首次消费时,
// 对主库只打一次查询。
func resolveInviter(userId int) (inviterEntry, bool, error) {
	if e, ok := peekInviter(userId); ok {
		return e, false, nil
	}
	type resolved struct {
		entry inviterEntry
	}
	v, err, _ := inviterSF.Do(strconv.Itoa(userId), func() (any, error) {
		var row struct {
			InviterId int
			Username  string
			Email     string
			CreatedAt int64
		}
		err := model.DB.Model(&model.User{}).
			Select("inviter_id", "username", "email", "created_at").
			Where("id = ?", userId).
			Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 用户已被删除。缓存成"无邀请人",否则每条日志都会再查一次主库。
			getInviterCache().Set(userId, inviterEntry{})
			return resolved{}, nil
		}
		if err != nil {
			return resolved{}, err
		}
		name := row.Username
		if name == "" {
			name = row.Email
		}
		e := inviterEntry{
			InviterId:      row.InviterId,
			InviteeName:    name,
			InviteeCreated: row.CreatedAt,
		}
		getInviterCache().Set(userId, e)
		return resolved{entry: e}, nil
	})
	if err != nil {
		return inviterEntry{}, false, err
	}
	return v.(resolved).entry, true, nil
}

// invalidateInviter 在管理员改动 users.inviter_id 之后手动失效。
// userId <= 0 表示清空全部。
func invalidateInviter(userId int) {
	if userId <= 0 {
		getInviterCache().Purge()
		return
	}
	getInviterCache().Delete(userId)
}

// inviterCacheStats 供管理端健康面板判断缓存是否真的在起作用。
func inviterCacheStats() map[string]any {
	hits, misses := cacheHit.Load(), cacheMiss.Load()
	total := hits + misses
	rate := 0.0
	if total > 0 {
		rate = float64(hits) / float64(total)
	}
	return map[string]any{
		"size":     getInviterCache().Len(),
		"capacity": inviterCacheCapacity,
		"hits":     hits,
		"misses":   misses,
		"hit_rate": rate,
	}
}

// warnUnknownInviter 在解析失败时限频告警。用户被删掉是正常现象,
// 但持续失败意味着主库有问题,不能完全静默。
func warnUnknownInviter(userId int, err error) {
	common.SysError("qianye: 解析邀请关系失败 user=" + strconv.Itoa(userId) + ": " + err.Error())
}
