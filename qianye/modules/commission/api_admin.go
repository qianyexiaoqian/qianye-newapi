package commission

import (
	"encoding/json" // 仅取 RawMessage 类型;编解码一律走 common.*
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// adminGetConfig 返回生效配置 + YAML 只读快照。
//
// 分成两块是刻意的:YAML 段(三个口径开关、扫描参数)涉及安全与启动行为,
// 只能改文件后重载;运营参数(费率、成熟期、封顶)才允许在这里改。
func adminGetConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	ctx := c.Request.Context()
	overrides, err := loadOverrides(ctx)
	if err != nil {
		internalError(c, err)
		return
	}
	presentLegacyOverridesAsPercent(overrides)
	s := effectiveCtx(ctx)
	cm := config.Get().Commission
	rules, err := listGroupRates(ctx)
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{
		// 费率一律以**百分比字符串**下发。用字符串而不是 JSON 数字:
		// 10.25 在 JS 的 Number 里同样是二进制浮点,回填输入框时可能变成
		// 10.249999999999998,运营再点一次保存就把这个数字存进了资金配置。
		"effective": gin.H{
			keyTopupRatePercent:   s.TopupRatePercent(),
			keyConsumeRatePercent: s.ConsumeRatePercent(),
			keyMinSettleQuota:     s.MinSettleQuota,
			keyMaxPerOrderQuota:   s.MaxPerOrderQuota,
			keyHoldingDays:        s.HoldingDays,
			keyDailyCapQuota:      s.DailyCapQuota,
			keyLargeAlertQuota:    s.LargeAlertQuota,
			keyMinInviteeAgeHour:  s.MinInviteeAgeHours,
		},
		"overrides":     overrides,
		"editable_keys": editableKeys,
		"percent_keys":  []string{keyTopupRatePercent, keyConsumeRatePercent},
		"group_rates":   groupRateViews(rules),
		"yaml_readonly": gin.H{
			"enabled":                       cm.Enabled,
			"topup_rate_percent":            cm.TopupRatePercent,
			"consume_rate_percent":          cm.ConsumeRatePercent,
			"exclude_redemption_and_manual": cm.ExcludeRedemptionAndManual,
			"exclude_subscription_consume":  cm.ExcludeSubscriptionConsume,
			"refund_clawback":               cm.RefundClawback,
			"settle_interval_seconds":       cm.SettleIntervalSecs,
			"topup_scan_interval_seconds":   cm.TopupScanIntervalSec,
			"topup_scan_lookback_hours":     cm.TopupScanLookbackHours,
			"inviter_cache_seconds":         cm.InviterCacheSecs,
		},
	})
}

