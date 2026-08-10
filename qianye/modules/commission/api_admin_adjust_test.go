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

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_adjust_test.go —— 手工增减佣金的回归网。
//
// 这个接口凭空加钱/扣钱,没有任何业务单据触发它。所以这里守的不是"算得对",
// 而是四件只有让数据库真跑一遍才看得见的事:
//
//  1. 它落成的是一条**账目行**,而不是把余额列改掉 —— 并且落账之后
//     Σaccrual / Σsettlement / balance 三者仍然自洽(上一轮实测里
//     "直接删计佣行"正是在这里破的);
//  2. 扣减有下界:超过可回收上限必须 400,而不是悄悄变成一笔冻结提现的欠账;
//  3. 幂等:同一个 client_request_id 重放三次,账本只动一次;
//  4. 参数级拒绝一个字节都不许改到库,而动过库的失败照样留痕。

func adjustBody(userId int, delta int64, reason, clientId string) string {
	return `{"user_id":` + strconv.Itoa(userId) +
		`,"delta_quota":` + strconv.FormatInt(delta, 10) +
		`,"reason":"` + reason + `"` +
		`,"client_request_id":"` + clientId + `"}`
}

func callAdjust(t *testing.T, body string) *adjustResponse {
	t.Helper()
	rec := callAdminHandler(t, http.MethodPost,
		"/api/qy/admin/commission/balances/adjust", body, adminAdjustCommission)
	out := &adjustResponse{Code: rec.Code, Body: rec.Body.String()}
	_ = common.Unmarshal(rec.Body.Bytes(), out)
	return out
}

type adjustResponse struct {
	Code int    `json:"-"`
	Body string `json:"-"`
	Data struct {
		DeltaQuota         int64  `json:"delta_quota"`
		Created            bool   `json:"created"`
		AccrualNo          string `json:"accrual_no"`
		ReclaimableCeiling int64  `json:"reclaimable_ceiling"`
	} `json:"data"`
}

func adjustAuditLogs(t *testing.T, gdb *gorm.DB) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", "commission.balance.manual_adjust").
		Order("id asc").Find(&rows).Error)
	return rows
}

func manualAccrualsOf(t *testing.T, gdb *gorm.DB, userId int) []Accrual {
	t.Helper()
	var rows []Accrual
	require.NoError(t, gdb.Where("inviter_id = ? AND source_type = ?", userId, SourceManual).
		Order("id asc").Find(&rows).Error)
	return rows
}

// assertCommissionConservation 是账目守恒断言,本文件所有"改钱"用例的收尾。
//
// 三条式子必须同时成立,它们互相咬合;任意一条破掉,余额行与结算流水就再也
// 对不回去(而这正是"直接 UPDATE 余额列 / 直接 DELETE 计佣行"的后果):
//
//	Σ accrual.settled_amount = Σ settlement(granted − reclaimed) + balance.unsettled_amount
//	total_earned − total_clawback = Σ settlement(granted − reclaimed)
//	available + frozen + withdrawn = total_earned − total_clawback
func assertCommissionConservation(t *testing.T, gdb *gorm.DB, userId int) {
	t.Helper()
	bal := balanceOf(t, gdb, userId)
	require.NotNil(t, bal, "余额行必须存在,否则谈不上守恒")

	var accruals []Accrual
	require.NoError(t, gdb.Where("inviter_id = ?", userId).Find(&accruals).Error)
	settledSum := decimal.Zero
	for _, a := range accruals {
		settledSum = settledSum.Add(a.SettledAmount)
	}

	netSettled := int64(0)
	for _, s := range settlementsOf(t, gdb, userId) {
		netSettled += s.GrantedQuota - s.ReclaimedQuota
	}

	assert.True(t,
		settledSum.Equal(decimal.NewFromInt(netSettled).Add(bal.UnsettledAmount)),
		"Σ已吸收计佣(%s)必须等于 Σ结算净额(%d)+ 未结算余数(%s)",
		settledSum.String(), netSettled, bal.UnsettledAmount.String())
	assert.Equal(t, netSettled, bal.TotalEarnedQuota-bal.TotalClawbackQuota,
		"累计已结算−累计冲正必须等于结算单的净额之和")
	assertLedgerIdentity(t, bal)
}

