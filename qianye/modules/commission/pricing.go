package commission

// pricing.go —— 一次计佣的定价解析:返佣费率 + 法币折算比例。
//
// # 这个文件存在的唯一理由
//
// 两档比例都按**上线(推广人)自己**的账号分组取值,而在此之前它们取的是
// 两个不同的人:
//
//	费率     grouprate.go  ← 下线的分组
//	法币比例 fiatrate.go    ← 上线的分组
//
// 那个不一致不是设计,是两轮开发各自选了一个口径而没人把它们放在一起看。
// 表现是同一条 accrual 行里 rate_group 记着 A 的分组、usd_rate 却是按 B 的
// 分组算出来的,而这一行的三条恒等式(base × rate = gross、gross = settled +
// outstanding、usd_rate 加权平均)**全部成立** —— 没有任何守卫会响。
//
// 修法不是在两处各写一句"记得取上线",而是把"取谁的分组"这个决定收进
// **一个**函数:上线解析一次,同一个字符串同时喂给两档。口径从此不可能分叉,
// 因为根本没有第二个地方可以做这个决定。pricing_single_resolver_guard_test.go
// 把"计佣路径不得自己调 resolveRate / resolveFiatRate"写成断言。
//
// # 降级
//
// 主库读不到上线那一行时**不放弃计佣**:guard.HotAsync 没有重试,返回错误
// 等于这笔佣金永远丢了。两档一起跳过分组层,回落到各自的全局层,并计一次
// inviterGroupDegrade。
//
// 跳过分组层而不是"按 default 分组判定":后者会让一次主库抖动变成"这批人
// 被当成默认分组的用户",而如果运营恰好给 default 配了一档费率,那一档会被
// 当成事实冻结进账本。"读不到"与"这个人在 default 组"是两件事,账本上必须
// 分得开 —— 前者的 rate_group 是空串,后者是 "default"。

import (
	"context"
	"errors"
)

// pricingDecision 是一次计佣的定价快照,两档都会被冻结进 accrual 行。
//
// 刻意成组返回而不是两个独立的函数各返回一个:成组是"它们出自同一次分组
// 解析"这件事在类型上的表达。拆开之后调用方就有机会在两次调用之间做别的事,
// 而"别的事"里最便宜的一件就是把另一个人的 id 传进去。
type pricingDecision struct {
	// Rate 是返佣费率(百分比 × 100)与判定用的分组,冻结进 rate_units/rate_group。
	Rate rateDecision
	// Fiat 是「佣金额度 → 法币」的折算比例与它命中的层级,冻结进 usd_rate。
	Fiat fiatDecision
}

// sameGroup 断言两档确实取自同一个分组。
//
// 它不是防御性编程的残渣,而是这条不变量在**运行期**的落点:上面那个断言
// (守卫测试)只管住"谁能调解析函数",管不住有人在 resolveInviterPricing
// 内部把两个不同的字符串喂进去。调用方据此决定要不要计降级。
func (p pricingDecision) sameGroup() bool { return p.Rate.Group == p.Fiat.Group }

// resolveInviterPricing 是计佣路径上**唯一**的定价解析入口。
//
// inviterId 是这笔佣金的收款人。函数先把他的账号分组解析出来,再用**同一个
// 分组字符串**分别解析费率与法币比例。
//
// source 决定走费率的哪一档(消费 / 兑换码 / 充值),法币比例不分档。
func resolveInviterPricing(ctx context.Context, inviterId int, source string, s opSettings) pricingDecision {
	e, _, err := resolveInviter(ctx, inviterId)
	return pricingFromInviterEntry(ctx, e, err, source, s)
}

// pricingFromInviterEntry 把一次上线解析的结果变成定价决策。
//
// 它与 resolveInviterPricing 分开,是因为「解析上线」与「据此定价」是两件事,
// 而**只有后者**决定钱:降级判据、两档共用同一个分组字符串、降级计数,全在这里。
// 分开之后这条判据可以脱离主库直接测(见 pricing_missing_inviter_test.go),
// 而在合成一体时它只能靠"把主库句柄置空"来触发 —— 那是生产上几乎不发生的
// 那一支,恰恰不是本轮查出的那一支。
func pricingFromInviterEntry(ctx context.Context, e inviterEntry, err error, source string, s opSettings) pricingDecision {
	// "主库报错" 与 "主库里没有这一行" 是同一件事的两种形状:两者都意味着
	// **拿不到上线的分组**,都必须跳过分组层。原先只判 err != nil,而
	// record-not-found 走的是 err == nil + 零值 entry 那一支 —— 也就是最常见
	// 的那种"读不到"(推广人被删/被软删)永远不会触发降级,反而被
	// billingGroup("") 折成 "default",按 default 分组的费率与法币档冻结进账本。
	if err == nil && e.Missing {
		err = errors.New("上线账号在主库中不存在(可能已被删除)")
	}
	if err != nil {
		inviterGroupDegrade.noteCtx(ctx, "读取上线分组失败,费率与法币比例一起跳过分组层: "+err.Error())
		// 费率:matched=false 走纯全局档,与"这个分组没配规则"完全同一条路径
		// (共用 rateUnitsFor)。Group 留空,标记本行是一次降级。
		//
		// 法币:fiatRateFor(nil) 同样跳过分组层。它自己会把层内降级写进
		// Degraded,那一条仍然要上报 —— 两个原因(读不到人 / 库里存着非法比例)
		// 各计各的,合并计数会让排查的人看不出到底哪一个在响。
		fiat := fiatRateFor(nil, s)
		if fiat.Degraded != "" {
			fiatRateDegrade.noteCtx(ctx, fiat.Degraded)
		}
		return pricingDecision{
			Rate: rateDecision{Units: rateUnitsFor(GroupRate{}, false, source, s)},
			Fiat: fiat,
		}
	}
	// 一个字符串,两档共用。这一行就是本文件的全部内容。
	g := e.Group
	return pricingDecision{
		Rate: resolveRate(ctx, g, source, s),
		Fiat: resolveFiatRate(ctx, g, s),
	}
}
