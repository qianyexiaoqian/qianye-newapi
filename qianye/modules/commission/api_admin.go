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
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
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
	redactSecretOverrides(overrides)
	s := effectiveCtx(ctx)
	cm := config.Get().Commission
	rules, err := listGroupRates(ctx)
	if err != nil {
		internalError(c, err)
		return
	}
	fiatRules, err := listFiatRates(ctx)
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
		// fiat_rate_keys 是取值为**法币折算比例**(一个乘数,不是百分比)的键。
		// 前端据此渲染一个不带 "%" 的输入框 —— 把它画成百分比框,运营填 7.3
		// 就会以为自己配的是 7.3%,而实际生效的是"一美元折 7.3 元"。
		"fiat_rate_keys": []string{keyFiatRateDefault},
		"group_rates":    groupRateViews(rules),
		"fiat_rates":     fiatRateViews(fiatRules, s),
		"yaml_readonly": gin.H{
			"enabled":                       cm.Enabled,
			"topup_rate_percent":            cm.TopupRatePercent,
			"consume_rate_percent":          cm.ConsumeRatePercent,
			"redemption_rate_percent":       cm.RedemptionRatePercent,
			"exclude_redemption_and_manual": cm.ExcludeRedemptionAndManual,
			"exclude_subscription_consume":  cm.ExcludeSubscriptionConsume,
			"refund_clawback":               cm.RefundClawback,
			// 一日一结算之后这一项是**心跳周期**,不再是结算周期。
			// 前端的只读展示必须跟着改口径,否则运营会照着它去推算
			// "我改小一点用户就能早点拿到钱"—— 那件事现在由 day_offset_minutes
			// 与 holding_days 决定。
			"settle_interval_seconds":     cm.SettleIntervalSecs,
			"day_offset_minutes":          cm.DayOffsetMinutes,
			"topup_scan_interval_seconds": cm.TopupScanIntervalSec,
			"topup_scan_lookback_hours":   cm.TopupScanLookbackHours,
			"inviter_cache_seconds":       cm.InviterCacheSecs,
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
		// JSON null 是**全表统一**的"清空"令牌。
		//
		// 在此之前两个可空字段的清空约定正好相反:redemption_rate_percent 只认
		// 空串(发 null 会掉进下面的百分比分支,报「不是合法数值: "null"」——
		// 它把 JSON null 当成了 n-u-l-l 四个字母的字符串,是未处理而不是刻意
		// 拒绝),fiat_rate_default 只认 null(空串按下面那段注释是刻意拒绝的)。
		// 于是**没有任何一种统一写法能在一个报文里同时清空两档**,任何非本仓
		// 前端的调用方都得为这两个键各写一套序列化规则。
		//
		// 修法是把 null 补成两边都认,而不是把 fiat 的空串放开 —— 后者会让
		// "运营把输入框清空又按了保存"静默改掉全站佣金折算口径。
		if isNullablePercentKey(k) && (isJSONNull(raw) || strings.TrimSpace(lit) == "") {
			// 空 = 清掉这条覆盖,让该档回落到 YAML(YAML 也没写就是"跟随充值档")。
			// 记成空串,写库那一步据此走 DELETE 而不是 UPSERT —— 留一行 v=''
			// 在 qy_settings 里同样能被读成"没配",但它会让运营在库里看到一条
			// 空值行,而"这一行到底算不算配过"是没人愿意再猜一遍的问题。
			normalized[k] = ""
			continue
		}
		if isFiatRateKey(k) {
			// 兜底档单独一支:它是一个乘数(7.3 = 一美元折 7.3 元),
			// 不是 0..100 的百分比,也不是额度。
			//
			// 清空这一档要用 JSON null,**不能**用空串,两者在这里语义相反:
			//
			//   - ""   → 400。空串只可能来自"运营把输入框清空了又按了保存",
			//     而这一档的零值/空值全都是资损形状(见 parseFiatRate),
			//     把它当成一次删除等于让一次误操作静默改掉全站佣金折算口径。
			//   - null → 删掉这条覆盖,回落到第三层(全站充值汇率)。
			//
			// 这一支不能省。fiatRateFor 声明的第三层是"全站充值汇率",而在它
			// 出现之前,兜底档一旦配过就再也回不去 —— 手填一个与当前
			// USDExchangeRate 相同的数字只是数值上的巧合:充值汇率此后再改,
			// 佣金折算不会跟着走,而界面上仍然显示 layer=default。产品自己
			// 定义的一层永久不可达,只能进库里 DELETE,运营没有这条路。
			if isJSONNull(raw) {
				normalized[k] = ""
				continue
			}
			rate, err := parseFiatRate(lit)
			if err != nil {
				badRequest(c, "qy_invalid_param", "法币折算兜底比例"+err.Error())
				return
			}
			// 存规范化后的形状("7.30" → "7.3"),否则前后对比与审计快照
			// 会出现一堆没有任何人改过的假差异。
			normalized[k] = rate.String()
			continue
		}
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
		// 金额类一律夹在主库额度上限内,与 transfer 的 settingBounds 同口径。
		//
		// 没有上界时最坏的一个具体形状:把「最小结算额度」设成 5000000000
		// (按 USD 录入之后只要敲 5 位数),而 computeSettlement 里的 net 经
		// common.QuotaFromDecimalChecked 已经被夹在 int32 内 —— 于是
		// net < minSettle 恒成立,net 恒为 0,**全站所有邀请人的佣金永远不再
		// 落账**:不报错、不告警、没有日志,未结算额一路累加。
		// 超过 MaxQuota 的门槛不是"更宽松",是"永远无法满足"。
		if isQuotaKey(k) && v > int64(common.MaxQuota) {
			badRequest(c, "qy_invalid_param", "配置项 "+k+
				" 超出主库额度上限("+strconv.Itoa(common.MaxQuota)+"),这个门槛永远无法被满足")
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
			// 空值只可能来自两处:可空百分比键收到空串,或法币兜底档收到
			// 显式 null(其余键的校验早已 400)。两者含义相同 ——
			// "取消这条覆盖"。删除必须跟其余键在同一个事务里:把兑换码档改回
			// 「跟随充值档」和调整充值档本身常常是同一次操作,一半生效的组合
			// 是谁都没有批准过的。
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

// secretOverrideKeys 是**绝不下发**的运营覆盖键。
//
// invitee_ref_salt 是 inviteeRef 的 HMAC 密钥,settings.go 明写它「部署一次、
// 永不轮换」(轮换会让全部历史 ref 失效)。它不在 editableKeys 里,写这一侧
// 早就关死了,但读这一侧此前把 qy_settings 里的 commission 覆盖**整个** map
// 原样回显 —— 于是一个永不可轮换的密钥出现在一个会被截图、被前端缓存、
// 被日志代理与错误上报记录的 JSON 里。
//
// 白名单式地删,而不是"以后记得别加":新增任何 scope=commission 的密钥类配置
// 只要登记进这里就自动不下发。
var secretOverrideKeys = []string{keyRefSalt}

// redactSecretOverrides 就地摘掉密钥类覆盖。
//
// 刻意是"删掉"而不是"替换成 ***":前端只按 editableKeys 渲染输入框,一个
// 掩码值除了给人一种"这里有个可以改的东西"的错觉之外没有任何用处,而它一旦
// 被原样提交回来就是把密钥写成八个星号。
func redactSecretOverrides(overrides map[string]string) {
	for _, k := range secretOverrideKeys {
		delete(overrides, k)
	}
}

// settingsSnapshot 把生效配置摊成对外形状:费率是百分比字符串,其余是整数。
// 接口回显与审计快照共用它,免得两处形状漂移。
func settingsSnapshot(s opSettings) map[string]any {
	// 没配分组档的人走哪一层、按几折算。传 nil 表示"分组层没有取值",
	// 与 fiatRateFor 在真实计佣路径上的判定是同一个函数,不另写一份。
	fallback := fiatRateFor(nil, s)
	return map[string]any{
		keyTopupRatePercent:   s.TopupRatePercent(),
		keyConsumeRatePercent: s.ConsumeRatePercent(),
		// 兑换码档下发**两个**值,缺一不可:
		//
		//	redemption_rate_percent            配的是什么("" = 没单独配)
		//	redemption_rate_effective_percent  实际按几个点算(没配时 = 充值档)
		//
		// 只发第一个,界面上是一个空输入框,运营看不出兑换码到底返几个点;
		// 只发第二个,输入框会被回填成充值档的数字,运营下一次保存就把
		// "跟随"固化成了一个显式费率,从此改充值档不再带动兑换码 —— 一次
		// 什么都没改的保存,静默改变了系统行为。
		keyRedemptionRatePercent:            s.RedemptionRatePercent(),
		"redemption_rate_effective_percent": s.EffectiveRedemptionRatePercent(),
		"redemption_rate_follows_topup":     s.RedemptionRateUnits == nil,
		// 法币折算的兜底档同样下发**三个**值,理由与兑换码那一档同构:
		//
		//	fiat_rate_default            配的是什么("" = 从未配过)
		//	fiat_rate_effective          没配分组档的人实际按几折算
		//	fiat_rate_effective_layer    上面那个数来自哪一层(default / global / none)
		//
		// 只发第一个,界面上是个空输入框,运营看不出佣金到底按什么比例折;
		// 只发第二个,输入框会被回填成全站充值汇率,运营下一次保存就把"跟随
		// 全站汇率"固化成了一个显式数字 —— 一次什么都没改的保存,静默切断了
		// 佣金折算与充值汇率的联动。
		keyFiatRateDefault:          s.FiatRateDefaultString(),
		"fiat_rate_effective":       fallback.Rate.String(),
		"fiat_rate_effective_layer": fallback.Layer,
		// 全站充值汇率(第 3 层)原样下发。审计快照要靠它解释"当时没配兜底档,
		// 于是按这个数折算" —— 它不是本模块的配置,却决定本模块的钱。
		"fiat_rate_global":   currentUsdRate().String(),
		keyMinSettleQuota:    s.MinSettleQuota,
		keyMaxPerOrderQuota:  s.MaxPerOrderQuota,
		keyHoldingDays:       s.HoldingDays,
		keyDailyCapQuota:     s.DailyCapQuota,
		keyLargeAlertQuota:   s.LargeAlertQuota,
		keyMinInviteeAgeHour: s.MinInviteeAgeHours,
	}
}

// jsonScalarLiteral 取出一个 JSON 标量的十进制字面量。
//
// 数字原样返回(绝不先解析成 float64 —— 10.25 会在那一步就失真),
// 字符串脱掉引号。两种写法都收是刻意的:前端发字符串最安全,但工具、
// 脚本和历史客户端习惯发数字,让它们直接 400 只会制造无谓的故障。
// isJSONNull 判断一个字段是不是显式写成了 JSON null。
//
// 必须在 jsonScalarLiteral 之前判:那个函数会把 null 原样返回成字面量
// "null",与运营真的填了 null 这四个字母分不开。
//
// 存在的理由是"清空"和"填错了"必须是两个不同的动作:对法币折算兜底档
// 来说,空串是误操作(400),null 才是"我要回落到全站充值汇率"。
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

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
	// 用指针而不是空串:JS 里 "" 与 "0" 都是假值,只有 null 不会被
	// `value ? … : 跟随` 这类写法把显式 0% 也画成"跟随"。
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
		// RedemptionPercent 可空,而且**字段缺失与 null 是同一个意思**:
		// 本组不单独配兑换码档。这条兼容性是刻意的 —— 本接口是整行 upsert,
		// 老版本前端(以及运维手里的脚本)发的报文里根本没有这个字段,
		// 缺失被读成"不配"正好让它们保持升级前的行为。
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
		badRequest(c, "qy_invalid_param", "充值返佣比例"+err.Error())
		return
	}
	consumeUnits, err := config.RatePercentUnits(req.ConsumePercent)
	if err != nil {
		badRequest(c, "qy_invalid_param", "消费返佣比例"+err.Error())
		return
	}
	// 兑换码档:null 与空串都表示"本组不单独配"。只有填了内容才校验并落值,
	// 而 "0" 走的是这条正常路径 —— 显式 0% 与不配是两件事,这一行是它们唯一
	// 的分岔点。
	var redemptionUnits *int
	if req.RedemptionPercent != nil && strings.TrimSpace(*req.RedemptionPercent) != "" {
		units, err := config.RatePercentUnits(*req.RedemptionPercent)
		if err != nil {
			badRequest(c, "qy_invalid_param", "兑换码返佣比例"+err.Error())
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

// fiatRateView 是一条分组法币比例下发给管理端的形状。
type fiatRateView struct {
	GroupName string `json:"group_name"`
	// Rate 是十进制字符串。与费率同一条理由:JSON number 到了 JS 那一侧就是
	// 二进制浮点,7.25 有可能回填成 7.249999999999999,运营再点一次保存就把
	// 这个数存进了资金配置。
	Rate string `json:"rate"`
	// EffectiveRate / EffectiveLayer 是这个分组**实际**按几折算、走的哪一层。
	// 规则被禁用(或比例非法)时它们指向兜底档/全站汇率 —— 界面必须能一眼
	// 看出"这一行现在其实没在生效",否则关掉一条规则和删掉它长得一模一样。
	EffectiveRate  string `json:"effective_rate"`
	EffectiveLayer string `json:"effective_layer"`
	Enabled        bool   `json:"enabled"`
	Remark         string `json:"remark"`
	OperatorId     int    `json:"operator_id"`
	UpdatedAt      int64  `json:"updated_at"`
}

func fiatRateViews(rows []FiatRate, s opSettings) []fiatRateView {
	out := make([]fiatRateView, 0, len(rows))
	for i := range rows {
		r := rows[i]
		d := fiatRateFor(&r, s)
		out = append(out, fiatRateView{
			GroupName:      r.GroupName,
			Rate:           r.Rate.String(),
			EffectiveRate:  d.Rate.String(),
			EffectiveLayer: d.Layer,
			Enabled:        r.Enabled,
			Remark:         r.Remark,
			OperatorId:     r.OperatorId,
			UpdatedAt:      r.UpdatedAt,
		})
	}
	return out
}

// adminPutFiatRate 新增或修改一条分组法币折算比例。
//
// 与分组费率一样必须写审计:它直接决定平台按什么价把佣金结给推广者,
// 而且更隐蔽 —— 它不改变任何一个额度数字,只改变那些额度值多少钱。
//
// 改动**只对此后的计佣与结算生效**:比例在计佣当刻冻结进
// qy_commission_accrual.usd_rate,已经算进 available_fiat 的是按当时比例
// 算出的绝对值,不会被重算。这句话同时写在界面上。
func adminPutFiatRate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		GroupName string `json:"group_name"`
		Rate      string `json:"rate"`
		Enabled   bool   `json:"enabled"`
		Remark    string `json:"remark"`
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
	rate, err := parseFiatRate(req.Rate)
	if err != nil {
		badRequest(c, "qy_invalid_param", "法币折算比例"+err.Error())
		return
	}

	ctx := c.Request.Context()
	before, err := findFiatRate(ctx, group)
	if err != nil {
		internalError(c, err)
		return
	}
	row := FiatRate{
		GroupName:  group,
		Rate:       rate,
		Enabled:    req.Enabled,
		Remark:     truncate(req.Remark, 255),
		OperatorId: c.GetInt("id"),
	}
	if err := upsertFiatRate(ctx, &row); err != nil {
		internalError(c, err)
		return
	}
	s := effectiveCtx(ctx)
	writeFiatRateAudit(c, "commission.fiat_rate.update",
		"修改分组法币折算比例(只对此后的计佣与结算生效): "+group, before, &row, s)
	respond(c, fiatRateViews([]FiatRate{row}, s)[0])
}

// adminDeleteFiatRate 删除一条分组档,该分组随即回落兜底档。
//
// 只有分组档可删。兜底档是 qy_settings 的一项配置,写入侧拒绝空值 ——
// 删掉兜底档会让一批没配分组档的用户悄悄退回充值页汇率,而界面上还写着
// 兜底档,这正是本仓在 default 用户分组与违规兜底类型上栽过两次的坑。
func adminDeleteFiatRate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	group := normalizeGroup(c.Query("group_name"))
	if group == "" {
		badRequest(c, "qy_invalid_param", "必须指定分组名")
		return
	}
	ctx := c.Request.Context()
	before, err := findFiatRate(ctx, group)
	if err != nil {
		internalError(c, err)
		return
	}
	removed, err := deleteFiatRate(ctx, group)
	if err != nil {
		internalError(c, err)
		return
	}
	if !removed {
		badRequest(c, "qy_not_found", "该分组没有单独的法币折算比例")
		return
	}
	writeFiatRateAudit(c, "commission.fiat_rate.delete",
		"删除分组法币折算比例(回落兜底档): "+group, before, nil, effectiveCtx(ctx))
	respond(c, gin.H{"group_name": group, "deleted": true})
}

// writeFiatRateAudit 落一条分组法币比例变更审计,写入与删除共用。
//
// 快照里带上**生效后的比例与层级**而不只是这一行的原始值:事后翻审计的人
// 要回答的是"这一改之后那批用户按几折算",而在规则被禁用、比例非法时,
// 行里的数字与实际生效的数字根本不是同一个。
func writeFiatRateAudit(c *gin.Context, action, reason string, before, after *FiatRate, s opSettings) {
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      reason,
		BeforeSnap:  fiatRateSnapshot(before, s),
		AfterSnap:   fiatRateSnapshot(after, s),
	})
}

