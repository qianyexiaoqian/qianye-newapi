package qianye

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audit_coverage_guard_test.go —— 锁住资金路径上的审计埋点。
//
// # 为什么必须是这种形状的测试
//
// 这一批缺陷全都是"某个动作没有埋点":新增/删除收款账号、上传打款凭证、
// 申诉审核、用户提交申诉、管理员手动结算、划转/提现被风控拒绝、
// 配置变更失败。它们的共同点是 —— **没有任何普通测试会因为埋点缺失而变红**。
// 接口照常返回 200,业务照常生效,只有事后要复盘的时候才发现库里什么都没有。
//
// 一次性把它们补齐不难,难的是让它们别再被删掉:埋点是最容易在重构中
// "顺手清掉的死代码"(它没有返回值、没有调用者依赖它)。因此把
// "这些函数里必须有审计写入"本身变成断言。
//
// # 这条锁抓不到什么
//
// 它只看**有没有调用**,不看调用的内容对不对、也不看是不是所有分支都覆盖到。
// 一个 `if false { audit.Write(...) }` 能骗过它。它的职责是防止整段消失,
// 不是证明埋点正确 —— 后者由各模块自己的用例负责。
// 计数下界(want)针对的是"成功写了、失败没写"那一类:那正是本轮修掉的形状之一。

// auditWriteFuncs 是"真的会往审计表写一行"的函数名。
//
// 只认函数名不认包名:模块内的 writePayeeAudit / writeConfigUpdateAudit /
// writeRuleFailure 是本地封装,它们最终都落到 audit.Write。
var auditWriteFuncs = map[string]bool{
	"Write":                  true, // audit.Write
	"WriteTx":                true, // audit.WriteTx
	"WriteConfigUpdate":      true, // audit.WriteConfigUpdate
	"writeConfigUpdateAudit": true,
	"writePayeeAudit":        true,
	"writeRuleFailure":       true,
}

// auditRequired 列出必须留痕的资金路径,值是该函数体内审计写入的**最少**次数。
//
// 大于 1 的都是"成功与失败各一条"。只写成功那一条正是被修掉的缺陷:
// 划转/提现被风控拒绝、配置变更事务回滚,在此之前统统零痕迹,
// 而"这个账号连续 20 次撞日限额"恰恰是最需要查的东西。
var auditRequired = []struct {
	file string
	fn   string
	want int
	why  string
}{
	{"modules/withdraw/api_user.go", "handleCreatePayee", 2,
		"收款账号=钱最终打到哪里,改收款人是提现欺诈第一步;成功与失败都要留痕"},
	{"modules/withdraw/api_user.go", "handleDeletePayee", 2,
		"删除收款账号同上;删掉的是哪一张卡由 before 快照回答"},
	{"modules/withdraw/api_user.go", "handleUploadProof", 1,
		"打款凭证是线下法币争议里唯一的物证,伪造凭证是这条链路最常见的攻击"},
	{"modules/withdraw/create.go", "create", 2,
		"被风控闸门挡下的提现申请必须留痕,否则封号前的连续尝试查不到"},
	{"modules/transfer/service.go", "create", 2,
		"被风控拒绝的划转必须留痕,否则「连续撞日限额+换收款人」这种洗号形状零痕迹"},
	{"modules/violation/api_admin.go", "adminReviewAppeal", 1,
		"申诉裁决能一次性撤销封禁并翻转扣费,在此之前整个函数零审计"},
	{"modules/violation/api_user.go", "userCreateAppeal", 1,
		"申诉提交要与裁决成对留痕,否则时间线缺掉用户那一半"},
	{"modules/commission/api_admin.go", "adminSettle", 1,
		"手动结算把冻结佣金变成可提现余额,是真的动钱;谁按的按钮必须可查"},
	{"modules/commission/api_admin.go", "adminInvalidateCache", 1,
		"缓存失效是「改完费率立刻生效」这条动作链的最后一步"},
	{"modules/commission/api_admin.go", "adminPutConfig", 2,
		"费率变更成功与失败都要留痕"},
	{"modules/transfer/api_admin_config.go", "adminPutTransferConfig", 3,
		"门槛变更:成功、回读失败、事务回滚三条路径各一条"},
	{"modules/usergroup/api_admin.go", "adminPutConfig", 2,
		"默认分组决定此后所有新用户能不能用模型;写失败同样要留痕"},
	{"modules/grouppricing/api_admin.go", "adminCreateRule", 2,
		"分组定价的成功审计写在 WriteTx 里,事务一回滚就消失,失败必须在事务外补一条"},
	{"modules/grouppricing/api_admin.go", "adminUpdateRule", 2, "同上"},
	{"modules/grouppricing/api_admin.go", "adminDeleteRule", 2, "同上"},
}

func TestFundPathsKeepTheirAuditWrites(t *testing.T) {
	for _, want := range auditRequired {
		t.Run(want.file+":"+want.fn, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, want.file, nil, 0)
			require.NoError(t, err)

			var fn *ast.FuncDecl
			for _, decl := range file.Decls {
				d, ok := decl.(*ast.FuncDecl)
				if ok && d.Recv == nil && d.Name.Name == want.fn {
					fn = d
				}
			}
			require.NotNilf(t, fn, "%s 里必须有 func %s —— 改名了就把这张表一起改", want.file, want.fn)

			got := 0
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					if auditWriteFuncs[f.Sel.Name] {
						got++
					}
				case *ast.Ident:
					if auditWriteFuncs[f.Name] {
						got++
					}
				}
				return true
			})

			assert.GreaterOrEqualf(t, got, want.want,
				"%s 至少需要 %d 处审计写入,实际 %d 处。理由:%s。\n"+
					"没有埋点的接口不会报错,只会在事故复盘时安静地什么都查不到",
				want.fn, want.want, got, want.why)
		})
	}
}
