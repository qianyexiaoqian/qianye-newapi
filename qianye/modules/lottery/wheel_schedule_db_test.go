package lottery

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 转盘像抽卡卡池:运营只填「开始 / 结束」。draw_at 不填(0)时由后端按
// 「结束 + reveal_delay_seconds」派生;填了照旧按通用规则校验,派生只补空。
func TestWheelScheduleDerivesDrawAtFromCloseAt(t *testing.T) {
	env := newWheelEnv(t, nil)
	env.user = wheelAdminId

	code, body := env.call(t, http.MethodPost, "/admin/lottery/activities",
		wheelCreateBody(t, func(m map[string]any) { delete(m, "draw_at") }))
	require.Equalf(t, http.StatusOK, code, "不填 draw_at 的转盘草稿应当创建成功: %s", body)
	act, err := loadActivityAny(context.Background(), env.ext, jsonString(t, body, "data", "act_no"))
	require.NoError(t, err)
	assert.Equal(t, act.CloseAt+int64(config.Get().Lottery.RevealDelaySeconds), act.DrawAt,
		"draw_at 必须等于结束时间加强制间隔")
	assert.EqualValues(t, 0, act.SettleDeadline, "转盘没有结算截止")

	// 明确给出且早于结束时间的 draw_at 仍然被通用规则拒绝:派生不是"随便填都行"。
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities",
		wheelCreateBody(t, func(m map[string]any) { m["draw_at"] = m["close_at"].(int64) - 1 }))
	assert.Equalf(t, http.StatusBadRequest, code, "早于结束时间的 draw_at 应当 400: %s", body)
}

// ─────────────────────────── 发布后改排期(api_admin_schedule.go)───────────────────────────

// scheduleBody 拼一次「改排期」的请求体。
func scheduleBody(t *testing.T, openAt, closeAt int64) string {
	t.Helper()
	raw, err := common.Marshal(map[string]any{"open_at": openAt, "close_at": closeAt})
	require.NoError(t, err)
	return string(raw)
}

// 转盘的承诺原像不含排期:同一场转盘换三个时刻,承诺一个字节都不变;批次玩法照旧变。
func TestWheelCommitIgnoresSchedule(t *testing.T) {
	const seed = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	base := Activity{
		ActNo: "LOTTESTACT01", Kind: KindDraw, Algo: AlgoV2, DrawMode: DrawModeWheel,
		RulesHash: "rh", SpecHash: "sh", StakeQuota: 100,
		OpenAt: 1_800_000_000, CloseAt: 1_800_003_600, DrawAt: 1_800_003_601,
	}
	moved := base
	moved.OpenAt, moved.CloseAt, moved.DrawAt = 1_700_000_000, 1_900_000_000, 1_900_000_001
	assert.Equal(t, CommitHashV2(&base, nil, seed), CommitHashV2(&moved, nil, seed),
		"转盘的 open_at / close_at / draw_at 不进承诺原像")

	prob, probMoved := base, moved
	prob.DrawMode, probMoved.DrawMode = DrawModeProb, DrawModeProb
	assert.NotEqual(t, CommitHashV2(&prob, nil, seed), CommitHashV2(&probMoved, nil, seed),
		"批次玩法的时刻仍然钉在承诺里")
	assert.NotEqual(t, CommitHashV2(&base, nil, seed), CommitHashV2(&prob, nil, seed),
		"draw_mode 分量在前:两种原像形状不可能互相重放")
}

