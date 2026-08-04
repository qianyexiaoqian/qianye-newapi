package qianye

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 全部模块的路由必须能同时挂上同一棵 gin 路由树。
//
// # 为什么需要这条
//
// gin 在**注册**阶段就会对冲突的路由 panic,而注册发生在 main.go 里 ——
// 也就是说一次路由冲突的表现是"进程根本起不来",而不是某个接口 404。
// 最容易撞上的两种形状:
//
//	静态段与参数段同级	/ticket/list  与  /ticket/:no
//	同一位置两个参数名	/ticket/:id   与  /ticket/:no/reply
//
// 后者正是本轮改动引入的风险:用户端从按自增 id 寻址改成按业务单号寻址
// (用户视图刻意不下发主键),三条路径的参数名必须一起改。漏掉任何一条,
// 单元测试全绿、构建通过,而线上是启动即崩。
//
// 这条测试直接调 RegisterRoutes,因此它覆盖的是**真实的注册顺序与分组**,
// 不是一份手抄的路由清单。
func TestEveryModuleRouteRegistersOnOneTree(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 扩展默认关闭时 RegisterRoutes 直接返回,那样这条测试什么都没量。
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte("enabled: true\n"+
		"database:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n"), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
	require.True(t, config.Enabled(), "配置没生效的话下面这次注册是空跑")

	engine := gin.New()
	require.NotPanics(t, func() { RegisterRoutes(engine) },
		"路由冲突在 gin 里是注册期 panic —— 线上的表现是进程起不来")

	// 顺带确认树不是空的:RegisterRoutes 提前 return 时上面的 NotPanics 也会过。
	require.NotEmpty(t, engine.Routes())
}
