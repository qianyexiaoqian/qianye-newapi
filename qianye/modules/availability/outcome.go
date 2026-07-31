package availability

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/qianye/config"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ─────────────────────────── 可用率口径(必读) ───────────────────────────
//
// 一句话:可用率 = success / counted,counted 是「本次请求能证明模型可用与否」的样本数。
//
// 每个端到端样本被分入且仅分入一个 Outcome:
//
//	Outcome         判定                                     分母  分子  可配
//	success         成功且流未出错                              ✓     ✓    —
//	soft_fail       success=true 但流中途超时/断裂/有错误帧        ✓     ✗    —
//	no_channel      该分组下没有可用渠道                          ✓     ✗    —
//	timeout         上游响应超时 / ctx deadline / 流超时          ✓     ✗    —
//	upstream_error  上游 5xx、连接失败、空响应、渠道密钥失效等       ✓     ✗    —
//	internal_error  本平台内部错误(序列化、SQL、relay 构造失败)    ✓     ✗    —
//	rate_limit      429                                        可配   ✗    count_rate_limited
//	client_error    用户 4xx(参数非法、敏感词、模型不存在)         可配   ✗    count_client_errors
//	quota_error     额度不足 / 预扣失败                          ✗     ✗    硬排除
//	violation       违规拦截(violation_fee.*)                   ✗     ✗    硬排除
//	client_gone     客户端主动断开                               ✗     ✗    硬排除
//	channel_test    管理员的渠道测试流量                          ✗     ✗    硬排除
//
// 三条硬排除没有开关,理由分别是:
//  1. 额度不足是用户钱包问题,不是模型可用性 —— 上游自己也给这类错误挂了
//     ErrOptionWithNoRecordErrorLog,认为它不该进错误统计;
//  2. 违规拦截是策略命中,与「模型能不能用」正交;
//  3. 渠道测试会走完整 relay,管理员点一次「测试所有渠道」就能瞬间灌入几百条样本,
//     把真实业务可用率冲垮。这是上游 perf_metrics 现在就存在的污染,不能继承。
//
// 客户端断开同样排除:用户自己关掉连接,不能算模型不可用。
//
// 覆盖面的局限(必须在 UI 上声明):RecordRelaySample 只有三个调用点
// (relay 重试全败、文本主路径成功、音频路径成功),因此 Midjourney / Suno /
// 视频等异步 task 路径不产生样本 —— 这些模型显示 no_data 而非 0%。
// 该覆盖面与模型广场现有可用率完全一致,两个页面的数字可比。

// Outcome 是单次端到端样本的分类结果。取值同时用作 API 里的 top_reason。
type Outcome string

const (
	OutcomeSuccess     Outcome = "success"
	OutcomeSoftFail    Outcome = "soft_fail"
	OutcomeNoChannel   Outcome = "no_channel"
	OutcomeTimeout     Outcome = "timeout"
	OutcomeUpstream    Outcome = "upstream_error"
	OutcomeRateLimit   Outcome = "rate_limit"
	OutcomeClientError Outcome = "client_error"
	OutcomeInternal    Outcome = "internal_error"
	OutcomeQuota       Outcome = "quota_error"
	OutcomeViolation   Outcome = "violation"
	OutcomeClientGone  Outcome = "client_gone"
	OutcomeChannelTest Outcome = "channel_test"
)

// classifyInput 是分类所需的全部信息。
//
// 刻意不直接收 *RelayInfo:分类是纯函数,单测不该被迫构造一个几十字段的大结构体;
// 更重要的是采样点必须在 relay 线程内把这几个值取干净,不能把 info 指针带去
// 后台协程 —— StreamStatus 在 relay 结束后仍可能被写。
type classifyInput struct {
	Success       bool
	IsChannelTest bool
	EndReason     relaycommon.StreamEndReason
	StreamErrors  int
	Err           *types.NewAPIError
}

