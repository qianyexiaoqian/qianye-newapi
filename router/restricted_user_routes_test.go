package router

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 受限账号(users.status = Disabled)可达面的结构性保证。
//
// # 这条测试守的是什么
//
// 运行时的判据已经是默认拒绝(middleware/restricted_user.go 是白名单),所以
// 「新增一条会话接口」不会造成越权。真正会静默出事的是另外两种形状:
//
//	白名单写错	":no" 写成 ":id" —— 编译过、单测全绿,线上工单页 403
//	新增一条匿名接口	它根本不经过会话链,白名单管不到,而没有人会注意到
//
// 因此这里拿**真实的 gin 路由树**逐条核对:每一条路由都必须被显式归类,
// 白名单里的每一条都必须真实存在。新增一条没归类的路由 = 变红,作者必须表态。
//
// # 归类规则的写法为什么允许前缀
//
// 前缀规则**只用在拒绝侧**,而且只用在整棵子树同构的地方(例如 /api/qy/admin/、
// /api/channel/)。拒绝是安全方向:前缀下新增的路由既被本表覆盖,运行时也照样
// 默认拒绝。放行侧与匿名侧一律精确匹配,因为那两侧新增一条就是新增一个可达面。
//
// # 不包含 SetWebRouter
//
// 前端壳(静态资源 + NoRoute → index.html)必须对所有人可达,否则受限页面本身
// 打不开;它也不注册任何鉴权中间件。把它拉进来只会引入 embed 资产依赖。

// restrictedAnonymousRoutes 无需任何凭据即可到达。受限账号同样可达,这是刻意的:
// 登录/登出/refresh 都在这一档 —— 它们根本不经过会话鉴权链,放开靠的是
// common.UserStatusAllowsSession,不是白名单。
var restrictedAnonymousRoutes = []string{
	// 站点基础信息与前端引导
	"GET /api/about",
	"GET /api/home_page_content",
	"GET /api/notice",
	"GET /api/privacy-policy",
	"GET /api/ratio_config",
	"GET /api/setup",
	"POST /api/setup",
	"GET /api/status",
	"GET /api/uptime/status",
	"GET /api/user-agreement",
	"GET /api/qy/config",
	// 注册 / 找回 / 验证码
	"GET /api/reset_password",
	"GET /api/verification",
	"POST /api/user/register",
	"POST /api/user/reset",
	"GET /api/user/groups",
	// 登录:受限账号必须能走完这几条,否则整个需求不成立
	"POST /api/user/login",
	"POST /api/user/login/2fa",
	"POST /api/user/passkey/login/begin",
	"POST /api/user/passkey/login/finish",
	"GET /api/oauth/wechat",
	"GET /api/oauth/telegram/login",
	// 会话自管理:只挂 SessionCookieOriginGuard,凭 refresh cookie 自证
	"POST /api/user/auth/refresh",
	"POST /api/user/auth/logout",
	// 非标 OAuth 绑定回调(绑定本身在 bind/start 那一步就已被拒)
	"GET /api/oauth/telegram/bind/:flow_token",
	// 支付回调:由上游签名自证,与登录态无关
	"POST /api/creem/webhook",
	"POST /api/stripe/webhook",
	"POST /api/waffo/webhook",
	"POST /api/waffo-pancake/webhook/:env",
	"GET /api/user/epay/notify",
	"POST /api/user/epay/notify",
	"GET /api/subscription/epay/notify",
	"POST /api/subscription/epay/notify",
	"GET /api/subscription/epay/return",
	"POST /api/subscription/epay/return",
	// 抽奖公正性证据链:要求登录等于把历史公正查询锁在注册墙后面
	"GET /api/qy/lottery/public/:act_no/proof",
	// mj 出图:注册在 TokenAuth 之前
	"GET /mj/image/:id",
	"GET /:mode/mj/image/:id",
}

// restrictedDeniedTokenRoutes / Prefixes 走**令牌鉴权链**。
// 受限账号的已有令牌在 middleware/auth.go 的 TokenAuth / TokenAuthReadOnly
// 处整体失效,与会话白名单无关。
var restrictedDeniedTokenRoutes = []string{
	"GET /api/usage/token/",
	"GET /api/log/token",
}

var restrictedDeniedTokenPrefixes = []string{
	"/v1/",     // 含 /v1/videos/:task_id/content 这条 TokenOrUserAuth 混合入口:
	"/v1beta/", // 会话凭据那一支也被白名单拒(见 TokenOrUserAuth)
	"/mj/",
	"/:mode/mj/",
	"/suno/",
	"/kling/",
	"/jimeng/",
	"/dashboard/billing/",
}

