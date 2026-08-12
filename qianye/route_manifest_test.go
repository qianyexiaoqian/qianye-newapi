package qianye

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ManifestPath 是扩展路由清单的仓库相对路径。
//
// 它是**给前端读的**:前端的所有 qy 请求路径都要拿它对账,一条前端调得出、
// 后端注册不了的路径必须在测试期变红,而不是等项目方在浏览器 console 里
// 看见 404 —— 违规类型页那次就是这么被发现的,而且报了三次。
const ManifestPath = "route_manifest.txt"

const manifestUpdateEnv = "QY_ROUTE_MANIFEST_UPDATE"

const manifestHeader = `# qy 扩展的 HTTP 路由清单 —— 由 go test 生成,不要手改。
#
# 生成方式(改动任何路由之后必须重跑,否则 TestQyRouteManifestIsCurrent 会红):
#     QY_ROUTE_MANIFEST_UPDATE=1 go test ./qianye/ -run TestQyRouteManifestIsCurrent -count=1
#
# 内容来自 RegisterRoutes 真实挂上的 gin 路由树(engine.Routes()),不是手抄的清单,
# 因此组前缀 /api/qy、/api/qy/admin 与参数段名都与线上逐字一致。
# 消费方:web/src/features/qy/__tests__/qy-request-paths.ts
`

// buildRouteManifest 把 RegisterRoutes 真正挂上的路由树渲染成清单文本。
func buildRouteManifest(t *testing.T) string {
	t.Helper()
	gin.SetMode(gin.TestMode)

	// 扩展默认关闭时 RegisterRoutes 直接返回,那样导出的会是一份空清单 ——
	// 而空清单会让前端那条守卫把**每一条**路径都判成"后端没有",
	// 或者(更糟)被当成"没什么可比的"而整体跳过。
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte("enabled: true\n"+
		"database:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n"), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
	require.True(t, config.Enabled(), "配置没生效的话下面这次注册是空跑")

	engine := gin.New()
	RegisterRoutes(engine)
	routes := engine.Routes()
	require.NotEmpty(t, routes, "一条路由都没注册上")

	lines := make([]string, 0, len(routes))
	for _, r := range routes {
		lines = append(lines, r.Method+" "+r.Path)
	}
	sort.Strings(lines)
	return manifestHeader + strings.Join(lines, "\n") + "\n"
}

// 路由清单必须与真实路由树一致。
//
// # 为什么需要这条
//
// 清单是前端对账的唯一依据,一份过期的清单会把守卫悄悄变成摆设:删掉的路由
// 还留在清单里,前端继续调它、继续 404,而测试全绿。所以清单不能靠人记得更新 ——
// 这条测试就是那个"记得"。改了任何路由而没重新生成,它立刻变红并给出命令。
func TestQyRouteManifestIsCurrent(t *testing.T) {
	want := buildRouteManifest(t)

	if os.Getenv(manifestUpdateEnv) != "" {
		require.NoError(t, os.WriteFile(ManifestPath, []byte(want), 0o600))
		t.Log("已重新生成 " + ManifestPath)
		return
	}

	raw, err := os.ReadFile(ManifestPath)
	require.NoError(t, err, "路由清单不存在,用 "+manifestUpdateEnv+"=1 重新生成")
	// 归一行尾再比。.gitattributes 已经把这个文件钉成 eol=lf,但那只管住走 git 的
	// 路径 —— 一次 `git stash -u` 往返、一台改了 core.autocrlf 的机器,都能把它变成
	// CRLF。那时逐字比对会把**每一行**都报成差异,一份完全正确的清单看起来像全错。
	got := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if got == want {
		return
	}

	// 逐行报差异而不是 require.Equal 整串:222 行的清单整串打出来没人读得下去,
	// 而真正要看的只有多出来 / 少掉的那几条。
	routeLines := func(text string) map[string]bool {
		set := map[string]bool{}
		for _, line := range strings.Split(text, "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				set[line] = true
			}
		}
		return set
	}
	inGot := routeLines(got)
	var added, removed []string
	for line := range routeLines(want) {
		if !inGot[line] {
			added = append(added, "  + "+line)
		}
		delete(inGot, line)
	}
	for line := range inGot {
		removed = append(removed, "  - "+line)
	}
	sort.Strings(added)
	sort.Strings(removed)
	t.Fatalf("路由清单已过期(+ 是路由树里有而清单里没有,- 是反过来):\n%s\n%s\n"+
		"重新生成:%s=1 go test ./qianye/ -run TestQyRouteManifestIsCurrent -count=1",
		strings.Join(added, "\n"), strings.Join(removed, "\n"), manifestUpdateEnv)
}
