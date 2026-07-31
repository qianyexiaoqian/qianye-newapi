// Package logmetrics 给使用日志补齐「推理强度」与「缓存百分比」两列所需的数据。
//
// # 为什么必须动上游而不是旁路
//
// 两列里只有「推理强度」的一部分现成可用(relayInfo.ReasoningEffort,由 OpenAI /
// DeepSeek / xAI / Gemini level 几条 adaptor 分支写入)。Claude 原生 thinking、
// Gemini 数值 thinkingBudget、Qwen thinking_budget、以及非 o 系列的通用 OpenAI
// 兼容渠道,全都拿不到 —— 纯前端方案会让这些行永远空白。
//
// 「缓存百分比」的分子(cached_tokens)一直有,分母没有:prompt_tokens 是否已经
// 包含 cached_tokens 取决于计费语义,而 IsClaudeUsageSemantic 只在 relay 内部算得出。
// 旁路中间件跑在 c.Next() 之后,usage 早已被消费,只能自己重新推断 —— 为规避
// 语义陷阱而选的方案反而必须再踩一遍同一个陷阱。
//
// # 落地形态
//
// 两个 hook 变量(service/qy_logmetrics_export.go)+ 上游两个文件各一行调用。
// 本模块不新建任何数据表,不访问扩展库,数据全部写进上游 logs.other 的 qy_ 前缀键。
// 因此扩展库掉线不影响这两列 —— 这是路线 A 相对旁路方案的额外收益。
//
// # 老日志如何处理
//
// 本模块上线前的日志既没有 qy_ver 也没有可靠的语义标记,分母不可判定。
// 处理原则是宁可显示「—」也绝不给出一个可能错误的百分比:错的数字用户会当真,
// 而且落在 0-100% 区间时静默出错,永远不会被发现。qy_ver 就是那条正向水位线。
package logmetrics

import (
	"fmt"
	"math"
	"runtime/debug"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// LogVersion 是写入 logs.other 的版本水位线。
//
// 前端据此区分「本模块处理过的日志」与「历史日志」,是缓存率降级决策树的锚点。
// 用逐行标记而非全局 epoch 时间戳:后者在重启、回滚、多节点灰度时都会失真。
//
// 递增规则:只有当 qy_* 键的语义发生不兼容变化时才 +1;新增键不递增。
const LogVersion = 1

// logs.other 的键名。全部平铺在顶层而不放进 admin_info ——
// 与现有的 reasoning_effort / cache_tokens 可见性保持一致,普通用户在自己的
// 日志里能看见。所有值都是整数或枚举字符串,不含请求内容、PII 或渠道信息。
const (
	KeyVer          = "qy_ver"
	KeyReasoning    = "qy_reasoning"
	KeySemantic     = "qy_semantic"
	KeyInputTotal   = "qy_input_total"
	KeyCacheRead    = "qy_cache_read"
	KeyCacheWrite   = "qy_cache_write"
	KeyCacheAnomaly = "qy_cache_anomaly"
)

// 计费语义的正向双向标记。
//
// 上游的 other["usage_semantic"] 只在 anthropic 时写,缺失是个二义状态
// (「新的 OpenAI 语义日志」∪「该键上线前的全部日志」),无法用来判别。
// 本模块两个方向都写,让缺失只意味着一件事:本模块没跑过。
const (
	SemanticAnthropic = "anthropic"
	SemanticOpenAI    = "openai"
)

// Mod 是本模块在扩展模块注册表中的实现。
type Mod struct{ module.Base }

func (Mod) Name() string { return "logmetrics" }

// InstallHooks 把实现注入上游 service 包的 hook 变量。
//
// 注入时机在 qianye.Init() 内,早于 HTTP 监听与后台协程,因此这里的普通赋值
// 不存在并发读写窗口。运行期禁止再改写这些变量。
func (Mod) InstallHooks() {
	service.QyLogMetricsAttachReasoning = AttachReasoning
	service.QyLogMetricsAttachCacheBasis = AttachCacheBasis
	hooksInstalled.Store(true)
	installedAt.Store(common.GetTimestamp())
}

// RegisterAdminRoutes 注册自检端点。
//
// 两个 hook 都是静默 fail-open 的:出错时零用户可见症状。没有这个端点,
// 「两列一直是空的」到底是「没人用思考模型」还是「hook 根本没生效」无法区分。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/log-metrics/health", Health)
}

func init() { module.Register(Mod{}) }

// ───────────────────────────── HOOK A:推理强度 ─────────────────────────────

// AttachReasoning 把归一化后的推理强度写进 other["qy_reasoning"],
// 并无条件打上版本水位线 other["qy_ver"]。
//
// ctx 目前未使用。保留它是因为这个签名属于上游耦合面:日后若需要读某个
// context key,改签名就得再动一次上游文件,而多带一个参数是零成本的。
func AttachReasoning(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other map[string]interface{}) {
	showReasoning, showCacheRatio := columnsEnabled()
	if other == nil || (!showReasoning && !showCacheRatio) {
		return
	}
	defer recoverHook("attach_reasoning")

	statTotal.Add(1)
	// 水位线与具体哪一列开着无关:它标记的是「这条日志由本模块处理过」,
	// 前端两列的降级判断都依赖它。
	other[KeyVer] = LogVersion

	if !showReasoning {
		return
	}
	r := detectReasoning(relayInfo)
	if r == nil {
		statMiss.Add(1)
		return
	}
	if r.Src == SrcRelayInfo {
		statFromRelayInfo.Add(1)
	} else {
		statFromRequest.Add(1)
	}
	other[KeyReasoning] = map[string]interface{}{
		"level":  r.Level,
		"raw":    r.Raw,
		"budget": r.Budget,
		"src":    r.Src,
	}
}

