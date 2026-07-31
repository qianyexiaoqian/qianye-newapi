package violation

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/shopspring/decimal"

	"github.com/gin-gonic/gin"
)

// usdPerRatioUnit 是"模型倍率 → 美元"的折算基准:倍率 1 = $0.002 / 1K token
// (common.QuotaPerUnit 的定义就是这个基准的倒数)。
//
// model_price_multiple 的语义因此是"按该模型 1K token 的价格 × 倍数罚款",
// 高价模型罚得多、便宜模型罚得少,这正是"按模型价格计费"的需求本意。
const usdPerRatioUnit = 0.002

// feeResult 是一次扣费计算的完整结果。
//
// Want 与 Charged 分两列存在:PostConsumeQuota 底层没有余额校验,
// 余额不足时"想扣多少 / 实际扣多少"必须都留痕,否则事后无法解释账目差额。
type feeResult struct {
	Mode       string
	BaseUsd    decimal.Decimal
	Multiple   decimal.Decimal
	GroupRatio decimal.Decimal
	Want       int64
	Charged    int64
	Status     string
	Err        string
	Clamp      string
	// ForceBanWeight 由 insufficient_balance_policy = ban 触发:
	// 扣不到钱就直接把这次违规当成"计数直接顶到阈值",立刻进入封号判定。
	ForceBanWeight bool
}

// computeFee 计算应扣额度。纯计算,不访问数据库,便于直接做边界测试。
func computeFee(cr *compiledRule, info *relaycommon.RelayInfo) feeResult {
	res := feeResult{Mode: cr.R.FeeMode, Status: FeeStatusNone}
	if !charges(cr.R.Action) || cr.R.FeeMode == FeeNone {
		return res
	}

	cfg := config.Get().Violation
	groupRatio := decimal.NewFromFloat(info.PriceData.GroupRatioInfo.GroupRatio)
	// 与上游 calcViolationFeeQuota 保持一致:分组倍率非正一律不扣费。
	// 这类分组通常是内部测试或赠送分组,对它们罚款没有意义且极易引发投诉。
	if !groupRatio.IsPositive() {
		return res
	}
	res.GroupRatio = groupRatio

	var base decimal.Decimal
	switch cr.R.FeeMode {
	case FeeFixed:
		base = cr.R.FeeFixed
		if !base.IsPositive() {
			base = decimalFromConfig(cfg.FixedFeeAmount)
		}
	case FeeModelPriceMultiple:
		mult := cr.R.FeeMultiple
		if !mult.IsPositive() {
			mult = decimalFromConfig(cfg.FeeMultiplier)
		}
		res.Multiple = mult
		unit := decimal.NewFromFloat(info.PriceData.ModelPrice)
		if !info.PriceData.UsePrice {
			unit = decimal.NewFromFloat(info.PriceData.ModelRatio).Mul(decimal.NewFromFloat(usdPerRatioUnit))
		}
		base = unit.Mul(mult)
	default:
		return res
	}
	if !base.IsPositive() {
		return res
	}
	res.BaseUsd = base

	// 美元 → quota,再乘分组倍率。全程 decimal,只在最后一步转整数。
	quotaDec := base.Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(groupRatio)
	q, clamp := common.QuotaFromDecimalChecked(quotaDec)
	if clamp != nil {
		// 额度饱和几乎必然意味着倍数或单价配错了,必须留痕并在管理端高亮。
		res.Clamp = mapToJSON(clamp.AuditMap())
	}
	if q <= 0 {
		return res
	}

	want := int64(q)
	// 两道上限:规则级(防单条规则配错)与全局级(防所有规则一起配错)。
	if cr.R.FeeMaxQuota > 0 && want > cr.R.FeeMaxQuota {
		want = cr.R.FeeMaxQuota
	}
	if cfg.MaxFeeQuota > 0 && want > cfg.MaxFeeQuota {
		want = cfg.MaxFeeQuota
	}
	// 跨库写主库前的硬约束:users.quota 是 int32。
	if want > int64(common.MaxQuota) {
		want = int64(common.MaxQuota)
	}
	if want <= 0 {
		return res
	}
	res.Want = want
	return res
}

// applyBalancePolicy 根据余额策略决定实际扣多少。
//
// 之所以不做 `WHERE quota >= ?` 的条件扣减:那必须绕过 service.PostConsumeQuota,
// 而它是唯一同时维护「钱包/订阅分流 + 令牌额度同步 + 余额告警」的入口。
// 绕过它造成的缓存与 DB 不一致,比小额负余额严重得多。
func applyBalancePolicy(res *feeResult, available int64) {
	if res.Want <= 0 {
		return
	}
	switch config.Get().Violation.InsufficientBalancePolicy {
	case config.InsufficientNegative:
		res.Charged = res.Want
		res.Status = FeeStatusCharged
	case config.InsufficientBan:
		if available < res.Want {
			res.Charged = 0
			res.Status = FeeStatusInsufficient
			res.ForceBanWeight = true
			return
		}
		res.Charged = res.Want
		res.Status = FeeStatusCharged
	default: // clamp
		if available <= 0 {
			res.Charged = 0
			res.Status = FeeStatusInsufficient
			return
		}
		if res.Want > available {
			res.Charged = available
			res.Status = FeeStatusTruncated
			return
		}
		res.Charged = res.Want
		res.Status = FeeStatusCharged
	}
}

