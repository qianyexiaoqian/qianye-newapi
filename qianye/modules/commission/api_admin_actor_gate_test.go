package commission

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_actor_gate_test.go —— 四条动钱接口的操作人闸门。
//
// callAdminHandler 里的操作人固定是 id=7 / role=10(RoleAdminUser),
// 与生产上 middleware.AdminAuth() 写进上下文的三个键一致。
//
// 这里守的是本轮越权梳理查出的三条自营通道:
//
//  1. balances/withdrawn 把**已提现调低**时,差额会回到 available_quota
//     (newAvail = avail - delta,delta 为负)。也就是说这条"迁移登记"接口
//     反向用就是一台铸币机:把自己的 withdrawn 清零,可提现凭空回满,
//     再走一次提现即可落到主库额度。
//  2. relations/bind 与 rebind 把自己设成某个高消费账号的邀请人,
//     此后那个人每一笔消费的返佣都记到操作人头上 —— 而且不像手工调整那样
//     留下一条 manual 计佣行,事后只看流水会以为这是真实推广。
//  3. settle 手动结算把自己的冻结佣金提前解冻。它不造钱,但成熟期存在的理由
//     正是"下线退款/冲正还来得及追回",自解冻等于单方面取消这段窗口。
//
// 对照组同样必要:把闸门写成"一律拒绝"也能让拒绝那半全绿,而那是把整个
// 佣金管理台锁死。

const gateActorId = 7 // 与 callAdminHandler 里的操作人一致

// seedGateUser 往主库插一个指定角色的账号。
func seedGateUser(t *testing.T, mainDB *gorm.DB, id int, role int) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.User{
		Id: id, Username: "gate" + strconv.Itoa(id), Role: role,
		AffCode: "gateaff" + strconv.Itoa(id),
	}).Error)
}

func deniedAuditsOf(t *testing.T, gdb *gorm.DB, action string) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", action).Order("id asc").Find(&rows).Error)
	return rows
}

// TestAdminSetWithdrawn_RefusesSelfAndPeerTargets 钉住迁移编辑的操作人闸门。
//
// 变异验证:把 api_admin_balance.go 里那三行 denyActorOverTarget 删掉,
// 两个拒绝用例的状态码、余额断言与审计断言同时变红。
func TestAdminSetWithdrawn_RefusesSelfAndPeerTargets(t *testing.T) {
	cases := []struct {
		name       string
		targetId   int
		targetRole int
		wantCode   string
	}{
		{"受益人就是操作人自己", gateActorId, common.RoleAdminUser, "qy_self_dealing"},
		{"受益人是同级管理员", 8801, common.RoleAdminUser, "qy_target_not_manageable"},
		{"受益人是 root", 8802, common.RoleRootUser, "qy_target_not_manageable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			useConfig(t, commissionRateConfig("10", "5"))
			useAdminAPI(t)
			mainDB := useMainDB(t, &model.User{})
			seedGateUser(t, mainDB, tc.targetId, tc.targetRole)
			// 已提现 5000、可提现 0:调低已提现就是把 5000 变回可提现。
			seedLedgerBalance(t, gdb, tc.targetId, 0, 0, 5000, "0")

			rec := callAdminHandler(t, http.MethodPost,
				"/api/qy/admin/commission/balances/withdrawn",
				setWithdrawnBody(tc.targetId, 0, "把已提现清零,额度回到可提现"),
				adminSetWithdrawn)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)

			after := balanceOf(t, gdb, tc.targetId)
			require.NotNil(t, after)
			assert.EqualValues(t, 5000, after.WithdrawnQuota, "被拒的请求不许改到库")
			assert.EqualValues(t, 0, after.AvailableQuota,
				"可提现一分都不许回补 —— 回补就是凭空造出一笔可提现的钱")

			denied := deniedAuditsOf(t, gdb, "commission.balance.withdrawn.set.actor_denied")
			require.Len(t, denied, 1, "被拒的动钱尝试必须留痕")
			assert.Equal(t, qymodel.ResultFail, denied[0].Result)
			assert.Equal(t, gateActorId, denied[0].ActorUserId)
			assert.Equal(t, tc.targetId, denied[0].TargetUserId)
		})
	}
}

