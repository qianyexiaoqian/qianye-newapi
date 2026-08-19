package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const authIdentityContextKey = "auth_identity"

type dashboardCredentialKind int

const (
	dashboardCredentialUnmatched dashboardCredentialKind = iota
	dashboardCredentialInternal
	dashboardCredentialPAT
)

func validUserInfo(username string, role int) bool {
	// check username is empty
	if strings.TrimSpace(username) == "" {
		return false
	}
	if !common.IsValidateRole(role) {
		return false
	}
	return true
}

func authHelper(c *gin.Context, minRole int) {
	user, identity, useAccessToken, err := authenticateDashboardRequest(c)
	if err != nil {
		writeDashboardAuthError(c, err)
		return
	}
	// 受限账号(status = Disabled)不再一刀切 401,而是落到白名单判据上。
	// 顺序必须保持「先判 status 再判 role」:一个被自动封禁模块封掉的管理员
	// 绝不能因为 role 高就带着完整管理权继续操作。admitRestrictedUser 内部
	// 也再挡一次 minRole,两处都在。
	if user.Status != common.UserStatusEnabled {
		if !admitRestrictedUser(c, user.Id, minRole) {
			return
		}
	}
	if user.Role < minRole {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "AUTH_INSUFFICIENT_PRIVILEGE", "message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege)})
		return
	}
	if !validUserInfo(user.Username, user.Role) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_INVALID", "message": common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)})
		return
	}
	setDashboardAuthContext(c, user, identity, useAccessToken)

	// 管理/root 写操作审计兜底：内聚在鉴权链路里，保证任何经过 AdminAuth/RootAuth
	// 的写接口都会自动留痕（无需在路由上单独挂审计中间件，避免漏挂）。
	// handler 内手动埋点者会设置 ContextKeyAuditLogged，finishAdminAudit 据此跳过。
	var auditWriter *auditResponseWriter
	if minRole >= common.RoleAdminUser {
		auditWriter = beginAdminAudit(c)
	}

	c.Next()

	finishAdminAudit(c, auditWriter)
}

func TryUserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		user, identity, credentialKind, err := classifyDashboardCredential(c)
		if err != nil {
			writeDashboardAuthError(c, err)
			return
		}
		if credentialKind != dashboardCredentialUnmatched {
			// TryUserAuth 过去**根本不判 status**,于是 /api/pricing、
			// /api/perf-metrics、/api/rankings、/api/oauth/state、
			// /api/oauth/:provider 这几条会让受限账号带着完整身份通过 ——
			// 其中 /api/oauth/* 在带身份时走的是**绑定**分支,那是改凭据面。
			// 白名单必须显式覆盖这条链,否则它就是黑名单的第一个漏点。
			//
			// 这里刻意不「降级为匿名」:匿名通过 /api/oauth/:provider 会被当成
			// 登录/注册而不是绑定,把一次拒绝变成一次身份漂移。
			if user.Status != common.UserStatusEnabled && !admitRestrictedUser(c, user.Id, common.RoleCommonUser) {
				return
			}
			setDashboardAuthContext(c, user, identity, credentialKind == dashboardCredentialPAT)
		}
		c.Next()
	}
}

func UserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleCommonUser)
	}
}

func AdminAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleAdminUser)
	}
}

func RootAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleRootUser)
	}
}

// GetAuthIdentity returns a dashboard session identity. PAT-authenticated
// requests intentionally have no SessionID and cannot manage browser sessions.
func GetAuthIdentity(c *gin.Context) (service.AuthIdentity, bool) {
	value, ok := c.Get(authIdentityContextKey)
	if !ok {
		return service.AuthIdentity{}, false
	}
	identity, ok := value.(service.AuthIdentity)
	return identity, ok
}

