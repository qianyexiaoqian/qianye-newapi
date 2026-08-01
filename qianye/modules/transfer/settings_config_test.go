package transfer

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// settings_config_test.go —— 划转门槛可在线配置(需求 3-A / 3-C)的回归。
//
// # 这一组测试防的是什么
//
// 本仓库反复出现的第一种缺陷形状是**断链**:纯函数写对了,调度层/消费层没接上。
// 门槛覆盖层尤其容易踩:loadOverrides 与 applyOverrides 单独看都对,只要
// handleGetLimits 还在读 config.Get().Transfer,运营在管理端改的每一个数字
// 都只是写进了一张没人读的表 —— 而界面会显示"已保存"。
//
// 因此这里的断言一律**从真正的消费点进**:
//
//   - 用户端 /transfer/limits 的 HTTP 处理器(3-A 的可见后果 + 3-C 的剩余量)
//   - reserveRisk 本体(锁内那道闸门到底按谁的门槛判)
//   - 管理端 PUT 的 HTTP 处理器(事务性、白名单、跨字段校验、审计)
//
// 只测 applyOverrides 的话,把 handleGetLimits 改回读 YAML,测试照样全绿。

// ───────────────────────────── 脚手架 ─────────────────────────────

// settingsTables 是本文件用到的全部扩展库表。
//
// qy_settings 必须建:effectiveCtx 每次都会查它,表不存在时读失败 ——
// 而"读失败会怎样"本身就是这里要测的性质之一。
func settingsTables() []any {
	return []any{&Order{}, &UserState{}, &LookupLog{}, &GroupRule{},
		&qymodel.Setting{}, &qymodel.AuditLog{}}
}

// newSettingsTestDB 建一个承载扩展库全部划转相关表的测试库,并接到 db.Get()。
//
// 用文件库而不是 ":memory:":effectiveCtx 走的是 db.Get() 拿到的连接,
// 与 handler 里那条查 UserState 的语句不在同一个上下文,":memory:" 按连接
// 隔离会让它们各看到一个空库。
func newSettingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	// db.LockForUpdate 无条件挂 clause.Locking{Strength:"UPDATE"}(扩展库固定
	// 是 MySQL),而 sqlite 不认 FOR UPDATE。把 FOR 子句渲染成空,让语句其余
	// 部分原样交给数据库执行。测试是单协程的,行锁在这里本来也没有语义。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}

	require.NoError(t, gdb.AutoMigrate(settingsTables()...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	invalidateSettings()
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		invalidateSettings()
		_ = sqlDB.Close()
	})
	return gdb
}

// useSettingsConfig 临时替换扩展的全局配置快照。
// Audit.Enabled 必须显式打开:审计断言是本文件的一半内容。
func useSettingsConfig(t *testing.T, tr config.Transfer) {
	t.Helper()
	enabled := true
	prev := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: tr,
		Audit:    config.Audit{Enabled: &enabled, SnapshotMaxBytes: 4096},
	})
	t.Cleanup(func() { qyConfig.Store(prev) })
}

// putSetting 直接往 qy_settings 里写一条覆盖(模拟"运营已经配过"或"有人手工改库")。
func putSetting(t *testing.T, gdb *gorm.DB, key, value string) {
	t.Helper()
	require.NoError(t, gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "updated_at"}),
	}).Create(&qymodel.Setting{
		Scope: settingScope, K: key, V: value, UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()
}

func settingRows(t *testing.T, gdb *gorm.DB) []qymodel.Setting {
	t.Helper()
	var rows []qymodel.Setting
	require.NoError(t, gdb.Where("scope = ?", settingScope).Order("k asc").Find(&rows).Error)
	return rows
}

func configAudits(t *testing.T, gdb *gorm.DB) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", "transfer.config.update").
		Order("id asc").Find(&rows).Error)
	return rows
}

// callTransferHandler 驱动一个划转接口处理器。
func callTransferHandler(t *testing.T, method, target, body string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("username", "admin1")
	h(c)
	return rec
}

// failOnSettingKey 让写到某个配置键的那条 INSERT 报错。
//
// 模拟的是 MySQL 死锁:它既不是参数校验错误(前置校验挡不住),也不是连接级
// 错误(db.MarkFailure 不会因此打开熔断),正是"批量保存写到一半炸掉"最真实的形状。
func failOnSettingKey(t *testing.T, gdb *gorm.DB, key string) {
	t.Helper()
	const name = "test:transfer_fail_on_setting_key"
	require.NoError(t, gdb.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*qymodel.Setting)
		if !ok || row.K != key {
			return
		}
		tx.AddError(errors.New("Error 1213: Deadlock found when trying to get lock; try restarting transaction"))
	}))
	t.Cleanup(func() { _ = gdb.Callback().Create().Remove(name) })
}

