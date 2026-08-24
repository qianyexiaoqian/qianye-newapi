package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/version"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// update_check_test.go —— 检查更新的对外契约。
//
// 守的核心只有一条,而且是这个端点存在的全部理由:**六种结局各说各的话**。
// 一个"全都 catch 成 检查失败"的实现能通过任何只断言"点了不崩"的用例,
// 却让运维在离线部署、仓库改名、GitHub 限流这三件事之间毫无线索地试错。
// 所以下面每一条都同时断言 HTTP 状态码与业务 code —— 少断言其中之一,
// 把两种失败塌成同一个 code 的改动就能溜过去。

// updateCheckBody 是 /admin/version/check-update 的响应形状,与前端
// admin-health/types.ts 的 QyUpdateCheck 逐字段对齐。
type updateCheckBody struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Status      string `json:"status"`
		Current     string `json:"current"`
		Latest      string `json:"latest"`
		ReleaseName string `json:"release_name"`
		ReleaseURL  string `json:"release_url"`
		PublishedAt string `json:"published_at"`
		Prerelease  bool   `json:"prerelease"`
	} `json:"data"`
}

// callCheckUpdate 把更新源指向 handler(通常是 httptest 起的假 GitHub),
// 走真实 handler,返回 HTTP 状态码与解包后的响应体。
func callCheckUpdate(t *testing.T, endpoint string) (int, updateCheckBody) {
	t.Helper()
	prev := forkReleasesEndpoint
	forkReleasesEndpoint = endpoint
	t.Cleanup(func() { forkReleasesEndpoint = prev })

	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/admin/version/check-update", nil)
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)
	AdminCheckUpdate(c)

	var body updateCheckBody
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body), "body=%s", res.Body.String())
	return res.Code, body
}

