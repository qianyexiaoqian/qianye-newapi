package audit

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// denied_actor_test.go —— 「被鉴权挡掉的那个人是谁」必须记得下。
//
// 中间件挂在 UserAuth/AdminAuth **之前**,理由写在 qianye/router.go 上:
// 「c.Next() 之后一样读得到认证写进 context 的身份,却额外记下被认证挡掉的
// 401/403 —— 后者正是越权探测的形状」。这句话在 403 分支上原先不成立:
// authHelper 在 role 不足时直接 AbortWithStatusJSON,setDashboardAuthContext
// 从未执行,于是 c.GetInt("id")/("role") 恒为 0 —— 一个**已登录**的普通账号
// 挨个戳管理端接口留下的 403 行,与真匿名扫描的 401 行在
// actor_user_id / actor_role / actor_type / actor_name / auth_method
// 五列上逐列相同(全空),只剩一个可伪造的 IP;而 actorType 又会把这批行
// 读成「匿名探测」,方向性地误导仲裁人。

func TestMiddleware_KeepsTheIdentityOfSomeoneDeniedByRole(t *testing.T) {
	row := runThroughMiddleware(t, http.MethodPost, "/api/qy/admin/commission/settle",
		"/api/qy/admin/commission/settle", strings.NewReader(`{"user_id":9}`), "application/json",
		func(c *gin.Context) {
			// 凭据验过了(所以 DeniedActor* 有值),但 role 不足 —— 鉴权链
			// 直接 Abort,"id"/"role" 这两个键一个都没写。
			c.Set(common.DeniedActorIdKey, 4242)
			c.Set(common.DeniedActorNameKey, "qy-probe")
			c.Set(common.DeniedActorRoleKey, common.RoleCommonUser)
			c.Set(common.DeniedActorAccessTokenKey, true)
			c.AbortWithStatus(http.StatusForbidden)
		})

	require.NotNil(t, row)
	assert.Equal(t, 403, row.StatusCode)
	assert.False(t, row.Success)
	assert.Equal(t, 4242, row.ActorUserId, "一个已登录账号的越权探测不能记成匿名")
	assert.Equal(t, "qy-probe", row.ActorName)
	assert.Equal(t, common.RoleCommonUser, row.ActorRole)
	assert.Equal(t, qymodel.AuthMethodAccessToken, row.AuthMethod)
	assert.NotEmpty(t, row.ActorType, "身份已知就不能再留空 —— 留空的含义是「不知道是谁」")
}

// 反向:真匿名请求仍然必须留空。两者不能被这条回落抹平成同一种。
func TestMiddleware_StillLeavesTrulyAnonymousRequestsBlank(t *testing.T) {
	row := runThroughMiddleware(t, http.MethodPost, "/api/qy/admin/commission/settle",
		"/api/qy/admin/commission/settle", strings.NewReader(`{}`), "application/json",
		func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })

	require.NotNil(t, row)
	assert.Zero(t, row.ActorUserId)
	assert.Empty(t, row.ActorType)
	assert.Empty(t, row.AuthMethod)
}

// 鉴权放行时,"id"/"role" 优先于回落键 —— 回落只在身份取不到时才该起作用。
func TestMiddleware_PrefersTheAuthenticatedIdentityOverTheFallback(t *testing.T) {
	row := runThroughMiddleware(t, http.MethodPost, "/api/qy/admin/commission/settle",
		"/api/qy/admin/commission/settle", strings.NewReader(`{}`), "application/json",
		func(c *gin.Context) {
			c.Set(common.DeniedActorIdKey, 4242)
			c.Set(common.DeniedActorNameKey, "qy-probe")
			c.Set(common.DeniedActorRoleKey, common.RoleCommonUser)
			c.Set("id", 7)
			c.Set("username", "root")
			c.Set("role", common.RoleRootUser)
			c.JSON(http.StatusOK, gin.H{})
		})

	require.NotNil(t, row)
	assert.Equal(t, 7, row.ActorUserId)
	assert.Equal(t, "root", row.ActorName)
	assert.Equal(t, common.RoleRootUser, row.ActorRole)
}

// 被挡下来的**管理端读取**也要留痕。
//
// 「挨个戳一遍管理端接口」里有一多半是 GET,原先一条都不记(shouldRecord 只收
// 写方法 + 三条敏感读白名单),于是最该被抓的那个形状在台账里只剩下写接口那几行。
// 成功的读取仍然不记 —— 列表/详情每天几千次,记下来会把台账稀释到无法扫读。
func TestMiddleware_RecordsAdminReadsOnlyWhenTheyAreDenied(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		route      string
		status     int
		wantRecord bool
	}{
		{"管理端读取被 403 挡下:记", "/api/qy/admin/overdraft", "/api/qy/admin/overdraft", http.StatusForbidden, true},
		{"管理端读取被 401 挡下:记", "/api/qy/admin/leases", "/api/qy/admin/leases", http.StatusUnauthorized, true},
		{"管理端读取成功:不记", "/api/qy/admin/overdraft", "/api/qy/admin/overdraft", http.StatusOK, false},
		{"管理端读取 404:不记(业务错误不是探测信号)", "/api/qy/admin/overdraft", "/api/qy/admin/overdraft", http.StatusNotFound, false},
		{"用户端读取被挡:不记(受限账号的浏览器会持续轮询,会把信号淹掉)",
			"/api/qy/violations", "/api/qy/violations", http.StatusForbidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := runThroughMiddleware(t, http.MethodGet, tc.route, tc.path, nil, "",
				func(c *gin.Context) { c.AbortWithStatus(tc.status) })
			if tc.wantRecord {
				require.NotNil(t, row, "被挡下的管理端读取必须留痕")
				assert.Equal(t, tc.status, row.StatusCode)
				return
			}
			assert.Nil(t, row)
		})
	}
}

