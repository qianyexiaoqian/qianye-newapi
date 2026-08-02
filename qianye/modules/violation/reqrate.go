package violation

import (
	"context"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// reqrate.go —— request_rate 规则的数据源:单用户非流式请求的短窗计数。
//
// # 为什么不新造一套滑动窗口
//
// 本仓库已经有两套现成设施,先看过再决定:
//
//   - common/limiter(Redis 令牌桶)只回答"放不放行",拿不到"窗口内有多少条"。
//     多条阈值不同的规则(60/min 记录、120/min 拦截)必须共用同一个计数,
//     用它就得给每条规则各开一个桶,而那些桶各自消耗、互不相干,
//     "这个用户这一分钟发了多少条"这个数就永远不存在了。
//   - common.InMemoryRateLimiter 同样是放行判定,而且是进程内的,没有 Redis 分支。
//
// 所以这里只补最小的一块:**返回计数**的窗口计数器,Redis 优先、进程内兜底。
//
// # 为什么是固定窗口而不是 ZSET 滑动窗口
//
// 固定窗口的计数一定 <= 同一时刻任何滑动窗口的计数(窗口长度相同时,固定窗口
// 覆盖的时间段是某个滑动窗口的子集)。也就是说它**只会漏判、不会误判**。
// 对一条会扣钱封号、且已知存在大量合法高频非流式用户(embedding 批处理、
// 批量分类、agent 工具循环、结构化输出流水线)的判据,这个不对称方向是对的。
// 顺带它复用了 middleware/rate-limit.go 已经在线上跑着的那段 INCR+EXPIRE 形状,
// 而不是引入第二种没人验证过的窗口语义。
const rateWindowSeconds = 60

// rateRedisTimeout 是频率计数允许占用 relay 热路径的时间上界。
//
// 必须有:这是 relay 线程里唯一一次同步网络调用。Redis 抖一下就让全站请求
// 多等一个 DSN 超时是不可接受的,而超时的后果只是这一次少数一条(fail-open)。
const rateRedisTimeout = 200 * time.Millisecond

// rateLocalMaxUsers 是进程内兜底计数的用户数上界。
//
// 正常情况下这张表的大小就等于"最近一分钟活跃的用户数",过期项由写路径顺手清掉。
// 上界只是防止一次异常流量把它撑成内存泄漏:超过之后不再为新用户建桶
// (返回 0 = 不判定),而不是无限增长。
const rateLocalMaxUsers = 100_000

// rateWindowScript 原子地推进一次计数并返回窗口内的条数。
//
// TTL < 0 的补救分支不是多余的:INCR 成功之后进程崩溃或 EXPIRE 被单独失败时,
// 这个键会永久存在,该用户从此每一个请求都在同一个"窗口"里累加,几小时后
// 必然越过任何阈值 —— 一个只影响个别用户、且完全不会报错的永久误封。
const rateWindowScript = `
local c = redis.call('INCR', KEYS[1])
if c == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
  return c
end
if redis.call('TTL', KEYS[1]) < 0 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return c
`

// rateKeyPrefix 与主程序的 "rateLimit:v2" 命名空间刻意分开:两者的窗口语义、
// 清理时机与所有者都不同,共用前缀会让任何一次 KEYS/SCAN 排障把它们混为一谈。
const rateKeyPrefix = "qy:vio:reqrate:"

type rateBucket struct {
	start int64
	count int
}

var (
	// rateRedisFails 是 Redis 计数失败次数,rateLocalHits 是落到进程内兜底的次数。
	//
	// 两个都要暴露给管理端:进程内计数是**每节点各数各的**,N 个节点部署时
	// 单节点看到的条数只有真实值的约 1/N,阈值等于被放大了 N 倍。运营必须能一眼
	// 看出"我现在到底在哪种模式下调阈值",否则会一路往下调,直到某天 Redis
	// 恢复、真实计数回来,一次性误伤一大批人。
	rateRedisFails atomic.Int64
	rateLocalHits  atomic.Int64
	// rateLocalFull 是因为兜底表已满而放弃计数的次数。
	rateLocalFull atomic.Int64

	rateMu      sync.Mutex
	rateLocal   = make(map[int]*rateBucket)
	rateSweptAt int64
)

// bumpRequestRate 推进用户的非流式请求计数并返回窗口内条数(含本次)。
//
// **热路径 fail-open**:任何失败都返回 0,而 compile 保证阈值下界是 1,
// 于是 0 不可能命中任何频率规则 —— 计数设施故障时请求一律放行。
// 这与参照实现相反(它在存储故障时拒绝请求),而本项目的铁律是扩展绝不能
// 成为 relay 的单点故障:风控是附加物,转发是主业务。
func bumpRequestRate(ctx context.Context, userId int) int {
	if userId <= 0 {
		return 0
	}
	if common.RedisEnabled && common.RDB != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		cctx, cancel := context.WithTimeout(ctx, rateRedisTimeout)
		defer cancel()
		n, err := common.RDB.Eval(cctx, rateWindowScript,
			[]string{rateKeyPrefix + strconv.Itoa(userId)}, rateWindowSeconds).Int64()
		if err == nil {
			if n <= 0 {
				return 0
			}
			if n > math.MaxInt32 {
				return math.MaxInt32
			}
			return int(n)
		}
		rateRedisFails.Add(1)
		// 回落进程内而不是直接返回 0:Redis 抖动通常是秒级的,整段停止判定
		// 等于给采集方开一扇窗。进程内计数一定不高于真实值,方向仍然是 fail-open。
	}
	rateLocalHits.Add(1)
	return bumpRequestRateLocal(userId, common.GetTimestamp())
}

// bumpRequestRateLocal 是无 Redis 时的进程内计数。now 由调用方注入以便直接测。
func bumpRequestRateLocal(userId int, now int64) int {
	rateMu.Lock()
	defer rateMu.Unlock()

	// 过期项在写路径顺手清:本模块不允许起裸 goroutine,而清理本身是 O(活跃用户数),
	// 每 rateWindowSeconds 才做一次。
	if now-rateSweptAt >= rateWindowSeconds || len(rateLocal) >= rateLocalMaxUsers {
		for id, b := range rateLocal {
			if now-b.start >= rateWindowSeconds {
				delete(rateLocal, id)
			}
		}
		rateSweptAt = now
	}

	b := rateLocal[userId]
	if b == nil {
		if len(rateLocal) >= rateLocalMaxUsers {
			rateLocalFull.Add(1)
			return 0
		}
		b = &rateBucket{start: now, count: 1}
		rateLocal[userId] = b
		return 1
	}
	if now-b.start >= rateWindowSeconds {
		b.start = now
		b.count = 1
		return 1
	}
	if b.count < math.MaxInt32 {
		b.count++
	}
	return b.count
}
