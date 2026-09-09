package violation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AI 审核在 relay 热路径上的挂载。
//
// 两个时机的调度点都在 PreRelayGuard:
//
//	转发前(phase=prompt)      同步 → 结论回来才继续,命中即拒。代价是延迟。
//	转发后(phase=post_async)  异步 → 丢进 guard.HotAsync,本次请求一秒不等。
//
// 抽样是**各摇各的**(两个时机的抽样率可以按作用域分别配,见 aireview_scope.go),
// 但同时被抽中时只发一次调用:异步那一侧复用同步的结论(见 aiPreReview)。

// aiPreReview 是 AI 审核的唯一入口,由 PreRelayGuard 在本地规则**全部未命中**
// 之后调用。返回非 nil 表示拦截。
//
// # 为什么排在本地规则之后
//
// 本地规则是纯内存的词表与正则,AI 审核是一次外部调用。先便宜后昂贵是一方面;
// 更硬的一条是既有契约:「只取优先级最高的一条规则作为处置依据 —— 一次请求
// 扣两次费、封两次号在任何口径下都是错的」(见 verdict 的注释)。本地已经给出
// 结论时再叠一次 AI,就会让同一次请求落两条记录、加两次计数、扣两次费。
//
// # 它永远不会因为审核出问题而拦截
//
// 唯一返回错误的路径是"模型明确判定违规、命中了一条 enforce 的 block 规则"。
// 超时、非法 JSON、5xx、无可用渠道 —— 全部走到 return nil。
func aiPreReview(c *gin.Context, info *relaycommon.RelayInfo, snap *snapshot, in scanInput, text string) error {
	if !snap.aiOn() {
		noteScan(false)
		return nil
	}
	// 自己审自己的断路器。放在抽样之前:被识别为本进程发出的审核调用时,
	// 连随机数都不该摇(它的抽中与否会污染抽样率的实际口径)。
	if c.Request != nil && c.Request.Header.Get(aiReviewLoopHeader) == processLoopToken {
		noteScan(false)
		return nil
	}
	// ── 作用域闸,排在抽样之前 ──
	//
	// 顺序是契约,不是优化。反过来(先摇骰子、抽中之后再判这条请求在不在
	// 作用域内)会让界面上那个"10%"变成"作用域内的 10% 乘以一个谁也说不出来
	// 的数",而抽样率是本功能唯一的成本闸门,它必须是字面意思。
	//
	// scopeFor 是纯内存比较:没有分配、没有加锁、没有随机数。作用域外的
	// 请求在这里就返回 0/0,一次 crypto/rand 都不摇 —— aiSampleRolls 计数器
	// 钉住了这一点(见 TestAIScopeSamplingZeroCost)。
	//
	// sc 一路带到审核调用与命中处置:这一档用哪份提示词、命中记成哪一类,
	// 与抽样率来自**同一次**匹配。分两次查会让它们在快照刷新时来自不同版本。
	sc, preBps, asyncBps := snap.ai.scopeFor(in.Model, in.Group)
	// 两个时机各自还要有规则在等着。只配了转发后规则的站点不该为转发前那条
	// 同步路径付任何代价 —— 那条路径的代价是给用户加一次外部调用的延迟。
	if !snap.hasAIPrompt {
		preBps = 0
	}
	if !snap.hasAIAsync {
		asyncBps = 0
	}
	// 两个时机**各摇各的**。共用一次抽样的话,转发后想开 10%、转发前只想开
	// 1% 就无法表达:同一枚骰子的结果会把两者绑成同一批请求。
	//
	// `preBps > 0 &&` 不是可有可无的短路,它**就是**零开销契约的落点:
	// 作用域外(以及作用域内但该时机免审)的请求在这里连 sampleAI 都不进,
	// 因此 aiSampleRolls 一次都不加。这一行之前一度还有一句
	// `if preBps <= 0 && asyncBps <= 0 { return }`,读起来像"零开销靠它兜住",
	// 而它其实一个分支都挡不住(删掉之后行为与计数逐字节不变)。留着这种
	// 看起来是防线、实际是复读的语句,下一个人重构时会先删掉真正的那道闸。
	doPre := preBps > 0 && sampleAI(preBps)
	doAsync := asyncBps > 0 && sampleAI(asyncBps)
	if !doPre && !doAsync {
		noteScan(false)
		return nil
	}

	// gin.Context 与 relayInfo 上的值必须在**这一刻**抄下来:异步那一侧几百毫秒
	// 之后才会用到它们,而那时 c 已经被 gin 的 sync.Pool 交给下一个请求了。
	rc := captureRecordCtx(c, info)
	files := filesOf(info)

	var out *aiOutcome
	if doPre {
		ctx := context.Background()
		if c.Request != nil {
			ctx = c.Request.Context()
		}
		out = runAIReview(ctx, snap.ai, sc, text, snap.ai.PreTimeoutMs)
	}

	if doAsync {
		// 转发后审核。同步侧已经拿到结论时直接复用,不发第二次调用 ——
		// 否则同时被两个时机抽中的请求会付两份钱,而两次调用问的是同一段
		// 文本、用的是同一份提示词,第二次的答案不会带来任何新信息。
		dispatchAIAsync(snap, sc, rc, in, text, files, out)
	}

	if out == nil {
		// 只有转发后审核被抽中:同步侧什么都没做,请求原样继续。
		noteScan(false)
		return nil
	}

	// 送审内容在**这里**组装,不是落库那一刻:文本还在手上,而 persistAIReview
	// 是异步的。两条出口(没命中 / 命中)共用同一份,避免只有命中时才留内容 ——
	// 那恰好把最需要复核的一类("判了未违规,但看起来该拦")排除在外。
	// out.Violated 是模型这一次的判定。判了违规的行走"始终留、留完整"那一档
	// (见 reviewLogContent),所以这一步必须排在 runAIReview 之后。
	content, contentChars := reviewLogContent(snap.ai, text, out.Violated)

	v := matchAIVerdict(snap.ai, snap.promptRules, in, out, sc)
	if v == nil || v.Rule == nil {
		noteScan(false)
		// 没命中也要落审核明细:抽样跑了、钱花了,而这是唯一的痕迹。
		persistAIReview(newAIReviewRow(rc, PhasePrompt, out, 0, 0, content, contentChars))
		return nil
	}

	shadow, shadowReason := effectiveShadow(v.Rule)
	block := blocks(v.Rule.R.Action) && !shadow
	noteScan(block)

	// 拦截文案优先取**判出这次违规的那个渠道**上配的那一句。
	//
	// 项目方的原话是"这个返回文案在审核渠道里设定返回"。规则那一份留着:
	// 本地词表/正则规则根本没有渠道可言,它们的拦截文案只可能来自规则。
	// 两者都有时以渠道为准 —— 渠道是更靠近"这一次是谁判的"那一端的信息。
	//
	// 必须算在 handleHit **之前**:那一步会把这句话抄进使用记录里的那一行
	// (Record.BlockedReason)。算在后面的话,日志拿不到它,于是用户在 API 上
	// 看到渠道那句、在使用记录里看到写死的兜底那句 —— 这正是 2026-09-07 报上来的问题。
	v.BlockOverride = aiChannelBlockMessage(snap.ai, out.ChannelId)
	// 邮件通知与拦截文案取自同一个渠道,也在同一处算好:两者都是"这一次是谁判的"
	// 那一端的信息,而 newRecord 是它们唯一的消费点。
	v.EmailNotice = aiChannelEmailNotice(snap.ai, out.ChannelId)

	in.AI = out
	handleHit(c, info, PhasePrompt, in, v, shadow, shadowReason, block)
	persistAIReview(newAIReviewRow(rc, PhasePrompt, out, v.Rule.R.Id, 0, content, contentChars))

	if !block {
		return nil
	}
	return violationBlockError(v.Rule, v.BlockOverride)
}

