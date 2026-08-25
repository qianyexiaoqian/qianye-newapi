package lottery

// ball_full_e2e_db_test.go —— 双色球从「建一场」到「买一注」的整条路,
// 全部走**真实的 HTTP handler**,并在最后逐字节复算证据链。
//
// 为什么要有这一条,而不是靠已有的几条分段用例:
//
//	ball_golden_test / fairness_v2_test   摇号纯函数,早就绿了
//	ball_ui_contract_db_test              报名请求体带号、大厅下发 draw_mode
//	ball_e2e_db_test                      开奖结果可独立复算
//
// 三条各自绿着,而玩法整体可以照样不存在 —— 断掉的从来是**接缝**:
// 建活动时期次没绑上、发布时号池没进承诺哈希、大厅把它当普通抽奖列出来、
// 报名时选号没落进链。这条用例把四个接缝串成一次真实的用户旅程,
// 中间任何一处断掉都会红,而不是"某个纯函数仍然算得对"。
//
// 走的是真实端点:
//
//	POST /admin/lottery/series                     建期次系列(号池 + 注资)
//	POST /admin/lottery/activities                 建草稿(draw_mode=ball)
//	POST /admin/lottery/activities/:act_no/publish 发布(此刻算 commit_hash)
//	GET  /lottery/activities                       大厅列表(用户视角)
//	POST /lottery/activities/:act_no/entries       带号报名(扣真钱)
//	GET  /lottery/my/entries                       我的参与(选号必须留得住)
//	GET  /lottery/activities/:act_no/proof         公示证据

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

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

const (
	ballE2EAdminId = 9001
	ballE2EUserId  = 9002
)

// newBallMainDB 建一个主库并接到 model.DB —— 报名要真的扣额度,
// 而"扣钱"这一步正是 twophase 两库协议唯一会出事的地方。
func newBallMainDB(t *testing.T, quota int) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	// 探针表跟着一起建:main_outbox_enabled 默认就是 true,线上每一笔跨库资金
	// 操作都会先在这张表上认领单号。少了它,测试跑的是一条线上根本不存在的
	// "没有探针"的分支 —— 而"没有探针"恰恰是资金判定里最危险的那一支。
	require.NoError(t, gdb.AutoMigrate(&model.User{}, &model.Log{}, &model.QyFundOutbox{}))

	prevType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	prevDB := model.DB
	prevMem, prevRedis := common.MemoryCacheEnabled, common.RedisEnabled
	prevOptions := common.OptionMap
	prevLogDB := model.LOG_DB
	model.DB = gdb
	// 账本日志走日志库句柄。不接上它,提交后处理里的 QyRecordLedgerLog 会对一个
	// nil 句柄取值 —— twophase 的 AfterCommit 会把 panic 拦下来只记一行日志,
	// 于是用例照常"通过",而线上每一笔参与都少一条账本流水。
	model.LOG_DB = gdb
	common.OptionMap = map[string]string{}
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = prevDB, prevLogDB
		common.SetMainDatabaseType(prevType)
		common.MemoryCacheEnabled, common.RedisEnabled = prevMem, prevRedis
		common.OptionMap = prevOptions
		_ = sqlDB.Close()
	})

	require.NoError(t, gdb.Create(&model.User{
		Id: ballE2EUserId, Username: "ball-buyer", Password: "x",
		AffCode: "affball", Group: "default", Quota: quota, Status: common.UserStatusEnabled,
	}).Error)
	return gdb
}

// ballE2ERouter 把真实路由挂起来,并按角色注入用户 id。
func ballE2ERouter() *gin.Engine {
	r := gin.New()
	admin := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", ballE2EAdminId); h(c) }
	}
	user := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", ballE2EUserId); h(c) }
	}
	r.POST("/admin/lottery/series", admin(handleCreateSeries))
	r.POST("/admin/lottery/activities", admin(handleCreateActivity))
	r.POST("/admin/lottery/activities/:act_no/publish", admin(handlePublishActivity))
	r.GET("/lottery/activities", user(handleListActivities))
	r.GET("/lottery/activities/:act_no", user(handleGetActivity))
	r.POST("/lottery/activities/:act_no/entries", user(handleCreateEntry))
	r.GET("/lottery/my/entries", user(handleListMyEntries))
	r.GET("/lottery/activities/:act_no/proof", user(handleGetProof))
	return r
}

