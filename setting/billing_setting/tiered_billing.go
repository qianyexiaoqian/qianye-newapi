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

// BillingModeOptionKey is the options-table key that holds the per-model
// billing-mode map. It is the sibling of BillingExprOptionKey and the two are
// only meaningful together.
const BillingModeOptionKey = "billing_setting." + BillingModeField

// ValidateBillingModeJSON is the pre-persist gate for the billing_mode option.
//
// 这个键此前在 model/option.go 的 validateOptionValue 里**一条 case 都没有**,
// 而它的兄弟键 billing_expr 有。于是两件事都放行:
//
//  1. 任意字符串都能当计费模式。`{"gpt-4o":"totally-bogus-mode"}` 干净落库
//     (实测 HTTP 200)。它今天恰好无害(非 tiered_expr 一律回落倍率),
//     但那是巧合而不是判据 —— 下一个新增的模式一旦被拼错就静默走错分支。
//  2. 把一个模型标成 tiered_expr 却不给表达式。此后该模型**每一次**请求都
//     400「is configured as tiered_expr but has no billing expression」,
//     重启不自愈;而 model/pricing.go 在表达式为空时**不下发** billing_mode,
//     定价页把它显示成一个普通倍率模型 —— 界面说正常、线上 100% 失败。
//
// 第 1 条在这里直接拒。第 2 条**刻意只告警不拒**:两个键各写各的(管理端是
// 逐键串行 PUT),任何一种保存顺序下都必然存在一个中间状态使某一侧暂时孤立,
// 拒绝会让"新增一个阶梯模型"这个正常操作在第一发 PUT 上就失败。所以判据留在
// relay 侧(fail-closed:400,不中继不扣费),这里负责把它**说出来**,让运维
// 不必等到用户报障才知道。
func ValidateBillingModeJSON(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var modes map[string]string
	if err := common.UnmarshalJsonStr(value, &modes); err != nil {
		return fmt.Errorf("billing_mode 不是合法的 JSON 对象: %w", err)
	}
	orphans := make([]string, 0, 4)
	for modelName, mode := range modes {
		switch strings.TrimSpace(mode) {
		case BillingModeRatio:
		case BillingModeTieredExpr:
			if expr, ok := GetBillingExpr(modelName); !ok || strings.TrimSpace(expr) == "" {
				orphans = append(orphans, modelName)
			}
		default:
			return fmt.Errorf("模型 %s 的计费模式 %q 不是合法取值(只能是 %s 或 %s)",
				modelName, mode, BillingModeRatio, BillingModeTieredExpr)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		common.SysError(fmt.Sprintf(
			"billing_mode: 下列模型被标成 %s 但 billing_expr 里没有可用的表达式,它们的每一次请求都会 400,请补上表达式或把模式改回 %s: %s",
			BillingModeTieredExpr, BillingModeRatio, strings.Join(orphans, ", ")))
	}
	return nil
}

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
	// 反向的孤立同样要说出来:删掉某个模型的表达式时,billing_mode 里那条
	// tiered_expr 不会跟着走,而那正是"每一次请求都 400"的另一半来路。
	// 与 ValidateBillingModeJSON 同理,只告警不拒 —— 拒了就没有任何一种保存
	// 顺序能同时通过两道闸。
	orphans := make([]string, 0, 4)
	for modelName, mode := range billingSetting.BillingMode {
		if strings.TrimSpace(mode) != BillingModeTieredExpr {
			continue
		}
		if strings.TrimSpace(exprs[modelName]) == "" {
			orphans = append(orphans, modelName)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		common.SysError(fmt.Sprintf(
			"billing_expr: 下列模型在 billing_mode 里仍是 %s,但这次保存之后它们没有可用的表达式,每一次请求都会 400: %s",
			BillingModeTieredExpr, strings.Join(orphans, ", ")))
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
//
// 三个数字形状必须全收,因为**管理端的可视化编辑器三个都放行**
// (web/src/features/pricing/lib/billing-expr.ts 的 NUMERIC_LITERAL_REGEX
// 是 `-?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?`,并把那段文本原样拼进表达式):
//   - 十进制      2000
//   - 科学计数法  2e3 / 1.23e8 / 1E8 / 1.5e+3
//   - 无整数位    .5
//
// 少收任何一种,那一档的阈值就取不到,smokeTestBoundaryVectors 永远踩不到
// 它,于是**同一条规则换个写法就能绕过非负烟测**:`c <= 2000 ? … : … - 50000`
// 被拒,`c <= 2e3 ? … : … - 50000` 干净落库,而后者在 c=2001 时结算为负。
var exprNumberRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])(\d+(?:\.\d*)?(?:[eE][+-]?\d+)?|\.\d+(?:[eE][+-]?\d+)?)`)

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
		if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
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
// 三件事必须同时做对,少一件就有一整族为负的分支能干净落库:
//
//  1. **请求体要按 gjson 路径搭成嵌套结构**。param() 读的是一条 JSON 路径
//     (expr.md: "Reads a JSON path"),而探针原先造的是平坦顶层键
//     body["metadata.tier"]。gjson 对 {"metadata.tier":"vip"} 取 metadata.tier
//     返回"不存在" —— 于是
//
//     tier("a", p*3 + c*15 - (param("metadata.tier") == "vip" ? 20000 : 0))
//
//     这一档一次都评估不到。落库之后调用方只要在请求体里多写一个字段就把这次
//     调用变成免费的:上游照常被调用、真实成本照付、用户余额一分不扣,而
//     OpenAI 协议里的 metadata / user 完全由调用方填写。实测端到端两条除请求体
//     多两个字段外完全相同的请求,一条扣 225、一条扣 0,两行日志的 other
//     逐字节同形。
//
//  2. **要按"哪个键跟哪个词比"造组合**。把所有键设成同一个探针值时,
//     param("user") == "vip" && param("channel") == "web" 这种两键与条件一次都
//     命不中。exprKeyAssociations 从表达式文本里把 键→字面量 的配对抽出来,
//     再让每个键各取各的值同时成立。全键 × 全探针的组合是指数的,而语法上
//     那个配对已经写在脸上了。
//
//  3. **字符串字面量不能被数字阈值挤掉**。探针数组原先是 true/false/"on"/0/1
//     后面接最多 16 个数字阈值,再把字符串字面量补到同一个 32 的坑里 ——
//     档数一多(实测 16 档、19 个字面量就够),真正把关的那个词排在坑外,
//     整条式子一鉴一放。现在数字与字符串各有各的预算。
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
		if len(probes) >= smokeTestNumericProbeCap {
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
	stringProbes := 0
	for _, lit := range exprStringLiterals(exprStr) {
		if stringProbes >= smokeTestStringProbeCap {
			break
		}
		stringProbes++
		probes = append(probes, lit)
	}

	for _, probe := range probes {
		body := map[string]any{}
		for _, key := range params {
			setJSONPath(body, key, probe)
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
	requests = append(requests, smokeTestAssociationRequests(exprStr)...)
	// 头部命中与否本身就是一档:再补一条「这些头一个都没有」的形状。
	if len(headers) > 0 && len(params) > 0 {
		requests = append(requests, billingexpr.RequestInput{Body: []byte(`{}`)})
	}
	return requests
}

const (
	// 数字阈值与字符串字面量各自的探针预算。分开记的理由见 smokeTestRequests
	// 第 3 条:合在一个坑里时字符串排在数字后面,档数一多就被整族挤出探针集。
	smokeTestNumericProbeCap = 16
	smokeTestStringProbeCap  = 32
	// 一份表达式最多组出多少条"每个键各取各值"的组合请求。
	smokeTestComboCap = 64
)

// exprAssocCmpRe 抓 `param("K") == "L"` / `header("K") >= 123` 这一族比较。
var exprAssocCmpRe = regexp.MustCompile(
	`(param|header)\s*\(\s*"([^"]*)"\s*\)\s*(?:==|!=|>=|<=|>|<)\s*(?:"([^"]*)"|(\d+(?:\.\d+)?)|(true|false))`)

// exprAssocHasRe 抓 `has(param("K"), "L")`。
var exprAssocHasRe = regexp.MustCompile(
	`has\s*\(\s*(param|header)\s*\(\s*"([^"]*)"\s*\)\s*,\s*"([^"]*)"\s*\)`)

type keyAssociation struct {
	kind   string // param | header
	key    string
	values []any
}

// exprKeyAssociations 把「哪个键跟哪个字面量比」从表达式文本里抽出来。
//
// 这是"两个键各要一个不同的值"那一族与条件唯一便宜的解法:全键 × 全探针的
// 组合是指数的,而语法上 `param("user") == "vip"` 已经把配对写在脸上了。
func exprKeyAssociations(exprStr string) []keyAssociation {
	order := make([]string, 0, 8)
	byKey := make(map[string]*keyAssociation, 8)
	add := func(kind, key string, value any) {
		if key == "" || value == nil {
			return
		}
		id := kind + ":" + key
		assoc, ok := byKey[id]
		if !ok {
			assoc = &keyAssociation{kind: kind, key: key}
			byKey[id] = assoc
			order = append(order, id)
		}
		for _, existing := range assoc.values {
			if existing == value {
				return
			}
		}
		if len(assoc.values) >= 4 {
			return
		}
		assoc.values = append(assoc.values, value)
	}

	for _, m := range exprAssocCmpRe.FindAllStringSubmatch(exprStr, -1) {
		switch {
		case m[3] != "":
			add(m[1], m[2], m[3])
		case m[4] != "":
			if v, err := strconv.ParseFloat(m[4], 64); err == nil {
				add(m[1], m[2], v)
			}
		case m[5] != "":
			add(m[1], m[2], m[5] == "true")
		}
	}
	for _, m := range exprAssocHasRe.FindAllStringSubmatch(exprStr, -1) {
		add(m[1], m[2], m[3])
	}

	out := make([]keyAssociation, 0, len(order))
	for _, id := range order {
		out = append(out, *byKey[id])
	}
	return out
}

// smokeTestAssociationRequests 造出「每个键同时取到自己那个字面量」的请求。
//
// 组合数按 smokeTestComboCap 截断,截断时退化成"一个键取字面量、其余键缺席"
// 的逐条形态 —— 那仍然比原先的全键同值强,而全键同值对与条件是零覆盖。
func smokeTestAssociationRequests(exprStr string) []billingexpr.RequestInput {
	assocs := exprKeyAssociations(exprStr)
	if len(assocs) == 0 {
		return nil
	}

	combos := 1
	for _, assoc := range assocs {
		combos *= len(assoc.values)
		if combos > smokeTestComboCap {
			break
		}
	}

	out := make([]billingexpr.RequestInput, 0, smokeTestComboCap+len(assocs))
	if combos <= smokeTestComboCap {
		indices := make([]int, len(assocs))
		for {
			body := map[string]any{}
			hdr := map[string]string{}
			for i, assoc := range assocs {
				value := assoc.values[indices[i]]
				if assoc.kind == "param" {
					setJSONPath(body, assoc.key, value)
				} else {
					hdr[assoc.key] = fmt.Sprint(value)
				}
			}
			if encoded, err := common.Marshal(body); err == nil {
				out = append(out, billingexpr.RequestInput{Headers: hdr, Body: encoded})
			}
			pos := len(assocs) - 1
			for pos >= 0 {
				indices[pos]++
				if indices[pos] < len(assocs[pos].values) {
					break
				}
				indices[pos] = 0
				pos--
			}
			if pos < 0 {
				break
			}
		}
		return out
	}

	for _, assoc := range assocs {
		for _, value := range assoc.values {
			body := map[string]any{}
			hdr := map[string]string{}
			if assoc.kind == "param" {
				setJSONPath(body, assoc.key, value)
			} else {
				hdr[assoc.key] = fmt.Sprint(value)
			}
			if encoded, err := common.Marshal(body); err == nil {
				out = append(out, billingexpr.RequestInput{Headers: hdr, Body: encoded})
			}
		}
	}
	return out
}

// setJSONPath 按 gjson 的路径语义把一个值写进探针请求体。
//
// 只处理探针需要的那一小块语法:`.` 分段、`\.` 转义成字面点号、纯数字段当
// 数组下标(`messages.0.role`)。写不进去(同一份体里既有 `a` 又有 `a.b`
// 这种自相矛盾的路径)时静默跳过 —— 少一条探针远好过让整个烟测因为一份
// 畸形表达式而崩掉。
func setJSONPath(root map[string]any, path string, value any) {
	segments := splitJSONPath(path)
	if len(segments) == 0 {
		return
	}
	setJSONSegments(root, segments, value)
}

// setJSONSegments 递归写入。数组下标那一支必须回写整个切片(切片是值类型,
// 就地 append 改不到父容器里的那一份)。
func setJSONSegments(container any, segments []string, value any) {
	seg := segments[0]
	rest := segments[1:]

	switch node := container.(type) {
	case map[string]any:
		if len(rest) == 0 {
			node[seg] = value
			return
		}
		child, ok := node[seg]
		if !ok {
			child = newJSONChild(rest[0])
		}
		node[seg] = writeJSONChild(child, rest, value)
	case *[]any:
		idx, err := strconv.Atoi(seg)
		if err != nil || idx < 0 || idx > 32 {
			return
		}
		for len(*node) <= idx {
			*node = append(*node, nil)
		}
		if len(rest) == 0 {
			(*node)[idx] = value
			return
		}
		child := (*node)[idx]
		if child == nil {
			child = newJSONChild(rest[0])
		}
		(*node)[idx] = writeJSONChild(child, rest, value)
	}
}

func newJSONChild(nextSegment string) any {
	if isAllDigits(nextSegment) {
		return []any{}
	}
	return map[string]any{}
}

func writeJSONChild(child any, rest []string, value any) any {
	switch typed := child.(type) {
	case map[string]any:
		setJSONSegments(typed, rest, value)
		return typed
	case []any:
		slice := typed
		setJSONSegments(&slice, rest, value)
		return slice
	default:
		// 路径互相矛盾(先写了标量,又要往里面钻)。跳过而不是覆盖:
		// 覆盖会让同一份探针里先写的那个键静默消失。
		return child
	}
}

func splitJSONPath(path string) []string {
	segments := make([]string, 0, 4)
	var cur strings.Builder
	for i := 0; i < len(path); i++ {
		switch {
		case path[i] == '\\' && i+1 < len(path):
			i++
			cur.WriteByte(path[i])
		case path[i] == '.':
			segments = append(segments, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(path[i])
		}
	}
	segments = append(segments, cur.String())
	if len(segments) > 8 {
		return nil
	}
	for _, seg := range segments {
		if seg == "" {
			return nil
		}
	}
	return segments
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// smokeTestClockDims 是每一个时间函数的取值范围,与 Go 标准库一致。
var smokeTestClockDims = []struct {
	name   string
	lo, hi int
	set    func(*billingexpr.ClockOverride, int)
}{
	{"hour", 0, 23, func(c *billingexpr.ClockOverride, v int) { c.Hour = v }},
	{"minute", 0, 59, func(c *billingexpr.ClockOverride, v int) { c.Minute = v }},
	{"weekday", 0, 6, func(c *billingexpr.ClockOverride, v int) { c.Weekday = v }},
	{"month", 1, 12, func(c *billingexpr.ClockOverride, v int) { c.Month = v }},
	{"day", 1, 31, func(c *billingexpr.ClockOverride, v int) { c.Day = v }},
}

var exprTimeFuncRe = regexp.MustCompile(`\b(hour|minute|weekday|month|day)\s*\(`)

// smokeTestClockCap 是时钟探针的硬上限。几个维度一起用上时全排列有六万多种,
// 而烟测是管理端保存路径上的同步动作。
const smokeTestClockCap = 64

// smokeTestClocks 组出烟测要读到的"墙钟"。
//
// 不加这一维的表现是:`hour("UTC") == 2 ? tier("night", p*3 - 99999) : ...`
// 这类"夜间打折"表达式只在**运营点保存那一秒**的钟点上被评估一次。实测把
// N 从 0 试到 23,只有等于当时 UTC 小时的那一条被拦下,另外 23 条全部干净
// 落库 —— 到了那个时段全站该模型静默零收入(或者整条模型 400,取决于那个
// 常数减在哪一档),而管理端看不出任何异常。按时段分档是 expr.md 与前端
// 预设组(Night discount / Weekend discount)双重推荐的写法,不是歪门邪道。
//
// 用到时间函数时返回的第一条是一个**固定**的基准时钟而不是 nil:一道会因为
// 当前几点钟而给出不同结论的校验闸门,本身就是缺陷。
func smokeTestClocks(exprStr string) []*billingexpr.ClockOverride {
	used := map[string]bool{}
	for _, m := range exprTimeFuncRe.FindAllStringSubmatch(exprStr, -1) {
		used[m[1]] = true
	}
	if len(used) == 0 {
		return []*billingexpr.ClockOverride{nil}
	}

	base := billingexpr.ClockOverride{Month: 1, Day: 1}
	literals := exprBoundaryValues(exprStr)

	type clockDim struct {
		index      int
		candidates []int
	}
	dims := make([]clockDim, 0, len(smokeTestClockDims))
	for i, d := range smokeTestClockDims {
		if !used[d.name] {
			continue
		}
		all := make([]int, 0, d.hi-d.lo+1)
		for v := d.lo; v <= d.hi; v++ {
			all = append(all, v)
		}
		dims = append(dims, clockDim{index: i, candidates: all})
	}

	product := 1
	for _, d := range dims {
		product *= len(d.candidates)
	}

	out := []*billingexpr.ClockOverride{&base}
	push := func(c billingexpr.ClockOverride) {
		if len(out) >= smokeTestClockCap {
			return
		}
		out = append(out, &c)
	}

	if product+1 <= smokeTestClockCap {
		indices := make([]int, len(dims))
		for {
			c := base
			for i, d := range dims {
				smokeTestClockDims[d.index].set(&c, d.candidates[indices[i]])
			}
			push(c)
			pos := len(dims) - 1
			for pos >= 0 {
				indices[pos]++
				if indices[pos] < len(dims[pos].candidates) {
					break
				}
				indices[pos] = 0
				pos--
			}
			if pos < 0 {
				break
			}
		}
		return out
	}

	// 全排列放不下时:按表达式自己的数字字面量取点(阈值就写在里面),
	// 逐维各扫一遍。交叉条件覆盖不到,那是硬上限下的取舍 —— 但单条件的
	// "夜间/周末打折"是绝大多数,而它们在这里全被覆盖。
	for _, d := range dims {
		spec := smokeTestClockDims[d.index]
		picked := map[int]bool{spec.lo: true, spec.hi: true}
		for _, lit := range literals {
			v := int(lit)
			for _, at := range []int{v - 1, v, v + 1} {
				if at >= spec.lo && at <= spec.hi {
					picked[at] = true
				}
			}
		}
		values := make([]int, 0, len(picked))
		for v := range picked {
			values = append(values, v)
		}
		sort.Ints(values)
		for _, v := range values {
			c := base
			spec.set(&c, v)
			push(c)
		}
	}
	return out
}

// smokeTestEvalBudget 是一次烟测最多跑多少遍表达式。
//
// 越过它之后,时钟与请求这两维只与**基础**向量集相乘(而不是与逐字面量展开的
// 边界向量集相乘)。三族形状各自仍被覆盖,而最坏耗时保持在管理端点保存能
// 接受的量级。
const smokeTestEvalBudget = 300000

func smokeTestExpr(exprStr string) error {
	baseVectors := smokeTestVectors()
	vectors := append(append([]billingexpr.TokenParams{}, baseVectors...),
		smokeTestBoundaryVectors(exprStr)...)
	requests := smokeTestRequests(exprStr)
	clocks := smokeTestClocks(exprStr)

	overBudget := len(vectors)*len(requests)*len(clocks) > smokeTestEvalBudget

	for ci, clock := range clocks {
		for ri, request := range requests {
			request.Clock = clock
			run := vectors
			if overBudget && ci > 0 && ri > 0 {
				run = baseVectors
			}
			for _, v := range run {
				result, _, err := billingexpr.RunExprWithRequest(exprStr, v, request)
				if err != nil {
					return fmt.Errorf("vector %s%s: run failed: %w",
						describeVector(v), describeEnv(request), err)
				}
				if result < 0 {
					return fmt.Errorf("vector %s%s: result %f < 0",
						describeVector(v), describeEnv(request), result)
				}
			}
		}
	}
	return nil
}

// describeEnv 把翻负的那一条请求与时钟一起写进错误里。
//
// 只报向量的话,"客户端自称 vip 时为负"与"凌晨两点为负"在管理端看到的是
// 同一句 `{p=0, c=0, len=0}` —— 运营会去改 token 那一侧的系数,而问题根本
// 不在那里。
func describeEnv(request billingexpr.RequestInput) string {
	parts := make([]string, 0, 3)
	if len(request.Body) > 0 && string(request.Body) != "{}" {
		body := string(request.Body)
		if len(body) > 160 {
			body = body[:160] + "…"
		}
		parts = append(parts, "body="+body)
	}
	if len(request.Headers) > 0 {
		keys := make([]string, 0, len(request.Headers))
		for k := range request.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, k+"="+request.Headers[k])
		}
		parts = append(parts, "headers{"+strings.Join(pairs, ", ")+"}")
	}
	if c := request.Clock; c != nil {
		parts = append(parts, fmt.Sprintf("clock{hour=%d, minute=%d, weekday=%d, month=%d, day=%d}",
			c.Hour, c.Minute, c.Weekday, c.Month, c.Day))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
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
