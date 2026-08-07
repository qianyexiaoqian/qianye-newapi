package groupmatrix

import (
	"go/ast"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noautoscope_test.go —— 钉死本轮撤销的那件事:**scope 行只能由人写出来**。
//
// ══════════════════════ 撤销了什么,以及为什么需要守卫 ══════════════════════
//
// 上一轮实现过「新建的用户分组默认全遮断」:一个后台对账任务发现 options.GroupRatio
// 里出现了没见过的 key,就自动为它建一条 mode=enforce、零 grant 的 scope 行。
//
// 本轮项目方把口径**反转**了:「用户组若未设定范围,模型组可以直接使用,按照模型组的
// 兜底倍率显示」。自动接管与它正好相反,于是整台机器连同它的配置项、登记簿表、
// 界面提示一起撤掉。
//
// 撤销之所以无损,只因为当时三张表都是 0 行 —— 从来没有任何用户分组被自动接管过。
// **一旦有运营开始配范围,反转就不再无损**,所以这条守卫要把"不会再长回来"钉死:
// 没有它,半年后有人"顺手"加回一个自动接管,这次撤销就白做了,而表现是
// 一批用户在没有任何人操作的情况下突然选不到任何模型分组。

// TestNoAutoScopeCreation 断言除管理端 handler 外没有任何路径写 scope 行。
//
// 判据是"哪个函数里出现了对 Scope 的写操作",而不是"文件里有没有某个字符串":
// 后者会被一次改名绕过,而前者要绕过就得把写操作真的挪进管理端 handler ——
// 那正是我们要求的位置。
func TestNoAutoScopeCreation(t *testing.T) {
	// 管理端的接管开关。它是**唯一**允许创建/更新 scope 行的入口,
	// 而且它挂着 CriticalRateLimit + 审计 + 切 enforce 前的影响面闸门。
	allowed := map[string]struct{}{"adminPutScope": {}}

	// 会写出一条 scope 行的 GORM 动词。Delete 不在其列:撤销范围是回退方向,
	// 任何路径都可以做;而本守卫防的是"没有人按过按钮却多出一行"。
	writeVerbs := map[string]struct{}{
		"Create": {}, "Save": {}, "Updates": {}, "Update": {},
		"FirstOrCreate": {}, "Upsert": {},
	}

	for _, path := range packageGoFiles(t) {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		file := parseFileOrFail(t, base)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if _, ok := allowed[fn.Name.Name]; ok {
				continue
			}
			assert.False(t, writesScopeRow(t, base, fn, writeVerbs),
				"%s 的 %s 会写出一条 qy_group_scopes 行 —— 除管理端 adminPutScope 之外任何路径都不许。\n"+
					"「新分组默认全遮断」已于本轮撤销,新口径是「未设定范围 = 全部模型分组可用」。\n"+
					"自动建 scope 行会让一批用户在没有任何人操作的情况下突然选不到任何模型分组",
				base, fn.Name.Name)
		}
	}
}

// writesScopeRow 判断函数体里有没有"把一个 Scope 写进库"的调用。
//
// 两种形状都要认:
//
//	tx.Create(&Scope{...}) / tx.Save(after)   —— 实参是 Scope 值或构造它的 newScope
//	tx.Model(&Scope{}).Updates(...)           —— 目标由 Model 指定
func writesScopeRow(t *testing.T, path string, fn *ast.FuncDecl, verbs map[string]struct{}) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, isWrite := verbs[sel.Sel.Name]; !isWrite {
			return true
		}
		// 实参或链式接收者里提到 Scope / newScope 就算命中。文本比对是刻意的:
		// 类型推导要跑一遍完整的 type checker,而本守卫必须在纯 AST 下可用;
		// 误报的方向是"逼人回来确认一次",那正是想要的。
		text := exprText(t, path, call)
		if strings.Contains(text, "Scope{") || strings.Contains(text, "newScope(") {
			found = true
		}
		return true
	})
	return found
}

func packageGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			out = append(out, e.Name())
		}
	}
	require.NotEmpty(t, out)
	return out
}

// TestUnsetScopeAllowsAnyTokenGroup 是撤销之后**写侧**不会误挡的证明。
//
// 新口径下"没有 scope 行"意味着**全部模型分组可用**。写侧若还按旧口径把它当成
// "还没配、先拦着",用户就会在一个本该畅通的分组上被拒绝建令牌 ——
// 而那正是撤销要消灭的表现。
//
// 顺带覆盖一条容易被忽略的:分组名根本不在分组倍率表里(孤儿)时同样必须放行。
// 那种令牌确实会在请求时被上游 403,但拦在写侧只是把"将来会 403"换成"现在改不动",
// 而且会堵死孤儿令牌唯一的自救出口(把分组改成别的 / 留空)。
func TestUnsetScopeAllowsAnyTokenGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "paid": 1})
	// 库里一条 scope 行都没有 —— 全站都处于「未设定范围」。
	require.NoError(t, reload())
	useOwnerGroup(t, "vip")

	c := ctxWithUser(7)
	for _, newGroup := range []string{"paid", "default", "vip", "从来没配过的分组"} {
		assert.NoError(t, CheckTokenGroup(c, "", newGroup),
			"用户分组未设定范围时,写侧必须放行任意模型分组(含不在倍率表里的孤儿名)—— "+
				"新口径是「未设定范围 = 全部可用」,把它当成「还没配、先拦着」就是旧口径的残留")
	}

	// 读侧同样必须是恒等的:两侧永远说同一句话。
	in := map[string]string{"default": "默认分组", "vip": "vip分组"}
	got := Resolve("vip", in)
	assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer(),
		"未设定范围必须原样返回上游那一张 map")

	// 反面:真的设定了空范围时,写侧必须拦得住 —— 否则本测试只是在证明
	// "写侧永远放行",那种全绿毫无意义。
	seedScope(t, gdb, "vip", ModeEnforce, false)
	assert.Error(t, CheckTokenGroup(c, "", "paid"),
		"「已设定范围且范围为空」是与「未设定范围」完全不同的一档,它必须真的拦得住")
}
