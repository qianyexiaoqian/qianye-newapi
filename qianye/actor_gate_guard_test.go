package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actor_gate_guard_test.go —— 「谁能把钱/权限改到谁头上」的接线守卫。
//
// 上游对「管理员管理用户」有一条明确的自我保护:controller/user.go 的
// canManageTargetRole 要求 myRole == Root || myRole > targetRole。它挂在
// GetUser / UpdateUser / ManageUser / DeleteUser / AdminClearUserBinding /
// AdminResetPasskey / AdminDisable2FA / UnbindCustomOAuthByAdmin 上,
// 却漏掉了另外一批**同样能让钱或权限发生变化**的接口 —— 本轮梳理逐条查出的
// 就是这批漏网的:
//
//	POST /api/user/topup/complete              管理员补单,直接给订单归属人加额度
//	POST /api/subscription/admin/bind          免费发一份付费套餐
//	POST /api/subscription/admin/users/:id/subscriptions        同上,目标走路径参数
//	POST /api/subscription/admin/users/:id/subscriptions/reset  已用量清零 = 再送一轮
//	POST /api/qy/admin/commission/balances/withdrawn 已提现调低 → 可提现凭空回满
//	POST /api/qy/admin/commission/relations/bind    把自己设成某人的邀请人
//	POST /api/qy/admin/commission/relations/rebind  同上,且不留"本来没有邀请人"的痕迹
//	POST /api/qy/admin/commission/settle            绕过成熟期先解冻自己那一份
//	POST /api/qy/admin/withdraw/:id/*               同级互批(自审自批之外的另一半)
//	POST /api/qy/admin/pay-password/:user_id/*      拆掉划转的第二因子
//	POST /api/qy/admin/violation/records/:id/revoke 撤自己的违规记录并退款
//	POST /api/qy/admin/violation/bans/:userId/unban 给自己解封
//	POST /api/qy/admin/violation/counters/:userId/reset 把自己离封号的距离清零
//	POST /api/qy/admin/violation/appeals/:id/review 批准自己的申诉(顺带退款+解封)
//
// 判据装好之后,真正的失败模式不是"判据写错了",而是**下一个人加接口时忘了接**
// —— 本仓反复出现的形状。所以这里直接扫 AST:每一个已经接上的处理器都必须
// 在函数体里出现它那一档的闸门调用,少一个就红。
//
// 这份清单**刻意跨包、跨模块**放在一处:分散到各模块的测试里就没有任何一个
// 地方能回答"全站一共有几条动钱接口接了判据",而那正是这次梳理要交付的东西。

// actorGate 是一条接线断言:file 里的 fn 必须调用 gate。
type actorGate struct {
	// route 只用于失败信息,让人不必先读代码就知道被拒的是哪条 HTTP 路由。
	route string
	file  string
	fn    string
	// gate 是闸门调用的**函数名**(选择器只取最后一段,guard.ActorMayActOnCtx
	// 记 ActorMayActOnCtx)。各模块的响应信封不同,因此闸门是各自的薄包装,
	// 但它们最终都落到 guard.ActorMayActOn 一条判据上。
	gate string
}

// repoFile 把仓库相对路径解析成本测试可读的路径。本文件在 qianye/ 下,
// 所以上溯一级就是仓库根。
func repoFile(rel string) string { return filepath.Join("..", filepath.FromSlash(rel)) }

