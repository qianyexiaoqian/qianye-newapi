package billing_setting

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/samber/lo"
)

const (
	BillingModeRatio      = "ratio"
	BillingModeTieredExpr = "tiered_expr"
	BillingModeField      = "billing_mode"
	BillingExprField      = "billing_expr"
)

// BillingSetting is managed by config.GlobalConfig.Register.
// DB keys: billing_setting.billing_mode, billing_setting.billing_expr
type BillingSetting struct {
	BillingMode map[string]string `json:"billing_mode"`
	BillingExpr map[string]string `json:"billing_expr"`
}

var billingSetting = BillingSetting{
	BillingMode: make(map[string]string),
	BillingExpr: make(map[string]string),
}

func init() {
	config.GlobalConfig.Register("billing_setting", &billingSetting)
}

// ---------------------------------------------------------------------------
// Read accessors (hot path, must be fast)
// ---------------------------------------------------------------------------

func GetBillingMode(model string) string {
	if mode, ok := billingSetting.BillingMode[model]; ok {
		return mode
	}
	return BillingModeRatio
}

func GetBillingExpr(model string) (string, bool) {
	expr, ok := billingSetting.BillingExpr[model]
	return expr, ok
}

func GetBillingModeCopy() map[string]string {
	return lo.Assign(billingSetting.BillingMode)
}

func GetBillingExprCopy() map[string]string {
	return lo.Assign(billingSetting.BillingExpr)
}

func GetPricingSyncData(base map[string]any) map[string]any {
	extra := make(map[string]any, 2)
	if modes := GetBillingModeCopy(); len(modes) > 0 {
		extra[BillingModeField] = modes
	}
	if exprs := GetBillingExprCopy(); len(exprs) > 0 {
		extra[BillingExprField] = exprs
	}
	return lo.Assign(base, extra)
}

// ---------------------------------------------------------------------------
// Smoke test (called externally for validation before save)
// ---------------------------------------------------------------------------

func SmokeTestExpr(exprStr string) error {
	return smokeTestExpr(exprStr)
}

// BillingExprOptionKey is the options-table key that holds the per-model
// expression map (config.GlobalConfig registers this struct under
// "billing_setting", so each field lands under "<prefix>.<json tag>").
const BillingExprOptionKey = "billing_setting." + BillingExprField

// ValidateBillingExprJSON is the pre-persist gate for the billing_expr option.
//
// It runs the documented save-time validation (compile + non-negative smoke
// test) over every expression in the map. Without it a syntactically broken
// expression 400s every request for that model on each relay, and an
// arithmetically negative one turns settlement into a credit. It has to run
// before DB.Save for the same reason the ratio tables do: UpdateOption
// persists first and loads into memory second, so a rejected-at-load value
// stays in the database and survives a restart.
func ValidateBillingExprJSON(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var exprs map[string]string
	if err := common.UnmarshalJsonStr(value, &exprs); err != nil {
		return fmt.Errorf("billing_expr 不是合法的 JSON 对象: %w", err)
	}
	for modelName, exprStr := range exprs {
		if strings.TrimSpace(exprStr) == "" {
			continue
		}
		if err := smokeTestExpr(exprStr); err != nil {
			return fmt.Errorf("模型 %s 的计费表达式校验未通过: %w", modelName, err)
		}
	}
	return nil
}

