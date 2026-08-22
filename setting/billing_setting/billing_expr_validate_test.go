package billing_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pkg/billingexpr/expr.md promises that a billing expression is compiled and
// smoke-tested for non-negative results on save. The smoke test existed but had
// zero callers, so a syntactically broken expression could be persisted (400ing
// every request for that model, surviving restarts) and an arithmetically
// negative one turned settlement into a credit. controller/ratio_sync.go writes
// this same key from a remote site's pricing feed, which makes the gap remotely
// reachable.
//
// Removing the smoke-test call from ValidateBillingExprJSON turns every
// wantErr row here green-to-red.
func TestValidateBillingExprJSONRejectsBrokenAndNegativeExpressions(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{
			name:  "empty value is accepted",
			value: "",
		},
		{
			name:  "empty map is accepted",
			value: `{}`,
		},
		{
			name:  "blank expression for a model is skipped",
			value: `{"m":"   "}`,
		},
		{
			name:  "well-formed non-negative expression",
			value: `{"m":"tier(\"base\", p * 3 + c * 15)"}`,
		},
		{
			name:  "tiered expression with a request probe",
			value: `{"m":"param(\"service_tier\") == \"fast\" ? tier(\"fast\", p * 4) : tier(\"normal\", p * 2)"}`,
		},
		{
			name:    "expression that goes negative for small prompts",
			value:   `{"m":"tier(\"promo\", p * 3 - 20000)"}`,
			wantErr: true,
		},
		{
			name:    "expression that is negative everywhere",
			value:   `{"m":"tier(\"credit\", 0 - p)"}`,
			wantErr: true,
		},
		{
			name:    "syntactically broken expression",
			value:   `{"m":"tier(\"base\", p * )"}`,
			wantErr: true,
		},
		{
			name:    "not a JSON object",
			value:   `["tier(\"base\", p)"]`,
			wantErr: true,
		},
		{
			name:    "truncated JSON",
			value:   `{"m":"tier(\"base\", p)"`,
			wantErr: true,
		},
		{
			name:    "one bad model among several rejects the whole write",
			value:   `{"good":"tier(\"base\", p * 3)","bad":"tier(\"promo\", p - 999999)"}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBillingExprJSON(tc.value)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestBillingExprOptionKeyMatchesTheRegisteredConfigKey(t *testing.T) {
	// The pre-persist validator switches on this literal key; if the config
	// prefix or the json tag ever changes, the validator silently stops running.
	assert.Equal(t, "billing_setting.billing_expr", BillingExprOptionKey)
}

// TestSmokeTestCatchesNegativesThatOnlyAppearOnSkewedVectors 是这道闸门的补齐。
//
// 原先的向量集只有 4 条 P==C、且 7 个子类变量恒为 0 的取值,于是两整类为负的
// 表达式能通过校验落库,而 smokeTestExpr 的注释写着它存在的理由就是挡住这两类:
//
//	① 只在 c>p 时为负 —— 落库之后该模型**每一次**请求都 400
//	   「pre-consume quota cannot be negative」(预扣侧 c 取 max_tokens 兜底 8192),
//	   客户端拿不到任何可操作的提示,重启也不恢复,只能翻到运营设置改表达式;
//	② 只在某个子类命中时为负(「缓存命中返一部分钱」这种很自然的促销形状)
//	   —— 平时正常收费,一旦 cached_tokens 上来金额就被地板夹成 0,静默零收入,
//	   而从管理端看表达式本身完全正常。
func TestSmokeTestCatchesNegativesThatOnlyAppearOnSkewedVectors(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{name: "只在 c>p 时为负", expr: `tier("promo", p * 3 - c * 1)`, wantErr: true},
		{name: "缓存命中返钱", expr: `tier("c", p * 3 + c * 15 - cr * 30)`, wantErr: true},
		{name: "缓存写入返钱", expr: `tier("cc", p * 3 - cc * 50)`, wantErr: true},
		{name: "1 小时缓存返钱", expr: `tier("cc1h", p * 3 - cc1h * 100)`, wantErr: true},
		{name: "图片输入返钱", expr: `tier("img", p * 3 - img * 100)`, wantErr: true},
		{name: "图片输出返钱", expr: `tier("imgo", p * 3 - img_o * 100)`, wantErr: true},
		{name: "音频输入返钱", expr: `tier("ai", p * 3 - ai * 100)`, wantErr: true},
		{name: "音频输出返钱", expr: `tier("ao", p * 3 - ao * 100)`, wantErr: true},
		{name: "单项都不为负、合起来为负", expr: `tier("mix", p * 3 + c * 3 - cr - cc - cc1h - img - img_o - ai - ao)`, wantErr: true},

		// 正常的表达式一条都不许被误伤 —— 系数全正的就是绝大多数真实定价。
		{name: "全正系数:通过", expr: `tier("base", p * 3 + c * 15 + cr * 0.3 + cc * 3.75)`},
		{name: "带全部子类的全正系数:通过", expr: `tier("full", p * 3 + c * 15 + cr * 0.3 + cc * 3.75 + cc1h * 6 + img * 2 + img_o * 4 + ai * 10 + ao * 20)`},
		{name: "分档 + 请求探针:通过", expr: `len > 200000 ? tier("hi", p * 6 + c * 30) : tier("lo", p * 3 + c * 15)`},
		{name: "常数项为正:通过", expr: `tier("flat", 1000 + p * 3)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := smokeTestExpr(tc.expr)
			if tc.wantErr {
				require.Error(t, err, "这条表达式会算出负数,不能落库")
				return
			}
			require.NoError(t, err, "正常的定价表达式被误伤了")
		})
	}
}

