// Package relayguard 是上游 service.QyPreRelayGuard 这**一个**插槽的分发器。
//
// # 为什么需要它
//
// 上游 controller/relay.go 里只有一行挂载点,而它对应的是一个**单槽变量**:
//
//	err = service.QyPreRelayGuard(c, relayInfo, meta, err)
//
// 第一个接进来的模块(违规检测)当时直接写 `service.QyPreRelayGuard = PreRelayGuard`。
// 那在只有一个消费方时没有问题,第二个消费方出现的那一刻就变成了**静默互斥**:
// 后赋值的那一个把前一个整个顶掉,而编译、单测、启动日志全部正常,唯一的表现是
// 其中一个功能线上零命中。同样的形状已经在 model.QyOnConsumeLog 上被记过一笔
// (见 qianye/modules/lottery/module.go 的注释:「那是单槽变量,绝不去抢」)——
// 那一次的绕法是"另一个模块自己扫库",这一次绕不开:风控预警要的正是热路径上
// 那一刻的提示词文本,只有这个挂载点拿得到。
//
// 于是把插槽本身收进来:模块登记,分发器持有,上游那一行一个字都不改。
//
// # 观察者排在闸门之前
//
// 两类挂钩的次序是契约,不是实现细节:
//
//	Observer  只观察,永不拦截 —— 先跑
//	Gate      可以拒绝这次请求 —— 后跑,第一个报错即中止
//
// 反过来(闸门先跑)会让**被拦下来的那一次请求完全不进观察**,而那恰恰是风控
// 最想看到的一条:一个账号连续撞违规词、每次都被拒,监听记录里却一条都没有。
//
// # 自调用断路器住在这里
//
// AI 审核会向"某个渠道"发出站请求。渠道的 base_url 误填成本站自己时(演示环境
// 最容易发生的一次误配),那一次出站请求会重新走进 relay —— 于是本进程的风控
// 会去审自己发出的审核请求,而请求体里是**另一个用户**的原文,记录却挂在持有
// 那把 key 的账号上。谁都解释不了它是怎么来的。
//
// 断法是出站时带上本进程启动时生成的随机令牌,入口处看到即整条放过。令牌与
// header 名从违规模块搬到这里,理由是它已经有第二个读者了:一份"本进程发给
// 自己的调用"的判据抄成两份,漏掉的那一份不会报错,只会开始记录别人的内容。
//
// 令牌是随机数而不是固定值:固定值等于给所有客户端发了一张"加一行 header 即可
// 关掉全部前置风控"的门票。
package relayguard

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// SelfCallHeader 是自调用断路器的请求头。
//
// 名字里仍然写着 ai-review 是刻意的:它已经在线上跑了若干个版本,改名不会带来
// 任何好处,却会在灰度期间制造一个"新旧两个进程互相认不出对方"的窗口 ——
// 而那个窗口里发生的正是这个 header 要防的事。
const SelfCallHeader = "X-Qy-Ai-Review-Loopguard"

// selfCallToken 是本进程的随机令牌,进程内唯一。
var selfCallToken = newSelfCallToken()

func newSelfCallToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// 拿不到随机数时退回一个**仍然唯一、但不可预测性弱**的值:断路器继续
		// 在本进程内成立(出站与入站比的是同一个变量),而外部要伪造它得先猜中
		// 本进程启动那一纳秒。退回一个固定串才是错的 —— 那会变成一条"加一行
		// header 即可关掉全部前置风控"的绕过通道。
		return "disabled-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(buf)
}

// SelfCallToken 返回本进程的令牌。
func SelfCallToken() string { return selfCallToken }

// MarkSelfCall 给一个由本进程发出的出站请求打上令牌。
func MarkSelfCall(h http.Header) {
	if h == nil {
		return
	}
	h.Set(SelfCallHeader, selfCallToken)
}

// IsSelfCall 判定这次入站请求是不是本进程自己发出的。
func IsSelfCall(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return c.Request.Header.Get(SelfCallHeader) == selfCallToken
}

// Observer 是"只观察、永不拦截"的前置挂钩。
//
// 没有返回值不是省事:有返回值就迟早有人在里面 return 一个 error,而观察者
// 排在闸门之前,那等于让一个旁路功能获得拦截主业务请求的能力。
type Observer func(c *gin.Context, info *relaycommon.RelayInfo, meta *types.TokenCountMeta)

// Gate 是可以拒绝这次请求的前置闸门。返回非 nil 即拦截,后续闸门不再执行。
type Gate func(c *gin.Context, info *relaycommon.RelayInfo, meta *types.TokenCountMeta) error

type observerEntry struct {
	name string
	fn   Observer
}

type gateEntry struct {
	name string
	fn   Gate
}

var (
	mu        sync.RWMutex
	observers []observerEntry
	gates     []gateEntry
)

// RegisterObserver 登记一个观察者。由模块的 InstallHooks 调用。
func RegisterObserver(name string, fn Observer) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	observers = append(observers, observerEntry{name: name, fn: fn})
}

// RegisterGate 登记一个闸门。由模块的 InstallHooks 调用。
func RegisterGate(name string, fn Gate) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	gates = append(gates, gateEntry{name: name, fn: fn})
}

// Install 把分发器接到上游插槽上。挂载点在 bootstrap.Init 的 InstallHooks 之后。
//
// 一个挂钩都没登记时**不接**:那时上游那个 no-op 就是最省的实现,而接上去
// 会让每一次 relay 多走一层函数调用与两次空切片遍历。
func Install() {
	mu.RLock()
	empty := len(observers) == 0 && len(gates) == 0
	mu.RUnlock()
	if empty {
		return
	}
	service.QyPreRelayGuard = Dispatch
}

// Dispatch 是真正被 relay 调用的那一个函数。
//
// upstreamErr 是 ModelPriceHelper 的错误,原样透传:调用点复用了上游已有的
// `if err != nil` 分支,因此上游已经失败时这里必须什么都不做。这一条以前抄在
// 每个消费方里,现在只剩这一处。
func Dispatch(c *gin.Context, info *relaycommon.RelayInfo, meta *types.TokenCountMeta, upstreamErr error) error {
	if upstreamErr != nil {
		return upstreamErr
	}
	if c == nil || info == nil {
		return nil
	}
	// 自调用整条放过,观察者也不例外:那一次请求的内容属于**别人**,把它记进
	// 持有 key 的那个账号名下,比不记要坏得多。
	if IsSelfCall(c) {
		return nil
	}

	mu.RLock()
	obs, gts := observers, gates
	mu.RUnlock()

	for _, o := range obs {
		o.fn(c, info, meta)
	}
	for _, g := range gts {
		if err := g.fn(c, info, meta); err != nil {
			return err
		}
	}
	return nil
}
