package withdraw

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// review_peer_approval_test.go —— 越级互批闸门的"申请人已经不在了"这一格。
//
// 同级/更高级申请人那两格由 review_payout_gate_test.go 的
// TestAdminDecisionsRefusePeerAndHigherRoleWithdrawals 覆盖,这里不重复。
//
// 这一格单独存在的理由:闸门要回查申请人的角色(guard.ActorMayActOn),而
// "查不到"与"角色更高"在实现上是两条分支。把查不到那条写成放行,一张属于
// 已删账号的单就可以被任何管理员推到终态,而这张单的终点是主库额度 ——
// 也就是把钱打给一个连角色都读不出来的 id。软删不算:判据走 Unscoped,
// 软删的账号仍然按它的角色判,所以这里必须硬删才碰得到这条分支。
func TestAdminDecisionsRefuseWithdrawalsOfVanishedApplicants(t *testing.T) {
	tc := selfReviewCases[0]
	e := newReviewEnv(t)
	const applicant = 7
	w := seedForSelfReview(t, e, "WD-GONE-1", applicant, tc.method, tc.status, 50000)
	require.NoError(t, e.main.Unscoped().Delete(&model.User{}, applicant).Error)

	res := callSelfReview(t, tc.handler, w.Id, tc.body)

	assert.Equal(t, http.StatusForbidden, res.Code, res.Body.String())
	assert.Equal(t, errPeerReview.Code, respCode(t, res))
	assert.Equal(t, tc.status, e.status(t, w.Id), "被拒的决定不许改状态")
}
