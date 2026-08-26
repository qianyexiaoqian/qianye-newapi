package lottery

// ball_multi_entry_db_test.go —— 一次买多注。
//
// 这一组用例回答的是同一个问题的六个面:
//
//	N 注真的是 N 张独立的票吗          —— 序号、链环、资金单、账本流水各 N 份
//	N 注真的只扣 N × 单注吗            —— 真打主库余额,前后差自己算一遍
//	整批重放会不会重复扣费             —— 同一个 crid 再发一次,余额一个字节都不许动
//	撞上每人上限时前面几注怎么办       —— 买成的算数,没买成的一分钱不扣,并说清停在哪
//	配了冷却的活动还买不买得了多注     —— 批内不互相计时,下一次提交照旧要等
//	每一注是不是各自与开奖号比对       —— 同一次提交里的四注开出三种不同结果
//
// 走的全是真实 HTTP handler:多注是一条**接缝**功能(请求体 → 派生幂等键 →
// N 次 twophase → 一个信封),而接缝断掉时每一个纯函数都还是绿的。

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// decodeEntryBatch 把参与接口的响应信封解成回执批。
//
// 走响应**字节**而不是 handler 的返回值:前端拿到的是这些字节,字段名写错一个,
// 结构体断言照样全绿而界面上是空的。
func decodeEntryBatch(t *testing.T, body []byte) entryReceiptBatch {
	t.Helper()
	var env struct {
		Success bool              `json:"success"`
		Message string            `json:"message"`
		Data    entryReceiptBatch `json:"data"`
	}
	require.NoErrorf(t, common.Unmarshal(body, &env), "响应不是合法 JSON: %s", body)
	require.Truef(t, env.Success, "响应不是成功信封: %s", body)
	return env.Data
}

// errorCode 取失败信封里的业务码。
func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoErrorf(t, common.Unmarshal(body, &env), "响应不是合法 JSON: %s", body)
	require.Falsef(t, env.Success, "这是一个成功信封,没有业务码: %s", body)
	return env.Code
}

// activityDetailOf 打真实的详情端点,取出前端据以渲染"还能买几注"的那几个字段。
func activityDetailOf(t *testing.T, r *gin.Engine, actNo string) activityDetail {
	t.Helper()
	code, body := callJSON(t, r, http.MethodGet, "/lottery/activities/"+actNo, "")
	require.Equalf(t, http.StatusOK, code, "活动详情失败: %s", body)
	var env struct {
		Data activityDetail `json:"data"`
	}
	require.NoError(t, common.Unmarshal(body, &env))
	return env.Data
}

// seedBallActivity 直接落一场已发布的双色球,并配好种子行。
//
// 用它而不是走管理端的建活动 + 发布:这一组用例要拨的是 max_entries_per_user、
// cooldown_seconds 这类闸门,以及开奖前后的时刻 —— 走真实创建流程要为每一条
// 用例各编一份能通过全部发布期校验的表单,而它们要断言的东西一个字节都不在
// 那些校验里。带号报名与开奖仍旧走真实路径。
func seedBallActivity(t *testing.T, ext *gorm.DB, mutate func(*Activity)) *Activity {
	t.Helper()
	act := seedActivity(t, ext, func(a *Activity) {
		a.Kind = KindDraw
		a.Algo = AlgoV2
		a.DrawMode = DrawModeBall
		a.AllowMultiWin = true
		a.RulesText = `{"min_quota":0}`
		a.RulesHash = RulesHash(`{"min_quota":0}`)
		a.BallRedPool = 12
		a.BallRedPick = 3
		a.BallBluePool = 4
		a.BallBluePick = 1
		a.ChainHead = ""
		if mutate != nil {
			mutate(a)
		}
	})
	require.NoError(t, ext.Create(&Seed{
		ActId: act.Id, Seed: newSecret(), RefSalt: newSecret(), IpSalt: newSecret(),
		CreatedAt: common.GetTimestamp(),
	}).Error)
	return act
}

// entryBody 拼一次多注提交的请求体。
func entryBody(t *testing.T, crid string, picks []string) string {
	t.Helper()
	raw, err := common.Marshal(map[string]any{
		"client_request_id": crid,
		"picks":             picks,
	})
	require.NoError(t, err)
	return string(raw)
}

// ─────────────────────────── 一次买多注:钱与票 ───────────────────────────

