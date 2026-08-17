package commission

import (
	"context"
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHardExcluded 锁定"无开关"的排除项。
//
// 违规扣费的那笔钱是罚款不是消费,给邀请人分成等于"下线违规、上线获利"。
// 这属于逻辑错误而非口径偏好,任何配置都不得让它返佣。
func TestHardExcluded(t *testing.T) {
	violation := model.RecordConsumeLogParams{
		Quota: 1000,
		Other: map[string]interface{}{"violation_fee": true},
	}
	assert.True(t, hardExcluded(violation))

	// 渠道可用性测试跑在管理员账号上,不是真实消费。
	// 首选判据是写日志那一侧打的显式标记,与文案无关。
	channelTestMarked := model.RecordConsumeLogParams{
		Quota: 1000, TokenId: 7, TokenName: "运营自己建的令牌",
		Other: map[string]interface{}{model.ChannelTestLogOtherKey: true},
	}
	assert.True(t, hardExcluded(channelTestMarked))

	// 兜底判据:标记出现之前写下的存量日志只有 token_name 可认。
	channelTest := model.RecordConsumeLogParams{
		Quota: 1000, TokenId: 0, TokenName: model.ChannelTestTokenName,
	}
	assert.True(t, hardExcluded(channelTest))

	// 同名令牌但确实是用户自己建的(TokenId > 0),不能误杀。
	realToken := model.RecordConsumeLogParams{
		Quota: 1000, TokenId: 7, TokenName: model.ChannelTestTokenName,
	}
	assert.False(t, hardExcluded(realToken))

	// 标记为 false 不等于命中 —— 只有显式 true 才算。
	notMarked := model.RecordConsumeLogParams{
		Quota: 1000, TokenId: 7, TokenName: "default",
		Other: map[string]interface{}{model.ChannelTestLogOtherKey: false},
	}
	assert.False(t, hardExcluded(notMarked))

	normal := model.RecordConsumeLogParams{
		Quota: 1000, TokenId: 3, TokenName: "default",
		Other: map[string]interface{}{"violation_fee": false},
	}
	assert.False(t, hardExcluded(normal))

	assert.False(t, hardExcluded(model.RecordConsumeLogParams{Quota: 1000}))
}

// TestIsSubscriptionConsume 确认订阅消费的判据是正向标记。
//
// 绝不能反过来用 other["wallet_quota_deducted"] 判断:那个键只在订阅分支
// 被写入,钱包分支根本不写,取零值会把所有钱包消费误判成订阅消费,
// 一开排除开关就等于全站停返佣。
func TestIsSubscriptionConsume(t *testing.T) {
	assert.True(t, isSubscriptionConsume(map[string]interface{}{"billing_source": "subscription"}))
	assert.False(t, isSubscriptionConsume(map[string]interface{}{"billing_source": "wallet"}))
	assert.False(t, isSubscriptionConsume(map[string]interface{}{"wallet_quota_deducted": 0}))
	assert.False(t, isSubscriptionConsume(map[string]interface{}{}))
	assert.False(t, isSubscriptionConsume(nil))
}

// TestAllSourcesUseAccountGroupForRate 锁定四条来源共用同一个分组口径:
// 下线的【账号分组】(users.group,即 inviterEntry.InviteeGroup)。
//
// 这是选定的口径 —— 佣金档位跟人走,不跟单次请求走。四条来源必须一致:
// 只要有一条改成按"本次请求的计价分组"解析,同一个下线同一天就会同时出现
// 两个费率档,日聚合桶按 (下线, 日期, 费率) 建键,于是裂成多行,
// 对账时"这个下线这天返了多少"再也不是一个数。
//
// 消费与任务补扣走 accrueConsume,充值与兑换码走 accrueOneShot ——
// 两条独立的代码路径,所以两边都要落库回读,不能只测一条。
func TestAllSourcesUseAccountGroupForRate(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	// vip 分组:充值 12%、消费 8%,都与全局默认(10%/5%)不同,
	// 这样"没按分组走"会直接体现成金额不同,而不是碰巧相等。
	seedGroupRate(t, gdb, "vip", "12", "8", true)

	newInvitee := func(id int) {
		getInviterCache().Set(id, inviterEntry{
			InviterId:      42,
			InviteeName:    "u" + itoa(id),
			InviteeCreated: common.GetTimestamp() - 30*86400,
			InviteeGroup:   "vip",
		})
	}
	for _, id := range []int{910, 911, 912} {
		newInvitee(id)
	}

	ctx := context.Background()
	at := common.GetTimestamp()

	// 消费(= relay 结算)与任务补扣共用 accrueConsume。
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: 910, Quota: 10000, At: at}))
	// 充值与兑换码共用 accrueOneShot,各自一条订单号。
	require.NoError(t, accrueOneShot(ctx, 911, 10000, decimal.Zero,
		SourceTopup, topupIdemKey("TX-910"), "TX-910"))
	require.NoError(t, accrueOneShot(ctx, 912, 10000, decimal.Zero,
		SourceRedemption, redemptionIdemKey(77), "RD77"))

	consumeRow := accrualOfInvitee(t, gdb, 910)
	assert.Equal(t, "vip", consumeRow.RateGroup, "消费必须按账号分组计佣")
	assert.Equal(t, 800, consumeRow.RateUnits)
	assert.Equal(t, "800", consumeRow.GrossAmount.String(), "10000 × 8% = 800")

	topupRow := accrualOfInvitee(t, gdb, 911)
	assert.Equal(t, "vip", topupRow.RateGroup, "充值必须按账号分组计佣")
	assert.Equal(t, 1200, topupRow.RateUnits)
	assert.Equal(t, "1200", topupRow.GrossAmount.String(), "10000 × 12% = 1200")

	// 兑换码走充值那一档,不是自己一档。
	redeemRow := accrualOfInvitee(t, gdb, 912)
	assert.Equal(t, "vip", redeemRow.RateGroup, "兑换码必须按账号分组计佣")
	assert.Equal(t, 1200, redeemRow.RateUnits)
}

// consumeEvent 刻意不带"本次请求的计价分组"。
//
// 带上它就等于给日后的自己留了个诱惑:在 accrueConsume 里改用它解析费率,
// 于是同一个下线同一天裂成多个费率档。类型里没有这个字段,是让这条口径
// 在编译期就成立,而不是靠注释提醒。
//
// 这条断言是结构性的:字段数变了就说明有人往事件里塞了新东西,
// 需要重新想清楚"它会不会成为第二个费率来源"。
func TestConsumeEventCarriesNoPricingGroup(t *testing.T) {
	typ := reflect.TypeOf(consumeEvent{})
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		names = append(names, typ.Field(i).Name)
	}
	assert.Equal(t, []string{"InviteeId", "Quota", "At"}, names,
		"consumeEvent 只该带下线、金额、时刻 —— 分组口径固定取账号分组")
}
