package lottery

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// admin_support.go —— 管理端与证据链接口共用的读取、快照与审计辅助。

// errBadRequest 是参数类错误的统一形状。
//
// 每一种参数错误都单独造一个包级变量,只会得到一堆只用一次的符号;
// 而前端对这一整类的处理是相同的(把 message 显示在表单上方)。
func errBadRequest(msg string) *bizError {
	return newBizError(http.StatusBadRequest, "qy_lot_bad_request", msg)
}

var (
	errPayoutNotFound = newBizError(http.StatusNotFound, "qy_lot_payout_not_found", "该出款记录不存在")
	// errPayoutNeedsManual 是"这一笔不能自动重试"的诚实回答:资金单还没落定,
	// 或者它被判失败而主库探针说钱其实动了。两种情况下重开一张单都可能重复发钱,
	// 只能由人核对主库流水之后决定。
	errPayoutNeedsManual = newBizError(http.StatusConflict, "qy_lot_payout_needs_manual",
		"该笔出款的资金单尚未落定或与主库探针矛盾,不能自动重试,请先人工核对主库流水")
	errProofNotReady = newBizError(http.StatusConflict, "qy_lot_proof_not_ready",
		"该活动尚未封盘,证据链要等名单冻结之后才能生成")
	errProofDisabled = newBizError(http.StatusNotFound, "qy_lot_proof_disabled", "证据链端点已关闭")

	// errNotTextPrize 刻意不是 404:履行/撤销/揭示三个接口对**额度奖**必须明确
	// 拒绝,因为额度奖的 paid 是资金终态、永远不可撤。回 404 会让人以为只是
	// 单号打错了,然后去找一个"正确"的单号再试一次。
	errNotTextPrize = newBizError(http.StatusForbidden, "qy_lot_not_text_prize",
		"该笔不是文本奖:额度奖由出款链路自动到账,不能人工履行或撤销")
	errPrizeAlreadyFulfilled = newBizError(http.StatusConflict, "qy_lot_prize_fulfilled",
		"该文本奖已经履行过了;要更正内容请先撤销,撤销会留下不可删除的履历")
	errPrizeNotFulfilled = newBizError(http.StatusConflict, "qy_lot_prize_not_fulfilled",
		"该文本奖尚未履行")
	// errPrizeSecretUnreadable 是"我读不出来"的诚实回答,绝不把密文的字节
	// 当明文展示 —— 那会让管理员把一串乱码发给中奖者。
	errPrizeSecretUnreadable = newBizError(http.StatusInternalServerError, "qy_lot_prize_unreadable",
		"该文本奖的内容无法读取,请联系运维核对密钥版本")
)

// loadActivityAny 按活动号读取活动,**不限状态**。
//
// 与 entry.go 的 loadActivityByNo 刻意分开:那一个只服务报名路径,
// 因此把"必须是 published"编进了函数本身。管理端要能看草稿、看已结束的场次,
// 用同一个函数就会逼人去掉那个状态判定,而那是报名路径最重要的一道闸门。
func loadActivityAny(ctx context.Context, gdb *gorm.DB, actNo string) (*Activity, error) {
	if actNo == "" {
		return nil, errActivityNotFound
	}
	var a Activity
	if err := gdb.WithContext(ctx).Where("act_no = ?", actNo).Take(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errActivityNotFound
		}
		db.MarkFailure(err)
		return nil, wrapInternal("读取活动", err)
	}
	return &a, nil
}

// loadSeedForReveal 是包内**唯一**读取种子原文的函数。
//
// 它只有三个合法调用点:发布时算 commit_hash、开奖时算 final_seed、
// 揭示后把种子放进证据链。别处一律用 loadSalts 那个只取盐的投影 ——
// 把这条约束做成"只有一个函数能读"而不是"大家记得别读",
// 是因为泄漏一次就永久毁掉这场活动的公正性,而没有任何测试会因此变红。
//
// 调用方必须自己保证时机正确(揭示前绝不下发)。
func loadSeedForReveal(ctx context.Context, gdb *gorm.DB, actId int64) (string, error) {
	var s Seed
	if err := gdb.WithContext(ctx).Where("act_id = ?", actId).Take(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", wrapInternal("读取活动随机源", errors.New("随机源不存在"))
		}
		db.MarkFailure(err)
		return "", wrapInternal("读取活动随机源", err)
	}
	if s.Seed == "" {
		return "", wrapInternal("读取活动随机源", errors.New("随机源为空"))
	}
	return s.Seed, nil
}

// loadRoster 按 entry_no 字节序升序流式读出有效名单。
//
// 排序必须由数据库用 entry_no 做,而不是先取出来再在内存里排:证据链的
// roster_hash 依赖这个顺序,任何一处顺序不一致都会让第三方复算失败。
// 上限由 lottery.max_total_entries_hard 保证(名单冻结要在单事务里算完)。
func loadRoster(ctx context.Context, gdb *gorm.DB, actId int64) ([]Entry, error) {
	rows := make([]Entry, 0, 256)
	err := gdb.WithContext(ctx).
		Where("act_id = ? AND status = ?", actId, EntrySuccess).
		Order("entry_no asc").Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取有效名单", err)
	}
	return rows, nil
}