// fakeGitHub 起一个假的 GitHub,按给定的状态码/响应头/响应体作答。
func fakeGitHub(t *testing.T, status int, headers map[string]string, payload string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 顺带钉住两条请求侧的契约:GitHub 对没有 User-Agent 的请求直接 403,
		// 而我们刻意不转发管理员自己的 UA。
		assert.Equal(t, "new-api-qy-update-check", r.Header.Get("User-Agent"))
		assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// 三类失败各有各的 code,而且不许互相塌陷。
//
// 「仓库不存在」与「还没发过 release」用的是同一个上游状态码家族,把它们分开
// 正是选 /releases 列表而不是 /releases/latest 的全部理由 —— 所以这里既有 404
// 的用例,也有下面那条 200+[] 的用例,两条必须给出完全不同的结论。
func TestCheckUpdateSeparatesEachFailureCause(t *testing.T) {
	// 一个已经关掉的服务器地址:连不上,拿不到任何 HTTP 响应。
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	resetIn10Min := strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)

	cases := []struct {
		name      string
		endpoint  func(t *testing.T) string
		wantHTTP  int
		wantCode  string
		wantInMsg string
		notInMsg  string
		whyItsOwn string
	}{
		{
			name:      "连不上:离线部署 / DNS / 超时",
			endpoint:  func(*testing.T) string { return closedURL },
			wantHTTP:  http.StatusBadGateway,
			wantCode:  codeUpdateUnreachable,
			wantInMsg: "连不上更新源",
			whyItsOwn: "这台机器出不去外网,与更新源本身好不好无关",
		},
		{
			name: "404:仓库不存在 / 改名 / 转私有",
			endpoint: func(t *testing.T) string {
				return fakeGitHub(t, http.StatusNotFound, nil, `{"message":"Not Found"}`)
			},
			wantHTTP:  http.StatusBadGateway,
			wantCode:  codeUpdateSourceMissing,
			wantInMsg: "404",
			whyItsOwn: "要去查仓库地址,而不是查网络",
		},
		{
			name: "403 + 额度归零:限流",
			endpoint: func(t *testing.T) string {
				return fakeGitHub(t, http.StatusForbidden,
					map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": resetIn10Min},
					`{"message":"API rate limit exceeded"}`)
			},
			wantHTTP:  http.StatusTooManyRequests,
			wantCode:  codeUpdateRateLimited,
			wantInMsg: "分钟后恢复",
			whyItsOwn: "等一会儿就好,不用查任何东西",
		},
		{
			name: "403 但没有额度标记:被拒,不是限流",
			endpoint: func(t *testing.T) string {
				return fakeGitHub(t, http.StatusForbidden, nil, `{"message":"Forbidden"}`)
			},
			wantHTTP: http.StatusBadGateway,
			wantCode: codeUpdateForbidden,
			// 这一条最容易被塌进限流。断言 message 里**没有**"稍后再点",
			// 因为对它来说等待是无效建议。
			notInMsg:  "稍后再点",
			whyItsOwn: "等待不会让它恢复,要查的是出站 IP",
		},
		{
			name: "429:GitHub 的二级限流,没有额度头也算限流",
			endpoint: func(t *testing.T) string {
				return fakeGitHub(t, http.StatusTooManyRequests, nil, `{"message":"slow down"}`)
			},
			wantHTTP:  http.StatusTooManyRequests,
			wantCode:  codeUpdateRateLimited,
			whyItsOwn: "与 403 限流同一个处置,但状态码不同,不能只认 403",
		},
		{
			name: "其它非 2xx:状态码必须出现在 message 里",
			endpoint: func(t *testing.T) string {
				return fakeGitHub(t, http.StatusBadGateway, nil, `<html>bad gateway</html>`)
			},
			wantHTTP:  http.StatusBadGateway,
			wantCode:  codeUpdateUnexpectedStatus,
			wantInMsg: "502",
			whyItsOwn: "唯一的线索就是那个状态码,塌成'检查失败'等于把它扔了",
		},
		{
			name: "200 但不是 release 列表:被网关门户页劫持",
			endpoint: func(t *testing.T) string {
				return fakeGitHub(t, http.StatusOK, nil, `<html>portal login</html>`)
			},
			wantHTTP:  http.StatusBadGateway,
			wantCode:  codeUpdateBadPayload,
			whyItsOwn: "网络'通'了但通到了别处,与连不上是两件事",
		},
		{
			name: "200 但最新 release 没有 tag_name",
			endpoint: func(t *testing.T) string {
				return fakeGitHub(t, http.StatusOK, nil, `[{"name":"未命名","html_url":"https://example.invalid"}]`)
			},
			wantHTTP:  http.StatusBadGateway,
			wantCode:  codeUpdateBadPayload,
			whyItsOwn: "没有版本号就无从比较,不能当成'已是最新'",
		},
	}

	seenCodes := map[string][]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := callCheckUpdate(t, tc.endpoint(t))
			require.Equal(t, tc.wantHTTP, code)
			require.False(t, body.Success)
			assert.Equal(t, tc.wantCode, body.Code, "根因:"+tc.whyItsOwn)
			assert.NotEmpty(t, body.Message, "失败必须带一句能读的原因")
			if tc.wantInMsg != "" {
				assert.Contains(t, body.Message, tc.wantInMsg)
			}
			if tc.notInMsg != "" {
				assert.NotContains(t, body.Message, tc.notInMsg)
			}
		})
		seenCodes[tc.wantCode] = append(seenCodes[tc.wantCode], tc.name)
	}

	// 反方向的断言:把两种根因塌成同一个 code 的改动,必须在这里变红。
	// 8 条用例压成 6 个 code —— 只有两处允许共享:403 限流与 429(处置相同,
	// 都是等),以及两种"200 但答非所问"(都是响应体不可用)。其余各占一个。
	assert.Len(t, seenCodes, 6,
		"失败形态压缩成的 code 数量变了 —— 要么合并了根因,要么漏了一种")
	assert.Len(t, seenCodes[codeUpdateRateLimited], 2, "403 限流与 429 应当共享限流这一档")
	assert.Len(t, seenCodes[codeUpdateBadPayload], 2, "两种响应体不可用共享同一档")
}

