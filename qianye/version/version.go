// Package version 暴露本 fork 的版本标识,供运维排障与检查更新使用。
//
// # 两个版本号,互不相干
//
// 这是本包最重要的一条,也是它曾经做错过的那一条:
//
//	内核版本 CoreVersion  上游 new-api 的版本,与上游**逐字一致**,不带任何后缀。
//	                      构建脚本把它注入上游的 common.Version,于是 /api/status
//	                      的 version、X-New-Api-Version 响应头、以及「系统维护 →
//	                      当前版本」三处与上游完全同义。
//	二开版本 ForkVersion  我们自己的版本号,与上游的版本节奏无关,可比大小,
//	                      单独一栏显示,并且有自己的检查更新。
//
// 一度有一版把两者合成 `v1.0.0-rc.25+qy.2` 塞进 common.Version。合并的代价很具体:
// 「当前版本」那一栏于是既不是上游版本也不是我们的版本;上游那颗「检查更新」
// 按钮拿它跟上游 release 的 tag_name 做**相等比较**,永远不相等,于是永远报
// 「有新版本」;而 /api/status 的 version 是上游既有契约,外部脚本按上游版本号
// 的形状在读它。两个号分开之后这三条同时消失。
//
// # 三个值,两种来源
//
//	Build     二开当前提交   构建期 ldflags 注入(git describe 的原样输出)
//	Core/Fork/Upstream       baseline.txt 声明,编译期 go:embed 进二进制
//
// 为什么 Build 走注入而其余走声明 —— 这是本包唯一需要解释的设计:
//
//   - Build 回答「这个二进制是哪个提交编出来的」。它**只有构建那一刻的 git**
//     知道,源码里写不出来,所以必须注入,注不进就诚实报 unknown。
//   - 「同步到上游哪一版」一度也走注入,值由 `git describe --tags --abbrev=0`
//     算出 —— 而那个命令量的是 **tag 可达性**。本 fork 靠逐提交挑拣同步,挑拣
//     不产生祖先关系,于是树里已经是 rc.25 了,describe 还在报 rc.24,且不报错。
//     更要命的是这个结论**带人的判断**(哪些提交故意不同步),没有哪条 git
//     命令算得出来。结论:它不是「构建期才知道」的量,而是「同步时人拍板」的量。
//   - 二开版本更彻底:「这一轮该进 MINOR 还是 PATCH」根本不是可计算的量。
//
// Build 由构建期 ldflags 注入(见 qianye/scripts/build.ps1 与 build.sh):
//
//	-X 'github.com/QuantumNous/new-api/qianye/version.Build=v1.0.0-rc.24-109-g1228d77e8'
//
// -X 的符号路径必须写完整模块路径。写成 `new-api/qianye/version.Build` 这类短
// 路径时,Go 链接器会**静默丢弃**这条 -X:不报错、不告警、构建照样成功,只是
// 版本永远是默认值。仓库里 .github/workflows/release.yml 与 electron-build.yml
// 的若干处正是这个形态(上游既有缺陷),不要以它们为模板 —— 要照抄的是
// Dockerfile 那行。
//
// 同一条链接器规则也是声明值不能「注入优先、声明兜底」的原因:-X 只能覆盖
// **常量字面量初始化**的字符串变量。一旦把声明值写成变量的初值,它就不再是常量
// 初始化,-X 会被静默丢弃 —— 于是「两个来源」在实现上根本立不住,只会变成一个
// 看起来可覆盖、实际永远覆盖不了的陷阱。所以声明侧索性只保留声明这一个来源。
//
// 为什么单独成包而不是放在 qianye 根包:根包 qianye 已经 import 了
// qianye/controller(RegisterRoutes 要挂路由),controller 若反向 import 根包
// 就构成导入循环。本包刻意零依赖(只用 strconv/strings 与 embed),任何层都能引用。
package version

import (
	_ "embed"
	"strconv"
	"strings"
)

// Build 是二开当前构建的提交,取 `git describe --tags` 的原样输出,
// 例 v1.0.0-rc.24-109-g1228d77e8。
//
// 它**不是**二开版本号(那是 ForkVersion):这个值是从上游的 tag 算出来的,
// 上游一打新 tag 它就整体跳变,而且每次提交都变。它的用途只有一个 ——
// 排障时回答「你这台机器上跑的到底是哪个提交」。
//
// 刻意留空,而不是预置一个像模像样的版本号:未注入时报 "unknown" 远比报一个假
// tag 安全 —— 排障时被伪造的版本号误导,比根本看不到版本号更糟。
//
// 只能是简单字符串字面量初始化(这里是零值),否则链接器无法用 -X 覆盖。
var Build string

// Unknown 是未注入、或声明缺失/不可解析时的取值。
const Unknown = "unknown"

