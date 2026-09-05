package groupns

// usergroup_options_carry_test.go —— 用户分组改名 / 删除必须带走(或清掉)**每一处
// 以用户分组名为键**的 options,不止交叉倍率与充值折扣那两处。
//
// D-08 之后又多了三处同样以 users.group 为键的配置,住在上游 setting/:
//
//	ModelRequestRateLimitGroup   按用户分组的 RPM 上限(安全闸门)
//	ModelRequestConcurrencyGroup 按用户分组的在途并发上限(安全闸门)
//	TokenDefaultGroups           令牌创建界面按用户分组预选的模型分组
//
// 漏掉它们的两个方向都真实伤人:改名后这一档人的限流/并发从改名那一秒起静默失效、
// 回落到全站默认(限流是安全设施,静默失效比配错更糟);删除后旧名字上留一条永不
// 命中的规则,名字被重新用上时它突然复活,一批毫不相干的新用户落进老闸门里 ——
// 正是 qianye/usergroup_residue_coverage_test.go 文件头要防的形状。

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useUserGroupKeyedOptions 装载那三处以用户分组名为键的 options,并在用例结束时还原。
func useUserGroupKeyedOptions(t *testing.T, rpm, concurrency, tokenDefaults string) {
	t.Helper()
	prevRPM := setting.ModelRequestRateLimitGroup2JSONString()
	prevConc := setting.ModelRequestConcurrencyGroup2JSONString()
	prevTok := setting.TokenDefaultGroups2JSONString()

	require.NoError(t, setting.UpdateModelRequestRateLimitGroupByJSONString(rpm))
	require.NoError(t, setting.UpdateModelRequestConcurrencyGroupByJSONString(concurrency))
	require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(tokenDefaults))

	t.Cleanup(func() {
		_ = setting.UpdateModelRequestRateLimitGroupByJSONString(prevRPM)
		_ = setting.UpdateModelRequestConcurrencyGroupByJSONString(prevConc)
		_ = setting.UpdateTokenDefaultGroupsByJSONString(prevTok)
	})
}

func TestRenameUserGroupCarriesEveryUserGroupKeyedOption(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	useFakeResidue(t, gdb)
	useUserGroupKeyedOptions(t,
		`{"老名字":[100,50]}`, `{"老名字":3}`, `{"老名字":"池"}`)

	require.NoError(t, gdb.Create(newUserGroup("老名字", 1, now())).Error)
	seedUser(t, main, 1, "老名字")

	res := callGroupHandler(t, http.MethodPost, "/user-groups/老名字/rename",
		gin.Param{Key: "name", Value: "老名字"},
		`{"new_name":"新名字","expect_users":1}`, adminRenameUserGroup)
	require.Equalf(t, http.StatusOK, res.Code, "改名应当成功: %s", res.Body.String())

	total, success, found := setting.GetGroupRateLimit("新名字")
	assert.Truef(t, found, "改名必须带走按用户分组配的 RPM 限流(安全设施不能静默失效)")
	assert.Equal(t, 100, total)
	assert.Equal(t, 50, success)
	assert.Equalf(t, 3, setting.GetGroupConcurrencyLimit("新名字"), "改名必须带走并发上限")
	assert.Equalf(t, "池", setting.GetTokenDefaultGroup("新名字"), "改名必须带走令牌默认模型分组")

	_, _, staleFound := setting.GetGroupRateLimit("老名字")
	assert.Falsef(t, staleFound, "旧名字上不能留下一条永远不会命中的限流规则")
	assert.Zerof(t, setting.GetGroupConcurrencyLimit("老名字"), "旧名字上不能留下并发规则")
	assert.Emptyf(t, setting.GetTokenDefaultGroup("老名字"), "旧名字上不能留下令牌默认分组")
}

func TestDeleteUserGroupClearsEveryUserGroupKeyedOption(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	useFakeResidue(t, gdb)
	useUserGroupKeyedOptions(t,
		`{"旧档":[100,50]}`, `{"旧档":3}`, `{"旧档":"池"}`)

	require.NoError(t, gdb.Create(newUserGroup("旧档", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("新档", 1, now())).Error)
	seedUser(t, main, 1, "旧档")

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/旧档",
		gin.Param{Key: "name", Value: "旧档"},
		`{"expect_users":1,"migrate_to":"新档"}`, adminDeleteUserGroup)
	require.Equalf(t, http.StatusOK, res.Code, "删除应当成功: %s", res.Body.String())

	_, _, found := setting.GetGroupRateLimit("旧档")
	assert.Falsef(t, found, "被删掉的用户分组不能在 RPM 限流表里留下一条永不命中的安全闸门")
	assert.Zerof(t, setting.GetGroupConcurrencyLimit("旧档"), "被删掉的用户分组不能留下并发规则")
	assert.Emptyf(t, setting.GetTokenDefaultGroup("旧档"), "被删掉的用户分组不能留下令牌默认分组")
}
