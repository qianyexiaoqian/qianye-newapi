package invite

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_admin_relation_test.go —— 邀请关系列表与手工绑定/换绑/解绑的回归网。
//
// 这一批用例守的是四件只有让**两个数据库**都真跑一遍才看得见的事:
//
//  1. 列表的数据源是主库 users,不是扩展库的懒建快照 —— 拿快照当数据源
//     会让管理端看到一张少了绝大多数关系的表,而且没有任何报错;
//  2. 绑定写的是权威字段 users.inviter_id,并且防住自邀请与任意长度的环;
//  3. 解绑**一个字节都不动账本**:星屑那一侧汇总出来的历史收益原样回显;
//  4. 每一条写路径的成功与失败都留痕。

// TestAdminListRelations_ReadsMainDbNotLazySnapshot 是本页最重要的一条。
//
// qy_invite_relation 是**懒建**的:ensureRelation 只在某个下线第一次被解析到上线时
// 才写那一行。拿快照当数据源的实现同样能跑通所有"绑定/解绑"用例,却会让管理端
// 看到一张少了绝大多数关系的表。
//
// 回滚验证:把 listBoundRelations 的数据源从 model.User 换成 InviteRelation,
// 本用例立刻变红(拿到 1 条而不是 3 条)。
func TestAdminListRelations_ReadsMainDbNotLazySnapshot(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	useRewardTotals(t, map[[2]int]int64{{1, 2}: 1500})

	seedUser(t, mainDB, 1, "boss", 0, 1000)
	seedUser(t, mainDB, 2, "alice", 1, 2000)
	seedUser(t, mainDB, 3, "bob", 1, 3000)
	seedUser(t, mainDB, 4, "carol", 1, 4000)
	// 只有 alice 被解析过,所以扩展库里只有她那一行快照。
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 2, InviterId: 1, MaskedName: "a**e", InviteeRef: "ref2",
		BoundAt: 2000, CreatedAt: now, UpdatedAt: now,
	}).Error)

	items, total := listRelations(t, "sort=invitee")
	require.EqualValues(t, 3, total, "三条绑定全都要出现,快照缺行不是漏行的理由")
	require.Len(t, items, 3)

	assert.Equal(t, 2, items[0].InviteeId)
	assert.Equal(t, "alice", items[0].InviteeUsername)
	assert.Equal(t, 1, items[0].InviterId)
	assert.Equal(t, "boss", items[0].InviterUsername, "邀请人用户名必须回主库补上")
	assert.True(t, items[0].SnapshotPresent)
	assert.EqualValues(t, 2000, items[0].BoundAt)
	assert.EqualValues(t, 1500, items[0].TotalStardust, "累计星屑按这一对由星屑侧汇总")

	assert.Equal(t, 3, items[1].InviteeId)
	assert.False(t, items[1].SnapshotPresent, "没有快照要如实说,而不是谎称绑定时间是 0")
	assert.EqualValues(t, 3000, items[1].InviteeCreatedAt)
	assert.Zero(t, items[1].TotalStardust)
}

// TestAdminListRelations_FindsFromEitherSide 守"从任一侧反查"。
//
// 回滚验证:把 EitherId 那一支的 OR 改成只匹配 invitee,
// "按邀请人名字查"会退化成空结果。
func TestAdminListRelations_FindsFromEitherSide(t *testing.T) {
	newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 21, "root", 0, 1000)
	seedUser(t, mainDB, 22, "mid", 21, 2000)
	seedUser(t, mainDB, 23, "leaf", 22, 3000)
	seedUser(t, mainDB, 24, "other", 21, 4000)

	// 作为邀请人查:root 名下两条。
	_, total := listRelations(t, "username=root")
	assert.EqualValues(t, 2, total)

	// 作为被邀请人 + 邀请人查:mid 既是 root 的下线,也是 leaf 的上线。
	items, total := listRelations(t, "username=mid&sort=invitee")
	require.EqualValues(t, 2, total)
	require.Len(t, items, 2)
	assert.Equal(t, 22, items[0].InviteeId)
	assert.Equal(t, 23, items[1].InviteeId)

	// 查无此人必须回空页。忽略掉筛选返回的是**全表**,而它看起来与
	// "这个人排在第一页"一模一样 —— 而这一页上有解绑按钮。
	items, total = listRelations(t, "username=nobody")
	assert.EqualValues(t, 0, total)
	assert.Empty(t, items)
}

