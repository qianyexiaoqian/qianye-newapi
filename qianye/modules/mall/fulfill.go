package mall

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"gorm.io/gorm"
)

// fulfill.go —— 管理端的履行与退款动作(design §6.4):
//
//	physical  ship(paid → shipped,可选一步到 done)、fail(paid|shipped → failed,退)
//	code      revoke-code(done → revoked,码标 revoked,退)
//	plan      adjudicate(held → done / failed,凭人工核对结论)
//
// 每一条都先锁订单行再做带状态条件的 CAS(applyTransition),并发的两个管理员
// 后到者拿到"状态刚刚被改变",而不是把同一张单处理两次。

const (
	maxTrackingRunes = 128
	maxShipNoteRunes = 255
	maxReasonRunes   = 255
	// minReasonRunes:退款类动作的事由必须写清楚,它是这笔星屑事后唯一的解释。
	minReasonRunes = 2
)

// 人工裁决的两种结论。取值直接进审计,用稳定的英文标识。
const (
	VerdictApplied    = "applied"
	VerdictNotApplied = "not_applied"
)

var (
	errAdjudicateNotHeld = newBizError(http.StatusConflict, codeBadStatus,
		"只有「待核对」(held)的套餐订单才需要人工裁决:其余状态要么还在自动链路里,要么已经落定")
	errAdjudicateNoOrder = newBizError(http.StatusConflict, codeBadStatus,
		"这张订单没有资金单,不存在要核对的主库流水")
	errAdjudicateOrderSettled = newBizError(http.StatusConflict, codeBadStatus,
		"这张订单的资金单已经成功,等待对账任务收尾即可,不必人工裁决")
)

// shipInput 是发货的请求体(契约 §5,done 是本实现追加的可选参数)。
type shipInput struct {
	TrackingNo string `json:"tracking_no"`
	ShipNote   string `json:"ship_note"`
	// Done 为 true 时发货后直接完结(shipped → done);对已 shipped 的单只做完结。
	Done bool `json:"done"`
}

// reasonInput 是 fail / revoke-code 的请求体。
type reasonInput struct {
	Reason string `json:"reason"`
}

// adjudicateInput 是人工裁决的请求体。
type adjudicateInput struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// acceptReason 校验退款类动作的事由。
func acceptReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(reason)
	if n < minReasonRunes || n > maxReasonRunes {
		return "", errBadRequest("必须填写事由(2~255 个字符),它会进入审计与订单时间线")
	}
	return reason, nil
}

// shipOrder 给实物订单填单号发货;done=true 时一步完结。
func shipOrder(ctx context.Context, orderNo string, in shipInput, adminId int) (*Order, error) {
	handle := db.Get()
	if handle == nil {
		return nil, db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)
	tracking := strings.TrimSpace(in.TrackingNo)
	if utf8.RuneCountInString(tracking) > maxTrackingRunes {
		return nil, errBadRequest("物流单号不超过 128 个字符")
	}
	note := strings.TrimSpace(in.ShipNote)
	if utf8.RuneCountInString(note) > maxShipNoteRunes {
		return nil, errBadRequest("发货备注不超过 255 个字符")
	}

	var out *Order
	err := gdb.Transaction(func(tx *gorm.DB) error {
		o, err := lockOrderByNo(tx, orderNo)
		if err != nil {
			return err
		}
		if o.Kind != KindPhysical {
			return errBadStatus
		}
		now := common.GetTimestamp()
		switch o.Status {
		case StatusPaid:
			if tracking == "" {
				return errBadRequest("发货必须填写物流单号")
			}
			// 奖品单可能还没有地址:没有地址就没有可以发的货。
			if addressMissing(o) {
				return errAddressMissing
			}
			if err := applyTransition(tx, o, transition{
				From: []string{StatusPaid}, To: StatusShipped, Action: ActionShip,
				Note: "已发货,物流单号 " + tracking, ActorId: adminId,
				Updates: map[string]any{"tracking_no": tracking, "ship_note": note},
			}); err != nil {
				return err
			}
			o.TrackingNo, o.ShipNote = tracking, note
			if !in.Done {
				break
			}
			if err := applyTransition(tx, o, transition{
				From: []string{StatusShipped}, To: StatusDone, Action: ActionDone,
				Note: "发货即完结", ActorId: adminId, Updates: map[string]any{"fulfilled_at": now},
			}); err != nil {
				return err
			}
			o.FulfilledAt = now
		case StatusShipped:
			if !in.Done {
				return errBadStatus
			}
			updates := map[string]any{"fulfilled_at": now}
			if tracking != "" {
				updates["tracking_no"] = tracking
				o.TrackingNo = tracking
			}
			if err := applyTransition(tx, o, transition{
				From: []string{StatusShipped}, To: StatusDone, Action: ActionDone,
				Note: "已完结", ActorId: adminId, Updates: updates,
			}); err != nil {
				return err
			}
			o.FulfilledAt = now
		default:
			return errBadStatus
		}
		out = o
		return nil
	})
	if err != nil {
		return nil, bizOrInternal("发货", err)
	}
	return out, nil
}

