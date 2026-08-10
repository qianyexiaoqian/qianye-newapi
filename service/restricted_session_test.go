package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 受限账号必须能建会话、能带着会话过校验、能 refresh。
//
// 这三条是「被禁用仍可提工单」的物理前提:
//   - createLoginSession 拒 → 登录成功也拿不到 access_token
//   - ValidateLoginSession 拒 → middleware 的白名单永远没有机会被评估
//   - RefreshLoginSession 拒 → access_token 只有 900 秒,15 分钟后被踢下线,
//     而且那条路径会**主动撤销会话**,用户连重新 refresh 的机会都没有
func TestRestrictedUserKeepsWorkingSession(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
		Update("status", common.UserStatusDisabled).Error)

	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "agent")
	require.NoError(t, err, "受限账号必须能建会话")

	identity := AuthIdentity{
		UserID:          user.Id,
		SessionID:       bundle.Session.SID,
		UserAuthVersion: user.AuthVersion,
		SessionVersion:  1,
	}
	_, validated, err := ValidateLoginSession(identity)
	require.NoError(t, err, "受限账号的会话必须仍然有效,否则白名单没有机会生效")
	assert.Equal(t, common.UserStatusDisabled, validated.Status,
		"会话校验必须把真实状态交给下游,middleware 靠它判白名单")

	refreshed, _, err := RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "127.0.0.1", "agent")
	require.NoError(t, err, "受限账号必须能 refresh,否则 900 秒后被强制登出")
	assert.NotEmpty(t, refreshed.AccessToken)

	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusActive, stored.Status,
		"refresh 不得因为受限而撤销会话")
}

// 未知状态(status=0 这类半截数据)一律不得持有会话:判据是显式白名单,
// 不是「!= Enabled 就当受限」。
func TestUnknownUserStatusCannotHoldSession(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "agent")
	require.NoError(t, err)
	identity := AuthIdentity{
		UserID:          user.Id,
		SessionID:       bundle.Session.SID,
		UserAuthVersion: user.AuthVersion,
		SessionVersion:  1,
	}

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
		Update("status", 0).Error)

	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)

	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "agent")
	assert.ErrorIs(t, err, ErrLoginSessionInvalid)
}