// rosterLines 把参与明细投影成证据链要的最小行。
func rosterLines(rows []Entry) []RosterLine {
	out := make([]RosterLine, 0, len(rows))
	for _, e := range rows {
		out = append(out, RosterLine{
			EntryNo: e.EntryNo, UserRef: e.UserRef, OptNo: e.OptNo, Amount: e.Amount,
			Pick: e.Pick,
		})
	}
	return out
}

// writeActivityEvent 往事件流写一行。
//
// 事件流**对用户可见、属于证据链**,与 qianye/service/audit 是两回事:
// 那是跨模块管理台账,可以按保留期清理。两边都要写。
func writeActivityEvent(tx *gorm.DB, actId int64, from, to, action, actorType string,
	actorUserId int, detail map[string]any) error {
	return tx.Create(&Event{
		ActId:       actId,
		FromStatus:  from,
		ToStatus:    to,
		Action:      action,
		ActorType:   actorType,
		ActorUserId: actorUserId,
		Detail:      snapText(detail),
		CreatedAt:   common.GetTimestamp(),
	}).Error
}

// snapText 把任意值编码成审计/事件里的快照文本。
//
// 序列化失败返回显式标记而不是空串:空串在详情里与"本来就没有快照"
// (新增操作的 before)无法区分,而这两件事的含义相反。
func snapText(v any) string {
	if v == nil {
		return ""
	}
	b, err := common.Marshal(v)
	if err != nil {
		return "<snapshot marshal failed: " + err.Error() + ">"
	}
	return string(b)
}

// activitySnapshot 显式挑字段构造活动快照。
//
// **绝不整行序列化 Activity**:那是本仓反复出现的泄漏形状,而且一旦有人给
// Activity 加一个敏感列(比如把种子挪回来),整行序列化会立刻把它写进审计表。
// in 非 nil 时补上本次提交的奖档/选项摘要 —— 那是"改的是什么"的答案。
func activitySnapshot(a *Activity, in *activityInput) map[string]any {
	m := map[string]any{}
	if a != nil {
		m["act_no"] = a.ActNo
		m["kind"] = a.Kind
		m["status"] = a.Status
		m["outcome"] = a.Outcome
		m["title"] = a.Title
		m["stake_quota"] = a.StakeQuota
		m["open_at"] = a.OpenAt
		m["close_at"] = a.CloseAt
		m["draw_at"] = a.DrawAt
		m["settle_deadline"] = a.SettleDeadline
		m["fee_bps"] = a.FeeBps
		m["allow_multi_win"] = a.AllowMultiWin
		m["min_entries_to_hold"] = a.MinEntriesToHold
		m["rules_hash"] = a.RulesHash
		m["spec_hash"] = a.SpecHash
		m["commit_hash"] = a.CommitHash
	}
	if in != nil {
		m["prize_count"] = len(in.Prizes)
		m["option_count"] = len(in.Options)
		m["rules"] = in.Rules
	}
	return m
}

func payoutSnapshot(p *Payout) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"payout_no": p.PayoutNo,
		"kind":      p.Kind,
		"status":    p.Status,
		"user_id":   p.UserId,
		"amount":    p.AmountQuota,
		"attempts":  p.Attempts,
		"last_err":  p.LastError,
	}
}

// writeAdminAudit 落一条管理端审计。
//
// 成功与失败共用同一条路径:"有人在这一刻试图做这件事、失败了"是最需要留痕的
// 事实之一,而只写成功那一条正是本仓被修掉过的缺陷形状 ——
// 接口照常返回、业务照常生效,只有事后要复盘时才发现库里什么都没有。
func writeAdminAudit(c *gin.Context, action, traceNo, result, reason, before, after string) {
	audit.Write(c, audit.Entry{
		TraceNo:     traceNo,
		Category:    auditCategory,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      audit.Truncate(reason, 500),
		BeforeSnap:  before,
		AfterSnap:   after,
	})
}

// writeSystemAudit 落一条系统侧审计(定时任务、结算路径)。
//
// ActorType=system 必须能与人工操作区分,否则事故复盘时分不清是人干的
// 还是程序干的。
func writeSystemAudit(action, traceNo, result, reason, after string) {
	audit.Write(nil, audit.Entry{
		TraceNo:   traceNo,
		Category:  auditCategory,
		Action:    action,
		ActorType: qymodel.ActorSystem,
		Result:    result,
		Reason:    audit.Truncate(reason, 500),
		AfterSnap: after,
	})
}

// auditReason 从错误里取出可以写进审计的理由。
//
// 业务错误取 code + 文案(它们是稳定的);内部错误取原文 —— 审计表只有管理员
// 能看,而"到底是哪个语句失败了"正是复盘时要的东西。
func auditReason(err error) string {
	if err == nil {
		return ""
	}
	if be, ok := AsBizError(err); ok {
		return be.ErrCode() + ": " + be.Message()
	}
	return err.Error()
}

// ─────────────────────────── 竞猜结算 ───────────────────────────