// 改排期:三列变、承诺不变、事件行与审计各一条;改完转动按新窗口判;改完 close_at
// 之后 runLock 按新时间封盘;封盘后揭示的承诺校验仍然 PASS —— 这就是把排期从
// 原像里拿出来的全部意义。
func TestWheelScheduleUpdateKeepsCommitAndDrivesLifecycle(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	actNo := env.publishWheel(t)
	act := *loadActByNo(t, env.ext, actNo)
	require.NotEmpty(t, act.CommitHash)

	env.user = wheelUserA
	code, body := env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "s-1", "a"))
	require.Equalf(t, http.StatusOK, code, "%s", body)

	now := common.GetTimestamp()
	openAt, closeAt := now-30, now+7200
	env.user = wheelAdminId
	code, body = env.call(t, http.MethodPut, "/admin/lottery/activities/"+actNo+"/schedule", scheduleBody(t, openAt, closeAt))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	assert.Equal(t, StatusPublished, jsonString(t, body, "data", "status"))

	after := loadAct(t, env.ext, act.Id)
	assert.Equal(t, openAt, after.OpenAt)
	assert.Equal(t, closeAt, after.CloseAt)
	assert.Equal(t, closeAt+int64(config.Get().Lottery.RevealDelaySeconds), after.DrawAt, "draw_at 重新派生")
	assert.EqualValues(t, 0, after.SettleDeadline)
	assert.Equal(t, act.CommitHash, after.CommitHash, "承诺一个字节都不动")
	assert.Equal(t, StatusPublished, after.Status)

	var events []Event
	require.NoError(t, env.ext.Where("act_id = ? AND action = ?", act.Id, ActionScheduleChanged).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, StatusPublished, events[0].FromStatus)
	assert.Equal(t, StatusPublished, events[0].ToStatus)
	assert.Equal(t, qymodel.ActorAdmin, events[0].ActorType)
	assert.Equal(t, wheelAdminId, events[0].ActorUserId)
	assert.Contains(t, events[0].Detail, `"before":{"close_at":`+strconv.FormatInt(act.CloseAt, 10))
	assert.Contains(t, events[0].Detail, `"after":{"close_at":`+strconv.FormatInt(closeAt, 10))

	var audits []qymodel.AuditLog
	require.NoError(t, env.ext.Where("action = ? AND trace_no = ?", "lottery.activity.schedule", actNo).Find(&audits).Error)
	require.Len(t, audits, 1)
	assert.Equal(t, qymodel.ResultOK, audits[0].Result)
	assert.Contains(t, audits[0].BeforeSnap, `"open_at":`+strconv.FormatInt(act.OpenAt, 10))
	assert.Contains(t, audits[0].BeforeSnap, `"draw_at":`+strconv.FormatInt(act.DrawAt, 10))
	assert.Contains(t, audits[0].AfterSnap, `"close_at":`+strconv.FormatInt(closeAt, 10))
	assert.Contains(t, audits[0].AfterSnap, `"draw_at":`+strconv.FormatInt(after.DrawAt, 10))

	// 改完还能转:时间窗读的是活动行此刻的值。
	env.user = wheelUserA
	code, body = env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "s-2", "b"))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	assert.Equal(t, 2, decodeSpin(t, body).Seq)

	// 把结束时间改到下一秒,等钟走过去,runLock 按新时间封盘。
	env.user = wheelAdminId
	closeSoon := common.GetTimestamp() + 1
	code, body = env.call(t, http.MethodPut, "/admin/lottery/activities/"+actNo+"/schedule", scheduleBody(t, openAt, closeSoon))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	runLock(context.Background())
	assert.Equal(t, StatusPublished, loadAct(t, env.ext, act.Id).Status, "还没到点不封")
	for common.GetTimestamp() < closeSoon {
		time.Sleep(50 * time.Millisecond)
	}
	runLock(context.Background())
	locked := loadAct(t, env.ext, act.Id)
	require.Equal(t, StatusLocked, locked.Status, "到了新的 close_at 就封盘")
	assert.Equal(t, closeSoon, locked.CloseAt)
	assert.Equal(t, 2, locked.RosterCount)

	// 封盘之后排期不能再动。
	code, body = env.call(t, http.MethodPut, "/admin/lottery/activities/"+actNo+"/schedule", scheduleBody(t, openAt, closeAt))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_wheel_schedule_locked", errorCode(t, body))

	// 揭示:承诺校验用的是活动行上**改过的**三个时刻 —— 它们不在原像里,所以照样 PASS。
	require.NoError(t, revealActivity(context.Background(), env.ext, loadAct(t, env.ext, act.Id)))
	revealed := loadAct(t, env.ext, act.Id)
	assert.Equal(t, StatusSettling, revealed.Status)
	assert.Equal(t, OutcomeDrawn, revealed.Outcome)
	assert.NotZero(t, revealed.RevealedAt)
	var refused int64
	require.NoError(t, env.ext.Model(&Flag{}).Where("act_id = ? AND code = ?", act.Id, FlagRevealRefuse).Count(&refused).Error)
	assert.Zero(t, refused, "改过排期的转盘绝不能被自己的承诺校验拒绝揭示")
}

// 「立即开始」= 同一接口 open_at=now:一场还没开放的转盘立刻能转。
func TestWheelScheduleStartNow(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	env.user = wheelAdminId
	code, body := env.call(t, http.MethodPost, "/admin/lottery/activities",
		wheelCreateBody(t, func(m map[string]any) {
			m["open_at"] = common.GetTimestamp() + 3600
			m["close_at"] = common.GetTimestamp() + 7200
			delete(m, "draw_at")
		}))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	actNo := jsonString(t, body, "data", "act_no")
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/publish", "{}")
	require.Equalf(t, http.StatusOK, code, "%s", body)
	act := loadActByNo(t, env.ext, actNo)

	env.user = wheelUserA
	code, body = env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "n-1", "a"))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_wheel_closed", errorCode(t, body))

	env.user = wheelAdminId
	code, body = env.call(t, http.MethodPut, "/admin/lottery/activities/"+actNo+"/schedule",
		scheduleBody(t, common.GetTimestamp(), act.CloseAt))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	assert.LessOrEqual(t, loadAct(t, env.ext, act.Id).OpenAt, common.GetTimestamp())

	env.user = wheelUserA
	code, body = env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "n-2", "a"))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	assert.Equal(t, 1, decodeSpin(t, body).Seq)
}

