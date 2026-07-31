package perfmetrics

// qy_export.go —— 千夜扩展「模型可用率监控」与上游 pkg/perf_metrics 包的唯一耦合面。
//
// 这是纯新增文件,合并上游时冲突为 0。hook 变量带 no-op 默认实现,因此:
//   - 调用点是同包调用,连 import 都不用改,只加一行;
//   - 扩展未启用(甚至整个 qianye 目录被删掉)时行为与上游逐字节一致,无需 nil 判断。
//
// 赋值只在 qianye.Init() 里发生一次,早于 HTTP 监听与后台协程,不存在并发读写窗口。
//
// 铁律:本文件禁止 import 任何 qianye/* 包 —— pkg/perf_metrics 是被扩展依赖的一方
// (service 已经 import 它),反向依赖会形成 import 环。

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// QyOnRelaySample 在每次端到端 relay 采样结束时触发。
//
// 为什么挂在 RecordRelaySample 而不是 Record:Record 收到的 Sample 只有一个裸 bool,
// 无法区分「上游 5xx」「用户 4xx」「额度不足」「客户端断开」,可用率口径就无从谈起。
// RelayInfo 则带着 LastError(错误码 + HTTP 状态)、StreamStatus(软失败与断流原因)、
// UsingGroup(auto 解析之后的真实分组)与 ChannelMeta,分类所需的信息全部在这里。
//
// 实现约定(违反任一条都会伤到 relay):
//   - 只做纯内存读与 O(1) 计算,任何 DB/Redis/网络 IO 必须交给 guard.HotAsync 的 worker;
//   - 不得把 info 指针带出本次调用 —— relay 结束后 StreamStatus 仍可能被其它协程写;
//   - 自行吞掉 panic。
//
// 注意与上游 Record 的差别:本 hook 不受 perf_metrics_setting.Enabled 支配。
// 那个开关是管理员随时可关的,可用率看板不能因此凭空断档。
var QyOnRelaySample = func(info *relaycommon.RelayInfo, success bool, outputTokens int64) {}
