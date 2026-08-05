package lottery

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// lifecycle.go —— 活动状态机的自动推进:封盘 → 揭示 → 结算 → 完成。
//
// # 为什么这四步全部由定时任务触发,管理端一个按钮都没有
//
// 抽奖结果 = f(种子, 冻结名单, 规则) 的确定函数,**与执行时刻完全无关**。
// 但"什么时候封盘"和"什么时候开奖"如果由人按按钮,管理员就能在名单对自己
// 有利的那一刻按下去 —— 选时攻击。所以 close_at 与 draw_at 在 publish 时
// 进承诺哈希,之后只有租约任务按时间触发,管理端**没有**提前截止、
// 也没有立即开奖的接口。
//
// 管理员剩下的唯一动作是整场取消,而取消必然全额退款、必然公示、必然写审计:
// 他只能"不开",不能"挑一个开"。
//
// 与此配套的一条铁律:**绝不允许把开奖时才知道的量(区块哈希、开奖时的
// 时间戳、最后一笔投注)混进随机源** —— 那正是把选时攻击重新引进来的经典错误。

// batchPerRound 是每轮处理的活动数上界。
// 一轮跑不完的留给下一轮:租约任务宁可慢一点,也不要一轮占住连接几分钟。
const batchPerRound = 20

// ─────────────────────────── 封盘 ───────────────────────────

// runLock 到点封盘。
//
// 封盘那一刻做三件事,而且必须在同一个事务里:
//  1. 状态 CAS published → locked
//  2. 仍是 pending 的参与标成 excluded(它们的钱去哪由资金单的终态决定)
//  3. 按 entry_no 字节序算出 roster_hash 并**立即落库公开**
//
// 第 3 步先于种子公开是整个协议的关键:任何人在 close_at 到 draw_at 之间
// 抓一份 proof,就持有了一份平台无法否认的名单快照。
func runLock(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算:逐条 WithContext 漏一条,就等于在这条链路上开了一个
	// 没有上界的口子 —— 语句级预算只对 WithContext 的语句生效。
	gdb = gdb.WithContext(ctx)
	now := common.GetTimestamp()
	var rows []Activity
	if err := gdb.WithContext(ctx).
		Where("status = ? AND close_at <= ?", StatusPublished, now).
		Order("id asc").Limit(batchPerRound).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/lottery: 扫描待封盘活动失败: " + err.Error())
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := lockActivity(ctx, gdb, &rows[i]); err != nil {
			common.SysError(fmt.Sprintf("qianye/lottery: 活动 %s 封盘失败: %v", rows[i].ActNo, err))
		}
	}
}

// lockActivity 封盘。
//
// 名单**必须在事务内、且在 pending→excluded 清扫之后**才读:
// 在事务外先算 roster_hash,读完到事务开始之间任何一笔在途参与落定成 success,
// 就会得到一条既不在冻结名单里、也没被标 excluded 的条目 —— 到了开奖时刻,
// revealActivity 重算的名单与已公开的快照对不上,活动被自己的防篡改校验
// 永久拒绝开奖,全场的钱既不派也不退。清扫的 UPDATE 会在那些 pending 行上取锁,
// 并发的 markEntrySuccess 要么排在它前面(那就进名单)、要么排在它后面
// (那就撞上 excluded 而不生效),没有第三种可能。
func lockActivity(ctx context.Context, gdb *gorm.DB, act *Activity) error {
	now := common.GetTimestamp()

	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Activity{}).
			Where("id = ? AND status = ?", act.Id, StatusPublished).
			Updates(map[string]any{
				"status":     StatusLocked,
				"locked_at":  now,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// 别的节点抢先了(或活动刚被取消)。不是错误。
			return nil
		}

		excluded, err := excludePendingEntries(tx, act.Id, now)
		if err != nil {
			return err
		}

		roster, err := loadRoster(ctx, tx, act.Id)
		if err != nil {
			return err
		}
		hash, count := RosterHashFor(act.Algo, act.ActNo, act.CommitHash, rosterLines(roster))

		// 参与人数不足即流局全退。这是平台侧唯一的止损阀,且对用户完全公平。
		shortfall := act.MinEntriesToHold > 0 && count < act.MinEntriesToHold
		to := StatusLocked
		frozen := map[string]any{
			"roster_hash":  hash,
			"roster_count": count,
			"updated_at":   now,
		}
		if shortfall {
			// 直接跳过揭示进入结算:名单都不够,抽出来的名次没有意义,
			// 而种子一旦公开就再也不能用于同一场活动。
			to = StatusSettling
			frozen["status"] = StatusSettling
			frozen["outcome"] = OutcomeVoidMinEntries
		}
		if err := tx.Model(&Activity{}).Where("id = ?", act.Id).Updates(frozen).Error; err != nil {
			return err
		}

		return writeActivityEvent(tx, act.Id, StatusPublished, to, ActionLock,
			qymodel.ActorSystem, 0, map[string]any{
				"roster_hash":  hash,
				"roster_count": count,
				"excluded":     excluded,
				"shortfall":    shortfall,
			})
	})
}

// excludePendingEntries 把仍未落定的参与标成 excluded 并一次性回落 pending_count。
//
// **这里不登记退款**:那笔资金单可能最终判定为 Failed(主库根本没扣钱),
// 退一笔从没收过的钱会在资金表里留下一条假的成功记录。真正的去向由
// convergeExcluded 按资金单的终态决定。
//
// 封盘与"整场取消"两条路径共用它。取消若跳过这一步,在途的 pending 条目就
// 永远没人收敛:convergeExcluded 只处理 excluded,而 finishIfDone 把 pending
// 计入未结算 —— 活动会永久停在 settling,连带永久占用一个并发活动名额。
func excludePendingEntries(tx *gorm.DB, actId, now int64) (int64, error) {
	ex := tx.Model(&Entry{}).
		Where("act_id = ? AND status = ?", actId, EntryPending).
		Updates(map[string]any{"status": EntryExcluded, "settled_at": now})
	if ex.Error != nil {
		return 0, ex.Error
	}
	if ex.RowsAffected == 0 {
		return 0, nil
	}
	// pending_count 在这里一次性回落。convergeExcluded 之后不再动它,
	// 否则同一条参与会被扣两次计数。
	err := tx.Model(&Activity{}).Where("id = ?", actId).
		Update("pending_count", gorm.Expr("CASE WHEN pending_count >= ? THEN pending_count - ? ELSE 0 END",
			ex.RowsAffected, ex.RowsAffected)).Error
	if err != nil {
		return 0, err
	}
	return ex.RowsAffected, nil
}

