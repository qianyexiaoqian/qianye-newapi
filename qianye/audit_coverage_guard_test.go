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
	"afterRuleChange":        true, // violation:版本号 +1 + 重载 + 审计,三件一起做
	"writeDeleteAudit":       true,
	"writeAdminAudit":        true,
	"writeSystemAudit":       true,
	"putConfigFailed":        true,
	"writeTicketAudit":       true,
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
	{"modules/violation/api_admin.go", "adminSetRuleEnabled", 1,
		"列表行内的快速启停是安全配置变更:停用一条防护规则不会有任何症状 —— " +
			"接口照常 200、业务照常跑、只是从此零命中,与「内置规则包从没导入过」完全同形。" +
			"「谁在什么时候把哪条规则关了」事后只能靠这条埋点回答"},
	{"modules/violation/api_admin.go", "adminCreateRule", 1,
		"规则直接决定谁被扣钱、谁被封号;新增一条 enforce 规则是这一页最重的动作之一"},
	{"modules/violation/api_admin.go", "adminUpdateRule", 1,
		"改 pattern / mode 等于改扣费与封号的判据,before/after 是事后唯一能回答「改的是什么」的东西"},
	{"modules/violation/api_admin.go", "adminDeleteRule", 1,
		"软删之后规则行从列表消失,删的是哪一条只剩审计能回答"},
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
	{"modules/subscription/delete.go", "adminDeletePlan", 2,
		"删套餐会级联作废用户订阅与待处理订单,且套餐行本身会消失 —— " +
			"before 快照是事后唯一能回答「删的是什么」的东西;被拒绝的那次同样要留痕"},
	{"modules/subscription/api_admin.go", "adminPutSeat", 2,
		"全站总名额决定这个套餐还能不能卖出去,改小之后立刻生效;写失败同样要留痕"},
	{"modules/lottery/api_admin.go", "handleCreateActivity", 2,
		"创建活动决定平台最多会发出去多少额度(抽奖派奖是净增发);被上限拦下的那次同样要留痕"},
	{"modules/lottery/api_admin.go", "handleUpdateActivity", 2,
		"草稿期改的是还没被承诺的内容,before/after 是事后唯一能回答「改的是什么」的东西"},
	{"modules/lottery/api_admin.go", "handlePublishActivity", 2,
		"承诺生成是全模块最关键的一次写:三个哈希一经公布就是对外承诺。" +
			"成功那条写在 WriteTx 里(与状态转移同生共死),事务一回滚就消失,失败必须在事务外补一条"},
	{"modules/lottery/api_admin.go", "handleCancelActivity", 2,
		"取消是管理员唯一能改变活动结局的动作,必然触发全额退款;被拒绝的那次同样是重要信号"},
	{"modules/lottery/api_admin.go", "handleSetGuessResult", 2,
		"竞猜结果是链下事实、由人手工指定,是全模块最大的信任缺口 —— " +
			"谁在什么时候录了什么、依据是什么,必须永久可查"},
	{"modules/lottery/api_admin.go", "handleRetryPayout", 2,
		"重试出款直接决定一笔钱会不会再发一次;失败的那次同样要留痕"},
	{"modules/apiaddr/api_admin.go", "adminCreate", 2,
		"地址簿里的 URL 会被原样复制进用户的客户端配置;加了一条指向哪里、" +
			"被上限/判重挡下的那次同样要留痕"},
	{"modules/apiaddr/api_admin.go", "adminUpdate", 2,
		"改 URL 等于把已经在用这条地址的用户悄悄指向别处,before/after 是事后" +
			"唯一能回答「改的是什么」的东西"},
	{"modules/apiaddr/api_admin.go", "adminDelete", 2,
		"删除后行本身消失,before 快照是唯一能回答「删的是哪一条」的东西"},
	{"modules/apiaddr/api_admin.go", "adminReorder", 2,
		"顺序决定用户默认看到/直接采用的是哪一条;并发重排被拒的那次同样是重要信号"},
	{"modules/lottery/api_admin_config.go", "handlePutConfig", 2,
		"手续费与奖品上限决定平台会发出去多少钱;越界被拒的那次同样要留痕"},
	{"modules/ticket/create.go", "create", 2,
		"被防滥用闸门挡下的建单尝试必须留痕,否则「这个账号一分钟内被冷却拦了 40 次」" +
			"这种形状事后完全查不到;成功那条是工单的起点"},
	{"modules/ticket/api_admin.go", "handleAdminSetStatus", 2,
		"关单/重开是管理员单方面改变工单结局的动作,而工单里承载着对用户的承诺;" +
			"被状态机拒绝的那次同样是信号"},
	{"modules/ticket/api_admin.go", "handleAdminSetPriority", 2,
		"等级决定这张单在队列里的位置,改低等于让它沉底;写失败同样要留痕"},
	{"modules/ticket/api_admin.go", "handleAdminAssign", 2,
		"指派把工单从公共队列移进某个人的待办,指错人=一张被静默丢弃的工单;" +
			"before 快照是事后唯一能回答「原来指给谁」的东西"},
	{"modules/ticket/api_admin.go", "handleAdminReply", 1,
		"客服说过的话是对用户的承诺;消息本身 append-only,但「谁在什么时候回的」" +
			"要能不翻正文就查到"},
	{"modules/ticket/api_user.go", "handleUploadImage", 1,
		"工单图片是用户唯一能往磁盘写内容的入口,谁在什么时候从哪个 IP 传的必须可查"},
	{"modules/ticket/api_admin.go", "handleAdminUploadImage", 1,
		"管理端这条上传路径的风险只会更高(AdminAuth 下可以往任意工单塞图);" +
			"埋点没有任何调用者依赖,是最典型的「看起来像死代码」"},
	{"modules/ticket/api_user.go", "handleDiscardImage", 1,
		"丢弃会真的从磁盘删文件。上传留痕而删除不留痕的话," +
			"「这个账号传过多少、现在还剩什么」在事后只剩一半答案"},
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
