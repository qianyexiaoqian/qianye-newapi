package stardust

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_admin_ledger_test.go —— 管理端三张列表的筛选与跨库取名。

func TestAdminListsBalancesLedgerAndAccruals(t *testing.T) {
	env := newAPIEnv(t, nil)
	env.seedUser(t, 901, "alice", common.RoleCommonUser)
	env.seedUser(t, 902, "bob", common.RoleCommonUser)
	env.seedUser(t, 903, "carol", common.RoleCommonUser)
	_, err := Credit(env.ext, Posting{UserId: 901, Kind: KindLotPrize, Amount: 100, IdemScope: "t", IdemKey: "a1", ActNo: "LT-1"})
	require.NoError(t, err)
	_, err = Debit(env.ext, Posting{UserId: 901, Kind: KindLotStake, Amount: 20, IdemScope: "t", IdemKey: "a2", ActNo: "LT-2"})
	require.NoError(t, err)
	_, err = Credit(env.ext, Posting{UserId: 902, Kind: KindInviteTopup, Amount: 5, IdemScope: "t", IdemKey: "b1", PeerUserId: 903})
	require.NoError(t, err)
	r := adminRouter(7, common.RoleAdminUser)

	// 余额:用户名来自主库,carry 是字符串。
	data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/balances", ""))
	assert.EqualValues(t, 2, data["total"])
	byUser := map[float64]map[string]any{}
	for _, it := range itemsOf(t, data) {
		byUser[it["user_id"].(float64)] = it
	}
	require.Contains(t, byUser, float64(901))
	assert.Equal(t, "alice", byUser[901]["username"])
	assert.EqualValues(t, 80, byUser[901]["available"])
	assert.EqualValues(t, 20, byUser[901]["total_spent"])
	assert.Equal(t, "0", byUser[901]["carry"])
	assert.Equal(t, "bob", byUser[902]["username"])

	cases := []struct {
		name  string
		query string
		want  []float64
	}{
		{"按用户名前缀", "keyword=ali", []float64{901}},
		{"按 id 精确", "keyword=902", []float64{902}},
		{"按邮箱前缀", "keyword=bob@", []float64{902}},
		{"大小写折叠", "keyword=ALI", []float64{901}},
		{"查无此人回空页", "keyword=zzz", []float64{}},
		{"有账号没余额行的人不出现", "keyword=carol", []float64{}},
		{"user_id 精确", "user_id=902", []float64{902}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/balances?"+tc.query, ""))
			assert.EqualValues(t, len(tc.want), data["total"])
			got := make([]float64, 0, 2)
			for _, it := range itemsOf(t, data) {
				got = append(got, it["user_id"].(float64))
			}
			assert.Equal(t, tc.want, got)
		})
	}

	// 流水:user_id / kind / act_no 三个筛选,items 带 user_id。
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/ledger?user_id=901", ""))
	assert.EqualValues(t, 2, data["total"])
	items := itemsOf(t, data)
	require.Len(t, items, 2)
	assert.EqualValues(t, 901, items[0]["user_id"])
	assert.Equal(t, string(KindLotStake), items[0]["kind"], "最新的在前")
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/ledger?act_no=LT-1", ""))
	assert.EqualValues(t, 1, data["total"])
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/ledger?kind=invite_topup", ""))
	items = itemsOf(t, data)
	require.Len(t, items, 1)
	assert.EqualValues(t, 903, items[0]["peer_user_id"])
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/ledger", ""))
	assert.EqualValues(t, 3, data["total"], "不筛就是全站")
	assert.Equal(t, codeBadRequest, codeOf(t,
		call(t, r, http.MethodGet, "/api/qy/admin/stardust/ledger?kind=bogus", ""), http.StatusBadRequest))

	// 日桶:user_id / bucket_date / status。
	now := common.GetTimestamp()
	day1, day2 := invite.DayKey(now-2*86400), invite.DayKey(now-86400)
	seedAccrual(t, env.ext, 901, day1, AccrualSettled, "3.25", 0)
	seedAccrual(t, env.ext, 902, day2, AccrualHeld, "1.75", 0)
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/accruals?status=held", ""))
	items = itemsOf(t, data)
	require.Len(t, items, 1)
	assert.EqualValues(t, 902, items[0]["user_id"])
	assert.Equal(t, HoldOverdraft, items[0]["hold_reason"])
	assert.Equal(t, "1.75", items[0]["gross"])
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/accruals?bucket_date="+day1, ""))
	assert.EqualValues(t, 1, data["total"])
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/accruals?user_id=901&status=held", ""))
	assert.EqualValues(t, 0, data["total"])
	assert.Len(t, itemsOf(t, data), 0, "空页的 items 是 [] 而不是 null")
	assert.Equal(t, codeBadRequest, codeOf(t,
		call(t, r, http.MethodGet, "/api/qy/admin/stardust/accruals?bucket_date=2026-01", ""), http.StatusBadRequest))
	assert.Equal(t, codeBadRequest, codeOf(t,
		call(t, r, http.MethodGet, "/api/qy/admin/stardust/accruals?status=weird", ""), http.StatusBadRequest))
}

