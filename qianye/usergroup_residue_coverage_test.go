package qianye

// usergroup_residue_coverage_test.go —— 锁住「以用户分组名为键的表必须登记处置」。
//
// # 这条锁防的是什么
//
// 删除一个用户分组时,它的名字散落在多个模块的表里。每一处漏掉的后果都**不会
// 报错**,只会安静地留下一条挂在死名字上的配置:
//
//	漏掉 qy_group_scopes        矩阵页的行轴凭空多出一档已经不存在的用户分组
//	漏掉 qy_commission_group_rate 一条永远不会命中的返佣费率
//	漏掉 qy_transfer_group_rules  一条永远不会命中的**资金闸门** —— 运营以为设了防,
//	                              而那批人已经迁走了;更糟的是这个名字将来被重新用上时,
//	                              一批毫不相干的新用户会突然出现在一条老白名单里
//
// 没有任何普通测试会因为漏掉一个模块而变红:删除照常返回 200,业务照常跑。
// 所以把"这张表登记过没有"本身变成断言。
//
// # 它抓不到什么
//
// 只看**有没有登记**,不看 Probe/Sweep 写得对不对 —— 后者由各模块自己的用例负责。
// 一个 `Sweep: func(...) error { return nil }` 能骗过它。它的职责是防止一整个模块
// 被忘掉,而那正是"新加一张表"最常见的失败方式。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userGroupColumns 是"这一列装的是 users.group 的取值"的列名。
//
// 判据用**列名**而不是字段名:字段名可以随便起(RateGroup / FromGroup),
// 而列名是写进库里、被 SQL 直接引用的那一个,改它必然是一次显式的迁移。
var userGroupColumns = map[string]bool{
	"user_group": true,
	"from_group": true,
	"group_name": true,
}

// userGroupListFields 是**不落在自己一列上**的分组名单。
//
// 它们住在 JSON 文本(抽奖的 rules_text)或逗号串(划转的 to_groups、
// 违规规则的 group_scope)里,因此上面那条按列名扫 gorm tag 的判据一个都看不见。
// 这不是理论风险:lottery 的 allow_groups / deny_groups 逐字比 users.group,
// 而它在这条守卫加上这一段之前从未被登记过 —— 一份挂在死名字上的参与白名单,
// 在这个名字被将来某次新建重新用上时会让一批毫不相干的新用户凭空获得资格。
//
// 判据用 **json tag**:这些字段的名字就是落进文本里的那个键,改它是一次显式的
// 兼容性变更(会让历史 rules_hash 复算不出来),不会被人顺手改掉。
var userGroupListFields = map[string]bool{
	"allow_groups": true,
	"deny_groups":  true,
	"to_groups":    true,
	"group_scope":  true,
}

// notAUserGroup 是**显式豁免**:列名/字段名撞上了,但它装的不是用户分组。
//
// 必须逐条给理由,不接受空白豁免 —— 一条没有理由的豁免与一次遗漏长得一模一样。
var notAUserGroup = map[string]string{
	"availability:group_name": "可用率按**模型分组**(渠道池子)分桶,与「这个人是谁」无关",
	"violation:group_scope": "规则作用域比的是 relayInfo.UsingGroup(见 compiledRule.inScope)," +
		"那是这次请求实际路由到的**模型分组**,不是「这个人是谁」。" +
		"它属于模型分组删除那条链路,不在用户分组的处置范围里",
}

// knownKeyedModules 是此刻确实有用户分组键的模块。
//
// 它让这条守卫**不会空转**:扫描逻辑一旦被改坏(gorm tag 的写法变了、
// AST 遍历少走一层),hits 会对每个模块都返回空,于是上面那个循环一条断言都不发,
// 测试全绿而实际什么都没检查。那正是"覆盖率型测试"最典型的坏死方式。
//
// lottery 在列表里,正是因为它命中的是 json tag 那一半判据:少了它,
// 把 userGroupListFields 整段删掉也不会有任何测试变红。
var knownKeyedModules = []string{"commission", "groupmatrix", "lottery", "transfer"}

