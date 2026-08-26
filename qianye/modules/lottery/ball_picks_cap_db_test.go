package lottery

// ball_picks_cap_db_test.go —— 「一次最多下多少注」做成活动可配。
//
// 项目方原话:「能下多少注默认是 10 注,可以在活动设置最多能下多少注,最高 999 注。」
//
// 这一组回答的是同一句话的六个面:
//
//	没配过的活动还是 10 吗              —— 零值 = 没配过,不是"不限"也不是"不能买"
//	配到 999 真的买得到 999 注吗        —— 真打一遍,钱与票各自数一遍
//	配到 1000 会被挡住吗                —— 硬顶在创建、改值、受理三处口径一致
//	发布之后还能不能改                  —— 能,而且每一次都写审计(它不进任何哈希原像)
//	撞上每人/全场上限时用户何时知道      —— 详情页在**按下确认之前**就说得出还能买几注
//	999 注的时间预算够不够               —— 预算按注数给,不是一次冷路径的 3 秒
//
// 999 注那条用例是本组最慢的一条(SQLite 内存库约 8 秒),但它必须真打:
// 派生幂等键的列宽、批内冷却豁免、时间预算、余额差额四件事,任何一件写错的
// 表现都是"前面几百注买成了、后面被截断",而那在小样本上永远不会出现。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const picksCapAdminId = 4301

// picksCapRouter 在用户侧的那几条之外挂上改值接口。
//
// 不去动 ballE2ERouter:那一份被整个 ball 家族共用,给它加一条只有这里用得上的
// 管理端路由,等于让别的用例的路由树与线上不一致。
func picksCapRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", picksCapAdminId); h(c) }
	}
	user := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", ballE2EUserId); h(c) }
	}
	r.PUT("/admin/lottery/activities/:act_no/picks-cap", admin(handleSetPicksCap))
	r.GET("/lottery/activities/:act_no", user(handleGetActivity))
	r.POST("/lottery/activities/:act_no/entries", user(handleCreateEntry))
	return r
}

func newPicksCapEnv(t *testing.T) *gorm.DB {
	t.Helper()
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		EntryCloseGraceSeconds: 0, RevealDelaySeconds: 0,
		MaxStakeQuota: 5_000_000, MaxTotalPrizeQuota: 5_000_000,
		MaxActiveActivities: 16, MaxPrizeTiers: 8, MaxOptions: 8,
		MaxTotalEntriesHard: 50_000,
		// 整批预算必须显式给:零值会让 entryBatchContext 只剩冷路径那一份,
		// 而这一组要证明的正是"预算按注数给"。
		EntryBatchMaxMs: 120_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	return ext
}

// newFileBackedEnv 与 newPayoutEnv + newBallMainDB 做同样的事,只把两个库换成
// 落盘的 SQLite 文件 —— 唯一的差别就是"连接掉了库还在"。
//
// 只有真的会把连接跑坏的用例需要它(见 TestEntryBatchTruncatesSafelyWhenBudgetRunsOut),
// 所以不去改那两个共用夹具:整套 ball 家族都在用它们,换成落盘会让每一条用例
// 多出一次磁盘 IO。
func newFileBackedEnv(t *testing.T, lot config.Lottery, quota int) (ext, main *gorm.DB) {
	t.Helper()
	dir := t.TempDir()
	open := func(name string) *gorm.DB {
		gdb, err := gorm.Open(sqlite.Open(filepath.Join(dir, name)), &gorm.Config{
			Logger: gormlogger.Discard,
		})
		require.NoError(t, err)
		sqlDB, err := gdb.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return gdb
	}

	ext = open("ext.db")
	require.NoError(t, ext.AutoMigrate(tables()...))
	require.NoError(t, ext.AutoMigrate(&qymodel.FundOrder{}, &qymodel.AuditLog{}))

	main = open("main.db")
	require.NoError(t, main.AutoMigrate(&model.User{}, &model.Log{}, &model.QyFundOutbox{}))
	require.NoError(t, main.Create(&model.User{
		Id: ballE2EUserId, Username: "ball-buyer", Password: "x",
		AffCode: "affbudget", Group: "default", Quota: quota,
		Status: common.UserStatusEnabled,
	}).Error)

	prevType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	prevDB, prevLogDB := model.DB, model.LOG_DB
	prevMem, prevRedis := common.MemoryCacheEnabled, common.RedisEnabled
	prevOptions := common.OptionMap
	model.DB, model.LOG_DB = main, main
	common.OptionMap = map[string]string{}
	common.MemoryCacheEnabled, common.RedisEnabled = false, false

	prevHandle := qyDBHandle.Swap(ext)
	prevHealthy := qyDBHealthy.Swap(true)
	outboxOn := true
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled: true, Lottery: lot,
		TwoPhase: config.TwoPhase{
			MainOutboxEnabled: &outboxOn, OutboxRetentionDays: 30, BatchSize: 200,
		},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		model.DB, model.LOG_DB = prevDB, prevLogDB
		common.SetMainDatabaseType(prevType)
		common.MemoryCacheEnabled, common.RedisEnabled = prevMem, prevRedis
		common.OptionMap = prevOptions
	})
	invalidateSettings()
	return ext, main
}

// picksOf 拼 n 注号(号池 12 选 3 + 4 选 1 有 880 种组合,
// 不够 999 注,所以后面必然出现重号 —— 那正是真实彩票允许的,见 acceptPickList)。
func picksOf(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		r1 := 1 + i%10
		out[i] = fmt.Sprintf("%02d,%02d,%02d|%02d", r1, r1+1, r1+2, 1+(i/10)%4)
	}
	return out
}

// ─────────────────────────── 零值语义 ───────────────────────────

