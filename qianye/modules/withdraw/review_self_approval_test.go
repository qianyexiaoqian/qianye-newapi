package withdraw

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// review_self_approval_test.go —— 提现的自审自批闸门。
//
// 审计实测的那条链最后一步是:同一个 role=10 账号 POST
// /api/qy/admin/withdraw/<自己的单>/approve → 200,单据当场终结。管理端的人工
// 决定当时全部只挂 middleware.AdminAuth(),没有任何一处比过 w.UserId 与操作人。
//
// 提现改成人工发放之后决定从六个收敛到四个,闸门本身一分没变 ——
// 这里断言两件事:
//   - 四个决定**逐个**都拒绝自己的单,且单据状态与佣金余额纹丝不动;
//   - 别人的单照常处理(否则"修好了"其实是把管理端整个卡死)。

const selfReviewAdminId = 99

// seedForSelfReview 造一张属于 owner 的单,状态/方式按调用方给定。
//
// 佣金余额与冻结流水一并造出来:SettleFrozen / UnfreezeForWithdraw 都先核对
// "这笔钱当初真的被冻过",缺了它们几条路径会因为错误的原因失败,
// 而"因为错误的原因没动钱"与"闸门起作用了"在断言上长得一模一样。
func seedForSelfReview(t *testing.T, e *reviewEnv, no string, owner int,
	method, status string, quota int64) *Withdrawal {
	t.Helper()
	now := common.GetTimestamp()
	w := &Withdrawal{
		WithdrawNo: no,
		IdemScope:  idemScope,
		IdemKey:    idemKeyOf(owner, "seed-"+no),
		UserId:     owner,
		Method:     method,
		Status:     status,
		Quota:      quota,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	require.NoError(t, e.ext.Create(w).Error)

	var existing int64
	require.NoError(t, e.ext.Model(&commission.Balance{}).
		Where("user_id = ?", owner).Count(&existing).Error)
	if existing == 0 {
		require.NoError(t, e.ext.Create(&commission.Balance{
			UserId: owner, FrozenQuota: quota, AvailableQuota: 0,
			AvailableFiat: decimal.Zero, UnsettledAmount: decimal.Zero,
			CreatedAt: now, UpdatedAt: now,
		}).Error)
	}
	require.NoError(t, e.ext.Create(&commission.FreezeRecord{
		RefNo: no, Action: commission.FreezeActionFreeze,
		UserId: owner, Quota: quota, Fiat: decimal.Zero, CreatedAt: now,
	}).Error)
	require.NoError(t, e.main.Create(&model.User{
		Id: owner, Username: "u" + strconv.Itoa(owner),
		Quota: 0, Status: common.UserStatusEnabled,
	}).Error)
	return w
}

func callSelfReview(t *testing.T, h gin.HandlerFunc, id int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/qy/admin/withdraw/"+
		strconv.FormatInt(id, 10)+"/action", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	c.Set("id", selfReviewAdminId)
	c.Set("username", "admin")
	c.Set("role", common.RoleAdminUser)
	h(c)
	return res
}

// selfReviewCases 是四个人工决定各自的"参数完全合法"调用。
//
// 参数必须合法:每个 handler 在取单之前还有各自的参数校验(理由必填、
// 发放凭证必填、实发金额必须与单据相等),用一个非法参数去打会拿到那一档的
// 错误码,于是测试即使在闸门被删掉之后也照样"通过"。
var selfReviewCases = []struct {
	name    string
	handler gin.HandlerFunc
	method  string
	status  string
	body    string
}{
	{"通过", handleAdminApprove, config.WithdrawMethodQuota, StatusPending, `{}`},
	{"驳回", handleAdminReject, config.WithdrawMethodQuota, StatusPending, `{"reason":"资料不符"}`},
	{"标记已发放", handleAdminMarkPaid, config.WithdrawMethodQuota, StatusApproved,
		`{"payout_ref":"LOG-1","confirm_quota":50000}`},
	{"标记发放失败", handleAdminFail, config.WithdrawMethodFiat, StatusApproved,
		`{"reason":"对方账户已注销"}`},
}

// TestAdminDecisionsRefuseTheOperatorsOwnWithdrawal 逐个证明四条路都被挡住。
func TestAdminDecisionsRefuseTheOperatorsOwnWithdrawal(t *testing.T) {
	for _, tc := range selfReviewCases {
		t.Run(tc.name, func(t *testing.T) {
			e := newReviewEnv(t)
			// 单据的申请人就是操作人本人。
			w := seedForSelfReview(t, e, "WD-SELF-1", selfReviewAdminId,
				tc.method, tc.status, 50000)

			res := callSelfReview(t, tc.handler, w.Id, tc.body)

			assert.Equal(t, http.StatusForbidden, res.Code, res.Body.String())
			assert.Equal(t, "qy_wd_self_review", respCode(t, res))
			assert.Equal(t, tc.status, e.status(t, w.Id), "被拒的决定不许改状态")

			var bal commission.Balance
			require.NoError(t, e.ext.Where("user_id = ?", selfReviewAdminId).Take(&bal).Error)
			assert.EqualValues(t, 50000, bal.FrozenQuota, "佣金必须原样冻着")
			assert.EqualValues(t, 0, bal.WithdrawnQuota)
			assert.EqualValues(t, 0, bal.AvailableQuota)

			// 被拒的自审自批必须留痕:它是这条链上最容易被反复尝试的一步,
			// 而事后仲裁只认审计表。
			var denials []qymodel.AuditLog
			require.NoError(t, e.ext.Where("action = ?", "withdraw.self_review_denied").
				Find(&denials).Error)
			require.Len(t, denials, 1)
			assert.Equal(t, qymodel.ResultFail, denials[0].Result)
			assert.Equal(t, selfReviewAdminId, denials[0].ActorUserId)
			assert.Equal(t, selfReviewAdminId, denials[0].TargetUserId)
			assert.Equal(t, w.WithdrawNo, denials[0].TraceNo)
		})
	}
}

// TestAdminDecisionsStillProcessOtherPeoplesWithdrawals 是对照组。
//
// 少了它,把 loadDecidableWithdrawal 改成"一律拒绝"也能让上面那组全绿 ——
// 而那是把整个提现审核台锁死。
func TestAdminDecisionsStillProcessOtherPeoplesWithdrawals(t *testing.T) {
	for _, tc := range selfReviewCases {
		t.Run(tc.name, func(t *testing.T) {
			e := newReviewEnv(t)
			const applicant = 7
			w := seedForSelfReview(t, e, "WD-OTHER-1", applicant, tc.method, tc.status, 50000)

			res := callSelfReview(t, tc.handler, w.Id, tc.body)

			assert.Equal(t, http.StatusOK, res.Code, res.Body.String())
			assert.NotEqual(t, tc.status, e.status(t, w.Id),
				"别人的单必须真的被推进到下一个状态")
		})
	}
}

// TestEveryAdminDecisionGoesThroughTheSelfReviewGate 是接线守卫。
//
// 闸门装在 loadDecidableWithdrawal 里,而"装了没接上"是本仓反复出现的形状:
// 将来任何人再加一个人工决定,只要他照着邻居抄成 loadWithdrawal,
// 上面那些用例一条都不会红。这里直接扫 AST。
func TestEveryAdminDecisionGoesThroughTheSelfReviewGate(t *testing.T) {
	// 这四个函数各自代表一次不可逆的人工决定:佣金退不退、单据封不封。
	decisions := []string{"approve", "reject", "markPayout", "markFailed"}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "review.go", nil, 0)
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		want := false
		for _, name := range decisions {
			if fn.Name.Name == name {
				want = true
			}
		}
		if !want {
			continue
		}
		seen[fn.Name.Name] = true
		gated, bare := false, false
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			switch ident.Name {
			case "loadDecidableWithdrawal":
				gated = true
			case "loadWithdrawal":
				bare = true
			}
			return true
		})
		assert.True(t, gated, fn.Name.Name+" 必须经 loadDecidableWithdrawal 取单")
		assert.False(t, bare, fn.Name.Name+" 不许直接用 loadWithdrawal 绕过自审自批闸门")
	}
	for _, name := range decisions {
		assert.True(t, seen[name], "review.go 里找不到 "+name+",这份清单已经过期")
	}
}
