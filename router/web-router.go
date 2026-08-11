package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// WebAssets holds the embedded dashboard frontend assets.
type WebAssets struct {
	BuildFS   embed.FS
	IndexPage []byte
}

func SetWebRouter(router *gin.Engine, assets WebAssets) {
	frontendFS := common.EmbedFolder(assets.BuildFS, "web/dist")

	router.Use(gzip.Gzip(gzip.DefaultCompression))
	router.Use(middleware.GlobalWebRateLimit())
	router.Use(middleware.Cache())
	router.Use(static.Serve("/", frontendFS))
	router.NoRoute(FrontendFallback(assets.IndexPage))
}

// FrontendFallback 是所有没命中后端路由、也没命中静态文件的请求的归宿。
//
// 它必须区分两类「没命中」,否则前端会拿到一个无法诊断的错误:
//   - SPA 路由(/console/token)→ 发 index.html,由前端路由接管;
//   - 构建产物(/static/js/async/1234.abcdef.js)→ 如实 404。
func FrontendFallback(indexPage []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(middleware.RouteTagKey, "web")
		if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") || strings.HasPrefix(c.Request.RequestURI, "/assets") {
			controller.RelayNotFound(c)
			return
		}
		if IsFrontendBuildAsset(c.Request.URL.Path) {
			// 构建产物缺失必须如实 404。发回 index.html 会让浏览器把一整页 HTML
			// 当 JS 执行,报出 `SyntaxError: Unexpected token '<'`,而真正的原因
			// ——「这个 chunk 在当前二进制里不存在」——完全看不出来。
			// no-store 是必须的:上面的 middleware.Cache() 给非 "/" 的响应统一挂了
			// max-age=604800,缓存一个 404 会让这台机器上的这个 URL 一周内都不再回源。
			c.Header("Cache-Control", "no-store")
			c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("asset not found"))
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexPage)
	}
}

// frontendBuildAssetExtensions 是前端构建产物可能出现的后缀。
//
// 用后缀白名单而不是「路径里有点就算资源」:SPA 的动态路由段是用户数据
// (例如 /chat/$chatId),里面出现点号是合法的,把它们判成资源会让整页 404。
var frontendBuildAssetExtensions = map[string]struct{}{
	".js": {}, ".mjs": {}, ".cjs": {}, ".css": {}, ".map": {},
	".json": {}, ".txt": {}, ".wasm": {}, ".webmanifest": {},
	".ico": {}, ".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {},
	".svg": {}, ".webp": {}, ".avif": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".otf": {}, ".eot": {},
}

// IsFrontendBuildAsset 判断一个没命中静态文件的路径请求的是不是前端构建产物。
//
// 这条判定决定 SPA 兜底的边界:命中的走 404,没命中的才发 index.html。
// 前端路由一律不带后缀,所以「带构建产物后缀」等价于「请求的是打包出来的文件」;
// rsbuild 的产物根目录 /static/ 额外整段算进来,兜住 LICENSE.txt 这类附带文件。
func IsFrontendBuildAsset(requestPath string) bool {
	if strings.HasPrefix(requestPath, "/static/") {
		return true
	}
	name := requestPath[strings.LastIndexByte(requestPath, '/')+1:]
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return false
	}
	_, ok := frontendBuildAssetExtensions[strings.ToLower(name[dot:])]
	return ok
}
