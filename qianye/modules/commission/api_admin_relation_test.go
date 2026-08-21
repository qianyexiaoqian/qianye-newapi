package commission

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_relation_test.go —— AFF 关系列表与手工绑定/解绑的回归网。
//
// 这一批用例守的是四件只有让**两个数据库**都真跑一遍才看得见的事:
//
//  1. 列表的数据源是主库 users,不是扩展库的懒建快照 —— 拿快照当数据源
//     会让管理端看到一张少了绝大多数关系的表,而且没有任何报错;
//  2. 绑定写的是权威字段 users.inviter_id,并且防住自邀请与任意长度的环;
//  3. 解绑**一个字节都不动账本**:已产生的佣金原样保留,恒等式仍然成立;
//  4. 每一条写路径的成功与失败都留痕。

// seedUser 往主库插一个账号。inviterId 为 0 表示没有上线。
//
// aff_code 必须逐个不同:主库那一列带 uniqueIndex,留空会让第二个账号
// 撞唯一约束(这恰好也说明 aff_code 是"这个人自己的推广码",不是绑定关系)。
func seedUser(t *testing.T, mainDB *gorm.DB, id int, name string, inviterId int, createdAt int64) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.User{
		Id: id, Username: name, InviterId: inviterId, CreatedAt: createdAt,
		AffCode: "aff" + strconv.Itoa(id),
	}).Error)
}

func relationAuditLogs(t *testing.T, gdb *gorm.DB, action string) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", action).Order("id asc").Find(&rows).Error)
	return rows
}

func inviterIdOf(t *testing.T, mainDB *gorm.DB, id int) int {
	t.Helper()
	var u model.User
	require.NoError(t, mainDB.Where("id = ?", id).Take(&u).Error)
	return u.InviterId
}

