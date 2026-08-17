package commission

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_users_test.go —— 以用户为中心的佣金总表、换绑、以及拉黑那两个缺陷。
//
// 这一批用例守的是五件只有让**两个数据库**都真跑一遍才看得见的事:
//
//  1. 拉黑一条**快照表里没有行**的真实关系,必须真的把它拉黑,而不是回 200
//     什么都不做(线上实测过的形状:377 条真实关系 / 11 行快照);
//  2. invitee_id ≤ 0(手工调整那类不挂在任何下线上的计佣行)必须给出
//     **专门的** code 与文案,而不是与报文错误共用"请求参数有误";
//  3. 总表的每一个数字都能手算复现,而且下线数走的是主库权威字段;
//  4. 换绑改的是 users.inviter_id,防环/防自邀/防同人,且**一个字节都不动账本**;
//  5. 搜索与三个筛选各自真的收窄了结果集,而不是被忽略后返回全表。

// ───────────────────────── 小工具 ─────────────────────────

func listUserCommissions(t *testing.T, query string) ([]userCommissionView, int64) {
	t.Helper()
	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/users?"+query, "", adminListUserCommissions)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items []userCommissionView `json:"items"`
			Total int64                `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data.Items, resp.Data.Total
}

func userRowById(t *testing.T, items []userCommissionView, id int) userCommissionView {
	t.Helper()
	for _, it := range items {
		if it.UserId == id {
			return it
		}
	}
	t.Fatalf("总表里找不到 user_id=%d", id)
	return userCommissionView{}
}

func userIdsOf(items []userCommissionView) []int {
	out := make([]int, 0, len(items))
	for _, it := range items {
		out = append(out, it.UserId)
	}
	return out
}

func blockBody(inviteeId int, blocked bool, reason string) string {
	return `{"invitee_id":` + strconv.Itoa(inviteeId) +
		`,"blocked":` + strconv.FormatBool(blocked) +
		`,"reason":"` + reason + `"}`
}

// codeOf 取出失败响应里的业务 code。前端就是靠它分辨该弹哪一句话。
func codeOf(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Success bool   `json:"success"`
	}
	require.NoError(t, common.Unmarshal(body, &resp))
	return resp.Code
}

// ───────────────────────── 缺陷一:拉黑静默失败 ─────────────────────────

// TestAdminBlockRelation_BlocksRelationWithoutSnapshotRow 是本文件最重要的一条。
//
// qy_invite_relation 是**懒建**的:ensureRelation 只在某个下线第一次产生佣金时
// 才写那一行。线上实测 users 里 377 条绑定、快照表 11 行 —— 也就是 97% 的真实
// 关系在这张表里根本没有行。旧实现直接 `Where("invitee_id = ?").Updates(...)`,
// 影响 0 行,接口照样回 `{"blocked":true}`。
//
// 后果不是"少了个提示":blockedInvitees() 读的正是
// `qy_invite_relation WHERE blocked = true`,没有行 = 这个人从来没被拉黑过。
// 运营看到成功提示以为自刷被止住,而佣金一分不少地继续计给上线。
//
// 回滚验证:把 setRelationBlocked 里补建快照行的那一段删掉(退回纯 Updates),
// 本用例立刻变红 —— rel 为 nil,且 blockedInvitees 不含 42。
func TestAdminBlockRelation_BlocksRelationWithoutSnapshotRow(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 41, "inviter", 0, 1000)
	seedUser(t, mainDB, 42, "downline", 41, 2000)
	// 刻意**不**建快照行:这正是线上 97% 关系的样子。
	require.Nil(t, relationRowOf(t, gdb, 42))

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/block",
		blockBody(42, true, "疑似自刷"), adminBlockRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rel := relationRowOf(t, gdb, 42)
	require.NotNil(t, rel, "快照缺行时必须按主库的权威字段补建,否则这次拉黑等于没做")
	assert.True(t, rel.Blocked)
	assert.Equal(t, 41, rel.InviterId, "补出来的行必须挂在主库那个权威邀请人上")
	assert.Equal(t, "疑似自刷", rel.BlockReason, "人工事由落 block_reason")
	assert.Equal(t, "", rel.RiskFlags, "risk_flags 是自动风控的地盘,人工事由不许写进去")
	assert.EqualValues(t, 2000, rel.BoundAt, "自动绑定发生在注册那一刻")

	// 计佣链路真正读的是这个集合。上面那一行不进这里,拉黑就是白做的。
	assert.True(t, blockedInvitees(t.Context())[42],
		"计佣链路读的是 blockedInvitees(),补建的行必须让它认得出来")
}

// TestAdminBlockRelation_UpdatesExistingSnapshotAndUnblocks 守"有快照行"那一路
// 没有被上一条的修复带坏,并且解封能把标记摘掉。
func TestAdminBlockRelation_UpdatesExistingSnapshotAndUnblocks(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 51, "inviter", 0, 1000)
	seedUser(t, mainDB, 52, "downline", 51, 2000)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 52, InviterId: 51, InviteeRef: "ref52",
		BoundAt: 2000, CreatedAt: now, UpdatedAt: now,
	}).Error)

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/block",
		blockBody(52, true, "风控命中"), adminBlockRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, relationRowOf(t, gdb, 52).Blocked)

	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/block",
		blockBody(52, false, "人工复核后放行"), adminBlockRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, relationRowOf(t, gdb, 52).Blocked, "解封必须把标记摘掉")
	assert.False(t, blockedInvitees(t.Context())[52])
	// 快照行不能被解封动作删掉:计佣行还指着这个 invitee_id。
	assert.NotNil(t, relationRowOf(t, gdb, 52))
}

// TestAdminBlockRelation_RejectsWithDistinctCodes 守缺陷二:错误必须分得清。
//
// 项目方今天遇到的 400 就在这里:手工调整产生的计佣行 invitee_id = 0
// (它不挂在任何下线上),前端无条件渲染「拉黑」,后端与"报文格式错"共用
// 同一个 qy_invalid_param + "请求参数有误"。运营因此以为自己填错了参数,
// 甚至怀疑那一次是不是已经动了钱。
//
// 回滚验证:把 InviteeId <= 0 那一支并回 ShouldBindJSON 的 err 判断,
// 第二个子用例立刻变红(拿到 qy_invalid_param 而不是 qy_rel_no_relation)。
func TestAdminBlockRelation_RejectsWithDistinctCodes(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 61, "someone", 0, 1000)

	cases := []struct {
		name     string
		body     string
		wantCode string
		wantWhy  string
	}{
		{
			name: "报文本身坏了", body: `{"invitee_id":`,
			wantCode: "qy_invalid_param",
			wantWhy:  "这一路是真的报文问题,提示" + "请求格式错误" + "是对的",
		},
		{
			name: "手工调整行:不挂在任何邀请关系上", body: blockBody(0, true, "误点"),
			wantCode: "qy_rel_no_relation",
			wantWhy: "invitee_id=0 的计佣行来自手工调整,它不属于任何一条邀请关系;" +
				"这与「报文写错了」是两件事,必须给不同的 code",
		},
		{
			name: "账号不存在", body: blockBody(99999, true, "打错了 id"),
			wantCode: "qy_rel_user_not_found",
			wantWhy:  "拉黑一个不存在的账号必须明确报错,不能悄悄建出一行快照",
		},
		{
			name: "账号存在但没有上线", body: blockBody(61, true, "没有上线的人"),
			wantCode: "qy_rel_not_bound",
			wantWhy:  "没有邀请关系就没有" + "停止未来计佣" + "这件事可做",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAdminHandler(t, http.MethodPost,
				"/api/qy/admin/commission/relations/block", tc.body, adminBlockRelation)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, tc.wantCode, codeOf(t, rec.Body.Bytes()), tc.wantWhy)
		})
	}
	// 四次拒绝都不该在快照表里留下任何一行。
	var n int64
	require.NoError(t, gdb.Model(&InviteRelation{}).Count(&n).Error)
	assert.EqualValues(t, 0, n, "被拒绝的拉黑不能留下半行快照")
}

// TestAdminBlockRelation_AuditsSuccessAndFailure 守埋点:成功与失败都要留痕。
func TestAdminBlockRelation_AuditsSuccessAndFailure(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 71, "inviter", 0, 1000)
	seedUser(t, mainDB, 72, "downline", 71, 2000)

	callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/block",
		blockBody(72, true, "疑似自刷"), adminBlockRelation)
	callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/block",
		blockBody(99999, true, "打错了 id"), adminBlockRelation)

	logs := relationAuditLogs(t, gdb, "commission.relation.block")
	require.Len(t, logs, 2, "成功与失败各一条 —— 「有人试图拉黑一个不存在的账号」同样要查得到")
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, 72, logs[0].TargetUserId)
	assert.Contains(t, logs[0].Reason, "已发放的佣金不回收",
		"语义必须写死在埋点正文里:拉黑只停未来,不动账本")
	assert.Equal(t, qymodel.ResultFail, logs[1].Result)
}

// ───────────────────────── 用户维度总表 ─────────────────────────

// seedUserCommissionFixture 搭一套能手算的场景,总表的每个用例共用它。
//
//	boss(100)   ── 拉了 alice / bob / carol 三个人,自己没有上线
//	  alice(101) ── 拉了 dave 一个人;她自己被拉黑(她的消费不再给 boss 计佣)
//	    dave(103)
//	  bob(102)   ── 没有下线,也没有余额
//	  carol(104) ── 没有下线,但账上有钱
//	loner(105)  ── 与返佣完全无关:没有上线、没有下线、没有佣金账
func seedUserCommissionFixture(t *testing.T, gdb *gorm.DB, mainDB *gorm.DB) {
	t.Helper()
	seedUser(t, mainDB, 100, "boss", 0, 1000)
	seedUser(t, mainDB, 101, "alice", 100, 2000)
	seedUser(t, mainDB, 102, "bob", 100, 3000)
	seedUser(t, mainDB, 104, "carol", 100, 4000)
	seedUser(t, mainDB, 103, "dave", 101, 5000)
	seedUser(t, mainDB, 105, "loner", 0, 6000)

	// boss 名下两笔计佣:1500(来自 alice)+ 250(来自 bob)= 1750。
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 100, 101
		a.GrossAmount = decimal.NewFromInt(1500)
	})
	seedAccrual(t, gdb, 2, func(a *Accrual) {
		a.InviterId, a.InviteeId = 100, 102
		a.GrossAmount = decimal.NewFromInt(250)
	})
	// 一笔作废行:不能进任何聚合。
	seedAccrual(t, gdb, 3, func(a *Accrual) {
		a.InviterId, a.InviteeId = 100, 102
		a.GrossAmount = decimal.NewFromInt(999)
		a.Status = StatusVoided
	})

	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&Balance{
		UserId: 100, AvailableQuota: 1200, FrozenQuota: 300, WithdrawnQuota: 200,
		TotalEarnedQuota: 1800, TotalClawbackQuota: 100,
		UnsettledAmount: decimal.NewFromInt(0), AvailableFiat: decimal.Zero,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	// carol 有钱但没有下线 —— 用来把"有余额"与"有下线"两个筛选区分开。
	require.NoError(t, gdb.Create(&Balance{
		UserId: 104, AvailableQuota: 70,
		TotalEarnedQuota: 70, UnsettledAmount: decimal.NewFromInt(0),
		AvailableFiat: decimal.Zero, CreatedAt: now, UpdatedAt: now,
	}).Error)
	// alice 作为下线被拉黑。
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 101, InviterId: 100, InviteeRef: "ref101", BoundAt: 2000,
		Blocked: true, RiskFlags: "reciprocal_invite", CreatedAt: now, UpdatedAt: now,
	}).Error)
}

// TestAdminListUserCommissions_AggregatesPerUser 是总表的本体:每个数字手算复现。
//
// 回滚验证:把 hydrateUserCommissionViews 里 ⑦ 的 `status <> voided` 去掉,
// boss 的 accrual_gross 会从 1750 变成 2749,本用例立刻变红。
func TestAdminListUserCommissions_AggregatesPerUser(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUserCommissionFixture(t, gdb, mainDB)

	items, total := listUserCommissions(t, "sort=user&page_size=50")
	// boss / alice / bob / carol / dave 五个人与返佣有关;loner 无关,不该出现。
	require.EqualValues(t, 5, total)
	assert.Equal(t, []int{100, 101, 102, 103, 104}, userIdsOf(items))
	assert.NotContains(t, userIdsOf(items), 105,
		"与返佣毫无关系的账号不该出现在这张表上 —— 全站账号铺满会让运营翻不到要找的人")

	boss := userRowById(t, items, 100)
	assert.Equal(t, 3, boss.InviteeCount, "alice / bob / carol,数的是主库权威字段")
	assert.Equal(t, 0, boss.InviterId)
	assert.False(t, boss.InviterResolved)
	assert.True(t, boss.HasBalanceRow)
	assert.EqualValues(t, 1200, boss.AvailableQuota)
	assert.EqualValues(t, 300, boss.FrozenQuota)
	assert.EqualValues(t, 200, boss.WithdrawnQuota)
	assert.EqualValues(t, 1800, boss.TotalEarnedQuota)
	assert.EqualValues(t, 100, boss.TotalClawbackQuota)
	// 恒等式:1800 − 100 − 300 − 200 = 1200,与 available 一致,漂移为 0。
	assert.EqualValues(t, 1200, boss.DerivedAvailable)
	assert.EqualValues(t, 0, boss.LedgerDrift)
	assert.Equal(t, 1, boss.BlockedInviteeCount, "alice 那条关系被拉黑了")

	alice := userRowById(t, items, 101)
	assert.Equal(t, 1, alice.InviteeCount, "alice 拉了 dave")
	assert.Equal(t, 100, alice.InviterId)
	assert.Equal(t, "boss", alice.InviterUsername, "上线用户名必须回主库补上")
	assert.True(t, alice.InviterResolved)
	assert.True(t, alice.InviterBlocked, "她作为下线被拉黑了")
	assert.False(t, alice.HasBalanceRow, "她自己还没产生过佣金")
	assert.EqualValues(t, 0, alice.AvailableQuota)

	bob := userRowById(t, items, 102)
	assert.Equal(t, 0, bob.InviteeCount)
	assert.False(t, bob.HasBalanceRow)
	assert.False(t, bob.InviterBlocked)
}

// TestAdminListUserCommissions_SearchAndFilters 守搜索与三个筛选。
//
// 每一条都必须**真的收窄**结果集。被忽略的筛选条件返回的是全表,而它看起来
// 与"这个人排在第一页"一模一样 —— 而这一页上有改钱和改关系的按钮。
func TestAdminListUserCommissions_SearchAndFilters(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUserCommissionFixture(t, gdb, mainDB)
	// 给 carol 一个邮箱,验证按邮箱搜。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 104).
		Update("email", "carol@example.invalid").Error)

	cases := []struct {
		name  string
		query string
		want  []int
		why   string
	}{
		{"按用户名前缀", "keyword=ali&sort=user", []int{101},
			"边打字边搜必须命中前缀"},
		{"按 id 精确", "keyword=102&sort=user", []int{102},
			"纯数字优先当 id"},
		{"按邮箱前缀", "keyword=carol@&sort=user", []int{104},
			"邮箱也要能搜"},
		{"查无此人回空页", "keyword=nobody&sort=user", []int{},
			"忽略筛选返回全表,与「他排在第一页」看起来一模一样"},
		{"有下线", "has_invitees=true&sort=user", []int{100, 101},
			"只有 boss 与 alice 拉到过人"},
		{"有余额", "has_balance=true&sort=user", []int{100, 104},
			"carol 没有下线但账上有钱,她必须在;bob 有关系没有钱,必须不在"},
		{"被拉黑", "blocked=true&sort=user", []int{101},
			"只有 alice 那条关系被拉黑"},
		{"LIKE 通配符按字面量处理", "keyword=%25&sort=user", []int{},
			"`%` 不转义的话它会匹配所有人 —— 那是一张假的搜索结果"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, total := listUserCommissions(t, tc.query+"&page_size=50")
			assert.Equal(t, tc.want, userIdsOf(items), tc.why)
			assert.EqualValues(t, len(tc.want), total, tc.why)
		})
	}
}

// TestAdminListUserCommissions_SortsAcrossBothDatabases 守跨库排序。
//
// 「谁拉的人最多」的数在主库、「可提现最多」的数在扩展库,任何一条 SQL 都
// 排不动它们两个。所以排序必须发生在 join 之后的内存里 —— 在数据库里排的话
// 只能排一半,而"只排了一半的排序"比不提供排序更糟:它看起来是对的。
//
// 回滚验证:把 adminListUserCommissions 里的 sort.SliceStable 删掉,
// 两个子用例都变红(拿到的是主库返回的天然顺序)。
func TestAdminListUserCommissions_SortsAcrossBothDatabases(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUserCommissionFixture(t, gdb, mainDB)
	// climber 的 id 比所有人都大、下线也比所有人都多。没有他的话
	// "按下线数降序"恰好等于"按 id 升序",这条断言就是空转的
	// (实测:去掉 climber 后把 invitees 排序改成恒 false,用例照样绿)。
	seedUser(t, mainDB, 110, "climber", 0, 7000)
	for i := range 5 {
		seedUser(t, mainDB, 111+i, "climber-down-"+strconv.Itoa(i), 110, 8000)
	}

	// 拉的人最多的在最前:climber 5 人、boss 3 人、alice 1 人,其余 0 人。
	items, _ := listUserCommissions(t, "sort=invitees&page_size=50")
	require.NotEmpty(t, items)
	assert.Equal(t, 110, items[0].UserId,
		"下线数(主库)降序 —— id 最大的人排在最前,这一维必须真的被排过")
	assert.Equal(t, []int{110, 100, 101}, userIdsOf(items)[:3])
	assert.Equal(t, []int{102, 103, 104, 111, 112, 113, 114, 115},
		userIdsOf(items)[3:], "下线数相同的一律按 user_id 升序,保证翻页不漏行")

	// 可提现最多的在最前:boss 1200、carol 70,其余 0。carol(104)排在
	// alice(101)前面,所以这一条同样不会退化成 id 序。
	items, _ = listUserCommissions(t, "sort=available&page_size=50")
	assert.Equal(t, []int{100, 104}, userIdsOf(items)[:2],
		"可提现(扩展库)降序 —— 它与上一条不在同一个库里,却在同一个下拉里")
}

// TestAdminListUserCommissions_TotalsFollowTheFilter 守合计。
//
// 合计必须跟着**当前筛选**走。给一个全表合计而筛选只剩两个人,运营会拿着
// 一个和眼前列表对不上的数字去对账。
func TestAdminListUserCommissions_TotalsFollowTheFilter(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUserCommissionFixture(t, gdb, mainDB)

	readTotals := func(query string) (int, int64, int64, int) {
		t.Helper()
		rec := callAdminHandler(t, http.MethodGet,
			"/api/qy/admin/commission/users?"+query, "", adminListUserCommissions)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp struct {
			Data struct {
				Totals struct {
					UserCount      int   `json:"user_count"`
					AvailableQuota int64 `json:"available_quota"`
					WithdrawnQuota int64 `json:"withdrawn_quota"`
					InviteeCount   int   `json:"invitee_count"`
				} `json:"totals"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
		tt := resp.Data.Totals
		return tt.UserCount, tt.AvailableQuota, tt.WithdrawnQuota, tt.InviteeCount
	}

	// 不筛:5 个人,可提现 1200 + 70,已提现 200,下线 3 + 1。
	users, available, withdrawn, invitees := readTotals("page_size=50")
	assert.Equal(t, 5, users)
	assert.EqualValues(t, 1270, available)
	assert.EqualValues(t, 200, withdrawn)
	assert.Equal(t, 4, invitees)

	// 只看有下线的:boss + alice,可提现只剩 boss 的 1200。
	users, available, withdrawn, invitees = readTotals("has_invitees=true&page_size=50")
	assert.Equal(t, 2, users, "合计里的人数必须等于筛选后的行数")
	assert.EqualValues(t, 1200, available, "carol 的 70 被筛掉了,合计里就不该还有它")
	assert.EqualValues(t, 200, withdrawn)
	assert.Equal(t, 4, invitees)
}

