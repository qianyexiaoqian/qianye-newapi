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
