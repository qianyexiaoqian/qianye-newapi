package transfer

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// createRequest 是发起划转的请求体。
//
// Amount 一律是 quota 整数,后端绝不接受前端传的美元浮点 ——
// 浮点换算的舍入分歧会直接变成对不上的账。
type createRequest struct {
	ToUserId int    `json:"to_user_id"`
	Amount   int64  `json:"amount"`
	Remark   string `json:"remark"`
	// ClientRequestId 由前端在"打开确认弹窗时"生成并缓存,重试沿用同一个。
	// 点击时才生成会让每次重试变成一笔新划转,幂等彻底失效(裁定 C10)。
	ClientRequestId string `json:"client_request_id"`
	Confirm         bool   `json:"confirm"`
}

type createResponse struct {
	OrderNo          string `json:"order_no"`
	Status           string `json:"status"`
	Amount           int64  `json:"amount"`
	FeeQuota         int64  `json:"fee_quota"`
	ToUserId         int    `json:"to_user_id"`
	ToMaskedUsername string `json:"to_masked_username"`
	MyQuotaAfter     int64  `json:"my_quota_after"`
	CreatedAt        int64  `json:"created_at"`
}

type previewRequest struct {
	Identifier string `json:"identifier"`
	Amount     int64  `json:"amount"`
}

type previewResponse struct {
	Exists         bool   `json:"exists"`
	UserId         int    `json:"user_id"`
	MaskedUsername string `json:"masked_username"`
	MaskedEmail    string `json:"masked_email"`
	Receivable     bool   `json:"receivable"`
	BlockedReason  string `json:"blocked_reason"`
	// 金额相关字段只在请求里带了 amount 时填充,供确认弹窗直接展示"实际扣除多少"。
	Amount   int64 `json:"amount"`
	FeeQuota int64 `json:"fee_quota"`
	Total    int64 `json:"total"`
}

// recordItem 是普通用户可见的流水视图。
// client_ip / user_agent / fail_reason 一律不下发 —— 那是管理端排障用的。
type recordItem struct {
	OrderNo        string `json:"order_no"`
	Direction      string `json:"direction"`
	Counterparty   string `json:"counterparty"`
	CounterpartyId int    `json:"counterparty_id"`
	Amount         int64  `json:"amount"`
	FeeQuota       int64  `json:"fee_quota"`
	Status         string `json:"status"`
	FailCode       string `json:"fail_code"`
	Remark         string `json:"remark"`
	CreatedAt      int64  `json:"created_at"`
	SettledAt      int64  `json:"settled_at"`
}

type limitsResponse struct {
	MinQuota            int64  `json:"min_quota"`
	MaxPerTxQuota       int64  `json:"max_per_tx_quota"`
	DailyMaxQuota       int64  `json:"daily_max_quota"`
	DailyMaxCount       int    `json:"daily_max_count"`
	FeeBps              int    `json:"fee_bps"`
	FeeMinQuota         int64  `json:"fee_min_quota"`
	CooldownSecs        int    `json:"cooldown_seconds"`
	RecipientLookup     string `json:"recipient_lookup"`
	RemainingDailyQuota int64  `json:"remaining_daily_quota"`
	RemainingDailyCount int    `json:"remaining_daily_count"`
	CooldownUntil       int64  `json:"cooldown_until"`
	TransferableQuota   int64  `json:"transferable_quota"`
	BlockedReason       string `json:"blocked_reason"`
	// GroupPolicy 让用户在填收款人**之前**就知道自己能转给哪些分组。
	// 只包含发起方自己的策略,不含任何其他人的分组归属。
	GroupPolicy groupPolicyView `json:"group_policy"`
}

// handleCreate 是唯一会动钱的入口,已挂 CriticalRateLimit。
func handleCreate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	resp, err := create(c, c.GetInt("id"), req)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, resp)
}

// handlePreview 校验收款人并回显脱敏信息,供确认弹窗使用。
func handlePreview(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	var req previewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	// 规则读失败一律报错而不是按"不限制"预览:预览如果比提交宽松,
	// 用户会先看到绿色的"收款人已确认",再在提交时吃一个 403。
	rules, err := loadGroupRules()
	if err != nil {
		respondErr(c, err)
		return
	}
	resp := resolveRecipient(c, c.GetInt("id"), req.Identifier, rules)
	if req.Amount > 0 {
		cfg := config.Get().Transfer
		// 金额非法时不报错,只是不回填:预览的主要用途是确认收款人,
		// 真正的金额判定在提交时统一做,两处各判一次会出现口径漂移。
		if fee, err := computeFee(req.Amount, cfg.FeeBps, cfg.FeeMinQuota); err == nil &&
			req.Amount+fee <= int64(common.MaxQuota) {
			resp.Amount = req.Amount
			resp.FeeQuota = fee
			resp.Total = req.Amount + fee
		}
	}
	respondOK(c, resp)
}

