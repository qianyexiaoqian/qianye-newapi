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
//
// 四个字段回答四个不同的问题,不要合并 —— 尤其是 core 和 fork:
//
//	core     上游 new-api 的内核版本是哪一版?  运行期实际的 common.Version
//	fork     我们自己的二开版本是第几版?      baseline.txt 声明,go:embed 编进来
//	upstream 我们的代码同步到上游哪个提交?    baseline.txt 声明,比 core 更细
//	build    这个二进制是我们哪个提交编的?    构建期注入,注不进报 unknown
//
// core 与 fork 是**两个互不相干的版本号**,曾经有一版把它们合成
// `v1.0.0-rc.25+qy.2` 一个字符串,那正是要拆掉的东西:合并之后「当前版本」
// 既不是上游版本也不是我们的版本,上游那颗检查更新按钮拿它跟 release 的
// tag_name 做相等比较于是永远报「有新版本」。
//
// core 刻意报**运行期实际值**而不是由声明重新算一遍:构建脚本正常走完时它
// 等于声明里的 upstream_tag,而漏了 ldflags 的二进制在这里会露出上游默认值
// v0.0.0。让这两种情况长得不一样,才能一眼看出「这个包是不是按流程出的」——
// 若这里改成现算,一个没走构建脚本的二进制也会报出一个漂亮的版本号,
// 而 /api/status 仍是 v0.0.0,两边对不上却谁都不报错。
//
// fork 反过来只能来自声明:它不经 ldflags(声明值一旦写成变量初值就不再是
// 常量初始化,-X 会被链接器静默丢弃,理由见 qianye/version 的包注释),
// 因此这里读的就是唯一那份来源。
func AdminVersion(c *gin.Context) {
	ok(c, gin.H{
		// 二开当前构建的提交(git describe --tags 原样)。它**不是**版本号,
		// 是"你这台机器跑的哪个提交"。
		"build": version.CurrentBuild(),
		// 同步基线的精确提交,例 v1.0.0-rc.25-1-g2d8e50bf3。
		"upstream": version.SyncedUpstream(),
		// 上游自己的版本号变量。未经构建期注入时它是 common/constants.go 里的
		// "v0.0.0",这里原样透出而不做美化:那是上游自己的诚实默认值。
		"core": common.Version,
		// 二开版本号,例 v0.1.0。检查更新比的就是它。
		"fork": version.ForkVersion(),
	})
}
