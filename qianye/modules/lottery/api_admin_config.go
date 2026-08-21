package lottery

import (
	"encoding/json" // 仅取 RawMessage 类型;编解码一律走 common.*
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// api_admin_config.go —— 运营参数的读写(qy_settings, scope=lottery)。
//
// 形状照抄 transfer/api_admin_config.go(已过多轮审计):下发
// effective + overrides + bounds + yaml_readonly + yaml_defaults 五块,
// 前端按 editable_keys 渲染字段、按 bounds 做输入校验,**绝不自己抄一份区间** ——
// 两份区间迟早漂移成"界面允许、后端 400",或者更糟的"界面拒绝、后端放行"。

// settingBound 是一个可写键的取值区间(闭区间)。
type settingBound struct {
	Lo int64
	Hi int64
}

// settingBounds 按当前 YAML 算出每个可写键的区间。
//
// 上界必须取自 YAML 而不是写死:max_guess_fee_bps 与 max_total_prize_quota
// 的硬上界就是配置文件里的那两个值,允许在线调到它们之上等于让"防止把 5%
// 手滑打成 50%"和"抽奖派奖是净增发"这两道闸门自己可以被手滑掉。
func settingBounds() map[string]settingBound {
	c := config.Get().Lottery
	return map[string]settingBound{
		keyShowEntry: {Lo: 0, Hi: 1},
		// 四个玩法开关同样是 0/1。它们**没有 YAML 上界**可取:玩法显隐是纯展示
		// 口径,关掉一种玩法既不放大任何资金敞口、也不放宽任何闸门 ——
		// 唯一的硬闸仍然是 YAML 的 lottery.enabled(关掉整块)。
		keyShowPlayDrawRank: {Lo: 0, Hi: 1},
		keyShowPlayDrawProb: {Lo: 0, Hi: 1},
		keyShowPlayDrawBall: {Lo: 0, Hi: 1},
		keyShowPlayGuess:    {Lo: 0, Hi: 1},
		// 上界同样取自 YAML。并发进行中的活动数是全站累计净增发的唯一乘数
		// (每一场各吃一个 max_total_prize_quota,没有全站累计闸门),写死一个
		// 1000 等于允许运营在线把敞口放大 50 倍,而 YAML 拦不住它 ——
		// 这正是这个函数开头那句"上界必须取自 YAML 而不是写死"要防的事。
		keyMaxActiveActivities:  {Lo: 1, Hi: int64(c.MaxActiveActivities)},
		keyMaxGuessFeeBps:       {Lo: 0, Hi: int64(c.MaxGuessFeeBps)},
		keyDefaultGuessFeeBps:   {Lo: 0, Hi: int64(c.MaxGuessFeeBps)},
		keyMaxTotalPrizeQuota:   {Lo: 1, Hi: c.MaxTotalPrizeQuota},
		keyLargePrizeAlertQuota: {Lo: 0, Hi: c.MaxTotalPrizeQuota},
	}
}

// settingsSnapshot 是生效值的下发形状,键名与 editableKeys 逐字一致。
//
// 用 map 而不是结构体:前端按 editable_keys 逐键取值渲染,两边靠同一组字符串
// 对齐;结构体的 json tag 与这份键名清单是两处声明,漂移一次就是一格空白输入框。
func settingsSnapshot(s opSettings) map[string]int64 {
	return map[string]int64{
		keyShowEntry:            boolToInt64(s.ShowEntry),
		keyShowPlayDrawRank:     boolToInt64(s.ShowPlayDrawRank),
		keyShowPlayDrawProb:     boolToInt64(s.ShowPlayDrawProb),
		keyShowPlayDrawBall:     boolToInt64(s.ShowPlayDrawBall),
		keyShowPlayGuess:        boolToInt64(s.ShowPlayGuess),
		keyMaxActiveActivities:  int64(s.MaxActiveActivities),
		keyDefaultGuessFeeBps:   int64(s.DefaultGuessFeeBps),
		keyMaxGuessFeeBps:       int64(s.MaxGuessFeeBps),
		keyMaxTotalPrizeQuota:   s.MaxTotalPrizeQuota,
		keyLargePrizeAlertQuota: s.LargePrizeAlertQuota,
	}
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// handleGetConfig 返回生效参数 + 运营覆盖 + 取值区间 + YAML 只读快照。
func handleGetConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	ctx := c.Request.Context()
	overrides, err := loadOverrides(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	set := effectiveCtx(ctx)
	cfg := config.Get().Lottery

	bounds := make(map[string]gin.H, len(editableKeys))
	for key, b := range settingBounds() {
		bounds[key] = gin.H{"min": b.Lo, "max": b.Hi}
	}

	respondOK(c, gin.H{
		"effective": settingsSnapshot(set),
		// 库里现存的组合非法时,这一页恰恰是修复它的唯一界面,不能跟着一起打不开。
		// mergeOverrides 已经把越界值丢弃回落,所以生效值恒自洽;这里下发的是
		// "有没有被丢弃过",让界面能直说"某一项配置没生效"。
		"effective_valid": overridesAllApplied(set, overrides),
		"overrides":       overrides,
		"editable_keys":   editableKeys,
		"bounds":          bounds,
		// YAML 段:安全闸门与结构性参数,只能改文件后重载 ——
		// 那是一次看得见、留得下痕迹的动作。
		"yaml_readonly": gin.H{
			"enabled":                      cfg.Enabled,
			"proof_public":                 cfg.ProofOpen(),
			"pay_password_threshold_quota": cfg.PayPasswordThresholdQuota,
			"entry_close_grace_seconds":    cfg.EntryCloseGraceSeconds,
			"reveal_delay_seconds":         cfg.RevealDelaySeconds,
			"payout_max_attempts":          cfg.PayoutMaxAttempts,
			"max_total_entries_hard":       cfg.MaxTotalEntriesHard,
			"max_prize_tiers":              cfg.MaxPrizeTiers,
			"max_options":                  cfg.MaxOptions,
			"max_stake_quota":              cfg.MaxStakeQuota,
			"spend_max_lookback_days":      cfg.SpendMaxLookbackDays,
			// 封面上传的三项。前端据此决定"上传"按钮出不出现、accept 写什么、
			// 以及在本地就把超限的文件拦下来 —— 让用户把 5 MiB 传完再看到 413,
			// 是最贵的一种拒绝方式。accept 只是体验,真正的判定是服务端的魔数。
			"cover_enabled":     cfg.CoverOn(),
			"cover_max_bytes":   cfg.CoverMaxBytes,
			"cover_accept_mime": CoverAcceptMimes(),
			// spend_ready_from 是运行期事实而不是配置项:它由重算任务在整个窗口
			// 回填完成之后写入。放在只读段里,是因为管理员必须看得到它 ——
			// 为 0 时任何带"近 N 日消费"的活动都会被拒绝创建。
			"spend_ready_from": SpendReadyFrom(),
		},
		// 基线值。运营需要知道"清掉覆盖之后会回到哪里",否则删除覆盖这个动作
		// 等于闭眼跳。绝大多数键的基线来自 YAML;四个玩法开关没有 YAML 对应项,
		// 基线恒为 1(全部显示)——键名沿用 yaml_defaults 是为了不动前端契约。
		"yaml_defaults": settingsSnapshot(baseSettings(cfg)),
	})
}

// overridesAllApplied 判断库里的覆盖是不是全都真的生效了。
//
// mergeOverrides 对越界值的处理是**丢弃并回落 YAML**(不是夹到边界),
// 所以"库里写了 5000 而生效值是 2000"这种情况不会让功能停摆,但它是一个
// 必须被说出来的事实 —— 否则运营会一直以为自己配的是 5000。
func overridesAllApplied(set opSettings, overrides map[string]string) bool {
	live := settingsSnapshot(set)
	for key, raw := range overrides {
		want, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			// show_entry 历史上写的是 true/false,不是数字。
			if b, bErr := strconv.ParseBool(strings.TrimSpace(raw)); bErr == nil {
				want = boolToInt64(b)
			} else {
				return false
			}
		}
		if got, ok := live[key]; !ok || got != want {
			return false
		}
	}
	return true
}

// handlePutConfig 保存运营参数。
//
// 校验一律"越界即拒绝",不夹到边界:夹取会让运营以为自己配的是 50%、
// 实际跑的是 20%,那正是本仓反复栽跟头的"以为改了其实没改"。
//
// 成功与失败都写审计:费率与奖品上限决定平台会发出去多少钱,
// "谁在什么时候把上限调高了"必须可查,而被拒绝的那次同样是重要信号。
func handlePutConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	// 用 RawMessage 而不是 map[string]int64:前端发字符串最安全,但工具、脚本
	// 与历史客户端习惯发数字,让它们直接 400 只会制造无谓的故障。
	var req map[string]json.RawMessage
	if err := c.ShouldBindJSON(&req); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "lottery.config.update",
			Result: qymodel.ResultFail,
			Reason: "请求体解析失败",
		})
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}

	ctx := c.Request.Context()
	before := effectiveCtx(ctx)
	if len(req) == 0 {
		respondOK(c, gin.H{"effective": settingsSnapshot(before)})
		return
	}

	// 候选值从**当前生效值**出发而不是零值:跨字段校验必须看到"这次没改的那些
	// 字段现在是多少",否则单独把 default_guess_fee_bps 调到上限之上会一路放行。
	candidate := before
	bounds := settingBounds()
	kv := make(map[string]string, len(req))
	for key, raw := range req {
		b, ok := bounds[key]
		if !ok {
			putConfigFailed(c, before, "包含不可在线修改的配置项: "+key)
			return
		}
		v, err := strconv.ParseInt(jsonScalarLiteral(raw), 10, 64)
		if err != nil {
			putConfigFailed(c, before, "配置项 "+key+" 的取值不是整数")
			return
		}
		if v < b.Lo || v > b.Hi {
			putConfigFailed(c, before, "配置项 "+key+" 的取值超出允许范围")
			return
		}
		assignSetting(&candidate, key, v)
		kv[key] = strconv.FormatInt(v, 10)
	}
	// 跨字段:默认手续费不得超过手续费上限。两者可以在同一次请求里一起改,
	// 所以必须在全部赋值之后才判。
	if candidate.DefaultGuessFeeBps > candidate.MaxGuessFeeBps {
		putConfigFailed(c, before, "默认手续费不得超过手续费上限")
		return
	}
	if candidate.LargePrizeAlertQuota > candidate.MaxTotalPrizeQuota {
		putConfigFailed(c, before, "大额告警阈值不得超过奖品总额度上限")
		return
	}

	// 按键名排序后写入:给并发的两次保存一个固定的加锁顺序 —— 两个管理员同时
	// 保存互相交叉的键集时,乱序写正是死锁的配方。
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(kv))
	for _, k := range keys {
		ordered[k] = kv[k]
	}

	if err := saveOverrides(ctx, c.GetInt("id"), ordered); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "lottery.config.update",
			Result: qymodel.ResultFail,
			Reason: auditReason(err),
			Before: settingsSnapshot(before),
		})
		respondErr(c, wrapInternal("保存运营参数", err))
		return
	}
	invalidateSettings()

	after := effectiveCtx(ctx)
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "lottery.config.update",
		Result: qymodel.ResultOK,
		Before: settingsSnapshot(before),
		After:  settingsSnapshot(after),
	})
	respondOK(c, gin.H{"effective": settingsSnapshot(after)})
}