// 零值 = **没配过,按默认 10 走**。不是"不限",也不是"一注都不能买"。
//
// 这一条是整个改动里唯一一处会伤到存量数据的地方:AutoMigrate 给全部已发布的
// 双色球活动补上的就是 0。读成"不限"等于一次加列把每一场的单次上限从 10 悄悄
// 抬到 999(没有任何人做过这个决定);读成"不能买"等于一次加列把它们全部变成
// 不可参与。所以三件事要一起断言:详情页说 10、第 10 注买得到、第 11 注被拒。
func TestPicksCapZeroMeansDefaultNotUnlimitedNotZero(t *testing.T) {
	ext := newPicksCapEnv(t)
	main := newBallMainDB(t, 1_000_000)
	r := picksCapRouter()

	act := seedBallActivity(t, ext, nil)
	require.Zero(t, act.MaxPicksPerRequest, "夹具必须是没配过的那一档,否则这条测了个空")

	detail := activityDetailOf(t, r, act.ActNo)
	assert.Equal(t, defaultPicksPerRequest, detail.MaxPicksPerRequest,
		"没配过的活动下发的是默认 10 —— 下发 0 会让前端把「再加一注」永久置灰")

	// 第 10 注买得到:0 不是"一注都不能买"。
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries",
		entryBody(t, "zero-1", picksOf(defaultPicksPerRequest)))
	require.Equalf(t, http.StatusOK, code, "默认 10 注必须买得到: %s", body)
	batch := decodeEntryBatch(t, body)
	assert.Equal(t, defaultPicksPerRequest, batch.Accepted)
	assert.EqualValues(t, int64(defaultPicksPerRequest)*act.StakeQuota, batch.TotalQuota)

	var buyer model.User
	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&buyer).Error)
	assert.EqualValues(t, 1_000_000-int64(defaultPicksPerRequest)*act.StakeQuota, buyer.Quota)

	// 第 11 注被拒:0 不是"不限"。
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries",
		entryBody(t, "zero-2", picksOf(defaultPicksPerRequest+1)))
	require.Equalf(t, http.StatusBadRequest, code, "没配过的活动第 11 注必须被拒: %s", body)
	assert.Equal(t, "qy_lot_too_many_picks", errorCode(t, body))

	quotaAfter := userQuota(t, main)
	assert.EqualValues(t, buyer.Quota, quotaAfter, "整批被拒时一分钱都不许扣")
}

// ─────────────────────────── 配到 999 ───────────────────────────

// 配到硬顶就真的能一次买 999 注:钱、票、链、资金单、幂等键各自数一遍。
//
// 它同时证伪四件"看起来不会错"的事:
//   - 派生幂等键 `#998` 装不进 idem_key(96)—— 装不下时 MySQL 会静默截断,
//     第 99 与第 990 注撞成同一个键,用户少买一注而没有任何一处报错;
//   - 整批共用一份冷路径预算 —— 那样第 86 注上下就会被 errBatchBudget 截断;
//   - 冷却在批内互相计时 —— 那样第 2 注就买不了;
//   - 全场上限按 999 撞线 —— 夹具的 max_total_entries 必须真的容得下。
func TestPicksCapNineNinetyNineBuysEveryLine(t *testing.T) {
	if testing.Short() {
		t.Skip("999 注真打约 8 秒,-short 下跳过")
	}
	ext := newPicksCapEnv(t)
	const startQuota = 5_000_000
	main := newBallMainDB(t, startQuota)
	r := picksCapRouter()

	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = maxPicksPerRequestHard
		a.MaxTotalEntries = 50_000
		// 冷却开着:批内豁免必须在 999 注上仍然成立。
		a.CooldownSeconds = 600
	})

	detail := activityDetailOf(t, r, act.ActNo)
	require.Equal(t, maxPicksPerRequestHard, detail.MaxPicksPerRequest)

	picks := picksOf(maxPicksPerRequestHard)
	// 期望值独立算一遍,不从响应里抄。
	wantTotal := int64(maxPicksPerRequestHard) * act.StakeQuota
	require.EqualValues(t, 999_000, wantTotal)

	started := time.Now()
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "cap999", picks))
	elapsed := time.Since(started)
	require.Equalf(t, http.StatusOK, code, "999 注提交失败: %s", body)
	t.Logf("999 注(SQLite 内存库)耗时 %s,每注 %s", elapsed, elapsed/maxPicksPerRequestHard)

	batch := decodeEntryBatch(t, body)
	assert.Equal(t, maxPicksPerRequestHard, batch.Requested)
	assert.Equal(t, maxPicksPerRequestHard, batch.Accepted,
		"一注都不许被时间预算截断 —— 截断在这里的表现就是「配得到 999、买不到 999」")
	assert.Empty(t, batch.FailedCode)
	assert.Equal(t, wantTotal, batch.TotalQuota)
	require.Len(t, batch.Entries, maxPicksPerRequestHard)

	// 钱:独立算出的期望 == 实测。
	var buyer model.User
	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&buyer).Error)
	assert.EqualValues(t, startQuota-wantTotal, buyer.Quota,
		"主库余额必须正好少了 999 × 单注参与费")

	// 票:999 张独立的票、999 个互不相同的幂等键、一条连得上的链。
	var rows []Entry
	require.NoError(t, ext.Where("act_id = ?", act.Id).Order("seq asc").Find(&rows).Error)
	require.Len(t, rows, maxPicksPerRequestHard)
	keys := make(map[string]bool, len(rows))
	for i, e := range rows {
		require.Equalf(t, picks[i], e.Pick, "第 %d 注的号对不上", i+1)
		require.Equalf(t, i+1, e.Seq, "第 %d 注的序号对不上", i+1)
		require.LessOrEqualf(t, len(e.IdemKey), 96,
			"第 %d 注的派生幂等键 %q 越过了 idem_key 的列宽", i+1, e.IdemKey)
		require.Falsef(t, keys[e.IdemKey], "第 %d 注复用了幂等键 %q", i+1, e.IdemKey)
		keys[e.IdemKey] = true
	}
	assert.Equal(t, act.CommitHash, batch.Entries[0].PrevHash)
	for i := 1; i < len(batch.Entries); i++ {
		require.Equalf(t, batch.Entries[i-1].ChainHash, batch.Entries[i].PrevHash,
			"第 %d 注没有接在第 %d 注后面", i+1, i)
	}

	var orders int64
	require.NoError(t, ext.Model(&qymodel.FundOrder{}).
		Where("idem_scope = ?", idemScopeEntry).Count(&orders).Error)
	assert.EqualValues(t, maxPicksPerRequestHard, orders,
		"一注一张资金单 —— 合成一张 999 倍金额的单会让 RefId 指不回具体哪条明细")

	// 整批重放:一分钱都不许再扣,而且拿回的必须是原来那 999 张票。
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "cap999", picks))
	require.Equalf(t, http.StatusOK, code, "999 注整批重放失败: %s", body)
	replay := decodeEntryBatch(t, body)
	require.Len(t, replay.Entries, maxPicksPerRequestHard)
	assert.Equal(t, wantTotal, replay.TotalQuota)
	for i := range replay.Entries {
		require.Equalf(t, batch.Entries[i].EntryNo, replay.Entries[i].EntryNo,
			"第 %d 注的重放拿回了一张新票", i+1)
	}
	assert.EqualValues(t, startQuota-wantTotal, userQuota(t, main),
		"整批重放之后余额必须一个字节都没动")

	var afterReplay int64
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&afterReplay).Error)
	assert.EqualValues(t, maxPicksPerRequestHard, afterReplay, "重放不许多出一张票")
}

