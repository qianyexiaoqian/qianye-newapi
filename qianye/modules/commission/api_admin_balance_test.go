package commission

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// api_admin_balance_test.go —— 佣金余额总览与「已提现」迁移编辑的回归网。
//
// 这个接口没有任何业务单据兜底:它直接改 qy_commission_balance 的两列。
// 因此这里守的不是"函数算得对",而是四件只有让数据库与 HTTP 层真跑一遍
// 才看得见的事:
//
//  1. 恒等式 可提现+冻结+已提现 = 已结算−已冲正 在改动前后都成立;
//  2. 绝对值语义 —— 重复提交必须是空操作,而不是把额度扣两遍;
//  3. 越界与缺事由的请求**一个字节都不许改到库**,且失败照样留痕;
//  4. 挂载点真的接上了 —— 处理器写对了却没挂路由、没挂行锁、没挂关键限流,
//     是本仓反复出现的"断链"形状,行为断言与 AST 调用点断言必须同时有。

// setWithdrawnBody 拼一个迁移编辑请求体。
func setWithdrawnBody(userId int, target int64, reason string) string {
	return `{"user_id":` + strconv.Itoa(userId) +
		`,"withdrawn_quota":` + strconv.FormatInt(target, 10) +
		`,"reason":"` + reason + `"}`
}

// seedLedgerBalance 插入一行**恒等式成立**的佣金余额。
//
// 刻意让调用方只给 available/frozen/withdrawn 三个数,total_earned 由它们相加
// 得到:测试数据本身就违反恒等式的话,后面所有关于恒等式的断言都是自证。
func seedLedgerBalance(t *testing.T, gdb *gorm.DB, userId int, available, frozen, withdrawn int64, fiat string) *Balance {
	t.Helper()
	now := common.GetTimestamp()
	b := &Balance{
		UserId:           userId,
		UnsettledAmount:  decimal.Zero,
		AvailableQuota:   available,
		FrozenQuota:      frozen,
		WithdrawnQuota:   withdrawn,
		TotalEarnedQuota: available + frozen + withdrawn,
		AvailableFiat:    decimal.RequireFromString(fiat),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, gdb.Create(b).Error)
	return b
}

// seedMainUserFor 往主库插一个普通账号,让操作人闸门(guard.ActorMayActOn)
// 能回查到目标角色。
//
// 迁移编辑是本模块四条动钱接口之一,它必须先回答"操作人能不能改这个人的钱"。
// 测试里的操作人固定是 id=7 / role=10(见 callAdminHandler),所以目标只要是
// 一个存在的、角色更低的账号就能走完闸门 —— 闸门本身的真值表在
// TestAdminSetWithdrawn_RefusesSelfAndPeerTargets 里单独钉。
func seedMainUserFor(t *testing.T, userId int) {
	t.Helper()
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, userId, "u"+strconv.Itoa(userId), 0, 1000)
}

// assertLedgerIdentity 断言恒等式在这一行上成立。
func assertLedgerIdentity(t *testing.T, b *Balance) {
	t.Helper()
	require.NotNil(t, b)
	assert.Equal(t, b.TotalEarnedQuota-b.TotalClawbackQuota,
		b.AvailableQuota+b.FrozenQuota+b.WithdrawnQuota,
		"可提现+冻结中+已提现 必须恒等于 已结算−已冲正,破了这条账本就与结算流水对不回去了")
}

func withdrawnAuditLogs(t *testing.T, gdb *gorm.DB) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", "commission.balance.withdrawn.set").
		Order("id asc").Find(&rows).Error)
	return rows
}

