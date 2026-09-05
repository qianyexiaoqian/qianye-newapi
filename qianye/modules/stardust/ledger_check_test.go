package stardust

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ledger_check_test.go —— 体检必须对"绕过 Credit / Debit 直接改表"喊疼。
//
// 三种改坏的形状各占一个用户:直接 UPDATE available(I0 与 I1 同时失效)、
// 删掉余额行(有流水没余额)、以及一个 I2 恰好靠 carry 闭合的正常人作对照。

func TestLedgerCheckFlagsTamperedBalancesAndHeldAge(t *testing.T) {
	env := newAPIEnv(t, func(s *config.Stardust) { s.HeldAlertDays = 7 })
	r := adminRouter(7, common.RoleAdminUser)
	now := common.GetTimestamp()
	const path = "/api/qy/admin/stardust/ledger-check"

	// 701 自洽:gross 5.4 = 发出的 5 + carry 0.4,I2 在 |diff| < 1 内闭合。
	_, err := Credit(env.ext, Posting{UserId: 701, Kind: KindLotPrize, Amount: 10, IdemScope: "t", IdemKey: "p"})
	require.NoError(t, err)
	rebate, err := Credit(env.ext, Posting{UserId: 701, Kind: KindConsumeRebate, Amount: 5, IdemScope: "t", IdemKey: "r"})
	require.NoError(t, err)
	seedAccrual(t, env.ext, 701, invite.DayKey(now-86400), AccrualSettled, "5.4", rebate.LedgerId)
	require.NoError(t, env.ext.Model(&Balance{}).Where("user_id = ?", 701).Update("carry", "0.4").Error)
	// computed 桶不进 I2。
	seedAccrual(t, env.ext, 701, invite.DayKey(now), AccrualComputed, "100", 0)

	data := dataOf(t, call(t, r, http.MethodGet, path, ""))
	assert.Equal(t, true, data["ok"])
	assert.EqualValues(t, 1, data["checked_users"])
	assert.EqualValues(t, 0, data["drifted_users"])
	assert.EqualValues(t, 0, data["worst_user_id"])
	assert.Equal(t, "0", data["worst_drift"])
	assert.EqualValues(t, 0, data["held_rows"])
	assert.Equal(t, "", data["oldest_held_day"])
	assert.Equal(t, false, data["held_alert"])

	// 702:有人直接把 available 改成 999;703:余额行被删了。
	_, err = Credit(env.ext, Posting{UserId: 702, Kind: KindLotPrize, Amount: 10, IdemScope: "t", IdemKey: "q"})
	require.NoError(t, err)
	require.NoError(t, env.ext.Model(&Balance{}).Where("user_id = ?", 702).Update("available", 999).Error)
	_, err = Credit(env.ext, Posting{UserId: 703, Kind: KindLotPrize, Amount: 3, IdemScope: "t", IdemKey: "s"})
	require.NoError(t, err)
	require.NoError(t, env.ext.Where("user_id = ?", 703).Delete(&Balance{}).Error)
	// 两个 held 桶,最老的 30 天前。
	oldest := invite.DayKey(now - 30*86400)
	seedAccrual(t, env.ext, 701, oldest, AccrualHeld, "1", 0)
	seedAccrual(t, env.ext, 702, invite.DayKey(now-2*86400), AccrualHeld, "1", 0)

	data = dataOf(t, call(t, r, http.MethodGet, path, ""))
	assert.Equal(t, true, data["ok"])
	assert.EqualValues(t, 2, data["checked_users"], "703 没有余额行,不在核对集合里,但计入漂移")
	assert.EqualValues(t, 2, data["drifted_users"])
	assert.EqualValues(t, 702, data["worst_user_id"])
	assert.Equal(t, "989", data["worst_drift"])
	assert.EqualValues(t, 2, data["held_rows"])
	assert.Equal(t, oldest, data["oldest_held_day"])
	assert.Equal(t, true, data["held_alert"], "最老的 held 桶 30 天 ≥ 阈值 7 天")

	// 阈值改成 0 = 不告警,而积龄数字照常下发。
	require.NoError(t, saveOverrides(context.Background(), map[string]string{keyHeldAlertDays: "0"}, 1))
	data = dataOf(t, call(t, r, http.MethodGet, path, ""))
	assert.Equal(t, false, data["held_alert"])
	assert.Equal(t, oldest, data["oldest_held_day"])
	assert.EqualValues(t, 2, data["drifted_users"], "体检只读,跑几遍结果都一样")
}
