package commission

import (
	"context"
	"encoding/json" // 仅取 RawMessage 类型;编解码一律走 common.*
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// adminGetConfig 返回生效配置 + YAML 只读快照。
//
// 分成两块是刻意的:YAML 段(三个口径开关、任务节奏)涉及安全与启动行为,
// 只能改文件后重载;运营参数(费率、持有期、封顶、入账门槛)才允许在这里改。
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
		"effective": settingsSnapshot(s),
		"overrides": overrides,
		// editable_keys 决定前端渲染哪些输入框。percent_keys 里的键取值是
		// 百分比字符串;nullable_percent_keys 是其中**允许留空**的那些,空
		// 表示"没单独配,跟随充值档"。前端不猜哪个键可空 —— 猜错的方向恰好是
		// 把空当成 0 提交上来,那是一次没有人批准的费率归零。
		"editable_keys":         editableKeys,
		"percent_keys":          []string{keyTopupRatePercent, keyConsumeRatePercent, keyRedemptionRatePercent},
		"nullable_percent_keys": []string{keyRedemptionRatePercent},
		"group_rates":           groupRateViews(rules),
		"yaml_readonly": gin.H{
			"enabled":                       cm.Enabled,
			"topup_rate_bps":                cm.TopupRateBps,
			"consume_rate_bps":              cm.ConsumeRateBps,
			"redemption_rate_bps":           cm.RedemptionRateBps,
			"exclude_redemption_and_manual": cm.ExcludeRedemptionAndManual,
			"exclude_subscription_consume":  cm.ExcludeSubscriptionConsume,
			"refund_clawback":               cm.RefundClawback,
			// 一日一结算之后这一项是**心跳周期**,不再是结算周期。
			"settle_interval_seconds": cm.SettleIntervalSecs,
			"credit_interval_seconds": cm.CreditIntervalSecs,
			"day_offset_minutes":      invite.DayOffsetMinutes(),
		},
	})
}

