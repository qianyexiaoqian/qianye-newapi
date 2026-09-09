package relayguard

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relayguard_test.go —— 分发器的三条契约。
//
// 它们都属于"删掉之后什么都不会报错"的那一类:上游透传被删,一次定价失败会
// 变成一次正常转发;观察者与闸门的顺序被调换,被拦下来的请求从此不进取证记录;
// 自调用放行被删,本站的审核请求会把别人的内容记到持有 key 的账号名下。

func resetRegistry(t *testing.T) {
	t.Helper()
	mu.Lock()
	prevObs, prevGates := observers, gates
	observers, gates = nil, nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		observers, gates = prevObs, prevGates
		mu.Unlock()
	})
}

func newCtx(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

// 上游已经失败时,分发器必须原样把那个错误还回去,一个挂钩都不跑。
//
// 这条契约原本抄在每个消费方里(violation 的 PreRelayGuard 顶上),随插槽
// 一起搬到这里之后就只剩这一份。它守的是 relay.go 那个调用点:
// `err = service.QyPreRelayGuard(c, info, meta, err)` 复用了上游已有的
// `if err != nil` 分支,吞掉入参就等于把一次定价失败变成一次正常转发。
func TestDispatchPassesUpstreamErrorThroughUntouched(t *testing.T) {
	resetRegistry(t)
	ran := false
	RegisterObserver("probe", func(*gin.Context, *relaycommon.RelayInfo, *types.TokenCountMeta) { ran = true })

	upstream := errors.New("model price error")
	got := Dispatch(newCtx(t), &relaycommon.RelayInfo{}, nil, upstream)

	assert.Same(t, upstream, got, "上游错误必须原样返回,不能被包装也不能被吞掉")
	assert.False(t, ran, "上游已经失败时一个挂钩都不该跑")
}

// 观察者必须排在闸门之前,而且闸门拦下来之后观察者已经跑完了。
//
// 顺序反过来的症状最隐蔽:功能"正常"(拦截照拦、记录照记),只是**被拦下来的
// 那一批请求**在取证记录里一条都没有 —— 而一个连续撞违规词的账号恰恰全是
// 被拦下来的请求。
func TestObserversRunBeforeGatesAndSeeBlockedRequests(t *testing.T) {
	resetRegistry(t)
	var order []string
	RegisterGate("blocker", func(*gin.Context, *relaycommon.RelayInfo, *types.TokenCountMeta) error {
		order = append(order, "gate")
		return errors.New("blocked")
	})
	RegisterObserver("watcher", func(*gin.Context, *relaycommon.RelayInfo, *types.TokenCountMeta) {
		order = append(order, "observer")
	})

	err := Dispatch(newCtx(t), &relaycommon.RelayInfo{}, nil, nil)

	require.Error(t, err, "闸门返回的错误必须冒泡出去")
	assert.Equal(t, []string{"observer", "gate"}, order,
		"观察者必须先跑:被闸门拦下来的那一次请求正是最该留档的一次")
}

// 第一个报错的闸门中止其余闸门。
//
// 一次请求只按一条规则处置 —— 让第二个闸门继续跑,同一次请求会被扣两次费、
// 记两条违规。
func TestDispatchStopsAtTheFirstFailingGate(t *testing.T) {
	resetRegistry(t)
	second := false
	RegisterGate("first", func(*gin.Context, *relaycommon.RelayInfo, *types.TokenCountMeta) error {
		return errors.New("blocked")
	})
	RegisterGate("second", func(*gin.Context, *relaycommon.RelayInfo, *types.TokenCountMeta) error {
		second = true
		return nil
	})

	require.Error(t, Dispatch(newCtx(t), &relaycommon.RelayInfo{}, nil, nil))
	assert.False(t, second, "第一个闸门拦下之后,后面的闸门不该再跑")
}

// 带着本进程令牌的请求整条放过,观察者也不例外。
//
// 场景:审核渠道的 base_url 被填成本站自己。那一次出站请求的请求体里是**别人**
// 的原文,而它会带着持有那把 key 的账号身份重新进入 relay。放行的是整条链路,
// 不只是审核那一层 —— 把那段内容记进取证记录,比不记要坏得多。
func TestSelfCallSkipsEveryHook(t *testing.T) {
	resetRegistry(t)
	touched := false
	RegisterObserver("watcher", func(*gin.Context, *relaycommon.RelayInfo, *types.TokenCountMeta) { touched = true })
	RegisterGate("blocker", func(*gin.Context, *relaycommon.RelayInfo, *types.TokenCountMeta) error {
		touched = true
		return errors.New("blocked")
	})

	c := newCtx(t)
	MarkSelfCall(c.Request.Header)
	require.True(t, IsSelfCall(c), "MarkSelfCall 写下的头必须被 IsSelfCall 认出来")

	assert.NoError(t, Dispatch(c, &relaycommon.RelayInfo{}, nil, nil))
	assert.False(t, touched, "自调用必须整条放过")
}

// 令牌是随机的,外部猜不到 —— 一个写死的头值不能绕过任何闸门。
func TestForgedSelfCallHeaderIsNotAccepted(t *testing.T) {
	c := newCtx(t)
	c.Request.Header.Set(SelfCallHeader, "1")
	assert.False(t, IsSelfCall(c))

	c.Request.Header.Set(SelfCallHeader, SelfCallToken()[:8])
	assert.False(t, IsSelfCall(c), "令牌前缀不算匹配,必须整串相等")
}
