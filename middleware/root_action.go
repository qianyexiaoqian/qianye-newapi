package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// root_action.go —— 「这一个动作只有超级管理员能做」的唯一判据。
//
// # 为什么不是把整条路由换成 RootAuth()
//
// 被提档的从来不是一整个资源面,而是资源面上的**某一个动作**:兑换码只提"铸码",
// 抽奖只提"设定开奖结果"。整组换 RootAuth 会把读、改、删、开活动、封盘、取消
// 一起连坐掉 —— 那是另一个决定,而且是没人做过的决定。
//
// RootActionGate 因此挂在**单条路由**上,挂在 AdminAuth 之后:身份、受限账号、
// 管理档已经判完,这里只回答最后一问「这个人是不是 root」。
//
// # 为什么判据要单独成一个函数,而不是各处 `if c.GetInt("role") < 100`
//
// 抄出来的第二份必然在三件事上漂移:
//   - 响应 code。塌成通用 403 之后前端只能显示"权限不足",而这里唯一有用的
//     下一步是"找超级管理员",两者在界面上完全不可区分(本仓已经修过一次这个形状)。
//   - 审计。被拒的越权尝试是最需要留痕的一类事件,而 GET 路由压根不走
//     middleware/audit.go 的写操作兜底(beginAdminAudit 只包 POST/PUT/PATCH/DELETE),
//     收款人明文与凭证图片恰好都是 GET。
//   - 清单。判据散开之后没有任何一个地方能回答"全站一共有几个动作被提到了超管"。
//
// 后一条由 qianye/root_action_guard_test.go 钉住:那份清单同时是路由接线的断言。
//
// # 与 SecureVerificationRequired / RequireSecurityProof 的关系
//
// 两者防的不是同一件事,因此叠加而不是互相替代:
//   - 档位(本文件)回答**谁有资格**看/写这个东西;
//   - 二次校验(secure_verification.go)回答**此刻坐在键盘前的是不是本人** ——
//     一张被偷走的会话或 PAT 即便属于 root 也拿不到收款人明文。
//
// 提现收款人明文两道都要过,先档位后证明:证明的验证会读 session 身份并有
// 加密开销,把最便宜、最粗的那一道排在前面。

// RootOnlyAction 是一个被提到超级管理员档的**动作**标识。
//
// 值同时是审计里的 attempted_action 与前端可读的动作名,因此用点分小写、
// 与既有审计 action 命名一致(见 middleware/audit.go 的 auditRouteActions)。
type RootOnlyAction string

const (
	// RootActionRedemptionCreate 是铸码。兑换码明文等同现金,而铸码是**凭空
	// 增发**:面额上界只有 common.MaxQuota,建多少张只受 count<=100 限制,
	// 且建完之后这批码就在建码人自己的桶里,他随时可以自己兑掉。
	// 查/改/删不提档 —— 它们已经被"按发码人分桶"限制在自己那一桶里,
	// 而 role=10 从此建不出码,那一桶恒为空。
	RootActionRedemptionCreate RootOnlyAction = "redemption.create"

	// RootActionGroupNamespaceWrite 是分组命名空间的写入(建/改/改名/删/回填/
	// 迁移/设默认模型分组/模型分组启停)。它决定"空分组令牌落进哪个池子",
	// 一次误配横跨两个数据库改六张表,影响一整档人的可用分组与账单。
	// 读不提档:影响面预览是这些写动作的前置条件,而分组名本身在模型广场上公开。
	RootActionGroupNamespaceWrite RootOnlyAction = "group_namespace.write"

	// RootActionUserGroupDefaultWrite 是"新注册用户落进哪个分组"。
	// 它决定此后每一个新账号的可用模型与价格,写一次影响的是全部未来用户。
	// 读不提档:role=10 必须能看见当前值,否则他连"为什么新用户没模型"都查不了。
	RootActionUserGroupDefaultWrite RootOnlyAction = "user_group.default.write"

	// RootActionWithdrawPayeeReveal 是提现收款人明文与打款凭证图片。
	// 两者同属 PII 且同属一次线下打款的证据面,因此同一档。
	// 提档**不取代**二次校验,理由见本文件顶部。
	RootActionWithdrawPayeeReveal RootOnlyAction = "withdraw.payee.reveal"

	// RootActionLotteryResultSet 是设定/更改开奖结果。
	//
	// 抽奖只有这**一个**动作被提档:开活动、发布、封盘、取消、隐藏、删除、
	// 换封面、履行奖品、解决对账标记、参与、查看,role=10 全部照旧。
	// 抽奖(draw)与双色球的结果来自 commit-reveal 随机源,管理员无从influence;
	// 只有竞猜(guess)的结果是**链下事实**,必须由人录入 —— 那一处就是全站
	// 唯一"管理员说了算"的开奖口。
	RootActionLotteryResultSet RootOnlyAction = "lottery.result.set"

	// RootActionLotteryPayoutAdjudicate 是「凭人工核对结论给一笔出款落账」。
	//
	// 它是抽奖第二个、也是最后一个被提档的动作,与开奖结果同一条理由的另一半:
	// 那一个是"链下事实必须由人录入",这一个是"自动判定已经答不出来,必须由人
	// 推翻它"。所有自动判据(资金单终态 + 主库 outbox 探针)在这一笔上要么互相
	// 矛盾、要么全都说"判不出来",系统因此把它挂起;本动作是绕过全部自动判据的
	// **最终裁决**,而其中一支(判定"确实没发放")会让主库对同一个人再加一次钱。
	//
	// 通用对账台的 /fund-orders/:order_no/resolve 留在 role=10,是因为它只收
	// Uncertain —— 那是系统自己承认"我不知道"的状态,人只是替它把话说完。
	// 这里推翻的是一个**已经给过的 failed 结论**,严格更危险,所以档位更高。
	//
	// 「重试」不提档:它只在探针明确说"主库没动"时才换代次,判据仍然是机器的。
	RootActionLotteryPayoutAdjudicate RootOnlyAction = "lottery.payout.adjudicate"

	// RootActionUpdateCheck 是「检查二开是否有新版本」。
	//
	// 它是这份清单里唯一一个不动钱也不动 PII 的动作,列进来的理由是另一条:
	// 它是全站唯一一条会让**服务端自己**向第三方(github.com)开出站连接的
	// 管理端路由。那是一次站点行为,不是一次数据读取 —— 离线/内网部署把
	// "这台机器不主动连外网"当成部署前提,而这颗按钮能一次性推翻它。
	//
	// 提的仍然只是这一个动作:版本号的**显示**(GET /admin/version)留在
	// role=10,而且刻意连 requireCore 都不走 —— 排障的第一个问题是"跑的是哪个
	// 版本",它必须在任何降级下都答得出。被提档的只有"替本站发这一次请求"。
	RootActionUpdateCheck RootOnlyAction = "update.check"
)