// ─────────────────────────── 揭示与开奖 ───────────────────────────

// runReveal 到点揭示种子并抽出名单。
//
// 只处理抽奖:竞猜没有抽签,封盘之后等管理员录结果(或到 settle_deadline 自动流局)。
func runReveal(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算:逐条 WithContext 漏一条,就等于在这条链路上开了一个
	// 没有上界的口子 —— 语句级预算只对 WithContext 的语句生效。
	gdb = gdb.WithContext(ctx)
	now := common.GetTimestamp()
	var rows []Activity
	if err := gdb.WithContext(ctx).
		Where("status = ? AND kind = ? AND draw_at <= ? AND outcome = ?",
			StatusLocked, KindDraw, now, OutcomeNone).
		Order("id asc").Limit(batchPerRound).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/lottery: 扫描待开奖活动失败: " + err.Error())
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := revealActivity(ctx, gdb, &rows[i]); err != nil {
			common.SysError(fmt.Sprintf("qianye/lottery: 活动 %s 开奖失败: %v", rows[i].ActNo, err))
		}
	}
}

// revealActivity 执行一次开奖。**单个扩展库事务,一分钱不动。**
//
// 出款只落 planned 计划行,真正动钱由 worker 逐笔驱动 —— 绝不在这里循环
// twophase.Execute,那会让一次开奖变成 N 次跨库往返,任何一次中途失败
// 都留下一半发一半没发的场面。
//
// 重复触发被三重挡住:lease 单节点 + 状态 CAS + uk(act_id, entry_id, kind)。
func revealActivity(ctx context.Context, gdb *gorm.DB, act *Activity) error {
	// 揭示延迟必须按**实际封盘时刻**再算一次,不能只靠创建时校验过的
	// draw_at - close_at。封盘任务落后时(节点重启、租约易主、一批活动同时到点)
	// 名单哈希可能刚刚才公开几秒,而 draw_at 早已过去 —— 那时立刻揭示种子,
	// "任何人都能在揭示前抓一份平台无法否认的名单快照"这条地基就不成立了。
	// 补足间隔只是让这一场晚开几十秒,而这正是承诺-揭示协议的全部价值所在。
	if delay := int64(config.Get().Lottery.RevealDelaySeconds); act.LockedAt > 0 && delay > 0 {
		if ready := act.LockedAt + delay; common.GetTimestamp() < ready {
			return nil
		}
	}

	seedHex, err := loadSeedForReveal(ctx, gdb, act.Id)
	if err != nil {
		return err
	}

	// 机械校验:承诺对不上一律**拒绝开奖**,绝不"以种子为准"继续。
	//
	// 它拦得住"改了种子忘了改哈希"这一类真实事故,也拦得住有人直接改库。
	// 注意校验的是完整的 CommitHash 原像,而不只是 sha256(seed):承诺覆盖的是
	// 随机源 + 参与条件 + 奖档 + 四个时刻 + 每一个影响结果的布尔,少验一项
	// 就等于允许管理员在不碰种子的前提下改掉其中一项。
	// 期次快照必须原样参与复算:双色球的号池、期号与开局池子都在承诺原像里,
	// 少传一段就等于允许运营在发布之后把池子改大、把号池改小。
	// 非 ball 活动这些列恒为零值,复算结果与 v1/无期次时完全一致。
	if want := CommitHashFor(act, seriesSnapshotOf(act), seedHex); want != act.CommitHash {
		suspendReveal(ctx, act, "承诺哈希校验失败:重算 "+want+" 落库 "+act.CommitHash)
		return errCommitMismatch
	}

	roster, err := loadRoster(ctx, gdb, act.Id)
	if err != nil {
		return err
	}
	lines := rosterLines(roster)
	hash, count := RosterHashFor(act.Algo, act.ActNo, act.CommitHash, lines)
	// 名单在封盘时已经公开过一次。这里重算比对是"名单被事后改动"的
	// 唯一自动检出手段 —— 对不上就停手,绝不用一份与已公开快照不同的名单开奖。
	if hash != act.RosterHash || count != act.RosterCount {
		// 用 errRosterDrift 而不是 errCommitMismatch:两者的排障方向完全相反。
		// 名单漂移最常见的成因是并发时序,种子与承诺一个字节都没动 ——
		// 而顶层日志若写着"种子与承诺哈希不一致",第一小时的排查就会花在
		// 一次根本没有发生的篡改上,同时全场资金还冻着。
		suspendReveal(ctx, act, fmt.Sprintf(
			"名单与封盘时的快照不一致:重算 %s(%d) 已公开 %s(%d)",
			hash, count, act.RosterHash, act.RosterCount))
		return wrapInternal("开奖", errRosterDrift)
	}

	var prizes []Prize
	if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).
		Order("tier asc").Find(&prizes).Error; err != nil {
		db.MarkFailure(err)
		return wrapInternal("读取奖档", err)
	}
	// 奖档表在开奖前**必须**重算一次原像。
	//
	// 承诺哈希覆盖的是 spec_hash,而 spec_hash 是发布那一刻算好后落在活动行上的
	// 一个字符串 —— 只核 commit=H(...spec_hash...) 证明的是"这个字符串没被改",
	// 不是"奖档表没被改"。封盘之后 roster_hash 已公开、final_seed 对持有种子的
	// 人已可算,此刻改一行 win_ppm 就等于点名挑中奖者(每张票的 r 已确定,把区间
	// 挪到覆盖目标票即可),改 amount_quota 则绕过发布期
	// Σ(count×amount) ≤ max_total_prize_quota 那道净增发闸门。
	// 两者原本都只有"用户自己下载证据链跑脚本"才能发现。
	if err := checkSpecIntegrity(act, prizes); err != nil {
		suspendReveal(ctx, act, err.Error())
		return wrapInternal("开奖", errCommitMismatch)
	}
	tiers := make([]Tier, 0, len(prizes))
	for _, p := range prizes {
		tiers = append(tiers, Tier{
			Tier: p.Tier, Count: p.Count, Amount: p.AmountQuota,
			PrizeType: p.Type(), WinPpm: p.WinPpm,
		})
	}

	final := FinalSeed(act.ActNo, seedHex, act.RosterHash, act.RosterCount, act.Algo)
	winners, err := pickWinnersByMode(act, final, lines, tiers, prizes)
	if err != nil {
		// 概率之和越界、或均分把某个人摊薄到 0:两者都意味着这一场根本不该被
		// 开出去。挂起等人,绝不发一份算不平的名单。
		suspendReveal(ctx, act, "抽取中奖名单失败: "+err.Error())
		return wrapInternal("开奖", err)
	}

	byEntryNo := make(map[string]*Entry, len(roster))
	for i := range roster {
		byEntryNo[roster[i].EntryNo] = &roster[i]
	}
	plans := make([]PayoutPlan, 0, len(winners))
	var payoutSum int64
	textGrants := 0
	for _, w := range winners {
		e := byEntryNo[w.EntryNo]
		if e == nil {
			return wrapInternal("开奖", fmt.Errorf("中奖位指向了不存在的参与明细 %s", w.EntryNo))
		}
		// 两条派奖腿在这里分开,而且**只在这里**分开:额度奖落 planned 交给
		// twophase 的资金链路,文本奖落 granted(终态)等人工履行。
		// 两者共用同一个 uk(act_id, entry_id, kind) 与同一次 ON CONFLICT DO NOTHING,
		// 因此"开奖跑两遍"在计划层整体撞键,不需要任何额外的协调。
		kind := PayoutPrize
		if w.PrizeType == PrizeTypeText {
			kind = PayoutText
			textGrants++
		} else {
			payoutSum += w.Amount
		}
		plans = append(plans, PayoutPlan{
			EntryId: e.Id, UserId: e.UserId, Kind: kind,
			Tier: w.Tier, DrawPos: w.Pos, Amount: w.Amount,
		})
	}

	now := common.GetTimestamp()
	revealed := map[string]any{
		"status":           StatusSettling,
		"outcome":          OutcomeDrawn,
		"revealed_at":      now,
		"payout_quota":     payoutSum,
		"text_grant_count": textGrants,
		"updated_at":       now,
	}
	if act.DrawMode == DrawModeBall {
		reds, blues := ballResultOf(act, final)
		revealed["ball_result"] = BallResultText(reds, blues)
		// 手续费是本期投注里**没有**进池子的那一部分,与滚存和注资无关 ——
		// 滚存零费率,否则平台会反复抽自己的种子钱,池子在一次都没派出去之前
		// 就被费率蚕食干净。
		revealed["platform_fee_quota"] = act.PoolQuota - ballPoolIn(act)
	}
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Activity{}).
			Where("id = ? AND status = ? AND outcome = ?", act.Id, StatusLocked, OutcomeNone).
			Updates(revealed)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return nil // 别的节点抢先了
		}
		if err := tx.Model(&Seed{}).Where("act_id = ?", act.Id).
			Update("revealed_at", now).Error; err != nil {
			return err
		}
		if err := PlanPayouts(tx, act.Id, plans); err != nil {
			return err
		}
		return writeActivityEvent(tx, act.Id, StatusLocked, StatusSettling, ActionReveal,
			qymodel.ActorSystem, 0, map[string]any{
				"final_seed":   final,
				"roster_hash":  act.RosterHash,
				"draw_mode":    act.DrawMode,
				"winner_count": len(winners),
				"payout_quota": payoutSum,
				"text_grants":  textGrants,
			})
	})
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("开奖", err)
	}
	writeSystemAudit("lottery.reveal", act.ActNo, qymodel.ResultOK, "",
		snapText(map[string]any{
			"final_seed": final, "winners": len(winners), "payout_quota": payoutSum,
			"draw_mode": act.DrawMode, "text_grants": textGrants,
		}))
	return nil
}

