package stardust

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_user_test.go —— 用户端四条只读接口的契约(stardust-api-contract §2)。

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

// TestForecastEstimatesTomorrowAndThrottlesManualRefresh —— 「明日预计到账」的口径与节流。
//
// 主库用户 id 取 801–806(与本包其它用例不重叠:估算缓存按 user_id 活在进程里)。
// 刻度 500000、消费返 100%(10000 bps)让换算一眼可算:基数 ÷ 500000 就是计提。
func TestForecastEstimatesTomorrowAndThrottlesManualRefresh(t *testing.T) {
	env := newTaskEnv(t, func(s *config.Stardust) {
		s.SettleDelayMinutes = 30
		s.ConsumeBps = 10_000
		s.InviteConsumeBps = 1_000
	})
	withCompliance(t, true)
	now := common.GetTimestamp()
	today := invite.DayStart(now)

	seedMainUser(t, env.main, model.User{Id: 801, Quota: 1_000})
	seedMainUser(t, env.main, model.User{Id: 802, InviterId: 801})
	seedMainUser(t, env.main, model.User{Id: 803, InviterId: 801})
	seedRelation(t, env, 801, 802, "in***02", now-86400, false)
	seedRelation(t, env, 801, 803, "in***03", now-86400, true)
	require.NoError(t, env.ext.Create(&Balance{
		UserId: 801,
		Carry:  decimal.RequireFromString("0.37"), InviteCarry: decimal.RequireFromString("0.9"),
	}).Error)
	require.NoError(t, env.main.Create(&[]model.Log{
		consumeLog(801, today+10, 3_000_000, ""),
		consumeLog(801, today+20, 700_000, ""),
		// 三条排除口径都不进基数,与日结逐字相同。
		consumeLog(801, today+30, 9_000_000, `{"violation_fee":true}`),
		consumeLog(801, today+40, 9_000_000, `{"channel_test":true}`),
		// 昨天的消费属于昨天那一桶,不该被算进"明天要发的"。
		consumeLog(801, today-10, 9_000_000, ""),
		consumeLog(802, today+10, 1_000_000, ""),
		// 803 已被停止计返:它的消费一分都不进上线的估算。
		consumeLog(803, today+10, 9_000_000, ""),
	}).Error)

	t.Run("两条线各自取整再相加", func(t *testing.T) {
		data := dataOf(t, call(t, userRouter(801), http.MethodGet, "/api/qy/stardust/forecast", ""))
		assert.Equal(t, invite.DayKey(now), data["day"], "估的是今天这一桶")
		assert.EqualValues(t, invite.NextDayStart(now)+30*60, data["settle_at"],
			"今天这一桶恒在下一个日界 + 结算延迟发放")
		assert.Equal(t, "", data["hold_reason"])

		consume := data["consume"].(map[string]any)
		assert.EqualValues(t, 3_700_000, consume["base_quota"])
		assert.EqualValues(t, 10_000, consume["rate_bps"])
		assertDecimalEq(t, "7.4", consume["gross"])
		assertDecimalEq(t, "0.37", consume["carry"])
		assert.EqualValues(t, 7, consume["estimated"], "floor(0.37 + 7.4)")

		inv := data["invite"].(map[string]any)
		assert.Equal(t, true, inv["applies"])
		assert.Equal(t, true, inv["counted"])
		assert.EqualValues(t, 1_000_000, inv["base_quota"], "被拉黑的下线不计")
		assert.EqualValues(t, 1_000, inv["rate_bps"])
		assertDecimalEq(t, "0.2", inv["gross"])
		assertDecimalEq(t, "0.9", inv["carry"])
		assert.EqualValues(t, 1, inv["estimated"], "floor(0.9 + 0.2)")

		// 8 而不是 9:两条线各自 floor(0.37+7.4)=7 与 floor(0.9+0.2)=1,
		// 先把两笔计提加起来再取整会多发一颗。
		assert.EqualValues(t, 8, data["estimated_total"])
	})

	t.Run("手动刷新在节流窗口内不重算", func(t *testing.T) {
		r := userRouter(801)
		first := dataOf(t, call(t, r, http.MethodGet, "/api/qy/stardust/forecast", ""))
		require.NoError(t, env.main.Create(&model.Log{
			UserId: 801, CreatedAt: today + 50, Type: model.LogTypeConsume, Quota: 5_000_000,
		}).Error)

		again := dataOf(t, call(t, r, http.MethodGet, "/api/qy/stardust/forecast?refresh=1", ""))
		assert.Equal(t, first["computed_at"], again["computed_at"], "刚算过就重算等于把按钮变成压测器")
		assert.EqualValues(t, 3_700_000, again["consume"].(map[string]any)["base_quota"])
		assert.EqualValues(t, first["computed_at"].(float64)+forecastManualMinSecs, again["refresh_after"],
			"refresh_after 告诉界面按钮什么时候才有用")

		// 缓存那一份够老了:手动刷新这才真的重算,新落的消费进了基数。
		cached, found, err := forecastCache.Get(801)
		require.NoError(t, err)
		require.True(t, found)
		cached.ComputedAt -= forecastManualMinSecs
		forecastCache.Set(801, cached)
		fresh := dataOf(t, call(t, r, http.MethodGet, "/api/qy/stardust/forecast?refresh=1", ""))
		assert.EqualValues(t, 8_700_000, fresh["consume"].(map[string]any)["base_quota"])
		assert.EqualValues(t, 17, fresh["consume"].(map[string]any)["estimated"], "floor(0.37 + 17.4)")
	})

	t.Run("暂缓的账号预计到账为零,但计提照样算出来", func(t *testing.T) {
		seedMainUser(t, env.main, model.User{Id: 804, Quota: -1})
		require.NoError(t, env.main.Create(&model.Log{
			UserId: 804, CreatedAt: today + 10, Type: model.LogTypeConsume, Quota: 2_500_000,
		}).Error)

		data := dataOf(t, call(t, userRouter(804), http.MethodGet, "/api/qy/stardust/forecast", ""))
		assert.Equal(t, HoldOverdraft, data["hold_reason"])
		consume := data["consume"].(map[string]any)
		assertDecimalEq(t, "5", consume["gross"], "计提照常发生,只是不入账")
		assert.EqualValues(t, 0, consume["estimated"])
		assert.EqualValues(t, 0, data["estimated_total"])
		assert.Nil(t, balanceOf(t, env.ext, 804), "估算是只读的,不得凭空建余额行")
	})

	t.Run("邀请功能关掉时下线那一条线整条不成立", func(t *testing.T) {
		useConfig(t, &config.Config{
			Enabled:  true,
			Stardust: config.Stardust{Enabled: true, Name: "星屑", ConsumeBps: 10_000, InviteConsumeBps: 1_000},
			Invite:   config.Invite{Enabled: false, InviterCacheSecs: 300},
		})
		seedMainUser(t, env.main, model.User{Id: 805})
		seedMainUser(t, env.main, model.User{Id: 806, InviterId: 805})
		seedRelation(t, env, 805, 806, "in***06", now-86400, false)
		require.NoError(t, env.main.Create(&model.Log{
			UserId: 806, CreatedAt: today + 10, Type: model.LogTypeConsume, Quota: 5_000_000,
		}).Error)

		inv := dataOf(t, call(t, userRouter(805), http.MethodGet,
			"/api/qy/stardust/forecast", ""))["invite"].(map[string]any)
		assert.Equal(t, false, inv["applies"])
		assert.EqualValues(t, 0, inv["base_quota"])
		assert.EqualValues(t, 0, inv["rate_bps"])
	})
}

// assertDecimalEq 按**数值**比较下发的 decimal 字符串。
//
// 不能直接比字面量:扩展库的 decimal(30,10) 在 MySQL / PostgreSQL 上补满小数位、
// 在测试用的 sqlite 上不补,而这条差异与被测的换算毫无关系。
func assertDecimalEq(t *testing.T, want string, got any, msg ...any) {
	t.Helper()
	s, ok := got.(string)
	require.True(t, ok, "decimal 必须以字符串下发,拿到 %T", got)
	assert.True(t, decimal.RequireFromString(s).Equal(decimal.RequireFromString(want)),
		append([]any{"期望 " + want + ",实得 " + s}, msg...)...)
}
