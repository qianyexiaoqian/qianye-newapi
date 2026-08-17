package model

// subscription_sale_window_test.go —— 套餐的发售时间窗。
//
// 三层,少一层都会漏掉一整类缺陷:
//
//	纯判据   PlanSaleWindowError / ValidatePlanSaleWindow 的真值表,含 0 与边界
//	购买路径 时间窗真的挡住了付款(余额购买 / 兑换码),且拒绝时一分钱没动
//	存量保护 停售之后,**已经买到手的订阅照常有效** —— 这条是项目方的硬要求
//
// 只有第一层的话,一个"判据写对了但没人调用"的实现会全绿;只有第二层的话,
// 0 表示不限的方向不对称(见 SubscriptionPlan.SaleEndAt 注释)会溜过去。

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanSaleWindowError 是「此刻能不能新买」的真值表。
//
// now 固定成 1_700_000_000,窗口两端全部相对它写死 —— 不用 time.Now():
// 取真实时钟会让"未开售"与"已停售"这两档在 CI 的某一秒变成对方。
func TestPlanSaleWindowError(t *testing.T) {
	const now int64 = 1_700_000_000

	cases := []struct {
		name    string
		startAt int64
		endAt   int64
		wantErr error
	}{
		{
			name:    "两端都不限:随时可买(存量套餐迁移后的取值)",
			startAt: 0,
			endAt:   0,
			wantErr: nil,
		},
		{
			name:    "窗口内",
			startAt: now - 3600,
			endAt:   now + 3600,
			wantErr: nil,
		},
		{
			name:    "未开售",
			startAt: now + 1,
			endAt:   now + 3600,
			wantErr: ErrPlanNotOnSaleYet,
		},
		{
			name:    "已停售",
			startAt: now - 3600,
			endAt:   now - 1,
			wantErr: ErrPlanSaleEnded,
		},
		{
			// 左闭:开售那一秒就能买。写成 now <= start 的话,运营配的
			// "10:00 开售"实际要等到 10:00:01,而抢购型套餐就差这一秒。
			name:    "边界:此刻正好是开售时刻,可买",
			startAt: now,
			endAt:   now + 3600,
			wantErr: nil,
		},
		{
			// 右开:停售那一秒已经买不了。与 SubscriptionActiveEndTimeSQL 的
			// `end_time > ?` 同口径,两处对"到点"的理解必须一致。
			name:    "边界:此刻正好是停售时刻,不可买",
			startAt: now - 3600,
			endAt:   now,
			wantErr: ErrPlanSaleEnded,
		},
		{
			// **这一条是本文件最重要的一行。** SaleEndAt 上的 0 如果没有被特判成
			// 「不限」,`now >= 0` 恒真 —— 全站每一个没配停售时间的套餐(也就是
			// 迁移后的全部存量套餐)会在这一秒集体停售。
			name:    "只设开售、停售不限:开售后永远可买",
			startAt: now - 1,
			endAt:   0,
			wantErr: nil,
		},
		{
			name:    "只设开售、停售不限:开售前仍然不可买",
			startAt: now + 1,
			endAt:   0,
			wantErr: ErrPlanNotOnSaleYet,
		},
		{
			name:    "只设停售、开售不限:停售前可买",
			startAt: 0,
			endAt:   now + 1,
			wantErr: nil,
		},
		{
			name:    "只设停售、开售不限:停售后不可买",
			startAt: 0,
			endAt:   now,
			wantErr: ErrPlanSaleEnded,
		},
		{
			// 未开售的优先级高于已停售。两端都错配时(理论上被
			// ValidatePlanSaleWindow 挡住,但存量脏数据可能绕过校验),
			// 报"尚未开售"比报"已停售"更接近真相:这个套餐一天都没卖过。
			name:    "脏数据:停售早于发售,报未开售",
			startAt: now + 10,
			endAt:   now - 10,
			wantErr: ErrPlanNotOnSaleYet,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := &SubscriptionPlan{Id: 1, SaleStartAt: tc.startAt, SaleEndAt: tc.endAt}
			err := PlanSaleWindowError(plan, now)
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}

	// plan 为 nil 时放行:调用点的上一行往往是 GetSubscriptionPlanById,它失败时
	// plan 是 nil 而 err 非空。在这里 panic 或报"未开售"都会盖住真正的原因。
	assert.NoError(t, PlanSaleWindowError(nil, now))
}

// TestValidatePlanSaleWindow 钉住管理端保存时的校验。
func TestValidatePlanSaleWindow(t *testing.T) {
	cases := []struct {
		name    string
		startAt int64
		endAt   int64
		wantErr bool
	}{
		{name: "两端都不限", startAt: 0, endAt: 0, wantErr: false},
		{name: "正常窗口", startAt: 1000, endAt: 2000, wantErr: false},
		{name: "只设开售", startAt: 1000, endAt: 0, wantErr: false},
		{
			// 这一条是最容易被写坏的:把判断写成 `endAt <= startAt` 而不先排除
			// 0,"只设停售时间"就会被算成 0 <= 1000 之外的另一支而误拒 ——
			// 表现是运营配了停售时间却怎么都保存不上,报错还说"停售必须晚于发售"。
			name: "只设停售(开售不限)", startAt: 0, endAt: 1000, wantErr: false,
		},
		{name: "停售早于发售", startAt: 2000, endAt: 1000, wantErr: true},
		{
			// 相等 = 空窗口。窗口左闭右开,[X, X) 里没有任何一秒可以买,
			// 而管理端上它看起来完全像是配好了。
			name: "停售等于发售(空窗口)", startAt: 1000, endAt: 1000, wantErr: true,
		},
		{name: "发售为负", startAt: -1, endAt: 0, wantErr: true},
		{name: "停售为负", startAt: 0, endAt: -1, wantErr: true},
		{name: "上界之内", startAt: 0, endAt: PlanSaleWindowMaxUnix, wantErr: false},
		{
			// 毫秒时间戳粘错格子的典型值(1.7e12)。不挡住的话,套餐落库之后
			// 前端 new Date() 直接是 Invalid Date,那一格渲染成空白 ——
			// 运营看不出配错了,只看到套餐永远不开售。
			name: "毫秒时间戳粘进秒字段", startAt: 1_700_000_000_000, endAt: 0, wantErr: true,
		},
		{name: "超上界", startAt: 0, endAt: PlanSaleWindowMaxUnix + 1, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePlanSaleWindow(tc.startAt, tc.endAt)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// seedSaleWindowPlan 建一个套餐并把发售时间窗**显式写一次**。
//
// 显式写第二次不是啰嗦:两列带 `not null;default:0`,GORM 的 Create 会把 0
// 当零值交给列默认值(结果一样),但非 0 值也可能因为将来加上别的 tag 而被
// 略过 —— 与 seedRedeemPlan 处理 enabled 的理由同源。一次 Updates 让这个夹具
// 与"库里到底存了什么"之间不留猜测空间。
func seedSaleWindowPlan(t *testing.T, plan *SubscriptionPlan, startAt, endAt int64) *SubscriptionPlan {
	t.Helper()
	require.NoError(t, DB.Create(plan).Error)
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", plan.Id).
		Updates(map[string]any{"sale_start_at": startAt, "sale_end_at": endAt}).Error)
	plan.SaleStartAt, plan.SaleEndAt = startAt, endAt
	InvalidateSubscriptionPlanCache(plan.Id)
	t.Cleanup(func() { InvalidateSubscriptionPlanCache(plan.Id) })
	return plan
}

// TestPurchaseSubscriptionWithBalanceRespectsSaleWindow 时间窗真的挡在付款前面。
//
// 三个断言各有分工:被拒、**钱没扣**、没发出订阅。只断言"报错了"是不够的 ——
// 本仓的扣款与发货在同一个事务里,一次写在扣款之后的拒绝同样会报错,
// 但那时用户看到的是"扣款失败"而不是"未开售",且依赖回滚才没丢钱。
func TestPurchaseSubscriptionWithBalanceRespectsSaleWindow(t *testing.T) {
	now := common.GetTimestamp()

	cases := []struct {
		name    string
		planId  int
		startAt int64
		endAt   int64
		wantErr error
	}{
		{name: "未开售", planId: 7401, startAt: now + 3600, endAt: 0, wantErr: ErrPlanNotOnSaleYet},
		{name: "已停售", planId: 7402, startAt: 0, endAt: now - 1, wantErr: ErrPlanSaleEnded},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withMultiConnDB(t)
			seedSaleWindowPlan(t, &SubscriptionPlan{
				Id: tc.planId, Title: "时间窗套餐", PriceAmount: 1,
				DurationUnit: SubscriptionDurationMonth, DurationValue: 1,
				TotalAmount: 1000, Enabled: true,
			}, tc.startAt, tc.endAt)

			user := &User{Username: "qy-salewindow", Password: "password",
				Status: common.UserStatusEnabled, Quota: 10_000_000, Group: "default"}
			require.NoError(t, DB.Create(user).Error)

			err := PurchaseSubscriptionWithBalance(user.Id, tc.planId)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErr)

			var after User
			require.NoError(t, DB.First(&after, "id = ?", user.Id).Error)
			assert.Equal(t, 10_000_000, after.Quota, "被时间窗拒绝时一分钱都不该动")

			var subCount int64
			require.NoError(t, DB.Model(&UserSubscription{}).
				Where("user_id = ?", user.Id).Count(&subCount).Error)
			assert.Zero(t, subCount)

			var orderCount int64
			require.NoError(t, DB.Model(&SubscriptionOrder{}).
				Where("user_id = ?", user.Id).Count(&orderCount).Error)
			assert.Zero(t, orderCount, "拒绝的购买不该在订单表里留下成功记录")
		})
	}

	t.Run("窗口内照常成交", func(t *testing.T) {
		withMultiConnDB(t)
		seedSaleWindowPlan(t, &SubscriptionPlan{
			Id: 7403, Title: "在售套餐", PriceAmount: 1,
			DurationUnit: SubscriptionDurationMonth, DurationValue: 1,
			TotalAmount: 1000, Enabled: true,
		}, now-3600, now+3600)

		user := &User{Username: "qy-salewindow-ok", Password: "password",
			Status: common.UserStatusEnabled, Quota: 10_000_000, Group: "default"}
		require.NoError(t, DB.Create(user).Error)

		require.NoError(t, PurchaseSubscriptionWithBalance(user.Id, 7403))

		var sub UserSubscription
		require.NoError(t, DB.Where("user_id = ?", user.Id).First(&sub).Error)
		assert.Equal(t, "active", sub.Status)
		assert.EqualValues(t, 1000, sub.AmountTotal)
	})
}

