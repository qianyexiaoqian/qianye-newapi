package commission

// fiatrate.go —— 「站内佣金余额 → 法币」的折算比例,按分组分档 + 兜底档。
//
// # 这个比例是什么
//
// 站内佣金余额本身是**额度**(qy_commission_balance.available_quota)。把它折成
// 一个法币数字(available_fiat)需要一个比例,链路是
//
//	fiat_delta = net_quota / common.QuotaPerUnit × 本笔冻结的比例
//
// 在本档出现之前,这个比例全站只有一个:operation_setting.USDExchangeRate ——
// 那是**充值页**的汇率,和"平台愿意按什么价把佣金结给推广者"根本不是一回事。
// 本文件把它分成三层,并保留全站汇率作为最后一层,使任何存量站点升级上来、
// 什么都不配时行为一分不变。
//
// # 口径:按【邀请人(上线)】的分组,与费率那一档**同源**
//
// grouprate.go 的三档费率与本文件的折算比例取的是**同一个人在同一时刻**的
// 账号分组:收款人自己的分组。两者由 pricing.go 的 resolveInviterPricing 一处
// 解析、一个字符串喂两档,谁都不能单独决定"取谁的分组"。
//
// 曾经不是这样:本档一直按上线分组,而费率那一档按**下线**分组。那个不一致
// 在账面上完全看不出来 —— 一条 accrual 行的 rate_group 记着下线的分组、
// usd_rate 却是按上线分组算出来的,三条恒等式全部成立。2026-08-18 统一到上线。
//
// 按上线分组的三条理由(费率那一侧的理由见 grouprate.go 文件头):
//
//  1. **谁被折算就用谁的档。** available_fiat 是上线的钱,提现单是上线在提,
//     打款也是打给上线。用下线的分组决定上线能拿多少法币,等于让一个上线
//     既看不见也控制不了的属性决定他的收款金额。
//
//  2. **可解释,而且这是"看得出走哪一层"的前提。** 上线打开提现页看到
//     "余额 ≈ ¥X",这个 X 必须能被他自己看得见的一个档位解释。取下线分组的话,
//     同一个上线的余额是 N 个下线分组的加权混合,界面上根本画不出"你走的是
//     哪一层" —— 而那正是本档要求的东西。
//
//  3. **总额已被费率档封死。** 折算比例乘的是上线自己已经挣到的那笔余额,
//     调高比例只改变同一笔佣金的计价单位,不会凭空多出一分佣金。
//     "VIP 推广者按更高比例结汇"本来就是运营想要的杠杆。
//
// # 层级
//
//	1. 分组档   本表里该上线分组的一行,enabled 且比例合法
//	2. 兜底档   qy_settings 的 commission/fiat_rate_default
//	3. 全站汇率 operation_setting.USDExchangeRate(充值页那个汇率)
//
// 顺序是"越具体越优先",且**第 3 层恒存在** —— 所以不可能出现"没配的分组
// 落进一个不存在的比例"。兜底档在接口层不可清空(见 parseFiatRate 的说明):
// 清空它等于把一批分组悄悄退回充值汇率,而那恰恰是本档要摆脱的东西。
//
// # 逐笔冻结,改比例不追溯
//
// 解析出来的比例在**计佣当刻**写进 qy_commission_accrual.usd_rate,结算时按
// 那一列的加权平均折算(settle.go 的 batchRate/applyFiat)。因此:
//
//   - 改比例只影响**此后**产生的计佣行,以及由它们驱动的结算;
//   - 已经写进 available_fiat 的钱是按当时比例算出的**绝对值**,不会被重算,
//     回收时按额度比例等比缩减,平均比例保持不变。
//
// 这与 UsdRate 一直以来的冻结语义完全一致,本文件没有引入第二套时点规则。
//
// # 零值
//
// 比例 0 既不是"免费"也不是"没配",它是**非法输入**,写入侧一律 400。
// 理由:0 会让 applyFiat 一分法币都不加,而额度照加 —— available_fiat 与
// available_quota 就此永久漂移,提现按 available_fiat 折算会给用户 0 元,
// 而他的站内佣金余额明明是正的。想让某个分组"不赚钱",既有的杠杆是把返佣
// 费率设成 0%(那样两侧都停在 0,账是平的),不是把计价单位设成 0。
// "没配"一律用**缺行 / NULL** 表达,与 RedemptionRateUnits 同一套写法。

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/shopspring/decimal"
	"gorm.io/gorm/clause"
)

