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
		Fork     string `json:"fork"`
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

// setInjectedBuild 模拟构建期 ldflags 的注入结果,并在用例结束后还原 ——
// 这是个包级变量,不还原会污染同包的其他用例。
//
// 只剩 build 一个:upstream 已经从「ldflags 注入」改成「baseline.txt 声明 +
// go:embed」,不再是运行期可改的变量,所以用例也没法(也不该)伪造它。
func setInjectedBuild(t *testing.T, build string) {
	t.Helper()
	prev := version.Build
	version.Build = build
	t.Cleanup(func() { version.Build = prev })
}

// 未注入 / 注入 / 注入空白三种形态下的返回值。
//
// "未注入返回 unknown" 是要守住的那一条:默认值一旦被改成某个像模像样的版本号,
// 排障时会读到一个**假的**版本,那比读不到版本更糟 —— 管理员会据此排除掉
// 实际有问题的那个提交。
func TestAdminVersionReportsInjectedBuild(t *testing.T) {
	cases := []struct {
		name      string
		build     string
		wantBuild string
	}{
		{
			name: "未注入时诚实报 unknown,不伪造版本号",
			// 零值 = 直接 go build、没走构建脚本的形态。
			wantBuild: version.Unknown,
		},
		{
			name:      "注入后原样返回 git describe 的输出",
			build:     "v1.0.0-rc.24-109-g1228d77e8",
			wantBuild: "v1.0.0-rc.24-109-g1228d77e8",
		},
		{
			// git 不可用时脚本会退化成 -X 'pkg.Build=',链接器会老实写入空串。
			// 前端拿到空串渲染成一片空白,看起来像页面坏了。
			name:      "注入空串或纯空白同样归一到 unknown",
			build:     "   ",
			wantBuild: version.Unknown,
		},
		{
			name:      "带 dirty 后缀的本地构建原样透出",
			build:     "v1.0.0-rc.24-109-g1228d77e8-dirty",
			wantBuild: "v1.0.0-rc.24-109-g1228d77e8-dirty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setInjectedBuild(t, tc.build)

			code, body := callAdminVersion(t)
			require.Equal(t, http.StatusOK, code)
			require.True(t, body.Success)
			assert.Equal(t, tc.wantBuild, body.Data.Build)
			// upstream 与 build 无关:它是编译进来的声明,不随注入变化。
			assert.Equal(t, version.SyncedUpstream(), body.Data.Upstream)
			// core 直接透传上游包级变量,不做任何美化或替换 —— 未经构建脚本的
			// 二进制在这里会露出上游默认值 v0.0.0,那正是我们要能看见的事实。
			assert.Equal(t, common.Version, body.Data.Core)
		})
	}
}

// upstream 字段报的是编译进来的同步基线,不是「HEAD 够得着的最近 tag」。
//
// 这两件事在本仓已经分叉:HEAD 的 `git describe --tags --abbrev=0` 是
// v1.0.0-rc.24,而树里同步到的上游其实是 v1.0.0-rc.25 之后的一个提交。旧实现
// 把前者当成后者注入,于是 /api/status 整整落后一个 release 却不报错。
//
// 断言故意不写死具体版本号(那样每次同步都要改用例,而且改了也证明不了什么),
// 而是断言它与 qianye/version 的声明同源、且不是那个会被误用的 git describe 值。
func TestAdminVersionUpstreamComesFromDeclarationNotTagReachability(t *testing.T) {
	setInjectedBuild(t, "v1.0.0-rc.24-109-g1228d77e8")

	code, body := callAdminVersion(t)
	require.Equal(t, http.StatusOK, code)

	require.NotEqual(t, version.Unknown, body.Data.Upstream,
		"baseline.txt 没被编进来,upstream 退化成 unknown")
	assert.Equal(t, version.SyncedUpstream(), body.Data.Upstream)

	// build 是「我们自己的提交」,upstream 是「同步到的上游提交」。两者同源就说明
	// 实现又退回了拿 git describe 冒充同步基线的老路。
	assert.NotEqual(t, body.Data.Build, body.Data.Upstream,
		"upstream 与 build 撞成同一个值,说明它又是从 HEAD 的 tag 可达性算出来的")
}

// 内核版本与二开版本是**两个**字段,而且互不污染。
//
// 这一条守的是本轮被推翻的那个做法:曾经 common.Version 被注入成
// `<上游 tag>+qy.<轮次>`,于是「当前版本」既不是上游版本也不是我们的版本,
// 而上游那颗检查更新按钮拿它跟 release 的 tag_name 做相等比较,永远不相等。
//
// 断言不写死具体版本号(那样每次发版都要改用例,而且改了也证明不了什么),
// 而是断言两个字段的**形状约束**:内核版本里不许出现二开痕迹,二开版本必须
// 是能比大小的 vX.Y.Z,两者不许相等。
func TestAdminVersionKeepsCoreAndForkSeparate(t *testing.T) {
	setInjectedBuild(t, "v1.0.0-rc.24-109-g1228d77e8")

	code, body := callAdminVersion(t)
	require.Equal(t, http.StatusOK, code)

	require.NotEqual(t, version.Unknown, body.Data.Fork,
		"baseline.txt 没声明二开版本,fork 退化成 unknown")
	assert.Equal(t, version.ForkVersion(), body.Data.Fork)

	// 二开版本必须落在检查更新认得的方案里,否则那颗按钮永远只会说「比不出来」。
	_, _, _, ok := version.ForkVersionNumbers(body.Data.Fork)
	assert.True(t, ok, "fork(%s)不是 vMAJOR.MINOR.PATCH,检查更新无法比较",
		body.Data.Fork)

	// 内核版本必须干净。注入了 `+qy.N` 的旧做法在这两条上必红。
	assert.NotContains(t, body.Data.Core, "+qy.",
		"core 里带着二开后缀 —— 它必须与上游 release 的 tag 逐字一致")
	assert.NotContains(t, body.Data.Core, body.Data.Fork,
		"core 里嵌进了二开版本号,两个版本号又被合成了一个")

	// 四个字段回答四个不同的问题,不许有两个撞成同一个值。
	assert.NotEqual(t, body.Data.Core, body.Data.Fork)
	assert.NotEqual(t, body.Data.Build, body.Data.Fork,
		"fork 与 build 撞成同一个值 —— 二开版本号又变回 git describe 的输出了")
	assert.NotEqual(t, body.Data.Upstream, body.Data.Build)
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
	setInjectedBuild(t, "v1.0.0-rc.24-109-g1228d77e8")

	gin.SetMode(gin.TestMode)
	healthRes := httptest.NewRecorder()
	healthCtx, _ := gin.CreateTestContext(healthRes)
	healthCtx.Request = httptest.NewRequest(http.MethodGet, "/api/qy/admin/health", nil)
	AdminHealth(healthCtx)
	require.NotEqual(t, http.StatusOK, healthRes.Code,
		"前提不成立:健康页本应被 guard 挡掉,否则这条用例证明不了任何事")

	code, body := callAdminVersion(t)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "v1.0.0-rc.24-109-g1228d77e8", body.Data.Build)
	assert.Equal(t, version.SyncedUpstream(), body.Data.Upstream)
}
