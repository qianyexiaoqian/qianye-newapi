package version

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upstreamTagPattern 是上游 release tag 的形状:vX.Y.Z,可带 `-rc.N` 一类预发布段。
// 这里只管形状,不管值 —— 值对不对由人在同步时判断,形状错了(少个 v、写成
// rc.52、粘上 CR)则纯粹是笔误,应该在 CI 就红,而不是编进二进制发出去。
var upstreamTagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)*$`)

// describeSuffixPattern 是 `git describe --tags` 在 tag 之后追加的 `-<n>-g<sha>`。
var describeSuffixPattern = regexp.MustCompile(`^-\d+-g[0-9a-f]{7,40}$`)

// 随仓库发布的 baseline.txt 必须是可解析且形状合法的。
//
// 这条不是把文件内容抄一遍(那样的断言只会跟着改动一起改,什么都挡不住),
// 它挡的是**笔误**:tag 少写 v、describe 与 tag 对不上号(典型是只改了其中一个)、
// 二开版本写成 tag 形状或还停在 v0.0.0。这几种都会让线上报出一个查无此版的版本号。
func TestShippedBaselineIsWellFormed(t *testing.T) {
	b := parseBaseline(baselineFile)

	require.NotEmpty(t, b.upstreamTag, "baseline.txt 缺 upstream_tag")
	require.NotEmpty(t, b.upstreamDescribe, "baseline.txt 缺 upstream_describe")
	require.NotEmpty(t, b.qyVersion, "baseline.txt 缺 qy_version")

	assert.Regexp(t, upstreamTagPattern, b.upstreamTag,
		"upstream_tag 不是 vX.Y.Z[-预发布] 形态")

	// describe 要么正好落在 tag 上,要么是该 tag 加上 `-<n>-g<sha>`。
	// 这条能抓住「改了 tag 忘了改 describe」——两处指向不同 release 时必红。
	assert.True(t, strings.HasPrefix(b.upstreamDescribe, b.upstreamTag),
		"upstream_describe(%s)没有以 upstream_tag(%s)开头,两处指向了不同的上游版本",
		b.upstreamDescribe, b.upstreamTag)
	if suffix := strings.TrimPrefix(b.upstreamDescribe, b.upstreamTag); suffix != "" {
		assert.Regexp(t, describeSuffixPattern, suffix,
			"upstream_describe 的后缀不是 git describe 的 -<n>-g<sha> 形态")
	}

	// 二开版本必须落在 CompareFork 认得的方案里 —— 认不出来的话检查更新会退化成
	// 「比不出来」,那颗按钮就等于没有。
	major, minor, patch, ok := ForkVersionNumbers(b.qyVersion)
	require.True(t, ok, "qy_version(%s)不是 vMAJOR.MINOR.PATCH 形态", b.qyVersion)
	assert.NotEqual(t, [3]int{0, 0, 0}, [3]int{major, minor, patch},
		"qy_version 还停在 v0.0.0:它比任何已发布版本都旧,检查更新会永远报有新版")

	// 两个版本号必须真的是两个。撞在一起说明又有人把它们合成了一个值。
	assert.NotEqual(t, b.upstreamTag, b.qyVersion,
		"二开版本与内核版本取了同一个值,这两个号刻意互不相干")
	assert.NotContains(t, b.upstreamTag, "qy",
		"upstream_tag 里出现了 qy 后缀 —— 内核版本必须与上游逐字一致")
}

// 解析器要扛住这个文件在真实编辑中会长成的样子。
//
// 每一条都对应一种改坏方式:注释里带等号(这个文件里就有)、Windows 编辑器留下
// 的 CRLF、手滑加的空格、同步时在文件末尾追加而不是就地改(重复键)。
func TestParseBaselineHandlesRealEditingShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want baseline
	}{
		{
			name: "标准三行",
			raw:  "upstream_tag=v1.0.0-rc.25\nupstream_describe=v1.0.0-rc.25-1-g2d8e50bf3\nqy_version=v0.1.0\n",
			want: baseline{"v1.0.0-rc.25", "v1.0.0-rc.25-1-g2d8e50bf3", "v0.1.0"},
		},
		{
			// 注释行里写了**真键名**也不能生效。挡住它的是 switch 的精确匹配:
			// 切出来的键是 `# 例如 upstream_tag`,不是 `upstream_tag`。
			// 把匹配放宽成 HasSuffix/Contains 之类,这条立刻红 —— baseline.txt 的
			// 说明文字里就举了带键名的例子,放宽匹配等于让注释改写真实版本号。
			// 注释刻意放在真键**之后**:放在前面的话,后一行的正常赋值会把注入
			// 值覆盖掉(同名键取最后一次),用例就算实现被改坏也照样绿。
			name: "注释里写了真键名也不参与赋值",
			raw:  "upstream_tag=v1.0.0-rc.25\nqy_version=v0.1.0\n# 例如 upstream_tag=v9.9.9\n# 例如 qy_version=v9.9.9\n",
			want: baseline{upstreamTag: "v1.0.0-rc.25", qyVersion: "v0.1.0"},
		},
		{
			// CR 留在值尾部会被原样拼进 ldflags,把一个参数拆成两个,
			// 链接器随后报 unknown flag —— 而且报的位置离病因很远。
			name: "CRLF 与首尾空格被清掉",
			raw:  "upstream_tag = v1.0.0-rc.25 \r\n  qy_version=v0.1.0\r\n",
			want: baseline{upstreamTag: "v1.0.0-rc.25", qyVersion: "v0.1.0"},
		},
		{
			name: "空行与纯注释行被跳过",
			raw:  "\n# 说明\n\nupstream_tag=v1.0.0-rc.25\n\n",
			want: baseline{upstreamTag: "v1.0.0-rc.25"},
		},
		{
			// 同名键取最后一次:同步时在文件末尾追加新值是很自然的手法,
			// 必须与 build.ps1 / build.sh 的读法一致,否则同一份声明编出两个版本号。
			name: "重复键取最后一次出现",
			raw:  "upstream_tag=v1.0.0-rc.24\nupstream_tag=v1.0.0-rc.25\n",
			want: baseline{upstreamTag: "v1.0.0-rc.25"},
		},
		{
			// 上一版的键名。留着这条是因为它现在**必须被忽略**:如果哪天有人
			// 把合成版本号的老实现搬回来,这条会提醒他键已经换了。
			name: "已废弃的 qy_iteration 不再被认",
			raw:  "qy_iteration=2\nupstream_tag=v1.0.0-rc.25\n",
			want: baseline{upstreamTag: "v1.0.0-rc.25"},
		},
		{
			name: "不认识的键与没有等号的行被忽略",
			raw:  "garbage\nsome_other_key=x\nupstream_tag=v1.0.0-rc.25\n",
			want: baseline{upstreamTag: "v1.0.0-rc.25"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parseBaseline(tc.raw))
		})
	}
}

