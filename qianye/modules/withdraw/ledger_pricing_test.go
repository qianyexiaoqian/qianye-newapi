package withdraw

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ledger_pricing_test.go —— 「提现单上的金额 == 账本上被冻走的法币」。
//
// # 被测的缺陷(实测到打款)
//
// 提现单的应付金额此前是提现模块自己算的:quota / QuotaPerUnit × 充值页汇率;
// 而 qy_commission_balance.available_fiat 是按计佣当刻的三层折算比例
// (分组档 → 兜底档 → 全站汇率)一笔笔攒起来的绝对值,冻结时按额度比例等比削走。
// 两个数各算各的:
//
//   - 给 vip 配 8.5 的结汇比例 → 用户在「我的推广」页看到 ¥850,账本也确实冻走
//     850,而单据只让运营付 ¥100 —— 系统性少付 88%,「VIP 按更高比例结汇」
//     这个杠杆对打款完全失效;
//   - 管理员把充值汇率从 1 改到 7.3 → 同一笔按比例 1 冻结的余额开出 7.3 倍的单,
//     平台系统性超付。
//
// 默认配置(没配任何档 + 汇率恒定)下两者恰好相等,所以它是"配置即触发"的
// 潜伏错价,而不是装完就错 —— 因此这里的夹具必须让两侧**取值不同**,
// 否则测试永远是绿的。

// ledgerCfg 是一份开着法币方式、闸门全松的配置。
func ledgerCfg() config.Withdraw {
	return config.Withdraw{
		Enabled:       true,
		Methods:       []string{config.WithdrawMethodQuota, config.WithdrawMethodFiat},
		MinQuota:      500000,
		MinFiatAmount: "10",
		FiatCurrency:  "CNY",
	}
}

// seedFiatBalance 造一行带法币口径的佣金余额。fiat 是"这些额度在账本上折合多少法币"
// (concurrency_test.go 里那个 seedBalance 只给额度,法币恒为 0,不适用于法币单)。
func seedFiatBalance(t *testing.T, gdb *gorm.DB, userId int, quota int64, fiat, currency string) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&commission.Balance{
		UserId:         userId,
		AvailableQuota: quota,
		AvailableFiat:  decimal.RequireFromString(fiat),
		FiatCurrency:   currency,
		CreatedAt:      now,
		UpdatedAt:      now,
	}).Error)
}

func fiatOrder(userId int, quota int64, idem string) *Withdrawal {
	now := common.GetTimestamp()
	return &Withdrawal{
		WithdrawNo:         "WD-" + idem,
		IdemScope:          idemScope,
		IdemKey:            idemKeyOf(userId, idem),
		UserId:             userId,
		Method:             config.WithdrawMethodFiat,
		Status:             StatusPending,
		Quota:              quota,
		FrozenQuotaPerUnit: decimal.NewFromInt(500000),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func balanceOf(t *testing.T, gdb *gorm.DB, userId int) commission.Balance {
	t.Helper()
	var bal commission.Balance
	require.NoError(t, gdb.Where("user_id = ?", userId).Take(&bal).Error)
	return bal
}

// 全额提现:单据金额必须等于被冻走的那一笔,而不是按充值汇率现算的数。
func TestPriceFiatInTx_AmountComesFromLedgerNotRechargeRate(t *testing.T) {
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = original })
	// 充值页汇率 = 1。若单据仍按它算,50,000,000 / 500000 × 1 = 100 —— 那正是缺陷值。
	operation_setting.USDExchangeRate = 1

	gdb := newTestDB(t)
	// 账本按分组档 8.5 攒出来的:50,000,000 额度 ↔ 850 CNY。
	seedFiatBalance(t, gdb, 7, 50000000, "850", "CNY")

	w := fiatOrder(7, 50000000, "idem-full")
	acc := acceptedRequest{IdemKey: "idem-full", Method: config.WithdrawMethodFiat, Quota: 50000000}
	replay, err := submitInTx(gdb, w, nil, acc, ledgerCfg(), "u")
	require.NoError(t, err)
	require.Nil(t, replay)

	assert.True(t, w.GrossAmount.Equal(decimal.RequireFromString("850")),
		"单据应付 %s ≠ 账本冻走的 850 —— 这正是「账面 850、单据 100」那个错价", w.GrossAmount)
	assert.True(t, w.NetAmount.Equal(decimal.RequireFromString("850")))
	assert.Equal(t, "CNY", w.Currency)
	assert.True(t, w.FrozenFxRate.Equal(decimal.RequireFromString("8.5")),
		"冻结汇率应当由金额反解出来,得到 %s", w.FrozenFxRate)

	// 账本侧:850 一分不剩地被冻走,冻结流水记的也是 850。
	bal := balanceOf(t, gdb, 7)
	assert.EqualValues(t, 0, bal.AvailableQuota)
	assert.True(t, bal.AvailableFiat.IsZero(), "available_fiat=%s", bal.AvailableFiat)
	assert.EqualValues(t, 50000000, bal.FrozenQuota)

	var frz commission.FreezeRecord
	require.NoError(t, gdb.Where("ref_no = ? AND action = ?", w.WithdrawNo, commission.FreezeActionFreeze).
		Take(&frz).Error)
	assert.True(t, frz.Fiat.Equal(w.GrossAmount),
		"冻结流水 %s 与单据应付 %s 必须是同一个数", frz.Fiat, w.GrossAmount)
}