// pickWinnersByMode 按定档方式分派抽取算法。
//
// rank 与 prob 共用**完全相同**的票面推导(FinalSeed → Ticket,一个字节都没变),
// 区别只在读票面的那把尺子:rank 按票面排序取前 N,prob 把票面前 64 位折算成
// 一个六位摇号结果再落区间。刻意不引入第二个随机源 —— 多一个源就多一处
// 需要独立证明其公正性的地方。
//
// 未知的 draw_mode 一律报错而不是回落成 rank:回落会让一场按"概率制"公示的
// 活动被按名次开出去,而用户看到的规则页与实际执行的规则不是同一套。
// ball 是第三套语义:随机量的作用域从"每张票"扩大到"整个活动"—— 全场共摇一次,
// 一张票中不中由它自己选的号与开奖号的匹配数决定,与别人买了什么完全无关。
// 随机源仍然是同一个 final_seed,只是换了一个域前缀(qylot-ball-v2)。
func pickWinnersByMode(act *Activity, final string, lines []RosterLine, tiers []Tier, prizes []Prize) ([]Winner, error) {
	switch act.DrawMode {
	case "", DrawModeRank:
		return PickWinners(final, act.ActNo, lines, tiers, act.AllowMultiWin), nil
	case DrawModeProb:
		return PickWinnersProb(final, act.ActNo, lines, tiers)
	case DrawModeBall:
		reds, blues := ballResultOf(act, final)
		return PickWinnersBall(reds, blues, lines, ballTiersOf(prizes), ballPoolOpen(act))
	}
	return nil, fmt.Errorf("未知的定档方式 %q", act.DrawMode)
}

