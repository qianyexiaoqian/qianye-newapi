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
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
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