// adminPutConfig 修改运营参数。
//
// 每一次改动都必须写审计:费率直接决定平台要付多少钱,
// "谁在什么时候把 3% 改成 8%"事后必须能查到人。
func adminPutConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	// 用 RawMessage 而不是 map[string]float64:费率支持两位小数,而 float64
	// 表示不了 10.25,把它解成浮点再存回去就已经不是运营填的那个数了。
	// 原始字面量交给 decimal 解析,数字与带引号的字符串都收。
	var req map[string]json.RawMessage
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if len(req) == 0 {
		badRequest(c, "qy_invalid_param", "没有需要修改的配置项")
		return
	}

	// 先把全部取值校验并规范化,再统一落库:一半写进去一半 400,
	// 会留下一个谁都没批准的中间费率组合。
	normalized := make(map[string]string, len(req))
	for k, raw := range req {
		if !editable(k) {
			badRequest(c, "qy_invalid_param", "不可修改的配置项: "+k)
			return
		}
		lit := jsonScalarLiteral(raw)
		if isPercentKey(k) {
			units, err := config.RatePercentUnits(lit)
			if err != nil {
				badRequest(c, "qy_invalid_param", "返佣比例("+k+")"+err.Error())
				return
			}
			// 存规范化后的百分比而不是原始输入:"10.250" 与 "10.25" 是同一个
			// 费率,落库前统一形状,前后对比与审计快照才不会出现假差异。
			normalized[k] = config.FormatRatePercent(units)
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(lit), 10, 64)
		if err != nil || v < 0 {
			badRequest(c, "qy_invalid_param", "配置项 "+k+" 必须是非负整数")
			return
		}
		if k == keyMinSettleQuota && v <= 0 {
			badRequest(c, "qy_invalid_param", "最小结算额度必须大于 0")
			return
		}
		normalized[k] = strconv.FormatInt(v, 10)
	}

	ctx := c.Request.Context()
	before := effectiveCtx(ctx)
	operatorId := c.GetInt("id")

	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	// 按键名排序后写入。两个理由:失败留痕可复现;更要紧的是给并发的两次调价
	// 一个固定的加锁顺序 —— 两个管理员同时保存互相交叉的键集时,乱序写正是
	// 死锁的配方,而死锁恰好是这段代码原先最怕的那种失败。
	keys := make([]string, 0, len(normalized))
	for k := range normalized {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 整批包进一个事务。前面的校验只挡住了"一半写进去一半 400";写库过程中
	// 第二个键失败同样会留下一个谁都没有批准的中间费率组合,而且更糟 ——
	// 那时接口已经改过库了,却只回了个 500,运营重试一次就可能再改一遍。
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, k := range keys {
			if err := writeSetting(tx, k, normalized[k], operatorId); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// 审计必须落在事务之外,否则它会跟着一起回滚 —— 而"有人在这一刻试图
		// 改费率、失败了"正是最需要留痕的事实。
		//
		// after 取的是**回滚之后重新读库**的真实值,不是这次想写的值:
		// 万一回滚没有生效(连接被掐、DDL 混进来),这条记录就是唯一能看出
		// "库里已经变了"的地方。事实本身必须留痕,哪怕它与预期不符。
		invalidateSettings()
		writeConfigUpdateAudit(c, qymodel.ResultFail,
			"修改返佣运营参数失败(事务已回滚): "+err.Error(), before, effectiveCtx(ctx))
		internalError(c, err)
		return
	}

	// 新键落地后清掉同义的 1.x 万分比键,避免两个键长期并存、谁生效说不清。
	if _, ok := normalized[keyTopupRatePercent]; ok {
		dropSetting(ctx, legacyKeyTopupRateBps)
	}
	if _, ok := normalized[keyConsumeRatePercent]; ok {
		dropSetting(ctx, legacyKeyConsumeRateBps)
	}
	invalidateSettings()
	after := effectiveCtx(ctx)
	writeConfigUpdateAudit(c, qymodel.ResultOK, "修改返佣运营参数", before, after)
	respond(c, gin.H{"effective": settingsSnapshot(after)})
}

// writeConfigUpdateAudit 落一条运营参数变更审计,成功与失败共用。
//
// 快照走百分比视图而不是裸结构体:事后翻审计的是人,让他去把 1025 心算回
// 10.25% 就是在给自己埋坑。失败那条同样带前后快照 —— 它回答的是
// "有人在这个时刻想把费率改成什么、库里现在实际是什么"。
//
// 组装与写入本身住在 audit.WriteConfigUpdate:这段代码曾经在 commission 与
// transfer 各有一份、除 Action 外逐字节相同。这里只剩"哪个 Action、
// 快照取什么视图"这两件模块自己的事。operatorId 不再往下传 ——
// 共享实现直接读 context,与这里的 c.GetInt("id") 是同一个来源。
func writeConfigUpdateAudit(c *gin.Context, result, reason string, before, after opSettings) {
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "commission.config.update",
		Result: result,
		Reason: reason,
		Before: settingsSnapshot(before),
		After:  settingsSnapshot(after),
	})
}

func editable(key string) bool {
	for _, k := range editableKeys {
		if k == key {
			return true
		}
	}
	return false
}

// presentLegacyOverridesAsPercent 把只存在于 1.x 万分比键下的运营覆盖,
// 以新的百分比键补进 overrides。
//
// 前端用 overrides 判断"这一项是否已被运营覆盖"并打标。升级之后运营还没
// 重新保存过配置时,库里只有旧键 —— 不补的话界面会显示"未覆盖",而
// rateOverride 明明正在按它计佣。这正是本项目最忌讳的"看到的与生效的不一致"。
func presentLegacyOverridesAsPercent(overrides map[string]string) {
	for _, pair := range [][2]string{
		{keyTopupRatePercent, legacyKeyTopupRateBps},
		{keyConsumeRatePercent, legacyKeyConsumeRateBps},
	} {
		newKey, legacyKey := pair[0], pair[1]
		if overrides[newKey] != "" || overrides[legacyKey] == "" {
			continue
		}
		bps, err := strconv.Atoi(strings.TrimSpace(overrides[legacyKey]))
		if err != nil || bps < 0 || bps > config.MaxRateUnits {
			// 非法的旧值不会生效(rateOverride 会丢弃它),这里也就不该
			// 谎称"已被覆盖"。
			continue
		}
		overrides[newKey] = config.FormatRatePercent(bps)
	}
}

