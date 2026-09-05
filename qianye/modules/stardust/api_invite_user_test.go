package stardust

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_invite_user_test.go —— 「我的推广」四条用户端接口与管理端日桶明细的形状(invite-contract.md)。
//
// 形状即契约:前端 Y2 按这些键名写页面。每个键都要出现、类型要对;越权与脱敏是这几条
// 接口唯一的安全面 —— 下线的真实用户名不能出现在响应的任何位置。
// 主库用户 id 取 601–640 / 701–710。

// seedInviteAccrual 插一行下线消费返日桶。
func seedInviteAccrual(t *testing.T, env apiEnv, inviterId, inviteeId int, day string, base int64, gross, status, ledgerNo string) {
	t.Helper()
	now := common.GetTimestamp()
	row := InviteAccrual{
		InviterId: inviterId, InviteeId: inviteeId, BucketDate: day, BaseQuota: base,
		RateGroup: "vip", Bps: 2500, QuotaPerUnit: 500_000, Gross: decimal.RequireFromString(gross),
		Status: status, LedgerNo: ledgerNo, CreatedAt: now, UpdatedAt: now,
	}
	if status == AccrualHeld {
		row.HoldReason = HoldOverdraft
	}
	require.NoError(t, env.ext.Create(&row).Error)
}

