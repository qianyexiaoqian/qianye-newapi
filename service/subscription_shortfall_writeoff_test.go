package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscription_shortfall_writeoff_test.go —— 「套餐撞到 amount_total 上限之后,
// 剩下那一段归谁」的资金核实。
//
// ═══════════ 缺陷长什么样 ═══════════
//
// 套餐余额不够一次预扣额时按剩余额度**部分预扣**(这是「套餐尾数花不掉」的解法)。
// 真实花费超出那点余额时,差额撞到 amount_total 上限,此前**无条件**
// model.DecreaseUserQuota 落到钱包上 —— 套餐上那个「额度用尽后允许使用钱包余额」
// 从来没有被问过。运营勾了"不允许",钱包照扣。
//
// 修法:那一段改问 QyWalletMayCoverSubscriptionShortfall(与请求前那道钱包出资
// 闸门**同一个实现**)。不许补收时核销 —— 请求已经跑完,上游 token 已经烧掉,
// 拒绝换不回它们,只能让平台自己吃下,并且必须在账面上留下这一行。
//
// 下面每一个用例都同时核三件事:套餐的 amount_used、用户钱包余额、
// 以及三个落点加起来等不等于这一笔该收的钱。

func seedPlanRow(t *testing.T, id int, title string) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.SubscriptionPlan{
		Id: id, Title: title, DurationUnit: "month", DurationValue: 1, Enabled: true,
	}).Error)
}

func seedSubscriptionWithPlan(t *testing.T, id, userId, planId int, amountTotal, amountUsed int64) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.UserSubscription{
		Id:          id,
		UserId:      userId,
		PlanId:      planId,
		AmountTotal: amountTotal,
		AmountUsed:  amountUsed,
		Status:      "active",
		StartTime:   time.Now().Unix(),
		EndTime:     time.Now().Add(30 * 24 * time.Hour).Unix(),
	}).Error)
}

// shortfallGateCall 记下闸门被问到时看见了什么。判据本身在
// qianye/modules/groupns 的判据表里核实,这里核的是**调用方有没有把正确的坐标
// 递过去** —— 递错了的话那张表再绿也拦不住任何东西。
type shortfallGateCall struct {
	userId     int
	userGroup  string
	modelGroup string
	shortfall  int64
}

func stubShortfallGate(t *testing.T, mayCharge bool) *[]shortfallGateCall {
	t.Helper()
	original := QyWalletMayCoverSubscriptionShortfall
	calls := make([]shortfallGateCall, 0, 2)
	QyWalletMayCoverSubscriptionShortfall = func(userId int, userGroup, modelGroup string, shortfall int64) bool {
		calls = append(calls, shortfallGateCall{userId, userGroup, modelGroup, shortfall})
		return mayCharge
	}
	t.Cleanup(func() { QyWalletMayCoverSubscriptionShortfall = original })
	return &calls
}