// restrictedDeniedSessionPrefixes 是整棵同构的会话链子树(全部 AdminAuth/RootAuth,
// 或全部 UserAuth,没有匿名成员)。
var restrictedDeniedSessionPrefixes = []string{
	"/api/qy/admin/",
	"/api/authz/",
	"/api/channel/",
	"/api/custom-oauth-provider/",
	"/api/deployments/",
	"/api/models/",
	"/api/option/",
	"/api/performance/",
	"/api/prefill_group/",
	"/api/ratio_sync/",
	"/api/redemption/",
	"/api/subscription/admin/",
	"/api/system-info/",
	"/api/system-task/",
	"/api/token/",
	"/api/vendors/",
}

// restrictedDeniedSessionRoutes 是**混合子树**里的逐条拒绝。
// /api/user/、/api/oauth/、/api/qy/(非 admin)、/api/subscription/、/api/log/
// 这几棵下面既有匿名路由也有会话路由,所以一律精确列举 —— 那正是「新增一条
// 匿名接口而没人注意」最可能发生的地方。
var restrictedDeniedSessionRoutes = []string{
	// 令牌(API Key)管理之外的自助只读面:不在最小渲染集合内
	"GET /api/data/",
	"GET /api/data/flow",
	"GET /api/data/flow/self",
	"GET /api/data/self",
	"GET /api/data/users",
	"GET /api/group/",
	"GET /api/log/",
	"GET /api/log/channel_affinity_usage_cache",
	"GET /api/log/search",
	"GET /api/log/self",
	"GET /api/log/self/search",
	"GET /api/log/self/stat",
	"GET /api/log/stat",
	"GET /api/mj/",
	"GET /api/mj/self",
	"GET /api/model-group/options",
	"GET /api/models",
	"GET /api/status/test",
	"GET /api/task/",
	"GET /api/task/self",
	"GET /api/user-group/options",
	"GET /api/qy/api-addresses",
	"GET /api/qy/availability/matrix",
	"GET /api/qy/availability/series",
	"GET /api/qy/session-stats",
	"GET /api/user/models",
	"GET /api/user/self/groups",
	"GET /api/user/sessions",
	"DELETE /api/user/sessions/:sid",
	"POST /api/user/sessions/revoke-others",
	// 条件公共面(HeaderNavModuleAuth / TryUserAuth)。requireAuth=false 时
	// TryUserAuth 过去根本不判 status;白名单显式覆盖了这条链,受限账号在这里
	// 也是 403 而不是「带着身份通过」。
	"GET /api/pricing",
	"GET /api/perf-metrics",
	"GET /api/perf-metrics/summary",
	"GET /api/rankings",
	"POST /api/oauth/state",
	"GET /api/oauth/:provider",
	// 会话链上的 relay:playground 走 UserAuth,最容易被当成普通管理台接口漏进白名单
	"POST /pg/chat/completions",
	// 钱:划转 / 提现 / 佣金
	"GET /api/qy/commission/invitees",
	"GET /api/qy/commission/records",
	"GET /api/qy/commission/summary",
	"GET /api/qy/transfer/contacts",
	"GET /api/qy/transfer/limits",
	"GET /api/qy/transfer/records",
	"POST /api/qy/transfer",
	"POST /api/qy/transfer/contacts",
	"POST /api/qy/transfer/preview",
	"PUT /api/qy/transfer/contacts/:id",
	"DELETE /api/qy/transfer/contacts/:id",
	"GET /api/qy/withdraw/:id",
	"GET /api/qy/withdraw/:id/proof",
	"GET /api/qy/withdraw/config",
	"GET /api/qy/withdraw/payees",
	"GET /api/qy/withdraw/records",
	"POST /api/qy/withdraw",
	"POST /api/qy/withdraw/:id/cancel",
	"POST /api/qy/withdraw/payees",
	"POST /api/qy/withdraw/proofs",
	"DELETE /api/qy/withdraw/payees/:ref",
	"POST /api/user/aff_transfer",
	"GET /api/user/aff",
	// 充值 / 兑换 / 签到
	"GET /api/user/checkin",
	"POST /api/user/checkin",
	"GET /api/user/topup",
	"GET /api/user/topup/info",
	"GET /api/user/topup/self",
	"POST /api/user/topup",
	"POST /api/user/topup/complete",
	"POST /api/user/amount",
	"POST /api/user/pay",
	"POST /api/user/creem/pay",
	"POST /api/user/stripe/amount",
	"POST /api/user/stripe/pay",
	"POST /api/user/waffo/amount",
	"POST /api/user/waffo/pay",
	"POST /api/user/waffo-pancake/amount",
	"POST /api/user/waffo-pancake/pay",
	// 抽奖 / 竞猜
	"GET /api/qy/lottery/activities",
	"GET /api/qy/lottery/activities/:act_no",
	"GET /api/qy/lottery/activities/:act_no/eligibility",
	"GET /api/qy/lottery/my-entries",
	"GET /api/qy/lottery/my/prizes/:payout_no",
	"GET /api/qy/lottery/series/:series_no",
	"POST /api/qy/lottery/activities/:act_no/entries",
	// 订阅
	"GET /api/subscription/plans",
	"GET /api/subscription/self",
	"POST /api/subscription/balance/pay",
	"POST /api/subscription/creem/pay",
	"POST /api/subscription/epay/pay",
	"POST /api/subscription/stripe/pay",
	"POST /api/subscription/waffo-pancake/pay",
	"GET /api/qy/subscription/entitlements",
	// 个人资料 / 凭据 / 支付密码:受限账号不应能改变自己的凭据面
	"PUT /api/user/self",
	"DELETE /api/user/self",
	"PUT /api/user/setting",
	"GET /api/user/token",
	"GET /api/user/passkey",
	"DELETE /api/user/passkey",
	"POST /api/user/passkey/register/begin",
	"POST /api/user/passkey/register/finish",
	"POST /api/user/passkey/verify/begin",
	"POST /api/user/passkey/verify/finish",
	"GET /api/user/2fa/status",
	"POST /api/user/2fa/setup",
	"POST /api/user/2fa/enable",
	"POST /api/user/2fa/disable",
	"POST /api/user/2fa/backup_codes",
	"POST /api/oauth/email/bind",
	"POST /api/oauth/wechat/bind",
	"POST /api/oauth/telegram/bind/start",
	"GET /api/user/oauth/bindings",
	"DELETE /api/user/oauth/bindings/:provider_id",
	"POST /api/verify",
	"GET /api/qy/pay-password",
	"POST /api/qy/pay-password",
	"PUT /api/qy/pay-password",
	"POST /api/qy/pay-password/recover/code",
	"POST /api/qy/pay-password/recover/reset",
	// 管理端(混合子树 /api/user/ 下的 admin 分组)。authHelper 先判 status
	// 再判 role,所以被禁用的管理员在这里同样只有 403,role 不给他任何旁路。
	"GET /api/user/",
	"GET /api/user/:id",
	"GET /api/user/search",
	"GET /api/user/2fa/stats",
	"GET /api/user/:id/oauth/bindings",
	"POST /api/user/",
	"POST /api/user/manage",
	"PUT /api/user/",
	"DELETE /api/user/:id",
	"DELETE /api/user/:id/2fa",
	"DELETE /api/user/:id/reset_passkey",
	"DELETE /api/user/:id/bindings/:binding_type",
	"DELETE /api/user/:id/oauth/bindings/:provider_id",
}

