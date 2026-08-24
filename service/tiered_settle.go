package service

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// TieredResultWrapper wraps billingexpr.TieredResult for use at the service layer.
type TieredResultWrapper = billingexpr.TieredResult

// BuildTieredTokenParams constructs billingexpr.TokenParams from a dto.Usage,
// normalizing P and C so they mean "tokens not separately priced by the
// expression". Sub-categories (cache, image, audio) are only subtracted
// when the expression references them via their own variable.
//
// GPT-format APIs report prompt_tokens / completion_tokens as totals that
// include all sub-categories (cache, image, audio). Claude-format APIs
// report them as text-only. This function normalizes to text-only when
// sub-categories are separately priced.
func BuildTieredTokenParams(usage *dto.Usage, isClaudeUsageSemantic bool, usedVars map[string]bool) billingexpr.TokenParams {
	p := float64(usage.PromptTokens)
	// c 必须与倍率路径走**同一条归一化**:上游把思考 token 放在 completion_tokens
	// 之外时(gemini-3-flash 一次真实调用 {prompt 100, completion 1, reasoning 53,
	// total 154}),倍率路径已经在 calculateTextQuotaSummary 里把它补进 completion
	// 并计费,阶梯路径原先直接取 usage.CompletionTokens 原值 —— 于是同一条上游
	// 响应,按倍率收 524、按表达式只收 158(诚实价 555),输出项少收 54 倍。
	//
	// 更糟的是消费日志里 completion_tokens 记的是**已归一化的 54**,扣的钱按 1 算,
	// 账单自己和自己对不上:把日志里的三项代回日志里那条表达式得不到日志里的金额。
	//
	// 本站挂 tiered_expr 的正是 gemini 那一族,而它们的表达式把 c 的系数定在
	// 50~120,输出侧是主要收入项 —— 少了这一句,那些模型的思考 token 全免费。
	//
	// reasoningTokensOutsideCompletion 自带「prompt+completion+reasoning == total
	// 且 prompt+completion != total」的三数互证,Claude 语义天然不满足,所以这里
	// 无条件调用是安全的,不会把 Claude 那一支多算一遍。
	c := float64(usage.CompletionTokens + reasoningTokensOutsideCompletion(usage))
	cr := float64(usage.PromptTokensDetails.CachedTokens)
	cc5m := float64(usage.PromptTokensDetails.CacheCreationTokensTotal())
	cc1h := float64(0)

	if usage.UsageSemantic == "anthropic" {
		cc1h = float64(usage.ClaudeCacheCreation1hTokens)
		// cc5m 曾经在这里被 ClaudeCacheCreation5mTokens **覆盖**,而那个字段只有在
		// 上游额外返回 cache_creation.ephemeral_5m_input_tokens 这个拆分对象时才有值。
		// 上游只报标准的 cache_creation_input_tokens 时(AWS Bedrock 的 Claude 就
		// 从来不报拆分,官方 API 也只在开了 1h beta 时才报)覆盖后的 cc 恒为 0;
		// 而 Claude 的 input_tokens 本来就**不含**缓存写入,这批 token 也不在 p 里。
		// 结果是缓存写入(单价是输入价的 1.25 倍、prompt caching 场景下常常是账单
		// 主项)在 tiered_expr 这条路上**整段免费**,同时 len 被低估、分档条件可能
		// 掉到便宜档。倍率路径没有这个问题:它自己算了 remaining = 总量 − 5m − 1h。
		//
		// 表达式语言只有 cc(5m)与 cc1h 两个缓存写入变量,没有"其余"这一档,
		// 所以把余数并进 cc —— 与 relaykit 的 NormalizeCacheCreationSplit 同口径,
		// 也与倍率路径按 CacheCreationRatio 收 remaining 的结果一致。
		if total := float64(usage.PromptTokensDetails.CacheCreationTokensTotal()); total > 0 {
			cc5m = float64(usage.ClaudeCacheCreation5mTokens)
			if remaining := total - cc5m - cc1h; remaining > 0 {
				cc5m += remaining
			}
		} else {
			cc5m = float64(usage.ClaudeCacheCreation5mTokens)
		}
	}

	img := float64(usage.PromptTokensDetails.ImageTokens)
	ai := float64(usage.PromptTokensDetails.AudioTokens)
	imgO := float64(usage.CompletionTokenDetails.ImageTokens)
	ao := float64(usage.CompletionTokenDetails.AudioTokens)

	// len = total input context length for tier condition evaluation.
	// Non-Claude: prompt_tokens already includes everything.
	// Claude: input_tokens is text-only, so add cache read + cache creation.
	inputLen := p
	if isClaudeUsageSemantic {
		inputLen = p + cr + cc5m + cc1h
	}

	if !isClaudeUsageSemantic {
		if usedVars["cr"] {
			p -= cr
		}
		if usedVars["cc"] {
			p -= cc5m
		}
		if usedVars["cc1h"] {
			p -= cc1h
		}
		if usedVars["img"] {
			p -= img
		}
		if usedVars["ai"] {
			p -= ai
		}
		if usedVars["img_o"] {
			c -= imgO
		}
		if usedVars["ao"] {
			c -= ao
		}
	}

	// OpenAI cache-write usage reports unadjusted prefix counts, so cr + cc can
	// exceed the prompt and drive the remainder negative. Clamp at zero.
	if p < 0 {
		p = 0
	}
	if c < 0 {
		c = 0
	}

	return billingexpr.TokenParams{
		P:    p,
		C:    c,
		Len:  inputLen,
		CR:   cr,
		CC:   cc5m,
		CC1h: cc1h,
		Img:  img,
		ImgO: imgO,
		AI:   ai,
		AO:   ao,
	}
}

