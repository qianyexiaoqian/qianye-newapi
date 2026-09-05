package stardust

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_admin_rates_test.go —— 分组比例档与套餐返还定义的读写契约(契约 §3)。

// useUpstreamGroups 钉住"在册 ∪ 登记表"两份清单,测完还原。
func useUpstreamGroups(t *testing.T, ratios map[string]float64, declared []string) {
	t.Helper()
	prevJSON := ratio_setting.GroupRatio2JSONString()
	raw, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(string(raw)))
	prevDeclared := service.QyDeclaredUserGroups
	service.QyDeclaredUserGroups = func() []string { return declared }
	t.Cleanup(func() {
		_ = ratio_setting.UpdateGroupRatioByJSONString(prevJSON)
		service.QyDeclaredUserGroups = prevDeclared
	})
}

func TestAdminGroupRatesRequireKnownGroupAndInvalidateCache(t *testing.T) {
	env := newAPIEnv(t, nil)
	useUpstreamGroups(t, map[string]float64{"default": 1, "VIP": 2, "auto": 1}, []string{"Silver"})
	r := adminRouter(7, common.RoleAdminUser)
	ctx := context.Background()

	assert.Equal(t, codeGroupUnknown, codeOf(t, call(t, r, http.MethodPut,
		"/api/qy/admin/stardust/group-rates/ghost", `{"consume_bps":7000,"enabled":true}`), http.StatusBadRequest))

	// 大小写折叠后落到 vip 那一行;null 的档位存 NULL,不是 0。
	data := dataOf(t, call(t, r, http.MethodPut, "/api/qy/admin/stardust/group-rates/VIP",
		`{"consume_bps":7000,"invite_topup_bps":null,"enabled":true}`))
	assert.Equal(t, "vip", data["user_group"])
	assert.EqualValues(t, 7000, data["consume_bps"])
	assert.Nil(t, data["invite_topup_bps"])
	assert.Nil(t, data["invite_redeem_bps"])
	assert.Equal(t, true, data["enabled"])
	assert.EqualValues(t, 7, data["operator_id"])
	got, ok := groupRateFor(ctx, "VIP")
	require.True(t, ok, "写入后缓存必须立即失效")
	require.NotNil(t, got.ConsumeBps)
	assert.Equal(t, 7000, *got.ConsumeBps)
	assert.Nil(t, got.InviteTopupBps)

	assert.Equal(t, codeBadRequest, codeOf(t, call(t, r, http.MethodPut,
		"/api/qy/admin/stardust/group-rates/Silver", `{"consume_bps":-1,"enabled":true}`), http.StatusBadRequest))
	assert.Equal(t, codeBadRequest, codeOf(t, call(t, r, http.MethodPut,
		"/api/qy/admin/stardust/group-rates/Silver", `{"invite_redeem_bps":10000001,"enabled":true}`), http.StatusBadRequest))
	// 登记表里的分组同样可配;整行禁用等价于没配。
	dataOf(t, call(t, r, http.MethodPut, "/api/qy/admin/stardust/group-rates/silver", `{"enabled":false}`))
	_, ok = groupRateFor(ctx, "silver")
	assert.False(t, ok, "禁用的行等价于没配")

	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/group-rates", ""))
	items := itemsOf(t, data)
	require.Len(t, items, 2)
	assert.Equal(t, "silver", items[0]["user_group"])
	assert.Equal(t, "vip", items[1]["user_group"])
	assert.Equal(t, []any{"default", "silver", "vip"}, data["groups"], "在册 ∪ 登记表,去掉 auto,已排序")

	// 再写同一分组是 upsert,不是第二行。
	dataOf(t, call(t, r, http.MethodPut, "/api/qy/admin/stardust/group-rates/vip", `{"consume_bps":1,"enabled":true}`))
	var n int64
	require.NoError(t, env.ext.Model(&GroupRate{}).Count(&n).Error)
	assert.EqualValues(t, 2, n)
	got, _ = groupRateFor(ctx, "vip")
	assert.Equal(t, 1, *got.ConsumeBps)

	data = dataOf(t, call(t, r, http.MethodDelete, "/api/qy/admin/stardust/group-rates/VIP", ""))
	assert.Equal(t, true, data["deleted"])
	_, ok = groupRateFor(ctx, "vip")
	assert.False(t, ok, "删除后缓存必须立即失效")
	data = dataOf(t, call(t, r, http.MethodDelete, "/api/qy/admin/stardust/group-rates/vip", ""))
	assert.Equal(t, false, data["deleted"], "重复删除是幂等的")

	okPut, failPut := countResults(auditRowsOf(t, env.ext, auditGroupRatePut))
	assert.Equal(t, 3, okPut)
	assert.Equal(t, 3, failPut, "未登记 / 两次越界都要留痕")
	okDel, failDel := countResults(auditRowsOf(t, env.ext, auditGroupRateDelete))
	assert.Equal(t, 2, okDel)
	assert.Zero(t, failDel)
}