// settingsSnapshot 把生效配置摊成对外形状:费率是百分比字符串,其余是整数。
// 接口回显与审计快照共用它,免得两处形状漂移。
func settingsSnapshot(s opSettings) map[string]any {
	return map[string]any{
		keyTopupRatePercent:   s.TopupRatePercent(),
		keyConsumeRatePercent: s.ConsumeRatePercent(),
		keyMinSettleQuota:     s.MinSettleQuota,
		keyMaxPerOrderQuota:   s.MaxPerOrderQuota,
		keyHoldingDays:        s.HoldingDays,
		keyDailyCapQuota:      s.DailyCapQuota,
		keyLargeAlertQuota:    s.LargeAlertQuota,
		keyMinInviteeAgeHour:  s.MinInviteeAgeHours,
	}
}

// jsonScalarLiteral 取出一个 JSON 标量的十进制字面量。
//
// 数字原样返回(绝不先解析成 float64 —— 10.25 会在那一步就失真),
// 字符串脱掉引号。两种写法都收是刻意的:前端发字符串最安全,但工具、
// 脚本和历史客户端习惯发数字,让它们直接 400 只会制造无谓的故障。
func jsonScalarLiteral(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if err := common.Unmarshal(raw, &out); err == nil {
			return strings.TrimSpace(out)
		}
	}
	return s
}

// groupRateView 是分组费率下发给管理端的形状:比例一律百分比。
type groupRateView struct {
	GroupName      string `json:"group_name"`
	TopupPercent   string `json:"topup_rate_percent"`
	ConsumePercent string `json:"consume_rate_percent"`
	Enabled        bool   `json:"enabled"`
	Remark         string `json:"remark"`
	OperatorId     int    `json:"operator_id"`
	UpdatedAt      int64  `json:"updated_at"`
}

func groupRateViews(rows []GroupRate) []groupRateView {
	out := make([]groupRateView, 0, len(rows))
	for _, r := range rows {
		out = append(out, groupRateView{
			GroupName:      r.GroupName,
			TopupPercent:   r.TopupPercent(),
			ConsumePercent: r.ConsumePercent(),
			Enabled:        r.Enabled,
			Remark:         r.Remark,
			OperatorId:     r.OperatorId,
			UpdatedAt:      r.UpdatedAt,
		})
	}
	return out
}

// adminPutGroupRate 新增或修改一条分组费率规则。
//
// 刻意不提供独立的"列出分组费率"接口:规则表随 GET /commission/config 一起
// 下发(group_rates 字段)。两者永远要同屏展示 —— 只看规则表看不出没配规则的
// 分组按几个点返 —— 拆成两个接口只会多一次往返和一个会各自过期的缓存键。
//
// 与全局费率一样必须写审计:分组费率同样直接决定平台要付多少钱,
// 而且它更隐蔽 —— 只影响一部分用户,不看审计根本查不出是谁改的。
func adminPutGroupRate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		GroupName      string `json:"group_name"`
		TopupPercent   string `json:"topup_rate_percent"`
		ConsumePercent string `json:"consume_rate_percent"`
		Enabled        bool   `json:"enabled"`
		Remark         string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	group := normalizeGroup(req.GroupName)
	if group == "" {
		badRequest(c, "qy_invalid_param", "必须指定分组名")
		return
	}
	if len([]rune(group)) > 64 {
		badRequest(c, "qy_invalid_param", "分组名过长")
		return
	}
	topupUnits, err := config.RatePercentUnits(req.TopupPercent)
	if err != nil {
		badRequest(c, "qy_invalid_param", "充值返佣比例"+err.Error())
		return
	}
	consumeUnits, err := config.RatePercentUnits(req.ConsumePercent)
	if err != nil {
		badRequest(c, "qy_invalid_param", "消费返佣比例"+err.Error())
		return
	}

	ctx := c.Request.Context()
	before, err := findGroupRate(ctx, group)
	if err != nil {
		internalError(c, err)
		return
	}
	row := GroupRate{
		GroupName:        group,
		TopupRateUnits:   topupUnits,
		ConsumeRateUnits: consumeUnits,
		Enabled:          req.Enabled,
		Remark:           truncate(req.Remark, 255),
		OperatorId:       c.GetInt("id"),
	}
	if err := upsertGroupRate(ctx, &row); err != nil {
		internalError(c, err)
		return
	}

	beforeSnap := ""
	if before != nil {
		b, _ := common.Marshal(groupRateViews([]GroupRate{*before})[0])
		beforeSnap = string(b)
	}
	afterSnap, _ := common.Marshal(groupRateViews([]GroupRate{row})[0])
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      "commission.group_rate.update",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      "修改分组返佣比例: " + group,
		BeforeSnap:  beforeSnap,
		AfterSnap:   string(afterSnap),
	})
	respond(c, groupRateViews([]GroupRate{row})[0])
}