// TestAdminListUserCommissions_ZeroBalanceRowIsNotMistakenForMoney 守一个具体的
// 空值形状:没有余额行的人,他的 `unsettled_amount` 必须是 "0" 而不是空串。
//
// 这不是美观问题。"有余额"筛选判的是 `unsettled_amount == "0"`;手搭一个
// balanceView{} 出来那一格是空串,于是判定不成立 —— 一个佣金账都没有的人
// 会被当成"账上有钱"筛出来,而运营正是照着这个筛选去找该给谁结算。
//
// 回滚验证:把 buildUserCommissionRows 里的 newBalanceView(Balance{UserId:...})
// 换回 balanceView{UserId: ...},本用例与「有余额」那个子用例一起变红。
func TestAdminListUserCommissions_ZeroBalanceRowIsNotMistakenForMoney(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUserCommissionFixture(t, gdb, mainDB)

	items, _ := listUserCommissions(t, "sort=user&page_size=50")
	bob := userRowById(t, items, 102)
	require.False(t, bob.HasBalanceRow, "bob 一分佣金都没产生过")
	assert.Equal(t, "0", bob.UnsettledAmount, "没有余额行不等于没有这一列")
	assert.Equal(t, "0", bob.AvailableFiat)
	assert.EqualValues(t, 0, bob.DerivedAvailable)
	assert.EqualValues(t, 0, bob.LedgerDrift)
}