// TestAdminAdjust_IncreaseLandsAsAccrualNotAColumnEdit 是本接口的本体。
//
// 加钱必须落成一条可追溯的账目行(有单号、有理由、有操作人),再由既有的结算
// 流程吸收进余额 —— 而不是把 available_quota 直接加上去。
//
// 回滚验证:把 writeAccrualTx 那一段换成
// applyBalance(tx, userId, map[string]any{"available_quota": bal.AvailableQuota + delta}),
// 账目行断言与守恒断言同时变红(库里多了 5000 却没有任何流水解释它)。
func TestAdminAdjust_IncreaseLandsAsAccrualNotAColumnEdit(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 100, "inviter", 0, 1000)

	got := callAdjust(t, adjustBody(100, 5000, "客服补偿:漏算的推广佣金", "req-1"))
	require.Equal(t, http.StatusOK, got.Code, got.Body)
	assert.EqualValues(t, 5000, got.Data.DeltaQuota)
	assert.True(t, got.Data.Created)
	assert.NotEmpty(t, got.Data.AccrualNo)

	rows := manualAccrualsOf(t, gdb, 100)
	require.Len(t, rows, 1, "必须恰好落一条 manual 计佣行")
	a := rows[0]
	assert.Equal(t, SourceManual, a.IdemScope)
	assert.EqualValues(t, 0, a.InviteeId, "手工调整没有下线,invitee_id 必须是 0")
	assert.EqualValues(t, 0, a.RateUnits, "手工调整不按任何比例算出来")
	assert.EqualValues(t, 5000, a.BaseQuota, "BaseQuota 冻结「这次填了多少」,是幂等指纹")
	assert.True(t, a.GrossAmount.Equal(decimal.NewFromInt(5000)))
	assert.EqualValues(t, 0, a.MatureAt, "手工调整立即成熟,否则运营会以为接口没生效")
	assert.Contains(t, a.Remark, "漏算的推广佣金")

	bal := balanceOf(t, gdb, 100)
	require.NotNil(t, bal)
	assert.EqualValues(t, 5000, bal.AvailableQuota, "落账之后必须立刻结算进可提现")
	assert.EqualValues(t, 5000, bal.TotalEarnedQuota)
	require.Len(t, settlementsOf(t, gdb, 100), 1, "必须有一张结算单解释这 5000")
	assertCommissionConservation(t, gdb, 100)

	logs := adjustAuditLogs(t, gdb)
	require.Len(t, logs, 1)
	assert.Equal(t, qymodel.ResultOK, logs[0].Result)
	assert.Equal(t, 100, logs[0].TargetUserId)
	assert.Equal(t, 7, logs[0].ActorUserId)
	assert.EqualValues(t, 5000, logs[0].AmountQuota)
	assert.Equal(t, a.AccrualNo, logs[0].TraceNo, "审计与账本行要互相指得回去")
	assert.Contains(t, logs[0].Reason, "漏算的推广佣金")
	assert.Contains(t, logs[0].BeforeSnap, `"available_quota":0`)
	assert.Contains(t, logs[0].AfterSnap, `"available_quota":5000`)
}

