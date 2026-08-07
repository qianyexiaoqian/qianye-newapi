package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// group_ratio_single_resolver_guard_test.go —— 把「分组倍率只有一个解析器」
// 变成一条会红的事实。
//
// # 为什么需要这条守卫
//
// 上游原本有三份逐字相同的倍率解析:
//
//	relay/helper/price.go        预扣 / 主计价
//	service/quota.go             WSS / 音频结算
//	service/task_billing.go      Task 差额结算
//
// 三份复制品迟早会漂移,而且已经漂过一次:task_billing 那份写成了
// `GetGroupGroupRatio(group, group)`(矩阵对角格),预扣按交叉格打折、结算按对角格
// 原价,差额以**追扣**落到用户头上 —— 而三份各自与自己一致,所以任何单元测试都是绿的。
//
// 本轮把它们合并到 ratio_setting.ResolveGroupRatio。合并本身不产生持续保护:
// 下一个人加一条计费路径时,复制粘贴出第四份仍然是最省事的写法。因此把
// 「计费路径不得自己查倍率表」写成断言。
//
// # 这条守卫抓不到什么
//
// 它只看有没有直接调用那两个底层查表函数,不看调用的结论对不对。
// 结论的正确性由 setting/ratio_setting/qy_ratio_export_test.go 的等价表负责。

// billingRatioCallSites 是**只允许经由 ResolveGroupRatio 拿倍率**的计费文件。
//
// 名单是白名单而不是黑名单:新增一条计费路径时它不会自动被守住,
// 这是刻意的取舍 —— 一条只在"文件名恰好匹配"时才生效的守卫会给人虚假的安全感。
// 新路径的作者必须回到这里加一行,而那一行正是他停下来想一想的时刻。
var billingRatioCallSites = []string{
	"relay/helper/price.go",
	"service/quota.go",
	"service/task_billing.go",
	"service/text_quota.go",
}

// forbiddenRatioLookups 是计费路径上禁止直接调用的底层查表函数。
//
// GetUserGroupRatio 也在列:它是**展示口径**(走 InspectGroupRatio,miss 不告警、
// 不进 admin_info)。计费路径调它会让这一笔的 fail-open 在日志里彻底消失,
// 而金额照扣 —— 那正是本轮在消灭的形状。
var forbiddenRatioLookups = map[string]string{
	"GetGroupGroupRatio": "交叉格查表",
	"GetGroupRatio":      "兜底倍率查表",
	"GetUserGroupRatio":  "展示口径的倍率解析(miss 不进 admin_info)",
}

func TestBillingPathsResolveGroupRatioThroughSingleResolver(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	var offenders []string
	resolverUsers := map[string]bool{}

	for _, rel := range billingRatioCallSites {
		path := filepath.Join(root, filepath.FromSlash(rel))
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, perr, "无法解析 %s", rel)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			case *ast.Ident:
				name = fn.Name
			}
			if why, bad := forbiddenRatioLookups[name]; bad {
				offenders = append(offenders, rel+" 直接调用 "+name+"("+why+")")
			}
			if name == "ResolveGroupRatio" {
				resolverUsers[rel] = true
			}
			return true
		})
	}

	assert.Empty(t, offenders,
		"计费路径必须经由 ratio_setting.ResolveGroupRatio 解析分组倍率。"+
			"直接查表就是在造第 N 份复制品 —— 上一份复制品漂成了矩阵对角格,"+
			"预扣打折、结算原价,差额以追扣落到用户头上,而每份复制品都与自己一致所以全绿。"+
			"新增判据(缺失标记、灰度开关)只能加在 setting/ratio_setting/qy_ratio_export.go 里")

	// 反向断言:合一之后这三条路径**确实**在用那个解析器。
	// 没有这一半,把三处的解析整段删掉(倍率恒为 1)同样能让上面的断言通过。
	for _, rel := range []string{"relay/helper/price.go", "service/quota.go", "service/task_billing.go"} {
		assert.True(t, resolverUsers[rel],
			"%s 不再调用 ratio_setting.ResolveGroupRatio —— 分组倍率要么被整段删掉了,"+
				"要么又被换成了本地实现", rel)
	}
}

// TestGroupRatioFallbackReachesConsumeLog 断言每一个写消费日志的计费出口都同时
// 挂了两个 admin_info 标记。
//
// 只挂 quota_saturation 而漏挂 group_ratio_missing 的那一条路径,表现是:
// 这条路径上的 fail-open 计费在日志里**完全没有痕迹**,而其它路径有 ——
// 于是排查的人会得出"只有 WSS 会出这个问题"这种完全错误的结论。
func TestGroupRatioFallbackReachesConsumeLog(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	for _, rel := range []string{"service/quota.go", "service/task_billing.go", "service/text_quota.go"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, perr)

		saturation, fallback := 0, 0
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			// ── 两个形态都要认 ──
			//
			// 文本/WSS 侧手上有 *RelayInfo,走 attachQuotaSaturation /
			// attachGroupRatioFallback;异步 Task 差额结算手上**没有** RelayInfo,
			// 走 …ToOther 那一对。此前这里只认前一对,于是 task_billing.go 的
			// `attachQuotaSaturationToOther` 落不进任何一支、计数 0==0 通过 ——
			// 一个假绿,而 Task 恰恰是单笔金额最大的那条计费链路。
			switch id.Name {
			case "attachQuotaSaturation", "attachQuotaSaturationToOther":
				saturation++
			case "attachGroupRatioFallback", "attachGroupRatioFallbackToOther":
				fallback++
			}
			return true
		})
		assert.Equal(t, saturation, fallback,
			"%s 里 attachQuotaSaturation 与 attachGroupRatioFallback 的调用次数必须相等:"+
				"两者都是「这一笔算钱时出了异常」的管理端标记,漏挂一处等于那条路径上的"+
				"fail-open 计费永远查不出来", rel)
		assert.Positive(t, fallback, "%s 是写消费日志的计费出口,必须挂 attachGroupRatioFallback", rel)
	}
}
