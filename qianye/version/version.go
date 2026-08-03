// Package version 暴露二开自身的版本标识,供运维排障使用。
//
// 两个值都由构建期 ldflags 注入(见 qianye/scripts/build.ps1 与 build.sh):
//
//	-X 'github.com/QuantumNous/new-api/qianye/version.Upstream=v1.0.0-rc.23'
//	-X 'github.com/QuantumNous/new-api/qianye/version.Build=v1.0.0-rc.23-16-g422ba0a3'
//
// **符号路径必须写完整模块路径**。写成 `new-api/qianye/version.Build` 这类短路径时,
// Go 链接器会**静默丢弃**这条 -X:不报错、不告警、构建照样成功,只是版本永远是
// 默认值。仓库里 .github/workflows/release.yml 与 electron-build.yml 的若干处正是
// 这个形态(上游既有缺陷),不要以它们为模板 —— 要照抄的是 Dockerfile 那行。
//
// 为什么单独成包而不是放在 qianye 根包:根包 qianye 已经 import 了
// qianye/controller(RegisterRoutes 要挂路由),controller 若反向 import 根包
// 就构成导入循环。本包刻意零依赖(只用 strings),任何层都能引用。
package version

import "strings"

// Upstream 是最近一次同步到的上游 tag,例 v1.0.0-rc.23。
// Build 是二开当前版本,取 `git describe --tags` 的原样输出,
// 例 v1.0.0-rc.23-16-g422ba0a3。
//
// 两者刻意留空,而不是预置一个像模像样的版本号:未注入时报 "unknown"
// 远比报一个假 tag 安全 —— 排障时被伪造的版本号误导,比根本看不到版本号更糟。
//
// 只能是简单字符串字面量初始化(这里是零值),否则链接器无法用 -X 覆盖。
var (
	Upstream string
	Build    string
)

// Unknown 是未注入、或注入了空串/纯空白时的取值。
const Unknown = "unknown"

// CurrentBuild 返回二开当前版本。
func CurrentBuild() string { return orUnknown(Build) }

// SyncedUpstream 返回最近一次同步到的上游 tag。
func SyncedUpstream() string { return orUnknown(Upstream) }

// orUnknown 把"没注入"和"注入了空串"归一到同一个诚实的取值。
//
// 后者不是假设:ldflags 里 `-X 'pkg.Build='`(git 不可用时脚本退化的形态)
// 是合法的,链接器会老老实实写入空串,前端拿到后渲染成一片空白 ——
// 那看起来像页面坏了,而不像"这台机器上没有版本信息"。
func orUnknown(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return Unknown
	}
	return v
}
