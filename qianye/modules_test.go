package qianye

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 每一个 qianye/modules/ 下的模块目录都必须出现在注册表里。
//
// 为什么需要这条结构性断言:modules.go 的 blank import 是模块生效的**唯一**开关,
// 而它是所有并行开发共享的文件。漏加一行的后果是整个模块静默失效 ——
// 代码写了、测试绿了、编译过了,但 init() 从不执行,表不建、路由不注册、
// hook 不注入,管理端页面 404。
//
// 这不是假设:usergroup 模块就这样被漏了两次(期间还有两个新模块被正确加了进来,
// 更说明"下次记得"不是可靠的机制)。本项目反复出现的失败形状就是
// "写了但没接上",这条断言把 modules.go 这一处彻底堵死。
func TestEveryModuleDirectoryIsRegistered(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("modules"))
	require.NoError(t, err, "读取 qianye/modules 目录失败")

	registered := make(map[string]bool, len(module.All()))
	for _, m := range module.All() {
		registered[m.Name()] = true
	}

	var missing []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// 模块的 Name() 与目录名约定一致。不一致的模块请在这里显式豁免,
		// 而不是把断言放宽 —— 放宽一次,这条防线就退化成注释。
		if !registered[name] {
			missing = append(missing, name)
		}
	}

	assert.Empty(t, missing,
		"以下模块目录存在但未在 qianye/modules.go 里 blank import,"+
			"它们的 init() 不会执行 —— 表不会建、路由不会注册、hook 不会注入: %v", missing)
}

// 反向:注册表里不该有已被删除的模块。
//
// 删模块时忘了删 import 会导致编译失败(比较显眼),但如果只是把模块改名,
// 就会留下一个名字对不上的注册项,而 Name() 是租约命名与日志的依据。
func TestNoRegisteredModuleWithoutDirectory(t *testing.T) {
	var orphans []string
	for _, m := range module.All() {
		if _, err := os.Stat(filepath.Join("modules", m.Name())); os.IsNotExist(err) {
			orphans = append(orphans, m.Name())
		}
	}
	assert.Empty(t, orphans,
		"以下模块已注册但找不到对应目录,可能是改名后未同步: %v", orphans)
}
