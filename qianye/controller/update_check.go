package controller

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/version"

	"github.com/gin-gonic/gin"
)

// update_check.go —— 二开版本的「检查更新」。
//
// # 它只查二开,不查内核
//
// 「系统维护」页上现在有两个版本号,各有各的检查更新:
//
//	内核版本(上游 new-api)  上游那颗按钮,浏览器直接问 Calcium-Ion/new-api 的
//	                        release。上游的代码,本文件不碰。
//	二开版本(我们自己)      本端点,服务端问我们自己 fork 的 release。
//
// 两者的更新源、版本方案、发版节奏全都不同,合成一颗按钮只会让人分不清
// 「有新版本」说的是谁的新版本。
//
// # 为什么是服务端发请求,而不是照抄上游的浏览器直连
//
// 上游那颗按钮是浏览器 fetch 到 api.github.com。它对上游够用,对这里不够用,
// 理由只有一个,但很硬:**跨域 fetch 的失败全都塌成同一个 TypeError**。
// 网络不通、公司代理拦截、DNS 被劫持、GitHub 限流 —— 浏览器一律只给
// "Failed to fetch",连状态码都拿不到。而这三种情况的下一步动作完全不同
// (等网络 / 找网管 / 等一小时),把它们塌成一句"检查失败"等于什么都没说。
// 本仓刚刚修过好几处「错误信息把根因藏起来」的形状,不该在这里再造一个。
//
// 服务端发请求换来的是完整的 HTTP 语义:状态码、限流响应头、传输层错误,
// 于是下面 6 种结局各有各的 code 和各自说得清的 message。
//
// 附带的两个好处:管理员的浏览器不再直连 GitHub(他的 IP 不出现在 GitHub 的
// 日志里),以及结论对每个管理员一致 —— 它是这套部署的属性,而不是「谁的
// 网好谁能查到」。
//
// 代价要说清楚:站点因此会在**被点击时**向 github.com 发一次出站请求。
// 这就是为什么它是手动的、只有超管能点、且限了流。
//
// # 为什么手动,不自动定时
//
//   - 定时向第三方发请求是一次**站点行为变更**,没有哪个运维为它点过头。
//   - 内网/离线部署会让定时任务永远失败,于是要么天天报警,要么把报警关掉 ——
//     后者顺手把真正的告警也一起关掉了。
//   - 匿名 GitHub API 的额度是 **60 次/小时/来源 IP**。多个实例出在同一个
//     NAT 后面时,定时检查会替所有人把额度烧完。
//   - 最根本的:我们**绝不自动下载、绝不自动更新**。既然结论只能由人来处置,
//     那就由人来问。
//
// # 绝不自动下载
//
// 响应里只有版本号和一个 release 页面的链接。没有任何一处会去取 asset、
// 没有下载端点、没有自更新。升级是运维的动作,这颗按钮只负责告诉他该动了。
//
// # 为什么提到超管
//
// 它是 /api/qy/admin 下唯一一条会让**服务端自己**向第三方开出站连接的路由。
// 那是站点级行为,不是一次数据读取。版本号的**显示**仍然留在 AdminAuth
// (GET /admin/version),被提档的只有"发这一次请求"这个动作 —— 与
// root_action.go 顶部说的"提的是动作不是资源面"是同一条口径。

// forkReleasesEndpoint 是二开 release 的查询地址。
//
// 用 `/releases?per_page=1` 而不是 `/releases/latest`,这是本文件第二个需要
// 解释的选择,而且是实测出来的:
//
//	GET /repos/qianyexiaoqian/qianye-newapi/releases/latest  →  404(实测)
//	GET /repos/qianyexiaoqian/definitely-not-a-repo/releases/latest → 404
//
// 前者 404 的原因是**我们一个 release 都还没发**,后者是**仓库不存在**。
// 同一个状态码,两件毫不相干的事,而它们的下一步一个是"去发个 release"、
// 一个是"仓库名写错了或者仓库被设成私有了"。`/releases` 列表把它们分开了:
//
//	仓库在、没有 release  →  200 + []      (实测)
//	仓库不在 / 私有       →  404
//
// 变量而非常量:测试要指向 httptest 起的假 GitHub。它不从配置读 ——
// 更新源是代码事实,做成可配等于给「把检查更新指向任意地址」开一扇门,
// 而这条路由的出站正是要被约束的那件事。
var forkReleasesEndpoint = "https://api.github.com/repos/qianyexiaoqian/qianye-newapi/releases?per_page=1"

// forkReleasesPage 是给人看的 release 列表页,在「一个 release 都没有」和
// 「远端版本号解析不了」这两种结局里作为唯一的去处下发。
const forkReleasesPage = "https://github.com/qianyexiaoqian/qianye-newapi/releases"