// fiatRateSnapshot 是一条分组法币比例的审计快照,nil 渲染成空串(该行不存在)。
func fiatRateSnapshot(row *FiatRate, s opSettings) string {
	if row == nil {
		return ""
	}
	b, _ := common.Marshal(fiatRateViews([]FiatRate{*row}, s)[0])
	return string(b)
}

// accrualView 是一条计佣行加上"它背后那条邀请关系此刻的开关状态"。
//
// 为什么要多这一位:停止计佣(拉黑)是一个**可逆开关**,后端从一开始就收
// `blocked bool`、同一个接口既停又恢复。但计佣流水页此前拿不到当前状态,
// 只能画一个单向的「停止计佣」按钮 —— 于是"停了就再也恢复不了"变成了
// 运营眼里的事实,进而变成"这套审核是不是多余、不如直接解绑关系"的结论。
// 状态不下发,恢复入口就无法存在;这一位是那个入口的前提。
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
	// 补上"这条关系此刻是不是被停了"。
	//
	// 刻意**不**走 blockedInvitees():那份快照缓存 60 秒,而运营点完「恢复计佣」
	// 立刻就会看这张列表 —— 读缓存会让按钮在一整个 TTL 里显示成没生效,
	// 运营于是再点一次。本页每次只有一屏行,按这一屏的 invitee_id 直查一次库。
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
		if err := db.Get().WithContext(ctx).Model(&InviteRelation{}).
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
	// 操作人闸门。冲正是**损害方向**的动作，上一轮补判据时只覆盖了增益方向
	// （手工增减、已提现迁移、邀请关系绑定/换绑、手动结算），于是一个 role=10
	// 可以把同级管理员甚至 root 的佣金冲成 0、再继续冲成负的 unsettled 把对方
	// 挂上 debt_blocked（此后对方所有新佣金先被欠账吸收、提现被冻结），而受害者
	// 唯一的恢复入口 balances/adjust 恰恰是**接了**判据的，他自己救不回来。
	// 闸门落在受益人（原单的上线）身上，与 relations/bind 落在 inviter_id 上同源。
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
		// 与"成功了"同样需要留痕 —— 紧邻的 adminSettle 就是成功失败同一出口,
		// 这里原本两条失败分支一条审计都不写,事后完全查不到。
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
// user_id 查询串与 JSON 请求体都收。只收查询串是不行的:紧邻的同类写接口
// POST /commission/balances/adjust 从请求体读,两个接口摆在一起而参数位置
// 相反,调用方发 {"user_id":123} 拿到的是 "必须指定 user_id" —— 一句与
// "我明明传了 user_id" 直接冲突的提示,而且这一支在 badRequest 之前返回,
// 连一条失败审计都不写,事后完全查不到有人试过。
func adminSettle(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	userId := httpq.Int(c, "user_id", 0)
	if userId <= 0 {
		// 请求体是可选的:查询串给够了就不必带 body,报文坏了也不该盖掉
		// 查询串那一路 —— 这里只在查询串没给出 user_id 时才去看它。
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
	// 手动结算把冻结佣金提前变成可提现余额。它不凭空造钱,但它能让操作人
	// **绕过成熟期**先拿到自己的那一份 —— 冻结期存在的理由正是「下线退款/
	// 冲正还来得及追回」,自己给自己解冻等于单方面取消这段追回窗口。
	if denyActorOverTarget(c, "commission.settle.manual", userId) {
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
//
// # 这个处理器修过两个缺陷,两个都要在这里说清楚
//
// ## 一、"参数格式错" 与 "这条记录压根没有邀请关系" 被混成同一个 400
//
// 旧代码写的是 `if err := ...; err != nil || req.InviteeId <= 0`,两种完全
// 不同的失败共用 `qy_invalid_param` + "请求格式错误"。而实际撞上这个 400 的
// 是**手工调整产生的计佣行**:它们 invitee_id = 0(见 api_admin_adjust.go ——
// 这笔钱不是从谁的消费里分出来的,不挂在任何下线上),运营在佣金流水页对着
// 这样一行点「拉黑」,拿到的提示是"请求参数有误",于是他会怀疑自己填错了、
// 再点一次,或者更糟 —— 怀疑刚才那次是不是已经扣了钱。
//
// 现在分成两个 code:`qy_invalid_param` 只保留给真正的报文问题,
// "这一行不挂在任何邀请关系上"是 `qy_rel_no_relation`,文案直接回答
// "为什么这一行不能拉黑"。前端据此**根本不渲染**这个按钮(治本),
// 这条 400 是治标的第二道闸。
//
// ## 二、拉黑一条真实存在的关系会静默地什么都不做,却回 200
//
// 旧代码直接 `Model(&InviteRelation{}).Where("invitee_id = ?").Updates(...)`。
// 而 qy_invite_relation 是**懒建**的快照:ensureRelation 只在某个下线第一次
// 产生佣金时才写那一行。备份库实测 users 里 377 条绑定、快照表 11 行 ——
// 也就是说 **97% 的真实关系在这张表里没有行**,`Updates` 影响 0 行,
// 接口照样回 `{"blocked":true}`。
//
// 后果不是"少了个提示":`blockedInvitees()` 读的正是
// `qy_invite_relation WHERE blocked = true`,快照行不存在 = 这个人从来没被
// 拉黑过。运营看到成功提示,以为自刷已经被止住,而佣金一分不少地继续计给
// 上线。实测证据:对 1316(users.inviter_id = 1302,快照表无行)拉黑返回
// 200 success,而 `SELECT ... WHERE invitee_id = 1316` 仍然是 0 行。
//
// 修法是**按权威字段补建快照行**,与 markRelationUnbound 对缺行的处理同一手法:
// 主库 users.inviter_id 说这条关系存在,快照就该有一行;主库说不存在而快照也
// 没有,才是真的"没有这条关系"(`qy_rel_not_bound`)。
func adminBlockRelation(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		InviteeId int    `json:"invitee_id"`
		Blocked   bool   `json:"blocked"`
		Reason    string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.InviteeId <= 0 {
		// 与上面那条 400 刻意用不同的 code:前端要能分辨"我发错了报文"
		// 与"这一行本来就不是一条邀请关系"。
		badRequest(c, "qy_rel_no_relation",
			"这条记录没有关联的邀请关系(手工调整等非邀请来源的计佣行不挂在任何下线上),无法拉黑")
		return
	}

	ctx := c.Request.Context()
	// 受益人是**邀请人**,与 bind/rebind 同一条判据 —— 只是这条接口的报文里
	// 没有 inviter_id,得先按关系解析出来。
	//
	// 两个方向都动的是别人的钱:blocked=false 是"恢复计佣",一个 role=10 管理员
	// 可以用它把上级基于风控停掉的、落在**自己名下**的计佣重新打开,而且迟付
	// 回捞(sweepLateTopups)会把停止窗口内的充值一并补算 —— 收益面比接口文案
	// 承诺的"停止期间不补算"还大;blocked=true 则能把任意更高权限账号(实测含
	// root)名下的进项静默停掉。这张表的 blocked 列只有 setRelationBlocked
	// 一个写入口,闸门漏在这里就是彻底没有。
	if denyActorOverTarget(c, "commission.relation.block", currentInviterId(ctx, req.InviteeId)) {
		return
	}
	before := relationSnapshot(ctx, req.InviteeId)
	inviterId, err := setRelationBlocked(ctx, req.InviteeId, req.Blocked, req.Reason)
	if err != nil {
		writeRelationAudit(c, "commission.relation.block", req.InviteeId, qymodel.ResultFail,
			blockVerb(req.Blocked)+"邀请关系的计佣失败: "+err.Error()+" | 事由: "+req.Reason,
			before, relationSnapshot(ctx, req.InviteeId))
		respondRelationError(c, err)
		return
	}

	invalidateBlocked()
	writeRelationAudit(c, "commission.relation.block", req.InviteeId, qymodel.ResultOK,
		blockVerb(req.Blocked)+"邀请关系的计佣(邀请人 "+itoa(inviterId)+"):"+
			blockOutcome(req.Blocked)+" | 事由: "+req.Reason,
		before, relationSnapshot(ctx, req.InviteeId))
	respond(c, gin.H{
		"invitee_id": req.InviteeId,
		"inviter_id": inviterId,
		"blocked":    req.Blocked,
	})
}

// blockVerb 与 blockOutcome 拼出这条审计的正文。
//
// 拆成方向相关的两段,是因为共用一句话曾经写出过自相矛盾的记录:
// 解封那一次的正文是「解封邀请关系:只停止未来计佣,已发放的佣金不回收」——
// 前半句说恢复、后半句说停止。审计正文是事后仲裁"这一刻到底发生了什么"的
// 唯一凭据,它不能两头都占。
//
// 动词跟界面走(停止计佣 / 恢复计佣):运营在界面上按的是那两个字,
// 事后来查审计时找的也是那两个字。
func blockVerb(blocked bool) string {
	if blocked {
		return "停止"
	}
	return "恢复"
}

func blockOutcome(blocked bool) string {
	if blocked {
		return "邀请关系保留(可追溯、可随时恢复),只停止未来计佣;已发放的佣金不回收(要收回须单独走冲正)"
	}
	return "从此刻起恢复计佣;停止期间发生的消费与充值不补算,已发放的佣金不受影响"
}

// setRelationBlocked 把拉黑标记写进快照表,快照行缺失时按主库的权威字段补建。
//
// 返回这条关系的邀请人 id,供审计与响应回显 —— 拉黑之后运营最需要知道的是
// "我刚刚断掉的是谁的进项"。
func setRelationBlocked(ctx context.Context, inviteeId int, blocked bool, reason string) (int, error) {
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	if model.DB == nil {
		return 0, db.ErrNotReady
	}

	var invitee model.User
	err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username", "email", "inviter_id", "created_at").
		Where("id = ?", inviteeId).Take(&invitee).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, errRelUserMissing
	}
	if err != nil {
		return 0, err
	}

	var snaps []InviteRelation
	if err := gdb.WithContext(ctx).Where("invitee_id = ?", inviteeId).
		Limit(1).Find(&snaps).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}

	// 权威字段优先:主库说"现在绑着谁"才是这条关系此刻的样子。主库为 0 时
	// 回落到快照的 inviter_id —— 那是"已解绑但历史还在"的形状,对它解封
	// (blocked=false)仍然有意义。两边都没有,才是真的没有这条关系。
	inviterId := invitee.InviterId
	if inviterId == 0 && len(snaps) > 0 {
		inviterId = snaps[0].InviterId
	}
	if inviterId == 0 {
		return 0, errRelNotBound
	}

	now := common.GetTimestamp()
	if len(snaps) > 0 {
		// 只写 block_reason,**不碰 risk_flags** —— 后者是 ensureRelation 写的
		// 自动风控标记(reciprocal_invite),一次人工停/恢复就把它抹掉的话,
		// AFF 关系页上那个徽标从此显示的是人写的话,而"系统判定为互刷"这个
		// 事实再也看不到了。两列各管各的。
		if err := gdb.WithContext(ctx).Model(&InviteRelation{}).
			Where("invitee_id = ?", inviteeId).
			Updates(map[string]any{
				"blocked":      blocked,
				"block_reason": truncate(reason, 255),
				"updated_at":   now,
			}).Error; err != nil {
			db.MarkFailure(err)
			return 0, err
		}
		return inviterId, nil
	}

	// 快照缺行。补一行而不是回 200 假装成功 —— blockedInvitees() 只认这张表,
	// 没有行就等于没拉黑。bound_at 取下线的注册时间:自动绑定发生在那一刻。
	row := InviteRelation{
		InviteeId:   inviteeId,
		InviterId:   inviterId,
		MaskedName:  truncate(MaskUsername(displayName(invitee)), 64),
		InviteeRef:  inviteeRef(inviteeId, refSalt()),
		BoundAt:     invitee.CreatedAt,
		BlockReason: truncate(reason, 255),
		Blocked:     blocked,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := gdb.WithContext(ctx).Save(&row).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	return inviterId, nil
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
	// 法币折算比例的分组档同理(fiatrate.go 的 fiatRateCache)。它与分组费率是
	// 同一形状的第五把缓存,当初漏在这里的后果一模一样:比例不是通过本模块的
	// PUT 改的时候(直接改扩展库、从备份恢复、或者多节点部署里在另一个节点上改的),
	// 运营按了"失效缓存"仍要干等一分钟,而这一分钟里发出去的佣金按旧比例冻结
	// 进 accrual.usd_rate,按本模块"逐笔冻结、改比例不追溯"的语义永不重算。
	invalidateFiatRates()
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
//
// ledger_check 是第三类,也是最安静的一类:账本自己跟自己对不上。它不会报错、
// 不会丢队列、不会降级,三张表各自看起来都很正常 —— 只有把它们三个放在一起
// 减一次才看得见。加进来的直接原因见 ledgerCheck 的说明。
func adminHealth(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	ctx := c.Request.Context()
	pending, _ := pendingInviters(settleInviterBatch)
	respond(c, gin.H{
		"metrics":       metricsSnapshot(),
		"hot_queue":     guard.QueueStats(),
		"inviter_cache": inviterCacheStats(),
		// cache_sync 是五把进程内缓存的跨节点失效通道。enabled=false 或
		// failed 持续增长,意味着别的节点可能仍在按旧费率/旧拉黑名单计佣 ——
		// 那是一段没有任何账本痕迹的错价窗口(见 cachesync.go)。
		"cache_sync": cacheSyncStats(),
		// daily_settle 是一日一结算之后**唯一**能回答"今天的佣金发了没有"的
		// 一段。pending_inviters 在这个节奏下几乎没有信息量:一天里绝大部分
		// 时间它都是"有一堆人等着",那是正常的,而"今天那一跑挂在半路"
		// 在它上面看起来一模一样。
		"daily_settle":    dailySettleSnapshot(common.GetTimestamp()),
		"topup_low_water": peekTopupCursor(),
		// pending_inviters 只取第一页,所以它是"至少有这么多人在等",
		// 封顶 settleInviterBatch,不是队列全长。
		"pending_inviters":   len(pending),
		"blocked_relations":  len(blockedInvitees(ctx)),
		"effective_settings": effectiveCtx(ctx),
		"ledger_check":       ledgerCheck(ctx),
		"degraded": gin.H{
			"settings":   settingsDegrade.stats(),
			"group_rate": groupRateDegrade.stats(),
			// fiat_rate 与另外两个同一条理由,而且它更安静:折算比例走错一层
			// 不会让任何额度数字看起来不对,只会让那批佣金的**法币**口径偏掉,
			// 而 available_fiat 是提现金额的唯一依据。
			"fiat_rate": fiatRateDegrade.stats(),
			// inviter_group 是本轮把费率口径改到上线分组之后新增的依赖:
			// 两档比例现在都取自**上线**那一行 users,主库读不到他就两档
			// 一起跳过分组层。与上面两个分开报,是因为处置不同 ——
			// 这一个响意味着主库有问题,不是扩展库。
			"inviter_group": inviterGroupDegrade.stats(),
		},
	})
}

// maxLedgerCheckUsers 是一次体检最多核对多少行余额。
//
// 超过就只报"这次没查全",而不是拖着体检接口跑一张全表 join —— 体检本身把
// 站点拖慢,是最没道理的一种故障。
const maxLedgerCheckUsers = 20000

// ledgerCheck 是返佣账本的自洽性体检。
//
// # 为什么必须由**服务端**报,而不是留给谁去写 SQL
//
// 本模块有三条恒等式,它们都不会自己喊疼;这里体检的是**前两条**:
//
//	I1  Σ计佣行.settled_amount(status≠voided) == Σ结算单(granted−reclaimed) + 未结算余数
//	I2  可提现 + 冻结 + 已提现 == 累计earned − 累计clawback
//	I3  base_quota × rate_bps / 10000 == gross_amount + capped_amount(逐行)
//
// # I3 为什么不在这里
//
// 它不是一条全表恒等式:只有 source_type ∈ {consume, topup, redemption} 的
// 正额计佣行满足它,manual(费率恒为 0)与 clawback(base_quota 是负数的幂等
// 指纹,金额还会被等比冲正与 remaining 削减)按设计就不满足 —— 逐条理由写在
// accrual.go 的 capGross 上。无限定地全表算 I3,报出来的"漂移行数"绝大多数会
// 是这两类完全正常的行,那个数字比没有更糟:它会让真正的一条被淹掉,
// 上一轮已经有人照着无限定的注释口径报过一次误报。
//
// 所以要么带着那三个 source_type 的过滤条件做,要么不做。这里选择不做:
// I1/I2 覆盖的是"钱的总量对不对",而 I3 覆盖的是"这一行为什么是这个数",
// 后者的读者是查某一笔账的人,他手上已经有 source_type,而全站计数对他没用。
//
// I2 每一行余额上都算好了(balanceView.LedgerDrift),运营在余额页一眼能看见。
// **I1 此前站内任何地方都看不见** —— 它跨 qy_commission_accrual、
// qy_commission_settlement、qy_commission_balance 三张表,任何单表视图都答不了。
//
// 这不是假想的风险:备份库上 inviter 1622 的 I1 漂移是 −0.3663,而他的 I2 恰好
// 是 0,于是余额页、总表、健康面板三个地方全都显示"这行账很正常"。成因指纹是
// qy_commission_accrual 的 id 跨到 90 却只剩 28 行 —— 有人删过计佣行。账本是
// append-only 的,删掉一行的钱不会跟着消失,它只是从此对不上。
//
// 所以这里报的是"哪几行对不上、差多少、最坏的是谁",让下一次漂移在发生当天
// 就有人看得见,而不是等下一轮排查时再从 SQL 里刨出来。
//
// # self_invited_users
//
// 顺带体检的一条数据异常:users.inviter_id == users.id(自己邀请自己)。
// 计佣热路径已经挡住了它(hook.go 两处 `e.InviterId == inviteeId` 直接跳过),
// 写路径也有 qy_rel_self_invite 拦着,所以**它不会产生钱**;但存量数据里那一条
// 谁也不会主动去查,而同样的形状出现在真人身上就是一条自刷返佣关系。
//
// 读失败不让整个健康面板 500:体检项缺失时明说 ok=false,而队列、缓存、降级
// 那几个指标本来就该继续可见 —— 排查故障时最不需要的就是"健康页也打不开"。
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
		Select("user_id, COALESCE(SUM(granted_quota - reclaimed_quota), 0) AS net").
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
		// I2:与余额页那一列同一条恒等式,在这里做成全站计数 —— 一行一行翻过去
		// 找非零值,是没有人会做的事。
		if b.AvailableQuota != b.TotalEarnedQuota-b.TotalClawbackQuota-b.FrozenQuota-b.WithdrawnQuota {
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
	// settle_drift 就是那条此前站内看不见的 I1。
	out["settle_drifted_users"] = settleDrifted
	out["settle_drift_total"] = totalDrift.String()
	out["settle_drift_worst_user_id"] = worstUser
	out["settle_drift_worst"] = worstDrift.String()
	// balance_drift 是余额页 ledger_drift 那一列的全站计数(I2)。
	out["balance_drifted_users"] = balanceDrifted
	out["self_invited_users"] = selfInvited
	return out
}
