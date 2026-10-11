package stardust

import (
	"context"
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

// api_invite_user_test.go —— 「我的推广」星屑那一半的三条用户端接口(invite-contract.md)。
//
// 形状即契约:前端按这些键名写页面。越权与脱敏是这几条接口唯一的安全面 —— 下线的
// 真实用户名不能出现在响应的任何位置。下线带来的返还只走推广佣金:这里不再有星屑侧的
// 比例、昨日已返、今日待返基数,「这个下线带来多少佣金」由 invite.PairCommissionTotals
// 回答(佣金模块注入;本包测试里用桩)。主库用户 id 取 601–610。

func seedRelation(t *testing.T, env apiEnv, inviterId, inviteeId int, masked string, boundAt int64, blocked bool) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, env.ext.Create(&invite.InviteRelation{
		InviteeId: inviteeId, InviterId: inviterId, MaskedName: masked, InviteeRef: "ref" + itoa(inviteeId),
		BoundAt: boundAt, Blocked: blocked, CreatedAt: now, UpdatedAt: now,
	}).Error)
}

func itoa(i int) string { return strconv.Itoa(i) }

// stubPairCommission 把佣金模块注入的汇总槽换成固定答案,测试结束还原。
func stubPairCommission(t *testing.T, answers map[[2]int]invite.PairCommission) {
	t.Helper()
	prev := invite.PairCommissionTotals
	invite.PairCommissionTotals = func(_ context.Context, pairs [][2]int) map[[2]int]invite.PairCommission {
		out := map[[2]int]invite.PairCommission{}
		for _, p := range pairs {
			if a, ok := answers[p]; ok {
				out[p] = a
			}
		}
		return out
	}
	t.Cleanup(func() { invite.PairCommissionTotals = prev })
}