// invalidateSettingsDuringQuery 在第一条打到 table 的查询返回之后立刻提交一次改动。
// 用来复现"在途快照把已经生效的新配置按回去"这个窗口。
func invalidateSettingsDuringQuery(t *testing.T, gdb *gorm.DB, table string, commit func()) {
	t.Helper()
	const name = "test:transfer_invalidate_during_query"
	var once sync.Once
	require.NoError(t, gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != table {
			return
		}
		once.Do(commit)
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(name) })
}

// limitsPayload 是 /transfer/limits 响应里我们关心的那几个字段。
type limitsPayload struct {
	Data struct {
		MinQuota            int64 `json:"min_quota"`
		MaxPerTxQuota       int64 `json:"max_per_tx_quota"`
		DailyMaxQuota       int64 `json:"daily_max_quota"`
		DailyMaxCount       int   `json:"daily_max_count"`
		RemainingDailyQuota int64 `json:"remaining_daily_quota"`
		RemainingDailyCount int   `json:"remaining_daily_count"`
		FeeBps              int   `json:"fee_bps"`
	} `json:"data"`
}

// ───────────────────────────── 3-A / 3-C:门槛真的通到用户端 ─────────────────────────────

// TestGetLimitsServesAdminOverridesNotYaml 是需求 3-A 的**端到端**回归,
// 同时钉死 3-C 的那条链路。
//
// 场景:YAML 里日额度是 2 亿、日次数 20,运营在管理端把它们改成 100 万 / 3 次。
// 用户打开划转页,/transfer/limits 必须回运营配的那一份,并据此算出今日剩余。
//
// 缺陷版本(handleGetLimits 读 config.Get().Transfer)的后果不是"少显示一点",
// 而是**界面说还能转 2 亿、提交时被拒**:提交路径读的是覆盖后的值。
//
// 回滚验证:把 `settings.Transfer` 换回 `config.Get().Transfer`,
// daily_max_quota 断言立刻变红(2 亿 ≠ 100 万)。
func TestGetLimitsServesAdminOverridesNotYaml(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())
	mainDB := newMainDB(t)
	seedMainUser(t, mainDB, 1, "default", 90000000)

	putSetting(t, gdb, keyMinQuota, "100000")
	putSetting(t, gdb, keyDailyMaxQuota, "1000000")
	putSetting(t, gdb, keyDailyMaxCount, "3")

	// 今天已经转出 40 万、1 笔。剩余量必须按**覆盖后的**日额度算。
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&UserState{
		UserId: 1, DayBucket: dayBucket(now),
		DayOutQuota: 400000, DayOutCount: 1, UpdatedAt: now,
	}).Error)

	rec := callTransferHandler(t, http.MethodGet, "/api/qy/transfer/limits", "", handleGetLimits)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got limitsPayload
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &got))

	assert.EqualValues(t, 1000000, got.Data.DailyMaxQuota,
		"用户端拿到的日额度必须是运营配的 100 万,而不是 YAML 里的 2 亿")
	assert.EqualValues(t, 3, got.Data.DailyMaxCount, "日次数同上")
	assert.EqualValues(t, 100000, got.Data.MinQuota, "单笔下限同上")
	assert.EqualValues(t, 50000000, got.Data.MaxPerTxQuota,
		"没有被覆盖的键必须原样来自 YAML —— 覆盖层不得把没配过的字段清零")

	// 3-C:剩余量按覆盖后的门槛算出来。门槛为 0(不限)时前端整块不显示,
	// 所以这两个数只有在门槛大于 0 时才有意义,而它现在确实大于 0 了。
	assert.EqualValues(t, 600000, got.Data.RemainingDailyQuota,
		"今日剩余额度 = 覆盖后的日额度 100 万 - 已转出 40 万")
	assert.EqualValues(t, 2, got.Data.RemainingDailyCount,
		"今日剩余次数 = 覆盖后的日次数 3 - 已用 1")
}

