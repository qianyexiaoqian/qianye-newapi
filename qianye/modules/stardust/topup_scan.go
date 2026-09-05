package stardust

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"gorm.io/gorm/clause"
)

// topup_scan.go —— 邀请返(下线充值)。上游没有充值成功 hook,只能扫主库 top_ups。
//
// 前向低水位游标 + 迟付回收 + 首启不补历史。
// 判定函数由 invite 导出(
// ExcludedTopUp / TopUpBaseQuota / InviteeEligible)。订阅付费单在 top_ups 里也有一行
// (trade_no 就是 subscription_orders.trade_no),它只走套餐返 —— 排除是 stardust 专属,
// 前向与迟付两趟都要过这一层(D-E)。

const (
	topupLowWaterKey  = "stardust.topup_low_water"
	topupLateWaterKey = "stardust.topup_late_water"

	topupScanBatch      = 500
	topupScanMaxBatches = 20 // 单轮最多 10000 行,避免一次持锁太久
	// topupLateGraceSec 是迟付回收水位线的安全余量:水位线永远停在"本轮开始时刻减去
	// 这个余量",宁可重扫(命中幂等键的空插入)也不越过(永久漏发)。
	topupLateGraceSec = 120
	// topupSettleGraceSec 是前向扫描对"刚转成功"的订单多等的时间:这段里的订单按
	// 窗口内未决对待、把游标钉在它之前而**不是**在 SQL 里过滤掉 —— 过滤掉再推进游标
	// 就是把它永久越过,只能指望迟付回收兜底。
	topupSettleGraceSec = 120
	// topupPendingLookbackSec 是守候未决订单的窗口:窗口外的 pending 视为死单不再守候,
	// 否则一笔永不支付的订单会把游标永久钉死。stardust 段没有这个键,写死 72 小时。
	topupPendingLookbackSec = int64(72 * 3600)

	topupIdemScope = "sd_topup"
)

func init() {
	taskStarters = append(taskStarters, func() {
		if invite.SharedInfraWanted() {
			invite.StartSharedInfra()
		}
		every := config.Get().Stardust.TopupScanIntervalSeconds
		if every <= 0 {
			every = 60
		}
		lease.Run("stardust.topup_scan", time.Duration(every)*time.Second, runTopupScan)
	})
}

// runTopupScan 扫描主库 top_ups 并给已成功订单的邀请人记星屑。
//
// 迟付回收排在前向扫描**之前**:此刻 low 还是上一轮的游标,本轮刚支付的订单都在它
// 上面,不会被这一趟白扫一遍。任一笔失败都把游标钉在该单之前:
// 停滞看得见,漏发看不见。
func runTopupScan(ctx context.Context) {
	if ctx.Err() != nil || !config.Get().Stardust.Enabled || model.DB == nil {
		return
	}
	low, err := loadTopupCursor(ctx)
	if err != nil {
		warnf("读取充值扫描游标失败: %v", err)
		return
	}
	sweepLateTopups(ctx, low)

	now := common.GetTimestamp()
	lookback := now - topupPendingLookbackSec
	for i := 0; i < topupScanMaxBatches; i++ {
		if ctx.Err() != nil {
			return
		}
		var rows []model.TopUp
		if err := model.DB.WithContext(ctx).Where("id > ?", low).
			Order("id asc").Limit(topupScanBatch).Find(&rows).Error; err != nil {
			warnf("扫描 top_ups 失败: %v", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		subs, err := subscriptionTradeNos(ctx, rows)
		if err != nil {
			warnf("查询 subscription_orders 失败,本轮不推进游标: %v", err)
			return
		}
		out := scanTopups(rows, lookback, now, func(r *model.TopUp) error {
			if subscriptionTopUp(r, subs) {
				return nil
			}
			return accrueTopUp(ctx, r)
		})
		next := lowWaterAfter(out)
		// 游标只能前进:倒退会让已记账的订单被重扫,前进过头则会漏单。
		if next <= low {
			return
		}
		if err := saveKV(ctx, topupLowWaterKey, next); err != nil {
			warnf("写入充值扫描游标失败: %v", err)
			return
		}
		low = next
		if out.MinFailed > 0 || len(rows) < topupScanBatch {
			return
		}
	}
}

// scanOutcome 汇总一批订单扫描出来的三个游标约束。
type scanOutcome struct {
	MaxScanned int64 // 本批实际扫到的最大 id
	MinPending int64 // 窗口内最早的未决(或刚转成功仍在余量内的)订单 id,0 表示没有
	MinFailed  int64 // 本批首个记账失败的订单 id,0 表示没有
}

// scanTopups 处理一批订单并汇总游标约束。accrue 作为参数传入,让"哪些订单允许游标
// 越过"这段策略能脱离数据库单独验证。
func scanTopups(rows []model.TopUp, lookback, now int64, accrue func(*model.TopUp) error) scanOutcome {
	var out scanOutcome
	for idx := range rows {
		r := &rows[idx]
		id := int64(r.Id)
		if id > out.MaxScanned {
			out.MaxScanned = id
		}
		switch r.Status {
		case common.TopUpStatusSuccess:
			if r.CompleteTime > now-topupSettleGraceSec {
				if out.MinPending == 0 || id < out.MinPending {
					out.MinPending = id
				}
				continue
			}
			if err := accrue(r); err != nil && (out.MinFailed == 0 || id < out.MinFailed) {
				out.MinFailed = id
				// 只对本批首个失败的订单告警,逐条打印会在持续失败时把日志刷爆。
				warnf("充值订单 id=%d trade_no=%s 记星屑失败,游标不再前进: %v", id, r.TradeNo, err)
			}
		case common.TopUpStatusPending:
			if r.CreateTime >= lookback && (out.MinPending == 0 || id < out.MinPending) {
				out.MinPending = id
			}
		}
	}
	return out
}

// lowWaterAfter 计算一批扫描之后低水位游标能推进到哪里,取三个约束的最小值。
// MinFailed 这一路是必须的:扫描语句是 `WHERE id > low`,单向且不可回头。
func lowWaterAfter(o scanOutcome) int64 {
	next := o.MaxScanned
	if o.MinPending > 0 && o.MinPending-1 < next {
		next = o.MinPending - 1
	}
	if o.MinFailed > 0 && o.MinFailed-1 < next {
		next = o.MinFailed - 1
	}
	return next
}

// subscriptionTradeNos 一次查出本批里出现在 subscription_orders 的 trade_no 集合。
func subscriptionTradeNos(ctx context.Context, rows []model.TopUp) (map[string]bool, error) {
	nos := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].Status == common.TopUpStatusSuccess && rows[i].TradeNo != "" {
			nos = append(nos, rows[i].TradeNo)
		}
	}
	hits := make(map[string]bool, len(nos))
	if len(nos) == 0 {
		return hits, nil
	}
	var found []string
	if err := model.DB.WithContext(ctx).Model(&model.SubscriptionOrder{}).
		Where("trade_no IN ?", nos).Pluck("trade_no", &found).Error; err != nil {
		return nil, err
	}
	for _, no := range found {
		hits[no] = true
	}
	return hits, nil
}