// 内核版本逐字等于声明的上游 tag —— 没有前缀、没有后缀、不做任何合成。
//
// 这一条是本轮改动的核心。曾经的实现返回 `<tag>+qy.<轮次>`,而那个值会被注入
// common.Version,让 /api/status 与上游那颗检查更新按钮同时失真。
func TestCoreVersionIsTheUpstreamTagVerbatim(t *testing.T) {
	cases := []struct {
		name string
		in   baseline
		want string
	}{
		{
			name: "预发布 tag 原样返回",
			in:   baseline{upstreamTag: "v1.0.0-rc.25", qyVersion: "v0.1.0"},
			want: "v1.0.0-rc.25",
		},
		{
			name: "正式版 tag 原样返回",
			in:   baseline{upstreamTag: "v1.2.3", qyVersion: "v9.9.9"},
			want: "v1.2.3",
		},
		{
			// 二开版本变了,内核版本一个字都不能动。合成实现在这条上必红:
			// 它会返回 v1.0.0-rc.25+qy.<某个值>。
			name: "二开版本进位不影响内核版本",
			in:   baseline{upstreamTag: "v1.0.0-rc.25", qyVersion: "v0.2.0"},
			want: "v1.0.0-rc.25",
		},
		{
			// 声明缺失时报 unknown,而不是拿 describe 或二开版本顶替。
			name: "缺 tag 时报 unknown",
			in:   baseline{upstreamDescribe: "v1.0.0-rc.25-1-g2d8e50bf3", qyVersion: "v0.1.0"},
			want: Unknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := declared
			declared = tc.in
			t.Cleanup(func() { declared = prev })

			got := CoreVersion()
			assert.Equal(t, tc.want, got)
			// 逐字一致的另一半:内核版本里不许出现任何二开痕迹。
			assert.NotContains(t, got, "qy")
			assert.NotContains(t, got, "+")
		})
	}
}

