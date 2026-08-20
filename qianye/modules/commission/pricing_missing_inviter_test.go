package commission

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 「读不到上线」有两种形状:主库报错,和主库里根本没有这一行。
//
// 后者才是生产上最常见的那一种(推广人被删/被软删),而它原先走的是
// err == nil + 零值 entry 那一支:Group 是空串,billingGroup 把空串折成
// "default",于是这笔佣金被当成"上线就在 default 分组"定价并**冻结进账本**。
// 表现有三条,每一条都很贵:
//
//  1. 账本上"读不到上线"与"上线在 default 组"不可区分,事后认不出这批行;
//  2. 站点只要给 default 配过分组费率(很常见),这批佣金按 default 档发,
//     而不是设计声明的全局兜底档 —— 实测 5% 变成 33%;
//  3. inviter_group 降级计数器恒不响,没有任何信号。
//
// 判据落在 inviterEntry.Missing 上:它的零值是 false = "查到了",所以任何
// 忘了设置它的新路径退化成旧行为,而不是把全站佣金推进降级分支。
func TestMissingInviterIsADegradeNotADefaultGroup(t *testing.T) {
	t.Run("查不到的上线必须带 Missing 标记", func(t *testing.T) {
		var e inviterEntry
		assert.False(t, e.Missing, "零值必须是「查到了」,否则忘写的新路径会把佣金整片推进降级")
	})

	t.Run("Missing 的条目走降级:rate_group 必须留空,绝不能是 default", func(t *testing.T) {
		s := opSettings{}
		d := pricingFromInviterEntry(context.Background(), inviterEntry{Missing: true}, nil, SourceConsume, s)
		require.Equal(t, "", d.Rate.Group,
			"读不到上线时 rate_group 必须是空串(降级痕迹);写成 default 就与「上线真的在 default 组」不可区分")
		assert.False(t, d.Rate.Matched, "降级行不允许命中任何分组规则")
		assert.Equal(t, "", d.Fiat.Group, "法币比例必须与费率跳过同一层")
	})

	t.Run("查得到的上线照常按他自己的分组定价", func(t *testing.T) {
		s := opSettings{}
		d := pricingFromInviterEntry(context.Background(), inviterEntry{Group: "vip"}, nil, SourceConsume, s)
		assert.Equal(t, "vip", d.Rate.Group)
		assert.Equal(t, "vip", d.Fiat.Group)
	})
}
