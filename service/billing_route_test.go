package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// billing_route_test.go —— NewBillingSession 的扣费顺序判据表。
//
// ═══════════ 本轮改了什么 ═══════════
//
// 扣费顺序曾由每用户的 UserSetting.BillingPreference 决定(四个值),现在写死:
//
//	有活跃订阅 → 先试订阅(候选按 end_time asc,范围不匹配与余额不够本次预扣的跳过)
//	订阅出不了资 → **一律**落到钱包,不再查 allow_wallet_overflow、不再直接 403
//
// 钱包这一侧还有一道闸门(QyModelGroupFundingAllowed)。闸门的判据表在
// qianye/modules/groupns/funding_test.go;这里把它 stub 成一个布尔,只验证
// **路由**:谁先谁后、闸门在哪一步被问、拒绝时返回哪个错误码。
//
// 错误码必须可分辨:AccessDenied = 你没有这个分组的出资资格(充钱包也没用),
// InsufficientUserQuota = 你余额不足。混成一个,客户端就只能瞎猜。

const (
	billingRouteUser  = 90_001
	billingRoutePreQ  = 100 // 本次预扣额度
	billingRoutePlanA = 90_101
	billingRoutePlanB = 90_102
)

// stubFundingGate 把钱包出资闸门换成一个固定答案,并数它被问了几次。
func stubFundingGate(t *testing.T, allow bool) *int {
	t.Helper()
	calls := 0
	prev := QyModelGroupFundingAllowed
	QyModelGroupFundingAllowed = func(int, string, string) (bool, string) {
		calls++
		if allow {
			return true, ""
		}
		return false, "模型分组由套餐解锁且额度已用尽,该套餐不允许使用钱包余额"
	}
	t.Cleanup(func() { QyModelGroupFundingAllowed = prev })
	return &calls
}

// stubCandidateUsable 模拟「余额使用范围仅限别的分组」:该套餐不是本次的出资候选。
func stubCandidateUsable(t *testing.T, usable bool) {
	t.Helper()
	prev := model.QySubscriptionCandidateUsable
	model.QySubscriptionCandidateUsable = func(int, string) bool { return usable }
	t.Cleanup(func() { model.QySubscriptionCandidateUsable = prev })
}

func seedBillingRoutePlan(t *testing.T, id int) {
	t.Helper()
	plan := &model.SubscriptionPlan{
		Id: id, Title: "route-plan", PriceAmount: 1,
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
		QuotaResetPeriod: model.SubscriptionResetNever,
	}
	require.NoError(t, model.DB.Create(plan).Error)
	// 套餐有一层进程内缓存,同一个 id 跨用例复用会读到上一个用例的行。
	t.Cleanup(func() { model.InvalidateSubscriptionPlanCache(id) })
}

func seedBillingRouteSub(t *testing.T, id, planId int, total, used int64) {
	t.Helper()
	now := common.GetTimestamp()
	sub := &model.UserSubscription{
		Id: id, UserId: billingRouteUser, PlanId: planId, Status: "active",
		StartTime: now - 60, EndTime: now + 86400,
		AmountTotal: total, AmountUsed: used,
		AllowWalletOverflow: true,
	}
	require.NoError(t, model.DB.Create(sub).Error)
}

func newBillingRouteRelayInfo(requestId string, legacyPreference string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RequestId:       requestId,
		UserId:          billingRouteUser,
		UserGroup:       "default",
		UsingGroup:      "pro",
		OriginModelName: "gpt-test",
		// IsPlayground 只是绕开令牌额度这一层:令牌不是本表要验的东西。
		IsPlayground:    true,
		ForcePreConsume: true,
		UserSetting:     dto.UserSetting{BillingPreference: legacyPreference},
	}
}

func userQuotaNow(t *testing.T) int {
	t.Helper()
	q, err := model.GetUserQuota(billingRouteUser, false)
	require.NoError(t, err)
	return q
}