// TestAdminAdjust_DecreaseIsBoundedByReclaimableCeiling 守下界。
//
// 没有这道校验时余额也不会变成负数(computeSettlement 会把回收额夹在可提现内),
// 但超出的部分会变成一笔**负余数**,也就是欠账 —— 欠账会冻结这个人的提现,
// 而运营完全不知道自己制造了它。
//
// 回滚验证:把 `if eventual < 0 { return errAdjustOverReclaimable }` 删掉,
// 越界那一次会返回 200,而 debt_blocked 断言变红。
func TestAdminAdjust_DecreaseIsBoundedByReclaimableCeiling(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 110, "inviter", 0, 1000)

	require.Equal(t, http.StatusOK,
		callAdjust(t, adjustBody(110, 5000, "补发历史佣金", "seed")).Code)
	require.EqualValues(t, 5000, balanceOf(t, gdb, 110).AvailableQuota)

	over := callAdjust(t, adjustBody(110, -5001, "扣回多发的佣金", "req-over"))
	require.Equal(t, http.StatusBadRequest, over.Code, over.Body)
	assert.Contains(t, over.Body, "qy_adj_over_reclaimable")

	after := balanceOf(t, gdb, 110)
	assert.EqualValues(t, 5000, after.AvailableQuota, "被拒的请求一个字节都不许改到库")
	assert.False(t, after.DebtBlocked, "越界的扣减绝不能变成一笔谁都没批准的欠账")
	assert.Len(t, manualAccrualsOf(t, gdb, 110), 1, "被拒时不许留下账目行")

	// 边界的另一侧:恰好等于上限必须放行,否则运营永远清不空一个人的佣金。
	ok := callAdjust(t, adjustBody(110, -5000, "扣回多发的佣金", "req-exact"))
	require.Equal(t, http.StatusOK, ok.Code, ok.Body)
	assert.EqualValues(t, -5000, ok.Data.DeltaQuota)

	after = balanceOf(t, gdb, 110)
	assert.EqualValues(t, 0, after.AvailableQuota)
	assert.EqualValues(t, 5000, after.TotalClawbackQuota)
	assert.False(t, after.DebtBlocked)
	assertCommissionConservation(t, gdb, 110)

	logs := adjustAuditLogs(t, gdb)
	require.Len(t, logs, 3, "越界被拒的那次同样要留痕")
	assert.Equal(t, qymodel.ResultFail, logs[1].Result)
	assert.EqualValues(t, 0, logs[1].AmountQuota, "失败路径账本上什么都没发生,金额必须是 0")
	assert.EqualValues(t, -5000, logs[2].AmountQuota, "扣减的审计金额带方向")
}

// TestAdminAdjust_CeilingCountsOnlyMaturedAccruals 守可回收上限的成熟度口径。
//
// 上限里可以算上"已成熟、尚未结算"的应发佣金:它与手工的负额行会进**同一批**
// 结算,能互相抵掉。**未成熟**的不行 —— 它进不了这一批,超出部分只能变成欠账。
//
// 回滚验证:把 reclaimableCeiling 里的 `all.Sub(unmatured)` 改回 `all`,
// 第二个子用例变红(未成熟的 900 被当成可扣,扣完立刻 debt_blocked)。
func TestAdminAdjust_CeilingCountsOnlyMaturedAccruals(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 120, "matured", 0, 1000)
	seedUser(t, mainDB, 121, "unmatured", 0, 1000)

	now := common.GetTimestamp()
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId, a.InviteeId = 120, 900
		a.GrossAmount = decimal.NewFromInt(800)
		a.MatureAt = now - 3600
	})
	seedAccrual(t, gdb, 2, func(a *Accrual) {
		a.InviterId, a.InviteeId = 121, 901
		a.GrossAmount = decimal.NewFromInt(900)
		a.MatureAt = now + 7200
	})

	t.Run("已成熟的待结算佣金算进可回收上限", func(t *testing.T) {
		got := callAdjust(t, adjustBody(120, -800, "撤销这一批待结算佣金", "m-1"))
		require.Equal(t, http.StatusOK, got.Code, got.Body)
		bal := balanceOf(t, gdb, 120)
		require.NotNil(t, bal)
		assert.EqualValues(t, 0, bal.AvailableQuota)
		assert.False(t, bal.DebtBlocked, "同一批里正负相抵,不该产生欠账")
		assertCommissionConservation(t, gdb, 120)
	})

	t.Run("未成熟的待结算佣金不算进可回收上限", func(t *testing.T) {
		got := callAdjust(t, adjustBody(121, -900, "撤销这一批待结算佣金", "u-1"))
		require.Equal(t, http.StatusBadRequest, got.Code, got.Body)
		assert.Contains(t, got.Body, "qy_adj_over_reclaimable")
		assert.Empty(t, manualAccrualsOf(t, gdb, 121))
	})
}

