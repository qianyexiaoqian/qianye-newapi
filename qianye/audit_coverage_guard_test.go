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
	"writeGroupLimitAudit":   true, // transfer:按用户分组的门槛分档,成功与失败同一出口
	"writePayeeAudit":        true,
	"writeRuleFailure":       true,
	"afterRuleChange":        true, // violation:版本号 +1 + 重载 + 审计,三件一起做
	"writeDeleteAudit":       true,
	"writeAdminAudit":        true,
	"writeSystemAudit":       true,
	"putConfigFailed":        true,
	"writeTicketAudit":       true,
	"writeMatrixFailure":     true, // groupmatrix:矩阵写入失败(含 409)的补写
	"writeBatchAudit":        true, // channelops:批次结局 + 失败明细,成功与失败同一出口
	"writeScopeFailure":      true, // groupmatrix:接管变更失败的补写
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
	{"modules/groupns/api_admin.go", "adminSetDefaultModelGroup", 2,
		"「用户分组的默认模型分组」决定该分组下**全部空分组令牌**去哪个渠道池子、按哪个倍率计费。" +
			"配错一个值是整组用户同时 503 或整组用户静默换价,而接口照常 200。" +
			"失败也必须留痕:写失败时库里到底变没变是不确定的,而「有人在这一刻试图把默认改成 X」" +
			"这个事实与成功的那次同等重要"},
	{"modules/groupns/modelgroup_api.go", "adminDeleteModelGroup", 2,
		"删一个模型分组会横跨两个数据库改掉它的全部引用:分组倍率表、全局「用户可选分组」、" +
			"auto 顺序、交叉倍率的内层键、用户分组 × 模型分组的授权、套餐解锁。" +
			"删完之后这些行不复存在,before 快照(完整影响面)是事后唯一能回答" +
			"「删的时候站上是什么样、有没有人点过那两个强制覆盖」的东西;" +
			"被闸门拒绝的那次同样要留痕 —— 「有人正在试图删掉一个还有 200 个令牌指着的分组」" +
			"正是最需要事后能查到的形状"},
	{"modules/groupns/modelgroup_api.go", "adminUpdateModelGroup", 2,
		"模型分组备注是**面向用户**的文案:它覆盖 options.UserUsableGroups 里那段说明," +
			"直接显示在令牌分组下拉与模型广场上。改它等于改一段对外承诺" +
			"(本站现有的那段写着「用户数据本站均不会留存」);" +
			"enabled 还能把一个分组从全站下拉里遮断。写失败同样要留痕"},
	{"modules/groupns/api_admin.go", "adminBackfill", 2,
		"回填会往两张登记表里新增行,而登记表是管理端写校验与默认值解析的判据。" +
			"它只新增、不改已有行,但「谁在什么时候把一批名字登记了进来」是排查" +
			"「这个分组怎么突然可以配默认了」的第一个问题;失败同样留痕"},
	{"modules/groupns/usergroup_admin.go", "adminCreateUserGroup", 2,
		"新建一个用户分组本身零行为变化,但它是此后一切分档的起点:谁在什么时候" +
			"造出了这一档、当时给了什么备注,是排查「这个分组是干什么用的」的唯一材料;" +
			"写失败同样留痕(名字唯一性是跨两个命名空间的判定,被拒的那次说明有人正在" +
			"试图用一个模型分组的名字建用户分组)"},
	{"modules/groupns/usergroup_admin.go", "adminUpdateUserGroup", 2,
		"展示属性改动不动路由与计费,但 enabled 会影响它在下拉与可选清单里的可见性;" +
			"before/after 是事后唯一能回答「改的是什么」的东西"},
	{"modules/groupns/usergroup_admin.go", "adminRenameUserGroup", 2,
		"改名横跨两个数据库改六张表(users / user_subscriptions / subscription_plans /" +
			"登记表 / 范围与授权 / 费率与划转规则),中途失败会停在半成状态。" +
			"「改到哪一步了」只存在于这条审计的 Partial 里;失败那条因此比成功那条更重要"},
	{"modules/groupns/usergroup_admin.go", "adminDeleteUserGroup", 2,
		"删除一个用户分组同时是一次**批量改价**与一次**批量权限变更** —— 这批人迁过去之后" +
			"可用模型分组与倍率都会变,而「删掉一个分组」这句话里一个字都没提到这件事。" +
			"迁移前后的可用清单差与倍率差必须逐条进审计正文:源分组删掉之后就再也重算不出来了。" +
			"被闸门拒绝(套餐仍在引用、目标分组一个模型分组都用不了、影响面已漂移)的那次同样要留痕"},
	{"modules/groupns/usergroup_migrate.go", "adminMigrateUserGroup", 2,
		"「一键迁移」把整整一档人的 users.group 改掉,是一次批量改价与一次批量权限变更 —— " +
			"迁过去之后可用模型分组与倍率都会变,而「迁移用户」这句话里一个字都没提到。" +
			"迁移前后的可用清单差与倍率差必须逐条进审计正文;主库事务失败那次同样要留痕 —— " +
			"「有人在这一刻试图把 700 个人挪进 X」与成功的那次同等重要"},
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
	{"modules/transfer/api_admin_limits.go", "adminPutGroupLimit", 2,
		"按用户分组的门槛分档与全站门槛同一档:它直接决定「这一组人一天能转走多少」," +
			"而且改一档只影响一批人 —— 事后要回答「是谁在什么时候把 vip 的日额度放大了十倍」," +
			"只能靠这条埋点。写失败同样留痕:库里到底变没变是不确定的"},
	{"modules/transfer/api_admin_limits.go", "adminDeleteGroupLimit", 2,
		"删掉一档等于把这一组人的门槛整体换回全站兜底(通常更宽松),而界面上只是少了一行。" +
			"被删掉的那一档配了什么,只存在于这条埋点的 before 快照里;删除失败同样要留痕"},
	{"modules/usergroup/api_admin.go", "adminPutConfig", 2,
		"默认分组决定此后所有新用户能不能用模型;写失败同样要留痕"},
	{"modules/subscription/delete.go", "adminDeletePlan", 2,
		"删套餐会级联作废用户订阅与待处理订单,且套餐行本身会消失 —— " +
			"before 快照是事后唯一能回答「删的是什么」的东西;被拒绝的那次同样要留痕"},
	{"modules/subscription/api_admin.go", "adminPutSeat", 2,
		"全站总名额决定这个套餐还能不能卖出去,改小之后立刻生效;写失败同样要留痕"},
	{"modules/planentitlement/api_admin.go", "adminPutEntitlement", 3,
		"套餐解锁哪些模型分组、以及它的余额能不能用在别的分组上 —— 前者决定「谁能用什么」," +
			"后者直接决定「这笔钱从哪个池子扣」,与改分组倍率同一档。" +
			"三条路径各一条:成功、写失败、连旧值都没读到就失败(库里没变,但「有人在这一刻" +
			"试图改它」照样要留痕,那正是扩展库抖动期间最需要知道的事)"},
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
	{"modules/lottery/text_prize.go", "handleFulfillPrize", 2,
		"文本奖是全模块唯一一处「钱之外还欠着东西」:兑换码一旦填进去,中奖者立刻能看到" +
			"并可能当场用掉。谁在什么时候给谁发了什么档的奖,必须永久可查;" +
			"撞上「已履行过」被拒的那次同样要留痕 —— 那正是重复发码的现场"},
	{"modules/lottery/text_prize.go", "handleUnfulfillPrize", 2,
		"撤销只是账面纠错:明文可能已经被用户看到并用掉了,撤不回来。" +
			"所以这条埋点连同不可删除的 Event 履历,是事后判断「到底发没发出去」的唯一材料;" +
			"对额度奖的撤销尝试被 403 挡下同样要留痕"},
	{"modules/lottery/text_prize.go", "handleRevealPrizeSecret", 2,
		"管理员看兑换码明文与提现页揭示收款账号同一档:「谁在什么时候看了哪一条」" +
			"是事后唯一能回答的东西;尚未履行/解不开而失败的那次同样要留痕"},
	{"modules/lottery/series.go", "handleCreateSeries", 2,
		"期次系列的 issue_cap 是双色球全部损失的上界,创建时冻结、此后任何接口都改不了 —— " +
			"「这个系列最多能发出去多少」事后只能靠这条创建记录回答;被上限拦下的那次同样要留痕"},
	{"modules/lottery/series.go", "handleFundSeries", 2,
		"注资是全模块**唯一**一处平台主动抬高净增发上限的动作:" +
			"谁在什么时候给哪个系列注了多少,是事后复盘资金规模的唯一材料;" +
			"撞上 issue_cap 被拒的那次同样是重要信号(有人正在试图把上限撑破)"},
	{"modules/lottery/series.go", "handleCloseSeries", 2,
		"关闭系列会让滚存池整块作废,而那笔额度在用户眼里就是「下一期的奖池」—— " +
			"关的是哪个系列、当时池子里还有多少、理由是什么,必须永久可查"},
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
	{"modules/groupmatrix/api_admin.go", "adminPutMatrix", 3,
		"矩阵改动直接决定谁能发出请求(撤销一格 = 一批用户立刻 403)与按什么倍率扣钱。" +
			"倍率发布、清单变更各一条,被 base_ratio_hash 挡回去的那次同样要留痕 —— " +
			"「有人拿着一份过期的预览在按保存」正是最需要事后能查到的形状"},
	{"modules/groupmatrix/api_admin.go", "adminPutScope", 2,
		"接管一个用户分组会让上游的全局白名单与 +:/-: 规则整体失效,切 enforce 会立刻收紧;" +
			"被 impact_hash 挡回去(预览已过期)的那次同样是重要信号"},
	{"modules/groupmatrix/api_admin.go", "adminRepairToken", 2,
		"把一条令牌的分组置空会改变它此后使用的分组与倍率,是真的动扣费口径;写失败同样要留痕"},
	// channelops 的三条都只登记 1:它们各自只有一处 audit.WriteConfigUpdate,
	// Result 由本批的结局算出来(一条都没成功 = fail),成功与失败共用同一条路径。
	// 拆成两个分支只会得到两段除 Result 以外逐字节相同的代码,而漏掉其中一段
	// 正是这张表要防的形状 —— 这里用"只有一个出口"从根上消掉了它。
	{"modules/channelops/api_admin.go", "adminBatchDelete", 1,
		"删渠道不可逆,而且会连带清掉 abilities 里的路由行;删的是哪几个、" +
			"哪几个没删掉,事后只剩这条审计的 before/after 能回答"},
	{"modules/channelops/api_admin.go", "adminBatchStatus", 1,
		"批量停用会让一批渠道立刻退出路由,线上表现是「某些模型突然没有可用渠道」;" +
			"「谁在什么时候停了哪一批」必须可查"},
	{"modules/channelops/api_admin.go", "adminBatchResetUsage", 1,
		"清 used_quota 抹掉的是渠道成本核算的累计值,没有任何补算路径;" +
			"清了多少条、原本是谁,只有这条审计说得出来"},
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
