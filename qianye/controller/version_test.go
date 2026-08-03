package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/version"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// versionBody 是 /admin/version 的对外契约,前端 admin-health/types.ts 的
// QyVersionInfo 与之逐字段对齐,字段改名即前端拿到 undefined。
type versionBody struct {
	Success bool `json:"success"`
	Data    struct {
		Build    string `json:"build"`
		Upstream string `json:"upstream"`
		Core     string `json:"core"`
	} `json:"data"`
}

// callAdminVersion 走真实 handler,返回 HTTP 状态码与解包后的响应体。
func callAdminVersion(t *testing.T) (int, versionBody) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/admin/version", nil)
	c.Set("id", 1)
	AdminVersion(c)

	var body versionBody
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body), "body=%s", res.Body.String())
	return res.Code, body
}

// setInjectedVersion 模拟构建期 ldflags 的注入结果,并在用例结束后还原 ——
// 这两个是包级变量,不还原会污染同包的其他用例。
func setInjectedVersion(t *testing.T, build, upstream string) {
	t.Helper()
	prevBuild, prevUpstream := version.Build, version.Upstream
	version.Build, version.Upstream = build, upstream
	t.Cleanup(func() {
		version.Build, version.Upstream = prevBuild, prevUpstream
	})
}

// 未注入 / 注入 / 注入空白三种形态下的返回值。
//
// "未注入返回 unknown" 是要守住的那一条:默认值一旦被改成某个像模像样的版本号,
// 排障时会读到一个**假的**版本,那比读不到版本更糟 —— 管理员会据此排除掉
// 实际有问题的那个提交。
func TestAdminVersionReportsInjectedValues(t *testing.T) {
	cases := []struct {
		name         string
		build        string
		upstream     string
		wantBuild    string
		wantUpstream string
	}{
		{
			name: "未注入时诚实报 unknown,不伪造版本号",
			// 零值 = 直接 go build、没走构建脚本的形态。
			wantBuild:    version.Unknown,
			wantUpstream: version.Unknown,
		},
		{
			name:         "注入后原样返回 git describe 的输出",
			build:        "v1.0.0-rc.23-16-g422ba0a3",
			upstream:     "v1.0.0-rc.23",
			wantBuild:    "v1.0.0-rc.23-16-g422ba0a3",
			wantUpstream: "v1.0.0-rc.23",
		},
		{
			// git 不可用时脚本会退化成 -X 'pkg.Build=',链接器会老实写入空串。
			// 前端拿到空串渲染成一片空白,看起来像页面坏了。
			name:         "注入空串或纯空白同样归一到 unknown",
			build:        "",
			upstream:     "   ",
			wantBuild:    version.Unknown,
			wantUpstream: version.Unknown,
		},
		{
			name:         "带 dirty 后缀的本地构建原样透出",
			build:        "v1.0.0-rc.23-16-g422ba0a3-dirty",
			upstream:     "v1.0.0-rc.23",
			wantBuild:    "v1.0.0-rc.23-16-g422ba0a3-dirty",
			wantUpstream: "v1.0.0-rc.23",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setInjectedVersion(t, tc.build, tc.upstream)

			code, body := callAdminVersion(t)
			require.Equal(t, http.StatusOK, code)
			require.True(t, body.Success)
			assert.Equal(t, tc.wantBuild, body.Data.Build)
			assert.Equal(t, tc.wantUpstream, body.Data.Upstream)
			// core 直接透传上游包级变量,不做任何美化或替换。
			assert.Equal(t, common.Version, body.Data.Core)
		})
	}
}

// 扩展库不可用时,版本端点仍然是 200 —— 这是它与 /health 分开的**全部理由**。
//
// 把 requireCore 加回 AdminVersion 会让这条用例变红:同样的进程状态下
// AdminHealth 被 guard 挡成非 200,而版本信息恰恰是排障的第一个问题。
func TestAdminVersionStaysReadableWhenExtDBUnavailable(t *testing.T) {
	// 显式构造"扩展已启用、但库连不上"的状态:句柄为 nil ⇒ db.Available() 为 false。
	prevHandle := qyDBHandle.Swap(nil)
	prevCfg := qyConfig.Swap(&config.Config{Enabled: true})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyConfig.Store(prevCfg)
	})
	setInjectedVersion(t, "v1.0.0-rc.23-16-g422ba0a3", "v1.0.0-rc.23")

	gin.SetMode(gin.TestMode)
	healthRes := httptest.NewRecorder()
	healthCtx, _ := gin.CreateTestContext(healthRes)
	healthCtx.Request = httptest.NewRequest(http.MethodGet, "/api/qy/admin/health", nil)
	AdminHealth(healthCtx)
	require.NotEqual(t, http.StatusOK, healthRes.Code,
		"前提不成立:健康页本应被 guard 挡掉,否则这条用例证明不了任何事")

	code, body := callAdminVersion(t)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "v1.0.0-rc.23-16-g422ba0a3", body.Data.Build)
	assert.Equal(t, "v1.0.0-rc.23", body.Data.Upstream)
}