// 报错信息必须把翻负的那一条向量原样说出来。只报 {p, c} 的话,
// 「缓存命中时为负」被拒时运营在管理端只看到 p=1 c=1,无从知道是哪个变量。
func TestSmokeTestErrorNamesTheOffendingVariable(t *testing.T) {
	err := smokeTestExpr(`tier("c", p * 3 + c * 15 - cr * 30)`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cr=", "错误里必须点名是 cr 把结果拖成负数的")
}

// 向量集本身的形状:必须同时覆盖 P≠C 与「单个子类远大于 P/C」。
// 少了任何一半,上面那两整类就又能落库了。
func TestSmokeTestVectorsCoverSkewAndEverySubCategory(t *testing.T) {
	vectors := smokeTestVectors()
	require.NotEmpty(t, vectors)

	hasPGreater, hasCGreater := false, false
	dominant := map[string]bool{}
	for _, v := range vectors {
		if v.P > v.C {
			hasPGreater = true
		}
		if v.C > v.P {
			hasCGreater = true
		}
		for name, value := range map[string]float64{
			"cr": v.CR, "cc": v.CC, "cc1h": v.CC1h,
			"img": v.Img, "img_o": v.ImgO, "ai": v.AI, "ao": v.AO,
		} {
			if value > v.P && value > v.C {
				dominant[name] = true
			}
		}
	}
	assert.True(t, hasPGreater, "缺少「输入远大于输出」的向量")
	assert.True(t, hasCGreater, "缺少「输出远大于输入」的向量 —— tier(\"promo\", p*3-c) 就是从这个缺口落库的")
	for _, name := range []string{"cr", "cc", "cc1h", "img", "img_o", "ai", "ao"} {
		assert.Truef(t, dominant[name], "缺少「%s 远大于 P/C」的向量,减去这一项的表达式会漏网", name)
	}
}

// TestSmokeTestCatchesNegativesHiddenInsideABranch 是这道闸门的第二次补齐。
//
// 上一次补的是**向量的失衡**(P/C 对比、子类顶到极值)。这一次补的是**分档**:
// 运营写的表达式自己带阈值,而烟测原本的取值集合是固定的五个点
// {0, 1, 1e3, 1e5, 1e6} —— 落在那五个点之外的每一个档,以及每一个靠
// param()/header() 分出来的档,都不会被评估到一次。
//
// 两条实测落库过的表达式:
//
//	A（len 分档）"len <= 2000 ? std : (len <= 50000 ? mid : big)"
//	  五个点全部落进 std 与 big,中间那一档一次都没跑到。落库之后:命中 mid
//	  且 token 少时 raw 为负、被地板夹成 0 —— **静默零收入**,而同一个模型在
//	  另外两档收费完全正常,从管理端根本看不出这个模型有一段是免费的;
//	  预扣侧命中 mid 时则是 400「pre-consume quota cannot be negative」,
//	  整条模型对那个 token 区间不可用。
//
//	B（param 分档）'param("stream") == true ? s : n'
//	  烟测原本那两条请求体里根本没有 stream 字段,于是流式那一档从来不评估。
//	  落库之后**任何流式客户端**打这个模型都 400 —— 这一档由客户端说了算。
//
// 「保存时校验非负」这条承诺,在分档表达式上原本只覆盖了运营碰巧写在那五个点
// 附近的那些档。
func TestSmokeTestCatchesNegativesHiddenInsideABranch(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{
			name:    "中间那一档为负(len 分档)",
			expr:    `len <= 2000 ? tier("std", p*3 + c*15) : (len <= 50000 ? tier("mid", p*3 + c*15 - 20000) : tier("big", p*6 + c*30))`,
			wantErr: true,
		},
		{
			name:    "最高那一档为负",
			expr:    `len <= 128000 ? tier("lo", p*3 + c*15) : tier("hi", p*3 + c*15 - 999999)`,
			wantErr: true,
		},
		{
			name:    "刚好越过阈值一格才为负",
			expr:    `len < 4096 ? tier("small", p*3) : tier("big", p*3 - 100000)`,
			wantErr: true,
		},
		{
			// 一条**窄带**为负:两个阈值都是开区间,阈值点本身落在好的那一档上。
			// 只在字面量上取一个点是踩不到它的 —— 必须在 v-1 / v+1 上也各取一次。
			// 这种形状不是构造出来的:「某个 token 区间打折」写出来就长这样,
			// 而它落库之后正好是那个区间里的请求静默零收入。
			name:    "只有阈值旁边一格才为负的窄带",
			expr:    `len > 2000 && len < 2100 ? tier("band", 0 - p) : tier("ok", p*3)`,
			wantErr: true,
		},
		{
			name:    "流式那一档为负(param 探针)",
			expr:    `param("stream") == true ? tier("s", p*3 + c*15 - 50000) : tier("n", p*3 + c*15)`,
			wantErr: true,
		},
		{
			name:    "非流式那一档为负(反向,证明两侧都跑到)",
			expr:    `param("stream") == true ? tier("s", p*3 + c*15) : tier("n", p*3 + c*15 - 50000)`,
			wantErr: true,
		},
		{
			name:    "按请求头分档,命中那一档为负",
			expr:    `header("x-fast") != "" ? tier("fast", p*3 - 90000) : tier("std", p*3)`,
			wantErr: true,
		},
		{
			name:    "按 c 分档,中间档为负(不是只有 len 会分档)",
			expr:    `c <= 512 ? tier("a", p*3 + c*15) : (c <= 8192 ? tier("b", p*3 + c*15 - 70000) : tier("c", p*6 + c*30))`,
			wantErr: true,
		},

		// 正常的分档表达式一条都不许被误伤 —— 它们是这个功能存在的理由。
		{
			name: "三档全正:通过",
			expr: `len <= 2000 ? tier("std", p*3 + c*15) : (len <= 50000 ? tier("mid", p*4 + c*20) : tier("big", p*6 + c*30))`,
		},
		{
			name: "流式加价:通过",
			expr: `param("stream") == true ? tier("s", p*3.3 + c*16.5) : tier("n", p*3 + c*15)`,
		},
		{
			name: "请求头加价 + 常数起步价:通过",
			expr: `header("anthropic-beta") != "" ? tier("fast", 500 + p*6) : tier("std", 500 + p*3)`,
		},
		{
			name: "阈值上带小数:通过",
			expr: `len <= 1024.5 ? tier("a", p*3) : tier("b", p*6)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SmokeTestExpr(tc.expr)
			if tc.wantErr {
				require.Error(t, err, "这条表达式有一整档是负的,不能落库")
				return
			}
			require.NoError(t, err, "正常的分档定价被误伤了")
		})
	}
}

// TestBoundaryValuesIgnoreDigitsInsideIdentifiers 守阈值提取的一个具体坑。
//
// 变量名里有 cc1h / img_o,直接 `\d+` 会把 cc1h 里的 1 当成阈值。多提一个 1
// 本身无害(只是多跑几条向量),但它说明这条正则没有对齐标识符边界 —— 那时
// `p3` 这类将来可能出现的变量名会被拆出一个假阈值,而真正的阈值可能被吞掉。
func TestBoundaryValuesIgnoreDigitsInsideIdentifiers(t *testing.T) {
	got := exprBoundaryValues(`cc1h * 6 + img_o * 2 + tier("t", p * 3 - 20000)`)
	assert.Equal(t, []float64{2, 3, 6, 20000}, got,
		"cc1h 里的 1 不是阈值;而 6 / 2 / 3 / 20000 是真的字面量")
}
