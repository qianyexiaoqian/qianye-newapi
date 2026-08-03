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

func TestNewAffCodeLength(t *testing.T) {
	truncateTables(t)

	// 刻意写字面量 8 而不是 AffCodeLength:拿常量去断言由同一个常量生成的值
	// 是一句永真的废话,把位数改回 4 它照样全绿。实测过这个形状 —— 它不变红。
	code := NewAffCode(DB)
	assert.Len(t, code, 8,
		"邀请码位数变了。4 位在千级用户量下冲突概率已不可忽略,提到 8 位是这次改动的目的")
	for _, r := range code {
		assert.True(t,
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'),
			"邀请码要进 URL(推广链接 ?aff=xxx),只能是字母数字,出现了 %q", string(r))
	}
}

// TestNewAffCodeRetriesOnCollision 锁住撞码重试。
//
// 改动前这条路径**根本不存在**:随机撞上已有的 aff_code 时,
// tx.Create 被唯一索引拒绝,整个注册失败。8 位之后概率极低,
// 但"极低"不等于"不会",而重试的成本只有一次索引命中的 COUNT。
func TestNewAffCodeRetriesOnCollision(t *testing.T) {
	truncateTables(t)

	const taken = "TAKENAFF"
	require.NoError(t, DB.Create(&User{
		Id: 1, Username: "aff-collision", Password: "password123",
		Email: "aff-collision@example.com", AffCode: taken,
	}).Error)

	saved := affCodeGenerator
	t.Cleanup(func() { affCodeGenerator = saved })

	// 前两次都吐出已被占用的码,第三次才给一个空闲的。
	seq := []string{taken, taken, "FREEAFF1"}
	i := 0
	affCodeGenerator = func() string {
		got := seq[i]
		i++
		return got
	}

	assert.Equal(t, "FREEAFF1", NewAffCode(DB),
		"撞上已存在的 aff_code 时必须重新生成,否则注册会被唯一索引直接打回")
	assert.Equal(t, 3, i, "三次候选必须都真的去库里查过,而不是只查第一次")
}

// TestNewAffCodeGivesUpAfterMaxAttempts 锁住上限:绝不无界循环。
func TestNewAffCodeGivesUpAfterMaxAttempts(t *testing.T) {
	truncateTables(t)

	const taken = "ALWAYSHIT"
	require.NoError(t, DB.Create(&User{
		Id: 1, Username: "aff-giveup", Password: "password123",
		Email: "aff-giveup@example.com", AffCode: taken,
	}).Error)

	saved := affCodeGenerator
	t.Cleanup(func() { affCodeGenerator = saved })

	calls := 0
	affCodeGenerator = func() string {
		calls++
		return taken
	}

	assert.Equal(t, taken, NewAffCode(DB))
	assert.Equal(t, affCodeMaxAttempts, calls,
		"重试次数必须有上限:一个永远撞码的字符集配置不能把注册请求挂死")
}

// TestEveryAffCodeWriteGoesThroughNewAffCode 是这次收敛的**防漂移锁**。
//
// 上游把 `common.GetRandomString(4)` 抄了三份(User.Insert / User.InsertWithTx /
// controller.GetAffCode)。位数是全站唯一的业务参数,散成 N 份就一定会漏改一份 ——
// 而漏改不报错,只让一部分用户拿到 4 位码。
//
// 这条断言的不是"当前三处都改对了"(那种断言改完就失效),而是
// **今后任何一处给 AffCode 赋值都必须走 NewAffCode**。有人再抄第四份时它会变红。
func TestEveryAffCodeWriteGoesThroughNewAffCode(t *testing.T) {
	files := []string{
		"user.go",
		filepath.Join("..", "controller", "user.go"),
		filepath.Join("..", "controller", "oauth.go"),
	}

	writes := 0
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
		require.NoError(t, err, "应当可解析: %s", path)

		ast.Inspect(file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range as.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "AffCode" || i >= len(as.Rhs) {
					continue
				}
				writes++
				call, ok := as.Rhs[i].(*ast.CallExpr)
				require.True(t, ok,
					"%s 里给 AffCode 赋了一个非函数调用的值 —— 邀请码只能由 NewAffCode 生成", path)
				assert.Equal(t, "NewAffCode", calleeName(call.Fun),
					"%s 里有一处 AffCode 赋值没走 model.NewAffCode。"+
						"再抄一份随机码生成就等于再埋一处「改位数时会漏改」的拷贝", path)
			}
			return true
		})
	}

	assert.Equal(t, 3, writes,
		"上游给 AffCode 赋值的地方从 3 处变成了 %d 处。"+
			"新增的那一处是不是也该走 NewAffCode?想清楚再改这个数字", writes)
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}