var actorGates = []actorGate{
	// ── 上游侧:该接 canManageTargetRole 却没接的 ──
	{"POST /api/user/topup/complete", "controller/topup.go", "AdminCompleteTopUp", "canManageTargetRole"},
	{"POST /api/subscription/admin/bind", "controller/subscription.go", "AdminBindSubscription", "requireManageableUser"},
	{"POST /api/subscription/admin/users/:id/subscriptions", "controller/subscription.go", "AdminCreateUserSubscription", "requireManageableUser"},
	{"POST /api/subscription/admin/users/:id/subscriptions/reset", "controller/subscription.go", "AdminResetUserSubscriptionsByPlan", "requireManageableUser"},
	// 整盘重置是按人重置的粗口径兄弟:同样是「把 amount_used 清回 0 = 再送一轮
	// 额度」，而它原先一道判据都没有 —— role=10 只要自己名下有那张套餐，一次
	// 调用就把**自己**的已用量清零(自益)，顺带动了全站该套餐持有者(含 root)。
	// 它的目标不是一个 user_id 而是一整批人，所以判据下沉到 model 层逐行套用，
	// 这里断言控制器把操作人身份传下去了。
	{"POST /api/subscription/admin/plans/:id/subscriptions/reset", "controller/subscription.go", "AdminResetPlanSubscriptions", "subscriptionActorOf"},
	// 作废与硬删除是**纯损害**方向:把一条已生效(可能是真金白银买的)订阅立刻
	// 取消并把对方的用户分组打回默认组。目标同样不在报文里，而在订阅行的归属人
	// 上 —— 与 relations/unbind、relations/block 完全同形，只是当初没进这张清单，
	// 于是 role=10 能作废并硬删 role=100 的有效订阅。
	{"POST /api/subscription/admin/user_subscriptions/:id/invalidate", "controller/subscription.go", "AdminInvalidateUserSubscription", "requireManageableUser"},
	{"DELETE /api/subscription/admin/user_subscriptions/:id", "controller/subscription.go", "AdminDeleteUserSubscription", "requireManageableUser"},

	// ── 扩展侧:佣金 ──
	{"POST /api/qy/admin/commission/balances/adjust", "qianye/modules/commission/api_admin_adjust.go", "requireAdjustableTarget", "ActorMayActOn"},
	{"POST /api/qy/admin/commission/balances/withdrawn", "qianye/modules/commission/api_admin_balance.go", "adminSetWithdrawn", "denyActorOverTarget"},
	{"POST /api/qy/admin/commission/relations/bind", "qianye/modules/commission/api_admin_relation.go", "adminBindRelation", "denyActorOverTarget"},
	{"POST /api/qy/admin/commission/relations/rebind", "qianye/modules/commission/api_admin_relation.go", "adminRebindRelation", "denyActorOverTarget"},
	// 解绑与停/恢复计佣是同一资源的**相反方向**,受益人是关系上的邀请人而不是
	// 报文里的某个 id —— 正因为报文里没有 inviter_id,这两条当初被整套闸门漏掉:
	// role=10 曾能单方面清掉 root 的 users.inviter_id(断掉对方全部未来进项、
	// 且自己无法复原,因为 bind/rebind 对 root 是 403),也曾能把上级基于风控
	// 停掉的、落在自己名下的计佣重新解封。
	{"POST /api/qy/admin/commission/relations/unbind", "qianye/modules/commission/api_admin_relation.go", "adminUnbindRelation", "denyActorOverTarget"},
	{"POST /api/qy/admin/commission/relations/block", "qianye/modules/commission/api_admin.go", "adminBlockRelation", "denyActorOverTarget"},
	{"POST /api/qy/admin/commission/settle", "qianye/modules/commission/api_admin.go", "adminSettle", "denyActorOverTarget"},
	// 冲正是损害方向:一个 role=10 曾能把同级/root 的佣金冲成 0，再冲成负的
	// unsettled 把对方挂上 debt_blocked，而受害者的恢复入口是接了判据的。
	{"POST /api/qy/admin/commission/clawback", "qianye/modules/commission/api_admin.go", "adminClawback", "denyActorOverTarget"},

	// ── 扩展侧:提现(六个人工决定的单一取单入口)──
	{"POST /api/qy/admin/withdraw/:id/*", "qianye/modules/withdraw/review.go", "loadDecidableWithdrawal", "ActorMayActOnCtx"},

	// ── 扩展侧:支付密码(两个写动作的公共骨架)──
	{"POST /api/qy/admin/pay-password/:user_id/{reset,unlock}", "qianye/modules/paypass/api_admin.go", "adminMutate", "adminTargetActable"},

	// ── 扩展侧:两阶段资金单的人工裁决 ──
	// 判成 success 会跑完整条收尾链路，抽奖 convergeExcluded 的 Success 分支会
	// 据此真的退一笔款；这是提现改人工发放后唯一能让 role=10 单方面推资金单
	// 终态的接口。
	{"POST /api/qy/admin/fund-orders/:order_no/resolve", "qianye/controller/admin.go", "AdminResolveFundOrder", "ActorMayActOnCtx"},

	// ── 扩展侧:抽奖出款的人工落账 ──
	// 上面那一条只收 Uncertain（系统自己说"我不知道"），而这一条推翻的是一个
	// 已经给过的 failed 结论：一支把钱在账上宣布为已付清，另一支让主库对
	// 同一个人再加一次钱。受益人在出款行上（payout.user_id），不在报文里。
	{"POST /api/qy/admin/lottery/activities/:act_no/payouts/:payout_no/adjudicate", "qianye/modules/lottery/payout_adjudicate.go", "handleAdjudicatePayout", "ActorMayActOnCtx"},

	// ── 扩展侧:违规处置 ──
	{"POST /api/qy/admin/violation/records/:id/revoke", "qianye/modules/violation/api_admin.go", "adminRevokeRecord", "denyActorOverTarget"},
	{"POST /api/qy/admin/violation/bans/:userId/unban", "qianye/modules/violation/api_admin.go", "adminUnban", "denyActorOverTarget"},
	{"POST /api/qy/admin/violation/counters/:userId/reset", "qianye/modules/violation/api_admin.go", "adminResetCounter", "denyActorOverTarget"},
	{"POST /api/qy/admin/violation/appeals/:id/review", "qianye/modules/violation/api_admin.go", "adminReviewAppeal", "denyActorOverTarget"},
}