// classify 把一次采样归入唯一的 Outcome。纯函数,无 IO,判定顺序即短路顺序。
func classify(in classifyInput) Outcome {
	// 渠道测试最先短路:它可能同时命中下面任何一条,但永远只算测试流量。
	if in.IsChannelTest {
		return OutcomeChannelTest
	}
	// 客户端断开与成功失败无关:用户关掉连接时 relay 既可能已返回部分内容(success),
	// 也可能带着 context.Canceled 失败,两种都不该计入。
	if in.EndReason == relaycommon.StreamEndReasonClientGone {
		return OutcomeClientGone
	}
	if in.Success {
		if isSoftStreamFailure(in) {
			return OutcomeSoftFail
		}
		return OutcomeSuccess
	}
	return classifyError(in)
}

// isSoftStreamFailure 判定「返回了 200 和部分内容,但这次对话其实是坏的」。
//
// 这是自建采样相对上游 perf_metrics 的核心增量:后者只有一个裸 bool,
// 流到一半断掉、上游中途推来 error 帧的请求,在它眼里都是成功。
//
// eof / done / handler_stop 是正常收尾(与 StreamStatus.IsNormalEnd 保持一致),
// 不算软失败;非流式请求 EndReason 为空,也走这条正常分支。
func isSoftStreamFailure(in classifyInput) bool {
	if in.StreamErrors > 0 {
		return true
	}
	switch in.EndReason {
	case relaycommon.StreamEndReasonTimeout,
		relaycommon.StreamEndReasonScannerErr,
		relaycommon.StreamEndReasonPanic,
		relaycommon.StreamEndReasonPingFail:
		return true
	default:
		return false
	}
}

func classifyError(in classifyInput) Outcome {
	err := in.Err
	if err == nil {
		// 失败却没有错误对象,只能归为内部问题 —— 静默丢弃会让分母凭空缩水。
		return OutcomeInternal
	}
	code := err.GetErrorCode()
	status := err.StatusCode

	switch {
	case isQuotaCode(code):
		return OutcomeQuota
	case isViolationCode(code):
		return OutcomeViolation
	case code == types.ErrorCodeGetChannelFailed:
		return OutcomeNoChannel
	case isTimeout(in, code):
		return OutcomeTimeout
	// 429 必须先于 upstream 判定:上游限流常以 bad_response_status_code 上报,
	// 按错误码归类会把它错算成上游故障。
	case status == 429:
		return OutcomeRateLimit
	case isUpstreamCode(code) || types.IsChannelError(err):
		return OutcomeUpstream
	// 内部错误码必须先于 status >= 500 判定:types.NewError 默认就带 500,
	// 否则平台自己的序列化/SQL 故障会被全部记到上游头上,排障时找错方向。
	case isInternalCode(code):
		return OutcomeInternal
	case status >= 500:
		return OutcomeUpstream
	case isClientCode(code) || (status >= 400 && status < 500):
		return OutcomeClientError
	default:
		return OutcomeInternal
	}
}

func isQuotaCode(code types.ErrorCode) bool {
	return code == types.ErrorCodeInsufficientUserQuota ||
		code == types.ErrorCodePreConsumeTokenQuotaFailed
}

// isViolationCode 与违规模块的口径保持一致:只认 violation_fee.* 前缀。
// 敏感词拦截(sensitive_words_detected)归 client_error —— 那是请求内容问题。
func isViolationCode(code types.ErrorCode) bool {
	return strings.HasPrefix(string(code), "violation_fee.")
}

func isTimeout(in classifyInput, code types.ErrorCode) bool {
	if code == types.ErrorCodeChannelResponseTimeExceeded {
		return true
	}
	if in.EndReason == relaycommon.StreamEndReasonTimeout {
		return true
	}
	return in.Err != nil && errors.Is(in.Err, context.DeadlineExceeded)
}