// chargeFee 执行扣费并写主库计费日志。
//
// 影子模式在调用方就已经拦下,这里只处理真实扣费。任何失败都只落 fee_status,
// 绝不抛错、绝不阻断请求 —— 罚款失败不该变成用户的 500。
func chargeFee(c *gin.Context, info *relaycommon.RelayInfo, cr *compiledRule, rec *Record, res *feeResult) {
	if res.Want <= 0 {
		return
	}

	// 读的是缓存里的余额快照,与扣费不是同一个原子操作。并发违规扣费仍可能把
	// 余额扣成小额负数(上界 = 并发数 × 单笔费用),这个残留竞态已知且可接受:
	// fee_quota_want / fee_quota 两列 + 管理端负余额告警足以事后发现与纠正。
	available := int64(0)
	if q, err := model.GetUserQuota(info.UserId, false); err == nil {
		available = int64(q)
	}
	applyBalancePolicy(res, available)
	if res.Charged <= 0 {
		return
	}
	// 下界防御:PostConsumeQuota 对负数走 IncreaseUserQuota,
	// 也就是"给违规用户送钱"。这是它的符号语义陷阱,必须显式挡住。
	if res.Charged > int64(common.MaxQuota) {
		res.Charged = int64(common.MaxQuota)
	}

	if err := service.PostConsumeQuota(info, int(res.Charged), 0, true); err != nil {
		res.Status = FeeStatusFailed
		res.Err = truncate(err.Error(), 512)
		res.Charged = 0
		return
	}

	model.UpdateUserUsedQuotaAndRequestCount(info.UserId, int(res.Charged))
	if info.ChannelMeta != nil && info.ChannelId != 0 {
		model.UpdateChannelUsedQuota(info.ChannelId, int(res.Charged))
	}

	writeConsumeLog(c, info, cr, rec, res)
}

// writeConsumeLog 写一条主库计费日志,告诉用户"为什么被扣钱"。
//
// 这是需求原文"给用户写入一条计费记录说明扣费原因"的落点,也是所有前端改动
// 全部回滚后的最后防线 —— 用户在原生使用日志页就能看到扣费与原因,前端零改动。
func writeConsumeLog(c *gin.Context, info *relaycommon.RelayInfo, cr *compiledRule, rec *Record, res *feeResult) {
	reason := cr.R.PublicReason
	if reason == "" {
		reason = "内容违反使用策略"
	}
	channelId := 0
	if info.ChannelMeta != nil {
		channelId = info.ChannelId
	}

	// 字段归属遵循裁定 C34:用户应知的(单号/原因/费用)放 other 顶层,
	// 仅管理员可见的(命中词/rule_id/内部阶段/额度饱和)放 admin_info ——
	// model/log.go 的 formatUserLogs 会为普通用户删掉 admin_info。
	adminInfo := map[string]any{
		"qy_rule_id":       cr.R.Id,
		"qy_rule_name":     cr.R.Name,
		"qy_phase":         rec.Phase,
		"qy_matched_terms": rec.MatchedTerms,
	}
	if res.Clamp != "" {
		adminInfo["quota_saturation"] = res.Clamp
	}
	other := map[string]any{
		"violation_fee":       true,
		"violation_fee_code":  violationErrorCode(cr.R.Id),
		"qy_violation_rec_no": rec.RecNo,
		"qy_reason":           reason,
		"fee_quota":           res.Charged,
		"fee_quota_want":      res.Want,
		"base_amount":         res.BaseUsd.String(),
		"group_ratio":         res.GroupRatio.String(),
		"admin_info":          adminInfo,
	}

	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId:      channelId,
		ModelName:      info.OriginModelName,
		TokenName:      c.GetString("token_name"),
		Quota:          int(res.Charged),
		Content:        "违规扣费:" + reason,
		TokenId:        info.TokenId,
		UseTimeSeconds: int(common.GetTimestamp() - info.StartTime.Unix()),
		IsStream:       info.IsStream,
		Group:          info.UsingGroup,
		Other:          other,
	})
}

func violationErrorCode(ruleId int64) string {
	return fmt.Sprintf("qy_violation.%d", ruleId)
}

func decimalFromConfig(s string) decimal.Decimal {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero
	}
	return d
}

func mapToJSON(m map[string]interface{}) string {
	if len(m) == 0 {
		return ""
	}
	return truncate(common.MapToJsonStr(m), 512)
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:safeCut(s, max)]
}