// TestGetLimitsHidesRemainingWhenLimitIsZero 钉住 3-C 的另一半口径:
// 门槛配成 0(不限)时,剩余量必须保持 0 —— 前端据此整块不渲染。
//
// 这条不是可有可无的对称性测试:如果后端在"不限"时把剩余量填成一个大数,
// 前端那个 `if (limits.daily_max_quota > 0)` 判断就成了唯一的防线,
// 而任何人删掉它都会让用户看到一个凭空捏造的"今日还可转 2 亿"。
func TestGetLimitsHidesRemainingWhenLimitIsZero(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())
	mainDB := newMainDB(t)
	seedMainUser(t, mainDB, 1, "default", 90000000)

	putSetting(t, gdb, keyDailyMaxQuota, "0")
	putSetting(t, gdb, keyDailyMaxCount, "0")

	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&UserState{
		UserId: 1, DayBucket: dayBucket(now),
		DayOutQuota: 400000, DayOutCount: 1, UpdatedAt: now,
	}).Error)

	rec := callTransferHandler(t, http.MethodGet, "/api/qy/transfer/limits", "", handleGetLimits)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got limitsPayload
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &got))
	assert.Zero(t, got.Data.DailyMaxQuota, "0 就是运营配的值,不得被 YAML 顶回去")
	assert.Zero(t, got.Data.RemainingDailyQuota, "不限时不得下发任何剩余额度")
	assert.Zero(t, got.Data.RemainingDailyCount, "不限时不得下发任何剩余次数")
}

// ───────────────────────────── 锁内闸门用的是受理时那份快照 ─────────────────────────────

// TestReserveRiskJudgesByTheSnapshotItWasGiven 钉死"create() 只取一次门槛快照"。
//
// 场景:受理时的快照说"每天只能转 1 笔",而全局配置与 qy_settings 都说 100 笔
// —— 这正是"运营在用户提交与落账之间改了一次门槛"的形状。
// 锁内那道闸门必须按传进来的快照判,不得自己回头再读一次配置。
//
// 两处读不同的门槛是资损:方向一是用户白吃一次冷却与风控预占(受理放行、
// 锁内拒绝);方向二更糟 —— 用户看到的是旧门槛,而钱按新门槛放行了。
//
// 回滚验证:把 evaluateRisk 的入参换回 config.Get().Transfer,
// 第二笔会被放行(100 笔的额度),`第二笔必须被拒` 断言变红。
func TestReserveRiskJudgesByTheSnapshotItWasGiven(t *testing.T) {
	gdb := newSettingsTestDB(t)

	// 全局配置与 qy_settings 一致地宽松:任何"自己回头再读一次"的写法
	// 都会读到 100 笔,从而放行第二笔。
	loose := baseConfig()
	loose.DailyMaxCount = 100
	loose.CooldownSecs = 0
	useSettingsConfig(t, loose)
	putSetting(t, gdb, keyDailyMaxCount, "100")

	// 受理时冻结的快照:每天 1 笔。冷却关掉,否则第二笔会先撞冷却,
	// 那样即使闸门失效测试也照样"通过"——一次典型的假回归。
	snapshot := baseConfig()
	snapshot.DailyMaxCount = 1
	snapshot.CooldownSecs = 0

	acc := acceptedRequest{FromUserId: 1, ToUserId: 2, Amount: 1000000, Total: 1000000}
	now := common.GetTimestamp()

	require.NoError(t, reserveRisk(gdb, acc, snapshot, now), "第一笔应放行")

	// 清掉未结算标记:否则第二笔会因 pending_count > 0 被拒,
	// 而那与日次数闸门毫无关系 —— 又一次假回归的入口。
	require.NoError(t, gdb.Model(&UserState{}).Where("user_id = ?", 1).
		Update("pending_count", 0).Error)

	err := reserveRisk(gdb, acc, snapshot, now)
	assert.ErrorIs(t, err, errDailyCountExceeded,
		"第二笔必须被拒:锁内闸门只能按受理时那份快照判,不得自己回头读配置")
}

// ───────────────────────────── 管理端写入 ─────────────────────────────