// subscriptionTopUp 回答"这一行是不是订阅付费单":判据是 subscription_orders 里
// 存在同一个 trade_no(精确关联)。旧的"provider 空且 Amount=0"三元组只做日志级兜底:
// 两者不一致时告警,不改判定。
func subscriptionTopUp(r *model.TopUp, subs map[string]bool) bool {
	hit := subs[r.TradeNo]
	if looksLikeSub := r.PaymentProvider == "" && r.Amount == 0; looksLikeSub != hit {
		warnf("充值单 %s 的订阅判据不一致(subscription_orders 命中=%v,provider/amount 形状=%v),以 subscription_orders 为准",
			r.TradeNo, hit, looksLikeSub)
	}
	return hit
}

// sweepLateTopups 回收「前向游标已经越过、之后才转 success」的充值订单。
//
// 前向游标为了不被死单钉死会放过窗口外的 pending 单,但被放过的订单照样可以在几天后
// 被合法回调付成 success;`WHERE id > low` 单向不可回头,那笔星屑就永久丢了。
// 回收判据用 complete_time:漏掉的那些订单的共同点正是"id 早、完成晚"。
func sweepLateTopups(ctx context.Context, low int64) {
	if low <= 0 {
		return
	}
	startedAt := common.GetTimestamp()
	watermark, err := loadTopupLateWatermark(ctx, startedAt)
	if err != nil {
		warnf("读取迟付回收水位线失败: %v", err)
		return
	}
	var rows []model.TopUp
	if err := model.DB.WithContext(ctx).
		Where("id <= ? AND status = ? AND complete_time > ?", low, common.TopUpStatusSuccess, watermark).
		Order("complete_time asc").Limit(topupScanBatch).Find(&rows).Error; err != nil {
		warnf("扫描迟付充值订单失败: %v", err)
		return
	}
	subs, err := subscriptionTradeNos(ctx, rows)
	if err != nil {
		warnf("查询 subscription_orders 失败,迟付回收本轮跳过: %v", err)
		return
	}
	for idx := range rows {
		if ctx.Err() != nil {
			return
		}
		r := &rows[idx]
		if subscriptionTopUp(r, subs) {
			continue
		}
		if err := accrueTopUp(ctx, r); err != nil {
			// 水位线不推进,下一轮连着它一起重来。
			warnf("迟付充值订单 id=%d trade_no=%s 记星屑失败,回收水位线不再前进: %v", r.Id, r.TradeNo, err)
			return
		}
	}
	next := startedAt - topupLateGraceSec
	if len(rows) >= topupScanBatch {
		// 本轮被批次上限截断:水位线只能推到最后一行那一刻之前,剩下的下一轮继续。
		next = rows[len(rows)-1].CompleteTime - 1
	}
	if next <= watermark {
		return
	}
	if err := saveKV(ctx, topupLateWaterKey, next); err != nil {
		warnf("写入迟付回收水位线失败: %v", err)
	}
}

