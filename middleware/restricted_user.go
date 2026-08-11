package middleware

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"

	"github.com/gin-gonic/gin"
)

// 受限账号(users.status = UserStatusDisabled)的会话白名单。
//
// # 语义
//
// 「禁用」不再是一刀切封号,而是**受限账号**:能登录进来说话(提工单、申诉),
// 但不能动任何资源与钱。
//
// # 为什么是白名单而不是黑名单
//
// 黑名单的失败方式是静默的:今后任何新增的接口默认对受限账号**开放**,而且没有
// 人会注意到 —— 直到某天有人发现被封的号还能划转余额。白名单的失败方式是响亮的:
// 新接口默认被拒,用户会立刻来报「点不了」。
//
// 这个判据必须做在 authHelper / TryUserAuth / TokenOrUserAuth **内部**,按
// method + gin FullPath 精确匹配,而不是在某个路由组上摘掉中间件:
// UserAuth/AdminAuth/RootAuth 三个包装函数收敛到同一个 authHelper,
// 在组上动手等于放开整棵会话树。
//
// # 边界
//
//   - 只覆盖**会话鉴权链**。令牌链(TokenAuth / TokenAuthReadOnly,relay 与全部
//     异步任务接口)与会话链没有任何共用代码,那里的判据仍是「必须 Enabled」,
//     受限账号的已有令牌一律 403 —— 否则封号就只剩「不能登管理台」一层皮。
//   - 只覆盖 minRole == RoleCommonUser 的路由。即便有人误把一条管理端路径写进
//     下面这张表,admitRestrictedUser 也会因为 minRole 不符而拒绝:
//     绝不能出现「role 够高就跳过 status 判据」的旁路。
//   - 登录 / 登出 / refresh 不在表里,因为它们根本不经过会话鉴权链
//     (/api/user/login 无 UserAuth;/api/user/auth/{refresh,logout} 只挂
//     SessionCookieOriginGuard)。放开它们靠的是 common.UserStatusAllowsSession。
//
// # 结构性保证
//
// router/restricted_user_routes_test.go 拿**真实的 gin 路由树**逐条核对:
// 表里的每一条都必须真实存在(防手滑写错 :param 名),而路由树里的每一条都必须
// 被显式归类(放行 / 拒绝 / 匿名可达)。新增一条没归类的路由 = 测试变红。
var restrictedUserAllowedRoutes = map[string]struct{}{
	// 渲染受限页所需的最小自身信息:用户名、status、余额。
	// 它同时是前端判定「我被限制了」的唯一权威来源。
	"GET /api/user/self": {},

	// ── 工单:申诉的正式入口,需求原文点名要留的那一块 ────────────────
	// config 必须一起放开,否则前端只能盲填标题/正文长度然后被 400。
	"GET /api/qy/ticket/config":     {},
	"GET /api/qy/ticket/list":       {},
	"GET /api/qy/ticket/:no":        {},
	"POST /api/qy/ticket":           {},
	"POST /api/qy/ticket/:no/reply": {},
	// close 是 ticket.max_open_per_user 这道闸唯一由用户控制的出口
	// (另一个出口 auto_close 只对「管理员已回复、用户 7 天没回应」的单生效)。
	// 不放开的话,受限账号挂满 5 张单之后就再也提不了新单,而他收到的提示
	// 是一个自己无法执行的下一步。
	"POST /api/qy/ticket/:no/close": {},
	// 附件:唯一能消耗宿主机磁盘的用户入口,已挂 CriticalRateLimit +
	// pendingUploadMax + 单人磁盘总量闸,配额与正常账号完全一致。
	"POST /api/qy/ticket/images":     {},
	"GET /api/qy/ticket/images/:ref": {},
	// 丢弃未提交的图片。不放开会让 pendingUploadMax 的名额被占死。
	"DELETE /api/qy/ticket/images/:ref": {},

	// ── 违规明细与结构化申诉 ────────────────────────────────────────
	// 一个看不到原因的申诉入口只会变成工单洪水:用户不知道自己犯了什么,
	// 只能一张一张单地问。这三条都是本仓已有的、比工单更结构化的通道。
	"GET /api/qy/violation/my-summary": {},
	"GET /api/qy/violation/my-records": {},
	// 类型公示是「我到底还能犯几次」的唯一答案,而受限账号正是最需要这个答案的人:
	// 他已经越过一次线了,不告诉他每一类的阈值与自己的计数,他只能靠再撞一次来试探。
	// 接口只下发 published=true 的对外文案(白名单结构体 userCategoryView),
	// 不含内部名/内部说明/规则判据,泄漏面与 my-summary 同级。
	"GET /api/qy/violation/my-categories": {},
	"POST /api/qy/violation/appeals":      {},
}

// restrictedUserContextKey 标记「本次请求的身份是一个受限账号」。
// handler 可以据此在响应里做降级(例如工单页顶部的说明条)。
const restrictedUserContextKey = "user_restricted"

// RestrictedUserRouteAllowed 报告受限账号能否到达某条会话链路由。
// fullPath 必须是 gin 注册时的模式(含 :param 段),不是请求的实际 URL。
func RestrictedUserRouteAllowed(method, fullPath string) bool {
	if method == "" || fullPath == "" {
		return false
	}
	_, ok := restrictedUserAllowedRoutes[method+" "+fullPath]
	return ok
}

// RestrictedUserAllowedRoutes 返回白名单的全部条目("METHOD /path"),已排序。
// 供 router 包的守卫测试拿真实路由树核对,不要在业务代码里调用。
func RestrictedUserAllowedRoutes() []string {
	out := make([]string, 0, len(restrictedUserAllowedRoutes))
	for key := range restrictedUserAllowedRoutes {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// IsRestrictedUser 报告当前请求的身份是否为受限账号。
func IsRestrictedUser(c *gin.Context) bool {
	return c.GetBool(restrictedUserContextKey)
}

// admitRestrictedUser 是受限账号在会话鉴权链上的唯一放行口。
// 放行时返回 true 并打上 restrictedUserContextKey;否则已经写好 403 并 Abort。
//
// 响应刻意是 403 而不是 401:受限账号**是登录着的**,401 会被前端的通用拦截器
// 当成「会话失效」而清空凭据并跳登录页,于是用户永远进不去工单页。
// code 也与未登录分开(AUTH_USER_RESTRICTED vs AUTH_UNAUTHORIZED /
// AUTH_SESSION_REVOKED),让前端能一眼区分「你被限制了」和「你没登录」。
func admitRestrictedUser(c *gin.Context, userID, minRole int) bool {
	if minRole <= common.RoleCommonUser && RestrictedUserRouteAllowed(c.Request.Method, c.FullPath()) {
		c.Set(restrictedUserContextKey, true)
		return true
	}
	// 留痕只覆盖写方法。受限账号的浏览器会持续轮询若干只读接口,把 GET 也记下来
	// 只会把「被封的号还在试着划转/建 key」这条真正有价值的信号淹掉;
	// 而写请求本身已经被 GlobalAPIRateLimit 限速,不构成日志放大。
	// /api/qy/** 另有 audit.Middleware 把包括本次 403 在内的整条请求落库。
	switch c.Request.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		logger.LogWarn(c, fmt.Sprintf("restricted user %d denied at %s %s",
			userID, c.Request.Method, c.FullPath()))
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"success":    false,
		"code":       "AUTH_USER_RESTRICTED",
		"restricted": true,
		"message":    common.TranslateMessage(c, i18n.MsgAuthUserRestricted),
	})
	return false
}
