package commission

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件守 F3(批量保存的事务性与失败留痕)与 F5 的管理端一侧(降级计数器
// 真的被健康接口吐出来)。
//
// 三条测试一律从 HTTP 处理器进,不直接调 writeSetting:F3 的缺陷不在
// writeSetting 里 —— 那个函数单独看没有任何问题,问题在**调用它的那个循环
// 没有事务、失败分支没有审计**。只测写入函数的话,把事务整段删掉测试照样全绿。

// qyDBHealthy 是熔断的健康标志。guard.RequireAPI 要求它为 true,
// 而它只有真的 db.Init 过才会被置上。
//
//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

// useAdminAPI 把扩展置成"库健康",让 guard.RequireAPI 放行到处理器本体。
func useAdminAPI(t *testing.T) {
	t.Helper()
	prev := qyDBHealthy.Swap(true)
	t.Cleanup(func() { qyDBHealthy.Store(prev) })
}

func callAdminHandler(t *testing.T, method, target, body string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 7)
	c.Set("username", "admin7")
	h(c)
	return rec
}

// failOnSettingKey 让写到某个配置键的那条 INSERT 报错。
//
// 模拟的是 MySQL 死锁:它既不是参数校验错误(前置校验挡不住),也不是连接级
// 错误(db.MarkFailure 不会因此打开熔断),正是"批量保存写到一半炸掉"最真实的形状。
func failOnSettingKey(t *testing.T, gdb *gorm.DB, key string) {
	t.Helper()
	const name = "test:fail_on_setting_key"
	require.NoError(t, gdb.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*qymodel.Setting)
		if !ok || row.K != key {
			return
		}
		tx.AddError(errors.New("Error 1213: Deadlock found when trying to get lock; try restarting transaction"))
	}))
	t.Cleanup(func() { _ = gdb.Callback().Create().Remove(name) })
}

func configAuditLogs(t *testing.T, gdb *gorm.DB) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", "commission.config.update").
		Order("id asc").Find(&rows).Error)
	return rows
}

func settingRows(t *testing.T, gdb *gorm.DB) []qymodel.Setting {
	t.Helper()
	var rows []qymodel.Setting
	require.NoError(t, gdb.Where("scope = ?", settingScope).Order("k asc").Find(&rows).Error)
	return rows
}

// TestAdminPutConfig_FailedBatchRollsBackAndAudits 是 F3 的本体。
//
// 场景:一次保存两个键,第二个键撞死锁。缺陷版本的后果是第一个键已经落库、
// 接口回 500、审计表一条记录都没有 —— 库里留下一个谁都没批准的费率,
// 而所有节点会在 60 秒内开始按它计佣。
//
// 回滚验证:把 Transaction 换回裸 for 循环,`残留的键` 断言变红;
// 单独删掉失败分支的审计,`失败必须留痕` 断言变红。两处互不遮蔽。
func TestAdminPutConfig_FailedBatchRollsBackAndAudits(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	// consume_rate_percent 按键名排在 holding_days 之前,因此它会先写成功,
	// 再由 holding_days 触发失败 —— 正是"写到一半"的那一刻。
	failOnSettingKey(t, gdb, keyHoldingDays)

	rec := callAdminHandler(t, http.MethodPut, "/api/qy/admin/commission/config",
		`{"consume_rate_percent":"8.25","holding_days":3}`, adminPutConfig)

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	assert.Empty(t, settingRows(t, gdb),
		"批量保存失败后不能有任何一个键残留:半套费率是谁都没有批准过的费率")

	invalidateSettings()
	assert.Equal(t, 500, effective().ConsumeRateUnits, "费率必须仍是改动前的 5%")

	logs := configAuditLogs(t, gdb)
	require.Len(t, logs, 1,
		"失败必须留痕:运营看到 500 会重试,没有这条记录就分不清库里的值是哪一次写的")
	assert.Equal(t, qymodel.ResultFail, logs[0].Result)
	assert.Contains(t, logs[0].Reason, "回滚")
	assert.Contains(t, logs[0].BeforeSnap, `"consume_rate_percent":"5"`)
	assert.Contains(t, logs[0].AfterSnap, `"consume_rate_percent":"5"`,
		"AfterSnap 必须是回滚之后重新读库的真实值 —— 它才是「库里现在到底是什么」的凭据")
}