// ───────────────────────── 换绑 ─────────────────────────

func rebindBody(inviteeId, inviterId int, reason string) string {
	return `{"invitee_id":` + strconv.Itoa(inviteeId) +
		`,"inviter_id":` + strconv.Itoa(inviterId) +
		`,"reason":"` + reason + `"}`
}

// TestAdminRebindRelation_MovesAuthorityAndKeepsLedger 是换绑的本体。
//
// 两件事必须同时成立:主库的权威字段真的改了(否则从下一笔起佣金还是算给老上线),
// 并且**账本一个字节都没动**(老上线名下已经产生的计佣行原样保留)。
//
// 回滚验证:把 rebindRelation 里 CAS 那条 Update 删掉,inviter_id 断言变红;
// 若改成顺手把老关系的 accrual 删掉/改指向,kept 与账本断言变红。
func TestAdminRebindRelation_MovesAuthorityAndKeepsLedger(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 200, "old-inviter", 0, 1000)
	seedUser(t, mainDB, 201, "new-inviter", 0, 1000)
	seedUser(t, mainDB, 202, "downline", 200, 2000)
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 200, 202
		a.GrossAmount = decimal.NewFromInt(880)
	})

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/rebind",
		rebindBody(202, 201, "原推广人离职,关系移交"), adminRebindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Data struct {
			OldInviterId int   `json:"old_inviter_id"`
			InviterId    int   `json:"inviter_id"`
			KeptQuota    int64 `json:"kept_commission_quota"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, 200, resp.Data.OldInviterId)
	assert.Equal(t, 201, resp.Data.InviterId)
	assert.EqualValues(t, 880, resp.Data.KeptQuota,
		"回显的是**原邀请人**保留下来的佣金 —— 这就是「历史保留」那句话的量化形式")

	assert.Equal(t, 201, inviterIdOf(t, mainDB, 202), "权威字段必须落到主库 users.inviter_id")
	rel := relationRowOf(t, gdb, 202)
	require.NotNil(t, rel)
	assert.Equal(t, 201, rel.InviterId, "快照要跟上,否则列表与权威字段说的不是一回事")

	// 账本原样:老上线名下那 880 一分不少,也没有多出任何一行。
	var accruals []Accrual
	require.NoError(t, gdb.Order("id asc").Find(&accruals).Error)
	require.Len(t, accruals, 1, "换绑不该产生任何计佣行")
	assert.Equal(t, 200, accruals[0].InviterId, "历史计佣行仍然挂在老邀请人名下")
	assert.Equal(t, "880", accruals[0].GrossAmount.String())
}

// TestAdminRebindRelation_Rejections 守四道闸门,每一道给独立的 code。
func TestAdminRebindRelation_Rejections(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	// a → b → c 这条链;另有 solo 没有上线。
	seedUser(t, mainDB, 210, "a", 0, 1000)
	seedUser(t, mainDB, 211, "b", 210, 2000)
	seedUser(t, mainDB, 212, "c", 211, 3000)
	seedUser(t, mainDB, 213, "solo", 0, 4000)

	cases := []struct {
		name     string
		body     string
		wantCode string
		wantHTTP int
		why      string
	}{
		{"自邀请", rebindBody(211, 211, "手滑选到了他自己"), "qy_rel_self_invite",
			http.StatusBadRequest, "自己邀请自己是最基本的自刷形状"},
		{"两两互邀成环", rebindBody(210, 211, "把 a 挂到 b 名下"), "qy_rel_not_bound",
			http.StatusBadRequest, "a 没有上线,该走绑定而不是换绑"},
		{"三人环 a→b→c→a", rebindBody(211, 212, "把 b 挂到 c 名下"), "qy_rel_cycle",
			http.StatusBadRequest,
			"只挡两两互邀会漏掉 a→b→c→a:它同样是自刷,只是多拉了一个号"},
		{"换成同一个人", rebindBody(212, 211, "换成他现在这个上线"), "qy_rel_same_inviter",
			http.StatusBadRequest,
			"什么都没变却回成功,运营下一步就会去账本里找那笔并不存在的变化"},
		{"没有上线的账号", rebindBody(213, 210, "solo 本来就没有上线"), "qy_rel_not_bound",
			http.StatusBadRequest, "该走绑定;悄悄替运营换一个动作执行是最不该做的"},
		{"账号不存在", rebindBody(212, 99999, "新上线不存在"), "qy_rel_user_not_found",
			http.StatusBadRequest, "绑到一个不存在的账号上等于把佣金发进黑洞"},
		{"事由太短", rebindBody(212, 210, "改"), "qy_reason_required",
			http.StatusBadRequest, "改动资金归属没有事由,事后无法与误操作区分"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAdminHandler(t, http.MethodPost,
				"/api/qy/admin/commission/relations/rebind", tc.body, adminRebindRelation)
			assert.Equal(t, tc.wantHTTP, rec.Code, rec.Body.String())
			assert.Equal(t, tc.wantCode, codeOf(t, rec.Body.Bytes()), tc.why)
		})
	}

	// 每一次拒绝之后,主库的三条关系必须原封不动。
	assert.Equal(t, 0, inviterIdOf(t, mainDB, 210))
	assert.Equal(t, 210, inviterIdOf(t, mainDB, 211))
	assert.Equal(t, 211, inviterIdOf(t, mainDB, 212))
	assert.Equal(t, 0, inviterIdOf(t, mainDB, 213))
	// 也不该在扩展库留下任何快照。
	var n int64
	require.NoError(t, gdb.Model(&InviteRelation{}).Count(&n).Error)
	assert.EqualValues(t, 0, n)
}

// TestAdminRebindRelation_AuditsSuccessAndFailure 守埋点,并锁住正文里的语义。
//
// 「已产生的佣金全部保留、不再产生新的」这句话是事后解释"这个人的佣金为什么
// 停在这个数"的唯一材料,它必须写死在埋点正文里。
func TestAdminRebindRelation_AuditsSuccessAndFailure(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 220, "old", 0, 1000)
	seedUser(t, mainDB, 221, "new", 0, 1000)
	seedUser(t, mainDB, 222, "downline", 220, 2000)

	callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/rebind",
		rebindBody(222, 221, "原推广人离职,关系移交"), adminRebindRelation)
	// 再来一次:现在上线已经是 221,撞 same_inviter。
	callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/rebind",
		rebindBody(222, 221, "重复提交同一次移交"), adminRebindRelation)

	logs := relationAuditLogs(t, gdb, "commission.relation.rebind")
	require.Len(t, logs, 2, "成功与失败各一条")
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, 222, logs[0].TargetUserId)
	assert.Contains(t, logs[0].Reason, "全部保留")
	assert.Contains(t, logs[0].Reason, "不再产生新的")
	assert.NotEmpty(t, logs[0].BeforeSnap, "before 快照是事后回答「原来绑的是谁」的唯一材料")
	assert.NotEmpty(t, logs[0].AfterSnap)
	assert.Equal(t, qymodel.ResultFail, logs[1].Result)
}

// ───────────────── 缺陷三:确认框念错了金额 ─────────────────

// TestAdminListUserCommissions_InviterCommissionIsThePairNotTheOwnEarnings
// 守的是「管理绑定关系」确认框上那个金额。
//
// 这一列回答的问题是:「把这条关系换掉或解掉之后,有多少钱会留在**原邀请人**
// 名下」。它是 (上线, 本人) 这一对的计佣合计,与本人自己的 total_earned_quota
// 是**方向相反**的两个数 —— 后者是他从自己所有下线身上挣的。
//
// 缺陷的形状(实测于备份库):397 号自己没有下线,total_earned_quota = 0,而他
// 上线 391 从他身上已经挣到 13517。前端渲染 total_earned_quota 时,确认框写
// 「保留 0」,点下去之后的成功提示写「保留 13517」—— 同一次操作、同一个标签、
// 两个数差 13517,而这是一个改钱的页面。
//
// 本用例的 fixture 复刻这个形状:alice(101)自己没有余额行(earned = 0),
// 她上线 boss(100)从她身上挣到 1500。
//
// 回滚验证:把 attachInviterCommission 那一行去掉(或让它抄
// TotalEarnedQuota),alice 的断言立刻变红。
func TestAdminListUserCommissions_InviterCommissionIsThePairNotTheOwnEarnings(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUserCommissionFixture(t, gdb, mainDB)

	items, _ := listUserCommissions(t, "sort=user&page_size=50")

	cases := []struct {
		name       string
		userId     int
		wantPair   int64
		wantEarned int64
		why        string
	}{
		{
			name: "他自己没有下线,但上线从他身上挣了钱", userId: 101,
			wantPair: 1500, wantEarned: 0,
			why: "线上 397 号的形状:渲染成 total_earned_quota 会写「保留 0」,而实际保留 1500",
		},
		{
			name: "作废的计佣行不算进这一对", userId: 102,
			wantPair: 250, wantEarned: 0,
			why: "bob 名下 250 + 999(voided);把作废行算进来就是当着运营的面虚报保留额",
		},
		{
			name: "没有上线的人恒为 0,哪怕他自己挣了很多", userId: 100,
			wantPair: 0, wantEarned: 1800,
			why: "boss 从三个下线身上挣了 1800,但没有人从他身上挣过 —— 这一列必须是 0",
		},
		{
			name: "有上线但这一对没产生过佣金", userId: 104,
			wantPair: 0, wantEarned: 70,
			why: "carol 账上有 70 是她自己的钱,不是她上线从她身上挣的",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := userRowById(t, items, tc.userId)
			assert.EqualValues(t, tc.wantPair, row.InviterCommissionQuota, tc.why)
			assert.EqualValues(t, tc.wantEarned, row.TotalEarnedQuota,
				"对照组:这两个数必须不相等,否则这条用例证明不了任何东西")
		})
	}
}

// TestAdminListUserCommissions_InviterCommissionMatchesUnbindResponse 把
// 确认框上那个数与动作完成后回显的那个数**钉死成同一个**。
//
// 这两个数字来自两条不同的路由(列表 GET 与解绑 POST),运营在同一次操作里
// 会先后读到它们。它们必须逐位相等 —— 不等就是当着运营的面自相矛盾,而这正是
// 缺陷被发现的方式。共用 pairCommissionQuotas 一份实现就是为了让它们不可能不等。
//
// 回滚验证:让 attachInviterCommission 改用任何"看起来差不多"的口径
// (比如不排除 voided、或按 invitee 单列求和),本用例立刻变红。
func TestAdminListUserCommissions_InviterCommissionMatchesUnbindResponse(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	seedUserCommissionFixture(t, gdb, mainDB)

	items, _ := listUserCommissions(t, "sort=user&page_size=50")
	fromList := userRowById(t, items, 101).InviterCommissionQuota
	require.NotZero(t, fromList, "fixture 必须让这一对真的产生过佣金,否则 0 == 0 证明不了什么")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/unbind",
		unbindBody(101, "用户申诉,推广关系错绑"), adminUnbindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			KeptQuota int64 `json:"kept_commission_quota"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))

	assert.EqualValues(t, fromList, resp.Data.KeptQuota,
		"确认框上写的「会保留多少」与点完之后回显的「保留了多少」必须是同一个数")
}

