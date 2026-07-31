package grouppricing

import (
	"github.com/QuantumNous/new-api/qianye/config"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// hook.go —— 注入 relay/helper 三个计价挂载点的实现体。
//
// 这三个函数跑在 relay 的同步计价链路上,每一次请求都会走。因此它们:
//   - 只做内存查找(规则来自 atomic 快照),不碰数据库
//   - 影子差额只做一次 map 查找 + atomic 累加,落库交给 guard.HotAsync 的 worker
//   - 任何"拿不准"的情形一律返回入参原值(等于上游原逻辑)
//
// 三者结构完全一致,刻意不抽公共骨架:抽出来之后每个口径都要靠一个
// interface{} 或者一串回调把差异塞回去,而这三段各自只有十来行,
// 读起来必须一眼看出"什么情况下会改动扣费"。

// applyModelPrice 覆盖按次固定价。
//
// 入参 price/usePrice 即 ratio_setting.GetModelPrice 的返回值。
// usePrice==false 表示该模型全局是按 token 计费的,此时命中 price 规则会
// **切换计费口径**,差额无法按比例折算,记录里会标成 exact=false。
func applyModelPrice(info *relaycommon.RelayInfo, price float64, usePrice bool) (float64, bool) {
	rule, ok := resolve(info, ModePrice)
	if !ok {
		return price, usePrice
	}
	// 旧值只有在原本就按次计费时才有比较意义。原本不按次计费(usePrice==false)时
	// GetModelPrice 返回的是 -1 这个哨兵值,把它当成"旧价 -1"会算出一个荒谬的差额。
	oldText, exact := "", false
	if usePrice {
		oldText, exact = formatFloat(price), price > 0
	}
	note(info, ModePrice, oldText, rule, exact)

	if config.Get().GroupPricing.IsShadow() {
		return price, usePrice
	}
	return rule.ValueFloat, true
}

// applyModelRatio 覆盖按 token 计费的模型倍率。
//
// 入参 ratio/ok 即 ratio_setting.GetModelRatio 的前两个返回值。
// ok==false 表示该模型没有配置全局倍率(上游据此报"价格未配置"),
// 此时分组级倍率成为唯一价格来源 —— 那不是打折而是从无到有,exact=false。
func applyModelRatio(info *relaycommon.RelayInfo, ratio float64, ok bool) (float64, bool) {
	rule, hit := resolve(info, ModeRatio)
	if !hit {
		return ratio, ok
	}
	oldText, exact := "", false
	if ok {
		oldText, exact = formatFloat(ratio), ratio > 0
	}
	note(info, ModeRatio, oldText, rule, exact)

	if config.Get().GroupPricing.IsShadow() {
		return ratio, ok
	}
	return rule.ValueFloat, true
}

// applyTieredQuota 覆盖阶梯表达式计价。
//
// 阶梯计价的"价格"是一整条表达式,没有可替换的标量,因此分组级覆盖在这条
// 路径上的语义是乘数:最终 quota = 表达式结果 × 乘数 × 分组倍率,
// 与另外两条路径的"分组级价 × 分组倍率"保持同一个相乘形状。
//
// 旧值恒为 1(未覆盖即乘 1),所以这条路径的差额永远是精确的。
func applyTieredQuota(info *relaycommon.RelayInfo, quotaBeforeGroup float64) float64 {
	rule, ok := resolve(info, ModeTiered)
	if !ok {
		return quotaBeforeGroup
	}
	note(info, ModeTiered, "1", rule, true)

	if config.Get().GroupPricing.IsShadow() {
		return quotaBeforeGroup
	}
	return quotaBeforeGroup * rule.ValueFloat
}

// resolve 查出本次请求在指定口径下生效的覆盖规则。
//
// 分组取 info.UsingGroup 而不是 info.UserGroup。理由必须写清楚,因为这两个
// 字段名只差一个词,取错了不会报任何错,只会安静地按错误的分组扣钱:
//
//   - UserGroup 是用户所属分组;UsingGroup 是本次请求**实际使用**的分组。
//     auto 分组重试时 HandleGroupRatio 会把 UsingGroup 改写成真正命中的那个分组。
//   - 与分组价相乘的分组倍率取自 ratio_setting.GetGroupRatio(UsingGroup)。
//     价格取 UserGroup、倍率取 UsingGroup,相乘出来的数字不对应任何真实定价。
//   - 消费日志的 group 列写的也是 UsingGroup(见 model.RecordConsumeLogParams)。
//     取错会让影子差额与主库日志按不同维度归档,对账时两边永远对不上。
//
// 规则口径与请求口径不匹配时返回 false(例如给一个走阶梯计价的模型配了
// price 规则)。这类"配了却不生效"的规则由管理端写入校验负责告警,
// 热路径只管不生效,绝不猜。
func resolve(info *relaycommon.RelayInfo, mode string) (*compiledRule, bool) {
	if info == nil || !config.Get().GroupPricing.Enabled {
		return nil, false
	}
	maybeRefresh()

	rule, ok := lookupOverride(info.UsingGroup, info.OriginModelName)
	if !ok || rule.Mode != mode {
		return nil, false
	}
	return rule, true
}
