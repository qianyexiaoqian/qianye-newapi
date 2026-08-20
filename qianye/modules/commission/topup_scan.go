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

// topupSweepKey 是迟付回收扫描的时间水位线在共享 qy_kv 表里的键。
// 存的是"已经回收到哪个 complete_time 为止"。
const topupSweepKey = "commission.topup_sweep_high_water"

const (
	topupScanBatch      = 500
	topupScanMaxBatches = 20 // 单轮最多处理 10000 行,避免一次持锁太久
	// topupSweepGrace 是迟付回收水位线的安全余量(秒)。
	//
	// 水位线永远停在"本轮开始时刻减去这个余量",宁可重扫也不越过:一笔订单可能
	// 在本轮取数之后、水位线写入之前才提交,而 complete_time 取的是它自己的时刻。
	// 重扫的代价只是一次命中唯一键的空插入,越过的代价是佣金永久丢失。
	topupSweepGrace = 120
)

// runTopupScan 扫描主库 top_ups 并为已成功的订单计佣。它由两趟组成。
//
// 【前向低水位游标】top_ups 没有 updated_at 列,订单又先以 pending 插入、
// 之后才转 success,所以任何"只往前走的 id 游标"都会漏单 —— 订单 100 还
// pending 时游标已经走到 200,等它转 success 就再也不会被看到。
// 解法是低水位:游标只推进到"窗口内最早那个仍未决的订单之前"。窗口外的
// pending 不再守候(否则一笔永不支付的订单会把游标永久钉死),于是重扫成本
// 只与窗口内的订单量有关,不随历史总量增长。
//
// 【迟付回收】上面那句"窗口外的 pending 不再守候"是一个真实的缺口:被放过的
// 订单照样可以在几天后被合法回调付成 success(见 sweepLateTopups)。
// 第二趟按 complete_time 把它们捞回来 —— 现在所有结算路径都会写
// complete_time,时间维度因此可用,而它恰好与 id 维度正交:漏掉的那些订单的
// 共同特征正是"id 早、完成晚"。两趟共用幂等键 topup:<trade_no>,重叠无害。
func runTopupScan(ctx context.Context) {
	if !config.Get().Commission.Enabled {
		return
	}
	low, err := loadTopupCursor()
	if err != nil {
		warnf("读取充值扫描游标失败: %v", err)
		return
	}
	// 迟付回收排在前向扫描**之前**:此刻 low 还是上一轮的游标,本轮刚支付的订单
	// 都在它上面,不会被这一趟白扫一遍,回收面只剩真正被越过的那些。
	sweepLateTopups(ctx, low)

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

// sweepLateTopups 回收「前向游标已经越过、之后才转 success」的充值订单。
//
// 为什么必须有这一趟:前向游标为了不被永不支付的死单钉死,会放过窗口外的
// pending 单(见 scanBatch)。但"放过"不等于"那笔订单不会再被支付" ——
// epay 的回调对订单年龄零校验、网关侧的支付链接通常长期有效、订阅单同理、
// 管理员还能对着一张老单补单。一旦它在游标之后才转 success,
// 扫描语句 `WHERE id > low` 单向不可回头,这笔佣金就永久丢了:
// 幂等键 topup:<trade_no> 只防重复不防遗漏,账本三张表全部自洽,
// 健康面板与流水页都看不出少了一行,站内也没有任何地方拿 success 充值单
// 去对过计佣行。首启 bootstrap 一次性放过的存量 pending 单同样落在这个形状里。
//
// 回收判据用 complete_time 而不是 id:漏掉的那些订单的共同点正是"id 早、完成晚"。
// 水位线只跟时间走,与游标无关,因此一笔在窗口外被支付的订单照样能被捞回来。
func sweepLateTopups(ctx context.Context, low int64) {
	if low <= 0 {
		return
	}
	startedAt := common.GetTimestamp()
	watermark, err := loadTopupSweepWatermark(startedAt)
	if err != nil {
		warnf("读取迟付回收水位线失败: %v", err)
		return
	}
	var rows []model.TopUp
	err = model.DB.WithContext(ctx).
		Where("id <= ? AND status = ? AND complete_time > ?",
			low, common.TopUpStatusSuccess, watermark).
		Order("complete_time asc").Limit(topupScanBatch).Find(&rows).Error
	if err != nil {
		warnf("扫描迟付充值订单失败: %v", err)
		return
	}
	for idx := range rows {
		if ctx.Err() != nil {
			return
		}
		r := &rows[idx]
		if err := accrueTopUp(ctx, r); err != nil {
			// 水位线不推进,下一轮连着它一起重来。与前向扫描的 MinFailed 同一取舍:
			// 停滞看得见,漏发看不见。
			warnf("迟付充值订单 id=%d trade_no=%s 计佣失败,回收水位线不再前进: %v", r.Id, r.TradeNo, err)
			return
		}
		topupLateSwept.Add(1)
	}
	next := startedAt - topupSweepGrace
	if len(rows) >= topupScanBatch {
		// 本轮被批次上限截断:水位线只能推到最后一行那一刻之前,剩下的下一轮继续。
		next = rows[len(rows)-1].CompleteTime - 1
	}
	if next <= watermark {
		return
	}
	if err := saveTopupSweepWatermark(next); err != nil {
		warnf("写入迟付回收水位线失败: %v", err)
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
	// 管理员补单认 complete_source。
	//
	// 旧判据是 payment_method == "manual",而全仓没有任何一条路径会把它写成
	// 这个值:ManualCompleteTopUp 原样保留用户下单时选的支付方式,只把 "admin"
	// 传给日志的 provider 形参,那个值不落 top_ups 表。于是"管理员补单不返佣"
	// 这半个开关从来没生效过 —— 运营在配置注释里读到的是两条都堵上了,
	// 实际上"让小号建单再补单"这条凭空造佣金的路一直开着。
	// 反过来它还是个活着的误判:支付方式的 type 由管理端自由填写,
	// 一旦有人把某个易支付通道命名成 manual,真实付款会被整批误杀。
	if config.Get().Commission.ExcludeRedemptionAndManual && t.CompleteSource == model.TopUpCompleteSourceAdmin {
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
	case model.PaymentProviderStripe:
		return quotaFromDecimal(money.Mul(qpu)), money
	case "":
		// 空 provider 有两种来源,判据必须落在 **Amount** 上而不是 provider:
		//   - 订阅付费单(upsertSubscriptionTopUpTx 硬编码 Amount=0)→ 按 Money;
		//   - **payment_provider 列存在之前的历史 epay 订单**(Amount>0)→ 与
		//     model.TopUp.CreditQuota 的 default 分支一样按 Amount × QuotaPerUnit。
		//
		// 原先把两者合并进 stripe 那一支(注释里写的前提是"Amount=0",代码却没查),
		// 于是历史折扣订单(amount=10 / money=8)的计佣基数按 Money 算,与实际到账
		// 额度差一个折扣率;开启过 operation_setting.Price ≠ 1 的站点差得更多。
		// 这批老单仍可被管理员补单激活,补完就会被迟付回收捞进来按错口径计佣。
		if t.Amount > 0 {
			return quotaFromDecimal(decimal.NewFromInt(t.Amount).Mul(qpu)), money
		}
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

// loadTopupSweepWatermark 读迟付回收水位线,不存在时按 now 落一条。
//
// 零值不能当"从头扫":那会把平台上线以来所有已完成的充值全部补返一遍佣金 ——
// 与 bootstrapCursor 拒绝补历史是同一条理由。缺键的语义是"从现在开始看",
// 所以初始化必须写进库,而不是每轮临时取一个 now。
func loadTopupSweepWatermark(now int64) (int64, error) {
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	var row qymodel.KV
	err := gdb.Where("k = ?", topupSweepKey).Take(&row).Error
	if err == nil {
		v, convErr := strconv.ParseInt(row.V, 10, 64)
		if convErr != nil {
			return now, nil
		}
		return v, nil
	}
	// 首次落库退一个安全余量:恰好在这一秒完成、而且已经落在游标下方的订单
	// 否则会卡在 `complete_time > watermark` 的边界上被整个跳过。
	bootstrap := now - topupSweepGrace
	if err := saveTopupSweepWatermark(bootstrap); err != nil {
		return 0, err
	}
	common.SysLog("qianye/commission: 迟付回收水位线初始化为 " + strconv.FormatInt(bootstrap, 10) +
		"(历史已完成订单不补返佣金)")
	return bootstrap, nil
}

func saveTopupSweepWatermark(v int64) error {
	return saveKV(topupSweepKey, v)
}

func saveTopupCursor(v int64) error {
	return saveKV(topupCursorKey, v)
}

// saveKV 写一条扫描水位线。游标与迟付回收水位线共用同一张 qy_kv 表、
// 同一套 upsert 与失败标记,分开写只会得到两段除键名外逐字节相同的代码。
func saveKV(key string, v int64) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	row := qymodel.KV{K: key, V: strconv.FormatInt(v, 10), UpdatedAt: common.GetTimestamp()}
	err := gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}