// ForkTagPrefix 是二开版本在 fork 仓库里对应的 git tag 前缀,
// 例如版本 v0.1.0 对应 tag `qy-v0.1.0`。
//
// 加前缀不是洁癖:上游的 tag 名字全是 `v*`,一旦有人把 upstream 的 tag 推进
// fork(`git push --tags` 一次就够),不带前缀的两套 tag 会混在同一个命名空间
// 里,检查更新会把上游的 release 当成我们的,并且报出一个我们从没发布过的版本。
const ForkTagPrefix = "qy-"

// baselineFile 是版本声明。用 go:embed 而不是运行时读盘:文件被删或改名
// 时**编译失败**,而不是等到线上才悄悄退回 unknown。
//
//go:embed baseline.txt
var baselineFile string

// baseline 是 baseline.txt 解析后的三个字段。
type baseline struct {
	upstreamTag      string
	upstreamDescribe string
	qyVersion        string
}

// declared 在包初始化时解析一次。解析失败不 panic:版本号是排障用的展示值,
// 为了它把整个进程带走是负收益 —— 形状错误由 version_test.go 在 CI 拦下。
var declared = parseBaseline(baselineFile)

// parseBaseline 解析 `key=value` 形式的声明文件。
//
// 注释行不需要单独剔除:键名是**精确匹配**的,`# 例如 upstream_tag=v9.9.9`
// 切出来的键是 `# 例如 upstream_tag`,撞不上三个真键中的任何一个。曾经这里有一条
// `strings.HasPrefix(line, "#")` 的跳过分支,变异测试证明它删掉之后没有任何一条
// 用例会红 —— 它是死代码,真正在挡注释注入的是下面那个 switch 的精确匹配,
// 所以留一条用例专门盯住「注释里写了真键名也不生效」。
//
// 同名键取**最后一次**出现,与 shell / dotenv 一族的惯例一致;构建脚本
// (build.ps1 / build.sh)读同一个文件,两侧口径必须一样,否则同一份声明能编出
// 两个版本号。
func parseBaseline(raw string) baseline {
	var b baseline
	for _, line := range strings.Split(raw, "\n") {
		// TrimSpace 顺带去掉 CRLF 的 CR:这个文件在 Windows 上被编辑过就会带上,
		// 而残留的 CR 会把 ldflags 的一个参数拆成两个。
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "upstream_tag":
			b.upstreamTag = value
		case "upstream_describe":
			b.upstreamDescribe = value
		case "qy_version":
			b.qyVersion = value
		}
	}
	return b
}

// CurrentBuild 返回二开当前构建的提交(git describe 原样)。
func CurrentBuild() string { return orUnknown(Build) }

// SyncedUpstream 返回同步基线的精确提交,例 v1.0.0-rc.25-1-g2d8e50bf3。
//
// 返回 describe 而不是光秃秃的 tag:基线常常落在两个 tag 之间,只报 tag 会把
// 「rc.25 之后又同步了一个提交」说成「rc.25」。
//
// 它与 CoreVersion 的区别正是「精确到提交」与「与上游逐字一致」之差:对外那一栏
// 必须能跟上游的 release 对上号,这一栏则是给排障的人看的更细的位置。
func SyncedUpstream() string { return orUnknown(declared.upstreamDescribe) }

// CoreVersion 返回内核版本,即上游 new-api 的版本号,**逐字一致、不带后缀**。
//
// 这个值经构建脚本注入上游的 common.Version,于是 /api/status 的 version 字段、
// X-New-Api-Version 响应头、以及「系统维护 → 当前版本」都与上游同义。
// 任何后缀都会破坏这一点:上游那颗检查更新按钮把它跟 release 的 tag_name 做
// **相等比较**,加了后缀就永远不相等,于是永远报「有新版本」。
func CoreVersion() string { return orUnknown(declared.upstreamTag) }

// ForkVersion 返回二开版本号,形如 v0.1.0。
//
// 它与内核版本互不相干:同步一次上游不会让它进位,发一版二开也不会改内核版本。
// 形状恒为 vMAJOR.MINOR.PATCH —— 因为它必须能比大小(见 CompareFork)。
func ForkVersion() string { return orUnknown(declared.qyVersion) }

