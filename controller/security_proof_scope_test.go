package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// security_proof_scope_test.go —— 安全验证范围的签发白名单。
//
// 白名单与消费方分处两个包:isAllowedSecurityProofScope 决定
// UniversalVerify / PasskeyVerifyBegin **肯不肯签**,而消费方
// (middleware.RequireSecurityProof 的调用点)决定**认不认**。
// 两边对不上的表现不是编译错误,而是"验证做了、接口照样 403" ——
// 对渠道密钥来说,那等于没有人能看到密钥明文。
func TestSecurityProofScopeWhitelist(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope string
		want  bool
	}{
		{"渠道密钥", securityProofScopeChannelKeyRead, true},
		{"passkey 注册", securityProofScopePasskeyRegister, true},
		{"passkey 删除", securityProofScopePasskeyDelete, true},
		{"没登记过的范围", "channel.key.write", false},
		{"空范围", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAllowedSecurityProofScope(tc.scope))
		})
	}
}
