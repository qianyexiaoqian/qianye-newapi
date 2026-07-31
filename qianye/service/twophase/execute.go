// Package twophase 实现跨库两阶段资金操作。
//
// 要解决的根本问题:记账在扩展库、钱在主库,两者不能放进同一个事务。
// 中间必然存在一个窗口 —— 主库已提交但扩展库还没回写。进程在这一刻崩溃,
// 钱已经动了却没有任何记录,这在资金系统里是不可接受的。
//
// 解法是主库 outbox:把单号连同资金变更写进同一个主库事务。
// 之后无论进程怎么崩,补偿任务都能通过查询 outbox 精确判定"主库到底动没动"。
//
// 划转、佣金兑现、提现到账、佣金冲正全部走这里,不要各写一套。
package twophase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"gorm.io/gorm"
)

// maxErrBytes 是 qy_fund_orders.last_error 的列宽(varchar(512))。
const maxErrBytes = 512

var (
	// ErrAmountOutOfRange 表示金额超出主库 users.quota 的 int32 容量。
	// 绝不静默截断:那会让用户凭空少钱或多钱。
	ErrAmountOutOfRange = errors.New("qianye: 金额超出允许范围")
	// ErrInProgress 表示同一幂等键的单据正在处理中。
	ErrInProgress = errors.New("qianye: 该请求正在处理中,请稍候")
	// ErrOrderFailed 表示单据此前已明确失败。
	ErrOrderFailed = errors.New("qianye: 该请求此前已失败")
	// ErrIdemConflict 表示同一幂等键上一次携带的资金要素与本次不同。
	//
	// 这不是"重复提交",是"用同一个键提交了另一笔请求"。必须让调用方按 409 处理:
	// 既不能返回原单的成功,更不能拿本次请求的参数去写成功审计。
	ErrIdemConflict = errors.New("qianye: 幂等键与原请求的资金要素不一致")
	// ErrOrderDiverged 表示回写时发现单据已被别的路径推进到非成功状态。
	// 钱可能已经动了,调用方绝不能按"本次成功"收尾。
	ErrOrderDiverged = errors.New("qianye: 单据已被其他路径推进")
)

// Request 描述一次跨库资金操作。
type Request struct {
	Kind      string
	IdemScope string
	// IdemKey 与 IdemScope 组成唯一键。重复提交、支付回调重放、补偿重扫
	// 都靠它归并,绝不能省略。
	IdemKey string

	// Fingerprint 是本次请求资金要素的摘要,用 Digest() 生成。
	//
	// 唯一键只保证"不重复执行",保证不了"重放的是同一个请求" —— 幂等键往往由
	// 前端生成,换金额、换收款人复用同一个键时,幂等命中会返回原单成功。
	// 填了它,resolveExisting 会在不一致时返回 ErrIdemConflict。
	//
	// 可选:留空表示不参与校验(历史单据的列值也是空)。空值一律跳过校验,
	// 不是判为不匹配 —— 否则升级瞬间全部历史重放都会变成冲突。
	Fingerprint string

	UserId     int
	PeerUserId int

	AmountQuota int64
	FeeQuota    int64

	RefType string
	RefId   string

	// LocalDetail 在创建 pending 单的同一扩展库事务内插入业务明细行。
	// 明细与单据必须同生共死,否则会出现有单无明细的孤儿数据。
	LocalDetail func(tx *gorm.DB, order *qymodel.FundOrder) error

	// MainApply 在主库事务内执行真正的资金变更。
	//
	// 实现约定(违反会出资损):
	//   - 扣款一律用 WHERE quota >= ? 并检查 RowsAffected == 1,
	//     绝不使用 DecreaseUserQuota(它没有余额校验,会扣成负数)
	//   - 加款前校验不超过 common.MaxQuota
	//   - 涉及多个用户时按 user id 升序加锁,避免 A→B / B→A 死锁
	//   - 禁止调用 user.Update() 或 IncrementUserAuthVersionWithTx(会吊销用户会话)
	MainApply func(tx *gorm.DB, order *qymodel.FundOrder) error

	// AfterCommit 在主库事务提交后执行:缓存失效、写账本日志。
	// 失败只记日志,不影响资金结果 —— 钱已经动了,不能因为日志没写就回滚。
	AfterCommit func(order *qymodel.FundOrder)

	// LocalCommit 在扩展库回写 success 的同一事务内执行业务副作用,
	// 例如扣减佣金余额。
	LocalCommit func(tx *gorm.DB, order *qymodel.FundOrder) error
}