// GetSessionAuthIdentity returns only identities backed by a live dashboard
// session. PAT-authenticated requests intentionally fail this check.
func GetSessionAuthIdentity(c *gin.Context) (service.AuthIdentity, bool) {
	identity, ok := GetAuthIdentity(c)
	if !ok {
		identity = service.AuthIdentity{
			UserID:          c.GetInt("id"),
			SessionID:       c.GetString("session_id"),
			UserAuthVersion: c.GetInt64("auth_version"),
			SessionVersion:  c.GetInt64("session_version"),
		}
	}
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 || identity.SessionVersion <= 0 {
		return service.AuthIdentity{}, false
	}
	return identity, true
}

func authenticateDashboardRequest(c *gin.Context) (*model.UserBase, service.AuthIdentity, bool, error) {
	user, identity, credentialKind, err := classifyDashboardCredential(c)
	if err != nil {
		return nil, service.AuthIdentity{}, credentialKind == dashboardCredentialPAT, err
	}
	if credentialKind == dashboardCredentialUnmatched {
		return nil, service.AuthIdentity{}, false, service.ErrAuthTokenInvalid
	}
	return user, identity, credentialKind == dashboardCredentialPAT, nil
}

func classifyDashboardCredential(c *gin.Context) (*model.UserBase, service.AuthIdentity, dashboardCredentialKind, error) {
	raw, ok := authorizationToken(c.GetHeader("Authorization"))
	if !ok {
		return nil, service.AuthIdentity{}, dashboardCredentialUnmatched, nil
	}
	identity, internal, err := service.ParseDashboardAccessToken(raw)
	if internal {
		if err != nil {
			return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
		}
		_, user, err := service.ValidateLoginSession(identity)
		if err != nil {
			return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
		}
		return user, identity, dashboardCredentialInternal, nil
	}
	patUser, err := model.ValidateAccessToken(raw)
	if err != nil {
		return nil, service.AuthIdentity{}, dashboardCredentialPAT, err
	}
	if patUser == nil || patUser.Id <= 0 {
		return nil, service.AuthIdentity{}, dashboardCredentialUnmatched, nil
	}
	user, err := model.GetUserCache(patUser.Id)
	if err != nil {
		return nil, service.AuthIdentity{}, dashboardCredentialPAT, err
	}
	return user, service.AuthIdentity{UserID: user.Id, UserAuthVersion: user.AuthVersion}, dashboardCredentialPAT, nil
}

func authorizationToken(header string) (string, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false
	}
	parts := strings.Fields(header)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		header = parts[1]
	} else if len(parts) != 1 {
		return "", false
	}
	return header, header != ""
}

func setDashboardAuthContext(c *gin.Context, user *model.UserBase, identity service.AuthIdentity, useAccessToken bool) {
	c.Header("Auth-Version", "864b7076dbcd0a3c01b5520316720ebf")
	c.Set("username", user.Username)
	c.Set("role", user.Role)
	c.Set("id", user.Id)
	c.Set("group", user.Group)
	c.Set("user_group", user.Group)
	c.Set("use_access_token", useAccessToken)
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
	c.Set(authIdentityContextKey, identity)
	user.WriteContext(c)
}

func writeDashboardAuthError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrAuthTokenExpired) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_TOKEN_EXPIRED", "message": common.TranslateMessage(c, i18n.MsgAuthNotLoggedIn)})
		return
	}
	if errors.Is(err, service.ErrLoginSessionRevoked) || errors.Is(err, gorm.ErrRecordNotFound) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_SESSION_REVOKED", "message": common.TranslateMessage(c, i18n.MsgAuthNotLoggedIn)})
		return
	}
	if errors.Is(err, service.ErrAuthTokenInvalid) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_UNAUTHORIZED", "message": common.TranslateMessage(c, i18n.MsgAuthAccessTokenInvalid)})
		return
	}
	common.SysLog("dashboard authentication error: " + err.Error())
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"success": false, "code": "AUTH_INTERNAL_ERROR", "message": common.TranslateMessage(c, i18n.MsgDatabaseError)})
}

