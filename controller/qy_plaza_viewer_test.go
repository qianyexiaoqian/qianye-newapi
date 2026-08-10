package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// qy_plaza_viewer_test.go —— 未登录访客在模型广场上看到什么。
//
// 被测的不变式只有一条,但它是整条需求的全部:
//
//	未登录看到的模型与倍率  ==  「注册默认用户分组」的已登录用户看到的模型与倍率
//
// 断言两份**完整响应体**相等(除去 anonymous_preview 那一位)而不是各自
// 逐字段核对期望值:后者会随着 pricing 响应新增字段而慢慢失去覆盖面,
// 而"两条路径给出同一个答案"这件事无论加多少字段都仍然成立。

type plazaPricingResponse struct {
	Success          bool               `json:"success"`
	Data             []model.Pricing    `json:"data"`
	GroupRatio       map[string]float64 `json:"group_ratio"`
	UsableGroup      map[string]string  `json:"usable_group"`
	AutoGroups       []string           `json:"auto_groups"`
	AnonymousPreview bool               `json:"anonymous_preview"`
}

const (
	plazaDefaultGroup = "qy-plaza-default"
	plazaOtherGroup   = "qy-plaza-other"
	plazaDefaultModel = "qy-plaza-default-model"
	plazaOtherModel   = "qy-plaza-other-model"
)

// withPlazaCatalog 铺一个两分组、两模型的站点,并把全局「用户可选分组」白名单
// 清空 —— 那正是本站的真实配置,也是这条缺陷的触发条件:白名单空着时,
// 匿名口径(userGroup == "")解析出来的可选清单是空集,于是价格页整页为空。
func withPlazaCatalog(t *testing.T, groupRatioJSON, groupGroupRatioJSON string) *gorm.DB {
	t.Helper()

	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 1, Type: constant.ChannelTypeOpenAI, Name: "qy-plaza-channel", Status: common.ChannelStatusEnabled},
	}).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: plazaDefaultGroup, Model: plazaDefaultModel, ChannelId: 1, Enabled: true},
		{Group: plazaOtherGroup, Model: plazaOtherModel, ChannelId: 1, Enabled: true},
	}).Error)

	prevGroupRatio := ratio_setting.GroupRatio2JSONString()
	prevGroupGroupRatio := ratio_setting.GroupGroupRatio2JSONString()
	prevUsable := setting.UserUsableGroups2JSONString()
	prevAuto := setting.AutoGroups2JsonString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(prevGroupRatio))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(prevGroupGroupRatio))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(prevUsable))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(prevAuto))
		model.InvalidatePricingCache()
	})

	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(groupRatioJSON))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(groupGroupRatioJSON))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	model.InvalidatePricingCache()

	return db
}

// withNewUserGroup 替换「新注册用户落进哪个用户分组」这个只读事实。
//
// 替 hook 而不是去建扩展库:controller 侧要验的是"有没有按这个事实渲染",
// 那个事实是怎么读出来的由 qianye/modules/usergroup 自己的测试负责。
func withNewUserGroup(t *testing.T, group string) {
	t.Helper()
	prev := model.QyNewUserGroup
	model.QyNewUserGroup = func() string { return group }
	t.Cleanup(func() { model.QyNewUserGroup = prev })
}

func callGetPricing(t *testing.T, userId int, userGroup string) plazaPricingResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/pricing", nil)
	if userId > 0 {
		c.Set("id", userId)
		c.Set("group", userGroup)
	}

	GetPricing(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload plazaPricingResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	return payload
}

func plazaModelNames(pricing []model.Pricing) []string {
	names := make([]string, 0, len(pricing))
	for _, item := range pricing {
		names = append(names, item.ModelName)
	}
	return names
}

