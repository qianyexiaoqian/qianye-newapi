// Package hooks 负责把扩展的实现注入到上游包的 hook 变量里。
//
// 为什么用变量注入而不是直接 import:
// model / service / pkg 是底层包,扩展依赖它们。如果这些包反过来 import qianye/*,
// 就形成循环依赖,编译不过。
//
// 变量注入把方向倒过来:上游包只声明一个默认空实现的函数变量(在各自的
// qy_export.go 里,那是纯新增文件),扩展在启动时给它赋值。上游既有文件中
// 只需要插入一行同包调用,连 import 都不必改 —— 这是把改动预算压到
// 二十行以内的关键。
package hooks

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Install 注入全部 hook 实现。
//
// 调用时机为 qianye.Init(),它在 main.go 的 InitResources() 内执行,
// 早于 HTTP 监听与所有后台协程,因此这些变量的赋值不存在并发窗口。
// 也正因如此,运行期禁止再改写它们。
func Install() {
	installConsumeHooks()
	common.SysLog("qianye: 上游 hook 已注入")
}

// installConsumeHooks 挂载消费与充值相关的 hook。
//
// 具体实现由返佣模块(P4)提供,当前阶段先保持上游默认的空实现 ——
// 地基阶段只验证注入机制本身可用。
func installConsumeHooks() {
	// P4 返佣模块落地后在此注入:
	//   model.QyOnConsumeLog = commission.OnConsumeLog
	//   model.QyOnRedeemSuccess = commission.OnRedeemSuccess
	//
	// 实现体必须走 guard.HotAsync:消费日志在 relay 结算路径上,
	// 同步查库会给主库加上与 relay QPS 等量的读压力。
	_ = model.QyOnConsumeLog
}