// TestPartialPreConsumeShortfallHonorsTheWalletOverflowSwitch 走**完整链路**:
// 真的 PreConsumeUserSubscription(含部分预扣的候选选择)+ 真的 Settle。
//
// 不用手填 preConsumed 的原因:部分预扣是不是真的发生、发生了多少,本身就是这条
// 链路的一半。手填等于把被测对象换成一个假设。
func TestPartialPreConsumeShortfallHonorsTheWalletOverflowSwitch(t *testing.T) {
	const (
		userId     = 7301
		planId     = 7401
		subId      = 7501
		startQuota = 1_000_000
		// 一次请求的预扣估算额。真实花费通常是它的几十到上百分之一 ——
		// 这正是「尾数花不掉」的成因:只按整额筛候选,每张套餐都会留下
		// 「一次预扣额 − 1」的死残额。
		requestQuota = 3048
		usingGroup   = "反重力的哈基米"
		userGroup    = "default"
	)

	cases := []struct {
		name              string
		amountTotal       int64
		amountUsedBefore  int64
		actualQuota       int
		gateMayCharge     bool
		wantPreConsumed   int64 // 部分预扣真的发生了吗、发生了多少
		wantAmountUsed    int64
		wantWalletCharged int64
		wantWrittenOff    int64
		why               string
	}{
		{
			name:        "余额充足:整额预扣,真实花费远小于预扣额,退回去",
			amountTotal: 100_000, amountUsedBefore: 0, actualQuota: 28,
			gateMayCharge:   false, // 闸门根本不会被问到
			wantPreConsumed: requestQuota, wantAmountUsed: 28,
			why: "没有差额撞上限,这条路径与本次改动无关,必须逐位不变",
		},
		{
			name:        "尾数场景 + 花得起:部分预扣 100,真实花费 28,钱包一分不动",
			amountTotal: 4200, amountUsedBefore: 4100, actualQuota: 28,
			gateMayCharge:   false,
			wantPreConsumed: 100, wantAmountUsed: 4128,
			why: "**「尾数花不掉」在绝大多数请求上的样子**:预扣额是真实花费的上百倍," +
				"那点残额完全够用,结算退回去之后套餐只吃下 28。" +
				"这一格证明「不许钱包补收」并不会把尾数重新困死 —— 它压根走不到补收那一步",
		},
		{
			name:        "尾数场景 + 花超了 + 闸门允许补收:差额落钱包(改动前的唯一行为)",
			amountTotal: 4200, amountUsedBefore: 4100, actualQuota: 5000,
			gateMayCharge:   true,
			wantPreConsumed: 100, wantAmountUsed: 4200, wantWalletCharged: 4900,
			why: "用户分组本身含这个模型分组、或套餐勾了允许 —— 照收,与改动前逐位一致",
		},
		{
			name:        "尾数场景 + 花超了 + 闸门拒绝补收:核销,钱包一分不动",
			amountTotal: 4200, amountUsedBefore: 4100, actualQuota: 5000,
			gateMayCharge:   false,
			wantPreConsumed: 100, wantAmountUsed: 4200, wantWrittenOff: 4900,
			why: "**项目方点名要的那一格**:开关说了不许,就真的一分钱都不从钱包扣。" +
				"请求已经跑完,拒绝换不回上游 token,这一段由平台承担",
		},
		{
			name:        "整额预扣也会撞上限:真实花费超出整张套餐,闸门拒绝",
			amountTotal: 4200, amountUsedBefore: 0, actualQuota: 9000,
			gateMayCharge:   false,
			wantPreConsumed: requestQuota, wantAmountUsed: 4200, wantWrittenOff: 4800,
			why: "撞上限**不是部分预扣独有的**:整额预扣之后真实花费超过预扣估算额同样会撞。" +
				"判据挂在差额上而不是挂在「这次是不是部分预扣」上,两条路径才不会分家",
		},
		{
			name:        "整额预扣撞上限 + 闸门允许:照收",
			amountTotal: 4200, amountUsedBefore: 0, actualQuota: 9000,
			gateMayCharge:   true,
			wantPreConsumed: requestQuota, wantAmountUsed: 4200, wantWalletCharged: 4800,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			seedUser(t, userId, startQuota)
			seedPlanRow(t, planId, "尾数套餐")
			seedSubscriptionWithPlan(t, subId, userId, planId, tc.amountTotal, tc.amountUsedBefore)
			calls := stubShortfallGate(t, tc.gateMayCharge)

			funding := &SubscriptionFunding{
				requestId:  fmt.Sprintf("req-%s-%d", t.Name(), time.Now().UnixNano()),
				userId:     userId,
				modelName:  "gpt-4o",
				amount:     requestQuota,
				usingGroup: usingGroup,
				userGroup:  userGroup,
			}
			require.NoError(t, funding.PreConsume(0))
			require.Equal(t, tc.wantPreConsumed, funding.preConsumed,
				"部分预扣是这条链路的一半:预扣到多少决定了后面欠着多少")

			// BillingSession.Settle 传的差额恒为「真实花费 − 想扣的那个数」。
			require.NoError(t, funding.Settle(tc.actualQuota-requestQuota))

			var sub model.UserSubscription
			require.NoError(t, model.DB.Where("id = ?", subId).First(&sub).Error)
			assert.Equalf(t, tc.wantAmountUsed, sub.AmountUsed, "理由:%s", tc.why)
			assert.LessOrEqual(t, sub.AmountUsed, sub.AmountTotal,
				"amount_total 是硬上限,钳位之后不允许被越过")

			walletAfter, err := model.GetUserQuota(userId, true)
			require.NoError(t, err)
			assert.Equal(t, startQuota-int(tc.wantWalletCharged), walletAfter)
			assert.GreaterOrEqual(t, walletAfter, 0, "核销路径不得把余额扣成负数")

			assert.Equal(t, tc.wantWalletCharged, funding.SettleWalletShortfall())
			assert.Equal(t, tc.wantWrittenOff, funding.SettleWrittenOff())

			// 三个落点必须正好凑齐这一笔请求该收的钱。少一分就是"日志记了、
			// 没人付";多一分就是重复扣。
			collected := tc.wantPreConsumed + funding.SettleApplied() +
				funding.SettleWalletShortfall() + funding.SettleWrittenOff()
			assert.EqualValues(t, tc.actualQuota, collected,
				"套餐预扣 + 套餐结算 + 钱包补收 + 平台核销 == 真实花费")

			if tc.wantWalletCharged == 0 && tc.wantWrittenOff == 0 {
				assert.Empty(t, *calls, "没有正差额时不该惊动闸门,更不该查主库")
				return
			}
			require.Len(t, *calls, 1)
			assert.Equal(t, shortfallGateCall{
				userId:     userId,
				userGroup:  userGroup,
				modelGroup: usingGroup,
				shortfall:  tc.wantWalletCharged + tc.wantWrittenOff,
			}, (*calls)[0],
				"闸门的第一判据是「用户分组含不含这个模型分组」——"+
					"坐标递错了,groupns 那张判据表再绿也拦不住任何东西")
		})
	}
}

