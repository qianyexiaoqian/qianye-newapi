package qianye

// modelgroup_residue_coverage_test.go —— 锁住「以模型分组名为键的表必须登记处置」。
//
// 它与 usergroup_residue_coverage_test.go 是同一条锁的两个轴,手法逐条同源。
// 分成两条而不是加一个参数:两个轴的列名集合、豁免理由、以及"哪些模块确实有键"
// 完全不同,合并出来的会是一个每处都要 if 的函数,而这类守卫的全部价值就在于
// 它自己足够简单到不会坏。
//
// # 这条锁防的是什么
//
// 删除一个模型分组时,它的名字散落在多个模块的表里。每一处漏掉的后果都**不会
// 报错**,只会安静地留下一条挂在死名字上的行:
//
//	漏掉 qy_group_grants        一条永远不会命中的授权;而这个名字将来被重新
//	                            用上时,一批毫不相干的用户会突然拿到访问权
//	漏掉 qy_plan_group_grants   已购套餐解锁着一个不存在的分组
//	漏掉 qy_violation_rule      **一道安全闸门**的作用域里留着死名字;exclude 模式下
//	                            这个名字被重建时,一批新流量会凭空落进老豁免里
//
// # 它抓不到什么
//
// 只看**有没有登记**,不看 Probe/Sweep 写得对不对 —— 后者由各模块自己的用例负责。
// 一个 `Sweep: func(...) error { return nil }` 能骗过它(availability 就是故意
// 这么写的,因为它的处置确实是 keep)。它的职责是防止一整个模块被忘掉。

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modelGroupColumns 是"这一列装的是 channels.group / abilities.group 的取值"的列名。
//
// 判据用**列名**而不是字段名,理由同用户分组那条:列名是写进库里、被 SQL 直接
// 引用的那一个,改它必然是一次显式的迁移。
var modelGroupColumns = map[string]bool{
	"model_group": true,
	"group_name":  true,
}

// modelGroupListFields 是**不落在自己一列上**的模型分组名单。
//
// `group_scope` 住在一个逗号串里(违规规则的作用域),按列名扫 gorm tag 看不见它。
// 判据用 json tag:那个名字就是接口契约的一部分,改它是一次显式的兼容性变更。
var modelGroupListFields = map[string]bool{"group_scope": true}

// notAModelGroup 是**显式豁免**:列名撞上了,但它装的不是模型分组。
//
// 必须逐条给理由,不接受空白豁免 —— 一条没有理由的豁免与一次遗漏长得一模一样。
var notAModelGroup = map[string]string{
	"commission:group_name": "返佣费率按**用户分组**分档(这个人属于哪一档 → 抽成几个点)," +
		"它属于用户分组删除那条链路,见 usergroup_residue_coverage_test.go",
}

// knownModelKeyedModules 是此刻确实有模型分组键的模块。
//
// 它让这条守卫**不会空转**:扫描逻辑一旦被改坏,hits 会对每个模块都返回空,
// 于是下面那个循环一条断言都不发,测试全绿而实际什么都没检查。
//
// violation 在列表里,正是因为它命中的是 json tag 那一半判据:少了它,
// 把 modelGroupListFields 整段删掉也不会有任何测试变红。
var knownModelKeyedModules = []string{
	"availability", "groupmatrix", "planentitlement", "violation",
}

// TestEveryModelGroupKeyedTableDeclaresItsDisposition 扫全部扩展模块。
func TestEveryModelGroupKeyedTableDeclaresItsDisposition(t *testing.T) {
	modules, err := os.ReadDir("modules")
	require.NoError(t, err)

	for _, module := range knownModelKeyedModules {
		require.NotEmptyf(t, modelGroupKeyedColumns(t, filepath.Join("modules", module)),
			"扫描器必须在 %s 里找到模型分组键 —— 找不到说明扫描逻辑本身坏了,"+
				"而坏掉的扫描器会让这条守卫对每一个模块都静默放行", module)
	}

	for _, entry := range modules {
		if !entry.IsDir() {
			continue
		}
		module := entry.Name()
		// groupns 自己持有注册表,它的 qy_model_groups / qy_user_groups 由
		// modelgroup_store.go 的删除主干直接处理,不走注册表。
		if module == "groupns" {
			continue
		}
		hits := modelGroupKeyedColumns(t, filepath.Join("modules", module))
		remaining := make([]string, 0, len(hits))
		for _, col := range hits {
			if _, exempt := notAModelGroup[module+":"+col]; !exempt {
				remaining = append(remaining, col)
			}
		}
		if len(remaining) == 0 {
			continue
		}
		assert.Truef(t, declaresModelResidue(t, filepath.Join("modules", module)),
			"模块 %s 有以模型分组名为键的列(%s),但它没有调用 groupns.RegisterModelResidue。\n"+
				"删掉一个模型分组时这些行会原样留下,而留下**不会报任何错** —— "+
				"它只是一条永远不会命中的配置,与「配置正确」在界面上长得一模一样;"+
				"更糟的是这个名字将来被重新建出来时,那条老配置会突然重新命中。\n"+
				"请在该模块下新建 modelgroup_residue.go,在包 init() 里登记 Probe/Sweep,"+
				"并显式给出 clean / rewrite / keep / block 中的一种;"+
				"如果这一列装的其实不是模型分组,请把它连同理由加进 notAModelGroup",
			module, strings.Join(remaining, "、"))
	}
}

func modelGroupKeyedColumns(t *testing.T, dir string) []string {
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
			for name := range modelGroupListFields {
				if strings.Contains(tag, `json:"`+name+`"`) {
					seen[name] = true
				}
			}
			if !strings.Contains(tag, "gorm:") {
				return true
			}
			for col := range modelGroupColumns {
				// 认 `column:model_group` 与字段名蛇形化两种形式:没写 column 时
				// GORM 按字段名做蛇形转换,而那正好等于这里要找的列名。
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

func declaresModelResidue(t *testing.T, dir string) bool {
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
			if !ok || sel.Sel.Name != "RegisterModelResidue" {
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