// TestAdminAdjust_IsIdempotentPerClientRequestId 守幂等。
//
// 加钱/减钱的接口没有幂等键时,一次网络超时重试就是第二笔,而多发出去的佣金
// 没有任何自动路径能收回来。
//
// 回滚验证:把幂等键里的 in.ClientId 换成 common.GetUUID(),本用例变红
// (三次调用落三条账目行、余额变成 15000)。
func TestAdminAdjust_IsIdempotentPerClientRequestId(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 130, "inviter", 0, 1000)

	body := adjustBody(130, 5000, "客服补偿", "same-request")
	for i := 0; i < 3; i++ {
		got := callAdjust(t, body)
		require.Equal(t, http.StatusOK, got.Code, got.Body)
		if i == 0 {
			assert.True(t, got.Data.Created)
			assert.EqualValues(t, 5000, got.Data.DeltaQuota)
		} else {
			assert.False(t, got.Data.Created, "重放不该再落一条账目行")
			assert.EqualValues(t, 0, got.Data.DeltaQuota)
		}
	}

	assert.Len(t, manualAccrualsOf(t, gdb, 130), 1)
	assert.EqualValues(t, 5000, balanceOf(t, gdb, 130).AvailableQuota)
	assertCommissionConservation(t, gdb, 130)

	logs := adjustAuditLogs(t, gdb)
	require.Len(t, logs, 3, "三次调用都要留痕")
	assert.EqualValues(t, 5000, logs[0].AmountQuota)
	assert.EqualValues(t, 0, logs[1].AmountQuota,
		"重放时账本上什么都没再发生,审计金额必须是 0 而不是请求里的 5000")
	assert.Contains(t, logs[1].Reason, "幂等重放")
}

// TestAdminAdjust_IdemKeyReusedWithOtherAmountConflicts 守幂等键被换参数重放。
//
// client_request_id 由前端在弹窗打开时生成并缓存,管理员改了金额再提交会复用
// 同一个键。回 200 就等于承认这次调整成功了,而账本上执行的是上一次的金额。
//
// 回滚验证:把指纹比对(BaseQuota / InviterId)那两行删掉,本用例变红(拿到 200)。
func TestAdminAdjust_IdemKeyReusedWithOtherAmountConflicts(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 140, "inviter", 0, 1000)

	require.Equal(t, http.StatusOK,
		callAdjust(t, adjustBody(140, 3000, "客服补偿", "dup-key")).Code)

	got := callAdjust(t, adjustBody(140, 9000, "客服补偿(改大了)", "dup-key"))
	require.Equal(t, http.StatusConflict, got.Code, got.Body)
	assert.Contains(t, got.Body, "qy_idem_key_conflict")

	assert.Len(t, manualAccrualsOf(t, gdb, 140), 1)
	assert.EqualValues(t, 3000, balanceOf(t, gdb, 140).AvailableQuota)
	assertCommissionConservation(t, gdb, 140)
}

// TestAdminAdjust_RejectsBadRequests 是参数闸门的表驱动。
//
// 全部发生在动库之前,所以都不该产生审计噪音,也不该建出余额行。
//
// 回滚验证:去掉任意一道校验,对应那一行变红。
func TestAdminAdjust_RejectsBadRequests(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 150, "inviter", 0, 1000)

	overMax := strconv.FormatInt(int64(common.MaxQuota)+1, 10)
	cases := []struct {
		name string
		body string
		code string
	}{
		{"零调整没有意义", adjustBody(150, 0, "随便写点理由", "c1"), "qy_invalid_param"},
		{"事由太短", adjustBody(150, 100, "补", "c2"), "qy_reason_required"},
		{"缺幂等键", adjustBody(150, 100, "客服补偿佣金", ""), "qy_invalid_param"},
		{"超过单次上限", `{"user_id":150,"delta_quota":` + overMax +
			`,"reason":"客服补偿佣金","client_request_id":"c3"}`, "qy_invalid_param"},
		{"负方向超过单次上限", `{"user_id":150,"delta_quota":-` + overMax +
			`,"reason":"客服补偿佣金","client_request_id":"c4"}`, "qy_invalid_param"},
		{"没给调整额度", `{"user_id":150,"reason":"客服补偿佣金","client_request_id":"c5"}`,
			"qy_invalid_param"},
		{"没给 user_id", `{"delta_quota":100,"reason":"客服补偿佣金","client_request_id":"c6"}`,
			"qy_invalid_param"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := callAdjust(t, tc.body)
			require.Equal(t, http.StatusBadRequest, got.Code, got.Body)
			assert.Contains(t, got.Body, tc.code)
		})
	}

	assert.Nil(t, balanceOf(t, gdb, 150), "参数级拒绝不该凭空建出一行余额")
	assert.Empty(t, adjustAuditLogs(t, gdb), "动库之前的拒绝不该产生审计噪音")
}

