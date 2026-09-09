package qianye

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// 台账表清单与主库表清单必须互斥。
//
// 同一个模型同时出现在 Tables() 与 LogTables() 里,在**没有配 log_database 的
// 部署上完全正常**(两份清单会被 bootstrap 合并,AutoMigrate 幂等),而在配了的
// 部署上会在两个库里各建一张同名表。写入只落其中一个,读取落哪个取决于代码里
// 那一行用的是 db.Get() 还是 db.Log() —— 两者随时可能漂移,而症状是"日志页
// 一直是空的,但表明明在涨"。
//
// 这条断言拿的是**运行期注册表**而不是源码文本:模块把模型挪到哪一份清单里
// 是一次编辑就能做到的事,靠 review 记住不可靠(usergroup 被漏注册过两次)。
func TestLogTablesAndMainTablesAreDisjoint(t *testing.T) {
	for _, m := range module.All() {
		main := make(map[string]bool)
		for _, tb := range m.Tables() {
			main[fmt.Sprintf("%T", tb)] = true
		}
		if lt, ok := m.(module.LogTabler); ok {
			for _, tb := range lt.LogTables() {
				name := fmt.Sprintf("%T", tb)
				assert.Falsef(t, main[name],
					"模块 %s 把 %s 同时登记进了 Tables() 与 LogTables() —— "+
						"配了 log_database 的部署会在两个库里各建一张,写入与读取会落到不同的库上",
					m.Name(), name)
			}
		}
		// 风控预警存储节点那一份更硬:它**没有**"没配就跟着主库走"这一档
		// (见 module.WatchTabler),所以同一个模型出现在两边不是"某些部署上
		// 会分家",而是每一个部署都会在两个库里各建一张。
		wt, ok := m.(module.WatchTabler)
		if !ok {
			continue
		}
		logged := make(map[string]bool)
		if lt, ok := m.(module.LogTabler); ok {
			for _, tb := range lt.LogTables() {
				logged[fmt.Sprintf("%T", tb)] = true
			}
		}
		for _, tb := range wt.WatchTables() {
			name := fmt.Sprintf("%T", tb)
			assert.Falsef(t, main[name] || logged[name],
				"模块 %s 把 %s 同时登记进了 WatchTables() 与 Tables()/LogTables() —— "+
					"两个库里会各建一张同名表,写入只落其中一个,而读取落哪个取决于"+
					"代码里那一行用的是 db.Get()/db.Log() 还是 db.Watch()",
				m.Name(), name)
		}
	}
}

// 风控预警的表不能用主库或台账库的句柄去查。
//
// 与 TestLogTablesAreNotQueriedThroughTheMainHandle 是同一条防线的另一半,
// 但失败形状更彻底:台账表用错句柄只在配了 log_database 的部署上读到空表,
// 而这里存储节点是**必配**的,用错句柄的那一行在**每一个**部署上都读空 ——
// 或者更糟,把监听记录写进主扩展库,而那正是这个功能被单独拆出去要避免的事。
func TestWatchTablesAreNotQueriedThroughOtherHandles(t *testing.T) {
	for _, m := range module.All() {
		wt, ok := m.(module.WatchTabler)
		if !ok || len(wt.WatchTables()) == 0 {
			continue
		}
		files, err := filepath.Glob(filepath.Join("modules", m.Name(), "*.go"))
		require.NoError(t, err)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(f)
			require.NoError(t, err)
			for i, line := range strings.Split(string(raw), "\n") {
				if !strings.Contains(line, "db.Get()") && !strings.Contains(line, "db.Log()") {
					continue
				}
				for _, tb := range wt.WatchTables() {
					short := fmt.Sprintf("%T", tb)
					if i := strings.LastIndex(short, "."); i >= 0 {
						short = short[i+1:]
					}
					assert.NotContainsf(t, line, short+"{}",
						"%s:%d 用主库/台账库句柄查风控预警表 %s —— 请改用 db.Watch()",
						f, i+1, short)
				}
			}
		}
	}
}

// 台账表清单不能为空之后又没有人真的用 db.Log() 去读写它。
//
// 这条守的是本轮引入的那个新失败面:AIReview 搬进台账库之后,任何一处仍然
// 用 db.Get() 去查它的代码,在没分家的部署上完全正常(两个句柄是同一个),
// 只有配了 log_database 的部署会读到一张空表。而那种部署恰恰是量最大的那些。
//
// 判据是源码文本,不是类型系统 —— 这里没有类型能表达"这个模型只能用那个句柄"。
// 文本判据的代价是它可能被绕过(先取句柄再用),换来的是它能在 review 之前就
// 拦下最自然的那种写法。
func TestLogTablesAreNotQueriedThroughTheMainHandle(t *testing.T) {
	for _, m := range module.All() {
		lt, ok := m.(module.LogTabler)
		if !ok || len(lt.LogTables()) == 0 {
			continue
		}
		files, err := filepath.Glob(filepath.Join("modules", m.Name(), "*.go"))
		require.NoError(t, err)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(f)
			require.NoError(t, err)
			for i, line := range strings.Split(string(raw), "\n") {
				if !strings.Contains(line, "db.Get()") {
					continue
				}
				for _, tb := range lt.LogTables() {
					short := fmt.Sprintf("%T", tb)
					if i := strings.LastIndex(short, "."); i >= 0 {
						short = short[i+1:]
					}
					assert.NotContainsf(t, line, short+"{}",
						"%s:%d 用 db.Get() 查台账表 %s —— 配了 log_database 的部署会读到一张空表,"+
							"请改用 db.Log()", f, i+1, short)
				}
			}
		}
	}
}
