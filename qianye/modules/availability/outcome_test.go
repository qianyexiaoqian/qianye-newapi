package availability

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 分类是整个模块的地基:它一旦错位,分子分母同时错,而错出来的数字看上去完全合理。
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		in   classifyInput
		want Outcome
	}{
		{
			name: "普通成功",
			in:   classifyInput{Success: true},
			want: OutcomeSuccess,
		},
		{
			name: "流正常收尾(eof)仍是成功",
			in:   classifyInput{Success: true, EndReason: relaycommon.StreamEndReasonEOF},
			want: OutcomeSuccess,
		},
		{
			name: "返回 200 但流中途超时 = 软失败",
			in:   classifyInput{Success: true, EndReason: relaycommon.StreamEndReasonTimeout},
			want: OutcomeSoftFail,
		},
		{
			name: "返回 200 但上游推来错误帧 = 软失败",
			in:   classifyInput{Success: true, StreamErrors: 2},
			want: OutcomeSoftFail,
		},
		{
			name: "渠道测试优先于一切,即使失败也不污染业务统计",
			in: classifyInput{IsChannelTest: true, Err: types.NewErrorWithStatusCode(
				errors.New("boom"), types.ErrorCodeBadResponseStatusCode, 500)},
			want: OutcomeChannelTest,
		},
		{
			name: "客户端断开:成功侧",
			in:   classifyInput{Success: true, EndReason: relaycommon.StreamEndReasonClientGone},
			want: OutcomeClientGone,
		},
		{
			name: "客户端断开:失败侧",
			in: classifyInput{EndReason: relaycommon.StreamEndReasonClientGone,
				Err: types.NewError(context.Canceled, types.ErrorCodeDoRequestFailed)},
			want: OutcomeClientGone,
		},
		{
			name: "该分组无可用渠道",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("no channel"), types.ErrorCodeGetChannelFailed, 503)},
			want: OutcomeNoChannel,
		},
		{
			name: "额度不足永远排除,与 HTTP 状态无关",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("no quota"), types.ErrorCodeInsufficientUserQuota, 403)},
			want: OutcomeQuota,
		},
		{
			name: "预扣费失败同样排除",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("pre consume"), types.ErrorCodePreConsumeTokenQuotaFailed, 403)},
			want: OutcomeQuota,
		},
		{
			name: "违规拦截排除",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("csam"), types.ErrorCodeViolationFeeGrokCSAM, 400)},
			want: OutcomeViolation,
		},
		{
			name: "渠道响应超时",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("slow"), types.ErrorCodeChannelResponseTimeExceeded, 504)},
			want: OutcomeTimeout,
		},
		{
			name: "context deadline 也是超时",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				context.DeadlineExceeded, types.ErrorCodeDoRequestFailed, 500)},
			want: OutcomeTimeout,
		},
		{
			name: "上游 429 归限流,不能被 bad_response_status_code 抢先归成上游故障",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, 429)},
			want: OutcomeRateLimit,
		},
		{
			name: "上游 5xx",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, 502)},
			want: OutcomeUpstream,
		},
		{
			name: "连接上游失败",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("dial fail"), types.ErrorCodeDoRequestFailed, 0)},
			want: OutcomeUpstream,
		},
		{
			name: "渠道密钥失效属于上游不可用",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("invalid key"), types.ErrorCodeChannelInvalidKey, 401)},
			want: OutcomeUpstream,
		},
		{
			name: "用户参数非法",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("bad param"), types.ErrorCodeInvalidRequest, 400)},
			want: OutcomeClientError,
		},
		{
			name: "敏感词拦截算用户请求问题,不算违规扣费",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("sensitive"), types.ErrorCodeSensitiveWordsDetected, 400)},
			want: OutcomeClientError,
		},
		{
			name: "平台自身的 SQL 故障归内部,不能记到上游头上",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("sql"), types.ErrorCodeQueryDataError, 500)},
			want: OutcomeInternal,
		},
		{
			name: "序列化失败(NewError 默认带 500)同样归内部",
			in: classifyInput{Err: types.NewError(
				errors.New("marshal"), types.ErrorCodeJsonMarshalFailed)},
			want: OutcomeInternal,
		},
		{
			name: "无法识别的 5xx 归上游:未标注的 5xx 绝大多数来自上游响应",
			in: classifyInput{Err: types.NewErrorWithStatusCode(
				errors.New("weird"), types.ErrorCode("something_new"), 503)},
			want: OutcomeUpstream,
		},
		{
			name: "失败但没有错误对象,不能静默丢弃",
			in:   classifyInput{Success: false},
			want: OutcomeInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classify(tc.in))
		})
	}
}