// TestGetPricingAnonymousMatchesDefaultRegistrationGroup 是这条需求的验收点。
//
// 四个用例覆盖的是**倍率来源**的四种形状,因为"价格也要跟着对"是最容易做错的
// 一半:未登录时倍率必须来自默认分组(含交叉格覆盖),而不是 1.0、不是别的分组、
// 也不是 GroupRatio 的原始值。
func TestGetPricingAnonymousMatchesDefaultRegistrationGroup(t *testing.T) {
	cases := []struct {
		name                string
		groupRatioJSON      string
		groupGroupRatioJSON string
		wantModels          []string
		wantGroupRatio      map[string]float64
	}{
		{
			name:                "默认分组的倍率来自 GroupRatio 兜底",
			groupRatioJSON:      `{"` + plazaDefaultGroup + `":0.5,"` + plazaOtherGroup + `":3}`,
			groupGroupRatioJSON: `{}`,
			wantModels:          []string{plazaDefaultModel},
			wantGroupRatio:      map[string]float64{plazaDefaultGroup: 0.5},
		},
		{
			// 交叉格是本仓的实扣价来源。未登录页面若印 GroupRatio 的兜底值 0.5,
			// 而新用户实际按 0.25 计费,那就是一次价格欺骗 —— 方向哪一边都不行。
			name:                "交叉格覆盖必须压过兜底",
			groupRatioJSON:      `{"` + plazaDefaultGroup + `":0.5,"` + plazaOtherGroup + `":3}`,
			groupGroupRatioJSON: `{"` + plazaDefaultGroup + `":{"` + plazaDefaultGroup + `":0.25}}`,
			wantModels:          []string{plazaDefaultModel},
			wantGroupRatio:      map[string]float64{plazaDefaultGroup: 0.25},
		},
		{
			// 0 是「显式免费」,不是「没配」。它必须原样出现在未登录的价格页上。
			name:                "交叉格的显式 0 不能被当成未配置",
			groupRatioJSON:      `{"` + plazaDefaultGroup + `":0.5,"` + plazaOtherGroup + `":3}`,
			groupGroupRatioJSON: `{"` + plazaDefaultGroup + `":{"` + plazaDefaultGroup + `":0}}`,
			wantModels:          []string{plazaDefaultModel},
			wantGroupRatio:      map[string]float64{plazaDefaultGroup: 0},
		},
		{
			// 全局白名单里额外开放的分组,未登录同样看得到 —— 口径是「默认分组能选的
			// 全部模型分组」,不是「默认分组自己那一格」。
			name:                "全局白名单开放的分组一并纳入",
			groupRatioJSON:      `{"` + plazaDefaultGroup + `":0.5,"` + plazaOtherGroup + `":3}`,
			groupGroupRatioJSON: `{}`,
			wantModels:          []string{plazaDefaultModel, plazaOtherModel},
			wantGroupRatio:      map[string]float64{plazaDefaultGroup: 0.5, plazaOtherGroup: 3},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := withPlazaCatalog(t, tc.groupRatioJSON, tc.groupGroupRatioJSON)
			withNewUserGroup(t, plazaDefaultGroup)

			if tc.name == "全局白名单开放的分组一并纳入" {
				require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(
					`{"`+plazaOtherGroup+`":"公开分组"}`))
			}

			require.NoError(t, db.Create(&model.User{
				Id: 9101, Username: "qy-plaza-member", Password: "password",
				Group: plazaDefaultGroup, Status: common.UserStatusEnabled,
			}).Error)

			anonymous := callGetPricing(t, 0, "")
			member := callGetPricing(t, 9101, plazaDefaultGroup)

			assert.True(t, anonymous.AnonymousPreview,
				"未登录响应必须带 anonymous_preview,否则前端无法说明这一页是按谁的档算的")
			assert.False(t, member.AnonymousPreview)

			assert.ElementsMatch(t, tc.wantModels, plazaModelNames(anonymous.Data))
			assert.Equal(t, tc.wantGroupRatio, anonymous.GroupRatio)

			// 最强的那一条:两条路径除了预览标记之外必须逐字节相同。
			anonymous.AnonymousPreview = false
			assert.Equal(t, member, anonymous,
				"未登录看到的与该分组已登录用户看到的必须完全一致 —— 不一致意味着访客"+
					"照着页面充值之后会看到另一套模型或另一套价格")
		})
	}
}

