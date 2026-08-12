package violation

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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

	v := matchAIVerdict(snap.promptRules, in, out, sc)
	if v == nil || v.Rule == nil {
		noteScan(false)
		// 没命中也要落审核明细:抽样跑了、钱花了,而这是唯一的痕迹。
		persistAIReview(newAIReviewRow(rc, PhasePrompt, out, 0, 0))
		return nil
	}

	shadow, shadowReason := effectiveShadow(v.Rule)
	block := blocks(v.Rule.R.Action) && !shadow
	noteScan(block)

	in.AI = out
	handleHit(c, info, PhasePrompt, in, v, shadow, shadowReason, block)
	persistAIReview(newAIReviewRow(rc, PhasePrompt, out, v.Rule.R.Id, 0))

	if !block {
		return nil
	}
	return violationBlockError(v.Rule)
}

// violationBlockError 构造返回给客户端的拦截错误。
//
// 提成函数是因为本地规则与 AI 审核两条路径都要构造它,而其中的两个
// ErrOption 都是**必须**的:漏掉 SkipRetry 会让一次违规被放大成 N 次上游调用,
// 漏掉 NoRecordErrorLog 会把违规拒绝算进渠道错误统计、进而触发渠道自动禁用。
// 抄第二份迟早会漏掉其中一个,而漏掉之后没有任何症状能指向这里。
func violationBlockError(cr *compiledRule) error {
	msg := cr.R.BlockMessage
	if msg == "" {
		msg = defaultBlockMessage
	}
	return types.NewErrorWithStatusCode(
		errors.New(msg),
		types.ErrorCode(violationErrorCode()),
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
		types.ErrOptionWithNoRecordErrorLog(),
	)
}

// matchAIVerdict 在给定的规则桶里找出第一条被这次审核结论命中的规则。
//
// 与本地 scan 共用 applies(作用域闸)与 matchAIRule(判据),顺序也一样是
// 按优先级取第一条 —— 管理端试跑与线上判据必须逐字节相同,而"作用域"这一半
// 最容易在第二份实现里被漏掉。
//
// # 作用域指定的类型只影响"记成哪一类",绝不影响"命中不命中"
//
// sc.CategoryId 落在 verdict.CategoryOverride 上,由 newRecord 消费。它**不**
// 参与 matchAIRule 的类型白名单判定:那张白名单问的是"模型说了什么",而覆盖
// 问的是"我们怎么归档"。混在一起的后果是一条作用域指定了类型 X 之后,
// 全站所有白名单为 X 的规则会突然命中这一档里的**每一次**违规判定 ——
// 一次静默的、成数量级的判据放宽,而界面上什么都没变。
func matchAIVerdict(rules []*compiledRule, in scanInput, out *aiOutcome, sc *aiScopeRT) *verdict {
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
			CategoryOverride: scopeCategoryId(sc),
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
	v := matchAIVerdict(rules, in, out, sc)
	if v == nil || v.Rule == nil {
		return persistAIReviewCtx(ctx, gdb, newAIReviewRow(rc, PhasePostAsync, out, 0, 0))
	}
	// 异步时机恒不阻断、恒不扣费(ValidateRule 已经把 action 钉死在 record),
	// 所以这里不走 handleHit —— 那条路会去算费、读余额、碰 gin.Context,
	// 而这三样在异步 worker 上要么不存在、要么已经属于别的请求了。
	shadow, shadowReason := effectiveShadow(v.Rule)
	in.AI = out
	rec := newRecord(rc, PhasePostAsync, in, v, shadow, shadowReason, false)
	rec.FeeStatus = FeeStatusNone
	rec.CountWeight = v.Rule.R.CountWeight

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
	return persistAIReviewCtx(ctx, gdb, newAIReviewRow(rc, PhasePostAsync, out, v.Rule.R.Id, rec.Id))
}

// newAIReviewRow 把一次调用的结果组装成待写入的审核明细。
//
// out 为 nil 也要落一行(记成 no_channel):那说明"抽中了但一次调用都没发出去",
// 而那是配置问题,必须能在成本页上看见,不能静默消失。
func newAIReviewRow(rc recordCtx, phase string, out *aiOutcome, ruleId, recordId int64) *AIReview {
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
		LatencyMs:        out.LatencyMs,
		RuleId:           ruleId,
		RecordId:         recordId,
		RequestId:        truncate(rc.RequestId, 64),
		ModelName:        rc.ModelName,
		UsingGroup:       rc.UsingGroup,
		CreatedAt:        common.GetTimestamp(),
	}
}

// persistAIReview 把审核明细交给异步队列(同步时机用)。
func persistAIReview(row *AIReview) {
	if !db.Available() {
		recordDrops.Add(1)
		return
	}
	guard.HotAsync("violation.ai_review_log", func(ctx context.Context) error {
		gdb := db.Get()
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
	return gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
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
		Prompt: "", MaxInputChars: defaultAIMaxInputChars,
		ThirdPartyNoticeAck: false,
		CreatedAt:           now, UpdatedAt: now,
	}
	return gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}
