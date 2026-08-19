package commission

import (
	"context"
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ErrNothingToClawback 表示该下线名下没有可冲正的佣金。
var ErrNothingToClawback = errors.New("commission: 没有可冲正的佣金")

// clawbackIdemKey 生成冲正的幂等键。
//
// 必须在投递到异步队列之前算好并随闭包带走:worker 会重试,
// 每次重试重新生成 key 就等于把一次退款冲正成好几次。
func clawbackIdemKey(taskId string, userId int, quota int64) string {
	if taskId != "" {
		return SourceClawback + ":task:" + taskId + ":" + strconv.FormatInt(quota, 10)
	}
	// 没有任务号时只能用一次性随机键。它保证"同一次退款的多次重试"归并,
	// 但无法归并"同一次退款被上游重复上报"—— 上游没提供任何稳定标识。
	return SourceClawback + ":u" + itoa(userId) + ":" + common.GetUUID()
}

// clawback 为一笔退款生成负额计佣行。
//
// 账本 append-only:冲正永远是一条独立的负额行,绝不去改原行。
// 这样"原本发了多少、后来冲了多少"在任何时刻都能各自查证。
func clawback(ctx context.Context, inviteeId int, refundQuota int64, idemKey, sourceRef, reason string) error {
	if refundQuota <= 0 {
		return nil
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	// 上限必须等 origin 定下来才算得出:它要按"负数行记在谁名下"收口(见
	// netAccrued),而那个人正是 origin.InviterId。这条路径本身是低频的,
	// 多做一次原单查询换一个正确的上限,是划算的。
	origin, err := consumeOriginForRefund(gdb, inviteeId, bucketDate(common.GetTimestamp()))
	if err != nil {
		return err
	}
	if origin == nil {
		// 一条消费计佣行都取不到(消费行还没落库、或全部被作废)。此时宁可按
		// **当前的消费费率**冲正 —— 口径至少是对的,只是时点偏新;借用一条
		// 充值行的费率则连方向都是随机的。
		e, _, err := resolveInviter(ctx, inviteeId)
		if err != nil {
			return err
		}
		if e.InviterId == 0 || e.InviterId == inviteeId {
			return nil
		}
		s := effectiveCtx(ctx)
		// 费率与法币比例都按**当刻的上线分组**解析,走与计佣写入侧同一个入口:
		// 这一路本来就是"没有原单可复制,只能按当前口径",那就该是当前的
		// **完整**口径。分开取(或让 writeAccrual 去拿全站充值汇率)会让配了
		// 分组档的上线在这条路上按另一套口径冲正,而他的原单是按分组档发出去的
		// —— 差额永久留在 available_fiat 上。
		//
		// RefAccrualId 留 0:确实没有可指向的原单,随便挂一行会让管理端
		// "这笔被冲了多少"的溯源指向一笔无关的账。
		p := resolveInviterPricing(ctx, e.InviterId, SourceConsume, s)
		origin = &Accrual{
			InviterId: e.InviterId,
			RateUnits: p.Rate.Units,
			RateGroup: p.Rate.Group,
			UsdRate:   p.Fiat.Rate,
		}
	}

	remaining, err := netAccrued(gdb, inviteeId, origin.InviterId)
	if err != nil {
		return err
	}
	// 冲正上限是"这个下线给这个邀请人一共挣过多少净佣金"。
	// 超额冲正会让邀请人为别人名下的计佣买单。净额为零时这一对要么
	// 从未产生过佣金、要么已经被冲平,两种情况都无事可做。
	if remaining.LessThanOrEqual(decimal.Zero) {
		return nil
	}

	amount := clawbackAmountFor(refundQuota, origin)
	if amount.GreaterThan(remaining) {
		amount = remaining
	}
	if amount.IsZero() {
		return nil
	}

	inserted, err := writeAccrual(ctx, accrualInput{
		SourceType: SourceClawback,
		IdemKey:    idemKey,
		SourceRef:  sourceRef,
		InviterId:  origin.InviterId,
		InviteeId:  inviteeId,
		BaseQuota:  -refundQuota,
		// 冲正原样复制**被退款那笔消费**冻结的费率与分组,绝不用当前值:
		// 原单按 8% 发出去、退款时按现行的 5% 冲回来,差额就永久留在邀请人账上。
		// 这与复制 UsdRate 是同一个道理。
		RateUnits: origin.RateUnits,
		RateGroup: origin.RateGroup,
		Gross:     amount.Neg(),
		UsdRate:   origin.UsdRate,
		// 冲正立即成熟:让它陪着原单等成熟期,等于给"充值→拿佣金→退款"
		// 留出一个可以先提现走人的窗口。
		MatureAt:     0,
		Status:       StatusAccrued,
		RefAccrualId: origin.Id,
		Remark:       truncate(reason, 255),
	})
	if err != nil {
		return err
	}
	// 只有真的插入了新行才计数:worker 重试与上游重复上报都会走到这里,
	// 幂等命中时自增会让"到底冲正了几笔"这个数字失去意义。
	if inserted {
		clawbackCreated.Add(1)
	}
	return nil
}

// clawbackAmountFor 算一笔退款应当冲回多少佣金。
//
// 一般情况就是"按原单冻结的费率重算"。但原单可能被单笔封顶(max_per_order_quota)
// 削过:那一行 gross_amount < base_quota × rate_bps / 10000,按费率重算会冲掉
// 比当初实际发出去的更多 —— 上线为一笔只挣到 100 的消费被冲掉 500,方向是
// 平台凭空多收回,而且冲正行自己也是一条 base×rate ≠ gross 的不自洽行。
// netAccrued 只在冲到这一对(上线,下线)的净额见底时才截住,截不住"多冲了
// 但还没冲光"的中间那一大段。
//
// 所以按原单**实际计到的比例**等比冲正:
//
//	amount = 按费率重算 × gross / (gross + capped)
//
// 没被削过时 capped 为 0,这个式子逐位退化成 calcGross(refundQuota, rate),
// 与改动前完全一致。
func clawbackAmountFor(refundQuota int64, origin *Accrual) decimal.Decimal {
	full := calcGross(refundQuota, origin.RateUnits)
	if !origin.CappedAmount.IsPositive() {
		return full
	}
	theoretical := origin.GrossAmount.Add(origin.CappedAmount)
	if !theoretical.IsPositive() {
		return full
	}
	return full.Mul(origin.GrossAmount).Div(theoretical)
}

// consumeOriginForRefund 找出一笔消费退款应当按哪一行冻结的费率冲正。
//
// 只认 source_type = consume 的正额行,并优先命中被退款那一天的日聚合桶。
//
// 为什么不能取"该下线最近一条正额行":那一行可以是任意来源。默认配置充值
// 10%、消费 5%,于是"当天先消费(落 5% 的日聚合行)、晚些时候充值(落 10%
// 的 topup 行,id 更大)"这种普通用量,就会让随后的一笔任务退款按 10% 去冲
// 一笔只发过 5% 的佣金 —— 正好 2 倍超额冲正。方向由两个费率的大小关系决定:
// 反过来配则是冲正不足,平台永久少收回。账本 append-only,冲错了改不回来。
//
// 返回 nil 表示这个下线没有任何消费计佣行,调用方须改用当前的消费费率,
// 而不是退而求其次去拿一条充值行。
func consumeOriginForRefund(gdb *gorm.DB, inviteeId int, day string) (*Accrual, error) {
	// 两次 Find 而不是一条带 CASE 的 ORDER BY:后者要写方言相关的原生 SQL,
	// 而冲正本身是低频路径,两条查询都走 invitee_id 索引。
	// 用 Find 而不是 Take,是为了让"没有这样的行"是空切片而不是一个要靠
	// errors.Is 分辨的错误。
	var rows []Accrual
	err := gdb.Where("invitee_id = ? AND source_type = ? AND bucket_date = ? AND gross_amount > 0",
		inviteeId, SourceConsume, day).Order("id desc").Limit(1).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	if len(rows) > 0 {
		return &rows[0], nil
	}
	// 跨天退款(异步任务隔夜结算、上游延迟上报)回落到该下线最近的一条消费行:
	// 费率口径仍然是对的,只是可能不是被退款那一天的那条桶。
	err = gdb.Where("invitee_id = ? AND source_type = ? AND gross_amount > 0",
		inviteeId, SourceConsume).Order("id desc").Limit(1).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// netAccrued 返回**这一对(邀请人, 下线)**之间的净计佣额(已扣除历史冲正)。
//
// 必须同时按 inviter_id 收口,不能只按 invitee_id:负数行是记在 origin.InviterId
// 名下的,上限却按别人名下的计佣算出来,就等于让 A 挣的钱去给 B 的冲正兜底。
// 两条真实路径会踩到:
//
//   - 换绑(api_admin_relation.go 的 rebindRelation 明确支持,且历史佣金留在原
//     邀请人名下):下线先给 A 挣了 100 万,换绑到 B 之后只挣了 10,这时对 B 的
//     那一行冲正 50 万照样通过,B 凭空背上 50 万欠账。
//   - 手工调整(api_admin_adjust.go 写 InviteeId: 0):按 invitee_id=0 汇总等于
//     "全站所有手工调整之和",上限彻底失去意义。实测中一个只入账过 1000 的账号
//     被冲正了 30 万,unsettled_amount 变成 -299000 且 debt_blocked 置位 ——
//     佣金侧的 available+frozen+withdrawn == earned-clawback 恒等式照样成立,
//     超额部分全进了负数结转,对账发现不了。
func netAccrued(gdb *gorm.DB, inviteeId, inviterId int) (decimal.Decimal, error) {
	var raw string
	err := gdb.Model(&Accrual{}).
		Where("invitee_id = ? AND inviter_id = ? AND status <> ?", inviteeId, inviterId, StatusVoided).
		Select("COALESCE(SUM(gross_amount), 0)").Scan(&raw).Error
	if err != nil {
		db.MarkFailure(err)
		return decimal.Zero, err
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, nil
	}
	return d, nil
}

// manualClawback 是管理端的人工冲正入口。
//
// quota 是管理员填写的整数额度,不按费率换算 —— 人工冲正的场景(拒付、
// 事后判定为刷单)本来就无法用费率反推。
func manualClawback(ctx context.Context, accrualId int64, quota int64, idemSuffix, reason string) (*Accrual, error) {
	if quota <= 0 || quota > int64(common.MaxQuota) {
		return nil, errors.New("commission: 冲正额度必须大于 0 且不超过单笔上限")
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)
	var origin Accrual
	if err := gdb.Where("id = ?", accrualId).Take(&origin).Error; err != nil {
		return nil, ErrNothingToClawback
	}

	key := SourceClawback + ":manual:" + idemSuffix

	// 幂等回读必须**早于**"还有没有可冲的钱"这道判断。
	//
	// 一次把某一对(上线,下线)的净额冲满(或超额冲正被削到恰好归零)之后,
	// remaining 就是 0。此时同一个 client_request_id 的合法重试(HTTP 超时后
	// 前端原样重发,弹窗不换键)会被下面的 remaining<=0 提前拦掉,拿到一条
	// "没有可冲正的佣金" —— 与事实完全相反:钱其实已经冲掉了。管理员照着这句
	// 提示会去改金额再试,而专门为重放写的参数比对与 409 冲突保护在这条路径上
	// 根本到不了,"同一个键被换了参数重放"这件事也就无法被识别。
	var replay Accrual
	err := gdb.Where("idem_scope = ? AND idem_key = ?", SourceClawback, normalizeIdemKey(key)).
		Take(&replay).Error
	if err == nil {
		if err := sameClawbackRequest(&replay, accrualId, quota); err != nil {
			return nil, err
		}
		return &replay, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	amount := decimal.NewFromInt(quota)
	remaining, err := netAccrued(gdb, origin.InviteeId, origin.InviterId)
	if err != nil {
		return nil, err
	}
	if remaining.LessThanOrEqual(decimal.Zero) {
		return nil, ErrNothingToClawback
	}
	if amount.GreaterThan(remaining) {
		amount = remaining
	}

	inserted, err := writeAccrual(ctx, accrualInput{
		SourceType: SourceClawback,
		IdemKey:    key,
		SourceRef:  origin.AccrualNo,
		InviterId:  origin.InviterId,
		InviteeId:  origin.InviteeId,
		// BaseQuota 冻结"管理员这一次填了多少",充当幂等指纹的金额分量。
		// 不能拿 Gross 反推:Gross 已被 remaining 削过,同一个请求在不同
		// 时刻会落出不同的值,拿它比对会把合法重试误判成冲突。
		// 取负号与自动冲正路径(clawback)保持同一符号约定。
		BaseQuota:    -quota,
		RateUnits:    origin.RateUnits,
		RateGroup:    origin.RateGroup,
		Gross:        amount.Neg(),
		UsdRate:      origin.UsdRate,
		MatureAt:     0,
		Status:       StatusAccrued,
		RefAccrualId: origin.Id,
		Remark:       truncate(reason, 255),
	})
	if err != nil {
		return nil, err
	}

	var created Accrual
	if err := gdb.Where("idem_scope = ? AND idem_key = ?", SourceClawback, normalizeIdemKey(key)).
		Take(&created).Error; err != nil {
		return nil, err
	}
	if !inserted {
		// 幂等命中。必须确认重放的确实是同一个请求:client_request_id 由前端
		// 生成并在弹窗打开时缓存,管理员改了 accrual_id 或金额再提交会复用同一个键。
		// 不比对就会返回旧单,而调用方按"本次新建"写下一条金额虚高的成功审计。
		if err := sameClawbackRequest(&created, accrualId, quota); err != nil {
			return nil, err
		}
		return &created, nil
	}
	clawbackCreated.Add(1)
	return &created, nil
}

// ErrClawbackIdemConflict 表示同一个幂等键被换了参数重放。
var ErrClawbackIdemConflict = errors.New("commission: 幂等键已被另一次冲正请求占用")

// clawbackAuditAmount 返回一次冲正在审计里应当记的金额。
//
// 必须取账本行的真实 Gross,而不是请求里的 quota:幂等重放与 remaining 削减
// 两种情况下 quota 都与资金侧实际发生的金额不符,而审计表是这套资金系统
// 事后仲裁的唯一凭据。先 Floor 再取整,不用 QuotaFromDecimal 自带的
// 四舍五入 —— 审计金额宁可略小于账本也不能虚报。取绝对值是因为
// AmountQuota 只记标量,"这是一次冲正"由 Action 表达。
func clawbackAuditAmount(a *Accrual) int64 {
	return int64(common.QuotaFromDecimal(a.GrossAmount.Abs().Floor()))
}

// sameClawbackRequest 比对幂等命中的旧单与本次请求的资金要素。
//
// 只比"请求本身说了什么"(冲哪一行、冲多少),不比落库后的 Gross ——
// Gross 受 remaining 削减,同一个请求在不同时刻可能得出不同的值。
func sameClawbackRequest(created *Accrual, accrualId, quota int64) error {
	if created.RefAccrualId != accrualId {
		return ErrClawbackIdemConflict
	}
	// BaseQuota 是本次修复才开始写入的指纹分量,历史行为 0。
	// 空指纹一律放行:把升级前落的老单判成冲突,等于让管理员永远重试不了。
	if created.BaseQuota != 0 && created.BaseQuota != -quota {
		return ErrClawbackIdemConflict
	}
	return nil
}
