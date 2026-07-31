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
