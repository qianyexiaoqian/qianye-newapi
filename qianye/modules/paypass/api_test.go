package paypass

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRouter 搭一个只挂本模块路由的引擎,用一个假的鉴权中间件顶替上游 UserAuth。
//
// 走真实路由而不是直接调 handler:CriticalRateLimit 与路径参数解析都挂在路由上,
// 直接调 handler 的测试看不见它们 —— 而"验密接口有没有挂关键操作限流"正是
// 一条验收项。
func newRouter(t *testing.T, userId int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := func(c *gin.Context) {
		c.Set("id", userId)
		c.Set("username", "u"+strconv.Itoa(userId))
		// role 不能省:管理端两个写动作的操作人闸门(guard.ActorMayActOn)读的
		// 就是它,而 middleware.AdminAuth() 在生产里必然与 id / username 一起
		// 写入(middleware/auth.go)。少写这一行等于让所有用例跑在一个
		// "角色未知"的操作人身上,而那一格是 fail-closed 的。
		c.Set("role", common.RoleAdminUser)
		c.Next()
	}
	g := r.Group("/api/qy")
	g.Use(auth)
	Mod{}.RegisterUserRoutes(g)
	admin := g.Group("/admin")
	admin.Use(auth)
	Mod{}.RegisterAdminRoutes(admin)
	return r
}