// TestAdminSetWithdrawn_StillEditsOrdinaryUsers 是对照组。
func TestAdminSetWithdrawn_StillEditsOrdinaryUsers(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedGateUser(t, mainDB, 8803, common.RoleCommonUser)
	seedLedgerBalance(t, gdb, 8803, 0, 0, 5000, "0")

	rec := callAdminHandler(t, http.MethodPost,
		"/api/qy/admin/commission/balances/withdrawn",
		setWithdrawnBody(8803, 0, "登记错了,回退这 5000"), adminSetWithdrawn)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	after := balanceOf(t, gdb, 8803)
	assert.EqualValues(t, 0, after.WithdrawnQuota)
	assert.EqualValues(t, 5000, after.AvailableQuota, "普通用户的迁移编辑必须照常生效")
	assert.Empty(t, deniedAuditsOf(t, gdb, "commission.balance.withdrawn.set.actor_denied"))
}

// TestAdminBindRelation_RefusesActorAsInviter 钉住"把自己设成上线"这条。
//
// 变异验证:把 api_admin_relation.go 里 adminBindRelation 那三行
// denyActorOverTarget 删掉,自营那一格从 403 变成 200,且 users.inviter_id
// 真的被改成了操作人 —— 断言全红。
func TestAdminBindRelation_RefusesActorAsInviter(t *testing.T) {
	cases := []struct {
		name        string
		inviterId   int
		inviterRole int
		wantCode    string
	}{
		{"把自己设成上线", gateActorId, common.RoleAdminUser, "qy_self_dealing"},
		{"把同级管理员设成上线", 8811, common.RoleAdminUser, "qy_target_not_manageable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			useConfig(t, commissionRateConfig("10", "5"))
			useAdminAPI(t)
			mainDB := useMainDB(t, &model.User{})
			seedGateUser(t, mainDB, tc.inviterId, tc.inviterRole)
			seedUser(t, mainDB, 8899, "whale", 0, 1000) // 高消费下线,自由人

			rec := callAdminHandler(t, http.MethodPost,
				"/api/qy/admin/commission/relations/bind",
				bindBody(8899, tc.inviterId, "把这个人挂到我名下"), adminBindRelation)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)
			assert.Zero(t, inviterIdOf(t, mainDB, 8899),
				"被拒的绑定不许动主库的 inviter_id —— 动了此后每一笔消费的返佣都改了收款人")
			require.Len(t, deniedAuditsOf(t, gdb, "commission.relation.bind.actor_denied"), 1)
		})
	}
}

// TestAdminBindRelation_StillBindsThirdParties 是对照组。
func TestAdminBindRelation_StillBindsThirdParties(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 8821, "promoter", 0, 1000)
	seedUser(t, mainDB, 8822, "invitee", 0, 2000)

	rec := callAdminHandler(t, http.MethodPost,
		"/api/qy/admin/commission/relations/bind",
		bindBody(8822, 8821, "客服核实后手工补绑"), adminBindRelation)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 8821, inviterIdOf(t, mainDB, 8822))
	assert.Empty(t, deniedAuditsOf(t, gdb, "commission.relation.bind.actor_denied"))
}

// TestAdminSettle_RefusesSelfTarget 钉住"自己给自己解冻"。
//
// 变异验证:把 api_admin.go 里 adminSettle 那三行 denyActorOverTarget 删掉,
// 403 变 200 且结算单真的落库,两条断言同时红。
func TestAdminSettle_RefusesSelfTarget(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useMoneyGlobals(t, 7.3, 500000)
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedGateUser(t, mainDB, gateActorId, common.RoleAdminUser)
	seedBalance(t, gdb, gateActorId, "4000")

	rec := callAdminHandler(t, http.MethodPost,
		"/admin/commission/settle?user_id="+strconv.Itoa(gateActorId), "", adminSettle)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_self_dealing")
	assert.Empty(t, settlementsOf(t, gdb, gateActorId),
		"被拒的手动结算不许落结算单 —— 落了就等于绕过成熟期把自己的佣金解冻了")
	require.Len(t, deniedAuditsOf(t, gdb, "commission.settle.manual.actor_denied"), 1)
}