// assignSetting 把一个已经过区间校验的取值写进候选配置。
func assignSetting(s *opSettings, key string, v int64) {
	switch key {
	case keyShowEntry:
		s.ShowEntry = v != 0
	case keyShowPlayDrawRank:
		s.ShowPlayDrawRank = v != 0
	case keyShowPlayDrawProb:
		s.ShowPlayDrawProb = v != 0
	case keyShowPlayDrawBall:
		s.ShowPlayDrawBall = v != 0
	case keyShowPlayGuess:
		s.ShowPlayGuess = v != 0
	case keyMaxActiveActivities:
		s.MaxActiveActivities = int(v)
	case keyDefaultGuessFeeBps:
		s.DefaultGuessFeeBps = int(v)
	case keyMaxGuessFeeBps:
		s.MaxGuessFeeBps = int(v)
	case keyMaxTotalPrizeQuota:
		s.MaxTotalPrizeQuota = v
	case keyLargePrizeAlertQuota:
		s.LargePrizeAlertQuota = v
	}
}

// jsonScalarLiteral 取出一个 JSON 标量的十进制字面量。
//
// 数字原样返回,字符串脱掉引号,布尔换成 0/1。三种写法都收是刻意的:
// 前端发字符串最安全,但工具与历史客户端习惯发数字或布尔。
func jsonScalarLiteral(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "true":
		return "1"
	case "false":
		return "0"
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if err := common.Unmarshal(raw, &out); err == nil {
			out = strings.TrimSpace(out)
			switch out {
			case "true":
				return "1"
			case "false":
				return "0"
			}
			return out
		}
	}
	return s
}

// putConfigFailed 把一次被拒绝的配置变更同时写进审计与响应。
//
// 单独抽出来是因为它有多个调用点,而"被拒绝的那次也要留痕"正是本仓补过的
// 缺陷 —— 漏掉任意一个分支,那条路径就会安静地什么都查不到。
func putConfigFailed(c *gin.Context, before opSettings, reason string) {
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "lottery.config.update",
		Result: qymodel.ResultFail,
		Reason: reason,
		Before: settingsSnapshot(before),
	})
	respondErr(c, errBadRequest(reason))
}
