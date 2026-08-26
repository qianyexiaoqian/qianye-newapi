package billingexpr

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/tidwall/gjson"
)

// RunExpr compiles (with cache) and executes an expression string.
// The environment exposes:
//   - p, c             — prompt / completion tokens (auto-excluding separately-priced sub-categories)
//   - len              — total input context length for tier conditions (never reduced by sub-category exclusion)
//   - cr, cc, cc1h     — cache read / creation / creation-1h tokens
//   - tier(name, value) — trace callback that records which tier matched
//   - max, min, abs, ceil, floor — standard math helpers
//
// Returns the resulting float64 quota (before group ratio) and a TraceResult
// with side-channel info captured by tier() during execution.
func RunExpr(exprStr string, params TokenParams) (float64, TraceResult, error) {
	return RunExprWithRequest(exprStr, params, RequestInput{})
}

func RunExprWithRequest(exprStr string, params TokenParams, request RequestInput) (float64, TraceResult, error) {
	entry, err := compileEntryFromCacheByHash(exprStr, ExprHashString(exprStr))
	if err != nil {
		return 0, TraceResult{}, err
	}
	return runProgram(entry.prog, entry.requestRules, params, request)
}

// RunExprByHash is like RunExpr but accepts a pre-computed hash for the cache
// lookup, avoiding a redundant SHA-256 computation when the caller already
// holds BillingSnapshot.ExprHash.
func RunExprByHash(exprStr, hash string, params TokenParams) (float64, TraceResult, error) {
	return RunExprByHashWithRequest(exprStr, hash, params, RequestInput{})
}

func RunExprByHashWithRequest(exprStr, hash string, params TokenParams, request RequestInput) (float64, TraceResult, error) {
	entry, err := compileEntryFromCacheByHash(exprStr, hash)
	if err != nil {
		return 0, TraceResult{}, err
	}
	return runProgram(entry.prog, entry.requestRules, params, request)
}

