package commission

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_block_test.go —— 「停止计佣」到底是不是一个可逆开关。
//
// # 这批用例回答的是一个被当面提出来的质疑
//
// 项目方原话:「佣金审核这里是不是有点多余?停止计佣去把这个人的 aff 关系
// 解绑不就好了?而且停止计佣就没有办法恢复计算了。」
//
// 后半句在**代码上**不成立(adminBlockRelation 从一开始就收 `blocked bool`,
// 同一个接口既停又恢复),但没有任何一条测试证明"恢复之后真的会重新计佣" ——
// 而那正是这个质疑的要害。这条链路有三段各自能独立失效:
//
//	快照行的 blocked 列  →  blockedInvitees() 的进程内缓存  →  accrueConsume 的分支
//
// 中间那段缓存 60 秒。少一次 invalidateBlocked(),"停止"要等一分钟才生效、
// "恢复"同样要等一分钟 —— 运营在那一分钟里看到的正是"点了没用"。库里那一列
// 却是对的,所以只断言快照行的测试对这类缺陷完全无感。
//
// 因此这里一律**从 HTTP 处理器进**,再让 accrueConsume 真的写库、真的回读。
//
// # 三个日期桶不是凑数
//
// 消费返佣按 (下线, 自然日) 聚合成一行。三次消费落在三个不同的自然日上,
// 于是"停止期间那一笔有没有被补算"这个问题有了一个**可判定的形状**:
// 它要么是独立的第三行(补算了),要么根本不存在(没补算)。
// 挤在同一天里的话三笔会累加进同一行,只能靠金额反推,而金额相等的两种
// 解释(没计 vs 计了又被冲掉)在那一行上长得一模一样。
//
// 顺带避开 writeAccrual 的 Accumulate 分支 —— 那条分支的 RowsAffected 口径
// 在 MySQL 与 sqlite 上不同,testdb_test.go 明确说了不在这里覆盖。

// blockedRelationOf 回读一条关系此刻的 blocked 标记。关系行不存在时报错:
// 这批用例里"没有快照行"永远意味着前置步骤没做成,而不是一个待断言的取值。
func blockedRelationOf(t *testing.T, gdb *gorm.DB, inviteeId int) bool {
	t.Helper()
	rel := relationRowOf(t, gdb, inviteeId)
	require.NotNil(t, rel, "下线 %d 的关系快照行不存在", inviteeId)
	return rel.Blocked
}

// accrualsOfInviteeByDay 把某个下线的计佣行按自然日索引。
func accrualsOfInviteeByDay(t *testing.T, gdb *gorm.DB, inviteeId int) map[string]Accrual {
	t.Helper()
	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", inviteeId).Order("id asc").Find(&rows).Error)
	byDay := make(map[string]Accrual, len(rows))
	for _, row := range rows {
		byDay[row.BucketDate] = row
	}
	require.Len(t, byDay, len(rows), "同一个自然日出现了两行,日聚合键坏了")
	return byDay
}