// 二开版本独立于内核版本,并且两者互不污染。
func TestForkVersionIsIndependentFromCoreVersion(t *testing.T) {
	prev := declared
	declared = baseline{
		upstreamTag:      "v1.0.0-rc.25",
		upstreamDescribe: "v1.0.0-rc.25-1-g2d8e50bf3",
		qyVersion:        "v0.1.0",
	}
	t.Cleanup(func() { declared = prev })

	assert.Equal(t, "v0.1.0", ForkVersion())
	assert.Equal(t, "v1.0.0-rc.25", CoreVersion())
	assert.Equal(t, "v1.0.0-rc.25-1-g2d8e50bf3", SyncedUpstream())

	// 只改上游侧,二开版本不动。
	declared = baseline{upstreamTag: "v1.0.0-rc.99", upstreamDescribe: "v1.0.0-rc.99", qyVersion: "v0.1.0"}
	assert.Equal(t, "v0.1.0", ForkVersion(), "同步一次上游把二开版本也带跑了")

	// 只改二开侧,内核版本不动。
	declared = baseline{upstreamTag: "v1.0.0-rc.99", upstreamDescribe: "v1.0.0-rc.99", qyVersion: "v3.4.5"}
	assert.Equal(t, "v1.0.0-rc.99", CoreVersion(), "发一版二开把内核版本也带跑了")

	// 二开版本缺失时报 unknown,不回落到内核版本 —— 回落会让「没声明」看起来
	// 像「二开版本就是上游版本」,而那恰恰是本轮要拆开的那个误解。
	declared = baseline{upstreamTag: "v1.0.0-rc.25", upstreamDescribe: "v1.0.0-rc.25"}
	assert.Equal(t, Unknown, ForkVersion())
}

// SyncedUpstream 报的是 describe(精确提交),不是 tag。
//
// 这两个值在基线正好落在 tag 上时相等,平时不等 —— 用一个刻意让它们不等的输入
// 才能真的分辨出实现读了哪个字段。
func TestSyncedUpstreamReportsExactCommitNotBareTag(t *testing.T) {
	prev := declared
	declared = baseline{
		upstreamTag:      "v1.0.0-rc.25",
		upstreamDescribe: "v1.0.0-rc.25-1-g2d8e50bf3",
		qyVersion:        "v0.1.0",
	}
	t.Cleanup(func() { declared = prev })

	assert.Equal(t, "v1.0.0-rc.25-1-g2d8e50bf3", SyncedUpstream())
	assert.Equal(t, "v1.0.0-rc.25", CoreVersion(), "内核版本被 describe 的后缀污染了")

	// 声明缺失时同样报 unknown,不回落到 tag:回落会让「同步到 rc.25 之后第 1 个
	// 提交」被说成「同步到 rc.25」,而这正是本次要修掉的那种沉默的偏差。
	declared = baseline{upstreamTag: "v1.0.0-rc.25", qyVersion: "v0.1.0"}
	assert.Equal(t, Unknown, SyncedUpstream())
}

// Build 未注入 / 注入空白时报 unknown,注入后原样透出。
func TestCurrentBuildNormalizesUninjectedValues(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "未注入", in: "", want: Unknown},
		{name: "注入了空串", in: "   ", want: Unknown},
		{name: "注入后原样透出", in: "v1.0.0-rc.24-109-g1228d77e8", want: "v1.0.0-rc.24-109-g1228d77e8"},
		{name: "dirty 后缀保留", in: "v1.0.0-rc.24-109-g1228d77e8-dirty", want: "v1.0.0-rc.24-109-g1228d77e8-dirty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := Build
			Build = tc.in
			t.Cleanup(func() { Build = prev })

			assert.Equal(t, tc.want, CurrentBuild())
		})
	}
}