// TestAdminSetWithdrawn_MovesQuotaFromAvailable 是本接口的本体。
//
// 迁移的语义是"这笔钱在旧系统已经付过了",所以它必须**从可提现里划走**。
// 只加 withdrawn 不减 available 的实现同样能让列表页显示对,但用户仍然能把
// 同一笔佣金在这里再提一次 —— 正是项目方要避免的那件事。
//
// 回滚验证:把 "available_quota": newAvail 这一行从 applyBalance 的 map 里删掉,
// 本测试的可提现断言与恒等式断言同时变红。
func TestAdminSetWithdrawn_MovesQuotaFromAvailable(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	seedMainUserFor(t, 42)
	seedLedgerBalance(t, gdb, 42, 8000, 1000, 500, "0")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
		setWithdrawnBody(42, 3500, "迁移旧佣金系统历史提现"), adminSetWithdrawn)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	after := balanceOf(t, gdb, 42)
	require.NotNil(t, after)
	assert.EqualValues(t, 3500, after.WithdrawnQuota, "已提现必须被设成请求里的绝对值")
	assert.EqualValues(t, 8000-3000, after.AvailableQuota,
		"差额 3000 必须从可提现里划走,否则用户能把旧系统已付的佣金再提一次")
	assert.EqualValues(t, 1000, after.FrozenQuota, "冻结中不该被这个接口碰到")
	assert.EqualValues(t, 9500, after.TotalEarnedQuota, "累计已结算不是这个接口能改的")
	assertLedgerIdentity(t, after)

	var resp struct {
		Data struct {
			DeltaQuota int64 `json:"delta_quota"`
			After      struct {
				AvailableQuota int64 `json:"available_quota"`
				LedgerDrift    int64 `json:"ledger_drift"`
			} `json:"after"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	assert.EqualValues(t, 3000, resp.Data.DeltaQuota)
	assert.EqualValues(t, 5000, resp.Data.After.AvailableQuota)
	assert.EqualValues(t, 0, resp.Data.After.LedgerDrift)
}

// TestAdminSetWithdrawn_IsAbsoluteAndIdempotent 守"绝对值"这个语义选择。
//
// 迁移是"这个人历史上已经领过 X",不是"再加 X"。增量语义下,一次网络重试或者
// 运营手滑双击就会把可提现扣两遍,而扣掉的额度没有任何自动路径能还回来。
//
// 回滚验证:把 delta := target - bal.WithdrawnQuota 改成 delta := target
// (增量语义),第二次提交后的断言全部变红。
func TestAdminSetWithdrawn_IsAbsoluteAndIdempotent(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	// 刻意不用 7:那是 callAdminHandler 里操作人自己的 id,操作人闸门会先拒。
	seedMainUserFor(t, 8)
	seedLedgerBalance(t, gdb, 8, 9000, 0, 0, "0")

	body := setWithdrawnBody(8, 4000, "迁移旧系统已付佣金")
	for i := 0; i < 3; i++ {
		rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
			body, adminSetWithdrawn)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	after := balanceOf(t, gdb, 8)
	assert.EqualValues(t, 4000, after.WithdrawnQuota, "重复提交同一个绝对值必须是空操作")
	assert.EqualValues(t, 5000, after.AvailableQuota, "可提现只能被划走一次")
	assertLedgerIdentity(t, after)

	logs := withdrawnAuditLogs(t, gdb)
	require.Len(t, logs, 3, "三次调用都要留痕:第二三次是「运营确认过这个数」,同样是事实")
	assert.EqualValues(t, 4000, logs[0].AmountQuota, "第一次的审计金额是真实划转额")
	assert.EqualValues(t, 0, logs[1].AmountQuota,
		"重复提交时资金侧什么都没发生,审计金额必须是 0 而不是目标值")
	assert.EqualValues(t, 0, logs[2].AmountQuota)
}

// TestAdminSetWithdrawn_RejectsOverAvailable 守住"可提现绝不能被改成负数"。
//
// 回滚验证:删掉 `if delta > bal.AvailableQuota` 这道校验,
// 可提现会变成 -1,余额零变化断言与恒等式断言同时变红。
func TestAdminSetWithdrawn_RejectsOverAvailable(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	seedMainUserFor(t, 9)
	seedLedgerBalance(t, gdb, 9, 1000, 0, 200, "0")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
		setWithdrawnBody(9, 1201, "迁移旧系统数据"), adminSetWithdrawn)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qy_withdrawn_over_available")

	after := balanceOf(t, gdb, 9)
	assert.EqualValues(t, 200, after.WithdrawnQuota, "被拒的请求一个字节都不许改到库")
	assert.EqualValues(t, 1000, after.AvailableQuota)
	assertLedgerIdentity(t, after)

	logs := withdrawnAuditLogs(t, gdb)
	require.Len(t, logs, 1, "失败必须留痕:运营看到 400 会换个数再试,没有这条就分不清哪次生效了")
	assert.Equal(t, qymodel.ResultFail, logs[0].Result)
	assert.EqualValues(t, 0, logs[0].AmountQuota, "失败路径资金侧没发生任何事,金额必须是 0")

	// 边界的另一侧:恰好等于「已提现+可提现」必须放行,否则运营永远清不空一个人的可提现。
	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
		setWithdrawnBody(9, 1200, "迁移旧系统数据"), adminSetWithdrawn)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	after = balanceOf(t, gdb, 9)
	assert.EqualValues(t, 0, after.AvailableQuota)
	assertLedgerIdentity(t, after)
}

// TestAdminSetWithdrawn_RequiresReason 守强制事由(与 withdraw 看 PII 明文同口径)。
//
// 回滚验证:把 `< 4` 改成 `< 0`,两个子用例同时变红。
func TestAdminSetWithdrawn_RequiresReason(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	seedMainUserFor(t, 11)
	seedLedgerBalance(t, gdb, 11, 5000, 0, 0, "0")

	for _, reason := range []string{"", "迁移", "   x  "} {
		rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
			setWithdrawnBody(11, 100, reason), adminSetWithdrawn)
		require.Equal(t, http.StatusBadRequest, rec.Code, "事由 %q 必须被拒", reason)
		assert.Contains(t, rec.Body.String(), "qy_reason_required")
	}
	assert.EqualValues(t, 0, balanceOf(t, gdb, 11).WithdrawnQuota)
	assert.Empty(t, withdrawnAuditLogs(t, gdb),
		"参数级拒绝发生在动库之前,不该产生审计噪音")
}

// TestAdminSetWithdrawn_LowersBackToAvailable 守回退方向。
//
// 运营把 5000 填成 50000 之后必须能改回来,否则那笔可提现就永久卡住了。
//
// 回滚验证:把 delta<0 时的分支去掉(例如加一句 `if delta < 0 { return err }`),
// 本测试变红。
func TestAdminSetWithdrawn_LowersBackToAvailable(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	seedMainUserFor(t, 21)
	seedLedgerBalance(t, gdb, 21, 6000, 0, 0, "0")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
		setWithdrawnBody(21, 6000, "迁移旧系统已付佣金"), adminSetWithdrawn)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.EqualValues(t, 0, balanceOf(t, gdb, 21).AvailableQuota)

	rec = callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
		setWithdrawnBody(21, 1500, "填错一位数,更正为 1500"), adminSetWithdrawn)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	after := balanceOf(t, gdb, 21)
	assert.EqualValues(t, 1500, after.WithdrawnQuota)
	assert.EqualValues(t, 4500, after.AvailableQuota, "多登记的 4500 必须还回可提现")
	assertLedgerIdentity(t, after)
}

// TestAdminSetWithdrawn_ScalesAvailableFiat 守法币与额度不许漂移。
//
// available_fiat 是按计佣当刻的**冻结汇率**折算的,提现模块直接读它。
// 只改额度不改法币,剩余额度对应的平均汇率就被拉偏,用户实际拿到的钱会算错。
//
// 回滚验证:把 "available_fiat" 这一行从 applyBalance 的 map 里删掉,本测试变红
// (法币仍是 100,而额度只剩四分之一)。
func TestAdminSetWithdrawn_ScalesAvailableFiat(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	seedMainUserFor(t, 31)
	seedLedgerBalance(t, gdb, 31, 8000, 0, 0, "100")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
		setWithdrawnBody(31, 6000, "迁移旧系统已付佣金"), adminSetWithdrawn)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	after := balanceOf(t, gdb, 31)
	assert.EqualValues(t, 2000, after.AvailableQuota)
	assert.True(t, after.AvailableFiat.Equal(decimal.RequireFromString("25")),
		"法币必须按额度比例同步缩到 25,实际 %s", after.AvailableFiat.String())
}

// TestAdminSetWithdrawn_AuditCarriesSnapshots 守审计快照。
//
// 这个接口没有任何业务单据,审计是它唯一的事后凭据 —— 快照里必须同时能看到
// 改动前后的四个额度列,否则"这个人的可提现为什么少了 3000"无从回答。
//
// 回滚验证:把 BeforeSnap/AfterSnap 两个字段从 writeWithdrawnAudit 里删掉,本测试变红。
func TestAdminSetWithdrawn_AuditCarriesSnapshots(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	seedMainUserFor(t, 55)
	seedLedgerBalance(t, gdb, 55, 7000, 0, 250, "0")

	rec := callAdminHandler(t, http.MethodPost, "/api/qy/admin/commission/balances/withdrawn",
		setWithdrawnBody(55, 4250, "迁移:旧系统已付 4250"), adminSetWithdrawn)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	logs := withdrawnAuditLogs(t, gdb)
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, 55, logs[0].TargetUserId)
	assert.Equal(t, 7, logs[0].ActorUserId, "操作人必须落到审计上")
	assert.EqualValues(t, 4000, logs[0].AmountQuota, "审计金额是真实划转额,不是请求里的目标值")
	assert.Contains(t, logs[0].Reason, "旧系统已付 4250")
	assert.Contains(t, logs[0].BeforeSnap, `"withdrawn_quota":250`)
	assert.Contains(t, logs[0].BeforeSnap, `"available_quota":7000`)
	assert.Contains(t, logs[0].AfterSnap, `"withdrawn_quota":4250`)
	assert.Contains(t, logs[0].AfterSnap, `"available_quota":3000`)
}

// TestAdminListBalances_ShowsDerivedAvailableAndDrift 守列表页把恒等式摊开。
//
// 只给一个"可提现"数字的话,"有佣金却提不了"永远要靠翻代码回答;
// 而 ledger_drift 是唯一能让运营在改钱**之前**发现这行账本已经坏了的信号。
//
// 回滚验证:把 DerivedAvailable/LedgerDrift 的赋值改成常数 0,drift 断言变红。
func TestAdminListBalances_ShowsDerivedAvailableAndDrift(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	useMainDB(t, &model.User{})

	seedLedgerBalance(t, gdb, 101, 3000, 500, 1500, "0")
	// 人为制造一行漂移的账本:已结算比三项之和多 777。
	drifted := seedLedgerBalance(t, gdb, 102, 1000, 0, 0, "0")
	require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", drifted.UserId).
		Update("total_earned_quota", 1777).Error)

	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/balances?sort=user", "", adminListBalances)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Data struct {
			Items  []balanceView `json:"items"`
			Total  int64         `json:"total"`
			Totals struct {
				AvailableQuota int64 `json:"available_quota"`
				WithdrawnQuota int64 `json:"withdrawn_quota"`
			} `json:"totals"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Items, 2)
	assert.EqualValues(t, 2, resp.Data.Total)

	healthy := resp.Data.Items[0]
	assert.Equal(t, 101, healthy.UserId)
	assert.EqualValues(t, 3000, healthy.DerivedAvailable, "5000−0−500−1500 = 3000")
	assert.EqualValues(t, 0, healthy.LedgerDrift)

	bad := resp.Data.Items[1]
	assert.Equal(t, 102, bad.UserId)
	assert.EqualValues(t, 1777, bad.DerivedAvailable)
	assert.EqualValues(t, -777, bad.LedgerDrift,
		"账本漂移必须在列表上直接可见,不能等到运营改完钱才发现")

	assert.EqualValues(t, 4000, resp.Data.Totals.AvailableQuota, "合计要跟着同一组筛选走")
	assert.EqualValues(t, 1500, resp.Data.Totals.WithdrawnQuota)
}

