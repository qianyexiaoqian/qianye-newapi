package withdraw

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

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
		fiat := gin.H{
			"currency":       cfg.FiatCurrency,
			"min_amount":     cfg.MinFiatAmount,
			"fee_bps":        cfg.FiatFeeBps,
			"payee_channels": SupportedChannels(),
		}
		// 汇率只是预览值。真正生效的是提交那一刻冻结进单据的汇率,
		// 前端必须把这一点显式告诉用户,否则汇率一变就会有人来投诉。
		if rates, err := freezeRates(); err == nil {
			fiat["preview_quota_per_unit"] = rates.QuotaPerUnit.String()
			fiat["preview_fx_rate"] = rates.FxRate.String()
		}
		data["fiat"] = fiat
	}
	respondOK(c, data)
}

// handleCreate 是唯一会动佣金的用户入口,已挂 CriticalRateLimit。
func handleCreate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagWithdraw) {
		return
	}
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
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
	page, size := paginate(c)
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
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
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
		respondErr(c, err)
		return
	}
	respondOK(c, view)
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
	if err := deletePayeeAccount(c.GetInt("id"), ref); err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"ref": ref})
}

// ─────────────────────────── 查询参数辅助 ───────────────────────────

func pathId(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// paginate 解析分页参数。上限 100,防止一次把整张表拉进内存。
func paginate(c *gin.Context) (page, size int) {
	page = intQuery(c, "p", 1)
	if page < 1 {
		page = 1
	}
	size = intQuery(c, "page_size", 20)
	if size < 1 || size > 100 {
		size = 20
	}
	return page, size
}

func intQuery(c *gin.Context, key string, def int) int {
	v, err := strconv.Atoi(c.Query(key))
	if err != nil {
		return def
	}
	return v
}

func int64Query(c *gin.Context, key string, def int64) int64 {
	v, err := strconv.ParseInt(c.Query(key), 10, 64)
	if err != nil {
		return def
	}
	return v
}

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