// TestAdminAdjust_RejectsUnknownUser 挡住手滑打错 user_id。
//
// lockBalance 是"不存在即建",少了这道校验就会在扩展库里凭空留下一行
// 永远没人认领的余额。
//
// 回滚验证:把 requireExistingUser 那一句删掉,余额行断言变红。
func TestAdminAdjust_RejectsUnknownUser(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	useMainDB(t, &model.User{})

	got := callAdjust(t, adjustBody(999, 100, "客服补偿佣金", "ghost"))
	require.Equal(t, http.StatusBadRequest, got.Code, got.Body)
	assert.Contains(t, got.Body, "qy_adj_user_not_found")
	assert.Nil(t, balanceOf(t, gdb, 999))

	logs := adjustAuditLogs(t, gdb)
	require.Len(t, logs, 1, "这一步已经进到业务层了,失败必须留痕")
	assert.Equal(t, qymodel.ResultFail, logs[0].Result)
}

// TestAdminAdjust_ConservationSurvivesMixedAdjustments 是守恒的压轴。
//
// 一串加减混合之后,Σ计佣 / Σ结算 / 余额三者仍然必须互相解释得通。
// 这正是"直接改余额列"或"直接删计佣行"会破掉的东西。
func TestAdminAdjust_ConservationSurvivesMixedAdjustments(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 160, "inviter", 0, 1000)

	steps := []struct {
		delta int64
		key   string
	}{{7000, "s1"}, {-2500, "s2"}, {1200, "s3"}, {-500, "s4"}}
	want := int64(0)
	for _, s := range steps {
		got := callAdjust(t, adjustBody(160, s.delta, "分批调整推广佣金", s.key))
		require.Equal(t, http.StatusOK, got.Code, got.Body)
		want += s.delta
		assertCommissionConservation(t, gdb, 160)
	}

	bal := balanceOf(t, gdb, 160)
	require.NotNil(t, bal)
	assert.EqualValues(t, want, bal.AvailableQuota, "四次调整的净额必须原样落在可提现上")
	assert.Len(t, manualAccrualsOf(t, gdb, 160), len(steps))
}

// TestAdminAdjust_MountPoints 是断链防护的 AST 一侧。
//
// 行为断言只能证明"路由在",证明不了"幂等判定、上下界校验与落账全在同一把
// 行锁与同一个事务里"—— 后者只有在并发下才会现形,而单元测试是单协程的。
//
// 回滚验证:把 lockBalance 换成裸 tx.Take、把 writeAccrualTx 换回 writeAccrual
// (它自取 db.Get(),那条 INSERT 就跑在锁外了)、或去掉 POST 的 crit 参数,
// 对应断言各自变红。
func TestAdminAdjust_MountPoints(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "api_admin_adjust.go", nil, 0)
	require.NoError(t, err)

	inTxCalls := map[string]bool{}
	postCritical := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		switch fn.Name.Name {
		case "applyManualAdjust":
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
		case "registerAdjustRoutes":
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
		"applyManualAdjust 必须在事务闭包里取余额行锁,否则与结算/提现冻结并发会丢更新")
	assert.True(t, inTxCalls["reclaimableCeiling"],
		"下界校验必须在同一把锁下,锁外算出来的上限在写入时可能已经不成立")
	assert.True(t, inTxCalls["writeAccrualTx"],
		"账目行必须用事务句柄写:writeAccrual 自取 db.Get(),那条 INSERT 会跑在锁外")
	assert.True(t, postCritical, "直接改钱的写接口必须挂 middleware.CriticalRateLimit()")

	modFile, err := parser.ParseFile(fset, "module.go", nil, 0)
	require.NoError(t, err)
	mounted := map[string]bool{}
	ast.Inspect(modFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			mounted[id.Name] = true
		}
		return true
	})
	assert.True(t, mounted["registerAdjustRoutes"])
	assert.True(t, mounted["registerRelationRoutes"])
}
