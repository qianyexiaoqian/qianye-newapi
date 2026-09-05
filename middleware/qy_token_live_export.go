package middleware

import (
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

// qy_token_live_export.go —— 纯新增文件,合并上游时冲突为 0。
//
// 它给「API 密钥」页每一行提供两个实时数:
//
//	当前并发    此刻正在跑、还没返回的转发请求数
//	近 1 分钟   最近 60 秒内用这把 key 打进来的转发请求数
//
// 项目方原话:「api密钥增加一个 sub2 一样显示一下 key 当前并发数(每)分钟。」
//
// ────────────────── 为什么是进程内计数 ──────────────────
//
// 与同目录 qy_rate_limit_export.go 里那套分组并发计数同一个理由,而且更强:
//
// 并发计数要求**每一次 acquire 都有配对的 release**。放进 Redis 之后,节点崩在
// 请求中途时那几个占位不会有人来还,那把 key 的并发数就会永久停在一个虚高的值上,
// 而且没有任何迹象 —— 一个**只用来显示**的数字骗起人来比限流器更彻底:限流器
// 至少会因为放不出请求而被投诉,显示位不会。要做对就得给每个在途请求一个带
// 时间戳的成员、按 TTL 清扫、再用 Lua 把三步做成原子的,而本机与目标部署都没有
// Redis,那一套写出来一行都测不到。
//
// 所以这里给的是一个**语义明确、可被完整测试**的东西:按节点计数。单节点部署下
// 它就是字面意思;多节点部署下,每个节点只看得见打到自己身上的那一部分。
// 这句话必须写在界面的悬浮提示里(见 web/src/features/keys/lib/live-stats.ts),
// 而不是留在这里让用户自己猜为什么两次刷新的数对不上。
//
// 进程重启会把两个数一起清零。它们都会自愈:在途数随下一批请求恢复,
// 近 1 分钟数最多 60 秒后恢复。因此不做任何持久化。
//
// ────────────────── 为什么是 60 个一秒桶 ──────────────────
//
// 「近 1 分钟」最直白的写法是给每把 key 存一串请求时间戳、读的时候扔掉过期的。
// 那个结构的内存**由流量决定**:一把 key 每秒 500 次请求就是 3 万个时间戳,
// 而这是一个显示位,不该让"被人打爆的那把 key"顺带把内存也打爆。
//
// 环形桶的内存是固定的(每把活跃 key 约 250 字节),代价是 1 秒的粒度 ——
// 对一个每 5 秒刷新一次的界面来说,这个代价是零。
type qyTokenLiveEntry struct {
	// userID 让读取侧不必回主库查"这把 key 是谁的"。写入侧本来就拿得到
	// (TokenAuth 在同一次请求里 c.Set("id", token.UserId)),存下来之后
	// 整条读取路径不碰任何数据库。
	userID   int
	inFlight int
	// baseSec 是 buckets 里"最新的那一秒"。buckets[bucketIndex(s)] 记的是
	// 第 s 秒发生的请求数,且只在 (baseSec-59, baseSec] 这一段内有效。
	baseSec int64
	buckets [QyTokenLiveWindowSeconds]int32
}

const (
	// QyTokenLiveWindowSeconds 是「近 1 分钟」的窗口长度,同时是环的长度。
	// 它会随响应下发给前端 —— 界面上那句"近 60 秒"必须由后端说了算,
	// 否则改窗口时会留下一句一直没人改的文案。
	QyTokenLiveWindowSeconds = 60
	// qyTokenLiveSweepEverySeconds 是清扫间隔。清扫是 O(活跃 key 数),
	// 而它在写入路径的锁里跑,所以刻意做成一个窗口才一次:一把 key 静默满
	// 一个窗口之后才可能被回收,表的规模因此稳定在"近两分钟用过的 key"。
	qyTokenLiveSweepEverySeconds = QyTokenLiveWindowSeconds
)

// QyTokenLive 是一把令牌此刻的两个实时数。
type QyTokenLive struct {
	InFlight int
	Requests int
}

type qyTokenLiveRegistry struct {
	mu sync.Mutex
	// now 只为测试可注入。生产实例用挂钟秒 —— 见 roll 里对时钟回拨的处理。
	now    func() int64
	tokens map[int]*qyTokenLiveEntry
	// byUser 让「读当前用户的全部 key」是 O(这个用户的活跃 key 数) 而不是
	// O(全站活跃 key 数)。读取发生在密钥页每 5 秒一次的轮询里,而这把锁同时
	// 挡着**每一次转发请求**的入口 —— 拿它扫全表是在用界面刷新去拖慢转发。
	byUser    map[int]map[int]struct{}
	lastSweep int64
}

func newQyTokenLiveRegistry(now func() int64) *qyTokenLiveRegistry {
	return &qyTokenLiveRegistry{
		now:    now,
		tokens: map[int]*qyTokenLiveEntry{},
		byUser: map[int]map[int]struct{}{},
	}
}

var qyTokenLive = newQyTokenLiveRegistry(func() int64 { return time.Now().Unix() })

// bucketIndex 把"第几秒"映射到环上的下标。
// 取模前先归正,是因为 Go 的 % 对负数返回负数,而负的下标会 panic ——
// 挂钟被设到 1970 之前虽然荒唐,但那时崩的是整个转发入口,不是这一列。
func bucketIndex(sec int64) int {
	i := sec % QyTokenLiveWindowSeconds
	if i < 0 {
		i += QyTokenLiveWindowSeconds
	}
	return int(i)
}

// roll 把环推进到 now 那一秒:中间跳过的秒清零,因为它们已经滑出窗口了。
func (e *qyTokenLiveEntry) roll(now int64) {
	if now == e.baseSec {
		return
	}
	// 时钟回拨(NTP 校时、运维改系统时间)时整段清零,而不是相信一个更小的秒数:
	// 保留旧桶会让"近 1 分钟"在回拨的那几秒里把未来的计数也算进去。
	// 代价是那一刻的计数偏小,而偏小的显示位不会让任何人做出错误决定。
	if now < e.baseSec {
		e.buckets = [QyTokenLiveWindowSeconds]int32{}
		e.baseSec = now
		return
	}
	if delta := now - e.baseSec; delta >= QyTokenLiveWindowSeconds {
		e.buckets = [QyTokenLiveWindowSeconds]int32{}
	} else {
		for s := e.baseSec + 1; s <= now; s++ {
			e.buckets[bucketIndex(s)] = 0
		}
	}
	e.baseSec = now
}

func (e *qyTokenLiveEntry) record(now int64) {
	e.roll(now)
	e.buckets[bucketIndex(now)]++
}

func (e *qyTokenLiveEntry) requests(now int64) int {
	e.roll(now)
	total := 0
	for _, n := range e.buckets {
		total += int(n)
	}
	return total
}

// enter 记一次请求并占一个在途位。返回的 release 必须被调用,且只能调用一次;
// tokenID <= 0(没有令牌身份的路由)返回 nil,调用方据此跳过。
func (r *qyTokenLiveRegistry) enter(tokenID, userID int) (release func()) {
	if tokenID <= 0 {
		return nil
	}
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked(now)

	entry := r.tokens[tokenID]
	if entry == nil {
		entry = &qyTokenLiveEntry{userID: userID, baseSec: now}
		r.tokens[tokenID] = entry
		owned := r.byUser[userID]
		if owned == nil {
			owned = map[int]struct{}{}
			r.byUser[userID] = owned
		}
		owned[tokenID] = struct{}{}
	}
	entry.record(now)
	entry.inFlight++

	// once 而不是裸减:中间件用 defer 保证配对,但 defer 只保证"至少写对一次",
	// 保证不了调用方将来不会在别处再还一次。多还一次的表现是并发数越用越小,
	// 最后恒为 0 —— 又一个只会骗人、不会报错的失败形态。
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			// 这里一定还是同一个 entry:清扫只删 inFlight == 0 的行,
			// 而这一行在 release 跑完之前 inFlight >= 1。
			if cur := r.tokens[tokenID]; cur != nil && cur.inFlight > 0 {
				cur.inFlight--
			}
		})
	}
}

