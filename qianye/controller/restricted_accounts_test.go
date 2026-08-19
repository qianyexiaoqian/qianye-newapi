package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 受限账号总览端点的两条契约:
//
//	① 分档表与会话白名单**双向**盖满 —— 这是本文件存在的主要理由,见下面那条测试
//	   自己的说明;
//	② 计数与「管理员点进用户列表能看到的那批人」口径一致(status=disabled、
//	   未软删),否则页面上的数字与列表条数对不上,而这一页的全部作用就是那个数字。

// useMainDBForRestrictedAccounts 把主库句柄换成一个只承载 users 的内存库。
func useMainDBForRestrictedAccounts(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// loadRestrictedAccountsConfig 加载一份可用配置,并按需开关工单/违规两个模块。
func loadRestrictedAccountsConfig(t *testing.T, ticket, violation bool) {
	t.Helper()
	yaml := "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n"
	if ticket {
		yaml += "ticket:\n  enabled: true\n"
	}
	if violation {
		yaml += "violation:\n  enabled: true\n"
	}
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
}

func callRestrictedAccountsOverview(t *testing.T) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/admin/restricted-accounts", nil)
	AdminRestrictedAccountsOverview(c)

	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &body))
	return rec.Code, body.Data
}

// 分档表必须盖满白名单,而且不许重叠。
//
// ── 为什么这条断言值得单独存在 ──
// 这一页对管理员的承诺是「受限账号还能做的就是这些」。承诺的兑现方式是把
// middleware 里那份白名单分档展示 —— 而分档表是**另一份**清单,两份清单各自
// 漂移正是本仓反复出现的缺陷形状。漏一档的表现尤其安静:白名单里新加一条
// (比如将来放开「查看自己的账单」),页面上四句话一个字都不变,管理员照旧
// 拿着一份过期的说明去回答用户"我为什么点不了"。
//
// 反方向同样要守:一档认领不到任何路由时,页面上会渲染一行"受限账号仍可 XX",
// 而那条通道其实已经被从白名单里摘掉了 —— 比不显示更糟。
func TestRestrictedCapabilitiesCoverWhitelistExactlyOnce(t *testing.T) {
	allowed := middleware.RestrictedUserAllowedRoutes()
	require.NotEmpty(t, allowed, "白名单空了,下面的断言会变成空转")

	claimed := map[string][]string{}
	for _, entry := range allowed {
		path := restrictedRoutePath(entry)
		require.NotEmpty(t, path, "白名单条目 %q 不是 \"METHOD /path\" 形状", entry)
		owners := make([]string, 0, 1)
		for _, group := range restrictedCapabilities {
			if group.owns(path) {
				owners = append(owners, group.Key)
			}
		}
		require.Len(t, owners, 1,
			"白名单条目 %q 被 %v 认领 —— 必须恰好一档:零档 = 这一页少说了一项受限账号还能做的事,多档 = 同一条路由在页面上出现两次",
			entry, owners)
		claimed[owners[0]] = append(claimed[owners[0]], entry)
	}

	for _, group := range restrictedCapabilities {
		assert.NotEmpty(t, claimed[group.Key],
			"分档 %q 一条白名单路由都认领不到 —— 页面会渲染一行「受限账号仍可…」,而那条通道已经不在白名单里了",
			group.Key)
	}

	// 前缀匹配必须按路径段收尾。裸 strings.HasPrefix 会让这三条穿过去,
	// 而它们都不在白名单里 —— 那等于页面把不存在的能力算进承诺。
	for _, path := range []string{
		"/api/user/self-service",
		"/api/qy/tickets",
		"/api/qy/restricted-notice-preview",
	} {
		for _, group := range restrictedCapabilities {
			assert.False(t, group.owns(path),
				"%q 被 %q 档认领了,但它根本不在白名单里", path, group.Key)
		}
	}
}

// 计数口径 = 管理员在用户列表里筛 status=disabled 看到的那批人。
func TestRestrictedAccountsOverviewCountsOnlyLiveDisabledUsers(t *testing.T) {
	gdb := useMainDBForRestrictedAccounts(t)
	loadRestrictedAccountsConfig(t, true, true)

	seed := []struct {
		id      int
		name    string
		status  int
		deleted bool
	}{
		{1, "qy-live-enabled", common.UserStatusEnabled, false},
		{2, "qy-live-disabled-a", common.UserStatusDisabled, false},
		{3, "qy-live-disabled-b", common.UserStatusDisabled, false},
		// 已软删的受限账号不算:管理员点进用户列表也看不到他,
		// 两个数字对不上比没有数字更糟。
		{4, "qy-gone-disabled", common.UserStatusDisabled, true},
	}
	for _, row := range seed {
		user := &model.User{Id: row.id, Username: row.name, Status: row.status, AffCode: "a" + row.name}
		require.NoError(t, gdb.Create(user).Error)
		if row.deleted {
			require.NoError(t, gdb.Delete(user).Error)
		}
	}

	code, data := callRestrictedAccountsOverview(t)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 2, data["count"])
}

// available 跟着模块开关走,而不是跟着白名单走。
//
// 白名单是静态的:ticket.enabled=false 时那 9 条路由压根没注册,受限账号手里
// 一条申诉通道都没有,而白名单照样写着"工单放行"。只照搬白名单的页面会告诉
// 管理员"他能提工单",然后用户点进去 404。
func TestRestrictedAccountsOverviewReportsModuleAvailability(t *testing.T) {
	useMainDBForRestrictedAccounts(t)

	cases := []struct {
		name              string
		ticket, violation bool
	}{
		{"两个模块都开", true, true},
		{"工单关、违规开", false, true},
		{"两个模块都关", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loadRestrictedAccountsConfig(t, tc.ticket, tc.violation)
			code, data := callRestrictedAccountsOverview(t)
			require.Equal(t, http.StatusOK, code)

			caps, okType := data["capabilities"].([]any)
			require.True(t, okType, "capabilities 不是数组")
			got := map[string]bool{}
			for _, item := range caps {
				row, rowOK := item.(map[string]any)
				require.True(t, rowOK)
				got[row["key"].(string)] = row["available"].(bool)
				// 每一档都必须带上它认领的白名单条目原文:这一页的可核对性
				// 全靠它,少了就退化成四句需要读 Go 才能验证的断言。
				assert.NotEmpty(t, row["routes"], "分档 %v 没有下发 routes", row["key"])
			}
			assert.Equal(t, map[string]bool{
				// self / notice 不依赖任何模块:关掉之后前端连"我被限制了"
				// 都判定不出来,它们恒为 true。
				"self":      true,
				"notice":    true,
				"ticket":    tc.ticket,
				"violation": tc.violation,
			}, got)
		})
	}
}