// ballResultOf 摇出本期的开奖号。
//
// 它是 (final_seed, act_no, 号池) 的**纯函数**,因此可以在需要的地方各算一次
// 而不必在调用链里传来传去 —— 代价是 ≤ 52 次 HMAC,收益是没有任何一处能把
// "算出来的号"与"落库展示的号"弄成两个不同的值。
func ballResultOf(act *Activity, final string) ([]int, []int) {
	reds := BallDraw(final, act.ActNo, BallColorRed, act.BallRedPool, act.BallRedPick)
	blues := BallDraw(final, act.ActNo, BallColorBlue, act.BallBluePool, act.BallBluePick)
	return reds, blues
}

func ballTiersOf(prizes []Prize) []BallTier {
	out := make([]BallTier, 0, len(prizes))
	for _, p := range prizes {
		out = append(out, BallTier{
			Tier: p.Tier, RedMatch: p.RedMatch, BlueMatch: p.BlueMatch,
			Count: p.Count, Amount: p.AmountQuota,
			PoolShareBps: p.PoolShareBps, PrizeType: p.Type(),
		})
	}
	return out
}

// ballCommittedQuota 是本期已登记的派奖总额(**不看出款状态**)。
//
// 它是"这一期从池子里承诺出去多少"的权威口径:一笔卡在 held 的出款仍然算数,
// 因为它随时可能被管理员重试成功。非双色球恒返回 0。
func ballCommittedQuota(ctx context.Context, gdb *gorm.DB, act *Activity) (int64, error) {
	if act.DrawMode != DrawModeBall || act.SeriesId == 0 {
		return 0, nil
	}
	var total int64
	err := gdb.WithContext(ctx).Model(&Payout{}).
		Select("COALESCE(SUM(amount_quota), 0)").
		Where("act_id = ? AND kind = ?", act.Id, PayoutPrize).
		Scan(&total).Error
	if err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	return total, nil
}

func ballCarryOf(act *Activity, committed int64) int64 {
	if act.DrawMode != DrawModeBall {
		return 0
	}
	return ballPoolOpen(act) - committed
}

// suspendReveal 在承诺校验失败时挂起活动并告警。
//
// **绝不自动作废、绝不自动开奖**:两者都是在一个已知被篡改的现场上继续动钱。
// 落一条 flag + 一条失败审计,交给人。
func suspendReveal(ctx context.Context, act *Activity, reason string) {
	common.SysError(fmt.Sprintf("qianye/lottery: 活动 %s 拒绝开奖: %s", act.ActNo, reason))
	raiseFlag(ctx, act.Id, FlagRevealRefuse, reason)
	writeSystemAudit("lottery.reveal", act.ActNo, qymodel.ResultFail, reason, "")
}

// checkSpecIntegrity 从奖档表**重算** spec 原像,与活动行上已公示的 spec_hash 比对。
//
// 这是"公示的奖品与概率为真"这条主张的执行点。没有它,承诺链只证明了
// spec_hash 这个字符串没被改,而 qy_lot_prize 的行是可以随便改的 ——
// 而抽取名单、算金额读的是**行**,不是那个字符串。
//
// 只对抽奖生效:竞猜的 spec 是选项表,它的漂移由 settleGuess 那一侧的
// win_option_id 归属校验覆盖,而且竞猜没有"事后改一个数就能挑中奖者"的形状
// (赔付是按池子比例连续分配的)。
func checkSpecIntegrity(act *Activity, prizes []Prize) error {
	if act.Kind != KindDraw {
		return nil
	}
	lines := make([]string, 0, len(prizes))
	for _, p := range prizes {
		lines = append(lines, prizeSpecLineOf(act.Algo, p))
	}
	if got := SpecHashFor(act.Algo, lines); got != act.SpecHash {
		return fmt.Errorf(
			"奖档表与公示的 spec_hash 不一致:重算 %s 已公开 %s —— 发布之后奖品金额或中奖概率被改动过",
			got, act.SpecHash)
	}
	// spec_text 是落库的那份原像字节,证据链直接下发它。它与重算结果不一致时,
	// 用户拿到的那份"公示奖档"与真正参与抽取的那份不是同一个。
	if want := strings.Join(lines, SEP); want != act.SpecText {
		return fmt.Errorf("奖档表与落库的 spec_text 不一致 —— 证据链下发的公示奖档与实际参与抽取的不是同一份")
	}
	return nil
}

// ─────────────────────────── 逾期流局 ───────────────────────────

// runVoidExpired 把逾期未录结果的竞猜自动流局。
//
// 没有这一条,管理员可以无限期扣着奖池不结算 —— 那是竞猜最现实的资损形状,
// 而且它连"作弊"都算不上,只需要什么都不做。
func runVoidExpired(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算:逐条 WithContext 漏一条,就等于在这条链路上开了一个
	// 没有上界的口子 —— 语句级预算只对 WithContext 的语句生效。
	gdb = gdb.WithContext(ctx)
	now := common.GetTimestamp()
	var rows []Activity
	if err := gdb.WithContext(ctx).
		Where("status = ? AND kind = ? AND settle_deadline > 0 AND settle_deadline <= ? AND win_option_id = ?",
			StatusLocked, KindGuess, now, 0).
		Order("id asc").Limit(batchPerRound).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		act := &rows[i]
		err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&Activity{}).
				Where("id = ? AND status = ? AND win_option_id = ?", act.Id, StatusLocked, 0).
				Updates(map[string]any{
					"status":     StatusSettling,
					"outcome":    OutcomeVoidDeadline,
					"updated_at": now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return nil
			}
			return writeActivityEvent(tx, act.Id, StatusLocked, StatusSettling, ActionVoid,
				qymodel.ActorSystem, 0, map[string]any{"reason": "逾期未录入结果,自动流局全额退款"})
		})
		if err != nil {
			db.MarkFailure(err)
			common.SysError("qianye/lottery: 自动流局失败: " + err.Error())
			continue
		}
		writeSystemAudit("lottery.void", act.ActNo, qymodel.ResultOK, "逾期未录入结果,自动流局", "")
	}
}