// 改排期被拒的每一种形状:非转盘、非 published(草稿 / 提前结束)、时刻不合法。
// 每一次被拒都留一条失败审计,活动行一列都不动。
func TestWheelScheduleRejections(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	env.user = wheelAdminId
	now := common.GetTimestamp()

	// 非转盘:一场概率制抽奖的草稿。四个时刻进它的承诺原像,连草稿都不走这条路。
	code, body := env.call(t, http.MethodPost, "/admin/lottery/activities",
		wheelCreateBody(t, func(m map[string]any) {
			m["draw_mode"] = DrawModeProb
			m["title"] = "概率制"
			m["prizes"] = []map[string]any{
				{"tier": 1, "name": "头奖", "amount_quota": 500, "count": 2, "prize_type": PrizeTypeQuota, "win_ppm": 300000},
			}
		}))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	probNo := jsonString(t, body, "data", "act_no")

	// 草稿转盘 + 已提前结束的转盘。
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities", wheelCreateBody(t, nil))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	draftNo := jsonString(t, body, "data", "act_no")
	lockedNo := env.publishWheel(t)
	env.user = wheelUserA
	code, body = env.call(t, http.MethodPost, "/lottery/activities/"+lockedNo+"/spins", spinBody(t, "r-1", "a"))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	env.user = wheelAdminId
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities/"+lockedNo+"/cancel", `{"reason":"提前收场"}`)
	require.Equalf(t, http.StatusOK, code, "%s", body)
	liveNo := env.publishWheel(t)
	env.user = wheelAdminId

	for _, tc := range []struct {
		name   string
		actNo  string
		body   string
		status int
		code   string
	}{
		{"非转盘", probNo, scheduleBody(t, now-30, now+7200), http.StatusConflict, "qy_lot_schedule_not_wheel"},
		{"草稿", draftNo, scheduleBody(t, now-30, now+7200), http.StatusConflict, "qy_lot_wheel_schedule_locked"},
		{"已提前结束", lockedNo, scheduleBody(t, now-30, now+7200), http.StatusConflict, "qy_lot_wheel_schedule_locked"},
		{"结束早于当前", liveNo, scheduleBody(t, now-7200, now-1), http.StatusBadRequest, "qy_lot_bad_request"},
		{"结束等于当前", liveNo, scheduleBody(t, now-7200, now), http.StatusBadRequest, "qy_lot_bad_request"},
		{"结束早于开始", liveNo, scheduleBody(t, now+7200, now+3600), http.StatusBadRequest, "qy_lot_bad_request"},
		{"缺开始", liveNo, scheduleBody(t, 0, now+3600), http.StatusBadRequest, "qy_lot_bad_request"},
		{"超出地平线", liveNo, scheduleBody(t, now, now+maxScheduleHorizonSeconds+1), http.StatusBadRequest, "qy_lot_bad_request"},
		{"请求体不合法", liveNo, `{"open_at":"x"}`, http.StatusBadRequest, "qy_lot_bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := *loadActByNo(t, env.ext, tc.actNo)
			code, body := env.call(t, http.MethodPut, "/admin/lottery/activities/"+tc.actNo+"/schedule", tc.body)
			assert.Equalf(t, tc.status, code, "%s", body)
			assert.Equal(t, tc.code, errorCode(t, body))
			after := loadAct(t, env.ext, before.Id)
			assert.Equal(t, before.OpenAt, after.OpenAt)
			assert.Equal(t, before.CloseAt, after.CloseAt)
			assert.Equal(t, before.DrawAt, after.DrawAt)
			assert.Equal(t, before.Status, after.Status)
			var events int64
			require.NoError(t, env.ext.Model(&Event{}).Where("act_id = ? AND action = ?", before.Id, ActionScheduleChanged).Count(&events).Error)
			assert.Zero(t, events)
			var audits int64
			require.NoError(t, env.ext.Model(&qymodel.AuditLog{}).
				Where("action = ? AND trace_no = ? AND result = ?", "lottery.activity.schedule", tc.actNo, qymodel.ResultFail).
				Count(&audits).Error)
			assert.Greater(t, audits, int64(0), "被拒的那次同样要留痕")
		})
	}
}

// loadActByNo 按活动号读一行。
func loadActByNo(t *testing.T, gdb *gorm.DB, actNo string) *Activity {
	t.Helper()
	var act Activity
	require.NoError(t, gdb.Where("act_no = ?", actNo).Take(&act).Error)
	return &act
}