// failOrder 由管理员把实物订单标记为失败并退星屑。
func failOrder(ctx context.Context, orderNo, reason string, adminId int) (*Order, error) {
	handle := db.Get()
	if handle == nil {
		return nil, db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)
	var out *Order
	err := gdb.Transaction(func(tx *gorm.DB) error {
		o, err := lockOrderByNo(tx, orderNo)
		if err != nil {
			return err
		}
		if o.Kind != KindPhysical || !statusIn(o.Status, []string{StatusPaid, StatusShipped}) {
			return errBadStatus
		}
		if err := refundAndTransition(tx, o, "管理员标记失败: "+reason, transition{
			From: []string{StatusPaid, StatusShipped}, To: StatusFailed, Action: ActionFail,
			Note: reason + "(" + stardust.UnitName() + "已退回)", ActorId: adminId,
			Updates: map[string]any{"fail_reason": reason},
		}); err != nil {
			return err
		}
		o.FailReason = reason
		out = o
		return nil
	})
	if err != nil {
		return nil, bizOrInternal("标记失败", err)
	}
	return out, nil
}

// revokeCode 撤回一枚已发出的兑换码并退星屑。码标 revoked 但**不清空密文**:
// 用户可能已经用掉了那串码,抹掉记录等于抹掉争议时唯一的证据。
func revokeCode(ctx context.Context, orderNo, reason string, adminId int) (*Order, error) {
	handle := db.Get()
	if handle == nil {
		return nil, db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)
	var out *Order
	err := gdb.Transaction(func(tx *gorm.DB) error {
		o, err := lockOrderByNo(tx, orderNo)
		if err != nil {
			return err
		}
		if o.Kind != KindCode {
			return errBadStatus
		}
		if o.Status == StatusRevoked {
			return errCodeRevoked
		}
		if o.Status != StatusDone {
			return errBadStatus
		}
		res := tx.Model(&CodeStock{}).
			Where("id = ? AND order_id = ? AND status = ?", o.CodeStockId, o.Id, CodeIssued).
			Update("status", CodeRevoked)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errStatusConflict
		}
		if err := refundAndTransition(tx, o, "管理员撤回兑换码: "+reason, transition{
			From: []string{StatusDone}, To: StatusRevoked, Action: ActionRevokeCode,
			Note: reason + "(" + stardust.UnitName() + "已退回)", ActorId: adminId,
			Updates: map[string]any{"fail_reason": reason},
		}); err != nil {
			return err
		}
		o.FailReason = reason
		out = o
		return nil
	})
	if err != nil {
		return nil, bizOrInternal("撤回兑换码", err)
	}
	return out, nil
}