// 配到硬顶之后第 1000 注仍然被拒,而且报错里念的是这一场的那个数。
func TestPicksCapRejectsBeyondHardCapAtEntry(t *testing.T) {
	ext := newPicksCapEnv(t)
	main := newBallMainDB(t, 5_000_000)
	r := picksCapRouter()
	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = maxPicksPerRequestHard
	})

	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries",
		entryBody(t, "over-1", picksOf(maxPicksPerRequestHard+1)))
	require.Equalf(t, http.StatusBadRequest, code, "第 1000 注必须被拒: %s", body)
	assert.Equal(t, "qy_lot_too_many_picks", errorCode(t, body))
	assert.Contains(t, string(body), strconv.Itoa(maxPicksPerRequestHard),
		"报错里必须念出这一场的上限 —— 一句「注数超出限制」只能让用户去二分试")
	assert.EqualValues(t, 5_000_000, userQuota(t, main), "整批被拒时一分钱都不许扣")
}

// 库里被写进一个越过硬顶的值时,受理端仍然只认硬顶。
//
// 接口挡得住,直接改库挡不住 —— 而一个 5000 的值会让 entryBatchContext 算出一个
// 荒唐的截止时刻。夹在读的那一处(picksCapOf)意味着无论那一列是什么,
// 受理端永远认同一个上界。
func TestPicksCapClampsOverwideColumnValueOnRead(t *testing.T) {
	ext := newPicksCapEnv(t)
	main := newBallMainDB(t, 5_000_000)
	r := picksCapRouter()
	act := seedBallActivity(t, ext, nil)
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
		Update("max_picks_per_request", 5000).Error)

	detail := activityDetailOf(t, r, act.ActNo)
	assert.Equal(t, maxPicksPerRequestHard, detail.MaxPicksPerRequest,
		"下发的必须是夹过的那个数,不是库里那个 5000")

	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries",
		entryBody(t, "clamp-1", picksOf(maxPicksPerRequestHard+1)))
	require.Equalf(t, http.StatusBadRequest, code, "库里写了 5000 也不许放行 1000 注: %s", body)
	assert.Equal(t, "qy_lot_too_many_picks", errorCode(t, body))
	assert.EqualValues(t, 5_000_000, userQuota(t, main))
}

// ─────────────────────────── 发布之后改值 ───────────────────────────

// 发布之后仍然改得动,而且改完立刻生效、每一次都写审计。
//
// 「发布后可改」是本轮的一条明确取舍:这一格不进 rules_hash / spec_hash /
// commit_hash 的任何一个原像,它不改变任何人最终能拿到几张票,也不改变开奖。
// 所以这条用例的前半段必须先证明**承诺哈希一个字节都没变** —— 否则"可改"就
// 变成了"发布后能改承诺",那是整套公正性协议的反面。
func TestPicksCapEditableAfterPublishAndDoesNotTouchCommit(t *testing.T) {
	ext := newPicksCapEnv(t)
	main := newBallMainDB(t, 5_000_000)
	r := picksCapRouter()

	act := seedBallActivity(t, ext, func(a *Activity) {
		a.Status = StatusPublished
		a.MaxPicksPerRequest = 0
	})
	require.NotEmpty(t, act.CommitHash, "夹具必须是已经有承诺的那一档")
	commitBefore, rulesBefore, specBefore := act.CommitHash, act.RulesHash, act.SpecHash

	// 改之前:默认 10,第 11 注被拒。
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "pub-1", picksOf(11)))
	require.Equalf(t, http.StatusBadRequest, code, "改之前第 11 注应当被拒: %s", body)

	code, body = callJSON(t, r, http.MethodPut,
		"/admin/lottery/activities/"+act.ActNo+"/picks-cap", `{"max_picks_per_request":50}`)
	require.Equalf(t, http.StatusOK, code, "已发布活动改单次上限失败: %s", body)

	// 三个哈希一个字节都不许动。
	var reloaded Activity
	require.NoError(t, ext.Where("id = ?", act.Id).Take(&reloaded).Error)
	assert.Equal(t, commitBefore, reloaded.CommitHash, "改这一格绝不能碰承诺哈希")
	assert.Equal(t, rulesBefore, reloaded.RulesHash)
	assert.Equal(t, specBefore, reloaded.SpecHash)
	assert.Equal(t, 50, reloaded.MaxPicksPerRequest)

	// 改完立刻生效:同样的 11 注这次买得到。
	detail := activityDetailOf(t, r, act.ActNo)
	assert.Equal(t, 50, detail.MaxPicksPerRequest)
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "pub-2", picksOf(11)))
	require.Equalf(t, http.StatusOK, code, "改完之后 11 注应当买得到: %s", body)
	assert.Equal(t, 11, decodeEntryBatch(t, body).Accepted)
	assert.EqualValues(t, 5_000_000-11*act.StakeQuota, userQuota(t, main))

	// 审计:before/after 两份快照都要说得出原始值与生效值。
	var logs []qymodel.AuditLog
	require.NoError(t, ext.Where("action = ?", "lottery.activity.picks_cap").
		Order("id asc").Find(&logs).Error)
	require.Len(t, logs, 1, "改一次就要留一条痕")
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Contains(t, logs[0].BeforeSnap, `"effective":10`,
		"before 必须说得出改之前**生效**的是 10,而不是只记一个看不懂的 0")
	assert.Contains(t, logs[0].AfterSnap, `"max_picks_per_request":50`)
}

