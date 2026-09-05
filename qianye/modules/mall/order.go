package mall

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// order.go —— 订单状态机、时间线与退款(withdraw 的 applyTransition 形状)。
//
// 三种 kind 的状态取值(契约 §4):
//
//	code      done → revoked
//	physical  paid → shipped → done / paid → cancelled / paid|shipped → failed
//	plan      paid → done / paid → failed / paid → held / held → done / held → failed

const (
	StatusPaid      = "paid"
	StatusShipped   = "shipped"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
	StatusHeld      = "held"
	StatusRevoked   = "revoked"
)

// 事件动作。稳定的英文标识,前端按 key 做 i18n。
const (
	ActionPay        = "pay"
	ActionIssueCode  = "issue_code"
	ActionShip       = "ship"
	ActionDone       = "done"
	ActionCancel     = "cancel"
	ActionFail       = "fail"
	ActionHold       = "hold"
	ActionRevokeCode = "revoke_code"
	ActionAdjudicate = "adjudicate"
)

// idemScope 是星屑流水与资金单共用的幂等作用域。
const idemScope = "mall"

// allowedTransitions 是状态机的唯一真相。缺席即非法。
//
// 终态(cancelled / failed / revoked)没有出边;done 只对 code 有一条出边(revoked),
// 其余 kind 的 done 也不再变化 —— 由调用点按 kind 再判一次。
var allowedTransitions = map[string]map[string]bool{
	StatusPaid: {
		StatusShipped:   true,
		StatusDone:      true,
		StatusCancelled: true,
		StatusFailed:    true,
		StatusHeld:      true,
	},
	StatusShipped: {
		StatusDone:   true,
		StatusFailed: true,
	},
	StatusHeld: {
		StatusDone:   true,
		StatusFailed: true,
	},
	StatusDone: {
		StatusRevoked: true,
	},
}

// openStatuses 是"未完结"的口径:删除商品的闸门与地址保留期都看它。
var openStatuses = []string{StatusPaid, StatusShipped, StatusHeld}

// countedStatuses 是每人限购要数的口径:失败与取消的单不占名额。
func countedStatusesSQL(q *gorm.DB) *gorm.DB {
	return q.Where("status NOT IN ?", []string{StatusFailed, StatusCancelled})
}

// transition 描述一次状态跃迁及其副作用。
type transition struct {
	// From 是允许出发的状态集合;To 是目标。每一对都必须在 allowedTransitions 里。
	From    []string
	To      string
	Action  string
	Note    string
	ActorId int
	// Updates 是随状态一起写入的业务列(单号、事由等),与状态同一条 UPDATE 落库。
	Updates map[string]any
}

// applyTransition 用带状态条件的 UPDATE 完成跃迁,并在同一事务内写事件。
//
// 非法跃迁与并发冲突一律靠 `WHERE id=? AND status IN ?` 的 RowsAffected 判定,
// 绝不"先读后写"。返回 errStatusConflict 表示单据已被别人处理。
func applyTransition(tx *gorm.DB, o *Order, t transition) error {
	if len(t.From) == 0 {
		return errors.New("qianye/mall: 状态跃迁缺少来源状态")
	}
	for _, from := range t.From {
		if !allowedTransitions[from][t.To] {
			return errBadStatus
		}
	}
	now := common.GetTimestamp()
	updates := map[string]any{"status": t.To, "updated_at": now}
	for k, v := range t.Updates {
		updates[k] = v
	}
	res := tx.Model(&Order{}).
		Where("id = ? AND status IN ?", o.Id, t.From).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errStatusConflict
	}
	o.Status = t.To
	o.UpdatedAt = now
	return writeEvent(tx, o.Id, t.Action, t.Note, t.ActorId)
}

// writeEvent 追加一条时间线。事件只增不改,因此天然免并发。
func writeEvent(tx *gorm.DB, orderId int64, action, note string, actorId int) error {
	return tx.Create(&OrderEvent{
		OrderId:   orderId,
		Action:    action,
		Note:      audit.Truncate(note, 255),
		ActorId:   actorId,
		CreatedAt: common.GetTimestamp(),
	}).Error
}

