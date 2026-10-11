package commission

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件守「下线买套餐」这一档计佣的全部口径:
//   - 三条渠道(在线 / 余额 / 套餐兑换码)各走各的档,管理员绑定不计;
//   - 在线那档没单独配时跟随充值档;
//   - 在线买套餐在 top_ups 里的那一行不再按充值重复计;
//   - 套餐额度的消费由 exclude_subscription_consume 决定计不计,且该开关可在管理端改。

func accrualsBySource(t *testing.T, gdb *gorm.DB, source string) []Accrual {
	t.Helper()
	var rows []Accrual
	require.NoError(t, gdb.Where("source_type = ?", source).Order("id asc").Find(&rows).Error)
	return rows
}

func TestPlanChannelMapping(t *testing.T) {
	cases := []struct {
		name   string
		g      model.QySubscriptionGrant
		source string
		key    string
		ref    string
		money  string
		cash   bool
		ok     bool
	}{
		{"在线支付按实付", model.QySubscriptionGrant{Source: stardust.PlanSourceOrder, TradeNo: "SUB1", Money: 8, PriceAmount: 10},
			SourcePlanOrder, "plan_order:SUB1", "SUB1", "8", true, true},
		{"在线支付实付缺失时按售价", model.QySubscriptionGrant{Source: stardust.PlanSourceOrder, TradeNo: "SUB2", PriceAmount: 10},
			SourcePlanOrder, "plan_order:SUB2", "SUB2", "10", true, true},
		{"余额按实扣的余额价", model.QySubscriptionGrant{Source: stardust.PlanSourceBalance, TradeNo: "SUB3", Money: 6, PriceAmount: 10},
			SourcePlanBalance, "plan_balance:SUB3", "SUB3", "6", false, true},
		{"兑换码按售价", model.QySubscriptionGrant{Source: stardust.PlanSourceRedemption, RedemptionId: 7, PriceAmount: 20},
			SourcePlanRedemption, "plan_redemption:7", "RD7", "20", false, true},
		{"管理员绑定不计", model.QySubscriptionGrant{Source: stardust.PlanSourceAdmin, PriceAmount: 20}, "", "", "", "0", false, false},
		{"缺订单号不计", model.QySubscriptionGrant{Source: stardust.PlanSourceOrder, Money: 8}, "", "", "", "0", false, false},
		{"商城自购不计", model.QySubscriptionGrant{Source: "stardust", TradeNo: "SD1", PriceAmount: 10}, "", "", "", "0", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, key, ref, money, cash, ok := planChannel(tc.g)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.source, source)
			assert.Equal(t, tc.key, key)
			assert.Equal(t, tc.ref, ref)
			assert.Equal(t, tc.money, money.String())
			assert.Equal(t, tc.cash, cash)
		})
	}
}

