package commission

import (
	"context"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/shopspring/decimal"
	"gorm.io/gorm/clause"
)

// topupCursorKey 是低水位游标在共享 qy_kv 表里的键。
const topupCursorKey = "commission.topup_low_water"

const (
	topupScanBatch      = 500
	topupScanMaxBatches = 20 // 单轮最多处理 10000 行,避免一次持锁太久
)

// runTopupScan 扫描主库 top_ups 并为已成功的订单计佣。
//
// 为什么不用轮询 complete_time 或 updated_at:
//   - top_ups 根本没有 updated_at 列;
//   - epay 回调只改 status,complete_time 恒为 0;
//   - 订单先以 pending 插入、之后才转 success。
//
// 三条事实叠加,任何"只往前走的 id 游标"都必然漏单 —— 订单 100 还 pending 时
// 游标已经走到 200,等它转 success 就再也不会被看到。
//
// 解法是低水位:游标只推进到"窗口内最早那个仍未决的订单之前"。窗口外的
// pending 视为死单(epay 订单会过期),不再守候,于是重扫成本只与
// 窗口内的订单量有关,不随历史总量增长。
func runTopupScan(ctx context.Context) {
	if !config.Get().Commission.Enabled {
		return
	}
	low, err := loadTopupCursor()
	if err != nil {
		warnf("读取充值扫描游标失败: %v", err)
		return
	}

	lookback := lookbackStart()
	for i := 0; i < topupScanMaxBatches; i++ {
		if ctx.Err() != nil {
			return
		}
		var rows []model.TopUp
		err := model.DB.Where("id > ?", low).Order("id asc").Limit(topupScanBatch).Find(&rows).Error
		if err != nil {
			warnf("扫描 top_ups 失败: %v", err)
			return
		}
		if len(rows) == 0 {
			return
		}

		out := scanBatch(rows, lookback, func(r *model.TopUp) error { return accrueTopUp(ctx, r) })
		next := lowWaterAfter(out)
		if out.MinFailed > 0 {
			topupHeld.Add(1)
		}
		// 游标只能前进。倒退会让已计佣的订单被重扫(唯一索引兜得住,
		// 但会白白打一遍库),前进过头则会漏单。
		if next <= low {
			return
		}
		if err := saveTopupCursor(next); err != nil {
			warnf("写入充值扫描游标失败: %v", err)
			return
		}
		low = next
		if out.MinFailed > 0 {
			// 游标已被钉在失败订单之前,继续下一批只会把同一笔再打一遍库。
			// 本轮到此为止,剩下的窗口下一个扫描周期再处理。
			return
		}
		if len(rows) < topupScanBatch {
			return
		}
	}
}

// scanOutcome 汇总一批订单扫描出来的三个游标约束。
type scanOutcome struct {
	MaxScanned int64 // 本批实际扫到的最大 id
	MinPending int64 // 窗口内最早的未决订单 id,0 表示没有
	MinFailed  int64 // 本批首个计佣失败的订单 id,0 表示没有
}

// scanBatch 处理一批订单并汇总游标约束。
//
// accrue 作为参数传入(而不是直接调 accrueTopUp)是为了让这段"哪些订单
// 允许游标越过"的策略能被独立验证。审计结论里这一类缺陷的共同形状正是
// "纯函数算对了、调度层断链",而调度层恰恰是最难在集成环境里复现的一层。
func scanBatch(rows []model.TopUp, lookback int64, accrue func(*model.TopUp) error) scanOutcome {
	var out scanOutcome
	for idx := range rows {
		r := &rows[idx]
		id := int64(r.Id)
		if id > out.MaxScanned {
			out.MaxScanned = id
		}
		switch r.Status {
		case common.TopUpStatusSuccess:
			if err := accrue(r); err != nil && (out.MinFailed == 0 || id < out.MinFailed) {
				out.MinFailed = id
				// 只对本批首个失败的订单告警。游标会被钉在它之前,后面的订单
				// 下一轮连着它一起重扫;逐条打印会在持续失败时把日志刷爆。
				warnf("充值订单 id=%d trade_no=%s 计佣失败,游标不再前进: %v", id, r.TradeNo, err)
			}
		case common.TopUpStatusPending:
			// 窗口外的未决订单视为死单(epay 订单会过期),不再守候,
			// 否则一笔永不支付的订单会把游标永久钉死。
			if r.CreateTime >= lookback && (out.MinPending == 0 || id < out.MinPending) {
				out.MinPending = id
			}
		}
	}
	return out
}

// lowWaterAfter 计算一批扫描之后低水位游标能推进到哪里,取三个约束的最小值。
//
// MinFailed 这一路是必须的:扫描语句是 `WHERE id > low`,单向且不可回头。
// 计佣失败绝大多数是死锁 / 锁等待超时这类可重试错误 —— db.isConnLevelError
// 不认它们,熔断不会打开,扫描会照常跑完整批。旧实现在失败分支只打一行
// "下轮重扫会重试"的日志却照样把游标推到 MaxScanned,与日志描述正好相反:
// 那笔订单再也不会被扫到,佣金永久丢失,而 trade_no 唯一索引只防重复、不防遗漏。
//
// 代价是一笔持续失败的订单会把游标钉住、后面的订单一起等。这是刻意的取舍:
// 漏发是静默且不可逆的,停滞则会被 topup_cursor_held 指标与日志立刻暴露出来。
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