// adminDeleteGroupRate 删除一条规则,该分组随即回落全局默认费率。
func adminDeleteGroupRate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	group := normalizeGroup(c.Query("group_name"))
	if group == "" {
		badRequest(c, "qy_invalid_param", "必须指定分组名")
		return
	}
	ctx := c.Request.Context()
	before, err := findGroupRate(ctx, group)
	if err != nil {
		internalError(c, err)
		return
	}
	removed, err := deleteGroupRate(ctx, group)
	if err != nil {
		internalError(c, err)
		return
	}
	if !removed {
		badRequest(c, "qy_not_found", "该分组没有单独的费率规则")
		return
	}
	beforeSnap := ""
	if before != nil {
		b, _ := common.Marshal(groupRateViews([]GroupRate{*before})[0])
		beforeSnap = string(b)
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      "commission.group_rate.delete",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      "删除分组返佣比例(回落全局默认): " + group,
		BeforeSnap:  beforeSnap,
	})
	respond(c, gin.H{"group_name": group, "deleted": true})
}

// adminListRecords 分页查询全平台计佣流水。
func adminListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Accrual{})

	if v := httpq.Int(c, "inviter_id", 0); v > 0 {
		q = q.Where("inviter_id = ?", v)
	}
	if v := httpq.Int(c, "invitee_id", 0); v > 0 {
		q = q.Where("invitee_id = ?", v)
	}
	if v := c.Query("source_type"); v != "" {
		q = q.Where("source_type = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("accrual_no"); v != "" {
		q = q.Where("accrual_no = ?", v)
	}
	if v := httpq.Int64(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := httpq.Int64(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	// 下发给前端的数组一律显式初始化,理由见 qianye/json_array_guard_test.go。
	rows := make([]Accrual, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	// 管理端返回原始行:管理员本就有权看到完整的 user_id 与订单号,
	// 脱敏只针对"邀请人看下线"这个方向。
	respond(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size})
}

// adminClawback 人工冲正。
func adminClawback(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		AccrualId       int64  `json:"accrual_id"`
		Quota           int64  `json:"quota"`
		Reason          string `json:"reason"`
		ClientRequestId string `json:"client_request_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.Reason == "" {
		// 人工改动资金必须留下理由,否则事后无法复盘。
		badRequest(c, "qy_reason_required", "必须填写冲正理由")
		return
	}
	if req.ClientRequestId == "" {
		badRequest(c, "qy_invalid_param", "缺少 client_request_id")
		return
	}
	operatorId := c.GetInt("id")
	created, err := manualClawback(c.Request.Context(), req.AccrualId, req.Quota,
		itoa(operatorId)+":"+req.ClientRequestId, req.Reason)
	if errors.Is(err, ErrClawbackIdemConflict) {
		// 同一个 client_request_id 换了参数重放。返回 409 而不是 200:
		// 回 200 就等于承认这次冲正成功了,而资金侧执行的是上一次的参数。
		respondFail(c, http.StatusConflict, "qy_idem_key_conflict",
			"该请求标识已被另一次冲正占用,请刷新后重新发起")
		return
	}
	if err != nil {
		badRequest(c, "qy_clawback_failed", err.Error())
		return
	}
	// 金额取回读行的真实 Gross,绝不用 req.Quota:后者在幂等重放与 remaining
	// 削减两种情况下都与资金侧实际发生的金额不符,而审计表是这套资金系统
	// 事后仲裁的唯一凭据。
	audit.Write(c, audit.Entry{
		TraceNo:      created.AccrualNo,
		Category:     qymodel.AuditCategoryCommission,
		Action:       "commission.clawback",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  operatorId,
		ActorName:    c.GetString("username"),
		TargetUserId: created.InviterId,
		AmountQuota:  clawbackAuditAmount(created),
		Result:       qymodel.ResultOK,
		Reason:       req.Reason,
	})
	respond(c, gin.H{"accrual_no": created.AccrualNo, "gross_amount": created.GrossAmount.String()})
}

// adminSettle 立即结算,不必等下一个周期。
func adminSettle(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	userId := httpq.Int(c, "user_id", 0)
	if userId <= 0 {
		badRequest(c, "qy_invalid_param", "必须指定 user_id")
		return
	}
	// 手动结算会把成熟的冻结佣金变成可提现余额,也就是真的动钱。
	// 定时结算走 system 身份、有租约日志;手动这一路在此之前没有任何审计 ——
	// 事后能看到"这笔佣金在某个时刻结算了",看不到"是谁按的按钮"。
	// 成功与失败都写:失败那条回答"有人在这一刻试图给某个用户结算"。
	err := settleOne(userId)
	result, reason := qymodel.ResultOK, "管理员手动触发结算"
	if err != nil {
		result, reason = qymodel.ResultFail, "管理员手动触发结算失败: "+err.Error()
	}
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryCommission,
		Action:       "commission.settle.manual",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		Result:       result,
		Reason:       reason,
	})
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"settled": true, "user_id": userId})
}

// adminBlockRelation 拉黑/解封一条邀请关系。
//
// 拉黑只停止未来计佣,不回收已发放的佣金 —— 回收要走冲正,那是另一个决定,
// 必须由人显式做出并留下理由。
func adminBlockRelation(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		InviteeId int    `json:"invitee_id"`
		Blocked   bool   `json:"blocked"`
		Reason    string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.InviteeId <= 0 {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	err := db.Get().Model(&InviteRelation{}).Where("invitee_id = ?", req.InviteeId).
		Updates(map[string]any{
			"blocked":    req.Blocked,
			"risk_flags": truncate(req.Reason, 255),
			"updated_at": common.GetTimestamp(),
		}).Error
	if err != nil {
		internalError(c, err)
		return
	}
	invalidateBlocked()
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryCommission,
		Action:       "commission.relation.block",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: req.InviteeId,
		Result:       qymodel.ResultOK,
		Reason:       req.Reason,
	})
	respond(c, gin.H{"invitee_id": req.InviteeId, "blocked": req.Blocked})
}

// adminInvalidateCache 在管理员改过 users.inviter_id 之后手动失效缓存。
func adminInvalidateCache(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	invalidateInviter(httpq.Int(c, "user_id", 0))
	invalidateSettings()
	invalidateBlocked()
	// 分组费率也必须清(D4)。漏掉它的后果最隐蔽:运营改完分组费率、
	// 按了"失效缓存"、看到成功提示,却仍要等最长 settingsCacheSeconds
	// 才真正生效 —— 这期间发出去的佣金按旧费率冻结进账本,追不回来。
	invalidateGroupRates()
	// 失效缓存本身不改任何账目,但它是"改完费率立刻让新价生效"这条动作链的
	// 最后一步 —— 排查"这笔佣金为什么按新费率算"时,需要知道这一步发生在
	// 哪个时刻、是谁做的。
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryCommission,
		Action:       "commission.cache.invalidate",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: httpq.Int(c, "user_id", 0),
		Result:       qymodel.ResultOK,
		Reason:       "手动失效返佣缓存(邀请人/运营参数/拉黑名单/分组费率)",
	})
	respond(c, gin.H{"invalidated": true})
}

// adminHealth 暴露返佣链路的关键指标。
//
// hot_queue.dropped > 0 必须告警:那是本模块唯一会造成
// "用户该拿的钱没拿到"的路径。
//
// degraded.* > 0 是第二类必须盯着的信号,性质与丢队列相反:钱照发了,但发的
// 是按默认口径算出来的钱。降级行与正常行在 qy_commission_accrual 里长得
// 一模一样,只有这个计数器能告诉运营"哪段时间的佣金要复核"。
func adminHealth(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	ctx := c.Request.Context()
	pending, _ := pendingInviters(settleInviterBatch)
	respond(c, gin.H{
		"metrics":            metricsSnapshot(),
		"hot_queue":          guard.QueueStats(),
		"inviter_cache":      inviterCacheStats(),
		"topup_low_water":    peekTopupCursor(),
		"pending_inviters":   len(pending),
		"blocked_relations":  len(blockedInvitees(ctx)),
		"effective_settings": effectiveCtx(ctx),
		"degraded": gin.H{
			"settings":   settingsDegrade.stats(),
			"group_rate": groupRateDegrade.stats(),
		},
	})
}
