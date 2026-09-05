package transfer

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zz_audit_risk_gates_test.go —— 资金安全审计探针:风控闸门的边界值 + 验密闸门的失败方向。
//
// 风控闸门的每一条都写成 `cfg.X > 0 && ...`,也就是说任何一个把配置读成 0 /
// 负数的路径都会让那道闸门**静默消失**。这里从动钱入口逐条打边界:
// 恰好等于上限必须放行,超一分必须拒,而且拒的时候钱一分没动。

// TestAuditDailyQuotaGateIsExactAtTheMoneyEntrance 钉住日额度的边界。
//
// 判据是 sender.DayOutQuota + (amount+fee) > daily_max_quota。手续费**计入**
// 日额度,所以边界要连着手续费一起算 —— 只按 amount 算的实现会在这里多放行
// 一笔手续费的量。
func TestAuditDailyQuotaGateIsExactAtTheMoneyEntrance(t *testing.T) {
	cfg := auditWideOpenGlobal()
	cfg.FeeBps = 100 // 1%
	cfg.DailyMaxQuota = 2_020_000
	// 两笔 1,000,000 各带 10,000 手续费,合计恰好 2,020,000。

	_, mainDB := auditEnv(t, cfg, map[int]int{1: 90_000_000, 2: 0})

	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "audit-daily-1",
	}))
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "audit-daily-2",
	}), "恰好用满日额度的那一笔必须放行")

	before := quotaOf(t, mainDB, 1)
	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "audit-daily-3",
	})
	require.Error(t, err)
	assert.Same(t, errDailyLimitExceeded, err)
	assert.Equal(t, before, quotaOf(t, mainDB, 1), "撞日额度的那一笔不得扣钱")
	assert.Equal(t, 2_000_000, quotaOf(t, mainDB, 2))

	// 超一分同样要拒:上一笔已经用满,这里再打一个最小金额验证边界不是"约等于"。
	err = callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1, Confirm: true, ClientRequestId: "audit-daily-4",
	})
	require.Error(t, err)
	assert.Same(t, errDailyLimitExceeded, err)
	assert.Equal(t, before, quotaOf(t, mainDB, 1))
}

// TestAuditDailyCountGateIsExactAtTheMoneyEntrance 钉住日笔数的边界。
func TestAuditDailyCountGateIsExactAtTheMoneyEntrance(t *testing.T) {
	cfg := auditWideOpenGlobal()
	cfg.DailyMaxCount = 2

	_, mainDB := auditEnv(t, cfg, map[int]int{1: 90_000_000, 2: 0})
	for i := 1; i <= 2; i++ {
		require.NoError(t, callCreate(t, 1, createRequest{
			ToUserId: 2, Amount: 1_000, Confirm: true,
			ClientRequestId: "audit-count-" + strconv.Itoa(i),
		}), "第 %d 笔应当放行", i)
	}
	before := quotaOf(t, mainDB, 1)

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000, Confirm: true, ClientRequestId: "audit-count-3",
	})
	require.Error(t, err)
	assert.Same(t, errDailyCountExceeded, err)
	assert.Equal(t, before, quotaOf(t, mainDB, 1), "撞日笔数的那一笔不得扣钱")
	assert.Equal(t, 2_000, quotaOf(t, mainDB, 2))
}

// TestAuditCooldownGateBlocksTheSecondTransferAtTheMoneyEntrance 钉住冷却。
//
// 冷却是唯一一条只看时间、不看金额的闸门,也是最容易被"改成只在预览里判"
// 的那一条 —— 预览不动钱,判在那里等于没判。
func TestAuditCooldownGateBlocksTheSecondTransferAtTheMoneyEntrance(t *testing.T) {
	cfg := auditWideOpenGlobal()
	cfg.CooldownSecs = 3600

	_, mainDB := auditEnv(t, cfg, map[int]int{1: 90_000_000, 2: 0})
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000, Confirm: true, ClientRequestId: "audit-cd-1",
	}))
	before := quotaOf(t, mainDB, 1)

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000, Confirm: true, ClientRequestId: "audit-cd-2",
	})
	require.Error(t, err)
	assert.Same(t, errCooldown, err)
	assert.Equal(t, before, quotaOf(t, mainDB, 1), "冷却期内的那一笔不得扣钱")
	assert.Equal(t, 1_000, quotaOf(t, mainDB, 2))
}

