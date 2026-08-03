package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/version"

	"github.com/gin-gonic/gin"
)

// AdminVersion 返回版本三元组:二开当前版本 / 同步上游版本 / 上游内核版本。
//
// **刻意不走 requireCore(也就是 guard.RequireAPI(FlagCore))**,这是本文件
// 唯一需要解释的设计:
//
//   - 同目录的 AdminHealth 第一行就是 requireCore,而它在扩展库不可用时直接
//     503。也就是说,排障页最该被打开的那一刻,它打不开。版本信息是排障的
//     第一个问题("现在跑的到底是哪个版本"),把它挂在同一个门后面等于让这个
//     问题在最需要答案时无解。
//   - 版本号是编译期常量,不读扩展库、不读配置、不可能失败。让一个必然成功的
//     只读端点依赖一个可能不可用的组件,是纯粹的负收益。
//   - 安全边界没有放松:路由挂在 registerAdminRoutes 下,已经过 AdminAuth;
//     整个 /api/qy 组也只在 config.Enabled() 时才注册。这里放开的仅仅是
//     "扩展库连不上"这一档降级,不是鉴权。
//
// core 直接读上游已导出的包级变量 common.Version,零上游改动 —— 不需要也不
// 应该去改 controller/misc.go 的 /api/status。
func AdminVersion(c *gin.Context) {
	ok(c, gin.H{
		// 二开当前版本(git describe --tags 原样)。
		"build": version.CurrentBuild(),
		// 最近一次同步到的上游 tag。
		"upstream": version.SyncedUpstream(),
		// 上游自己的版本号。未经构建期注入时它是 common/constants.go 里的
		// "v0.0.0",这里原样透出而不做美化:那是上游自己的诚实默认值。
		"core": common.Version,
	})
}