// aiChannelBlockMessage 取某个审核渠道配的拦截文案,取不到时返回空串。
//
// 走快照而不是查库:这是热路径,而快照里本来就有(装配期抄进了 aiChannelRT)。
// 渠道刚被删掉、或这一轮解不开密钥被跳过时找不到 —— 那时返回空串,
// 调用方回落到规则自己的那一份,与这一列存在之前逐字节一致。
func aiChannelBlockMessage(rt *aiRuntime, channelId int64) string {
	if ch := rt.channelById(channelId); ch != nil {
		return ch.BlockMessage
	}
	return ""
}

// aiCategoryOverride 给出这一次命中「计次记到哪一类」的类型 id,0 = 两处都没指定。
//
// 三个候选按 **作用域 > 渠道 > 规则** 取,规则那一档由 newRecord 自己兜底
// (这里返回 0 就是它)。为什么是这个顺序:
//
//   - 作用域那一格写着「这一档的命中**一律**记为」,而"一律"没有第二种读法。
//     它同时是更窄的选择器(一组分组 + 一组模型),而同一个渠道会被多档作用域用到。
//   - 渠道压过规则,与拦截文案、通知邮件同一条理由:渠道是更靠近"这一次是谁判的"
//     那一端的信息,而一条 ai_review 规则会被分发到协议、类别体系都不同的多个渠道。
//   - 模型自己回的 category 一档都不参与。它逐次调用波动,而类型计数是封号判据的
//     一条线 —— 挂在一个不可复现的值上,"这个用户在这一类上第几次"就没有答案了。
//     它仍然完整落在 AIReview.Category / RawCategory 上,也仍然是规则类型白名单的
//     唯一判据:模型说了什么决定**命不命中**,运营配了什么决定**记成哪一类**。
//
// 渠道在快照里找不到(刚被停用、删除,或这一轮密钥解不开)时返回 0 —— 与这一列
// 存在之前逐字节一致,而不是拿一个猜来的类型去推封号线。
func aiCategoryOverride(rt *aiRuntime, channelId int64, sc *aiScopeRT) int64 {
	if id := scopeCategoryId(sc); id > 0 {
		return id
	}
	if ch := rt.channelById(channelId); ch != nil {
		return ch.CategoryId
	}
	return 0
}

