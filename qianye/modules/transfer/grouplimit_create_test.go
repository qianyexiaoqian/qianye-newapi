package transfer

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// grouplimit_create_test.go —— 分档在**真正会动钱的入口**上的回归。
//
// # 为什么必须单独有这一组
//
// grouplimit_test.go 里那条 TestTransferRejectsAboveTierPerTxCapEvenWhenGlobalAllows
// 的注释写着「从真正会动钱的入口进」,函数体调的却是 validateCreate 这个纯函数,
// cfg 由测试自己从 settings.transferFor("vip") 取出来 —— 与 service.go 里那一行
// 没有任何关联。把 create() 里的 `cfg, err := settings.transferFor(senderNow.Group)`
// 改回 `cfg := settings.Transfer`,分档就在受理校验、新账号冻结、日额度、冷却、
// 收款方闸门五处**全部**失效,而整包测试仍然全绿。那正是本仓头号缺陷形状:
// 「界面显示已保存、判定端从没读到」。
//
// 因此这一组一律从 create() 进,并且断言到扩展库里的观测面(订单表、风控状态行)。

// createEnv 建起 create() 需要的两个库:扩展库(门槛/分档/资金单/风控状态)与主库(users)。
func createEnv(t *testing.T, global config.Transfer) (*gorm.DB, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gdb := newSettingsTestDB(t)
	// 资金单表:create() 的动钱那一步走 twophase.Execute,没有它连 reserveRisk
	// 都进不去 —— 而收款方闸门恰恰判在 reserveRisk 里。
	require.NoError(t, gdb.AutoMigrate(&qymodel.FundOrder{}))
	mainDB := newMainDB(t)
	useSettingsConfig(t, global)
	invalidateSettings()
	return gdb, mainDB
}

// createGlobal 是本文件的全站兜底门槛:冷却与冻结期关掉(它们会抢在收款方闸门
// 之前把请求拒掉,断言就分不出拒绝到底来自哪一道闸门),收款方闸门放到 20。
func createGlobal() config.Transfer {
	cfg := tierGlobal()
	cfg.CooldownSecs = 0
	cfg.NewAccountFreezeHours = 0
	cfg.FeeBps = 0
	cfg.FeeMinQuota = 0
	return cfg
}

func seedUserState(t *testing.T, gdb *gorm.DB, userId int, dayInCount int) {
	t.Helper()
	require.NoError(t, gdb.Create(&UserState{
		UserId:     userId,
		DayBucket:  dayBucket(common.GetTimestamp()),
		DayInCount: dayInCount,
		UpdatedAt:  common.GetTimestamp(),
	}).Error)
}

// callCreate 从 create() 这个真正会动钱的入口打进去。
//
// 刻意不经 handleCreate:那一层还有支付密码闸门(paypass_gate_test.go 专门测它),
// 混进来会让本文件的失败原因多一种可能。
func callCreate(t *testing.T, fromUserId int, req createRequest) error {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/qy/transfer", nil)
	c.Set("id", fromUserId)
	c.Set("username", "u"+strconv.Itoa(fromUserId))
	_, err := create(c, fromUserId, req)
	return err
}