// TestAdminListBalances_UnknownUsernameReturnsEmptyPage 守一条最危险的静默失效。
//
// 用户名查无此人时若把筛选条件直接丢掉,返回的是**未经筛选的全表**,
// 而它看起来与"这个人排在第一页"一模一样 —— 迁移正是照着第一行改钱的。
//
// 回滚验证:把查无此人那一支改成 `// 忽略这个条件`,本测试变红(拿到 2 条)。
func TestAdminListBalances_UnknownUsernameReturnsEmptyPage(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	mainDB := useMainDB(t, &model.User{})
	require.NoError(t, mainDB.Create(&model.User{Id: 201, Username: "alice"}).Error)

	seedLedgerBalance(t, gdb, 201, 1000, 0, 0, "0")
	seedLedgerBalance(t, gdb, 202, 2000, 0, 0, "0")

	var resp struct {
		Data struct {
			Items  []balanceView `json:"items"`
			Total  int64         `json:"total"`
			Totals struct {
				AvailableQuota int64 `json:"available_quota"`
			} `json:"totals"`
		} `json:"data"`
	}

	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/balances?username=nobody", "", adminListBalances)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data.Items, "查无此人必须回空页,绝不能退化成全表")
	assert.EqualValues(t, 0, resp.Data.Total)

	rec = callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/balances?username=alice", "", adminListBalances)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Items, 1)
	assert.Equal(t, 201, resp.Data.Items[0].UserId)
	assert.Equal(t, "alice", resp.Data.Items[0].Username, "用户名必须回主库补上")
	assert.True(t, resp.Data.Items[0].UserResolved)
	// 同一个 *gorm.DB 上连着跑 Count / SUM / Find 三个终结方法,
	// 条件或 Select 一旦互相污染,合计就会变成"全表合计"或者行里少几列。
	assert.EqualValues(t, 1, resp.Data.Total)
	assert.EqualValues(t, 1000, resp.Data.Totals.AvailableQuota,
		"合计必须只统计筛出来的那一个人,不能退化成全表 3000")
	assert.EqualValues(t, 1000, resp.Data.Items[0].AvailableQuota,
		"聚合查询的 Select 不许漏进列表查询,否则整行只剩两列")
}