func subAmountUsed(t *testing.T, id int) int64 {
	t.Helper()
	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("id = ?", id).First(&sub).Error)
	return sub.AmountUsed
}

// billingRouteCase 是判据表的一行。
type billingRouteCase struct {
	name string
	// 订阅:0 张 / 1 张。total/used 决定它这一笔出不出得了资。
	withSub  bool
	subTotal int64
	subUsed  int64
	// candidateUsable=false 模拟「余额仅限别的模型分组」。
	candidateUsable bool
	walletQuota     int
	gateAllows      bool
	// 期望
	wantSource string // BillingSourceSubscription / BillingSourceWallet / "" 表示失败
	// wantSubConsumed 是套餐这一次真正被扣掉的额度。0 = 与 billingRoutePreQ 相同
	// (整额预扣);余额不足一次预扣额时套餐按剩余额度**部分**出资。
	wantSubConsumed int64
	wantCode        types.ErrorCode
	wantGate        int // 闸门被问的次数
	why             string
}

func billingRouteTable() []billingRouteCase {
	return []billingRouteCase{
		{
			name: "无订阅 + 闸门放行 + 钱包够", withSub: false,
			candidateUsable: true, walletQuota: 10_000, gateAllows: true,
			wantSource: BillingSourceWallet, wantGate: 1,
			why: "没有套餐可扣,直接走钱包",
		},
		{
			name: "无订阅 + 闸门放行 + 钱包为空", withSub: false,
			candidateUsable: true, walletQuota: 0, gateAllows: true,
			wantCode: types.ErrorCodeInsufficientUserQuota, wantGate: 1,
			why: "余额不足是 InsufficientUserQuota,充值就能解决",
		},
		{
			name: "无订阅 + 闸门放行 + 钱包不够本次预扣", withSub: false,
			candidateUsable: true, walletQuota: billingRoutePreQ - 1, gateAllows: true,
			wantCode: types.ErrorCodeInsufficientUserQuota, wantGate: 1,
			why: "预扣门槛本身把人挡住,仍然是余额不足这一类",
		},
		{
			name: "无订阅 + 闸门拒绝 + 钱包够", withSub: false,
			candidateUsable: true, walletQuota: 10_000, gateAllows: false,
			wantCode: types.ErrorCodeAccessDenied, wantGate: 1,
			why: "「没有这个分组的出资资格」必须与「余额不足」分开 —— 充钱包解决不了它",
		},
		{
			name: "有订阅 + 余额够 + 钱包够", withSub: true, subTotal: 10_000, subUsed: 0,
			candidateUsable: true, walletQuota: 10_000, gateAllows: true,
			wantSource: BillingSourceSubscription, wantGate: 0,
			why: "套餐优先。闸门一次都不该被问 —— 这一笔根本没走到钱包",
		},
		{
			name: "有订阅 + 余额够 + 闸门拒绝 + 钱包为空", withSub: true, subTotal: 10_000, subUsed: 0,
			candidateUsable: true, walletQuota: 0, gateAllows: false,
			wantSource: BillingSourceSubscription, wantGate: 0,
			why: "套餐付得起时,钱包状态与闸门结论都与这一笔无关",
		},
		{
			name: "有订阅 + 余额耗尽 + 闸门放行 + 钱包够", withSub: true, subTotal: 10_000, subUsed: 10_000,
			candidateUsable: true, walletQuota: 10_000, gateAllows: true,
			wantSource: BillingSourceWallet, wantGate: 1,
			why: "**项目方点名要改的核心格**:订阅出不了资就落钱包,不再直接 403",
		},
		{
			name:    "有订阅 + 尾数(余额>0 但不够本次预扣) + 钱包够",
			withSub: true, subTotal: 10_000, subUsed: 10_000 - (billingRoutePreQ - 1),
			candidateUsable: true, walletQuota: 10_000, gateAllows: true,
			wantSource: BillingSourceSubscription, wantGate: 0,
			wantSubConsumed: billingRoutePreQ - 1,
			why: "尾数必须花得掉。筛候选用的是**预扣估算额**(真实花费的几十到上百倍)," +
				"整额覆盖不了就跳过的话,每张套餐用到尾巴都会留下「一次预扣额 − 1」的死钱," +
				"用户看到的是「套餐还有余额,却在扣钱包」。现在套餐先出到 0,不够的部分" +
				"在结算时由钱包补收(那条路径本来就存在)",
		},
		{
			name: "有订阅 + 余额耗尽 + 闸门拒绝 + 钱包够", withSub: true, subTotal: 10_000, subUsed: 10_000,
			candidateUsable: true, walletQuota: 10_000, gateAllows: false,
			wantCode: types.ErrorCodeAccessDenied, wantGate: 1,
			why: "纯解锁分组耗尽 + 运营显式禁止钱包续付 —— 这一档才 403,且不是额度不足",
		},
		{
			name: "有订阅 + 余额耗尽 + 闸门放行 + 钱包为空", withSub: true, subTotal: 10_000, subUsed: 10_000,
			candidateUsable: true, walletQuota: 0, gateAllows: true,
			wantCode: types.ErrorCodeInsufficientUserQuota, wantGate: 1,
			why: "两个资金来源都空了,报余额不足",
		},
		{
			name:    "有订阅 + 范围不匹配(余额还在但用不了) + 钱包够",
			withSub: true, subTotal: 10_000, subUsed: 0,
			candidateUsable: false, walletQuota: 10_000, gateAllows: true,
			wantSource: BillingSourceWallet, wantGate: 1,
			why: "范围不匹配的套餐视同无套餐:钱还在,只是这一笔用不了",
		},
		{
			name:    "有订阅 + 范围不匹配 + 钱包为空",
			withSub: true, subTotal: 10_000, subUsed: 0,
			candidateUsable: false, walletQuota: 0, gateAllows: true,
			wantCode: types.ErrorCodeInsufficientUserQuota, wantGate: 1,
			why: "套餐用不上、钱包也空",
		},
		{
			name:    "有订阅 + amount_total<=0(不限量) + 钱包够",
			withSub: true, subTotal: 0, subUsed: 999_999,
			candidateUsable: true, walletQuota: 10_000, gateAllows: true,
			wantSource: BillingSourceSubscription, wantGate: 0,
			why: "amount_total<=0 是不限量,不是零额度 —— 它永远出得了资",
		},
	}
}