// Digest 按**用户这次请求说了什么**算出指纹,供调用方填进 Fingerprint:
//
//	r := twophase.Request{...}
//	r.Fingerprint = r.Digest()          // 需要额外要素时 r.Digest(payeeDigest)
//
// 抽成方法而不是让各模块自己拼字符串:字段顺序或分隔符只要有一处不一致,
// 同一笔请求就会算出不同指纹,幂等重放会被误判成冲突 —— 那是把"防伪造审计"
// 变成"正常重试打不通"。extra 用于承载不在 Request 里但决定资金去向的要素
// (例如提现的收款方式与账号摘要)。
//
// 口径:只收"请求要素",不收"服务端派生量"。
//
// 因此刻意**不含 FeeQuota**:手续费是受理时按当时的 transfer.fee_* 配置算出来的,
// 用户在请求里根本说不了它。用户首次提交撞上超时、运营恰好在这中间调了费率,
// 客户端拿同一个 client_request_id 重试就会算出另一个指纹 → ErrIdemConflict → 409
// 「请到划转记录中核对」,而原单其实压根没成功,用户既转不成也不敢重发。
// 这与 commission/clawback.go 的 sameClawbackRequest 刻意不比 GrossAmount 是同一条
// 理由(被服务端削过/派生的量不能当指纹分量),两处口径必须一致。
//
// AmountQuota 必须留下:它就是用户请求里写的金额,换金额重放正是指纹要识破的攻击。
// 手续费由金额与配置唯一决定,漏掉它也不会放过任何一种换参重放。
//
// 用 \x1f(单元分隔符)而不是竖线做分隔:它不会出现在业务字符串里,
// 避免 "a|b" 与 "a" + "|b" 撞出同一个指纹。
func (r Request) Digest(extra ...string) string {
	parts := make([]string, 0, 7+len(extra))
	parts = append(parts,
		r.Kind,
		r.IdemScope,
		strconv.Itoa(r.UserId),
		strconv.Itoa(r.PeerUserId),
		strconv.FormatInt(r.AmountQuota, 10),
		r.RefType,
		r.RefId,
	)
	parts = append(parts, extra...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	// sha256 的十六进制正好 64 字符,与 qy_fund_orders.fingerprint 的列宽一致。
	return hex.EncodeToString(sum[:])
}

// Execute 执行一次跨库资金操作。
//
// 步骤:
//  1. 【扩展库事务】落 pending 单 + 业务明细。幂等键冲突则返回已有单的结果。
//  2. 【主库事务】登记 outbox → 执行资金变更。outbox 冲突说明此前已生效,跳过。
//  3. 提交后:缓存失效 + 写账本日志(失败不影响结果)。
//  4. 【扩展库事务】回写 success + 业务副作用。
//
// 若步骤 2 提交成功但步骤 4 失败,单据停在 pending,由补偿任务通过 outbox 探针修复。
func Execute(ctx context.Context, req Request) (*qymodel.FundOrder, error) {
	if !db.Available() {
		return nil, db.ErrNotReady
	}
	if err := validateAmount(req.AmountQuota); err != nil {
		return nil, err
	}
	if req.IdemScope == "" || req.IdemKey == "" {
		return nil, errors.New("qianye: 缺少幂等键")
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}

	order, existing, err := createOrLoadOrder(gdb, req)
	if err != nil {
		return nil, err
	}
	if existing {
		return resolveExisting(order, req.Fingerprint)
	}

	// ── 阶段二:主库事务 ──
	applied, mainErr := applyOnMainDB(req, order)
	if mainErr != nil {
		markFailed(gdb, order, mainErr)
		return order, mainErr
	}

	// ── 阶段三:提交后的非事务收尾 ──
	if req.AfterCommit != nil {
		safeAfterCommit(req.AfterCommit, order)
	}

	// ── 阶段四:回写扩展库 ──
	won, err := markSuccess(gdb, order, req.LocalCommit)
	if err != nil {
		// 主库已经生效,这里失败不能回滚。留 pending 交给补偿任务。
		common.SysError(fmt.Sprintf(
			"qianye: 单号 %s 主库已生效但扩展库回写失败,已交由补偿任务处理: %v", order.OrderNo, err))
		return order, nil
	}
	if !won {
		// CAS 没命中:补偿任务或人工裁决已经推进了这张单,order 里已是回读到的真实状态。
		// 这条转移不是我们做的,审计留给赢下 CAS 的那一方写,这里绝不能补一条 ResultOK ——
		// 对方可能推成的是 Failed/Uncertain。
		return order, divergedError(order)
	}

	auditTransition(order, qymodel.ResultOK, transitionReason(applied))
	return order, nil
}

// createOrLoadOrder 落 pending 单;幂等键冲突时加载已有单。
func createOrLoadOrder(gdb *gorm.DB, req Request) (*qymodel.FundOrder, bool, error) {
	now := common.GetTimestamp()
	order := &qymodel.FundOrder{
		OrderNo:     NewOrderNo(req.Kind),
		Kind:        req.Kind,
		Status:      qymodel.StatusPending,
		IdemScope:   req.IdemScope,
		IdemKey:     req.IdemKey,
		Fingerprint: req.Fingerprint,
		UserId:      req.UserId,
		PeerUserId:  req.PeerUserId,
		AmountQuota: req.AmountQuota,
		FeeQuota:    req.FeeQuota,
		RefType:     req.RefType,
		RefId:       req.RefId,
		NodeName:    common.NodeName,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	err := gdb.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		if req.LocalDetail != nil {
			return req.LocalDetail(tx, order)
		}
		return nil
	})
	if err == nil {
		return order, false, nil
	}
	if !isDuplicateKey(err) {
		db.MarkFailure(err)
		return nil, false, err
	}

	// 幂等命中:加载已有单。
	var found qymodel.FundOrder
	if e := gdb.Where("idem_scope = ? AND idem_key = ?", req.IdemScope, req.IdemKey).
		First(&found).Error; e != nil {
		// 也可能是单号碰撞而非幂等键冲突,此时重试一次即可。
		return nil, false, fmt.Errorf("qianye: 单据冲突且无法加载已有单据: %w", e)
	}
	return &found, true, nil
}

