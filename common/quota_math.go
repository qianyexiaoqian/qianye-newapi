package common

import (
	"fmt"
	"math"
	"strconv"

	"github.com/shopspring/decimal"
)

// Quota conversions are centralized here so every billing path shares one
// saturation + logging policy. An oversized product must clamp (or be
// rejected by a *Strict variant) instead of wrapping around and turning a
// charge into a credit.
//
// # Why the bound is NOT the column width
//
// Until this constant was re-derived, MaxQuota was math.MaxInt32 and the
// stated reason was "quota columns are 32-bit integers in the database".
// That reason is false, and it is cheap to check:
//
//	model/user.go   Quota int `gorm:"type:int"`   -> MySQL/PG bigint, SQLite INTEGER
//	model/token.go  RemainQuota int               -> MySQL/PG bigint, SQLite INTEGER
//	model/log.go    Quota int                     -> MySQL/PG bigint, SQLite INTEGER
//
// The `type:int` tag does not name a SQL type. GORM maps it onto the generic
// schema.Int *kind* (gorm/schema/field.go: `case Bool, Int, Uint, ...`), and
// the dialector then derives the SQL type from field.Size, which is 64 for a
// Go `int` on every 64-bit build. So the tag is a no-op and all three
// dialects build a 64-bit column. TestQuotaColumnsAre64BitOnEveryDialect
// (model/quota_column_width_test.go) runs AutoMigrate against an empty
// database of each dialect and asserts exactly that, so the false premise
// cannot come back by accident.
//
// # What the bound actually protects
//
// It is an *arithmetic* bound: the largest quota every downstream calculation
// can still carry without overflowing. Three ceilings apply, and MaxQuota is
// the smallest of them rounded down to a power of two:
//
//  1. float64 exact integers. saturateQuota takes a float64 and does int(v);
//     that is only exact below 2^53. Ceiling: 2^53.
//  2. Number.MAX_SAFE_INTEGER. Quota values reach the React admin as JSON
//     numbers and are parsed as float64. Ceiling: 2^53 - 1.
//  3. int64 headroom for the largest *unchecked* multiplier a quota-bounded
//     value meets in the money paths. That multiplier is the lottery roster
//     size (`hit * p.AmountQuota` in qianye/modules/lottery/api_admin.go),
//     bounded by lottery.max_total_entries_hard, which config validation caps
//     at 2^19 (config.MaxLotteryEntriesHard). Ceiling: MaxInt64 / 2^19 = 2^44.
//
// (3) is the binding one, and MaxQuota is set one power of two below it so the
// worst-case product lands at 2^62, half of MaxInt64, rather than exactly on
// the boundary. In the default scale (common.QuotaPerUnit = 500000) MaxQuota is
// $17,592,186.04 per single quota value, up from $4,294.97 under the old int32
// bound.
//
// Basis-point arithmetic (the other family of raw int64 products, e.g.
// `(PoolQuota + Amount) * PoolShareBps / 10000`) tops out at
// 2*MaxQuota*10^4 = 2^44 * 10^4 ~= 1.8e17, two decimal orders below MaxInt64.
//
// The const block below turns all of this into compile errors, so raising
// MaxQuota without redoing the derivation does not build.
const (
	MaxQuota = 1 << 43 // 8,796,093,022,208 quota
	MinQuota = -MaxQuota
)

// MaxQuotaWorstMultiplier is the largest integer a quota-bounded value is
// multiplied by in a raw int64 product anywhere in the money paths. It mirrors
// config.MaxLotteryEntriesHard; the two are tied together by
// TestQuotaWorstMultiplierMirrorsLotteryEntriesHardCap (in qianye/config,
// which is the only side that can see both) so neither can drift alone.
// (common cannot import qianye/config: qianye/config imports common.)
//
// It has to be **exported** for that tie to exist at all. While it was
// unexported the comment above was simply false: no test could name it, and
// TestLotteryEntriesHardCapMatchesQuotaBound only ever asserted the *product*
// `MaxQuota * MaxLotteryEntriesHard <= 2^62`, which is a different claim.
// Halving this mirror alone therefore compiled, passed every test, and
// silently loosened the compile-time assertion below by a factor of two --
// enough to let MaxQuota be raised to 2^44, where the real worst-case product
// is 2^63 (an int64 wraparound).
const MaxQuotaWorstMultiplier = 1 << 19

