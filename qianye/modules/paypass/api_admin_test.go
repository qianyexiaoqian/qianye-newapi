package paypass

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 管理员提前解除锁定,并且必须留下审计。
//
// 裁决 1 原话:「管理员可提前解除、可重置用户支付密码,这两个动作都必须写
// qianye/service/audit 审计。」没有审计的话,"谁在什么时候把这个账号解锁了"
// 在事后完全无法回答 —— 而解锁之后紧接着发生的往往就是一笔划转。
func TestAdminUnlockClearsLockAndWritesAudit(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	seedUser(t, mainDB, 7400, "victim", "")
	seedUser(t, mainDB, 9001, "root", "")
	setPassword(t, gdb, 7400, goodPassword)
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", 7400).
		Updates(map[string]any{"fail_count": 5,
			"locked_until": common.GetTimestamp() + 3600}).Error)
	r := newRouter(t, 9001)

	rec := do(r, http.MethodPost, "/api/qy/admin/pay-password/7400/unlock", `{"reason":"用户申诉"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	row := rowOf(t, gdb, 7400)
	assert.Zero(t, row.FailCount)
	assert.Zero(t, row.LockedUntil)
	// 解锁不动密码本身。
	assert.True(t, row.isSet())
	require.NoError(t, verify(context.Background(), 7400, goodPassword))

	assert.Equal(t, []string{"pay_password.unlock:ok"}, auditActions(t, gdb))
	var entry qymodel.AuditLog
	require.NoError(t, gdb.Order("id desc").First(&entry).Error)
	assert.Equal(t, qymodel.ActorAdmin, entry.ActorType)
	assert.Equal(t, 9001, entry.ActorUserId)
	assert.Equal(t, 7400, entry.TargetUserId)
	assert.Equal(t, "用户申诉", entry.Reason)
	assert.Contains(t, entry.BeforeSnap, `"fail_count":5`)
	assert.Contains(t, entry.AfterSnap, `"fail_count":0`)
	assert.NotContains(t, entry.BeforeSnap, row.Hash)
}

// 管理员重置支付密码:清空密码但不代设新密码,用户下次划转会被要求重新设置。
func TestAdminResetClearsPasswordAndWritesAudit(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	seedUser(t, mainDB, 7410, "victim", "")
	seedUser(t, mainDB, 9001, "root", "")
	setPassword(t, gdb, 7410, goodPassword)
	r := newRouter(t, 9001)

	rec := do(r, http.MethodPost, "/api/qy/admin/pay-password/7410/reset", `{"reason":"用户忘记且无邮箱"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.False(t, rowOf(t, gdb, 7410).isSet())
	// 重置之后的状态必须是"未设置",而不是"密码错误" —— 前端据此引导重新设置。
	assert.ErrorIs(t, verify(context.Background(), 7410, goodPassword), errPayPwdNotSet)
	assert.Equal(t, []string{"pay_password.reset:ok"}, auditActions(t, gdb))
}

// 破坏性动作必须填理由,且被拒时不留审计(没发生的事不该有记录)。
func TestAdminResetRequiresReason(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	seedUser(t, mainDB, 7420, "victim", "")
	seedUser(t, mainDB, 9001, "root", "")
	setPassword(t, gdb, 7420, goodPassword)
	r := newRouter(t, 9001)

	rec := do(r, http.MethodPost, "/api/qy/admin/pay-password/7420/reset", `{"reason":"   "}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), errReasonRequired.Code)
	assert.True(t, rowOf(t, gdb, 7420).isSet())
	assert.Empty(t, auditActions(t, gdb))
}

// 目标用户不存在时必须报 404,而不是静默"成功"。
//
// 静默成功的后果是:管理员输错一位数字,以为解锁了张三,实际什么都没发生,
// 而审计里还留着一条成功记录 —— 事后复盘会被这条假记录带偏。
func TestAdminRejectsUnknownUser(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	seedUser(t, mainDB, 9001, "root", "")
	r := newRouter(t, 9001)

	rec := do(r, http.MethodPost, "/api/qy/admin/pay-password/999999/unlock", `{}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), errUserNotFound.Code)
	assert.Empty(t, auditActions(t, gdb))
}

// 划转功能被临时关停时,用户端接口按约定 404,但管理端必须照常可用。
//
// 关停划转恰恰是出事时的第一个动作,那之后才轮到处理用户的锁定申诉 ——
// 管理端跟着一起 404 的话,运营在最需要它的时候没有工具。
func TestAdminStaysAvailableWhenTransferDisabled(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	seedUser(t, mainDB, 7430, "victim", "")
	seedUser(t, mainDB, 9001, "root", "")
	setPassword(t, gdb, 7430, goodPassword)
	prev := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: config.Transfer{Enabled: false},
	})
	t.Cleanup(func() { qyConfig.Store(prev) })
	r := newRouter(t, 9001)

	userSide := do(r, http.MethodGet, "/api/qy/pay-password", "")
	assert.Equal(t, http.StatusNotFound, userSide.Code)

	adminSide := do(r, http.MethodPost, "/api/qy/admin/pay-password/7430/unlock", `{}`)
	assert.Equal(t, http.StatusOK, adminSide.Code, adminSide.Body.String())
	assert.Equal(t, []string{"pay_password.unlock:ok"}, auditActions(t, gdb))
}
