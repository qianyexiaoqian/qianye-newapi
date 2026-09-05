package lottery

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// wheel_lifecycle_db_test.go —— 转盘的生命周期与创建期校验。
//
// 转盘的钱在每一转当场花掉、奖当场到账,所以"全额退款"的五种收场对它**结构上不可达**:
// 人数不足流局(lockActivity)、整场取消(handleCancelActivity)、逾期流局(runVoidExpired,
// 只扫竞猜)、全部猜错 / 无对手盘(settleGuessResult,只对竞猜)。这一组用例逐条证明
// 每一个写入这些 outcome 的地方对转盘都关着门,而不是只断言 isFullRefundOutcome 的取值表。

// seedWheelActivity 直接落一场已发布的转盘(带种子与奖档),供生命周期用例使用。
func seedWheelActivity(t *testing.T, ext *gorm.DB, mutate func(*Activity)) *Activity {
	t.Helper()
	act := seedActivity(t, ext, func(a *Activity) {
		a.Algo = AlgoV2
		a.DrawMode = DrawModeWheel
		a.AllowMultiWin = true
		a.RulesText = `{"min_quota":0}`
		a.RulesHash = RulesHash(`{"min_quota":0}`)
		a.ChainHead = ""
		if mutate != nil {
			mutate(a)
		}
	})
	require.NoError(t, ext.Create(&Seed{
		ActId: act.Id, Seed: newSecret(), RefSalt: newSecret(), IpSalt: newSecret(),
		CreatedAt: common.GetTimestamp(),
	}).Error)
	prizes := []Prize{
		{ActId: act.Id, Tier: 1, Name: "头奖", AmountQuota: 500, Count: 2, StockLeft: 2, PrizeType: PrizeTypeQuota, WinPpm: 300000},
		{ActId: act.Id, Tier: 2, Name: wheelNoneName, PrizeType: PrizeTypeNone, WinPpm: 700000},
	}
	require.NoError(t, ext.Create(&prizes).Error)
	lines := make([]string, 0, len(prizes))
	for _, p := range prizes {
		lines = append(lines, prizeSpecLineOf(AlgoV2, p))
	}
	commit, err := computeCommit(context.Background(), ext, act)
	require.NoError(t, err)
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Updates(map[string]any{
		"spec_text": joinSpec(lines), "spec_hash": SpecHashV2(lines),
		"commit_hash": commit, "chain_head": commit, "published_at": common.GetTimestamp(),
	}).Error)
	return loadAct(t, ext, act.Id)
}

func joinSpec(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += SEP
		}
		out += l
	}
	return out
}

// 到点封盘对转盘跳过人数不足流局:即便库里的 min_entries_to_hold 被改成正数,
// 一转都没有的转盘也只是 locked,绝不写 void_min_entries(那会把它推进全额退款)。
func TestWheelLockNeverVoidsForShortfall(t *testing.T) {
	ext := newFundTestDB(t)
	act := seedWheelActivity(t, ext, func(a *Activity) {
		a.MinEntriesToHold = 5
		a.CloseAt = common.GetTimestamp() - 1
	})
	require.NoError(t, lockActivity(context.Background(), ext, act))
	after := loadAct(t, ext, act.Id)
	assert.Equal(t, StatusLocked, after.Status)
	assert.Equal(t, OutcomeNone, after.Outcome, "转盘没有流局这一说")
	assert.NotZero(t, after.LockedAt)
	assert.NotEmpty(t, after.RosterHash, "封盘仍然冻结(空)名单:承诺照旧")
	assert.Zero(t, after.RosterCount)

	// 对照:批次玩法在同样的数据上会流局。
	prob := seedActivity(t, ext, func(a *Activity) {
		a.Algo = AlgoV2
		a.DrawMode = DrawModeProb
		a.MinEntriesToHold = 5
		a.CloseAt = common.GetTimestamp() - 1
	})
	require.NoError(t, lockActivity(context.Background(), ext, prob))
	assert.Equal(t, OutcomeVoidMinEntries, loadAct(t, ext, prob.Id).Outcome)
}

