package billing_setting

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 保存时那道非负烟测的**覆盖面**回归。
//
// 这四族形状全部实测过端到端:表达式干净落库、上游照常被调用、真实成本照付,
// 而结算侧的非负地板把负数静默夹成 0(service/tiered_settle.go),于是这次调用
// 一分不收,消费日志与正常计费的那一行逐字节同形。唯一痕迹是后端 stderr 里
// 一行 SysError。判据是 `param()` / 时间函数由**调用方或墙钟**决定,而它们
// 在保存那一刻全都取不到真值。
//
// 每一条都成对写:负的那一份必须被拒,同形状但恒非负的那一份必须放行 ——
// 否则"把闸门焊死"也能让上半张表全绿。
func TestSmokeTestCatchesNegativesGatedOnRequestShape(t *testing.T) {
	cases := []struct {
		name   string
		expr   string
		reject bool
	}{
		{
			name:   "嵌套路径 param(\"metadata.tier\")",
			expr:   `tier("a", p * 3 + c * 15 - (param("metadata.tier") == "vip" ? 20000 : 0))`,
			reject: true,
		},
		{
			name:   "嵌套路径但恒非负",
			expr:   `tier("a", p * 3 + c * 15 + (param("metadata.tier") == "vip" ? 20000 : 0))`,
			reject: false,
		},
		{
			// 比较写反(字面量在左)。association 那条正则只认 param() 在左的
			// 写法,所以这一条只能靠"字符串字面量当探针值 + 请求体按路径搭"
			// 那条路抓到 —— 它同时钉住了 setJSONPath 与字符串探针预算两件事。
			name:   "字面量写在左边 + 嵌套路径",
			expr:   `tier("a", p * 3 + c * 15 - ("vip" == param("metadata.tier") ? 20000 : 0))`,
			reject: true,
		},
		{
			name:   "两层嵌套 param(\"a.b.c\")",
			expr:   `tier("a", p * 3 + c * 15 - (param("meta.plan.name") == "gold" ? 20000 : 0))`,
			reject: true,
		},
		{
			name:   "数组下标 param(\"messages.0.role\")",
			expr:   `tier("a", p * 3 + c * 15 - (param("messages.0.role") == "system" ? 20000 : 0))`,
			reject: true,
		},
		{
			name:   "嵌套路径 + has()",
			expr:   `tier("a", p * 3 + c * 15 - (has(param("meta.plan"), "gold") ? 20000 : 0))`,
			reject: true,
		},
		{
			name:   "两个键的与条件",
			expr:   `tier("a", p * 3 + c * 15 - (param("user") == "vip" && param("channel") == "web" ? 20000 : 0))`,
			reject: true,
		},
		{
			name:   "三个键的与条件",
			expr:   `tier("a", p * 3 + c * 15 - (param("user") == "vip" && param("channel") == "web" && param("plan") == "max" ? 20000 : 0))`,
			reject: true,
		},
		{
			name:   "键 + 请求头的与条件",
			expr:   `tier("a", p * 3 + c * 15 - (param("user") == "vip" && header("x-tier") == "gold" ? 20000 : 0))`,
			reject: true,
		},
		{
			name:   "两个键的与条件但恒非负",
			expr:   `tier("a", p * 3 + c * 15 + (param("user") == "vip" && param("channel") == "web" ? 20000 : 0))`,
			reject: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := smokeTestExpr(tc.expr)
			if tc.reject {
				require.Error(t, err, "这条表达式会在命中的请求上算出负数，必须在保存时就被拒")
				assert.Contains(t, err.Error(), "< 0")
				assert.Contains(t, err.Error(), "body=",
					"错误里必须写出是哪一份请求体踩中的：只报 token 向量的话运营会去改系数，而问题不在那里")
			} else {
				assert.NoError(t, err, "恒非负的同形表达式不能被误拒")
			}
		})
	}
}

// 时间分档:表达式只在一天里的某个钟点 / 某个星期几为负。
//
// 不枚举墙钟时,这一族只在**运营点保存那一秒**的读数上被评估一次。实测把
// `hour("UTC") == N` 的 N 从 0 试到 23,只有等于当时 UTC 小时的那一条被拒,
// 另外 23 条全部干净落库。
func TestSmokeTestEvaluatesEveryClockReading(t *testing.T) {
	for hour := 0; hour <= 23; hour++ {
		expr := fmt.Sprintf(
			`hour("UTC") == %d ? tier("night", p * 3 + c * 15 - 99999) : tier("day", p * 3 + c * 15)`, hour)
		err := smokeTestExpr(expr)
		require.Errorf(t, err, "hour == %d 那一档为负，却放行了", hour)
		assert.Contains(t, err.Error(), "clock{",
			"错误里必须写出是哪一个钟点踩中的")
	}

	for weekday := 0; weekday <= 6; weekday++ {
		expr := fmt.Sprintf(
			`weekday("UTC") == %d ? tier("we", p * 3 + c * 15 - 99999) : tier("d", p * 3 + c * 15)`, weekday)
		require.Errorf(t, smokeTestExpr(expr), "weekday == %d 那一档为负，却放行了", weekday)
	}

	// 反面:按时段打折但打不到负数的那一份必须照常放行，否则这道闸门等于
	// 把 expr.md 与前端预设组里推荐的整类写法禁掉了。
	assert.NoError(t, smokeTestExpr(
		`hour("UTC") >= 22 || hour("UTC") < 6 ? tier("night", p * 1.5 + c * 7) : tier("day", p * 3 + c * 15)`))
	assert.NoError(t, smokeTestExpr(
		`weekday("UTC") == 0 || weekday("UTC") == 6 ? tier("we", p * 2.4 + c * 12) : tier("d", p * 3 + c * 15)`))
}