// smokeTestVectors 是保存时那道非负烟测的取值集合。
//
// 它必须覆盖**每一个**表达式能引用的 token 变量,而且必须让它们互相失衡。
// 原先只有 4 条 P==C 且 7 个子类变量恒为 0 的向量,于是两整类为负的表达式能落库:
//   - 只在 c>p 时为负,例如 tier("promo", p*3 - c*1) —— 落库之后该模型**每一次**
//     请求都 400「pre-consume quota cannot be negative」(预扣侧 c 取 max_tokens
//     兜底),客户端拿不到任何可操作的提示,重启也不恢复;
//   - 只在某个子类命中时为负,例如「缓存命中返一部分钱」的 tier("c", p*3 + c*15 - cr*30)
//     —— 平时正常收费,一旦 cached_tokens 上来金额就被地板夹成 0,静默零收入。
//
// 判据是「任意一个变量单独放大都不能把结果拖成负数」,所以除了 P/C 失衡,
// 还要逐个把子类变量顶到远大于 P/C 的量级 —— 只把它们一起调大会被系数抵消掉。
func smokeTestVectors() []billingexpr.TokenParams {
	magnitudes := []float64{1e3, 1e5, 1e6}
	vectors := []billingexpr.TokenParams{{}}
	for _, m := range magnitudes {
		// P/C 三态:相等、输入远大于输出、输出远大于输入。
		vectors = append(vectors,
			billingexpr.TokenParams{P: m, C: m, Len: m},
			billingexpr.TokenParams{P: m, C: 1, Len: m},
			billingexpr.TokenParams{P: 1, C: m, Len: 1},
		)
		// 逐个子类顶到 m,P/C 压到 1:任何 "减去某个子类" 的形状都会在这里翻负。
		base := billingexpr.TokenParams{P: 1, C: 1, Len: 1}
		for _, set := range []func(*billingexpr.TokenParams){
			func(t *billingexpr.TokenParams) { t.CR = m; t.Len = m },
			func(t *billingexpr.TokenParams) { t.CC = m; t.Len = m },
			func(t *billingexpr.TokenParams) { t.CC1h = m; t.Len = m },
			func(t *billingexpr.TokenParams) { t.Img = m },
			func(t *billingexpr.TokenParams) { t.ImgO = m },
			func(t *billingexpr.TokenParams) { t.AI = m },
			func(t *billingexpr.TokenParams) { t.AO = m },
		} {
			v := base
			set(&v)
			vectors = append(vectors, v)
		}
		// 全部子类同时拉满,兜住「单项都不为负、合起来为负」的交叉项。
		vectors = append(vectors, billingexpr.TokenParams{
			P: m, C: m, Len: m, CR: m, CC: m, CC1h: m, Img: m, ImgO: m, AI: m, AO: m,
		})
	}
	return vectors
}

// smokeTestVarSetters 把表达式里的每一个 token 变量映射到「怎么把它设成某个值」。
//
// 键名与 billingexpr 求值时的 env 逐字一致(见 pkg/billingexpr/run.go)。
// 少一个键的表现是:那个变量上的分档边界永远不会被烟测踩到。
var smokeTestVarSetters = map[string]func(*billingexpr.TokenParams, float64){
	"p":     func(t *billingexpr.TokenParams, v float64) { t.P = v },
	"c":     func(t *billingexpr.TokenParams, v float64) { t.C = v },
	"len":   func(t *billingexpr.TokenParams, v float64) { t.Len = v },
	"cr":    func(t *billingexpr.TokenParams, v float64) { t.CR = v },
	"cc":    func(t *billingexpr.TokenParams, v float64) { t.CC = v },
	"cc1h":  func(t *billingexpr.TokenParams, v float64) { t.CC1h = v },
	"img":   func(t *billingexpr.TokenParams, v float64) { t.Img = v },
	"img_o": func(t *billingexpr.TokenParams, v float64) { t.ImgO = v },
	"ai":    func(t *billingexpr.TokenParams, v float64) { t.AI = v },
	"ao":    func(t *billingexpr.TokenParams, v float64) { t.AO = v },
}

// exprNumberRe 抓表达式里的数字字面量。
//
// 前面那个分组是为了避开**标识符里的数字**:变量名里有 `cc1h`、`img_o`,
// 直接 `\d+` 会把 `cc1h` 里的 1 当成一个阈值。Go 的 regexp 没有 lookbehind,
// 所以用一个捕获组把前导字符吃掉。
var exprNumberRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])(\d+(?:\.\d+)?)`)

// exprStringArgRe 抓 param("...") / header("...") 里的键名。
var exprStringArgRe = regexp.MustCompile(`(param|header)\s*\(\s*"([^"]*)"`)

// exprStringLitRe 抓表达式里**任意位置**的双引号字符串字面量。
var exprStringLitRe = regexp.MustCompile(`"([^"]*)"`)