// TestEveryFundOrPrivilegeAdminHandlerGatesItsTarget 逐条证明闸门接上了。
func TestEveryFundOrPrivilegeAdminHandlerGatesItsTarget(t *testing.T) {
	for _, g := range actorGates {
		t.Run(g.route, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, repoFile(g.file), nil, 0)
			require.NoError(t, err, g.file+" 解析失败")

			fn := findFuncDecl(file, g.fn)
			require.NotNil(t, fn, g.file+" 里找不到 "+g.fn+",这份清单已经过期")

			assert.True(t, callsFunc(fn, g.gate),
				g.fn+" 必须调用 "+g.gate+" 判断操作人能不能作用于目标账号;"+
					"少了它,"+g.route+" 对 role=10 就是一条自营通道")
		})
	}
}

// TestActorGateJudgementHasExactlyOneImplementation 守单一判据。
//
// 十五个调用点各自的错误信封不同,所以闸门是十五个薄包装;但**判据**只能有
// 一份。谁再在自己模块里写一遍 actorRole > targetRole,这里就红 ——
// 抄出来的第二份迟早会与上游 canManageTargetRole 漂移,而漂移的方向永远是
// "更宽松",因为收紧会立刻有人来报障。
func TestActorGateJudgementHasExactlyOneImplementation(t *testing.T) {
	// guard 包自己是判据的家,controller/user.go 是上游那一份(两者逐字同义,
	// guard.ManageableTarget 的注释里点名指向它)。除此之外任何地方出现
	// 角色大小比较都是抄了第三份。
	allowed := map[string]bool{
		filepath.FromSlash("qianye/guard/fund_actor.go"): true,
		filepath.FromSlash("controller/user.go"):         true,
	}
	for _, g := range actorGates {
		if allowed[filepath.FromSlash(g.file)] {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, repoFile(g.file), nil, parser.ParseComments)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			bin, ok := n.(*ast.BinaryExpr)
			if !ok {
				return true
			}
			if bin.Op != token.GTR && bin.Op != token.LSS {
				return true
			}
			// 只认"两侧都提到 role"的比较:角色大小判断的形状。
			left, right := exprText(bin.X), exprText(bin.Y)
			if strings.Contains(strings.ToLower(left), "role") &&
				strings.Contains(strings.ToLower(right), "role") {
				assert.Fail(t, "判据被抄了第二份",
					g.file+" 里出现了角色大小比较;判据只有 qianye/guard/fund_actor.go 一份")
			}
			return true
		})
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// callsFunc 回答 fn 的函数体里有没有调用名为 name 的函数。
// 选择器表达式只比最后一段:guard.ActorMayActOnCtx 与 ActorMayActOnCtx 是同一件事。
func callsFunc(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == name {
				found = true
			}
		case *ast.SelectorExpr:
			if f.Sel.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}
