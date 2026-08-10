package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 受限账号(status = Disabled)必须能用密码登录 —— 否则「被禁用仍可提工单」
// 整个需求不成立。同时必须保住上游刻意设计的性质:
// **只有密码正确的人才能得知这个账号被限制了**。
//
// 账号不存在 / 密码错 / 状态未知,三者返回的错误必须逐字相同,攻击者拿不到
// 「这个用户名存在」或「这个号被封了」的信号。
func TestValidateAndFillAllowsRestrictedLoginWithoutLeakingAccountState(t *testing.T) {
	truncateTables(t)
	const password = "restricted-login-pass-1"
	hashed, err := common.Password2Hash(password)
	require.NoError(t, err)

	for _, seed := range []struct {
		username string
		status   int
	}{
		{"restricted-login-enabled", common.UserStatusEnabled},
		{"restricted-login-disabled", common.UserStatusDisabled},
		{"restricted-login-unknown", 0},
	} {
		user := &User{
			Username: seed.username, Password: hashed, Role: common.RoleCommonUser,
			Status: seed.status, Group: "default", AuthVersion: 1,
			AffCode: "aff-" + seed.username,
		}
		require.NoError(t, DB.Create(user).Error)
		// users.status 带 GORM 默认值,零值不会被 Create 写进去 —— 而「半截数据」
		// 这个用例要的正是一行 status=0,必须显式改回来。
		require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).
			Update("status", seed.status).Error)
	}

	for _, tc := range []struct {
		name     string
		username string
		password string
		wantErr  error
	}{
		{"enabled with right password", "restricted-login-enabled", password, nil},
		// 核心行为变更:受限账号密码对就能进来。
		{"restricted with right password", "restricted-login-disabled", password, nil},
		{"enabled with wrong password", "restricted-login-enabled", "wrong-password", ErrInvalidCredentials},
		{"restricted with wrong password", "restricted-login-disabled", "wrong-password", ErrInvalidCredentials},
		{"unknown status with right password", "restricted-login-unknown", password, ErrInvalidCredentials},
		{"missing account", "restricted-login-nobody", password, ErrInvalidCredentials},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := &User{Username: tc.username, Password: tc.password}
			err := user.ValidateAndFill()
			if tc.wantErr == nil {
				require.NoError(t, err)
				assert.Equal(t, tc.username, user.Username)
				return
			}
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}

	// 逐字相同:三种失败必须无法区分。
	wrongPasswordOnRestricted := (&User{Username: "restricted-login-disabled", Password: "wrong-password"}).ValidateAndFill()
	wrongPasswordOnEnabled := (&User{Username: "restricted-login-enabled", Password: "wrong-password"}).ValidateAndFill()
	missingAccount := (&User{Username: "restricted-login-nobody", Password: password}).ValidateAndFill()
	require.Error(t, wrongPasswordOnRestricted)
	assert.Equal(t, wrongPasswordOnEnabled.Error(), wrongPasswordOnRestricted.Error(),
		"密码错在受限账号与正常账号上必须给出同一个错误")
	assert.Equal(t, wrongPasswordOnEnabled.Error(), missingAccount.Error(),
		"账号不存在与密码错必须给出同一个错误")
}

func TestUserStatusAllowsSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   bool
	}{
		{"enabled", common.UserStatusEnabled, true},
		{"disabled is restricted, not banned", common.UserStatusDisabled, true},
		{"zero value is never a session state", 0, false},
		{"unknown future status must opt in explicitly", 99, false},
		{"negative", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, common.UserStatusAllowsSession(tc.status))
		})
	}
}