// TestAdminBindRelation_WritesAuthoritativeFieldAndSnapshot 是绑定的本体。
//
// 回滚验证:把主库那条 Update("inviter_id", inviterId) 删掉(只写扩展库快照),
// inviter_id 断言立刻变红 —— 而列表页看起来一切正常。
func TestAdminBindRelation_WritesAuthoritativeFieldAndSnapshot(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 31, "inviter", 0, 1000)
	seedUser(t, mainDB, 32, "orphan", 0, 2000)
	// 一条带着上一次拉黑标记的旧快照:手工绑定必须把它清掉,否则"绑好了但永远不返"。
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 32, InviterId: 99, InviteeRef: "old32", Blocked: true, RiskFlags: "reciprocal_invite",
		BoundAt: 1, CreatedAt: now, UpdatedAt: now,
	}).Error)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/bind",
		bindBody(32, 31, "线下确认的推广关系"), adminBindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, 31, inviterIdOf(t, mainDB, 32), "权威字段必须被写上")
	rel := relationRowOf(t, gdb, 32)
	require.NotNil(t, rel)
	assert.Equal(t, 31, rel.InviterId)
	assert.False(t, rel.Blocked, "手工绑定必须清掉旧快照的拉黑标记")
	assert.Empty(t, rel.RiskFlags)
	assert.NotEmpty(t, rel.InviteeRef)
	assert.NotEmpty(t, rel.MaskedName)
	assert.GreaterOrEqual(t, rel.BoundAt, now, "手工绑定的 bound_at 是此刻,不是注册时间")

	// 缓存也必须立刻反映:下一次判定要认新上线。
	match, err := InviteeEligible(t.Context(), 32)
	require.NoError(t, err)
	assert.Equal(t, 31, match.InviterId)
	assert.True(t, match.Eligible())

	logs := relationAuditLogs(t, gdb, "invite.relation.bind")
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, 32, logs[0].TargetUserId)
	assert.Contains(t, logs[0].BeforeSnap, `"inviter_id":99`, "before 快照要记下被覆盖的旧关系")
	assert.Contains(t, logs[0].AfterSnap, `"inviter_id":31`)
}

// TestAdminBindRelation_BindsAccountWithNullInviterColumn 守 CAS 的 IS NULL 那一支。
//
// users.inviter_id 在三种数据库上都是可空、无默认;导入 / 迁移出来的账号那一列是
// NULL,而 `NULL = 0` 恒为 UNKNOWN。少了 IS NULL,这批账号永远 409 —— 一个重试
// 不好的确定性失败。
func TestAdminBindRelation_BindsAccountWithNullInviterColumn(t *testing.T) {
	newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 41, "inviter", 0, 1000)
	seedUser(t, mainDB, 42, "imported", 0, 2000)
	require.NoError(t, mainDB.Exec("UPDATE users SET inviter_id = NULL WHERE id = 42").Error)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/bind",
		bindBody(42, 41, "导入账号补绑"), adminBindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 41, inviterIdOf(t, mainDB, 42))
}

// TestAdminBindRelation_RejectsSelfInviteAndCycles 守三道闸门:自邀请、已绑定、任意长度的环。
func TestAdminBindRelation_RejectsSelfInviteAndCycles(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	// 51 → 52 → 53 已经是一条链;把 51 绑到 53 名下会成三人环。
	seedUser(t, mainDB, 51, "a", 0, 1000)
	seedUser(t, mainDB, 52, "b", 51, 2000)
	seedUser(t, mainDB, 53, "c", 52, 3000)
	seedUser(t, mainDB, 54, "d", 0, 4000)

	for _, tc := range []struct {
		name     string
		body     string
		wantCode string
	}{
		{"自邀请", bindBody(54, 54, "自己邀请自己"), "qy_rel_self_invite"},
		{"已绑定", bindBody(52, 54, "改成另一个指向"), "qy_rel_already_bound"},
		{"三人环", bindBody(51, 53, "闭环测试用例"), "qy_rel_cycle"},
		{"查无此人", bindBody(54, 999, "不存在的邀请人"), "qy_rel_user_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/bind",
				tc.body, adminBindRelation)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)
		})
	}
	assert.Equal(t, 0, inviterIdOf(t, mainDB, 51), "被拒的绑定不许动主库")
	assert.Equal(t, 51, inviterIdOf(t, mainDB, 52))
	logs := relationAuditLogs(t, gdb, "invite.relation.bind")
	require.Len(t, logs, 4, "每一次被拒都要留痕")
	for _, l := range logs {
		assert.Equal(t, qymodel.ResultFail, l.Result)
	}
}