func do(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// 首次设置成功,再设一次必须被拒。
// 允许覆盖等于"不知道旧密码也能改密码",支付密码就失去了全部意义。
func TestSetIsFirstTimeOnly(t *testing.T) {
	gdb := newTestDB(t)
	r := newRouter(t, 7300)

	rec := do(r, http.MethodPost, "/api/qy/pay-password", `{"password":"pay-pwd-8891"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, rowOf(t, gdb, 7300).isSet())

	rec = do(r, http.MethodPost, "/api/qy/pay-password", `{"password":"another-one-9"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), errPayPwdAlreadySet.Code)

	// 原密码必须还是第一次那个。
	require.NoError(t, verify(context.Background(), 7300, "pay-pwd-8891"))
}

func TestSetRejectsWeakPassword(t *testing.T) {
	newTestDB(t)
	r := newRouter(t, 7310)

	rec := do(r, http.MethodPost, "/api/qy/pay-password", `{"password":"123456"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), errPayPwdWeak.Code)
}

// 改密必须先验旧密码,且验错要计入锁定策略。
//
// 这条路径不走同一套 verify 的话,它就成了一个不受锁定约束的试密口 ——
// 攻击者根本不用去打划转接口。
func TestChangeVerifiesOldPasswordAndCountsFailures(t *testing.T) {
	gdb := newTestDB(t)
	r := newRouter(t, 7320)
	setPassword(t, gdb, 7320, goodPassword)

	rec := do(r, http.MethodPut, "/api/qy/pay-password",
		`{"old_password":"wrong-old-1","password":"brand-new-77"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), errPayPwdWrong.Code)
	assert.Equal(t, 1, rowOf(t, gdb, 7320).FailCount)

	rec = do(r, http.MethodPut, "/api/qy/pay-password",
		`{"old_password":"`+goodPassword+`","password":"brand-new-77"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.NoError(t, verify(context.Background(), 7320, "brand-new-77"))
	assert.ErrorIs(t, verify(context.Background(), 7320, goodPassword), errPayPwdWrong)
	// 改密成功要顺带清空错误计数(上面那次失败 + 这次失败,期望只剩最后这一次)。
	assert.Equal(t, 1, rowOf(t, gdb, 7320).FailCount)
}

// 未绑定邮箱时只提示去绑定,**绝不在这条路径上代为绑定**。
//
// 裁决 1 点名的红线:允许在找回流程里填一个新邮箱,等于给了一条绕过原邮箱的
// 改绑路径 —— 拿到会话的攻击者把找回邮箱换成自己的,支付密码立刻形同虚设。
// 断言里回读了 users.email:接口不仅要拒绝,还必须一个字节都没改。
func TestRecoverRefusesAndNeverBindsEmail(t *testing.T) {
	newTestDB(t)
	mainDB := useMainDB(t)
	seedUser(t, mainDB, 7330, "no-mail", "")
	r := newRouter(t, 7330)

	rec := do(r, http.MethodPost, "/api/qy/pay-password/recover/code",
		`{"email":"attacker@example.com"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), errPayPwdEmailUnbound.Code)

	rec = do(r, http.MethodPost, "/api/qy/pay-password/recover/reset",
		`{"email":"attacker@example.com","code":"whatever","password":"brand-new-77"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), errPayPwdEmailUnbound.Code)

	var email string
	require.NoError(t, mainDB.Raw("SELECT email FROM users WHERE id = ?", 7330).Scan(&email).Error)
	assert.Empty(t, email, "找回流程改动了用户的绑定邮箱 —— 这正是被裁决明确禁止的绕过路径")
}

// 邮箱验证码换新密码:验证码正确才放行,并且用完即失效。
func TestRecoverResetConsumesCodeOnce(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	seedUser(t, mainDB, 7340, "has-mail", "owner@example.com")
	setPassword(t, gdb, 7340, goodPassword)
	// 顺带把账号锁上:找回必须能把用户从锁定里救出来,否则这条路径没有意义。
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", 7340).
		Updates(map[string]any{"fail_count": 9, "locked_until": common.GetTimestamp() + 3600}).Error)
	r := newRouter(t, 7340)

	rec := do(r, http.MethodPost, "/api/qy/pay-password/recover/reset",
		`{"code":"bad-code","password":"brand-new-77"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), errPayPwdCodeInvalid.Code)

	common.RegisterVerificationCodeWithKey(recoverKey(7340, "owner@example.com"),
		"the-code", recoverPurpose)
	rec = do(r, http.MethodPost, "/api/qy/pay-password/recover/reset",
		`{"code":"the-code","password":"brand-new-77"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	row := rowOf(t, gdb, 7340)
	assert.Zero(t, row.FailCount)
	assert.Zero(t, row.LockedUntil)
	require.NoError(t, verify(context.Background(), 7340, "brand-new-77"))

	// 同一个验证码不能用第二次。
	rec = do(r, http.MethodPost, "/api/qy/pay-password/recover/reset",
		`{"code":"the-code","password":"third-password-1"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), errPayPwdCodeInvalid.Code)
}

// 写接口必须挂 middleware.CriticalRateLimit()。
//
// 把限流上限压到 1,第二次请求就该拿 429。这条断言的价值在于它测的是**路由**:
// 有人把 middleware.CriticalRateLimit() 从 module.go 的某一行删掉时,
// 所有业务测试仍然全绿,只有这一条会红。
func TestCriticalRateLimitGuardsWriteEndpoints(t *testing.T) {
	newTestDB(t)
	prevEnable, prevNum, prevDur := common.CriticalRateLimitEnable,
		common.CriticalRateLimitNum, common.CriticalRateLimitDuration
	// 强制走内存限流器:开发机上若配了 REDIS_CONN_STRING,rateLimitFactory 会选
	// Redis 分支,而测试进程里没有连接,限流会以 500 失败而不是 429 —— 那样这条
	// 断言测的就不是"有没有挂限流"了。
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	common.CriticalRateLimitEnable = true
	common.CriticalRateLimitNum = 1
	common.CriticalRateLimitDuration = 600
	t.Cleanup(func() {
		common.CriticalRateLimitEnable = prevEnable
		common.CriticalRateLimitNum = prevNum
		common.CriticalRateLimitDuration = prevDur
		common.RedisEnabled = prevRedis
	})
	// 路由必须在改完开关之后再建:限流中间件是在注册路由时按开关选定的。
	r := newRouter(t, 7350)

	first := do(r, http.MethodPost, "/api/qy/pay-password", `{"password":"pay-pwd-8891"}`)
	require.NotEqual(t, http.StatusTooManyRequests, first.Code)
	second := do(r, http.MethodPost, "/api/qy/pay-password", `{"password":"pay-pwd-8891"}`)
	assert.Equal(t, http.StatusTooManyRequests, second.Code,
		"POST /pay-password 没有挂 CriticalRateLimit —— 它是一个可以被脚本刷的写接口")
}

// 状态接口只暴露状态,绝不暴露任何与哈希有关的东西。
func TestStatusNeverLeaksHash(t *testing.T) {
	gdb := newTestDB(t)
	setPassword(t, gdb, 7360, goodPassword)
	r := newRouter(t, 7360)

	rec := do(r, http.MethodGet, "/api/qy/pay-password", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"is_set":true`)
	assert.NotContains(t, body, "$2a$")
	assert.NotContains(t, body, rowOf(t, gdb, 7360).Hash)
}
