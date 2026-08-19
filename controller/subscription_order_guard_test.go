package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscription_order_guard_test.go —— 锁住四个网关下单 handler 里那两行。
//
// 这两行都是**每个 handler 一行**、删掉之后不报错不告警的类型,而且四个文件
// 逐行同构、都是上游合并的冲突文件 —— 冲掉一行的表现只是"某一个网关的用户
// 偶尔会遇到怪事",没有任何用例会红。
//
//	PreviewUserGroupPurchase   付款**之前**问一次「你是不是已经永久拥有该用户组」。
//	                           这条规则在 CreateUserSubscriptionFromPlanTx 里是一条
//	                           return error,而支付回调走的就是那个函数。现在回调侧
//	                           对已付款的一档改成放行(否则订单永久卡 pending),
//	                           于是**这一行成了它唯一的实际拦截点** —— 没了它,
//	                           用户会付一笔买不到任何东西的钱。
//	SubscriptionPlanSnapshot   把下单那一刻的套餐随订单带走。没了它,回调回落到
//	                           按 plan_id 现读:运营在用户付款途中改价格/额度/时长/
//	                           升级组,这一单就以旧价成交新内容(实测 1 元换到
//	                           9,000,000 额度 + vip 组 + 12 个月)。

var subscriptionOrderGuardSites = []struct {
	file string
	fn   string
}{
	{"subscription_payment_epay.go", "SubscriptionRequestEpay"},
	{"subscription_payment_stripe.go", "SubscriptionRequestStripePay"},
	{"subscription_payment_creem.go", "SubscriptionRequestCreemPay"},
	{"subscription_payment_waffo_pancake.go", "SubscriptionRequestWaffoPancakePay"},
}

func funcCallsByName(t *testing.T, file, fnName string) map[string]bool {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	require.NoErrorf(t, err, "应当可解析 %s", file)

	var fn *ast.FuncDecl
	for _, decl := range parsed.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Recv == nil && d.Name.Name == fnName {
			fn = d
		}
	}
	require.NotNilf(t, fn, "%s 里找不到 func %s —— 上游改了函数名?"+
		"改名之后这张表必须跟着改,否则这条锁会变成永远绿的摆设", file, fnName)

	calls := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.SelectorExpr:
			calls[f.Sel.Name] = true
		case *ast.Ident:
			calls[f.Name] = true
		}
		return true
	})
	return calls
}

func TestSubscriptionPayHandlersKeepTheirPaidOrderGuards(t *testing.T) {
	for _, site := range subscriptionOrderGuardSites {
		t.Run(site.file, func(t *testing.T) {
			calls := funcCallsByName(t, site.file, site.fn)
			assert.Truef(t, calls["PreviewUserGroupPurchase"],
				"%s 缺少下单前的用户组规则预检 —— 没有它,已经永久拥有该组的用户"+
					"会付一笔买不到任何东西的钱(回调侧为了不卡死订单已经改成放行)", site.file)
			assert.Truef(t, calls["SubscriptionPlanSnapshot"],
				"%s 的订单没有带走套餐快照 —— 回调会回落到按 plan_id 现读,"+
					"运营在用户付款途中改套餐会让这一单以旧价成交新内容", site.file)
		})
	}
}
