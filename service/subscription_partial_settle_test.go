package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 套餐余额不足一次预扣额时按剩余额度**部分**预扣。调用方给的差额是按"想扣多少"
// 算的(令牌那一侧确实是按它预扣、按它退的),套餐这一侧必须把"想扣"与"真扣"
// 之间那段补回来 —— 不补的话:
//
//	退款方向:delta 是按 3048 算的 −3020,而套餐只被扣了 100,直接退 3020 会把
//	          这张套餐里**别人**的消费一起退掉(SettleUserSubscriptionDelta 会
//	          一路夹到 0);
//	补扣方向:少收同样一段。
func TestSubscriptionSettleDeltaAccountsForAPartialReserve(t *testing.T) {
	cases := []struct {
		name        string
		delta       int
		requested   int64
		preConsumed int64
		want        int64
	}{
		{"整额预扣时恒等变换", -3020, 3048, 3048, -3020},
		{"只扣到 100、真实花费 28:套餐从 100 退回 28", -3020, 3048, 100, -72},
		{"只扣到 100、真实花费 5000:套餐补 4900(撞上限后落钱包)", 1952, 3048, 100, 4900},
		{"真实花费恰好等于预扣额,但套餐还欠着一段", 0, 3048, 100, 2948},
		{"整张套餐只有 1000:真实花费 28", -3020, 3048, 1000, -972},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, subscriptionSettleDelta(tc.delta, tc.requested, tc.preConsumed))
		})
	}
}

// 差额恰好为 0(真实花费等于预扣估算额)时不能早退:套餐那一侧还欠着
// requested − preConsumed 没收,早退就把这段钱永久漏掉。
func TestPartialReserveForcesASettlePassEvenWhenTheDeltaIsZero(t *testing.T) {
	full := &BillingSession{funding: &SubscriptionFunding{amount: 3048, preConsumed: 3048}}
	assert.False(t, full.fundingHasPartialReserve())

	partial := &BillingSession{funding: &SubscriptionFunding{amount: 3048, preConsumed: 100}}
	assert.True(t, partial.fundingHasPartialReserve())

	wallet := &BillingSession{funding: &WalletFunding{}}
	assert.False(t, wallet.fundingHasPartialReserve())
}
