package common

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 钳制审计标记必须是 JSON 编得出来的。
//
// QuotaClamp.Original 是**导致这次钳制的那个原始 float64**,而最典型的钳制
// 来源恰恰是 ±Inf / NaN。json.Marshal 对这三个值会让**整张 map** 失败,
// 而 MapToJsonStr 把错误吞掉返回空串 —— 于是那条消费日志的整个 other 列变成
// ”,连带 model_ratio / group_ratio / cache_ratio / use_channel 一起丢光。
// AGENTS.md 承诺的「钳制事件双通道可检出」恰好在最需要它的那一类事件上失效。
func TestQuotaClampAuditMapSurvivesNonFiniteOriginals(t *testing.T) {
	cases := []struct {
		name     string
		original float64
		want     interface{}
	}{
		{"正无穷", math.Inf(1), "+Inf"},
		{"负无穷", math.Inf(-1), "-Inf"},
		{"NaN", math.NaN(), "NaN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clamp := &QuotaClamp{Op: "QuotaRound", Kind: QuotaClampOverflow, Original: tc.original, Clamped: MaxQuota}
			other := map[string]interface{}{
				"model_ratio":      2.0,
				"group_ratio":      1.0,
				"admin_info":       map[string]interface{}{"quota_saturation": clamp.AuditMap()},
				"quota_saturation": clamp.AuditMap(),
			}
			encoded := MapToJsonStr(other)
			require.NotEmpty(t, encoded, "一个非有限值不许把整条日志的 other 列抹成空串")
			assert.Contains(t, encoded, `"model_ratio":2`, "计费上下文必须还在")
			assert.Contains(t, encoded, `"original":"`+tc.want.(string)+`"`)

			back, err := StrToMap(encoded)
			require.NoError(t, err, "落库的值必须还能读回来")
			assert.Equal(t, 2.0, back["model_ratio"])
		})
	}
}

// 有限值仍然记成数字,不许因为上面那道改写变成字符串。
func TestQuotaClampAuditMapKeepsFiniteOriginalsNumeric(t *testing.T) {
	clamp := &QuotaClamp{Op: "QuotaFromFloat", Kind: QuotaClampOverflow, Original: 1e20, Clamped: MaxQuota}
	assert.Equal(t, 1e20, clamp.AuditMap()["original"])
	encoded := MapToJsonStr(map[string]interface{}{"quota_saturation": clamp.AuditMap()})
	assert.Contains(t, encoded, `"original":100000000000000000000`)
	assert.Nil(t, (*QuotaClamp)(nil).AuditMap())
}

// MapToJsonStr 自己也不许被一个非有限值整段打掉 —— 钳制标记只是最常见的
// 来源,倍率连乘溢出同样会把 +Inf 塞进别的字段。
func TestMapToJsonStrKeepsSiblingsWhenOneValueIsNonFinite(t *testing.T) {
	got := MapToJsonStr(map[string]interface{}{
		"model_ratio": 2.0,
		"broken":      math.Inf(1),
		"nested":      map[string]interface{}{"deep": math.NaN()},
		"list":        []interface{}{1.0, math.Inf(-1)},
	})
	require.NotEmpty(t, got)
	assert.Contains(t, got, `"model_ratio":2`)
	assert.Contains(t, got, `"broken":"+Inf"`)
	assert.Contains(t, got, `"deep":"NaN"`)
	assert.Contains(t, got, `"-Inf"`)
}
