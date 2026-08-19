package withdraw

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handleGetConfig 返回提现门槛与当前用户的可提现额度。
//
// 前端据此决定"申请"按钮是否可点、金额输入的上下界与实时换算,
// 免得用户填完一整张表单才被后端告知不满足条件。
func handleGetConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	cfg := config.Get().Withdraw
	userId := c.GetInt("id")

	withdrawable, err := commission.Withdrawable(userId)
	if err != nil {
		respondErr(c, err)
		return
	}
	usage, err := loadDailyUsage(db.Get(), userId)
	if err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	// 四项额度/频率上限必须下发:后端拦得住不代表用户知道为什么被拦。
	// 不下发的话,用户只会在填完整张表单之后收到一句"已达上限",
	// 而"上限是多少、我今天还剩多少"全靠猜。
	data := gin.H{
		"methods":             cfg.Methods,
		"min_quota":           cfg.MinQuota,
		"remark_max_runes":    cfg.RemarkMaxRunes,
		"daily_max_count":     cfg.DailyMaxCount,
		"max_quota_per_order": cfg.MaxQuotaPerOrder,
		"daily_max_quota":     cfg.DailyMaxQuota,
		"cooldown_seconds":    cfg.CooldownSecs,
		"max_pending_orders":  cfg.MaxPendingOrders,
		"used_today":          usage.Active,
		"used_today_quota":    usage.Quota,
		"payee_account_max":   cfg.PayeeAccountMax,
		"review_sla_hours":    cfg.ReviewSLAHours,
		"auto_credit":         cfg.AutoCredit(),
		"withdrawable_quota":  withdrawable,
	}
	if cfg.HasWithdrawMethod(config.WithdrawMethodFiat) {
		// 币种与汇率都取自**账本**(qy_commission_balance),不是当前配置:
		// 单据金额已经改成"冻结时从 available_fiat 削走的那个数",预览再按
		// 充值页汇率算就会重新出现"页面写 ¥850、单据开 ¥100"的那种落差。
		ledger, err := commission.WithdrawableFiat(userId)
		if err != nil {
			respondErr(c, err)
			return
		}
		fiat := gin.H{
			"currency":       ledger.Currency,
			"min_amount":     cfg.MinFiatAmount,
			"fee_bps":        cfg.FiatFeeBps,
			"payee_channels": SupportedChannels(),
			// 凭证的三项必须下发:前端要据此决定渲不渲染上传控件、accept 填什么、
			// 以及在【选文件的那一刻】就告诉用户"这张 9 MB 的图不行",
			// 而不是让他等一次完整上传之后才收到 413。
			"proof_enabled":   cfg.ProofOn(),
			"proof_max_bytes": cfg.ProofMaxBytes,
			"proof_accept":    ProofAcceptMimes(),
		}
		// 汇率只是预览值:它是"当前这笔可提余额的平均结汇比例",由账本反解
		// (available_fiat ÷ available_quota 折成的 usd)。真正生效的是提交
		// 那一刻从账本冻走的绝对金额,前端必须把这一点显式告诉用户 ——
		// 提交前又结算进一笔新佣金,平均比例就会变。
		//
		// 余额为 0 或法币折算不可用时不下发这两项(前端按缺省处理,不显示那一行):
		// 编一个 1 出来会让"站点从没维护过比例"看起来完全正常。
		if perUnit, err := frozenQuotaPerUnit(); err == nil {
			if rate := impliedFxRate(ledger.Amount, withdrawable, perUnit); rate.IsPositive() {
				fiat["preview_quota_per_unit"] = perUnit.String()
				fiat["preview_fx_rate"] = rate.String()
			}
		}
		data["fiat"] = fiat
	}
	respondOK(c, data)
}