// 管理端「取消」对转盘 = 提前封盘;一转都没有时才真正作废;已封盘且有转动的转盘什么都做不了。
func TestWheelCancelIsEarlyLockUnlessNobodySpun(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	actNo := env.publishWheel(t)
	var act Activity
	require.NoError(t, env.ext.Where("act_no = ?", actNo).Take(&act).Error)

	// 有人转过:取消 = 提前封盘。
	env.user = wheelUserA
	code, body := env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "c-1", "a"))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	balance := stardustOf(t, env.ext, wheelUserA)

	env.user = wheelAdminId
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/cancel", `{"reason":"提前收场"}`)
	require.Equalf(t, http.StatusOK, code, "%s", body)
	assert.Equal(t, StatusLocked, jsonString(t, body, "data", "status"))
	locked := loadAct(t, env.ext, act.Id)
	assert.Equal(t, StatusLocked, locked.Status)
	assert.Equal(t, OutcomeNone, locked.Outcome, "绝不写 cancelled:本金已花、奖已到账,退一遍就是双付")
	assert.Empty(t, locked.CancelReason)
	assert.NotZero(t, locked.LockedAt)
	assert.Equal(t, 1, locked.RosterCount)
	assert.Equal(t, act.CloseAt, locked.CloseAt, "close_at / draw_at 不动:提前封盘不改排期,揭示按 max(draw_at, locked_at+delay)")
	assert.Equal(t, act.DrawAt, locked.DrawAt)
	var refunds int64
	require.NoError(t, env.ext.Model(&Payout{}).Where("act_id = ? AND kind = ?", act.Id, PayoutRefund).Count(&refunds).Error)
	assert.Zero(t, refunds, "绝不登记退款")
	assert.Equal(t, balance, stardustOf(t, env.ext, wheelUserA), "余额一分不动")
	var events []Event
	require.NoError(t, env.ext.Where("act_id = ? AND action = ?", act.Id, ActionLock).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Contains(t, events[0].Detail, `"early":true`)
	assert.Contains(t, events[0].Detail, "提前收场")

	// 封盘之后再点取消:什么都做不了,它只等揭示。
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/cancel", `{"reason":"再来一次"}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_wheel_no_cancel", errorCode(t, body))
	assert.Equal(t, StatusLocked, loadAct(t, env.ext, act.Id).Status)

	// runSettle 对 locked 的转盘不做任何事(它只扫 settling)。
	runSettle(context.Background())
	assert.Equal(t, StatusLocked, loadAct(t, env.ext, act.Id).Status)

	// 一转都没有的转盘:真正作废,并且能干净收尾(没有本金要退)。
	empty := env.publishWheel(t)
	var emptyAct Activity
	require.NoError(t, env.ext.Where("act_no = ?", empty).Take(&emptyAct).Error)
	env.user = wheelAdminId
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities/"+empty+"/cancel", `{"reason":"配错了"}`)
	require.Equalf(t, http.StatusOK, code, "%s", body)
	assert.Equal(t, OutcomeCancelled, jsonString(t, body, "data", "outcome"))
	cancelled := loadAct(t, env.ext, emptyAct.Id)
	assert.Equal(t, StatusSettling, cancelled.Status)
	assert.Equal(t, OutcomeCancelled, cancelled.Outcome)
	assert.NotEmpty(t, cancelled.RosterHash, "空名单也要冻结:验证脚本第 3 步要比对它")
	runSettle(context.Background())
	finished := loadAct(t, env.ext, emptyAct.Id)
	assert.Equal(t, StatusFinished, finished.Status)
	require.NoError(t, env.ext.Model(&Payout{}).Where("act_id = ?", emptyAct.Id).Count(&refunds).Error)
	assert.Zero(t, refunds)
}

// planFullRefund 对转盘是结构性的挡板:即便有人把 outcome 改成了全退的那几种,
// 也不会登记一笔退款 —— 本金已在每一转当场花掉。
func TestWheelFullRefundIsStructurallyUnreachable(t *testing.T) {
	ext := newFundTestDB(t)
	act := seedWheelActivity(t, ext, nil)
	seedSuccessEntry(t, ext, act, 31, act.StakeQuota)
	seedSuccessEntry(t, ext, act, 32, act.StakeQuota)

	for _, outcome := range []string{
		OutcomeCancelled, OutcomeVoidMinEntries, OutcomeVoidDeadline,
		OutcomeVoidNoWinner, OutcomeVoidAllCorrect,
	} {
		require.True(t, isFullRefundOutcome(outcome), outcome)
		require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
			Updates(map[string]any{"status": StatusSettling, "outcome": outcome}).Error)
		require.NoError(t, planFullRefund(context.Background(), ext, loadAct(t, ext, act.Id)))
		var refunds int64
		require.NoError(t, ext.Model(&Payout{}).Where("act_id = ? AND kind = ?", act.Id, PayoutRefund).Count(&refunds).Error)
		assert.Zerof(t, refunds, "outcome=%s 对转盘不得登记退款", outcome)
	}

	// 逾期流局只扫竞猜:一场 settle_deadline 早已过去、卡在 locked 的转盘原样不动。
	stuck := seedWheelActivity(t, ext, func(a *Activity) {
		a.Status = StatusLocked
		a.LockedAt = common.GetTimestamp() - 100
		a.SettleDeadline = common.GetTimestamp() - 10
	})
	runVoidExpired(context.Background())
	assert.Equal(t, StatusLocked, loadAct(t, ext, stuck.Id).Status)
	assert.Equal(t, OutcomeNone, loadAct(t, ext, stuck.Id).Outcome)
}

