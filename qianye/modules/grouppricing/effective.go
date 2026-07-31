package grouppricing

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/shopspring/decimal"
)

// isPattern 判断模型名是不是通配形态("*" 或 "前缀*")。
func isPattern(modelName string) bool {
	return strings.HasSuffix(strings.TrimSpace(modelName), "*")
}

// effective.go —— 折算后的**最终生效价**。
//
// 用户拍板选了"分组级价 × 分组倍率"的相乘方案。我在设计阶段提醒过:相乘意味着
// 运营在输入框里填的那个数不是用户最终付的价,心算很容易错一位。因此
// **管理端必须直接显示折算后的最终生效价**,这是这个方案的必要配套,不是可选装饰。
//
// 本文件是那个折算的唯一实现,列表接口与试算接口共用 —— 两处各写一套折算,
// 迟早会有一处漏乘分组倍率,而那一处正好是运营天天看的那个。

// Effective 是一条规则在某个分组下的最终生效价。
//
// 全部金额/倍率字段都是十进制字符串:这些数字会被运营直接拿去和对外报价单
// 核对,经过 float64 打印出来的 0.30000000000000004 会让人对整套系统失去信任。
type Effective struct {
	// GroupRatio 是该分组当前的分组倍率(ratio_setting.GetGroupRatio)。
	GroupRatio string `json:"group_ratio"`

	// GlobalValue 是该模型当前的全局价/倍率(未配置时为空)。
	GlobalValue string `json:"global_value,omitempty"`
	// GlobalEffective = 全局价 × 分组倍率,也就是**改动前**这个分组实际付的价。
	GlobalEffective string `json:"global_effective,omitempty"`

	// RuleValue 是规则里填的分组级价/倍率。
	RuleValue string `json:"rule_value"`
	// RuleEffective = 分组级价 × 分组倍率,也就是**切换后**这个分组实际付的价。
	// 这一个字段就是这套 UI 存在的全部理由。
	RuleEffective string `json:"rule_effective"`

	// DeltaPercent 是相对改动前的涨跌幅(百分比字符串,负数表示变便宜)。
	// 全局价缺失时为空 —— 没有基准就没有涨跌幅,不能拿 0 去凑一个数。
	DeltaPercent string `json:"delta_percent,omitempty"`

	// Unit 说明 RuleEffective 的单位,直接显示在数字后面。
	Unit string `json:"unit"`

	// QuotaPerCall 只在 price 口径下有值:一次调用实际扣多少 quota。
	// 运营看的是余额数字,美元是他们心里的换算,quota 才是账面上真正减少的量。
	QuotaPerCall int64 `json:"quota_per_call,omitempty"`

	// Warning 是"这条规则配了也不会生效"的显式提示。
	//
	// 本扩展已经四次栽在"配置定义了却没有消费方"上。规则口径与模型当前的
	// 全局计费口径不匹配(给按 token 计费的模型配 tiered 乘数、给阶梯计价的
	// 模型配 price)时,它会安静地一直不生效,而运营从列表上完全看不出来。
	Warning string `json:"warning,omitempty"`
}

// computeEffective 折算一条 (分组, 模型, 口径, 值) 的最终生效价。
func computeEffective(group, modelName, mode string, value decimal.Decimal) Effective {
	group = normalizeGroup(group)
	groupRatio := decimal.NewFromFloat(ratio_setting.GetGroupRatio(group))

	e := Effective{
		GroupRatio: normalizeDecimal(groupRatio),
		RuleValue:  normalizeDecimal(value),
	}

	ruleEff := value.Mul(groupRatio)
	e.RuleEffective = normalizeDecimal(ruleEff)

	// 通配规则没有唯一的"全局价"可比 —— 它覆盖的是一批模型。
	// 硬取一个值来当基准会让涨跌幅指向一个根本不存在的模型。
	pattern := isPattern(modelName)

	var globalValue decimal.Decimal
	hasGlobal := false
	switch mode {
	case ModePrice:
		e.Unit = "美元/次"
		if p, ok := ratio_setting.GetModelPrice(modelName, false); ok && !pattern {
			globalValue, hasGlobal = decimal.NewFromFloat(p), true
		}
		e.QuotaPerCall = ruleEff.Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart()
	case ModeRatio:
		e.Unit = "模型倍率"
		if r, ok, _ := ratio_setting.GetModelRatio(modelName); ok && !pattern {
			globalValue, hasGlobal = decimal.NewFromFloat(r), true
		}
	case ModeTiered:
		e.Unit = "阶梯乘数"
		globalValue, hasGlobal = decimal.NewFromInt(1), true
	}

	if hasGlobal {
		e.GlobalValue = normalizeDecimal(globalValue)
		e.GlobalEffective = normalizeDecimal(globalValue.Mul(groupRatio))
		if !globalValue.IsZero() {
			delta := value.DivRound(globalValue, 12).Sub(decimal.NewFromInt(1)).
				Mul(decimal.NewFromInt(100))
			e.DeltaPercent = delta.StringFixed(2)
		}
	}
	e.Warning = modeMismatchWarning(modelName, mode)
	return e
}

// modeMismatchWarning 判断规则口径与模型当前的全局计费口径是否对得上。
//
// 返回的是告警而不是错误:全局计费口径随时可能被改(管理员在「分组与模型定价设置」
// 里加一条按次价,这个模型就从按 token 变成按次了),把它做成硬校验会让一条
// 昨天还合法的规则今天改不动。但它必须在列表里持续可见 —— 一条静默不生效的
// 价格规则,和一个定义了却没有消费方的配置项是同一种缺陷。
func modeMismatchWarning(modelName, mode string) string {
	if isPattern(modelName) {
		// 通配规则覆盖的是一批模型,它们的全局计费口径可能各不相同,
		// 判不出一个确定的结论。给一个不确定的结论比不给更糟。
		return "通配规则:覆盖范围内各模型的全局计费口径可能不同,请用试算接口逐个确认是否生效"
	}
	expr, hasExpr := billing_setting.GetBillingExpr(modelName)
	tiered := billing_setting.GetBillingMode(modelName) == billing_setting.BillingModeTieredExpr &&
		hasExpr && strings.TrimSpace(expr) != ""

	switch mode {
	case ModeTiered:
		if !tiered {
			return "该模型当前不是阶梯表达式计价,tiered 乘数不会生效"
		}
	case ModePrice:
		if tiered {
			return "该模型当前是阶梯表达式计价,price 覆盖不会生效(请改用 tiered 口径)"
		}
		if _, ok := ratio_setting.GetModelPrice(modelName, false); !ok {
			return "该模型当前按 token 计费,配 price 覆盖会把它在本分组下切换成按次计费 —— " +
				"计价口径发生变化,影子差额无法按比例折算,请确认这是本意"
		}
	case ModeRatio:
		if tiered {
			return "该模型当前是阶梯表达式计价,ratio 覆盖不会生效(请改用 tiered 口径)"
		}
		if _, ok := ratio_setting.GetModelPrice(modelName, false); ok {
			return "该模型当前按次固定价计费,ratio 覆盖不会生效(请改用 price 口径)"
		}
	}
	return ""
}