// 改值接口的取值边界与创建时逐条一致,而且**被拒的那次也写审计**。
//
// 三处口径必须是同一个:创建(buildActivity)、改值(handleSetPicksCap)、
// 受理(acceptPickList)。任何一处松一格,运营都能配出一个界面接受、
// 另一处拒绝的值。
func TestPicksCapSetterBoundsAndAudit(t *testing.T) {
	ext := newPicksCapEnv(t)
	newBallMainDB(t, 5_000_000)
	r := picksCapRouter()
	act := seedBallActivity(t, ext, func(a *Activity) { a.Status = StatusPublished })

	cases := []struct {
		name string
		body string
		code int
		want int
	}{
		{"填 0 = 不配置", `{"max_picks_per_request":0}`, http.StatusOK, 0},
		{"填 1 = 一次只能买一注", `{"max_picks_per_request":1}`, http.StatusOK, 1},
		{"填硬顶", `{"max_picks_per_request":999}`, http.StatusOK, 999},
		{"超过硬顶", `{"max_picks_per_request":1000}`, http.StatusBadRequest, 999},
		{"负数", `{"max_picks_per_request":-1}`, http.StatusBadRequest, 999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := callJSON(t, r, http.MethodPut,
				"/admin/lottery/activities/"+act.ActNo+"/picks-cap", tc.body)
			require.Equalf(t, tc.code, code, "响应: %s", body)
			var row Activity
			require.NoError(t, ext.Where("id = ?", act.Id).Take(&row).Error)
			assert.Equal(t, tc.want, row.MaxPicksPerRequest,
				"被拒的那次不许改动库里的值")
		})
	}

	var fails int64
	require.NoError(t, ext.Model(&qymodel.AuditLog{}).
		Where("action = ? AND result = ?", "lottery.activity.picks_cap", qymodel.ResultFail).
		Count(&fails).Error)
	assert.EqualValues(t, 2, fails,
		"被拒的两次同样要留痕 —— 「有人在反复试探这一格的上界」正是要能查到的形状")
}

// 同一个 client_request_id 被并发重发时,钱只扣一份、票只有一份。
//
// 这是把批量放在服务端而不是让前端连打 N 次的**全部理由**:前端每一次点击都要
// 自己造一个新 crid,一次超时重发就是真的多扣一笔。而"重发"在真实网络上从来
// 不是"上一次结束之后才开始"—— 它与上一次并行。
//
// # 这条用例断言的是结果,不是哪一道防线挡住的
//
// 本模块对"同一次意图的多次请求"有**两道**重叠的防线,而且它们在并发与串行
// 两种时序下各自生效:
//
//	· 并发那一段:后到的那次撞上 checkCaps 里的 errEntryInFlight(本人还有未结算
//	  的参与时一律拒绝)—— 那是**资金正确性条件**,余额与名单的差额必须能归因
//	  到具体哪一笔;
//	· 串行那一段:先到的那次已经落定,后到的靠派生幂等键逐注命中原单。
//
// 所以这条用例分两段打:先并发一轮,再**串行**重放一次。少了第二段,一个把
// 派生幂等键改成每次都不同的改动照样能通过 —— 因为并发那一段是 in-flight 挡的。
func TestPicksCapConcurrentSameBatchChargesOnce(t *testing.T) {
	const startQuota = 1_000_000
	ext, main := newFileBackedEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		EntryCloseGraceSeconds: 0, RevealDelaySeconds: 0,
		MaxStakeQuota: 5_000_000, MaxTotalEntriesHard: 50_000,
		EntryBatchMaxMs: 60_000,
	}, startQuota)
	r := picksCapRouter()
	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = 20
		a.MaxTotalEntries = 50_000
	})

	const lines = 8
	body := entryBody(t, "race-1", picksOf(lines))
	post := func() int {
		req := httptest.NewRequest(http.MethodPost,
			"/lottery/activities/"+act.ActNo+"/entries", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	// ── 第一段:并发重发 ──
	const racers = 4
	codes := make([]int, racers)
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			codes[i] = post()
		}(i)
	}
	wg.Wait()

	// 每一条都必须拿到一个**明确**的回答。并发下允许出现 409(上一次还没落定),
	// 但绝不允许 500 —— 那意味着这条路径在并发下没有想清楚自己该说什么。
	for i, code := range codes {
		assert.Lessf(t, code, 500, "第 %d 条并发提交回了 %d", i+1, code)
	}
	assert.EqualValues(t, startQuota-lines*act.StakeQuota, userQuota(t, main),
		"并发重发之后余额只许少一份")

	// ── 第二段:串行重放 ──
	//
	// 此刻先到的那一批已经全部落定,in-flight 那道防线不再生效 —— 挡住这一次的
	// 只可能是派生幂等键。它是"整批重放一分钱都不多扣"这条主张的唯一支点。
	require.Equal(t, http.StatusOK, post(), "串行重放必须拿回原来那一批")
	assert.EqualValues(t, startQuota-lines*act.StakeQuota, userQuota(t, main),
		"串行重放之后余额仍然只许少一份 —— 派生幂等键必须逐注命中原单")

	var rows int64
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&rows).Error)
	assert.EqualValues(t, lines, rows, "票也只许有一份")

	var orders int64
	require.NoError(t, ext.Model(&qymodel.FundOrder{}).
		Where("idem_scope = ?", idemScopeEntry).Count(&orders).Error)
	assert.EqualValues(t, lines, orders, "资金单同样只许有一份")
}

// ─────────────────────────── 提前提示 ───────────────────────────

