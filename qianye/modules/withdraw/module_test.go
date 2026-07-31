package withdraw

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 路由树里静态段与通配段是相邻的(/withdraw/payees 与 /withdraw/:id),
// gin 在注册冲突时是 panic 而不是返回错误 —— 也就是说写错了会直接让进程起不来。
// 这条测试把"服务能启动"这个前提固定住。
func TestRegisterRoutes_NoConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()

	require.NotPanics(t, func() {
		Mod{}.RegisterUserRoutes(engine.Group("/api/qy"))
		Mod{}.RegisterAdminRoutes(engine.Group("/api/qy/admin"))
	})

	registered := map[string]bool{}
	for _, r := range engine.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"POST /api/qy/withdraw",
		"GET /api/qy/withdraw/records",
		"POST /api/qy/withdraw/:id/cancel",
		"GET /api/qy/withdraw/payees",
		"POST /api/qy/withdraw/payees",
		"GET /api/qy/admin/withdraw",
		"POST /api/qy/admin/withdraw/:id/approve",
		"POST /api/qy/admin/withdraw/:id/reject",
		"POST /api/qy/admin/withdraw/:id/mark-paid",
		"GET /api/qy/admin/withdraw/:id/payee",
	} {
		assert.True(t, registered[want], "缺少路由 %s", want)
	}
}