// ───────────────────────────── HOOK B:缓存分母 ─────────────────────────────

// AttachCacheBasis 把缓存百分比的权威分母固化进 other。
//
// 存分子分母而不是存算好的百分比:前端可以按需展示「12.3%」或「1,024 / 8,320」
// 两种形态,未来做聚合统计(平均命中率)也能正确加权求和。存结果会丢信息。
func AttachCacheBasis(other map[string]interface{}, promptTokens, cacheReadTokens, cacheWriteTokens int, isClaudeUsageSemantic bool) {
	if _, showCacheRatio := columnsEnabled(); other == nil || !showCacheRatio {
		return
	}
	defer recoverHook("attach_cache_basis")

	statCacheBasis.Add(1)
	// 无条件写:它同时是「分母已固化」的标记。缺失即代表本 hook 没跑过
	// (例如 MJ / 异步任务这类不走 PostTextConsumeQuota 的日志)。
	if isClaudeUsageSemantic {
		other[KeySemantic] = SemanticAnthropic
	} else {
		other[KeySemantic] = SemanticOpenAI
	}

	basis := computeCacheBasis(promptTokens, cacheReadTokens, cacheWriteTokens, isClaudeUsageSemantic)
	if basis.InputTotal > 0 {
		other[KeyInputTotal] = basis.InputTotal
	}
	if basis.CacheRead > 0 {
		other[KeyCacheRead] = basis.CacheRead
	}
	if basis.CacheWrite > 0 {
		other[KeyCacheWrite] = basis.CacheWrite
	}
	if basis.Anomaly {
		statAnomaly.Add(1)
		other[KeyCacheAnomaly] = true
	}
}

// ───────────────────────────── 开关与降级 ─────────────────────────────

// columnsEnabled 返回两列各自的开关状态。
//
// 刻意不走 guard.Feature:那要求扩展库可用,而本模块完全不碰扩展库 ——
// 数据直接写进上游的 logs.other。让日志两列因为扩展库掉线而缺数据,
// 是一次没有任何必要的降级,而且缺的那几条日志再也补不回来。
func columnsEnabled() (reasoning, cacheRatio bool) {
	if !config.Enabled() {
		return false, false
	}
	c := config.Get().LogMetrics
	return c.ReasoningColumn(), c.CacheRatioColumn()
}

// recoverHook 是两个 hook 的最后一道闸。
//
// 两列是锦上添花,消费日志是账本。上游 usage 结构畸形、DTO 类型断言意外、
// 客户端传了畸形 JSON —— 任一 panic 逃逸都会让这条计费日志彻底丢失,
// 那是不可接受的严重故障,所以一律吞掉。
//
// 告警按累计次数降频:上游一旦持续返回畸形数据,逐条打日志会在几分钟内刷爆磁盘。
func recoverHook(name string) {
	r := recover()
	if r == nil {
		return
	}
	n := statPanic.Add(1)
	if n == 1 || n == 100 || n%1000 == 0 {
		common.SysError(fmt.Sprintf(
			"qianye/logmetrics: %s 发生 panic(已拦截,计费日志不受影响),累计 %d 次: %v\n%s",
			name, n, r, debug.Stack()))
	}
}

// ───────────────────────────── 进程内计数器 ─────────────────────────────
//
// 刻意用累计值而非 1 小时滑窗:自检要回答的问题是「hook 到底跑没跑」,
// 滑窗在低峰期读到 0 反而会被误判成 hook 失效。多节点各报各的,不聚合、不落库。

var (
	hooksInstalled atomic.Bool
	installedAt    atomic.Int64

	statTotal         atomic.Int64 // 经过 HOOK A 的消费日志数
	statFromRelayInfo atomic.Int64 // 命中 relayInfo.ReasoningEffort(零解析开销)
	statFromRequest   atomic.Int64 // 由原始请求 DTO 探测得出
	statMiss          atomic.Int64 // 未探测到任何思考参数(绝大多数普通请求属于此类)
	statCacheBasis    atomic.Int64 // 经过 HOOK B 的日志数
	statAnomaly       atomic.Int64 // 上游 usage 自相矛盾(分子 > 分母 / 负数 / 溢出)
	statPanic         atomic.Int64 // 被拦截的 panic 次数,非 0 即需排查
)

// maxTokenCount 是 token 计数写入 logs.other 前的上界。
//
// logs 的相关列与主库 quota 一样是 32 位,更重要的是上游返回的 usage 完全
// 由外部服务控制,可能是畸形大值。这里只做钳制不做换算,不涉及任何金额。
const maxTokenCount = math.MaxInt32