// 三条闸门都要在**按下确认之前**说得出剩余量,而且各说各的。
//
// 999 注一次提交会同时撞上两条既有上限:
//   - 每人参与上限(硬顶 500,因为 checkCaps 锁内一次只读得回 500 条);
//   - 全场参与上限(硬顶 50000)。
//
// 撞上时的表现是**部分成交**:前面几注真的买成了、后面的一分钱没扣。这没问题,
// 但用户不该是靠这份回执才第一次知道自己只能买那么多 —— 详情页必须先说。
func TestPicksCapDetailReportsEveryRemainingBeforeSubmit(t *testing.T) {
	ext := newPicksCapEnv(t)
	newBallMainDB(t, 5_000_000)
	r := picksCapRouter()

	t.Run("没有任何上限时两个剩余量都是 null", func(t *testing.T) {
		act := seedBallActivity(t, ext, func(a *Activity) {
			a.MaxPicksPerRequest = maxPicksPerRequestHard
			a.MaxEntriesPerUser, a.MaxAttemptsPerUser, a.MaxTotalEntries = 0, 0, 0
		})
		detail := activityDetailOf(t, r, act.ActNo)
		assert.Nil(t, detail.MyEntriesRemaining,
			"没有每人上限时必须是 null 而不是 0 —— 0 的意思是买满了")
		assert.Nil(t, detail.TotalEntriesRemaining,
			"没有全场上限时必须是 null 而不是 0 —— 0 的意思是全场已满")
		assert.Equal(t, maxPicksPerRequestHard, detail.MaxPicksPerRequest)
	})

	t.Run("每人上限撞在 999 之前", func(t *testing.T) {
		act := seedBallActivity(t, ext, func(a *Activity) {
			a.MaxPicksPerRequest = maxPicksPerRequestHard
			a.MaxEntriesPerUser = perUserCapHard
			a.MaxTotalEntries = 50_000
		})
		detail := activityDetailOf(t, r, act.ActNo)
		require.NotNil(t, detail.MyEntriesRemaining)
		assert.Equal(t, perUserCapHard, *detail.MyEntriesRemaining,
			"每人上限的硬顶是 500,它比 999 紧 —— 用户必须在选号之前就看到这个数")
		assert.Less(t, *detail.MyEntriesRemaining, detail.MaxPicksPerRequest,
			"这一场真正能一次买的是 500 而不是 999,两个数必须都下发")
	})

	t.Run("尝试上限比参与上限更紧时报更紧的那个", func(t *testing.T) {
		act := seedBallActivity(t, ext, func(a *Activity) {
			a.MaxEntriesPerUser = 20
			a.MaxAttemptsPerUser = 5
		})
		detail := activityDetailOf(t, r, act.ActNo)
		require.NotNil(t, detail.MyEntriesRemaining)
		assert.Equal(t, 5, *detail.MyEntriesRemaining,
			"两道每人闸门是并列判定的,报宽的那个等于把另一道留到按下确认之后才说")
	})

	t.Run("全场只剩几个名额时说得出来", func(t *testing.T) {
		act := seedBallActivity(t, ext, func(a *Activity) {
			a.MaxPicksPerRequest = maxPicksPerRequestHard
			a.MaxTotalEntries = 100
			a.ActiveCount = 93
			a.PendingCount = 4
		})
		detail := activityDetailOf(t, r, act.ActNo)
		require.NotNil(t, detail.TotalEntriesRemaining)
		assert.Equal(t, 3, *detail.TotalEntriesRemaining,
			"判据与 checkCaps 同一条:max - active - pending")
	})

	t.Run("全场已满时是 0 而不是负数", func(t *testing.T) {
		act := seedBallActivity(t, ext, func(a *Activity) {
			a.MaxTotalEntries = 10
			a.ActiveCount = 12
		})
		detail := activityDetailOf(t, r, act.ActNo)
		require.NotNil(t, detail.TotalEntriesRemaining)
		assert.Equal(t, 0, *detail.TotalEntriesRemaining,
			"上限被在线调低之后计数会大过它,而一个负数会让 `remaining > 0` 恒假、"+
				"`remaining >= n` 在 n 也为负时反而为真")
	})
}

// 撞上每人上限时,提示里的那个数与真正买成的注数**必须一致**。
//
// 这条把"提前提示"与"实际结果"钉在一起:提示说还能买 4 注,提交 20 注就该
// 恰好买成 4 注。两个数对不上时,提示比没有提示更糟 —— 用户会照着它算钱。
func TestPicksCapHintMatchesWhatActuallyGetsBought(t *testing.T) {
	ext := newPicksCapEnv(t)
	const startQuota = 1_000_000
	main := newBallMainDB(t, startQuota)
	r := picksCapRouter()

	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = 100
		a.MaxEntriesPerUser = 4
		a.MaxAttemptsPerUser = 40
	})

	detail := activityDetailOf(t, r, act.ActNo)
	require.NotNil(t, detail.MyEntriesRemaining)
	hinted := *detail.MyEntriesRemaining
	require.Equal(t, 4, hinted)

	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "hint-1", picksOf(20)))
	require.Equalf(t, http.StatusOK, code, "部分成交必须是 200: %s", body)
	batch := decodeEntryBatch(t, body)
	assert.Equal(t, 20, batch.Requested)
	assert.Equal(t, hinted, batch.Accepted,
		"界面上说还能买 %d 注,就必须恰好买成 %d 注", hinted, hinted)
	assert.Equal(t, "qy_lot_user_cap", batch.FailedCode)
	assert.EqualValues(t, int64(hinted)*act.StakeQuota, batch.TotalQuota)
	assert.EqualValues(t, startQuota-int64(hinted)*act.StakeQuota, userQuota(t, main),
		"没买成的那 16 注一分钱都不许扣")

	after := activityDetailOf(t, r, act.ActNo)
	require.NotNil(t, after.MyEntriesRemaining)
	assert.Equal(t, 0, *after.MyEntriesRemaining)
}

