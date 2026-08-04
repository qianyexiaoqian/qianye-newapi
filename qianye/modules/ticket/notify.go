package ticket

import (
	"fmt"
	"runtime/debug"

	"github.com/QuantumNous/new-api/common"
)

// 通知口子。
//
// # 本轮**不做**站内信/邮件,但必须先留出接口
//
// 需求明确"通知先不做"。问题在于:等到真要做的时候,发信这件事会被塞进哪里?
// 没有约定的话,答案通常是"在每个 handler 里各加一段 SMTP 调用" —— 于是
// 四个写入点各有一份收件人计算、各有一份失败处理,而其中一处会忘了判
// "别把用户自己的回复通知给他自己"。
//
// 所以这里先把**唯一的出口**钉住:三个事件,一个函数变量,四个调用点。
// 接通知的那一天只需要在 InstallHooks 里给 Notify 赋一个实现,
// 业务代码一行都不用改,也不会长出第二个发信点。
//
// # 为什么是函数变量而不是接口/注册表
//
// 与上游 hook 的写法保持一致(`var QyOnX = func(...){}`):这套写法在本仓已经
// 有既成惯例,调用点只有一行、不引入 import、不需要初始化顺序假设。
// 注册表(多个订阅者)是过度设计 —— 通知的订阅者天然只有一个"发信服务"。
//
// # 实现方要遵守的两条
//
//  1. **不要在里面做同步 I/O**。调用点在 HTTP 请求的返回路径上,
//     一次 SMTP 超时会变成用户眼里的"提交卡住了"。丢进队列再返回。
//  2. **自己判重**。用户回复自己的工单不该给自己发信;管理员的内部备注
//     (Internal)根本不会走到这里 —— 那是 reply.go 已经过滤掉的。
const (
	NotifyCreated = "created" // 用户新建工单(收件人:客服)
	NotifyReplied = "replied" // 有人追加了一条对方可见的消息(收件人:另一方)
	NotifyClosed  = "closed"  // 工单被关闭(收件人:工单所属用户)
)

// NotifyEvent 是一次工单通知的全部输入。
//
// 刻意**不含消息正文**:正文可能有几 KB 且是用户隐私,通知实现真要用它,
// 应当自己按 TicketId 去读 —— 那时它至少要显式决定"要不要把用户的原文
// 塞进一封明文邮件里"。
type NotifyEvent struct {
	Kind        string
	TicketId    int64
	TicketNo    string
	OwnerUserId int
	Title       string
	Priority    string

	// ActorType ∈ user|admin|system,ActorName 是操作者显示名。
	ActorType string
	ActorName string
}

// Notify 是工单通知的唯一出口。默认空实现 —— 本轮不发任何通知。
var Notify = func(NotifyEvent) {}

// notify 调用 Notify 并吞掉 panic。
//
// 通知是附加物:一个写错的实现 panic 之后,不该让用户看到"工单提交失败" ——
// 工单事务此刻**已经提交**了,报错只会让他再提交一遍。
func notify(ev NotifyEvent) {
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf(
				"qianye/ticket: 通知回调 panic(已拦截,工单本身不受影响): %v\n%s", r, debug.Stack()))
		}
	}()
	Notify(ev)
}