func isUpstreamCode(code types.ErrorCode) bool {
	switch code {
	case types.ErrorCodeDoRequestFailed,
		types.ErrorCodeBadResponse,
		types.ErrorCodeBadResponseBody,
		types.ErrorCodeBadResponseStatusCode,
		types.ErrorCodeEmptyResponse,
		types.ErrorCodeReadResponseBodyFailed,
		types.ErrorCodeAwsInvokeError:
		return true
	default:
		return false
	}
}

// isInternalCode 是本平台自己的故障:与上游无关,但用户确实用不了,因此计入分母。
// 单独成类是为了让运营一眼看出「该找我们还是找上游」。
func isInternalCode(code types.ErrorCode) bool {
	switch code {
	case types.ErrorCodeGenRelayInfoFailed,
		types.ErrorCodeJsonMarshalFailed,
		types.ErrorCodeQueryDataError,
		types.ErrorCodeUpdateDataError,
		types.ErrorCodeModelPriceError,
		types.ErrorCodeInvalidApiType:
		return true
	default:
		return false
	}
}

func isClientCode(code types.ErrorCode) bool {
	switch code {
	case types.ErrorCodeInvalidRequest,
		types.ErrorCodeBadRequestBody,
		types.ErrorCodeReadRequestBodyFailed,
		types.ErrorCodeConvertRequestFailed,
		types.ErrorCodeCountTokenFailed,
		types.ErrorCodeSensitiveWordsDetected,
		types.ErrorCodeAccessDenied,
		types.ErrorCodePromptBlocked,
		types.ErrorCodeModelNotFound:
		return true
	default:
		return false
	}
}

// ─────────────────────────── 口径与状态判定 ───────────────────────────

// 状态阈值与样本下限暂无配置字段承载,固定为常量。
//
// minSamples 的存在是为了不显示 1/1 = 100% 或 0/1 = 0% 这种误导性数字:
// 样本不足时给 low_sample 状态而不给百分比。
const (
	defaultMinSamples        = 20
	defaultOkThreshold       = 99.0
	defaultDegradedThreshold = 95.0
)

// 六态。核心是把「没有数据」与「全挂了」彻底分开 —— 无数据显示 0% 会让运营
// 误判为故障并做出错误决策,这是可用率看板最常见也最贵的一个坑。
const (
	StateOk         = "ok"          // 可用率 >= ok_threshold
	StateDegraded   = "degraded"    // 介于两个阈值之间
	StateDown       = "down"        // 低于 degraded_threshold
	StateLowSample  = "low_sample"  // 有样本但不足以下结论,不给百分比
	StateNoData     = "no_data"     // 该分组有这个模型,但时段内没有请求
	StateNotOffered = "not_offered" // 该分组根本不提供这个模型
)

// Definition 是当前生效的口径,随每次查询一并返回。
//
// 可用率是最容易引发争议的指标,页面必须能在首屏回答「99.4% 是怎么算出来的」。
// 没有这个东西,上线后会持续收到「为什么我这边失败了这里显示 100%」的工单。
type Definition struct {
	Numerator         string   `json:"numerator"`
	Denominator       []string `json:"denominator"`
	Excluded          []string `json:"excluded"`
	CountClientErrors bool     `json:"count_client_errors"`
	CountRateLimited  bool     `json:"count_rate_limited"`
	MinSamples        int      `json:"min_samples"`
	// PerfMinSamples 是延迟 / 首字 / 速度的样本下限,与 MinSamples 不同,理由见 perf.go。
	// 下发它是为了让页面能当场解释「延迟这一列为什么是横杠」。
	PerfMinSamples    int     `json:"perf_min_samples"`
	OkThreshold       float64 `json:"ok_threshold"`
	DegradedThreshold float64 `json:"degraded_threshold"`
	Note              string  `json:"note"`
}