// 撞上全场上限时同样是部分成交,而三个数仍然说得清。
func TestPicksCapPartialFillAtTotalCap(t *testing.T) {
	ext := newPicksCapEnv(t)
	const startQuota = 1_000_000
	main := newBallMainDB(t, startQuota)
	r := picksCapRouter()

	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = 100
		a.MaxTotalEntries = 6
	})
	detail := activityDetailOf(t, r, act.ActNo)
	require.NotNil(t, detail.TotalEntriesRemaining)
	require.Equal(t, 6, *detail.TotalEntriesRemaining)

	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "total-1", picksOf(20)))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	batch := decodeEntryBatch(t, body)
	assert.Equal(t, 20, batch.Requested)
	assert.Equal(t, 6, batch.Accepted, "全场只剩 6 个名额,就只买得到 6 注")
	assert.Equal(t, "qy_lot_cap_reached", batch.FailedCode,
		"停在哪一注、为什么停必须说得出来,而且不能与每人上限混成同一个码")
	assert.EqualValues(t, 6*act.StakeQuota, batch.TotalQuota)
	assert.EqualValues(t, startQuota-6*act.StakeQuota, userQuota(t, main))

	after := activityDetailOf(t, r, act.ActNo)
	require.NotNil(t, after.TotalEntriesRemaining)
	assert.Equal(t, 0, *after.TotalEntriesRemaining)
}

// ─────────────────────────── 创建期校验 ───────────────────────────

// 建活动时这一格的取值区间,以及 0 落库之后仍然读成默认 10。
func TestPicksCapAcceptedAtCreate(t *testing.T) {
	ext := newDraftEnv(t)
	r := draftRouter()
	now := common.GetTimestamp()

	create := func(n string) (int, []byte) {
		return callJSON(t, r, http.MethodPost, "/admin/lottery/activities", `{
			"kind":"draw","draw_mode":"rank","title":"注数上限",
			"stake_quota":1000,
			"open_at":`+strconv.FormatInt(now+60, 10)+`,
			"close_at":`+strconv.FormatInt(now+3600, 10)+`,
			"draw_at":`+strconv.FormatInt(now+7200, 10)+`,
			"settle_deadline":`+strconv.FormatInt(now+86400, 10)+`,
			"max_picks_per_request":`+n+`,
			"rules":{},
			"prizes":[{"tier":1,"name":"一等奖","amount_quota":1000,"count":1}]}`)
	}

	for _, tc := range []struct {
		name string
		n    string
		code int
		// wantStored 是落库的原始值,wantEffective 是 picksCapOf 读出来的生效值。
		// 两个数必须分开断言:0 与 10 的**生效值**相同而**原始值**不同,
		// 只断言其中一个,"原样落库不回填"这条口径就没人守。
		wantStored    int
		wantEffective int
	}{
		{"0 合法(= 不配置)", "0", http.StatusOK, 0, defaultPicksPerRequest},
		{"1 合法", "1", http.StatusOK, 1, 1},
		{"10 与 0 生效值相同但落库不同", "10", http.StatusOK, 10, defaultPicksPerRequest},
		{"999 合法", "999", http.StatusOK, 999, 999},
		{"1000 被拒", "1000", http.StatusBadRequest, 0, 0},
		{"负数被拒", "-1", http.StatusBadRequest, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := create(tc.n)
			require.Equalf(t, tc.code, code, "响应: %s", body)
			if tc.code != http.StatusOK {
				assert.Contains(t, string(body), strconv.Itoa(maxPicksPerRequestHard),
					"报错必须念出上界,否则运营只能二分试")
				return
			}
			actNo := jsonString(t, body, "data", "act_no")
			var row Activity
			require.NoError(t, ext.Where("act_no = ?", actNo).Take(&row).Error)
			assert.Equal(t, tc.wantStored, row.MaxPicksPerRequest,
				"原样落库 —— 回填默认值会让这一列再也分不出「没碰过」与「明确填了 10」")
			assert.Equal(t, tc.wantEffective, picksCapOf(&row))
		})
	}
}

// ─────────────────────────── 时间预算 ───────────────────────────

// 预算必须随注数长,而且必须封顶。
//
// 两头都会出事:不随注数长时 999 注在第 86 注上下被截断(配得到、买不到);
// 不封顶时一个 999 注的请求会跑到反代的读超时之外,用户拿到 504 而钱已经扣了。
func TestEntryBatchContextBudget(t *testing.T) {
	prev := qyConfig.Load()
	t.Cleanup(func() { qyConfig.Store(prev) })

	cases := []struct {
		name    string
		coldMs  int
		maxMs   int
		picks   int
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			// 单注提交必须与改造前逐字节相同:拿到的就是冷路径那一份加一注。
			name: "单注", coldMs: 3000, maxMs: 45_000, picks: 1,
			wantMin: 3000 * time.Millisecond, wantMax: 3200 * time.Millisecond,
		},
		{
			name: "十注", coldMs: 3000, maxMs: 45_000, picks: 10,
			wantMin: 4000 * time.Millisecond, wantMax: 4000 * time.Millisecond,
		},
		{
			// 999 注够到上界:线性算出来是 102.9 秒,被 45 秒截住。
			// 45 秒仍然是实测 36 秒的 1.25 倍,健康的机器跑得完。
			name: "999 注被上界截住", coldMs: 3000, maxMs: 45_000, picks: maxPicksPerRequestHard,
			wantMin: 45 * time.Second, wantMax: 45 * time.Second,
		},
		{
			// 上界配得比冷路径预算还小时,单注仍然拿得到冷路径那一份 ——
			// 否则一个配错的 entry_batch_max_ms 会把整条参与路径关掉。
			name: "上界小于冷路径预算", coldMs: 3000, maxMs: 1000, picks: 1,
			wantMin: 3000 * time.Millisecond, wantMax: 3000 * time.Millisecond,
		},
		{
			// 上界配成 0(没写这一项)时不封顶,回落到线性预算。
			name: "上界为 0 时不封顶", coldMs: 3000, maxMs: 0, picks: 100,
			wantMin: 13 * time.Second, wantMax: 13 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qyConfig.Store(&config.Config{
				Enabled: true,
				Runtime: config.Runtime{ColdPathTimeoutMs: tc.coldMs},
				Lottery: config.Lottery{Enabled: true, EntryBatchMaxMs: tc.maxMs},
			})
			ctx, cancel := entryBatchContext(context.Background(), tc.picks)
			defer cancel()
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "批量提交必须有截止时刻 —— 没有上界的请求会一直占着连接")
			budget := time.Until(deadline)
			assert.GreaterOrEqual(t, budget, tc.wantMin-100*time.Millisecond)
			assert.LessOrEqual(t, budget, tc.wantMax+100*time.Millisecond)
		})
	}
}