// violationBlockError 构造返回给客户端的拦截错误。
//
// 提成函数是因为本地规则与 AI 审核两条路径都要构造它,而其中的两个
// ErrOption 都是**必须**的:漏掉 SkipRetry 会让一次违规被放大成 N 次上游调用,
// 漏掉 NoRecordErrorLog 会把违规拒绝算进渠道错误统计、进而触发渠道自动禁用。
// 抄第二份迟早会漏掉其中一个,而漏掉之后没有任何症状能指向这里。
// override 非空时压过规则自己那一份(AI 审核这条路上由渠道提供),
// 空串表示"不覆盖" —— 本地规则那条路恒传空串,它手上没有渠道。
func violationBlockError(cr *compiledRule, override string) error {
	return types.NewErrorWithStatusCode(
		errors.New(clientBlockMessage(cr, override)),
		types.ErrorCode(violationErrorCode()),
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
		types.ErrOptionWithNoRecordErrorLog(),
	)
}

// clientBlockMessage 算出这一次**真正回给客户端**的那句话。
//
// 提成函数是因为它有第二个调用方:newRecord 要把同一个结果抄进 Record.BlockedReason,
// 好让使用记录里那一行与用户在 API 上看到的完全一致。两边各算一次的后果不是报错,
// 而是同一次拦截在两个地方长出两句话 —— 那正是这段代码要消灭的东西。
//
// 优先级:渠道 → 规则 → 内置兜底。渠道排在最前,与 aiPreReview 的注释同源:
// 它是更靠近"这一次是谁判的"那一端的信息。
func clientBlockMessage(cr *compiledRule, override string) string {
	msg := strings.TrimSpace(override)
	if msg == "" {
		msg = cr.R.BlockMessage
	}
	if msg == "" {
		msg = defaultBlockMessage
	}
	return msg
}

