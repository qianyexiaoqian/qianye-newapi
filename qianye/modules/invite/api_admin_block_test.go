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

// api_admin_block_test.go —— 「停止返还」到底是不是一个可逆开关。
//
// 这条链路有三段各自能独立失效:
//
//	快照行的 blocked 列  →  blockedInvitees() 的进程内缓存  →  InviteeEligible 的判定
//
// 中间那段缓存 60 秒。少一次 invalidateBlocked(),"停止"要等一分钟才生效、
// "恢复"同样要等一分钟 —— 运营在那一分钟里看到的正是"点了没用"。库里那一列
// 却是对的,所以只断言快照行的测试对这类缺陷完全无感。因此这里一律**从 HTTP
// 处理器进**,再拿 InviteeEligible(星屑各条发放路径唯一的判定入口)真的回读。

// TestBlockStopsEligibilityAndUnblockRestoresIt 是这批用例的主干:停 → 不合格;
// 恢复 → 立刻合格。中间不等任何 TTL。
func TestBlockStopsEligibilityAndUnblockRestoresIt(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	now := common.GetTimestamp()
	seedUser(t, mainDB, 91, "qy-inviter-91", 0, now-90*86400)
	seedUser(t, mainDB, 92, "qy-downline-92", 91, now-90*86400)

	// 首次解析:合格,并且懒建了快照行。
	match, err := InviteeEligible(t.Context(), 92)
	require.NoError(t, err)
	require.True(t, match.Eligible())
	require.Equal(t, 91, match.InviterId)
	require.NotNil(t, relationRowOf(t, gdb, 92), "首次回源必须补建快照")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
		blockBody(92, true, "疑似自刷,先停"), adminBlockRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	match, err = InviteeEligible(t.Context(), 92)
	require.NoError(t, err)
	assert.False(t, match.Eligible(), "拉黑必须立刻生效,不等 60 秒 TTL")
	assert.True(t, match.Blocked)
	assert.Equal(t, 91, match.InviterId, "拉黑不改变关系本身,只停返还")

	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
		blockBody(92, false, "核实为误判,恢复"), adminBlockRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	match, err = InviteeEligible(t.Context(), 92)
	require.NoError(t, err)
	assert.True(t, match.Eligible(), "恢复同样必须立刻生效")
}

// TestAdminBlockRelation_BlocksRelationWithoutSnapshotRow 守懒建快照的缺行情形。
//
// 旧实现对缺行的关系 Updates 影响 0 行却照样回 200 —— 运营以为自刷被止住,
// 返还却一分不少地继续发。缺行时必须按主库的权威字段补一行。
func TestAdminBlockRelation_BlocksRelationWithoutSnapshotRow(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 93, "up", 0, 1000)
	seedUser(t, mainDB, 94, "down", 93, 2000)
	require.Nil(t, relationRowOf(t, gdb, 94), "前提:还没有快照行")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
		blockBody(94, true, "风控停返"), adminBlockRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rel := relationRowOf(t, gdb, 94)
	require.NotNil(t, rel, "缺行必须补建,否则 blockedInvitees 根本看不见这条拉黑")
	assert.True(t, rel.Blocked)
	assert.Equal(t, 93, rel.InviterId)
	assert.EqualValues(t, 2000, rel.BoundAt, "补建的行按注册时间推定绑定时刻")
	match, err := InviteeEligible(t.Context(), 94)
	require.NoError(t, err)
	assert.False(t, match.Eligible())

	// 没有关系的账号:400 且 code 与"报文错"分开。
	seedUser(t, mainDB, 95, "alone", 0, 3000)
	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
		blockBody(95, true, "没有上线"), adminBlockRelation)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_rel_not_bound")
	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
		blockBody(0, true, "没有 id"), adminBlockRelation)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_rel_no_relation")
}

// TestBlockAuditReasonMatchesDirection 钉住审计正文的方向。
//
// 审计正文是事后仲裁"这一刻到底发生了什么"的唯一凭据,两个方向共用同一句话
// 会写出自相矛盾的记录。顺带把"停止期间不补算"这条语义也钉进正文。
func TestBlockAuditReasonMatchesDirection(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	now := common.GetTimestamp()
	seedUser(t, mainDB, 101, "qy-inviter-101", 0, now-90*86400)
	seedUser(t, mainDB, 102, "qy-downline-102", 101, now-90*86400)

	for _, blocked := range []bool{true, false} {
		rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
			blockBody(102, blocked, "E2E 复核"), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	logs := relationAuditLogs(t, gdb, "invite.relation.block")
	require.Len(t, logs, 2)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Contains(t, logs[0].Reason, "只停止未来的邀请返", "停止那一次的正文必须说清它停的是什么")
	assert.NotContains(t, logs[1].Reason, "只停止未来的邀请返", "恢复那一次的正文不能还写着停止")
	assert.Contains(t, logs[1].Reason, "不补算", "恢复那一次必须写明停止期间不补算")
}

// TestBlockKeepsAutomaticRiskFlag 守两列的分工:risk_flags 归自动风控,block_reason 归人工事由。
func TestBlockKeepsAutomaticRiskFlag(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	now := common.GetTimestamp()
	seedUser(t, mainDB, 71, "ring-a", 72, now-86400)
	seedUser(t, mainDB, 72, "ring-b", 71, now-86400)
	// 自动风控已经把这条关系标成互邀环路并顺手停掉了。
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 72, InviterId: 71, InviteeRef: "ref72",
		RiskFlags: "reciprocal_invite", Blocked: true,
		BoundAt: now - 86400, CreatedAt: now, UpdatedAt: now,
	}).Error)

	t.Run("恢复不抹掉自动标记", func(t *testing.T) {
		rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
			blockBody(72, false, "核实为同一人的两台设备,先恢复"), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rel := relationRowOf(t, gdb, 72)
		require.NotNil(t, rel)
		assert.Equal(t, "reciprocal_invite", rel.RiskFlags,
			"自动风控标记必须原样留着 —— 人工事由不该顶掉「系统判定为互刷」这个事实")
		assert.Equal(t, "核实为同一人的两台设备,先恢复", rel.BlockReason)
		assert.False(t, rel.Blocked)
	})

	t.Run("再次停止只覆盖人工事由那一列", func(t *testing.T) {
		rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/block",
			blockBody(72, true, "复核后仍判定自刷"), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rel := relationRowOf(t, gdb, 72)
		require.NotNil(t, rel)
		assert.Equal(t, "reciprocal_invite", rel.RiskFlags)
		assert.Equal(t, "复核后仍判定自刷", rel.BlockReason)
		assert.True(t, rel.Blocked)
	})
}

