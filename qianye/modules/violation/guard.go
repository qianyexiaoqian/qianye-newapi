package violation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ctxKeyPostDone 是事后检测的幂等位。
//
// 必须有:PostRelayGuard 挂在 relay.go 的 defer 里,而 defer 在 panic-recover
// 路径与未来的上游改动下都可能重入,重入就是重复扣费。
const ctxKeyPostDone = constant.ContextKey("qy_violation_post_done")

// defaultBlockMessage 是拦截时的默认文案。
//
// 对外文案永远不包含命中词、规则内部名、规则 id 与阈值细节 —— 那等于把规则库
// 免费送给刷子,一次探测就能反推出整张词表。
const defaultBlockMessage = "您的请求因违反内容策略被拒绝"

// PreRelayGuard 是能力 B(转发前提示词拦截)的入口。
//
// 挂载点选在 relay.go 的 ModelPriceHelper 之后、PreConsumeBilling 之前,同时满足:
//   - relayInfo.PriceData 已就绪 → "模型价格 × 倍数"扣费可用;
//   - 尚未预扣费 → 拦截时不产生预扣费/退款的往返;
//   - 早于重试循环 → 这才是真正的"转发上游之前"。
//
// 入参 upstreamErr 是 ModelPriceHelper 的错误,原样透传:调用点复用了上游已有的
// `if err != nil` 分支,因此这个函数必须在上游已经失败时什么都不做。
//
// 返回的 *types.NewAPIError 会被上游的 types.NewError 原样保留(errors.As 命中),
// 再由 relay.go 的 defer 按 relayFormat 序列化成 OpenAI / Claude / Realtime 三种
// 格式 —— 本模块不写任何序列化代码。
func PreRelayGuard(c *gin.Context, info *relaycommon.RelayInfo, meta *types.TokenCountMeta, upstreamErr error) error {
	if upstreamErr != nil {
		return upstreamErr
	}
	if c == nil || info == nil {
		return nil
	}
	cfg := config.Get().Violation
	if !cfg.Enabled || !cfg.PrecheckEnabled {
		return nil
	}
	// 热路径绝不能因为本模块 panic:relay 是主业务,风控是附加物。
	defer recoverHot("pre_relay_guard")

	maybeRefresh()
	snap := Snapshot()
	if !snap.hasPrompt() {
		return nil
	}

	// 请求频率(防蒸馏)判据的计数必须在扫描之前推进,而且只在这个挂载点成立:
	// info.IsStream 要等 GenRelayInfo 之后才确定,而计数又必须早于预扣费 ——
	// 本函数恰好夹在两者之间,零上游改动。
	//
	// 只数非流式请求:批量采集训练语料的一方要完整 JSON,不会开 stream。
	// 排除 Playground:那是站内调试点击,把管理员自己数进封号线只会制造工单。
	// (渠道测试走的是另一条路径,根本到不了这里。)
	rate := 0
	if snap.hasRate && !info.IsStream && !info.IsPlayground {
		ctx := context.Background()
		if c.Request != nil {
			ctx = c.Request.Context()
		}
		rate = bumpRequestRate(ctx, info.UserId)
	}

	text := promptText(meta, info)
	// 没有 prompt 文本时仍可能有频率规则要判。计数为 0 才是真的没有任何规则
	// 可能命中 —— compile 把频率阈值的下界钉在 1 就是为了让这一步成立。
	if text == "" && rate <= 0 {
		return nil
	}

	in := scanInput{
		Model:     info.OriginModelName,
		Group:     info.UsingGroup,
		Text:      clipHeadTail(text, maxScanBytes),
		RateCount: rate,
	}
	v := scanPrompt(snap, in)
	if v == nil || v.Rule == nil {
		noteScan(false)
		return nil
	}

	shadow, shadowReason := effectiveShadow(v.Rule)
	block := blocks(v.Rule.R.Action) && !shadow
	noteScan(block)

	handleHit(c, info, PhasePrompt, in, v, shadow, shadowReason, block)
	if !block {
		return nil
	}

	msg := v.Rule.R.BlockMessage
	if msg == "" {
		msg = defaultBlockMessage
	}
	return types.NewErrorWithStatusCode(
		errors.New(msg),
		types.ErrorCode(violationErrorCode(v.Rule.R.Id)),
		http.StatusBadRequest,
		// 必须跳过重试:换个渠道重发同一段违规 prompt 没有任何意义,
		// 只会把一次违规放大成 N 次上游调用。
		types.ErrOptionWithSkipRetry(),
		// 违规拒绝不是渠道故障,不该计入渠道错误日志与自动禁用统计。
		types.ErrOptionWithNoRecordErrorLog(),
	)
}