// handleCreate 是唯一会动佣金的用户入口,已挂 CriticalRateLimit。
//
// # 验密闸门为什么在这里,而不在 submitInTx 里
//
// design-13-paypass.md §0 把「佣金提现未接入支付密码」列为已知缺口:
// 拿到会话的攻击者挡不住划转,却能走提现把钱**真的转出站外**。这一行补的就是它。
//
// 位置有三个硬约束,缺一条都不成立:
//
//   - 必须在 ShouldBindJSON **之后** —— 密码随请求体来;
//   - 必须在 create() **之前** —— create 里就开始预检余额、组装单据,
//     进事务后第一条语句就是佣金余额行锁,放进去等于"先动钱再问密码";
//   - 必须在**事务之外**。这一条是本模块特有的,理由有两层:
//     ① submitInTx 的顺序不变量要求佣金余额行锁是事务的第一条语句
//     (见 submitInTx 的说明与 TestSubmitInTxLocksBeforeItReads),
//     验密塞在锁之前会直接把那把锁挤到第二位,四道风控闸门当场静默失效;
//     ② 就算放在锁**之后**也不行 —— bcrypt(cost 10)是几十毫秒的 CPU 开销,
//     那会把每一笔提现的行锁持有时间抬到 bcrypt 的量级,同一用户的并发申请
//     全排在这把锁后面;而且 paypass.verify 用的是自己的 db.Get() 句柄
//     (它必须脱离请求 ctx,否则客户端断连就能取消失败计数写入),
//     在持锁事务里再去借第二条连接写另一张表,是连接池耗尽型死锁的标准形状。
//
// 验密失败时零副作用:此刻还没有任何单据、任何冻结、任何事务。
//
// Require 不通过时已写好响应并 Abort,这里直接 return。它没有任何可以表达
// 豁免的入参 —— 想给提现开后门的人必须先改 paypass.Require 的签名,
// 那是一次看得见的动作,而不是在调用点悄悄包一个 if。
func handleCreate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	// 没设过支付密码时**拒绝并引导去设置**(paypass 返回 qy_pay_pwd_not_set),
	// 不放行。放行看起来"照顾了老用户",实际是把闸门变成一个自助开关:
	// 盗号者拿到会话后只要挑一个没设过密码的账号,提现就完全不设防 ——
	// 而提现是钱**离开站点**的那条路,比只在站内挪账的划转更不该宽松。
	// 划转(裁决 1「首次使用强制设置」)与抽奖已经是这个口径,三条出钱路径
	// 对同一个问题给出两种答案,正是下一个绕过被造出来的地方。
	if !paypass.Require(c, c.GetInt("id"), req.PayPassword) {
		return
	}
	view, err := create(c, c.GetInt("id"), req)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleListRecords 返回当前用户的提现历史。
func handleListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Withdrawal{}).Where("user_id = ?", c.GetInt("id"))
	q = applyStatusFilter(q, c.Query("status"))
	if v := strings.TrimSpace(c.Query("method")); v != "" {
		q = q.Where("method = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	var rows []Withdrawal
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	items := make([]*orderView, 0, len(rows))
	for i := range rows {
		items = append(items, toUserView(&rows[i], nil))
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleGetRecord 返回单据详情与完整时间线。
//
// 时间线是需求"什么时候打的款 / 什么时候拒绝的 / 拒绝理由"的直接答案,
// 用户端也能看到 —— 把它藏进管理端等于没做。
func handleGetRecord(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	w, err := loadUserWithdrawal(c.GetInt("id"), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	events, err := loadEvents(w.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, toUserView(w, events))
}

// handleCancel 撤销申请。
func handleCancel(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	view, err := cancelByUser(c, c.GetInt("id"), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleListPayees 返回已保存的收款方式(仅脱敏值)。
func handleListPayees(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	views, err := listPayeeAccounts(c.GetInt("id"))
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"items": views})
}

type createPayeeRequest struct {
	Channel string            `json:"channel"`
	Label   string            `json:"label"`
	Payee   map[string]string `json:"payee"`
}

// handleCreatePayee 保存一个收款方式。
func handleCreatePayee(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	// 收款信息只在法币路径才有意义;方式没开就不该接受任何 PII 落库。
	if !config.Get().Withdraw.HasWithdrawMethod(config.WithdrawMethodFiat) {
		respondErr(c, errMethodNotAllowed)
		return
	}
	var req createPayeeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	channel, data, err := acceptPayee(req.Channel, req.Payee)
	if err != nil {
		respondErr(c, err)
		return
	}
	view, err := createPayeeAccount(c.GetInt("id"), channel, data, req.Label)
	if err != nil {
		writePayeeAudit(c, payeeAudit{Action: "withdraw.payee.create",
			Result: qymodel.ResultFail, Reason: "新增收款账号失败: " + err.Error()})
		respondErr(c, err)
		return
	}
	// 收款账号决定这个人的钱最终打到哪里。改收款人是提现欺诈的第一步,
	// 而在这条埋点之前,新增/删除收款账号在资金审计里完全没有痕迹 ——
	// 事后只能看到"打给了这张卡",看不到"这张卡是什么时候、从哪个 IP 绑上来的"。
	writePayeeAudit(c, payeeAudit{Action: "withdraw.payee.create",
		Result: qymodel.ResultOK, Ref: view.Ref, After: view, Reason: "新增收款账号"})
	respondOK(c, view)
}

// payeeAudit 是一次收款账号变更的审计输入。
type payeeAudit struct {
	Action string
	Result string
	Reason string
	Ref    string
	Before *payeeView
	After  *payeeView
}

// writePayeeAudit 落一条收款账号变更审计。
//
// 快照走 payeeView(masked)而不是明文:明文的访问本身有独立的、保留期更长的
// qy_pii_audits,把它复制进这张表等于绕过那套管控。
func writePayeeAudit(c *gin.Context, a payeeAudit) {
	userId := c.GetInt("id")
	audit.Write(c, audit.Entry{
		TraceNo:      a.Ref,
		Category:     qymodel.AuditCategoryWithdraw,
		Action:       a.Action,
		ActorType:    qymodel.ActorUser,
		ActorUserId:  userId,
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		Result:       a.Result,
		Reason:       a.Reason,
		BeforeSnap:   payeeSnapshot(a.Before),
		AfterSnap:    payeeSnapshot(a.After),
	})
}

func payeeSnapshot(v *payeeView) string {
	if v == nil {
		return ""
	}
	b, err := common.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// handleDeletePayee 删除一个收款方式(软删除,保留风控指纹)。
func handleDeletePayee(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	ref := strings.TrimSpace(c.Param("ref"))
	if ref == "" {
		respondErr(c, errInvalidParam)
		return
	}
	before, err := deletePayeeAccount(c.GetInt("id"), ref)
	if err != nil {
		writePayeeAudit(c, payeeAudit{Action: "withdraw.payee.delete", Ref: ref,
			Result: qymodel.ResultFail, Before: before,
			Reason: "删除收款账号失败: " + err.Error()})
		respondErr(c, err)
		return
	}
	writePayeeAudit(c, payeeAudit{Action: "withdraw.payee.delete", Ref: ref,
		Result: qymodel.ResultOK, Before: before, Reason: "删除收款账号"})
	respondOK(c, gin.H{"ref": ref})
}

// handleUploadProof 接收一张凭证图片,返回可用于提交申请的 ref。
//
// 分两步(先传图拿 ref、再提交申请)而不是一次性 multipart 提交,是刻意的:
// 申请那一步跑在一个会冻结佣金的扩展库事务里,把几 MiB 的图片解析、魔数校验
// 与磁盘写入塞进去,等于让事务持有时间跟着用户的上行带宽走。
func handleUploadProof(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	userId := c.GetInt("id")
	row, err := acceptProofUpload(c, userId)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 打款凭证是线下法币打款争议里唯一的物证。上传这一步在此之前没有任何
	// 资金审计:事后能看到图,却看不到"这张图是谁、什么时候、从哪个 IP 传的",
	// 而伪造凭证恰恰是这条链路上最容易发生的事。
	//
	// 只在成功后写:失败的上传(格式不对、超限)没有产生物证,
	// 它的调用事实由请求台账(qy_request_audits)覆盖。
	audit.Write(c, audit.Entry{
		TraceNo:      row.Ref,
		Category:     qymodel.AuditCategoryWithdraw,
		Action:       "withdraw.proof.upload",
		ActorType:    qymodel.ActorUser,
		ActorUserId:  userId,
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		Result:       qymodel.ResultOK,
		Reason:       "上传打款凭证",
		AfterSnap: `{"ref":"` + row.Ref + `","mime_type":"` + row.MimeType +
			`","size":` + strconv.FormatInt(row.Size, 10) + `}`,
	})
	respondOK(c, gin.H{
		"ref":        row.Ref,
		"mime_type":  row.MimeType,
		"size":       row.Size,
		"created_at": row.CreatedAt,
	})
}

// handleGetProof 回给本人自己的凭证图片。
//
// 越权判定靠 loadUserWithdrawal —— user_id 进 WHERE 条件,而不是取回来再比。
func handleGetProof(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	w, err := loadUserWithdrawal(c.GetInt("id"), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	p, err := loadProofOfWithdrawal(w.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	serveProof(c, p)
}

// ─────────────────────────── 查询参数辅助 ───────────────────────────

func pathId(c *gin.Context) (int64, bool) {
	return httpq.PathInt64(c, "id")
}

// listPaging 是提现列表接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 这里原本是一份从 controller 复制出去的私有实现,而且是没有跟上补丁的那一份:
// 裸 strconv.Atoi 没有任何上界,paginate 只夹了 page<1。
// ?p=184467440737095518&page_size=100 会让 (page-1)*size 溢出成负数,
// 直接喂进 GORM 的 Offset() —— 而这是一个登录用户就能打到的提现历史只读接口。
var listPaging = httpq.Spec{}

// applyStatusFilter 支持逗号分隔的多状态筛选。
// 只接受已知状态,拒绝把任意字符串拼进 SQL 的可能。
func applyStatusFilter(q *gorm.DB, raw string) *gorm.DB {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return q
	}
	wanted := make([]string, 0, 4)
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if _, ok := knownStatuses[s]; ok {
			wanted = append(wanted, s)
		}
	}
	if len(wanted) == 0 {
		return q
	}
	return q.Where("status IN ?", wanted)
}

var knownStatuses = map[string]struct{}{
	StatusPending: {}, StatusApproved: {}, StatusPaying: {},
	StatusPaid: {}, StatusRejected: {}, StatusCancelled: {}, StatusFailed: {},
}
