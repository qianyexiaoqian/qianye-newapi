package violation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
)

// TestMain 给整个包的测试一个足够宽松的扫描预算。
//
// 为什么需要它:scan 有一道墙钟预算(violation.scan_timeout_ms,默认 20ms),
// 用来防止"规则数 × 文本长度"的累积开销拖住 relay 热路径 —— 这在生产上是对的。
// 但测试里它会变成时间依赖:关键词匹配首次要构建 AC 自动机(service.getOrBuildAC
// 的进程级缓存冷启动),跑全量测试套件时多个包并行、CPU 被争抢,这一步就可能
// 超过 20ms,于是 scan 在检查第一条规则之前就带着 Timeout 返回。
//
// 表现是这样一类测试:单独跑必绿,`go test ./qianye/...` 偶尔红,重跑又绿 ——
// 已经实际发生过两次(TestPriorityWins 与 TestMatchTypes 的 v.Terms 为 nil)。
// 这种测试比不写更糟:平时全绿会让人相信违规匹配是可靠的,而它其实只是没被
// 真正执行过。
//
// 这里把预算放大到 5 秒。**它不削弱任何断言** —— 被测的是"命中哪条规则、
// 命中哪些词",不是"多快命中"。真正需要固化超时行为的测试自己去调小预算,
// 那才是有意为之的断言。
// useGenerousScanBudget 保证本次测试内 scan 的墙钟预算足够宽松。
//
// 必须由每个依赖匹配结果的测试自己调用,不能只在 TestMain 里设一次:
// 同包的 useTestConfig 会调 config.Load() 覆盖全局配置指针,而 t.Setenv 只还原
// 环境变量、还原不了那个指针。于是"前面跑过哪个测试"就决定了这里的预算,
// 顺序一变就时绿时红。
//
// 放大预算不削弱任何断言:被测的是"命中哪条规则、命中哪些词",不是"多快命中"。
func useGenerousScanBudget(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qianye.yaml")
	yaml := "enabled: true\n" +
		"database:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy_test\"\n" +
		"violation:\n  enabled: true\n  scan_timeout_ms: 5000\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	t.Setenv(config.EnvConfigPath, path)
	if err := config.Load(); err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "qy-violation-test-*")
	if err != nil {
		panic("violation 测试无法创建临时目录: " + err.Error())
	}
	path := filepath.Join(dir, "qianye.yaml")
	// 只开扫描预算这一项;其余保持零值,各测试自行按需覆盖。
	yaml := "enabled: true\n" +
		"database:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy_test\"\n" +
		"violation:\n  enabled: true\n  scan_timeout_ms: 5000\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		panic("violation 测试无法写入配置: " + err.Error())
	}
	os.Setenv(config.EnvConfigPath, path)
	if err := config.Load(); err != nil {
		panic("violation 测试无法加载配置: " + err.Error())
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
