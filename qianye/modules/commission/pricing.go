package commission

// pricing.go —— 一次计佣的定价解析:按**上线(推广人)自己**的账号分组取费率。
//
// # 这个文件存在的唯一理由
//
// "取谁的分组"这个决定只能在**一个**函数里做:上线解析一次,同一个分组字符串
// 喂给费率档。口径从此不可能分叉,因为根本没有第二个地方可以做这个决定。
// pricing_single_resolver_guard_test.go 把"计佣路径不得自己调 resolveRate"写成断言。
//
// 上线的分组由 invite.UserGroupOf 给出:与邀请关系同一份缓存、同一次回源。
//
// # 降级
//
// 主库读不到上线那一行时**不放弃计佣**:guard.HotAsync 没有重试,返回错误
// 等于这笔佣金永远丢了。跳过分组层,回落到全局层,并计一次 inviterGroupDegrade。
//
// 跳过分组层而不是"按 default 分组判定":后者会让一次主库抖动变成"这批人
// 被当成默认分组的用户",而如果运营恰好给 default 配了一档费率,那一档会被
// 当成事实冻结进账本。"读不到"与"这个人在 default 组"是两件事,账本上必须
// 分得开 —— 前者的 rate_group 是空串,后者是 "default"。

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/qianye/modules/invite"
)

// resolveInviterPricing 是计佣路径上**唯一**的定价解析入口。
//
// inviterId 是这笔佣金的收款人。函数先把他的账号分组解析出来,再据此解析费率。
// source 决定走费率的哪一档(消费 / 兑换码 / 充值)。
func resolveInviterPricing(ctx context.Context, inviterId int, source string, s opSettings) rateDecision {
	group, missing, err := invite.UserGroupOf(ctx, inviterId)
	return pricingFromInviterGroup(ctx, group, missing, err, source, s)
}

// pricingFromInviterGroup 把一次上线解析的结果变成定价决策。
//
// 它与 resolveInviterPricing 分开,是因为「解析上线」与「据此定价」是两件事,
// 而**只有后者**决定钱:降级判据与降级计数全在这里。分开之后这条判据可以脱离
// 主库直接测(见 pricing_missing_inviter_test.go)。
func pricingFromInviterGroup(ctx context.Context, group string, missing bool, err error, source string, s opSettings) rateDecision {
	// "主库报错" 与 "主库里没有这一行" 是同一件事的两种形状:两者都意味着
	// **拿不到上线的分组**,都必须跳过分组层。record-not-found 走的是 err == nil +
	// missing 那一支 —— 也就是最常见的那种"读不到"(推广人被删/被软删),
	// 不能被 billingGroup("") 折成 "default" 按默认分组的档冻结进账本。
	if err == nil && missing {
		err = errors.New("上线账号在主库中不存在(可能已被删除)")
	}
	if err != nil {
		inviterGroupDegrade.noteCtx(ctx, "读取上线分组失败,费率跳过分组层: "+err.Error())
		// matched=false 走纯全局档,与"这个分组没配规则"完全同一条路径
		// (共用 rateUnitsFor)。Group 留空,标记本行是一次降级。
		return rateDecision{Units: rateUnitsFor(GroupRate{}, false, source, s)}
	}
	return resolveRate(ctx, group, source, s)
}