const (
	classRestrictedAllow = "restricted-allow"
	classDeniedSession   = "session-deny"
	classDeniedToken     = "token-deny"
	classAnonymous       = "anonymous"
)

func buildRestrictedPolicyRouteTree(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	// 扩展默认关闭时 RegisterRoutes 直接返回,那样 /api/qy/** 整棵树都不在,
	// 这条测试就量不到工单白名单。
	path := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(path, []byte("enabled: true\n"+
		"database:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n"), 0o600))
	t.Setenv(config.EnvConfigPath, path)
	require.NoError(t, config.Load())
	require.True(t, config.Enabled())

	engine := gin.New()
	// 注册顺序与 main.go 一致:扩展先于上游,否则 /api/qy 会被上游的分组吃掉。
	qianye.RegisterRoutes(engine)
	SetApiRouter(engine)
	SetDashboardRouter(engine)
	SetRelayRouter(engine)
	SetVideoRouter(engine)
	return engine
}

func classifyRestrictedRoute(key, path string, allow, anonymous, token, session map[string]struct{}) string {
	if _, ok := allow[key]; ok {
		return classRestrictedAllow
	}
	if _, ok := anonymous[key]; ok {
		return classAnonymous
	}
	if _, ok := token[key]; ok {
		return classDeniedToken
	}
	if _, ok := session[key]; ok {
		return classDeniedSession
	}
	for _, prefix := range restrictedDeniedTokenPrefixes {
		if strings.HasPrefix(path, prefix) {
			return classDeniedToken
		}
	}
	for _, prefix := range restrictedDeniedSessionPrefixes {
		if strings.HasPrefix(path, prefix) {
			return classDeniedSession
		}
	}
	return ""
}

func toRestrictedKeySet(t *testing.T, name string, keys []string) map[string]struct{} {
	t.Helper()
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		_, duplicate := set[key]
		require.Falsef(t, duplicate, "%s 里出现重复条目 %q", name, key)
		set[key] = struct{}{}
	}
	return set
}

