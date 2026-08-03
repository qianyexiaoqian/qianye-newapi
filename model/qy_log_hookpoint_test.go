package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qy_log_hookpoint_test.go —— 锁死 model/log.go 里两个佣金 hook 相对
// common.LogConsumeEnabled 早退的位置。
//
// # 为什么必须有这条
//
// QyOnConsumeLog / QyOnTaskBillingLog 是佣金结算的**唯一**入口:下线产生消费时,
// 上线的佣金就是在这两个 hook 里累计的。它们所在的两个函数开头都有一句
//
//	if !common.LogConsumeEnabled { return }
//
// 这是一个纯粹的**日志**开关 —— 站长关掉它只是不想写消费日志,与佣金无关。
// 但只要 hook 落到早退之后,关掉日志开关就等于把全站佣金一起关掉:下线照常消费、
// 上线一分钱拿不到,而且没有任何报错、没有任何界面提示,只有月底对账时才会发现。
//
// model/log.go 是上游合并的冲突文件(本次同步 upstream/main 就改了同文件的
// formatUserLogs)。合并时 hook 与早退的相对位置被换掉是完全现实的事故,
// 而此前**没有任何测试保护**这两行的位置。
//
// 用位置(token.Pos)而不是调用序列来断言:早退是一个 if 语句而非函数调用,
// 序列表里没有它。

const logGoPath = "log.go"

func TestCommissionHooksRunBeforeLogConsumeEarlyReturn(t *testing.T) {
	cases := []struct {
		fn   string
		hook string
	}{
		{fn: "RecordConsumeLog", hook: "QyOnConsumeLog"},
		{fn: "RecordTaskBillingLog", hook: "QyOnTaskBillingLog"},
	}

	file, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(logGoPath), nil, 0)
	require.NoError(t, err, "应当可解析 model/log.go")

	for _, tc := range cases {
		t.Run(tc.fn, func(t *testing.T) {
			body := qyFuncBody(file, tc.fn)
			require.NotNil(t, body, "model/log.go 里找不到 %s —— 上游改了函数名?", tc.fn)

			hookPos := qyFirstCallPos(body, tc.hook)
			gatePos := qyFirstIdentPos(body, "LogConsumeEnabled")

			require.NotEqual(t, token.NoPos, hookPos,
				"%s 里缺少 %s 挂载点:下线消费不再产生任何佣金,而且没有任何报错",
				tc.fn, tc.hook)
			require.NotEqual(t, token.NoPos, gatePos,
				"%s 里找不到 common.LogConsumeEnabled 开关 —— 上游改了消费日志开关?", tc.fn)

			assert.Less(t, int(hookPos), int(gatePos),
				"%s 必须排在 common.LogConsumeEnabled 早退之前:那是日志开关不是佣金开关, "+
					"排在后面会让站长一关日志就把全站佣金静默清零", tc.hook)
		})
	}
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

func qyFuncBody(file *ast.File, name string) *ast.BlockStmt {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn.Body
		}
	}
	return nil
}

// qyFirstCallPos 返回函数体内第一次调用 name 的位置,找不到时返回 token.NoPos。
func qyFirstCallPos(body *ast.BlockStmt, name string) token.Pos {
	pos := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || pos != token.NoPos {
			return pos == token.NoPos
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == name {
				pos = call.Pos()
			}
		case *ast.SelectorExpr:
			if f.Sel.Name == name {
				pos = call.Pos()
			}
		}
		return pos == token.NoPos
	})
	return pos
}

// qyFirstIdentPos 返回函数体内第一次出现标识符 name 的位置。
// 早退是 if 语句而非调用,所以按标识符匹配。
func qyFirstIdentPos(body *ast.BlockStmt, name string) token.Pos {
	pos := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		if pos != token.NoPos {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			pos = id.Pos()
			return false
		}
		return true
	})
	return pos
}
