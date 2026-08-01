package transfer

import (
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
)

// api_contacts.go —— 联系人簿的用户端接口(需求 3-C)。
//
// 四个接口,全部只作用于调用者自己的簿子。**没有任何一个接口会动钱**,
// 也没有任何一个接口的输出会被 /transfer 的执行路径读到 ——
// 这条隔离由 contacts_isolation_test.go 双向钉死,详见 contacts.go 顶部。
//
// 前端拿到 contactView 之后做的唯一一件事是:把 user_id 填进收款人输入框。
// 之后照旧走 preview → 二次确认 → 提交(验密、限额、分组、冷却一个不少)。

// addContactRequest 是添加联系人的请求体。
//
// 收的是 identifier(与 /transfer/preview 完全相同的字段语义)而不是 user_id:
// 直接收 user_id 等于把解析这一步交给前端,而解析恰恰是反枚举防线所在的位置 ——
// 开关(recipient_lookup)、日志(qy_transfer_lookup_logs)、限流都挂在那里。
type addContactRequest struct {
	Identifier string `json:"identifier"`
	Alias      string `json:"alias"`
}

type renameContactRequest struct {
	Alias string `json:"alias"`
}

// handleListContacts 返回调用者自己的联系人簿。
func handleListContacts(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	ctx := c.Request.Context()
	rows, err := loadContacts(ctx, c.GetInt("id"))
	if err != nil {
		respondErr(c, err)
		return
	}
	// max 下发给前端只为渲染"还能加几个",不是权限:真正的上限判定在
	// saveContact 的事务里。
	respondOK(c, gin.H{"items": hydrateContacts(ctx, rows), "max": maxContactsPerUser})
}

// handleAddContact 添加一个联系人。已挂 SearchRateLimit(见 module.go)。
//
// # 为什么必须复用 resolveRecipient
//
// 「输入一个标识,看它返回存在还是不存在」就是用户枚举。/transfer/preview
// 上为此挂了三道防线:recipient_lookup 开关(默认只认纯数字 ID,永不接受
// 用户名模糊搜索)、每次解析都落一条 qy_transfer_lookup_logs(限流只能限
// 速率,发现不了慢速扫库)、以及按用户 ID 而非 IP 的限流(抗代理轮换)。
//
// 如果这里自己写一遍"按 ID/邮箱查一下用户在不在",那三道防线就全部绕过了,
// 而且是以"这只是个通讯录功能"的名义绕过的 —— 那正是本仓反复出现的
// 「同一概念的第 N 份拷贝各自漂移」形状。所以这里一个字都不自己解析。
func handleAddContact(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	var req addContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}

	// 规则读失败一律报错而不是按"不限制"继续:与 handlePreview 同口径。
	// 这里读它只是为了满足 resolveRecipient 的签名,分组判定的结论
	// **刻意不用于拒绝添加**(见下面 switch 的说明)。
	rules, err := loadGroupRules()
	if err != nil {
		respondErr(c, err)
		return
	}

	me := c.GetInt("id")
	resolved := resolveRecipient(c, me, req.Identifier, rules)
	switch {
	case !resolved.Exists:
		respondErr(c, errContactUserNotFound)
		return
	case resolved.BlockedReason == blockedSelf:
		respondErr(c, errContactSelf)
		return
	}
	// 对方被封禁、或分组规则当前不允许转给他 —— 都**不阻止添加**。
	// 理由:添加联系人不是资金操作,而封禁会解除、分组规则会改。
	// 因为"现在转不了"就拒绝存一个联系人,只会让用户在规则放开后发现
	// 自己的簿子里凭空少了一个人。这些状态由列表如实标出(disabled),
	// 真正的拒绝发生在提交那一刻 —— 判定只有一处。

	view, err := saveContact(c.Request.Context(), me, resolved.UserId,
		resolved.MaskedUsername, acceptAlias(req.Alias))
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleRenameContact 改备注名。备注名是 owner 自己写的字符串,只有他看得到。
func handleRenameContact(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req renameContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	if err := renameContact(c.Request.Context(), c.GetInt("id"), id,
		acceptAlias(req.Alias)); err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"id": id})
}

// handleDeleteContact 删除一条联系人。
func handleDeleteContact(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	if err := deleteContact(c.Request.Context(), c.GetInt("id"), id); err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"id": id})
}