// 仓库在、但一个 release 都没发过 —— 这是本 fork **此刻**的真实状态。
//
// 它必须是**成功**的一档结论。报成失败会把人推去查网络,而实际该做的是去发版。
// 这条与上面的 404 用例合起来,就是选 /releases 列表而不是 /releases/latest
// 的证据:后者对这两种情况都返回 404,分不开。
func TestCheckUpdateReportsNoReleaseAsSuccessNotFailure(t *testing.T) {
	code, body := callCheckUpdate(t, fakeGitHub(t, http.StatusOK, nil, `[]`))

	require.Equal(t, http.StatusOK, code)
	require.True(t, body.Success, "空 release 列表被报成了失败")
	assert.Equal(t, updateStatusNoRelease, body.Data.Status)
	assert.Equal(t, version.ForkVersion(), body.Data.Current)
	assert.Empty(t, body.Data.Latest, "一个 release 都没有,不许编一个版本号出来")
	assert.Equal(t, forkReleasesPage, body.Data.ReleaseURL, "没有 release 时也要给个去处")
}

// 有 release 时的三种判定,以及"绝不自动下载"的形状。
func TestCheckUpdateComparesAgainstDeclaredForkVersion(t *testing.T) {
	current := version.ForkVersion()
	require.NotEqual(t, version.Unknown, current, "前提不成立:baseline.txt 没声明二开版本")

	cases := []struct {
		name       string
		tag        string
		wantStatus string
	}{
		{name: "远端更新", tag: "qy-v999.0.0", wantStatus: updateStatusUpdateAvailable},
		{name: "完全相同", tag: version.ForkTagPrefix + current, wantStatus: updateStatusUpToDate},
		{name: "本机更新(改完还没发版)", tag: "qy-v0.0.0", wantStatus: updateStatusAhead},
		{name: "远端 tag 不在方案里", tag: "release-2026-08", wantStatus: updateStatusLatestUnparsable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := `[{"tag_name":"` + tc.tag + `","name":"某一版",` +
				`"html_url":"https://github.com/qianyexiaoqian/qianye-newapi/releases/tag/` + tc.tag + `",` +
				`"published_at":"2026-08-23T00:00:00Z","prerelease":false,` +
				`"assets":[{"browser_download_url":"https://example.invalid/new-api.exe"}]}]`

			code, body := callCheckUpdate(t, fakeGitHub(t, http.StatusOK, nil, payload))
			require.Equal(t, http.StatusOK, code)
			require.True(t, body.Success)

			assert.Equal(t, tc.wantStatus, body.Data.Status)
			assert.Equal(t, current, body.Data.Current)
			assert.Equal(t, tc.tag, body.Data.Latest, "远端 tag 原样透出,不做美化")
			assert.Equal(t, "2026-08-23T00:00:00Z", body.Data.PublishedAt)

			// 绝不自动下载:响应里给出的去处只能是给人点的 release 页面,
			// 而 payload 里那个 assets 下载直链一个字都不该出现。
			assert.Contains(t, body.Data.ReleaseURL, "/releases/tag/")
			assert.NotContains(t, body.Data.ReleaseURL, "browser_download_url")
			assert.NotContains(t, body.Data.ReleaseURL, "example.invalid")
		})
	}
}