// TestReciprocalInviteIsAutoBlocked 守互邀环路的自动拉黑:A 邀 B 且 B 邀 A 是最常见的
// 双账号自刷手法,首次解析就要停,两边都不合格。
func TestReciprocalInviteIsAutoBlocked(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	mainDB := useMainDB(t, &model.User{})
	now := common.GetTimestamp()
	seedUser(t, mainDB, 111, "ring-a", 112, now-86400)
	seedUser(t, mainDB, 112, "ring-b", 111, now-86400)

	for _, id := range []int{111, 112} {
		// 解析 111 时会顺带把 112 的上线缓存起来;清掉它,让 112 也从源头解析一次,
		// 这样两个方向的快照都是"首次见到"那一支建出来的。
		invalidateInviter(0)
		match, err := InviteeEligible(t.Context(), id)
		require.NoError(t, err)
		assert.False(t, match.Eligible(), "user %d", id)
		assert.True(t, match.Blocked, "user %d", id)
		rel := relationRowOf(t, gdb, id)
		require.NotNil(t, rel, "user %d", id)
		assert.Equal(t, "reciprocal_invite", rel.RiskFlags, "user %d", id)
	}
}

// TestInviteeEligibleHonoursTheModuleSwitch 钉住 invite.enabled 的语义:
// 关掉 = 不建关系、不给任何人合格。
func TestInviteeEligibleHonoursTheModuleSwitch(t *testing.T) {
	gdb := newTestDB(t)
	cfg := inviteConfig(0)
	cfg.Invite.Enabled = false
	useConfig(t, cfg)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 121, "up", 0, 1000)
	seedUser(t, mainDB, 122, "down", 121, 2000)

	match, err := InviteeEligible(t.Context(), 122)
	require.NoError(t, err)
	assert.False(t, match.Eligible())
	assert.Nil(t, relationRowOf(t, gdb, 122), "关掉之后连快照都不建")
}

// 互邀环闭合时**两条腿**都要停,不能只停后发现的那一条。
//
// blocked 只在 INSERT 那一刻算一次,而 OnConflict{DoNothing} 保证已存在的行
// 一个字节都不会变;两条腿又几乎不可能同时出现(先有 A→B,后来 A 的上线才被
// 绑成 B)。于是环闭合时只有 B→A 被拉黑,A→B 那条陈旧的行此后再也不会被
// 重新审视 —— "互邀自刷"这条判据的效力被砍掉一半。
func TestReciprocalInviteBlocksBothLegs(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	mainDB := useMainDB(t, &model.User{})

	now := common.GetTimestamp()
	// 第一步:B(102)是 A(101)的下线,此时 A 还没有上线,环没闭合。
	seedUser(t, mainDB, 101, "qy-recip-a", 0, now-90*86400)
	seedUser(t, mainDB, 102, "qy-recip-b", 101, now-90*86400)

	match, err := InviteeEligible(t.Context(), 102)
	require.NoError(t, err)
	require.True(t, match.Eligible(), "环还没闭合,B 这条腿应当正常")

	var legAB InviteRelation
	require.NoError(t, gdb.Where("invitee_id = ?", 102).First(&legAB).Error)
	require.False(t, legAB.Blocked, "此刻 A→B 还不该被拉黑")

	// 第二步:管理员把 A 的上线绑成 B,环闭合。A 再消费时落 B→A 这条腿。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 101).
		Update("inviter_id", 102).Error)
	invalidateInviter(101)

	match, err = InviteeEligible(t.Context(), 101)
	require.NoError(t, err)
	assert.False(t, match.Eligible(), "环闭合时新落的这条腿必须被拉黑")

	// 关键断言:**先前那条腿**也必须停。
	require.NoError(t, gdb.Where("invitee_id = ?", 102).First(&legAB).Error)
	assert.True(t, legAB.Blocked,
		"A→B 这条陈旧的腿也必须停 —— 只停一条等于让互邀环继续按一半的速度发星屑")
	assert.Equal(t, "reciprocal_invite", legAB.RiskFlags)

	// 缓存也要跟着失效,否则库里对了、判定还按旧快照走 60 秒。
	match, err = InviteeEligible(t.Context(), 102)
	require.NoError(t, err)
	assert.False(t, match.Eligible(), "拉黑之后的判定必须立刻生效,不等 TTL")
}