func seedRelation(t *testing.T, env apiEnv, inviterId, inviteeId int, masked string, boundAt int64, blocked bool) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, env.ext.Create(&invite.InviteRelation{
		InviteeId: inviteeId, InviterId: inviterId, MaskedName: masked, InviteeRef: "ref" + itoa(inviteeId),
		BoundAt: boundAt, Blocked: blocked, CreatedAt: now, UpdatedAt: now,
	}).Error)
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestInviteUserEndpointsShapes(t *testing.T) {
	env := newTaskEnv(t, func(s *config.Stardust) { s.InviteConsumeBps = 1000; s.InviteTopupBps = 300 })
	withCompliance(t, true)
	now := common.GetTimestamp()
	yesterday := invite.DayKey(invite.DayStart(now) - 1)
	dayBefore := invite.DayKey(invite.DayStart(now) - 1 - secondsPerDay)
	r := userRouter(601)

	require.NoError(t, env.ext.Create(&GroupRate{UserGroup: "vip", InviteConsumeBps: bpsPtr(2500), Enabled: true}).Error)
	invalidateGroupRates()
	seedMainUser(t, env.main, model.User{Id: 601, Group: "vip"})
	seedMainUser(t, env.main, model.User{Id: 602, Username: "real-name-602", InviterId: 601})
	seedMainUser(t, env.main, model.User{Id: 603, Username: "real-name-603", InviterId: 601})
	seedMainUser(t, env.main, model.User{Id: 604, Username: "real-name-604", InviterId: 601})
	seedRelation(t, env, 601, 602, "re***02", now-3*86400, false)
	seedRelation(t, env, 601, 603, "re***03", now-2*86400, true)
	seedRelation(t, env, 601, 604, "re***04", now-86400, false)
	// 别人的下线:不能混进来。
	seedMainUser(t, env.main, model.User{Id: 605})
	seedMainUser(t, env.main, model.User{Id: 606, InviterId: 605})
	seedRelation(t, env, 605, 606, "ot***06", now-86400, false)

	// 昨天:602 与 604 各一桶已结算(同一行流水,发了 3),603 一桶 held。前天:602 一桶已结算。
	res, err := Credit(env.ext, Posting{
		UserId: 601, Kind: KindInviteConsume, Amount: 3, IdemScope: "t", IdemKey: "y",
		RefType: inviteSettleRefType, RefNo: invite.DayKey(now), Remark: "下线消费返 " + yesterday,
	})
	require.NoError(t, err)
	seedInviteAccrual(t, env, 601, 602, yesterday, 5_000_000, "2.5", AccrualSettled, res.LedgerNo)
	seedInviteAccrual(t, env, 601, 604, yesterday, 1_000_000, "0.5", AccrualSettled, res.LedgerNo)
	seedInviteAccrual(t, env, 601, 603, yesterday, 2_000_000, "1", AccrualHeld, "")
	seedInviteAccrual(t, env, 601, 602, dayBefore, 3_000_000, "1.5", AccrualSettled, "SDOLD")
	seedInviteAccrual(t, env, 605, 606, yesterday, 9_000_000, "4.5", AccrualSettled, "")
	// 其它邀请类流水:602 充值返 7;一条非邀请类(抽奖)不进推广页。
	_, err = Credit(env.ext, Posting{UserId: 601, Kind: KindInviteTopup, Amount: 7, IdemScope: "t", IdemKey: "tp", PeerUserId: 602, RefNo: "T1"})
	require.NoError(t, err)
	_, err = Credit(env.ext, Posting{UserId: 601, Kind: KindLotPrize, Amount: 100, IdemScope: "t", IdemKey: "lot"})
	require.NoError(t, err)
	// 今天:602 已花 1M,603(拉黑)花 9M 不算,606 是别人的。
	today := invite.DayStart(now)
	require.NoError(t, env.main.Create(&[]model.Log{
		consumeLog(602, today+10, 1_000_000, ""),
		consumeLog(603, today+10, 9_000_000, ""),
		consumeLog(606, today+10, 9_000_000, ""),
	}).Error)

	t.Run("summary", func(t *testing.T) {
		data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/summary", ""))
		assert.EqualValues(t, 3, data["invitee_count"])
		assert.EqualValues(t, 1, data["blocked_count"])
		totals := data["totals"].(map[string]any)
		assert.EqualValues(t, 3, totals["invite_consume"])
		assert.EqualValues(t, 7, totals["invite_topup"])
		assert.EqualValues(t, 0, totals["invite_redeem"])
		assert.EqualValues(t, 0, totals["invite_register"])
		assert.EqualValues(t, 0, totals["plan_inviter"])
		assert.EqualValues(t, 10, totals["all"], "抽奖那 100 不是推广收益")
		y := data["yesterday"].(map[string]any)
		assert.EqualValues(t, 8_000_000, y["base_quota"], "含 held 的那一桶")
		assert.EqualValues(t, 3, y["granted"], "到账取流水上的整数,不是 gross 取整")
		assert.EqualValues(t, 1, y["held"])
		assert.EqualValues(t, 1_000_000, data["pending_today_base_quota"], "拉黑的下线与别人的下线都不算")
		rate := data["rate"].(map[string]any)
		assert.Equal(t, "vip", rate["group"])
		assert.EqualValues(t, 2500, rate["invite_consume_bps"], "按我自己的分组档")
		assert.EqualValues(t, 300, rate["invite_topup_bps"])
		assert.EqualValues(t, 200, rate["invite_redeem_bps"])
		assert.EqualValues(t, 50, rate["invite_register_stardust"])
		assert.Equal(t, true, data["compliance_confirmed"])
		assert.EqualValues(t, 0, data["day_offset_minutes"])

		withCompliance(t, false)
		data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/summary", ""))
		assert.Equal(t, false, data["compliance_confirmed"])
		assert.EqualValues(t, 0, data["rate"].(map[string]any)["invite_consume_bps"], "合规未确认时按 0 下发")
		withCompliance(t, true)
	})

	t.Run("invitees", func(t *testing.T) {
		data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/invitees?p=1&page_size=2", ""))
		assert.EqualValues(t, 3, data["total"])
		assert.EqualValues(t, 1, data["p"])
		assert.EqualValues(t, 2, data["page_size"])
		items := itemsOf(t, data)
		require.Len(t, items, 2, "分页生效")
		assert.EqualValues(t, 604, items[0]["user_id"], "最近绑定的排前面")
		assert.Equal(t, "re***04", items[0]["username_masked"])
		assert.Equal(t, false, items[0]["blocked"])
		assert.Equal(t, yesterday, items[0]["last_active_day"])
		assert.EqualValues(t, 1_000_000, items[0]["total_base_quota"])
		assert.EqualValues(t, 0, items[0]["total_stardust"], "0.5 向下取整")
		assert.EqualValues(t, 603, items[1]["user_id"])
		assert.Equal(t, true, items[1]["blocked"])

		data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/invitees?p=2&page_size=2", ""))
		items = itemsOf(t, data)
		require.Len(t, items, 1)
		assert.EqualValues(t, 602, items[0]["user_id"])
		assert.EqualValues(t, 8_000_000, items[0]["total_base_quota"], "两天的基数之和")
		assert.EqualValues(t, 7+4, items[0]["total_stardust"], "充值返 7 + 日桶 gross (2.5+1.5) 取整 4")
		assert.NotContains(t, string(call(t, r, http.MethodGet, "/api/qy/invite/invitees", "").Body.Bytes()), "real-name",
			"真实用户名不得出现在响应里")
	})

	t.Run("records", func(t *testing.T) {
		data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/records", ""))
		assert.EqualValues(t, 2, data["total"], "抽奖流水不在推广记录里")
		items := itemsOf(t, data)
		require.Len(t, items, 2)
		assert.Equal(t, string(KindInviteTopup), items[0]["kind"], "新的在前")
		assert.EqualValues(t, 7, items[0]["amount"])
		assert.EqualValues(t, 602, items[0]["invitee_id"])
		assert.Equal(t, "re***02", items[0]["invitee_masked"])
		assert.Equal(t, "T1", items[0]["ref_no"])
		assert.Equal(t, string(KindInviteConsume), items[1]["kind"])
		assert.EqualValues(t, 0, items[1]["invitee_id"], "下线消费返按邀请人汇总发,没有单个下线")
		assert.Equal(t, "", items[1]["invitee_masked"])
		assert.Equal(t, "下线消费返 "+yesterday, items[1]["remark"])
		for _, key := range []string{"ledger_no", "created_at"} {
			assert.Contains(t, items[1], key)
		}

		data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/records?kind=invite_consume", ""))
		assert.EqualValues(t, 1, data["total"])
		codeOf(t, call(t, r, http.MethodGet, "/api/qy/invite/records?kind=lot_prize", ""), http.StatusBadRequest)
	})

	t.Run("invitee-daily", func(t *testing.T) {
		data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/invitee-daily", ""))
		assert.Equal(t, yesterday, data["day"], "缺省是昨天")
		items := itemsOf(t, data)
		require.Len(t, items, 3, "别人的下线不在里面")
		assert.EqualValues(t, 602, items[0]["invitee_id"], "按基数降序")
		assert.Equal(t, "re***02", items[0]["invitee_masked"])
		assert.EqualValues(t, 5_000_000, items[0]["base_quota"])
		assert.EqualValues(t, 2500, items[0]["bps"])
		assert.Equal(t, "2.5", items[0]["gross"])
		assert.Equal(t, AccrualSettled, items[0]["status"])
		assert.Equal(t, AccrualHeld, items[1]["status"])
		assert.EqualValues(t, 8_000_000, data["total_base_quota"])
		assert.Equal(t, "4", data["total_gross"])

		data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/invitee-daily?day="+dayBefore, ""))
		assert.Len(t, itemsOf(t, data), 1)
		codeOf(t, call(t, r, http.MethodGet, "/api/qy/invite/invitee-daily?day=2026-09-01", ""), http.StatusBadRequest)
	})

	t.Run("邀请关掉时整组 404", func(t *testing.T) {
		cfg := stardustConfig(func(s *config.Stardust) { s.QuotaPerUnit = 500_000 })
		cfg.Invite.Enabled = false
		useConfig(t, cfg)
		for _, p := range []string{"summary", "invitees", "records", "invitee-daily"} {
			assert.Equal(t, http.StatusNotFound, call(t, r, http.MethodGet, "/api/qy/invite/"+p, "").Code, p)
		}
	})
}