// definitionOf 由配置推导口径。分母清单按开关动态拼装,前端直接渲染这份清单,
// 不在两侧各写一遍规则。
func definitionOf(cfg config.Availability) Definition {
	denominator := []string{
		string(OutcomeSuccess), string(OutcomeSoftFail), string(OutcomeNoChannel),
		string(OutcomeTimeout), string(OutcomeUpstream), string(OutcomeInternal),
	}
	excluded := []string{
		string(OutcomeQuota), string(OutcomeViolation),
		string(OutcomeClientGone), string(OutcomeChannelTest),
	}
	if cfg.CountRateLimited {
		denominator = append(denominator, string(OutcomeRateLimit))
	} else {
		excluded = append(excluded, string(OutcomeRateLimit))
	}
	if cfg.CountClientErrors {
		denominator = append(denominator, string(OutcomeClientError))
	} else {
		excluded = append(excluded, string(OutcomeClientError))
	}
	return Definition{
		Numerator:         string(OutcomeSuccess),
		Denominator:       denominator,
		Excluded:          excluded,
		CountClientErrors: cfg.CountClientErrors,
		CountRateLimited:  cfg.CountRateLimited,
		MinSamples:        defaultMinSamples,
		PerfMinSamples:    perfMinSamples,
		OkThreshold:       defaultOkThreshold,
		DegradedThreshold: defaultDegradedThreshold,
		Note:              "端到端口径:一次请求内部重试多个渠道,只要最终成功即计为成功;不含 Midjourney / 视频 / 音乐等异步任务",
	}
}

// counted 是分母:能够证明「模型可用与否」的样本数。
func counted(b *Bucket, d Definition) int64 {
	n := b.SuccessCount + b.FailSoftStream + b.FailNoChannel +
		b.FailTimeout + b.FailUpstream + b.FailInternal
	if d.CountRateLimited {
		n += b.FailRateLimit
	}
	if d.CountClientErrors {
		n += b.FailClientError
	}
	return n
}

// availabilityOf 返回百分比。分母为 0 时返回 nil —— 绝不返回 0。
//
// success 会被夹到 counted 以内:flush 的 drain 逐字段 Swap,极短窗口内
// 可能出现某个样本的成功计数落在新批、分母计数落在旧批,导致 success > counted。
// 两批最终都会累加进同一 DB 行,总量不丢,但单次查询要能兜住。
func availabilityOf(success, denom int64) *float64 {
	if denom <= 0 {
		return nil
	}
	if success > denom {
		success = denom
	}
	if success < 0 {
		success = 0
	}
	v := math.Round(float64(success)/float64(denom)*10000) / 100
	return &v
}

// stateOf 判定六态之一,并给出该显示的可用率(不该显示时为 nil)。
func stateOf(b *Bucket, hasChannel bool, d Definition) (string, *float64) {
	denom := counted(b, d)
	switch {
	case b.ReqTotal == 0 && !hasChannel:
		return StateNotOffered, nil
	case b.ReqTotal == 0:
		return StateNoData, nil
	case denom < int64(d.MinSamples):
		// 全部样本都被口径排除时 denom 为 0,同样落在这里:有流量但无法下结论。
		return StateLowSample, nil
	}
	av := availabilityOf(b.SuccessCount, denom)
	if av == nil {
		return StateLowSample, nil
	}
	switch {
	case *av >= d.OkThreshold:
		return StateOk, av
	case *av >= d.DegradedThreshold:
		return StateDegraded, av
	default:
		return StateDown, av
	}
}

// topReason 返回占比最大的失败原因,用于矩阵单元格的 tooltip。
// 被硬排除的类别不参与:它们不是「不可用的原因」。
func topReason(b *Bucket) string {
	candidates := []struct {
		name  Outcome
		count int64
	}{
		{OutcomeUpstream, b.FailUpstream},
		{OutcomeTimeout, b.FailTimeout},
		{OutcomeNoChannel, b.FailNoChannel},
		{OutcomeSoftFail, b.FailSoftStream},
		{OutcomeRateLimit, b.FailRateLimit},
		{OutcomeClientError, b.FailClientError},
		{OutcomeInternal, b.FailInternal},
	}
	best := ""
	var bestCount int64
	for _, c := range candidates {
		if c.count > bestCount {
			best, bestCount = string(c.name), c.count
		}
	}
	return best
}
