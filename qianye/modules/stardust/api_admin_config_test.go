package stardust

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_admin_config_test.go —— 五段下发、稀疏保存、失败留痕。

func TestAdminConfigGetFiveSectionsAndPutSparsePatch(t *testing.T) {
	env := newAPIEnv(t, func(s *config.Stardust) {
		s.SettleDelayMinutes = 30
		s.SettleIntervalSeconds = 300
		s.MaxManualAdjust = 5000
	})
	withCompliance(t, false)
	r := adminRouter(7, common.RoleAdminUser)

	data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/config", ""))
	// 局部变量不叫 effective:那会遮蔽包级的 effective(),下面还要拿它核对进程内快照。
	eff, ok := data["effective"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 10_000, eff[keyConsumeBps])
	assert.EqualValues(t, 0, eff[keyInviteTopupBps], "合规未确认时邀请类按 0 下发")
	keys, ok := data["editable_keys"].([]any)
	require.True(t, ok)
	assert.Len(t, keys, len(editableKeys))
	bounds := data["bounds"].(map[string]any)
	consume := bounds[keyConsumeBps].(map[string]any)
	assert.EqualValues(t, 0, consume["lo"])
	assert.EqualValues(t, maxBps, consume["hi"])
	_, hasName := bounds[keyName]
	assert.False(t, hasName, "name 是字符串,没有数值区间")
	overrides, ok := data["overrides"].(map[string]any)
	require.True(t, ok, "没有覆盖时也是对象而不是 null")
	assert.Empty(t, overrides)
	yaml := data["yaml_readonly"].(map[string]any)
	assert.EqualValues(t, QuotaPerUnit(), yaml["quota_per_unit"])
	assert.EqualValues(t, 30, yaml["settle_delay_minutes"])
	assert.EqualValues(t, 300, yaml["settle_interval_seconds"])
	assert.EqualValues(t, 5000, yaml["max_manual_adjust"])
	assert.Equal(t, true, yaml["exclude_subscription_consume"])
	assert.Equal(t, true, yaml["exclude_manual_topup"])
	assert.Equal(t, false, yaml["compliance_confirmed"])

	// 稀疏保存:数字、带空白的字符串、字符串形式的布尔都收。
	data = dataOf(t, call(t, r, http.MethodPut, "/api/qy/admin/stardust/config",
		`{"consume_bps": 2500, "name": " 星尘 ", "show_entry": "0"}`))
	eff = data["effective"].(map[string]any)
	assert.EqualValues(t, 2500, eff[keyConsumeBps])
	assert.Equal(t, "星尘", eff[keyName])
	assert.EqualValues(t, 0, eff[keyShowEntry])
	assert.Equal(t, 2500, effective().ConsumeBps, "保存后本进程快照必须立即失效")
	assert.Equal(t, "星尘", UnitName())

	logs := auditRowsOf(t, env.ext, configAuditAction)
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, 7, logs[0].ActorUserId)
	assert.Contains(t, logs[0].BeforeSnap, `"consume_bps":10000`)
	assert.Contains(t, logs[0].AfterSnap, `"consume_bps":2500`)

	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/config", ""))
	overrides = data["overrides"].(map[string]any)
	assert.Equal(t, "2500", overrides[keyConsumeBps])
	assert.Equal(t, "0", overrides[keyShowEntry], "布尔归一成 0/1 落库")
}

func TestAdminConfigPutRejectionsLeaveAnAuditTrail(t *testing.T) {
	env := newAPIEnv(t, nil)
	withCompliance(t, false)
	r := adminRouter(7, common.RoleAdminUser)

	cases := []struct {
		name string
		body string
		code string
	}{
		{"合规未确认时邀请类正值", `{"invite_topup_bps": 5}`, codeComplianceRequired},
		{"白名单之外的键", `{"max_manual_adjust": 1}`, codeBadSetting},
		{"比例越界", `{"consume_bps": 10000001}`, codeBadSetting},
		{"请求体不是对象", `[1,2]`, codeBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, r, http.MethodPut, "/api/qy/admin/stardust/config", tc.body)
			assert.Equal(t, tc.code, codeOf(t, rec, http.StatusBadRequest))
		})
	}
	assert.Equal(t, 10_000, effective().ConsumeBps, "被拒的保存一个键都不许生效")
	logs := auditRowsOf(t, env.ext, configAuditAction)
	okCount, failCount := countResults(logs)
	assert.Zero(t, okCount)
	assert.Equal(t, len(cases), failCount, "每一次被拒的保存都要留痕")

	// 空 patch 是无操作:不写库、不写审计。
	data := dataOf(t, call(t, r, http.MethodPut, "/api/qy/admin/stardust/config", `{}`))
	assert.EqualValues(t, 10_000, data["effective"].(map[string]any)[keyConsumeBps])
	assert.Len(t, auditRowsOf(t, env.ext, configAuditAction), len(cases))
}
