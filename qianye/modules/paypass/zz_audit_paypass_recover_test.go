package paypass

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zz_audit_paypass_recover_test.go —— 资金系统审计:邮箱找回这条"绕过锁定"的合法
// 通道本身不能被拿来做别的坏事。
//
// 找回是唯一一条用户自助、不需要旧密码就能重置支付密码(并解锁)的路径,它的
// 安全性完全压在"验证码只发到已绑定邮箱 + 一次性消费 + 8 位十六进制 + 限流"上。
// 这里盯两件既有测试没盯的事:
//   1. 输错验证码不得消费掉那张有效验证码(否则一次误触就让用户手里的邮件作废);
//   2. 反复输错验证码不得反而把账号"喂"进支付密码的错误计数/锁定里 —— 找回与
//      验密是两套独立的失败计数,串台的话会出现"想自救反而把自己锁死"。

// 输错验证码任意多次,都不消费那张有效码,也不触碰支付密码的错误计数;
// 之后用正确码仍然能一次成功。
func TestAuditWrongRecoverCodeNeitherConsumesCodeNorCountsLockout(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	const userId = 7700
	const email = "owner@example.com"
	seedUser(t, mainDB, userId, "has-mail", email)
	setPassword(t, gdb, userId, goodPassword)
	r := newRouter(t, userId)

	// 注册一张真实有效的验证码(模拟用户已收到邮件)。
	common.RegisterVerificationCodeWithKey(recoverKey(userId, email), "the-real-code", recoverPurpose)

	// 连续输错 8 次验证码。
	for i := 0; i < 8; i++ {
		rec := do(r, http.MethodPost, "/api/qy/pay-password/recover/reset",
			`{"code":"wrong-guess","password":"brand-new-77"}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), errPayPwdCodeInvalid.Code)
	}

	// 找回失败绝不能计进支付密码的错误计数 —— 那是划转验密那一套的计数,
	// 串台会让"忘了支付密码想找回"的用户反被锁进划转的锁里。
	assert.Zero(t, rowOf(t, gdb, userId).FailCount,
		"输错找回验证码污染了支付密码的错误计数 —— 找回与验密的失败计数串台了")

	// 有效码没有被这些错误尝试消费掉:现在用它必须一次成功。
	rec := do(r, http.MethodPost, "/api/qy/pay-password/recover/reset",
		`{"code":"the-real-code","password":"brand-new-77"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, verify(context.Background(), userId, "brand-new-77"),
		"输错验证码把那张有效码提前作废了 —— 一次误触就让用户手里的邮件失效")
}

// 找回把被锁死的账号救出来:这条通道存在的全部意义就是"锁了也能自救"。
// 用完之后账号必须是未锁、可用新密码验密的状态。
//
// 既有 TestRecoverResetConsumesCodeOnce 验的是"码一次性",这条补上"找回必须能
// 解锁"这一半 —— 若 replacePassword 漏清 locked_until,用户改完密码仍然进不去。
func TestAuditRecoverRescuesLockedAccount(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	const userId = 7710
	const email = "locked@example.com"
	seedUser(t, mainDB, userId, "locked-user", email)
	setPassword(t, gdb, userId, goodPassword)
	// 把账号锁到很远的将来。
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", userId).
		Updates(map[string]any{
			"fail_count": 9, "locked_until": common.GetTimestamp() + 100000,
		}).Error)
	// 锁定态下正确密码也进不去(前置条件成立)。
	require.ErrorIs(t, verify(context.Background(), userId, goodPassword), errPayPwdLocked)

	common.RegisterVerificationCodeWithKey(recoverKey(userId, email), "rescue-code", recoverPurpose)
	r := newRouter(t, userId)
	rec := do(r, http.MethodPost, "/api/qy/pay-password/recover/reset",
		`{"code":"rescue-code","password":"rescued-pwd-9"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	row := rowOf(t, gdb, userId)
	assert.Zero(t, row.FailCount, "找回后错误计数没清")
	assert.Zero(t, row.LockedUntil, "找回后锁没解开 —— 用户改了密码仍然被挡在门外")
	require.NoError(t, verify(context.Background(), userId, "rescued-pwd-9"),
		"找回重置后新密码验不过 —— 这条自救通道只是看起来通了")
}
