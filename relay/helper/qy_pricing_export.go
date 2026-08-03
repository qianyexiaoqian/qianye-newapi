package helper

// qy_pricing_export.go —— 千夜扩展「模型按分组单独定价」与上游计价链路之间的唯一耦合面。
//
// 这是一个纯新增文件。因为与调用点同包,relay/helper/price.go 里只需插入普通赋值语句,
// 连 import 都不必改 —— 上游 diff 一共 5 行,全是单行赋值,合并上游时冲突面接近 0。
//
// 铁律:本文件禁止 import 任何 qianye/* 包。relay/helper 是被扩展依赖的下层包,
// 反向依赖会形成 import 环。实现体由 qianye/modules/grouppricing 在 qianye.Init()
// 阶段注入,那一刻早于任何 HTTP 请求与后台协程,不存在并发读写窗口。
//
// 默认实现是**恒等函数**:原样返回入参。因此扩展未安装/未启用时,计价结果与上游
// 逐位一致(不是"近似一致"——恒等函数不做任何浮点运算,连一次舍入都不会引入)。
// 调用点也因此不需要任何 nil 判断或开关判断。
//
// ─────────────────────────── 为什么是这三个挂载点 ───────────────────────────
//
// 上游计价有三个入口,取值口只有三类,一个都不能漏 —— 漏一个就意味着某条计价
// 路径不按分组价走,而运营从管理界面上完全看不出来:
//
//	ModelPriceHelper        对话/文本/音频/实时  → QyGroupModelPrice + QyGroupModelRatio
//	ModelPriceHelperPerCall MJ / Task 按次       → QyGroupModelPrice + QyGroupModelRatio
//	modelPriceHelperTiered  阶梯表达式计价        → QyGroupTieredQuota
//
// 结算侧(service/text_quota.go、service/quota.go)读的是 relayInfo.PriceData,
// 而 PriceData 正是这三个函数的产物,所以覆盖这三处即覆盖全部扣费。
// 唯二的例外见 qianye/modules/grouppricing/grouppricing.go 的"已知不覆盖范围"。
//
// ─────────────────────────── 分组取值口径 ───────────────────────────
//
// 实现方必须读 info.UsingGroup(本次请求实际使用的分组),不是 info.UserGroup
// (用户所属分组)。auto 分组重试时 HandleGroupRatio 会把 UsingGroup 改写成
// 真正命中的分组,而分组倍率、消费日志的 group 列也都取自 UsingGroup。
// 取错会让"分组价"与"分组倍率"来自两个不同的分组,相乘出来的价格没有任何含义,
// 且与消费日志对不上账。
//
// 因此 QyGroupModelPrice 在 ModelPriceHelper 里的插入位置必须在
// HandleGroupRatio 之后 —— 上游原本先取价、后解析 auto 分组,顺序是反的。
// 这一点由 qianye/modules/grouppricing/hookpoint_test.go 用 AST 锁死。

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

var (
	// QyGroupModelPrice 覆盖「按次固定价」。
	//
	// 入参 price/usePrice 就是 ratio_setting.GetModelPrice 的返回值,
	// 返回值语义完全相同:(价格, 是否按固定价计费)。
	//
	// 允许把 usePrice 从 false 改成 true(给一个原本按 token 计费的模型
	// 在某个分组上配按次价),此时计费口径本身发生变化 —— 实现方必须在影子
	// 差额里把这类行标成"不可按比例折算",不能假装它是一个折扣。
	QyGroupModelPrice = func(info *relaycommon.RelayInfo, price float64, usePrice bool) (float64, bool) {
		return price, usePrice
	}

	// QyGroupModelRatio 覆盖「按 token 计费的模型倍率」。
	//
	// 入参 ratio/ok 是 ratio_setting.GetModelRatio 的前两个返回值。
	// ok 为 false 表示该模型没有配置倍率(上游据此报"价格未配置"错误),
	// 实现方返回 true 即可让分组级倍率成为该模型在该分组下的唯一价格来源。
	//
	// 刻意不覆盖 completion_ratio(补全倍率):见 grouppricing 包注释,
	// 覆盖它会破坏"实际扣费对覆盖值线性"这一性质,而影子模式的差额精确性
	// 完全建立在那个性质上。
	QyGroupModelRatio = func(info *relaycommon.RelayInfo, ratio float64, ok bool) (float64, bool) {
		return ratio, ok
	}

	// QyGroupTieredQuota 覆盖「阶梯表达式计价」。
	//
	// 入参是表达式算出的、尚未乘分组倍率的 quota。阶梯计价的"价格"是一整条
	// 表达式,没法用一个标量替换,因此分组级覆盖在这条路径上的语义是**乘数**:
	// 最终 quota = 表达式结果 × 分组乘数 × 分组倍率,与另外两条路径的
	// "分组级价 × 分组倍率"保持同一个相乘形状。
	//
	// ⚠ 返回值只用于算本次预扣的金额,**不能**写回
	// BillingSnapshot.EstimatedQuotaBeforeGroup。快照里那个字段的语义是
	// "未乘任何分组因子的表达式结果":auto 重试切分组后
	// service.refreshTieredBillingGroup 会拿它按新分组重算预留额,
	// 存进去的若是已乘乘数的值,原分组的乘数就会被带进新分组的预留额。
	QyGroupTieredQuota = func(info *relaycommon.RelayInfo, quotaBeforeGroup float64) float64 {
		return quotaBeforeGroup
	}
)