func TestAdminPlanRewardsRoundTrip(t *testing.T) {
	env := newAPIEnv(t, nil)
	r := adminRouter(7, common.RoleAdminUser)
	ctx := context.Background()

	data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/plan-rewards/9", ""))
	assert.Equal(t, false, data["exists"])
	assert.EqualValues(t, 9, data["plan_id"])
	assert.EqualValues(t, 10_000, data["buyer_bps"])
	assert.EqualValues(t, 0, data["inviter_bps"])
	assert.Equal(t, []any{"order", "balance"}, data["sources"], "没配过的套餐按默认口径下发")

	assert.Equal(t, codeBadSource, codeOf(t, call(t, r, http.MethodPut, "/api/qy/admin/stardust/plan-rewards/9",
		`{"buyer_bps":5000,"inviter_bps":100,"sources":["order","paypal"]}`), http.StatusBadRequest))
	assert.Equal(t, codeBadRequest, codeOf(t, call(t, r, http.MethodPut, "/api/qy/admin/stardust/plan-rewards/9",
		`{"buyer_bps":10000001,"inviter_bps":100,"sources":["order"]}`), http.StatusBadRequest))
	assert.Equal(t, codeBadRequest, codeOf(t, call(t, r, http.MethodGet,
		"/api/qy/admin/stardust/plan-rewards/abc", ""), http.StatusBadRequest))
	_, exists := planRewardFor(ctx, 9)
	assert.False(t, exists, "被拒的写入一行都不落")

	data = dataOf(t, call(t, r, http.MethodPut, "/api/qy/admin/stardust/plan-rewards/9",
		`{"buyer_bps":5000,"inviter_bps":100,"sources":["ADMIN","order","order"]}`))
	assert.Equal(t, true, data["exists"])
	assert.Equal(t, []any{"admin", "order"}, data["sources"], "去重、小写、排序")
	got, exists := planRewardFor(ctx, 9)
	require.True(t, exists, "写入后缓存必须立即失效")
	assert.Equal(t, 5000, got.BuyerBps)
	assert.Equal(t, 100, got.InviterBps)
	assert.True(t, got.SourceAllowed(PlanSourceAdmin))
	assert.False(t, got.SourceAllowed(PlanSourceBalance))
	assert.Equal(t, 7, got.OperatorId)

	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/plan-rewards/9", ""))
	assert.Equal(t, true, data["exists"])

	data = dataOf(t, call(t, r, http.MethodDelete, "/api/qy/admin/stardust/plan-rewards/9", ""))
	assert.Equal(t, false, data["exists"])
	assert.EqualValues(t, 10_000, data["buyer_bps"])
	_, exists = planRewardFor(ctx, 9)
	assert.False(t, exists)

	okPut, failPut := countResults(auditRowsOf(t, env.ext, auditPlanRewardPut))
	assert.Equal(t, 1, okPut)
	assert.Equal(t, 2, failPut)
	okDel, _ := countResults(auditRowsOf(t, env.ext, auditPlanRewardDel))
	assert.Equal(t, 1, okDel)
}
