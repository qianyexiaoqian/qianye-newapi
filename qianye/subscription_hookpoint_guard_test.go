package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscription_hookpoint_guard_test.go —— 锁住"全站总名额"闸门在上游代码里的
// 五个挂载点。
//
// # 为什么这条锁必须存在
//
// 这五行是本功能与上游之间**全部**的耦合面,每处 1 行:
//
//	model/subscription.go                            强一致闸门(订阅创建事务内)
//	controller/subscription_payment_epay.go          下单前置预检
//	controller/subscription_payment_stripe.go        同上
//	controller/subscription_payment_creem.go         同上
//	controller/subscription_payment_waffo_pancake.go 同上
//
// 它们全都是**上游合并的冲突文件**,而"合并时被冲掉一行"是完全现实的事故。
// 冲掉之后的表现是:限量套餐静默失效(hook 保持恒等默认实现,谁都能买),
// 或者用户付完钱才在回调里发现没名额 —— 两种都不报错、不告警。
//
// 而模块自己的用例一条都发现不了这件事:它们直接调 gateSeat,或者最多走一次
// model.CreateUserSubscriptionFromPlanTx(那条只覆盖 model 那一处)。四个网关的
// 预检行更是零覆盖 —— 把任意一行删掉,`go test ./qianye/...` 全绿。
//
// # 这条锁抓不到什么
//
// 它只看"这个文件的这个函数里有没有调这个 hook",不看参数对不对、也不看位置。
// 参数与语义由 modules/subscription 自己的用例负责。它的职责是防止整行消失。

// seatGateCallSites 是必须存在闸门调用的上游函数。路径相对 qianye/ 目录。
var seatGateCallSites = []struct {
	file string
	fn   string
	why  string
}{
	{"../model/subscription.go", "CreateUserSubscriptionFromPlanTx",
		"订阅创建的唯一收口点。这一行没了,三条创建路径(支付回调/管理员绑定/余额购买)" +
			"全部绕过名额,限量套餐静默变成不限量"},
	{"../controller/subscription_payment_epay.go", "SubscriptionRequestEpay",
		"下单前置预检。回调那一档是刻意 fail-open 的(钱已经付了不能拒)," +
			"所以这一行才是名额在支付链路上唯一的实际拦截点 —— 没了它,卖满之后用户" +
			"仍能看到收款二维码,付完钱才发现名额超了"},
	{"../controller/subscription_payment_stripe.go", "SubscriptionRequestStripePay", "同上"},
	{"../controller/subscription_payment_creem.go", "SubscriptionRequestCreemPay", "同上"},
	{"../controller/subscription_payment_waffo_pancake.go", "SubscriptionRequestWaffoPancakePay", "同上"},
}

func TestSeatGateKeepsItsUpstreamCallSites(t *testing.T) {
	for _, want := range seatGateCallSites {
		t.Run(want.file, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), want.file, nil, 0)
			require.NoErrorf(t, err, "应当可解析 %s", want.file)

			var fn *ast.FuncDecl
			for _, decl := range file.Decls {
				d, ok := decl.(*ast.FuncDecl)
				if ok && d.Recv == nil && d.Name.Name == want.fn {
					fn = d
				}
			}
			require.NotNilf(t, fn, "%s 里找不到 func %s —— 上游改了函数名?"+
				"改名之后这张表必须跟着改,否则这条锁会变成永远绿的摆设", want.file, want.fn)

			found := false
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					// model 包内是裸调用,controller 里是 model.QyGate...
					if f.Sel.Name == "QyGateSubscriptionSeat" {
						found = true
					}
				case *ast.Ident:
					if f.Name == "QyGateSubscriptionSeat" {
						found = true
					}
				}
				return !found
			})

			assert.Truef(t, found,
				"%s 的 %s 里缺少 QyGateSubscriptionSeat 挂载点。理由:%s。\n"+
					"这一行没了不会报错、不会告警,只会让名额限制安静地不生效",
				want.file, want.fn, want.why)
		})
	}
}

// 上游那五个调用点复用的是紧随其后的既有 `if err != nil`,靠的是 hook 的
// **恒等契约**:入参 err 非 nil 时原样返回。默认实现一旦不再恒等(比如有人
// 顺手改成 `return nil`),扩展未安装的部署会把上游所有"套餐不存在""周期算不出来"
// 的错误一起吞掉 —— 用户会拿到一个成功但空白的下单响应。
//
// 契约写在注释里不够,这里把它变成断言:默认实现必须原样返回入参。
func TestSeatGateDefaultImplementationIsIdentity(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../model/qy_subscription_export.go", nil, 0)
	require.NoError(t, err)

	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "QyGateSubscriptionSeat" {
			return true
		}
		found = true
		require.Len(t, spec.Values, 1, "hook 必须有默认实现,否则未安装扩展时是 nil 直接 panic")
		lit, ok := spec.Values[0].(*ast.FuncLit)
		require.True(t, ok, "默认实现必须是就地的函数字面量,便于一眼看出它是恒等的")

		params := lit.Type.Params.List
		last := params[len(params)-1]
		require.Len(t, last.Names, 1)
		errName := last.Names[0].Name

		require.Len(t, lit.Body.List, 1, "默认实现只能有一句 return,多一句就不再是恒等")
		ret, ok := lit.Body.List[0].(*ast.ReturnStmt)
		require.True(t, ok)
		require.Len(t, ret.Results, 1)
		ident, ok := ret.Results[0].(*ast.Ident)
		require.True(t, ok, "默认实现必须直接返回入参 err,不许包装、不许替换")
		assert.Equal(t, errName, ident.Name,
			"默认实现必须原样返回最后一个入参(err)。返回别的东西(尤其是 nil)会让"+
				"未安装扩展的部署把上游的错误全部吞掉,用户拿到一个成功但空白的响应")
		return false
	})
	require.True(t, found, "model/qy_subscription_export.go 里必须声明 QyGateSubscriptionSeat")
}