// ─────────────────────────── 结算收尾 ───────────────────────────

// runSettle 推进 settling 态的活动:登记退款计划 → 收敛未决条目 → 判定完成。
//
// 真正的出款由 DrivePayouts 单独驱动。两者分开是因为出款的节奏(每 10 秒)
// 与活动收尾的节奏不同,而且出款失败不该阻塞别的活动收尾。
func runSettle(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算:逐条 WithContext 漏一条,就等于在这条链路上开了一个
	// 没有上界的口子 —— 语句级预算只对 WithContext 的语句生效。
	gdb = gdb.WithContext(ctx)
	var rows []Activity
	if err := gdb.WithContext(ctx).
		Where("status = ?", StatusSettling).
		Order("id asc").Limit(batchPerRound).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		act := &rows[i]
		if isFullRefundOutcome(act.Outcome) {
			if err := planFullRefund(ctx, gdb, act); err != nil {
				common.SysError(fmt.Sprintf(
					"qianye/lottery: 活动 %s 登记全额退款失败: %v", act.ActNo, err))
				continue
			}
		}
		convergeExcluded(ctx, gdb, act)
		finishIfDone(ctx, gdb, act)
	}
}

// isFullRefundOutcome 列出"本金原样退回"的四种收场。
//
// 取消 / 人数不足流局 / 逾期流局 / 全部猜错(含无输家、无对手盘)共用同一条
// 退款路径,而不是各建一个状态 —— 状态机每多一个分支,"这一步之后还能不能
// 动钱"就多一次要人去推理的地方。
func isFullRefundOutcome(outcome string) bool {
	switch outcome {
	case OutcomeCancelled, OutcomeVoidMinEntries, OutcomeVoidDeadline,
		OutcomeVoidNoWinner, OutcomeVoidAllCorrect:
		return true
	}
	return false
}

// planFullRefund 给全部有效参与登记退款计划。
//
// 幂等靠 uk(act_id, entry_id, kind):重复跑只会整体撞键,不会产生双份退款。
// 竞猜的"全部猜错"在 settleGuessResult 里已经登记过一次,这里再跑一遍
// 同样撞键返回 —— 两条路径共用同一个唯一键,不需要额外的协调。
func planFullRefund(ctx context.Context, gdb *gorm.DB, act *Activity) error {
	roster, err := loadRoster(ctx, gdb, act.Id)
	if err != nil {
		return err
	}
	if len(roster) == 0 {
		return nil
	}
	plans := make([]PayoutPlan, 0, len(roster))
	var sum int64
	for i := range roster {
		plans = append(plans, PayoutPlan{
			EntryId: roster[i].Id, UserId: roster[i].UserId,
			Kind: PayoutRefund, Amount: roster[i].Amount,
		})
		sum += roster[i].Amount
	}
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := PlanPayouts(tx, act.Id, plans); err != nil {
			return err
		}
		// refund_quota 用条件更新而不是累加:重复跑时累加会让金额翻倍,
		// 而这个字段是管理端"本场收支"直读的。
		return tx.Model(&Activity{}).Where("id = ?", act.Id).
			Update("refund_quota", sum).Error
	})
}

// convergeExcluded 收敛封盘时未决的参与。
//
// **退款由资金单的终态驱动,永不投机性地登记。** 三种情况:
//
//	Success           → 钱确实收了但没参加,登记退款
//	Failed            → 什么都没发生,标 failed
//	pending/uncertain → 不动,下一轮再看;超时落 flag 转人工
//
// 曾经的做法是封盘时就给 pending 条目登记退款,再在"确认没扣钱"时把那笔
// 退款标成 paid 且金额记 0 —— 那是在资金表里写一条假的成功记录,
// 事后对账会被它误导。
func convergeExcluded(ctx context.Context, gdb *gorm.DB, act *Activity) {
	var rows []Entry
	if err := gdb.WithContext(ctx).
		Where("act_id = ? AND status = ?", act.Id, EntryExcluded).
		Order("id asc").Limit(500).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if len(rows) == 0 {
		return
	}
	manualAfter := int64(config.Get().Lottery.ExcludedManualAfterSeconds)
	now := common.GetTimestamp()

	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		e := &rows[i]
		var order qymodel.FundOrder
		if err := gdb.WithContext(ctx).Where("order_no = ?", e.OrderNo).
			Take(&order).Error; err != nil {
			// 没有资金单说明这条压根没进跨库链路,直接判失败。
			if err := gdb.WithContext(ctx).Model(&Entry{}).
				Where("id = ? AND status = ?", e.Id, EntryExcluded).
				Update("status", EntryFailed).Error; err != nil {
				db.MarkFailure(err)
			}
			continue
		}

		switch order.Status {
		case qymodel.StatusSuccess:
			plans := []PayoutPlan{{
				EntryId: e.Id, UserId: e.UserId, Kind: PayoutRefund, Amount: e.Amount,
			}}
			err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if err := PlanPayouts(tx, act.Id, plans); err != nil {
					return err
				}
				return tx.Model(&Entry{}).
					Where("id = ? AND status = ?", e.Id, EntryExcluded).
					Update("status", EntryRefunded).Error
			})
			if err != nil {
				db.MarkFailure(err)
			}
		case qymodel.StatusFailed:
			if err := gdb.WithContext(ctx).Model(&Entry{}).
				Where("id = ? AND status = ?", e.Id, EntryExcluded).
				Update("status", EntryFailed).Error; err != nil {
				db.MarkFailure(err)
			}
		default:
			// pending / uncertain:钱可能已经动了,不猜。
			if manualAfter > 0 && now-e.CreatedAt > manualAfter {
				raiseFlag(ctx, act.Id, FlagEntryStuck,
					"参与 "+e.EntryNo+" 的资金单长期不可判定: "+e.OrderNo)
			}
		}
	}
}

