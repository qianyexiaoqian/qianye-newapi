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
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"gorm.io/gorm"
)

var (
	// ErrAmountOutOfRange 表示金额超出主库 users.quota 的 int32 容量。
	// 绝不静默截断:那会让用户凭空少钱或多钱。
	ErrAmountOutOfRange = errors.New("qianye: 金额超出允许范围")
	// ErrInProgress 表示同一幂等键的单据正在处理中。
	ErrInProgress = errors.New("qianye: 该请求正在处理中,请稍候")
	// ErrOrderFailed 表示单据此前已明确失败。
	ErrOrderFailed = errors.New("qianye: 该请求此前已失败")
)

// Request 描述一次跨库资金操作。
type Request struct {
	Kind      string
	IdemScope string
	// IdemKey 与 IdemScope 组成唯一键。重复提交、支付回调重放、补偿重扫
	// 都靠它归并,绝不能省略。
	IdemKey string

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

	order, existing, err := createOrLoadOrder(req)
	if err != nil {
		return nil, err
	}
	if existing {
		return resolveExisting(order)
	}

	// ── 阶段二:主库事务 ──
	applied, mainErr := applyOnMainDB(req, order)
	if mainErr != nil {
		markFailed(order, mainErr)
		return order, mainErr
	}

	// ── 阶段三:提交后的非事务收尾 ──
	if req.AfterCommit != nil {
		safeAfterCommit(req.AfterCommit, order)
	}

	// ── 阶段四:回写扩展库 ──
	if err := markSuccess(order, req.LocalCommit); err != nil {
		// 主库已经生效,这里失败不能回滚。留 pending 交给补偿任务。
		common.SysError(fmt.Sprintf(
			"qianye: 单号 %s 主库已生效但扩展库回写失败,已交由补偿任务处理: %v", order.OrderNo, err))
		return order, nil
	}

	auditTransition(order, qymodel.ResultOK, transitionReason(applied))
	return order, nil
}

// createOrLoadOrder 落 pending 单;幂等键冲突时加载已有单。
func createOrLoadOrder(req Request) (*qymodel.FundOrder, bool, error) {
	now := common.GetTimestamp()
	order := &qymodel.FundOrder{
		OrderNo:     NewOrderNo(req.Kind),
		Kind:        req.Kind,
		Status:      qymodel.StatusPending,
		IdemScope:   req.IdemScope,
		IdemKey:     req.IdemKey,
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

	err := db.Get().Transaction(func(tx *gorm.DB) error {
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
	if e := db.Get().Where("idem_scope = ? AND idem_key = ?", req.IdemScope, req.IdemKey).
		First(&found).Error; e != nil {
		// 也可能是单号碰撞而非幂等键冲突,此时重试一次即可。
		return nil, false, fmt.Errorf("qianye: 单据冲突且无法加载已有单据: %w", e)
	}
	return &found, true, nil
}

// resolveExisting 决定幂等命中时返回什么。
func resolveExisting(order *qymodel.FundOrder) (*qymodel.FundOrder, error) {
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

func markSuccess(order *qymodel.FundOrder, localCommit func(*gorm.DB, *qymodel.FundOrder) error) error {
	now := common.GetTimestamp()
	return db.Get().Transaction(func(tx *gorm.DB) error {
		// CAS:补偿任务可能已抢先处理,RowsAffected == 0 时让出。
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
		order.Status = qymodel.StatusSuccess
		order.SettledAt = now
		order.UpdatedAt = now
		if localCommit != nil {
			return localCommit(tx, order)
		}
		return nil
	})
}

func markFailed(order *qymodel.FundOrder, cause error) {
	now := common.GetTimestamp()
	msg := cause.Error()
	if len(msg) > 512 {
		msg = msg[:512]
	}
	err := db.Get().Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
		Updates(map[string]any{
			"status":     qymodel.StatusFailed,
			"last_error": msg,
			"updated_at": now,
		}).Error
	if err != nil {
		db.MarkFailure(err)
		common.SysError(fmt.Sprintf("qianye: 单号 %s 标记失败态时出错: %v", order.OrderNo, err))
		return
	}
	order.Status = qymodel.StatusFailed
	order.LastError = msg
	auditTransition(order, qymodel.ResultFail, msg)
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