// TestAdminInviteAccrualsListFiltersAndNamesBothSides 守管理端日桶明细:筛选、两侧用户名、参数校验。
func TestAdminInviteAccrualsListFiltersAndNamesBothSides(t *testing.T) {
	env := newTaskEnv(t, nil)
	now := common.GetTimestamp()
	day := invite.DayKey(now - 86400)
	older := invite.DayKey(now - 2*86400)
	r := adminRouter(7, common.RoleAdminUser)
	const path = "/api/qy/admin/invite/invite-accruals"

	seedMainUser(t, env.main, model.User{Id: 701, Username: "upline"})
	seedMainUser(t, env.main, model.User{Id: 702, Username: "downline-a"})
	seedMainUser(t, env.main, model.User{Id: 703, Username: "downline-b"})
	seedInviteAccrual(t, env, 701, 702, day, 1_000_000, "0.5", AccrualSettled, "SD1")
	seedInviteAccrual(t, env, 701, 703, day, 2_000_000, "1", AccrualHeld, "")
	seedInviteAccrual(t, env, 701, 702, older, 3_000_000, "1.5", AccrualSettled, "SD0")

	data := dataOf(t, call(t, r, http.MethodGet, path+"?day="+day, ""))
	assert.EqualValues(t, 2, data["total"])
	items := itemsOf(t, data)
	require.Len(t, items, 2)
	assert.Equal(t, "upline", items[0]["inviter_username"])
	assert.Equal(t, "downline-a", items[0]["invitee_username"])
	assert.EqualValues(t, 702, items[0]["invitee_id"])
	assert.Equal(t, "SD1", items[0]["ledger_no"])
	assert.Equal(t, AccrualHeld, items[1]["status"])
	assert.Equal(t, HoldOverdraft, items[1]["hold_reason"])

	data = dataOf(t, call(t, r, http.MethodGet, path+"?inviter_id=701&invitee_id=702", ""))
	assert.EqualValues(t, 2, data["total"], "不带 day 时跨天")
	assert.Equal(t, day, itemsOf(t, data)[0]["bucket_date"], "新的在前")
	data = dataOf(t, call(t, r, http.MethodGet, path+"?status=held", ""))
	assert.EqualValues(t, 1, data["total"])
	data = dataOf(t, call(t, r, http.MethodGet, path+"?inviter_id=999", ""))
	assert.EqualValues(t, 0, data["total"])
	assert.Len(t, itemsOf(t, data), 0, "空页也要是 [] 不是 null")

	codeOf(t, call(t, r, http.MethodGet, path+"?day=2026-09-01", ""), http.StatusBadRequest)
	codeOf(t, call(t, r, http.MethodGet, path+"?status=paid", ""), http.StatusBadRequest)
}
