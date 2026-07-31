package model

// qy_commission_export.go —— 返佣模块专用的 hook 声明。
//
// 与 qy_export.go 一样是纯新增文件,合并上游时冲突为 0。单独成文件是为了
// 让并行开发的各个扩展模块各写各的,不去争同一个文件。
//
// 铁律:本文件禁止 import 任何 qianye/* 包。model 是底层包,扩展依赖它,
// 反向依赖会成环。实现由 qianye.Init() 注入。

// QyOnTaskBillingLog 在 RecordTaskBillingLog 入口触发。
//
// 它覆盖异步任务结算后的增量:LogTypeConsume 是差额补扣(继续返佣),
// LogTypeRefund 是任务退款(触发佣金冲正)。任务的首次扣费走的是
// RecordConsumeLog,由 QyOnConsumeLog 覆盖,两者不重叠。
//
// 必须挂在 common.LogConsumeEnabled 早退判断之前,否则关闭了消费日志的
// 部署会静默收不到返佣与冲正事件。
var QyOnTaskBillingLog = func(params RecordTaskBillingLogParams) {}
