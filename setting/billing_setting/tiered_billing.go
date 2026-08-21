package billing_setting

import (
	"fmt"
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

func smokeTestExpr(exprStr string) error {
	vectors := smokeTestVectors()
	requests := []billingexpr.RequestInput{
		{},
		{
			Headers: map[string]string{
				"anthropic-beta": "fast-mode-2026-02-01",
			},
			Body: []byte(`{"service_tier":"fast","stream_options":{"include_usage":true},"messages":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21]}`),
		},
	}

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