// matchAIVerdict 在给定的规则桶里找出第一条被这次审核结论命中的规则。
//
// 与本地 scan 共用 applies(作用域闸)与 matchAIRule(判据),顺序也一样是
// 按优先级取第一条 —— 管理端试跑与线上判据必须逐字节相同,而"作用域"这一半
// 最容易在第二份实现里被漏掉。
//
// # 指定的类型只影响"记成哪一类",绝不影响"命中不命中"
//
// 作用域与渠道上那两格落在 verdict.CategoryOverride 上(取舍见 aiCategoryOverride),
// 由 newRecord 消费。它们**不**参与 matchAIRule 的类型白名单判定:那张白名单问的是
// "模型说了什么",而覆盖问的是"我们怎么归档"。混在一起的后果是一条作用域指定了
// 类型 X 之后,全站所有白名单为 X 的规则会突然命中这一档里的**每一次**违规判定 ——
// 一次静默的、成数量级的判据放宽,而界面上什么都没变。
func matchAIVerdict(rt *aiRuntime, rules []*compiledRule, in scanInput, out *aiOutcome, sc *aiScopeRT) *verdict {
	if out == nil || !out.decided() {
		return nil
	}
	for _, cr := range rules {
		if cr.R.MatchType != MatchAIReview || !cr.applies(in) {
			continue
		}
		terms := matchAIRule(cr, out)
		if len(terms) == 0 {
			continue
		}
		return &verdict{
			Rule: cr, Terms: terms, Snippet: out.Reason,
			CategoryOverride: aiCategoryOverride(rt, out.ChannelId, sc),
		}
	}
	return nil
}

// dispatchAIAsync 把转发后审核整个丢进异步队列。
//
// 队列满时会被 guard 丢弃并告警 —— 那正确:丢一条事后审核远好过拖垮 relay。
//
// primed 非 nil 时是同步侧已经拿到的结论,直接复用;为 nil 时在 worker 里
// 自己发一次调用(只配了转发后审核的站点走这条)。
func dispatchAIAsync(snap *snapshot, sc *aiScopeRT, rc recordCtx, in scanInput, text string, files []*types.FileMeta, primed *aiOutcome) {
	rt, rules := snap.ai, snap.asyncRules
	guard.HotAsync("violation.ai_review_async", func(ctx context.Context) error {
		gdb := db.Get()
		if gdb == nil {
			return db.ErrNotReady
		}
		// 句柄必须在这里就接上 ctx:worker 的预算(hot_async_timeout_ms)只对
		// WithContext 过的语句生效,漏接会让一条慢查询一直等到驱动层 readTimeout,
		// 期间它占着仅有的 2 个 hot worker 之一,把整条队列堵死。
		//
		// sc 与 rt 一样是快照里的只读指针:快照整体不可变、每次刷新整份替换,
		// 所以异步 worker 几百毫秒后读到的仍然是**当时**那一档的配置 ——
		// 这正确,审核问的就是那一刻的口径。
		return runAIAsyncReview(ctx, gdb.WithContext(ctx), rt, sc, rules, rc, in, text, files, primed)
	})
}