// TestAdminListBalances_SortIsStableWhenAmountsTie 守翻页不漏人。
//
// 金额相同的行在没有次级排序时顺序是不确定的,翻页会漏行也会重复行 ——
// 迁移时对着列表逐个改,漏掉的那个人就永远少扣一笔。
//
// 回滚验证:把 balanceSortOrders 里的 `, user_id asc` 全部删掉,
// 本测试在 MySQL 上变红(sqlite 恰好稳定,所以这条断言在本地是弱的,
// 真正的守护来自 balanceSortOrders 的字面量断言)。
func TestAdminListBalances_SortIsStableWhenAmountsTie(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)
	useMainDB(t, &model.User{})

	for _, id := range []int{301, 302, 303, 304} {
		seedLedgerBalance(t, gdb, id, 5000, 0, 0, "0")
	}

	seen := map[int]bool{}
	for page := 1; page <= 2; page++ {
		rec := callAdminHandler(t, http.MethodGet,
			"/api/qy/admin/commission/balances?p="+strconv.Itoa(page)+"&page_size=2", "", adminListBalances)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp struct {
			Data struct {
				Items []balanceView `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
		for _, it := range resp.Data.Items {
			assert.False(t, seen[it.UserId], "用户 %d 在两页里都出现了", it.UserId)
			seen[it.UserId] = true
		}
	}
	assert.Len(t, seen, 4, "四个人必须恰好各出现一次")

	for name, order := range balanceSortOrders {
		if name == "user" {
			continue
		}
		assert.Contains(t, order, "user_id asc",
			"排序 %q 缺少次级排序键,金额打平时翻页会漏人", name)
	}
}

// TestAdminBalanceRoutes_AreMounted 是断链防护的行为一侧。
//
// 处理器写对了却从没挂上路由,是本仓反复出现的形状:所有单元测试照样全绿,
// 而线上那个页面 404。
//
// 回滚验证:把 module.go 里的 registerBalanceRoutes(g, crit) 删掉,本测试变红。
func TestAdminBalanceRoutes_AreMounted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mod{}.RegisterAdminRoutes(engine.Group("/api/qy/admin"))

	want := map[string]string{
		"GET":  "/api/qy/admin/commission/balances",
		"POST": "/api/qy/admin/commission/balances/withdrawn",
	}
	got := map[string]string{}
	for _, r := range engine.Routes() {
		for method, path := range want {
			if r.Method == method && r.Path == path {
				got[method] = path
			}
		}
	}
	assert.Equal(t, want, got, "余额总览与迁移编辑两条路由必须真的挂上去")
}

// TestAdminSetWithdrawn_MountPoints 是断链防护的 AST 一侧。
//
// 行为断言只能证明"路由在",证明不了"读改写全在同一把行锁与同一个事务里"——
// 后者只有在并发下才会现形,而单元测试是单协程的。所以这里直接钉调用点:
//
//   - adminSetWithdrawn 必须在一个 .Transaction(...) 的闭包里同时调
//     lockBalance(取行锁)与 applyBalance(回写);
//   - registerBalanceRoutes 注册写接口时必须把 crit(关键操作限流)传进去。
//
// 回滚验证:把 lockBalance 换成裸 tx.Take、或把闭包里的写回挪到事务外、
// 或去掉 POST 的 crit 参数,对应断言各自变红。
func TestAdminSetWithdrawn_MountPoints(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "api_admin_balance.go", nil, 0)
	require.NoError(t, err)

	inTxCalls := map[string]bool{}
	postCritical := false

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		switch fn.Name.Name {
		case "adminSetWithdrawn":
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isSelectorCall(call, "Transaction") {
					return true
				}
				for _, arg := range call.Args {
					lit, ok := arg.(*ast.FuncLit)
					if !ok {
						continue
					}
					ast.Inspect(lit.Body, func(inner ast.Node) bool {
						if c, ok := inner.(*ast.CallExpr); ok {
							if id, ok := c.Fun.(*ast.Ident); ok {
								inTxCalls[id.Name] = true
							}
						}
						return true
					})
				}
				return true
			})
		case "registerBalanceRoutes":
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isSelectorCall(call, "POST") {
					return true
				}
				for _, arg := range call.Args {
					if id, ok := arg.(*ast.Ident); ok && id.Name == "crit" {
						postCritical = true
					}
				}
				return true
			})
		}
	}

	assert.True(t, inTxCalls["lockBalance"],
		"adminSetWithdrawn 必须在事务闭包里调 lockBalance 取行锁,否则与结算任务并发会丢更新")
	assert.True(t, inTxCalls["applyBalance"],
		"回写必须与取锁在同一个事务闭包内,读到的余额才是写回时的那一份")
	assert.True(t, postCritical,
		"直接改钱的写接口必须挂 middleware.CriticalRateLimit()")

	modFile, err := parser.ParseFile(fset, "module.go", nil, 0)
	require.NoError(t, err)
	mounted := false
	ast.Inspect(modFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "registerBalanceRoutes" {
			mounted = true
		}
		return true
	})
	assert.True(t, mounted, "registerBalanceRoutes 必须被 RegisterAdminRoutes 真的调到")
}

// isSelectorCall 判断 call 是不是形如 x.Name(...) 的调用。
func isSelectorCall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name
}
