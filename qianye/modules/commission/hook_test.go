package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
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
	channelTest := model.RecordConsumeLogParams{
		Quota: 1000, TokenId: 0, TokenName: "模型测试",
	}
	assert.True(t, hardExcluded(channelTest))

	// 同名令牌但确实是用户自己建的(TokenId > 0),不能误杀。
	realToken := model.RecordConsumeLogParams{
		Quota: 1000, TokenId: 7, TokenName: "模型测试",
	}
	assert.False(t, hardExcluded(realToken))

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

// 消费返佣的费率必须按【本次请求实际计价的分组】解析,不能用账号分组。
//
// 这是一条可被用户主动利用的套利路径:令牌可以指定分组(middleware/auth.go 会据此
// 覆盖 relayInfo.UsingGroup),而 RecordConsumeLogParams.Group 带的正是那个值。
// 若改用 users.group 解析费率,下线只要把令牌指到低毛利分组,就能让平台按批发价
// 收钱、按零售价付佣金 —— 而流水行本身自洽(基数 × 费率 = 佣金),事后查不出来。
//
// 这条测试钉的是"Group 有没有从 hook 参数一路传到费率解析",不是算术。
func TestConsumeEventCarriesRequestGroup(t *testing.T) {
	cases := []struct {
		name      string
		params    model.RecordConsumeLogParams
		wantGroup string
	}{
		{
			name:      "令牌指定了分组",
			params:    model.RecordConsumeLogParams{Quota: 100, Group: "wholesale"},
			wantGroup: "wholesale",
		},
		{
			name:      "auto 分组解析后的实际分组",
			params:    model.RecordConsumeLogParams{Quota: 100, Group: "vip"},
			wantGroup: "vip",
		},
		{
			name:      "上游未提供分组时留空,由 accrueConsume 回落账号分组",
			params:    model.RecordConsumeLogParams{Quota: 100},
			wantGroup: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := consumeEvent{
				InviteeId: 7,
				Quota:     int64(tc.params.Quota),
				Group:     tc.params.Group,
			}
			assert.Equal(t, tc.wantGroup, ev.Group,
				"计价分组必须原样带出 relay 线程 —— 丢掉它等于把费率口径交给账号分组")
		})
	}
}

// 任务计费(MJ/视频等)走的是另一条 hook,同样必须带上分组。
// 漏掉这一条就等于给套利留了一个只针对任务类模型的后门。
func TestTaskBillingCarriesRequestGroup(t *testing.T) {
	params := model.RecordTaskBillingLogParams{
		UserId: 7, Quota: 100, Group: "wholesale",
		LogType: model.LogTypeConsume,
	}
	ev := consumeEvent{
		InviteeId: params.UserId,
		Quota:     int64(params.Quota),
		Group:     params.Group,
	}
	assert.Equal(t, "wholesale", ev.Group,
		"任务计费与普通消费必须同一个分组口径")
}
