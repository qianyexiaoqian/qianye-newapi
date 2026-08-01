package paypass

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const goodPassword = "pay-pwd-8891"

// 未设置支付密码时必须报 qy_pay_pwd_not_set —— 这是"首次使用强制设置"的落地点。
// 前端按这个 code 把用户引到设置流程,回落成"密码错误"会让用户对着一个
// 根本不存在的密码反复试到锁定。
func TestVerifyRejectsWhenNeverSet(t *testing.T) {
	newTestDB(t)

	err := verify(context.Background(), 7001, goodPassword)
	assert.ErrorIs(t, err, errPayPwdNotSet)
}

// 三条失败路径都必须真的跑过一次慢哈希。
//
// 这条断言钉的是"响应时间不得泄露账号状态":没有它,把 `!row.isSet()` 的早返回
// 挪到 compareHash 之前,功能行为完全不变、其余测试全绿,而"未设置"会从
// 几十毫秒变成几百微秒 —— 一个不需要任何权限就能读的账号状态探针。
func TestVerifyAlwaysPaysSlowHashCost(t *testing.T) {
	gdb := newTestDB(t)
	setPassword(t, gdb, 7002, goodPassword)
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", 7002).
		Update("locked_until", common.GetTimestamp()+3600).Error)

	cases := []struct {
		name   string
		userId int
		want   *bizError
	}{
		{"未设置", 7003, errPayPwdNotSet},
		{"已锁定", 7002, errPayPwdLocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := slowCompares.Load()
			assert.ErrorIs(t, verify(context.Background(), tc.userId, goodPassword), tc.want)
			assert.Equal(t, before+1, slowCompares.Load(),
				"这条路径没有跑慢哈希,响应时间就成了账号状态探针")
		})
	}
}

// 占位摘要必须是一个成本与真实摘要完全相同的 bcrypt 串。
// 成本不同 = 时间不同 = 侧信道还在;而它又绝不能验证通过任何密码。
func TestDummyHashHasSameCostAndMatchesNothing(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyHash()))
	require.NoError(t, err)
	assert.Equal(t, hashCost, cost)
	assert.False(t, compareHash("", ""))
	assert.False(t, compareHash("", goodPassword))
}

// 输错一次记一次,输对一次清零。清零这一步漏掉的话,一个平时偶尔手滑的用户
// 会在若干天后突然被锁 —— 而且没有任何一次连续输错。
func TestVerifyCountsFailureAndClearsOnSuccess(t *testing.T) {
	gdb := newTestDB(t)
	setPassword(t, gdb, 7010, goodPassword)

	assert.ErrorIs(t, verify(context.Background(), 7010, "wrong-one"), errPayPwdWrong)
	assert.Equal(t, 1, rowOf(t, gdb, 7010).FailCount)

	assert.ErrorIs(t, verify(context.Background(), 7010, "wrong-two"), errPayPwdWrong)
	assert.Equal(t, 2, rowOf(t, gdb, 7010).FailCount)

	require.NoError(t, verify(context.Background(), 7010, goodPassword))
	after := rowOf(t, gdb, 7010)
	assert.Equal(t, 0, after.FailCount)
	assert.Zero(t, after.LockedUntil)
}

// 达到阈值即锁定,锁定期内**连正确密码也不放行**。
// 锁定只挡错误密码的话,拿到密码的攻击者完全不受影响,锁定就只惩罚了本人。
func TestVerifyLocksAtThresholdAndRejectsEvenCorrectPassword(t *testing.T) {
	gdb := newTestDB(t)
	putSetting(t, gdb, "pay_pwd_max_attempts", "2")
	putSetting(t, gdb, "pay_pwd_lock_minutes", "15")
	setPassword(t, gdb, 7020, goodPassword)

	assert.ErrorIs(t, verify(context.Background(), 7020, "nope-1"), errPayPwdWrong)
	// 第二次失败正好触发锁定,直接回"已锁定"而不是"密码错误"。
	assert.ErrorIs(t, verify(context.Background(), 7020, "nope-2"), errPayPwdLocked)

	row := rowOf(t, gdb, 7020)
	assert.Equal(t, 2, row.FailCount)
	assert.Greater(t, row.LockedUntil, common.GetTimestamp())
	assert.LessOrEqual(t, row.LockedUntil, common.GetTimestamp()+15*60)

	assert.ErrorIs(t, verify(context.Background(), 7020, goodPassword), errPayPwdLocked)
}

// 锁定到期后必须给一个完整的新窗口。
//
// 只清 locked_until 不清 fail_count 的话,解锁后第一次手滑就会立刻重新锁上 ——
// 锁定时长悄悄变成了无限,而配置页上写的还是 15 分钟。
func TestVerifyResetsCounterAfterLockExpires(t *testing.T) {
	gdb := newTestDB(t)
	putSetting(t, gdb, "pay_pwd_max_attempts", "2")
	setPassword(t, gdb, 7030, goodPassword)
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", 7030).
		Updates(map[string]any{
			"fail_count":   2,
			"locked_until": common.GetTimestamp() - 1,
		}).Error)

	// 到期后的第一次输错只应该记成"第 1 次",不该立刻重新锁定。
	assert.ErrorIs(t, verify(context.Background(), 7030, "nope"), errPayPwdWrong)
	row := rowOf(t, gdb, 7030)
	assert.Equal(t, 1, row.FailCount)
	assert.Zero(t, row.LockedUntil)
}

// 空密码不计入错误次数:它只可能来自没收集到输入的客户端,
// 计进去等于让一个前端 bug 把用户批量锁死。
func TestVerifyEmptyPasswordDoesNotCount(t *testing.T) {
	gdb := newTestDB(t)
	setPassword(t, gdb, 7040, goodPassword)

	assert.ErrorIs(t, verify(context.Background(), 7040, ""), errPayPwdRequired)
	assert.Equal(t, 0, rowOf(t, gdb, 7040).FailCount)
}

// 扩展库不可用时验密必须失败(fail-closed)。
// 回落成"放行"就是把整套支付密码变成了一个可以靠打挂扩展库来关掉的开关。
func TestVerifyFailsClosedWhenExtDBUnavailable(t *testing.T) {
	newTestDB(t)
	prev := qyDBHandle.Swap(nil)
	t.Cleanup(func() { qyDBHandle.Store(prev) })

	assert.Error(t, verify(context.Background(), 7050, goodPassword))
}

// 强度校验的边界。纯数字的弱模式(全同、连续)必须挡住 ——
// 6 位数字只有 100 万种,而这两类正是被猜中概率最高的那批。
func TestValidateStrength(t *testing.T) {
	cases := []struct {
		password string
		want     bool
	}{
		{"", false},
		{"12345", false},                  // 太短
		{"123456", false},                 // 连续递增
		{"654321", false},                 // 连续递减
		{"111111", false},                 // 全同
		{"192837", true},                  // 纯数字但无明显规律
		{"aaaaaa", false},                 // 全同(非数字也挡)
		{"abcdef", true},                  // 字母连续不挡:字母空间大得多
		{"pay-pwd-8891", true},            // 常规
		{"支付密码就是它", true},                 // 21 字节,落在 [6,64] 内
		{string(make([]byte, 65)), false}, // 超过 bcrypt 安全余量
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, validateStrength(tc.password), "password=%q", tc.password)
	}
}