// TestBlockStopsAccrualAndUnblockResumesIt 是这批用例的主干:
// 停 → 不计 → 恢复 → 重新计,而且停止期间那一笔**不补算**。
//
// 「不补算」是选定的语义,不是遗漏:停止计佣的意思就是"这段时间不算"。
// 如果恢复时把停止期间的消费补上,这个开关就没有任何风控意义 ——
// 拿它挡自刷的运营会在解封那一刻把挡掉的钱原样发出去。
//
// 回滚验证(逐条都试过):
//   - 删掉 accrueConsume 里的 blockedInvitees 分支 → 第 2 天那一行冒出来,
//     "停止期间不计佣"与"不补算"两条同时变红;
//   - 删掉 adminBlockRelation 里的 invalidateBlocked() → 第 2 天照样计佣
//     (旧快照还在 60 秒缓存里),"停止"那一半变红;
//   - 把恢复那一路改成不写库(例如让 setRelationBlocked 只接受 blocked=true)
//     → 第 3 天不再计佣,"恢复"那一半变红。
func TestBlockStopsAccrualAndUnblockResumesIt(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	const inviter, invitee = 71, 72
	now := common.GetTimestamp()
	seedUser(t, mainDB, inviter, "qy-inviter-71", 0, now-90*86400)
	seedUser(t, mainDB, invitee, "qy-downline-72", inviter, now-90*86400)

	// 三个自然日,从早到晚。bucketDate 按 UTC 取日,间隔一整天足够跨桶。
	dayBefore := now - 2*86400
	dayBlocked := now - 86400
	dayAfter := now

	toggle := func(blocked bool, reason string) {
		t.Helper()
		rec := callAdminHandler(t, http.MethodPost,
			"/api/qy/admin/commission/relations/block",
			blockBody(invitee, blocked, reason), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, blocked, blockedRelationOf(t, gdb, invitee))
	}

	ctx := context.Background()

	// ── 第 1 天:关系正常,消费 10000,按 5% 返 500。
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: invitee, Quota: 10000, At: dayBefore}))

	// ── 停止计佣,第 2 天再消费 10000。
	toggle(true, "疑似自刷,先停")
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: invitee, Quota: 10000, At: dayBlocked}))

	byDay := accrualsOfInviteeByDay(t, gdb, invitee)
	require.Len(t, byDay, 1, "停止计佣期间不该产生任何新的计佣行")
	assert.EqualValues(t, 10000, byDay[bucketDate(dayBefore)].BaseQuota)
	assert.Equal(t, "500", byDay[bucketDate(dayBefore)].GrossAmount.String(), "10000 × 5%")

	// ── 恢复计佣,第 3 天再消费 10000。
	toggle(false, "人工复核后放行")
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: invitee, Quota: 10000, At: dayAfter}))

	byDay = accrualsOfInviteeByDay(t, gdb, invitee)
	assert.Contains(t, byDay, bucketDate(dayAfter),
		"恢复之后必须重新计佣 —— 这正是项目方认为做不到的那件事")
	assert.EqualValues(t, 10000, byDay[bucketDate(dayAfter)].BaseQuota)
	assert.Equal(t, "500", byDay[bucketDate(dayAfter)].GrossAmount.String())

	assert.NotContains(t, byDay, bucketDate(dayBlocked),
		"停止期间那一笔消费不补算:恢复不是追认,它只对恢复之后的消费生效")
	require.Len(t, byDay, 2, "全程只该有第 1 天与第 3 天两行")
}