// ── 邀请关系上「受益人不在报文里」的那三条 ────────────────────────────────
//
// block / unbind / rebind 的受益人是**关系上的邀请人**,报文里只有 invitee_id。
// 正因为闸门当初是按「报文里的 target 字段」逐个接的,这三条(以及 rebind 的
// 原邀请人那一侧)被整套漏掉:实测 role=10 能把上级基于风控停掉的、落在自己
// 名下的计佣解封回来,也能单方面清掉 root 名下下线的 users.inviter_id。
//
// 这里的判据一律是「受益人 = 关系上的邀请人」,由 currentInviterId 解析。

// seedBoundPair 造一条真实关系:主库 users.inviter_id + 扩展库快照行。
//
// 两张表都要写:blocked 读的是快照,unbind 写的是主库,而闸门在两者之前
// 就要能解析出受益人 —— 只写一张表会让某一条用例因为解析不到受益人而
// "碰巧"通过,那种绿是假的。
func seedBoundPair(t *testing.T, mainDB *gorm.DB, gdb *gorm.DB, inviterId, inviterRole, inviteeId int) {
	t.Helper()
	seedGateUser(t, mainDB, inviterId, inviterRole)
	seedUser(t, mainDB, inviteeId, "downline"+strconv.Itoa(inviteeId), inviterId, 1000)
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: inviteeId, InviterId: inviterId,
		MaskedName: "d***e", InviteeRef: "ref" + strconv.Itoa(inviteeId),
		BoundAt: 1000, Blocked: true, BlockReason: "上级基于风控停掉的",
		CreatedAt: 1000, UpdatedAt: 1000,
	}).Error)
}

// TestAdminBlockRelation_RefusesActorOwnOrPeerInviter 钉住"自己给自己解封"。
//
// 变异验证:把 api_admin.go 里 adminBlockRelation 那三行 denyActorOverTarget
// 删掉,两格都从 403 变 200 且 blocked 真的被改回 false。
func TestAdminBlockRelation_RefusesActorOwnOrPeerInviter(t *testing.T) {
	cases := []struct {
		name        string
		inviterId   int
		inviterRole int
		wantCode    string
	}{
		{"受益人就是操作人自己", gateActorId, common.RoleAdminUser, "qy_self_dealing"},
		{"受益人是 root", 8842, common.RoleRootUser, "qy_target_not_manageable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			useConfig(t, commissionRateConfig("10", "5"))
			useAdminAPI(t)
			mainDB := useMainDB(t, &model.User{})
			const invitee = 8841
			seedBoundPair(t, mainDB, gdb, tc.inviterId, tc.inviterRole, invitee)

			rec := callAdminHandler(t, http.MethodPost,
				"/api/qy/admin/commission/relations/block",
				blockBody(invitee, false, "复核后放行"), adminBlockRelation)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)
			assert.True(t, blockedRelationOf(t, gdb, invitee),
				"被拒的解封不许改到库 —— 改了就是把别人停掉的进项重新打开")
			require.Len(t, deniedAuditsOf(t, gdb, "commission.relation.block.actor_denied"), 1,
				"被拒的自营/越级尝试必须留痕")
		})
	}
}