func refreshTieredBillingGroup(relayInfo *relaycommon.RelayInfo) (*billingexpr.BillingSnapshot, error) {
	if relayInfo == nil {
		return nil, nil
	}
	snap := relayInfo.TieredBillingSnapshot
	if snap == nil || snap.BillingMode != "tiered_expr" {
		return nil, nil
	}

	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio

	// snap.EstimatedQuotaBeforeGroup 是**未乘任何分组因子**的表达式结果
	// (见 relay/helper/price.go modelPriceHelperTiered)。分组级乘数和分组倍率
	// 一样是"当前分组"的属性,auto 重试切组后两者都必须按新分组重算。
	// 调用方 controller.getChannel 刚跑过 HandleGroupRatio,此刻
	// relayInfo.UsingGroup 已是新分组、PriceData.GroupRatioInfo 已是新倍率,
	// QyGroupTieredSettle 内部按 UsingGroup 查规则,拿到的就是新分组的乘数。
	//
	// 刻意不做「GroupRatio 没变就跳过」的短路:两个分组完全可以共用同一个倍率
	// (例如都配 1.0),而分组级乘数是按**分组名**查的,倍率相同不代表乘数相同。
	// 短路掉的那条路径正是"切组后仍按原分组乘数预留"的缺陷本体。
	// 少这一层短路不改变任何行为:调用方本来就每次尝试都无条件 Reserve 一次。
	beforeGroup := QyGroupTieredSettle(relayInfo, snap.EstimatedQuotaBeforeGroup)
	estimatedQuota, err := billingexpr.QuotaRoundStrict(beforeGroup * groupRatio)
	if err != nil {
		return nil, err
	}
	snap.GroupRatio = groupRatio
	snap.EstimatedQuotaAfterGroup = estimatedQuota
	return snap, nil
}