// TestAdminPutConfigAppliesImmediatelyAndAudits 是happy path:
// 写库 + 立即生效 + 审计留痕,三件事缺一不可。
func TestAdminPutConfigAppliesImmediatelyAndAudits(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())

	rec := callTransferHandler(t, http.MethodPut, "/api/qy/admin/transfer/config",
		`{"daily_max_quota":"1000000","cooldown_seconds":30}`, adminPutTransferConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows := settingRows(t, gdb)
	require.Len(t, rows, 2)
	assert.Equal(t, keyCooldownSecs, rows[0].K)
	assert.Equal(t, "30", rows[0].V)
	assert.Equal(t, 1, rows[0].OperatorId, "必须记下改动人")
	assert.Equal(t, keyDailyMaxQuota, rows[1].K)
	assert.Equal(t, "1000000", rows[1].V)

	// 立即生效:缓存必须在保存后失效,否则运营会以为"改了没用"再改一遍。
	s, err := effectiveCtx(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1000000, s.Transfer.DailyMaxQuota)
	assert.Equal(t, 30, s.Transfer.CooldownSecs)

	logs := configAudits(t, gdb)
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Contains(t, logs[0].BeforeSnap, `"daily_max_quota":200000000`)
	assert.Contains(t, logs[0].AfterSnap, `"daily_max_quota":1000000`)
}

// TestAdminPutConfigFailedBatchRollsBackAndAudits 守"整批一个事务 + 失败也留痕"。
//
// 场景:一次保存两个键,第二个键撞死锁。缺陷版本的后果是第一个键已经落库、
// 接口回 500、审计表一条记录都没有 —— 库里留下一组谁都没批准过的门槛,
// 而所有节点会在 60 秒内开始按它放行资金操作。
//
// 回滚验证:把 Transaction 换回裸 for 循环,`残留的键` 断言变红;
// 单独删掉失败分支的审计,`失败必须留痕` 断言变红。两处互不遮蔽。
func TestAdminPutConfigFailedBatchRollsBackAndAudits(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())
	// 键名排序后 cooldown_seconds 在 daily_max_count 之前,
	// 因此前者会先写成功,再由后者触发失败 —— 正是"写到一半"的那一刻。
	failOnSettingKey(t, gdb, keyDailyMaxCount)

	rec := callTransferHandler(t, http.MethodPut, "/api/qy/admin/transfer/config",
		`{"cooldown_seconds":30,"daily_max_count":5}`, adminPutTransferConfig)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	assert.Empty(t, settingRows(t, gdb),
		"批量保存失败后不能有任何一个键残留:半套门槛是谁都没有批准过的门槛")

	invalidateSettings()
	s, err := effectiveCtx(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 10, s.Transfer.CooldownSecs, "冷却必须仍是改动前的 10 秒")

	logs := configAudits(t, gdb)
	require.Len(t, logs, 1,
		"失败必须留痕:运营看到 500 会重试,没有这条记录就分不清库里的值是哪一次写的")
	assert.Equal(t, qymodel.ResultFail, logs[0].Result)
	assert.Contains(t, logs[0].Reason, "回滚")
	assert.Contains(t, logs[0].AfterSnap, `"cooldown_seconds":10`,
		"AfterSnap 必须是回滚之后重新读库的真实值 —— 它才是「库里现在到底是什么」的凭据")
}

// TestAdminPutConfigRejectsKeysOutsideTheWhitelist 守白名单。
//
// qy_settings 是**所有模块共用**的 KV 表。让管理端往里写任意键,等于把别的
// 模块(佣金费率、站点主题)的配置面也一起交出去了。
//
// # 为什么这里必须断言 error code 而不只是 400
//
// 实测过:把 editable() 那道检查整段删掉,只断言状态码的版本**照样全绿** ——
// 未知键会继续掉进 parseSetting,而它对 settingBounds 里没有的键同样回 400。
// 那是一次典型的假回归:测试保护的其实是另一道闸门,而白名单已经没了。
// code 是对外契约的一部分(见 errors.go 的说明),断言它才能把两道闸门分开。
func TestAdminPutConfigRejectsKeysOutsideTheWhitelist(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())

	for _, body := range []string{
		`{"recipient_lookup":"id_or_email"}`, // YAML 段:安全口径,不许在线改
		`{"topup_rate_percent":"99"}`,        // 别的模块的键
		`{"cooldown_seconds":30,"enabled":false}`,
	} {
		rec := callTransferHandler(t, http.MethodPut, "/api/qy/admin/transfer/config",
			body, adminPutTransferConfig)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)

		var got struct {
			Code string `json:"code"`
		}
		require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, errConfigKeyNotEditable.Code, got.Code,
			"必须是白名单拒绝的那个 code,而不是取值校验的: "+body)
	}
	assert.Empty(t, settingRows(t, gdb),
		"含非法键的请求必须整个拒绝:放行同一批里的合法键等于半套配置")
}