// PostRelayGuard 是能力 A(事后检测扣费)的入口。
//
// 挂在 relay.go defer 中 Billing.Refund 与上游内置 ChargeViolationFeeIfNeeded
// 之后:预扣费已退回,此时扣的违规费不会被退款冲掉。
// 不修改传入的 apiErr —— 上游错误原样回给用户,只是额外扣了钱。
func PostRelayGuard(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) {
	if c == nil || info == nil || apiErr == nil {
		return
	}
	cfg := config.Get().Violation
	if !cfg.Enabled || !cfg.PostChargeEnabled {
		return
	}
	if common.GetContextKeyBool(c, ctxKeyPostDone) {
		return
	}
	common.SetContextKey(c, ctxKeyPostDone, true)
	defer recoverHot("post_relay_guard")

	maybeRefresh()
	snap := Snapshot()
	if !snap.hasPost() {
		return
	}

	in := scanInput{
		Model:        info.OriginModelName,
		Group:        info.UsingGroup,
		ErrCode:      string(apiErr.GetErrorCode()),
		StatusCode:   apiErr.StatusCode,
		UpstreamText: clipHeadTail(apiErr.Error()+"\n"+apiErr.ToOpenAIError().Message, maxScanBytes),
		RejectReason: common.GetContextKeyString(c, constant.ContextKeyAdminRejectReason),
	}
	v := scanPost(snap, in)
	if v == nil || v.Rule == nil {
		return
	}

	shadow, shadowReason := effectiveShadow(v.Rule)
	// 去重:上游内置的 Grok 违规扣费(service.ChargeViolationFeeIfNeeded)已经
	// 就同一个错误扣过一次。这里只记录不扣费,零改动地避免重复罚款。
	if service.IsViolationFeeCode(apiErr.GetErrorCode()) {
		// 这一条被强制记成影子,原因是"上游已扣过"而不是规则模式或熔断 ——
		// 拿 shadowReason 顶上去会让统计里凭空多出一批并不存在的熔断样本。
		rec := newRecord(c, info, PhaseUpstreamErr, in, v, true, ShadowReasonDupBuiltin, false)
		rec.FeeStatus = FeeStatusSkippedDup
		persist(rec, nil)
		return
	}
	handleHit(c, info, PhaseUpstreamErr, in, v, shadow, shadowReason, false)
}

// 影子原因。落进 Record.ShadowReason,是"这一条为什么没有真实执行"的唯一答案。
const (
	// ShadowReasonRuleMode:规则自己声明的就是影子(Mode != enforce)。
	// 这是项目方要的那个用例产生的记录:预期内的观察样本。
	ShadowReasonRuleMode = "rule_mode"
	// ShadowReasonBreaker:规则声明的是真实执行,但熔断正在把全部规则钳成影子。
	// 这类记录是事故现场,不能和上一类混在一起做误判率统计。
	ShadowReasonBreaker = "breaker"
	// ShadowReasonDupBuiltin:上游内置的 Grok 违规扣费已经就同一个错误扣过一次,
	// 本模块只记录。与模式无关,单独一个取值。
	ShadowReasonDupBuiltin = "dup_builtin_fee"
)

// effectiveShadow 判定这次命中要不要按影子处理,并给出原因。
//
// 只有两层,顺序不能反:
//
//  1. **规则自己的 Mode**。这是运营表达的意图,是唯一的"模式"。判据写成
//     `== ModeEnforce` 而不是 `!= ModeShadow`:空串与任何未知取值都必须落在
//     影子一侧(滚动升级、手工 SQL、迁移没跑到的行都会产生空串)。
//  2. **熔断钳位**。规则说 enforce 时才轮到它。它不是模式,是刹车,
//     所以原因单独一个取值 —— 事后要能把"预期内的影子样本"和"熔断期间被迫
//     变成影子的真实规则"分开统计,否则误判率会被稀释得毫无意义。
//
// 提成函数是因为 PreRelayGuard 与 PostRelayGuard 都要算它:同一个判据抄两份,
// 迟早有一处漏掉熔断那一半,而漏掉的表现是"熔断响了却照样扣钱"。
func effectiveShadow(cr *compiledRule) (bool, string) {
	if cr == nil || cr.R.Mode != ModeEnforce {
		return true, ShadowReasonRuleMode
	}
	if on, reason := breakerTripped(); on {
		return true, reason
	}
	return false, ""
}