// TestAdminBindRelation_RequiresReason 守事由:改动收益归属的动作没有事由,事后无法与误操作区分。
func TestAdminBindRelation_RequiresReason(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 55, "inviter", 0, 1000)
	seedUser(t, mainDB, 56, "invitee", 0, 2000)

	for _, reason := range []string{"", "   ", "ok"} {
		rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/bind",
			bindBody(56, 55, reason), adminBindRelation)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "qy_reason_required")
	}
	assert.Empty(t, relationAuditLogs(t, gdb, "invite.relation.bind"),
		"连事由都没填的请求还没走到业务判定,不算一次尝试")
	assert.Equal(t, 0, inviterIdOf(t, mainDB, 56))
}

// TestAdminUnbindRelation_KeepsHistoricalStardard 是解绑语义的本体。
//
// 语义是「历史星屑保留、不再产生新的」。解绑一个字节都不动星屑账本;它只清主库的
// inviter_id、把快照标成已解绑,并把星屑那一侧汇总出来的历史收益写进审计与回显。
func TestAdminUnbindRelation_KeepsHistoricalStardust(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	useRewardTotals(t, map[[2]int]int64{{61, 62}: 1200})

	seedUser(t, mainDB, 61, "inviter", 0, 1000)
	seedUser(t, mainDB, 62, "invitee", 61, 2000)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 62, InviterId: 61, InviteeRef: "r62", BoundAt: 2000,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/unbind",
		unbindBody(62, "用户申诉,推广关系错绑"), adminUnbindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, 0, inviterIdOf(t, mainDB, 62), "权威字段必须被清零,从此不再返")
	rel := relationRowOf(t, gdb, 62)
	require.NotNil(t, rel, "快照必须保留,否则历史流水会失去脱敏名与 ref")
	assert.Positive(t, rel.UnboundAt)
	assert.Equal(t, 61, rel.InviterId)

	var resp struct {
		Data struct {
			KeptStardust int64 `json:"kept_stardust"`
			InviterId    int   `json:"inviter_id"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	assert.EqualValues(t, 1200, resp.Data.KeptStardust)
	assert.Equal(t, 61, resp.Data.InviterId)

	logs := relationAuditLogs(t, gdb, "invite.relation.unbind")
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Contains(t, logs[0].Reason, "已产生的星屑全部保留(1200)",
		"这条语义必须写进审计正文:解绑之后主库里已经没有任何线索了")
	assert.Contains(t, logs[0].AfterSnap, `"total_stardust":1200`,
		"after 快照要能回答「保留了多少」——它靠快照里的 inviter_id 回落才算得出来")

	// 解绑之后判定必须立刻认"没有上线"。
	match, err := InviteeEligible(t.Context(), 62)
	require.NoError(t, err)
	assert.False(t, match.Eligible())

	// 用户端的"我的下线数"必须把已解绑的排除掉:那条关系不会再挣钱了。
	var live int64
	require.NoError(t, gdb.Model(&InviteRelation{}).
		Where("inviter_id = ? AND unbound_at = ?", 61, 0).Count(&live).Error)
	assert.EqualValues(t, 0, live)
}

// TestAdminUnbindRelation_RepeatIsRejected 守重复解绑。
//
// 回 200 等于告诉运营"这次解绑成功了",而实际什么都没发生 ——
// 两次解绑之间可能有别人重新绑过。
func TestAdminUnbindRelation_RepeatIsRejected(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 71, "inviter", 0, 1000)
	seedUser(t, mainDB, 72, "invitee", 71, 2000)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/unbind",
		unbindBody(72, "第一次解绑"), adminUnbindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/unbind",
		unbindBody(72, "重复解绑"), adminUnbindRelation)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_rel_not_bound")

	logs := relationAuditLogs(t, gdb, "invite.relation.unbind")
	require.Len(t, logs, 2, "被拒的那次同样要留痕")
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, qymodel.ResultFail, logs[1].Result)
}

// TestAdminListRelations_UnboundScope 守"已解绑"这一路的数据源。
//
// 主库那边 inviter_id 已经清零,"他曾经是谁的下线"只剩扩展库快照说得出来。
//
// 回滚验证:把 markRelationUnbound 里"快照不存在就补建一行"那一支删掉,
// 本用例变红(已解绑列表为空)。
func TestAdminListRelations_UnboundScope(t *testing.T) {
	newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	useRewardTotals(t, map[[2]int]int64{{81, 82}: 777})

	seedUser(t, mainDB, 81, "inviter", 0, 1000)
	seedUser(t, mainDB, 82, "invitee", 81, 2000)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/unbind",
		unbindBody(82, "误绑,解除关系"), adminUnbindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// 绑定中列表里已经没有它了。
	_, total := listRelations(t, "")
	assert.EqualValues(t, 0, total)

	items, total := listRelations(t, "scope=unbound")
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, 82, items[0].InviteeId)
	assert.Equal(t, 81, items[0].InviterId)
	assert.Equal(t, "invitee", items[0].InviteeUsername)
	assert.Positive(t, items[0].UnboundAt)
	assert.EqualValues(t, 777, items[0].TotalStardust, "解绑之后这条关系历史上挣的星屑必须还查得到")
}

// TestAdminRebindRelation_MovesFutureRewardsAndKeepsHistory 是换绑的本体:
// 主库指向改到新邀请人,旧邀请人已经挣到的星屑原样回显,新旧相同直接拒。
func TestAdminRebindRelation_MovesFutureRewardsAndKeepsHistory(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, inviteConfig(0))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	useRewardTotals(t, map[[2]int]int64{{91, 93}: 350})

	seedUser(t, mainDB, 91, "old", 0, 1000)
	seedUser(t, mainDB, 92, "new", 0, 1000)
	seedUser(t, mainDB, 93, "down", 91, 2000)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/rebind",
		rebindBody(93, 91, "换成同一个人"), adminRebindRelation)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_rel_same_inviter")

	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/invite/relations/rebind",
		rebindBody(93, 92, "原推广人离职,关系移交"), adminRebindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			OldInviterId int   `json:"old_inviter_id"`
			InviterId    int   `json:"inviter_id"`
			KeptStardust int64 `json:"kept_stardust"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, 91, resp.Data.OldInviterId)
	assert.Equal(t, 92, resp.Data.InviterId)
	assert.EqualValues(t, 350, resp.Data.KeptStardust, "原邀请人从这条关系上挣到的星屑原样回显")
	assert.Equal(t, 92, inviterIdOf(t, mainDB, 93))
	rel := relationRowOf(t, gdb, 93)
	require.NotNil(t, rel)
	assert.Equal(t, 92, rel.InviterId)

	match, err := InviteeEligible(t.Context(), 93)
	require.NoError(t, err)
	assert.Equal(t, 92, match.InviterId, "换绑之后此后的返还归新邀请人")

	logs := relationAuditLogs(t, gdb, "invite.relation.rebind")
	require.Len(t, logs, 2)
	assert.Equal(t, qymodel.ResultFail, logs[0].Result)
	assert.Equal(t, qymodel.ResultOK, logs[1].Result)
	assert.Contains(t, logs[1].Reason, "已产生的星屑全部保留(350)")
}

// TestAdminRelationRoutes_AreMounted 是断链防护。
//
// 处理器写对了却从没挂上路由,是本仓反复出现的形状:所有单元测试照样全绿,
// 而线上那个页面 404。
func TestAdminRelationRoutes_AreMounted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mod{}.RegisterAdminRoutes(engine.Group("/api/qy/admin"))

	want := map[string]string{
		"GET/api/qy/admin/invite/relations":         "GET",
		"POST/api/qy/admin/invite/relations/bind":   "POST",
		"POST/api/qy/admin/invite/relations/rebind": "POST",
		"POST/api/qy/admin/invite/relations/unbind": "POST",
		"POST/api/qy/admin/invite/relations/block":  "POST",
		"POST/api/qy/admin/invite/cache/invalidate": "POST",
		"GET/api/qy/admin/invite/daily-consume":     "GET",
		"GET/api/qy/admin/invite/health":            "GET",
	}
	got := map[string]string{}
	for _, r := range engine.Routes() {
		key := r.Method + r.Path
		if _, ok := want[key]; ok {
			got[key] = r.Method
		}
	}
	assert.Equal(t, want, got, "关系与报表的路由必须真的挂上去")
}