// 分母口径必须随开关变化,且硬排除项在任何开关组合下都不得进入分母。
func TestCountedRespectsSwitches(t *testing.T) {
	// ReqTotal 必须等于各类之和,否则「排除了多少」这个减法就失去意义。
	b := &Bucket{
		ReqTotal:        116,
		SuccessCount:    50,
		FailSoftStream:  1,
		FailNoChannel:   2,
		FailTimeout:     3,
		FailUpstream:    4,
		FailInternal:    5,
		FailRateLimit:   6,
		FailClientError: 7,
		ExcQuota:        8,
		ExcViolation:    9,
		ExcClientGone:   10,
		ExcChannelTest:  11,
	}
	base := int64(50 + 1 + 2 + 3 + 4 + 5)

	cases := []struct {
		name       string
		cfg        config.Availability
		wantCounts int64
	}{
		{"默认口径:429 与 4xx 都不计入", config.Availability{}, base},
		{"计入限流", config.Availability{CountRateLimited: true}, base + 6},
		{"计入用户 4xx", config.Availability{CountClientErrors: true}, base + 7},
		{"两者都计入", config.Availability{CountRateLimited: true, CountClientErrors: true}, base + 6 + 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := definitionOf(tc.cfg)
			assert.Equal(t, tc.wantCounts, counted(b, d))
			// 额度不足 / 违规 / 客户端断开 / 渠道测试合计 38 条,
			// 任何开关组合都不能把它们拉进分母。
			assert.LessOrEqual(t, counted(b, d), b.ReqTotal-b.excludedTotal())
		})
	}
}

// 口径清单要能自解释:同一个 Outcome 不能既在分母又在排除项里。
func TestDefinitionListsAreDisjoint(t *testing.T) {
	for _, cfg := range []config.Availability{
		{},
		{CountRateLimited: true},
		{CountClientErrors: true},
		{CountRateLimited: true, CountClientErrors: true},
	} {
		d := definitionOf(cfg)
		inDenominator := map[string]bool{}
		for _, name := range d.Denominator {
			inDenominator[name] = true
		}
		for _, name := range d.Excluded {
			assert.Falsef(t, inDenominator[name], "%s 同时出现在分母与排除项", name)
		}
		assert.Contains(t, d.Denominator, string(OutcomeSuccess))
		assert.Contains(t, d.Excluded, string(OutcomeQuota))
		assert.Contains(t, d.Excluded, string(OutcomeViolation))
		assert.Contains(t, d.Excluded, string(OutcomeChannelTest))
		assert.Contains(t, d.Excluded, string(OutcomeClientGone))
	}
}

// 分母为 0 必须给 nil,绝不能给 0 —— 那会被前端渲染成 0%,让运营误判为全站故障。
func TestAvailabilityOfEmptyDenominator(t *testing.T) {
	assert.Nil(t, availabilityOf(0, 0))
	assert.Nil(t, availabilityOf(5, 0))
	assert.Nil(t, availabilityOf(0, -1))
}

func TestAvailabilityOfRounding(t *testing.T) {
	av := availabilityOf(1, 3)
	require.NotNil(t, av)
	assert.Equal(t, 33.33, *av)

	av = availabilityOf(0, 7)
	require.NotNil(t, av)
	assert.Equal(t, 0.0, *av, "有样本且全失败时,0% 是真实结论而不是空态")
}

// drain 逐字段 Swap 会有极短窗口让 success 超过分母,查询侧必须夹住。
func TestAvailabilityOfClampsRaceArtifact(t *testing.T) {
	av := availabilityOf(11, 10)
	require.NotNil(t, av)
	assert.Equal(t, 100.0, *av)
}

func TestStateOf(t *testing.T) {
	d := definitionOf(config.Availability{})
	full := func(success, upstream int64) *Bucket {
		return &Bucket{ReqTotal: success + upstream, SuccessCount: success, FailUpstream: upstream}
	}

	cases := []struct {
		name       string
		bucket     *Bucket
		hasChannel bool
		wantState  string
		wantAvNil  bool
	}{
		{"该分组没有这个模型", &Bucket{}, false, StateNotOffered, true},
		{"有渠道但时段内无请求", &Bucket{}, true, StateNoData, true},
		{"样本不足不给百分比", full(1, 0), true, StateLowSample, true},
		{"全部样本都被排除时同样是样本不足",
			&Bucket{ReqTotal: 50, ExcQuota: 50}, true, StateLowSample, true},
		{"健康", full(100, 0), true, StateOk, false},
		{"劣化", full(97, 3), true, StateDegraded, false},
		{"故障", full(50, 50), true, StateDown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, av := stateOf(tc.bucket, tc.hasChannel, d)
			assert.Equal(t, tc.wantState, state)
			if tc.wantAvNil {
				assert.Nil(t, av, "该状态下绝不能给出百分比")
			} else {
				require.NotNil(t, av)
			}
		})
	}
}

// 阈值边界必须是闭区间,否则恰好 99.00% 会被标成劣化,引发无意义的告警。
func TestStateThresholdBoundaries(t *testing.T) {
	d := definitionOf(config.Availability{})
	state, av := stateOf(&Bucket{ReqTotal: 100, SuccessCount: 99, FailUpstream: 1}, true, d)
	require.NotNil(t, av)
	assert.Equal(t, 99.0, *av)
	assert.Equal(t, StateOk, state)

	state, av = stateOf(&Bucket{ReqTotal: 100, SuccessCount: 95, FailUpstream: 5}, true, d)
	require.NotNil(t, av)
	assert.Equal(t, StateDegraded, state)
}

func TestTopReasonIgnoresExcluded(t *testing.T) {
	b := &Bucket{FailTimeout: 3, FailUpstream: 5, ExcClientGone: 100, ExcQuota: 200}
	assert.Equal(t, string(OutcomeUpstream), topReason(b))
	assert.Equal(t, "", topReason(&Bucket{ExcQuota: 10}), "只有排除项时没有失败原因可言")
}
