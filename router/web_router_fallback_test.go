package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这条测试守的是「前端间歇性 500 错误页」的后端半边。
//
// 现象:项目方看到全屏的「500 糟糕!出错了 :')」,刷新就好。实测复现下来,
// 那个页面根本不是收到了 HTTP 500 —— 是浏览器去取一个路由 chunk
// (/static/js/async/9243.b5c19ac00c.js),而这个 chunk 在当前二进制里不存在
// (重新构建后 hash 变了,旧 index.html 还在老标签页里)。
//
// 旧的 NoRoute 只放过 /v1 /api /assets 三个前缀,其余一律回 index.html + 200,
// 于是浏览器把一整页 HTML 当 JavaScript 执行,控制台只留下
// `SyntaxError: Unexpected token '<'`,rspack 抛 ChunkLoadError,
// 整棵 React 树掉进 __root.tsx 的 errorComponent,渲染成那个 500 页。
//
// 所以后端这一侧的契约是:构建产物缺失必须 404,且这个 404 绝不能被缓存
// —— middleware.Cache() 给非 "/" 的响应统一挂了 max-age=604800。
func TestFrontendFallbackDistinguishesBuildAssetsFromSPARoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	indexPage := []byte("<!doctype html><html><body>spa</body></html>")
	engine := gin.New()
	engine.Use(middleware.Cache())
	engine.NoRoute(FrontendFallback(indexPage))

	cases := []struct {
		name         string
		path         string
		wantStatus   int
		wantBody     string
		wantCacheHdr string
	}{
		{
			name:         "缺失的路由 chunk 必须 404,不能伪装成 index.html",
			path:         "/static/js/async/9243.b5c19ac00c.js",
			wantStatus:   http.StatusNotFound,
			wantBody:     "asset not found",
			wantCacheHdr: "no-store",
		},
		{
			// 判定必须看 URL.Path 而不是 RequestURI：后者带着查询串,
			// 后缀会变成 ".png?v=2",资源被判成 SPA 路由,404 又变回 200 HTML。
			name:         "带查询串的资源仍然按资源判",
			path:         "/logo.png?v=2",
			wantStatus:   http.StatusNotFound,
			wantBody:     "asset not found",
			wantCacheHdr: "no-store",
		},
		{
			name:         "/static/ 下无后缀的附带文件也算资源",
			path:         "/static/js/async/LICENSE",
			wantStatus:   http.StatusNotFound,
			wantBody:     "asset not found",
			wantCacheHdr: "no-store",
		},
		{
			name:         "根目录下的图片资源缺失也要 404",
			path:         "/logo.png",
			wantStatus:   http.StatusNotFound,
			wantBody:     "asset not found",
			wantCacheHdr: "no-store",
		},
		{
			name:         "SPA 路由仍然发 index.html",
			path:         "/console/token",
			wantStatus:   http.StatusOK,
			wantBody:     string(indexPage),
			wantCacheHdr: "no-cache",
		},
		{
			name:         "带查询串的 SPA 路由仍然发 index.html",
			path:         "/qy/admin/commission-records?tab=relations",
			wantStatus:   http.StatusOK,
			wantBody:     string(indexPage),
			wantCacheHdr: "no-cache",
		},
		{
			// 反过来的方向：查询串里带 .js 的 SPA 路由不能被判成资源,
			// 否则登录后的回跳链接会整页 404。
			name:         "查询串里出现资源后缀不影响 SPA 判定",
			path:         "/sign-in?redirect=/static/js/index.js",
			wantStatus:   http.StatusOK,
			wantBody:     string(indexPage),
			wantCacheHdr: "no-cache",
		},
		{
			name: "动态路由段里带点号的仍然是 SPA 路由 —— 这些是用户数据,不是资源",
			// 误判成资源会让一整页 404,比 chunk 404 严重得多。
			path:         "/chat/session.2026-08-10",
			wantStatus:   http.StatusOK,
			wantBody:     string(indexPage),
			wantCacheHdr: "no-cache",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			engine.ServeHTTP(recorder, request)

			require.Equal(t, tc.wantStatus, recorder.Code)
			assert.Equal(t, tc.wantBody, recorder.Body.String())
			assert.Equal(t, tc.wantCacheHdr, recorder.Header().Get("Cache-Control"))
		})
	}
}

func TestIsFrontendBuildAsset(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/static/js/async/1234.abcdef0123.js", true},
		{"/static/css/index.aabbcc.css", true},
		{"/static/js/index.js.map", true},
		{"/static/font/inter.woff2", true},
		{"/favicon.ico", true},
		{"/logo.PNG", true},
		{"/manifest.webmanifest", true},
		{"/robots.txt", true},
		{"/", false},
		{"/console", false},
		{"/console/token", false},
		{"/qy/admin/commission-records/relations", false},
		{"/chat/9f2c.4b81", false},
		{"/sign-in", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, IsFrontendBuildAsset(tc.path))
		})
	}
}
