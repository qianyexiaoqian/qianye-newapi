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