// 预算耗尽时是**安全截断**,不是一条 500。
//
// 把整批预算压到只够几注,再提交 999 注。两种落点都合法,而且必须说同一句话:
//
//	accepted > 0  —— 200 + 信封,前面几注真的成交、后面的一分钱没扣;
//	accepted == 0 —— 错误信封,这次提交什么都没发生。
//
// 哪一种落点由"截止时刻落在第几注"决定,而那取决于机器当时有多忙 —— 所以这条
// 用例**不去钉是哪一种**,只钉两件在两种落点下都必须成立的事:业务码是
// `qy_lot_batch_budget`(不是一句 500 内部错误),以及余额正好少了 accepted 注的钱。
func TestEntryBatchTruncatesSafelyWhenBudgetRunsOut(t *testing.T) {
	// 这一条**必须**用落盘的 SQLite,不能用 `:memory:`。
	//
	// 预算到期时那条语句返回 context deadline exceeded,database/sql 会把这条
	// 连接判成坏连接并关掉;而 `:memory:` 库的生命周期就是那条连接的生命周期
	// (池子被 SetMaxOpenConns(1) 限成一条),下一次取连接拿到的是一个**空库**,
	// 后面每一句断言都会报 "no such table"。这不是被测代码的问题,是夹具的问题,
	// 而它恰恰只在"真的把预算跑干"的用例上暴露出来。
	const startQuota = 5_000_000
	ext, main := newFileBackedEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		EntryCloseGraceSeconds: 0, RevealDelaySeconds: 0,
		MaxStakeQuota: 5_000_000, MaxTotalEntriesHard: 50_000,
		// 1 秒:落盘 SQLite 上够跑几十到几百注,绝不够跑 999 注。
		EntryBatchMaxMs: 1000,
	}, startQuota)
	r := picksCapRouter()
	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = maxPicksPerRequestHard
		a.MaxTotalEntries = 50_000
	})

	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries",
		entryBody(t, "budget-1", picksOf(maxPicksPerRequestHard)))

	accepted := 0
	if code == http.StatusOK {
		batch := decodeEntryBatch(t, body)
		require.Equal(t, maxPicksPerRequestHard, batch.Requested)
		require.Lessf(t, batch.Accepted, maxPicksPerRequestHard,
			"1 秒预算居然跑完了 999 注 —— 这条用例此刻测的不是截断: %s", body)
		require.Greater(t, batch.Accepted, 0)
		assert.Equal(t, "qy_lot_batch_budget", batch.FailedCode,
			"被预算停下必须说得出来 —— 一句内部错误会让用户以为整批都失败了")
		assert.Contains(t, batch.FailedMessage, "没有扣费",
			"用户此刻最需要知道的是「没买成的那几注一分钱都没扣」")
		assert.EqualValues(t, int64(batch.Accepted)*act.StakeQuota, batch.TotalQuota)
		require.Len(t, batch.Entries, batch.Accepted, "买到手的每一张票都必须出现在回执里")
		accepted = batch.Accepted
	} else {
		// 预算在第一注上就用完:这一支同样不许退化成一句 500 内部错误。
		require.Equalf(t, http.StatusConflict, code,
			"一注都没买成时必须是那条业务错误,而不是 500: %s", body)
		assert.Equal(t, "qy_lot_batch_budget", errorCode(t, body))
	}

	// 钱与票必须对得上 accepted,一笔不多一笔不少 —— 两种落点下都要成立。
	assert.EqualValues(t, startQuota-int64(accepted)*act.StakeQuota, userQuota(t, main),
		"余额必须正好等于 accepted × 单注参与费")
	var success int64
	require.NoError(t, ext.Model(&Entry{}).
		Where("act_id = ? AND status = ?", act.Id, EntrySuccess).Count(&success).Error)
	assert.EqualValues(t, accepted, success, "买成的票数必须等于回执里的 accepted")

	// 截止时刻落在 ChargeEntry **内部**时,那一注的预占行已经写下了 —— 它必须被
	// 回滚成 failed,**绝不能留在 pending**。
	//
	// 留 pending 的后果不是多一行脏数据:checkCaps 见到本人任何 pending 条目一律
	// 返回 errEntryInFlight,于是这个用户在**这一场**就整场 409 —— 恰恰是回执里
	// 那句「剩下的没有扣费,可以再提交一次」指的那次重提。而且没有任何自动出口:
	// Compensate 只扫 pending/in_doubt 的**资金单**,这一支的资金单终态是 failed,
	// 扫不到;只有封盘时的 excludePendingEntries 才会把它刷掉。
	//
	// 原先这里断言的是「全部行数 == accepted」,那条判据在这一支上本来就不成立
	// (被正确回滚的 failed 行同样占一行),于是它以约四分之一的概率无故变红,
	// 把真正的缺陷淹在噪声里。换成下面这两条之后判据既确定又更强。
	var stuck int64
	require.NoError(t, ext.Model(&Entry{}).
		Where("act_id = ? AND status = ?", act.Id, EntryPending).Count(&stuck).Error)
	assert.EqualValues(t, 0, stuck,
		"预算耗尽不许留下 pending 预占 —— 那会让这个用户在本场的每一次重试都被判成「上一次还在处理中」")
}

