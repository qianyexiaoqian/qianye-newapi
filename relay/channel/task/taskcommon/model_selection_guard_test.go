package taskcommon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modelCarryingSelectors 是「这个表达式取的就是本次请求要用的模型」的判据。
// 适配器装配上游报文时只有这几种写法把模型放进请求结构体。
var modelCarryingSelectors = map[string]bool{
	"UpstreamModelName": true,
	"Model":             true,
	"ModelName":         true,
	"ReqKey":            true,
}

// TestModelSelectionKeysCoverEveryTaskAdaptor 把「metadata 不得改写模型」这条
// 不变量钉在**字段名**上,而不是钉在某一个适配器上。
//
// 原缺陷正是名单漏项:守卫只删 metadata 里的 "model",而 kling 的模型字段叫
// model_name、jimeng 叫 req_key,两条路的模型被 metadata 整体顶掉,用户按便宜
// 模型付费、上游按贵模型出货,渠道 models / 令牌 model_limits / 分组 abilities
// 三层授权同时失效。
//
// 这条守卫扫全部 task 适配器:凡是被填入「本次请求的模型」的结构体字段,它的
// json 名必须在 relaycommon 的模型选择字段名单里。新增适配器时只要它的模型字段
// 叫了别的名字,这里当场变红。
func TestModelSelectionKeysCoverEveryTaskAdaptor(t *testing.T) {
	stripped := make(map[string]bool)
	for _, key := range relaycommon.ModelSelectionMetadataKeys() {
		stripped[key] = true
	}

	root := ".."
	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	foundTags := map[string][]string{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "taskcommon" {
			continue
		}
		pkgDir := filepath.Join(root, entry.Name())
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		require.NoError(t, err, pkgDir)

		for _, pkg := range pkgs {
			structTags := collectStructJSONTags(pkg)
			modelIdents := collectModelCarryingIdents(pkg)
			for _, file := range pkg.Files {
				ast.Inspect(file, func(n ast.Node) bool {
					lit, ok := n.(*ast.CompositeLit)
					if !ok {
						return true
					}
					typeName, ok := lit.Type.(*ast.Ident)
					if !ok {
						return true
					}
					fields, ok := structTags[typeName.Name]
					if !ok {
						return true
					}
					for _, elt := range lit.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := kv.Key.(*ast.Ident)
						if !ok || !carriesModel(kv.Value, modelIdents) {
							continue
						}
						tag, ok := fields[key.Name]
						if !ok || tag == "" {
							continue
						}
						where := entry.Name() + "." + typeName.Name + "." + key.Name
						foundTags[tag] = append(foundTags[tag], where)
						assert.Truef(t, stripped[tag],
							"%s 装载的是本次请求的模型,但它的 json 名 %q 不在 "+
								"relaycommon.ModelSelectionMetadataKeys() 里 —— "+
								"metadata 能用这个键把模型换掉而计费不知情。把 %q 加进名单。",
							where, tag, tag)
					}
					return true
				})
			}
		}
	}

	// 解析真的走通了才算数:少了任何一条,说明扫描本身瞎了,而不是仓里干净。
	for _, mustFind := range []string{"model", "model_name", "req_key"} {
		assert.NotEmptyf(t, foundTags[mustFind],
			"扫描没有找到任何以 %q 承载模型的字段,守卫失效了", mustFind)
	}
}

// collectStructJSONTags 返回 结构体名 → (字段名 → json 名)。
func collectStructJSONTags(pkg *ast.Package) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			fields := map[string]string{}
			for _, field := range st.Fields.List {
				if field.Tag == nil || len(field.Names) == 0 {
					continue
				}
				raw, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					continue
				}
				name := strings.Split(reflect.StructTag(raw).Get("json"), ",")[0]
				for _, ident := range field.Names {
					fields[ident.Name] = name
				}
			}
			out[spec.Name.Name] = fields
			return true
		})
	}
	return out
}

// collectModelCarryingIdents 收集「被直接赋成模型的局部变量名」,比如 ali 的
// upstreamModel := req.Model / upstreamModel = info.UpstreamModelName。
// 只认 RHS 恰好是一个模型选择器的写法,函数调用的结果不算(那类值通常是配置)。
func collectModelCarryingIdents(pkg *ast.Package) map[string]bool {
	idents := map[string]bool{}
	for _, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != len(assign.Rhs) {
				return true
			}
			for i, rhs := range assign.Rhs {
				sel, ok := rhs.(*ast.SelectorExpr)
				if !ok || !modelCarryingSelectors[sel.Sel.Name] {
					continue
				}
				if lhs, ok := assign.Lhs[i].(*ast.Ident); ok {
					idents[lhs.Name] = true
				}
			}
			return true
		})
	}
	return idents
}

func carriesModel(expr ast.Expr, modelIdents map[string]bool) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if modelCarryingSelectors[node.Sel.Name] {
				found = true
			}
		case *ast.Ident:
			if modelIdents[node.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}