// runAIAsyncReview 是转发后审核的本体,gdb 由调用方注入。
//
// 独立成函数不是为了缩短 dispatchAIAsync,而是因为它承载的两条不变量
// **只能在这里被直接测到**:异步命中要落记录并推进计数、而且恒不扣费恒不阻断。
// 它上面那层是 guard.HotAsync —— 测试环境里扩展不可用,队列作业根本不会执行,
// 断言会永远为真(与 persistRecord 从 persist 里提出来是同一条理由)。
func runAIAsyncReview(ctx context.Context, gdb *gorm.DB, rt *aiRuntime, sc *aiScopeRT, rules []*compiledRule,
	rc recordCtx, in scanInput, text string, files []*types.FileMeta, primed *aiOutcome) error {
	out := primed
	if out == nil {
		out = runAIReview(ctx, rt, sc, text, rt.AsyncTimeoutMs)
	}
	content, contentChars := reviewLogContent(rt, text, out != nil && out.Violated)
	v := matchAIVerdict(rt, rules, in, out, sc)
	if v == nil || v.Rule == nil {
		return persistAIReviewCtx(ctx, logDB(gdb), newAIReviewRow(rc, PhasePostAsync, out, 0, 0, content, contentChars))
	}
	// 异步时机恒不阻断、恒不扣费(ValidateRule 已经把 action 钉死在 record),
	// 所以这里不走 handleHit —— 那条路会去算费、读余额、碰 gin.Context,
	// 而这三样在异步 worker 上要么不存在、要么已经属于别的请求了。
	shadow, shadowReason := effectiveShadow(v.Rule)
	// 转发后审核恒不阻断,所以这条路上没有 BlockOverride;但**通知照发** ——
	// 用户的内容确实被判成了违规、确实计了次,只是这一次没拦住他而已。
	v.EmailNotice = aiChannelEmailNotice(rt, out.ChannelId)
	in.AI = out
	// CountWeight 由 newRecord 自己算(它是唯一知道这次命中落在哪个违规类型上的
	// 地方,而"没选类型 = 不计数"这条口径就挂在那上面)。这里曾经抄一份
	// `rec.CountWeight = v.Rule.R.CountWeight`,抄本会绕过那道闸。
	rec := newRecord(rc, PhasePostAsync, in, v, shadow, shadowReason, false)
	rec.FeeStatus = FeeStatusNone

	var payload *Payload
	if v.Rule.R.ArchiveContext {
		// files 是同步抄下来的描述符切片,这里只读不改。
		payload = buildEvidence(rec, in, v, files)
		rec.HasPayload = payload != nil
	}
	if shadow {
		shadowHits.Add(1)
	}
	if err := persistRecord(ctx, gdb, rec, payload, rec.CountWeight, shadow); err != nil {
		return err
	}
	return persistAIReviewCtx(ctx, logDB(gdb), newAIReviewRow(rc, PhasePostAsync, out, v.Rule.R.Id, rec.Id, content, contentChars))
}

// newAIReviewRow 把一次调用的结果组装成待写入的审核明细。
//
// out 为 nil 也要落一行(记成 no_channel):那说明"抽中了但一次调用都没发出去",
// 而那是配置问题,必须能在成本页上看见,不能静默消失。
func newAIReviewRow(rc recordCtx, phase string, out *aiOutcome, ruleId, recordId int64,
	content string, contentChars int) *AIReview {
	if out == nil {
		out = &aiOutcome{Outcome: OutcomeNoChannel}
	}
	return &AIReview{
		// 幂等键含时机:同一个请求的两个时机是两次独立的调用、两笔独立的花费,
		// 折成一行会让成本统计少掉一半。
		ReviewNo:         fmt.Sprintf("ai_%s_%s", truncate(rc.RequestId, 48), phase),
		UserId:           rc.UserId,
		Username:         rc.Username,
		Phase:            phase,
		ChannelId:        out.ChannelId,
		ChannelName:      truncate(out.ChannelName, 64),
		ReviewModel:      truncate(out.Model, 128),
		Outcome:          out.Outcome,
		Violated:         out.Violated,
		Category:         truncate(out.Category, 64),
		RawCategory:      truncate(out.RawCategory, 64),
		Confidence:       out.Confidence,
		Reason:           out.Reason,
		PromptTokens:     out.PromptTokens,
		CompletionTokens: out.CompletionTokens,
		TotalTokens:      out.TotalTokens,
		CostUsd:          out.CostUsd,
		// 判据里的 TotalTokens > 0 不是多余的:一条一次调用都没成功的链
		// (全是连不上、或者压根没渠道)Priced 也是 false,但它的花费 0 是准的,
		// 把它标成"算不准"会让成本页的告警数字被一堆网络故障灌满。
		CostUnknown: out.TotalTokens > 0 && !out.Priced,
		LatencyMs:   out.LatencyMs,
		Attempts:    out.Attempts,
		RuleId:      ruleId,
		RecordId:    recordId,
		RequestId:   truncate(rc.RequestId, 64),
		ModelName:   rc.ModelName,
		UsingGroup:  rc.UsingGroup,
		// 内容已经在 reviewLogContent 里脱敏并截到设置的字符上限,这里不再加工。
		// 再截一次的诱惑要忍住:两处上限迟早会漂移,而漂移的症状是
		// "设置里写着 1000 字,日志里只有 500" —— 没有人查得出第二把剪刀在哪。
		Content:      content,
		ContentChars: contentChars,
		CreatedAt:    common.GetTimestamp(),
	}
}

