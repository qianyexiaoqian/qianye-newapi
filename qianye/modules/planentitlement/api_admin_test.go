package planentitlement

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// putEntitlement 以管理员身份调一次写接口,返回 HTTP 状态码与响应体。
func putEntitlement(t *testing.T, planId int, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.PUT("/plans/:plan_id/entitlement", func(c *gin.Context) {
		c.Set("id", 999) // 操作者
		adminPutEntitlement(c)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		"/plans/"+strconv.Itoa(planId)+"/entitlement", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)

	out := map[string]any{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, out
}

func getEntitlementView(t *testing.T, planId int) entitlementView {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/plans/:plan_id/entitlement", adminGetEntitlement)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/plans/"+strconv.Itoa(planId)+"/entitlement", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var envelope struct {
		Data entitlementView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return envelope.Data
}

// TestPutEntitlementRejectsUnusableConfigurations 是写入侧的三条**硬拒绝**。
//
// 三条都不是洁癖,各自对应一个只有在事后才会被发现的失败:
//
//	不在分组倍率表里  上游 GetGroupRatio 找不到时静默返回 1(按原价扣费、零告警),
//	                  而请求又会被 ContainsGroupRatio 以「分组已被弃用」挡掉 ——
//	                  也就是保存成功、页面显示正常、功能一定不生效。
//	auto              它不是模型分组。
//	仅限 + 零绑定     一份任何请求都用不上的额度池,是纯粹的死钱。
func TestPutEntitlementRejectsUnusableConfigurations(t *testing.T) {
	newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8,"VIP":0.5}`)
	seedPlan(t, mainDB, 1, "套餐A")

	cases := []struct {
		name string
		body string
		want string
	}{
		{"模型分组不在倍率表里", `{"unlock_groups":["不存在的分组"]}`, "qy_plan_unlock_group_invalid"},
		{"auto 不能被解锁", `{"unlock_groups":["auto"]}`, "qy_plan_unlock_group_invalid"},
		{"仅限但零绑定", `{"unlock_groups":[],"balance_scope":"restricted"}`, "qy_plan_balance_scope_need_binding"},
		{"范围取值非法", `{"unlock_groups":["pro"],"balance_scope":"whatever"}`, "qy_plan_balance_scope_invalid"},
		{"缺 unlock_groups 字段", `{"balance_scope":"universal"}`, "qy_invalid_param"},
		{"套餐不存在", `{"unlock_groups":["pro"]}`, "qy_subscription_plan_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planId := 1
			if tc.want == "qy_subscription_plan_not_found" {
				planId = 4242
			}
			code, body := putEntitlement(t, planId, tc.body)
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Equal(t, tc.want, body["code"])
		})
	}

	t.Run("大小写近似项给出提示", func(t *testing.T) {
		_, body := putEntitlement(t, 1, `{"unlock_groups":["vip"]}`)
		msg, _ := body["message"].(string)
		assert.Contains(t, msg, "VIP",
			"分组名区分大小写,最常见的一次手误必须当场看懂,而不是保存成功却永远不生效")
	})
}

// TestPutEntitlementIsFullReplaceAndAudited 守三件事:整体替换、审计、快照即时生效。
//
// 审计是硬要求:这份配置直接决定「谁能用哪些模型分组」与「钱从哪个池子扣」。
// 没有 before 快照的话,事后没有任何东西能回答"改之前是什么样"。
func TestPutEntitlementIsFullReplaceAndAudited(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8,"lab":0.1}`)
	seedPlan(t, mainDB, 1, "套餐A")

	code, _ := putEntitlement(t, 1, `{"unlock_groups":["pro","lab"],"balance_scope":"restricted"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"lab", "pro"}, getEntitlementView(t, 1).UnlockGroups)
	assert.Equal(t, ScopeRestricted, getEntitlementView(t, 1).BalanceScope)
	// 写完立刻生效,不用等下一个刷新周期。
	assert.True(t, Current().Binds(1, "pro"))
	assert.Equal(t, ScopeRestricted, Current().BalanceScope(1))

	// 整体替换:少传的那个必须消失,而不是被当成"没提到就保持"。
	code, _ = putEntitlement(t, 1, `{"unlock_groups":["pro"],"balance_scope":"universal"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"pro"}, getEntitlementView(t, 1).UnlockGroups)
	assert.False(t, Current().Binds(1, "lab"))
	assert.Equal(t, ScopeUniversal, Current().BalanceScope(1))

	assert.Equal(t, []string{
		"subscription.plan_entitlement.update:ok",
		"subscription.plan_entitlement.update:ok",
	}, auditActions(t, gdb))

	// 值没变就不写库、不写审计:否则事后查"谁把这个套餐改成仅限了"要从一堆
	// 空改动里人工筛。响应仍然是完整视图 —— 前端拿响应替换本地状态这件事,
	// 不该因为"这次没改动"而变成另一条分支。
	code, body := putEntitlement(t, 1, `{"unlock_groups":["pro"],"balance_scope":"universal"}`)
	require.Equal(t, http.StatusOK, code)
	data, _ := body["data"].(map[string]any)
	assert.Equal(t, []any{"pro"}, data["unlock_groups"],
		"写接口必须回与 GET 同一个结构,否则前端 setQueryData 之后下一次渲染就是 undefined")
	assert.Len(t, auditActions(t, gdb), 2)
}

// TestPutEntitlementWritesFailureAudit 守失败路径同样留痕。
//
// 写失败时库里到底变没变是不确定的(连接被掐、部分提交),而"有人在这一刻试图
// 把某个套餐改成仅限"这个事实本身与成功的那次同等重要。
func TestPutEntitlementWritesFailureAudit(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")

	// 把绑定表删掉制造一次真实的写失败。审计表还在,所以失败那一条写得进去。
	require.NoError(t, gdb.Migrator().DropTable(&PlanGrant{}))
	t.Cleanup(func() { _ = gdb.AutoMigrate(&PlanGrant{}) })

	code, _ := putEntitlement(t, 1, `{"unlock_groups":["pro"]}`)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Equal(t, []string{"subscription.plan_entitlement.update:fail"}, auditActions(t, gdb))
}

// TestAdminViewShowsRealRatioAndMissingGroups 守管理端那张倍率小表说真话。
//
// 它必须**现算**(service.GetUserGroupRatio,与三条计费路径同一个判据),
// 不得由扩展侧缓存:这张表存在的全部理由,就是让运营在改倍率之前看见
// 「A 组的人经套餐可以到达 G,而 (A,G) 这一格现在是几倍」。
func TestAdminViewShowsRealRatioAndMissingGroups(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	putGrant(t, gdb, 1, "已经被删掉的分组")
	seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)

	view := getEntitlementView(t, 1)
	assert.Equal(t, []string{"已经被删掉的分组"}, view.MissingGroups,
		"引用了已删模型分组的绑定必须在管理端标红,它在用户侧一定不生效")
	assert.Equal(t, int64(1), view.ActiveSubscriptions,
		"改这份配置会立刻影响多少人,必须在保存之前就摆出来")
	assert.Contains(t, view.ModelGroupCandidates, "pro")
	assert.NotContains(t, view.ModelGroupCandidates, autoGroup)

	require.NotEmpty(t, view.RatioTable)
	for _, cell := range view.RatioTable {
		assert.Equal(t, "pro", cell.ModelGroup, "已删分组不该出现在倍率表里")
		assert.Equal(t, 0.8, cell.Ratio, "必须是 (用户分组, 模型分组) 的实际生效倍率")
		assert.Equal(t, "group_ratio", cell.Source,
			"没有交叉格时是「继承兜底」,界面上不能只显示一个数字了事")
	}
}

// TestDeleteForPlansClearsBothTables 守删除套餐之后不留绑定。
//
// 残留行不会造成错误的放行(没有任何订阅还指着一个已删的套餐),但它会让
// 「全站零绑定」那条零 I/O 短路永远短路不掉 —— 一个已经删干净的功能仍在
// 每个请求上产生一次 per-user 查询。
func TestDeleteForPlansClearsBothTables(t *testing.T) {
	gdb := newExtDB(t)
	setGroupRatios(t, `{"pro":0.8}`)
	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	putGrant(t, gdb, 2, "pro")

	removed, err := DeleteForPlans(t.Context(), []int{1})
	require.NoError(t, err)
	assert.Equal(t, int64(2), removed)

	require.NoError(t, reload())
	assert.False(t, Current().Binds(1, "pro"))
	assert.True(t, Current().Binds(2, "pro"), "只该删指定的那个套餐")
	assert.Equal(t, ScopeUniversal, Current().BalanceScope(1))

	var count int64
	require.NoError(t, gdb.Session(&gorm.Session{}).Model(&PlanBalancePolicy{}).Count(&count).Error)
	assert.Zero(t, count)
}