// ForkVersionNumbers 解析形如 `v1.2.3` 的二开版本号,返回三段数字。
//
// 接受可选的 `qy-` 前缀:远端 release 的 tag 名字带前缀(qy-v0.1.0),而声明里
// 存的是不带前缀的版本号(v0.1.0),两侧要能互相比较。`v` 本身也是可选的,
// 因为「发版时手滑漏了 v」是个真实的、且完全无害可容忍的输入。
//
// ok=false 表示这个字符串不在我们的版本方案里,调用方必须把它当成
// **「比不出来」**而不是「更旧」—— 把不认识的版本当成更旧会让检查更新在远端
// 用了别的命名方案时报「你已是最新」,那是最坏的一种错。
func ForkVersionNumbers(raw string) (major, minor, patch int, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, ForkTagPrefix)
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	out := [3]int{}
	for i, part := range parts {
		// 显式拒空:strconv.Atoi("") 也会报错,但 "01" 这类前导零是合法的,
		// 而 "+1" / "-1" 会被 Atoi 接受 —— 后者必须挡掉,负版本号没有意义,
		// 而且会让比较得出「-1 比 0 旧」这种查无此版的结论。
		if part == "" || strings.ContainsAny(part, "+-") {
			return 0, 0, 0, false
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, false
		}
		out[i] = n
	}
	return out[0], out[1], out[2], true
}

// ForkReleaseTagNumbers 解析**远端 release 的 tag**,前缀 `qy-` 是强制的。
//
// # 为什么这一侧必须严,而声明那一侧可以宽
//
// 判据是非对称的,因为两侧的输入来源完全不同:
//
//	声明侧  qianye/version/baseline.txt 里我们自己写下的 v0.1.0 —— 可信,
//	        前缀可有可无(发版时手滑漏个 v 是无害的输入)。
//	远端侧  GitHub 上 fork 仓库里**任何人推上去的任何一个 tag** 建出的 release
//	        —— 不可信,必须先证明"这是我们自己发的版"。
//
// `qy-` 前缀存在的全部理由就是这一条,而它以前是可选的,于是这层保护是空的:
// 上游的 tag 全是 `v*`,一旦有人 `git push --tags` 把 upstream 的 tag 推进 fork,
// .github/workflows/release.yml 的触发条件是 `push: tags: ['*','!*-alpha*']`,
// 也就是**自动**建出 Release。本地这份 clone 挂着 upstream 远端,681 个 tag 里
// 有 77 个是无预发布段的 `vX.Y.Z` 形态(v0.9.28 / v0.10.5 / v1.0.0 …),
// 全部可解析且全部大于 v0.1.0 —— 界面于是报「有新版本 v0.9.28(当前 v0.1.0)」
// 并把管理员指向那个 release 页。更糟的是那个 release 是在 fork 的树上按该 tag
// 构建的**纯上游代码**,不含 qianye 扩展;管理员照着这条升级提示下载替换,
// 等于把带扩展的部署降级成上游版。
//
// 不能简单地在 ForkVersionNumbers 上强制前缀:两侧共用同一个解析器时,
// 声明里的 v0.1.0 也会解析失败,检查更新恒为 current_unknown。
func ForkReleaseTagNumbers(raw string) (major, minor, patch int, ok bool) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, ForkTagPrefix) {
		return 0, 0, 0, false
	}
	return ForkVersionNumbers(s)
}

// CompareForkToReleaseTag 把本机声明的二开版本与**远端 release 的 tag** 比大小。
//
// 与 CompareFork 的差别只有一处:远端那一侧走 ForkReleaseTagNumbers,
// 也就是要求 `qy-` 前缀。不认识的 tag 返回 ok=false,调用方必须按
// **「比不出来」**处理而不是「更旧」—— 把不认识的版本当成更旧,会在远端换了
// 命名方案时报「你已是最新」,那是最坏的一种错,因为它让人停止检查。
func CompareForkToReleaseTag(current, releaseTag string) (int, bool) {
	cMajor, cMinor, cPatch, cOK := ForkVersionNumbers(current)
	rMajor, rMinor, rPatch, rOK := ForkReleaseTagNumbers(releaseTag)
	if !cOK || !rOK {
		return 0, false
	}
	for _, pair := range [3][2]int{{cMajor, rMajor}, {cMinor, rMinor}, {cPatch, rPatch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// CompareFork 比较两个二开版本号,返回 -1 / 0 / +1(a 更旧 / 相等 / a 更新)。
//
// ok=false 表示**任意一侧**不可解析。此时调用方不许猜:检查更新会把这种情况
// 显示成「远端版本号不在已知方案里,无法判断新旧」,并把两个原值都摆出来让人
// 自己看 —— 而不是塌成「已是最新」或「有新版本」中的任何一个。
func CompareFork(a, b string) (int, bool) {
	aMajor, aMinor, aPatch, aOK := ForkVersionNumbers(a)
	bMajor, bMinor, bPatch, bOK := ForkVersionNumbers(b)
	if !aOK || !bOK {
		return 0, false
	}
	for _, pair := range [3][2]int{{aMajor, bMajor}, {aMinor, bMinor}, {aPatch, bPatch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

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
