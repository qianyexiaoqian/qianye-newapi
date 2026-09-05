package middleware

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

// qy_rate_limit_export.go —— 纯新增文件,合并上游时冲突为 0。
//
// 它做两件事:
//  1. 把「按分组限流」的分组口径从**模型分组**纠正回**用户分组**;
//  2. 新增一道「按用户分组的在途并发上限」。
//
// ────────────────── 为什么分组口径是错的 ──────────────────
//
// 上游 middleware/model-rate-limit.go 取分组的写法是「令牌分组优先、用户分组兜底」。
// 在上游那套里 users.group 同时兼作模型分组,两者同名,这段代码看不出区别。
//
// 本 fork 把两者**拆开**了(qy_user_groups 与 qy_model_groups 是两张表,
// controller/group.go 的 GetUserGroupOptions / GetModelGroupOptions 是两个接口),
// 于是这段代码的实际语义塌成了「模型分组优先」:
//
//   - 令牌选了模型分组 vip   → 限流查的是 ModelRequestRateLimitGroup["vip"],
//     而运营在页面上填的是**用户分组**名,那一行静默失效;
//   - 令牌是 auto           → ContextKeyTokenGroup 字面量就是 "auto",于是
//     **全站所有 auto 令牌共用同一个桶**,谁都限不住谁;
//   - 只有"令牌没选分组"这一档,兜底分支才碰巧查到用户分组。
//
// 而页面自己的文案从上游起就写的是 "for a specific user group"。也就是说
// 实现与它自己声明的意图从来就不一致,fork 的分组拆分只是把它暴露了出来。
//
// ────────────────── 纠正之后的口径 ──────────────────
//
// **先查用户分组,查不到再按上游原样的键查一次。**
//
// 第二次查不是含糊,是**升级兼容**:升级前配好的表里,键有可能是模型分组名或
// "auto"。直接改口径会让那些行在升级那一刻集体失效 —— 限流是安全设施,
// 静默失效比配错更糟。而两个命名空间在本 fork 里是分开的,一个键要么是用户分组
// 要么不是,回落那一次不可能把「用户分组的配置」错配到别处。
//
// 用户分组取 ContextKeyUserGroup(= users.group)。**不能取 ContextKeyUsingGroup** ——
// 那是鉴权阶段解析出来的**模型分组**,auto 令牌下还会被 relay 改写。
func QyRateLimitGroup(c *gin.Context, upstreamGroup string) string {
	userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	if userGroup == "" {
		return upstreamGroup
	}
	if _, _, found := setting.GetGroupRateLimit(userGroup); found {
		return userGroup
	}
	// 用户分组没配,回落到上游那个键(可能是模型分组名或 auto)。
	// 两者都没配时返回哪个都一样:调用方查不到,用全站默认值。
	if upstreamGroup != "" {
		if _, _, found := setting.GetGroupRateLimit(upstreamGroup); found {
			return upstreamGroup
		}
	}
	return userGroup
}

// ─────────────────────── 分组并发上限 ───────────────────────

// qyConcurrencyCounter 是「每个用户分组当前有几个请求在飞」的进程内计数。
//
// # 为什么是进程内而不是 Redis
//
// 同文件上方那套 RPM 限流在 Redis 可用时是**跨节点**的,这里刻意不是,而且
// 这个差别必须写在界面上(见 i18n 的 qy_rate_limit_concurrency_hint):
//
// 并发计数与 RPM 计数的失败形态完全不同。RPM 是只增不减的固定窗口计数,
// 一个节点崩了,它在窗口里留下的计数最多让这一分钟少放几个请求,窗口一过自愈。
// 并发计数要求**每一次 acquire 都有配对的 release**:节点崩在请求中途时,
// Redis 里那几个占位不会有人来还,那个分组的并发额度就永久少掉几个,
// 而且没有任何迹象。要做对就得给每个在途请求一个带时间戳的成员、按 TTL 清扫、
// 再用 Lua 把「清扫 + 计数 + 占位」三步做成原子的 —— 那是一整套东西,
// 而本机与目标部署都没有 Redis,写出来一行都测不到。
//
// 所以这里给的是一个**语义明确、可被完整测试**的东西:按节点计数。
// 单节点部署下它就是字面意思;多节点部署下,配 N 的实际效果是「每节点 N」。
// 这句话进配置页的提示文案,而不是留在代码注释里让运营去猜。
type qyConcurrencyCounter struct {
	mu       sync.Mutex
	inFlight map[string]int
}

var qyConcurrency = &qyConcurrencyCounter{inFlight: map[string]int{}}

// acquire 尝试占一个位。limit <= 0 表示不限,此时不计数也不需要归还。
//
// 返回的 release **必须**被调用,且只能调用一次。中间件用 defer 保证这一点:
// 即使 handler panic,defer 也会在栈展开时跑到(gin 的 Recovery 在更外层)。
func (q *qyConcurrencyCounter) acquire(group string, limit int) (release func(), ok bool) {
	if limit <= 0 || group == "" {
		return nil, true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.inFlight[group] >= limit {
		return nil, false
	}
	q.inFlight[group]++

	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			if q.inFlight[group] <= 1 {
				// 删键而不是留一个 0:分组可以被运营改名/删除,
				// 留着零值会让这张表随时间只增不减。
				delete(q.inFlight, group)
				return
			}
			q.inFlight[group]--
		})
	}, true
}

// qyInFlight 只给测试用:读某个分组当前的在途数。
func qyInFlight(group string) int {
	qyConcurrency.mu.Lock()
	defer qyConcurrency.mu.Unlock()
	return qyConcurrency.inFlight[group]
}

// QyModelRequestConcurrencyLimit 按**用户分组**限制同时在飞的请求数。
//
// 它与 ModelRequestRateLimit 是两道独立的闸,各有各的开关:RPM 管「一分钟准入
// 几次」,并发管「同一时刻最多几个在跑」。一个跑 5 分钟的长上下文请求在 RPM 表里
// 只占一次计数,却实打实占住上游一条连接 5 分钟 —— 只配 RPM 的站点会在
// 「远未达到上限」的面板读数下把上游打满。
//
// 超限回 429 而不是排队:排队会把压力从上游挪到本站的内存里,而一个在队列里
// 等了两分钟才开始的请求,对调用方来说与超时没有区别,却多烧了两分钟连接。
func QyModelRequestConcurrencyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		group := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		limit := setting.GetGroupConcurrencyLimit(group)
		release, ok := qyConcurrency.acquire(group, limit)
		if !ok {
			abortWithOpenAiMessage(c, http.StatusTooManyRequests, fmt.Sprintf(
				"当前分组的并发请求数已达上限(%d),请等待正在进行的请求结束后再试", limit))
			return
		}
		if release != nil {
			defer release()
		}
		c.Next()
	}
}