// adjudicateOrder 凭人工核对结论给一张 held 的套餐订单落定(D-K 的 root 裁决端点)。
//
// 它补的死角:资金单被判 Failed,而主库探针说"已生效"或"判不出来"。补偿任务不扫
// Failed、复判只扫 Uncertain、对账退款只认 MainNotApplied —— 三条路全堵死,这一单
// 永远挂在 held。出口是**人看过主库之后的结论**,不是再探一次针。
//
//	applied      资金单 failed → success 走与补偿任务逐字相同的收尾(Resolver 把订单推到 done)
//	not_applied  退星屑、订单 held → failed;资金单保持 failed(那是事实,账本 append-only)
//
// 与补偿任务的互斥是结构性的:只收资金单处于 Failed 的那一笔,Pending / InDoubt /
// Uncertain 一律拒绝 —— 那三种状态下人和任务会对同一笔单双写。
func adjudicateOrder(ctx context.Context, orderNo, verdict, reason string, adminId int) (*Order, error) {
	handle := db.Get()
	if handle == nil {
		return nil, db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)
	o, err := loadOrderByNo(gdb, orderNo)
	if err != nil {
		return nil, err
	}
	if o.Kind != KindPlan {
		return nil, errBadStatus
	}
	if o.Status != StatusHeld {
		return nil, errAdjudicateNotHeld
	}
	if o.FundOrderNo == "" {
		return nil, errAdjudicateNoOrder
	}
	var fo qymodel.FundOrder
	err = gdb.Where("order_no = ?", o.FundOrderNo).Take(&fo).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, errAdjudicateNoOrder
	case err != nil:
		db.MarkFailure(err)
		return nil, wrapInternal("读取资金单", err)
	case fo.Status == qymodel.StatusSuccess:
		return nil, errAdjudicateOrderSettled
	case fo.Status != qymodel.StatusFailed:
		return nil, errFundOrderOpen
	}

	if verdict == VerdictApplied {
		settled, err := twophase.AdjudicateFailedAsApplied(ctx, &fo, "人工核对确认主库已发放订阅: "+reason)
		if err != nil {
			return nil, wrapInternal("按已发放落账", err)
		}
		if !settled {
			return nil, errStatusConflict
		}
		after, err := loadOrderByNo(gdb, orderNo)
		if err != nil {
			return nil, err
		}
		// 回读确认业务侧真的落定了:资金单已 success,订单推 done 靠的是注册进 twophase
		// 的 Resolver。回调没注册或自己失败时 resolveApplied 不报错,对管理员宣布
		// "落账完成"会让他关掉页面,而订单仍挂着。宁可回"尚未落定,请稍后复核"。
		if after.Status != StatusDone {
			common.SysError("qianye/mall: 套餐订单 " + orderNo +
				" 的资金单已改判为成功,但订单未落定(Resolver 未注册或执行失败),当前状态: " + after.Status)
			return nil, errNotSettled
		}
		return after, nil
	}

	// 确实没发放:退星屑。资金单保持 failed。
	err = gdb.Transaction(func(tx *gorm.DB) error {
		lo, err := lockOrderByNo(tx, orderNo)
		if err != nil {
			return err
		}
		if lo.Status != StatusHeld {
			return errStatusConflict
		}
		note := "人工核对确认订阅未发放," + stardust.UnitName() + "已退回: " + reason
		if err := refundAndTransition(tx, lo, note, transition{
			From: []string{StatusHeld}, To: StatusFailed, Action: ActionAdjudicate,
			Note: note, ActorId: adminId,
			Updates: map[string]any{"fail_reason": audit.Truncate(note, 255)},
		}); err != nil {
			return err
		}
		*o = *lo
		return nil
	})
	if err != nil {
		return nil, bizOrInternal("按未发放退款", err)
	}
	return o, nil
}

// bizOrInternal 把事务里冒出来的错误分成两类:业务错误原样回,其余包成内部错误并计入熔断。
func bizOrInternal(stage string, err error) error {
	if _, ok := AsBizError(BizOf(err)); ok {
		return BizOf(err)
	}
	db.MarkFailure(err)
	return wrapInternal(stage, err)
}