// compareToLatest 的六种结论。
//
// 单独测它而不是全部走 HTTP:current_unknown 那一档要求本机版本号缺失,而它是
// go:embed 进来的编译期常量,HTTP 层伪造不了。把判定抽成纯函数正是为了让这一档
// 也能被真的测到 —— 否则它就是一条永远跑不到的分支。
func TestCompareToLatestNeverGuessesWhenItCannotCompare(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    string
	}{
		{name: "远端更新", current: "v0.1.0", latest: "qy-v0.2.0", want: updateStatusUpdateAvailable},
		{name: "远端更新(跨 MAJOR)", current: "v0.9.9", latest: "qy-v1.0.0", want: updateStatusUpdateAvailable},
		{name: "相同", current: "v1.2.3", latest: "qy-v1.2.3", want: updateStatusUpToDate},
		// 远端 tag **必须**带 qy- 前缀,不带就是"比不出来"。
		//
		// 这一条以前写的是"不带前缀也认",而那让 qy- 前缀这层保护完全落空:
		// fork 仓库里的 tag 谁都能推,.github/workflows/release.yml 又是
		// `push: tags: ['*','!*-alpha*']` 自动建 Release 的。上游的 tag 全是
		// `v*`,本地这份 clone 挂着 upstream 远端、681 个 tag 里有 77 个是无
		// 预发布段的 vX.Y.Z 形态(v0.9.28 / v0.10.5 / v1.0.0 …),全部可解析
		// 且全部大于 v0.1.0 —— 一条 `git push --tags` 就能让这颗按钮报
		// 「有新版本 v0.9.28」,并把管理员指向一个**纯上游代码**的构建产物。
		{name: "远端不带 qy- 前缀:比不出来,绝不当成我们的版本",
			current: "v1.2.3", latest: "v1.2.3", want: updateStatusLatestUnparsable},
		{name: "上游的 GA tag 被推进 fork 并自动建了 release",
			current: "v0.1.0", latest: "v0.9.28", want: updateStatusLatestUnparsable},
		{name: "上游 GA tag 跨 MAJOR",
			current: "v0.1.0", latest: "v1.0.0", want: updateStatusLatestUnparsable},
		// 反方向:本机声明侧照旧宽松(那是我们自己写的,可信),
		// 带不带 qy-、带不带 v 都要认,否则检查更新恒为 current_unknown。
		{name: "本机声明带 qy- 前缀也认", current: "qy-v1.2.3", latest: "qy-v1.2.3", want: updateStatusUpToDate},
		{name: "本机声明漏了 v 也认", current: "1.2.3", latest: "qy-v1.2.3", want: updateStatusUpToDate},
		{name: "本机更新", current: "v1.3.0", latest: "qy-v1.2.9", want: updateStatusAhead},

		// 本机没声明:有远端版本也不许下结论。
		{name: "本机 unknown", current: version.Unknown, latest: "qy-v1.0.0", want: updateStatusCurrentUnknown},
		{name: "本机空串", current: "", latest: "qy-v1.0.0", want: updateStatusCurrentUnknown},
		// 本机不可解析时,即便远端也不可解析,报的仍是"本机不可解析" ——
		// 先修自己这边,那是我们能改的。
		{name: "两侧都不可解析时先说本机", current: version.Unknown, latest: "nightly", want: updateStatusCurrentUnknown},

		// 远端 tag 换了方案:不许当成"更旧"而报"已是最新" —— 那是最坏的一种错,
		// 因为它让人不再去看。
		{name: "远端是日期式 tag", current: "v0.1.0", latest: "release-2026-08", want: updateStatusLatestUnparsable},
		{name: "远端是上游形态的 tag", current: "v0.1.0", latest: "v1.0.0-rc.25", want: updateStatusLatestUnparsable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, compareToLatest(tc.current, tc.latest))
		})
	}
}

// 限流提示在拿不到 / 已过期的 reset 头上不许编一个假的等待时间。
func TestRateLimitResetHintOnlySpeaksWhenItKnows(t *testing.T) {
	cases := []struct {
		name    string
		reset   string
		wantHas bool
	}{
		{name: "没有 reset 头", reset: "", wantHas: false},
		{name: "reset 头不是数字", reset: "soon", wantHas: false},
		{name: "reset 已经过去", reset: strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10), wantHas: false},
		{name: "reset 在未来", reset: strconv.FormatInt(time.Now().Add(5*time.Minute).Unix(), 10), wantHas: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if tc.reset != "" {
				resp.Header.Set("X-RateLimit-Reset", tc.reset)
			}
			hint := rateLimitResetHint(resp)
			if tc.wantHas {
				assert.Contains(t, hint, "分钟后恢复")
			} else {
				assert.Empty(t, hint, "拿不到确定的恢复时间就什么都别说,不要编")
			}
		})
	}
}