// 部分提现:按额度比例削,单据金额同样等于被削掉的那一笔。
// 手续费从这个数里扣,而不是从另算的数里扣。
func TestPriceFiatInTx_PartialWithdrawTakesTheProportionalShare(t *testing.T) {
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = original })
	operation_setting.USDExchangeRate = 7.3 // 与账本比例(8.5)不同,故意的

	gdb := newTestDB(t)
	seedFiatBalance(t, gdb, 8, 50000000, "850", "CNY")

	cfg := ledgerCfg()
	cfg.FiatFeeBps = 200 // 2%

	w := fiatOrder(8, 20000000, "idem-part")
	acc := acceptedRequest{IdemKey: "idem-part", Method: config.WithdrawMethodFiat, Quota: 20000000}
	_, err := submitInTx(gdb, w, nil, acc, cfg, "u")
	require.NoError(t, err)

	// 850 × 20,000,000 / 50,000,000 = 340
	assert.True(t, w.GrossAmount.Equal(decimal.RequireFromString("340")), "gross=%s", w.GrossAmount)
	assert.True(t, w.FeeAmount.Equal(decimal.RequireFromString("6.8")), "fee=%s", w.FeeAmount)
	assert.True(t, w.NetAmount.Equal(decimal.RequireFromString("333.2")), "net=%s", w.NetAmount)
	assert.True(t, w.GrossAmount.Equal(w.FeeAmount.Add(w.NetAmount)))

	bal := balanceOf(t, gdb, 8)
	assert.EqualValues(t, 30000000, bal.AvailableQuota)
	assert.True(t, bal.AvailableFiat.Equal(decimal.RequireFromString("510")),
		"剩余法币 %s 应当是 850-340;两侧同源才对得上", bal.AvailableFiat)
}

// 币种跟着账本走,不跟着当前配置走。
//
// 运营改一次 withdraw.fiat_currency,历史金额一个数字没动、标签全变了 ——
// 那是 frozenFiatCurrency 早就定下的口径,提现单必须沿用同一列。
func TestPriceFiatInTx_CurrencyComesFromTheLedgerRow(t *testing.T) {
	gdb := newTestDB(t)
	seedFiatBalance(t, gdb, 9, 50000000, "850", "USD")

	cfg := ledgerCfg() // 配置里写的是 CNY
	w := fiatOrder(9, 50000000, "idem-cur")
	acc := acceptedRequest{IdemKey: "idem-cur", Method: config.WithdrawMethodFiat, Quota: 50000000}
	_, err := submitInTx(gdb, w, nil, acc, cfg, "u")
	require.NoError(t, err)

	assert.Equal(t, "USD", w.Currency, "币种必须来自账本那一列,而不是当前配置")
}

// 账本折不出正的法币值时必须拒绝建单,而不是开一张 0 元的打款单。
//
// 这是新字段的零值口径:available_fiat = 0 且 available_quota > 0,只可能是
// 三层比例在那段时间全部拿不出正数(fiatLayerNone),或者是本档上线前的存量行。
// 两种都不该被翻译成"这笔佣金一分钱都不值"。
func TestPriceFiatInTx_RejectsZeroLedgerFiat(t *testing.T) {
	gdb := newTestDB(t)
	seedFiatBalance(t, gdb, 10, 50000000, "0", "CNY")

	w := fiatOrder(10, 50000000, "idem-zero")
	acc := acceptedRequest{IdemKey: "idem-zero", Method: config.WithdrawMethodFiat, Quota: 50000000}
	_, err := submitInTx(gdb, w, nil, acc, ledgerCfg(), "u")
	assert.ErrorIs(t, err, commission.ErrFiatUnavailable)

	// 零副作用:既没有落单,也没有动账本。
	var cnt int64
	require.NoError(t, gdb.Model(&Withdrawal{}).Count(&cnt).Error)
	assert.EqualValues(t, 0, cnt)
	assert.EqualValues(t, 50000000, balanceOf(t, gdb, 10).AvailableQuota)
}

// 最低法币金额判的是账本口径的应付金额。
func TestPriceFiatInTx_MinAmountIsJudgedOnLedgerAmount(t *testing.T) {
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = original })
	// 按充值汇率算是 730(过线),按账本只有 5(不过线)。
	operation_setting.USDExchangeRate = 7.3

	gdb := newTestDB(t)
	seedFiatBalance(t, gdb, 11, 50000000, "5", "CNY")

	w := fiatOrder(11, 50000000, "idem-min")
	acc := acceptedRequest{IdemKey: "idem-min", Method: config.WithdrawMethodFiat, Quota: 50000000}
	_, err := submitInTx(gdb, w, nil, acc, ledgerCfg(), "u")
	assert.ErrorIs(t, err, errFiatBelowMin)
}

// quota 方式不碰法币侧:没有金额、没有币种、没有汇率,也不该因为账本法币为 0 被拒。
func TestPriceFiatInTx_SkipsQuotaOrders(t *testing.T) {
	gdb := newTestDB(t)
	seedFiatBalance(t, gdb, 12, 50000000, "0", "")

	w := fiatOrder(12, 50000000, "idem-quota")
	w.Method = config.WithdrawMethodQuota
	acc := acceptedRequest{IdemKey: "idem-quota", Method: config.WithdrawMethodQuota, Quota: 50000000}
	_, err := submitInTx(gdb, w, nil, acc, ledgerCfg(), "u")
	require.NoError(t, err)

	assert.True(t, w.GrossAmount.IsZero())
	assert.True(t, w.FrozenFxRate.IsZero())
	assert.Empty(t, w.Currency)
}