// 判定必须与"现在几点"无关。一道会因为墙钟而给出不同结论的校验闸门，
// 本身就是缺陷：同一份表达式上午存得下、下午存不下。
func TestSmokeTestClocksAreDeterministic(t *testing.T) {
	expr := `hour("Asia/Shanghai") == 3 ? tier("n", p * 3 - 99999) : tier("d", p * 3)`
	for i := 0; i < 3; i++ {
		require.Error(t, smokeTestExpr(expr))
	}
	clocks := smokeTestClocks(expr)
	require.NotEmpty(t, clocks)
	assert.NotNil(t, clocks[0],
		"用到时间函数时第一条也必须是固定的基准时钟，不能回落到真实墙钟")

	// 完全不用时间函数的表达式不该凭空多出一整维探针。
	plain := smokeTestClocks(`tier("a", p * 3 + c * 15)`)
	require.Len(t, plain, 1)
	assert.Nil(t, plain[0], "不用时间函数时必须原样走真实墙钟这一条")
}

// 字符串字面量的探针预算不能被数字阈值挤掉。
//
// 探针数组原先是「true/false/"on"/0/1 + 最多 16 个数字阈值 + 字符串补到 32」。
// 只要表达式里有 ≥11 个不同数字(任何多档定价都轻易超过)，数字就把坑填满，
// 字符串只剩 16 格 —— 实测 16 档、19 个字面量时，`vip` 排在第 19 位落在坑外，
// 整条式子一鉴一放。
func TestSmokeTestKeepsStringProbesWhenTierCountGrows(t *testing.T) {
	for _, tiers := range []int{8, 12, 16, 24, 30} {
		t.Run(fmt.Sprintf("%d 档", tiers), func(t *testing.T) {
			var sb strings.Builder
			for i := tiers; i >= 1; i-- {
				fmt.Fprintf(&sb, `len > %d ? tier("t%d", p * 3 + c * 15) : (`, i*1000, i)
			}
			// 刻意写成字面量在左:这样它只能被字符串探针抓到,
			// 而不会被 键→字面量 配对那条路顺手兜住。
			sb.WriteString(`tier("vipband", p * 3 + c * 15 - ("vip" == param("user") ? 20000 : 0))`)
			sb.WriteString(strings.Repeat(")", tiers))

			err := smokeTestExpr(sb.String())
			require.Error(t, err, "无论档数多少，vip 那个词都必须仍在探针集里")
			assert.Contains(t, err.Error(), "< 0")
		})
	}
}

// setJSONPath 是"按 gjson 路径造探针体"的那一半。gjson 对平坦点号键
// {"metadata.tier":"vip"} 取 metadata.tier 返回不存在 —— 这是整条 F1 的根因。
func TestSetJSONPathBuildsWhatGjsonCanRead(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"user", `{"user":"vip"}`},
		{"metadata.tier", `{"metadata":{"tier":"vip"}}`},
		{"a.b.c", `{"a":{"b":{"c":"vip"}}}`},
		{"messages.0.role", `{"messages":[{"role":"vip"}]}`},
		{"messages.1.role", `{"messages":[null,{"role":"vip"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			body := map[string]any{}
			setJSONPath(body, tc.path, "vip")
			raw, err := common.Marshal(body)
			require.NoError(t, err)
			encoded := string(raw)
			assert.Equal(t, tc.want, encoded)

			got, _, err := billingexpr.RunExprWithRequest(
				fmt.Sprintf(`tier("a", param(%q) == "vip" ? 1.0 : 0.0)`, tc.path),
				billingexpr.TokenParams{},
				billingexpr.RequestInput{Body: []byte(encoded)})
			require.NoError(t, err)
			assert.Equal(t, float64(1), got,
				"造出来的探针体必须真的能被 param(%q) 读到", tc.path)
		})
	}
}

// 键→字面量的配对是"两个键各要一个不同值"那族与条件唯一便宜的解法。
func TestExprKeyAssociationsPairsKeysWithTheirLiterals(t *testing.T) {
	assocs := exprKeyAssociations(
		`param("user") == "vip" && header("x-tier") == "gold" && param("n") >= 42 && has(param("m"), "pro") && param("s") == true`)
	got := map[string][]any{}
	for _, a := range assocs {
		got[a.kind+":"+a.key] = a.values
	}
	assert.Equal(t, []any{"vip"}, got["param:user"])
	assert.Equal(t, []any{"gold"}, got["header:x-tier"])
	assert.Equal(t, []any{float64(42)}, got["param:n"])
	assert.Equal(t, []any{"pro"}, got["param:m"])
	assert.Equal(t, []any{true}, got["param:s"])
}