// callJSON 打一次真实请求,返回 HTTP 码与整个响应信封的原始字节。
func callJSON(t *testing.T, r *gin.Engine, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// jsonString 从响应信封里按路径取一个字符串字段。
//
// 刻意走**响应体本身**而不是 handler 的返回结构体:前端拿到的是这些字节,
// 字段名写错一个,结构体断言照样全绿而界面上是空的。
func jsonString(t *testing.T, body []byte, path ...string) string {
	t.Helper()
	var root map[string]any
	require.NoErrorf(t, common.Unmarshal(body, &root), "响应不是合法 JSON: %s", body)
	var cur any = root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		require.Truef(t, ok, "路径 %v 在 %s 上走不通", path, body)
		cur = m[key]
	}
	out, ok := cur.(string)
	require.Truef(t, ok, "%v 不是字符串: %s", path, body)
	return out
}

func TestBallActivityFullJourney(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// 闸门显式给足:MaxTotalPrizeQuota 是"派奖对 users.quota 是净增发"的唯一
	// 硬闸门,零值等于把系列注资全部判成超限。RevealDelaySeconds 归零是为了
	// 让 close_at → draw_at 的最小间隔不挡住这条只跑到报名为止的旅程。
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
	main := newBallMainDB(t, 100_000)
	r := ballE2ERouter()

	now := common.GetTimestamp()

	// ── 1. 建期次系列 ─────────────────────────────────────────────
	// 红 12 选 3、蓝 4 选 1;投注 70% 入池;首笔注资 60000。
	code, body := callJSON(t, r, http.MethodPost, "/admin/lottery/series", `{
		"title":"双色球测试系列","red_pool":12,"red_pick":3,
		"blue_pool":4,"blue_pick":1,"pool_share_bps":7000,
		"issue_cap_quota":1000000,"seed_quota":60000}`)
	require.Equalf(t, http.StatusOK, code, "建系列失败: %s", body)
	seriesNo := jsonString(t, body, "data", "series_no")
	require.NotEmpty(t, seriesNo)

	// ── 2. 建草稿活动(draw_mode=ball) ────────────────────────────
	create := `{
		"kind":"draw","draw_mode":"ball","title":"双色球第 1 期",
		"stake_quota":1000,
		"open_at":` + strconv.FormatInt(now-60, 10) + `,
		"close_at":` + strconv.FormatInt(now+3600, 10) + `,
		"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
		"series_no":"` + seriesNo + `",
		"prizes":[
			{"tier":1,"name":"一等奖","prize_type":"quota","count":1,"red_match":3,"blue_match":1,"pool_share_bps":6000},
			{"tier":2,"name":"二等奖","prize_type":"quota","count":1,"red_match":3,"blue_match":0,"pool_share_bps":2500},
			{"tier":3,"name":"三等奖","prize_type":"quota","count":1,"red_match":2,"blue_match":1,"pool_share_bps":1500}
		]}`
	code, body = callJSON(t, r, http.MethodPost, "/admin/lottery/activities", create)
	require.Equalf(t, http.StatusOK, code, "建活动失败: %s", body)
	actNo := jsonString(t, body, "data", "act_no")
	require.NotEmpty(t, actNo)

	// 号池必须从系列**继承**到活动行上。不继承的表现是选号校验用零号池,
	// 每一注都被判成"号码越出池子"。
	var draft Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&draft).Error)
	assert.Equal(t, DrawModeBall, draft.DrawMode)
	assert.Equal(t, seriesNo, draft.SeriesNo)
	assert.Equal(t, 12, draft.BallRedPool)
	assert.Equal(t, 3, draft.BallRedPick)
	assert.Equal(t, 4, draft.BallBluePool)
	assert.Equal(t, 1, draft.BallBluePick)
	assert.Equal(t, StatusDraft, draft.Status)
	assert.Zero(t, draft.PoolOpenQuota,
		"草稿期还没有取走系列池 —— 池子在 publish 的同一个事务里才被冻结成本期开局池,"+
			"因为它要整份进 commit 原像")
	assert.Empty(t, draft.CommitHash, "草稿期不许有承诺 —— 内容还能随便改")

	// ── 3. 发布(此刻才算 commit_hash) ───────────────────────────
	code, body = callJSON(t, r, http.MethodPost,
		"/admin/lottery/activities/"+actNo+"/publish", `{}`)
	require.Equalf(t, http.StatusOK, code, "发布失败: %s", body)

	var published Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&published).Error)
	require.Equal(t, StatusPublished, published.Status)
	require.NotEmpty(t, published.CommitHash, "发布之后必须有承诺哈希")
	require.NotEmpty(t, published.RulesHash)
	assert.EqualValues(t, 60_000, published.PoolOpenQuota,
		"发布那一刻把系列池整块取走冻结成本期开局池,此后开放期内它是一个不会变的数 —— "+
			"用户看到的与承诺原像里的必须是同一个")
	assert.Equal(t, 1, published.IssueNo, "第一期的期号是 1")

	// ── 4. 大厅看得到,且看得出它是双色球 ─────────────────────────
	code, body = callJSON(t, r, http.MethodGet, "/lottery/activities", "")
	require.Equalf(t, http.StatusOK, code, "大厅列表失败: %s", body)
	var hall struct {
		Success bool `json:"success"`
		Data    struct {
			Items []activityBrief `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(body, &hall))
	require.True(t, hall.Success)

	var card *activityBrief
	for i := range hall.Data.Items {
		if hall.Data.Items[i].ActNo == actNo {
			card = &hall.Data.Items[i]
		}
	}
	require.NotNil(t, card, "刚发布的双色球必须出现在大厅里 —— 列不出来就等于没有这个玩法")
	assert.Equal(t, DrawModeBall, card.DrawMode, "不下发 draw_mode,卡片与普通抽奖长得一模一样")
	assert.Equal(t, seriesNo, card.SeriesNo)
	assert.Equal(t, 1, card.IssueNo)
	assert.Equal(t, 12, card.BallRedPool)
	assert.Equal(t, 3, card.BallRedPick)
	assert.Equal(t, 4, card.BallBluePool)
	assert.Equal(t, 1, card.BallBluePick)
	assert.EqualValues(t, 60_000, card.PoolOpenQuota,
		"卡片上的奖池必须是本期可派发的那一份(此刻还没有人投注,就是开局基数)")
	assert.Zero(t, card.PrizeTotalQuota,
		"三档全是浮动奖(amount 恒 0),prize_total_quota 对双色球恒是错的数")

	// ── 5. 选号参与 ───────────────────────────────────────────────
	// 故意提交乱序 + 缺前导零的 "12,3,5|2":前端选号器给出的就是这种形状,
	// 而进链的必须是归一化之后的那一份。
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+actNo+"/entries",
		`{"client_request_id":"ball-e2e-1","pick":"12,3,5|2"}`)
	require.Equalf(t, http.StatusOK, code, "报名失败: %s", body)

	// 单注提交走的也是多注那个信封:一注一个形状、多注另一个形状,会让前端
	// 按自己发了什么去决定怎么解析,而"发了几注"与"收下了几注"恰恰是这条链路上
	// 唯一会不一致的两个数。
	batch := decodeEntryBatch(t, body)
	require.Len(t, batch.Entries, 1)
	assert.Equal(t, 1, batch.Requested)
	assert.Equal(t, 1, batch.Accepted)
	assert.EqualValues(t, 1000, batch.TotalQuota, "单注提交的总额就是一注的参与费")
	assert.Empty(t, batch.FailedCode, "全部买成时不许留下一个失败码")

	receipt := batch.Entries[0]
	entryNo := receipt.EntryNo
	chainHash := receipt.ChainHash
	prevHash := receipt.PrevHash
	receiptCommit := receipt.CommitHash
	userRef := receipt.UserRef

	assert.Equal(t, "03,05,12|02", receipt.Pick, "回执上显示的必须是**进链的那一份**")
	assert.Equal(t, published.CommitHash, receiptCommit,
		"回执必须带上承诺哈希 —— 用户拿它去比对公示页,这是整条证据链的锚")
	assert.NotEmpty(t, userRef)

	// 钱真的扣了。
	var buyer model.User
	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&buyer).Error)
	assert.Equal(t, 100_000-1000, buyer.Quota, "参与费必须真的从主库扣掉")

	// ── 6. 证据链完整性:逐字节复算 ───────────────────────────────
	var entry Entry
	require.NoError(t, ext.Where("entry_no = ?", entryNo).Take(&entry).Error)
	assert.Equal(t, EntrySuccess, entry.Status)
	assert.Equal(t, 1, entry.Seq, "第一注的序号必须是 1")
	assert.Equal(t, "03,05,12|02", entry.Pick, "落库的选号必须是归一化之后的那一份")
	assert.Equal(t, chainHash, entry.ChainHash)
	assert.Equal(t, prevHash, entry.PrevHash)
	assert.NotEmpty(t, entry.EligibilitySnapshot,
		"报名那一刻的资格快照是事后仲裁的唯一凭据 —— 主库 users 没有历史版本")

	// 这一步是整条链的关键:用**公开可见的那几样**重新算一次哈希。
	// 任何一位对不上,说明进链的内容与公示出去的不是同一份。
	assert.Equal(t,
		ChainNextFor(published.Algo, entry.PrevHash, actNo, entry.Seq,
			entry.EntryNo, entry.UserRef, entry.OptNo, entry.Amount, entry.Pick),
		entry.ChainHash,
		"链哈希必须能用公开字段独立复算 —— 算不出来的链等于没有链")

	// 选号必须真的**参与**哈希:把它换掉一位,链哈希必须不同。
	// 少了这一条,「pick 进链」可以退化成"字段传到了但没进原像",
	// 而那正是「平台在开奖之后改掉某个人的号」这条攻击的落点 ——
	// 现有的全部校验(链尾、条目计数、名单重算)会照常全部通过。
	assert.NotEqual(t,
		ChainNextFor(published.Algo, entry.PrevHash, actNo, entry.Seq,
			entry.EntryNo, entry.UserRef, entry.OptNo, entry.Amount, "03,05,11|02"),
		entry.ChainHash,
		"改一位号码算出同一个链哈希 = 选号根本没进原像")

	// 首注的前哈希必须是承诺哈希本身:链的第一环锚在公示的那一刻上。
	assert.Equal(t, published.CommitHash, entry.PrevHash,
		"第一注的 prev_hash 必须是 commit_hash —— 否则链的起点无法被外部验证")

	// ── 7. 「我的参与」留得住这组号 ───────────────────────────────
	code, body = callJSON(t, r, http.MethodGet, "/lottery/my/entries", "")
	require.Equalf(t, http.StatusOK, code, "我的参与失败: %s", body)
	var mine struct {
		Data struct {
			Items []myEntryView `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(body, &mine))
	require.Len(t, mine.Data.Items, 1)
	assert.Equal(t, "03,05,12|02", mine.Data.Items[0].Pick,
		"回执弹窗关掉就没了,这份列表才是留得住的那一份 —— "+
			"事后争议的第一句话永远是「我买的明明是那一组」")
	assert.Equal(t, entry.ChainHash, mine.Data.Items[0].ChainHash)

	// ── 8. 账本流水必须真的落下来 ─────────────────────────────────
	//
	// 它写在 twophase 的 AfterCommit 里,而那一段的 panic 会被拦下来只记一行
	// 日志 —— 少写一条流水不会让任何请求失败,只会让事后对账时这笔钱"凭空消失"。
	var ledger []model.Log
	require.NoError(t, main.Where("user_id = ?", ballE2EUserId).Find(&ledger).Error)
	require.Len(t, ledger, 1, "每一笔参与必须留下一条账本流水")
	assert.Equal(t, model.LogTypeSystem, ledger[0].Type,
		"参与费不能记成 consume:那会让『累计消费满 N 才能参加』被抽奖本身刷高,是一个自举漏洞")
	assert.Contains(t, ledger[0].Content, entryNo)

	// ── 9. 封盘之前拿不到证据链,这是**对的** ─────────────────────
	//
	// 承诺-揭示协议的次序是硬的:名单哈希必须先于种子公开。开放期就能拿到
	// 完整证据 = 平台可以在还能改名单的时候先把证据发出去。
	code, body = callJSON(t, r, http.MethodGet,
		"/lottery/activities/"+actNo+"/proof", "")
	require.Equal(t, http.StatusConflict, code,
		"开放期必须拒绝出具证据链: %s", body)
	assert.Contains(t, string(body), "qy_lot_proof_not_ready")

	// ── 10. 封盘:名单哈希可独立复算 ─────────────────────────────
	//
	// 封盘时刻 close_at 本身进承诺原像,所以这里只能**直接改库**把它拨到过去
	// 来触发扫描 —— 而那正好制造出下一步要断言的那件事(见 10b)。
	require.NoError(t, ext.Model(&Activity{}).Where("act_no = ?", actNo).
		Updates(map[string]any{"close_at": now - 2, "draw_at": now - 1}).Error)
	runLock(context.Background())

	var locked Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&locked).Error)
	require.Equal(t, StatusLocked, locked.Status)
	require.NotEmpty(t, locked.RosterHash, "封盘必须公开名单哈希,而且它先于种子")
	require.Empty(t, locked.BallResult, "封盘时还不能有开奖号 —— 名单必须先于号码冻结")

	// 名单哈希可独立复算:用户手上只有回执里的那几样,能不能自己算出这个数,
	// 决定了这份公示到底是不是证据。双色球走 lot-v2,选号是原像的一个分量。
	wantRoster, count := RosterHashFor(locked.Algo, actNo, locked.CommitHash, []RosterLine{{
		EntryNo: entry.EntryNo, UserRef: entry.UserRef,
		OptNo: entry.OptNo, Amount: entry.Amount, Pick: entry.Pick,
	}})
	assert.Equal(t, 1, count)
	assert.Equal(t, wantRoster, locked.RosterHash,
		"名单哈希算不出来 = 「到底哪些票有效」这个集合没有被钉死")
	assert.Equal(t, AlgoV2, locked.Algo, "双色球必须走 lot-v2 —— v1 的原像里没有选号这一位")

	// 把整条链原样打出来。这条用例同时是一份"人能照着核对一遍"的样张:
	// 断言绿了但链上写的是别的东西,只有把它印出来才看得见。
	t.Logf("证据链: act=%s series=%s issue=%d", actNo, seriesNo, published.IssueNo)
	t.Logf("  commit_hash = %s", locked.CommitHash)
	t.Logf("  rules_hash  = %s", locked.RulesHash)
	t.Logf("  roster_hash = %s (条数 %d)", locked.RosterHash, count)
	t.Logf("  entry #%d pick=%s user_ref=%s", entry.Seq, entry.Pick, entry.UserRef)
	t.Logf("  prev_hash   = %s", entry.PrevHash)
	t.Logf("  chain_hash  = %s", entry.ChainHash)

	// ── 10b. 承诺是**有约束力**的:动过原像的活动开不了奖 ────────────
	//
	// 上一步为了触发扫描直接改了 close_at,而 close_at 进 commit 原像。
	// 揭示时重算承诺必然对不上,协议的正确反应是**拒绝开奖**,而不是
	// 「以种子为准继续」。这条断言把整条证据链从"能算出来"升级成"算错了会停" ——
	// 一条算得出来但没人拿它做判断的链,与没有链没有区别。
	runReveal(context.Background())
	var afterReveal Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&afterReveal).Error)
	assert.Empty(t, afterReveal.BallResult,
		"承诺原像被动过时必须拒绝开奖 —— 出了号码就说明校验形同虚设")
	assert.NotEqual(t, StatusFinished, afterReveal.Status)
	assert.Equal(t, published.CommitHash, afterReveal.CommitHash,
		"被拒绝的开奖不许回头去改承诺哈希,把自己「修正」成一致")

	// ── 11. 池子跟着投注涨:70% 入池 ───────────────────────────────
	var afterEntry Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&afterEntry).Error)
	assert.EqualValues(t, 1000, afterEntry.PoolQuota, "本期投注额")
	assert.EqualValues(t, 60_000+700, ballPoolOpen(&afterEntry),
		"本期可派发池 = 开局基数 + 投注额 × pool_share_bps")
}