// fiatRateScale 是比例的最大小数位,与存储列 decimal(18,8) 一致。
// 超过这个位数的输入直接拒绝而不是四舍五入:悄悄替运营改一个资金参数的
// 最后一位,是本仓明令禁止的那类"没有人签字的调价"。
const fiatRateScale = 8

// maxFiatRateValue 是比例的上界。
//
// 一百万足以覆盖任何真实币种(越南盾、印尼盾在两万五量级,恶性通胀币种也
// 到不了这个数),同时把手滑多敲几个零的后果限制成"一个明显离谱、一眼能
// 看出来的数",而不是一个能把 decimal(18,8) 撑爆的值。
var maxFiatRateValue = decimal.NewFromInt(1000000)

// 三个层级的名字。下发给管理端,界面据此画出"这个分组走的是哪一层"。
const (
	fiatLayerGroup   = "group"
	fiatLayerDefault = "default"
	fiatLayerGlobal  = "global"
	// fiatLayerNone 是三层都拿不出正比例时的结局:全站汇率被改成了 0 或负数。
	// 保留这个状态而不是伪造一个比例 —— 编不出来的汇率一旦落进账本就再也
	// 分不清"当时配的就是这个"还是"当时算错了"。
	fiatLayerNone = "none"
)

// FiatRate 是一条按分组的法币折算比例。
//
// 刻意**不复用** qy_commission_group_rate 那张表,即便两者现在按同一个人的
// 分组判定:一张表意味着"给某个分组配法币比例"必须连带填三个返佣百分比
// (反之亦然),而运营调这两件事的节奏完全不同 —— 费率是招募政策,比例是
// 结汇价格。两张表各自可增删,层级与回落也各写各的(费率回落全局默认值,
// 比例回落兜底档再回落全站汇率),塞进一行会让两套回落规则互相绊住。
type FiatRate struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// GroupName 是主库 users.group 的取值,一律以 groupname.Normalize 之后的
	// 形式存储,理由与 GroupRate.GroupName 逐字相同。
	GroupName string `json:"group_name" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_cfr_group"`

	// Rate 是「1 单位额度价值多少法币」链路上的那个乘数,与
	// qy_commission_accrual.usd_rate 同列型、同语义。
	//
	// 不可为 0(见文件头「零值」)。列上不设 default:落库的每一行都必须
	// 由写入侧显式给出一个合法比例。
	Rate decimal.Decimal `json:"rate" gorm:"type:decimal(18,8);not null"`

	// Enabled 为 false 时该档不生效,等价于回落兜底档 —— 不是"比例 0"。
	Enabled bool `json:"enabled" gorm:"not null"`

	Remark     string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	OperatorId int    `json:"operator_id" gorm:"not null;default:0"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt  int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (FiatRate) TableName() string { return "qy_commission_fiat_rate" }

// ───────────────────────── 解析与格式化 ─────────────────────────

var (
	errFiatRateEmpty    = errors.New("必须填写一个大于 0 的比例")
	errFiatRateFormat   = errors.New("必须是十进制数字")
	errFiatRateScale    = errors.New("最多 8 位小数")
	errFiatRateNonPos   = errors.New("必须大于 0(0 不是「免费」也不是「没配」,想停掉某个分组的佣金请把返佣费率设为 0%)")
	errFiatRateTooLarge = errors.New("超出上限 " + maxFiatRateValue.String())
)

// parseFiatRate 校验并规范化一个法币比例输入。
//
// 空串在这里是**错误**而不是"没配"。这是兜底档"不可删"的落点:分组档要取消
// 有 DELETE 接口(取消之后回落兜底档,层级完整),而兜底档只能改、不能清空 ——
// 清空它会让一批没配分组档的用户悄悄退回充值页汇率,界面上还写着兜底档。
// 想要"兜底档等于充值汇率"就把充值汇率那个数字填进来,让它变成一个显式决定。
func parseFiatRate(raw string) (decimal.Decimal, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return decimal.Zero, errFiatRateEmpty
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, errFiatRateFormat
	}
	if d.Exponent() < -fiatRateScale {
		return decimal.Zero, errFiatRateScale
	}
	if d.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, errFiatRateNonPos
	}
	if d.GreaterThan(maxFiatRateValue) {
		return decimal.Zero, errFiatRateTooLarge
	}
	return d, nil
}

// fiatRateSane 判定一个已经在库里/内存里的比例能不能拿来算钱。
//
// 写入侧的 400 只挡住管理端这一条路。qy_settings 与扩展库都是可以被人手工
// UPDATE 的,而一个 0 或负数的比例造成的是「额度照加、法币不加」这种无声的
// 账本漂移 —— 读取侧必须自己再判一次,判不过就回落下一层并留痕。
func fiatRateSane(d decimal.Decimal) bool {
	return d.GreaterThan(decimal.Zero) && d.LessThanOrEqual(maxFiatRateValue)
}

// ───────────────────────── 缓存 ─────────────────────────

var (
	fiatRateMu     sync.Mutex
	fiatRateCache  map[string]FiatRate
	fiatRateLoaded int64
	fiatRateEpoch  uint64
)

// fiatRates 返回全部分组档,按分组名索引。
//
// 结构与 groupRates 逐字同构(持锁只做读/写快照,SELECT 发在临界区之外,
// 写回时用代次判断快照是否已被作废),不要再分叉出第三种写法。
//
// 与 groupRates 的**唯一**差别:这里不在 SQL 里过滤 enabled,整表都进缓存,
// 由 fiatRateFor 这个纯函数去判 Enabled。层级判定(命中哪一层)是本档最容易
// 出错的地方,把它整个关在一个可以表驱动测试的纯函数里,比省下几行内存值钱。
func fiatRates(ctx context.Context) map[string]FiatRate {
	fiatRateMu.Lock()
	now := common.GetTimestamp()
	if fiatRateCache != nil && now-fiatRateLoaded < settingsCacheSeconds {
		cached := fiatRateCache
		fiatRateMu.Unlock()
		return cached
	}
	epoch := fiatRateEpoch
	snapshot := fiatRateCache
	fiatRateMu.Unlock()

	gdb := db.Get()
	if gdb == nil {
		if snapshot != nil {
			return snapshot
		}
		fiatRateDegrade.note("读取分组法币比例失败: " + db.ErrNotReady.Error())
		return map[string]FiatRate{}
	}
	var rows []FiatRate
	if err := gdb.WithContext(ctx).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		if snapshot != nil {
			return snapshot
		}
		fiatRateDegrade.note("读取分组法币比例失败: " + err.Error())
		return map[string]FiatRate{}
	}
	m := make(map[string]FiatRate, len(rows))
	for _, r := range rows {
		// 与 groupRates 同一条理由:建键与查表必须是同一个函数,
		// 否则升级前写进来的历史行会变成一条永远查不到的规则。
		m[billingGroup(r.GroupName)] = r
	}
	fiatRateMu.Lock()
	if fiatRateEpoch == epoch {
		fiatRateCache = m
		fiatRateLoaded = common.GetTimestamp()
	}
	fiatRateMu.Unlock()
	return m
}

func invalidateFiatRates() {
	fiatRateMu.Lock()
	fiatRateCache = nil
	fiatRateLoaded = 0
	fiatRateEpoch++
	fiatRateMu.Unlock()
}

// ───────────────────────── 层级判定 ─────────────────────────

// fiatDecision 是一次法币比例解析的结果。
type fiatDecision struct {
	// Rate 是本次生效的比例。Layer == fiatLayerNone 时它可能 ≤ 0,
	// 调用方不该据此算钱,但仍要把它冻结进账本行 —— 事实必须留痕。
	Rate decimal.Decimal
	// Layer 是它来自哪一层,只用于解释与界面,不参与算钱。
	Layer string
	// Group 是判定用的**上线**分组。空串表示这次根本没解析出上线的分组
	// (主库读失败),此时分组层被整个跳过。
	Group string
	// Degraded 非空表示"本次没能走到它本该走的那一层",内容是原因。
	//
	// 判定函数刻意**不自己上报**降级:同一个判定同时服务于两条路 ——
	// 真正在算钱的计佣路径,和管理端"这个分组现在按几折算"的试算回显。
	// 在判定里计数会让运营每刷新一次配置页就给降级计数器 +1,而那个计数器
	// 的全部用途是回答"哪段时间的佣金要复核"。上报由算钱那一侧的
	// resolveFiatRate / inviterFiatRate 负责。
	Degraded string
}

// fiatRateFor 是层级判定的纯函数部分。
//
// rule == nil 表示分组层没有可用取值:该分组没配、被禁用、或者调用方根本
// 没能解析出上线的分组。三种情形在结果上没有区别 —— 都往下一层走。
func fiatRateFor(rule *FiatRate, s opSettings) fiatDecision {
	degraded := ""
	if rule != nil && rule.Enabled {
		if fiatRateSane(rule.Rate) {
			return fiatDecision{Rate: rule.Rate, Layer: fiatLayerGroup}
		}
		// 库里存着一个算不了钱的比例。回落下一层是对的,但必须留痕:
		// 降级算出来的账目与正常账目在流水上长得一模一样。
		degraded = "分组 " + rule.GroupName + " 的法币比例 " +
			rule.Rate.String() + " 非法,已回落下一层"
	}
	if s.FiatRateDefault != nil {
		if fiatRateSane(*s.FiatRateDefault) {
			return fiatDecision{Rate: *s.FiatRateDefault, Layer: fiatLayerDefault, Degraded: degraded}
		}
		degraded = "兜底法币比例 " + s.FiatRateDefault.String() + " 非法,已回落全站充值汇率"
	}
	global := currentUsdRate()
	if fiatRateSane(global) {
		return fiatDecision{Rate: global, Layer: fiatLayerGlobal, Degraded: degraded}
	}
	// 三层都拿不出正比例 = 管理员把全站充值汇率改成了 0/负数,而且没配兜底档。
	// 这在本档出现之前就是既有行为(currentUsdRate 原样返回它),这里不改变
	// 结果,只把它从"静默"变成"有计数、有告警"。
	return fiatDecision{
		Rate:  global,
		Layer: fiatLayerNone,
		Degraded: "全站充值汇率 " + global.String() +
			" 非法且没有兜底法币比例,本次佣金的法币折算为 0",
	}
}

// resolveFiatRate 按上线分组解析法币比例,并上报降级。
//
// group 一律是**邀请人**的分组(口径见文件头)。空分组按 default 判定,
// 与 billingGroup 在费率那一侧的口径完全一致。
//
// **不要直接调用它。** 计佣路径一律走 pricing.go 的 resolveInviterPricing,
// 由那里保证费率与本档取的是同一个人同一时刻的分组;直接调用等于重新打开
// 两档口径分叉的那道口子。由 pricing_single_resolver_guard_test.go 钉住。
func resolveFiatRate(ctx context.Context, group string, s opSettings) fiatDecision {
	g := billingGroup(group)
	var rule *FiatRate
	if row, ok := fiatRates(ctx)[g]; ok {
		rule = &row
	}
	d := fiatRateFor(rule, s)
	d.Group = g
	if d.Degraded != "" {
		fiatRateDegrade.note(d.Degraded)
	}
	return d
}

// ───────────────────────── 写入 ─────────────────────────

// upsertFiatRate 新增或覆盖一条分组法币比例。调用方负责写审计。
func upsertFiatRate(ctx context.Context, row *FiatRate) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	// 归一化必须在这里再做一次,理由与 upsertGroupRate 逐字相同:
	// ON DUPLICATE KEY 的命中与否完全取决于这个值,而 group_name 不在
	// DoUpdates 里 —— 写进一个没归一的 "VIP" 会在 ci 排序规则下改掉 vip 那一行。
	row.GroupName = normalizeGroup(row.GroupName)
	if row.GroupName == "" {
		return errEmptyGroupName
	}
	if !fiatRateSane(row.Rate) {
		return errFiatRateNonPos
	}
	now := common.GetTimestamp()
	row.CreatedAt = now
	row.UpdatedAt = now
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "group_name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"rate", "enabled", "remark", "operator_id", "updated_at",
		}),
	}).Create(row).Error
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	invalidateFiatRates()
	return nil
}

// deleteFiatRate 删除一条分组档,该分组随即回落兜底档(兜底档没配才到全站汇率)。
// 返回是否真的删掉了一行,调用方据此区分"删除成功"与"本来就不存在"。
func deleteFiatRate(ctx context.Context, group string) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	g := normalizeGroup(group)
	if g == "" {
		return false, errEmptyGroupName
	}
	res := gdb.WithContext(ctx).Where("group_name = ?", g).Delete(&FiatRate{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	invalidateFiatRates()
	return res.RowsAffected > 0, nil
}

// listFiatRates 返回全部分组档(含被禁用的),供管理端展示。
func listFiatRates(ctx context.Context) ([]FiatRate, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []FiatRate
	if err := gdb.WithContext(ctx).Order("group_name asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}

// findFiatRate 读一条分组档,用于改动前后的审计快照。
func findFiatRate(ctx context.Context, group string) (*FiatRate, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []FiatRate
	if err := gdb.WithContext(ctx).Where("group_name = ?", normalizeGroup(group)).
		Limit(1).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}