// RootActionRequiredCode 是被本闸门拒绝时的响应 code。
//
// 刻意与上游 AUTH_INSUFFICIENT_PRIVILEGE 分开:那一个是"这条路由你整条都到不了",
// 而这一个是"这个页面你能用,只有这一个按钮不行"。用户要做的下一步不同
// (换个人登录 vs 找超管代做这一步),前端因此必须能区分。
const RootActionRequiredCode = "ROOT_ACTION_REQUIRED"

// RootActionGate 返回一条挂在单个路由上的闸门中间件。
//
// 必须挂在 AdminAuth() 之后(路由级中间件天然晚于组级中间件),否则
// c.GetInt("role") 恒为 0,所有人都会被拒。
func RootActionGate(action RootOnlyAction) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !RequireRootAction(c, action) {
			return
		}
		c.Next()
	}
}

// RequireRootAction 判断当前请求人是不是超级管理员;不是就写审计、写响应并
// 中止请求,返回 false。调用方在 false 时必须立刻 return。
//
// 导出是因为少数 handler 需要把闸门排在自己的参数校验之后(例如先确认目标
// 单据存在再判档,好让审计带上单号);目前全部调用点都走 RootActionGate。
func RequireRootAction(c *gin.Context, action RootOnlyAction) bool {
	if c.GetInt("role") >= common.RoleRootUser {
		return true
	}

	// 越权尝试自己写一条精确审计,并关掉写操作兜底 —— 兜底那条是
	// action="generic" 的 `METHOD /route`,既说不出被拒的是哪个动作,
	// 也压根不覆盖 GET(收款人明文与凭证图片恰好都是 GET)。
	operatorId := c.GetInt("id")
	ip := common.ClientIP(c)
	adminInfo := map[string]interface{}{
		"admin_id":       operatorId,
		"admin_username": c.GetString("username"),
		"admin_role":     c.GetInt("role"),
		"auth_method":    auditAuthMethod(c),
	}
	auditInfo := map[string]interface{}{
		"method":  c.Request.Method,
		"route":   c.FullPath(),
		"path":    c.Request.URL.Path,
		"status":  http.StatusForbidden,
		"success": false,
	}
	if len(c.Params) > 0 {
		params := map[string]string{}
		for _, p := range c.Params {
			params[p.Key] = p.Value
		}
		auditInfo["params"] = params
	}
	opParams := map[string]interface{}{
		"attempted_action": string(action),
		"required_role":    common.RoleRootUser,
	}
	content := "denied root-only action " + string(action)
	// 同步写,不走 gopool。
	//
	// finishAdminAudit 的兜底用异步是因为它落在**每一次**管理端写请求上;
	// 这里落在的是被拒的越权尝试 —— 频率低,而且它是这次请求留下的唯一一条
	// 记录。异步意味着进程在这一刻退出就把它丢了,而"管理员反复戳超管专属
	// 接口"正是最不该丢的那类事件。代价是给一个已经注定失败的请求多加一次
	// insert,与它本来就会触发的兜底那条一样。
	model.RecordOperationAuditLog(operatorId, content, ip, "authz.root_action_denied", opParams, adminInfo, auditInfo)
	common.SetContextKey(c, constant.ContextKeyAuditLogged, true)

	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"success": false,
		"code":    RootActionRequiredCode,
		// action 让前端能把"哪一个按钮不行"说出来,而不是只说"权限不足"。
		"action":  string(action),
		"message": common.TranslateMessage(c, i18n.MsgAuthRootActionRequired),
	})
	return false
}
