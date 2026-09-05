package invite

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_admin_actor_gate_test.go —— 四条改关系接口的操作人闸门。
//
// callAdminHandler 里的操作人固定是 id=7 / role=10(RoleAdminUser),
// 与生产上 middleware.AdminAuth() 写进上下文的三个键一致。
//
// 这里守的是三条自营通道:
//
//  1. relations/bind 与 rebind 把自己设成某个高消费账号的邀请人,
//     此后那个人每一笔消费的星屑都记到操作人头上 —— 而且不像手调那样
//     留下一条 manual 流水,事后只看流水会以为这是真实推广。
//  2. relations/unbind 单方面切断同级 / 更高账号的推广关系,而自己复原不回来。
//  3. relations/block 把上级基于风控停掉的、落在自己名下的返还重新打开。
//
// 对照组同样必要:把闸门写成"一律拒绝"也能让拒绝那半全绿,而那是把整个
// 关系管理台锁死。

const gateActorId = 7 // 与 callAdminHandler 里的操作人一致

// TestAdminBindRelation_RefusesActorAsInviter 钉住"把自己设成上线"这条。
func TestAdminBindRelation_RefusesActorAsInviter(t *testing.T) {
	cases := []struct {
		name        string
		inviterId   int
		inviterRole int
		wantCode    string
	}{
		{"邀请人就是操作人自己", gateActorId, common.RoleAdminUser, "qy_self_dealing"},
		{"邀请人是同级管理员", 8811, common.RoleAdminUser, "qy_target_not_manageable"},
		{"邀请人是 root", 8812, common.RoleRootUser, "qy_target_not_manageable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			useConfig(t, inviteConfig(0))
			useAdminAPI(t)
			mainDB := useMainDB(t, &model.User{})
			seedGateUser(t, mainDB, tc.inviterId, tc.inviterRole)
			seedGateUser(t, mainDB, 8813, common.RoleCommonUser)

			rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/bind",
				bindBody(8813, tc.inviterId, "把高消费用户挂到自己名下"), adminBindRelation)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)
			assert.Equal(t, 0, inviterIdOf(t, mainDB, 8813), "被拒的绑定不许动主库")
			denied := deniedAuditsOf(t, gdb, "invite.relation.bind.actor_denied")
			require.Len(t, denied, 1, "被拒的自营尝试必须留痕")
			assert.Equal(t, qymodel.ResultFail, denied[0].Result)
			assert.Equal(t, gateActorId, denied[0].ActorUserId)
		})
	}
}

// TestAdminBindRelation_StillBindsThirdParties 是对照组。
func TestAdminBindRelation_StillBindsThirdParties(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedGateUser(t, mainDB, 8821, common.RoleCommonUser)
	seedGateUser(t, mainDB, 8822, common.RoleCommonUser)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/bind",
		bindBody(8822, 8821, "线下确认的推广关系"), adminBindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 8821, inviterIdOf(t, mainDB, 8822))
	assert.Empty(t, deniedAuditsOf(t, gdb, "invite.relation.bind.actor_denied"))
}

// TestAdminBlockRelation_RefusesActorOwnOrPeerInviter 钉住"自己给自己解封"。
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
			useConfig(t, inviteConfig(0))
			useAdminAPI(t)
			mainDB := useMainDB(t, &model.User{})
			const invitee = 8841
			seedBoundPair(t, mainDB, gdb, tc.inviterId, tc.inviterRole, invitee)

			rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
				blockBody(invitee, false, "复核后放行"), adminBlockRelation)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)
			assert.True(t, blockedRelationOf(t, gdb, invitee),
				"被拒的解封不许改到库 —— 改了就是把别人停掉的进项重新打开")
			require.Len(t, deniedAuditsOf(t, gdb, "invite.relation.block.actor_denied"), 1,
				"被拒的自营/越级尝试必须留痕")
		})
	}
}

// TestAdminBlockRelation_StillBlocksOrdinaryInviters 是对照组:把闸门写成
// "一律拒绝"同样能让上面两格全绿,而那是把风控停返这件事整个锁死。
func TestAdminBlockRelation_StillBlocksOrdinaryInviters(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	const invitee = 8851
	seedBoundPair(t, mainDB, gdb, 8852, common.RoleCommonUser, invitee)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
		blockBody(invitee, false, "核实为同一人的两台设备"), adminBlockRelation)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, blockedRelationOf(t, gdb, invitee), "普通推广人的停/恢复必须照常生效")
	assert.Empty(t, deniedAuditsOf(t, gdb, "invite.relation.block.actor_denied"))
}

// TestAdminUnbindRelation_RefusesActorOwnOrPeerInviter 钉住"切断同级/更高账号的推广关系"。
//
// 主库 users.inviter_id 是发放唯一的回源判据(inviter.go 的 resolveInviter),
// 清零即断掉对方此后全部进项,而操作人自己复原不回来 —— bind/rebind 对同级
// 或更高的目标本来就是 403。
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
			useConfig(t, inviteConfig(0))
			useAdminAPI(t)
			mainDB := useMainDB(t, &model.User{})
			const invitee = 8861
			seedBoundPair(t, mainDB, gdb, tc.inviterId, tc.inviterRole, invitee)

			rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/unbind",
				unbindBody(invitee, "解除关系"), adminUnbindRelation)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)
			assert.Equal(t, tc.inviterId, inviterIdOf(t, mainDB, invitee),
				"被拒的解绑不许动主库的 inviter_id —— 那一列清零就是断掉对方全部未来收益")
			require.Len(t, deniedAuditsOf(t, gdb, "invite.relation.unbind.actor_denied"), 1)
		})
	}
}

// TestAdminRebindRelation_RefusesTakingFromPeerInviter 守换绑的**原**邀请人那一侧。
//
// 只判新邀请人是不够的:把 root 名下的下线改挂到一个 role=1 傀儡名下时,新
// 邀请人那一格轻松过闸,而被拿走进项的是 root。
func TestAdminRebindRelation_RefusesTakingFromPeerInviter(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	const invitee, puppet = 8871, 8873
	seedBoundPair(t, mainDB, gdb, 8872, common.RoleRootUser, invitee)
	seedGateUser(t, mainDB, puppet, common.RoleCommonUser)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/rebind",
		rebindBody(invitee, puppet, "原推广人离职,关系移交"), adminRebindRelation)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_target_not_manageable")
	assert.Equal(t, 8872, inviterIdOf(t, mainDB, invitee),
		"被拒的换绑不许动主库的 inviter_id —— 动了就等于绕开解绑把 root 的进项转走")
	require.Len(t, deniedAuditsOf(t, gdb, "invite.relation.rebind.actor_denied"), 1)
}
