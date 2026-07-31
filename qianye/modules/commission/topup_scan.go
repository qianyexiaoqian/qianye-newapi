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

		var minPending int64
		var maxScanned int64
		for idx := range rows {
			r := &rows[idx]
			id := int64(r.Id)
			if id > maxScanned {
				maxScanned = id
			}
			switch r.Status {
			case common.TopUpStatusSuccess:
				accrueTopUp(r)
			case common.TopUpStatusPending:
				if r.CreateTime >= lookback && (minPending == 0 || id < minPending) {
					minPending = id
				}
			}
		}

		next := maxScanned
		if minPending > 0 {
			next = minPending - 1
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
		if len(rows) < topupScanBatch {
			return
		}
	}
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
func accrueTopUp(t *model.TopUp) {
	topupScanned.Add(1)
	if excludedTopUp(t) {
		return
	}
	baseQuota, money := topUpBaseQuota(t)
	if baseQuota <= 0 {
		return
	}
	if err := accrueOneShot(t.UserId, baseQuota, money, SourceTopup,
		topupIdemKey(t.TradeNo), t.TradeNo); err != nil {
		warnf("充值订单 %s 计佣失败(下轮重扫会重试): %v", t.TradeNo, err)
	}
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