// finishIfDone 在全部出款到终态后把活动推进 finished。
//
// 终态是 paid 与 held 两种。held 也算终态是刻意的:它已经转人工,不该继续
// 阻塞整场活动的收尾 —— 但它会同时留下一条 payout_stuck 异常,管理端红点
// 不会因为活动"结束"了就消失。
func finishIfDone(ctx context.Context, gdb *gorm.DB, act *Activity) {
	var open int64
	if err := gdb.WithContext(ctx).Model(&Payout{}).
		Where("act_id = ? AND status IN ?", act.Id,
			[]string{PayoutPlanned, PayoutPaying, PayoutFailed}).
		Count(&open).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if open > 0 {
		return
	}
	var unsettled int64
	if err := gdb.WithContext(ctx).Model(&Entry{}).
		Where("act_id = ? AND status IN ?", act.Id, []string{EntryPending, EntryExcluded}).
		Count(&unsettled).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if unsettled > 0 {
		return
	}

	// 全额退款的四种收场必须逐条核对覆盖:planFullRefund 是先读名单再登记计划,
	// 而一笔在途参与可能恰好在读完之后才落定成 success。那一条不会出现在这一轮的
	// 退款计划里,却也不再是 pending/excluded —— 上面两道判定都放行,活动被推成
	// finished,而 runSettle 再也不扫 finished,那个人的参与费永久退不回来。
	// 退款与"success + refunded"条目是一一对应的(uk(act_id, entry_id, kind)),
	// 数量对不上就说明还有人没被覆盖,这一轮不收尾,下一轮 planFullRefund 会补上。
	if isFullRefundOutcome(act.Outcome) {
		var owed, planned int64
		if err := gdb.WithContext(ctx).Model(&Entry{}).
			Where("act_id = ? AND status IN ?", act.Id, []string{EntrySuccess, EntryRefunded}).
			Count(&owed).Error; err != nil {
			db.MarkFailure(err)
			return
		}
		if err := gdb.WithContext(ctx).Model(&Payout{}).
			Where("act_id = ? AND kind = ?", act.Id, PayoutRefund).
			Count(&planned).Error; err != nil {
			db.MarkFailure(err)
			return
		}
		if planned < owed {
			common.SysError(fmt.Sprintf(
				"qianye/lottery: 活动 %s 的退款计划只覆盖了 %d/%d 条,本轮不收尾",
				act.ActNo, planned, owed))
			return
		}
	}

	// 文本奖的覆盖度复核,与上面那条全额退款的复核完全对称。
	//
	// text_grant_count 是开奖事务里与 payout 计划**同时**写下的期望值。
	// 少了说明有中奖位没落库(或被删掉了),这一轮不收尾 —— 收了尾就再也没人
	// 扫这场活动,那些人的奖会永久消失且零告警。
	//
	// 注意 granted 行**不阻塞**收尾:人工履行没有 SLA,拿它卡收尾会让 finished
	// 永远到不了,连带永久占用一个并发活动名额。这里核的是"有没有这一行",
	// 不是"有没有人做完"。
	if act.TextGrantCount > 0 {
		var granted int64
		if err := gdb.WithContext(ctx).Model(&Payout{}).
			Where("act_id = ? AND kind = ?", act.Id, PayoutText).
			Count(&granted).Error; err != nil {
			db.MarkFailure(err)
			return
		}
		if granted < int64(act.TextGrantCount) {
			common.SysError(fmt.Sprintf(
				"qianye/lottery: 活动 %s 的文本奖只登记了 %d/%d 位,本轮不收尾",
				act.ActNo, granted, act.TextGrantCount))
			return
		}
	}

	type sums struct {
		Kind  string
		Total int64
	}
	var agg []sums
	if err := gdb.WithContext(ctx).Model(&Payout{}).
		Select("kind, COALESCE(SUM(amount_quota), 0) AS total").
		Where("act_id = ? AND status = ?", act.Id, PayoutPaid).
		Group("kind").Scan(&agg).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	var payoutSum, refundSum int64
	for _, a := range agg {
		if a.Kind == PayoutRefund {
			refundSum += a.Total
		} else {
			payoutSum += a.Total
		}
	}

	// 双色球:本期没派出去的池子要还回系列,滚进下一期。
	//
	// 用**计划口径**(已登记的派奖计划总额)而不是"真的发出去了多少":一笔转人工
	// 的 held 出款事后仍可能被管理员重试成功,按已付口径还回去会让那笔钱被发两次
	// —— 一次给中奖者,一次留在池子里等着发给下一期的人。
	committed, err := ballCommittedQuota(ctx, gdb, act)
	if err != nil {
		return
	}

	now := common.GetTimestamp()
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Activity{}).
			Where("id = ? AND status = ?", act.Id, StatusSettling).
			Updates(map[string]any{
				"status":       StatusFinished,
				"payout_quota": payoutSum,
				"refund_quota": refundSum,
				"settled_at":   now,
				"updated_at":   now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return nil
		}
		// 幂等由上面那次 CAS 保证:settling → finished 在整个活动生命周期里
		// 最多成功一次,系列池因此最多被结算一次。
		if err := settleSeriesPool(tx, act, committed); err != nil {
			return err
		}
		return writeActivityEvent(tx, act.Id, StatusSettling, StatusFinished, ActionFinish,
			qymodel.ActorSystem, 0, map[string]any{
				"payout_quota": payoutSum,
				"refund_quota": refundSum,
				"fee_quota":    act.PlatformFeeQuota,
				"pool_carry":   ballCarryOf(act, committed),
			})
	})
	if err != nil {
		db.MarkFailure(err)
		common.SysError(fmt.Sprintf("qianye/lottery: 活动 %s 收尾失败: %v", act.ActNo, err))
		return
	}
	writeSystemAudit("lottery.finish", act.ActNo, qymodel.ResultOK, "",
		snapText(map[string]any{"payout_quota": payoutSum, "refund_quota": refundSum}))
}

// ─────────────────────────── 对账 ───────────────────────────

