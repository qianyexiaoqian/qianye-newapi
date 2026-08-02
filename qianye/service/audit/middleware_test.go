package audit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// middleware_test.go —— 请求台账中间件。
//
// 这里守的两件事,任何一件出错都会把中间件从"兜底"变成"新的故障源":
//
//  1. **绝不能影响业务**:body 被读走却没回填,handler 就会读到空请求体 ——
//     一个为了留痕而加的中间件把所有 POST 打成 400。
//  2. **该记的必须记全,不该记的一条都不记**:路由模板而不是实际 URL、
//     401 也要记、普通 GET 不记。
//
// 这些性质只有让请求真的走一遍 gin 路由才验得了:c.FullPath() 与 c.Params
// 是路由匹配的产物,手工构造的 Context 里它们是空的,断言会全绿而毫无意义。

func init() { gin.SetMode(gin.TestMode) }

// runThroughMiddleware 让一次请求真的经过路由与中间件,返回中间件本会写入的那一行。
//
// 不断言 Record 的落库结果:那需要一个扩展库句柄,而这组用例要验的全部性质
// 都在"这一行长什么样"里。落库路径由 request_test.go 的队列用例覆盖。
func runThroughMiddleware(t *testing.T, method, routePath, requestURL string,
	body io.Reader, contentType string, handler gin.HandlerFunc,
) *qymodel.RequestAudit {
	t.Helper()

	var row *qymodel.RequestAudit
	engine := gin.New()
	group := engine.Group("/api/qy")
	// 与 qianye/router.go 同一个挂法:中间件在认证之前。
	group.Use(func(c *gin.Context) {
		if !shouldRecord(c.Request.Method, c.Request.Method+" "+c.FullPath()) {
			c.Next()
			return
		}
		captured := captureBody(c, c.Request.Method+" "+c.FullPath())
		start := time.Now()
		c.Next()
		row = buildRequestAudit(c, captured, time.Since(start))
	})
	group.Handle(method, strings.TrimPrefix(routePath, "/api/qy"), handler)

	req := httptest.NewRequest(method, requestURL, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	engine.ServeHTTP(httptest.NewRecorder(), req)
	return row
}

// 最关键的一条:读走 body 之后必须原样回填。
//
// 这个缺陷不会让台账出错,它会让**每一个 POST 接口**收到空请求体 ——
// 一个纯观测设施把整套写接口打死,而单测如果只断言台账内容会全绿。
func TestMiddleware_HandlerStillReadsFullBody(t *testing.T) {
	// 刻意超过一次 Read 的常见缓冲边界,逼出 MultiReader 拼接的边界问题。
	payload := `{"amount":123,"remark":"` + strings.Repeat("备注", 5000) + `"}`

	var seen string
	row := runThroughMiddleware(t, http.MethodPost, "/api/qy/transfer", "/api/qy/transfer",
		strings.NewReader(payload), "application/json",
		func(c *gin.Context) {
			raw, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			seen = string(raw)
			c.JSON(http.StatusOK, gin.H{})
		})

	assert.Equal(t, payload, seen, "handler 必须读到完整的原始请求体")
	require.NotNil(t, row)
	assert.True(t, row.Success)
	assert.Equal(t, 200, row.StatusCode)
}

// 路径存**路由模板**。存实际 URL 会让"同一个接口"散成上万个不同的 path 值:
// 既没法按接口聚合,也没法按接口筛选,而资源 ID 另有 params 列承载。
func TestMiddleware_StoresRouteTemplateNotActualURL(t *testing.T) {
	row := runThroughMiddleware(t, http.MethodPost,
		"/api/qy/admin/violation/appeals/:id/review",
		"/api/qy/admin/violation/appeals/8871/review?access_token=sk-live-abc&p=1",
		strings.NewReader(`{"decision":"approved"}`), "application/json",
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })

	require.NotNil(t, row)
	assert.Equal(t, "/api/qy/admin/violation/appeals/:id/review", row.Path)
	assert.NotContains(t, row.Path, "8871")
	assert.Contains(t, row.Params, "8871", "资源 ID 归 params 列")
	assert.NotContains(t, row.Query, "sk-live-abc", "query 必须脱敏后入库")
	assert.Contains(t, row.Query, "p=1")
}

// 401/403 必须留痕:被认证挡掉的写请求正是越权探测的形状。
// 中间件挂在认证之后就会把这一整类漏掉,而它恰恰是这张表最有价值的一部分。
func TestMiddleware_RecordsRequestsRejectedByAuth(t *testing.T) {
	row := runThroughMiddleware(t, http.MethodPost, "/api/qy/admin/withdraw/:id/approve",
		"/api/qy/admin/withdraw/12/approve", strings.NewReader(`{}`), "application/json",
		func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })

	require.NotNil(t, row)
	assert.Equal(t, 401, row.StatusCode)
	assert.False(t, row.Success)
	assert.Zero(t, row.ActorUserId)
	assert.Empty(t, row.ActorType,
		"匿名请求的 actor_type 必须留空 —— 记成 system 会污染「是人干的还是程序干的」这个判定")
}

