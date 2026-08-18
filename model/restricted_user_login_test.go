package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"golang.org/x/crypto/bcrypt"

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

// 登录接口的「账号是否存在」不能只在响应体上不可区分,耗时上也不能。
//
// 判据不用计时(机器负载一变就是假红/假绿,纪律明令禁止),改为数 bcrypt 的
// 调用次数:不存在的用户名、存在但没有口令摘要的半截行、存在且密码错,三条
// 失败路径都必须恰好跑一次口令比较。少跑一次,那条路径就快二十倍,用户名可以
// 被逐个枚举。
func TestValidateAndFillSpendsTheSameBcryptWorkOnEveryFailure(t *testing.T) {
	truncateTables(t)
	const password = "timing-oracle-pass-1"
	hashed, err := common.Password2Hash(password)
	require.NoError(t, err)

	present := &User{
		Username: "timing-present", Password: hashed, Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
		AffCode: "aff-timing-present",
	}
	require.NoError(t, DB.Create(present).Error)
	noHash := &User{
		Username: "timing-nohash", Password: hashed, Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
		AffCode: "aff-timing-nohash",
	}
	require.NoError(t, DB.Create(noHash).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", noHash.Id).
		Update("password", "").Error)

	original := validatePasswordAndHash
	t.Cleanup(func() { validatePasswordAndHash = original })

	for _, tc := range []struct {
		name      string
		username  string
		password  string
		wantErr   error
		wantCalls int
	}{
		{"账号不存在", "timing-nobody", password, ErrInvalidCredentials, 1},
		{"有行但没有口令摘要", "timing-nohash", password, ErrInvalidCredentials, 1},
		{"账号存在但密码错", "timing-present", "wrong-password", ErrInvalidCredentials, 1},
		{"账号存在且密码对", "timing-present", password, nil, 1},
		// 空口令在查库之前就被挡掉,这一条不构成预言机:任何用户名都走同一条路。
		{"用户名为空", "", password, ErrUserEmptyCredentials, 0},
		{"口令为空", "timing-present", "", ErrUserEmptyCredentials, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			validatePasswordAndHash = func(pwd, hash string) bool {
				calls++
				return original(pwd, hash)
			}
			user := &User{Username: tc.username, Password: tc.password}
			gotErr := user.ValidateAndFill()
			if tc.wantErr == nil {
				require.NoError(t, gotErr)
			} else {
				assert.ErrorIs(t, gotErr, tc.wantErr)
			}
			assert.Equal(t, tc.wantCalls, calls, "口令比较的次数决定了这条路径的耗时量级")
		})
	}
}

// 补齐工作量的那段摘要必须真的等价:cost 与真实口令一致,且永不匹配任何输入。
// cost 写错(比如手抄成 $2a$04$)时补的时间只有真实路径的 1/64,预言机照旧存在。
func TestMissingUserPasswordHashMatchesRealCost(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(missingUserPasswordHash))
	require.NoError(t, err, "哨兵摘要必须是一段合法的 bcrypt 摘要")
	assert.Equal(t, bcrypt.DefaultCost, cost,
		"哨兵摘要的 cost 必须与 common.Password2Hash 用的一致")

	realHash, err := common.Password2Hash("any-real-password")
	require.NoError(t, err)
	realCost, err := bcrypt.Cost([]byte(realHash))
	require.NoError(t, err)
	assert.Equal(t, realCost, cost)

	for _, pwd := range []string{"", "password", "admin", missingUserPasswordHash} {
		assert.False(t, common.ValidatePasswordAndHash(pwd, missingUserPasswordHash),
			"哨兵摘要绝不能与任何口令匹配")
	}
}
