package commission

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pricing_missing_inviter_test.go —— "主库里没有这个上线"必须是一次**降级**,
// 而不是"这个人在 default 组"。
//
// 上线被删(或被软删后被 deleted_at 作用域过滤掉)时,invite.UserGroupOf 回的是
// missing=true、err=nil。这一支若被当成正常解析,分组字符串是空串,会被
// billingGroup 折成 "default":
//
//  1. 一个已经不存在的推广人名下仍在按 default 组的分组档冻结费率;
//  2. 运营给 default 配了一档时,那一档而不是全局兜底档会被当成事实写进账本;
//  3. inviter_group 降级计数器恒不响,没有任何信号。
func TestMissingInviterIsADegradeNotADefaultGroup(t *testing.T) {
	t.Run("查不到的上线走降级:rate_group 必须留空,绝不能是 default", func(t *testing.T) {
		s := opSettings{ConsumeRateUnits: 500}
		d := pricingFromInviterGroup(context.Background(), "", true, nil, SourceConsume, s)
		require.Equal(t, "", d.Group,
			"读不到上线时 rate_group 必须是空串(降级痕迹);写成 default 就与「上线真的在 default 组」不可区分")
		assert.False(t, d.Matched, "降级行不允许命中任何分组规则")
		assert.Equal(t, 500, d.Units, "降级按全局默认档算,不是 0")
	})

	t.Run("主库报错同样走降级", func(t *testing.T) {
		s := opSettings{TopupRateUnits: 1000}
		d := pricingFromInviterGroup(context.Background(), "", false, errors.New("boom"), SourceTopup, s)
		assert.Equal(t, "", d.Group)
		assert.Equal(t, 1000, d.Units)
	})

	t.Run("查得到的上线照常按他自己的分组定价", func(t *testing.T) {
		s := opSettings{}
		d := pricingFromInviterGroup(context.Background(), "vip", false, nil, SourceConsume, s)
		assert.Equal(t, "vip", d.Group)
	})
}
