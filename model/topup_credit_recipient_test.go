package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 收款人不在时，充值结算必须整笔回滚，不能写成 success。
//
// users 带 gorm.DeletedAt 软删列，GORM 会给 UPDATE 自动补 `AND deleted_at IS
// NULL`。收款人在“打开收银台”与“网关回调到达”之间被删/被软删时，那条
// `quota = quota + X` 匹配 0 行且**不报错**：事务照常提交，订单写成 success、
// CompleteTime 落库、还记一条“充值成功”日志，而钱一分没进任何人钱包；
// 网关侧幂等会让重投同样认为已经成功，钱找不回来。
//
// 五条结算路径原先只有 epay 判了 RowsAffected，其余四条（stripe / creem /
// waffo / waffo-pancake）与管理员补单都只判 error。
func TestRechargePathsRefuseToSettleWhenTheRecipientIsGone(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		settle   func(tradeNo string) error
	}{
		{"epay", PaymentProviderEpay, func(no string) error {
			_, err := RechargeEpay(no, "alipay", "127.0.0.1")
			return err
		}},
		{"stripe", PaymentProviderStripe, func(no string) error {
			return Recharge(no, "cus_x", "127.0.0.1")
		}},
		{"creem", PaymentProviderCreem, func(no string) error {
			return RechargeCreem(no, "", "", "127.0.0.1")
		}},
		{"waffo", PaymentProviderWaffo, func(no string) error {
			return RechargeWaffo(no, "127.0.0.1")
		}},
		{"waffo-pancake", PaymentProviderWaffoPancake, func(no string) error {
			return RechargeWaffoPancake(no)
		}},
		{"管理员补单", PaymentProviderEpay, func(no string) error {
			return ManualCompleteTopUp(no, "127.0.0.1")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			user := &User{Id: 0, Username: "qy-gone-" + tc.name, Group: "default", Status: common.UserStatusEnabled}
			require.NoError(t, DB.Create(user).Error)

			tradeNo := "QYT-" + tc.name
			order := &TopUp{
				UserId: user.Id, Amount: 10, Money: 10,
				TradeNo: tradeNo, PaymentMethod: "alipay",
				PaymentProvider: tc.provider, Status: common.TopUpStatusPending,
			}
			require.NoError(t, order.Insert())

			// 收款人消失（软删，与管理端 ManageUser action=delete 同一条路）。
			require.NoError(t, DB.Delete(&User{}, user.Id).Error)

			err := tc.settle(tradeNo)
			assert.Error(t, err, "收款人不在时必须报错,而不是静默把订单写成 success")

			var after TopUp
			require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&after).Error)
			assert.Equal(t, common.TopUpStatusPending, after.Status,
				"额度一分没到账,订单绝不能停在 success —— 那会让网关重投也认为已完成")
			assert.Zero(t, after.CompleteTime, "没到账就不该写完成时间")

			var logs int64
			require.NoError(t, DB.Model(&Log{}).Where("type = ?", LogTypeTopup).Count(&logs).Error)
			assert.Zero(t, logs, "钱没到账却记一条充值成功日志,是在账上伪造凭据")
		})
	}
}