// 二开版本号的解析:接受什么、拒绝什么。
//
// 「拒绝」这一半比「接受」重要得多:不可解析必须走成「比不出来」,而不是被当成
// 某个默认值参与比较。把不认识的版本当成 0.0.0(最旧)会让检查更新在远端换了
// 命名方案时报「你已是最新」——最坏的一种错,因为它让人停止检查。
func TestForkVersionNumbersAcceptsOnlyTheDeclaredScheme(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want [3]int
		ok   bool
	}{
		{name: "带 v 前缀", in: "v0.1.0", want: [3]int{0, 1, 0}, ok: true},
		{name: "不带 v 前缀", in: "0.1.0", want: [3]int{0, 1, 0}, ok: true},
		{name: "远端 tag 的 qy- 前缀被剥掉", in: "qy-v1.2.3", want: [3]int{1, 2, 3}, ok: true},
		{name: "首尾空白被清掉", in: "  v1.2.3\n", want: [3]int{1, 2, 3}, ok: true},
		{name: "两位数段", in: "v10.20.30", want: [3]int{10, 20, 30}, ok: true},
		{name: "全零可解析(形状合法,是否该发布由声明用例管)", in: "v0.0.0", want: [3]int{0, 0, 0}, ok: true},

		{name: "空串", in: "", ok: false},
		{name: "只有 v", in: "v", ok: false},
		{name: "两段", in: "v1.2", ok: false},
		{name: "四段", in: "v1.2.3.4", ok: false},
		{name: "空段", in: "v1..3", ok: false},
		{name: "非数字段", in: "v1.2.x", ok: false},
		// 上游的 tag 形态。它必须比不出来 —— 一旦它被当成 v1.0.0 参与比较,
		// 上游的 release 就能冒充二开的新版本。
		{name: "上游预发布 tag 比不出来", in: "v1.0.0-rc.25", ok: false},
		{name: "带 build metadata 比不出来", in: "v1.0.0+qy.2", ok: false},
		// Atoi 接受 "+1"/"-1",必须显式挡掉:负版本号会让比较得出查无此版的结论。
		{name: "带正号的段", in: "v1.+2.3", ok: false},
		{name: "带负号的段", in: "v1.-2.3", ok: false},
		{name: "git describe 的输出", in: "v1.0.0-rc.24-109-g1228d77e8", ok: false},
		{name: "unknown", in: Unknown, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			major, minor, patch, ok := ForkVersionNumbers(tc.in)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, tc.want, [3]int{major, minor, patch})
			} else {
				// 不可解析时三个返回值必须是零值:调用方即便忽略 ok 也拿不到
				// 一个"看起来像真的"的版本号。
				assert.Equal(t, [3]int{0, 0, 0}, [3]int{major, minor, patch})
			}
		})
	}
}

// 版本比较的四个方向:相等 / 更新 / 更旧 / 比不出来。
//
// 每一段(MAJOR/MINOR/PATCH)都要单独有一条,否则「只比了第一段」的实现照样全绿。
func TestCompareForkOrdersEachSegment(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want int
		ok   bool
	}{
		{name: "完全相等", a: "v1.2.3", b: "v1.2.3", want: 0, ok: true},
		{name: "v 前缀不影响相等", a: "1.2.3", b: "v1.2.3", want: 0, ok: true},
		{name: "qy- 前缀不影响相等", a: "v0.1.0", b: "qy-v0.1.0", want: 0, ok: true},

		{name: "MAJOR 更大", a: "v2.0.0", b: "v1.9.9", want: 1, ok: true},
		{name: "MAJOR 更小", a: "v1.9.9", b: "v2.0.0", want: -1, ok: true},
		{name: "MINOR 更大", a: "v1.2.0", b: "v1.1.99", want: 1, ok: true},
		{name: "MINOR 更小", a: "v1.1.99", b: "v1.2.0", want: -1, ok: true},
		{name: "PATCH 更大", a: "v1.2.4", b: "v1.2.3", want: 1, ok: true},
		{name: "PATCH 更小", a: "v1.2.3", b: "v1.2.4", want: -1, ok: true},

		// 字符串比较会说 "v0.9.0" > "v0.10.0"。数值比较必须说反过来。
		{name: "两位数段不按字典序", a: "v0.10.0", b: "v0.9.0", want: 1, ok: true},

		{name: "左侧不可解析", a: Unknown, b: "v1.2.3", ok: false},
		{name: "右侧不可解析", a: "v1.2.3", b: "v1.0.0-rc.25", ok: false},
		{name: "两侧都不可解析", a: "", b: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CompareFork(tc.a, tc.b)
			require.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)

			if tc.ok {
				// 反对称性:调换两侧,结论必须取反。少了这一条,一个恒返回 -1
				// 的实现能在上面一半用例里蒙混过关。
				rev, revOK := CompareFork(tc.b, tc.a)
				require.True(t, revOK)
				assert.Equal(t, -tc.want, rev)
			}
		})
	}
}