// runReconcile 重算物化计数与名单哈希,把对不上的落成异常。
//
// 它是"名单被事后改动"与"计数漂移"的唯一自动检出手段。刻意只告警不自愈:
// 一个会自己改数的对账任务,在数据真的被篡改时会顺手把证据也抹平。
func runReconcile(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算:逐条 WithContext 漏一条,就等于在这条链路上开了一个
	// 没有上界的口子 —— 语句级预算只对 WithContext 的语句生效。
	gdb = gdb.WithContext(ctx)
	var rows []Activity
	if err := gdb.WithContext(ctx).
		Where("status IN ?", []string{StatusPublished, StatusLocked, StatusSettling}).
		Order("id asc").Limit(batchPerRound).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		reconcileActivity(ctx, gdb, &rows[i])
	}

	// 文本奖的对账**必须跨活动扫**,而不能挂在上面那个循环里。
	//
	// 上面只扫 published/locked/settling,而文本奖的 granted 行**不阻塞收尾**
	// (人工履行没有 SLA,拿它卡收尾会让 finished 永远到不了)。于是一场有
	// 文本奖的活动通常在几秒内就走到 finished,此后再也不进那个循环 ——
	// 挂在里面的"两周没人履行"告警会一次都不响,而那正是它唯一要抓的场景。
	auditTextPrizes(ctx, gdb)
	auditSpecDrift(ctx, gdb)
}

// specAuditCursor 是 auditSpecDrift 的活动 id 游标。与 textAuditCursor 同一条纪律。
var specAuditCursor int64

// auditSpecDrift 跨活动复核奖档表有没有被事后改动。**只告警不自愈。**
//
// 上面那个循环只扫 published/locked/settling,而奖档漂移在活动 **finished 之后**
// 同样有后果:改一个 win_ppm,所有拿着这份证据链跑验证脚本的用户都会算出
// 一份对不上的中奖名单并判 FAIL —— 平台看起来像在作弊,而平台侧零信号。
// 已开完的活动是"历史公正查询"的全部内容,它们才是最需要被持续盯住的那一批。
//
// 游标扫描而不是固定 LIMIT:已结束活动只增不减,固定 LIMIT 恒返回最老的那一批。
func auditSpecDrift(ctx context.Context, gdb *gorm.DB) {
	var rows []Activity
	err := gdb.WithContext(ctx).
		Select("id, act_no, kind, algo, spec_hash, spec_text").
		Where("kind = ? AND spec_hash <> ? AND id > ?", KindDraw, "", specAuditCursor).
		Order("id asc").Limit(batchPerRound * 10).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return
	}
	if len(rows) < batchPerRound*10 {
		specAuditCursor = 0
	} else {
		specAuditCursor = rows[len(rows)-1].Id
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		var prizes []Prize
		if err := gdb.WithContext(ctx).Where("act_id = ?", rows[i].Id).
			Order("tier asc").Find(&prizes).Error; err != nil {
			db.MarkFailure(err)
			return
		}
		if err := checkSpecIntegrity(&rows[i], prizes); err != nil {
			raiseFlag(ctx, rows[i].Id, FlagSpecDrift, err.Error())
		}
	}
}

func reconcileActivity(ctx context.Context, gdb *gorm.DB, act *Activity) {
	var agg struct {
		Cnt   int64
		Total int64
	}
	if err := gdb.WithContext(ctx).Model(&Entry{}).
		Select("COUNT(*) AS cnt, COALESCE(SUM(amount), 0) AS total").
		Where("act_id = ? AND status = ?", act.Id, EntrySuccess).
		Scan(&agg).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if agg.Total != act.PoolQuota {
		raiseFlag(ctx, act.Id, FlagPoolMismatch, fmt.Sprintf(
			"重算奖池 %d 与物化 %d 不一致", agg.Total, act.PoolQuota))
	}
	if agg.Cnt != int64(act.ActiveCount) {
		raiseFlag(ctx, act.Id, FlagCountDrift, fmt.Sprintf(
			"重算有效条目 %d 与物化 %d 不一致", agg.Cnt, act.ActiveCount))
	}

	// 哈希链的完整性。roster_hash 只覆盖 success 条目,失败/被排除的条目**不在
	// 名单里**,它们唯一的防篡改机制就是链 —— 删掉一条 failed 条目,重算奖池、
	// 有效计数、roster_hash 全都不变,平台侧零告警,而任何外部验证者都会当场
	// 报"序号断开"。这里不重推整条链(几万条,每分钟一次代价太大),只校验两个
	// O(1) 的不变量:条目数必须等于已分配的最大序号,链尾必须等于最后一条的
	// chain_hash。删除、插入、改动尾部三类篡改都会在这里露出来。
	var chainAgg struct {
		Cnt    int64
		MaxSeq int
	}
	if err := gdb.WithContext(ctx).Model(&Entry{}).
		Select("COUNT(*) AS cnt, COALESCE(MAX(seq), 0) AS max_seq").
		Where("act_id = ?", act.Id).Scan(&chainAgg).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if chainAgg.Cnt != int64(act.EntrySeq) || chainAgg.MaxSeq != act.EntrySeq {
		raiseFlag(ctx, act.Id, FlagChainDrift, fmt.Sprintf(
			"条目数 %d / 最大序号 %d 与已分配序号 %d 对不上",
			chainAgg.Cnt, chainAgg.MaxSeq, act.EntrySeq))
	} else if act.EntrySeq > 0 {
		var tail Entry
		err := gdb.WithContext(ctx).Select("chain_hash").
			Where("act_id = ? AND seq = ?", act.Id, act.EntrySeq).Take(&tail).Error
		if err != nil {
			db.MarkFailure(err)
			return
		}
		if tail.ChainHash != act.ChainHead {
			raiseFlag(ctx, act.Id, FlagChainDrift, fmt.Sprintf(
				"链尾 %s 与最后一条的 chain_hash %s 对不上", act.ChainHead, tail.ChainHash))
		}
	}

	// 奖档表一经发布就不该再变 —— 开奖前那道拒绝(checkSpecIntegrity)只在
	// locked → settling 那一瞬间跑过一次,而改动可以发生在它之后:改 amount_quota
	// 影响的是尚未出款的那些计划之外的复算口径,改 win_ppm 让所有历史证据链的
	// 独立复算集体报 FAIL,平台看起来像在作弊。这里每轮复核一次。
	if act.SpecHash != "" && act.Kind == KindDraw {
		var prizes []Prize
		if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).
			Order("tier asc").Find(&prizes).Error; err != nil {
			db.MarkFailure(err)
			return
		}
		if err := checkSpecIntegrity(act, prizes); err != nil {
			raiseFlag(ctx, act.Id, FlagSpecDrift, err.Error())
		}
	}

	// 名单一经公开就不该再变。重算比对是它被事后改动的唯一自动检出手段。
	if act.RosterHash != "" {
		roster, err := loadRoster(ctx, gdb, act.Id)
		if err != nil {
			return
		}
		hash, count := RosterHashFor(act.Algo, act.ActNo, act.CommitHash, rosterLines(roster))
		if hash != act.RosterHash || count != act.RosterCount {
			raiseFlag(ctx, act.Id, FlagRosterDrift, fmt.Sprintf(
				"重算名单 %s(%d) 与已公开 %s(%d) 不一致",
				hash, count, act.RosterHash, act.RosterCount))
		}
	}

	// 卡住的出款。held 单独计数:它已经转人工,但必须持续可见 ——
	// 只在转人工那一刻告警一次,红点会在管理员下次打开页面前就被日志滚走。
	var stuck int64
	if err := gdb.WithContext(ctx).Model(&Payout{}).
		Where("act_id = ? AND status = ?", act.Id, PayoutHeld).
		Count(&stuck).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if stuck > 0 {
		raiseFlag(ctx, act.Id, FlagPayoutStuck, fmt.Sprintf("有 %d 笔出款转人工待处理", stuck))
	}

}