// updateCheckTimeout 是单次检查的总上界。
//
// 取 10 秒:短到点了不会以为页面卡死,长到能容忍一次 TLS 握手加一次慢响应。
// 超时走的是传输层错误分支,与"限流"、"仓库不存在"分得开。
const updateCheckTimeout = 10 * time.Second

// maxUpdateCheckBody 是响应体的读取上界。
//
// 更新源在代码里写死、指向 GitHub,理论上不会返回巨大的响应;但"理论上不会"
// 不是不设上界的理由 —— 一个被劫持的 DNS 就能让这里读进一条无穷的流。
// per_page=1 的正常响应是几 KB,1 MB 有三个数量级的余量。
const maxUpdateCheckBody = 1 << 20

// updateCheckClient 独立于 relay 与 AI 审核的出站池:一年点不了几次的按钮
// 不该在业务连接池里占一个空闲连接。
//
// 不设 Transport 级的自定义:默认 Transport 已经带系统代理支持,而离线部署
// 里"走不走代理"恰恰是运维配好的那件事,这里不该绕过它。
var updateCheckClient = &http.Client{Timeout: updateCheckTimeout}

// githubRelease 只取用得上的字段。多余字段由 json 解码自然丢弃。
type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
}

// 检查更新的结局。**成功的那一档也有五种**,因为"没查出新版本"的原因不止一个,
// 而它们在界面上必须说得不一样。
const (
	// updateStatusUpdateAvailable 远端版本比本机新。唯一需要人动手的一档。
	updateStatusUpdateAvailable = "update_available"
	// updateStatusUpToDate 远端与本机相同。
	updateStatusUpToDate = "up_to_date"
	// updateStatusAhead 本机比远端**新**。正常出现在"改完还没发版"的机器上,
	// 不是错误 —— 但也绝不能说成"已是最新",那会掩盖"忘了发版"。
	updateStatusAhead = "ahead"
	// updateStatusNoRelease 仓库在,但一个 release 都没发过。
	// 这是本 fork **此刻的真实状态**,而它最容易被错报成"检查失败"。
	updateStatusNoRelease = "no_release"
	// updateStatusCurrentUnknown 本机的二开版本号没声明/解析不了,
	// 于是有远端版本也判不出新旧。远端值照样下发,让人自己看。
	updateStatusCurrentUnknown = "current_unknown"
	// updateStatusLatestUnparsable 远端 tag 不在我们的版本方案里
	// (例如有人手滑打了个 `release-2026-08` 的 tag)。同样不猜。
	updateStatusLatestUnparsable = "latest_unparsable"
)

// 失败的三类根因各有各的 code —— 这是本端点存在的理由之一,不许合并。
const (
	// codeUpdateUnreachable 压根没拿到 HTTP 响应:DNS、TCP、TLS、超时。
	// 离线部署走的是这一支,它应该长得像"这台机器连不上外网",
	// 而不是"更新源出问题了"。
	codeUpdateUnreachable = "qy_update_unreachable"
	// codeUpdateRateLimited GitHub 明确说额度用完了。等一会儿就好,不用查任何东西。
	codeUpdateRateLimited = "qy_update_rate_limited"
	// codeUpdateSourceMissing 404:仓库改名了、被删了、或者被设成了私有。
	// 与"没有 release"分得开(后者是 200 + 空数组,走 updateStatusNoRelease)。
	codeUpdateSourceMissing = "qy_update_source_missing"
	// codeUpdateForbidden 403 但不是限流:UA 被封、被 GitHub 的滥用检测挡了。
	// 与限流分开,因为"等一小时"对它没用。
	codeUpdateForbidden = "qy_update_forbidden"
	// codeUpdateUnexpectedStatus 其余任何非 2xx。message 里带上状态码 ——
	// 塌成"检查失败"就等于把唯一的线索扔了。
	codeUpdateUnexpectedStatus = "qy_update_unexpected_status"
	// codeUpdateBadPayload 200 但响应体不是 release 列表。
	// 典型是被酒店/公司网关的门户页劫持了。
	codeUpdateBadPayload = "qy_update_bad_payload"
)