// TestSaleWindowDoesNotAffectPurchasedSubscription 停售**绝不作废已购订阅**。
//
// 项目方原话:「已经买了的人,订阅照常有效到期」。这条用例是那句话的机器版本:
// 先在窗口内正常买一份,再把停售时间推到过去(模拟到点自动停售),然后确认
//
//	订阅仍然 status=active、end_time 未被改动
//	GetAllActiveUserSubscriptions 仍然数得到它(热路径判"有没有可用订阅"就靠它)
//	但同一个人**再买一次**会被拒 —— 也就是"续费算新购买"这条判断
//
// 前两点保证不误伤,第三点保证时间窗真的起了作用。少了第三点,一个把
// PlanSaleWindowError 整个删掉的实现也能让前两点全绿。
func TestSaleWindowDoesNotAffectPurchasedSubscription(t *testing.T) {
	withMultiConnDB(t)
	now := common.GetTimestamp()
	seedSaleWindowPlan(t, &SubscriptionPlan{
		Id: 7404, Title: "即将停售的月付", PriceAmount: 1,
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1,
		TotalAmount: 1000, Enabled: true,
	}, 0, now+3600)

	user := &User{Username: "qy-salewindow-holder", Password: "password",
		Status: common.UserStatusEnabled, Quota: 10_000_000, Group: "default"}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, PurchaseSubscriptionWithBalance(user.Id, 7404))

	var before UserSubscription
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&before).Error)
	require.Greater(t, before.EndTime, now, "夹具前提:买到的是一份还没到期的订阅")

	// 到点停售。
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", 7404).
		Update("sale_end_at", now-1).Error)
	InvalidateSubscriptionPlanCache(7404)

	var after UserSubscription
	require.NoError(t, DB.Where("id = ?", before.Id).First(&after).Error)
	assert.Equal(t, "active", after.Status, "停售不得改动任何已售出订阅的状态")
	assert.Equal(t, before.EndTime, after.EndTime, "停售不得把已售出订阅的到期时间提前")

	actives, err := GetAllActiveUserSubscriptions(user.Id)
	require.NoError(t, err)
	assert.Len(t, actives, 1, "停售后这份订阅必须仍然被算作生效中,否则用户当场用不了")

	err = PurchaseSubscriptionWithBalance(user.Id, 7404)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPlanSaleEnded, "续费与首购走同一条路径,停售之后一并拦下")
}