// textPrizeStaleSeconds 是"文本奖挂着没人履行"的告警阈值(14 天)。
//
// 用代码常量而不是配置键:它是一条运营纪律的下限,不是需要按站点调的参数。
const textPrizeStaleSeconds int64 = 14 * 24 * 3600

// textAuditCursor 是 auditTextPrizes 的活动 id 游标。
//
// 单进程单变量就够:这个任务跑在 lease.Run("lottery.reconcile") 里,
// 全集群同一时刻只有一个持租约的节点在跑。换节点时游标回到 0,
// 代价只是从头再扫一遍 —— 一次多余的全量复核,没有任何正确性后果。
var textAuditCursor int64

// auditTextPrizes 跨活动复核文本奖的两条不变量。**只告警不自愈。**
//
// 一个会自己改数的对账任务,在数据真被篡改时会顺手把证据抹平 ——
// 这条纪律对文本奖尤其重要:它是全模块唯一一处"钱之外还欠着东西"的地方。
//
// 两条都从 payout 表**分组聚合**出来,而不是逐个活动点查:文本奖的活动大多
// 已经 finished(granted 不阻塞收尾),按活动扫根本扫不到它们。
func auditTextPrizes(ctx context.Context, gdb *gorm.DB) {
	type actAgg struct {
		ActId int64
		Cnt   int64
	}

	// 真事故:有人中了文本奖,但系统里没有那一行。
	//
	// 判据是开奖那一刻与派奖计划**同事务**写下的 text_grant_count ——
	// 它与 payout 行同生共死,因此"行被删了"与"当初就没登记上"都会在这里露出来。
	var granted []actAgg
	err := gdb.WithContext(ctx).Model(&Payout{}).
		Select("act_id, COUNT(*) AS cnt").
		Where("kind = ?", PayoutText).
		Group("act_id").Scan(&granted).Error
	if err != nil {
		db.MarkFailure(err)
		return
	}
	byAct := make(map[int64]int64, len(granted))
	for _, g := range granted {
		byAct[g.ActId] = g.Cnt
	}

	// 游标扫描,不是"取最老的 200 场"。
	//
	// 有文本奖的活动几秒内就 finished 且 text_grant_count 永远 > 0,因此这张
	// 结果集只增不减:一个固定 LIMIT 恒返回 id 最小的那一批,第 201 场之后
	// 的丢行**永久零告警** —— 而那正是这条对账唯一要抓的场景。
	// 游标走到尾回绕成 0,于是每一场活动都会被周期性复核。
	var expected []Activity
	err = gdb.WithContext(ctx).
		Select("id, act_no, text_grant_count").
		Where("text_grant_count > 0 AND id > ?", textAuditCursor).
		Order("id asc").Limit(batchPerRound * 10).Find(&expected).Error
	if err != nil {
		db.MarkFailure(err)
		return
	}
	if len(expected) < batchPerRound*10 {
		textAuditCursor = 0
	} else {
		textAuditCursor = expected[len(expected)-1].Id
	}
	for _, a := range expected {
		if ctx.Err() != nil {
			return
		}
		if got := byAct[a.Id]; got != int64(a.TextGrantCount) {
			raiseFlag(ctx, a.Id, FlagTextGrantMissing, fmt.Sprintf(
				"文本奖登记 %d 位与开奖时算出的 %d 位不一致", got, a.TextGrantCount))
		}
	}

	// 运营欠账。这是全模块唯一一处盯人而不盯机器的机制 —— 没有它,
	// 文本奖会静默烂掉,而用户会以为是抽奖作弊。
	var stale []actAgg
	err = gdb.WithContext(ctx).Model(&Payout{}).
		Select("act_id, COUNT(*) AS cnt").
		Where("kind = ? AND fulfilled_at = 0 AND created_at < ?",
			PayoutText, common.GetTimestamp()-textPrizeStaleSeconds).
		Group("act_id").Scan(&stale).Error
	if err != nil {
		db.MarkFailure(err)
		return
	}
	for _, s := range stale {
		if ctx.Err() != nil {
			return
		}
		raiseFlag(ctx, s.ActId, FlagTextPrizeStale, fmt.Sprintf(
			"有 %d 份文本奖超过 14 天仍未履行", s.Cnt))
	}
}