// adminPutConfig 修改运营参数。
//
// 每一次改动都必须写审计:费率直接决定平台要付多少,
// "谁在什么时候把 3% 改成 8%"事后必须能查到人。
func adminPutConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	// 用 RawMessage 而不是 map[string]float64:费率支持两位小数,而 float64
	// 表示不了 10.25,把它解成浮点再存回去就已经不是运营填的那个数了。
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
		// JSON null 与空串都是可空键的"清空"令牌:清掉这条覆盖,让该档回落到 YAML。
		// 记成空串,写库那一步据此走 DELETE 而不是 UPSERT。
		if isNullablePercentKey(k) && (isJSONNull(raw) || strings.TrimSpace(lit) == "") {
			normalized[k] = ""
			continue
		}
		if isPercentKey(k) {
			units, err := config.RatePercentUnits(lit)
			if err != nil {
				badRequest(c, "qy_invalid_param", "计佣比例("+k+")"+err.Error())
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
		// 金额类一律夹在额度上限内。超过 MaxQuota 的门槛不是"更宽松",是"永远无法满足":
		// computeSettlement 里的 net 已被夹在 MaxQuota 内,net < minSettle 恒成立,
		// 全站所有邀请人的佣金永远不再落账 —— 不报错、不告警、没有日志。
		if isAmountKey(k) && v > int64(common.MaxQuota) {
			badRequest(c, "qy_invalid_param", "配置项 "+k+
				" 超出星屑上限("+strconv.Itoa(common.MaxQuota)+"),这个门槛永远无法被满足")
			return
		}
		if (k == keyMinSettleStardust || k == keyMinCreditStardust) && v <= 0 {
			badRequest(c, "qy_invalid_param", "配置项 "+k+" 必须大于 0")
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
	// 按键名排序后写入:失败留痕可复现;更要紧的是给并发的两次调价一个固定的
	// 加锁顺序 —— 两个管理员同时保存互相交叉的键集时,乱序写正是死锁的配方。
	keys := make([]string, 0, len(normalized))
	for k := range normalized {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 整批包进一个事务。写库过程中第二个键失败同样会留下一个谁都没有批准的
	// 中间费率组合,而且那时接口已经改过库了,却只回了个 500。
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, k := range keys {
			if normalized[k] == "" {
				if err := deleteSettingTx(tx, k); err != nil {
					return err
				}
				continue
			}
			if err := writeSetting(tx, k, normalized[k], operatorId); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// 审计必须落在事务之外,否则它会跟着一起回滚 —— 而"有人在这一刻试图
		// 改费率、失败了"正是最需要留痕的事实。after 取的是**回滚之后重新读库**的真实值。
		invalidateSettings()
		writeConfigUpdateAudit(c, qymodel.ResultFail,
			"修改佣金运营参数失败(事务已回滚): "+err.Error(), before, effectiveCtx(ctx))
		internalError(c, err)
		return
	}

	invalidateSettings()
	after := effectiveCtx(ctx)
	writeConfigUpdateAudit(c, qymodel.ResultOK, "修改佣金运营参数", before, after)
	respond(c, gin.H{"effective": settingsSnapshot(after)})
}

// writeConfigUpdateAudit 落一条运营参数变更审计,成功与失败共用。
//
// 快照走百分比视图而不是裸结构体:事后翻审计的是人,让他去把 1025 心算回
// 10.25% 就是在给自己埋坑。失败那条同样带前后快照。
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

// settingsSnapshot 把生效配置摊成对外形状:费率是百分比字符串,其余是整数。
// 接口回显与审计快照共用它,免得两处形状漂移。
func settingsSnapshot(s opSettings) map[string]any {
	return map[string]any{
		keyTopupRatePercent:   s.TopupRatePercent(),
		keyConsumeRatePercent: s.ConsumeRatePercent(),
		// 兑换码档下发**两个**值,缺一不可:
		//
		//	redemption_rate_percent            配的是什么("" = 没单独配)
		//	redemption_rate_effective_percent  实际按几个点算(没配时 = 充值档)
		//
		// 只发第一个,界面上是一个空输入框;只发第二个,输入框会被回填成充值档的
		// 数字,运营下一次保存就把"跟随"固化成了一个显式费率。
		keyRedemptionRatePercent:            s.RedemptionRatePercent(),
		"redemption_rate_effective_percent": s.EffectiveRedemptionRatePercent(),
		"redemption_rate_follows_topup":     s.RedemptionRateUnits == nil,
		keyMinSettleStardust:                s.MinSettleStardust,
		keyMaxPerOrderStardust:              s.MaxPerOrderStardust,
		keyHoldingDays:                      s.HoldingDays,
		keyMinCreditStardust:                s.MinCreditStardust,
		keyDailyCapStardust:                 s.DailyCapStardust,
		keyLargeAlertStardust:               s.LargeAlertStardust,
		keyMinInviteeAgeHour:                s.MinInviteeAgeHours,
	}
}

// isJSONNull 判断一个字段是不是显式写成了 JSON null。
//
// 必须在 jsonScalarLiteral 之前判:那个函数会把 null 原样返回成字面量
// "null",与运营真的填了 null 这四个字母分不开。
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
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
	// RedemptionPercent 是本组的兑换码档。**null = 本组没单独配**,
	// 按 redemptionRateUnits 的顺序回落(全局兑换码档 → 本组充值档)。
	RedemptionPercent *string `json:"redemption_rate_percent"`
	Enabled           bool    `json:"enabled"`
	Remark            string  `json:"remark"`
	OperatorId        int     `json:"operator_id"`
	UpdatedAt         int64   `json:"updated_at"`
}

func groupRateViews(rows []GroupRate) []groupRateView {
	out := make([]groupRateView, 0, len(rows))
	for _, r := range rows {
		out = append(out, groupRateView{
			GroupName:         r.GroupName,
			TopupPercent:      r.TopupPercent(),
			ConsumePercent:    r.ConsumePercent(),
			RedemptionPercent: r.RedemptionPercent(),
			Enabled:           r.Enabled,
			Remark:            r.Remark,
			OperatorId:        r.OperatorId,
			UpdatedAt:         r.UpdatedAt,
		})
	}
	return out
}

// adminPutGroupRate 新增或修改一条分组费率规则。
//
// 刻意不提供独立的"列出分组费率"接口:规则表随 GET /commission/config 一起
// 下发(group_rates 字段)。两者永远要同屏展示。
//
// 与全局费率一样必须写审计:分组费率同样直接决定平台要付多少,
// 而且它更隐蔽 —— 只影响一部分用户,不看审计根本查不出是谁改的。
func adminPutGroupRate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		GroupName      string `json:"group_name"`
		TopupPercent   string `json:"topup_rate_percent"`
		ConsumePercent string `json:"consume_rate_percent"`
		// RedemptionPercent 可空,而且**字段缺失与 null 是同一个意思**:本组不单独配兑换码档。
		RedemptionPercent *string `json:"redemption_rate_percent"`
		Enabled           bool    `json:"enabled"`
		Remark            string  `json:"remark"`
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
		badRequest(c, "qy_invalid_param", "充值计佣比例"+err.Error())
		return
	}
	consumeUnits, err := config.RatePercentUnits(req.ConsumePercent)
	if err != nil {
		badRequest(c, "qy_invalid_param", "消费计佣比例"+err.Error())
		return
	}
	// 兑换码档:null 与空串都表示"本组不单独配"。只有填了内容才校验并落值,
	// 而 "0" 走的是这条正常路径 —— 显式 0% 与不配是两件事。
	var redemptionUnits *int
	if req.RedemptionPercent != nil && strings.TrimSpace(*req.RedemptionPercent) != "" {
		units, err := config.RatePercentUnits(*req.RedemptionPercent)
		if err != nil {
			badRequest(c, "qy_invalid_param", "兑换码计佣比例"+err.Error())
			return
		}
		redemptionUnits = &units
	}

	ctx := c.Request.Context()
	before, err := findGroupRate(ctx, group)
	if err != nil {
		internalError(c, err)
		return
	}
	row := GroupRate{
		GroupName:           group,
		TopupRateUnits:      topupUnits,
		ConsumeRateUnits:    consumeUnits,
		RedemptionRateUnits: redemptionUnits,
		Enabled:             req.Enabled,
		Remark:              truncate(req.Remark, 255),
		OperatorId:          c.GetInt("id"),
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
		Reason:      "修改分组计佣比例: " + group,
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
		Reason:      "删除分组计佣比例(回落全局默认): " + group,
		BeforeSnap:  beforeSnap,
	})
	respond(c, gin.H{"group_name": group, "deleted": true})
}

