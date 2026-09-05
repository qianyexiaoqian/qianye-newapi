package paypass

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zz_audit_paypass_lock_bypass_test.go —— 资金系统审计:锁定期间的绕过面。
//
// 锁定是支付密码暴破防护的落地形态。一旦账号被锁,除了"管理员解锁"与"邮箱找回"
// (都需要第二因子)之外,任何用户端接口都不得成为把锁绕开、或不受锁定约束地
// 继续试密的通道。本文件逐一堵这几条路。

// 锁定期间用正确旧密码走改密,也必须被锁拒 —— 改密内部走的是同一套 verify,
// 锁定分支排在密码比对之前,所以"我知道旧密码"不能成为提前解锁的手段。
//
// 若改密不走同一套 verify(比如它自己读一次哈希直接比),它就成了一条不受锁定
// 约束的验证口:攻击者绕过划转接口,直接在改密接口上无限试密。
func TestAuditLockedAccountCannotBeChangedToEscapeLock(t *testing.T) {
	gdb := newTestDB(t)
	const userId = 7600
	r := newRouter(t, userId)
	setPassword(t, gdb, userId, goodPassword)
	lockUntil := common.GetTimestamp() + 3600
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", userId).
		Updates(map[string]any{"fail_count": 9, "locked_until": lockUntil}).Error)

	// 用**正确**的旧密码尝试改密。
	rec := do(r, http.MethodPut, "/api/qy/pay-password",
		`{"old_password":"`+goodPassword+`","password":"brand-new-77"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), errPayPwdLocked.Code,
		"锁定期内凭正确旧密码改密竟未被锁拒 —— 改密成了提前解锁的后门")

	// 密码必须一个字节都没变、锁必须还在。不走 verify(锁定态会被锁拒,分不清
	// 是"密码没变"还是"被锁挡下"),直接拿原哈希比对。
	row := rowOf(t, gdb, userId)
	assert.True(t, row.isSet(), "改密流程把已锁账号的密码清空了")
	assert.True(t, compareHash(row.Hash, goodPassword), "锁定期内改密竟改动了哈希")
	assert.False(t, compareHash(row.Hash, "brand-new-77"), "新密码竟被写入了 —— 锁定期改密未被拦住")
	assert.Equal(t, lockUntil, row.LockedUntil, "锁被改密流程清掉了")
}

// 锁定(且已设置)的账号走首次设置接口,必须被"已设置"挡下,不能借此覆盖密码、
// 更不能顺带清掉锁 —— claimFirstPassword 的 CAS 只在 hash 为空时命中。
func TestAuditLockedAccountCannotBeReSetToEscapeLock(t *testing.T) {
	gdb := newTestDB(t)
	const userId = 7610
	r := newRouter(t, userId)
	setPassword(t, gdb, userId, goodPassword)
	lockUntil := common.GetTimestamp() + 3600
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", userId).
		Updates(map[string]any{"fail_count": 9, "locked_until": lockUntil}).Error)

	rec := do(r, http.MethodPost, "/api/qy/pay-password", `{"password":"brand-new-77"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), errPayPwdAlreadySet.Code)

	row := rowOf(t, gdb, userId)
	assert.True(t, compareHash(row.Hash, goodPassword), "首次设置接口改动了已设账号的哈希")
	assert.False(t, compareHash(row.Hash, "brand-new-77"), "首次设置接口覆盖了已设账号的密码")
	assert.Equal(t, lockUntil, row.LockedUntil, "首次设置接口把已锁账号的锁清掉了")
}

// 锁到期后用**正确**密码验密:必须成功放行,且把残留的错误计数与锁一起清零。
//
// 既有 TestVerifyResetsCounterAfterLockExpires 只覆盖"到期后再输错记成第 1 次",
// 没有覆盖"到期后输对"这条路 —— 而"锁到期了正确密码却还被当成锁着"正是一条
// 会把用户永久挡在门外的回归。这条把它钉住。
func TestAuditCorrectPasswordOnExpiredLockSucceedsAndClears(t *testing.T) {
	gdb := newTestDB(t)
	const userId = 7620
	setPassword(t, gdb, userId, goodPassword)
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", userId).
		Updates(map[string]any{
			"fail_count":   9,
			"locked_until": common.GetTimestamp() - 1, // 刚过期
		}).Error)

	require.NoError(t, verify(context.Background(), userId, goodPassword),
		"锁已到期,正确密码却仍被拒 —— 锁的效力泄漏到了它的有效期之外")
	row := rowOf(t, gdb, userId)
	assert.Zero(t, row.FailCount, "到期成功验密后残留计数没清零 —— 下一次手滑会立刻重锁")
	assert.Zero(t, row.LockedUntil)
}

// 一个账号被锁,绝不能牵连另一个账号。
//
// verify 的 userId 恒取自会话("id"),攻击者只能锁自己;但计数/锁定的 SQL 一旦
// 把 WHERE user_id 写漏或写成范围条件,就会出现"锁 A 顺带把 B 也锁了"的连坐
// (针对他人账号的锁定型 DoS)。这条用并发把 A 打到锁死,同时断言 B 毫发无伤、
// 正确密码照常放行。
func TestAuditLockoutIsPerAccountNoCollateralLock(t *testing.T) {
	gdb := newTestDB(t)
	const victimA, bystanderB = 7630, 7631
	setPassword(t, gdb, victimA, goodPassword)
	setPassword(t, gdb, bystanderB, goodPassword)
	putSetting(t, gdb, "pay_pwd_max_attempts", "5")
	putSetting(t, gdb, "pay_pwd_lock_minutes", "30")

	var wg sync.WaitGroup
	for i := 0; i < auditConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = verify(context.Background(), victimA, "definitely-wrong")
		}()
	}
	wg.Wait()

	// A 被锁死。
	assert.ErrorIs(t, verify(context.Background(), victimA, goodPassword), errPayPwdLocked)
	// B 完全没被牵连:计数为 0、未锁、正确密码照常放行。
	rowB := rowOf(t, gdb, bystanderB)
	assert.Zero(t, rowB.FailCount, "旁观账号 B 的错误计数被 A 的暴破连坐推高了")
	assert.Zero(t, rowB.LockedUntil, "旁观账号 B 被 A 的暴破连坐锁定了")
	assert.NoError(t, verify(context.Background(), bystanderB, goodPassword),
		"锁定发生了跨账号连坐 —— 这是一条针对他人账号的锁定型 DoS")
}