// handleListRecords 返回当前用户参与的全部流水(转出与转入是同一条记录的两个视角)。
func handleListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	me := c.GetInt("id")
	page, size := paginate(c)

	q := db.Get().Model(&Order{})
	switch c.Query("direction") {
	case "out":
		q = q.Where("from_user_id = ?", me)
	case "in":
		q = q.Where("to_user_id = ?", me)
	default:
		q = q.Where("(from_user_id = ? OR to_user_id = ?)", me, me)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	var rows []Order
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	items := make([]recordItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, toRecordItem(r, me))
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// toRecordItem 由服务端算出方向并脱敏对手方。
// 前端不做这两件事:方向算错只是显示问题,脱敏放前端等于没脱敏。
func toRecordItem(r Order, me int) recordItem {
	item := recordItem{
		OrderNo:   r.OrderNo,
		Amount:    r.Amount,
		Status:    r.Status,
		FailCode:  r.FailCode,
		Remark:    r.Remark,
		CreatedAt: r.CreatedAt,
		SettledAt: r.SettledAt,
	}
	if r.FromUserId == me {
		item.Direction = "out"
		item.Counterparty = maskUsername(r.ToUsername)
		item.CounterpartyId = r.ToUserId
		item.FeeQuota = r.FeeQuota
		return item
	}
	item.Direction = "in"
	item.Counterparty = maskUsername(r.FromUsername)
	item.CounterpartyId = r.FromUserId
	return item
}

// handleGetLimits 返回当前用户的实时可用额度,供前端渲染剩余额度与禁用按钮。
func handleGetLimits(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	cfg := config.Get().Transfer
	me := c.GetInt("id")
	now := common.GetTimestamp()

	rules, err := loadGroupRules()
	if err != nil {
		respondErr(c, err)
		return
	}

	var state UserState
	// 从未参与过划转的用户没有状态行,那不是错误,零值就是正确答案。
	if err := db.Get().Where("user_id = ?", me).First(&state).Error; err != nil &&
		!errors.Is(err, gorm.ErrRecordNotFound) {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	rollDay(&state, dayBucket(now))

	resp := limitsResponse{
		MinQuota:        cfg.MinQuota,
		MaxPerTxQuota:   cfg.MaxPerTxQuota,
		DailyMaxQuota:   cfg.DailyMaxQuota,
		DailyMaxCount:   cfg.DailyMaxCount,
		FeeBps:          cfg.FeeBps,
		FeeMinQuota:     cfg.FeeMinQuota,
		CooldownSecs:    cfg.CooldownSecs,
		RecipientLookup: cfg.RecipientLookup,
		// 读不到主库用户行时(下面那个 if 不成立)保持这份中性值:my_group 为空,
		// 前端据此不渲染任何分组提示。绝不用 default 分组的结论顶替 ——
		// 那会给 vip 用户看一份不属于他的规则。
		GroupPolicy: groupPolicyView{
			Policy:        groupViewUnrestricted,
			AllowedGroups: []string{},
			DeniedGroups:  []string{},
		},
	}
	if cfg.DailyMaxQuota > 0 {
		resp.RemainingDailyQuota = clampNonNegative64(cfg.DailyMaxQuota - state.DayOutQuota)
	}
	if cfg.DailyMaxCount > 0 {
		resp.RemainingDailyCount = clampNonNegative(cfg.DailyMaxCount - state.DayOutCount)
	}
	if cfg.CooldownSecs > 0 && state.LastOutAt > 0 {
		if until := state.LastOutAt + int64(cfg.CooldownSecs); until > now {
			resp.CooldownUntil = until
		}
	}
	if state.PendingCount > 0 {
		resp.BlockedReason = "pending_exists"
	}

	if user, err := model.GetUserById(me, false); err == nil {
		// 历史数据里可能存在负余额(上游的扣费路径没有下限校验),
		// 对外展示成 0 而不是负数,否则前端会算出一个负的可转额度。
		resp.TransferableQuota = clampNonNegative64(int64(user.Quota))
		if freeze := int64(cfg.NewAccountFreezeHours) * 3600; freeze > 0 &&
			user.CreatedAt > 0 && now-user.CreatedAt < freeze {
			resp.BlockedReason = "account_too_new"
		}
		resp.GroupPolicy = describeGroupPolicy(rules, user.Group)
		// 分组级禁止转出排在最后覆盖:未结算与新账号都是会自行消失的临时状态,
		// 而这一条要等运营改规则才会解除,对用户来说是更要紧的那个原因。
		if resp.GroupPolicy.Policy == groupViewBlocked {
			resp.BlockedReason = blockedGroupBlocked
		}
	}
	respondOK(c, resp)
}

// handleAdminListRecords 返回完整字段(含 IP、失败原因、余额快照、真实用户名)。
// 用 FlagCore 而不是 FlagTransfer:功能被临时关停时,管理员仍然必须能查历史账。
func handleAdminListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	page, size := paginate(c)
	q := db.Get().Model(&Order{})

	if v := intQuery(c, "user_id", 0); v > 0 {
		q = q.Where("(from_user_id = ? OR to_user_id = ?)", v, v)
	}
	if v := c.Query("order_no"); v != "" {
		q = q.Where("order_no = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := int64Query(c, "min_amount", 0); v > 0 {
		q = q.Where("amount >= ?", v)
	}
	if v := int64Query(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := int64Query(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	var rows []Order
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size})
}

// paginate 解析分页参数。上限 100,防止管理端一次把整张表拉进内存。
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