// accrueTopUp 给一笔成功充值的邀请人记一笔 invite_topup。幂等键 topup:<trade_no>,
// 扫描、迟付回收、人工重扫任意重叠都不会重复记账。
//
// 返回 error 是有意义的:调用方要用它把游标钉在这笔订单之前。"口径排除"与
// "基数为零 / 不足 1 星屑"不是失败,返回 nil,游标照常越过。
func accrueTopUp(ctx context.Context, t *model.TopUp) error {
	if invite.ExcludedTopUp(t, config.Get().Stardust.ManualTopupExcluded()) {
		return nil
	}
	base, _ := invite.TopUpBaseQuota(t)
	if base <= 0 {
		return nil
	}
	match, err := invite.InviteeEligible(ctx, t.UserId)
	if err != nil {
		return err
	}
	if !match.Eligible() {
		return nil
	}
	bps, group := inviteRateFor(ctx, match.InviterGroup, inviteTopupRate)
	amount := stardustFromQuota(base, bps, QuotaPerUnit())
	if amount <= 0 {
		return nil
	}
	_, err = postCredit(ctx, Posting{
		UserId: match.InviterId, Kind: KindInviteTopup, Amount: amount,
		IdemScope: topupIdemScope, IdemKey: "topup:" + t.TradeNo,
		RefType: "topup", RefNo: t.TradeNo,
		PeerUserId: t.UserId, RateBps: bps, RateGroup: group, BaseQuota: base,
	})
	return err
}

// ───────────────────────── 游标读写(共享 qy_kv) ─────────────────────────

// loadTopupCursor 读低水位游标;首次运行定位到"现在"而不是 0:零值会把平台上线以来
// 的所有历史充值全部补返一遍。定位点取窗口内最早的未决订单,与稳态语义一致。
func loadTopupCursor(ctx context.Context) (int64, error) {
	v, ok, err := loadKV(ctx, topupLowWaterKey)
	if err != nil {
		return 0, err
	}
	if ok {
		return v, nil
	}
	low, err := bootstrapTopupCursor(ctx)
	if err != nil {
		return 0, err
	}
	if err := saveKV(ctx, topupLowWaterKey, low); err != nil {
		return 0, err
	}
	common.SysLog("qianye/stardust: 充值扫描游标初始化为 " + strconv.FormatInt(low, 10) + "(历史订单不补返星屑)")
	return low, nil
}

func bootstrapTopupCursor(ctx context.Context) (int64, error) {
	var minPending int64
	err := model.DB.WithContext(ctx).Model(&model.TopUp{}).
		Where("status = ? AND create_time >= ?", common.TopUpStatusPending, common.GetTimestamp()-topupPendingLookbackSec).
		Select("COALESCE(MIN(id), 0)").Scan(&minPending).Error
	if err != nil {
		return 0, err
	}
	if minPending > 0 {
		return minPending - 1, nil
	}
	var maxId int64
	if err := model.DB.WithContext(ctx).Model(&model.TopUp{}).
		Select("COALESCE(MAX(id), 0)").Scan(&maxId).Error; err != nil {
		return 0, err
	}
	return maxId, nil
}

// loadTopupLateWatermark 读迟付回收水位线,不存在时按 now 落一条(缺键的语义是
// "从现在开始看",初始化必须写进库而不是每轮临时取一个 now)。首次落库退一个安全余量:
// 恰好在这一秒完成、且已在游标下方的订单否则会卡在边界上被整个跳过。
func loadTopupLateWatermark(ctx context.Context, now int64) (int64, error) {
	v, ok, err := loadKV(ctx, topupLateWaterKey)
	if err != nil {
		return 0, err
	}
	if ok {
		return v, nil
	}
	bootstrap := now - topupLateGraceSec
	if err := saveKV(ctx, topupLateWaterKey, bootstrap); err != nil {
		return 0, err
	}
	common.SysLog("qianye/stardust: 迟付回收水位线初始化为 " + strconv.FormatInt(bootstrap, 10) + "(历史已完成订单不补返星屑)")
	return bootstrap, nil
}

// loadKV 读一条整数水位线。第二个返回值为 false 表示还没有这一行(或值不可解析,
// 按缺失处理:一个坏值不该让扫描从 0 开始补历史)。
func loadKV(ctx context.Context, key string) (int64, bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return 0, false, db.ErrNotReady
	}
	var rows []qymodel.KV
	if err := gdb.WithContext(ctx).Where("k = ?", key).Limit(1).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return 0, false, err
	}
	if len(rows) == 0 {
		return 0, false, nil
	}
	v, err := strconv.ParseInt(rows[0].V, 10, 64)
	if err != nil {
		warnf("qy_kv 里的 %s = %q 不是整数,按缺失处理", key, rows[0].V)
		return 0, false, nil
	}
	return v, true, nil
}

// saveKV 写一条水位线:游标与迟付回收水位线共用同一张 qy_kv 表、同一套 upsert。
func saveKV(ctx context.Context, key string, v int64) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	row := qymodel.KV{K: key, V: strconv.FormatInt(v, 10), UpdatedAt: common.GetTimestamp()}
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}