// AdminCheckUpdate 手动检查二开是否有新版本。
//
// 刻意不走 requireCore,与同目录的 AdminVersion 同一条理由:版本相关的判断
// 不读扩展库,而扩展库不可用恰恰是最想知道"是不是该升级了"的时刻。
func AdminCheckUpdate(c *gin.Context) {
	current := version.ForkVersion()

	// 出站预算按**本进程**计,而不是按客户端 IP。
	//
	// 路由上那道 CriticalRateLimit 的桶键是 mark + 客户端 IP + 路由
	// (middleware/rate-limit.go),**与本站的出站量无关**:它要保护的资源是
	// GitHub 的匿名额度,而那份额度按**服务端出站 IP** 计(60 次/小时)。
	// 两者不是同一个量,所以那道限流挡不住"把额度点光"这件事 —— 单个客户端
	// IP 的稳态预算恰好就是 20 次/1200 秒 = 60 次/小时(100% 吃满,零余量),
	// 而不同来源的管理员各占一桶,线性叠加。实测:10 个客户端 IP 各打 25 次,
	// allowed=200,200 次出站请求在 0.06 秒内从同一个进程打出去。
	//
	// 这里补的才是真正对得上那份额度的闸:一个进程级滑动窗口,上界取 GitHub
	// 额度的一半,给同一出口下的其他消费者(以及别的实例)留出余量。
	// 超出后走 429 + qy_update_rate_limited —— 与 GitHub 自己限流时同一个 code,
	// 因为管理员该做的事完全相同(等一会儿再点),多一个 code 只会多一句要
	// 分辨却分辨不出所以然的话。
	if wait, ok := updateCheckBudget.take(time.Now()); !ok {
		fail(c, http.StatusTooManyRequests, codeUpdateRateLimited,
			fmt.Sprintf("本站在最近一小时内已经向 github.com 查询过 %d 次,"+
				"暂停继续查询以免把匿名额度(60 次/小时/出站 IP)烧完%s。"+
				"这不是站点故障。", maxUpdateChecksPerHour, waitHint(wait)))
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, forkReleasesEndpoint, nil)
	if err != nil {
		serverError(c, err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// GitHub 对没有 User-Agent 的请求直接 403。写死一个固定值而不是转发
	// 管理员的 UA:后者会把管理员的浏览器指纹送到第三方,而这里不需要它。
	req.Header.Set("User-Agent", "new-api-qy-update-check")

	resp, err := updateCheckClient.Do(req)
	if err != nil {
		// err 里带的是更新源那个公开 URL 和传输层原因,没有任何凭据 ——
		// 本端点不发 token(fork 是公开仓库,匿名就能读)。原样透出,
		// 因为"connection refused"和"i/o timeout"对运维是两件事。
		fail(c, http.StatusBadGateway, codeUpdateUnreachable,
			"连不上更新源(服务器到 github.com 的出站请求失败):"+err.Error())
		return
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxUpdateCheckBody))

	switch {
	case resp.StatusCode == http.StatusNotFound:
		fail(c, http.StatusBadGateway, codeUpdateSourceMissing,
			"更新源返回 404:仓库不存在、已改名,或已被设为私有。"+
				"注意这与「还没发过 release」不是一回事,后者会正常返回。源:"+forkReleasesPage)
		return
	case resp.StatusCode == http.StatusTooManyRequests || isGitHubRateLimited(resp):
		fail(c, http.StatusTooManyRequests, codeUpdateRateLimited,
			"GitHub 接口额度已用完(匿名额度按来源 IP 计,每小时 60 次)"+rateLimitResetHint(resp)+
				"。这不是站点故障,稍后再点即可。")
		return
	case resp.StatusCode == http.StatusForbidden:
		fail(c, http.StatusBadGateway, codeUpdateForbidden,
			"更新源返回 403,且没有带限流标记:请求被 GitHub 拒绝(常见于出站 IP 被滥用检测拦下)。"+
				"与额度用完不同,等待并不会让它恢复。")
		return
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		fail(c, http.StatusBadGateway, codeUpdateUnexpectedStatus,
			"更新源返回了意料之外的状态码 "+strconv.Itoa(resp.StatusCode)+
				",请求没有失败但也没有成功。")
		return
	}

	if readErr != nil {
		fail(c, http.StatusBadGateway, codeUpdateUnreachable,
			"更新源的响应读到一半断了:"+readErr.Error())
		return
	}

	var releases []githubRelease
	if err := common.Unmarshal(body, &releases); err != nil {
		fail(c, http.StatusBadGateway, codeUpdateBadPayload,
			"更新源返回的不是 release 列表(响应体无法解析),"+
				"常见于出站流量被网关的门户页劫持:"+err.Error())
		return
	}

	// 空数组 = 仓库在、我们还没发过 release。这是**成功**的一档结论,
	// 不是失败 —— 报成失败会让人去查网络,而实际该做的是去发个 release。
	if len(releases) == 0 {
		ok(c, gin.H{
			"status":       updateStatusNoRelease,
			"current":      current,
			"latest":       "",
			"release_url":  forkReleasesPage,
			"published_at": "",
			"prerelease":   false,
		})
		return
	}

	latest := releases[0]
	if strings.TrimSpace(latest.TagName) == "" {
		fail(c, http.StatusBadGateway, codeUpdateBadPayload,
			"更新源返回的最新 release 没有 tag_name,无从判断版本。")
		return
	}

	releaseURL := latest.HTMLURL
	if releaseURL == "" {
		releaseURL = forkReleasesPage
	}

	ok(c, gin.H{
		"status":       compareToLatest(current, latest.TagName),
		"current":      current,
		"latest":       latest.TagName,
		"release_name": latest.Name,
		"release_url":  releaseURL,
		"published_at": latest.PublishedAt,
		"prerelease":   latest.Prerelease,
	})
}

// compareToLatest 把本机版本与远端 tag 的比较结论翻成一个 status。
//
// 「比不出来」被拆成两档而不是一档:本机没声明(current_unknown)是我们自己的
// 构建流程出了问题,远端 tag 不合方案(latest_unparsable)是发版时 tag 打错了。
// 两者要修的地方不在一处。
func compareToLatest(current, latestTag string) string {
	if _, _, _, ok := version.ForkVersionNumbers(current); !ok {
		return updateStatusCurrentUnknown
	}
	// 远端 tag 走 CompareForkToReleaseTag(要求 `qy-` 前缀)而不是 CompareFork:
	// fork 仓库里的 tag 谁都能推,而 release.yml 是 `push: tags: ['*']` 自动建
	// Release 的。上游的 tag 全是 `v*`,不校验前缀时一条 `git push --tags`
	// 就能让这颗按钮把上游的 v0.9.28 报成「我们的新版本」,并把管理员指向一个
	// 纯上游代码的构建产物 —— 照着升级等于把带扩展的部署降级成上游版。
	cmp, ok := version.CompareForkToReleaseTag(current, latestTag)
	if !ok {
		return updateStatusLatestUnparsable
	}
	switch {
	case cmp < 0:
		return updateStatusUpdateAvailable
	case cmp > 0:
		return updateStatusAhead
	default:
		return updateStatusUpToDate
	}
}

// isGitHubRateLimited 判断一条 403 是不是限流。
//
// 判据是 `X-RateLimit-Remaining: 0`,不是状态码:GitHub 用 403 同时表达
// "额度用完"和"你被拒了",而这两件事的下一步一个是等、一个是查。
// 只看状态码就必然把它们塌成一句话。
func isGitHubRateLimited(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	return strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0"
}

// rateLimitResetHint 把 X-RateLimit-Reset(Unix 秒)翻成一句"还要等多久"。
//
// 直接下发那个时间戳等于让管理员自己去做时区换算。拿不到或已经过期时返回空串,
// 让上层的句子照样通顺 —— 不要为了凑一个提示编一个假的等待时间。
func rateLimitResetHint(resp *http.Response) string {
	raw := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset"))
	if raw == "" {
		return ""
	}
	reset, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return ""
	}
	wait := time.Until(time.Unix(reset, 0))
	if wait <= 0 {
		return ""
	}
	return ",约 " + strconv.Itoa(int(wait.Minutes())+1) + " 分钟后恢复"
}