// TestAdminPutConfigRejectsValuesOutsideBounds 守取值区间。
func TestAdminPutConfigRejectsValuesOutsideBounds(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())

	cases := map[string]string{
		"负数":         `{"cooldown_seconds":-1}`,
		"超过主库额度上限":   `{"max_per_tx_quota":2147483648}`,
		"费率超过 100%":  `{"fee_bps":10001}`,
		"支付密码阈值为 0":  `{"pay_pwd_max_attempts":0}`,
		"锁定时长为 0":    `{"pay_pwd_lock_minutes":0}`,
		"不是整数":       `{"daily_max_count":"abc"}`,
		"小数":         `{"daily_max_count":1.5}`,
		"支付密码阈值上界之外": `{"pay_pwd_max_attempts":101}`,
	}
	for name, body := range cases {
		rec := callTransferHandler(t, http.MethodPut, "/api/qy/admin/transfer/config",
			body, adminPutTransferConfig)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name+": "+body)
	}
	assert.Empty(t, settingRows(t, gdb))
}

// TestAdminPutConfigRejectsCrossFieldContradiction 守跨字段校验。
//
// 单笔下限被调到单笔上限之上时,**任何金额都不合法** —— 划转被静默关停,
// 而每一个字段单看都在自己的区间里。这正是逐字段校验抓不到的那一类。
//
// 回滚验证:删掉 adminPutTransferConfig 里的 config.ValidateTransfer 调用,
// 接口会回 200 并把 min_quota 写进库,两条断言同时变红。
func TestAdminPutConfigRejectsCrossFieldContradiction(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig()) // max_per_tx_quota = 5000 万

	rec := callTransferHandler(t, http.MethodPut, "/api/qy/admin/transfer/config",
		`{"min_quota":60000000}`, adminPutTransferConfig)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, settingRows(t, gdb),
		"下限高于上限时任何金额都不合法,这组配置一个字都不该落库")
}

// ───────────────────────────── 读取路径的失效与降级 ─────────────────────────────

// TestEffectiveSettingsRefuseToFallBackToYaml 守"读不到覆盖时绝不悄悄放宽"。
//
// 与本模块 loadGroupRules 同一条口径:回落 YAML 默认值往往比运营配置**更宽松**
// (YAML 的默认日额度是 2 亿),那等于扩展库抖一下就把风控门槛整体放开。
func TestEffectiveSettingsRefuseToFallBackToYaml(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())

	require.NoError(t, gdb.Migrator().DropTable(&qymodel.Setting{}))
	invalidateSettings()

	_, err := effectiveCtx(context.Background())
	assert.Error(t, err,
		"一份快照都没有时必须报错,而不是拿 YAML 的宽松默认值去放行划转")
}

// TestEffectiveSettingsServeStaleSnapshotWhenUnreadable 是上一条的另一半:
// 已经有过一份运营配置时,读失败必须沿用它(最多陈旧 60 秒),而不是回落 YAML。
func TestEffectiveSettingsServeStaleSnapshotWhenUnreadable(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())
	putSetting(t, gdb, keyDailyMaxQuota, "777777")

	s, err := effectiveCtx(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 777777, s.Transfer.DailyMaxQuota, "前提:先成功读到一份")

	// 让缓存过期但不清空 —— 这正是"60 秒到了,回库刷新时撞上故障"的形状。
	// invalidateSettings 会把快照清掉,那模拟的是另一件事(管理端刚改过)。
	settingsMu.Lock()
	settingsLoaded = 0
	settingsMu.Unlock()
	require.NoError(t, gdb.Migrator().DropTable(&qymodel.Setting{}))

	s, err = effectiveCtx(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 777777, s.Transfer.DailyMaxQuota,
		"读失败时沿用上一份运营快照,绝不回落到 YAML 的 2 亿")
}