// resolveExisting 决定幂等命中时返回什么。
//
// want 是本次请求的指纹。指纹校验必须排在状态分支之前:参数对不上时,无论原单
// 是成功还是失败,本次都不是"同一个请求的重放",调用方拿原单的结果去回应本次请求
// 就是在伪造记录(资金侧毫无痕迹,审计表却多出一条金额虚高的成功记录)。
func resolveExisting(order *qymodel.FundOrder, want string) (*qymodel.FundOrder, error) {
	// 任一侧为空都跳过:历史行的列值是空,尚未接入指纹的调用方传的也是空。
	// 把空当成"不匹配"会让升级瞬间所有正常重放变成 409。
	if want != "" && order.Fingerprint != "" && want != order.Fingerprint {
		return order, fmt.Errorf("%w: 单号 %s", ErrIdemConflict, order.OrderNo)
	}
	switch order.Status {
	case qymodel.StatusSuccess, qymodel.StatusReversed:
		return order, nil // 幂等命中,不再执行任何副作用
	case qymodel.StatusFailed:
		if order.LastError != "" {
			return order, fmt.Errorf("%w: %s", ErrOrderFailed, order.LastError)
		}
		return order, ErrOrderFailed
	default:
		grace := int64(config.Get().TwoPhase.PendingGraceSeconds)
		if common.GetTimestamp()-order.CreatedAt < grace {
			return order, ErrInProgress
		}
		// 超过宽限期的 pending 单交给补偿任务,不在请求线程里重试:
		// 重试需要主库探针,那是慢操作,不该阻塞用户请求。
		return order, ErrInProgress
	}
}

