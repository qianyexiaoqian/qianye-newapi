package stardust

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_user_test.go —— 用户端三条只读接口的契约(stardust-api-contract §2)。

func TestGetMeReturnsZeroRowWithoutInsertingAndSummarizesYesterday(t *testing.T) {
	env := newAPIEnv(t, func(s *config.Stardust) { s.SettleDelayMinutes = 30 })

	// 从没拿过星屑的人:零值行,而且不许凭空建行。
	before := common.GetTimestamp()
	rec := call(t, userRouter(501), http.MethodGet, "/api/qy/stardust/me", "")
	after := common.GetTimestamp()
	data := dataOf(t, rec)
	assert.Equal(t, "星屑", data["name"])
	assert.EqualValues(t, QuotaPerUnit(), data["quota_per_unit"])
	bal, ok := data["balance"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 0, bal["available"])
	assert.Equal(t, "0", bal["carry"], "carry 是 decimal 字符串")
	assert.Equal(t, "", bal["hold_reason"])
	assert.Nil(t, data["yesterday"])
	assert.EqualValues(t, 0, data["pending_held_count"])
	assert.Nil(t, balanceOf(t, env.ext, 501), "读接口不得凭空建余额行")
	// next_settle_at = 下一个日界 + settle_delay。请求跨过日界的概率极小,但两个候选都认。
	next := int64(data["next_settle_at"].(float64))
	assert.Contains(t, []int64{
		invite.NextDayStart(before) + 30*60, invite.NextDayStart(after) + 30*60,
	}, next)

	// 有账的人:昨日桶的 settled_amount 取流水行上真正发出去的整数,held 桶计数。
	_, err := Credit(env.ext, Posting{UserId: 502, Kind: KindLotPrize, Amount: 30, IdemScope: "t", IdemKey: "p1"})
	require.NoError(t, err)
	rebate, err := Credit(env.ext, Posting{UserId: 502, Kind: KindConsumeRebate, Amount: 7, IdemScope: "t", IdemKey: "r1"})
	require.NoError(t, err)
	now := common.GetTimestamp()
	yesterday := invite.DayKey(invite.DayStart(now) - 1)
	seedAccrual(t, env.ext, 502, yesterday, AccrualSettled, "7.4000000000", rebate.LedgerId)
	seedAccrual(t, env.ext, 502, invite.DayKey(now-3*86400), AccrualHeld, "1.5", 0)
	seedAccrual(t, env.ext, 502, invite.DayKey(now-4*86400), AccrualHeld, "2.5", 0)
	// 别人的 held 桶不算进我的计数。
	seedAccrual(t, env.ext, 503, invite.DayKey(now-4*86400), AccrualHeld, "9", 0)

	data = dataOf(t, call(t, userRouter(502), http.MethodGet, "/api/qy/stardust/me", ""))
	bal = data["balance"].(map[string]any)
	assert.EqualValues(t, 37, bal["available"])
	assert.EqualValues(t, 37, bal["total_earned"])
	y, ok := data["yesterday"].(map[string]any)
	require.True(t, ok, "昨日桶存在时必须下发摘要")
	assert.Equal(t, yesterday, y["bucket_date"])
	assert.Equal(t, AccrualSettled, y["status"])
	assert.EqualValues(t, 7, y["settled_amount"])
	assert.EqualValues(t, 3_700_000, y["base_quota"])
	assert.EqualValues(t, 10_000, y["rate_bps"])
	gross, ok := y["gross"].(string)
	require.True(t, ok, "gross 是 decimal 字符串")
	assert.True(t, decimal.RequireFromString(gross).Equal(decimal.RequireFromString("7.4")), gross)
	assert.EqualValues(t, 2, data["pending_held_count"])
}

func TestUserLedgerAndAccrualsFilterAndPaginate(t *testing.T) {
	env := newAPIEnv(t, nil)
	_, err := Credit(env.ext, Posting{UserId: 601, Kind: KindLotPrize, Amount: 100, IdemScope: "t", IdemKey: "k1", ActNo: "LT-1"})
	require.NoError(t, err)
	_, err = Debit(env.ext, Posting{UserId: 601, Kind: KindLotStake, Amount: 40, IdemScope: "t", IdemKey: "k2", ActNo: "LT-2", RefType: "lot_entry", RefNo: "LE-1"})
	require.NoError(t, err)
	_, err = Credit(env.ext, Posting{UserId: 601, Kind: KindLotRefund, Amount: 40, IdemScope: "t", IdemKey: "k3", ActNo: "LT-2"})
	require.NoError(t, err)
	// 别人的流水不可见。
	_, err = Credit(env.ext, Posting{UserId: 602, Kind: KindLotPrize, Amount: 1, IdemScope: "t", IdemKey: "k4"})
	require.NoError(t, err)
	r := userRouter(601)

	data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/stardust/ledger", ""))
	assert.EqualValues(t, 3, data["total"])
	items := itemsOf(t, data)
	require.Len(t, items, 3)
	assert.Equal(t, string(KindLotRefund), items[0]["kind"], "最新的在前")
	assert.EqualValues(t, 100, items[0]["balance_after"])
	assert.EqualValues(t, -40, items[1]["amount"], "扣减行金额带负号")
	assert.Equal(t, "LE-1", items[1]["ref_no"])
	_, leaked := items[0]["idem_key"]
	assert.False(t, leaked, "idem_key 装的是调用方原文,不下发")
	_, leakedId := items[0]["user_id"]
	assert.False(t, leakedId, "用户端不下发 user_id")

	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/stardust/ledger?kind=lot_stake", ""))
	assert.EqualValues(t, 1, data["total"])
	items = itemsOf(t, data)
	require.Len(t, items, 1)
	assert.Equal(t, "LT-2", items[0]["act_no"])

	assert.Equal(t, codeBadRequest, codeOf(t,
		call(t, r, http.MethodGet, "/api/qy/stardust/ledger?kind=bogus", ""), http.StatusBadRequest),
		"未知 kind 必须 400,而不是返回一页空表")

	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/stardust/ledger?page=2&page_size=2", ""))
	assert.EqualValues(t, 3, data["total"])
	assert.EqualValues(t, 2, data["page"])
	assert.EqualValues(t, 2, data["page_size"])
	assert.Len(t, itemsOf(t, data), 1, "第 2 页只剩最早的那一条")

	// 日桶:最近的在前,gross 是字符串。
	now := common.GetTimestamp()
	seedAccrual(t, env.ext, 601, invite.DayKey(now-2*86400), AccrualSettled, "3.25", 0)
	seedAccrual(t, env.ext, 601, invite.DayKey(now-86400), AccrualComputed, "1.75", 0)
	seedAccrual(t, env.ext, 602, invite.DayKey(now-86400), AccrualComputed, "9", 0)
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/stardust/accruals", ""))
	assert.EqualValues(t, 2, data["total"])
	items = itemsOf(t, data)
	require.Len(t, items, 2)
	assert.Equal(t, invite.DayKey(now-86400), items[0]["bucket_date"])
	assert.Equal(t, AccrualComputed, items[0]["status"])
	assert.Equal(t, "1.75", items[0]["gross"])
	assert.Equal(t, "default", items[0]["user_group"])
}