func lookbackStart() int64 {
	hours := config.Get().Commission.TopupScanLookbackHours
	if hours <= 0 {
		hours = 72
	}
	return common.GetTimestamp() - int64(hours)*3600
}

// accrueTopUp 为一笔成功的充值订单计佣。幂等键是 trade_no,
// 扫描、人工重扫、未来可能补的实时回调三条路径任意重叠都不会重复返佣。
//
// 返回 error 是有意义的:调用方要用它把游标钉在这笔订单之前(见 lowWaterAfter)。
// "口径排除"与"基数为零"不是失败,返回 nil,游标照常越过。
func accrueTopUp(ctx context.Context, t *model.TopUp) error {
	topupScanned.Add(1)
	if excludedTopUp(t) {
		return nil
	}
	baseQuota, money := topUpBaseQuota(t)
	if baseQuota <= 0 {
		return nil
	}
	return accrueOneShot(ctx, t.UserId, baseQuota, money, SourceTopup,
		topupIdemKey(t.TradeNo), t.TradeNo)
}

// excludedTopUp 判断这笔充值是否不该返佣。
func excludedTopUp(t *model.TopUp) bool {
	// 用余额支付产生的订单不能再返佣:那笔余额在充值进来时已经返过一次,
	// 再返一次等于同一笔钱付两遍佣金。
	if t.PaymentProvider == model.PaymentProviderBalance || t.PaymentMethod == model.PaymentMethodBalance {
		return true
	}
	// 管理员补单没有独立标记(上游未提供),只能靠 payment_method 兜底识别。
	if config.Get().Commission.ExcludeRedemptionAndManual && t.PaymentMethod == "manual" {
		return true
	}
	return false
}

// topUpBaseQuota 按支付渠道推算到账额度。
//
// 各条充值路径的换算方式并不一致,统一按 Amount × QuotaPerUnit 会算错:
//   - creem:Amount 本身就是额度,不再乘;
//   - stripe 与订阅付费单(payment_provider 为空、Amount=0):按 Money 换算;
//   - 其余(epay/waffo/...):Amount × QuotaPerUnit。
func topUpBaseQuota(t *model.TopUp) (int64, decimal.Decimal) {
	money := decimal.NewFromFloat(t.Money)
	qpu := decimal.NewFromFloat(common.QuotaPerUnit)
	switch t.PaymentProvider {
	case model.PaymentProviderCreem:
		return t.Amount, money
	case model.PaymentProviderStripe, "":
		return quotaFromDecimal(money.Mul(qpu)), money
	default:
		return quotaFromDecimal(decimal.NewFromInt(t.Amount).Mul(qpu)), money
	}
}

// quotaFromDecimal 把换算结果转成整数额度。
//
// 走 common 的饱和转换而不是裸 IntPart():主库额度列是 int32,
// 一个被篡改的订单金额不能变成负数额度,更不能成为负数佣金。
func quotaFromDecimal(d decimal.Decimal) int64 {
	v, clamp := common.QuotaFromDecimalChecked(d.Floor())
	if clamp != nil {
		warnf("充值基数换算触顶: %s", clamp.Error())
	}
	if v < 0 {
		return 0
	}
	return int64(v)
}

// ───────────────────────── 游标读写 ─────────────────────────

// peekTopupCursor 只读游标,不做初始化。健康面板必须用它 ——
// 一个 GET 接口不该顺手把游标写出来。
func peekTopupCursor() int64 {
	gdb := db.Get()
	if gdb == nil {
		return 0
	}
	var row qymodel.KV
	if err := gdb.Where("k = ?", topupCursorKey).Take(&row).Error; err != nil {
		return 0
	}
	v, err := strconv.ParseInt(row.V, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func loadTopupCursor() (int64, error) {
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	var row qymodel.KV
	err := gdb.Where("k = ?", topupCursorKey).Take(&row).Error
	if err == nil {
		v, convErr := strconv.ParseInt(row.V, 10, 64)
		if convErr != nil {
			return 0, nil
		}
		return v, nil
	}
	// 首次运行:必须把游标定位到"现在",否则会把平台上线以来的所有历史
	// 充值全部补返一遍佣金。定位点取窗口内最早的未决订单,与稳态语义一致。
	low, err := bootstrapCursor()
	if err != nil {
		return 0, err
	}
	if err := saveTopupCursor(low); err != nil {
		return 0, err
	}
	common.SysLog("qianye/commission: 充值扫描游标初始化为 " + strconv.FormatInt(low, 10) +
		"(历史订单不补返佣金)")
	return low, nil
}

func bootstrapCursor() (int64, error) {
	var minPending int64
	err := model.DB.Model(&model.TopUp{}).
		Where("status = ? AND create_time >= ?", common.TopUpStatusPending, lookbackStart()).
		Select("COALESCE(MIN(id), 0)").Scan(&minPending).Error
	if err != nil {
		return 0, err
	}
	if minPending > 0 {
		return minPending - 1, nil
	}
	var maxId int64
	if err := model.DB.Model(&model.TopUp{}).Select("COALESCE(MAX(id), 0)").Scan(&maxId).Error; err != nil {
		return 0, err
	}
	return maxId, nil
}

func saveTopupCursor(v int64) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	row := qymodel.KV{K: topupCursorKey, V: strconv.FormatInt(v, 10), UpdatedAt: common.GetTimestamp()}
	err := gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}