// TestAdminPutConfig_SuccessWritesAllKeysAndAudits 守住成功那一路没有被事务改造弄坏。
func TestAdminPutConfig_SuccessWritesAllKeysAndAudits(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)

	rec := callAdminHandler(t, http.MethodPut, "/api/qy/admin/commission/config",
		`{"consume_rate_percent":"8.25","holding_days":3}`, adminPutConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	invalidateSettings()
	s := effective()
	assert.Equal(t, 825, s.ConsumeRateUnits)
	assert.Equal(t, 3, s.HoldingDays)
	assert.Len(t, settingRows(t, gdb), 2, "两个键都必须落库")

	logs := configAuditLogs(t, gdb)
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Contains(t, logs[0].BeforeSnap, `"consume_rate_percent":"5"`)
	assert.Contains(t, logs[0].AfterSnap, `"consume_rate_percent":"8.25"`)
}

// TestAdminHealth_ExposesDegradeCounters 是 F5 管理端一侧。
//
// 降级计数器只有被健康接口吐出来才有意义 —— 一个没人读的计数器与静默降级
// 没有区别,这正是 rateDecision.Matched 的下场。
func TestAdminHealth_ExposesDegradeCounters(t *testing.T) {
	newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	resetDegrade(settingsDegrade)
	resetDegrade(groupRateDegrade)
	t.Cleanup(func() {
		resetDegrade(settingsDegrade)
		resetDegrade(groupRateDegrade)
	})

	groupRateDegrade.note("读取分组费率失败: connection refused")

	rec := callAdminHandler(t, http.MethodGet, "/api/qy/admin/commission/health", "", adminHealth)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Data struct {
			Degraded struct {
				GroupRate struct {
					Count      int64  `json:"count"`
					LastAt     int64  `json:"last_at"`
					LastReason string `json:"last_reason"`
				} `json:"group_rate"`
				Settings struct {
					Count int64 `json:"count"`
				} `json:"settings"`
			} `json:"degraded"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))

	assert.EqualValues(t, 1, resp.Data.Degraded.GroupRate.Count,
		"分组费率降级次数必须出现在健康接口上,否则运营无从知道哪段时间的佣金要复核")
	assert.Positive(t, resp.Data.Degraded.GroupRate.LastAt)
	assert.Contains(t, resp.Data.Degraded.GroupRate.LastReason, "connection refused")
	assert.EqualValues(t, 0, resp.Data.Degraded.Settings.Count)
}

// 额度门槛不许越过主库额度上限 —— 越过之后佣金会**永远不再落账**。
//
// 具体形状:结算金额 net 在 computeSettlement 里已被 common.QuotaFromDecimalChecked
// 夹在 int32 内,而 min_settle_quota 若能填到 int32 之外,`net < minSettle` 就恒成立,
// net 恒为 0。全站所有邀请人的佣金从此不落账:不报错、不告警、没有日志,
// 未结算额一路累加。这不是"更严格的门槛",是一个永远无法被满足的门槛。
//
// 改成按 USD 录入之后触发它只需要敲 5 位数,所以它不再是"几乎不可能的手滑"。
// 划转与抽奖两页早就被后端下发的 bounds 堵住了,只有佣金页没有。
func TestAdminPutConfig_RejectsQuotaThresholdAboveInt32(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)

	rec := callAdminHandler(t, http.MethodPut, "/api/qy/admin/commission/config",
		`{"min_settle_quota":5000000000}`, adminPutConfig)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, settingRows(t, gdb), "被拒的请求不该留下任何一个键")

	// 边界本身必须放行 —— 上界写成 `>=` 会让一个合法的极值配置被拒。
	rec = callAdminHandler(t, http.MethodPut, "/api/qy/admin/commission/config",
		`{"min_settle_quota":2147483647}`, adminPutConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// 库里已经存着的越界值同样必须被丢弃,而不是照单全收。
//
// 写入侧的 400 只挡住管理端这一条路:qy_settings 是可以被人手工 UPDATE 的,
// 历史数据也不会因为今天加了校验就消失。回落到 YAML 默认值是有界的损失,
// 而照单全收换来的是"全站佣金永远不再落账"。
func TestQuotaOverride_DropsValuesAboveInt32(t *testing.T) {
	s := opSettings{MinSettleQuota: 1000, DailyCapQuota: 2000}
	applyOverrides(&s, map[string]string{
		"min_settle_quota":            "5000000000",
		"max_daily_quota_per_inviter": "2147483647",
	})
	assert.EqualValues(t, 1000, s.MinSettleQuota,
		"越界的门槛必须回落到默认值,而不是生效")
	assert.EqualValues(t, 2147483647, s.DailyCapQuota, "边界值必须照常生效")
}

// ───────────────── 账本体检:那条站内此前看不见的恒等式 ─────────────────

// ledgerCheckView 是健康面板体检段的测试侧形状。
type ledgerCheckView struct {
	OK                bool   `json:"ok"`
	CheckedUsers      int    `json:"checked_users"`
	SettleDrifted     int    `json:"settle_drifted_users"`
	SettleDriftTotal  string `json:"settle_drift_total"`
	SettleDriftWorst  string `json:"settle_drift_worst"`
	SettleWorstUserId int    `json:"settle_drift_worst_user_id"`
	BalanceDrifted    int    `json:"balance_drifted_users"`
	SelfInvitedUsers  int    `json:"self_invited_users"`
}

// healthLedgerCheck 取出健康面板的体检段。
func healthLedgerCheck(t *testing.T) ledgerCheckView {
	t.Helper()
	rec := callAdminHandler(t, http.MethodGet, "/api/qy/admin/commission/health", "", adminHealth)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			LedgerCheck ledgerCheckView `json:"ledger_check"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data.LedgerCheck
}

// TestAdminHealth_ReportsLedgerIdentityDrift 守的是"账本自己跟自己对不上,
// 但站内没有任何一个地方看得见"这件事。
//
// 两条恒等式:
//
//	I1  Σ计佣行.settled_amount(status≠voided) == Σ结算单(granted−reclaimed) + 未结算余数
//	I2  可提现 + 冻结 + 已提现 == 累计earned − 累计clawback
//
// I2 在余额页每一行上都算好了;**I1 跨三张表,此前没有任何视图答得了它**。
// 备份库实测:inviter 1622 的 I1 漂移 −0.3663,而他的 I2 恰好是 0 —— 于是余额页、
// 用户总表、健康面板三处都显示"这行账很正常"。本用例把那个形状复刻成 fixture:
// **只有 I1 坏、I2 完好**,体检必须点名是谁、差多少。
//
// 回滚验证:把 adminHealth 里的 ledger_check 去掉,或让 I1 那一段跟着 I2 一起算,
// 本用例立刻变红。
func TestAdminHealth_ReportsLedgerIdentityDrift(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	// 自邀请:计佣热路径已经挡住它(不会产生钱),但存量数据里没人会去查。
	seedUser(t, mainDB, 700, "self-inviter", 700, 1000)
	seedUser(t, mainDB, 701, "clean", 0, 1000)

	now := common.GetTimestamp()
	// 干净的一行:结算掉 500,结算单发了 500,余数 0 —— I1 与 I2 都成立。
	require.NoError(t, gdb.Create(&Balance{
		UserId: 701, AvailableQuota: 500, TotalEarnedQuota: 500,
		UnsettledAmount: decimal.Zero, AvailableFiat: decimal.Zero,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 701, 801
		a.GrossAmount, a.SettledAmount = decimal.NewFromInt(500), decimal.NewFromInt(500)
		a.Status = StatusSettled
	})
	require.NoError(t, gdb.Create(&Settlement{
		SettleNo: "S-CLEAN", UserId: 701, GrantedQuota: 500,
		DeltaAmount: decimal.NewFromInt(500), CarryBefore: decimal.Zero,
		CarryAfter: decimal.Zero, UsdRateWeighted: decimal.Zero,
		FiatDelta: decimal.Zero, CreatedAt: now,
	}).Error)

	// 坏的一行:计佣行说结算掉了 500.5,结算单只发了 500,余数记的是 0.1 ——
	// 少了 0.4。而四列额度自洽,I2 = 0(正是 1622 的形状)。
	require.NoError(t, gdb.Create(&Balance{
		UserId: 700, AvailableQuota: 500, TotalEarnedQuota: 500,
		UnsettledAmount: decimal.RequireFromString("0.1"), AvailableFiat: decimal.Zero,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	seedAccrual(t, gdb, 2, func(a *Accrual) {
		a.InviterId, a.InviteeId = 700, 802
		a.GrossAmount = decimal.RequireFromString("500.5")
		a.SettledAmount = decimal.RequireFromString("500.5")
		a.Status = StatusSettled
	})
	require.NoError(t, gdb.Create(&Settlement{
		SettleNo: "S-DRIFT", UserId: 700, GrantedQuota: 500,
		DeltaAmount: decimal.NewFromInt(500), CarryBefore: decimal.Zero,
		CarryAfter: decimal.Zero, UsdRateWeighted: decimal.Zero,
		FiatDelta: decimal.Zero, CreatedAt: now,
	}).Error)

	lc := healthLedgerCheck(t)
	assert.True(t, lc.OK)
	assert.Equal(t, 2, lc.CheckedUsers)
	assert.Equal(t, 1, lc.SettleDrifted, "只有 700 那一行对不上")
	assert.Equal(t, 700, lc.SettleWorstUserId, "体检必须点名是谁 —— 否则运营还是得自己写 SQL")
	assert.Equal(t, "0.4", lc.SettleDriftWorst, "500.5 − 500 − 0.1 = 0.4")
	assert.Equal(t, "0.4", lc.SettleDriftTotal)
	assert.Equal(t, 0, lc.BalanceDrifted,
		"I2 全都成立 —— 这正是这条漂移此前藏得住的原因:余额页那一列看不见它")
	assert.Equal(t, 1, lc.SelfInvitedUsers, "存量的自邀请数据要有人报出来")
}

// TestAdminSettleAcceptsUserIdFromQueryOrBody 守"参数位置不能与紧邻的同类
// 接口相反"。
//
// POST /commission/settle 与 POST /commission/balances/adjust 是同一组管理端
// 写接口,前者只读查询串、后者只读请求体。运营工具按 adjust 的形状发
// {"user_id":…} 过来,拿到的是 "必须指定 user_id" —— 一句与事实直接冲突的
// 提示;而且那一支在 badRequest 之前返回,连失败审计都不写,事后查不到有人试过。
//
// 断言从 HTTP 处理器进,三种发法都要能落到同一个 settleOne 上。
func TestAdminSettleAcceptsUserIdFromQueryOrBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		body   string
		want   int
	}{
		{name: "查询串", target: "/admin/commission/settle?user_id=4242", body: "", want: http.StatusOK},
		{name: "请求体", target: "/admin/commission/settle", body: `{"user_id":4242}`, want: http.StatusOK},
		{name: "两个都给时查询串优先", target: "/admin/commission/settle?user_id=4242", body: `{"user_id":7}`, want: http.StatusOK},
		{name: "两处都没有才 400", target: "/admin/commission/settle", body: `{}`, want: http.StatusBadRequest},
		{name: "报文坏了也不能当成给了", target: "/admin/commission/settle", body: `{`, want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			useConfig(t, commissionConfig(1))
			useMoneyGlobals(t, 7.3, 500000)
			useAdminAPI(t)
			// 4242 有一笔够发的余数:结算真的会落一张单,于是"到底settle了谁"
			// 有一个落在库里的判据,而不是只看 HTTP 码。
			seedBalance(t, gdb, 4242, "4000")

			rec := callAdminHandler(t, http.MethodPost, tc.target, tc.body, adminSettle)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())

			rows := settlementsOf(t, gdb, 4242)
			if tc.want != http.StatusOK {
				assert.Empty(t, rows, "参数没给全却真的结算了")
				return
			}
			require.Len(t, rows, 1, "接口回了 200 但没有任何一张结算单落库")
			assert.EqualValues(t, 4000, rows[0].GrantedQuota)
			assert.Empty(t, settlementsOf(t, gdb, 7),
				"请求体里的 user_id 盖掉了查询串 —— 结算落到了另一个人头上")
		})
	}
}
