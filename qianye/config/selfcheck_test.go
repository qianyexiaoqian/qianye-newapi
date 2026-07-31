package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────── 机制本身:能不能发现空闸门 ───────────────────────

// 这一组用合成结构体验证检查逻辑,不依赖真实 Config 的字段构成 ——
// 否则任何人改一个真实配置项都会牵动它,而它要守的是"机制还在"。
func TestLeafFields_ExpandsSectionsAndKeepsGoNames(t *testing.T) {
	type inner struct {
		Days   int   `yaml:"days"`
		Toggle *bool `yaml:"toggle"`
	}
	type outer struct {
		Enabled bool     `yaml:"enabled"`
		Section inner    `yaml:"section"`
		Skipped string   `yaml:"-"`
		NoTag   string   // 没有 yaml tag 的字段不是配置项
		List    []string `yaml:"list"`
	}

	got := leafFields(reflect.TypeOf(outer{}), "")
	assert.Equal(t, []leafField{
		{path: "enabled", name: "Enabled"},
		{path: "section.days", name: "Days"},
		{path: "section.toggle", name: "Toggle"},
		{path: "list", name: "List"},
	}, got)
}

// CheckFieldConsumers 的三类判定必须都真的会报出来。这是整条防线的支点:
// 它一旦退化成恒返回空,新增的空闸门就又变回"启动日志里看不见"。
func TestCheckConsumers_ClassifiesEachKindOfDefect(t *testing.T) {
	res := checkConsumers(
		[]leafField{
			{path: "sec.wired", name: "Wired"},
			{path: "sec.dead", name: "Dead"},
			{path: "sec.forgotten", name: "Forgotten"},
		},
		map[string]consumer{
			"sec.wired":   {"qianye/db/db.go", "有真实消费方"},
			"sec.dead":    {"", "经核查确无消费方"},
			"sec.removed": {"qianye/db/db.go", "字段已被删除,登记表没跟上"},
		})

	assert.Equal(t, []string{"sec.forgotten"}, res.Unregistered, "新增却没登记的配置项必须被报出来")
	assert.Equal(t, []string{"sec.dead"}, res.Unconsumed, "登记为无消费方的配置项必须被报出来")
	assert.Equal(t, []string{"sec.removed"}, res.Stale, "登记表里多出来的条目必须被报出来")
	assert.False(t, res.clean())
}

// ─────────────────────── 真实配置:登记表必须与代码一致 ───────────────────────

// 每个配置项都必须登记消费方。
//
// 新增配置项时这条会失败 —— 这是刻意的:补一行登记的成本是十秒,
// 而漏接消费方的代价是运维改完 YAML 以为闸门关了、其实一直开着(C1/C2/OLD-1/OLD-2)。
func TestFieldConsumers_CoversEveryConfigField(t *testing.T) {
	res := CheckFieldConsumers()
	assert.Empty(t, res.Unregistered,
		"这些配置项没有登记消费方:请在 fieldConsumers 中补上真正读它的源文件,填不出来就说明开关还没接上代码")
	assert.Empty(t, res.Stale,
		"这些登记条目对应的字段已不存在:字段被删或改名后,登记表要一起改")
}

// 已知的空闸门必须逐个列出来,新增一个就要显式改这里。
//
// 允许存在空闸门(启动期会告警),但不允许"悄悄多一个"。
//
// 目前这个集合是**空的**:最后一个空闸门 audit.retention_days 已经接上
// qianye/service/audit/retention.go 的 Prune。清空这条断言是刻意的 ——
// 每次启动都出现、却没人能修的告警会训练运维忽略整套自检输出,
// 而自检正是本扩展为"配置项空转"这个形状建立的主要防线。
func TestFieldConsumers_KnownUnconsumedFieldsAreExactlyThese(t *testing.T) {
	assert.Empty(t, CheckFieldConsumers().Unconsumed,
		"空闸门的集合变了:新增的请先接消费方,确实接不上再登记为空并在这里列出")
}

// 登记表不许说谎:file 指向的源文件里必须真的引用了该字段(或读它的访问器)。
//
// 这条是 OLD-1 的直接回归 —— 当时 reconcile.go 的清理任务用的是包内常量
// lookupLogRetainDays,配置项 LookupLogRetainDays 一次都没被读到。
// 只要有人把它改回常量,这里就会失败。
func TestFieldConsumers_ConsumerFilesReallyReferenceTheField(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	accessors := accessorFields(t)

	for _, f := range leafFields(reflect.TypeOf(Config{}), "") {
		c, ok := fieldConsumers[f.path]
		if !ok || c.file == "" {
			continue
		}
		idents := identsIn(t, filepath.Join(repoRoot, filepath.FromSlash(c.file)))
		if idents[f.name] {
			continue
		}
		via := ""
		for method, fields := range accessors {
			if idents[method] && slices.Contains(fields, f.name) {
				via = method
				break
			}
		}
		assert.NotEmpty(t, via,
			"登记表说 %s 由 %s 消费,但那个文件里既没有 %s 也没有读它的访问器",
			f.path, c.file, f.name)
	}
}

// 防线自己也必须有消费方。
//
// 这条看着很绕,但它守的正是本文件要抓的那个形状:自检写好了、单元测试全绿、
// 登记表也维护着 —— 唯独没有任何人调用它,于是启动日志里一个字都不会出现,
// 而所有人都以为防线在岗。C1/C2/OLD-1/OLD-2 全都是这么发生的。
func TestLogFieldConsumerCheck_IsWiredIntoStartup(t *testing.T) {
	assert.True(t, identsIn(t, filepath.Join("..", "bootstrap.go"))["LogFieldConsumerCheck"],
		"qianye/bootstrap.go 的 Init 必须调用 LogFieldConsumerCheck,否则自检结果永远不会进启动日志")
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

// accessorFields 解析 config.go,得出"访问器方法 → 它读取的字段"。
//
// 必须解析出来而不是写死一张表:PricingOn 读的是 FilterPricing 这种对应关系
// 一旦被改动,写死的表会继续给出通过,防线就又变成了摆设。
func accessorFields(t *testing.T) map[string][]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "config.go", nil, 0)
	require.NoError(t, err)

	out := make(map[string][]string)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
			continue
		}
		recv := fn.Recv.List[0].Names[0].Name
		ast.Inspect(fn, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
				out[fn.Name.Name] = append(out[fn.Name.Name], sel.Sel.Name)
			}
			return true
		})
	}
	return out
}

// identsIn 返回一个源文件里出现过的全部标识符。
//
// 只看名字、不做类型解析:配置字段名(LookupLogRetainDays、PIIRetentionDays)
// 足够独特,重名代价远低于把 go/types 拖进单元测试。少数通用名(Enabled)
// 因此只是弱校验 —— 但那些字段本来也不是这类缺陷的高发区。
func identsIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err, "消费点文件应当可解析: %s", path)

	out := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			out[id.Name] = true
		}
		return true
	})
	return out
}
