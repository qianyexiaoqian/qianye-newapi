package stardust

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_admin_adjust_test.go —— 手调的四道契约:幂等先于上下界、扣减不得到负、
// 单次上限、三道闸门(超管档、自营、越级)。每一条都对应一个"把实现改坏之后
// 接口照常 200"的缺陷形状。

const adjustActor = 7

func adjustBody(userId int, delta int64, reason, crid string) string {
	return fmt.Sprintf(`{"user_id":%d,"delta":%d,"reason":%q,"client_request_id":%q}`, userId, delta, reason, crid)
}

func TestAdminAdjustCreditsReplaysAndRejectsConflicts(t *testing.T) {
	env := newAPIEnv(t, func(s *config.Stardust) { s.MaxManualAdjust = 1000 })
	env.seedUser(t, adjustActor, "root7", common.RoleRootUser)
	env.seedUser(t, 801, "alice", common.RoleCommonUser)
	r := adminRouter(adjustActor, common.RoleRootUser)
	const path = "/api/qy/admin/stardust/adjust"

	data := dataOf(t, call(t, r, http.MethodPost, path, adjustBody(801, 100, "活动补发", "Req-A")))
	assert.Equal(t, false, data["replayed"])
	assert.EqualValues(t, 100, data["balance_after"])
	assert.EqualValues(t, 100, data["delta"])
	assert.EqualValues(t, 801, data["user_id"])
	ledgerNo, _ := data["ledger_no"].(string)
	assert.True(t, strings.HasPrefix(ledgerNo, "SD"), ledgerNo)
	bal := balanceOf(t, env.ext, 801)
	require.NotNil(t, bal)
	assert.EqualValues(t, 100, bal.Available)
	assert.EqualValues(t, 100, bal.TotalAdjusted, "手调落 total_adjusted,不污染 total_earned")
	assert.Zero(t, bal.TotalEarned)
	rows := ledgerOf(t, env.ext, 801)
	require.Len(t, rows, 1)
	assert.Equal(t, adjustIdemScope, rows[0].IdemScope)
	assert.Equal(t, "manual:7:req-a", rows[0].IdemKey, "幂等键含操作人,client 段折叠小写")
	assert.Equal(t, adjustActor, rows[0].OperatorId)
	assert.Contains(t, rows[0].Remark, "活动补发")

	logs := auditRowsOf(t, env.ext, adjustAuditAction)
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, qymodel.AuditCategoryStardust, logs[0].Category)
	assert.EqualValues(t, 100, logs[0].AmountQuota)
	assert.Equal(t, 801, logs[0].TargetUserId)
	assert.Equal(t, adjustActor, logs[0].ActorUserId)
	assert.Equal(t, ledgerNo, logs[0].TraceNo, "审计与流水行互相指得回去")
	assert.Contains(t, logs[0].BeforeSnap, `"available":0`)
	assert.Contains(t, logs[0].AfterSnap, `"available":100`)

	// 幂等重放:同一个键(大小写折叠后)返回原单,余额不动,审计写"重放"且金额 0。
	data = dataOf(t, call(t, r, http.MethodPost, path, adjustBody(801, 100, "活动补发", "req-a")))
	assert.Equal(t, true, data["replayed"])
	assert.Equal(t, ledgerNo, data["ledger_no"])
	assert.EqualValues(t, 100, data["balance_after"])
	assert.EqualValues(t, 100, balanceOf(t, env.ext, 801).Available)
	assert.Len(t, ledgerOf(t, env.ext, 801), 1)
	logs = auditRowsOf(t, env.ext, adjustAuditAction)
	require.Len(t, logs, 2)
	assert.Equal(t, qymodel.ResultOK, logs[1].Result)
	assert.Zero(t, logs[1].AmountQuota, "重放时账本没动,审计金额必须是 0")
	assert.Contains(t, logs[1].Reason, "重放")

	// 同一个键换了 delta → 409,不动账。
	assert.Equal(t, codeIdemConflict, codeOf(t,
		call(t, r, http.MethodPost, path, adjustBody(801, 50, "活动补发", "Req-A")), http.StatusConflict))
	assert.EqualValues(t, 100, balanceOf(t, env.ext, 801).Available)
	assert.Len(t, ledgerOf(t, env.ext, 801), 1)

	// 扣到负 → 400,且不留幂等残行(否则同键重试会被判成重放)。
	assert.Equal(t, codeInsufficient, codeOf(t,
		call(t, r, http.MethodPost, path, adjustBody(801, -150, "误发收回", "req-b")), http.StatusBadRequest))
	assert.EqualValues(t, 100, balanceOf(t, env.ext, 801).Available)
	assert.Len(t, ledgerOf(t, env.ext, 801), 1)

	// 超上限:两个方向都拦;负方向的上限判定先于余额判定。
	for _, delta := range []int64{1001, -1001} {
		assert.Equal(t, codeAdjustTooLarge, codeOf(t,
			call(t, r, http.MethodPost, path, adjustBody(801, delta, "大额补发", "req-c")), http.StatusBadRequest))
	}
	assert.Len(t, ledgerOf(t, env.ext, 801), 1)

	// 正常扣减:带符号净额。
	data = dataOf(t, call(t, r, http.MethodPost, path, adjustBody(801, -30, "误发收回", "req-d")))
	assert.EqualValues(t, 70, data["balance_after"])
	bal = balanceOf(t, env.ext, 801)
	assert.EqualValues(t, 70, bal.Available)
	assert.EqualValues(t, 70, bal.TotalAdjusted)

	// 幂等先于上下界:上限调低之后重放旧单仍返回原单,而不是按新上限拒绝。
	useConfig(t, stardustConfig(func(s *config.Stardust) { s.MaxManualAdjust = 10 }))
	data = dataOf(t, call(t, r, http.MethodPost, path, adjustBody(801, 100, "活动补发", "req-a")))
	assert.Equal(t, true, data["replayed"])
	assert.Equal(t, ledgerNo, data["ledger_no"])
	assert.EqualValues(t, 70, balanceOf(t, env.ext, 801).Available)

	okCount, failCount := countResults(auditRowsOf(t, env.ext, adjustAuditAction))
	assert.Equal(t, 4, okCount)
	assert.Equal(t, 4, failCount, "409 / 不足 / 两次超上限都要留痕")
}

