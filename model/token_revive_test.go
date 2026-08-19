package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 令牌余额被并发预扣**瞬间**打到 0 时,ValidateUserToken 会把 tokens.status
// 落库写成 4(已耗尽);随后结算退款把 remain_quota 加回来,状态却没有任何一条
// 路径会改回去。结果是一张还有钱的令牌永久返回 401 Invalid token:实测退款后
// 仍有 3570 额度的令牌,间隔 2 秒单发两次仍是 401,库里 status 恒为 4。
//
// 预扣是**估算额**(通常是真实花费的几十到上百倍),所以这不需要恶意构造 ——
// 令牌用到尾段时一次正常并发就能把余额恰好扣到 0。
func TestExhaustedTokenComesBackWhenTheRefundLands(t *testing.T) {
	truncateTables(t)

	cases := []struct {
		name        string
		remain      int
		unlimited   bool
		wantRevived bool
	}{
		{"退款已到账,状态必须自愈", 3570, false, true},
		{"无限额度的令牌同样自愈", 0, true, true},
		{"余额确实还是 0 时不许放行", 0, false, false},
		{"负余额同样不许放行", -12, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := Token{
				UserId:         1,
				Key:            "revive-" + common.GetRandomString(10),
				Name:           "revive-test",
				Status:         common.TokenStatusExhausted,
				ExpiredTime:    -1,
				RemainQuota:    tc.remain,
				UnlimitedQuota: tc.unlimited,
			}
			require.NoError(t, token.Insert())

			reviveExhaustedToken(&token)

			want := common.TokenStatusExhausted
			if tc.wantRevived {
				want = common.TokenStatusEnabled
			}
			assert.Equal(t, want, token.Status)
			assert.Equal(t, want, getTokenFromDB(t, token.Id).Status,
				"自愈必须落库,否则下一次鉴权又读回已耗尽")
		})
	}
}

// 已停用(status=2,管理员或用户主动关的)绝不能被这条自愈路径顺手打开。
func TestReviveNeverTouchesADisabledToken(t *testing.T) {
	truncateTables(t)
	token := Token{
		UserId:      1,
		Key:         "revive-disabled-" + common.GetRandomString(10),
		Name:        "revive-test",
		Status:      common.TokenStatusDisabled,
		ExpiredTime: -1,
		RemainQuota: 10000,
	}
	require.NoError(t, token.Insert())
	reviveExhaustedToken(&token)
	assert.Equal(t, common.TokenStatusDisabled, token.Status)
	assert.Equal(t, common.TokenStatusDisabled, getTokenFromDB(t, token.Id).Status)
}