// These fail to compile if MaxQuota ever leaves the range the derivation above
// proves safe. Every element is an untyped constant that goes negative when its
// invariant breaks, and uint64(<negative constant>) is a compile-time error
// rather than a runtime surprise.
const (
	// (1)+(2): stay inside float64 / JS exact-integer territory.
	_ = uint64(1<<53 - MaxQuota)
	// (3): the largest unchecked multiplier is the lottery roster hard cap.
	_ = uint64(math.MaxInt64/MaxQuotaWorstMultiplier - MaxQuota)
	// bps products: 2*MaxQuota*10000 must still fit in int64.
	_ = uint64(math.MaxInt64/(2*10000) - MaxQuota)
	// The clamp must stay symmetric; MinQuota is what underflow saturates to.
	_ = uint64(MaxQuota + MinQuota)
)

// QuotaClampKind identifies why a quota conversion had to be saturated.
type QuotaClampKind string

// Clamp kinds reported by QuotaClamp.Kind.
const (
	QuotaClampOverflow  QuotaClampKind = "overflow"
	QuotaClampUnderflow QuotaClampKind = "underflow"
	QuotaClampNaN       QuotaClampKind = "nan"
)

// QuotaClamp describes a single saturation event: a quota conversion whose
// input fell outside the representable quota range (or was NaN) and was
// therefore clamped. It is surfaced to billing callers so the event can be
// recorded on the related consume/task log for admin auditing.
type QuotaClamp struct {
	Op       string         `json:"op"`       // "QuotaFromFloat" | "QuotaRound" | "QuotaFromDecimal"
	Kind     QuotaClampKind `json:"kind"`     // "overflow" | "underflow" | "nan"
	Original float64        `json:"original"` // best-effort pre-clamp value (decimal -> float64 approx)
	Clamped  int            `json:"clamped"`  // the saturated result actually used
}

// Error lets the same typed value serve both as the settlement audit marker
// and as the fail-fast error returned by strict pre-consume conversions.
func (c *QuotaClamp) Error() string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("quota conversion (%s) %s: original=%g, clamped=%d", c.Op, c.Kind, c.Original, c.Clamped)
}

// AuditMap renders the clamp as the marker stored under a log's
// admin_info.quota_saturation. Centralized here so every billing path (consume
// logs, task billing logs, task compensation logs) records the same shape.
func (c *QuotaClamp) AuditMap() map[string]interface{} {
	if c == nil {
		return nil
	}
	// original 必须是一个 encoding/json 编得出来的值。
	//
	// 它是**导致这次钳制的那个原始 float64**,而最典型的钳制来源恰恰是
	// ±Inf 与 NaN(倍率连乘溢出、表达式除零)。JSON 没有这三个值,
	// json.Marshal 会对整张 map 返回 UnsupportedValueError,而
	// common.MapToJsonStr 把错误吞掉返回空串 —— 于是这条消费日志的整个
	// `other` 列变成 '',连带 model_ratio / group_ratio / cache_ratio /
	// use_channel 一起丢光。也就是说 AGENTS.md 承诺的「钳制事件双通道可检出」
	// 恰好在**最需要它的那一类事件**上整段失效:那条天价日志在管理端看起来
	// 只是一行没有任何计费上下文的记录。
	//
	// 非有限值改记字符串("+Inf" / "-Inf" / "NaN"):这一栏本来就是给人看的
	// 审计标记,不参与任何算术,字符串比"整段消失"能回答的问题多得多。
	original := interface{}(c.Original)
	if math.IsInf(c.Original, 0) || math.IsNaN(c.Original) {
		original = strconv.FormatFloat(c.Original, 'g', -1, 64)
	}
	return map[string]interface{}{
		"op":       c.Op,
		"kind":     c.Kind,
		"original": original,
		"clamped":  c.Clamped,
	}
}

// saturateQuota converts an already-rounded quota value to int, clamping to
// [MinQuota, MaxQuota]. Whenever clamping (what would otherwise be an integer
// wraparound) or a NaN fallback is triggered it logs a warning, because in
// normal operation a single request never approaches these bounds — hitting
// them signals a bug or an abusive request. `op` names the caller. When a
// clamp occurs it returns a non-nil *QuotaClamp so callers can additionally
// record the event (e.g. on the consume log); the returned pointer is nil for
// in-range values.
func saturateQuota(value float64, op string) (int, *QuotaClamp) {
	var clamp *QuotaClamp
	switch {
	case math.IsNaN(value):
		clamp = &QuotaClamp{Op: op, Kind: QuotaClampNaN, Original: value, Clamped: 0}
	case value >= MaxQuota:
		clamp = &QuotaClamp{Op: op, Kind: QuotaClampOverflow, Original: value, Clamped: MaxQuota}
	case value <= MinQuota:
		clamp = &QuotaClamp{Op: op, Kind: QuotaClampUnderflow, Original: value, Clamped: MinQuota}
	default:
		return int(value), nil
	}
	SysError(clamp.Error())
	return clamp.Clamped, clamp
}