func RequirePermission(permission authz.Permission) func(c *gin.Context) {
	return func(c *gin.Context) {
		role := c.GetInt("role")
		userID := c.GetInt("id")
		if authz.Can(userID, role, permission) {
			c.Next()
			return
		}
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
		})
		c.Abort()
	}
}

func WssAuth(c *gin.Context) {

}

// TokenOrUserAuth allows either session-based user auth or API token auth.
// Used for endpoints that need to be accessible from both the dashboard and API clients.
func TokenOrUserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		raw, ok := authorizationToken(c.GetHeader("Authorization"))
		if ok {
			identity, internal, err := service.ParseDashboardAccessToken(raw)
			if !internal {
				TokenAuth()(c)
				return
			}
			if err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			_, user, err := service.ValidateLoginSession(identity)
			if err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			// 混合入口(/v1/videos/:task_id/content)按凭据形状分流:走到这里
			// 说明用的是会话凭据,因此同样受白名单管辖 —— 它绕开了 authHelper,
			// 漏在这里等于给受限账号留了一条会话认证的 relay 读取通道。
			if user.Status != common.UserStatusEnabled && !admitRestrictedUser(c, user.Id, common.RoleCommonUser) {
				return
			}
			setDashboardAuthContext(c, user, identity, false)
			c.Next()
			return
		}
		// Opaque credentials are relay API keys here, never dashboard PATs.
		TokenAuth()(c)
	}
}

// TokenAuthReadOnly 宽松版本的令牌认证中间件，用于只读查询接口。
// 只验证令牌 key 是否存在，不检查令牌状态、过期时间和额度。
// 即使令牌已过期、已耗尽或已禁用，也允许访问。
// 仍然检查用户是否被封禁。
func TokenAuthReadOnly() func(c *gin.Context) {
	return func(c *gin.Context) {
		key := c.Request.Header.Get("Authorization")
		if key == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgTokenNotProvided),
			})
			c.Abort()
			return
		}
		if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
			key = strings.TrimSpace(key[7:])
		}
		key = strings.TrimPrefix(key, "sk-")
		parts := strings.Split(key, "-")
		key = parts[0]

		token, err := model.GetTokenByKey(key, false)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgTokenInvalid),
				})
			} else {
				common.SysLog("TokenAuthReadOnly GetTokenByKey database error: " + err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
				})
			}
			c.Abort()
			return
		}

		// TokenAuthReadOnly must keep allowing other token states to query read-only
		// data, such as token usage logs; only explicitly disabled tokens are denied.
		if token.Status == common.TokenStatusDisabled {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgTokenStatusUnavailable),
			})
			c.Abort()
			return
		}

		userCache, err := model.GetUserCache(token.UserId)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 与 TokenAuth 同一处理(理由见那里的长注释):令牌所属账号已被
			// 软删除是一个稳定可复现的凭据状态,不是数据库故障。
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgTokenOwnerMissing),
			})
			c.Abort()
			return
		}
		if err != nil {
			common.SysLog(fmt.Sprintf("TokenAuthReadOnly GetUserCache error for user %d: %v", token.UserId, err))
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
			})
			c.Abort()
			return
		}
		// 令牌链的判据刻意保持「必须 Enabled」,不接 middleware/restricted_user.go
		// 的白名单:受限账号的**已有令牌必须整体失效**,否则封号就只剩
		// 「不能登管理台」一层皮。会话链与令牌链没有任何共用代码,不要把这里
		// 「顺手对齐」成会话链的写法。
		if userCache.Status != common.UserStatusEnabled {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgAuthUserBanned),
			})
			c.Abort()
			return
		}

		c.Set("id", token.UserId)
		c.Set("token_id", token.Id)
		c.Set("token_key", token.Key)
		c.Next()
	}
}

func TokenAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		// 先检测是否为ws
		if c.Request.Header.Get("Sec-WebSocket-Protocol") != "" {
			// Sec-WebSocket-Protocol: realtime, openai-insecure-api-key.sk-xxx, openai-beta.realtime-v1
			// read sk from Sec-WebSocket-Protocol
			key := c.Request.Header.Get("Sec-WebSocket-Protocol")
			parts := strings.Split(key, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "openai-insecure-api-key") {
					key = strings.TrimPrefix(part, "openai-insecure-api-key.")
					break
				}
			}
			c.Request.Header.Set("Authorization", "Bearer "+key)
		}
		// 检查path包含/v1/messages 或 /v1/models
		if strings.Contains(c.Request.URL.Path, "/v1/messages") || strings.Contains(c.Request.URL.Path, "/v1/models") {
			anthropicKey := c.Request.Header.Get("x-api-key")
			if anthropicKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+anthropicKey)
			}
		}
		// gemini api 从query中获取key
		if c.Request.URL.Path == "/v1/models" ||
			strings.HasPrefix(c.Request.URL.Path, "/v1beta/models") ||
			strings.HasPrefix(c.Request.URL.Path, "/v1beta/openai/models") ||
			strings.HasPrefix(c.Request.URL.Path, "/v1/models/") {
			skKey := c.Query("key")
			if skKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+skKey)
			}
			// 从x-goog-api-key header中获取key
			xGoogKey := c.Request.Header.Get("x-goog-api-key")
			if xGoogKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+xGoogKey)
			}
		}
		key := c.Request.Header.Get("Authorization")
		parts := make([]string, 0)
		if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
			key = strings.TrimSpace(key[7:])
		}
		if key == "" || key == "midjourney-proxy" {
			key = c.Request.Header.Get("mj-api-secret")
			if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
				key = strings.TrimSpace(key[7:])
			}
			key = strings.TrimPrefix(key, "sk-")
			parts = strings.Split(key, "-")
			key = parts[0]
		} else {
			key = strings.TrimPrefix(key, "sk-")
			parts = strings.Split(key, "-")
			key = parts[0]
		}
		token, err := model.ValidateUserToken(key)
		if token != nil {
			id := c.GetInt("id")
			if id == 0 {
				c.Set("id", token.UserId)
			}
		}
		if err != nil {
			if errors.Is(err, model.ErrDatabase) {
				common.SysLog("TokenAuth ValidateUserToken database error: " + err.Error())
				abortWithOpenAiMessage(c, http.StatusInternalServerError,
					common.TranslateMessage(c, i18n.MsgDatabaseError))
			} else {
				abortWithOpenAiMessage(c, http.StatusUnauthorized,
					common.TranslateMessage(c, i18n.MsgTokenInvalid))
			}
			return
		}

		allowIps := token.GetIpLimits()
		if len(allowIps) > 0 {
			clientIp := c.ClientIP()
			logger.LogDebug(c, "Token has IP restrictions, checking client IP %s", clientIp)
			ip := net.ParseIP(clientIp)
			if ip == nil {
				abortWithOpenAiMessage(c, http.StatusForbidden, "无法解析客户端 IP 地址")
				return
			}
			if common.IsIpInCIDRList(ip, allowIps) == false {
				abortWithOpenAiMessage(c, http.StatusForbidden, "您的 IP 不在令牌允许访问的列表中", types.ErrorCodeAccessDenied)
				return
			}
			logger.LogDebug(c, "Client IP %s passed the token IP restrictions check", clientIp)
		}

		userCache, err := model.GetUserCache(token.UserId)
		// 令牌所属账号已被软删除:tokens 行还在(两条软删除路径都不清理它,
		// 也没有任何后台任务会清),users 行已经进了软删除作用域,于是
		// GetUserCache 必然回 record not found。
		//
		// 这不是数据库故障,而是一个稳定可复现的凭据状态,方向上也一直是
		// fail-closed(请求确实被拒、不计费)。错的只是它的**表达**:
		// 500 + "Database error" 会让"删号之后忘记下线的脚本继续重试"这类
		// 高频调用在监控里与真实的数据库故障同色,把真故障淹掉。
		// 会话链(ValidateAccessToken 命中软删除作用域后返回 nil,nil)本来
		// 就回 401,令牌链在这里与它对齐。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			abortWithOpenAiMessage(c, http.StatusUnauthorized,
				common.TranslateMessage(c, i18n.MsgTokenOwnerMissing),
				types.ErrorCodeAccessDenied)
			return
		}
		if err != nil {
			common.SysLog(fmt.Sprintf("TokenAuth GetUserCache error for user %d: %v", token.UserId, err))
			abortWithOpenAiMessage(c, http.StatusInternalServerError,
				common.TranslateMessage(c, i18n.MsgDatabaseError))
			return
		}
		// 同 TokenAuthReadOnly:受限账号的令牌在这里整体失效,全部 relay
		// (/v1/*、/v1beta/*、/mj、/suno、/kling、/jimeng、/dashboard/billing/*)
		// 一律 403。这是「禁用」在改造后唯一保持一刀切的一条链。
		userEnabled := userCache.Status == common.UserStatusEnabled
		if !userEnabled {
			abortWithOpenAiMessage(c, http.StatusForbidden, common.TranslateMessage(c, i18n.MsgAuthUserBanned))
			return
		}

		userCache.WriteContext(c)

		userGroup := userCache.Group
		tokenGroup := token.Group
		if tokenGroup != "" {
			// check common.UserUsableGroups[userGroup]
			if _, ok := service.QyUsableGroupsForUser(userCache.Id, userGroup)[tokenGroup]; !ok {
				abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf("无权访问 %s 分组", tokenGroup))
				return
			}
			// check group in common.GroupRatio
			if !ratio_setting.ContainsGroupRatio(tokenGroup) {
				if tokenGroup != "auto" {
					abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf("分组 %s 已被弃用", tokenGroup))
					return
				}
			}
			userGroup = tokenGroup
		} else {
			// ── 空分组令牌:解析该用户分组的「默认模型分组」 ──────────────
			//
			// 上游在这一档直接把 users.group 当成模型分组用(userGroup 保持
			// userCache.Group 不变),于是用户分组一旦不再兼作模型分组就一个渠道
			// 都没有,表现是 503「No available channel for model X under group Y」。
			// 这个 else 分支既没有 hook 也没有任何校验,所以必须直接改上游。
			//
			// 默认实现恒返回 (userGroup, inherit) ⇒ 下面整段是 no-op ⇒ 逐位等于上游。
			defaultGroup, mode := service.QyResolveDefaultModelGroup(userCache.Id, userGroup)
			switch mode {
			case service.QyDefaultModeDeny:
				// 隔离组 / 封禁组显式声明"我就是不给兜底"。文案必须给出自救方法,
				// 否则用户只知道被拒绝、不知道下一步做什么。
				abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf(
					"用户分组 %s 未配置默认模型分组,请在令牌上显式选择一个分组", userGroup))
				return
			case service.QyDefaultModePin:
				// pin 必须跑与显式令牌分组**完全相同**的两道校验,顺序也一致。
				// 两条路径判据分家的表现是"令牌指定 X 能用、默认解析出 X 却被挡"。
				//
				// 失败是 500 而不是 403:这是**配置自相矛盾**(保存时已经硬拦过
				// has_route、可选清单与倍率表),403 的文案指向"权限",会让运营从
				// 第一分钟就查错方向。资金安全上 500 也更优 —— 请求没被服务,零花费;
				// 而"解析失败就静默回落身份"会把一次配置错误变成一次按错价路由。
				//
				// 两道失败分支都回调 service.QyNotePinRejected:保存时的三道闸门是
				// **一次性**的,而模型分组页删一行倍率、矩阵页撤一次授权都能让一个
				// 已生效的 pin 在运行时变成 500,那两个页面对 pin 一无所知。
				// 没有这个回调,健康面板的 pin_rejects 恒为 0,一次整组下线在面板上
				// 完全看不见。
				if _, ok := service.QyUsableGroupsForUser(userCache.Id, userGroup)[defaultGroup]; !ok {
					common.SysError(fmt.Sprintf(
						"用户分组 %s 的默认模型分组 %s 不在其可选清单里(配置自相矛盾,请改回 inherit)",
						userGroup, defaultGroup))
					service.QyNotePinRejected(userGroup, defaultGroup, "not in usable groups")
					abortWithOpenAiMessage(c, http.StatusInternalServerError,
						fmt.Sprintf("分组配置异常:用户分组 %s 的默认模型分组不可用,请联系管理员", userGroup))
					return
				}
				if !ratio_setting.ContainsGroupRatio(defaultGroup) {
					common.SysError(fmt.Sprintf(
						"用户分组 %s 的默认模型分组 %s 不在分组倍率表里(配置自相矛盾,请改回 inherit)",
						userGroup, defaultGroup))
					service.QyNotePinRejected(userGroup, defaultGroup, "not in GroupRatio")
					abortWithOpenAiMessage(c, http.StatusInternalServerError,
						fmt.Sprintf("分组配置异常:用户分组 %s 的默认模型分组未配置倍率,请联系管理员", userGroup))
					return
				}
				userGroup = defaultGroup
			}

			// 无论走哪一档,最终的 UsingGroup 都过一次分组倍率表的检查。
			//
			// 今天这一整段绕过 ContainsGroupRatio(它只在 tokenGroup != "" 分支里),
			// 于是"空分组令牌 + 孤儿 users.group"会静默按 fail-open 的 1.0 计费。
			// 默认策略 legacy_one ⇒ QyGroupRatioMissingDenied 恒 false ⇒ 行为不变;
			// 翻到 deny 才在这里拒绝 —— 鉴权处拒绝时请求还没花钱,
			// 而计费处报错时上游 token 已经烧掉了。
			if !ratio_setting.ContainsGroupRatio(userGroup) && service.QyGroupRatioMissingDenied(userGroup) {
				abortWithOpenAiMessage(c, http.StatusForbidden,
					fmt.Sprintf("模型分组 %s 未配置倍率", userGroup))
				return
			}
		}
		common.SetContextKey(c, constant.ContextKeyUsingGroup, userGroup)

		err = SetupContextForToken(c, token, parts...)
		if err != nil {
			return
		}
		c.Next()
	}
}