func runProgram(prog *vm.Program, requestRules []RequestRuleTrace, params TokenParams, request RequestInput) (float64, TraceResult, error) {
	trace := TraceResult{
		RequestRules: append([]RequestRuleTrace(nil), requestRules...),
	}
	headers := normalizeHeaders(request.Headers)

	env := map[string]interface{}{
		"p":     params.P,
		"c":     params.C,
		"len":   params.Len,
		"cr":    params.CR,
		"cc":    params.CC,
		"cc1h":  params.CC1h,
		"img":   params.Img,
		"img_o": params.ImgO,
		"ai":    params.AI,
		"ao":    params.AO,
		"tier": func(name string, value float64) float64 {
			trace.MatchedTier = name
			trace.Cost = value
			return value
		},
		requestRuleTraceFunction: func(ruleIndex int, matched bool, multiplier float64) float64 {
			if matched && ruleIndex >= 0 && ruleIndex < len(trace.RequestRules) {
				trace.RequestRules[ruleIndex].Matched = true
			}
			if matched {
				return multiplier
			}
			return 1
		},
		requestRuleTraceIntFunction: func(ruleIndex int, matched bool, multiplier int) int {
			if matched && ruleIndex >= 0 && ruleIndex < len(trace.RequestRules) {
				trace.RequestRules[ruleIndex].Matched = true
			}
			if matched {
				return multiplier
			}
			return 1
		},
		"header": func(key string) string {
			return headers[strings.ToLower(strings.TrimSpace(key))]
		},
		"param": func(path string) interface{} {
			path = strings.TrimSpace(path)
			if path == "" || len(request.Body) == 0 {
				return nil
			}
			result := gjson.GetBytes(request.Body, path)
			if !result.Exists() {
				return nil
			}
			return result.Value()
		},
		"has": func(source interface{}, substr string) bool {
			if source == nil || substr == "" {
				return false
			}
			return strings.Contains(fmt.Sprint(source), substr)
		},
		"hour":    func(tz string) int { return clockReading(request.Clock, tz, clockHour) },
		"minute":  func(tz string) int { return clockReading(request.Clock, tz, clockMinute) },
		"weekday": func(tz string) int { return clockReading(request.Clock, tz, clockWeekday) },
		"month":   func(tz string) int { return clockReading(request.Clock, tz, clockMonth) },
		"day":     func(tz string) int { return clockReading(request.Clock, tz, clockDay) },
		"max":     math.Max,
		"min":     math.Min,
		"abs":     math.Abs,
		"ceil":    math.Ceil,
		"floor":   math.Floor,
	}

	out, err := expr.Run(prog, env)
	if err != nil {
		return 0, trace, fmt.Errorf("expr run error: %w", err)
	}
	f, ok := out.(float64)
	if !ok {
		return 0, trace, fmt.Errorf("expr result is %T, want float64", out)
	}
	// 非有限结果必须在这里就变成错误,不能当成一个"很大的数"往下走。
	//
	// 表达式语言里有除法,也接受任意大的字面量,所以 `1 / (cr - 4096)` 与
	// `p * 1e308 * 10` 都能算出 ±Inf,`0/0` 与 `Inf-Inf` 能算出 NaN。这两类值
	// 沿计费链往下走会各自变成一次事故,而且两次都不报错:
	//
	//   - 结算侧 common.QuotaRoundChecked(+Inf) 把它**饱和**成 common.MaxQuota,
	//     于是一次请求扣掉整个额度上界(默认刻度下 ＄17,592,186),用户余额被
	//     扣穿,日志里是一条正常的 200;
	//   - 带工具附加费那一支走 decimal.NewFromFloat(±Inf),shopspring/decimal
	//     对非有限值直接 **panic**。panic 发生在响应体已经写给客户端之后、
	//     结算之前:客户端拿到完整回答,而这一笔既不结算、不记消费日志、也不退
	//     预扣 —— 钱少了一截,账本上查无此单。
	//
	// 放在 runProgram 而不是各调用点,是因为这里是全部三条入口(RunExpr /
	// RunExprByHash / 带 request 的两个变体)唯一的汇合处,漏一个调用点就等于
	// 漏一条计费路径。返回错误之后两侧都恰好 fail-closed:预扣侧
	// modelPriceHelperTiered 把它变成 400(不中继、不扣费),结算侧
	// TryTieredSettle 回落到已预扣的那个诚实数额并让 result=nil,于是带附加费
	// 那一支也走不进 decimal.NewFromFloat。
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, trace, fmt.Errorf("expr result is not a finite number (%v)", f)
	}
	return f, trace, nil
}

type clockField int

const (
	clockHour clockField = iota
	clockMinute
	clockWeekday
	clockMonth
	clockDay
)

// clockReading returns one calendar reading, honouring an injected override.
//
// Only the save-time smoke test injects one. Without the seam the whole
// hour()/weekday()/month()/day() family was evaluated exactly once — at the
// wall clock of the instant the operator pressed save — so an expression that
// goes negative at 02:00 saved at 14:00 passed validation and then produced a
// silent zero-revenue window (or a hard 400 on every request) once that hour
// arrived.
func clockReading(override *ClockOverride, tz string, field clockField) int {
	if override != nil {
		switch field {
		case clockHour:
			return override.Hour
		case clockMinute:
			return override.Minute
		case clockWeekday:
			return override.Weekday
		case clockMonth:
			return override.Month
		case clockDay:
			return override.Day
		}
	}
	now := timeInZone(tz)
	switch field {
	case clockHour:
		return now.Hour()
	case clockMinute:
		return now.Minute()
	case clockWeekday:
		return int(now.Weekday())
	case clockMonth:
		return int(now.Month())
	default:
		return now.Day()
	}
}

func timeInZone(tz string) time.Time {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return time.Now().UTC()
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Now().UTC()
	}
	return time.Now().In(loc)
}

func normalizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return map[string]string{}
	}
	normalized := make(map[string]string, len(headers))
	for key, value := range headers {
		k := strings.ToLower(strings.TrimSpace(key))
		v := strings.TrimSpace(value)
		if k == "" || v == "" {
			continue
		}
		normalized[k] = v
	}
	return normalized
}
