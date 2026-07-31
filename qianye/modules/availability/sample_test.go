package availability

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sync/atomic"
	"testing"
	_ "unsafe" // go:linkname

	"github.com/QuantumNous/new-api/qianye/config"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qyConfig 指向 qianye/config 的当前配置快照。
//
// onRelaySample 首行就读 config.Get().Availability.Enabled,而 config.Load()
// 只能从磁盘 YAML 读。不把开关打开,下面的测试全部会在第一行早退 ——
// 那种"绿"什么都没证明。commission 的测试也用同一手法。
//
//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// TestOnRelaySampleSurvivesDegenerateRelayInfo 驱动整条同步段。
//
// RelayInfo 有多条构造路径,采样 hook 挂在最尾部,拿到的可能是任何一种。
// 这里喂一个"什么都没填"的极端值 —— 零值 StartTime、nil StreamStatus、
// 空模型名、空分组 —— 断言它既不 panic 也不把垃圾数据放行。
//
// 可用率监控相对 relay 永远是附加物,不该有能力让用户请求 500。
func TestOnRelaySampleSurvivesDegenerateRelayInfo(t *testing.T) {
	cfg := &config.Config{Enabled: true}
	cfg.Availability.Enabled = true
	prev := qyConfig.Swap(cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })
	require.True(t, config.Get().Availability.Enabled,
		"开关没打开的话下面全部在第一行早退,测了个寂寞")

	cases := []struct {
		name string
		info *relaycommon.RelayInfo
	}{
		{"nil info", nil},
		{"全零值", &relaycommon.RelayInfo{}},
		{"只有模型名,时间与分组都缺", &relaycommon.RelayInfo{OriginModelName: "gpt-4o"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				onRelaySample(tc.info, false, 0)
			})
		})
	}

	// 缺模型名的样本必须被计数后丢弃,而不是静默消失 ——
	// 静默丢弃会让"分母对不上"变成无从排查的悬案。
	_, ok := buildSample(&relaycommon.RelayInfo{}, true, 0)
	assert.False(t, ok, "没有模型名的样本无法归档到任何单元格")

	s, ok := buildSample(&relaycommon.RelayInfo{OriginModelName: "gpt-4o"}, true, 0)
	require.True(t, ok)
	assert.Equal(t, unknownGroup, s.Group, "分组缺失必须落到 unknown 桶,不能是空串")
	assert.Zero(t, s.LatencyMs, "StartTime 为零值时延迟必须归零,否则是从 1970 年算起的天文数字")
}

// TestOnRelaySampleGuardsSyncSection 是结构性断言,不是行为断言 —— 这一点要说明白。
//
// pkg/perf_metrics/qy_export.go 明写实现方"自行吞掉 panic"。guard.HotAsync
// 内部的 recover 只保护闭包体(observe),保护不到同步段的 buildSample。
// 上面那条行为测试证明的是"今天的 buildSample 不 panic",证明不了
// "明天上游给 RelayInfo 加了字段之后仍然安全" —— 后者只能靠 defer 在不在。
//
// 之所以退而求其次用 AST:同步段当前没有任何可达的 panic 路径,
// 要构造一个就得往生产代码里插测试专用的注入点,那比缺陷本身更糟。
func TestOnRelaySampleGuardsSyncSection(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "sample.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "onRelaySample" {
			fn = d
			return false
		}
		return true
	})
	require.NotNil(t, fn, "onRelaySample 不见了 —— 采样 hook 被改名或删除")

	// defer 必须排在 buildSample 之前,否则保护不到它。
	deferAt, buildAt := -1, -1
	for i, stmt := range fn.Body.List {
		if d, ok := stmt.(*ast.DeferStmt); ok && deferAt < 0 {
			if sel, ok := d.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RecoverHot" {
				deferAt = i
			}
		}
		if buildAt < 0 && containsCall(stmt, "buildSample") {
			buildAt = i
		}
	}
	require.GreaterOrEqual(t, deferAt, 0,
		"onRelaySample 缺少 defer guard.RecoverHot —— 同步段 panic 会一路冒泡进 relay,变成用户请求 500")
	require.GreaterOrEqual(t, buildAt, 0, "onRelaySample 不再调用 buildSample,本断言需要重写")
	assert.Less(t, deferAt, buildAt, "defer 必须排在 buildSample 之前才保护得到它")
}

func containsCall(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if call, ok := x.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
				return false
			}
		}
		return !found
	})
	return found
}
