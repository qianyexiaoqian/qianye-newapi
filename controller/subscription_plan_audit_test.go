package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// subscription_plan_audit_test.go —— 「谁把发售/停售时间改了」必须查得到。
//
// 本轮之前整个 controller/subscription.go 零审计:改一次 sale_end_at 就决定了
// 此后谁能付款,而事后 qy_audit_logs 里一行都没有(实测对照:同一时间窗内
// 佣金侧每一次配置写入都留了痕)。这里断的不是"有没有调用审计函数"
// (那由 qianye/audit_coverage_guard_test.go 的源码扫描负责),而是
// **一次真实的 HTTP 请求跑完之后,扩展库里真的多了一行、且 before/after
// 两份快照里的发售窗确实不同**。
//
// 少了 before,审计只能回答"现在是什么",回答不了"原来是什么" —— 而运营
// 事故复盘要问的恰好是后者。

// qyAuditDBHandle / qyAuditDBHealthy 借出 qianye/db 的连接句柄与熔断标志。
// audit.Write 通过 db.Available() + db.Get() 自取句柄,不接受注入;
// db.Init 又只会拨 MySQL,这是让审计写进测试库的唯一办法。
//
//go:linkname qyAuditDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyAuditDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyAuditDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyAuditDBHealthy atomic.Bool