// TestGetPricingAnonymousKeepsEmptyWhenDefaultGroupHasNothing 钉死那条**刻意**
// 选择的边界:默认分组一个可用模型分组都没有时,返回空列表,不回落全量。
//
// 回落全量是"看起来更友好"的那一侧,但它把新用户根本调不通的模型印在价格页上,
// 而访客会照着那一页去充值。空列表至少与他注册之后看到的完全一致。
func TestGetPricingAnonymousKeepsEmptyWhenDefaultGroupHasNothing(t *testing.T) {
	withPlazaCatalog(t,
		// 默认分组名不在倍率表里 ⇒ GetUserUsableGroups 的自我补入不成立,
		// 全局白名单又是空的,于是可选清单确实是空集。
		`{"`+plazaOtherGroup+`":3}`, `{}`)
	withNewUserGroup(t, plazaDefaultGroup)

	anonymous := callGetPricing(t, 0, "")

	assert.Empty(t, anonymous.Data,
		"默认分组没有任何可用模型分组时必须给空列表,回落全量会把访客调不通的模型当成商品展示")
	assert.Empty(t, anonymous.GroupRatio)
	assert.Empty(t, anonymous.UsableGroup)
	assert.True(t, anonymous.AnonymousPreview)
}

// TestGetPricingAnonymousFallsBackToUpstreamDefaultWhenUnconfigured 覆盖
// 「扩展未启用 / 没配过默认分组」:model.QyNewUserGroup 的默认实现返回
// model.UpstreamDefaultUserGroup,而新用户那时确实落进 default。
//
// 断言的形式是与"今天的匿名口径"逐位相同,因为在没有 default 这一档的站点上
// GetUserUsableGroups("default") 与 GetUserUsableGroups("") 完全一致 ——
// 这就是"上线当天零变化"的结构性来源。
func TestGetPricingAnonymousFallsBackToUpstreamDefaultWhenUnconfigured(t *testing.T) {
	db := withPlazaCatalog(t,
		`{"`+model.UpstreamDefaultUserGroup+`":1,"`+plazaOtherGroup+`":3}`, `{}`)
	// default 这一档真的有模型:只有这样,"按 default 渲染"与"按匿名空串渲染"
	// 才区分得开 —— 前者靠 GetUserUsableGroups 的自我补入拿到 default,
	// 后者拿不到。少了这一行,本用例对回落方向完全没有分辨力。
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: model.UpstreamDefaultUserGroup, Model: "qy-plaza-upstream-default-model", ChannelId: 1, Enabled: true},
	}).Error)
	model.InvalidatePricingCache()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(
		`{"`+plazaOtherGroup+`":"公开分组"}`))

	// 刻意**不**替换 hook:走 model.QyNewUserGroup 的默认实现。
	require.Equal(t, model.UpstreamDefaultUserGroup, model.QyNewUserGroup())

	anonymous := callGetPricing(t, 0, "")

	assert.ElementsMatch(t,
		[]string{"qy-plaza-upstream-default-model", plazaOtherModel},
		plazaModelNames(anonymous.Data),
		"未配置默认分组时必须按上游兜底的 default 渲染,而不是退回匿名空串口径")
	assert.Equal(t,
		map[string]float64{model.UpstreamDefaultUserGroup: 1, plazaOtherGroup: 3},
		anonymous.GroupRatio)
}

// TestPlazaViewerUserGroupPrefersLoggedInIdentity 锁住那条判据本身。
//
// 单测这一层的价值在于它能覆盖"已登录但分组为空"这种上面那些端到端用例构造不出来
// 的形状:那时**不能**回落成注册默认分组 —— 那个人是真实存在的用户,
// 他的分组就是空的,给他看别人的价格是错的。
func TestPlazaViewerUserGroupPrefersLoggedInIdentity(t *testing.T) {
	withNewUserGroup(t, plazaDefaultGroup)

	cases := []struct {
		name   string
		userId int
		group  string
		want   string
	}{
		{name: "未登录取注册默认用户分组", userId: 0, group: "", want: plazaDefaultGroup},
		{name: "已登录取本人分组", userId: 42, group: plazaOtherGroup, want: plazaOtherGroup},
		{name: "已登录且分组为空时不借用默认分组", userId: 42, group: "", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.userId > 0 {
				c.Set("id", tc.userId)
				c.Set("group", tc.group)
			}
			assert.Equal(t, tc.want, plazaViewerUserGroup(c))
		})
	}
}