func TestNewBillingSessionRoutesSubscriptionFirstThenWallet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for i, tc := range billingRouteTable() {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			seedUser(t, billingRouteUser, tc.walletQuota)
			stubCandidateUsable(t, tc.candidateUsable)
			gateCalls := stubFundingGate(t, tc.gateAllows)

			subId := 0
			if tc.withSub {
				seedBillingRoutePlan(t, billingRoutePlanA)
				subId = 90_200 + i
				seedBillingRouteSub(t, subId, billingRoutePlanA, tc.subTotal, tc.subUsed)
			}

			ctx, _ := gin.CreateTestContext(nil)
			relayInfo := newBillingRouteRelayInfo("route-req-"+tc.name, "")
			session, apiErr := NewBillingSession(ctx, relayInfo, billingRoutePreQ)

			assert.Equalf(t, tc.wantGate, *gateCalls,
				"钱包出资闸门被问的次数不对。理由:%s", tc.why)

			if tc.wantSource == "" {
				require.NotNilf(t, apiErr, "这一格应当失败。理由:%s", tc.why)
				assert.Nil(t, session)
				assert.Equalf(t, tc.wantCode, apiErr.GetErrorCode(),
					"错误码必须可分辨。理由:%s", tc.why)
				// 失败的请求不能动任何一边的钱。
				assert.Equal(t, tc.walletQuota, userQuotaNow(t), "被拒的请求不该扣钱包")
				if tc.withSub {
					assert.Equal(t, tc.subUsed, subAmountUsed(t, subId), "被拒的请求不该扣套餐")
				}
				return
			}

			require.Nilf(t, apiErr, "这一格应当成功。理由:%s", tc.why)
			require.NotNil(t, session)
			assert.Equalf(t, tc.wantSource, relayInfo.BillingSource, "资金来源不对。理由:%s", tc.why)

			switch tc.wantSource {
			case BillingSourceWallet:
				assert.Equal(t, tc.walletQuota-billingRoutePreQ, userQuotaNow(t))
				if tc.withSub {
					assert.Equal(t, tc.subUsed, subAmountUsed(t, subId), "走钱包时套餐一分钱都不该动")
				}
			case BillingSourceSubscription:
				assert.Equal(t, tc.walletQuota, userQuotaNow(t), "走套餐时钱包一分钱都不该动")
				consumed := tc.wantSubConsumed
				if consumed == 0 {
					consumed = billingRoutePreQ
				}
				assert.Equal(t, tc.subUsed+consumed, subAmountUsed(t, subId))
			}
		})
	}
}