// TestExhaustedSubscriptionNeverPartiallyReserves 守住"余额为零"这一档。
//
// 部分预扣的第二轮只接受 remain > 0 的候选。整张套餐已经用尽时它必须**出不了资**
// (调用方据此回落钱包,并在那里过一次请求前的钱包出资闸门),而不是预扣 0 —— 预扣 0
// 会让 BillingSession 以为套餐出了资,于是这一笔的全部花费都走"结算差额"这条路,
// 每一次请求都撞一次上限、每一次都核销,核销量就再也没有上界了。
func TestExhaustedSubscriptionNeverPartiallyReserves(t *testing.T) {
	const (
		userId = 7302
		planId = 7402
		subId  = 7502
	)
	truncate(t)
	seedUser(t, userId, 1_000_000)
	seedPlanRow(t, planId, "已耗尽的套餐")
	seedSubscriptionWithPlan(t, subId, userId, planId, 4200, 4200)
	calls := stubShortfallGate(t, false)

	funding := &SubscriptionFunding{
		requestId: fmt.Sprintf("req-%d", time.Now().UnixNano()),
		userId:    userId, modelName: "gpt-4o", amount: 3048,
		usingGroup: "反重力的哈基米", userGroup: "default",
	}
	err := funding.PreConsume(0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscription quota insufficient",
		"调用方靠这个错误串回落钱包;换了措辞就等于把套餐用尽的人整体打挂")
	assert.Empty(t, *calls)

	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("id = ?", subId).First(&sub).Error)
	assert.EqualValues(t, 4200, sub.AmountUsed, "出不了资的候选不得被扣动一分")
}

// TestSettleSubscriptionDeltaNeverWritesOffARefund 守退款方向。
//
// 负差额(退款没能全额退进套餐,例如套餐已被续期清零)既不许往钱包里补
// —— 那笔钱当初未必是从钱包出的,补进去就是凭空发钱 —— 也不许被记成核销:
// 平台什么都没让出去,记一笔免单会让"这条规则少收了多少"这个数字直接失真。
func TestSettleSubscriptionDeltaNeverWritesOffARefund(t *testing.T) {
	const (
		userId = 7303
		planId = 7403
		subId  = 7503
	)
	truncate(t)
	seedUser(t, userId, 1_000_000)
	seedPlanRow(t, planId, "已被续期清零的套餐")
	// amount_used 已经是 0:再退 2976 只能退到 0,applied 只有 0,
	// 差额 −2976 无处可去 —— 而它恰恰不该有去处。
	seedSubscriptionWithPlan(t, subId, userId, planId, 4200, 0)
	calls := stubShortfallGate(t, true)

	split, err := settleSubscriptionDelta(userId, "default", "反重力的哈基米", subId, -2976)
	require.NoError(t, err)
	assert.EqualValues(t, 0, split.Applied)
	assert.EqualValues(t, 0, split.WalletCharged)
	assert.EqualValues(t, 0, split.WrittenOff)
	assert.Empty(t, *calls, "非正差额不该走到闸门")

	walletAfter, err := model.GetUserQuota(userId, true)
	require.NoError(t, err)
	assert.Equal(t, 1_000_000, walletAfter, "退不进套餐的那一段绝不能变成钱包里凭空多出来的钱")
}
