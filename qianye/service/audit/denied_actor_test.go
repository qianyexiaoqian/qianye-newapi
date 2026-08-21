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
