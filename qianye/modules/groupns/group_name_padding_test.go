package groupns

// group_name_padding_test.go —— 路径里带首尾空白的分组名必须被**拒绝**,而不是被
// TrimSpace 归一化后落到另一个分组上。
//
// 带空白的分组名有真实来路:渠道分组写成 "池A, 池B",model.AddAbilities 原样
// Split(group, ",") 落键 " 池B",回填把它登记成一个独立的模型分组。此前 9 个 handler
// 一律 `strings.TrimSpace(c.Param("name"))`,于是运营去删那行看起来是垃圾的 " 池B",
// handler 把参数 trim 成 "池B" —— 删掉的是正牌那一行,接口还回 200。修复后 handler
// 用 groupNamePathParam:带首尾空白直接 400,不触碰任何数据。

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 删除模型分组:针对带前导空格的 " 池B",不许落到正牌 "池B" 上。
func TestDeleteModelGroupRejectsAPaddedName(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainTestDB(t)
	enableExtAPI(t, gdb)
	syncHotAsync(t)
	useOptionSnapshot(t, `{"池A":1,"池B":1}`, `{"池B":"正牌池"}`, `[]`, `{}`)

	require.NoError(t, gdb.Create(newModelGroup("池B", 1, now())).Error)
	require.NoError(t, gdb.Create(newModelGroup(" 池B", 1, now())).Error)
	require.NoError(t, main.Create(&model.Channel{
		Id: 1, Name: "c1", Status: common.ChannelStatusEnabled, Group: "池A, 池B",
	}).Error)
	require.NoError(t, main.Create(&model.Ability{
		Group: " 池B", Model: "gpt-4", ChannelId: 1, Enabled: true,
	}).Error)

	res := callGroupHandler(t, http.MethodDelete, "/model-groups/%20%E6%B1%A0B",
		gin.Param{Key: "name", Value: " 池B"}, `{}`, adminDeleteModelGroup)

	assert.Equalf(t, http.StatusBadRequest, res.Code,
		"带首尾空白的分组名必须被拒绝,而不是被 trim 后落到另一行上: %s", res.Body.String())

	var realLeft int64
	require.NoError(t, gdb.Model(&ModelGroup{}).Where("name = ?", "池B").Count(&realLeft).Error)
	assert.EqualValuesf(t, 1, realLeft, "针对 %q 的删除不许动到 %q", " 池B", "池B")
	assert.Truef(t, ratio_setting.ContainsGroupRatio("池B"),
		"针对 %q 的删除不许把 %q 从分组倍率表里摘掉", " 池B", "池B")
}

// 删除用户分组:针对带前导空格的 " vip",不许落到正牌 "vip" 上、不许迁走它的用户。
func TestDeleteUserGroupRejectsAPaddedName(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})

	require.NoError(t, gdb.Create(newUserGroup(" vip", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("vip", 1, now())).Error)
	seedUser(t, main, 1, "vip")

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/%20vip",
		gin.Param{Key: "name", Value: " vip"},
		`{"expect_users":0,"migrate_to":"vip"}`, adminDeleteUserGroup)

	assert.Equalf(t, http.StatusBadRequest, res.Code,
		"带首尾空白的用户分组名必须被拒绝: %s", res.Body.String())

	var vipLeft int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "vip").Count(&vipLeft).Error)
	assert.EqualValuesf(t, 1, vipLeft, "针对 %q 的删除不许动到 %q 的登记行", " vip", "vip")

	var u model.User
	require.NoError(t, main.Where("id = ?", 1).Take(&u).Error)
	assert.Equalf(t, "vip", u.Group, "针对 %q 的删除不许迁走 %q 名下的用户", " vip", "vip")
}

// 影响面接口:针对带空白的名字必须 400,而不是回 200 并交出另一档人的数据
// (那会让确认弹窗核对的是另一档,运营据此按下确认)。
func TestUserGroupImpactRejectsAPaddedName(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})

	require.NoError(t, gdb.Create(newUserGroup(" vip", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("vip", 1, now())).Error)
	seedUser(t, main, 1, "vip")
	seedUser(t, main, 2, "vip")

	res := callGroupHandler(t, http.MethodGet, "/user-groups/%20vip/impact",
		gin.Param{Key: "name", Value: " vip"}, ``, adminUserGroupImpact)

	assert.Equalf(t, http.StatusBadRequest, res.Code,
		"影响面对带空白的名字必须 400,而不是交出 %q 的数据: %s", "vip", res.Body.String())
}
