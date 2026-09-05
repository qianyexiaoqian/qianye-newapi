package qianye

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// module_import_guard_test.go —— 功能模块之间的 import 图必须无环。
//
// # 为什么
//
// 星屑设计稿的第一版在同一节里写了两个相反的依赖方向:stardust 调 invite(当时叫
// commission)导出的邀请人解析,invite 又在兑换码回调末尾调 stardust —— 两条同时落地
// 就是 Go 编译期的 import cycle。方向定成了 stardust → invite(invite 只暴露
// AfterRedeemSuccess 等几个 func 变量给 stardust 赋值),但"定了"只活在文档里,下一个顺手在 invite 里
// import stardust 的人不会有任何东西提醒他,直到编译失败 —— 而那时通常已经改了一半。
//
// 这条守卫把整张模块间 import 图读出来做一次环检测:不只盯 invite↔stardust,
// 任何两个 qianye/modules/* 互相 import 都红。groupns/residue.go 头注释里"兄弟模块
// 互相 import 要靠注册表避开"就是同一条规矩。

const modulePkgPrefix = "github.com/QuantumNous/new-api/qianye/modules/"

func TestModuleImportGraphHasNoCycles(t *testing.T) {
	dirs, err := os.ReadDir("modules")
	require.NoError(t, err)

	graph := map[string]map[string]bool{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		name := d.Name()
		graph[name] = map[string]bool{}
		files, err := filepath.Glob(filepath.Join("modules", name, "*.go"))
		require.NoError(t, err)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			require.NoErrorf(t, err, "应当可解析 %s", f)
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(path, modulePkgPrefix) {
					continue
				}
				dep := strings.TrimPrefix(path, modulePkgPrefix)
				if i := strings.Index(dep, "/"); i >= 0 {
					dep = dep[:i]
				}
				if dep != name {
					graph[name][dep] = true
				}
			}
		}
	}

	// 具体点名那条曾经被写反的边,让失败信息直接指向设计稿。
	assert.False(t, graph["invite"]["stardust"],
		"invite 不得 import stardust:依赖方向是 stardust → invite,"+
			"invite 只暴露 AfterRedeemSuccess / PairRewardTotals 等 func 变量供 stardust 在 InstallHooks 里赋值(design-15 §4.3 / §4.6)")
	assert.True(t, graph["stardust"]["invite"], "stardust 必须 import invite:邀请人解析与日界都在那边,断言为假说明扫描器本身坏了")

	// 通用环检测:三色 DFS。
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = gray
		stack = append(stack, n)
		deps := make([]string, 0, len(graph[n]))
		for d := range graph[n] {
			deps = append(deps, d)
		}
		sort.Strings(deps)
		for _, d := range deps {
			switch color[d] {
			case gray:
				start := 0
				for i, s := range stack {
					if s == d {
						start = i
					}
				}
				cycle = append(append([]string{}, stack[start:]...), d)
				return true
			case white:
				if visit(d) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	names := make([]string, 0, len(graph))
	for n := range graph {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if color[n] == white && visit(n) {
			break
		}
	}
	assert.Emptyf(t, cycle, "qianye/modules 之间出现 import 环:%s。共用逻辑只能住在被依赖的一方或中立包(qianye/service/*),"+
		"反向通知用 func 变量(如 invite.AfterRedeemSuccess、groupns.PlanUnlockFundingState)", strings.Join(cycle, " → "))
}