func TestInviteUserEndpointsShapes(t *testing.T) {
	env := newTaskEnv(t, nil) // invite_register_stardust = 50
	withCompliance(t, true)
	now := common.GetTimestamp()
	yesterday := invite.DayKey(invite.DayStart(now) - 1)
	r := userRouter(601)

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

	// 星屑账本:已下线的星屑侧下线返留下的历史(消费返 3、602 的充值返 7),不回收;
	// 现行的注册奖(602)与套餐返上线(604);一条非邀请类(抽奖)不进推广页。
	for _, p := range []Posting{
		{UserId: 601, Kind: KindInviteConsume, Amount: 3, IdemScope: "t", IdemKey: "c", Remark: "下线消费返 " + yesterday},
		{UserId: 601, Kind: KindInviteTopup, Amount: 7, IdemScope: "t", IdemKey: "tp", PeerUserId: 602, RefNo: "T1"},
		{UserId: 601, Kind: KindInviteRegister, Amount: 2, IdemScope: "t", IdemKey: "rg", PeerUserId: 602},
		{UserId: 601, Kind: KindPlanInviter, Amount: 5, IdemScope: "t", IdemKey: "pl", PeerUserId: 604},
		{UserId: 601, Kind: KindLotPrize, Amount: 100, IdemScope: "t", IdemKey: "lot"},
	} {
		_, err := Credit(env.ext, p)
		require.NoError(t, err)
	}
	stubPairCommission(t, map[[2]int]invite.PairCommission{
		{601, 602}: {ConsumeBaseQuota: 8_000_000, LastDay: yesterday, Gross: decimal.RequireFromString("4.25")},
		{605, 606}: {ConsumeBaseQuota: 9_000_000, LastDay: yesterday, Gross: decimal.RequireFromString("9")},
	})

	t.Run("summary", func(t *testing.T) {
		data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/summary", ""))
		assert.EqualValues(t, 3, data["invitee_count"])
		assert.EqualValues(t, 1, data["blocked_count"])
		totals := data["totals"].(map[string]any)
		assert.EqualValues(t, 2, totals["invite_register"])
		assert.EqualValues(t, 5, totals["plan_inviter"])
		assert.EqualValues(t, 10, totals["legacy"], "已下线的星屑侧下线返合成一行历史")
		assert.EqualValues(t, 17, totals["all"], "抽奖那 100 不是推广收益")
		for _, gone := range []string{"invite_consume", "invite_topup", "invite_redeem"} {
			assert.NotContains(t, totals, gone, "不再与佣金并排各报一个下线返")
		}
		for _, gone := range []string{"yesterday", "pending_today_base_quota"} {
			assert.NotContains(t, data, gone, "下线返只走佣金,星屑侧没有日结那一套了")
		}
		rate := data["rate"].(map[string]any)
		assert.Equal(t, "vip", rate["group"])
		assert.EqualValues(t, 50, rate["invite_register_stardust"])
		assert.Len(t, rate, 2, "星屑侧只剩注册奖,没有下线充值 / 兑换码 / 消费比例")
		assert.Equal(t, true, data["compliance_confirmed"])
		assert.EqualValues(t, 0, data["day_offset_minutes"])

		withCompliance(t, false)
		data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/summary", ""))
		assert.Equal(t, false, data["compliance_confirmed"])
		assert.EqualValues(t, 0, data["rate"].(map[string]any)["invite_register_stardust"], "合规未确认时按 0 下发")
		withCompliance(t, true)
	})

	t.Run("invitees", func(t *testing.T) {
		data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/invitees?p=1&page_size=2", ""))
		assert.EqualValues(t, 3, data["total"])
		items := itemsOf(t, data)
		require.Len(t, items, 2, "分页生效")
		assert.EqualValues(t, 604, items[0]["user_id"], "最近绑定的排前面")
		assert.Equal(t, "re***04", items[0]["username_masked"])
		assert.Equal(t, "", items[0]["last_active_day"], "没有消费计佣")
		assertDecimalEq(t, "0", items[0]["commission_gross"])
		assert.EqualValues(t, 5, items[0]["total_stardust"], "套餐返上线")
		assert.EqualValues(t, 603, items[1]["user_id"])
		assert.Equal(t, true, items[1]["blocked"])

		data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/invitees?p=2&page_size=2", ""))
		items = itemsOf(t, data)
		require.Len(t, items, 1)
		assert.EqualValues(t, 602, items[0]["user_id"])
		assert.Equal(t, yesterday, items[0]["last_active_day"], "最近活跃取佣金的消费计佣桶日")
		assert.EqualValues(t, 8_000_000, items[0]["total_base_quota"])
		assertDecimalEq(t, "4.25", items[0]["commission_gross"], "带来多少佣金由佣金侧回答")
		assert.EqualValues(t, 7+2, items[0]["total_stardust"], "星屑账本:历史充值返 7 + 注册奖 2,不与佣金相加")
		assert.NotContains(t, string(call(t, r, http.MethodGet, "/api/qy/invite/invitees", "").Body.Bytes()), "real-name",
			"真实用户名不得出现在响应里")
	})

	t.Run("records", func(t *testing.T) {
		data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/records", ""))
		assert.EqualValues(t, 4, data["total"], "抽奖流水不在推广记录里;历史下线返照常可查")
		items := itemsOf(t, data)
		require.Len(t, items, 4)
		assert.Equal(t, string(KindPlanInviter), items[0]["kind"], "新的在前")
		assert.EqualValues(t, 604, items[0]["invitee_id"])
		assert.Equal(t, "re***04", items[0]["invitee_masked"])
		for _, key := range []string{"ledger_no", "created_at", "ref_no", "remark", "amount"} {
			assert.Contains(t, items[0], key)
		}

		data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/invite/records?kind=invite_consume", ""))
		assert.EqualValues(t, 1, data["total"])
		codeOf(t, call(t, r, http.MethodGet, "/api/qy/invite/records?kind=lot_prize", ""), http.StatusBadRequest)
	})

	t.Run("按下线逐日的星屑消费返明细已随下线返一起撤掉", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, call(t, r, http.MethodGet, "/api/qy/invite/invitee-daily", "").Code)
	})

	t.Run("邀请关掉时整组 404", func(t *testing.T) {
		cfg := stardustConfig(func(s *config.Stardust) { s.QuotaPerUnit = 500_000 })
		cfg.Invite.Enabled = false
		useConfig(t, cfg)
		for _, p := range []string{"summary", "invitees", "records"} {
			assert.Equal(t, http.StatusNotFound, call(t, r, http.MethodGet, "/api/qy/invite/"+p, "").Code, p)
		}
	})
}