// TestEveryUserGroupKeyedTableDeclaresItsDisposition 扫全部扩展模块。
func TestEveryUserGroupKeyedTableDeclaresItsDisposition(t *testing.T) {
	modules, err := os.ReadDir("modules")
	require.NoError(t, err)

	for _, module := range knownKeyedModules {
		require.NotEmptyf(t, userGroupKeyedColumns(t, filepath.Join("modules", module)),
			"扫描器必须在 %s 里找到用户分组列 —— 找不到说明扫描逻辑本身坏了,"+
				"而坏掉的扫描器会让这条守卫对每一个模块都静默放行", module)
	}

	for _, entry := range modules {
		if !entry.IsDir() {
			continue
		}
		module := entry.Name()
		hits := userGroupKeyedColumns(t, filepath.Join("modules", module))
		remaining := make([]string, 0, len(hits))
		for _, col := range hits {
			if _, exempt := notAUserGroup[module+":"+col]; !exempt {
				remaining = append(remaining, col)
			}
		}
		if len(remaining) == 0 {
			continue
		}
		assert.Truef(t, declaresResidue(t, filepath.Join("modules", module)),
			"模块 %s 有以用户分组名为键的列(%s),但它没有调用 groupns.RegisterResidue。\n"+
				"删掉一个用户分组时这些行会原样留下,而留下**不会报任何错** —— "+
				"它只是一条永远不会命中的配置,与「配置正确」在界面上长得一模一样。\n"+
				"请在该模块下新建 residue.go,在包 init() 里登记 Probe/Sweep,"+
				"并为每一列显式给出 clean / rewrite / keep / block 中的一种;"+
				"如果这一列装的其实不是用户分组,请把它连同理由加进 notAUserGroup",
			module, strings.Join(remaining, "、"))
	}
}

// userGroupKeyedColumns 返回该目录下出现过的用户分组键 —— 列名与 JSON 名单两种。
func userGroupKeyedColumns(t *testing.T, dir string) []string {
	t.Helper()
	seen := map[string]bool{}
	walkGoFiles(t, dir, func(path string, file *ast.File) {
		if strings.HasSuffix(path, "_test.go") {
			return
		}
		ast.Inspect(file, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok || field.Tag == nil {
				return true
			}
			tag := field.Tag.Value
			for name := range userGroupListFields {
				if strings.Contains(tag, `json:"`+name+`"`) {
					seen[name] = true
				}
			}
			if !strings.Contains(tag, "gorm:") {
				return true
			}
			for col := range userGroupColumns {
				// 认 `column:user_group` 与 `json:"user_group"` 之外的形式没有意义:
				// 没写 column 时 GORM 按字段名做蛇形转换,而那正好等于这里要找的列名。
				if strings.Contains(tag, "column:"+col) || matchesSnakeField(field, col) {
					seen[col] = true
				}
			}
			return true
		})
	})
	out := make([]string, 0, len(seen))
	for col := range seen {
		out = append(out, col)
	}
	return out
}

// matchesSnakeField 判断字段名蛇形化之后是不是这个列名(GORM 的默认命名策略)。
func matchesSnakeField(field *ast.Field, col string) bool {
	if len(field.Names) == 0 {
		return false
	}
	want := strings.ReplaceAll(col, "_", "")
	return strings.EqualFold(field.Names[0].Name, want)
}

// declaresResidue 判断该目录下有没有 groupns.RegisterResidue 调用。
func declaresResidue(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	walkGoFiles(t, dir, func(path string, file *ast.File) {
		if strings.HasSuffix(path, "_test.go") {
			return
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "RegisterResidue" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "groupns" {
				found = true
			}
			return true
		})
	})
	return found
}

func walkGoFiles(t *testing.T, dir string, fn func(path string, file *ast.File)) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		require.NoErrorf(t, err, "解析 %s 失败", path)
		fn(path, parsed)
	}
}