// TestPlanAccrualPerChannel 让三条渠道真的写进库,再回读冻结下来的档位与毛额。
func TestPlanAccrualPerChannel(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 0, 100) // $1 = 100 额度;星屑刻度 1
	withCompliance(t, true)
	setSettingOverride(t, gdb, keyPlanBalanceRatePercent, "3")
	setSettingOverride(t, gdb, keyPlanRedemptionRatePercent, "2")
	cacheUser(t, 42, 0, "vip")
	cacheUser(t, 900, 42, "default")
	ctx := context.Background()

	order := model.QySubscriptionGrant{UserId: 900, PlanId: 1, Source: stardust.PlanSourceOrder, TradeNo: "SUB1", Money: 10, PriceAmount: 12}
	require.NoError(t, accruePlanGrant(ctx, order))
	require.NoError(t, accruePlanGrant(ctx, order), "同一订单重投")
	rows := accrualsBySource(t, gdb, SourcePlanOrder)
	require.Len(t, rows, 1, "幂等:同一订单只落一行")
	assert.Equal(t, 1000, rows[0].RateUnits, "在线那档没单独配 ⇒ 跟随充值档 10%")
	assert.EqualValues(t, 1000, rows[0].BaseQuota, "$10 实付 × 100")
	assert.Equal(t, "100", rows[0].GrossAmount.String())
	assert.Equal(t, "10", rows[0].BaseMoney.String(), "在线单留下付款金额")
	assert.Equal(t, 42, rows[0].InviterId)

	balance := model.QySubscriptionGrant{UserId: 900, PlanId: 1, Source: stardust.PlanSourceBalance, TradeNo: "SUB2", Money: 6, PriceAmount: 12}
	require.NoError(t, accruePlanGrant(ctx, balance))
	rows = accrualsBySource(t, gdb, SourcePlanBalance)
	require.Len(t, rows, 1)
	assert.Equal(t, 300, rows[0].RateUnits, "余额购买走自己那档 3%,不是充值档")
	assert.EqualValues(t, 600, rows[0].BaseQuota, "按实扣的余额购买价 $6")
	assert.Equal(t, "18", rows[0].GrossAmount.String())
	assert.True(t, rows[0].BaseMoney.IsZero(), "余额不是这一刻的进账")

	redeem := model.QySubscriptionGrant{UserId: 900, PlanId: 1, Source: stardust.PlanSourceRedemption, RedemptionId: 5, PriceAmount: 20}
	require.NoError(t, accruePlanGrant(ctx, redeem))
	rows = accrualsBySource(t, gdb, SourcePlanRedemption)
	require.Len(t, rows, 1)
	assert.Equal(t, 200, rows[0].RateUnits)
	assert.EqualValues(t, 2000, rows[0].BaseQuota, "兑换码按套餐售价 $20")
	assert.Equal(t, "40", rows[0].GrossAmount.String())
	assert.Equal(t, "RD5", rows[0].SourceRef)

	admin := model.QySubscriptionGrant{UserId: 900, PlanId: 1, Source: stardust.PlanSourceAdmin, PriceAmount: 20}
	require.NoError(t, accruePlanGrant(ctx, admin))

	// 单独配了在线那档之后按它算,不再跟随充值档。
	setSettingOverride(t, gdb, keyPlanOrderRatePercent, "5")
	order2 := model.QySubscriptionGrant{UserId: 900, PlanId: 1, Source: stardust.PlanSourceOrder, TradeNo: "SUB3", Money: 10}
	require.NoError(t, accruePlanGrant(ctx, order2))
	rows = accrualsBySource(t, gdb, SourcePlanOrder)
	require.Len(t, rows, 2)
	assert.Equal(t, 500, rows[1].RateUnits)

	var total int64
	require.NoError(t, gdb.Model(&Accrual{}).Count(&total).Error)
	assert.EqualValues(t, 4, total, "管理员绑定不计佣")
}

// TestPlanAccrualZeroRateWritesNothing:余额与兑换码两档默认 0 —— 升级之后不配就不发。
func TestPlanAccrualZeroRateWritesNothing(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 0, 100)
	withCompliance(t, true)
	cacheUser(t, 42, 0, "default")
	cacheUser(t, 900, 42, "default")
	ctx := context.Background()

	require.NoError(t, accruePlanGrant(ctx, model.QySubscriptionGrant{UserId: 900, Source: stardust.PlanSourceBalance, TradeNo: "B1", Money: 6}))
	require.NoError(t, accruePlanGrant(ctx, model.QySubscriptionGrant{UserId: 900, Source: stardust.PlanSourceRedemption, RedemptionId: 1, PriceAmount: 6}))
	var total int64
	require.NoError(t, gdb.Model(&Accrual{}).Count(&total).Error)
	assert.Zero(t, total)
}

// TestPlanAccrualRespectsCompliance:合规条款没确认,三条渠道一律不计(与充值 / 消费同一道闸)。
func TestPlanAccrualRespectsCompliance(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 0, 100)
	withCompliance(t, false)
	setSettingOverride(t, gdb, keyPlanBalanceRatePercent, "3")
	cacheUser(t, 42, 0, "default")
	cacheUser(t, 900, 42, "default")
	ctx := context.Background()

	require.NoError(t, accruePlanGrant(ctx, model.QySubscriptionGrant{UserId: 900, Source: stardust.PlanSourceOrder, TradeNo: "O1", Money: 10}))
	require.NoError(t, accruePlanGrant(ctx, model.QySubscriptionGrant{UserId: 900, Source: stardust.PlanSourceBalance, TradeNo: "B1", Money: 6}))
	assert.Empty(t, accrualsBySource(t, gdb, SourcePlanOrder))
	assert.Empty(t, accrualsBySource(t, gdb, SourcePlanBalance))
}

// TestTopUpScanSkipsSubscriptionOrder:在线买套餐的付费单在 top_ups 里也有一行,
// 它已经按套餐档计过,充值扫描不能再按充值档计一次。
func TestTopUpScanSkipsSubscriptionOrder(t *testing.T) {
	gdb := newTestDB(t)
	mdb := useMainDB(t, &model.TopUp{}, &model.SubscriptionOrder{})
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 0, 100)
	withCompliance(t, true)
	cacheUser(t, 42, 0, "default")
	cacheUser(t, 900, 42, "default")
	ctx := context.Background()

	require.NoError(t, mdb.Create(&model.SubscriptionOrder{UserId: 900, PlanId: 1, Money: 10, TradeNo: "SUBX",
		PaymentMethod: "alipay", Status: common.TopUpStatusSuccess}).Error)
	plan := &model.TopUp{Id: 1, UserId: 900, Money: 10, TradeNo: "SUBX", PaymentMethod: "alipay", Status: common.TopUpStatusSuccess}
	// 同样是 Amount=0、provider 为空的形状,但不是套餐单(历史 stripe 一类):照常按充值计。
	plain := &model.TopUp{Id: 2, UserId: 900, Money: 5, TradeNo: "T2", PaymentMethod: "alipay", Status: common.TopUpStatusSuccess}
	require.NoError(t, accrueTopUp(ctx, plan))
	require.NoError(t, accrueTopUp(ctx, plain))

	rows := accrualsBySource(t, gdb, SourceTopup)
	require.Len(t, rows, 1)
	assert.Equal(t, "T2", rows[0].SourceRef)
}