// PrepareTieredBillingForSelectedGroup refreshes routing-dependent billing
// state before an upstream attempt. An existing session reserves any higher
// estimate before sending. If the initial group was free and skipped
// pre-consume, switching to a paid group creates the session at that point.
func PrepareTieredBillingForSelectedGroup(c *gin.Context, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	snap, err := refreshTieredBillingGroup(relayInfo)
	if err != nil {
		return types.NewErrorWithStatusCode(
			err,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if snap == nil {
		return nil
	}
	if snap.GroupRatio == 0 {
		// Paid-to-free keeps FreeModel as-is: FreeModel means "pre-consume was
		// skipped", which is not true once a session exists, and settlement
		// already yields 0 for a zero group ratio.
		return nil
	}

	// The selected group is paid; clear a FreeModel flag frozen when the
	// initial group was free so downstream state stays consistent.
	relayInfo.PriceData.FreeModel = false

	if relayInfo.Billing == nil {
		return PreConsumeBilling(c, snap.EstimatedQuotaAfterGroup, relayInfo)
	}
	if err := relayInfo.Billing.Reserve(snap.EstimatedQuotaAfterGroup); err != nil {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	relayInfo.FinalPreConsumedQuota = relayInfo.Billing.GetPreConsumedQuota()
	return nil
}

// TryTieredSettle checks if the request uses tiered_expr billing and, if so,
// computes the actual quota using the captured BillingSnapshot. Returns:
//   - ok=true, quota, result  when tiered billing applies
//   - ok=false, 0, nil        when it doesn't (caller should fall through to existing logic)
func TryTieredSettle(relayInfo *relaycommon.RelayInfo, params billingexpr.TokenParams) (ok bool, quota int, result *billingexpr.TieredResult) {
	snap := relayInfo.TieredBillingSnapshot
	if snap == nil || snap.BillingMode != "tiered_expr" {
		return false, 0, nil
	}

	requestInput := billingexpr.RequestInput{}
	if relayInfo.BillingRequestInput != nil {
		requestInput = *relayInfo.BillingRequestInput
	}

	tr, err := billingexpr.ComputeTieredQuotaWithRequest(snap, params, requestInput)
	if err != nil {
		quota = relayInfo.FinalPreConsumedQuota
		if quota <= 0 {
			quota = snap.EstimatedQuotaAfterGroup
		}
		return true, quota, nil
	}

	// 分组级价的乘数必须在这里再作用一次,否则预扣与结算不同口径。
	//
	// ComputeTieredQuotaWithRequest 是从 snap.ExprString **重跑表达式**的,
	// 它只乘 snap.GroupRatio —— 预扣侧 modelPriceHelperTiered 里那次
	// QyGroupTieredQuota 的乘数到这里已经不存在了。给分组配了折扣时,
	// 预扣按折扣价、结算按原价,差额以追扣落到用户头上。
	//
	// 作用在 before-group 上再重算,而不是乘最终那个已取整的 int:
	// 后者会引入与预扣侧不同的第二次舍入。公式与 billingexpr/settle.go 逐字一致。
	if adjusted := QyGroupTieredSettle(relayInfo, tr.ActualQuotaBeforeGroup); adjusted != tr.ActualQuotaBeforeGroup {
		after, clamp := common.QuotaRoundChecked(adjusted * snap.GroupRatio)
		tr.ActualQuotaBeforeGroup = adjusted
		tr.ActualQuotaAfterGroup = after
		if clamp != nil {
			tr.Clamp = clamp
		}
	}

	// Surface any int32 saturation from settlement onto RelayInfo so the
	// consume log records it under admin_info, regardless of which caller
	// (text, audio, WSS) consumes the returned quota. First non-nil wins.
	noteQuotaClamp(relayInfo, tr.Clamp)

	// 非负地板。表达式是运营自由填写的算术式（`p * 3 - 20000` 这种"前 2 万 token
	// 免费"的促销形状很自然），其中的 param(...) 还直接取自客户端请求体，所以
	// 结果可以为负。按倍率和按次两条路都有 `<=0 → 1` / `=0` 的地板兜住，
	// 唯独阶梯表达式的结果是原样返回的：负值经 SettleBilling 变成负 delta，
	// 资金来源执行 Increase —— 扣费变成给用户充值。
	if tr.ActualQuotaAfterGroup < 0 {
		common.SysError(fmt.Sprintf(
			"tiered billing expression produced a negative quota (%d) for model %s; clamped to 0",
			tr.ActualQuotaAfterGroup, relayInfo.OriginModelName))
		tr.ActualQuotaAfterGroup = 0
	}

	return true, tr.ActualQuotaAfterGroup, &tr
}