// TestRedeemPlanRollsBackWhenPlanSaleEnded 停售的套餐码兑不出货,且**码不被消耗**。
//
// 与 TestRedeemPlanRollsBackWhenPlanDisabled 同一个形状:检查排在 CAS 之前,
// 于是失败时码仍然是 enabled。运营把停售时间改回去之后,那批已经发出去的码
// 照样能用 —— 少了这条保证,一次停售配置会把手里的存量码全部烧掉。
func TestRedeemPlanRollsBackWhenPlanSaleEnded(t *testing.T) {
	withMultiConnDB(t)
	now := common.GetTimestamp()
	seedSaleWindowPlan(t, &SubscriptionPlan{
		Id: 7405, Title: "已停售套餐", DurationUnit: SubscriptionDurationMonth,
		DurationValue: 1, TotalAmount: 100, Enabled: true,
	}, 0, now-1)
	userId, key := setupRedeemFixture(t, &Redemption{
		ProductType: RedemptionProductPlan, ProductId: 7405,
	})

	_, err := Redeem(key, userId)
	require.Error(t, err)
	// 这里刻意**不**断言 ErrPlanSaleEnded:Redeem 对外一律返回 ErrRedeemFailed,
	// 真正的原因只进系统日志(见 redemption.go 的 `return nil, ErrRedeemFailed`)。
	// 断言具体错误会把一条与本功能无关的上游设计写死进这条用例。
	assert.ErrorIs(t, err, ErrRedeemFailed)

	var stored Redemption
	require.NoError(t, DB.First(&stored, "name = ?", "redeem-test").Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, stored.Status,
		"发不出货的兑换不能把码标成已用")
	assert.Zero(t, stored.UsedUserId)

	var subCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", userId).
		Count(&subCount).Error)
	assert.Zero(t, subCount)
}