func strictQuota(quota int, clamp *QuotaClamp) (int, error) {
	if clamp != nil {
		return 0, clamp
	}
	return quota, nil
}

// QuotaFromFloat converts a computed quota value to int, truncating toward
// zero, with saturation. Use for float products of prices, ratios, and
// user-controlled multipliers (image n, video seconds, resolution ratios).
func QuotaFromFloat(value float64) int {
	quota, _ := QuotaFromFloatChecked(value)
	return quota
}

// QuotaFromFloatChecked is QuotaFromFloat but also returns a non-nil
// *QuotaClamp when the value was clamped, so billing callers can audit it.
func QuotaFromFloatChecked(value float64) (int, *QuotaClamp) {
	return saturateQuota(value, "QuotaFromFloat")
}

// QuotaFromFloatStrict converts an in-range value and returns a typed
// *QuotaClamp error instead of allowing a saturated result to reach billing.
func QuotaFromFloatStrict(value float64) (int, error) {
	return strictQuota(QuotaFromFloatChecked(value))
}

// QuotaRound converts a float64 quota value to int using half-away-from-zero
// rounding, with saturation. Every tiered billing path (pre-consume,
// settlement, breakdown validation, log fields) MUST use this to avoid +-1
// discrepancies.
func QuotaRound(value float64) int {
	quota, _ := QuotaRoundChecked(value)
	return quota
}

// QuotaRoundChecked is QuotaRound but also returns a non-nil *QuotaClamp when
// the value was clamped, so billing callers can audit it.
func QuotaRoundChecked(value float64) (int, *QuotaClamp) {
	return saturateQuota(math.Round(value), "QuotaRound")
}

// QuotaRoundStrict rounds an in-range value and returns a typed *QuotaClamp
// error instead of allowing a saturated result to reach billing.
func QuotaRoundStrict(value float64) (int, error) {
	return strictQuota(QuotaRoundChecked(value))
}

// QuotaFromDecimal converts a computed quota decimal to int with saturation.
// The decimal is rounded (half away from zero) before conversion.
func QuotaFromDecimal(d decimal.Decimal) int {
	quota, _ := QuotaFromDecimalChecked(d)
	return quota
}

// QuotaFromDecimalChecked is QuotaFromDecimal but also returns a non-nil
// *QuotaClamp when the value was clamped, so billing callers can audit it.
func QuotaFromDecimalChecked(d decimal.Decimal) (int, *QuotaClamp) {
	f, _ := d.Round(0).Float64()
	return saturateQuota(f, "QuotaFromDecimal")
}

// QuotaFromDecimalStrict converts an in-range decimal quota and rejects a
// value that would otherwise be saturated at the MaxQuota boundary.
func QuotaFromDecimalStrict(d decimal.Decimal) (int, error) {
	return strictQuota(QuotaFromDecimalChecked(d))
}

// QuotaAddChecked adds delta to base in int64 space and saturates the result
// into [MinQuota, MaxQuota], reporting the clamp when it happens.
//
// Plain `user.Quota += delta` is a silent int64 wrap: a wallet parked near
// math.MaxInt64 (reachable in the past through an unbounded redemption face
// value) turns into a ~-9.2e18 balance on the very next credit, with no error,
// no log and no clamp marker. Every place that adds to a persisted quota
// column must go through this instead of the bare `+`.
func QuotaAddChecked(base int, delta int) (int, *QuotaClamp) {
	sum := int64(base) + int64(delta)
	switch {
	case sum > int64(MaxQuota):
		clamp := &QuotaClamp{Op: "QuotaAdd", Kind: QuotaClampOverflow, Original: float64(sum), Clamped: MaxQuota}
		SysError(clamp.Error())
		return MaxQuota, clamp
	case sum < int64(MinQuota):
		clamp := &QuotaClamp{Op: "QuotaAdd", Kind: QuotaClampUnderflow, Original: float64(sum), Clamped: MinQuota}
		SysError(clamp.Error())
		return MinQuota, clamp
	default:
		return int(sum), nil
	}
}
