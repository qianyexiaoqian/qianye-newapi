package commission

// secret_override_test.go —— 下线标识的 HMAC 密钥不得随管理端配置接口下发。
//
// 被守的缺陷(审计 consistency 视角 #9):GET /api/qy/admin/commission/config
// 把 qy_settings 里 scope=commission 的**全部**覆盖原样回显,其中包含
// invitee_ref_salt —— inviteeRef 的 HMAC 密钥。settings.go 明写它
// "部署一次、永不轮换"(轮换会让全部历史 ref 失效),所以它一旦出现在一个
// 会被截图、被前端缓存、被日志代理与错误上报记录的 JSON 里,就再也收不回来。

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminGetConfigNeverLeaksTheRefSalt(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)

	const salt = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&qymodel.Setting{
		Scope: settingScope, K: keyRefSalt, V: salt, UpdatedAt: now,
	}).Error)
	// 一条普通覆盖,证明这条测试不是靠"整个 overrides 段为空"蒙对的。
	require.NoError(t, gdb.Create(&qymodel.Setting{
		Scope: settingScope, K: keyConsumeRatePercent, V: "8.25", UpdatedAt: now,
	}).Error)

	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/config", "", adminGetConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := rec.Body.String()
	assert.NotContains(t, body, salt, "密钥本身绝不能出现在响应里")
	assert.False(t, strings.Contains(body, keyRefSalt),
		"连键名都不该出现:它会告诉读者去哪里找这个值")
	assert.Contains(t, body, `"consume_rate_percent":"8.25"`,
		"正常的运营覆盖必须照常回显 —— 否则这条断言是靠把整段抹掉蒙对的")

	// 摘掉的只是响应,库里那一行必须原样还在:密钥一旦被"顺手清理"就等于
	// 让全部历史 inviteeRef 失效。
	var row qymodel.Setting
	require.NoError(t, gdb.Where("scope = ? AND k = ?", settingScope, keyRefSalt).
		Take(&row).Error)
	assert.Equal(t, salt, row.V)
}

func TestRedactSecretOverridesDropsOnlySecrets(t *testing.T) {
	overrides := map[string]string{
		keyRefSalt:            "deadbeef",
		keyConsumeRatePercent: "5",
		keyDailyCapQuota:      "20000",
	}
	redactSecretOverrides(overrides)
	assert.NotContains(t, overrides, keyRefSalt)
	assert.Equal(t, "5", overrides[keyConsumeRatePercent])
	assert.Equal(t, "20000", overrides[keyDailyCapQuota])
}