// TestBlockAuditReasonMatchesDirection 守审计正文不能两头都占。
//
// 实测拿到过这样一条记录:「解封邀请关系(邀请人 X):只停止未来计佣,已发放的
// 佣金不回收」—— 前半句说恢复、后半句说停止,因为两个方向共用同一句话。
// 审计正文是事后仲裁"这一刻到底发生了什么"的唯一凭据,自相矛盾等于没有凭据。
//
// 顺带把"停止期间不补算"这条语义也钉进正文:它是这个开关的选定语义,
// 而运营翻审计的时候正需要知道恢复到底追不追认。
//
// 回滚验证:让 blockOutcome 两个方向返回同一句 → 两个子断言各红一条。
func TestBlockAuditReasonMatchesDirection(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	now := common.GetTimestamp()
	seedUser(t, mainDB, 101, "qy-inviter-101", 0, now-90*86400)
	seedUser(t, mainDB, 102, "qy-downline-102", 101, now-90*86400)

	for _, blocked := range []bool{true, false} {
		rec := callAdminHandler(t, http.MethodPost,
			"/api/qy/admin/commission/relations/block",
			blockBody(102, blocked, "E2E 复核"), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	logs := relationAuditLogs(t, gdb, "commission.relation.block")
	require.Len(t, logs, 2)
	assert.Contains(t, logs[0].Reason, "只停止未来计佣",
		"停止那一次的正文必须说清它停的是什么")
	assert.NotContains(t, logs[1].Reason, "只停止未来计佣",
		"恢复那一次的正文不能还写着「只停止未来计佣」")
	assert.Contains(t, logs[1].Reason, "不补算",
		"恢复那一次必须写明停止期间不补算 —— 运营翻审计正是为了确认这件事")
}

// TestBlockedRelationSkipsEverySource 守住"停止计佣"覆盖全部四条计佣来源。
//
// 消费/任务补扣走 accrueConsume,充值/兑换码走 accrueOneShot —— 两条**独立**的
// 代码路径,各自有一处 blockedInvitees 判断。只在其中一条上加判断的后果是:
// 运营停掉了一个自刷账号,他的消费返佣确实停了,而充值返佣一分不少地继续发。
//
// 回滚验证:删掉 accrueOneShot 里的 blockedInvitees 分支 → 后两个子用例变红;
// 删掉 accrueConsume 里的 → 第一个变红。
func TestBlockedRelationSkipsEverySource(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	now := common.GetTimestamp()
	seedUser(t, mainDB, 81, "qy-inviter-81", 0, now-90*86400)

	cases := []struct {
		name      string
		inviteeId int
		accrue    func(ctx context.Context, inviteeId int) error
	}{
		{
			name:      "消费(含任务补扣)",
			inviteeId: 82,
			accrue: func(ctx context.Context, id int) error {
				return accrueConsume(ctx, consumeEvent{InviteeId: id, Quota: 10000, At: common.GetTimestamp()})
			},
		},
		{
			name:      "充值",
			inviteeId: 83,
			accrue: func(ctx context.Context, id int) error {
				return accrueOneShot(ctx, id, 10000, decimal.Zero, SourceTopup,
					topupIdemKey("TX-BLOCK-83"), "TX-BLOCK-83")
			},
		},
		{
			name:      "兑换码",
			inviteeId: 84,
			accrue: func(ctx context.Context, id int) error {
				return accrueOneShot(ctx, id, 10000, decimal.Zero, SourceRedemption,
					redemptionIdemKey(84), "RD84")
			},
		},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedUser(t, mainDB, tc.inviteeId, "qy-downline-"+itoa(tc.inviteeId), 81, now-90*86400)

			rec := callAdminHandler(t, http.MethodPost,
				"/api/qy/admin/commission/relations/block",
				blockBody(tc.inviteeId, true, "停止计佣"), adminBlockRelation)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			require.NoError(t, tc.accrue(ctx, tc.inviteeId))
			var count int64
			require.NoError(t, gdb.Model(&Accrual{}).
				Where("invitee_id = ?", tc.inviteeId).Count(&count).Error)
			assert.Zero(t, count, "这条来源绕过了停止计佣开关")
		})
	}
}