// TestAdminBlockRelation_StillBlocksOrdinaryInviters 是对照组:把闸门写成
// "一律拒绝"同样能让上面两格全绿,而那是把风控停佣这件事整个锁死。
func TestAdminBlockRelation_StillBlocksOrdinaryInviters(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	const invitee = 8851
	seedBoundPair(t, mainDB, gdb, 8852, common.RoleCommonUser, invitee)

	rec := callAdminHandler(t, http.MethodPost,
		"/api/qy/admin/commission/relations/block",
		blockBody(invitee, false, "核实为同一人的两台设备"), adminBlockRelation)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, blockedRelationOf(t, gdb, invitee), "普通推广人的停/恢复必须照常生效")
	assert.Empty(t, deniedAuditsOf(t, gdb, "commission.relation.block.actor_denied"))
}

// TestAdminUnbindRelation_RefusesActorOwnOrPeerInviter 钉住"切断同级/更高账号
// 的推广关系"。
//
// 这条的危害方向与解封相反却更硬:主库 users.inviter_id 是计佣唯一的回源判据
// (inviter.go 的 resolveInviter),清零即断掉对方此后全部进项,而操作人自己
// 复原不回来 —— bind/rebind 对同级或更高的目标本来就是 403。
//
// 变异验证:把 api_admin_relation.go 里 adminUnbindRelation 那三行
// denyActorOverTarget 删掉,两格都从 403 变 200 且 inviter_id 真的被清零。
func TestAdminUnbindRelation_RefusesActorOwnOrPeerInviter(t *testing.T) {
	cases := []struct {
		name        string
		inviterId   int
		inviterRole int
		wantCode    string
	}{
		{"受益人就是操作人自己", gateActorId, common.RoleAdminUser, "qy_self_dealing"},
		{"受益人是同级管理员", 8862, common.RoleAdminUser, "qy_target_not_manageable"},
		{"受益人是 root", 8863, common.RoleRootUser, "qy_target_not_manageable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			useConfig(t, commissionRateConfig("10", "5"))
			useAdminAPI(t)
			mainDB := useMainDB(t, &model.User{})
			const invitee = 8861
			seedBoundPair(t, mainDB, gdb, tc.inviterId, tc.inviterRole, invitee)

			rec := callAdminHandler(t, http.MethodPost,
				"/api/qy/admin/commission/relations/unbind",
				unbindBody(invitee, "解除关系"), adminUnbindRelation)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)
			assert.Equal(t, tc.inviterId, inviterIdOf(t, mainDB, invitee),
				"被拒的解绑不许动主库的 inviter_id —— 那一列清零就是断掉对方全部未来佣金")
			require.Len(t, deniedAuditsOf(t, gdb, "commission.relation.unbind.actor_denied"), 1)
		})
	}
}

// TestAdminRebindRelation_RefusesTakingFromPeerInviter 守换绑的**原**邀请人那一侧。
//
// 只判新邀请人是不够的:把 root 名下的下线改挂到一个 role=1 傀儡名下时,新
// 邀请人那一格轻松过闸,而被拿走进项的是 root。它与解绑是同一个危害,只是多
// 绕了一个人。
//
// 变异验证:删掉 adminRebindRelation 里针对 currentInviterId 的那一次
// denyActorOverTarget(保留原有的新邀请人那一次),本用例从 403 变 200 且
// inviter_id 真的被改挂到傀儡账号上。
func TestAdminRebindRelation_RefusesTakingFromPeerInviter(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	const invitee, puppet = 8871, 8873
	seedBoundPair(t, mainDB, gdb, 8872, common.RoleRootUser, invitee)
	seedGateUser(t, mainDB, puppet, common.RoleCommonUser)

	rec := callAdminHandler(t, http.MethodPost,
		"/api/qy/admin/commission/relations/rebind",
		rebindBody(invitee, puppet, "原推广人离职,关系移交"), adminRebindRelation)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_target_not_manageable")
	assert.Equal(t, 8872, inviterIdOf(t, mainDB, invitee),
		"被拒的换绑不许动主库的 inviter_id —— 动了就等于绕开解绑把 root 的进项转走")
	require.Len(t, deniedAuditsOf(t, gdb, "commission.relation.rebind.actor_denied"), 1)
}
