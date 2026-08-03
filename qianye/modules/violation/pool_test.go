package violation

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useMainDB 把主库句柄临时换成内存库。
//
// poolBalance 走的是 model.GetUserQuota 与 model.DB 上的订阅查询,这两条路径
// 正是 B2 里"读错了池"的落点,不真的查一次库就验证不到。
func useMainDB(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	origDB, origRedis := model.DB, common.RedisEnabled
	model.DB = gdb
	// common.RedisEnabled 的包级初值是 true(只有 InitRedisClient 看到空连接串
	// 才会置 false),测试进程里没跑过它。不关掉的话 GetUserQuota 会去读一个
	// 不存在的 Redis 而不是这个内存库。
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = origDB
		common.RedisEnabled = origRedis
	})
}

// TestPoolBalanceFollowsBillingSource 是 B2 的核心回归。
//
// 扣款按 relayInfo.BillingSource 路由到钱包或订阅池(service/quota.go),
// 而旧实现的余额判定一律读钱包。订阅用户的钱包通常是 0,于是:
//   - 默认 clamp 策略下永远罚不到款,记录还写着"余额不足"误导管理员;
//   - ban 策略下 available(0) < want 直接 ForceBanWeight,订阅池里明明有钱
//     却首次违规即被自动封号。
//
// PreRelayGuard 阶段 BillingSource 还没被赋值,走钱包分支与那一刻的实际扣款
// 目标一致 —— 错位只出现在 post 阶段,所以只测 prompt 阶段永远发现不了。
func TestPoolBalanceFollowsBillingSource(t *testing.T) {
	cases := []struct {
		name      string
		source    string
		subId     int
		wantValue int64
		wantKnown bool
	}{
		{"prompt 阶段尚未赋值:读钱包", "", 0, 120, true},
		{"钱包计费:读钱包", service.BillingSourceWallet, 0, 120, true},
		// 这一条在旧实现下会读到钱包的 120 而不是订阅池的 400000。
		{"订阅计费:读订阅池剩余", service.BillingSourceSubscription, 31, 400000, true},
		{"订阅计费但订阅不限额:按主库单笔上限", service.BillingSourceSubscription, 32,
			int64(common.MaxQuota), true},
		{"订阅计费但缺少订阅 id:余额未知", service.BillingSourceSubscription, 0, 0, false},
		{"订阅计费但订阅行不存在:余额未知", service.BillingSourceSubscription, 999, 0, false},
		{"订阅已用超总额:夹到 0 而不是负数", service.BillingSourceSubscription, 33, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newMainDB(t)
			useMainDB(t, gdb)
			// 订阅用户的典型状态:钱包几乎是空的,钱都在订阅池里。
			require.NoError(t, gdb.Create(&model.User{Id: 88, Username: "u88", Quota: 120}).Error)
			require.NoError(t, gdb.Create(&model.UserSubscription{
				Id: 31, UserId: 88, AmountTotal: 1000000, AmountUsed: 600000}).Error)
			require.NoError(t, gdb.Create(&model.UserSubscription{
				Id: 32, UserId: 88, AmountTotal: 0, AmountUsed: 0}).Error)
			require.NoError(t, gdb.Create(&model.UserSubscription{
				Id: 33, UserId: 88, AmountTotal: 1000, AmountUsed: 9000}).Error)

			info := &relaycommon.RelayInfo{UserId: 88}
			info.BillingSource = tc.source
			info.SubscriptionId = tc.subId

			got, known := poolBalance(info)
			assert.Equal(t, tc.wantKnown, known)
			assert.Equal(t, tc.wantValue, got)
		})
	}
}

// TestUnknownBalanceNeverForcesBan 保证"读不出余额"不会被当成"余额为 0"。
//
// applyBalancePolicy 在 ban 策略下把 available < want 直接翻译成 ForceBanWeight,
// 也就是"这次违规的计数权重顶到阈值,立刻封号"。如果调用方在余额读失败时拿 0 顶上去,
// 一次 Redis 抖动就等于一次自动封号 —— 所以 chargeFee 必须在余额未知时整体放弃扣费。
func TestUnknownBalanceNeverForcesBan(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  insufficient_balance_policy: ban\n")

	// 先固化被规避的那个行为:0 余额在 ban 策略下确实会触发强制封号权重。
	zero := feeResult{Want: 1000, Status: FeeStatusNone}
	applyBalancePolicy(&zero, 0)
	require.True(t, zero.ForceBanWeight,
		"前置条件:ban 策略下余额为 0 就是封号,所以'未知'绝不能退化成 0")

	// 订阅计费但订阅 id 缺失 —— 扣费注定失败,余额也读不出来。
	info := &relaycommon.RelayInfo{UserId: 88}
	info.BillingSource = service.BillingSourceSubscription
	_, known := poolBalance(info)
	assert.False(t, known, "余额未知必须如实上报,由 chargeFee 放弃本次扣费")
}
