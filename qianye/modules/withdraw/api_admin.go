package withdraw

import (
	"errors"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 管理端一律用 FlagCore 而不是 FlagWithdraw 做门禁。
//
// 提现功能被临时关停时,队列里已经存在的单仍然必须能被处理 ——
// 否则用户的佣金会被永久冻在 frozen 里,关一个开关就制造一批资金纠纷。

// handleAdminList 是审核队列。
func handleAdminList(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Withdrawal{})
	q = applyStatusFilter(q, c.Query("status"))
	if v := strings.TrimSpace(c.Query("method")); v != "" {
		q = q.Where("method = ?", v)
	}
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	if v := strings.TrimSpace(c.Query("withdraw_no")); v != "" {
		q = q.Where("withdraw_no = ?", v)
	}
	if c.Query("risk_only") == "true" {
		q = q.Where("risk_flags <> ''")
	}
	if v := httpq.Int64(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := httpq.Int64(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	var rows []Withdrawal
	// 默认按申请时间正序:审核队列要先进先出,超时的单才会浮在最上面。
	order := "id asc"
	if c.Query("order") == "desc" {
		order = "id desc"
	}
	if err := q.Order(order).Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	items := make([]*adminOrderView, 0, len(rows))
	for i := range rows {
		items = append(items, toAdminView(&rows[i], nil))
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleAdminStats 返回队列角标:待审 / 待发放 / 两条时限的超时数。
//
// payout_sla_breached 是人工发放模型新引入的观测点:佣金在申请那一刻就已经
// 离开用户的可用池,审核通过后如果没人去发钱,用户就是"钱扣了、东西没拿到"。
// 系统替不了人发钱,但必须让积压在管理端第一屏就有数。
func handleAdminStats(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	type bucket struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
		Quota  int64  `json:"quota"`
	}
	// 显式初始化不是形式主义,这里是本轮唯一一处**线上已经炸过**的位置:
	// GORM 的 db.Scan() 在结果集一行都没有时根本不会碰 dest(finisher_api.go
	// 先 rows.Next() 再 ScanRows),于是 rows 保持 nil,序列化出来是
	// {"buckets":null} 而不是 {"buckets":[]},前端 buckets.find(...) 直接
	// 整页白屏。Find() 走的是另一条路(gorm.Scan 里 MakeSlice)不会留 nil,
	// 但两者的差别是 GORM 的内部实现细节,不该成为我们 JSON 契约的依据。
	// 判据与机器校验见 qianye/json_array_guard_test.go。
	rows := make([]bucket, 0, 2)
	if err := db.Get().Model(&Withdrawal{}).
		Select("status, COUNT(*) AS count, COALESCE(SUM(quota), 0) AS quota").
		Where("status IN ?", activeStatuses).
		Group("status").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Status < rows[j].Status })

	var slaBreached, payoutBreached int64
	if hours := config.Get().Withdraw.ReviewSLAHours; hours > 0 {
		deadline := common.GetTimestamp() - int64(hours)*3600
		if err := db.Get().Model(&Withdrawal{}).
			Where("status = ? AND created_at < ?", StatusPending, deadline).
			Count(&slaBreached).Error; err != nil {
			db.MarkFailure(err)
		}
	}
	// 发放时限从 reviewed_at 起算,不是 created_at:审核花掉的时间归审核时限管,
	// 两道时限共用一个起点会让它们互相污染,谁也说明不了问题出在哪一段。
	if hours := config.Get().Withdraw.PayoutSLAHours; hours > 0 {
		deadline := common.GetTimestamp() - int64(hours)*3600
		if err := db.Get().Model(&Withdrawal{}).
			Where("status = ? AND reviewed_at > 0 AND reviewed_at < ?", StatusApproved, deadline).
			Count(&payoutBreached).Error; err != nil {
			db.MarkFailure(err)
		}
	}
	respondOK(c, gin.H{
		"buckets":             rows,
		"sla_breached":        slaBreached,
		"payout_sla_breached": payoutBreached,
	})
}

// handleAdminGet 返回单据详情与完整时间线(含 Detail 与真实操作者姓名)。
func handleAdminGet(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	w, err := loadWithdrawal(id)
	if err != nil {
		respondErr(c, err)
		return
	}
	events, err := loadEvents(w.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, toAdminView(w, events))
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

// markPaidRequest 是「标记已发放」的请求体。
//
// confirm_quota / confirm_amount 按单据的 method 二选一必填,且必须与单据金额
// 相等(见 markPayout)。它们不是冗余参数:系统不动钱,这个终态动作能被验证的
// 只有"操作者是不是知道自己在发多少"。
type markPaidRequest struct {
	PayoutRef     string `json:"payout_ref"`
	ConfirmQuota  int64  `json:"confirm_quota"`
	ConfirmAmount string `json:"confirm_amount"`
	PaidAt        int64  `json:"paid_at"`
	PayoutNote    string `json:"payout_note"`
}

func handleAdminApprove(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := approve(c, id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleAdminReject 拒绝申请。理由必填 —— 需求原文的核心诉求之一。
func handleAdminReject(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req reasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := reject(c, id, req.Reason)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

func handleAdminMarkPaid(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req markPaidRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := markPayout(c, id, payoutInput{
		PayoutRef:     req.PayoutRef,
		ConfirmQuota:  req.ConfirmQuota,
		ConfirmAmount: req.ConfirmAmount,
		PaidAt:        req.PaidAt,
		Note:          req.PayoutNote,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

func handleAdminFail(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req reasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := markFailed(c, id, req.Reason)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleAdminRevealPayee 返回收款信息明文。
//
// 这是全模块唯一能拿到明文的出口,因此:
//   - 必须带一张 withdraw.payee.read 的安全证明(2FA 或 Passkey 现场签发),
//     见下面「为什么是第二因子」
//   - 强制填写事由(≥4 字符),没有事由的访问事后无法区分正常核对与顺手看看
//   - 每次调用写一条 qy_pii_audits + 一条全局审计,只增不改
//   - 密文损坏 / AAD 不符回 400 与明确提示(联系用户重新提供);密钥版本没配
//     则回 500 —— 后者是运维事故,让管理员去找用户要一遍银行卡号是错的解法
//
// # 为什么是第二因子,而不是 RootAuth
//
// 原先这条路的门槛只有 role≥10 加一段自由文本 reason。同一份代码对**同一量级**
// 的秘密(渠道上游 API Key,POST /api/channel/:id/key)坚持 RootAuth +
// SecureVerificationRequired,两者差了两档 —— 于是任何一个管理员会话或 PAT
// 泄漏,都等于全站提现用户的银行卡号 / 钱包地址 / PayPal 邮箱被批量导出
// (改 :id 遍历即可),留痕只是事后可追溯。
//
// 抬角色是错的解法:收款账号是**打款动作本身必须看到的东西** —— 法币提现要人
// 拿着卡号去银行或钱包转账,收成 root 专属等于全站只有 root 能付款,运营会立刻
// 绕道(把明文抄进工单、导出成表格),PII 反而流得更散。
// 真正要挡的是"拿到会话就等于拿到明文":安全证明绑死当前会话且必须现场过一次
// 2FA/Passkey,被盗的会话与 PAT(PAT 根本没有 session identity,直接被
// RequireSecurityProof 判 SECURITY_PROOF_INVALID)都拿不到它。
// 叠加既有的 CriticalRateLimit(20 次 / 20 分钟),"批量扒库"这条路被封死。
func handleAdminRevealPayee(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	// 安全证明排在最前面:它是这条路上唯一的事前控制,而后面每一步
	// (取单、解密、写 PII 审计)都已经在碰这条单据的数据了。
	if !middleware.RequireSecurityProof(c,
		middleware.SecurityProofScopeWithdrawPayeeRead, []string{"2fa", "passkey"}) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	reason := strings.TrimSpace(c.Query("reason"))
	if len([]rune(reason)) < 4 {
		respondErr(c, errReasonRequired)
		return
	}

	var payee Payee
	err := db.Get().Where("withdrawal_id = ?", id).Take(&payee).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondErr(c, errPayeeNotFound)
		return
	}
	if err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	data, err := openPayee(payee.Nonce, payee.Cipher, withdrawAAD(payee.WithdrawNo), payee.KeyVersion)
	if err != nil {
		// 失败的访问同样要留痕:反复解不开也是一种需要被发现的异常。
		recordPiiAccess(c, piiTarget{
			Resource:   "withdrawal_payee",
			ResourceId: payee.WithdrawalId,
			UserId:     payee.UserId,
			WithdrawNo: payee.WithdrawNo,
		}, reason, "view_plain_failed", "")
		respondErr(c, err)
		return
	}

	fields := make([]string, 0, len(data))
	for k := range data {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	recordPiiAccess(c, piiTarget{
		Resource:   "withdrawal_payee",
		ResourceId: payee.WithdrawalId,
		UserId:     payee.UserId,
		WithdrawNo: payee.WithdrawNo,
	}, reason, "view_plain", strings.Join(fields, ","))

	respondOK(c, gin.H{
		"channel":     payee.Channel,
		"masked":      payee.Masked,
		"payee":       data,
		"withdraw_no": payee.WithdrawNo,
	})
}

// handleAdminPiiAudits 查询明文访问记录。
func handleAdminPiiAudits(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&PiiAudit{})
	if v := httpq.Int(c, "admin_id", 0); v > 0 {
		q = q.Where("admin_id = ?", v)
	}
	if v := httpq.Int(c, "target_user_id", 0); v > 0 {
		q = q.Where("target_user_id = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	rows := make([]PiiAudit, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size})
}

// handleAdminGetProof 回给管理员一张凭证图片。
//
// 与 handleAdminRevealPayee 严格同口径(裁决 3 要求"与 payeeAccount 对齐"):
// 必填事由 ≥4 字符、写 qy_pii_audits + 全局审计、挂关键操作限流。
// 图片没有"脱敏版"这一层,所以它比收款账号更需要那条访问记录 ——
// 管理员看一眼就拿到了全部内容,事后唯一能追的只有"谁在什么时候、以什么事由看的"。
func handleAdminGetProof(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	// 安全证明与 handleAdminRevealPayee 用**同一个 scope**。
	//
	// 这一句原先漏了：上一轮给收款账号加固之后，同一张单的两条 PII 出口只有一条
	// 真的加固了 —— 实测同一个 role=10 的 PAT 打 /payee 是 403
	// SECURITY_PROOF_INVALID，打 /proof 直接 200 拿到与用户上传逐字节相同的
	// 银行转账截图（卡号、户名、金额、时间一次性全在里面，且凭证图片**没有**
	// 脱敏版），改 :id 还能跨用户遍历。也就是说“拿到会话/PAT ≠ 拿到明文”这个
	// 加固目标在两条出口里只成立一条。
	//
	// 共用 scope 而不是新开一个：一次打款本来就要同时看收款账号和凭证，
	// 分成两张证明只会让操作员连过两次 2FA，然后想办法绕开。
	if !middleware.RequireSecurityProof(c,
		middleware.SecurityProofScopeWithdrawPayeeRead, []string{"2fa", "passkey"}) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	reason := strings.TrimSpace(c.Query("reason"))
	if len([]rune(reason)) < 4 {
		respondErr(c, errReasonRequired)
		return
	}
	p, err := loadProofOfWithdrawal(id)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 先留痕再回文件:反过来的话,一次在传输中途断掉的下载就没有任何记录,
	// 而管理员浏览器里的图片已经渲染出来了。
	recordPiiAccess(c, piiTarget{
		Resource:   "withdrawal_proof",
		ResourceId: p.WithdrawalId,
		UserId:     p.UserId,
		WithdrawNo: p.WithdrawNo,
	}, reason, "view_proof", p.MimeType)
	serveProof(c, p)
}

// piiTarget 指明这次明文访问看的是谁的哪一份 PII。
//
// 收款账号与凭证图片是同一类东西的两个载体,因此共用同一张审计表与同一段写入
// 逻辑 —— 各写一份的话,合规导出必然只导出其中一半(而且是先写的那一半)。
type piiTarget struct {
	// Resource 区分 withdrawal_payee 与 withdrawal_proof,导出时按它分类。
	Resource   string
	ResourceId int64
	UserId     int
	WithdrawNo string
}

// recordPiiAccess 记录一次明文访问。写失败只告警不阻断 ——
// 但这是必须被发现的异常:审计静默丢失会让事故无法复盘。
func recordPiiAccess(c *gin.Context, target piiTarget, reason, action, fields string) {
	a := actorOf(c)
	row := &PiiAudit{
		Resource:     target.Resource,
		ResourceId:   target.ResourceId,
		TargetUserId: target.UserId,
		AdminId:      a.Id,
		AdminName:    truncate(a.Name, 64),
		Action:       action,
		Fields:       truncate(fields, 255),
		Reason:       truncate(reason, 255),
		Ip:           truncate(a.IP, 64),
		UserAgent:    truncate(c.Request.UserAgent(), 255),
		CreatedAt:    common.GetTimestamp(),
	}
	if err := db.Get().Create(row).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 写入 PII 访问审计失败: " + err.Error())
	}
	audit.Write(c, audit.Entry{
		TraceNo:      target.WithdrawNo,
		Category:     qymodel.AuditCategoryWithdraw,
		Action:       "withdraw.payee." + action,
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  a.Id,
		ActorName:    a.Name,
		TargetUserId: target.UserId,
		Result:       qymodel.ResultOK,
		Reason:       reason,
	})
}