// 「整批停在这里」的三种原因各说一句话,而且**互不覆盖**。
//
// 这三支在真实运行里出现的概率极不均匀:业务错误天天有,预算耗尽要压着截止
// 时刻才出现,内部错误几乎不出现。而端到端那条"把预算跑干"的用例落在哪一支
// 取决于截止时刻落在两条语句之间还是一条语句中间 —— 它证明得了"两条路径说
// 同一句话",证明不了"每一支各说各的"。这里把三支各钉一行。
func TestBatchStopReason(t *testing.T) {
	cases := []struct {
		name       string
		stopped    error
		budgetGone bool
		wantCode   string
	}{
		{
			name: "业务错误原样说出来", stopped: errUserCap,
			budgetGone: false, wantCode: "qy_lot_user_cap",
		},
		{
			// 余额不足就是余额不足。此刻预算也恰好用完并不改变这一注为什么没买成,
			// 改口成"超时了"会让用户去重试一次必然再失败的提交。
			name: "业务错误优先于预算", stopped: errUserCap,
			budgetGone: true, wantCode: "qy_lot_user_cap",
		},
		{
			// 预算在 ChargeEntry 内部到期时冒出来的是驱动包装过的错误。
			// 判据只能是本批的截止时刻,不能是错误文本。
			name: "预算用完时说的是被截断", stopped: context.DeadlineExceeded,
			budgetGone: true, wantCode: "qy_lot_batch_budget",
		},
		{
			// 各家驱动对 context 超时的包装方式不一样,连 errors.Is 都不保证成立。
			name: "驱动把超时改写成别的文本也照样认", stopped: errors.New("driver: bad connection"),
			budgetGone: true, wantCode: "qy_lot_batch_budget",
		},
		{
			name: "预算还在时不许把内部错误说成截断", stopped: errors.New("no such table"),
			budgetGone: false, wantCode: "qy_internal_error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := batchStopReason(tc.stopped, tc.budgetGone)
			assert.Equal(t, tc.wantCode, code)
			require.NotEmpty(t, msg, "accepted < requested 却说不出为什么,比说错更难查")
			if tc.wantCode == "qy_internal_error" {
				assert.NotContains(t, msg, "no such table",
					"内部错误的原文可能带 SQL 片段与表结构,不许回显给用户")
			}

			// **同一份判断也要管住 accepted == 0 那一支。** 两个落点分开写的
			// 表现是:预算在第一注上就用完时报一句 500,在第五注上用完时报
			// "只买成了前面几注" —— 对用户是同一件事,却被说成了两句话。
			got := batchStopError(tc.stopped, tc.budgetGone)
			if be, ok := AsBizError(got); ok {
				assert.Equal(t, tc.wantCode, be.ErrCode(),
					"零成交那一支报的码与部分成交那一支对不上")
				return
			}
			assert.Equal(t, "qy_internal_error", tc.wantCode,
				"归一不成业务错误的只能是那一支真正的内部错误")
			assert.Equal(t, tc.stopped, got, "内部错误必须原样带出去交给上层记日志")
		})
	}
}

// 非双色球一次仍然只能一注 —— 这一格对它们不开口子。
//
// 抽奖(rank / prob)与竞猜在协议上就是"一次一注 / 一次一票":
// 抽奖没有选号可挑,竞猜的一次提交是"押哪一项 + 押多少",押两次同一项与押一次
// 双倍金额在结算上完全等价。给它们加批量既没有需求,又要多一条能动钱的路径。
// 判据在 acceptPick:非双色球带号一律**拒绝**而不是忽略。
func TestPicksBatchStaysBallOnly(t *testing.T) {
	ext := newPicksCapEnv(t)
	main := newBallMainDB(t, 1_000_000)
	r := picksCapRouter()

	act := seedActivity(t, ext, func(a *Activity) {
		a.Kind = KindDraw
		a.Algo = AlgoV2
		a.DrawMode = DrawModeRank
		a.AllowMultiWin = true
		a.RulesText = `{"min_quota":0}`
		a.RulesHash = RulesHash(`{"min_quota":0}`)
		a.MaxPicksPerRequest = maxPicksPerRequestHard
		a.ChainHead = ""
	})
	require.NoError(t, ext.Create(&Seed{
		ActId: act.Id, Seed: newSecret(), RefSalt: newSecret(), IpSalt: newSecret(),
		CreatedAt: common.GetTimestamp(),
	}).Error)

	before := userQuota(t, main)
	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries",
		entryBody(t, "rank-1", []string{"01,02,03|01", "04,05,06|02"}))
	require.Equalf(t, http.StatusBadRequest, code,
		"普通抽奖带号必须被**拒绝**而不是忽略 —— 忽略意味着用户以为自己选了号: %s", body)
	assert.Equal(t, "qy_lot_pick_not_allowed", errorCode(t, body))
	assert.Equal(t, before, userQuota(t, main), "被拒的提交不许扣钱")

	// 不带号的单注照常受理,证明上面那条拒绝不是因为活动本身参与不了。
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", `{"client_request_id":"rank-2"}`)
	require.Equalf(t, http.StatusOK, code, "不带号的单注应当照常受理: %s", body)
	assert.Equal(t, 1, decodeEntryBatch(t, body).Accepted)
}

// client_request_id 的长度上界必须跟着硬顶一起走。
//
// 999 注的最长后缀是 `#998`(4 字节),而 act_no(27) + ":"(1) + 64 + 4 = 96,
// **恰好**等于 qy_lot_entry.idem_key 的列宽。再抬高硬顶必须先加宽那一列 ——
// 装不下时 MySQL 在非严格模式下静默截断,两注撞成同一个键,用户少买一注
// 而没有任何一处报错。
func TestPicksCapDerivedKeyFitsColumnAtHardCap(t *testing.T) {
	assert.Equal(t, maxClientRequestID+4, maxEntryRequestID,
		"后缀 `#998` 占 4 字节,ChargeEntry 认的上界必须比客户端那一份大 4")

	longest := strings.Repeat("z", maxClientRequestID)
	actNo := newActNo()
	require.Len(t, actNo, 27)

	seen := make(map[string]bool, maxPicksPerRequestHard)
	for i := 0; i < maxPicksPerRequestHard; i++ {
		crid := batchRequestId(longest, i)
		require.LessOrEqualf(t, len(crid), maxEntryRequestID,
			"第 %d 注的派生键越过了 ChargeEntry 的上界", i)
		key := buildIdemKey(actNo, crid)
		require.LessOrEqualf(t, len(key), 96,
			"第 %d 注的幂等键 %q 越过了列宽", i, key)
		require.Falsef(t, seen[key], "第 %d 注的幂等键与前面某一注撞了", i)
		seen[key] = true
	}
	assert.Equal(t, longest, batchRequestId(longest, 0),
		"第 0 注必须原样沿用客户端那一份 —— 单注提交要与改造前逐字节相同")
}
