package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件锁三条资金不变量:
//
//  1. 「能建出来的充值订单一定能结算」—— 换算口径只有 CreditQuota 一份,
//     结算侧超过 common.MaxQuota 会整笔回滚,用户付了钱拿不到额度;
//  2. 「一张兑换码只兑一次」—— 核销痕迹比 status 更硬,status 可以被管理端写回去;
//  3. 「兑换码只会加钱」—— 非正面额兑换出去的是一笔倒扣,而接口回的是 success。

func TestTopUpCreditQuotaIsFailClosedAtTheSettlementCeiling(t *testing.T) {
	prevQPU := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = prevQPU })

	// MaxQuota/QuotaPerUnit = 2147483647/500000 = 4294.96…,边界恰在 4294/4295 之间。
	for _, tc := range []struct {
		name    string
		topUp   TopUp
		want    int
		wantErr bool
	}{
		{
			name:  "易支付按 Amount × QuotaPerUnit",
			topUp: TopUp{PaymentProvider: PaymentProviderEpay, Amount: 4294},
			want:  2147000000,
		},
		{
			name:    "易支付越过上限即失败,不能悄悄截断",
			topUp:   TopUp{PaymentProvider: PaymentProviderEpay, Amount: 4295},
			wantErr: true,
		},
		{
			name:  "waffo 与易支付同口径",
			topUp: TopUp{PaymentProvider: PaymentProviderWaffo, Amount: 4294},
			want:  2147000000,
		},
		{
			name:    "waffo 越过上限即失败",
			topUp:   TopUp{PaymentProvider: PaymentProviderWaffo, Amount: 4295},
			wantErr: true,
		},
		{
			name:  "stripe 按 Money × QuotaPerUnit(Money 已含分组倍率)",
			topUp: TopUp{PaymentProvider: PaymentProviderStripe, Money: 4294, Amount: 1},
			want:  2147000000,
		},
		{
			name:    "stripe 的上限落在 Money 上,不是 Amount",
			topUp:   TopUp{PaymentProvider: PaymentProviderStripe, Money: 4295, Amount: 1},
			wantErr: true,
		},
		{
			name:  "creem 的 Amount 本身就是额度,不再乘单价",
			topUp: TopUp{PaymentProvider: PaymentProviderCreem, Amount: 1000},
			want:  1000,
		},
		{
			name:    "creem 同样有上限",
			topUp:   TopUp{PaymentProvider: PaymentProviderCreem, Amount: int64(common.MaxQuota) + 1},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.topUp.CreditQuota()
			if tc.wantErr {
				require.Error(t, err, "触顶必须报错,而不是返回一个被夹住的额度")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestManualCompleteTopUpMarksAdminCompleteSource(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 77001, Username: "qy_manual_topup", Status: common.UserStatusEnabled, Quota: 0}
	require.NoError(t, DB.Create(user).Error)

	prevQPU := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = prevQPU })

	topUp := &TopUp{
		UserId:          user.Id,
		Amount:          77,
		Money:           77,
		TradeNo:         "QY-MANUAL-77001",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      common.GetTimestamp() - 600,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())

	require.NoError(t, ManualCompleteTopUp("QY-MANUAL-77001", "127.0.0.1"))

	var reloaded TopUp
	require.NoError(t, DB.Where("trade_no = ?", "QY-MANUAL-77001").First(&reloaded).Error)
	assert.Equal(t, common.TopUpStatusSuccess, reloaded.Status)
	assert.Equal(t, TopUpCompleteSourceAdmin, reloaded.CompleteSource,
		"补单必须在订单行上留下真判据 —— payment_method 记的是用户选的支付方式,补单一个字都不改它")
	assert.Equal(t, "alipay", reloaded.PaymentMethod, "补单不得改写用户当初选的支付方式")

	var after User
	require.NoError(t, DB.Where("id = ?", user.Id).First(&after).Error)
	assert.Equal(t, 77*500000, after.Quota)
}

func seedQuotaRedemption(t *testing.T, id int, key string, quota int) *Redemption {
	t.Helper()
	r := &Redemption{
		Id:          id,
		UserId:      1,
		Key:         key,
		Status:      common.RedemptionCodeStatusEnabled,
		Name:        "qy-guard",
		Quota:       quota,
		CreatedTime: common.GetTimestamp(),
		ProductType: RedemptionProductQuota,
	}
	require.NoError(t, DB.Create(r).Error)
	// quota 列带 `default:100`,GORM 会把零值整列略过交给数据库补默认值,
	// 所以非正面额必须显式写回去 —— 这正是被测的那种码在库里的真实长相。
	require.NoError(t, DB.Model(&Redemption{}).Where("id = ?", id).Update("quota", quota).Error)
	r.Quota = quota
	return r
}

func TestRedeemRefusesCodesThatAlreadyCarryARedemptionTrace(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{
		Id: 77011, Username: "qy_redeem_first", AffCode: "qyaff77011", Status: common.UserStatusEnabled, Quota: 0,
	}).Error)
	require.NoError(t, DB.Create(&User{
		Id: 77012, Username: "qy_redeem_second", AffCode: "qyaff77012", Status: common.UserStatusEnabled, Quota: 0,
	}).Error)

	seedQuotaRedemption(t, 77020, "qyguardkey000000000000000000used", 1_000_000)

	res, err := Redeem("qyguardkey000000000000000000used", 77011)
	require.NoError(t, err)
	require.Equal(t, 1_000_000, res.Quota)

	// 管理端把状态硬写回启用(status_only 那条路在补上状态机之前正是这么做的),
	// redeemed_time / used_user_id 原封不动。
	require.NoError(t, DB.Model(&Redemption{}).Where("id = ?", 77020).
		Update("status", common.RedemptionCodeStatusEnabled).Error)

	_, err = Redeem("qyguardkey000000000000000000used", 77012)
	require.Error(t, err, "一张已经核销过的码不能因为状态被写回去就再兑一次")

	var second User
	require.NoError(t, DB.Where("id = ?", 77012).First(&second).Error)
	assert.Equal(t, 0, second.Quota, "第二个人一分钱都不该拿到")

	var code Redemption
	require.NoError(t, DB.Where("id = ?", 77020).First(&code).Error)
	assert.Equal(t, 77011, code.UsedUserId, "第一次兑换的核销痕迹不得被覆盖")
}

func TestRedeemRefusesNonPositiveQuotaCodes(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{
		Id: 77031, Username: "qy_redeem_neg", Status: common.UserStatusEnabled, Quota: 5_000_000,
	}).Error)

	for _, tc := range []struct {
		name  string
		id    int
		key   string
		quota int
	}{
		{"负面额的码兑换出去是一笔倒扣", 77040, "qyguardkey0000000000000000000neg", -500_000},
		{"零面额的码同样挡住", 77041, "qyguardkey00000000000000000zero0", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedQuotaRedemption(t, tc.id, tc.key, tc.quota)

			_, err := Redeem(tc.key, 77031)
			require.Error(t, err)

			var user User
			require.NoError(t, DB.Where("id = ?", 77031).First(&user).Error)
			assert.Equal(t, 5_000_000, user.Quota, "用户余额一分都不该动")

			var code Redemption
			require.NoError(t, DB.Where("id = ?", tc.id).First(&code).Error)
			assert.Equal(t, common.RedemptionCodeStatusEnabled, code.Status, "失败的兑换不消耗兑换码")
		})
	}
}
