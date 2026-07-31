package usergroup

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// callPut 打一次真实的 PUT handler。
//
// 走 handler 而不是直接调 validateDefaultGroup:校验函数自己写得再对,只要
// handler 忘了调它,非法分组照样存得进去。本项目已经反复出现过这个形状的缺陷
// (纯函数层做对了、调度层没接上),所以校验类的测试一律从 HTTP 入口进。
func callPut(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/qy/admin/user-group/config",
		bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 7)
	c.Set("username", "admin7")

	adminPutConfig(c)
	return rec
}

func storedDefaultGroup(t *testing.T, gdb *gorm.DB) (string, bool) {
	t.Helper()
	var rows []qymodel.Setting
	require.NoError(t, gdb.Where("scope = ? AND k = ?", settingScope, keyDefaultGroup).
		Find(&rows).Error)
	if len(rows) == 0 {
		return "", false
	}
	return rows[0].V, true
}

// 目标分组不存在时必须拒绝保存。
//
// 这是本模块最关键的一道闸门:分组不是实体表,写进去一个拼错的名字不会有任何
// 报错,后果是此后所有新用户匹配不到 abilities、一个模型都调不通。
func TestAdminPutConfig_RejectsUnknownGroup(t *testing.T) {
	extDB := newExtDB(t)
	newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8}`)

	rec := callPut(t, `{"default_group":"vlp"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	_, exists := storedDefaultGroup(t, extDB)
	assert.False(t, exists, "非法分组绝不能落库")
	assert.Equal(t, "", currentDefaultGroup())
}

// auto 是令牌的自动分组,不是用户分组,即使它在倍率表里也必须拒绝。
func TestAdminPutConfig_RejectsAutoGroup(t *testing.T) {
	extDB := newExtDB(t)
	newMainDB(t)
	useGroupRatio(t, `{"default":1,"auto":1}`)

	rec := callPut(t, `{"default_group":"auto"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	_, exists := storedDefaultGroup(t, extDB)
	assert.False(t, exists)
}

// 合法分组保存后必须立刻对新注册生效,不能等缓存过期。
func TestAdminPutConfig_AcceptsExistingGroupAndTakesEffect(t *testing.T) {
	extDB := newExtDB(t)
	mainDB := newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8}`)
	installHook(t)

	rec := callPut(t, `{"default_group":" vip "}`)
	require.Equal(t, http.StatusOK, rec.Code)

	stored, exists := storedDefaultGroup(t, extDB)
	require.True(t, exists)
	assert.Equal(t, "vip", stored, "两端空白必须被裁掉,否则存进去的是一个不存在的分组")

	assert.Equal(t, "vip", insertUser(t, mainDB, "heidi", ""))
}

// 空串是合法值,表示取消配置、回到上游默认行为。
func TestAdminPutConfig_EmptyValueClearsConfig(t *testing.T) {
	extDB := newExtDB(t)
	mainDB := newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8}`)
	installHook(t)

	seedDefaultGroup(t, extDB, "vip")
	require.Equal(t, "vip", insertUser(t, mainDB, "ivan", ""))

	rec := callPut(t, `{"default_group":""}`)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, upstreamDefaultGroup, insertUser(t, mainDB, "judy", ""))
}

// 缺字段与非法 JSON 都必须 400,而不是被当成「清空配置」。
func TestAdminPutConfig_RejectsMalformedBody(t *testing.T) {
	extDB := newExtDB(t)
	newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8}`)
	seedDefaultGroup(t, extDB, "vip")

	for _, body := range []string{`{}`, `not-json`} {
		rec := callPut(t, body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", body)
	}
	stored, exists := storedDefaultGroup(t, extDB)
	require.True(t, exists)
	assert.Equal(t, "vip", stored, "格式错误的请求不能改动既有配置")
}

// 下拉选项必须来自真实存在的分组,并且如实标出「该分组下有没有可用渠道」。
//
// has_channels 是运营唯一能提前看见「选了它新用户就用不了模型」的信号。
func TestAdminGetConfig_ListsRealGroupsWithChannelSignal(t *testing.T) {
	newExtDB(t)
	mainDB := newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8,"ghost":1}`)

	require.NoError(t, mainDB.Create(&model.Ability{
		Group: "vip", Model: "gpt-4o", ChannelId: 1, Enabled: true,
	}).Error)
	require.NoError(t, mainDB.Create(&model.Ability{
		Group: "default", Model: "gpt-4o", ChannelId: 2, Enabled: false,
	}).Error)

	options, probeOK := listGroupOptions()

	require.True(t, probeOK)
	byName := map[string]groupOption{}
	for _, o := range options {
		byName[o.Name] = o
	}
	require.Len(t, options, 3)
	assert.True(t, byName["vip"].HasChannels)
	assert.False(t, byName["default"].HasChannels, "禁用的 ability 不算可用渠道")
	assert.False(t, byName["ghost"].HasChannels)
	assert.InDelta(t, 0.8, byName["vip"].Ratio, 1e-9)
}

// 审计必写:这一项决定此后所有新用户能不能用模型。
func TestAdminPutConfig_WritesAudit(t *testing.T) {
	extDB := newExtDB(t)
	newMainDB(t)
	useGroupRatio(t, `{"default":1,"vip":0.8}`)

	// audit.Write 受 audit.enabled 开关控制,测试里显式打开。
	auditOn := true
	cfg := *qyConfig.Load()
	cfg.Audit.Enabled = &auditOn
	qyConfig.Store(&cfg)

	require.Equal(t, http.StatusOK, callPut(t, `{"default_group":"vip"}`).Code)

	var logs []qymodel.AuditLog
	require.NoError(t, extDB.Where("action = ?", "usergroup.default.update").Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Equal(t, 7, logs[0].ActorUserId)
	assert.Equal(t, "vip", logs[0].AfterSnap)
	assert.NotZero(t, logs[0].CreatedAt)
}