// handleHit 是"命中之后要做的全部事情":扣费(同步,主库)+ 归档/计数/封号(异步,扩展库)。
func handleHit(c *gin.Context, info *relaycommon.RelayInfo, phase string, in scanInput, v *verdict, shadow bool, shadowReason string, blocked bool) {
	rec := newRecord(c, info, phase, in, v, shadow, shadowReason, blocked)

	res := computeFee(v.Rule, info)
	if shadow {
		shadowHits.Add(1)
		if res.Want > 0 {
			// 影子模式下仍然算出金额并落库:切真实模式之前,管理员需要知道
			// "如果当时真扣,会扣掉多少钱"。
			res.Status = FeeStatusShadow
		}
	} else if res.Want > 0 && chargeable(info) {
		chargeFee(c, info, v.Rule, rec, &res)
	}
	applyFeeToRecord(rec, &res)

	var payload *Payload
	if v.Rule.R.ArchiveContext {
		// 证据必须在 relay 线程里同步组装:body 存储会在请求结束时释放,
		// 异步 goroutine 再去读只会拿到空数据。
		payload = buildEvidence(rec, in, v, filesOf(info))
		rec.HasPayload = payload != nil
	}

	weight := v.Rule.R.CountWeight
	if res.ForceBanWeight {
		// insufficient_balance_policy = ban:扣不到钱就直接顶到阈值,立刻进入封号判定。
		// (只可能来自 chargeFee → applyBalancePolicy,而影子分支根本不调 chargeFee,
		// 所以这一条不会在影子模式下发生。)
		weight = config.Get().Violation.AutoBanThreshold
	}
	// 影子记录也留下 count_weight:它回答"若真实执行,这一次会给计数加几"。
	// 它**不会**被写进计数器(persist 里影子直接跳过 bumpCounter),
	// counted 恒为 false,因此撤销记录时也不会触发计数回退。
	rec.CountWeight = weight

	persist(rec, payload)
}

// persist 把记录、证据、计数与封号全部交给异步 worker。
//
// relay 线程绝不等待扩展库:扩展库慢一秒就是全站 relay 慢一秒。
// 队列满时丢弃,并由 guard 统一告警 —— 丢一条违规记录远好过拖垮主业务。
//
// # 影子命中止步于"落一条记录"
//
// 裁决 2 的原话是「不扣费,不封号,**不记录违规次数**,存入日志,请求上下文,
// 供管理员核查」。此前的实现只跳过了最后一步(不执行封号),计数照常推进 ——
// 而封号判据 reachedThreshold(after, threshold) 完全由持久化的 hit_count 推导,
// 于是影子命中把用户一路推到封号线上,下一次**真实**命中读到被污染的计数,
// 立刻落封禁行,再由 runBanCompensate 每 5 分钟扫 pending 执行封号。
// 也就是说"影子模式不会真实执行"在旧实现里是假的,而且是延迟发作、
// 事后从记录表里完全看不出因果的那种假。
//
// 现在影子命中一个字节都不碰 qy_violation_counter。代价写在 CounterAfterShadow
// 的注释里:管理员失去了 O(1) 的"这个用户已经攒了多少次"。
func persist(rec *Record, payload *Payload) {
	if !db.Available() {
		recordDrops.Add(1)
		return
	}
	weight := rec.CountWeight
	shadow := rec.Shadow
	guard.HotAsync("violation.persist", func(ctx context.Context) error {
		gdb := db.Get()
		if gdb == nil {
			return db.ErrNotReady
		}
		return persistRecord(ctx, gdb, rec, payload, weight, shadow)
	})
}

// persistRecord 是落库那一段的本体,gdb 由调用方注入。
//
// 独立成函数不是为了缩短 persist,而是因为"影子命中不得推进计数"这条不变量
// 只能在这里被直接测到:它上面那层是 guard.HotAsync,测试环境里扩展库不可用,
// 队列作业根本不会执行,断言会永远为真(经典的假回归)。
func persistRecord(ctx context.Context, gdb *gorm.DB, rec *Record, payload *Payload, weight int, shadow bool) error {
	// rec_no 唯一索引兜住一切重入路径,冲突直接跳过而不是报错。
	res := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(rec)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	if payload != nil {
		payload.RecordId = rec.Id
		if err := gdb.WithContext(ctx).Create(payload).Error; err != nil {
			// 证据写失败不影响记录本身:主表已经有命中片段可供研判。
			common.SysError("qianye/violation: 归档证据写入失败: " + err.Error())
		}
	}
	// 影子命中在这里止步:记录与证据已经落好(供管理员核查),
	// 真实计数器一个字节都不动。
	if shadow || weight <= 0 {
		return nil
	}
	st, err := bumpCounter(ctx, gdb, rec.UserId, weight)
	if err != nil {
		return err
	}
	if err := gdb.WithContext(ctx).Model(&Record{}).Where("id = ?", rec.Id).
		Updates(map[string]any{"counted": true, "counter_after": st.HitCount}).Error; err != nil {
		return err
	}
	maybeAutoBan(ctx, gdb, rec, st)
	return nil
}