// TestBallMultiEntryBuysEveryLine 是这条功能的主用例。
//
// 它同时钉住三件在改造前根本不存在的事实:一次提交能买 N 注、这 N 注是 N 张
// 各自独立的票(各有序号、链环、资金单、账本流水)、以及整批重放一分钱都不会
// 再扣。第三件是把批量放在服务端而不是让前端连打 N 次的**全部理由** ——
// 前端每一次点击都要自己造一个新 crid,重发就是真的多扣一笔。
func TestBallMultiEntryBuysEveryLine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeQuota:          5_000_000,
		MaxTotalPrizeQuota:     5_000_000,
		MaxActiveActivities:    16,
		MaxPrizeTiers:          8,
		MaxTotalEntriesHard:    1_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	const startQuota = 100_000
	main := newBallMainDB(t, startQuota)
	r := ballE2ERouter()

	act := seedBallActivity(t, ext, nil)
	require.EqualValues(t, 1000, act.StakeQuota)

	// 五注,末一注与第一注**完全同号**:真实彩票允许重号,两张票各自参与开奖、
	// 各自算中奖。拒绝重号会让"机选 5 注"在小号池上有可观的概率整批被顶回来。
	picks := []string{
		"01,02,03|01",
		"04,05,06|02",
		"07,08,09|03",
		"10,11,12|04",
		"01,02,03|01",
	}
	// 期望值独立算一遍,不从响应里抄。
	wantTotal := int64(len(picks)) * act.StakeQuota
	require.EqualValues(t, 5000, wantTotal)

	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "multi-1", picks))
	require.Equalf(t, http.StatusOK, code, "多注报名失败: %s", body)

	batch := decodeEntryBatch(t, body)
	require.Len(t, batch.Entries, len(picks), "买了几注就要回几份回执")
	assert.Equal(t, len(picks), batch.Requested)
	assert.Equal(t, len(picks), batch.Accepted)
	assert.Equal(t, wantTotal, batch.TotalQuota, "总额必须是 N × 单注参与费")
	assert.Empty(t, batch.FailedCode, "全部买成时不许留下一个失败码")

	// 回执必须按**提交顺序**排,而且是归一化之后的那一份:用户下一次看到这串
	// 字节是在证据链里,两处对不上他会得出"平台改了我的号"的结论。
	for i, want := range picks {
		assert.Equalf(t, want, batch.Entries[i].Pick, "第 %d 注的号对不上", i+1)
		assert.Equalf(t, i+1, batch.Entries[i].Seq, "第 %d 注的序号对不上", i+1)
		assert.EqualValues(t, act.StakeQuota, batch.Entries[i].Amount)
		assert.Equal(t, EntrySuccess, batch.Entries[i].Status)
	}

	// 链是一条,不是五条:第 i 注的 prev 必须是第 i-1 注的 chain。
	// 断掉这一条,五注里任意一注被事后删掉都不会被任何自动化环节发现。
	assert.Equal(t, act.CommitHash, batch.Entries[0].PrevHash,
		"首注的 prev_hash 必须是承诺哈希 —— 否则链的起点无法被外部验证")
	for i := 1; i < len(batch.Entries); i++ {
		assert.Equalf(t, batch.Entries[i-1].ChainHash, batch.Entries[i].PrevHash,
			"第 %d 注没有接在第 %d 注后面", i+1, i)
	}

	// ── 钱:独立算出的期望 == 实测 ──
	var buyer model.User
	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&buyer).Error)
	assert.EqualValues(t, startQuota-wantTotal, buyer.Quota,
		"主库余额必须正好少了 N × 单注参与费")

	// 五张独立的票、五张独立的资金单、五条独立的账本流水。
	var rows []Entry
	require.NoError(t, ext.Where("act_id = ?", act.Id).Order("seq asc").Find(&rows).Error)
	require.Len(t, rows, len(picks))
	idemKeys := make(map[string]bool, len(rows))
	for i, e := range rows {
		assert.Equal(t, EntrySuccess, e.Status)
		assert.Equal(t, picks[i], e.Pick)
		assert.NotEmpty(t, e.OrderNo)
		assert.LessOrEqualf(t, len(e.IdemKey), 96,
			"派生幂等键 %q 越过了 qy_lot_entry.idem_key 的列宽", e.IdemKey)
		require.Falsef(t, idemKeys[e.IdemKey], "第 %d 注复用了幂等键 %q", i+1, e.IdemKey)
		idemKeys[e.IdemKey] = true
	}

	var orders []qymodel.FundOrder
	require.NoError(t, ext.Where("idem_scope = ?", idemScopeEntry).Find(&orders).Error)
	assert.Len(t, orders, len(picks),
		"一注一张资金单 —— 合成一张 N 倍金额的单会让 RefId 指不回具体哪条明细")

	var ledger []model.Log
	require.NoError(t, main.Where("user_id = ?", ballE2EUserId).Find(&ledger).Error)
	assert.Len(t, ledger, len(picks), "每一注都要留下自己那条账本流水")

	// ── 两处展示:「我的参与」与详情页各自看得到这 N 注 ──
	code, body = callJSON(t, r, http.MethodGet, "/lottery/my/entries", "")
	require.Equalf(t, http.StatusOK, code, "我的参与失败: %s", body)
	var mine struct {
		Data struct {
			Items []myEntryView `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(body, &mine))
	require.Len(t, mine.Data.Items, len(picks), "买了 N 注,「我的参与」就要列出 N 行")

	detail := activityDetailOf(t, r, act.ActNo)
	assert.Equal(t, len(picks), detail.MyEntryCount)
	require.Len(t, detail.MyTickets, len(picks), "详情页要能逐注看到自己的号")
	assert.Equal(t, defaultPicksPerRequest, detail.MaxPicksPerRequest,
		"没配过的活动下发的是默认 10,不是 0 也不是硬顶")
	assert.Nil(t, detail.MyEntriesRemaining,
		"本场没有每人上限,「还能买几注」必须是 null 而不是 0 —— 0 的意思是买满了")
	for i, ticket := range detail.MyTickets {
		assert.Equalf(t, picks[i], ticket.Pick, "详情页第 %d 注的号对不上", i+1)
	}

	// ── 整批重放:一分钱都不许再扣 ──
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "multi-1", picks))
	require.Equalf(t, http.StatusOK, code, "重放失败: %s", body)
	replay := decodeEntryBatch(t, body)
	require.Len(t, replay.Entries, len(picks))
	assert.Equal(t, wantTotal, replay.TotalQuota)
	for i := range replay.Entries {
		assert.Equalf(t, batch.Entries[i].EntryNo, replay.Entries[i].EntryNo,
			"重放必须拿回**原来那张票**,而不是新开一张")
	}

	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&buyer).Error)
	assert.EqualValues(t, startQuota-wantTotal, buyer.Quota,
		"重放之后余额必须一个字节都没动 —— 这是把批量放在服务端的全部理由")

	var afterReplay int64
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&afterReplay).Error)
	assert.EqualValues(t, len(picks), afterReplay, "重放不许多出一张票")
}

// TestBallMultiEntryStopsAtPerUserCap 钉住"撞上每人上限"的三件事:
// 前面几注算数、后面几注一分钱不扣、以及详情页在**按下确认之前**就说得出
// "你还能买几注"。
//
// 提前提示这一条是这条用例的重点:改造前 max_entries_per_user 只在活动行锁内
// 判定,用户唯一知道自己超了的方式是提交完被顶回来 —— 单注时那是一次白点击,
// 多注时那是"我选了十注,凭什么只买成三注"。
func TestBallMultiEntryStopsAtPerUserCap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8, EntryCloseGraceSeconds: 0,
		RevealDelaySeconds: 0, MaxStakeQuota: 5_000_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	const startQuota = 100_000
	main := newBallMainDB(t, startQuota)
	r := ballE2ERouter()

	act := seedBallActivity(t, ext, func(a *Activity) { a.MaxEntriesPerUser = 3 })

	before := activityDetailOf(t, r, act.ActNo)
	require.NotNil(t, before.MyEntriesRemaining, "配了每人上限就必须说得出还能买几注")
	assert.Equal(t, 3, *before.MyEntriesRemaining,
		"一注都没买时,「还能买几注」就是每人上限本身")

	picks := []string{
		"01,02,03|01", "04,05,06|02", "07,08,09|03", "10,11,12|04", "01,05,09|02",
	}
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "cap-1", picks))
	require.Equalf(t, http.StatusOK, code, "部分成交必须是 200 而不是 409: %s", body)

	batch := decodeEntryBatch(t, body)
	assert.Equal(t, 5, batch.Requested)
	assert.Equal(t, 3, batch.Accepted, "每人上限 3,第 4 注起必须停下")
	require.Len(t, batch.Entries, 3)
	assert.EqualValues(t, 3*act.StakeQuota, batch.TotalQuota,
		"总额必须按**买成的注数**算,不是按提交的注数")
	assert.Equal(t, "qy_lot_user_cap", batch.FailedCode,
		"停在哪一注、为什么停,必须说得出来 —— 说不出来的部分成交比失败更难查")
	assert.NotEmpty(t, batch.FailedMessage)

	var buyer model.User
	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&buyer).Error)
	assert.EqualValues(t, startQuota-3*act.StakeQuota, buyer.Quota,
		"没买成的那两注一分钱都不许扣")

	after := activityDetailOf(t, r, act.ActNo)
	require.NotNil(t, after.MyEntriesRemaining)
	assert.Equal(t, 0, *after.MyEntriesRemaining, "买满之后「还能买几注」是 0")
}

// TestBallMultiEntryCooldownCountsOneSubmissionOnce 钉住冷却的批内豁免。
//
// 没有它,任何配了 cooldown_seconds 的活动都**买不了第二注**:同一次提交里的
// 相邻两注只隔几毫秒。而豁免的边界必须窄到只覆盖"同一次提交" ——
// 所以这条用例的后半段同样重要:换一个 crid 再提交一次,照旧要等。
func TestBallMultiEntryCooldownCountsOneSubmissionOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8, EntryCloseGraceSeconds: 0,
		RevealDelaySeconds: 0, MaxStakeQuota: 5_000_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	main := newBallMainDB(t, 100_000)
	r := ballE2ERouter()

	act := seedBallActivity(t, ext, func(a *Activity) { a.CooldownSeconds = 600 })

	picks := []string{"01,02,03|01", "04,05,06|02", "07,08,09|03"}
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "cool-1", picks))
	require.Equalf(t, http.StatusOK, code, "一次提交里的多注不该被冷却拦住: %s", body)
	batch := decodeEntryBatch(t, body)
	assert.Equal(t, 3, batch.Accepted, "同一次提交是一个动作,批内不再互相计时")
	assert.Empty(t, batch.FailedCode)

	var buyer model.User
	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&buyer).Error)
	assert.EqualValues(t, 100_000-3*act.StakeQuota, buyer.Quota)

	// 换一个 crid 再来一注:这是**下一次**提交,冷却照旧要等。
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "cool-2", []string{"10,11,12|04"}))
	require.Equalf(t, http.StatusConflict, code, "下一次提交必须仍然被冷却拦住: %s", body)
	assert.Equal(t, "qy_lot_cooldown", errorCode(t, body))

	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&buyer).Error)
	assert.EqualValues(t, 100_000-3*act.StakeQuota, buyer.Quota,
		"被冷却拦下的那一注不许扣钱")
}

// TestBallMultiEntryPayPasswordJudgesTotal 钉住"验密阈值判的是整批总额"。
//
// 按单注判等于给出一条把余额烧光的绕路:阈值 2500、单注 1000 时,一次三注
// 买走 3000 而一次密码都不问。这条用例先证明单注确实低于阈值(所以放行),
// 再证明同样的单注凑成三注就必须验密 —— 少了前半段,后半段可能只是因为
// 阈值配得太低而恒真。
func TestBallMultiEntryPayPasswordJudgesTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8, EntryCloseGraceSeconds: 0,
		RevealDelaySeconds: 0, MaxStakeQuota: 5_000_000,
		PayPasswordThresholdQuota: 2500,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}, &paypass.PayPassword{}))
	main := newBallMainDB(t, 100_000)
	r := ballE2ERouter()

	act := seedBallActivity(t, ext, nil)
	require.EqualValues(t, 1000, act.StakeQuota, "单注 1000 < 阈值 2500,三注 3000 > 阈值")

	// 单注:低于阈值,连问都不问。
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "pw-1", []string{"01,02,03|01"}))
	require.Equalf(t, http.StatusOK, code, "单注低于阈值不该触发验密: %s", body)

	// 三注:总额越过阈值,必须验密。用户没设过支付密码,于是被引导去设置 ——
	// 关键是它**没有扣钱**,而不是具体哪一个码。
	quotaBefore := userQuota(t, main)
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries",
		entryBody(t, "pw-2", []string{"04,05,06|02", "07,08,09|03", "10,11,12|04"}))
	require.NotEqualf(t, http.StatusOK, code,
		"三注总额越过阈值却一次密码都没问 —— 这是一条把余额烧光的绕路: %s", body)
	assert.Contains(t, []string{"qy_pay_pwd_not_set", "qy_pay_pwd_required"},
		errorCode(t, body))
	assert.Equal(t, quotaBefore, userQuota(t, main), "验密没过的提交不许扣钱")
}

func userQuota(t *testing.T, main *gorm.DB) int {
	t.Helper()
	var u model.User
	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&u).Error)
	return u.Quota
}

// TestBallMultiEntryRequestIdBoundary 钉住 client_request_id 的两条边界。
//
// 它们必须**同时**成立,而且是两个不同的数:客户端能带 64 位(超一位 400),
// 而 ChargeEntry 自己认的是列宽反推出来的 66 位 —— 因为第 i 注的派生键会比
// 客户端那一份长两位。合成一个数的下场:要么 64 位的合法 crid 一买多注就被
// 自己的校验顶回来,要么用户输入的上界被悄悄放宽两位而列宽算错。
func TestBallMultiEntryRequestIdBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8, EntryCloseGraceSeconds: 0,
		RevealDelaySeconds: 0, MaxStakeQuota: 5_000_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	newBallMainDB(t, 100_000)
	r := ballE2ERouter()
	act := seedBallActivity(t, ext, nil)

	atCap := ""
	for len(atCap) < maxClientRequestID {
		atCap += "z"
	}
	picks := []string{"01,02,03|01", "04,05,06|02"}

	// 恰好 64 位:必须受理,而且第 2 注的派生键(66 位)也要被 ChargeEntry 认。
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, atCap, picks))
	require.Equalf(t, http.StatusOK, code, "恰好到上界的 crid 被拒了: %s", body)
	assert.Equal(t, 2, decodeEntryBatch(t, body).Accepted,
		"派生键比客户端那一份长两位,ChargeEntry 的上界必须容得下它")

	// 超一位:400,而且**一注都不许扣钱**。
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, atCap+"z", picks))
	require.Equalf(t, http.StatusBadRequest, code, "超长 crid 必须 400: %s", body)
	assert.Equal(t, "qy_lot_bad_request_id", errorCode(t, body))

	var count int64
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&count).Error)
	assert.EqualValues(t, len(picks), count, "被 400 顶回去的那一批不许落下任何一张票")
}

// ─────────────────────── 每一注各自开奖、各自中奖 ───────────────────────

// TestBallMultiEntryDrawsEachLineIndependently 证明同一次提交买下的 N 注是
// N 张**各自与开奖号比对**的票,而不是一张下注额 N 倍的票。
//
// 号池取红 3 选 2、蓝 1 选 1:三种红球组合两两相交,所以无论摇出哪一组,
// 三张不同号的票必然开出"一张全中 + 两张中一半"——**与种子无关的确定结局**,
// 用例因此不需要去凑一个能中奖的号,也不会因为换了随机源而偶发地红。
// 第四注与第一注同号,用来钉住重号:两张票各自定档、各自出款。
func TestBallMultiEntryDrawsEachLineIndependently(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8, EntryCloseGraceSeconds: 0,
		RevealDelaySeconds: 0, MaxStakeQuota: 5_000_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	newBallMainDB(t, 100_000)
	r := ballE2ERouter()

	now := common.GetTimestamp()
	specs := []PrizeSpec{
		{Tier: 1, Name: "一等奖", PrizeType: PrizeTypeQuota, RedMatch: 2, BlueMatch: 1, PoolShareBps: 5000},
		{Tier: 2, Name: "二等奖", PrizeType: PrizeTypeQuota, RedMatch: 1, BlueMatch: 1, AmountQuota: 500, Count: 20},
	}
	specLines := make([]string, 0, len(specs))
	for _, s := range specs {
		specLines = append(specLines, PrizeSpecLineV2(s))
	}

	// close_at / draw_at 先按"已经到点"落库,因为它们进承诺原像:承诺算完之后
	// 再把 close_at 临时拨到未来收报名,收完拨回来 —— 这样封盘时刻与承诺里的
	// 是同一个数,揭示那一步的承诺复算才过得去。
	act := seedBallActivity(t, ext, func(a *Activity) {
		a.Status = StatusDraft
		a.Title = "双色球多注期"
		a.OpenAt = now - 3600
		a.CloseAt = now - 2
		a.DrawAt = now - 1
		a.SettleDeadline = now + 86400
		a.BallRedPool = 3
		a.BallRedPick = 2
		a.BallBluePool = 1
		a.BallBluePick = 1
		a.PoolOpenQuota = 100_000
		a.PoolSeedQuota = 100_000
		a.PoolShareBps = 7000
		a.SpecHash = SpecHashFor(AlgoV2, specLines)
		a.SpecText = joinSep(specLines)
		a.CommitHash = ""
	})
	prizes := make([]Prize, 0, len(specs))
	for _, s := range specs {
		prizes = append(prizes, Prize{
			ActId: act.Id, Tier: s.Tier, Name: s.Name, PrizeType: PrizeTypeQuota,
			AmountQuota: s.AmountQuota, Count: s.Count,
			RedMatch: s.RedMatch, BlueMatch: s.BlueMatch, PoolShareBps: s.PoolShareBps,
		})
	}
	require.NoError(t, ext.Create(&prizes).Error)

	commit, err := computeCommit(context.Background(), ext, act)
	require.NoError(t, err)
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
		Updates(map[string]any{
			"status": StatusPublished, "commit_hash": commit, "chain_head": commit,
			"published_at": now, "close_at": now + 3600,
		}).Error)

	// 三种红球组合各一注,外加一注与首注同号。
	picks := []string{"01,02|01", "01,03|01", "02,03|01", "01,02|01"}
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "draw-1", picks))
	require.Equalf(t, http.StatusOK, code, "多注报名失败: %s", body)
	batch := decodeEntryBatch(t, body)
	require.Len(t, batch.Entries, len(picks))

	// 封盘 → 开奖。close_at 拨回承诺里的那个值。
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now-2).Error)
	runLock(context.Background())
	require.Equal(t, StatusLocked, loadAct(t, ext, act.Id).Status)
	runReveal(context.Background())

	drawn := loadAct(t, ext, act.Id)
	require.Equal(t, OutcomeDrawn, drawn.Outcome, "这一期必须真的开出号来")
	require.NotEmpty(t, drawn.BallResult)

	// 独立复算:用公开的种子自己摇一遍,再自己给每一注定一次档。
	seedHex, err := loadSeedForReveal(context.Background(), ext, act.Id)
	require.NoError(t, err)
	final := FinalSeed(drawn.ActNo, seedHex, drawn.RosterHash, drawn.RosterCount, drawn.Algo)
	reds := BallDraw(final, drawn.ActNo, BallColorRed, drawn.BallRedPool, drawn.BallRedPick)
	blues := BallDraw(final, drawn.ActNo, BallColorBlue, drawn.BallBluePool, drawn.BallBluePick)
	require.Equal(t, drawn.BallResult, BallResultText(reds, blues),
		"独立摇出来的号必须与平台公布的逐字一致")

	tiers := ballTiersOf(prizes)
	wantTier := make(map[string]int, len(picks))
	byPick := make([]int, len(picks))
	for i, p := range picks {
		tier, _, _ := MatchTier(reds, blues, p, tiers)
		wantTier[batch.Entries[i].EntryNo] = tier
		byPick[i] = tier
	}
	// 下面这个分布由号池的算术保证,与摇出哪一组红球无关 —— 用例因此不需要去凑
	// 一个能中奖的号,也不会因为换了随机源而偶发地红:
	// 红 3 选 2 只有三种组合,它们两两必然相交,所以开出的那一组恰好命中三种
	// 里的一种(全中),另外两种各中一半,一注都不会颗粒无收。
	distinct := map[int]int{byPick[0]: 1}
	distinct[byPick[1]]++
	distinct[byPick[2]]++
	require.Equal(t, 1, distinct[1],
		"三种不同号里必须恰有一注全中 —— 同一次提交买下的三注开出了两种不同结果,"+
			"这正是「每一注各自与开奖号比对」的形状")
	require.Equal(t, 2, distinct[2], "另外两注各中一半")
	require.Zero(t, distinct[0], "这个号池下不可能有一注颗粒无收")
	// 重号跟着首注走:同号必同档,这是"号码本身决定结果"的直接推论。
	require.Equal(t, byPick[0], byPick[3], "同一次提交里的两张同号票必须定同一档")

	// 系统登记的出款必须逐条对上:一注一行,各自的档位与独立复算一致。
	var payouts []Payout
	require.NoError(t, ext.Where("act_id = ? AND kind = ?", act.Id, PayoutPrize).
		Find(&payouts).Error)
	require.Len(t, payouts, len(picks),
		"四注中奖就要有四行出款 —— 少一行说明多注被当成了一张票")

	byEntryId := make(map[int64]Payout, len(payouts))
	for _, p := range payouts {
		byEntryId[p.EntryId] = p
	}
	var rows []Entry
	require.NoError(t, ext.Where("act_id = ?", act.Id).Order("seq asc").Find(&rows).Error)
	require.Len(t, rows, len(picks))
	for i, e := range rows {
		p, ok := byEntryId[e.Id]
		require.Truef(t, ok, "第 %d 注(%s)没有自己的出款行", i+1, e.Pick)
		assert.Equalf(t, wantTier[e.EntryNo], p.Tier,
			"第 %d 注(%s)的档位与独立复算不一致", i+1, e.Pick)
		assert.Positivef(t, p.AmountQuota, "第 %d 注中了却拿 0", i+1)
	}
	// 同号的两注必须**各拿一份**,而不是合并成一份。
	assert.Equal(t, byEntryId[rows[0].Id].Tier, byEntryId[rows[3].Id].Tier,
		"同一次提交里的两张同号票必须定同一档")
	assert.NotEqual(t, byEntryId[rows[0].Id].PayoutNo, byEntryId[rows[3].Id].PayoutNo,
		"同号的两注是两张票,各有自己的一份奖")

	// 详情页要能逐注显示各自的命中情况。
	detail := activityDetailOf(t, r, act.ActNo)
	require.Len(t, detail.MyTickets, len(picks))
	for i, ticket := range detail.MyTickets {
		assert.Equalf(t, picks[i], ticket.Pick, "详情页第 %d 注的号对不上", i+1)
		assert.Equalf(t, wantTier[ticket.EntryNo], ticket.WonTier,
			"详情页第 %d 注的中奖档位对不上", i+1)
	}
}

func joinSep(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += SEP
		}
		out += l
	}
	return out
}

// ─────────────────────────── 受理边界 ───────────────────────────

// TestAcceptPickList 钉住"这一次到底要买几注"的全部判据。
//
// 它是一个纯函数,但它决定了会被扣几笔钱 —— 而三条判据里有两条(同时带
// pick 与 picks、超过单次上限)在改造前根本不存在,静默择一或静默截断的后果
// 都是用户买到的不是他写的那几组号。
//
// 单次上限现在是**活动级**的,所以每一条用例都要显式给出这一场的 cap:
// 一个写死 10 的判定在配了 999 的活动上会把用户合法的第 11 注顶回来,
// 而在配了 1 的活动上会放过第 2 注。
func TestAcceptPickList(t *testing.T) {
	picks := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "01,02,03|01"
		}
		return out
	}

	cases := []struct {
		name     string
		cap      int
		req      entryRequest
		want     []string
		wantCode string
	}{
		{
			// 旧客户端与"只买一注"走的都是这一条:picks 缺席时选号仍取 pick。
			name: "缺 picks 时回落到单注",
			cap:  defaultPicksPerRequest,
			req:  entryRequest{Pick: "01,02,03|01"},
			want: []string{"01,02,03|01"},
		},
		{
			// 空数组与缺席在 wire 上不可区分(Go 里都是 len == 0),按同一条处理。
			name: "空 picks 等同缺席",
			cap:  defaultPicksPerRequest,
			req:  entryRequest{Picks: []string{}, Pick: "07,08,09|03"},
			want: []string{"07,08,09|03"},
		},
		{
			// 非双色球不带号:空串照样是"一注",由 acceptPick 决定它合不合法。
			name: "非双色球的空号仍是一注",
			cap:  defaultPicksPerRequest,
			req:  entryRequest{},
			want: []string{""},
		},
		{
			name: "多注原样带出,重号不合并",
			cap:  defaultPicksPerRequest,
			req:  entryRequest{Picks: []string{"01,02,03|01", "01,02,03|01"}},
			want: []string{"01,02,03|01", "01,02,03|01"},
		},
		{
			name: "恰好到默认上限",
			cap:  defaultPicksPerRequest,
			req:  entryRequest{Picks: picks(defaultPicksPerRequest)},
			want: picks(defaultPicksPerRequest),
		},
		{
			name:     "超过默认上限",
			cap:      defaultPicksPerRequest,
			req:      entryRequest{Picks: picks(defaultPicksPerRequest + 1)},
			wantCode: "qy_lot_too_many_picks",
		},
		{
			// 活动配到硬顶时第 999 注必须过 —— 这正是本轮要交付的那个数。
			name: "配到硬顶时 999 注通过",
			cap:  maxPicksPerRequestHard,
			req:  entryRequest{Picks: picks(maxPicksPerRequestHard)},
			want: picks(maxPicksPerRequestHard),
		},
		{
			name:     "配到硬顶时第 1000 注仍被拒",
			cap:      maxPicksPerRequestHard,
			req:      entryRequest{Picks: picks(maxPicksPerRequestHard + 1)},
			wantCode: "qy_lot_too_many_picks",
		},
		{
			// 配 1 = "一次只能买一注",是一个合法的运营取值。此时 picks 里
			// 放两注必须被拒,而不是静默只买第一注。
			name:     "配 1 时第 2 注被拒",
			cap:      1,
			req:      entryRequest{Picks: picks(2)},
			wantCode: "qy_lot_too_many_picks",
		},
		{
			name: "配 1 时单注照常受理",
			cap:  1,
			req:  entryRequest{Picks: picks(1)},
			want: picks(1),
		},
		{
			name:     "pick 与 picks 同时提交",
			cap:      defaultPicksPerRequest,
			req:      entryRequest{Pick: "01,02,03|01", Picks: []string{"04,05,06|02"}},
			wantCode: "qy_lot_pick_conflict",
		},
		{
			// 只有空白的 pick 不算"同时提交":一个把输入框清空的前端会发
			// `"pick":""`,拒绝它等于让多注在那些客户端上恒不可用。
			name: "空白的 pick 不算冲突",
			cap:  defaultPicksPerRequest,
			req:  entryRequest{Pick: "   ", Picks: []string{"04,05,06|02"}},
			want: []string{"04,05,06|02"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := acceptPickList(tc.cap, tc.req)
			if tc.wantCode != "" {
				be, ok := AsBizError(err)
				require.Truef(t, ok, "期望一条业务错误,拿到 %v", err)
				assert.Equal(t, tc.wantCode, be.ErrCode())
				if tc.wantCode == "qy_lot_too_many_picks" {
					// 报错必须念出**这一场**的那个数。一条恒说 10 的文案在配了
					// 999 的活动上就是假话,而用户的下一个动作只能是二分试。
					assert.Containsf(t, be.Message(), strconv.Itoa(tc.cap),
						"「一次最多买 N 注」里的 N 必须是这一场的上限 %d", tc.cap)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestBatchRequestIdFitsIdemKeyColumn 钉住派生幂等键的两条硬约束:
// 第 0 注**原样沿用**客户端那一份(旧客户端的重试才命中得了原单),
// 以及最长的那一条派生键仍然装得进 qy_lot_entry.idem_key 的列宽。
//
// 后者写错的表现极其隐蔽:MySQL 在非严格模式下会静默截断,于是第 9 注与
// 第 10 注的幂等键撞成同一个 —— 用户少买一注,而没有任何一处报错。
func TestBatchRequestIdFitsIdemKeyColumn(t *testing.T) {
	// 客户端能带的最长一份。
	longest := ""
	for len(longest) < maxClientRequestID {
		longest += "a"
	}
	require.Len(t, longest, maxClientRequestID)

	assert.Equal(t, longest, batchRequestId(longest, 0),
		"第 0 注必须原样沿用客户端那一份 —— 单注提交要与改造前逐字节相同")

	seen := make(map[string]bool, maxPicksPerRequestHard)
	actNo := newActNo()
	require.Len(t, actNo, 27, "列宽反推的余量是按 act_no 27 位算的")
	for i := 0; i < maxPicksPerRequestHard; i++ {
		crid := batchRequestId(longest, i)
		require.LessOrEqualf(t, len(crid), maxEntryRequestID,
			"第 %d 注的派生键越过了 ChargeEntry 的上界", i)
		key := buildIdemKey(actNo, crid)
		require.LessOrEqualf(t, len(key), 96,
			"第 %d 注的幂等键 %q 越过了 qy_lot_entry.idem_key 的列宽", i, key)
		require.Falsef(t, seen[key], "第 %d 注的幂等键与前面某一注撞了", i)
		seen[key] = true
	}
}