// 身份来自认证中间件写进 context 的键,而不是本包自己定义的一套。
func TestMiddleware_TakesActorFromUpstreamContextKeys(t *testing.T) {
	row := runThroughMiddleware(t, http.MethodPost, "/api/qy/admin/pay-password/:user_id/unlock",
		"/api/qy/admin/pay-password/4242/unlock", strings.NewReader(`{}`), "application/json",
		func(c *gin.Context) {
			c.Set("id", 7)
			c.Set("username", "root")
			c.Set("role", 100)
			c.Set("use_access_token", true)
			c.JSON(http.StatusOK, gin.H{})
		})

	require.NotNil(t, row)
	assert.Equal(t, 7, row.ActorUserId)
	assert.Equal(t, "root", row.ActorName)
	assert.Equal(t, 100, row.ActorRole)
	assert.Equal(t, qymodel.ActorAdmin, row.ActorType)
	assert.Equal(t, qymodel.AuthMethodAccessToken, row.AuthMethod)
	assert.Equal(t, 4242, row.TargetUserId, "目标用户来自路径参数")
}

// 整体由凭证构成的请求体不入库:键级脱敏在这些路由上不够用 ——
// 支付密码接口的 body 除了密码几乎没有别的字段。
func TestMiddleware_OmitsCredentialBearingBodies(t *testing.T) {
	row := runThroughMiddleware(t, http.MethodPost, "/api/qy/pay-password", "/api/qy/pay-password",
		strings.NewReader(`{"pay_password":"123456","confirm":"123456"}`), "application/json",
		func(c *gin.Context) {
			// handler 仍要能读到 body:凭证路由只是不入库,不是不给业务用。
			raw, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			assert.Contains(t, string(raw), "123456")
			c.JSON(http.StatusOK, gin.H{})
		})

	require.NotNil(t, row)
	assert.NotContains(t, row.Body, "123456")
	assert.Contains(t, row.Body, "credential-bearing body omitted")
}

// 记录范围:全部写方法 + 白名单内的敏感读取,其余 GET 一条不记。
// 把普通列表 GET 记进来会让台账被每天几千行"某人翻了一页"稀释到无法扫读。
func TestShouldRecord_WriteMethodsAndSensitiveReadsOnly(t *testing.T) {
	cases := []struct {
		method, route string
		want          bool
	}{
		{"POST", "/api/qy/transfer", true},
		{"PUT", "/api/qy/admin/transfer/config", true},
		{"PATCH", "/api/qy/admin/violation/mode", true},
		{"DELETE", "/api/qy/withdraw/payees/:ref", true},
		{"GET", "/api/qy/admin/withdraw/:id/payee", true},  // 解密收款明文
		{"GET", "/api/qy/admin/withdraw/:id/proof", true},  // 打款凭证原图
		{"GET", "/api/qy/admin/withdraw/pii-audits", true}, // 谁查过明文
		{"GET", "/api/qy/withdraw/records", false},
		{"GET", "/api/qy/admin/audit-logs", false},
		{"GET", "/api/qy/config", false},
		{"HEAD", "/api/qy/config", false},
		{"OPTIONS", "/api/qy/transfer", false},
	}
	for _, tc := range cases {
		got := shouldRecord(tc.method, tc.method+" "+tc.route)
		assert.Equalf(t, tc.want, got, "%s %s", tc.method, tc.route)
	}
}

// 动作名必须只由 method + 路由模板决定:同一个接口在两次改动之间换了名字,
// 会让按 action 做的历史查询悄悄漏掉前半段。
func TestDeriveAction_IsStableAndIdFree(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{"POST", "/api/qy/transfer", "transfer.create"},
		{"POST", "/api/qy/admin/withdraw/:id/approve", "admin.withdraw.approve.create"},
		{"DELETE", "/api/qy/withdraw/payees/:ref", "withdraw.payees.delete"},
		{"PUT", "/api/qy/admin/transfer/group-rules/:id", "admin.transfer.group_rules.update"},
		{"GET", "/api/qy/admin/withdraw/:id/payee", "admin.withdraw.payee.read"},
		{"POST", "/api/qy/pay-password/recover/code", "pay_password.recover.code.create"},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.want, DeriveAction(tc.method, tc.path), "%s %s", tc.method, tc.path)
	}
}