// maxUpdateChecksPerHour 是**整个进程**一小时内允许向 github.com 发出的检查次数。
//
// 取 30 而不是 60:匿名额度是 60 次/小时/出站 IP,而同一个出口后面可能还有
// 别的实例、别的工具在用同一份额度。留一半余量,代价只是"一小时点不了 31 次"
// —— 而这颗按钮的正常用法是一天点几次。
const maxUpdateChecksPerHour = 30

// updateCheckBudget 是那份预算的实现:一个固定容量的滑动窗口。
//
// 只记时间戳、不记是谁点的 —— 它保护的是本站对第三方额度的**总消耗**,
// 与谁点的无关。这一点正是它与路由上那道按客户端 IP 计的限流的根本区别。
var updateCheckBudget = &outboundBudget{limit: maxUpdateChecksPerHour, window: time.Hour}

type outboundBudget struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   []time.Time
}

// take 记一次出站。返回 (还要等多久, 是否放行);放行时等待时长无意义。
func (b *outboundBudget) take(now time.Time) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := now.Add(-b.window)
	kept := b.hits[:0]
	for _, t := range b.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.hits = kept
	if len(b.hits) >= b.limit {
		// 最早那一次滑出窗口的时刻,就是下一次能放行的时刻。
		return b.hits[0].Sub(cutoff), false
	}
	b.hits = append(b.hits, now)
	return 0, true
}

// waitHint 把一段等待时长翻成一句话,拿不到确定值时什么都不说。
// 与 rateLimitResetHint 同一条口径:不要为了凑一句提示编一个假的等待时间。
func waitHint(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	mins := int(d.Minutes()) + 1
	return fmt.Sprintf(",约 %d 分钟后恢复", mins)
}