// setupPlanAuditTest 备齐三样东西:主库(套餐)、扩展库(审计)、支付合规确认。
func setupPlanAuditTest(t *testing.T) (mainDB *gorm.DB, extDB *gorm.DB) {
	t.Helper()

	prevDB, prevLogDB := model.DB, model.LOG_DB
	prevRedis := common.RedisEnabled
	prevMainType, prevLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)

	dsn := fmt.Sprintf("file:%s_main?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	main, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = main, main
	require.NoError(t, main.AutoMigrate(&model.SubscriptionPlan{}))

	extDSN := fmt.Sprintf("file:%s_ext?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	ext, err := gorm.Open(sqlite.Open(extDSN), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	prevHandle := qyAuditDBHandle.Swap(ext)
	prevHealthy := qyAuditDBHealthy.Swap(true)

	// 套餐写接口的第一道闸门是支付合规确认,不打开就连不到审计那一行。
	payment := operation_setting.GetPaymentSetting()
	prevConfirmed, prevVersion := payment.ComplianceConfirmed, payment.ComplianceTermsVersion
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

	t.Cleanup(func() {
		payment.ComplianceConfirmed, payment.ComplianceTermsVersion = prevConfirmed, prevVersion
		qyAuditDBHandle.Store(prevHandle)
		qyAuditDBHealthy.Store(prevHealthy)
		model.DB, model.LOG_DB = prevDB, prevLogDB
		common.RedisEnabled = prevRedis
		common.SetDatabaseTypes(prevMainType, prevLogType)
		if sqlDB, e := main.DB(); e == nil {
			_ = sqlDB.Close()
		}
		if sqlDB, e := ext.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return main, ext
}

func callPlanAdmin(t *testing.T, handler gin.HandlerFunc, method, path, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	c.Set("id", 4242)
	c.Set("role", common.RoleRootUser)
	c.Set("username", "plan-operator")
	handler(c)
	return rec
}

func planAuditRows(t *testing.T, ext *gorm.DB, action string) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, ext.Where("action = ?", action).Order("id asc").Find(&rows).Error)
	return rows
}

// TestPlanSaleWindowChangeIsAuditable 走一次真实的 HTTP 更新,断言审计行里
// before/after 的发售窗确实不同 —— 这正是"谁把停售时间提前了"的答案所在。
func TestPlanSaleWindowChangeIsAuditable(t *testing.T) {
	mainDB, ext := setupPlanAuditTest(t)

	plan := model.SubscriptionPlan{
		Title: "audit-window-plan", PriceAmount: 1, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
		Enabled: true, SaleStartAt: 0, SaleEndAt: 1900000000,
	}
	require.NoError(t, mainDB.Create(&plan).Error)

	body := fmt.Sprintf(
		`{"plan":{"id":%d,"title":"audit-window-plan","price_amount":1,"currency":"USD",`+
			`"duration_unit":"month","duration_value":1,"enabled":true,`+
			`"sale_start_at":0,"sale_end_at":1800000000}}`, plan.Id)
	rec := callPlanAdmin(t, AdminUpdateSubscriptionPlan, http.MethodPut,
		"/api/subscription/admin/plans/"+fmt.Sprint(plan.Id), fmt.Sprint(plan.Id), body)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"success":true`)

	rows := planAuditRows(t, ext, "subscription.plan.update")
	require.Len(t, rows, 1, "一次成功的套餐更新必须且只留一行审计")
	row := rows[0]
	assert.Equal(t, qymodel.ResultOK, row.Result)
	assert.Equal(t, 4242, row.ActorUserId)
	assert.Equal(t, "plan-operator", row.ActorName)

	// before 必须是被覆盖掉的那一行,不是新值 —— 这条断言正是"审计能回答
	// 原来是什么"与"只能回答现在是什么"的分界。
	assert.Contains(t, row.BeforeSnap, `"sale_end_at":1900000000`)
	assert.Contains(t, row.AfterSnap, `"sale_end_at":1800000000`)
	assert.NotContains(t, row.BeforeSnap, `"sale_end_at":1800000000`)

	var stored model.SubscriptionPlan
	require.NoError(t, mainDB.Where("id = ?", plan.Id).First(&stored).Error)
	assert.EqualValues(t, 1800000000, stored.SaleEndAt, "库里也要真的改掉,审计不能是唯一的变化")
}

// TestPlanStatusToggleIsAuditable 锁住列表行内的快速上下架。
//
// 下架方向完全无症状:套餐只是从售卖页消失,与"从来没建过"无法区分。
func TestPlanStatusToggleIsAuditable(t *testing.T) {
	mainDB, ext := setupPlanAuditTest(t)

	plan := model.SubscriptionPlan{
		Title: "audit-status-plan", PriceAmount: 2, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
	}
	require.NoError(t, mainDB.Create(&plan).Error)

	rec := callPlanAdmin(t, AdminUpdateSubscriptionPlanStatus, http.MethodPut,
		"/api/subscription/admin/plans/"+fmt.Sprint(plan.Id)+"/status",
		fmt.Sprint(plan.Id), `{"enabled":false}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"success":true`)

	rows := planAuditRows(t, ext, "subscription.plan.status")
	require.Len(t, rows, 1)
	assert.Equal(t, qymodel.ResultOK, rows[0].Result)
	assert.Contains(t, rows[0].BeforeSnap, `"enabled":true`)
	assert.Contains(t, rows[0].AfterSnap, `"enabled":false`)
}

// TestPlanCreateIsAuditable 确认新建也留痕,且 before 为空、after 有价格。
//
// before 留空是有意义的信号:新建没有"原来的样子",而空串在详情里必须与
// "有快照但序列化失败"分得开(见 audit.snapshotJSON)。
func TestPlanCreateIsAuditable(t *testing.T) {
	_, ext := setupPlanAuditTest(t)

	rec := callPlanAdmin(t, AdminCreateSubscriptionPlan, http.MethodPost,
		"/api/subscription/admin/plans", "",
		`{"plan":{"title":"audit-create-plan","price_amount":7,"currency":"USD",`+
			`"duration_unit":"month","duration_value":1,"enabled":true,`+
			`"sale_start_at":1700000000,"sale_end_at":1800000000}}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"success":true`)

	rows := planAuditRows(t, ext, "subscription.plan.create")
	require.Len(t, rows, 1)
	assert.Equal(t, qymodel.ResultOK, rows[0].Result)
	assert.Equal(t, "", rows[0].BeforeSnap, "新建没有 before,必须是空串而不是 null 字面量")
	assert.Contains(t, rows[0].AfterSnap, `"price_amount":7`)
	assert.Contains(t, rows[0].AfterSnap, `"sale_start_at":1700000000`)
}

// TestPlanAuditSnapshotCarriesMoneyFields 钉住快照字段清单。
//
// 快照只取"决定能不能被买、按什么价、买到什么"的字段。少掉其中任何一格,
// 那一格的改动在事后就再也重建不出来 —— 而漏字段不会让任何别的测试变红。
func TestPlanAuditSnapshotCarriesMoneyFields(t *testing.T) {
	snap := planAuditSnapshot(&model.SubscriptionPlan{
		Id: 9, Title: "t", Enabled: true, PriceAmount: 3.5, Currency: "USD",
		DurationUnit: "month", DurationValue: 1,
		SaleStartAt: 111, SaleEndAt: 222, TotalAmount: 4444,
		MaxPurchasePerUser: 2, UpgradeGroup: "vip", DowngradeGroup: "default",
	})
	for _, key := range []string{
		"enabled", "price_amount", "sale_start_at", "sale_end_at",
		"total_amount", "max_purchase_per_user", "no_quota",
		"duration_unit", "duration_value", "upgrade_group", "downgrade_group",
	} {
		assert.Containsf(t, snap, key, "审计快照必须含 %s —— 它直接决定谁能付款/付多少/得到什么", key)
	}
	assert.EqualValues(t, 111, snap["sale_start_at"])
	assert.EqualValues(t, 222, snap["sale_end_at"])
	assert.Nil(t, planAuditSnapshot(nil), "没有 before 的场景必须给 nil,让 snapshotJSON 落成空串")
}