// newRecord 在 relay 线程里同步组装记录。
//
// 必须同步:gin.Context 在请求结束后不能再访问,用户名/令牌名/IP 这些字段
// 只能在这一刻取到。异步 worker 只负责写库。
func newRecord(c *gin.Context, info *relaycommon.RelayInfo, phase string, in scanInput, v *verdict, shadow bool, shadowReason string, blocked bool) *Record {
	// 影子记录不参与计数,counter_after 因此没有真实答案,统一取哨兵值。
	counterAfter := 0
	if shadow {
		counterAfter = CounterAfterShadow
	} else {
		// 真实执行的记录不该带影子原因:留着会让"按原因分组"的统计里出现一批
		// shadow=false 却写着 rule_mode 的行,而那是自相矛盾的。
		shadowReason = ""
	}
	channelId := 0
	// ChannelMeta 是内嵌指针,prompt 阶段还没选渠道,直接读 info.ChannelId 会 panic。
	if info.ChannelMeta != nil {
		channelId = info.ChannelId
	}
	requestId := c.GetString(common.RequestIdKey)
	if requestId == "" {
		requestId = info.RequestId
	}
	return &Record{
		RecNo:        fmt.Sprintf("vr_%s_%d", truncate(requestId, 40), v.Rule.R.Id),
		UserId:       info.UserId,
		Username:     truncate(c.GetString("username"), 64),
		TokenId:      info.TokenId,
		TokenName:    truncate(c.GetString("token_name"), 64),
		RuleId:       v.Rule.R.Id,
		RuleName:     truncate(v.Rule.R.Name, 128),
		PublicReason: truncate(v.Rule.R.PublicReason, 128),
		Phase:        phase,
		Action:       v.Rule.R.Action,
		Shadow:       shadow,
		ShadowReason: truncate(shadowReason, 64),
		Blocked:      blocked,
		ModelName:    truncate(info.OriginModelName, 128),
		UsingGroup:   truncate(info.UsingGroup, 64),
		ChannelId:    channelId,
		RelayFormat:  truncate(string(info.RelayFormat), 32),
		RequestId:    truncate(requestId, 64),
		Ip:           truncate(c.ClientIP(), 64),
		MatchedTerms: truncate(joinTerms(v.Terms), 1024),
		MatchSnippet: truncate(v.Snippet, 2048),
		CounterAfter: counterAfter,
		Status:       RecordActive,
		FeeStatus:    FeeStatusNone,
		CreatedAt:    common.GetTimestamp(),
	}
}

func applyFeeToRecord(rec *Record, res *feeResult) {
	rec.FeeMode = res.Mode
	rec.FeeBaseUsd = res.BaseUsd
	rec.FeeMultiple = res.Multiple
	rec.GroupRatio = res.GroupRatio
	rec.FeeQuotaWant = res.Want
	rec.FeeQuota = res.Charged
	rec.QuotaClamp = res.Clamp
	rec.FeeError = res.Err
	if res.Status != "" {
		rec.FeeStatus = res.Status
	}
}

// chargeable 判断这次请求是否应该真实扣费。
//
// 渠道测试与 Playground 不扣:前者是管理员在探活,后者是站内调试,
// 对它们罚款只会制造工单。
func chargeable(info *relaycommon.RelayInfo) bool {
	return !info.IsChannelTest && !info.IsPlayground
}

// promptText 取参与匹配的 prompt 文本。
//
// meta.CombineText 可能为空:relay.go 在"敏感词检测关闭 且 CountToken=false"时
// 走的是 fastTokenCountMetaForPricing,它不构建 CombineText。不补这一步,
// 全部 prompt 规则会在这种部署下静默失效且没有任何报错 —— 最危险的失败模式。
func promptText(meta *types.TokenCountMeta, info *relaycommon.RelayInfo) string {
	if meta != nil && meta.CombineText != "" {
		return meta.CombineText
	}
	if info.Request == nil {
		return ""
	}
	rebuilt := info.Request.GetTokenCountMeta()
	if rebuilt == nil {
		return ""
	}
	return rebuilt.CombineText
}

func filesOf(info *relaycommon.RelayInfo) []*types.FileMeta {
	if info.Request == nil {
		return nil
	}
	m := info.Request.GetTokenCountMeta()
	if m == nil {
		return nil
	}
	return m.Files
}

func joinTerms(terms []string) string {
	out := ""
	for i, t := range terms {
		if i > 0 {
			out += " | "
		}
		out += t
	}
	return out
}

func recoverHot(name string) {
	if r := recover(); r != nil {
		common.SysError(fmt.Sprintf(
			"qianye/violation: %s 发生 panic(已拦截,不影响 relay): %v\n%s", name, r, debug.Stack()))
	}
}