// TestNewBillingSessionIgnoresLegacyBillingPreference 是存量用户的行为断言。
//
// 库里有 23 个用户显式写过非默认的 billing_preference(wallet_first 17 / wallet_only 4 /
// subscription_only 2)。那个键仍然留在 users.setting 里(删字段会在他们下一次改
// 任何别的设置时被静默抹掉,见 relaykit/dto.UserSetting 的说明),但**不再被读取**。
//
// 两个方向都要断言,只断一个的话"根本没读设置"与"读了但恰好同向"分不开:
//
//	wallet_only      过去绝不动套餐 → 现在套餐优先,先扣套餐
//	subscription_only 过去订阅耗尽即 403 → 现在落到钱包(现网 id 504 / 1123 就是这两位)
func TestNewBillingSessionIgnoresLegacyBillingPreference(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name       string
		preference string
		subTotal   int64
		subUsed    int64
		wantSource string
		why        string
	}{
		{
			name: "wallet_only + 套餐有余额", preference: "wallet_only",
			subTotal: 10_000, subUsed: 0, wantSource: BillingSourceSubscription,
			why: "过去 wallet_only 让这一笔走钱包、套餐留着不动;写死之后先扣套餐",
		},
		{
			name: "wallet_first + 套餐有余额", preference: "wallet_first",
			subTotal: 10_000, subUsed: 0, wantSource: BillingSourceSubscription,
			why: "过去 wallet_first 先走钱包;写死之后先扣套餐",
		},
		{
			name: "subscription_only + 套餐耗尽", preference: "subscription_only",
			subTotal: 10_000, subUsed: 10_000, wantSource: BillingSourceWallet,
			why: "过去 subscription_only 在这里直接 403 且绝不动钱包;写死之后落到钱包",
		},
		{
			name: "subscription_first(旧默认) + 套餐耗尽", preference: "subscription_first",
			subTotal: 10_000, subUsed: 10_000, wantSource: BillingSourceWallet,
			why: "旧默认档在 allow_wallet_overflow=0 时会 403;现在一律落钱包",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			seedUser(t, billingRouteUser, 10_000)
			stubCandidateUsable(t, true)
			stubFundingGate(t, true)
			seedBillingRoutePlan(t, billingRoutePlanB)
			seedBillingRouteSub(t, 90_300, billingRoutePlanB, tc.subTotal, tc.subUsed)

			ctx, _ := gin.CreateTestContext(nil)
			relayInfo := newBillingRouteRelayInfo("legacy-"+tc.preference, tc.preference)
			session, apiErr := NewBillingSession(ctx, relayInfo, billingRoutePreQ)
			require.Nil(t, apiErr)
			require.NotNil(t, session)
			assert.Equalf(t, tc.wantSource, relayInfo.BillingSource,
				"存量 billing_preference 必须完全不影响扣费顺序。理由:%s", tc.why)
		})
	}
}