// 路由表里的每一条都必须被显式归类。新增一条没归类的路由 = 变红。
func TestEveryRouteIsClassifiedForRestrictedUsers(t *testing.T) {
	engine := buildRestrictedPolicyRouteTree(t)

	allow := toRestrictedKeySet(t, "白名单", middleware.RestrictedUserAllowedRoutes())
	anonymous := toRestrictedKeySet(t, "restrictedAnonymousRoutes", restrictedAnonymousRoutes)
	token := toRestrictedKeySet(t, "restrictedDeniedTokenRoutes", restrictedDeniedTokenRoutes)
	session := toRestrictedKeySet(t, "restrictedDeniedSessionRoutes", restrictedDeniedSessionRoutes)

	counts := map[string]int{}
	unclassified := []string{}
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		class := classifyRestrictedRoute(key, route.Path, allow, anonymous, token, session)
		if class == "" {
			unclassified = append(unclassified, key)
			continue
		}
		counts[class]++
	}
	sort.Strings(unclassified)
	require.Emptyf(t, unclassified,
		"以下路由既不在受限账号白名单里,也没有被显式归类为拒绝/匿名。\n"+
			"新增接口对受限账号默认关闭是运行时行为,但你必须在 router/restricted_user_routes_test.go 里表态:\n%s",
		strings.Join(unclassified, "\n"))

	// 归类不能全落进同一个桶 —— 那说明规则写歪了而不是路由表变了。
	assert.Equal(t, len(allow), counts[classRestrictedAllow])
	assert.NotZero(t, counts[classDeniedSession])
	assert.NotZero(t, counts[classDeniedToken])
	assert.NotZero(t, counts[classAnonymous])
}

// 白名单与归类表里的每一条精确条目都必须真实存在于路由树上。
// 守的是 ":no" 写成 ":id" 这类手滑:那样白名单永远匹配不上,受限账号连工单都打不开。
func TestRestrictedUserPolicyEntriesExistInRouteTree(t *testing.T) {
	engine := buildRestrictedPolicyRouteTree(t)

	registered := make(map[string]struct{}, len(engine.Routes()))
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = struct{}{}
	}

	declared := map[string][]string{
		"middleware.restrictedUserAllowedRoutes": middleware.RestrictedUserAllowedRoutes(),
		"restrictedAnonymousRoutes":              restrictedAnonymousRoutes,
		"restrictedDeniedTokenRoutes":            restrictedDeniedTokenRoutes,
		"restrictedDeniedSessionRoutes":          restrictedDeniedSessionRoutes,
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, key := range declared[name] {
			_, ok := registered[key]
			assert.Truef(t, ok, "%s 里的 %q 在真实路由树上不存在(参数名写错?路由被删了?)", name, key)
		}
	}
}

// 放行侧与拒绝侧不能重叠,前缀规则也不能盖住白名单。
// 重叠本身不会造成越权(运行时白名单先判),但它意味着两份声明已经在打架,
// 下一个改动的人无从判断哪一份才是意图。
func TestRestrictedUserAllowlistDoesNotOverlapDenials(t *testing.T) {
	allow := middleware.RestrictedUserAllowedRoutes()
	denied := map[string]string{}
	for _, key := range restrictedAnonymousRoutes {
		denied[key] = "restrictedAnonymousRoutes"
	}
	for _, key := range restrictedDeniedTokenRoutes {
		denied[key] = "restrictedDeniedTokenRoutes"
	}
	for _, key := range restrictedDeniedSessionRoutes {
		denied[key] = "restrictedDeniedSessionRoutes"
	}

	for _, key := range allow {
		if name, ok := denied[key]; ok {
			assert.Failf(t, "白名单与归类表冲突", "%q 同时出现在白名单和 %s 里", key, name)
		}
		path := strings.TrimPrefix(key, strings.SplitN(key, " ", 2)[0]+" ")
		for _, prefix := range append(append([]string{}, restrictedDeniedTokenPrefixes...), restrictedDeniedSessionPrefixes...) {
			assert.Falsef(t, strings.HasPrefix(path, prefix),
				"白名单条目 %q 落在拒绝前缀 %q 下,两份声明打架了", key, prefix)
		}
	}
}

// 白名单不得包含任何管理端路径。authHelper 是先判 status 再判 role,
// admitRestrictedUser 又额外挡了一次 minRole —— 这条测试把「不能有 role 旁路」
// 这个意图钉在数据上,而不是只钉在代码分支上。
func TestRestrictedUserAllowlistHasNoAdminSurface(t *testing.T) {
	for _, key := range middleware.RestrictedUserAllowedRoutes() {
		parts := strings.SplitN(key, " ", 2)
		require.Len(t, parts, 2, "白名单条目必须是 \"METHOD /path\" 形状: %q", key)
		assert.Falsef(t, strings.Contains(parts[1], "/admin"),
			"白名单里出现了管理端路径 %q", key)
		for _, prefix := range restrictedDeniedSessionPrefixes {
			assert.Falsef(t, strings.HasPrefix(parts[1], prefix), "%q 落在拒绝前缀 %q 下", key, prefix)
		}
	}
}
