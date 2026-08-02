package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/QuantumNous/new-api/common"
)

// request_audit_test.go —— 两张审计表的列表接口。
//
// 这里让查询真的打到 sqlite,而不是断言拼出来的 SQL 字符串:
// action 的前缀匹配靠的是 LIKE + ESCAPE,它到底匹配到哪些行只有数据库说了算,
// 而这正是这次要修的那个缺陷的核心 —— 输 `withdraw.` 返回空。

func newAuditListEnv(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&qymodel.AuditLog{}, &qymodel.RequestAudit{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{Enabled: true})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

func callAuditList(t *testing.T, handler gin.HandlerFunc, path, query string) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, path+"?"+query, nil)
	c.Set("id", 1)
	c.Set("username", "admin")
	handler(c)

	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success)
	return body.Data
}

func actionsOf(t *testing.T, data map[string]any) []string {
	t.Helper()
	items, ok := data["items"].([]any)
	require.True(t, ok, "响应里必须有 items")
	out := make([]string, 0, len(items))
	for _, it := range items {
		row, ok := it.(map[string]any)
		require.True(t, ok)
		out = append(out, row["action"].(string))
	}
	return out
}

// action 必须按前缀匹配。
//
// 修复前是精确匹配,而前端 placeholder 写着 `withdraw.approve`,诱导管理员
// 输 `withdraw.` 去看"提现这一块都发生过什么" —— 得到的是空列表,
// 而空列表看起来与"这段时间没有提现操作"完全一样。审计工具给出一个
// 看起来正常的错误答案,比直接报错危险得多。
func TestAdminListAuditLogs_ActionMatchesByPrefix(t *testing.T) {
	gdb := newAuditListEnv(t)
	for _, action := range []string{
		"withdraw.approve", "withdraw.reject", "withdraw.payee.create",
		"transfer.create", "site_theme.update",
	} {
		require.NoError(t, gdb.Create(&qymodel.AuditLog{
			Category: qymodel.AuditCategoryWithdraw, Action: action,
			ActorType: qymodel.ActorAdmin, Result: qymodel.ResultOK, CreatedAt: 1,
		}).Error)
	}

	got := actionsOf(t, callAuditList(t, AdminListAuditLogs, "/api/qy/admin/audit-logs", "action=withdraw."))
	assert.ElementsMatch(t,
		[]string{"withdraw.approve", "withdraw.reject", "withdraw.payee.create"}, got,
		"输 withdraw. 必须返回该前缀下的全部动作")

	// 精确名仍然要能查到:前缀匹配是精确匹配的超集,不能把老用法弄丢。
	got = actionsOf(t, callAuditList(t, AdminListAuditLogs, "/api/qy/admin/audit-logs", "action=withdraw.approve"))
	assert.Equal(t, []string{"withdraw.approve"}, got)

	// 下划线在 LIKE 里是"任意单字符"通配符。不转义的话 site_theme 会匹配到
	// siteXtheme —— 本项目的 action 里到处是下划线,这不是理论风险。
	require.NoError(t, gdb.Create(&qymodel.AuditLog{
		Category: qymodel.AuditCategoryConfig, Action: "siteXtheme.update",
		ActorType: qymodel.ActorAdmin, Result: qymodel.ResultOK, CreatedAt: 1,
	}).Error)
	got = actionsOf(t, callAuditList(t, AdminListAuditLogs, "/api/qy/admin/audit-logs", "action=site_theme"))
	assert.Equal(t, []string{"site_theme.update"}, got, "下划线必须被转义成字面量")

	// 用户输入里的 % 不得变成一个由调用方控制的通配符。
	got = actionsOf(t, callAuditList(t, AdminListAuditLogs, "/api/qy/admin/audit-logs", "action=%25"))
	assert.Empty(t, got, "输入的 %% 必须被当成普通字符")
}

// result / actor_type / ip 三个筛选是新加的。
// 没有它们,"某个 IP 都干了什么""失败的那些"只能靠翻页肉眼找。
func TestAdminListAuditLogs_FiltersByResultActorTypeAndIP(t *testing.T) {
	gdb := newAuditListEnv(t)
	rows := []qymodel.AuditLog{
		{Action: "withdraw.approve", Category: "withdraw", ActorType: qymodel.ActorAdmin,
			Result: qymodel.ResultOK, IP: "10.0.0.1", CreatedAt: 1},
		{Action: "withdraw.submit", Category: "withdraw", ActorType: qymodel.ActorUser,
			Result: qymodel.ResultFail, IP: "10.0.0.2", CreatedAt: 2},
		{Action: "commission.settle", Category: "commission", ActorType: qymodel.ActorSystem,
			Result: qymodel.ResultOK, IP: "", CreatedAt: 3},
	}
	for i := range rows {
		require.NoError(t, gdb.Create(&rows[i]).Error)
	}

	assert.Equal(t, []string{"withdraw.submit"},
		actionsOf(t, callAuditList(t, AdminListAuditLogs, "/api/qy/admin/audit-logs", "result=fail")))
	assert.Equal(t, []string{"commission.settle"},
		actionsOf(t, callAuditList(t, AdminListAuditLogs, "/api/qy/admin/audit-logs", "actor_type=system")))
	assert.Equal(t, []string{"withdraw.approve"},
		actionsOf(t, callAuditList(t, AdminListAuditLogs, "/api/qy/admin/audit-logs", "ip=10.0.0.1")))
}

// 请求台账最有价值的切片是**失败请求**:越权探测与暴力枚举全是失败请求。
// success 必须是三态(不传=全部),而不是"传了就只看成功"。
func TestAdminListRequestAudits_SuccessIsTriState(t *testing.T) {
	gdb := newAuditListEnv(t)
	rows := []qymodel.RequestAudit{
		{Action: "admin.withdraw.approve.create", Method: "POST", StatusCode: 200,
			Success: true, ActorUserId: 7, CreatedAt: 1},
		{Action: "admin.withdraw.approve.create", Method: "POST", StatusCode: 403,
			Success: false, ActorUserId: 9, IP: "1.2.3.4", CreatedAt: 2},
		{Action: "pay_password.recover.code.create", Method: "POST", StatusCode: 429,
			Success: false, ActorUserId: 9, IP: "1.2.3.4", CreatedAt: 3},
	}
	for i := range rows {
		require.NoError(t, gdb.Create(&rows[i]).Error)
	}

	all := actionsOf(t, callAuditList(t, AdminListRequestAudits, "/api/qy/admin/request-audits", ""))
	assert.Len(t, all, 3, "不传 success 必须返回全部")

	failed := actionsOf(t, callAuditList(t, AdminListRequestAudits, "/api/qy/admin/request-audits", "success=false"))
	assert.ElementsMatch(t,
		[]string{"admin.withdraw.approve.create", "pay_password.recover.code.create"}, failed)

	okOnly := actionsOf(t, callAuditList(t, AdminListRequestAudits, "/api/qy/admin/request-audits", "success=true"))
	assert.Len(t, okOnly, 1)

	// "这个 IP 都试过什么" —— 索取重置码的暴力枚举正是这个查询要回答的。
	byIP := actionsOf(t, callAuditList(t, AdminListRequestAudits, "/api/qy/admin/request-audits", "ip=1.2.3.4"))
	assert.Len(t, byIP, 2)

	// 两张表共用同一套 action 命名体系,前缀匹配也必须共用同一份实现。
	byPrefix := actionsOf(t, callAuditList(t, AdminListRequestAudits,
		"/api/qy/admin/request-audits", "action=admin.withdraw."))
	assert.Len(t, byPrefix, 2)
}
