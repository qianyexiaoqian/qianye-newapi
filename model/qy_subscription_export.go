package model

// qy_subscription_export.go —— 「订阅套餐全站总名额」的 hook 声明。
//
// 与 qy_export.go / qy_commission_export.go / qy_usergroup_export.go 一样是纯新增
// 文件,合并上游时冲突为 0。单独成文件是为了让并行开发的各个扩展模块各写各的,
// 不去争同一个文件。
//
// 铁律:本文件禁止 import 任何 qianye/* 包。model 是底层包,扩展依赖它,
// 反向依赖会成环。实现由 qianye.Init() 注入。

import "gorm.io/gorm"

// QyGateSubscriptionSeat 判定「这个用户现在能不能占用该套餐的一个全站名额」。
//
// # 与上游 MaxPurchasePerUser 的区别(别把两者混为一谈)
//
// 上游已有的 plan.MaxPurchasePerUser 是**每人限购次数** —— 管的是"一个人能买
// 几次"。本 hook 管的是**全站总名额** —— 同一时刻最多允许多少个**不同的人**
// 持有该套餐的有效订阅。两者互相独立,可以同时生效。
//
// 名额口径(由项目方拍板,实现方不得自行发散):
//   - 占用 = 当前 status='active' 的用户订阅里**不重复的 user_id 数量**;
//   - 同一个人持有该套餐的两条 active 订阅只占 1 个名额;
//   - 订阅到期(expired)或被取消(cancelled)后名额自动回收;
//   - 未配置或配置为 0 = 不限。
//
// # 签名为什么长这样
//
// tx:必须收下调用方的事务句柄。名额判定要数的是主库 user_subscriptions,而这
// 张表正在被同一个事务写入 —— 用另一个连接去数就看不见同事务内未提交的行,
// 一次请求里"先数后插"的自洽性直接没了。tx 为 nil 表示**预检模式**(HTTP 下单
// 之前的体验层调用,此时还没有事务),实现方回落到默认主库句柄。
//
// plan:传整个套餐而不是 plan.Id。实现方需要 plan.Enabled —— 预检模式下,
// 一个已停用且恰好卖满的套餐必须报"套餐未启用"(调用点紧随其后的那句检查),
// 而不是"名额已满",否则用户与客服会去追一个不存在的名额问题。传 Id 的话
// 实现方要么多查一次库,要么只能报错指错方向。plan 为 nil 时实现方必须放行。
//
// source:上游那个已经存在的来源标记("order" / "admin" / "balance")。
// 它是**资金安全的判据**,不是日志字段:source == "order" 意味着调用点是
// 支付回调,用户的钱已经付掉了 —— 那一刻拒绝等于把订单永久卡在 pending。
// 实现方必须对这一档 fail-open(详见下面"实现方必须 fail-open")。
//
// err:入参是调用点此刻已有的错误。这样调用点只需**新增一行**
// `err = QyGateSubscriptionSeat(...)`,复用紧随其后的既有 `if err != nil` 检查,
// 不必再插一个三行的 if 块,也不必改 import。实现方的硬性约定:
//
//	入参 err != nil 时必须原样返回它,一个字节都不许改,更不许吞掉。
//
// 这不是可选的礼貌 —— 上游那些错误(套餐不存在、周期算不出来)语义上优先于
// 名额判定,吞掉它们会把"套餐不存在"变成"名额已满"这种指错方向的报错。
//
// # 默认实现是恒等函数
//
// 扩展未安装 / 未启用时,调用点的行为与接入本 hook 之前逐字一致:err 原样透传,
// 既有的 if err != nil 该怎么走还怎么走。因此调用点不需要任何 nil 判断。
//
// # 实现方必须 fail-open
//
// 本 hook 跑在订阅创建的主库事务内部,其中一条路径是**支付回调**
// (CompleteSubscriptionOrder)—— 那时用户的钱已经付掉了。有两档必须放行:
//
//	扩展库不可用    读不到名额配置时按"不限"处理。fail-closed 的话扩展库打个嗝
//	                就会让一批已付款订单永远停在 pending。读取还要有硬超时
//	                (它压着一个已打开的主库事务)。
//	source=="order" 判定成功且**确实满员**时同样必须放行。这一条比上一条更容易
//	                被漏掉:满员是"闸门正常工作"的结果,看起来正是该拒绝的时候。
//	                但在支付回调里拒绝,回滚掉的是那个尚未写 success 的订单事务
//	                —— 钱收了、订阅发不出、订单永久 pending,而且网关每次重试都
//	                撞同一条死路。名额在这一档由下单前的预检负责(体验层),
//	                超出的部分只能事后由运营处理,那远比卡死一笔已付款订单轻。
//
// 赋值时机为 qianye.Init(),早于任何 HTTP 请求与后台协程,因此不存在并发读写
// 窗口 —— 也正因如此,运行期禁止改写本变量。
var QyGateSubscriptionSeat = func(tx *gorm.DB, plan *SubscriptionPlan, userId int, source string, err error) error {
	return err
}

// QyDowngradeUserGroupForSubscriptionTx 把一条订阅失效之后的用户分组回落暴露给扩展。
//
// 扩展的"强制删除套餐"要级联作废该套餐的全部活跃订阅,而作废一条订阅从来不只是
// 改 status:上游 AdminInvalidateUserSubscription 同时做三件事 —— 改状态、把
// end_time 推到当下、按 downgrade_group/prev_user_group 回落用户分组。只改状态的话
// 被删套餐升过组的用户会**永久**留在高级分组里:到期扫描只看 status='active',
// 回落目标只从 status='expired' 里找,cancelled 两边都不命中,系统此后没有任何
// 路径能把他们降回来。
//
// 导出而不是让扩展自己复刻一遍:这段逻辑有"还有别的升级订阅就不回落""显式降级组
// 优先于购买前快照"等多个分支,复刻出来的第二份必然与上游漂移。
func QyDowngradeUserGroupForSubscriptionTx(tx *gorm.DB, sub *UserSubscription, now int64) (string, error) {
	return downgradeUserGroupForSubscriptionTx(tx, sub, now)
}

// QyRefreshSubscriptionUserGroupCache 在分组回落提交之后刷新用户分组缓存。
// 不刷的话库里已经降级、缓存里还是高级组,用户会继续按高级组的价格与模型权限跑。
func QyRefreshSubscriptionUserGroupCache(userId int, operation string) {
	refreshSubscriptionUserGroupCache(userId, operation)
}