// TestShippedUpstreamTagActuallyExistsUpstream 把 upstream_tag 与**真实存在的
// 上游 tag 集合**对一次。
//
// # 为什么形状校验不够
//
// TestShippedBaselineIsWellFormed 只查形状(vX.Y.Z 形态)与两个键的自洽
// (describe 以 tag 开头)。而 upstream_tag 有两个读者 —— Go 侧的 parseBaseline
// 与 build.sh / build.ps1 —— 它们读的是**同一份文件**,所以值写错时两边一致地
// 错,buildscript_test.go 那条"唯一能红的地方"也不会红。
//
// 实测过的形状:把 upstream_tag 与 upstream_describe **一致地**写成
// v1.0.0-rc.52(一次复制粘贴同一个错值),`go test ./qianye/...` 整树全绿,
// 而上游从未发布过这个 release。传播链是:构建脚本把它注入 common.Version →
// /api/status 的 version、X-New-Api-Version 响应头、每条消费日志的 version、
// /api/qy/admin/version 的 core,以及「系统维护 → 当前版本」;更糟的是上游那颗
// 检查更新按钮做的是**字符串全等**比较(update-checker-section.tsx),
// 值不等就永远弹「有新版本」—— 正是本轮改造要消灭的那个失效,换了个入口回来。
//
// 单边写错已经被 describe 前缀校验挡住(实测会红),这一条补的是双边一致的错值。
//
// # 为什么可以跳过
//
// 判据要求本地 clone 里有上游的 tag。baseline.txt 自己写明上游 tag **刻意不推进
// fork 的命名空间**,所以一个只挂了 origin 的浅克隆里一个 tag 都没有 ——
// 那时红掉是误报,不是发现。判据因此是:仓库里**一个 vX.Y.Z 形态的 tag 都
// 没有**时跳过(拿不到事实),只要有一个就必须能找到 upstream_tag 本人。
func TestShippedUpstreamTagActuallyExistsUpstream(t *testing.T) {
	b := parseBaseline(baselineFile)
	require.NotEmpty(t, b.upstreamTag)

	out, err := exec.Command("git", "tag", "--list").Output()
	if err != nil {
		t.Skipf("跳过:这台机器上跑不了 git tag(%v)—— 拿不到事实就不下结论", err)
	}
	tags := map[string]bool{}
	upstreamShaped := 0
	for _, line := range strings.Split(string(out), "\n") {
		tag := strings.TrimSpace(line)
		if tag == "" {
			continue
		}
		tags[tag] = true
		if upstreamTagPattern.MatchString(tag) {
			upstreamShaped++
		}
	}
	if upstreamShaped == 0 {
		t.Skip("跳过:这份 clone 里一个上游形态的 tag 都没有(浅克隆 / 只挂了 origin)," +
			"拿不到上游 tag 集合就不下结论 —— 误红比漏判更坏")
	}

	assert.True(t, tags[b.upstreamTag],
		"baseline.txt 声明的 upstream_tag=%q 在这份 clone 的 tag 集合里找不到"+
			"(共 %d 个上游形态的 tag)。它会被构建脚本原样注入 common.Version,"+
			"于是 /api/status 的 version、X-New-Api-Version、每条消费日志的 version "+
			"与「系统维护 → 当前版本」全部变成一个上游从未发布过的版本号;"+
			"而上游那颗检查更新按钮做的是字符串全等比较,值不等就永远弹「有新版本」。"+
			"请核对 `git tag --list | grep %s`",
		b.upstreamTag, upstreamShaped, b.upstreamTag)
}
