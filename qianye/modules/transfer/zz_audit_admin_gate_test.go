package transfer

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zz_audit_admin_gate_test.go —— 资金安全审计探针:管理端接口的功能开关口径。
//
// 本模块给管理端接口定了一条明写的口径(api_group_rules.go 与
// api_admin_limits.go 的文件头、handleAdminListRecords 的注释,三处逐字相同):
//
//	权限用 guard.FlagCore 而不是 FlagTransfer —— 划转被临时关停时管理员仍然
//	必须能查看和调整规则,恰恰是「先把门槛配好再打开功能」这个顺序最需要它。
//
// 本用例把这条口径在**全部管理端端点**上量一遍,并守住一条已修缺陷:门槛配置页
// (adminGetTransferConfig / adminPutTransferConfig)曾是仅有的两个走 FlagTransfer
// 的端点,而**支付密码的锁定策略两个键就住在那一页**(settings.go 的
// keyPayPwdMaxAttempts / keyPayPwdLockMinutes,paypass/settings.go 明写 scope 复用
// "transfer"、本模块是它们唯一的写入侧)。
//
// 支付密码不只服务划转:guard.featureOn(FlagPayPassword) 是
// transfer.enabled || lottery.enabled || mall.enabled。于是
// 「只开商城、不开站内互转」这个合法组合下:用户要验密才能
// 下单、能设支付密码,而管理员一度**读不到也改不了** pay_pwd_max_attempts /
// pay_pwd_lock_minutes —— 唯一的写入端点恒 404,两项策略静默停在默认值 5 次 / 30 分钟。
// 修复已把这两个端点改回 FlagCore,与其余端点一致;本用例断言划转关停时它们仍可达。

// auditAdminGateConfig 装配「划转关停、商城开着」这个组合(支付密码仍被商城要求,模块因此仍在线)。
func auditAdminGateConfig(t *testing.T, transferEnabled bool) {
	t.Helper()
	enabled := true
	tr := auditWideOpenGlobal()
	tr.Enabled = transferEnabled
	prev := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: tr,
		Mall:     config.Mall{Enabled: true},
		Audit:    config.Audit{Enabled: &enabled, SnapshotMaxBytes: 4096},
	})
	t.Cleanup(func() { qyConfig.Store(prev) })
}

func auditCallAdmin(t *testing.T, method, path, body string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("username", "root")
	h(c)
	return rec
}

// TestAuditAdminEndpointsStayReachableWhileTransferIsOff 量出「关停划转之后
// 管理端还剩哪些端点可达」。
func TestAuditAdminEndpointsStayReachableWhileTransferIsOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newSettingsTestDB(t)
	auditAdminGateConfig(t, false)
	invalidateSettings()

	// 前提:支付密码此刻确实是"活的" —— 商城开着,用户会被要求验密。
	require.True(t, guard.FeatureConfigured(guard.FlagPayPassword),
		"商城开着时支付密码必须是活的,否则这条用例量的不是同一件事")

	reachable := []struct {
		name   string
		method string
		path   string
		body   string
		handle gin.HandlerFunc
	}{
		{"分组规则列表", http.MethodGet, "/admin/transfer/group-rules", "", handleAdminListGroupRules},
		{"门槛分档列表", http.MethodGet, "/admin/transfer/group-limits", "", adminListGroupLimits},
		{"划转流水", http.MethodGet, "/admin/transfer/records", "", handleAdminListRecords},
	}
	for _, tc := range reachable {
		t.Run("可达/"+tc.name, func(t *testing.T) {
			rec := auditCallAdmin(t, tc.method, tc.path, tc.body, tc.handle)
			assert.NotEqual(t, http.StatusNotFound, rec.Code,
				"这一条走 FlagCore,划转关停时必须仍然可达:%s", rec.Body.String())
		})
	}

	// ── 回归:门槛配置页(以及住在它里面的支付密码策略)已改走 FlagCore,
	//    划转关停时必须与其余 7 个管理端端点一样仍然可达 ──
	t.Run("回归/门槛配置页在划转关停时仍可达", func(t *testing.T) {
		rec := auditCallAdmin(t, http.MethodGet, "/admin/transfer/config", "", adminGetTransferConfig)
		assert.Equal(t, http.StatusOK, rec.Code,
			"GET /admin/transfer/config 已走 FlagCore,划转关停时必须仍可达:%s", rec.Body.String())
	})

	t.Run("回归/支付密码锁定策略在划转关停时可配", func(t *testing.T) {
		rec := auditCallAdmin(t, http.MethodPut, "/admin/transfer/config",
			`{"pay_pwd_max_attempts":3}`, adminPutTransferConfig)
		assert.NotEqual(t, http.StatusNotFound, rec.Code,
			"pay_pwd_* 的唯一写入端点已走 FlagCore,「只开商城不开划转」的站点也必须能改锁定阈值:%s",
			rec.Body.String())
		assert.NotContains(t, rec.Body.String(), guard.CodeFeatureOff,
			"划转关停不得再把这一页挡成 qy_feature_off")

		// 佐证:这两个键确实只能从这一页写,别处没有第二个入口 —— 所以这一页可达至关重要。
		assert.Contains(t, editableKeys, keyPayPwdMaxAttempts)
		assert.Contains(t, editableKeys, keyPayPwdLockMinutes)
	})
}
