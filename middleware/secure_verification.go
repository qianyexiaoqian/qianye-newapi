package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// SecurityProofScopeWithdrawPayeeRead 是提现收款信息明文的安全验证范围。
//
// 它定义在这里而不是 controller,是因为签发侧(controller 的 scope 白名单)与
// 消费侧(qianye/modules/withdraw 的管理端 handler)分属两个互不 import 的包,
// 而这两侧一旦拼错一个字母,表现是"验证做了、但那张 proof 永远对不上",
// 而不是编译错误。middleware 是两侧都已经依赖的最低层。
//
// 门槛为什么落在"第二因子"而不是 RootAuth:与渠道上游 API Key 不同,收款账号
// 是**打款操作本身必须看到的东西** —— 法币提现要人工去银行/钱包转账,把它收成
// root 专属等于全站只有 root 一个人能付款。所以这里要的是"确认坐在键盘前的
// 确实是这个管理员本人"(被盗的会话/PAT 单独拿不到明文),而不是抬高角色。
const SecurityProofScopeWithdrawPayeeRead = "withdraw.payee.read"

// SecureVerificationRequired protects channel key disclosure. Other sensitive
// operations validate their narrower proof scopes in their controller.
func SecureVerificationRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !RequireSecurityProof(c, "channel.key.read", []string{"2fa", "passkey"}) {
			return
		}
		c.Set("secure_verified", true)
		c.Next()
	}
}

// RequireSecurityProof validates a proof against the authenticated dashboard
// session and writes the shared proof error contract on failure.
func RequireSecurityProof(c *gin.Context, requiredScope string, allowedMethods []string) bool {
	identity, ok := GetSessionAuthIdentity(c)
	if !ok {
		securityProofError(c, "SECURITY_PROOF_INVALID", "安全验证状态无效")
		return false
	}
	raw := strings.TrimSpace(c.GetHeader("X-Security-Proof"))
	if raw == "" {
		securityProofError(c, "SECURITY_PROOF_REQUIRED", "需要安全验证")
		return false
	}
	if _, err := service.VerifySecurityProof(raw, identity, requiredScope, allowedMethods); err != nil {
		switch {
		case errors.Is(err, service.ErrAuthTokenExpired):
			securityProofError(c, "SECURITY_PROOF_EXPIRED", "安全验证已过期")
		case errors.Is(err, service.ErrProofScope):
			securityProofError(c, "SECURITY_PROOF_SCOPE_MISMATCH", "安全验证范围不匹配")
		case errors.Is(err, service.ErrProofMethod):
			securityProofError(c, "SECURITY_PROOF_METHOD_MISMATCH", "安全验证方式不匹配")
		default:
			securityProofError(c, "SECURITY_PROOF_INVALID", "安全验证状态无效")
		}
		return false
	}
	return true
}

func securityProofError(c *gin.Context, code, message string) {
	c.JSON(http.StatusForbidden, gin.H{
		"success": false,
		"message": message,
		"code":    code,
	})
	c.Abort()
}
