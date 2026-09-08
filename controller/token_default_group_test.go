package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestResolveTokenDefaultGroup 钉住「运营配了什么」与「这个人能选什么」的求交。
//
// 这一条是本功能唯一的安全属性:预选值必须落在该用户真实可选的清单内。
// 漏掉它的表现是用户打开新建令牌就看到一个提交即被拒的分组,而他没动过那一栏。
func TestResolveTokenDefaultGroup(t *testing.T) {
	usable := func(names ...string) map[string]map[string]interface{} {
		out := make(map[string]map[string]interface{}, len(names))
		for _, n := range names {
			out[n] = map[string]interface{}{"ratio": 1, "desc": n}
		}
		return out
	}

	tests := []struct {
		name      string
		config    string
		userGroup string
		usable    map[string]map[string]interface{}
		want      string
	}{
		{
			name:      "配了且该用户可选 → 返回它",
			config:    `{"vip":"vip-pool"}`,
			userGroup: "vip",
			usable:    usable("vip-pool", "default"),
			want:      "vip-pool",
		},
		{
			name:      "配了但该用户选不了 → 空串,由前端退回原有逻辑",
			config:    `{"vip":"vip-pool"}`,
			userGroup: "vip",
			usable:    usable("default"),
			want:      "",
		},
		{
			name:      "该用户分组没配 → 空串",
			config:    `{"vip":"vip-pool"}`,
			userGroup: "default",
			usable:    usable("default", "vip-pool"),
			want:      "",
		},
		{
			name:      "auto 是合法的预选值",
			config:    `{"vip":"auto"}`,
			userGroup: "vip",
			usable:    usable("default", "auto"),
			want:      "auto",
		},
		{
			name:      "空用户分组(匿名口径)→ 空串",
			config:    `{"vip":"vip-pool"}`,
			userGroup: "",
			usable:    usable("vip-pool"),
			want:      "",
		},
		{
			name:      "整份配置为空 → 空串,逐位等于本功能上线前",
			config:    `{}`,
			userGroup: "vip",
			usable:    usable("vip-pool"),
			want:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(tc.config))
			t.Cleanup(func() {
				require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(`{}`))
			})
			require.Equal(t, tc.want, resolveTokenDefaultGroup(tc.userGroup, tc.usable))
		})
	}
}

// TestValidateTokenDefaultGroups 钉住写入期的结构校验。
//
// 空键对应不到任何用户分组,空值等价于没配 —— 两者存进库只会让运营
// 以为配过了,而界面上什么都不会发生。
func TestValidateTokenDefaultGroups(t *testing.T) {
	require.NoError(t, setting.ValidateTokenDefaultGroups(`{}`))
	require.NoError(t, setting.ValidateTokenDefaultGroups(`{"vip":"vip-pool"}`))

	require.Error(t, setting.ValidateTokenDefaultGroups(`{"":"vip-pool"}`), "空用户分组名必须被拒")
	require.Error(t, setting.ValidateTokenDefaultGroups(`{"vip":""}`), "空默认模型分组必须被拒")
	require.Error(t, setting.ValidateTokenDefaultGroups(`not json`), "非法 JSON 必须被拒")
}

// TestUpdateTokenDefaultGroupsRejectsPartialWrite 钉住「解析失败不留半张表」。
//
// 直接往共享 map 上 Unmarshal 的话,一份中途失败的 JSON 会留下半新半旧的映射,
// 而调用方拿到 error 之后通常只把它记进日志 —— 于是线上跑着一份谁都没配过的组合。
func TestUpdateTokenDefaultGroupsRejectsPartialWrite(t *testing.T) {
	require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(`{"vip":"vip-pool"}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(`{}`))
	})

	require.Error(t, setting.UpdateTokenDefaultGroupsByJSONString(`{"a":`))
	require.Equal(t, "vip-pool", setting.GetTokenDefaultGroup("vip"), "解析失败后原映射必须原样保留")
}

// TestGetUserGroupOptionsPaging 守用户分组候选清单的翻页。
//
// 这份清单同时喂着五个下拉(用户编辑、限流规则、API 地址、套餐、令牌默认分组),
// 而只有「令牌默认分组」那一页按**行**消费它 —— 它每个用户分组画一行、每行一个
// 装着全部模型分组的下拉,分组一多就是几千个可聚焦节点。
//
// 因此这里有两条必须同时成立的断言,而它们的错误方向相反:
//
//	不带参数 → 全量。少给一档的表现是那一档人在用户编辑页永远选不上,
//	          而界面上看不出少了什么(下拉里"没有"和"不存在"长得一样)。
//	带了参数 → 一页 + 全量总数。总数按本页算的话翻页条会写「共 10 条」,
//	          运营据此认为分组只有 10 个。
func TestGetUserGroupOptionsPaging(t *testing.T) {
	setupUserGroupOptionsDB(t)
	for _, group := range []string{"g5", "g1", "g3", "g2", "g4"} {
		// aff_code 上有唯一索引,空串会在第二行撞掉 —— 与本用例无关,
		// 但不给的话建到第二个用户就失败。
		require.NoError(t, model.DB.Create(&model.User{
			Username: group + "-user", Password: "x", Group: group, AffCode: group,
		}).Error)
	}

	full := callUserGroupOptions(t, "")
	assert.Equal(t, []string{"g1", "g2", "g3", "g4", "g5"}, full.Data,
		"不带翻页参数必须是全量且已排序 —— 五个下拉都在按全量消费它")
	assert.Equal(t, 0, full.Total, "不带翻页参数时不下发总数")

	page2 := callUserGroupOptions(t, "?p=2&page_size=2")
	assert.Equal(t, []string{"g3", "g4"}, page2.Data)
	assert.Equal(t, 5, page2.Total, "总数是全量,不是本页条数")
	assert.Equal(t, 2, page2.Page)
	assert.Equal(t, 2, page2.PageSize)

	// 越界页码给空数组而不是 500/panic,总数仍然如实上报 —— 前端据此回落页码。
	beyond := callUserGroupOptions(t, "?p=99&page_size=2")
	assert.Empty(t, beyond.Data)
	assert.Equal(t, 5, beyond.Total)
}

type userGroupOptionsBody struct {
	Success  bool     `json:"success"`
	Data     []string `json:"data"`
	Total    int      `json:"total"`
	Page     int      `json:"p"`
	PageSize int      `json:"page_size"`
}

func callUserGroupOptions(t *testing.T, query string) userGroupOptionsBody {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/user-group/options"+query, nil)
	GetUserGroupOptions(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var body userGroupOptionsBody
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
	require.True(t, body.Success)
	return body
}

func setupUserGroupOptionsDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousCache := common.MemoryCacheEnabled
	previousRedis := common.RedisEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.User{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	// 直接换 model.DB 会绕过 InitDB,而 QyDistinctUserGroups 走的是裸 SQL 片段:
	// 不调 InitCol,那句 SELECT DISTINCT 的列名会是空串,SQL 直接语法错误。
	model.InitCol()
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		model.InitCol()
		common.MemoryCacheEnabled = previousCache
		common.RedisEnabled = previousRedis
	})
}