// TestOutOfRangeOverrideIsDroppedNotClamped 守"手工改坏的值只丢弃,不钳到边界"。
//
// qy_settings 是可以被人直接 UPDATE 的。一个被写坏的超大单笔上限若被钳成
// MaxQuota,就会静默地把单笔上限放开到主库额度上限;丢弃只是回落到 YAML 的
// 5000 万,损失有界且可解释。
func TestOutOfRangeOverrideIsDroppedNotClamped(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())

	putSetting(t, gdb, keyMaxPerTxQuota, "2147483648") // MaxQuota + 1
	putSetting(t, gdb, keyDailyMaxCount, "not-a-number")
	putSetting(t, gdb, keyCooldownSecs, "30") // 合法值必须照常生效

	s, err := effectiveCtx(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 50000000, s.Transfer.MaxPerTxQuota,
		"越界值必须被丢弃并回落 YAML,不得钳到 MaxQuota")
	assert.Equal(t, 20, s.Transfer.DailyMaxCount, "解析不了的值同样回落 YAML")
	assert.Equal(t, 30, s.Transfer.CooldownSecs,
		"同一批里的合法值不受影响 —— 一个坏值不该让整份配置失效")
}

// TestSettingsInvalidationSurvivesInFlightLoad 是代次校验的回归。
//
// 场景:某个请求的 SELECT 读到旧门槛 → 管理员保存了新门槛并 invalidateSettings()
// → 该请求回来把旧快照无条件写回缓存并盖上新时间戳。此后 60 秒全按已经作废的
// 门槛放行,而管理端界面显示的是新值。
//
// 回滚验证:去掉写回前的 `settingsEpoch == epoch` 判断,第二次读仍是 100 万,
// `不得把它按回去` 断言变红。
func TestSettingsInvalidationSurvivesInFlightLoad(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())
	putSetting(t, gdb, keyDailyMaxQuota, "1000000")

	invalidateSettingsDuringQuery(t, gdb, "qy_settings", func() {
		require.NoError(t, gdb.Model(&qymodel.Setting{}).
			Where("scope = ? AND k = ?", settingScope, keyDailyMaxQuota).
			Update("v", "2000000").Error)
		invalidateSettings()
	})

	ctx := context.Background()
	first, err := effectiveCtx(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1000000, first.Transfer.DailyMaxQuota, "前提:本次读到的确实是在途旧快照")

	second, err := effectiveCtx(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2000000, second.Transfer.DailyMaxQuota,
		"管理端已经把日额度改成 200 万并失效了缓存,在途的旧快照不得把它按回去")
}