// sweepLocked 回收"没有在途、窗口内也没有请求"的行。调用方必须持锁。
func (r *qyTokenLiveRegistry) sweepLocked(now int64) {
	if now >= r.lastSweep && now-r.lastSweep < qyTokenLiveSweepEverySeconds {
		return
	}
	r.lastSweep = now
	for tokenID, entry := range r.tokens {
		if entry.inFlight > 0 || entry.requests(now) > 0 {
			continue
		}
		delete(r.tokens, tokenID)
		owned := r.byUser[entry.userID]
		if owned == nil {
			continue
		}
		delete(owned, tokenID)
		// 删空的 map 要整个删掉:用户可以被删号,留一个空壳会让这张表只增不减。
		if len(owned) == 0 {
			delete(r.byUser, entry.userID)
		}
	}
}

// snapshot 读某个用户名下**当前有数**的令牌。
//
// 零值口径与「今日消耗」那一列逐字一致:两个数都是 0 的令牌**不在返回值里**,
// 由前端渲染成 0。缺席与 0 在用户眼里是同一件事("这把 key 现在没人用"),
// 而它们与"取不到"必须长得不一样 —— 后者由 HTTP 层负责。
func (r *qyTokenLiveRegistry) snapshot(userID int) map[int]QyTokenLive {
	if userID <= 0 {
		return map[int]QyTokenLive{}
	}
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[int]QyTokenLive, len(r.byUser[userID]))
	for tokenID := range r.byUser[userID] {
		entry := r.tokens[tokenID]
		if entry == nil {
			continue
		}
		live := QyTokenLive{InFlight: entry.inFlight, Requests: entry.requests(now)}
		if live.InFlight == 0 && live.Requests == 0 {
			continue
		}
		out[tokenID] = live
	}
	return out
}