// TestAdminListUserCommissions_InviterCommissionFollowsTheCurrentInviter 守的是
// 换过上线之后这一列指向谁。
//
// 换绑不动账本:老上线名下那些计佣行原样留着,只是从此不再产生新的。于是账本里
// 同一个 invitee 底下挂着**两个不同 inviter** 的计佣行。这一列问的是"**当前**
// 上线从他身上挣了多少",所以刚接手、还没挣到一分钱的新上线必须是 0。
//
// 把老上线那笔算到新上线头上的后果:运营接着对新上线点「移除」,确认框写
// 「会保留 880」,而实际留在新上线名下的是 0 —— 一个凭空多出来的数。
//
// 回滚验证:把 pairCommissionQuotas 里按 (inviter, invitee) 双列匹配那一段
// 退化成只按 invitee 匹配,本用例立刻变红(拿到 880)。这条正是先前那版测试
// 漏掉的形状 —— fixture 里没有人换过上线,双列与单列算出来一模一样。
func TestAdminListUserCommissions_InviterCommissionFollowsTheCurrentInviter(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	seedUser(t, mainDB, 300, "old-inviter", 0, 1000)
	seedUser(t, mainDB, 301, "new-inviter", 0, 1000)
	seedUser(t, mainDB, 302, "downline", 300, 2000)
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 300, 302
		a.GrossAmount = decimal.NewFromInt(880)
	})

	before, _ := listUserCommissions(t, "sort=user&page_size=50")
	assert.EqualValues(t, 880, userRowById(t, before, 302).InviterCommissionQuota,
		"换绑之前:老上线确实从他身上挣了 880")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/relations/rebind",
		rebindBody(302, 301, "原推广人离职,关系移交"), adminRebindRelation)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	after, _ := listUserCommissions(t, "sort=user&page_size=50")
	moved := userRowById(t, after, 302)
	require.Equal(t, 301, moved.InviterId, "权威字段应该已经指向新上线")
	assert.EqualValues(t, 0, moved.InviterCommissionQuota,
		"新上线还没从他身上挣到一分钱;把老上线那 880 算过来是凭空造出一个数")

	// 账本本身一个字节都没动:老上线那一对仍然是 880,只是不再是"当前"关系。
	assert.EqualValues(t, 880, pairCommissionQuota(t.Context(), 300, 302),
		"换绑不动账本 —— 老上线名下的计佣行必须原样保留")
}