// 403 是不是限流,判据是额度头而不是状态码。
func TestIsGitHubRateLimitedReadsTheQuotaHeaderNotTheStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		remaining string
		want      bool
	}{
		{name: "403 且额度归零", status: http.StatusForbidden, remaining: "0", want: true},
		{name: "403 且额度还有", status: http.StatusForbidden, remaining: "37", want: false},
		{name: "403 没有额度头", status: http.StatusForbidden, want: false},
		{name: "额度头带空白仍算数", status: http.StatusForbidden, remaining: " 0 ", want: true},
		// 200 不该走到这里,但它绝不能被判成限流 —— 那会把一次成功报成失败。
		{name: "200 即便额度归零也不是限流", status: http.StatusOK, remaining: "0", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: http.Header{}}
			if tc.remaining != "" {
				resp.Header.Set("X-RateLimit-Remaining", tc.remaining)
			}
			assert.Equal(t, tc.want, isGitHubRateLimited(resp))
		})
	}
}

// 出站预算必须按**本进程**计,而不是按客户端 IP。
//
// 路由上那道 CriticalRateLimit 的桶键是 mark + 客户端 IP + 路由,而它声称要
// 保护的资源(GitHub 匿名额度)按**服务端出站 IP** 计,60 次/小时。两者不是
// 同一个量:单个客户端 IP 的稳态预算恰好 20 次/1200 秒 = 60 次/小时 ——
// 100% 吃满、零余量;而不同来源的管理员各占一桶,线性叠加。实测 10 个客户端
// IP 各打 25 次:allowed=200,200 次出站请求在 0.06 秒内从同一个进程打出去,
// 中间件一次都没拦住"站点总量"。
//
// 这条判据钉住补上的那道闸:进程级滑动窗口,与是谁点的无关。
func TestOutboundBudgetIsPerProcessNotPerClient(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)

	t.Run("窗口内用满就拒,且给得出还要等多久", func(t *testing.T) {
		b := &outboundBudget{limit: 3, window: time.Hour}
		for i := 0; i < 3; i++ {
			wait, ok := b.take(base.Add(time.Duration(i) * time.Minute))
			require.True(t, ok, "第 %d 次应放行", i+1)
			assert.Zero(t, wait)
		}
		wait, ok := b.take(base.Add(3 * time.Minute))
		assert.False(t, ok, "第 4 次必须被拒 —— 预算是站点总量,不看是谁点的")
		assert.Positive(t, wait, "被拒时要说得出还要等多久,否则管理员只能反复试")
	})

	t.Run("最早那一次滑出窗口后放行一格", func(t *testing.T) {
		b := &outboundBudget{limit: 2, window: time.Hour}
		_, ok := b.take(base)
		require.True(t, ok)
		_, ok = b.take(base.Add(30 * time.Minute))
		require.True(t, ok)
		_, ok = b.take(base.Add(59 * time.Minute))
		require.False(t, ok, "窗口内还是两次")

		// 第一次落在 base,base+61min 时它已经滑出一小时窗口。
		_, ok = b.take(base.Add(61 * time.Minute))
		assert.True(t, ok, "滑动窗口必须真的滑动,否则一小时用满之后就永久锁死")
	})

	t.Run("放行与否与客户端无关(预算里根本没有身份这一维)", func(t *testing.T) {
		// 同一个进程、假设来自 100 个不同客户端 —— 预算只认次数。
		b := &outboundBudget{limit: maxUpdateChecksPerHour, window: time.Hour}
		allowed := 0
		for i := 0; i < maxUpdateChecksPerHour*3; i++ {
			if _, ok := b.take(base.Add(time.Duration(i) * time.Second)); ok {
				allowed++
			}
		}
		assert.Equal(t, maxUpdateChecksPerHour, allowed,
			"无论多少个来源,一小时内本进程对 github.com 的出站不许超过 %d 次",
			maxUpdateChecksPerHour)
	})

	t.Run("上界必须给 GitHub 那份额度留余量", func(t *testing.T) {
		assert.LessOrEqual(t, maxUpdateChecksPerHour, 60/2,
			"匿名额度是 60 次/小时/出站 IP,同一个出口后面可能还有别的实例与工具;"+
				"把上界顶到 60 等于把额度吃满、零余量")
	})
}