func TestAdminAdjustRejectsBadInputBeforeTouchingLedger(t *testing.T) {
	env := newAPIEnv(t, nil)
	env.seedUser(t, adjustActor, "root7", common.RoleRootUser)
	env.seedUser(t, 801, "alice", common.RoleCommonUser)
	r := adminRouter(adjustActor, common.RoleRootUser)

	cases := []struct {
		name string
		body string
	}{
		{"事由太短", adjustBody(801, 10, "补发", "k1")},
		{"缺 client_request_id", adjustBody(801, 10, "活动补发", "")},
		{"client_request_id 带 #", adjustBody(801, 10, "活动补发", "a#1")},
		{"client_request_id 非 ASCII", adjustBody(801, 10, "活动补发", "请求一")},
		{"delta 为 0", adjustBody(801, 0, "活动补发", "k2")},
		{"缺 delta", `{"user_id":801,"reason":"活动补发","client_request_id":"k3"}`},
		{"缺 user_id", adjustBody(0, 10, "活动补发", "k4")},
		{"请求体不是对象", `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, r, http.MethodPost, "/api/qy/admin/stardust/adjust", tc.body)
			assert.Equal(t, codeBadRequest, codeOf(t, rec, http.StatusBadRequest))
		})
	}
	assert.Nil(t, balanceOf(t, env.ext, 801), "参数校验在加锁之前,不得凭空建余额行")
	assert.Empty(t, ledgerOf(t, env.ext, 801))
	okCount, failCount := countResults(auditRowsOf(t, env.ext, adjustAuditAction))
	assert.Zero(t, okCount)
	assert.Equal(t, len(cases), failCount, "每一次被拒的尝试都要留痕")
}

func TestAdminAdjustGates(t *testing.T) {
	// stardustConfig 绕过 applyDefaults,max_manual_adjust 不给就是 0 —— 而 0 是
	// "关掉手调"(fail-closed),最后那条放行用例会被上限先拦下。
	env := newAPIEnv(t, func(s *config.Stardust) { s.MaxManualAdjust = 1000 })
	env.seedUser(t, adjustActor, "root7", common.RoleRootUser)
	env.seedUser(t, 8, "admin8", common.RoleAdminUser)
	env.seedUser(t, 802, "bob", common.RoleCommonUser)
	env.seedUser(t, 803, "admin803", common.RoleAdminUser)
	const path = "/api/qy/admin/stardust/adjust"

	// role=10 能到达这条路由(AdminAuth 放行),必须恰好在闸门这一步被挡住,
	// handler 一行都不许跑:没有余额行、没有本模块的审计,只有闸门自己写的那条上游审计。
	rec := call(t, adminRouter(8, common.RoleAdminUser), http.MethodPost, path, adjustBody(802, 10, "补发星屑", "k1"))
	assert.Equal(t, middleware.RootActionRequiredCode, codeOf(t, rec, http.StatusForbidden))
	assert.Nil(t, balanceOf(t, env.ext, 802))
	assert.Empty(t, auditRowsOf(t, env.ext, adjustAuditAction))
	var denied []model.Log
	require.NoError(t, env.main.Where("type = ?", model.LogTypeManage).Find(&denied).Error)
	require.Len(t, denied, 1, "被拒的越权尝试由闸门写一条上游操作审计")
	assert.Contains(t, denied[0].Content, string(middleware.RootActionStardustAdjust))

	root := adminRouter(adjustActor, common.RoleRootUser)
	// 自营:root 给自己加也不行。
	rec = call(t, root, http.MethodPost, path, adjustBody(adjustActor, 10, "补发星屑", "k2"))
	assert.Equal(t, "qy_self_dealing", codeOf(t, rec, http.StatusForbidden))
	// 目标不存在:说"查不到",不说权限。
	rec = call(t, root, http.MethodPost, path, adjustBody(999, 10, "补发星屑", "k3"))
	assert.Equal(t, "qy_sd_user_not_found", codeOf(t, rec, http.StatusBadRequest))
	assert.Nil(t, balanceOf(t, env.ext, 999), "目标不存在时不得凭空建余额行")
	// root 谁都能管:给一个 role=10 的账号手调是允许的。
	data := dataOf(t, call(t, root, http.MethodPost, path, adjustBody(803, 10, "补发星屑", "k4")))
	assert.EqualValues(t, 10, data["balance_after"])

	logs := auditRowsOf(t, env.ext, adjustAuditAction)
	okCount, failCount := countResults(logs)
	assert.Equal(t, 1, okCount)
	assert.Equal(t, 2, failCount, "自营与目标不存在都要留痕")
	assert.Equal(t, adjustActor, logs[0].TargetUserId)
	assert.Equal(t, 999, logs[1].TargetUserId)
}