func SetupContextForToken(c *gin.Context, token *model.Token, parts ...string) error {
	if token == nil {
		return fmt.Errorf("token is nil")
	}
	c.Set("id", token.UserId)
	c.Set("token_id", token.Id)
	c.Set("token_key", token.Key)
	c.Set("token_name", token.Name)
	c.Set("token_unlimited_quota", token.UnlimitedQuota)
	if !token.UnlimitedQuota {
		c.Set("token_quota", token.RemainQuota)
	}
	if token.ModelLimitsEnabled {
		c.Set("token_model_limit_enabled", true)
		c.Set("token_model_limit", token.GetModelLimitsMap())
	} else {
		c.Set("token_model_limit_enabled", false)
	}
	common.SetContextKey(c, constant.ContextKeyTokenGroup, token.Group)
	common.SetContextKey(c, constant.ContextKeyTokenCrossGroupRetry, token.CrossGroupRetry)
	if token.AutoGroups != "" {
		autoGroups, err := token.GetAutoGroups()
		if err != nil {
			common.SysError(fmt.Sprintf("failed to parse auto groups for token %d: %v", token.Id, err))
			autoGroups = []string{}
			common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, autoGroups)
		} else if len(autoGroups) > 0 {
			common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, autoGroups)
		}
	}
	if len(parts) > 1 {
		if model.IsAdmin(token.UserId) {
			c.Set("specific_channel_id", parts[1])
		} else {
			c.Header("specific_channel_version", "701e3ae1dc3f7975556d354e0675168d004891c8")
			abortWithOpenAiMessage(c, http.StatusForbidden, "普通用户不支持指定渠道")
			return fmt.Errorf("普通用户不支持指定渠道")
		}
	}
	return nil
}
