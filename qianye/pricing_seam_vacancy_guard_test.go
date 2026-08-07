package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pricing_seam_vacancy_guard_test.go —— 把「计价接缝当前刻意空置」这件事
// 变成一个被测试保护的状态。
//
// # 背景
//
// 「模型按分组单独定价」(grouppricing)已经下线,分组级价格改由
// (用户分组, 模型分组) 的倍率矩阵表达。模块整个删掉了,但它留在上游计价代码里的
// **5 个接缝**没有删:
//
//	relay/helper/price.go       QyGroupModelPrice ×2、QyGroupModelRatio ×2、QyGroupTieredQuota
//	service/task_billing.go     QyGroupTaskRatio
//	service/tiered_settle.go    QyGroupTieredSettle ×2
//
// 保留是**上游成本更低的一侧,而且低得不是一点点**:保留 = 上游 diff 变化 0 行;
// 删除 = 现在就要改 8 行位于真上游文件里的调用,跨 3 个文件、其中两个在计费主
// 路径上,并且要把 3 个已经合并稳定的文件重新变成冲突点。为了减少上游分歧去
// 修改上游文件,方向反了。
//
// # 这个文件防的是什么
//
// 「一个没人赋值的变量」放半年之后,会变成一个没人敢动、也没人说得清的疑问:
// 它是坏了?是死代码?还是有什么我不知道的东西在用它?两条断言把两个状态都钉死:
//
//	接缝存在  →  各包的 qy_pricing_hookpoint_test.go(AST 锁调用点与顺序)
//	接缝空着  →  本文件(行为上恒等 + 全仓没有任何非测试代码给它们赋值)
//
// 两个都是断言之后,它就不再是疑问,而是一条被写下来并且会红的事实。

// TestPricingHooksAreDeliberatelyVacant 用一组非平凡入参调用 5 个接缝变量,
// 断言它们原样返回。
//
// 用非平凡入参(而不是零值)是刻意的:恒等函数与"返回零值"、"返回常量"在零值
// 输入上无法区分,而后两者正是"有人赋了一个坏实现"的样子。
func TestPricingHooksAreDeliberatelyVacant(t *testing.T) {
	info := &relaycommon.RelayInfo{UserGroup: "vip", UsingGroup: "premium"}

	price, usePrice := helper.QyGroupModelPrice(info, 3.5, true)
	assert.Equal(t, 3.5, price, "QyGroupModelPrice 不再是恒等函数 —— 有人给接缝赋了实现")
	assert.True(t, usePrice, "QyGroupModelPrice 改写了 usePrice —— 计费口径会从按 token 变成按次")

	ratio, ok := helper.QyGroupModelRatio(info, 2.25, false)
	assert.Equal(t, 2.25, ratio, "QyGroupModelRatio 不再是恒等函数")
	assert.False(t, ok, "QyGroupModelRatio 改写了 ok —— 会把「价格未配置」变成一个凭空的价格")

	assert.Equal(t, 7.5, helper.QyGroupTieredQuota(info, 7.5), "QyGroupTieredQuota 不再是恒等函数")
	assert.Equal(t, 1.75, service.QyGroupTaskRatio("vip", "sora-2", 1.75), "QyGroupTaskRatio 不再是恒等函数")
	assert.Equal(t, 9.25, service.QyGroupTieredSettle(info, 9.25), "QyGroupTieredSettle 不再是恒等函数")
}

// pricingSeamVars 是 5 个接缝变量的名字。它们分属两个包,但"有没有人给它赋值"
// 这个判据与包无关 —— 赋值只可能写成 `包名.变量 = ...` 或(同包内)`变量 = ...`。
var pricingSeamVars = map[string]bool{
	"QyGroupModelPrice":   true,
	"QyGroupModelRatio":   true,
	"QyGroupTieredQuota":  true,
	"QyGroupTaskRatio":    true,
	"QyGroupTieredSettle": true,
}

// TestNoProductionCodeAssignsPricingSeams 扫描全仓非测试 Go 源码,断言没有任何
// 一处给这 5 个接缝变量赋值。
//
// 上一条断言的是"此刻进程里它们是恒等的",这一条断言的是"没有任何代码路径会让
// 它们不恒等"。两者差别很实在:注入通常发生在某个 Init() 里,而单元测试进程
// 未必跑过那个 Init。
//
// 将来真的要重新启用分组级定价时,这条会红 —— 那是想要的:它逼人回来把包注释里
// 那句「本接缝当前刻意空置」一起改掉,而不是让文档与代码悄悄分家。
func TestNoProductionCodeAssignsPricingSeams(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	var offenders []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", "vendor", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			// 解析不了的文件不该让这条守卫静默放行,但也不该让它变成一个
			// 与本断言无关的失败源 —— 记成 offender,由人来看一眼。
			offenders = append(offenders, path+": 无法解析: "+perr.Error())
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				name := ""
				switch v := lhs.(type) {
				case *ast.Ident:
					name = v.Name
				case *ast.SelectorExpr:
					name = v.Sel.Name
				}
				if pricingSeamVars[name] {
					offenders = append(offenders, rel+" 给 "+name+" 赋值")
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, offenders,
		"计价接缝当前**刻意空置**(grouppricing 已下线)。有代码给它们赋了实现,"+
			"说明分组级定价被重新启用了 —— 请同步更新 relay/helper/qy_pricing_export.go 与 "+
			"service/qy_pricing_export.go 的包注释,并把本条断言改成登记制:%v", offenders)
}
