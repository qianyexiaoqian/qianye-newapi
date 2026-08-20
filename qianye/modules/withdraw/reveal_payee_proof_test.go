package withdraw

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reveal_payee_proof_test.go —— 收款信息明文的第二因子闸门。
//
// 审计实测:一个 role=10、非 root 的账号用 PAT 就能连续导出三个不同用户的
// 收款明文(银行卡号 + 开户行 + 户名、USDT-TRC20 地址、PayPal 邮箱),改 :id
// 遍历即可批量。同一份代码对同一量级的秘密(渠道上游 API Key)坚持
// RootAuth + 2FA/Passkey。
//
// 这里守的是"拿到会话/PAT ≠ 拿到明文":闸门必须在**碰任何单据数据之前**生效,
// 并且它不能是一道谁都过不去的墙(否则法币打款没人能做)。

// revealRequest 造一次 GET /admin/withdraw/:id/payee。
//
// identity 决定上下文里有没有一个可用的会话身份:middleware.GetSessionAuthIdentity
// 在 auth_identity 缺席时回落到 id / session_id / auth_version / session_version
// 四个上下文键,所以这里直接写这四个键,与真实的 UserAuth 等价。
func revealRequest(t *testing.T, withIdentity bool, proof string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/qy/admin/withdraw/1/payee?reason=核对银行卡号后打款", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Set("id", revealAdminId)
	c.Set("username", "admin")
	c.Set("role", common.RoleAdminUser)
	if withIdentity {
		c.Set("session_id", revealSessionId)
		c.Set("auth_version", int64(1))
		c.Set("session_version", int64(1))
	}
	if proof != "" {
		c.Request.Header.Set("X-Security-Proof", proof)
	}
	handleAdminRevealPayee(c)
	return res
}

const (
	revealAdminId   = 91
	revealSessionId = "sess-reveal-1"
)

func revealIdentity() service.AuthIdentity {
	return service.AuthIdentity{
		UserID: revealAdminId, SessionID: revealSessionId,
		UserAuthVersion: 1, SessionVersion: 1,
	}
}

func TestRevealPayeeRequiresASecurityProof(t *testing.T) {
	env := newReviewEnv(t)
	// 闸门放行之后 handler 会去查 qy_wd_payee —— 表不存在时拿到的是
	// qy_internal_error,那会让"放行了"与"查不到收款信息"混成一个结果。
	require.NoError(t, env.ext.AutoMigrate(&Payee{}))
	prevSecret := common.SessionSecret
	common.SessionSecret = "test-session-secret-with-sufficient-entropy"
	t.Cleanup(func() { common.SessionSecret = prevSecret })

	scoped, _, err := service.IssueSecurityProof(revealIdentity(), "2fa",
		[]string{middleware.SecurityProofScopeWithdrawPayeeRead})
	require.NoError(t, err)
	otherScope, _, err := service.IssueSecurityProof(revealIdentity(), "2fa",
		[]string{"channel.key.read"})
	require.NoError(t, err)
	otherSession, _, err := service.IssueSecurityProof(service.AuthIdentity{
		UserID: revealAdminId, SessionID: "sess-somebody-else",
		UserAuthVersion: 1, SessionVersion: 1,
	}, "2fa", []string{middleware.SecurityProofScopeWithdrawPayeeRead})
	require.NoError(t, err)

	for _, tc := range []struct {
		name         string
		withIdentity bool
		proof        string
		wantCode     string
	}{
		// PAT 认证的管理员根本没有会话身份 —— 审计正是用 PAT 打穿的。
		{"PAT(没有会话身份)", false, "", "SECURITY_PROOF_INVALID"},
		{"有会话但没带证明", true, "", "SECURITY_PROOF_REQUIRED"},
		{"别的 scope 的证明", true, otherScope, "SECURITY_PROOF_SCOPE_MISMATCH"},
		{"别人会话签发的证明", true, otherSession, "SECURITY_PROOF_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := revealRequest(t, tc.withIdentity, tc.proof)
			assert.Equal(t, http.StatusForbidden, res.Code, res.Body.String())
			assert.Contains(t, res.Body.String(), tc.wantCode)
			// 闸门必须排在事由校验与取单之前:如果它排在后面,这里会先看到
			// qy_wd_reason_required 或 qy_wd_payee_not_found。
			assert.NotContains(t, res.Body.String(), "qy_wd_payee_not_found")
		})
	}

	// 对照组:证明合法时闸门放行,继续往下走到"这张单没有收款信息"。
	// 少了它,把 RequireSecurityProof 换成"一律拒绝"也能让上面全绿 ——
	// 而那会让全站没有人能完成一次法币打款。
	t.Run("合法证明必须放行", func(t *testing.T) {
		res := revealRequest(t, true, scoped)
		assert.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
		assert.Contains(t, res.Body.String(), "qy_wd_payee_not_found")
	})
}