// logDB 把一个主库句柄换成台账库句柄,并保留调用方已经接好的 ctx。
//
// 存在的理由:异步 worker 拿到的是 db.Get()(它还要写 qy_violation_record 那些
// 主库表),而审核明细住在台账库。分家之后用错句柄不会报错 —— 主库里同样有
// 一张同名表(没分家的部署迁出来的),写进去之后管理端从台账库读,永远是空的。
//
// 没分家时 db.Log() 返回的就是主库句柄,这一步是恒等的。
func logDB(gdb *gorm.DB) *gorm.DB {
	ldb := db.Log()
	if ldb == nil {
		return gdb
	}
	if gdb != nil && gdb.Statement != nil && gdb.Statement.Context != nil {
		return ldb.WithContext(gdb.Statement.Context)
	}
	return ldb
}

// persistAIReview 把审核明细交给异步队列(同步时机用)。
func persistAIReview(row *AIReview) {
	// 判据是**台账库**可用,不是主库。没分家时两者是同一件事;分家之后主库
	// 好好的、台账库熔断打开时,这一行必须被丢弃而不是排进队列 —— 队列只有
	// 4096 个槽,而热路径每一次抽中都会来这里排一次。
	if !db.LogAvailable() {
		recordDrops.Add(1)
		return
	}
	guard.HotAsync("violation.ai_review_log", func(ctx context.Context) error {
		gdb := db.Log()
		if gdb == nil {
			return db.ErrNotReady
		}
		return persistAIReviewCtx(ctx, gdb.WithContext(ctx), row)
	})
}

// persistAIReviewCtx 是落库那一段的本体。
//
// 独立出来不是为了缩短上面那个函数:异步时机已经跑在 worker 里,再套一层
// HotAsync 会让一次审核占两个队列槽,而队列只有 4096 个槽、溢出即丢弃。
func persistAIReviewCtx(ctx context.Context, gdb *gorm.DB, row *AIReview) error {
	if gdb == nil {
		return db.ErrNotReady
	}
	// review_no 唯一索引兜住重入路径(defer 重入、重试循环),冲突直接跳过。
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
	// 失败计进**台账库**自己的熔断计数。用 db.MarkFailure 会让一次日志库故障
	// 把主库的熔断也顶开,而那一刻 relay 与资金路径全都是好的
	// (没分家时 MarkLogFailure 自动转发给 MarkFailure,行为不变)。
	db.MarkLogFailure(err)
	return err
}

// ensureAISetting 补建设置行。启动期调用,幂等。
//
// 与 ensureDefaultBanPolicy 同一条理由:没有这一行时管理端会先看到一张空表,
// 让人以为"还没配、所以现在不生效"—— 而那句话恰好是对的,却看不出默认值是什么。
// 出厂值刻意是**关闭**,而且作用域策略表是空的(空表 = 不审核):AI 审核要
// 花钱、要把用户内容发往第三方,这两件事都不该由一次二进制升级替站点决定。
func ensureAISetting(ctx context.Context, gdb *gorm.DB) error {
	if gdb == nil {
		return db.ErrNotReady
	}
	now := common.GetTimestamp()
	row := AISetting{
		Id: 1, Enabled: false,
		PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
		MaxInputChars:       defaultAIMaxInputChars,
		ThirdPartyNoticeAck: false,
		// 审核日志的出厂档:留内容、留 1000 字、保 3 天。
		//
		// 与上面那三个"出厂即关闭"的字段方向相反,但不矛盾:那几个决定
		// **要不要把用户内容发出去**(要花钱、要出境,不该由一次升级替站点决定),
		// 这三个决定**已经发出去的那一次在自己库里留不留痕**。后者没有"先别开"
		// 的理由 —— 功能整体没开时它一行都不会写。
		LogContent:              true,
		LogContentViolationFull: boolPtr(true),
		LogContentMaxChars:      defaultAIReviewContentChars,
		LogRetentionDays:        defaultAIReviewRetentionDays,
		CreatedAt:               now, UpdatedAt: now,
	}
	return gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}