// refundOrderTx 把一张订单的星屑原路退回,返回退款流水号。
//
// 幂等键 mallrf:<order_no> 保证**同一订单至多退一次**:并发的两条退款路径
// (用户取消 vs 管理员标记失败、对账任务 vs 人工裁决)第二次只会读回已有流水,
// 余额不再动。状态机的 CAS 是第二道闸 —— 两道都在,才敢说"至多一次"。
// 调用方必须在同一事务里紧接着做状态跃迁,任一失败整体回滚。
func refundOrderTx(tx *gorm.DB, o *Order, reason string) (string, error) {
	res, err := stardust.Credit(tx, stardust.Posting{
		UserId:    o.UserId,
		Kind:      stardust.KindMallRefund,
		Amount:    o.Price,
		IdemScope: idemScope,
		IdemKey:   "mallrf:" + o.OrderNo,
		RefType:   "mall_order",
		RefNo:     o.OrderNo,
		Remark:    reason,
	})
	if err != nil {
		return "", err
	}
	if err := tx.Model(&Order{}).Where("id = ?", o.Id).
		Update("refund_ledger_no", res.LedgerNo).Error; err != nil {
		return "", err
	}
	o.RefundLedgerNo = res.LedgerNo
	return res.LedgerNo, nil
}

// refundAndTransition 是"退星屑 + 跃迁"这对动作的唯一组合点:
// 取消、标记失败、撤回兑换码、对账退款、人工裁决五条路径都走它。
//
// 调用方必须已经持有订单行锁(lockOrderByNo)。三步的锁序是 **订单行 → 商品行 →
// 余额行**:下单那一侧是 商品行 → 余额行 → 插入订单(新行,不与任何人争锁),
// 两侧对已有的两把锁取的顺序一致,不会互相等成环。
//
// 退掉的实物 / 套餐把 sold 减回去:限购计数本来就不数 failed / cancelled 的单,
// 库存若不跟着放回,一件被取消十次的限量商品会被十张死单占满。兑换码不放回 ——
// 那枚码已经发出去过(revoked 只是不再展示),库存以 unused 行数计,与 sold 无关。
// 减回去的语句排在 Credit 之前(商品行锁先于余额行锁);重放的退款(Credit 命中已有
// 流水)随后一定在状态 CAS 上落空,整个事务连同这一次减回一起回滚,不会减两次。
func refundAndTransition(tx *gorm.DB, o *Order, reason string, t transition) error {
	if o.Kind != KindCode {
		if err := tx.Model(&Product{}).Where("id = ? AND sold > 0", o.ProductId).
			Updates(map[string]any{"sold": gorm.Expr("sold - 1"), "updated_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
	}
	// 奖品单(source=lottery)价格恒 0、没有扣款流水,"退回去"这一步不存在:
	// 取消 / 标记失败 / 撤回码对它只剩状态跃迁与库存放回。stardust.Credit 本来就
	// 拒收 0 金额,这里按身份跳过是为了让它走同一条状态机而不是报参数错误。
	if o.Source != SourceLottery {
		if _, err := refundOrderTx(tx, o, reason); err != nil {
			return err
		}
	}
	return applyTransition(tx, o, t)
}

// statusIn 回答 s 是否在集合 set 里。
func statusIn(s string, set []string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// finalizeOrderTx 把 kind=plan 的订单从 paid|held 推到 done,回填订阅 id 与续期标记。
//
// 它是 LocalCommit / Resolver / 对账三条路径共用的幂等收尾:CAS 落空(已经 done)
// 返回 (false, nil),调用方按"别人已经做过"处理。
func finalizeOrderTx(tx *gorm.DB, o *Order, subId int, renewed bool) (bool, error) {
	now := common.GetTimestamp()
	res := tx.Model(&Order{}).
		Where("id = ? AND status IN ?", o.Id, []string{StatusPaid, StatusHeld}).
		Updates(map[string]any{
			"status":               StatusDone,
			"user_subscription_id": subId,
			"sub_renewed":          renewed,
			"fulfilled_at":         now,
			"updated_at":           now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	o.Status, o.UserSubscriptionId, o.SubRenewed = StatusDone, subId, renewed
	o.FulfilledAt, o.UpdatedAt = now, now
	note := "订阅已发放,user_subscription_id=" + strconv.Itoa(subId)
	if renewed {
		note = "订阅已续期,user_subscription_id=" + strconv.Itoa(subId)
	}
	return true, writeEvent(tx, o.Id, ActionDone, note, 0)
}

// ─────────────────────────── 读取 ───────────────────────────

// loadOrderByNo 按单号取一张订单。gdb 必须是已经绑好 ctx 的句柄。
func loadOrderByNo(gdb *gorm.DB, orderNo string) (*Order, error) {
	var o Order
	err := gdb.Where("order_no = ?", orderNo).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errOrderNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取订单", err)
	}
	return &o, nil
}

// loadUserOrder 按用户维度取单。user_id 必须进 WHERE 条件而不是取回来再比 ——
// 后者只要有人忘写一次判断就是越权。
func loadUserOrder(gdb *gorm.DB, userId int, orderNo string) (*Order, error) {
	var o Order
	err := gdb.Where("order_no = ? AND user_id = ?", orderNo, userId).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errOrderNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取订单", err)
	}
	return &o, nil
}

// lockOrderByNo 在事务里带行锁取单(状态跃迁前的读)。
func lockOrderByNo(tx *gorm.DB, orderNo string) (*Order, error) {
	var o Order
	err := db.LockForUpdate(tx).Where("order_no = ?", orderNo).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// loadEvents 读一张订单的时间线。上限兜底:事件是 append-only,不会太多,
// 但不能让详情接口把一张被反复操作过的单的全部事件拉进内存。
func loadEvents(gdb *gorm.DB, orderId int64) ([]gin.H, error) {
	rows := make([]OrderEvent, 0, 8)
	if err := gdb.Where("order_id = ?", orderId).Order("id asc").Limit(200).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取订单时间线", err)
	}
	out := make([]gin.H, 0, len(rows))
	for _, e := range rows {
		out = append(out, gin.H{"at": e.CreatedAt, "action": e.Action, "note": e.Note})
	}
	return out, nil
}

// ─────────────────────────── 视图 ───────────────────────────

// userOrderView 是用户端订单行(契约 §4):不含码、不含地址明文。
func userOrderView(o *Order) gin.H {
	return gin.H{
		"order_no":             o.OrderNo,
		"kind":                 o.Kind,
		"title":                o.Title,
		"price":                o.Price,
		"status":               o.Status,
		"tracking_no":          o.TrackingNo,
		"ship_note":            o.ShipNote,
		"fail_reason":          o.FailReason,
		"user_subscription_id": o.UserSubscriptionId,
		"sub_renewed":          o.SubRenewed,
		// source / ref_no:前端按 source 把抽奖所得的单标出来;ref_no 是出款号,
		// 点过去能对上「我的参与」里那一次中奖。address_missing 只对实物单有意义:
		// 奖品单要中奖者事后补填,列表上得先看得见"还没填"。
		"source":          o.Source,
		"ref_no":          o.RefNo,
		"address_missing": addressMissing(o),
		"created_at":      o.CreatedAt,
		"fulfilled_at":    o.FulfilledAt,
		"updated_at":      o.UpdatedAt,
	}
}

// adminOrderView 是管理端订单行(契约 §5):用户端 + user_id / username / fund_order_no。
func adminOrderView(o *Order, username string) gin.H {
	v := userOrderView(o)
	v["user_id"] = o.UserId
	v["username"] = username
	v["fund_order_no"] = o.FundOrderNo
	v["product_no"] = o.ProductNo
	return v
}

// orderReceipt 是下单接口的响应(契约 §4)。
func orderReceipt(o *Order, replayed bool) gin.H {
	return gin.H{
		"order_no":       o.OrderNo,
		"kind":           o.Kind,
		"status":         o.Status,
		"price":          o.Price,
		"replayed":       replayed,
		"code_available": o.Kind == KindCode && o.Status == StatusDone,
	}
}