// TestBlockPreservesRelationWhileUnbindRemovesIt 把「停止计佣」与「解绑」的
// 区别钉死在数据上,而不是只写在文案里。
//
// 这两个动作在界面上挨着,后果完全不同,而项目方的疑问正是从这里长出来的:
//
//	停止计佣:users.inviter_id 原样保留 —— 关系还在,谁邀请了谁仍然查得到,
//	          随时可以恢复;
//	解绑:    users.inviter_id 被清零 —— 关系没了,从此查不到谁邀请了谁,
//	          再绑是一条新关系,统计口径断在这里。
//
// 两个动作都**不动账本**:历史计佣行一条不少。
//
// 回滚验证:让 setRelationBlocked 顺手把 users.inviter_id 清零(那正是
// "停止计佣就等于解绑"这个想法的代码形态)→ 第一个子用例立刻变红。
func TestBlockPreservesRelationWhileUnbindRemovesIt(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	now := common.GetTimestamp()
	seedUser(t, mainDB, 91, "qy-inviter-91", 0, now-90*86400)
	seedUser(t, mainDB, 92, "qy-blocked-92", 91, now-90*86400)
	seedUser(t, mainDB, 93, "qy-unbound-93", 91, now-90*86400)

	ctx := context.Background()
	for _, id := range []int{92, 93} {
		require.NoError(t, accrueConsume(ctx, consumeEvent{
			InviteeId: id, Quota: 10000, At: now - 2*86400,
		}))
	}

	t.Run("停止计佣保留邀请关系,可追溯也可恢复", func(t *testing.T) {
		rec := callAdminHandler(t, http.MethodPost,
			"/api/qy/admin/commission/relations/block",
			blockBody(92, true, "疑似自刷,先停"), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		assert.Equal(t, 91, inviterIdOf(t, mainDB, 92),
			"停止计佣绝不能动权威字段:关系一旦没了就再也答不出谁邀请了谁")
		assert.True(t, blockedRelationOf(t, gdb, 92))
		var count int64
		require.NoError(t, gdb.Model(&Accrual{}).Where("invitee_id = ?", 92).Count(&count).Error)
		assert.EqualValues(t, 1, count, "历史计佣行不该被停止计佣动到")
	})

	t.Run("解绑清掉权威字段,历史佣金仍然保留", func(t *testing.T) {
		rec := callAdminHandler(t, http.MethodPost,
			"/api/qy/admin/commission/relations/unbind",
			`{"invitee_id":93,"reason":"下线本人要求解除绑定"}`, adminUnbindRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		assert.Zero(t, inviterIdOf(t, mainDB, 93),
			"解绑必须清掉 users.inviter_id —— 这正是它与停止计佣的分界")
		var count int64
		require.NoError(t, gdb.Model(&Accrual{}).Where("invitee_id = ?", 93).Count(&count).Error)
		assert.EqualValues(t, 1, count, "解绑保留历史佣金,只是不再产生新的")
	})
}

// TestListRecordsCarriesRelationBlockedFlag 守计佣流水页的恢复入口。
//
// 这一位是那个入口的前提:列表不下发当前状态,页面就只能画一个单向的
// 「停止计佣」按钮(此前 admin-commission-records 正是写死 `blocked: true`),
// 于是"停了就再也恢复不了"变成了运营眼里的事实。
//
// 回滚验证:把 respond 改回下发裸 rows → relation_blocked 全部缺失,
// 第一个断言变红。
func TestListRecordsCarriesRelationBlockedFlag(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)

	now := common.GetTimestamp()
	// 101 被停,102 正常,manual 那一行不挂在任何关系上(invitee_id = 0)。
	seedAccrual(t, gdb, 1, func(a *Accrual) { a.InviterId = 100; a.InviteeId = 101 })
	seedAccrual(t, gdb, 2, func(a *Accrual) { a.InviterId = 100; a.InviteeId = 102 })
	seedAccrual(t, gdb, 3, func(a *Accrual) {
		a.InviterId = 100
		a.InviteeId = 0
		a.SourceType = SourceManual
		a.IdemScope = SourceManual
	})
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 101, InviterId: 100, InviteeRef: "ref101",
		Blocked: true, BoundAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 102, InviterId: 100, InviteeRef: "ref102",
		BoundAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)

	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/records?p=1&page_size=20", "", adminListRecords)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items []struct {
				InviteeId       int  `json:"invitee_id"`
				RelationBlocked bool `json:"relation_blocked"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))

	got := make(map[int]bool, len(resp.Data.Items))
	for _, it := range resp.Data.Items {
		got[it.InviteeId] = it.RelationBlocked
	}
	assert.Equal(t, map[int]bool{101: true, 102: false, 0: false}, got,
		"每一行都要带上它背后那条关系此刻的开关状态")
}

// TestBlockKeepsAutomaticRiskFlag —— 人工停/恢复计佣不许抹掉自动风控标记。
//
// # 这是一条真实缺陷的回归位
//
// setRelationBlocked 原来把管理员填的事由写进 qy_invite_relation.risk_flags,
// 而那一列是 ensureRelation 在建快照时写自动判定的地方(目前唯一的取值是
// reciprocal_invite:A 邀 B 且 B 又邀 A,最常见的双账号自刷手法)。
// 于是任何一次人工停止或恢复,都会把"这条关系是系统自动判定为互刷的"
// 这个事实覆盖成一句人话 —— AFF 关系页那个徽标从此显示的是运营自己写的字,
// 而**没有任何报错、没有任何别的地方还留着原值**(审计的 before 快照留了,
// 但那要翻审计才看得到)。
//
// 修法是把两件事分成两列:risk_flags 归自动风控,block_reason 归人工事由。
//
// 变异验证:把 setRelationBlocked 的 Updates 里的 "block_reason" 改回
// "risk_flags",本用例第一条断言立刻变红。
func TestBlockKeepsAutomaticRiskFlag(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
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

	t.Run("恢复计佣不抹掉自动标记", func(t *testing.T) {
		rec := callAdminHandler(t, http.MethodPost,
			"/api/qy/admin/commission/relations/block",
			blockBody(72, false, "核实为同一人的两台设备,先恢复"), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rel := relationRowOf(t, gdb, 72)
		require.NotNil(t, rel)
		assert.Equal(t, "reciprocal_invite", rel.RiskFlags,
			"自动风控标记必须原样留着 —— 人工事由不该顶掉「系统判定为互刷」这个事实")
		assert.Equal(t, "核实为同一人的两台设备,先恢复", rel.BlockReason,
			"人工事由落自己的列")
		assert.False(t, rel.Blocked)
	})

	t.Run("再次停止只覆盖人工事由那一列", func(t *testing.T) {
		rec := callAdminHandler(t, http.MethodPost,
			"/api/qy/admin/commission/relations/block",
			blockBody(72, true, "复核后仍判定自刷"), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rel := relationRowOf(t, gdb, 72)
		require.NotNil(t, rel)
		assert.Equal(t, "reciprocal_invite", rel.RiskFlags)
		assert.Equal(t, "复核后仍判定自刷", rel.BlockReason)
		assert.True(t, rel.Blocked)
	})
}

// TestBlockReasonEmptyMeansNeverAsked 钉住新列的零值语义。
//
// block_reason 的空串 = **没填过事由**,不是"事由是空的",更不是任何一种
// 计佣开关状态。它与 blocked 完全正交:恢复计佣时事由照样留下(为什么恢复
// 与为什么停同样要查),所以不能拿"有没有事由"去推断"停没停"。
func TestBlockReasonEmptyMeansNeverAsked(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})

	now := common.GetTimestamp()
	seedUser(t, mainDB, 81, "up", 0, now-86400)
	seedUser(t, mainDB, 82, "down", 81, now-86400)

	// 从没被人工动过的关系:两列都是空。
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: 82, InviterId: 81, InviteeRef: "ref82",
		BoundAt: now - 86400, CreatedAt: now, UpdatedAt: now,
	}).Error)
	rel := relationRowOf(t, gdb, 82)
	require.NotNil(t, rel)
	assert.Equal(t, "", rel.BlockReason)
	assert.False(t, rel.Blocked)

	// 停一次、再恢复一次:blocked 回到 false,事由留下最后那一句。
	for _, step := range []struct {
		blocked bool
		reason  string
	}{{true, "先停"}, {false, "查清了,恢复"}} {
		rec := callAdminHandler(t, http.MethodPost,
			"/api/qy/admin/commission/relations/block",
			blockBody(82, step.blocked, step.reason), adminBlockRelation)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rel := relationRowOf(t, gdb, 82)
		require.NotNil(t, rel)
		assert.Equal(t, step.blocked, rel.Blocked)
		assert.Equal(t, step.reason, rel.BlockReason)
	}
	assert.False(t, blockedInvitees(t.Context())[82],
		"恢复之后计佣链路读到的集合里不该还有它")
}
