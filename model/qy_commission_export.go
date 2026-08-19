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

// QyOnUserGroupChanged 在 users.group 发生变化并且**已经提交**之后触发。
//
// 返佣模块把每个用户的账号分组与他的邀请关系缓存在同一条进程内记录里
// (qianye/modules/commission/inviter.go),而那个分组现在直接决定这个人作为
// **推广人**时的返佣费率与法币折算比例 —— 两档都按上线自己的分组取值。
//
// 不通知的表现是:推广人刚升到 vip,接下来最长 InviterCacheSecs(默认 300 秒)
// 里他名下产生的每一笔佣金仍按旧档计,而那些行的费率是**冻结**的,事后再刷
// 缓存也追不回来。反向同理 —— 套餐到期降级之后仍按高档多发五分钟。
//
// userId <= 0 表示"一批人的分组被改了,范围未知",实现方应清空整个缓存。
// 分组批量迁移(QyRewriteUserGroup)走这一支。
//
// 只能在事务提交之后调用:事务里调等于把一次回滚变成一次错误的缓存失效,
// 下一次读会把**尚未生效**的分组当成事实缓存起来。
var QyOnUserGroupChanged = func(userId int) {}