// applyOnMainDB 在主库事务内登记 outbox 并执行资金变更。
// 返回值表示本次是否真正执行了资金变更(false = 此前已生效)。
func applyOnMainDB(req Request, order *qymodel.FundOrder) (bool, error) {
	appliedNow := false
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if config.Get().TwoPhase.OutboxEnabled() {
			claimed, err := model.QyClaimFundOutbox(tx, &model.QyFundOutbox{
				OrderNo: order.OrderNo,
				Kind:    order.Kind,
				UserId:  order.UserId,
				PeerId:  order.PeerUserId,
				Amount:  order.AmountQuota,
			})
			if err != nil {
				return err
			}
			if !claimed {
				// 此前已生效(补偿任务重跑场景),必须跳过资金变更否则重复扣款。
				return nil
			}
		}
		appliedNow = true
		if req.MainApply == nil {
			return nil
		}
		return req.MainApply(tx, order)
	})
	return appliedNow, err
}

// markSuccess 把 pending 单回写成 success 并执行业务副作用。
//
// 返回值 won 表示本次是否赢下了 CAS。false 意味着补偿任务或人工裁决已抢先推进
// 这张单:此时 order 已被刷成回读到的真实状态,LocalCommit 不执行(赢家的
// Resolver 负责补做),调用方也不能按"本次成功"收尾。
func markSuccess(gdb *gorm.DB, order *qymodel.FundOrder, localCommit func(*gorm.DB, *qymodel.FundOrder) error) (bool, error) {
	now := common.GetTimestamp()
	won := false
	err := gdb.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&qymodel.FundOrder{}).
			Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
			Updates(map[string]any{
				"status":     qymodel.StatusSuccess,
				"settled_at": now,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		won = true
		order.Status = qymodel.StatusSuccess
		order.SettledAt = now
		order.UpdatedAt = now
		if localCommit != nil {
			return localCommit(tx, order)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if !won {
		reloadStatus(gdb, order)
	}
	return won, nil
}

// markFailed 把 pending 单回写成 failed。
//
// WHERE 里的 status = Pending 不是可有可无的乐观锁,而是这张单终态的唯一归属判定:
// 补偿任务在探针耗尽时会把单推成 Uncertain —— 那是系统里"我不知道钱动没动,禁止
// 自动回滚"的唯一出口。业务线程随后拿到一个确定性错误并不能推翻它(主库事务可能
// 在超时后才提交)。若这里无视 RowsAffected == 0 仍在内存里改成 Failed 并写审计,
// 调用方会据此走回滚分支(清预占、解冻佣金),最终 qy_fund_orders 是 Uncertain
// 等人工、业务明细已 failed 且预占已释放、审计声称转成了 failed —— 三份记录互相
// 矛盾,而 Uncertain 单不会再被 Compensate 扫到(它只查 pending),矛盾永不自愈。
//
// 所以 CAS 落空时必须回读真实状态交还给调用方(它们都按 order.Status == Failed
// 才回滚),并且不写审计:这条转移不是我们做的。
func markFailed(gdb *gorm.DB, order *qymodel.FundOrder, cause error) {
	now := common.GetTimestamp()
	msg := audit.Truncate(cause.Error(), maxErrBytes)
	res := gdb.Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
		Updates(map[string]any{
			"status":     qymodel.StatusFailed,
			"last_error": msg,
			"updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError(fmt.Sprintf("qianye: 单号 %s 标记失败态时出错: %v", order.OrderNo, res.Error))
		return
	}
	if res.RowsAffected == 0 {
		reloadStatus(gdb, order)
		common.SysError(fmt.Sprintf(
			"qianye: 单号 %s 标记失败态时发现单据已被其他路径推进为 %s(原始错误: %v),本次不改写状态、不写审计",
			order.OrderNo, qymodel.StatusName(order.Status), cause))
		return
	}
	order.Status = qymodel.StatusFailed
	order.LastError = msg
	order.UpdatedAt = now
	auditTransition(order, qymodel.ResultFail, msg)
}

// reloadStatus 在 CAS 落空后回读单据的真实状态。
//
// 回读失败时刻意保留内存里的 pending:调用方对 pending 的处理是"不回滚,交给补偿
// 任务",这是所有分支里最安全的那个。绝不能猜一个终态。
func reloadStatus(gdb *gorm.DB, order *qymodel.FundOrder) {
	var cur qymodel.FundOrder
	if err := gdb.Where("order_no = ?", order.OrderNo).Take(&cur).Error; err != nil {
		db.MarkFailure(err)
		common.SysError(fmt.Sprintf("qianye: 单号 %s 回读状态失败,按 pending 交给补偿任务: %v",
			order.OrderNo, err))
		return
	}
	order.Status = cur.Status
	order.LastError = cur.LastError
	order.SettledAt = cur.SettledAt
	order.UpdatedAt = cur.UpdatedAt
}

// divergedError 描述"回写时 CAS 落空"的结局。
//
// 对方推成 Success/Reversed 时返回 nil:主库确实生效了,业务收尾由赢家的
// Resolver 负责,这与幂等重放同义,不该让用户看到失败。
// 推成 Failed/Uncertain 时必须返回错误 —— 主库这一侧已经动过钱,而单据说没有,
// 调用方需要走各自的冲突处理(探针复核 → 转人工),绝不能当成功返回。
func divergedError(order *qymodel.FundOrder) error {
	if order.Status == qymodel.StatusSuccess || order.Status == qymodel.StatusReversed {
		return nil
	}
	return fmt.Errorf("%w: 单号 %s 当前状态 %s", ErrOrderDiverged, order.OrderNo,
		qymodel.StatusName(order.Status))
}

// safeAfterCommit 执行提交后收尾。此时钱已经动了,任何 panic 都不能冒泡。
func safeAfterCommit(fn func(*qymodel.FundOrder), order *qymodel.FundOrder) {
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf("qianye: 单号 %s 的提交后处理发生 panic(已拦截): %v",
				order.OrderNo, r))
		}
	}()
	fn(order)
}