// 竞猜录结果那条路(void_no_winner / void_all_correct 的唯一写入点)对转盘关着门。
func TestWheelRejectsGuessResult(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	actNo := env.publishWheel(t)
	env.user = wheelAdminId
	code, body := env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/guess-result",
		`{"opt_no":1,"evidence":"x"}`)
	assert.Equal(t, http.StatusBadRequest, code, "%s", body)
}

// 揭示对转盘只公开种子、复核合计;文本奖计数与出款表对不上时挂起,绝不在对不上账的
// 现场上公开种子。
func TestWheelRevealSuspendsWhenTextGrantsDrift(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	actNo := env.publishWheel(t)
	var act Activity
	require.NoError(t, env.ext.Where("act_no = ?", actNo).Take(&act).Error)
	env.user = wheelUserA
	code, body := env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "r-1", "a"))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	// 直接封盘而不是改 close_at 再等 runLock:这一条测的是揭示对文本奖计数的复核,
	// 排期与它无关(排期不进转盘的承诺原像,改 close_at 再等 runLock 的那条路由
	// wheel_schedule_db_test.go 覆盖)。
	require.NoError(t, lockActivity(context.Background(), env.ext, loadAct(t, env.ext, act.Id)))
	require.Equal(t, StatusLocked, loadAct(t, env.ext, act.Id).Status)

	// 把文本奖计数改成与出款表不符。
	require.NoError(t, env.ext.Model(&Activity{}).Where("id = ?", act.Id).Update("text_grant_count", 7).Error)
	require.Error(t, revealActivity(context.Background(), env.ext, loadAct(t, env.ext, act.Id)))
	after := loadAct(t, env.ext, act.Id)
	assert.Equal(t, StatusLocked, after.Status, "对不上账就挂起,绝不推进")
	assert.Zero(t, after.RevealedAt)
	var flags []Flag
	require.NoError(t, env.ext.Where("act_id = ? AND code = ?", act.Id, FlagRevealRefuse).Find(&flags).Error)
	require.Len(t, flags, 1)
	assert.Contains(t, flags[0].Detail, "文本奖")
}

