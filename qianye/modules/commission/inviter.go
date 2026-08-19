package commission

import (
	"context"
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
	// InviteeGroup 是下线所在的分组,分组差异化费率按它取值(口径见 grouprate.go)。
	//
	// 与邀请关系一起缓存而不是另开一次查询:两者出自同一行 users,
	// 分开查等于把主库读压力翻倍。代价是换组后最多 InviterCacheSecs
	// 才反映到费率上,这对一个按天聚合的返佣账本可以接受;要立刻生效
	// 可以走管理端的"失效缓存"。
	InviteeGroup string
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
//
// ctx 是热路径的 hot_path_timeout_ms 上界。回主库这一步同样要受它约束 ——
// 主库慢查询会把 worker 占满,后果与扩展库慢一模一样。
// (singleflight 的已知取舍:合并执行时用的是首个调用方的 ctx。这里所有调用方
// 的预算相同,不构成问题。)
func resolveInviter(ctx context.Context, userId int) (inviterEntry, bool, error) {
	if e, ok := peekInviter(userId); ok {
		return e, false, nil
	}
	// 主库句柄还没装上就回错而不是解引用 nil。调用方(计佣写入、法币比例解析)
	// 全都能优雅降级,而这里一个 panic 会顺着 singleflight 的 doCall 炸掉
	// **所有**在这一批合并等待的协程 —— 一次启动竞态换来整片 worker 崩溃。
	if model.DB == nil {
		return inviterEntry{}, false, errors.New("commission: 主库尚未初始化")
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
			Group     string
		}
		// group 是 MySQL/PostgreSQL 的保留字。这里用多参数形式的 Select,
		// GORM 会把每个名字当列名并交给方言加引号,不能拼成一个字符串。
		err := model.DB.WithContext(ctx).Model(&model.User{}).
			Select("inviter_id", "username", "email", "created_at", "group").
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
			InviteeGroup:   row.Group,
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

// unknownInviterWarnEvery 是同一类解析失败两次落日志之间的最小间隔(秒)。
//
// 与 guard.logThrottled 同一个数量级,理由也一样:这条日志是给人看"现在
// 主库有问题"的信号,一分钟一条足够,而它的**上限**才是要紧的事。
const unknownInviterWarnEvery = int64(60)

var (
	unknownInviterMu      sync.Mutex
	unknownInviterLastLog int64
	unknownInviterSkipped int64
)

// takeUnknownInviterWarnSlot 是限频的判定本体:放行则返回 true 与"上次放行
// 之后被压掉的条数",否则把这一条计进压掉的数里并返回 false。
//
// now 由调用方传入而不是自己取,这样这条规则可以在没有时钟的情况下逐格走查——
// 限频最要紧的性质("窗口内只放行一次"和"压掉的条数一条不丢")恰恰是靠
// 时间边界成立的,而用真实时钟去测它只能靠 sleep。
func takeUnknownInviterWarnSlot(now int64) (bool, int64) {
	unknownInviterMu.Lock()
	defer unknownInviterMu.Unlock()
	if unknownInviterLastLog != 0 && now-unknownInviterLastLog < unknownInviterWarnEvery {
		unknownInviterSkipped++
		return false, 0
	}
	unknownInviterLastLog = now
	skipped := unknownInviterSkipped
	unknownInviterSkipped = 0
	return true, skipped
}

// warnUnknownInviter 在解析失败时限频告警。用户被删掉是正常现象,
// 但持续失败意味着主库有问题,不能完全静默。
//
// # 为什么必须限频
//
// resolveInviter 只把 gorm.ErrRecordNotFound 写进缓存(那是"这个用户没了",
// 是稳定事实);其余错误 —— 主库超时、连接被打满、慢查询堆积 —— 一律直接
// 返回且**不进缓存**。于是主库一抖动,同一个用户的每一次 relay 请求都会重查
// 一次主库并走到这里,而这是整个返佣模块唯一一个按 relay QPS 增长的写入点。
//
// 不限频的后果是它恰好在主库已经不健康的那一刻,再按 QPS 给它加一份读压力
// 和一份日志量 —— 故障期间最不该做的两件事。旁边 guard.logThrottled 早就
// 有这个节流,而这一条在它之前就打出来了,所以那道闸拦不住。
//
// 被压掉的条数记在下一条日志里,否则"限频"会变成"丢失",看日志的人无法
// 判断这是偶发一次还是持续了一分钟的风暴。
func warnUnknownInviter(userId int, err error) {
	ok, skipped := takeUnknownInviterWarnSlot(common.GetTimestamp())
	if !ok {
		return
	}
	msg := "qianye: 解析邀请关系失败 user=" + strconv.Itoa(userId) + ": " + err.Error()
	if skipped > 0 {
		msg += "(另有 " + strconv.FormatInt(skipped, 10) + " 条同类失败被限频压掉)"
	}
	common.SysError(msg)
}
