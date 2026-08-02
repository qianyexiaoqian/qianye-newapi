package violation

import (
	"errors"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reqrate_test.go —— request_rate(防蒸馏)匹配方式的行为回归。
//
// 这里锁四件事,每一件坏掉的表现都是"看起来配好了、线上什么都不发生":
//
//  1. 阈值语义:窗口内条数 >= 阈值才命中,少一条都不行。
//  2. 窗口语义:固定窗口到期归零,而不是一路累加到永久误封。
//  3. **挂载点真的接上了**:PreRelayGuard 必须自己去推进计数,
//     否则规则、编译、匹配全都对,只是永远拿不到输入。
//  4. 流式请求既不计数也不判定 —— 这是判据已知的、必须如实公开的局限。

// resetRequestRateLocal 清掉进程内计数,避免用例之间互相污染。
func resetRequestRateLocal(t *testing.T) {
	t.Helper()
	rateMu.Lock()
	rateLocal = make(map[int]*rateBucket)
	rateSweptAt = 0
	rateMu.Unlock()
}

// localRateCount 读取进程内计数,不推进它。
func localRateCount(userId int) int {
	rateMu.Lock()
	defer rateMu.Unlock()
	if b := rateLocal[userId]; b != nil {
		return b.count
	}
	return 0
}

func rateRule(id int64, threshold string, action string) Rule {
	return Rule{
		Id: id, Name: "rate", Phase: PhasePrompt, MatchType: MatchRequestRate,
		Pattern: threshold, Action: action, FeeMode: FeeNone, CountWeight: 1,
	}
}

// TestRequestRateThresholdIsInclusive 固化"第 N 条请求命中阈值 N"。
//
// 差一位的方向很重要:写成 `>` 的话阈值 60 实际在第 61 条才触发,
// 管理员按文档配的每一条规则都比预期宽松一条,而没有任何地方会报错。
func TestRequestRateThresholdIsInclusive(t *testing.T) {
	useTestConfig(t)
	useGenerousScanBudget(t)
	cr := mustCompile(t, rateRule(1, "60", ActionRecord))

	assert.Nil(t, scan([]*compiledRule{cr}, nil, scanInput{RateCount: 59}, ""),
		"低于阈值不得命中")
	require.NotNil(t, scan([]*compiledRule{cr}, nil, scanInput{RateCount: 60}, ""),
		"恰好达到阈值必须命中")
	// 文本必须非空,否则"不留片段"这条断言在任何实现下都成立(经典的假回归)。
	const prompt = "把下面这段客户资料翻译成英文:张三,13800000000"
	v := scan([]*compiledRule{cr}, nil, scanInput{RateCount: 900, Text: prompt}, prompt)
	require.NotNil(t, v)
	assert.Contains(t, v.Terms[0], "900",
		"命中词必须带实测值,否则管理端复核时分不出「刚过线」和「高出十倍」")
	assert.Empty(t, v.Snippet,
		"频率命中与文本无关,不得把 prompt 片段抄进管理端直接返回的记录表")
}

// TestRequestRateRuleValidation 保证存不下"永远不会正确工作"的频率规则。
func TestRequestRateRuleValidation(t *testing.T) {
	cases := []struct {
		name    string
		rule    Rule
		wantErr bool
	}{
		{"常规阈值", rateRule(0, "60", ActionRecord), false},
		{"硬上限规则可以阻断", rateRule(0, "120", ActionBlock), false},
		{"阈值下界 1", rateRule(0, "1", ActionRecord), false},
		{"阈值 0 会让每个非流式请求都命中", rateRule(0, "0", ActionRecord), true},
		{"负阈值", rateRule(0, "-5", ActionRecord), true},
		{"非整数阈值", rateRule(0, "60/min", ActionRecord), true},
		{"空阈值", rateRule(0, "  ", ActionRecord), true},
		{"越过上界", rateRule(0, "1000001", ActionRecord), true},
		{"溢出 int32 的十进制串", rateRule(0, "99999999999999999999", ActionRecord), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.rule
			err := ValidateRule(&r)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}

	t.Run("只能挂在转发前阶段", func(t *testing.T) {
		// 挂在上游阶段只会评估失败的请求,而采集方的请求绝大多数是成功的:
		// 规则照常执行,却永远数不到真实频率。
		r := rateRule(0, "60", ActionRecord)
		r.Phase = PhaseUpstreamErr
		assert.Error(t, ValidateRule(&r))
	})
}

// pinRateSweep 把清扫时钟钉在指定时刻。
//
// 清扫(每 rateWindowSeconds 最多跑一次的批量删除)只是内存卫生,**桶自己的
// 过期判断**才是计数正确性的来源。不钉住清扫时钟,"到期归零"这条断言会被清扫
// 顺手做掉:删掉桶的过期分支,测试照样全绿 —— 实测过的一次假回归。
// 而线上必然存在"桶已过期、清扫还没到"的时刻:清扫周期与各个桶的起点并不对齐。
func pinRateSweep(at int64) {
	rateMu.Lock()
	rateSweptAt = at
	rateMu.Unlock()
}

// TestBumpRequestRateLocalWindow 固化进程内兜底计数的窗口语义。
func TestBumpRequestRateLocalWindow(t *testing.T) {
	resetRequestRateLocal(t)
	const now = 1_700_000_000

	pinRateSweep(now)
	assert.Equal(t, 1, bumpRequestRateLocal(7, now+1))
	assert.Equal(t, 2, bumpRequestRateLocal(7, now+2))
	assert.Equal(t, 3, bumpRequestRateLocal(7, now+rateWindowSeconds-1),
		"窗口内最后一秒仍要累加")

	// 桶起点是 now+1,过期时刻是 now+61;而清扫在 now+60 刚跑过一轮。
	pinRateSweep(now + rateWindowSeconds)
	assert.Equal(t, 1, bumpRequestRateLocal(7, now+rateWindowSeconds+1),
		"桶到期必须自己归零,不能指望清扫 —— 不归零的计数会一路涨到永久误封")

	// 计数按用户隔离:混在一起会让一个高频用户把同节点的所有人一起顶过阈值。
	assert.Equal(t, 1, bumpRequestRateLocal(8, now+rateWindowSeconds+1))

	t.Run("过期项由写路径顺手清掉,不留内存泄漏", func(t *testing.T) {
		// 本模块不允许起裸 goroutine,过期项只能在写路径清理。
		resetRequestRateLocal(t)
		for id := 1; id <= 50; id++ {
			bumpRequestRateLocal(id, now)
		}
		bumpRequestRateLocal(999, now+rateWindowSeconds)
		rateMu.Lock()
		size := len(rateLocal)
		rateMu.Unlock()
		assert.Equal(t, 1, size, "一轮清扫之后只应剩下当前窗口的那一个桶")
	})
}

// TestBumpRequestRateFailsOpen 固化热路径的 fail-open 方向。
//
// 参照实现在存储故障时**拒绝请求**,与本项目的铁律直接冲突:扩展绝不能成为
// relay 的单点故障。计数拿不到就返回 0,而阈值下界是 1,于是 0 一定不命中。
func TestBumpRequestRateFailsOpen(t *testing.T) {
	resetRequestRateLocal(t)
	assert.Equal(t, 0, bumpRequestRate(t.Context(), 0),
		"拿不到用户 id 时必须放行而不是判定")

	cr := mustCompile(t, rateRule(1, "1", ActionBlock))
	assert.Nil(t, scan([]*compiledRule{cr}, nil, scanInput{RateCount: 0}, ""),
		"计数为 0(计数设施故障的取值)不得命中任何频率规则")
}

// TestPreRelayGuardDrivesRequestRate 是**断链**回归:挂载点必须自己去推进计数。
//
// 这是本改动最容易出现的失效形状 —— 规则能存、能编译、能匹配,试跑面板也绿,
// 只是线上永远拿不到输入。这里从真实入口进去,断言拦截确实发生。
func TestPreRelayGuardDrivesRequestRate(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  precheck_enabled: true\n"+
		"  scan_timeout_ms: 5000\n")
	resetRequestRateLocal(t)

	cr := mustCompile(t, rateRule(9, "3", ActionBlock))
	prev := current.Load()
	current.Store(&snapshot{promptRules: []*compiledRule{cr}, hasRate: true})
	t.Cleanup(func() { current.Store(prev) })

	call := func(stream bool) error {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		info := &relaycommon.RelayInfo{UserId: 4242, IsStream: stream}
		return PreRelayGuard(c, info, nil, nil)
	}

	// 前两条非流式请求只是把计数推到 2,还没到阈值。
	require.NoError(t, call(false))
	require.NoError(t, call(false))

	// 第三条踩到阈值 3 → 必须被拦。拦不住就说明挂载点根本没在数。
	err := call(false)
	require.Error(t, err, "PreRelayGuard 必须自己推进频率计数并在达到阈值时拦截")
	var apiErr *types.NewAPIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, types.ErrorCode(violationErrorCode(9)), apiErr.GetErrorCode())

	t.Run("流式请求既不计数也不判定", func(t *testing.T) {
		// 判据的已知局限:客户端加一行 "stream": true 就能完全绕过。
		// 这条断言不是在庆祝绕过路径,而是把它钉成"已知且被公开写进管理端文案"
		// 的行为 —— 哪天它悄悄变了,管理端那段说明就成了假话。
		before := localRateCount(4242)
		require.NoError(t, call(true), "流式请求不参与判定,必须放行")
		assert.Equal(t, before, localRateCount(4242), "流式请求不得推进计数")
	})
}