// 「结算台 → 佣金用户 → 星屑总览」:每个用户的星屑按来源拆成 自身 / 下游 / 其他 / 已花费。
//
// 每一类都至少灌一笔,外加一个没有任何流水的余额行;断言拆分正确、
// 可用 = 自身 + 下游 + 其他 − 已花费,以及排序列与关键词检索生效。
func TestAdminStardustUserSummarySplitsBySource(t *testing.T) {
	env := newAPIEnv(t, nil)
	env.seedUser(t, 901, "alice", common.RoleCommonUser)
	env.seedUser(t, 902, "bob", common.RoleCommonUser)
	env.seedUser(t, 903, "carol", common.RoleCommonUser)

	credit := func(uid int, kind Kind, amount int64, key string) {
		_, err := Credit(env.ext, Posting{UserId: uid, Kind: kind, Amount: amount, IdemScope: "t", IdemKey: key, PeerUserId: 999})
		require.NoError(t, err)
	}
	// alice:自身 30+5,下游 7+3+2,其他 100(派奖),花掉 20。
	credit(901, KindConsumeRebate, 30, "a1")
	credit(901, KindPlanBuyer, 5, "a2")
	credit(901, KindInviteConsume, 7, "a3")
	credit(901, KindCommissionCredit, 3, "a4")
	credit(901, KindPlanInviter, 2, "a5")
	credit(901, KindLotPrize, 100, "a6")
	_, err := Debit(env.ext, Posting{UserId: 901, Kind: KindLotStake, Amount: 20, IdemScope: "t", IdemKey: "a7", ActNo: "LT-1"})
	require.NoError(t, err)
	// bob:只有下游 50。
	credit(902, KindInviteTopup, 50, "b1")
	// carol:有余额行、没有任何流水(手工建行,模拟历史上建过行的账号)。
	require.NoError(t, env.ext.Create(&Balance{UserId: 903}).Error)

	r := adminRouter(7, common.RoleAdminUser)
	data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/user-summary", ""))
	assert.EqualValues(t, 3, data["total"])
	byUser := map[float64]map[string]any{}
	order := []float64{}
	for _, it := range itemsOf(t, data) {
		byUser[it["user_id"].(float64)] = it
		order = append(order, it["user_id"].(float64))
	}
	assert.Equal(t, []float64{901, 902, 903}, order, "默认按可用星屑降序")

	alice := byUser[901]
	assert.Equal(t, "alice", alice["username"])
	assert.EqualValues(t, 35, alice["self_earned"])
	assert.EqualValues(t, 12, alice["downline_earned"])
	assert.EqualValues(t, 100, alice["other_earned"])
	assert.EqualValues(t, 20, alice["spent"])
	assert.EqualValues(t, 127, alice["available"])
	assert.EqualValues(t,
		alice["self_earned"].(float64)+alice["downline_earned"].(float64)+alice["other_earned"].(float64)-alice["spent"].(float64),
		alice["available"], "可用 = 自身 + 下游 + 其他 − 已花费")

	assert.EqualValues(t, 50, byUser[902]["downline_earned"])
	assert.EqualValues(t, 0, byUser[902]["self_earned"])
	for _, col := range []string{"available", "self_earned", "downline_earned", "other_earned", "spent"} {
		assert.EqualValues(t, 0, byUser[903][col], "没有流水的余额行 %s 必须是 0", col)
	}

	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/user-summary?sort=downline", ""))
	first := itemsOf(t, data)[0]
	assert.EqualValues(t, 902, first["user_id"], "按下游返还排序时 bob(50)排第一")

	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/user-summary?keyword=bob", ""))
	assert.EqualValues(t, 1, data["total"])
	assert.EqualValues(t, 902, itemsOf(t, data)[0]["user_id"])

	codeOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/user-summary?sort=password", ""), http.StatusBadRequest)
}

// TestAdminListsSortByColumnBothWays 守「按列从多到少 / 从少到多」:白名单里的列两个方向都能排,
// 白名单外的列与非法方向一律 400(列名永远不从请求里直接拼进 SQL)。
func TestAdminListsSortByColumnBothWays(t *testing.T) {
	env := newAPIEnv(t, nil)
	r := adminRouter(7, common.RoleAdminUser)
	for i, amount := range []int64{30, 10, 20} {
		uid := 951 + i
		env.seedUser(t, uid, "sort-"+itoa(uid), common.RoleCommonUser)
		_, err := Credit(env.ext, Posting{UserId: uid, Kind: KindLotPrize, Amount: amount, IdemScope: "t", IdemKey: "s" + itoa(uid)})
		require.NoError(t, err)
	}
	amounts := func(path, field string) []float64 {
		items := itemsOf(t, dataOf(t, call(t, r, http.MethodGet, path, "")))
		out := make([]float64, 0, len(items))
		for _, it := range items {
			out = append(out, it[field].(float64))
		}
		return out
	}

	assert.Equal(t, []float64{30, 20, 10}, amounts("/api/qy/admin/stardust/balances?sort=available&order=desc", "available"))
	assert.Equal(t, []float64{10, 20, 30}, amounts("/api/qy/admin/stardust/balances?sort=available&order=asc", "available"))
	assert.Equal(t, []float64{30, 20, 10}, amounts("/api/qy/admin/stardust/ledger?sort=amount", "amount"), "缺省方向是从多到少")
	assert.Equal(t, []float64{10, 20, 30}, amounts("/api/qy/admin/stardust/ledger?sort=amount&order=asc", "amount"))

	for _, bad := range []string{
		"/api/qy/admin/stardust/balances?sort=hold_reason",
		"/api/qy/admin/stardust/ledger?sort=amount&order=sideways",
		"/api/qy/admin/stardust/accruals?sort=status",
	} {
		assert.Equal(t, codeBadRequest, codeOf(t, call(t, r, http.MethodGet, bad, ""), http.StatusBadRequest), bad)
	}
}