// 创建期校验:转盘的奖档是单独一套。
func TestWheelCreateValidation(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000})
	env.user = wheelAdminId
	create := func(mutate func(m map[string]any)) (int, []byte) {
		return env.call(t, http.MethodPost, "/admin/lottery/activities", wheelCreateBody(t, mutate))
	}
	prizesOf := func(m map[string]any) []map[string]any { return m["prizes"].([]map[string]any) }

	t.Run("min_entries_to_hold 必须为 0", func(t *testing.T) {
		code, body := create(func(m map[string]any) { m["min_entries_to_hold"] = 1 })
		assert.Equal(t, http.StatusBadRequest, code, "%s", body)
		assert.Contains(t, jsonString(t, body, "message"), "最低成场人数")
	})
	t.Run("win_ppm 必须落在 (0, 1e6]", func(t *testing.T) {
		code, body := create(func(m map[string]any) { prizesOf(m)[0]["win_ppm"] = 0 })
		assert.Equal(t, http.StatusBadRequest, code, "%s", body)
		code, body = create(func(m map[string]any) { prizesOf(m)[0]["win_ppm"] = 1000001 })
		assert.Equal(t, http.StatusBadRequest, code, "%s", body)
	})
	t.Run("各档概率之和不得超过 100%", func(t *testing.T) {
		code, body := create(func(m map[string]any) { prizesOf(m)[0]["win_ppm"] = 900000 })
		assert.Equal(t, http.StatusBadRequest, code, "%s", body)
		assert.Contains(t, jsonString(t, body, "message"), "100%")
	})
	t.Run("恰好 100% 时派生的 none 档概率为 0(全中转盘)", func(t *testing.T) {
		code, body := create(func(m map[string]any) { prizesOf(m)[0]["win_ppm"] = 800000 })
		require.Equalf(t, http.StatusOK, code, "%s", body)
		var none Prize
		require.NoError(t, env.ext.Where("prize_type = ?", PrizeTypeNone).Order("id desc").Take(&none).Error)
		assert.Zero(t, none.WinPpm)
		assert.Equal(t, 3, none.Tier)
	})
	t.Run("没有 prob 那条 count×amount ≥ 全场上限 的均分规则", func(t *testing.T) {
		// 1 份 × 1 星屑,全场上限 1000:prob 会 400,转盘是硬库存,合法。
		code, body := create(func(m map[string]any) {
			prizesOf(m)[0]["count"] = 1
			prizesOf(m)[0]["amount_quota"] = 1
		})
		require.Equalf(t, http.StatusOK, code, "%s", body)
		prob := wheelCreateBody(t, func(m map[string]any) {
			m["draw_mode"] = DrawModeProb
			prizesOf(m)[0]["count"] = 1
			prizesOf(m)[0]["amount_quota"] = 1
		})
		code, body = env.call(t, http.MethodPost, "/admin/lottery/activities", prob)
		assert.Equal(t, http.StatusBadRequest, code, "prob 那条均分规则仍然在: %s", body)
	})
	t.Run("回显的 none 档被丢掉重算,不影响结果", func(t *testing.T) {
		code, body := create(func(m map[string]any) {
			m["prizes"] = append(prizesOf(m), map[string]any{
				"tier": 9, "name": "过期的谢谢参与", "prize_type": PrizeTypeNone, "win_ppm": 123456,
			})
		})
		require.Equalf(t, http.StatusOK, code, "%s", body)
		var rows []Prize
		actNo := jsonString(t, body, "data", "act_no")
		var act Activity
		require.NoError(t, env.ext.Where("act_no = ?", actNo).Take(&act).Error)
		require.NoError(t, env.ext.Where("act_id = ?", act.Id).Order("tier asc").Find(&rows).Error)
		require.Len(t, rows, 3)
		assert.Equal(t, 3, rows[2].Tier)
		assert.Equal(t, wheelNoneName, rows[2].Name)
		assert.Equal(t, 500000, rows[2].WinPpm)
	})
	t.Run("非转盘手填 none 档一律拒绝", func(t *testing.T) {
		code, body := env.call(t, http.MethodPost, "/admin/lottery/activities", wheelCreateBody(t, func(m map[string]any) {
			m["draw_mode"] = DrawModeProb
			m["prizes"] = append(prizesOf(m), map[string]any{
				"tier": 3, "name": "谢谢参与", "prize_type": PrizeTypeNone, "win_ppm": 500000,
			})
		}))
		assert.Equal(t, http.StatusBadRequest, code, "%s", body)
	})
	t.Run("只有 none 档没有真实奖:拒绝", func(t *testing.T) {
		code, body := create(func(m map[string]any) {
			m["prizes"] = []map[string]any{{"tier": 1, "name": "x", "prize_type": PrizeTypeNone, "win_ppm": 1000000}}
		})
		assert.Equal(t, http.StatusBadRequest, code, "%s", body)
	})
}

// 发布闸门:转盘只能以 lot-v2 发布(v1 的链原像没有装结果的那一位)。
func TestWheelPublishRequiresV2(t *testing.T) {
	assert.Error(t, checkAlgoPublishable(&Activity{Kind: KindDraw, DrawMode: DrawModeWheel, Algo: AlgoV1}))
	assert.NoError(t, checkAlgoPublishable(&Activity{Kind: KindDraw, DrawMode: DrawModeWheel, Algo: AlgoV2}))
	// 与既有的三种一样,未登记的定档方式仍然不许发布。
	assert.Error(t, checkAlgoPublishable(&Activity{Kind: KindDraw, DrawMode: "roulette", Algo: AlgoV2}))
}

// 转盘对 pickWinnersByMode 永远不可达:真走到就是分派漏了,必须报错而不是回落成 rank。
func TestPickWinnersByModeRefusesWheel(t *testing.T) {
	_, err := pickWinnersByMode(&Activity{DrawMode: DrawModeWheel}, "final", nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wheel")
}
