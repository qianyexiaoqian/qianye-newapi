package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// root_action_test.go —— RootActionGate 的行为契约。
//
// qianye/root_action_guard_test.go 守的是"闸门接在了哪几条路由上",它读 AST,
// 完全不执行代码 —— 判据本身写反了(`<=` 写成 `>=`、忘了 Abort、code 拼错)
// 它一个都发现不了。那三件事各自的后果分别是:全站放行、被拒之后 handler
// 照跑、前端塌回通用 403。所以判据要有一份真的跑起来的用例。

func setupRootActionTest(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	// 用户名回查会先走 Redis;测试里没有 Redis 客户端,不关掉就是空指针 panic。
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousType)
		common.SetLogDatabaseType(previousLogType)
		common.RedisEnabled = previousRedis
	})
}

// rootActionTestRequest 跑一次挂了闸门的路由,返回响应与 handler 有没有被执行。
func rootActionTestRequest(t *testing.T, role int) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	handlerRan := false
	engine.POST("/gated/:id", func(c *gin.Context) {
		c.Set("id", 7)
		c.Set("username", "qy-gate-probe")
		c.Set("role", role)
		c.Next()
	}, RootActionGate(RootActionRedemptionCreate), func(c *gin.Context) {
		handlerRan = true
		// 闸门放行之后 handler 必须还能自己决定要不要埋点,所以这里不该被
		// 提前标记过。
		assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyAuditLogged),
			"放行的请求不该被闸门标记成已审计,否则 handler 自己的埋点会被兜底逻辑当成重复而跳过")
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/gated/42", nil)
	engine.ServeHTTP(recorder, req)
	return recorder, handlerRan
}

func TestRootActionGateAdmitsOnlyRoot(t *testing.T) {
	cases := []struct {
		name       string
		role       int
		wantStatus int
		wantRan    bool
	}{
		{"普通用户", common.RoleCommonUser, http.StatusForbidden, false},
		// role=10 是这一整轮改造的主角:他能到达这条路由(AdminAuth 放行),
		// 必须恰好在这一步被挡住,而且 handler 一行都不许跑。
		{"管理员", common.RoleAdminUser, http.StatusForbidden, false},
		{"超级管理员", common.RoleRootUser, http.StatusOK, true},
		// 高于 root 的角色不存在,但判据写的是 >=,顺手把它钉住:
		// 写成 == 的话某天引入更高档就会把 root 之上的人一起拒掉。
		{"高于超管的角色", common.RoleRootUser + 1, http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupRootActionTest(t)
			recorder, ran := rootActionTestRequest(t, tc.role)
			assert.Equal(t, tc.wantStatus, recorder.Code)
			assert.Equal(t, tc.wantRan, ran,
				"被拒的请求绝不能继续执行 handler —— 忘了 Abort 时状态码看起来仍然是对的")
		})
	}
}

func TestRootActionGateDenialCarriesItsOwnCodeAndAction(t *testing.T) {
	setupRootActionTest(t)
	recorder, _ := rootActionTestRequest(t, common.RoleAdminUser)
	require.Equal(t, http.StatusForbidden, recorder.Code)

	var body struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Action  string `json:"action"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
	assert.False(t, body.Success)
	// code 是前端把这一档与"整条路由都到不了"区分开的**唯一**依据
	// (web/src/features/qy/lib/api.ts 的 QY_ERROR_CODE_I18N)。
	assert.Equal(t, RootActionRequiredCode, body.Code)
	assert.NotEqual(t, "AUTH_INSUFFICIENT_PRIVILEGE", body.Code)
	// action 让界面能说出"哪一个按钮不行"。
	assert.Equal(t, string(RootActionRedemptionCreate), body.Action)
	assert.NotEmpty(t, body.Message)
}

func TestRootActionGateDenialWritesAudit(t *testing.T) {
	setupRootActionTest(t)
	recorder, _ := rootActionTestRequest(t, common.RoleAdminUser)
	require.Equal(t, http.StatusForbidden, recorder.Code)

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Find(&logs).Error)
	require.Len(t, logs, 1, "被拒的越权尝试必须留下恰好一条审计")
	entry := logs[0]
	assert.Equal(t, model.LogTypeManage, entry.Type)
	assert.Equal(t, 7, entry.UserId)
	other, err := common.StrToMap(entry.Other)
	require.NoError(t, err)

	op, ok := other["op"].(map[string]any)
	require.True(t, ok, "审计缺少 op 段,前端无从本地化渲染")
	assert.Equal(t, "authz.root_action_denied", op["action"])
	params, ok := op["params"].(map[string]any)
	require.True(t, ok)
	// 只记"被拒了"不够:同一个管理员可能在四个不同的档位上各撞一次,
	// 分不出是哪一个动作就查不出他到底在试什么。
	assert.Equal(t, string(RootActionRedemptionCreate), params["attempted_action"])

	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "qy-gate-probe", adminInfo["admin_username"])
	auditInfo, ok := other["audit_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "/gated/:id", auditInfo["route"])
	assert.Equal(t, false, auditInfo["success"])
}

// TestRootActionGateSuppressesGenericFallbackAudit 守"被拒之后只留一条,
// 而且留的是精确那条"。
//
// authHelper 在 c.Next() 之后会跑 finishAdminAudit 兜底;闸门不把
// ContextKeyAuditLogged 打上的话,同一次被拒会再多出一条 action="generic"
// 的 `POST /route` —— 两条记录说的是同一件事,而信息量更少的那条会在
// 审计列表里与真正的那条并排出现。
func TestRootActionGateSuppressesGenericFallbackAudit(t *testing.T) {
	setupRootActionTest(t)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	var marked bool
	engine.POST("/gated", func(c *gin.Context) {
		c.Set("id", 7)
		c.Set("role", common.RoleAdminUser)
		writer := beginAdminAudit(c)
		c.Next()
		marked = common.GetContextKeyBool(c, constant.ContextKeyAuditLogged)
		finishAdminAudit(c, writer)
	}, RootActionGate(RootActionRedemptionCreate), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/gated", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.True(t, marked, "闸门必须标记 ContextKeyAuditLogged,否则兜底会再写一条 generic")

	var count int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&count).Error)
	assert.EqualValues(t, 1, count, "一次被拒只该留一条审计")
}
