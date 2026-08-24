package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildscript_test.go —— 构建脚本与本包对「内核版本是什么」必须给出同一个答案。
//
// # 为什么这条必须是行为断言,而不是读一遍脚本源码
//
// 内核版本有**两个读者**:构建脚本(把它注入 common.Version,决定 /api/status
// 与 X-New-Api-Version 报什么)和本包(CoreVersion(),决定管理端页面显示什么)。
// 两者读的是同一份 baseline.txt,但各自实现了一遍解析。
//
// 它们漂移时**没有任何一处会报错**:两边都能算出一个像模像样的版本号,只是不
// 相等。这正是本轮要修掉的那类沉默偏差的翻版 —— 上一版的脚本把两个版本号拼成
// `<tag>+qy.<轮次>` 注入 common.Version,而 Go 侧后来改回逐字,谁都不会红。
//
// 断言脚本的**源码文本**挡不住这个:注释挪一行、变量改个名就失效,而真正的
// 漂移(读了别的键、拼了个后缀、解析口径不一致)照样溜过去。所以这里真的把
// 脚本跑起来,拿它的输出跟 CoreVersion() 逐字比。

// repoRoot 从本包目录回溯到仓库根(qianye/version → ..→..)。
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(root, "go.mod"), "没找到仓库根")
	return root
}

// skipWhenVersionFileWritten 在 VERSION 非空时跳过。
//
// 两个脚本都是「VERSION 文件优先,为空才读声明」。仓库里那个文件是 0 字节
// (上游 CI 发版时才写),空是常态;真被写了的话这里比较的就不是同一件事了,
// 老实跳过比造一个假的绿色强。
func skipWhenVersionFileWritten(t *testing.T, root string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return // 文件不存在,脚本会直接走声明这一支
	}
	if strings.TrimSpace(string(raw)) != "" {
		t.Skip("VERSION 文件已被写入,构建脚本此时不读声明,这条比较不成立")
	}
}

// build.sh 算出的内核版本必须逐字等于 CoreVersion()。
func TestBuildShellScriptAgreesOnCoreVersion(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("环境里没有 sh,跳过(这条只在有 POSIX shell 的机器上有意义)")
	}
	root := repoRoot(t)
	skipWhenVersionFileWritten(t, root)

	out, err := exec.Command(sh, filepath.Join(root, "qianye", "scripts", "build.sh"), "--print-core").Output()
	require.NoError(t, err, "build.sh --print-core 执行失败")

	assert.Equal(t, CoreVersion(), strings.TrimSpace(string(out)),
		"build.sh 注入 common.Version 的值与 CoreVersion() 不一致 —— "+
			"/api/status 报的版本会和管理端页面显示的对不上,而且两边都不报错")
}

// build.ps1 算出的内核版本必须逐字等于 CoreVersion()。
//
// 它没有 --print-core,所以拿 -PrintOnly 的那一行输出来比。这也顺带钉住了
// 那一行的格式 —— 它是运维在构建日志里唯一能看到版本号的地方。
func TestBuildPowerShellScriptAgreesOnCoreVersion(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("build.ps1 是 Windows PowerShell 脚本")
	}
	pwsh, err := exec.LookPath("powershell")
	if err != nil {
		t.Skip("环境里没有 powershell")
	}
	root := repoRoot(t)
	skipWhenVersionFileWritten(t, root)

	out, err := exec.Command(pwsh, "-NonInteractive", "-File",
		filepath.Join(root, "qianye", "scripts", "build.ps1"), "-PrintOnly").Output()
	require.NoError(t, err, "build.ps1 -PrintOnly 执行失败")

	var shown string
	for _, line := range strings.Split(string(out), "\n") {
		if _, value, ok := strings.Cut(line, ":"); ok && strings.HasPrefix(line, "core ") {
			shown = strings.TrimSpace(value)
			break
		}
	}
	require.NotEmpty(t, shown, "build.ps1 的输出里没有 core 那一行:\n%s", out)

	assert.Equal(t, CoreVersion(), shown,
		"build.ps1 注入 common.Version 的值与 CoreVersion() 不一致")
}