func auditTransition(order *qymodel.FundOrder, result, reason string) {
	audit.Write(nil, audit.Entry{
		TraceNo:      order.OrderNo,
		Category:     qymodel.AuditCategoryFund,
		Action:       "fund." + order.Kind + "." + qymodel.StatusName(order.Status),
		ActorType:    qymodel.ActorSystem,
		ActorUserId:  order.UserId,
		TargetUserId: order.PeerUserId,
		AmountQuota:  order.AmountQuota,
		Result:       result,
		Reason:       reason,
	})
}

func transitionReason(appliedNow bool) string {
	if appliedNow {
		return ""
	}
	return "主库侧此前已生效,本次跳过资金变更"
}

// validateAmount 校验金额落在主库 users.quota 的 int32 容量内。
//
// 扩展库用 int64 承载聚合与中间量,但主库的额度列是 int32。
// 跨库前必须显式拒绝越界值,绝不能让它静默溢出成负数。
func validateAmount(amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("%w: 金额必须大于 0", ErrAmountOutOfRange)
	}
	if amount > int64(common.MaxQuota) {
		return fmt.Errorf("%w: 金额 %d 超过单笔上限 %d", ErrAmountOutOfRange, amount, common.MaxQuota)
	}
	return nil
}

func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate entry") ||
		strings.Contains(msg, "error 1062") ||
		strings.Contains(msg, "duplicate key")
}