// actor_type 必须只由**身份**决定,不能由路径决定。
//
// 曾经的判据是 `role >= RoleAdminUser || strings.HasPrefix(path, "/api/qy/admin/")`。
// 那个 || 的右半边在鉴权通过的行上恒为冗余(/api/qy/admin 整组挂着 AdminAuth),
// 它唯一能改变结果的场合恰恰是**被拒的越权探测**:一个 role=1 的普通账号挨个戳
// 管理端接口留下的 403,在 actor_type 上与真管理员的操作完全同色。
//
// 实测线上台账:actor_type='admin' 的行里有 810 行 actor_role=1,且这 810 行
// 100% 是 401/403。仲裁人按 actor_type='admin' 过滤会把它们当成管理员操作读,
// 按 'user' 过滤则整体漏掉;而筛选清单里没有 actor_role,补偿不了。
func TestMiddleware_ClassifiesActorByRoleNotByPath(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		role     int
		wantType string
	}{
		{
			name:     "普通用户戳管理端被拒:是 user,不是 admin",
			path:     "/api/qy/admin/violation/rules/:id",
			role:     common.RoleCommonUser,
			wantType: qymodel.ActorUser,
		},
		{
			name:     "真管理员走管理端:admin",
			path:     "/api/qy/admin/violation/rules/:id",
			role:     common.RoleAdminUser,
			wantType: qymodel.ActorAdmin,
		},
		{
			name:     "管理员走用户面:仍然是 admin(身份决定,不是路径)",
			path:     "/api/qy/withdraw/payees",
			role:     common.RoleAdminUser,
			wantType: qymodel.ActorAdmin,
		},
		{
			name:     "普通用户走用户面:user",
			path:     "/api/qy/withdraw/payees",
			role:     common.RoleCommonUser,
			wantType: qymodel.ActorUser,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := runThroughMiddleware(t, http.MethodPost, tc.path, tc.path,
				strings.NewReader(`{}`), "application/json",
				func(c *gin.Context) {
					c.Set(common.DeniedActorIdKey, 7007)
					c.Set(common.DeniedActorNameKey, "qy-classify-probe")
					c.Set(common.DeniedActorRoleKey, tc.role)
					c.Set(common.DeniedActorAccessTokenKey, true)
					c.AbortWithStatus(http.StatusForbidden)
				})
			require.NotNil(t, row)
			assert.Equal(t, tc.role, row.ActorRole)
			assert.Equal(t, tc.wantType, row.ActorType,
				"actor_type 必须只看 role。按路径判会让 role=%d 的越权探测"+
					"在台账上与真管理员的操作同色", tc.role)
		})
	}
}

// 成功的 check-update 必须留痕。
//
// 这条路由被提到超级管理员的全部理由是「它是一次站点行为,不是一次数据读取」——
// 它替本站向 github.com 开一次出站连接。可是提档之后台账只留下**被拒**的越权
// 尝试(RootActionGate 拒绝时写审计),成功的那一次一行都不写:实测该路径下
// 401 有 4 行、403 有 28 行、200 有 **0** 行。而离线/内网部署事后最想追问的
// 恰恰是"是谁、在什么时候替这台机器连了 github.com";gin 的访问日志有时间和
// IP,但没有任何身份列,答不出"是谁"。
func TestMiddleware_RecordsSuccessfulUpdateChecks(t *testing.T) {
	const route = "/api/qy/admin/version/check-update"
	row := runThroughMiddleware(t, http.MethodGet, route, route, nil, "",
		func(c *gin.Context) {
			c.Set("id", 1)
			c.Set("username", "root")
			c.Set("role", common.RoleRootUser)
			c.Status(http.StatusOK)
		})
	require.NotNil(t, row,
		"成功的检查更新必须落一行 —— 只记被拒的尝试等于答不出『是谁替这台机器连了 github.com』")
	assert.Equal(t, 200, row.StatusCode)
	assert.True(t, row.Success)
	assert.Equal(t, 1, row.ActorUserId)
	assert.Equal(t, qymodel.ActorAdmin, row.ActorType)

	// 反向:普通的管理端 GET 仍然不记,否则台账会被列表与详情稀释到无法扫读。
	other := runThroughMiddleware(t, http.MethodGet,
		"/api/qy/admin/violation/rules", "/api/qy/admin/violation/rules", nil, "",
		func(c *gin.Context) {
			c.Set("id", 1)
			c.Set("role", common.RoleRootUser)
			c.Status(http.StatusOK)
		})
	assert.Nil(t, other, "普通管理端 GET 成功时不记 —— 每天几千次,记下来只会把台账稀释掉")
}