// TestAdminConfigStaysReachableWhenSettingsAreCorrupt 守"别把管理员锁在门外"。
//
// 场景:有人绕过接口直接 UPDATE qy_settings,写出一组自相矛盾的门槛
// (下限 6000 万 > 上限 5000 万)。资金路径必须停(失败关闭),
// 但管理端**恰恰是修复它的唯一界面** —— 它要是也打不开,救场就只剩直连数据库。
//
// 回滚验证:把 adminGetTransferConfig / adminPutTransferConfig 改回调用
// effectiveCtx,两个接口双双回 503,`管理端必须仍然可达` 与 `管理员必须能改回来`
// 同时变红。
func TestAdminConfigStaysReachableWhenSettingsAreCorrupt(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())
	mainDB := newMainDB(t)
	seedMainUser(t, mainDB, 1, "default", 90000000)

	// 逐字段都在区间内,组合起来却让任何金额都不合法。
	putSetting(t, gdb, keyMinQuota, "60000000")

	rec := callTransferHandler(t, http.MethodGet, "/api/qy/transfer/limits", "", handleGetLimits)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"资金路径必须失败关闭:绝不拿一组没人批准过的门槛去放行划转")

	rec = callTransferHandler(t, http.MethodGet, "/api/qy/admin/transfer/config", "",
		adminGetTransferConfig)
	require.Equal(t, http.StatusOK, rec.Code,
		"管理端必须仍然可达,否则修复这一行只能靠直连数据库")
	var got struct {
		Data struct {
			EffectiveValid bool `json:"effective_valid"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &got))
	assert.False(t, got.Data.EffectiveValid,
		"界面必须知道当前这组值是非法的,否则运营看不出划转为什么停了")

	rec = callTransferHandler(t, http.MethodPut, "/api/qy/admin/transfer/config",
		`{"min_quota":500000}`, adminPutTransferConfig)
	require.Equal(t, http.StatusOK, rec.Code, "管理员必须能改回来: "+rec.Body.String())

	rec = callTransferHandler(t, http.MethodGet, "/api/qy/transfer/limits", "", handleGetLimits)
	assert.Equal(t, http.StatusOK, rec.Code, "改回合法值之后划转必须立刻恢复")
}

// TestCorruptSettingsAreNeverCached 守"非法组合不得变成降级兜底的那份快照"。
//
// 一旦它进了缓存,扩展库随后抖一下,读失败路径就会把这份没人批准过的门槛
// 当作"上一份运营快照"继续用下去。
func TestCorruptSettingsAreNeverCached(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())
	putSetting(t, gdb, keyMinQuota, "60000000")

	_, valid, err := resolveSettings(context.Background())
	require.NoError(t, err)
	require.False(t, valid, "前提:这组值确实不自洽")

	settingsMu.Lock()
	cached := settingsCache
	settingsMu.Unlock()
	assert.Nil(t, cached, "不自洽的配置绝不能进缓存")
}

// ───────────────────────────── 支付密码策略的交接契约 ─────────────────────────────

// TestPayPasswordKeysAreWritableAndReadBack 守支付密码那两个键的**写入侧**。
//
// 键的读取侧在 qianye/modules/paypass(验密失败计数与锁定判定),它不能反过来
// import 本包 —— 划转的执行入口要调 paypass.Require,再互相 import 就成环。
// 因此这里守的是本包该负责的那一半:管理端能把它们存进去、读回来仍是那个值;
// 两份常量的一致性由 paypass 的 settings_contract_test.go 顶着。
//
// 这条不是"测 map 能存能取":它走的是完整的 PUT → qy_settings → 合并读取,
// 把"管理端写的那个数字最终会不会到验密侧"整条链钉住。
func TestPayPasswordKeysAreWritableAndReadBack(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, baseConfig())

	s, err := effectiveCtx(context.Background())
	require.NoError(t, err)
	assert.Equal(t, defaultPayPwdMaxAttempts, s.PayPwdMaxAttempts, "未配置时用默认阈值")
	assert.Equal(t, defaultPayPwdLockMinutes, s.PayPwdLockMinutes, "未配置时用默认锁定时长")

	rec := callTransferHandler(t, http.MethodPut, "/api/qy/admin/transfer/config",
		`{"pay_pwd_max_attempts":3,"pay_pwd_lock_minutes":120}`, adminPutTransferConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// 落库的行键必须与 paypass 那边读的键**逐字一致**,否则那边永远读到默认值。
	rows := settingRows(t, gdb)
	require.Len(t, rows, 2)
	assert.Equal(t, keyPayPwdLockMinutes, rows[0].K)
	assert.Equal(t, "120", rows[0].V)
	assert.Equal(t, keyPayPwdMaxAttempts, rows[1].K)
	assert.Equal(t, "3", rows[1].V)

	s, err = effectiveCtx(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, s.PayPwdMaxAttempts)
	assert.Equal(t, 120, s.PayPwdLockMinutes)
}

// TestEditableKeysAndAssignSettingStayInSync 守"白名单里的每一个键都真的会生效"。
//
// 白名单与 assignSetting 的 switch 是两处登记。只在白名单里加一个键、忘了在
// switch 里接上,后果是:管理端能保存、库里有行、界面显示"已覆盖",而闸门
// 压根没变 —— 这正是本仓库登记过四次的"配置项零消费方"。
//
// 反向也要守:settingBounds 缺一个键会让 parseSetting 直接拒绝保存。
func TestEditableKeysAndAssignSettingStayInSync(t *testing.T) {
	for _, key := range editableKeys {
		_, ok := settingBounds[key]
		require.True(t, ok, "%s 没有登记取值区间,任何保存都会被拒", key)

		s := opSettings{}
		// 用区间下界 +1(仍在区间内)写进去,再从生效配置里读回来。
		// 值必须真的落到某个字段上,否则读回来还是零值。
		want := settingBounds[key].Lo + 1
		assignSetting(&s, key, want)
		assert.Equal(t, want, settingValue(s, key),
			"%s 在 assignSetting/settingValue 里没有接上:保存会成功,闸门却不会变", key)
	}
}

// TestSettingsSnapshotCoversEveryEditableKey 守审计快照的完整性。
// 快照少一个键,那一项的改动在审计里就是隐形的。
func TestSettingsSnapshotCoversEveryEditableKey(t *testing.T) {
	snap := settingsSnapshot(opSettings{Transfer: baseConfig()})
	require.Len(t, snap, len(editableKeys))
	for _, key := range editableKeys {
		_, ok := snap[key]
		assert.True(t, ok, "审计快照缺少 %s", key)
	}
	assert.EqualValues(t, int64(500000), snap[keyMinQuota])
	assert.EqualValues(t, int64(20), snap[keyDailyMaxCount])
}
