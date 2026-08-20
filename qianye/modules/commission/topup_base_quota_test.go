package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
)

// 计佣基数必须与**实际到账额度**同口径。
//
// 空 payment_provider 有两种来源：订阅付费单（upsertSubscriptionTopUpTx 硬编码
// Amount=0）与 payment_provider 列存在之前的历史 epay 订单（Amount>0）。
// 原先把两者合并进 stripe 那一支按 Money 算基数——注释里写的前提是“Amount=0”，
// 代码却从不检查。历史折扣订单（amount=10 / money=8）因此按 Money 算基数，
// 与实际到账额度差一个折扣率；开过 operation_setting.Price ≠ 1 的站点差更多。
// 这批老单仍可被管理员补单激活，补完就被迟付回收捞进来按错口径计佣。
func TestTopUpBaseQuotaMatchesWhatActuallyLands(t *testing.T) {
	prev := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = prev })

	cases := []struct {
		name     string
		topUp    model.TopUp
		wantBase int64
	}{
		{
			name:     "历史 epay 老单(provider 为空、Amount>0、带折扣):按 Amount 算",
			topUp:    model.TopUp{PaymentProvider: "", Amount: 10, Money: 8},
			wantBase: 10 * 500000,
		},
		{
			name:     "订阅付费单(provider 为空、Amount=0):按 Money 算",
			topUp:    model.TopUp{PaymentProvider: "", Amount: 0, Money: 30},
			wantBase: 30 * 500000,
		},
		{
			name:     "stripe:按 Money 算",
			topUp:    model.TopUp{PaymentProvider: model.PaymentProviderStripe, Amount: 4000, Money: 4},
			wantBase: 4 * 500000,
		},
		{
			name:     "creem:Amount 本身就是额度",
			topUp:    model.TopUp{PaymentProvider: model.PaymentProviderCreem, Amount: 5000000, Money: 10},
			wantBase: 5000000,
		},
		{
			name:     "epay:Amount × QuotaPerUnit",
			topUp:    model.TopUp{PaymentProvider: model.PaymentProviderEpay, Amount: 10, Money: 8},
			wantBase: 10 * 500000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := topUpBaseQuota(&tc.topUp)
			assert.Equal(t, tc.wantBase, base)

			// 与唯一的到账口径逐值对照:计佣基数就该等于实际到账额度。
			//
			// 订阅付费单除外:它不给钱包加额度(CreditQuota 对它恒为 0),
			// 发的是套餐权益,基数只能按成交金额算。
			isSubscriptionOrder := tc.topUp.PaymentProvider == "" && tc.topUp.Amount == 0
			if isSubscriptionOrder {
				return
			}
			credited, err := tc.topUp.CreditQuota()
			if err == nil {
				assert.Equal(t, int64(credited), base,
					"计佣基数与实际到账额度必须是同一个数,否则返佣多发或少发")
			}
		})
	}
}
