package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// quotaCapExempt 是**刻意**不走 checkQuotaCap 的额度类 YAML 字段。
//
// 留白名单而不是"漏了就漏了":每一条都必须写出理由,而且下一个人加字段时
// 必须显式决定它属于哪一边。
var quotaCapExempt = map[string]string{
	"image_user_quota_bytes": "它是**字节数**不是额度,与 common.MaxQuota 不同量纲",
	// D-16 把这三个门槛改名成 *_stardust 并换了单位;旧键名只剩一个 Deprecated
	// 占位,唯一的作用是让仍写着它们的存量 YAML 在严格解析下不至于启动失败
	// (adoptRenamedCommissionQuotaKeys 告警并置 nil)。它们的值一个字节都不参与
	// 任何计算,给一个永远不会被读的字段加上界只会让下一个人以为它还生效。
	// 真正的上界在同名的 *_stardust 键上,由 validateCommission 逐个 checkQuotaCap。
	"min_settle_quota":    "已废弃的占位键(→ min_settle_stardust),加载时置 nil,不参与任何计算",
	"max_per_order_quota": "已废弃的占位键(→ max_per_order_stardust),加载时置 nil,不参与任何计算",
	"min_credit_quota":    "已废弃的占位键(→ min_credit_stardust),加载时置 nil,不参与任何计算",
}

// TestEveryQuotaConfigFieldHasAnUpperBound 逐个额度类 YAML 字段核对上界。
//
// 此前只有 6 个键走 checkQuotaCap,而 config.go 里 `yaml:"*quota*"` 的额度键
// 有 14 个。漏掉的 8 个各自被下游的就地守卫接住(所以没有活的溢出),
// 但那意味着一份把 transfer.fee_min_quota 配成 MaxInt64 的 YAML 能干净启动,
// 然后**每一次划转**都在 computeFee 里报错;而
// lottery.pay_password_threshold_stardust 越界的表现是支付密码**永不触发** ——
// 那是安全弱化,连报错都没有。
//
// 判据用 AST 数 checkQuotaCap 的字面量参数,不靠人去数注释:清单一旦过期,
// 这条断言当场红。
func TestEveryQuotaConfigFieldHasAnUpperBound(t *testing.T) {
	fields := quotaYAMLFields(t)
	require.NotEmpty(t, fields, "没解析出任何额度类字段,判据本身坏了")

	guarded := quotaCapGuardedNames(t)
	require.NotEmpty(t, guarded)

	var missing []string
	for _, f := range fields {
		if _, ok := quotaCapExempt[f]; ok {
			continue
		}
		if !guarded[f] {
			missing = append(missing, f)
		}
	}
	assert.Emptyf(t, missing,
		"下列额度类配置键没有上界校验:%s\n"+
			"要么给它加一条 checkQuotaCap,要么写进 quotaCapExempt 并说明理由",
		strings.Join(missing, ", "))
}

// 上界本身必须真的拦得住,而 0 必须放行(0 一律是「不限制/不启用」)。
func TestCheckQuotaCapBounds(t *testing.T) {
	require.NoError(t, checkQuotaCap("probe", 0))
	require.NoError(t, checkQuotaCap("probe", int64(common.MaxQuota)))
	require.Error(t, checkQuotaCap("probe", int64(common.MaxQuota)+1))
	require.Error(t, checkQuotaCap("probe", math.MaxInt64))
	require.Error(t, checkQuotaCap("probe", -1))
}

// quotaYAMLFields 收集 Config 里全部 yaml 标签含 "quota" 的字段名。
func quotaYAMLFields(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	out := []string{}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		if rt.Kind() == reflect.Ptr {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if tag != "" && strings.Contains(tag, "quota") && !seen[tag] {
				seen[tag] = true
				out = append(out, tag)
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(Config{}))
	return out
}

// quotaCapGuardedNames 用 AST 数出 checkQuotaCap 第一个参数里出现过的 YAML 字段名。
//
// 参数写的是 "transfer.min_quota" 这种带前缀的全名,取最后一段即字段名。
func quotaCapGuardedNames(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "validate.go", nil, 0)
	require.NoError(t, err)

	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != "checkQuotaCap" || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name := strings.Trim(lit.Value, `"`)
		if idx := strings.LastIndex(name, "."); idx >= 0 {
			name = name[idx+1:]
		}
		out[name] = true
		return true
	})
	// 表驱动那一段(lottery 的四项)是 composite literal 里的字符串,
	// 上面的 CallExpr 分支看不到,单独收一遍。
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "name" {
			return true
		}
		lit, ok := kv.Value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name := strings.Trim(lit.Value, `"`)
		if idx := strings.LastIndex(name, "."); idx >= 0 {
			name = name[idx+1:]
		}
		out[name] = true
		return true
	})
	return out
}
