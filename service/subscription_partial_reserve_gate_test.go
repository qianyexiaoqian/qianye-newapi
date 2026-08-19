package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscription_partial_reserve_gate_test.go —— 部分预扣必须有人兜底。
//
// ═══════════ 被修掉的洞 ═══════════
//
// 钱包那条路强制 userQuota >= 预扣额(tryWallet 的 wallet_threshold 档)。
// 套餐这条路只要还剩 >= 1 个 quota,pickFundingSubscription 的第二轮就按剩余额度
// 部分预扣并放行整个请求,而路由是「订阅出得了资就不再看钱包」—— 钱包余额判据
// 在预扣阶段一次都不会被问到。真实花费在结算时撞到 amount_total 上限之后无条件
// 扣钱包(余额可为负)。
//
// 实测过的形态:钱包 0 + 一张 remain=1 的套餐 → 真打上游一条长请求 → HTTP 200,
// users.quota 0 → −190,300;同一账号把套餐拿掉就是 403。那 1 个 quota 换来了
// 一次不受任何余额判据约束的请求。
//
// ═══════════ 判据 ═══════════
//
// 套餐吃下 preConsumed,剩下的 shortfall 由钱包兜底 ⇒ 要求 userQuota >= shortfall。
// 与纯钱包路径「userQuota >= 预扣额」是同一条规则,只是套餐先付掉了一部分。
// 钱包里有钱的人照旧把尾数花光 —— 被挡住的只有"两边都付不起"的那一类。

const (
	partialGateUser = 90_401
	partialGatePlan = 90_402
	partialGateSub  = 90_403
	// 尾数只有 1:套餐出得了资,但只出得起 1/preQ。
	partialGateTail = 1
)

func seedPartialGateFixture(t *testing.T, walletQuota int, subTotal, subUsed int64) {
	t.Helper()
	truncate(t)
	seedUser(t, partialGateUser, walletQuota)
	stubCandidateUsable(t, true)
	seedBillingRoutePlan(t, partialGatePlan)

	sub := &model.UserSubscription{
		Id: partialGateSub, UserId: partialGateUser, PlanId: partialGatePlan, Status: "active",
		StartTime: 0, EndTime: 0,
		AmountTotal: subTotal, AmountUsed: subUsed,
		AllowWalletOverflow: true,
	}
	require.NoError(t, model.DB.Create(sub).Error)
}

func partialGateSession(t *testing.T, requestId string) (*BillingSession, *types.NewAPIError) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	info := newBillingRouteRelayInfo(requestId, "")
	info.UserId = partialGateUser
	return NewBillingSession(ctx, info, billingRoutePreQ)
}

func partialGateUsed(t *testing.T) int64 {
	t.Helper()
	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("id = ?", partialGateSub).First(&sub).Error)
	return sub.AmountUsed
}

func partialGateWallet(t *testing.T) int {
	t.Helper()
	q, err := model.GetUserQuota(partialGateUser, false)
	require.NoError(t, err)
	return q
}

// TestPartialReserveRejectedWhenWalletCannotCoverShortfall 是这条洞的正面用例:
// 钱包为 0 + 只剩 1 的套餐,必须 403,且尾数要被原样退回去。
func TestPartialReserveRejectedWhenWalletCannotCoverShortfall(t *testing.T) {
	seedPartialGateFixture(t, 0, 10_000, 10_000-partialGateTail)
	stubFundingGate(t, true)

	session, apiErr := partialGateSession(t, "partial-gate-empty-wallet")

	require.Nil(t, session, "钱包付不起差额时这一笔必须被挡在门外")
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode(),
		"两个资金来源加起来都不够,属于余额不足这一类(充值即可解决)")
	assert.Equal(t, int64(10_000-partialGateTail), partialGateUsed(t),
		"被拒的请求必须把已经部分预扣掉的尾数同步退回去 —— 否则下一次请求会读到一个被占着的尾数")
	assert.Equal(t, 0, partialGateWallet(t), "被拒的请求一分钱都不该动钱包")
}

// TestPartialReserveAllowedWhenWalletCoversShortfall 是「尾数必须花得掉」那条
// 设计目标的守门用例:钱包够付差额时,部分预扣照旧放行。
//
// 没有它,上一个用例可以靠"把第二轮整段删掉"来通过,而那会把尾数重新变成死钱。
func TestPartialReserveAllowedWhenWalletCoversShortfall(t *testing.T) {
	seedPartialGateFixture(t, billingRoutePreQ, 10_000, 10_000-partialGateTail)
	stubFundingGate(t, true)

	session, apiErr := partialGateSession(t, "partial-gate-funded-wallet")

	require.Nil(t, apiErr, "钱包足以覆盖差额时,尾数必须还能被花掉")
	require.NotNil(t, session)
	assert.Equal(t, BillingSourceSubscription, session.funding.Source())
	assert.Equal(t, int64(10_000), partialGateUsed(t), "套餐应当把尾数全部吃下")
	assert.Equal(t, billingRoutePreQ, partialGateWallet(t),
		"预扣阶段钱包不出钱,差额要等结算才落地")
}

// TestPartialReserveNotGatedWhenWalletMayNotCover 钱包本来就不许给这个分组出资时,
// 差额按配置由平台核销 —— 那一档不能拿钱包余额去拦人。
func TestPartialReserveNotGatedWhenWalletMayNotCover(t *testing.T) {
	seedPartialGateFixture(t, 0, 10_000, 10_000-partialGateTail)
	stubFundingGate(t, true)

	prev := QyWalletMayCoverSubscriptionShortfall
	QyWalletMayCoverSubscriptionShortfall = func(int, string, string, int64) bool { return false }
	t.Cleanup(func() { QyWalletMayCoverSubscriptionShortfall = prev })

	session, apiErr := partialGateSession(t, "partial-gate-writeoff")

	require.Nil(t, apiErr, "运营明说这个分组不花用户的钱,就不该用钱包余额当门槛")
	require.NotNil(t, session)
	assert.Equal(t, int64(10_000), partialGateUsed(t))
}

// TestFullReserveNeverConsultsWallet 整额覆盖那条路不受本闸门影响。
//
// 它守的是"别把闸门装错位置":第一轮整额命中时 shortfall 为 0,钱包余额与这一笔
// 无关(钱包 0 的用户拿着一张余额充足的套餐照样该被放行)。
func TestFullReserveNeverConsultsWallet(t *testing.T) {
	seedPartialGateFixture(t, 0, 10_000, 0)
	stubFundingGate(t, true)

	session, apiErr := partialGateSession(t, "partial-gate-full")

	require.Nil(t, apiErr)
	require.NotNil(t, session)
	assert.Equal(t, int64(billingRoutePreQ), partialGateUsed(t))
	assert.Equal(t, 0, partialGateWallet(t))
}