// TestReceiverDailyInGateReadsTheReceiverTierNotTheSenderTier 是本轮的 blocker 回归。
//
// receiver_daily_max_in_count 约束的是「这个账号每天能收几笔」。按发起方那一档
// 解析,闸门的上限就由被它约束的那一方自己挑:
//
//	运营给 vip 配「每天最多入账 1 笔」,攻击者用一堆 default 小号各转 1 笔到那个
//	vip 汇集账号 —— 按发起方(default)解析拿到的是全站的 20,全部通过。
//	而 users.group 是可以靠买套餐改写的,攻击者能直接买进最松的那一档。
//
// 这道闸门存在的唯一理由就是拦「一堆小号汇集到同一账号」(见 evaluateRisk),
// 分档若把它交给小号那一侧,它就等于不存在。
func TestReceiverDailyInGateReadsTheReceiverTierNotTheSenderTier(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	seedMainUser(t, mainDB, 1, "default", 90_000_000) // 发起方:全站那一档(20 笔)
	seedMainUser(t, mainDB, 2, "vip", 0)              // 收款方:vip 那一档(1 笔)
	// 收款方今天已经收过 1 笔,按它自己那一档已经满了。
	seedUserState(t, gdb, 2, 1)

	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "vip", Enabled: true, ReceiverDailyMaxInCount: i64(1),
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "recv-gate-1",
	})
	require.Error(t, err)
	assert.Same(t, errReceiverDailyInExceeded, err,
		"收款方口径的闸门必须按收款方那一档解析;按发起方(default → 全站 20)解析会把这一笔放行")

	// 「拒绝了」与「拒绝了且没动钱」是两回事。
	assert.Equal(t, 90_000_000, quotaOf(t, mainDB, 1), "被风控拒掉的划转不得动发起方余额")
	assert.Equal(t, 0, quotaOf(t, mainDB, 2))
}

// TestReceiverDailyInGateDoesNotImposeTheSenderTierOnTheReceiver 是反方向的对照。
//
// 同一个错误的另一面:发起方在一档「每天只能入账 1 笔」的分组里时,那条限制
// 绝不能被强加到它的每一个收款人身上 —— 那会让 vip 用户一转账就把对方冻住。
func TestReceiverDailyInGateDoesNotImposeTheSenderTierOnTheReceiver(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	seedMainUser(t, mainDB, 1, "vip", 90_000_000) // 发起方在「每天最多入账 1 笔」那一档
	seedMainUser(t, mainDB, 2, "default", 0)      // 收款方走全站那一档(20 笔)
	seedUserState(t, gdb, 2, 3)                   // 收款方今天已收 3 笔,全站那一档还很宽

	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "vip", Enabled: true, ReceiverDailyMaxInCount: i64(1),
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 1_000_000, Confirm: true, ClientRequestId: "recv-gate-2",
	})
	if err != nil {
		assert.NotSame(t, errReceiverDailyInExceeded, err,
			"发起方那一档的收款方闸门被错误地强加到了收款人身上")
	}
}

// TestCreateRejectsAboveTierPerTxCapAtTheMoneyEntrance 从 create() 进,
// 钉住「分档真的接在了受理校验上」。
//
// 对照组是同一笔金额、同一份全站门槛,只是发起方不在配了分档的那个分组里 ——
// 它必须不是 errAmountOutOfRange,否则上面那条断言可能只是因为别的原因失败。
func TestCreateRejectsAboveTierPerTxCapAtTheMoneyEntrance(t *testing.T) {
	gdb, mainDB := createEnv(t, createGlobal())
	seedMainUser(t, mainDB, 1, "vip", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)
	seedMainUser(t, mainDB, 3, "default", 90_000_000)

	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "vip", Enabled: true, MaxPerTxQuota: i64(1_000_000),
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()

	err := callCreate(t, 1, createRequest{
		ToUserId: 2, Amount: 2_000_000, Confirm: true, ClientRequestId: "tier-cap-create",
	})
	require.Error(t, err)
	assert.Same(t, errAmountOutOfRange, err,
		"create 必须按发起方那一档做受理校验;读 settings.Transfer 会按全站的 5000 万放行这一笔")

	// 对照:同一笔金额、同一份全站门槛,发起方不在那一档里 ⇒ 不该被金额闸门拒。
	err = callCreate(t, 3, createRequest{
		ToUserId: 2, Amount: 2_000_000, Confirm: true, ClientRequestId: "tier-cap-control",
	})
	if err != nil {
		assert.NotSame(t, errAmountOutOfRange, err,
			"对照组被同一道闸门拒了 —— 说明拒绝并非来自分档")
	}
}