// TestAuditNewAccountFreezeBlocksAtTheMoneyEntrance 钉住新账号冻结期。
//
// 这是唯一一道判据取自 users.created_at(攻击者改不了)的反滥用闸门。
// 批量注册的小号是零成本套现的入口,它一旦只在预览里判,就等于没有。
func TestAuditNewAccountFreezeBlocksAtTheMoneyEntrance(t *testing.T) {
	cfg := auditWideOpenGlobal()
	cfg.NewAccountFreezeHours = 24

	_, mainDB := auditEnv(t, cfg, map[int]int{1: 90_000_000, 2: 0})
	now := common.GetTimestamp()
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
		Update("created_at", now-3600).Error) // 注册 1 小时

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000, Confirm: true, ClientRequestId: "audit-freeze-1",
	})
	require.Error(t, err)
	assert.Same(t, errAccountTooNew, err)
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1))
	assert.Zero(t, quotaOf(t, mainDB, 2))

	// 对照:把注册时间挪到冻结期之外,同一笔必须成交。
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 1).
		Update("created_at", now-25*3600).Error)
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000, Confirm: true, ClientRequestId: "audit-freeze-2",
	}))
	assert.Equal(t, 1_000, quotaOf(t, mainDB, 2))
}

// TestAuditPayPasswordGateFailsClosedWhenItsStoreIsBroken 钉住验密闸门的失败方向。
//
// 支付密码是会话被盗时的最后一道拦截。它的存储读不到(表缺失、扩展库抖动)
// 时只有两种可能:fail-open —— 一次数据库故障就把全站的第二因子整体拆掉;
// fail-closed —— 这段时间谁都转不了账。资金路径上只有后者是可接受的。
//
// 本用例刻意用一个**钱真的能动**的环境:如果闸门 fail-open,主库余额会当场
// 变化,断言立刻变红。只断言状态码是不够的 —— 那分不出"拒绝了"和"拒绝了
// 且没动钱"。
func TestAuditPayPasswordGateFailsClosedWhenItsStoreIsBroken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// auditEnv 建的扩展库里**没有** qy_pay_passwords 表 —— 这正是"验密存储读不到"。
	gdb, mainDB := auditEnv(t, auditWideOpenGlobal(),
		map[int]int{1: 90_000_000, 2: 0, 3: 0})

	// 对照:同一个环境下,绕开 handler 直接调 create() 必须真的动钱。
	// 少了这一条,下面的"余额没动"可能只是因为这个环境根本转不通。
	require.NoError(t, callCreate(t, 1, createRequest{
		ToUserId: 3, Amount: 1_000, Confirm: true, ClientRequestId: "audit-pp-control",
	}))
	require.Equal(t, 1_000, quotaOf(t, mainDB, 3), "对照组:这个环境确实能动钱")

	beforeSender := quotaOf(t, mainDB, 1)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/qy/transfer",
		bytes.NewBufferString(`{"to_user_id":2,"amount":1000,"confirm":true,`+
			`"client_request_id":"audit-pp-broken","pay_password":"whatever"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	handleCreate(c)

	assert.NotEqual(t, http.StatusOK, rec.Code,
		"验密存储读不到时必须拒绝,绝不能 fail-open 把第二因子整体拆掉")
	assert.Equal(t, beforeSender, quotaOf(t, mainDB, 1),
		"被验密闸门拦下时发起方一分钱都不能少")
	assert.Zero(t, quotaOf(t, mainDB, 2),
		"被验密闸门拦下时收款方一分钱都不能多")

	var settled int64
	require.NoError(t, gdb.Model(&Order{}).
		Where("order_no = ? OR status = ?", "", statusPending).Count(&settled).Error)
	assert.Zero(t, settled, "被拦下的请求不得留下未结算单据")

	var toUser2 int64
	require.NoError(t, gdb.Model(&Order{}).Where("to_user_id = ?", 2).Count(&toUser2).Error)
	assert.Zero(t, toUser2, "被验密闸门拦下时不得为这一笔落任何明细单")
}
