package model

// subscription_sale_gate_coverage_test.go —— 每一个「新购买」入口都必须同时挡两件事。
//
// ═══════════════════════ 它防的是什么 ═══════════════════════
//
// 本仓的新购买入口有七个,分散在六个文件里(余额购买、四个支付网关的下单、
// 兑换码、购买预览),没有任何一个共同的收口函数 —— 真正的收口点
// CreateUserSubscriptionFromPlanTx 排在**付款之后**,在那里拒绝等于收了钱不给货
// (理由见 PlanSaleWindowError 的注释与 gate.go §四)。
//
// 于是"加一个新的支付网关"这件事的正常做法是复制一个现成的 handler 再改几行,
// 而复制时最容易漏掉的正好是这类并列的闸门。上一次漏掉 `!plan.Enabled` 的后果
// 是已下架套餐照卖;这次漏掉时间窗的后果是停售了还能买 —— 两者都不会报错、
// 不会有日志,只会在运营发现"我明明停售了"的那天才被看见。
//
// 判据:**任何文件里出现了 `!plan.Enabled` 这道下架闸门,同一个文件里就必须
// 出现 PlanSaleWindowError**。两者是同一件事的手动档与自动档,理应形影不离。
//
// 粒度是"每文件"而不是"每函数",这一点如实说明:它挡不住"在同一个文件里新写
// 一个 handler 却只抄了其中一句"的情形。选它是因为本仓的实际形状就是一个文件
// 一个下单入口,而按函数切分要引入 go/ast,换来的精度对不上这条守卫的用途。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// saleGateScanRoots 是新购买入口所在的两个目录。
//
// 刻意**不含** qianye/modules/subscription:那里的 gate.go 也读 plan.Enabled,
// 但它是名额闸门的一个分支判断(预检模式下让位给上游的"套餐未启用"),
// 不是一个下单入口,不该被要求再挡一次时间窗。
var saleGateScanRoots = []string{".", "../controller"}

// saleGateExempt 是读 plan.Enabled 但**不是购买入口**的文件。
//
// controller/redemption.go 那一处是**管理员建兑换码**时的套餐有效性校验,
// 不是一次购买。它刻意不挡时间窗:
//
//	未开售  必须放行。为下周才开售的套餐提前印一批码是正常运营动作,
//	        挡住等于逼运营先把开售时间改掉、印完再改回来。
//	已停售  放行是一个**已知且刻意留下的口子**:管理员可以为一个已停售的套餐
//	        印出一批当场就兑不出去的码(真正的拦截在 model.Redeem 里,见
//	        TestRedeemPlanRollsBackWhenPlanSaleEnded)。挡住它需要一个
//	        "只挡停售、不挡未开售"的第三种语义与它自己的错误码,而那属于
//	        兑换码模块的改动,不在发售时间窗这一轮里。钱不会因此少收或多收。
var saleGateExempt = map[string]struct{}{
	"../controller/redemption.go": {},
}

func TestEveryPlanEnabledGateAlsoChecksSaleWindow(t *testing.T) {
	var offenders []string

	for _, root := range saleGateScanRoots {
		entries, err := os.ReadDir(root)
		require.NoError(t, err, "读取目录 %s", root)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(root, name)
			if _, ok := saleGateExempt[filepath.ToSlash(path)]; ok {
				continue
			}
			raw, err := os.ReadFile(path)
			require.NoError(t, err, "读取 %s", path)

			hasEnabledGate := false
			for _, line := range strings.Split(string(raw), "\n") {
				trimmed := strings.TrimSpace(line)
				// 跳过注释:本文件之外,好几处注释都在解释这道闸门。
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.Contains(trimmed, "!plan.Enabled") {
					hasEnabledGate = true
					break
				}
			}
			if !hasEnabledGate {
				continue
			}
			if strings.Contains(string(raw), "PlanSaleWindowError") {
				continue
			}
			offenders = append(offenders, filepath.ToSlash(path))
		}
	}

	require.Emptyf(t, offenders,
		"以下文件挡了「套餐已下架」却没挡「未开售 / 已停售」:\n%s\n\n"+
			"发售时间窗是 enabled 的自动档,两者必须成对出现 —— "+
			"少一半的表现是运营配了停售时间、这条路径照卖不误,而且不会有任何报错或日志。\n"+
			"请在 `!plan.Enabled` 那句旁边补上 model.PlanSaleWindowError(plan, common.GetTimestamp())。",
		strings.Join(offenders, "\n"))
}