// TestConsumeSubscriptionSwitchIsLive:exclude_subscription_consume 在管理端改了之后
// 立刻对下一笔套餐额度消费生效(读的是 effective,不是 YAML 快照)。
func TestConsumeSubscriptionSwitchIsLive(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	cacheUser(t, 42, 0, "default")
	cacheUser(t, 900, 42, "default")
	cacheUser(t, 901, 42, "default")
	ctx := context.Background()
	now := common.GetTimestamp()

	require.False(t, effective().ExcludeSubscriptionConsume, "前提:YAML 基线是关")
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 900, Quota: 10000, At: now, Subscription: true}))
	assert.Len(t, accrualsBySource(t, gdb, SourceConsume), 1, "开关关着:套餐额度消费照常计")

	setSettingOverride(t, gdb, keyExcludeSubscriptionConsume, "1")
	require.True(t, effective().ExcludeSubscriptionConsume)
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 901, Quota: 10000, At: now, Subscription: true}))
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 901, Quota: 10000, At: now}))
	rows := accrualsBySource(t, gdb, SourceConsume)
	require.Len(t, rows, 2, "开关打开:套餐额度消费不计,余额消费照常计")
	assert.Equal(t, 901, rows[1].InviteeId)
	assert.EqualValues(t, 10000, rows[1].BaseQuota, "只有那笔余额消费进了基数")
}

// TestAdminPutSwitchesAndPlanRates 守管理端那条写入路:开关只收开 / 关、
// 套餐在线档可清空回到跟随、GET 下发 bool_keys 与生效值。
func TestAdminPutSwitchesAndPlanRates(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useAdminAPI(t)

	rec := callAdminHandler(t, http.MethodPut, "/admin/commission/config",
		`{"exclude_subscription_consume":true,"refund_clawback":"0","plan_order_rate_percent":"6","plan_balance_rate_percent":"2.5","plan_redemption_rate_percent":"1"}`,
		adminPutConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s := effective()
	assert.True(t, s.ExcludeSubscriptionConsume)
	assert.False(t, s.RefundClawback)
	require.NotNil(t, s.PlanOrderRateUnits)
	assert.Equal(t, 600, *s.PlanOrderRateUnits)
	assert.Equal(t, 250, s.PlanBalanceRateUnits)
	assert.Equal(t, 100, s.PlanRedemptionRateUnits)

	var row []struct{ V string }
	require.NoError(t, gdb.Table("qy_settings").Select("v").
		Where("scope = ? AND k = ?", settingScope, keyExcludeSubscriptionConsume).Scan(&row).Error)
	require.Len(t, row, 1)
	assert.Equal(t, "1", row[0].V, "开关规范化成 1 / 0 落库")

	rec = callAdminHandler(t, http.MethodPut, "/admin/commission/config", `{"refund_clawback":"maybe"}`, adminPutConfig)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = callAdminHandler(t, http.MethodPut, "/admin/commission/config", `{"plan_balance_rate_percent":""}`, adminPutConfig)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "余额档不可空:清空输入框不是「跟随」")

	rec = callAdminHandler(t, http.MethodPut, "/admin/commission/config", `{"plan_order_rate_percent":null}`, adminPutConfig)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s = effective()
	assert.Nil(t, s.PlanOrderRateUnits, "清空 ⇒ 跟随充值档")
	assert.Equal(t, 1000, s.EffectivePlanOrderRateUnits())

	rec = callAdminHandler(t, http.MethodGet, "/admin/commission/config", "", adminGetConfig)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"bool_keys":["exclude_subscription_consume","exclude_redemption_and_manual","refund_clawback"]`)
	assert.Contains(t, body, `"plan_order_rate_effective_percent":"10"`)
	assert.Contains(t, body, `"plan_order_rate_follows_topup":true`)
	assert.Contains(t, body, `"exclude_subscription_consume":true`)
	assert.NotContains(t, body, `"yaml_readonly":{"exclude`, "三个开关不再只读")
}
