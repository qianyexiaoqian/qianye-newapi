package transfer

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"

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
	// PayPassword 是裁决 1 的支付密码,只在这一个入口出现。
	//
	// 走请求体而不是请求头:它与本次划转是同一笔提交,分开传会让"这个密码
	// 授权的是哪一笔"变成一个需要额外约定的问题。paypass 另有 Middleware()
	// 形态供将来接入的、密码不随体走的路径使用。
	PayPassword string `json:"pay_password"`
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
	// 裁决 1 的验密闸门。位置有讲究:
	//
	//  - 必须在 ShouldBindJSON **之后** —— 密码随请求体来的;
	//  - 必须在 create() **之前** —— create 里就开始占额度、写单据了,
	//    放进去等于"先动钱再问密码";
	//  - 必须在**这一个**入口,不能挪进 create():handlePreview 也会调到
	//    create 附近的校验链,验密点一旦下沉就会连预览一起挡住,
	//    而预览不动钱、不该要密码。裁决 1 写的是"验密点只有一处"。
	//
	// Require 不通过时已写好响应并 Abort,这里直接 return。
	// 它没有任何可以表达豁免的入参 —— 联系人不构成豁免(裁决 1),
	// 想加豁免的人必须先改 paypass.Require 的签名,那是一次看得见的动作。
	if !paypass.Require(c, c.GetInt("id"), req.PayPassword) {
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
	// 门槛读失败一律报错,理由同上:预览若比提交宽松,用户会先看到一个
	// "手续费 0",再在提交时被另一个数字收费。
	settings, err := effectiveCtx(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	// 预览只回显手续费,而 fee_bps / fee_min_quota **刻意不可分档**
	// (见 tierableKeys 的说明,由 TestTierableKeysAreAllAddressable 钉住)。
	// 因此这里不去解析发起方那一档:那一次 users 主键查询对结果没有任何影响,
	// 净效果只有「给一个只读接口新增一条 503 分支 + 一次主库查询」,
	// 而预览是划转流程里调用最频繁的接口,那条 503 还会让配错档的那组人
	// 连「确认收款人是谁」都做不到。
	//
	// 手续费改成可分档的那一天,这里必须跟着改回按发起方那一档解析,
	// 否则用户先看到一个手续费、提交时被另一个数字收费。
	cfg := settings.Transfer

	resp := resolveRecipient(c, c.GetInt("id"), req.Identifier, rules)
	if req.Amount > 0 {
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
	page, size := httpq.Paginate(c, listPaging)

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
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
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
	ctx := c.Request.Context()
	me := c.GetInt("id")
	now := common.GetTimestamp()

	rules, err := loadGroupRules()
	if err != nil {
		respondErr(c, err)
		return
	}

	// 回显的必须是**提交时真正会生效的那份门槛**(YAML + qy_settings 覆盖)。
	// 这里读 YAML 而提交读覆盖后的值,就会出现"界面说还能转 100 万、
	// 提交却被拒"—— 而用户看不出问题出在哪。
	settings, err := effectiveCtx(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	var state UserState
	// 从未参与过划转的用户没有状态行,那不是错误,零值就是正确答案。
	if err := db.Get().WithContext(ctx).Where("user_id = ?", me).First(&state).Error; err != nil &&
		!errors.Is(err, gorm.ErrRecordNotFound) {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	bucket := dayBucket(now)
	rollDay(&state, bucket)

	// 主库用户行必须在算门槛**之前**读:门槛已经可以按用户分组分档(见
	// grouplimit.go),而这一页回显的必须是提交时真正会生效的那一档 ——
	// 回显全站兜底会让 vip 看到一个「还能转 100 万」、提交时按自己那一档被拒。
	//
	// 分档按**基准用户组**解析、并且要过 transferForSenderDay 的当日取严,
	// 两处都必须与 create() 逐字一致:少一处,这一页就会回显一个用户根本用不到的
	// 上限,而"界面说能转、提交说不能"是用户唯一能看到的症状。
	//
	// 读不到时(主库抖动)退回全站兜底并继续渲染:这一页的其余部分(剩余额度、
	// 冷却)仍然有用,整页报错反而让用户什么都看不到。基准用户组读不到时**只让
	// 门槛这一段**退回全站兜底,余额与分组提示照常渲染 —— 回显宽一点只是显示
	// 问题,资金路径那一侧仍然失败关闭(见 create)。
	// 分档配置本身读不到不在这一支里 —— 那由 effectiveCtx 直接拒掉。
	user, userErr := model.GetUserById(me, false)
	cfg := settings.Transfer
	if userErr == nil {
		if baseGroups, baseErr := baseUserGroups(user); baseErr == nil {
			tiered, tierErr := settings.transferForSenderDay(baseGroups, &state, bucket)
			if tierErr != nil {
				respondErr(c, tierErr)
				return
			}
			cfg = tiered
		}
	}

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

	if userErr == nil {
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
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Order{})

	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("(from_user_id = ? OR to_user_id = ?)", v, v)
	}
	if v := c.Query("order_no"); v != "" {
		q = q.Where("order_no = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := httpq.Int64(c, "min_amount", 0); v > 0 {
		q = q.Where("amount >= ?", v)
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
	// 下发给前端的数组一律显式初始化,理由见 qianye/json_array_guard_test.go。
	rows := make([]Order, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size})
}

// listPaging 是划转流水接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 这里原本是一份从 controller 复制出去的私有实现,而且是没有跟上补丁的那一份:
// 裸 strconv.Atoi 没有任何上界,paginate 只夹了 page<1。
// ?p=184467440737095518&page_size=100 会让 (page-1)*size 溢出成负数,
// 直接喂进 GORM 的 Offset() —— 而这是一个登录用户就能打到的资金流水只读接口。
var listPaging = httpq.Spec{}