// settleGuessResult 把竞猜结果钉死并落下出款计划。
//
// **单个扩展库事务,一分钱不动**:这里只往 qy_lot_payout 写 planned 行,
// 真正的出款由 worker 逐笔驱动。绝不在这里循环 twophase.Execute ——
// 那会让一次结算变成 N 次跨库调用,任何一次中途失败都留下一半发一半没发的场面。
//
// 分配算法与守恒断言在 SplitPool 里(纯函数,可被第三方复算)。
// 断言不成立就整个事务回滚 + 告警 + 落异常,绝不发出一笔对不上账的钱。
func settleGuessResult(ctx context.Context, act *Activity, opt *Option, evidence string, adminId int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	// 句柄一次性绑上调用方的预算,后续语句与被传入的子函数都带着它。
	gdb = gdb.WithContext(ctx)

	rows, err := loadRoster(ctx, gdb, act.Id)
	if err != nil {
		return err
	}
	all := rosterLines(rows)

	var pool int64
	winners := make([]RosterLine, 0, len(all))
	for _, l := range all {
		pool += l.Amount
		if l.OptNo == opt.OptNo {
			winners = append(winners, l)
		}
	}
	// 与物化计数交叉核对。对不上说明报名路径的计数回滚出了问题,
	// 此时任何分配结果都建立在一个错误的池子上。
	if pool != act.PoolQuota {
		raiseFlag(ctx, act.Id, FlagPoolMismatch,
			"结算时重算奖池与物化计数不一致: 重算 "+snapText(pool)+" 物化 "+snapText(act.PoolQuota))
		return wrapInternal("结算竞猜", errors.New("奖池与物化计数不一致,已挂起等待人工核对"))
	}

	fee, shares, err := SplitPool(pool, act.FeeBps, all, winners)
	if err != nil {
		raiseFlag(ctx, act.Id, FlagPoolMismatch, err.Error())
		return wrapInternal("分配奖池", err)
	}

	byEntryNo := make(map[string]*Entry, len(rows))
	for i := range rows {
		byEntryNo[rows[i].EntryNo] = &rows[i]
	}
	plans := make([]PayoutPlan, 0, len(shares))
	var payoutSum, refundSum int64
	for _, s := range shares {
		e := byEntryNo[s.EntryNo]
		if e == nil {
			return wrapInternal("分配奖池", errors.New("份额指向了不存在的参与明细 "+s.EntryNo))
		}
		kind := PayoutWin
		if s.Refund {
			kind = PayoutRefund
			refundSum += s.Amount
		} else {
			payoutSum += s.Amount
		}
		plans = append(plans, PayoutPlan{
			EntryId: e.Id, UserId: e.UserId, Kind: kind, Amount: s.Amount,
		})
	}
	// 出款顺序按 entry_no 升序,与 proof 里的顺序一致 —— 让"谁先到账"
	// 也是一个可复算的事实,而不是数据库返回顺序的副产品。
	sort.SliceStable(plans, func(i, j int) bool { return plans[i].EntryId < plans[j].EntryId })

	outcome := OutcomeDrawn
	switch {
	case len(winners) == 0:
		// 全部猜错:全额退回本金,手续费一分不收。见 SplitPool 的说明 ——
		// 只要平台在"没人猜中"时有收益,它就有动机去设置不可能达成的选项。
		outcome = OutcomeVoidNoWinner
	case fee == 0 && len(shares) == len(all):
		// 无输家 / 无对手盘,同口径全额退回。
		outcome = OutcomeVoidAllCorrect
	}

	now := common.GetTimestamp()
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Activity{}).
			Where("id = ? AND status = ? AND win_option_id = ?", act.Id, StatusLocked, 0).
			Updates(map[string]any{
				"status":             StatusSettling,
				"outcome":            outcome,
				"win_option_id":      opt.Id,
				"result_evidence":    audit.Truncate(evidence, 1000),
				"result_by":          adminId,
				"platform_fee_quota": fee,
				"updated_at":         now,
			})
		if res.Error != nil {
			return res.Error
		}
		// CAS 落空 = 结果已被别的路径录入(或活动已被取消)。绝不覆盖:
		// 结果一经写入不可修改是写在活动规则页上的承诺。
		if res.RowsAffected != 1 {
			return errResultLocked
		}
		if err := tx.Model(&Option{}).
			Where("id = ?", opt.Id).Update("is_winner", true).Error; err != nil {
			return err
		}
		if err := PlanPayouts(tx, act.Id, plans); err != nil {
			return err
		}
		return writeActivityEvent(tx, act.Id, StatusLocked, StatusSettling, ActionGuessResult,
			qymodel.ActorAdmin, adminId, map[string]any{
				"win_opt_no":   opt.OptNo,
				"pool_quota":   pool,
				"fee_quota":    fee,
				"payout_quota": payoutSum,
				"refund_quota": refundSum,
				"outcome":      outcome,
				"evidence":     evidence,
			})
	})
	if err != nil {
		if !errors.Is(err, errResultLocked) {
			db.MarkFailure(err)
			return wrapInternal("落下竞猜出款计划", err)
		}
		return err
	}
	return nil
}
