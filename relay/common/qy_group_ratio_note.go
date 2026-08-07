package common

// qy_group_ratio_note.go —— 纯新增文件,合并上游时冲突为 0。
//
// 它只做一件事:让「这一笔按凭空的 1.0 扣了费」这个标记在**两个包**里以同一种
// 方式挂上去。relay/helper 与 service 各有计费路径,而 relay/helper import service,
// 所以两者不可能共用任一方的私有函数;RelayInfo 是它们共同持有的那个对象,
// 方法挂在这里是唯一不制造第二份判据的位置。

import "github.com/QuantumNous/new-api/setting/ratio_setting"

// NoteGroupRatioFallback 记录一次静默 fail-open。
//
// 判据完全委托给 res.SilentFallback():调用方**不得**自己写
// `if res.BaseMissing && ...` —— 那正是三条计费路径漂移的起点。
//
// 非静默兜底(命中交叉格但兜底缺失)刻意不记:那一笔的价是运营配出来的,
// 记成资损是假阳性,而假阳性会把这张表刷成没人看的噪声。
// **标记是每一轮重新计算的,不是累加的。** HandleGroupRatio 在 auto / 跨分组重试的
// 每一轮都会被重跑(controller/relay.go 的重试循环里每轮一次 getChannel → 一次
// HandleGroupRatio),而 relayInfo 是循环外创建的同一个对象。非静默兜底时直接
// return 会把失败那一轮留下的标记原封不动带进成功那一轮的消费日志:请求按正确
// 倍率成功计费,日志里却挂着一个指向别的模型分组的 group_ratio_missing,管理员
// 按它补差就是给一笔本来收对了的请求退钱。所以这里必须先清空。
func (info *RelayInfo) NoteGroupRatioFallback(res ratio_setting.GroupRatioResolution) {
	if info == nil {
		return
	}
	if !res.SilentFallback() {
		info.GroupRatioFallback = nil
		return
	}
	info.GroupRatioFallback = &ratio_setting.GroupRatioMiss{
		UserGroup:    info.UserGroup,
		ModelGroup:   info.UsingGroup,
		AppliedRatio: res.Ratio,
	}
}

// QyWssGroupRatioPin 是一次 WSS 实时会话内钉住的分组倍率解析结果。
//
// 连同两个分组名一起存:auto 分组改写会在会话开始后改动 UsingGroup,
// 只存倍率的话,坐标变了倍率还挂在旧坐标上,而那是查不出来的。
type QyWssGroupRatioPin struct {
	UserGroup  string
	ModelGroup string
	Res        ratio_setting.GroupRatioResolution
}

// ResolveWssGroupRatio 解析分组倍率,并在**同一次实时会话内**钉住它。
//
// ── 为什么实时会话必须钉 ──
//
// 一次 WSS 会话会调很多次 PreWssConsumeQuota,每一次都真的扣一笔钱。
// 现场重算的话,运营在会话中途改一次 (用户分组, 模型分组) 的交叉倍率,
// 同一次会话的前半段与后半段就按两个价收费,而用户从头到尾看到的是同一个价;
// 方向反过来时是平台白亏。同步的文本/音频路径读的是请求开始时 HandleGroupRatio
// 钉下的 PriceData,异步 Task 读的是 TaskBillingContext 的 pin —— 这条是最后一处
// 现场重算的计费路径。
//
// 坐标(用户分组 / 模型分组)变了就重新解析并重新钉:那不是"倍率被改了",
// 那是这一段用量本来就属于另一个格子。
// 解析器**只在这里调一次**:多写一处(例如给 info == nil 单开一条早退)会让
// 「计费路径只有一个解析器」的守卫抓不到形状,而那条守卫正是靠调用数与实参
// 表达式来发现对角格缺陷的。
func (info *RelayInfo) ResolveWssGroupRatio() ratio_setting.GroupRatioResolution {
	userGroup, modelGroup := "", ""
	if info != nil {
		userGroup, modelGroup = info.UserGroup, info.UsingGroup
		if pin := info.QyWssGroupRatioPin; pin != nil &&
			pin.UserGroup == userGroup && pin.ModelGroup == modelGroup {
			return pin.Res
		}
	}
	res := ratio_setting.ResolveGroupRatio(userGroup, modelGroup)
	if info != nil {
		info.QyWssGroupRatioPin = &QyWssGroupRatioPin{
			UserGroup:  userGroup,
			ModelGroup: modelGroup,
			Res:        res,
		}
	}
	return res
}