// accrualView 是一条计佣行加上"它背后那条邀请关系此刻的开关状态"。
//
// 停止计佣(拉黑)是一个**可逆开关**(invite 的 relations/block),流水页要能
// 画出"停止 / 恢复"两个方向的按钮,就必须拿到当前状态。
type accrualView struct {
	Accrual
	// RelationBlocked = qy_invite_relation.blocked。invitee_id ≤ 0 的行
	// (手工调整)不挂在任何关系上,恒为 false。
	RelationBlocked bool `json:"relation_blocked"`
}

// adminListRecords 分页查询全平台计佣流水。
func adminListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	ctx := c.Request.Context()
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
	rows := make([]Accrual, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	// 补上"这条关系此刻是不是被停了"。本页每次只有一屏行,按这一屏的 invitee_id
	// 直查一次库,而不是读 60 秒的快照 —— 运营点完「恢复」立刻就会看这张列表。
	ids := make([]int, 0, len(rows))
	seen := make(map[int]bool, len(rows))
	for _, r := range rows {
		if r.InviteeId > 0 && !seen[r.InviteeId] {
			seen[r.InviteeId] = true
			ids = append(ids, r.InviteeId)
		}
	}
	blocked := make(map[int]bool, len(ids))
	if len(ids) > 0 {
		var blockedIds []int
		if err := db.Get().WithContext(ctx).Model(&invite.InviteRelation{}).
			Where("blocked = ? AND invitee_id IN ?", true, ids).
			Pluck("invitee_id", &blockedIds).Error; err != nil {
			internalError(c, err)
			return
		}
		for _, id := range blockedIds {
			blocked[id] = true
		}
	}
	// 管理端返回原始行:管理员本就有权看到完整的 user_id 与订单号,
	// 脱敏只针对"邀请人看下线"这个方向。
	// 下发给前端的数组一律显式初始化,理由见 qianye/json_array_guard_test.go。
	items := make([]accrualView, 0, len(rows))
	for _, r := range rows {
		items = append(items, accrualView{Accrual: r, RelationBlocked: blocked[r.InviteeId]})
	}
	respond(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// adminListCredits 分页查询全平台自动入账记录。
func adminListCredits(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().WithContext(c.Request.Context()).Model(&Credit{})
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("credit_no"); v != "" {
		q = q.Where("credit_no = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []Credit
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": creditViews(rows), "total": total, "p": page, "page_size": size})
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
	// 操作人闸门。冲正是**损害方向**的动作:一个 role=10 不该能把同级管理员甚至
	// root 的佣金冲成 0、再继续冲成负的 unsettled 把对方挂上 debt_blocked。
	// 闸门落在受益人(原单的上线)身上。
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var origin Accrual
	if err := gdb.WithContext(c.Request.Context()).Where("id = ?", req.AccrualId).Take(&origin).Error; err != nil {
		badRequest(c, "qy_clawback_failed", ErrNothingToClawback.Error())
		return
	}
	if denyActorOverTarget(c, "commission.clawback", origin.InviterId) {
		return
	}

	operatorId := c.GetInt("id")
	created, err := manualClawback(c.Request.Context(), req.AccrualId, req.Quota,
		itoa(operatorId)+":"+req.ClientRequestId, req.Reason)
	if err != nil {
		// 失败也要写审计。人工冲正是直接改钱的动作,"有人在这一刻试过、被拒了"
		// 与"成功了"同样需要留痕。
		reason := req.Reason + " / 失败: " + err.Error()
		audit.Write(c, audit.Entry{
			Category:     qymodel.AuditCategoryCommission,
			Action:       "commission.clawback",
			ActorType:    qymodel.ActorAdmin,
			ActorUserId:  operatorId,
			ActorName:    c.GetString("username"),
			TargetUserId: 0,
			AmountQuota:  req.Quota,
			Result:       qymodel.ResultFail,
			Reason:       truncate(reason, 255),
		})
		if errors.Is(err, ErrClawbackIdemConflict) {
			// 同一个 client_request_id 换了参数重放。返回 409 而不是 200:
			// 回 200 就等于承认这次冲正成功了,而资金侧执行的是上一次的参数。
			respondFail(c, http.StatusConflict, "qy_idem_key_conflict",
				"该请求标识已被另一次冲正占用,请刷新后重新发起")
			return
		}
		badRequest(c, "qy_clawback_failed", err.Error())
		return
	}
	// 金额取回读行的真实 Gross,绝不用 req.Quota:后者在幂等重放与 remaining
	// 削减两种情况下都与资金侧实际发生的金额不符。
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

// adminRerunDailySettle 让今天这一轮结算重新开跑。
//
// 它不结算任何人,只把 qy_commission_settle_run 里今天那一行改回"还要再跑",
// 真正的排空交给下一次心跳(见 rearmDailyRun 的注释)。
func adminRerunDailySettle(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	now := common.GetTimestamp()
	day := dayKey(now)
	rearmed, err := rearmDailyRun(day, now)
	result := qymodel.ResultOK
	reason := "重新排期今天(" + day + ")的结算运行"
	if err != nil {
		result = qymodel.ResultFail
		reason += " / 失败: " + err.Error()
	} else if !rearmed {
		reason += " / 今天还没有运行记录,下一次心跳本就会跑"
	}
	// 这一条不动钱,但它决定"当天剩下那批人今天还能不能拿到钱",
	// 而且只有在出过故障的那一天才会被按下 —— 事后复盘要能看见是谁按的。
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryCommission,
		Action:      "commission.settle.rerun",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      truncate(reason, 255),
	})
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"run_date": day, "rearmed": rearmed})
}

// adminSettle 立即结算,不必等下一个周期。
//
// user_id 查询串与 JSON 请求体都收:紧邻的同类写接口 POST /commission/balances/adjust
// 从请求体读,两个接口摆在一起而参数位置相反会让调用方拿到一句与
// "我明明传了 user_id" 直接冲突的提示。
func adminSettle(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	userId := httpq.Int(c, "user_id", 0)
	if userId <= 0 {
		var req struct {
			UserId int `json:"user_id"`
		}
		if err := c.ShouldBindJSON(&req); err == nil {
			userId = req.UserId
		}
	}
	if userId <= 0 {
		badRequest(c, "qy_invalid_param", "必须指定 user_id(查询串 ?user_id= 或请求体 {\"user_id\":…})")
		return
	}
	// 手动结算把成熟的计佣提前变成可用余额,随即会被自动入账。它不凭空造钱,
	// 但它能让操作人**绕过持有期**先拿到自己的那一份 —— 持有期存在的理由正是
	// 「下线退款 / 冲正还来得及追回」,自己给自己解冻等于单方面取消这段追回窗口。
	if denyActorOverTarget(c, "commission.settle.manual", userId) {
		return
	}
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

// adminHealth 暴露佣金链路的关键指标。
//
// hot_queue.dropped > 0 必须告警:那是本模块唯一会造成"用户该拿的钱没拿到"的路径。
//
// degraded.* > 0 是第二类必须盯着的信号:钱照发了,但发的是按默认口径算出来的钱。
// ledger_check 是第三类,也是最安静的一类:账本自己跟自己对不上。
// credit.held > 0 是第四类:有一笔钱既没到账也没退回,等着人去对账台裁决。
func adminHealth(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	ctx := c.Request.Context()
	pending, _ := pendingInviters(settleInviterBatch)
	respond(c, gin.H{
		"metrics":       metricsSnapshot(),
		"hot_queue":     guard.QueueStats(),
		"inviter_cache": invite.InviterCacheStats(),
		// cache_sync 是进程内缓存的跨节点失效通道(invite 代为广播)。enabled=false 或
		// failed 持续增长,意味着别的节点可能仍在按旧费率计佣。
		"cache_sync": invite.CacheSyncStats(),
		// daily_settle 是一日一结算之后**唯一**能回答"今天的佣金结了没有"的一段。
		"daily_settle": dailySettleSnapshot(common.GetTimestamp()),
		"credit":       creditSnapshot(ctx),
		// rate_overlap 是 D-16 之后**必须**摆在运营面前的一件事,见下面的说明。
		"rate_overlap":    rateOverlap(ctx),
		"topup_low_water": peekTopupCursor(),
		// pending_inviters 只取第一页,封顶 settleInviterBatch,不是队列全长。
		"pending_inviters":   len(pending),
		"effective_settings": effectiveCtx(ctx),
		"ledger_check":       ledgerCheck(ctx),
		"degraded": gin.H{
			"settings":   settingsDegrade.stats(),
			"group_rate": groupRateDegrade.stats(),
			// inviter_group 响意味着主库有问题,不是扩展库 —— 处置不同,所以分开报。
			"inviter_group": inviterGroupDegrade.stats(),
		},
	})
}

// rateOverlap 报告"同一笔基数正在被两条线同时计返"。
//
// # 为什么这是一个健康检查项而不是一条校验
//
// D-16 之后佣金与星屑侧的邀请返发的是**同一种钱**,而两边的三档打的是同一笔基数:
//
//	commission.topup_rate_bps       ↔ stardust.invite_topup_bps      (下线充值)
//	commission.consume_rate_bps     ↔ stardust.invite_consume_bps    (下线消费)
//	commission.redemption_rate_bps  ↔ stardust.invite_redeem_bps     (下线用兑换码)
//
// 两边同时为正,同一笔下线消费就会给上线落两笔星屑:一笔由星屑侧日结当场发,一笔
// 由佣金侧计佣、过持有期、攒够门槛再发。这**不是** bug —— 运营完全可能故意让"即时
// 小额 + 延迟大额"两条并存。所以代码不拒绝、不清零、不替谁做决定。
//
// 但它必须**看得见**。两条线的配置在两个不同的管理页上,各自都显示正常,谁也不会
// 主动去做这次比对;而症状是"平台付出的钱是账面比例的两倍",在成本报表上要几周
// 才浮出来。所以健康面板逐档报出重叠,由人来定留哪一条。
func rateOverlap(ctx context.Context) gin.H {
	cs := effectiveCtx(ctx)
	topupBps, redeemBps, consumeBps, _ := stardust.InviteRateSnapshot()

	rows := []struct {
		key        string
		commission int
		invite     int
	}{
		{"topup", cs.TopupRateUnits, topupBps},
		{"consume", cs.ConsumeRateUnits, consumeBps},
		{"redemption", cs.EffectiveRedemptionRateUnits(), redeemBps},
	}
	overlapping := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		if r.commission > 0 && r.invite > 0 {
			overlapping = append(overlapping, gin.H{
				"source":              r.key,
				"commission_rate_bps": r.commission,
				"invite_rate_bps":     r.invite,
			})
		}
	}
	return gin.H{"ok": len(overlapping) == 0, "sources": overlapping}
}

// maxLedgerCheckUsers 是一次体检最多核对多少行余额。
//
// 超过就只报"这次没查全",而不是拖着体检接口跑一张全表 join —— 体检本身把
// 站点拖慢,是最没道理的一种故障。
const maxLedgerCheckUsers = 20000

// ledgerCheck 是佣金账本的自洽性体检。
//
// 本模块有三条恒等式,它们都不会自己喊疼;这里体检的是**前两条**:
//
//	I1  Σ计佣行.settled_amount(status≠voided) == Σ结算单(granted−reclaimed) + 未结算余数
//	I2  可用 + 已入账 == 累计earned − 累计clawback
//	I3  base_quota × rate_bps / 10000 / quota_per_unit == gross_amount + capped_amount(逐行)
//
// I3 不在这里:它不是一条全表恒等式,只有 source_type ∈ {consume, topup, redemption}
// 的正额计佣行满足它(逐条理由写在 accrual.go 的 capGross 上),无限定地全表算
// 报出来的"漂移行数"绝大多数会是 manual / clawback 这两类完全正常的行。
//
// I1 跨 qy_commission_accrual、qy_commission_settlement、qy_commission_balance 三张表,
// 任何单表视图都答不了;这里报"哪几行对不上、差多少、最坏的是谁"。
//
// self_invited_users 顺带体检一条数据异常:users.inviter_id == users.id。
// 读失败不让整个健康面板 500:体检项缺失时明说 ok=false。
func ledgerCheck(ctx context.Context) gin.H {
	out := gin.H{"ok": false, "checked_users": 0}
	gdb := db.Get()
	if gdb == nil {
		out["error"] = db.ErrNotReady.Error()
		return out
	}

	var balances []Balance
	if err := gdb.WithContext(ctx).Limit(maxLedgerCheckUsers + 1).Find(&balances).Error; err != nil {
		db.MarkFailure(err)
		out["error"] = err.Error()
		return out
	}
	if len(balances) > maxLedgerCheckUsers {
		out["error"] = "余额行数超过一次体检的上界 " + strconv.Itoa(maxLedgerCheckUsers) +
			",本次未核对;需要把体检改成离线任务"
		return out
	}

	var accrued []struct {
		InviterId int
		Settled   string
	}
	if err := gdb.WithContext(ctx).Model(&Accrual{}).
		Select("inviter_id, COALESCE(SUM(settled_amount), 0) AS settled").
		Where("status <> ?", StatusVoided).Group("inviter_id").Scan(&accrued).Error; err != nil {
		db.MarkFailure(err)
		out["error"] = err.Error()
		return out
	}
	settledByUser := make(map[int]decimal.Decimal, len(accrued))
	for _, r := range accrued {
		d, err := decimal.NewFromString(r.Settled)
		if err != nil {
			continue
		}
		settledByUser[r.InviterId] = d
	}

	var granted []struct {
		UserId int
		Net    int64
	}
	if err := gdb.WithContext(ctx).Model(&Settlement{}).
		Select("user_id, COALESCE(SUM(granted - reclaimed), 0) AS net").
		Group("user_id").Scan(&granted).Error; err != nil {
		db.MarkFailure(err)
		out["error"] = err.Error()
		return out
	}
	grantedByUser := make(map[int]int64, len(granted))
	for _, r := range granted {
		grantedByUser[r.UserId] = r.Net
	}

	settleDrifted, balanceDrifted := 0, 0
	worstUser := 0
	worstDrift := decimal.Zero
	totalDrift := decimal.Zero
	for _, b := range balances {
		// I1:计佣行结算掉的钱,必须等于结算单发出去的钱加上还没凑够一个额度的余数。
		drift := settledByUser[b.UserId].
			Sub(decimal.NewFromInt(grantedByUser[b.UserId])).
			Sub(b.UnsettledAmount)
		if !drift.IsZero() {
			settleDrifted++
			totalDrift = totalDrift.Add(drift)
			if drift.Abs().GreaterThan(worstDrift.Abs()) {
				worstDrift, worstUser = drift, b.UserId
			}
		}
		// I2:与余额页那一列同一条恒等式,在这里做成全站计数。
		if b.Available != b.TotalEarned-b.TotalClawback-b.Credited {
			balanceDrifted++
		}
	}

	selfInvited := 0
	if model.DB != nil {
		var n int64
		// 列与列比较,三种数据库写法一致;两个列名都不是保留字,不需要方言引号。
		if err := model.DB.WithContext(ctx).Model(&model.User{}).
			Where("inviter_id = id").Count(&n).Error; err == nil {
			selfInvited = int(n)
		}
	}

	out["ok"] = true
	out["checked_users"] = len(balances)
	out["settle_drifted_users"] = settleDrifted
	out["settle_drift_total"] = totalDrift.String()
	out["settle_drift_worst_user_id"] = worstUser
	out["settle_drift_worst"] = worstDrift.String()
	out["balance_drifted_users"] = balanceDrifted
	out["self_invited_users"] = selfInvited
	return out
}

// requireReason 校验并规范化人工动钱接口的事由:至少 4 个字符。
// 没有事由的改账事后无法与误操作区分。
func requireReason(c *gin.Context, raw string) (string, bool) {
	reason := strings.TrimSpace(raw)
	if len([]rune(reason)) < 4 {
		badRequest(c, "qy_reason_required", "必须填写事由(至少 4 个字符)")
		return "", false
	}
	return reason, true
}

// displayName 给管理端一个可读的账号名:用户名为空时回落邮箱。
func displayName(u model.User) string {
	if u.Username != "" {
		return u.Username
	}
	return u.Email
}

// pairCommissionQuotas 按 (上线, 下线) 汇总上线从这个下线身上已经挣到的佣金额度。
//
// 「佣金总表」用它回答"把这条关系换掉或解掉之后,会留在原邀请人名下的那笔钱"。
// 读扩展库失败返回空表:这一列只是确认框上的提示。
func pairCommissionQuotas(ctx context.Context, pairs [][2]int) map[[2]int]int64 {
	out := make(map[[2]int]int64, len(pairs))
	if len(pairs) == 0 {
		return out
	}
	gdb := db.Get()
	if gdb == nil {
		return out
	}
	wanted := make(map[[2]int]bool, len(pairs))
	invitees := make([]int, 0, len(pairs))
	seen := make(map[int]bool, len(pairs))
	for _, p := range pairs {
		wanted[p] = true
		if seen[p[1]] {
			continue
		}
		seen[p[1]] = true
		invitees = append(invitees, p[1])
	}

	var rows []struct {
		InviterId int
		InviteeId int
		Gross     string
	}
	if err := gdb.WithContext(ctx).Model(&Accrual{}).
		Select("inviter_id, invitee_id, COALESCE(SUM(gross_amount), 0) AS gross").
		Where("invitee_id IN ? AND status <> ?", invitees, StatusVoided).
		Group("inviter_id, invitee_id").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		return out
	}
	for _, r := range rows {
		key := [2]int{r.InviterId, r.InviteeId}
		if !wanted[key] {
			continue
		}
		d, err := decimal.NewFromString(r.Gross)
		if err != nil {
			continue
		}
		out[key] = int64(common.QuotaFromDecimal(d.Floor()))
	}
	return out
}