// QyTokenLiveSnapshot 读某个用户名下每一把令牌此刻的在途数与近 60 秒请求数。
// 只读进程内存,不碰任何数据库。
func QyTokenLiveSnapshot(userID int) map[int]QyTokenLive {
	return qyTokenLive.snapshot(userID)
}

// QyTokenLiveStats 记录「这把 key 现在在被怎么用」。
//
// ────────────────── 挂载位置:紧跟在 TokenAuth 之后 ──────────────────
//
// 也就是**排在限流之前**。被 429 挡掉的请求一样计入"近 1 分钟":这一列要回答的
// 是「我这把 key 现在在被怎么用」,而"我明明发了很多请求却被限流了"正是它最常
// 被用来回答的问题。把被限流的请求藏起来,用户看到的是一个远低于自己实际发送量
// 的数字,于是这一列在最该说话的时候恰好哑掉。
//
// 在途数不受这个位置影响:被限流的请求当场返回,占位时间可以忽略。
//
// ────────────────── 挂在哪几条路由 ──────────────────
//
// 只挂**转发**入口(router/relay-router.go 与 router/video-router.go 里那几个
// TokenAuth 组)。/v1/models、/v1beta/models 这类只读模型清单刻意不计:
// 它们不往上游发请求,把它们算进"并发"会让一个只是在刷新模型列表的客户端
// 看起来正在占用配额。
func QyTokenLiveStats() gin.HandlerFunc {
	return func(c *gin.Context) {
		release := qyTokenLive.enter(
			common.GetContextKeyInt(c, constant.ContextKeyTokenId),
			c.GetInt("id"),
		)
		if release != nil {
			defer release()
		}
		c.Next()
	}
}