func relationRowOf(t *testing.T, gdb *gorm.DB, inviteeId int) *InviteRelation {
	t.Helper()
	var rows []InviteRelation
	require.NoError(t, gdb.Where("invitee_id = ?", inviteeId).Find(&rows).Error)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

func bindBody(inviteeId, inviterId int, reason string) string {
	return `{"invitee_id":` + strconv.Itoa(inviteeId) +
		`,"inviter_id":` + strconv.Itoa(inviterId) +
		`,"reason":"` + reason + `"}`
}

func unbindBody(inviteeId int, reason string) string {
	return `{"invitee_id":` + strconv.Itoa(inviteeId) + `,"reason":"` + reason + `"}`
}

// listRelations 调列表接口并解出 items/total。
func listRelations(t *testing.T, query string) ([]relationView, int64) {
	t.Helper()
	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/relations?"+query, "", adminListRelations)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items []relationView `json:"items"`
			Total int64          `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data.Items, resp.Data.Total
}

// TestAdminListRelations_ReadsMainDbNotLazySnapshot 是本页最重要的一条。
//
// qy_invite_relation 是**懒建**的:ensureRelation 只在某个下线第一次产生佣金时
// 才写那一行。备份库实测 users 里有 375 条绑定,而快照表只有 8 行。拿快照当
// 数据源的实现同样能跑通所有"绑定/解绑"用例,却会让管理端看到一张少了 98% 的表。
//
// 回滚验证:把 listBoundRelations 的数据源从 model.User 换成 InviteRelation,
// 本用例立刻变红(拿到 1 条而不是 3 条)。
func TestAdminListRelations_ReadsMainDbNotLazySnapshot(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 1, "boss", 0, 1000)
	seedUser(t, mainDB, 2, "alice", 1, 2000)
	seedUser(t, mainDB, 3, "bob", 1, 3000)
	seedUser(t, mainDB, 4, "carol", 1, 4000)
	// 只有 alice 产生过佣金,所以扩展库里只有她那一行快照。
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 2, InviterId: 1, MaskedName: "a**e", InviteeRef: "ref2",
		BoundAt: 2000, CreatedAt: now, UpdatedAt: now,
	}).Error)
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 1, 2
		a.GrossAmount = decimal.NewFromInt(1500)
		a.BaseQuota = 30000
	})

	items, total := listRelations(t, "sort=invitee")
	require.EqualValues(t, 3, total, "三条绑定全都要出现,快照缺行不是漏行的理由")
	require.Len(t, items, 3)

	assert.Equal(t, 2, items[0].InviteeId)
	assert.Equal(t, "alice", items[0].InviteeUsername)
	assert.Equal(t, 1, items[0].InviterId)
	assert.Equal(t, "boss", items[0].InviterUsername, "邀请人用户名必须回主库补上")
	assert.True(t, items[0].SnapshotPresent)
	assert.EqualValues(t, 2000, items[0].BoundAt)
	assert.Equal(t, "1500", items[0].TotalCommission, "累计佣金按这一对聚合")
	assert.EqualValues(t, 1500, items[0].TotalCommissionQuota)
	assert.EqualValues(t, 30000, items[0].TotalBaseQuota)

	assert.Equal(t, 3, items[1].InviteeId)
	assert.False(t, items[1].SnapshotPresent, "没有快照要如实说,而不是谎称绑定时间是 0")
	assert.EqualValues(t, 3000, items[1].InviteeCreatedAt)
	assert.Equal(t, "0", items[1].TotalCommission)
}

// TestAdminListRelations_AggregatesPerPairNotPerInvitee 守累计佣金的聚合口径。
//
// 一个账号解绑后重新绑给另一个邀请人时,老邀请人名下的历史计佣行仍然挂着同一个
// invitee_id。只按 invitee 聚合会把那笔钱算进新关系的"累计佣金"里 ——
// 新邀请人一分钱没挣,列表上却写着一大笔。
//
// 回滚验证:把 hydrateRelationViews 的 Group("inviter_id, invitee_id") 改成
// Group("invitee_id"),本用例变红(新关系上出现 900)。
func TestAdminListRelations_AggregatesPerPairNotPerInvitee(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 10, "old-inviter", 0, 1000)
	seedUser(t, mainDB, 11, "new-inviter", 0, 1000)
	seedUser(t, mainDB, 12, "downline", 11, 2000)

	// 老邀请人时期挣的 900,与新关系无关。
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 10, 12
		a.GrossAmount = decimal.NewFromInt(900)
	})
	seedAccrual(t, gdb, 2, func(a *Accrual) {
		a.InviterId, a.InviteeId = 11, 12
		a.GrossAmount = decimal.NewFromInt(40)
	})

	items, total := listRelations(t, "")
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, 11, items[0].InviterId)
	assert.Equal(t, "40", items[0].TotalCommission,
		"只能算这一对挣的钱,老邀请人时期的 900 不属于这条关系")
}

// TestAdminListRelations_FindsFromEitherSide 守"从任一侧反查"。
//
// 回滚验证:把 EitherId 那一支的 OR 改成只匹配 invitee,
// "按邀请人名字查"会退化成空结果。
func TestAdminListRelations_FindsFromEitherSide(t *testing.T) {
	newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
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
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 31, "inviter", 0, 1000)
	seedUser(t, mainDB, 32, "invitee", 0, 2000)
	// 快照上留着上一次的拉黑标记。绑好之后必须被清掉,否则表现是
	// "绑定成功但永远不产生佣金",而界面上什么都看不出来。
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 32, InviterId: 99, InviteeRef: "stale", Blocked: true,
		RiskFlags: "reciprocal_invite", UnboundAt: now - 100,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/bind",
		bindBody(32, 31, "客服核实后补绑推广关系"), adminBindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, 31, inviterIdOf(t, mainDB, 32), "权威字段必须落到主库")
	rel := relationRowOf(t, gdb, 32)
	require.NotNil(t, rel)
	assert.Equal(t, 31, rel.InviterId)
	assert.EqualValues(t, 0, rel.UnboundAt, "重新绑定必须清掉解绑标记")
	assert.False(t, rel.Blocked, "刚被管理员显式建立的关系不能还带着拉黑标记")
	assert.Equal(t, "", rel.RiskFlags)
	assert.Positive(t, rel.BoundAt, "手工绑定的绑定时间取此刻")

	// 上游那套邀请奖励池一个字都不能动:那笔额度是注册时 inviteUser() 发的,
	// 事后补发等于凭空造钱。
	var inviter model.User
	require.NoError(t, mainDB.Where("id = ?", 31).Take(&inviter).Error)
	assert.Zero(t, inviter.AffQuota)
	assert.Zero(t, inviter.AffCount)

	logs := relationAuditLogs(t, gdb, "commission.relation.bind")
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, 32, logs[0].TargetUserId)
	assert.Equal(t, 7, logs[0].ActorUserId)
	assert.Contains(t, logs[0].Reason, "客服核实后补绑推广关系")
	assert.Contains(t, logs[0].BeforeSnap, `"inviter_id":99`,
		"before 快照要能回答「原来绑的是谁」")
	assert.Contains(t, logs[0].AfterSnap, `"inviter_id":31`)
}

// inviter_id 为 NULL 的账号必须能被绑上线。
//
// users.inviter_id 在三种受支持数据库上都是可空、无默认(MySQL
// `bigint DEFAULT NULL`、PG `is_nullable=YES`),而 `NULL = 0` 在 SQL 里恒为
// UNKNOWN。CAS 写成 `inviter_id = 0` 时,这类账号的 RowsAffected 恒为 0 →
// errRelRaced → 409「邀请关系已被另一次操作改动,请刷新后重试」——
// 把一个**永远重试不好**的确定性失败说成并发冲突。而 rebind / unbind
// 又都要求 inviter_id != 0,三个写点全被挡在门外,只能 DBA 直接 UPDATE
// 才能解。常规注册走 GORM Create 会显式写 0,所以 NULL 只来自导入、迁移、
// 从旧库恢复,以及任何省略该列的外部 INSERT。
//
// 夹具里的 NULL 必须用裸 SQL 写进去:GORM 的 Create 会把零值当成 0 带进
// INSERT,那样造不出这个形状,测试会因为错误的原因变绿。
func TestAdminBindRelation_BindsAccountWithNullInviterColumn(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 41, "inviter", 0, 1000)
	require.NoError(t, mainDB.Exec(
		"INSERT INTO users (id, username, password, aff_code, created_at) VALUES (?, ?, ?, ?, ?)",
		42, "null-invitee", "x", "aff42", 2000).Error)
	var nullCount int64
	require.NoError(t, mainDB.Model(&model.User{}).
		Where("id = ? AND inviter_id IS NULL", 42).Count(&nullCount).Error)
	require.EqualValues(t, 1, nullCount, "夹具没造出 NULL,下面的断言不能算数")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/bind",
		bindBody(42, 41, "从旧库导入的账号补绑推广关系"), adminBindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 41, inviterIdOf(t, mainDB, 42), "权威字段必须落到主库")

	rel := relationRowOf(t, gdb, 42)
	require.NotNil(t, rel)
	assert.Equal(t, 41, rel.InviterId)

	logs := relationAuditLogs(t, gdb, "commission.relation.bind")
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
}

// TestAdminBindRelation_RejectsSelfInviteAndCycles 是防环的表驱动。
//
// 只挡 A↔B 两两互邀会漏掉 A→B→C→A —— 它同样是自刷,只是多拉了一个号。
//
// 回滚验证:把 invitePathReaches 的循环改成"只看一层"(depth < 1),
// 三人环那一行变红;整个函数返回 false,自邀请之外的两行都变红。
func TestAdminBindRelation_RejectsSelfInviteAndCycles(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	// 链:41 ← 42 ← 43(43 的上线是 42,42 的上线是 41),40 是自由人。
	seedUser(t, mainDB, 40, "free", 0, 1000)
	seedUser(t, mainDB, 41, "top", 0, 1000)
	seedUser(t, mainDB, 42, "mid", 41, 2000)
	seedUser(t, mainDB, 43, "low", 42, 3000)
	seedUser(t, mainDB, 44, "bound", 40, 4000)

	cases := []struct {
		name      string
		inviteeId int
		inviterId int
		wantCode  string
	}{
		{"自邀请", 40, 40, "qy_rel_self_invite"},
		{"两两互邀:把 41 绑给它自己的下线 42", 41, 42, "qy_rel_cycle"},
		{"三人环:把 41 绑给下线的下线 43", 41, 43, "qy_rel_cycle"},
		{"已经绑过上线", 44, 41, "qy_rel_already_bound"},
		{"被邀请人不存在", 9999, 41, "qy_rel_user_not_found"},
		{"邀请人不存在", 40, 9999, "qy_rel_user_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/bind",
				bindBody(tc.inviteeId, tc.inviterId, "测试绑定闸门"), adminBindRelation)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)
		})
	}

	// 被拒的请求一个字节都不许改到主库。
	assert.Equal(t, 0, inviterIdOf(t, mainDB, 40))
	assert.Equal(t, 0, inviterIdOf(t, mainDB, 41))
	assert.Equal(t, 40, inviterIdOf(t, mainDB, 44))

	logs := relationAuditLogs(t, gdb, "commission.relation.bind")
	assert.Len(t, logs, len(cases),
		"每一次被拒都要留痕:「有人正在试图给一个已经有上线的账号改指向」正是要查的形状")
	for _, l := range logs {
		assert.Equal(t, qymodel.ResultFail, l.Result)
	}

	// 正向:自由人可以绑到链顶,这条链不成环。
	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/bind",
		bindBody(40, 43, "正常补绑"), adminBindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 43, inviterIdOf(t, mainDB, 40))
}

// TestAdminBindRelation_RequiresReason 守强制事由(与 adminSetWithdrawn 同口径)。
//
// 回滚验证:把 requireReason 的 `< 4` 改成 `< 0`,本用例变红。
func TestAdminBindRelation_RequiresReason(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 51, "a", 0, 1000)
	seedUser(t, mainDB, 52, "b", 0, 1000)

	for _, reason := range []string{"", "补绑", "  x "} {
		rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/bind",
			bindBody(52, 51, reason), adminBindRelation)
		require.Equal(t, http.StatusBadRequest, rec.Code, "事由 %q 必须被拒", reason)
		assert.Contains(t, rec.Body.String(), "qy_reason_required")
	}
	assert.Equal(t, 0, inviterIdOf(t, mainDB, 52))
	assert.Empty(t, relationAuditLogs(t, gdb, "commission.relation.bind"),
		"参数级拒绝发生在动库之前,不该产生审计噪音")
}

// TestAdminUnbindRelation_KeepsHistoricalCommission 是解绑语义的本体。
//
// 语义是「历史佣金保留、不再产生新佣金」。删计佣行会让 Σaccrual 与 Σsettlement
// 当场对不上,而且已结算的部分早就变成了 available_quota、可能已经提现走了。
//
// 回滚验证:在 unbindRelation 里加一句
// tx.Where("invitee_id = ?", inviteeId).Delete(&Accrual{}),
// 账本断言与恒等式断言同时变红。
func TestAdminUnbindRelation_KeepsHistoricalCommission(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 61, "inviter", 0, 1000)
	seedUser(t, mainDB, 62, "invitee", 61, 2000)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 62, InviterId: 61, InviteeRef: "r62", BoundAt: 2000,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	// 历史:1200 已经计佣并全部结算进余额。
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 61, 62
		a.GrossAmount = decimal.NewFromInt(1200)
		a.SettledAmount = decimal.NewFromInt(1200)
		a.Status = StatusSettled
	})
	seedLedgerBalance(t, gdb, 61, 1200, 0, 0, "0")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/unbind",
		unbindBody(62, "用户申诉,推广关系错绑"), adminUnbindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, 0, inviterIdOf(t, mainDB, 62), "权威字段必须被清零,从此不再计佣")
	rel := relationRowOf(t, gdb, 62)
	require.NotNil(t, rel, "快照必须保留,否则历史计佣行会失去脱敏名与 ref")
	assert.Positive(t, rel.UnboundAt)
	assert.Equal(t, 61, rel.InviterId)

	var accruals []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", 62).Find(&accruals).Error)
	require.Len(t, accruals, 1, "已产生的计佣行一条都不许删")
	assert.True(t, accruals[0].GrossAmount.Equal(decimal.NewFromInt(1200)))

	after := balanceOf(t, gdb, 61)
	require.NotNil(t, after)
	assert.EqualValues(t, 1200, after.AvailableQuota, "已结算进余额的佣金不许被解绑动到")
	assertLedgerIdentity(t, after)

	var resp struct {
		Data struct {
			KeptCommissionQuota int64 `json:"kept_commission_quota"`
			InviterId           int   `json:"inviter_id"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	assert.EqualValues(t, 1200, resp.Data.KeptCommissionQuota)
	assert.Equal(t, 61, resp.Data.InviterId)

	logs := relationAuditLogs(t, gdb, "commission.relation.unbind")
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Contains(t, logs[0].Reason, "已产生的佣金全部保留",
		"这条语义必须写进审计正文:解绑之后主库里已经没有任何线索了")
	assert.Contains(t, logs[0].AfterSnap, `"total_commission":"1200"`,
		"after 快照要能回答「保留了多少」——它靠快照里的 inviter_id 回落才算得出来")

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
//
// 回滚验证:把 `if invitee.InviterId == 0 { return errRelNotBound }` 删掉,
// 第二次调用会返回 200 且不再留下 fail 审计。
func TestAdminUnbindRelation_RepeatIsRejected(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 71, "inviter", 0, 1000)
	seedUser(t, mainDB, 72, "invitee", 71, 2000)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/unbind",
		unbindBody(72, "第一次解绑"), adminUnbindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/unbind",
		unbindBody(72, "重复解绑"), adminUnbindRelation)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_rel_not_bound")

	logs := relationAuditLogs(t, gdb, "commission.relation.unbind")
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
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 81, "inviter", 0, 1000)
	seedUser(t, mainDB, 82, "invitee", 81, 2000)
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 81, 82
		a.GrossAmount = decimal.NewFromInt(777)
	})

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/unbind",
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
	assert.Equal(t, "777", items[0].TotalCommission,
		"解绑之后这条关系历史上挣的钱必须还查得到")
}

// TestAdminRelationRoutes_AreMounted 是断链防护。
//
// 处理器写对了却从没挂上路由,是本仓反复出现的形状:所有单元测试照样全绿,
// 而线上那个页面 404。
//
// 回滚验证:把 module.go 里的 registerRelationRoutes(g, crit) 删掉,本用例变红。
func TestAdminRelationRoutes_AreMounted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mod{}.RegisterAdminRoutes(engine.Group("/api/qy/admin"))

	want := map[string]string{
		"GET/api/qy/admin/commission/relations":         "GET",
		"POST/api/qy/admin/commission/relations/bind":   "POST",
		"POST/api/qy/admin/commission/relations/unbind": "POST",
		"POST/api/qy/admin/commission/balances/adjust":  "POST",
	}
	got := map[string]string{}
	for _, r := range engine.Routes() {
		key := r.Method + r.Path
		if _, ok := want[key]; ok {
			got[key] = r.Method
		}
	}
	assert.Equal(t, want, got, "关系列表/绑定/解绑与手工调整四条路由必须真的挂上去")
}
