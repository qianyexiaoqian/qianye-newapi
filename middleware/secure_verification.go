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
// 这道证明要的是"确认坐在键盘前的确实是这个管理员本人" —— 被盗的会话或 PAT
// 单独拿不到明文。它**不**回答"谁有资格看":那一问由 RootActionGate
// (middleware/root_action.go 的 RootActionWithdrawPayeeReveal)在同一条路由的
// 更前面回答,两道叠加。
//
// 这里曾经写着"门槛落在第二因子而不是 RootAuth,因为收款账号是打款本身必须
// 看到的东西,收成 root 专属等于全站只有 root 一个人能付款"。那个权衡已经被
// 项目方明确推翻:收款人明文与打款凭证提到超级管理员,代价(实际打款收口到
// 一个人)是有意付的,而四个人工决定仍留在 role>=10。两道并存不是冗余 ——
// 去掉档位,任意 role=10 做完一次 2FA 就能扒库;去掉证明,一张被偷走的 root
// 会话一条 curl 就能把全部收款账号读走。
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
