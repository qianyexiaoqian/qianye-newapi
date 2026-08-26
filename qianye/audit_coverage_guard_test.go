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
	"afterRuleBatch":         true, // violation:批量启停 / 批量作用分组的批次结局,成功与失败同一出口
	"writeDeleteAudit":       true,
	"writeAdminAudit":        true,
	"writeAdjudicateAudit":   true, // lottery:出款人工落账,成功与失败同一出口(额外带受益人与金额)
	"writeSystemAudit":       true,
	"putConfigFailed":        true,
	"writeTicketAudit":       true,
	"writeMatrixFailure":     true, // groupmatrix:矩阵写入失败(含 409)的补写
	"writeBatchAudit":        true, // channelops:批次结局 + 失败明细,成功与失败同一出口
	"writeScopeFailure":      true, // groupmatrix:接管变更失败的补写
	"writeRelationAudit":     true, // commission:AFF 关系绑定/解绑,成功与失败同一出口
	"writeAdjustAudit":       true, // commission:手工增减佣金,成功与失败同一出口
	"writeFiatRateAudit":     true, // commission:分组法币折算比例写入/删除,同一出口
	"writeBanPolicyAudit":    true, // violation:处置策略档写入/删除,成功与失败同一出口
	"writeCategoryAudit":     true, // violation:违规类型写入/归档,成功与失败同一出口
	"writeAIReviewAudit":     true, // violation:AI 审核渠道写入/删除,成功与失败同一出口
	"writeAISettingAudit":    true, // violation:AI 审核设置(抽样率、总开关)变更,成功与失败同一出口
	"writeAIScopeAudit":      true, // violation:AI 审核作用域策略(谁被送审、抽多少)写入/删除,成功与失败同一出口
	"afterAIChange":          true, // violation:bump 版本 + 重载 + 审计,三件一起做
	"writePlanAudit":         true, // controller/subscription.go:订阅套餐写接口,成功与失败同一出口
	"recordManageAudit":      true, // controller/:管理端资源写接口的统一审计出口(兑换码/渠道/系统设置)

	"writeRestrictedNoticeAudit":   true, // qianye/controller:受限账号公告写入,成功与失败同一出口
	"writeDailyConsumeExportAudit": true, // commission:日消费明细导出,成功与失败同一出口
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
	{"modules/violation/api_admin_batch.go", "adminBatchSetRuleEnabled", 1,
		"多选批量启停一次改掉几十条防护规则的开关。停用方向完全无症状(接口 200、" +
			"业务照常跑、从此零命中),启用方向可能一次把一批 enforce 规则送进真实扣费与封号 —— " +
			"两个方向事后都只剩这条埋点能回答「谁在什么时候把哪几条改了」。" +
			"批量与单条启停不同,**无变化也要留痕**:「20 条全都本来就是启用的」" +
			"常常意味着选错了范围,而那正是事后要查的东西"},
	{"modules/violation/api_admin_batch.go", "adminBatchSetRuleGroupScope", 1,
		"作用分组决定这条规则对哪些**模型分组**生效(比的是 relayInfo.UsingGroup)。" +
			"批量覆盖会把一批规则原有的作用域整串换掉 —— exclude 名单被覆盖等于" +
			"一批原本豁免的分组突然开始被拦被扣费,而规则本身看起来一个字都没改。" +
			"Before 快照里的 op / groups / 方向是事后唯一能重建「这一批原来是什么样」的东西"},
	{"modules/violation/api_admin_banpolicy.go", "adminUpsertBanPolicy", 2,
		"处置策略档决定谁在第几次违规被限制/封号,它比规则表更接近直接改账号状态。" +
			"成功与失败各一条:「我把阈值从 10 改成 3、没生效」只能靠失败审计回答。" +
			"after 快照里冻结的影响面(当时有多少存量账号已越线)几分钟后就无法复现"},
	{"modules/violation/api_admin_banpolicy.go", "adminDeleteBanPolicy", 3,
		"三条路径各一条:删掉普通档(成功)、删库失败、以及**兜底档被拒**。" +
			"最后那一条最容易漏 —— 它在 400 分支里,而「谁试过删兜底档」正是事后要查的:" +
			"删掉兜底档等于让所有没有专属策略的分组落进一个不存在的策略"},
	{"modules/violation/api_admin_category.go", "adminUpsertCategory", 2,
		"违规类型带自己的次数阈值(某一类累计 N 次即触发处置),它与策略档同级 ——" +
			"改一次就改变了「几次会被封」。成功与失败各一条:失败分支里那一条回答的是" +
			"「我把破限类的阈值从 10 改成 3、保存没生效」,而那正是要查的"},
	{"modules/violation/category_suggest.go", "adminApplySuggestedThresholds", 2,
		"一键写入建议阈值是**一次批量的封号线上线**:点一下,五个内置类型同时从" +
			"「不计门槛」变成「N 次就触发处置」,而处置动作由兜底策略档决定(现网是 ban)。" +
			"它没有任何用户可见的症状 —— 界面上只是几个数字变了,人是在各自下一次命中时才被封的。" +
			"成功与失败各一条,而且必须**逐类**留痕:事后要回答的是「哪几类被一键配上了、" +
			"当时写的是几次」,一条汇总行答不了这个问题",
	},
	{"modules/violation/api_admin_category.go", "adminArchiveCategory", 3,
		"三条路径各一条:归档成功、归档失败、以及**兜底类型被拒**。" +
			"最后那一条在 400 分支里,而「谁试过归档未分类」正是事后要查的:" +
			"归档它会让没有显式类型的规则变成无类型的孤儿,它们的命中从此不计入任何类型线。" +
			"归档成功那一条还要留下「这一类的规则被改绑到了哪里、改了几条」"},
	{"modules/violation/api_user.go", "userCreateAppeal", 1,
		"申诉提交要与裁决成对留痕,否则时间线缺掉用户那一半"},
	{"modules/violation/api_admin_aireview.go", "adminCreateAIChannel", 3,
		"AI 审核渠道决定**用户的请求内容被发到哪个第三方地址、用哪把密钥**。" +
			"三条路径各一条:参数校验失败、建行失败、密钥落库失败 —— 后两者都会留下" +
			"一条半成品渠道行,而「谁在什么时候往这里加了一个外部地址」是内容出境" +
			"唯一能事后追责的凭据"},
	{"modules/violation/api_admin_aireview.go", "adminUpdateAIChannel", 3,
		"改 base_url 等于改用户内容的去向,改密钥等于换一套凭证,两者都无症状:" +
			"接口 200、界面正常、审核照跑。before/after 快照(只含掩码,绝不含密文)" +
			"是事后唯一能回答「原来指向哪里」的东西;校验失败、密钥写失败各留一条"},
	{"modules/violation/api_admin_aireview.go", "adminDeleteAIChannel", 2,
		"删掉渠道之后密钥连同地址一起消失(硬删),删的是什么只剩 before 快照能回答;" +
			"删库失败同样要留痕 —— 那时库里到底还在不在是不确定的"},
	{"modules/violation/api_admin_aireview.go", "adminPutAISetting", 2,
		"抽样率直接决定每天为 AI 审核花多少钱,总开关决定用户请求内容会不会被" +
			"发往第三方。两者都是「改一次影响之后每一笔」的量,而且都没有任何" +
			"用户可见的症状。成功与失败各一条:被「未确认内容出境」闸门挡下的那次" +
			"同样要查得到 —— 它说明有人正在试图打开这个开关"},
	{"modules/violation/api_admin_aiscope.go", "adminUpsertAIScope", 6,
		"一条作用域策略同时决定四件事:**谁的请求内容会被发往第三方**、为此花多少钱、" +
			"送过去问的是哪一句话(这一档自己的审核提示词)、以及命中之后记进哪一个违规类型" +
			"(而类型计数是封号判据的一条线)。四者都没有任何用户可见的症状 —— 接口 200、" +
			"界面正常、业务照跑。把一个分组从 exclude 名单里拿掉、把抽样率从 1% 改成 50%、" +
			"把提示词里的「绝不执行」改成「必须执行」、把「命中一律记为」从破限改成未分类," +
			"事后都只有 before/after 快照能回答「原来是什么样」(提示词记的是指纹+档位," +
			"原文进快照会把 SnapshotMaxBytes 撑爆并截掉后面的字段)。校验失败、类型绑定指向" +
			"已归档类型、条数查询失败、撞上条数上限、写库失败五条失败路径各留一条:" +
			"「我配了三次都没保存上」只能靠失败审计回答"},
	{"modules/violation/api_admin_aiscope.go", "adminDeleteAIScope", 2,
		"删掉一条策略之后,原本被它覆盖的分组会**落回兜底抽样率** —— 可能从 0 变成在审、" +
			"也可能从 50% 变成 1%,而界面上只是少了一行。删的是什么只剩 before 快照能回答;" +
			"删库失败同样要留痕,那时库里到底还在不在是不确定的"},
	{"modules/commission/api_admin.go", "adminSettle", 1,
		"手动结算把冻结佣金变成可提现余额,是真的动钱;谁按的按钮必须可查"},
	{"modules/commission/api_admin.go", "adminClawback", 2,
		"人工冲正直接把上线的佣金扣回去。成功要留痕是显然的;失败同样要 —— " +
			"一次把某一对净额冲满之后,同一个 client_request_id 的合法重试会拿到" +
			"「没有可冲正的佣金」,而这条与事实相反的提示此前在审计表里一点痕迹都没有," +
			"「有人在这一刻试图冲正」根本查不到"},
	{"modules/commission/api_admin.go", "adminRerunDailySettle", 1,
		"一日一结算之下,重跑今天这一轮是当天那一跑挂掉之后唯一的整轮补救入口。" +
			"它不直接动钱,但它决定当天剩下那批人今天还能不能拿到钱,而且只会在" +
			"出过故障的那一天被按下 —— 事后复盘必须看得见是谁按的"},
	{"modules/commission/api_admin.go", "adminInvalidateCache", 1,
		"缓存失效是「改完费率立刻生效」这条动作链的最后一步"},
	{"modules/commission/api_admin.go", "adminBlockRelation", 2,
		"拉黑一条邀请关系 = 从这一刻起这个人的消费不再给上线分成,是一次没有任何" +
			"用户可见症状的资金决定。这条埋点还兜着一个刚修掉的形状:快照表是懒建的" +
			"(线上 377 条真实关系 / 11 行快照),旧实现对缺行的关系 Updates 影响 0 行" +
			"却照样回 200 —— 运营以为自刷被止住,佣金却一分不少地继续发。" +
			"失败那条同样要留痕:「有人正在试图拉黑一个不存在的账号」正是最需要查到的形状"},
	{"modules/commission/api_admin.go", "adminPutConfig", 2,
		"费率变更成功与失败都要留痕"},
	{"modules/commission/api_admin.go", "adminPutFiatRate", 1,
		"分组法币折算比例决定平台按什么价把佣金结给推广者。它比费率更隐蔽:" +
			"不改变任何一个额度数字,只改变那些额度值多少钱 —— 界面上的额度余额" +
			"一动不动,提现单上的金额却变了。而且它是逐笔冻结的,改完之后新旧两批" +
			"佣金按两个比例入账,「这个人的 available_fiat 为什么是这个数」事后" +
			"只能靠这条埋点的前后快照回答"},
	{"modules/commission/api_admin.go", "adminDeleteFiatRate", 1,
		"删掉分组档之后该分组回落兜底档,可能从 9 变成 7.3 —— 而界面上只是少了一行。" +
			"删的是什么只剩 before 快照能回答"},
	{"modules/commission/api_admin_relation.go", "adminBindRelation", 2,
		"手工绑定 AFF 关系改的是主库 users.inviter_id —— 从这一刻起,这个人此后所有的" +
			"消费与充值都会给另一个账号分成。它同时会把快照上的拉黑标记清掉。" +
			"before/after 快照(跨两个库拼出来)是事后唯一能回答「原来绑的是谁、" +
			"当时拉黑了没有」的东西;被防环/已绑定闸门拒绝的那次同样要留痕 —— " +
			"「有人正在试图给一个已经有上线的账号改指向」正是最需要查到的形状"},
	{"modules/commission/api_admin_relation.go", "adminRebindRelation", 2,
		"换绑把 users.inviter_id 从一个人挪到另一个人 —— 从这一刻起,这个账号此后所有的" +
			"消费与充值都改给新上线分成,而老上线名下已经产生的佣金全部保留。" +
			"这两句话合起来才是这次操作的全貌,而它们只存在于这条埋点的正文里" +
			"(响应里的 kept_commission_quota 是它的量化形式);before/after 快照跨两个库拼出来," +
			"是事后唯一能回答「原来绑的是谁」的东西 —— 主库那一格已经被覆盖了。" +
			"被防环/自邀请/同人闸门拒绝的那次同样要留痕",
	},
	{"modules/commission/api_admin_relation.go", "adminUnbindRelation", 2,
		"解绑之后主库的 inviter_id 就被清零了,「他曾经是谁的下线」在主库里一个字都不剩。" +
			"这条埋点的正文里写死了「已产生的佣金全部保留、不再产生新的」这条语义与" +
			"保留下来的金额,是事后解释「这个人的佣金为什么停在这个数」的唯一材料;" +
			"重复解绑被拒的那次同样要留痕"},
	{"modules/commission/api_admin_adjust.go", "adminAdjustCommission", 2,
		"手工增减佣金是纯粹的凭空加钱/扣钱,没有任何业务单据触发它。" +
			"它落成一条 manual 计佣行(账目可追溯),但「为什么要加这 5000」只存在于" +
			"这条埋点的事由里;越过可回收上限被 400 挡下的那次同样要留痕 —— " +
			"运营看到 400 会换个数再试,没有这条就分不清哪一次真的生效了"},
	{"modules/commission/api_daily_consume.go", "adminExportDailyConsume", 3,
		"日消费明细导出是这个模块里泄漏面最大的**读**操作:一次请求把一个区间内" +
			"全站每个人花了多少、属于哪个分组、上线是谁整表带走。它不改钱,所以不在" +
			"上面那些资金路径里,但事后追查数据外流时这里正好是个盲区 —— " +
			"「谁在什么时候导走了哪个区间、多少行」只存在于这条埋点里。" +
			"三条:成功一条,区间/关键词被拒一条,取数失败一条 —— 后两条尤其重要," +
			"连续试探区间上界正是拖库前的典型形状"},
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
	{"modules/lottery/api_admin_retire.go", "handleDeleteActivity", 2,
		"删除是本模块唯一一处让证据消失的动作:活动、投注、事件、选项、派奖、奖品、" +
			"种子、密钥履历、旗标十一张表一起走,删完之后**审计行是唯一还能证明这一场存在过的东西**。" +
			"它的 before 快照里装着 commit_hash / seed / roster_hash / chain_head 与全部资金口径," +
			"事后要回答「那一场到底公不公正、发了多少钱」只剩这一行。" +
			"被六道硬闸门(未结束/资金未落定/文本奖未履行/参与未结算/异常未处理/系列结转未收口)" +
			"或二次确认拒绝的那次同样要留痕 —— 「有人正在试图删掉一场还欠着钱的活动」" +
			"正是最需要事后能查到的形状"},
	{"modules/lottery/api_admin_retire.go", "handleHideActivity", 2,
		"下架把一场活动从用户端大厅撤下。它不动钱、可逆,但「这一场为什么突然从站上消失了」" +
			"只有这条埋点能回答;非终态被拒的那次同样要留痕 —— 下架一场进行中的活动" +
			"等于一次隐蔽的提前截止,那正是本模块从头到尾在防的形状"},
	{"modules/lottery/api_admin_retire.go", "handleUnhideActivity", 2,
		"重新上架同样要留痕:一场被下架过又放回去的活动,事后要能看出这段空窗期是谁开的"},
	{"modules/lottery/api_admin_cover.go", "handleSetActivityCover", 2,
		"换封面是**极少数在 publish 之后仍然可写**的活动字段(它不进任何哈希原像)。" +
			"正因如此它是唯一一个能在活动进行中改变卡片外观的动作:一场标着「一等奖 100 元」" +
			"的活动,封面上画什么由这条接口决定,而封面是用户在大厅里最先看到的东西。" +
			"before/after 两份快照回答「那一刻挂的是哪一张」;" +
			"被并发换图拒掉的那次同样要留痕 —— 那说明两个人正在同一场活动上互相覆盖"},
	{"modules/lottery/api_admin_picks.go", "handleSetPicksCap", 2,
		"「一次最多下多少注」与封面同属那一小撮**publish 之后仍然可写**的活动字段" +
			"(它不进任何哈希原像)。正因如此它是唯一一个能在活动进行中改变" +
			"「一个人一次能压多少钱」的动作:参与费 × 注数就是一次点击的扣款额," +
			"把 10 改成 999 等于把单次扣款上限抬高一百倍。before/after 两份快照同时记" +
			"原始值与生效值(0 与 10 在行为上一模一样,只看原始值分不出" +
			"「被清空了」与「明确填了 10」);被并发改值拒掉的那次同样要留痕 —— " +
			"那说明两个人正在同一场活动上互相覆盖"},
	{"modules/lottery/api_admin_cover.go", "handleUploadCover", 1,
		"上传封面是本模块唯一一条能往宿主机磁盘上写字节的路径。" +
			"「这个管理员账号传了多少张、多大」在事后只有这条埋点能回答;" +
			"失败的上传没有产生任何存储副作用,由请求台账覆盖,所以下界是 1 而不是 2"},
	{"modules/lottery/api_admin_cover.go", "handleDiscardCover", 1,
		"与上传那条成对:一张图从磁盘上出现与消失都要能对上," +
			"否则「这个账号现在还占着多少」只剩一半答案"},
	{"modules/lottery/api_admin.go", "handleSetGuessResult", 2,
		"竞猜结果是链下事实、由人手工指定,是全模块最大的信任缺口 —— " +
			"谁在什么时候录了什么、依据是什么,必须永久可查"},
	{"modules/lottery/api_admin.go", "handleRetryPayout", 2,
		"重试出款直接决定一笔钱会不会再发一次;失败的那次同样要留痕"},
	{"modules/lottery/payout_adjudicate.go", "handleAdjudicatePayout", 2,
		"人工落账是绕过全部自动判据的最终裁决:一支把「平台还欠着」的钱在账上宣布为已付清," +
			"另一支让主库对同一个人再加一次钱。审计里那条核对依据是这笔钱事后唯一的解释," +
			"被判据拒绝的那次(自营、越级、与补偿任务撞车)同样必须留痕"},
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
			"谁在什么时候清了哪几个渠道、各自抹掉多少(after 里的 " +
			"cleared_used_quota_total 与逐条 cleared_used_quota),只有这条审计说得出来"},
	{"modules/ticket/api_user.go", "handleDiscardImage", 1,
		"丢弃会真的从磁盘删文件。上传留痕而删除不留痕的话," +
			"「这个账号传过多少、现在还剩什么」在事后只剩一半答案"},
	{"controller/restricted_notice.go", "AdminPutRestrictedNotice", 2,
		"受限账号公告会渲染在**每一个**受限账号的首屏上,内容是申诉渠道与联系方式 ——" +
			"把它改成一个第三方的联系方式,就是把所有正在申诉的人导给别人,而接口照常 200。" +
			"before/after 是事后唯一能回答「谁把申诉入口改到哪去了、原来写的是什么」的东西;" +
			"被长度闸拒绝的那次同样留痕:「有人正在试图往受限用户首屏塞一段超长文本」" +
			"与成功的那次同等重要"},
	// 下面三条是**上游 controller/ 里的接口**,不在 qianye/modules/** 之内。
	// 登记它们的理由:它们与本目录下的资金配置接口是同一类东西(改一次决定
	// 此后谁能付款、按什么价),而在补上埋点之前整个 controller/subscription.go
	// 零审计 —— 佣金侧每一次配置写入都有审计,套餐侧一条都没有,资金相关配置
	// 面上两套标准。路径写成 ../ 是因为本测试跑在 qianye/ 目录下。
	{"../controller/subscription.go", "AdminCreateSubscriptionPlan", 2,
		"新建一个套餐就是往货架上放一件商品:价格、时长、发售窗、送多少额度," +
			"全都在这一次请求里定下。成功与失败各一条 —— 建库失败那次留下的是" +
			"「有人在这一刻试图上架一个 X 元的套餐」,与成功的那次同等重要"},
	{"../controller/subscription.go", "AdminUpdateSubscriptionPlan", 2,
		"sale_start_at / sale_end_at 是**到点自动上下架**,与 enabled(手动上下架)" +
			"一起决定此刻谁能付款;price_amount / total_amount 决定收多少钱、给多少额度。" +
			"改错任何一格的表现都是「套餐从货架上消失了」或「该停售的还在收钱」," +
			"而接口照常 200、界面照常渲染、没有任何报错。before/after 是事后唯一能" +
			"回答「谁把发售时间改了、原来是什么」的东西;写失败同样留痕"},
	{"../controller/redemption.go", "UpdateRedemption", 1,
		"兑换码是一条发钱通道:面额可以被改、状态可以被翻,而两者都没有任何用户可见的症状。" +
			"在补上这条埋点之前它只落一条路由级兜底日志(只有 method/path/status)——" +
			"没有码 id、没有 before/after,事后无从判断「哪张码被改过面额、哪张被翻回启用」," +
			"而这正是「一张码为什么发了两遍钱」唯一能查的地方"},
	{"../controller/subscription.go", "AdminUpdateSubscriptionPlanStatus", 2,
		"列表行内的快速上下架与 violation 的规则快速启停同形:下架方向完全无症状," +
			"套餐只是从售卖页消失,与「从来没建过」无法区分。谁在什么时候把哪个套餐" +
			"下架了,只剩这条埋点能回答;写失败同样留痕"},
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