// exprStringLiterals 返回表达式里出现过的字符串字面量,去重、保持出现顺序。
//
// 它们是烟测的探针值:按"客户端某字段等于/包含某个词"分档的表达式,只有把
// 那个词本身喂进去才评估得到那一档。空串跳过(它不改变任何比较的结果,
// 而 param() 取不到值时本来就是 nil)。
func exprStringLiterals(exprStr string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	for _, m := range exprStringLitRe.FindAllStringSubmatch(exprStr, -1) {
		lit := m[1]
		if lit == "" || seen[lit] {
			continue
		}
		seen[lit] = true
		out = append(out, lit)
	}
	return out
}

// exprBoundaryValues 返回表达式里出现过的数字字面量,按升序去重。
//
// 分档表达式的阈值就写在这些字面量里(`len <= 2000 ? ... : ...`),而烟测原本的
// 取值集合是固定的 {0, 1, 1e3, 1e5, 1e6} —— 落在这五个点之外的**每一个档**
// 都不会被评估到一次。
func exprBoundaryValues(exprStr string) []float64 {
	seen := map[float64]bool{}
	out := make([]float64, 0, 16)
	for _, m := range exprNumberRe.FindAllStringSubmatch(exprStr, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil || v < 0 || math.IsInf(v, 0) {
			continue
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= 32 {
			break
		}
	}
	sort.Float64s(out)
	return out
}

// smokeTestBoundaryVectors 在**每一个变量 × 每一个字面量**的边界上各取三点。
//
// 为什么必须按表达式取值而不是再加几个固定档:运营写的表达式自己带阈值,
// 阈值是什么只有表达式知道。`len <= 2000 ? tier("std", p*3+c*15)
// : (len <= 50000 ? tier("mid", p*3+c*15-20000) : ...)` 这一份在旧的五个点上
// 全部落进 std 与 big,中间那一档一次都没被评估到 —— 于是它带着一个
// 「token 少的时候静默零收入、token 中等的时候整条模型 400」的分支落了库。
//
// 基线取 {P:1, C:1, Len:1}:各档的常数项(那种 `- 20000` 的返现)在小 token 下
// 最容易翻负,正是要抓的形状。
func smokeTestBoundaryVectors(exprStr string) []billingexpr.TokenParams {
	values := exprBoundaryValues(exprStr)
	if len(values) == 0 {
		return nil
	}
	used := billingexpr.UsedVars(exprStr)
	base := billingexpr.TokenParams{P: 1, C: 1, Len: 1}
	vectors := make([]billingexpr.TokenParams, 0, len(values)*len(smokeTestVarSetters)*3)
	for name, set := range smokeTestVarSetters {
		if len(used) > 0 && !used[name] {
			continue
		}
		for _, v := range values {
			for _, at := range []float64{v - 1, v, v + 1} {
				if at < 0 {
					continue
				}
				vec := base
				set(&vec, at)
				vectors = append(vectors, vec)
			}
		}
	}
	return vectors
}

// smokeTestRequests 组出烟测要跑的请求形状。
//
// 前两条是固定的(空请求 + 一条带 anthropic-beta / service_tier 的)。真正要紧的是
// 后面那些:表达式可以用 `param("stream") == true ? ... : ...` 按**客户端传了什么**
// 分档,而原先那两条请求体里根本没有 stream 字段 —— 于是那一档从来不会被评估到,
// 而它落库之后是「任何流式客户端打这个模型都 400」。
//
// 做法是把表达式里出现过的每一个 param/header 键**一起**设成同一个探针值,
// 逐个值跑一遍。组合爆炸没有意义:要抓的是"某个分支为负",而这些分支绝大多数
// 只看一个键。
func smokeTestRequests(exprStr string) []billingexpr.RequestInput {
	requests := []billingexpr.RequestInput{
		{},
		{
			Headers: map[string]string{
				"anthropic-beta": "fast-mode-2026-02-01",
			},
			Body: []byte(`{"service_tier":"fast","stream_options":{"include_usage":true},"messages":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21]}`),
		},
	}

	params := make([]string, 0, 8)
	headers := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, m := range exprStringArgRe.FindAllStringSubmatch(exprStr, -1) {
		key := m[2]
		if key == "" || seen[m[1]+":"+key] {
			continue
		}
		seen[m[1]+":"+key] = true
		if m[1] == "param" {
			params = append(params, key)
		} else {
			headers = append(headers, key)
		}
		if len(params)+len(headers) >= 16 {
			break
		}
	}
	if len(params) == 0 && len(headers) == 0 {
		return requests
	}
	sort.Strings(params)
	sort.Strings(headers)

	probes := []any{true, false, "on", float64(0), float64(1)}
	for _, v := range exprBoundaryValues(exprStr) {
		probes = append(probes, v)
		if len(probes) >= 16 {
			break
		}
	}
	// 表达式里出现过的**字符串字面量**也必须当探针值喂进去。
	//
	// 原先的探针集合全是 true/false/"on"/0/1 加数字阈值,一个都命不中
	// `has(param("user"), "vip")` 或 `param("user") == "vip"` 这种按"客户端某个
	// 字段等于某个词"分档的写法。于是一条
	//
	//	tier("a", p * 3 + c * 15 - (has(param("user"), "vip") ? 20000 : 0))
	//
	// 能干净通过校验落库(实测保存返回 200),而线上对命中的请求算出负数,
	// 再被结算侧的非负地板静默夹成 0:上游照常被调用、真实成本照付、用户余额
	// 一分不扣,消费日志与正常计费请求逐字节相同(既不记 clamp 也不记喂进
	// 表达式的变量值),唯一痕迹是后端 stderr 里一行 SysError。
	// 而 OpenAI 协议里的 `user` 字段完全由调用方填写,谁都能自称 vip。
	//
	// 这不是 has() 专属:`==`、`has(param("model"),"gpt")` 都是同一类形状,
	// 所以抓的是**全部**字符串字面量,而不是某个函数的第二个参数。
	// 顺带把 tier 名也抓进来了,那只是多几条无害的探针。
	for _, lit := range exprStringLiterals(exprStr) {
		if len(probes) >= 32 {
			break
		}
		probes = append(probes, lit)
	}

	for _, probe := range probes {
		body := make(map[string]any, len(params))
		for _, key := range params {
			body[key] = probe
		}
		encoded, err := common.Marshal(body)
		if err != nil {
			continue
		}
		hdr := make(map[string]string, len(headers))
		for _, key := range headers {
			hdr[key] = fmt.Sprint(probe)
		}
		requests = append(requests, billingexpr.RequestInput{Headers: hdr, Body: encoded})
	}
	// 头部命中与否本身就是一档:再补一条「这些头一个都没有」的形状。
	if len(headers) > 0 && len(params) > 0 {
		requests = append(requests, billingexpr.RequestInput{Body: []byte(`{}`)})
	}
	return requests
}

func smokeTestExpr(exprStr string) error {
	vectors := append(smokeTestVectors(), smokeTestBoundaryVectors(exprStr)...)
	requests := smokeTestRequests(exprStr)

	for _, v := range vectors {
		for _, request := range requests {
			result, _, err := billingexpr.RunExprWithRequest(exprStr, v, request)
			if err != nil {
				return fmt.Errorf("vector %s: run failed: %w", describeVector(v), err)
			}
			if result < 0 {
				return fmt.Errorf("vector %s: result %f < 0", describeVector(v), result)
			}
		}
	}
	return nil
}

// describeVector 把翻负的那一条向量原样写进错误里。只报 {p, c} 的话,
// 「缓存命中时为负」这种被拒的表达式在管理端只会看到 p=1 c=1,运营无从知道
// 是哪一个变量把它拖下去的。
func describeVector(v billingexpr.TokenParams) string {
	parts := []string{fmt.Sprintf("p=%g", v.P), fmt.Sprintf("c=%g", v.C), fmt.Sprintf("len=%g", v.Len)}
	for _, kv := range []struct {
		name  string
		value float64
	}{
		{"cr", v.CR}, {"cc", v.CC}, {"cc1h", v.CC1h},
		{"img", v.Img}, {"img_o", v.ImgO}, {"ai", v.AI}, {"ao", v.AO},
	} {
		if kv.value != 0 {
			parts = append(parts, fmt.Sprintf("%s=%g", kv.name, kv.value))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
