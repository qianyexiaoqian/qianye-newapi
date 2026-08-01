package paypass

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

// 运营配置从 qy_settings(scope=transfer)读,键名与 settings 侧定义的一致。
//
// 这条测试守的是"断链":本仓已经四次出现"配置项定义齐全、没有任何代码读它"。
// 键名写错、scope 写错、或者有人给本模块另起一套配置,这里立刻变红。
// 端到端的那一半由 TestVerifyLocksAtThresholdAndRejectsEvenCorrectPassword 覆盖 ——
// 它把 pay_pwd_max_attempts 设成 2,然后真的在第 2 次失败时被锁。
func TestLoadPolicyReadsTransferScopedSettings(t *testing.T) {
	gdb := newTestDB(t)

	assert.Equal(t, policy{MaxAttempts: defaultMaxAttempts, LockMinutes: defaultLockMinutes},
		loadPolicy(context.Background()), "没有运营覆盖时必须落在默认值上")

	putSetting(t, gdb, "pay_pwd_max_attempts", "3")
	putSetting(t, gdb, "pay_pwd_lock_minutes", "45")
	assert.Equal(t, policy{MaxAttempts: 3, LockMinutes: 45}, loadPolicy(context.Background()))
}

// 越界与写坏的值一律**丢弃回落默认**,不做钳取。
//
// 钳取的问题:一个被手工写坏的 max_attempts=999999 钳到 100 之后,阈值依然高得
// 等于没有锁定;而回落到默认的 5 只是"运营的微调没生效",损失有界且可解释。
// 这与 commission 的 rateOverride 是同一条口径。
func TestLoadPolicyDiscardsInvalidOverrides(t *testing.T) {
	gdb := newTestDB(t)

	cases := []struct {
		name        string
		maxAttempts string
		lockMinutes string
	}{
		{"非数字", "abc", "1e3"},
		{"负数", "-1", "-5"},
		{"零", "0", "0"},
		{"超上界", strconv.Itoa(maxMaxAttempts + 1), strconv.Itoa(maxLockMinutes + 1)},
		{"空串", "", ""},
		{"带空白", "  4  ", "  9  "}, // TrimSpace 之后是合法值,这一行必须**生效**
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			putSetting(t, gdb, "pay_pwd_max_attempts", tc.maxAttempts)
			putSetting(t, gdb, "pay_pwd_lock_minutes", tc.lockMinutes)
			got := loadPolicy(context.Background())
			if tc.name == "带空白" {
				assert.Equal(t, policy{MaxAttempts: 4, LockMinutes: 9}, got)
				return
			}
			assert.Equal(t, policy{MaxAttempts: defaultMaxAttempts, LockMinutes: defaultLockMinutes}, got)
		})
	}
}

// 扩展库读不到时回落默认值而不是"不限次数"。
// 回落成不限次数的话,打挂扩展库就能把锁定策略整个关掉。
func TestLoadPolicyFallsBackWhenDBUnavailable(t *testing.T) {
	newTestDB(t)
	prev := qyDBHandle.Swap(nil)
	t.Cleanup(func() { qyDBHandle.Store(prev) })

	assert.Equal(t, policy{MaxAttempts: defaultMaxAttempts, LockMinutes: defaultLockMinutes},
		loadPolicy(context.Background()))
}
