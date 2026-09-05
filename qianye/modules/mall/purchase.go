package mall

import (
	"context"
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"gorm.io/gorm"
)

// purchase.go —— kind=code|physical 的下单:一个扩展库事务(design §6.2)。
//
// 顺序不变量:
//
//  1. 幂等重放先于一切(handler 在锁外、验密之前查一次;事务里撞 uk_qy_mlo_idem 再兜一次);
//  2. 商品行锁(条件 UPDATE sold+1)先于余额行锁(stardust.Debit)—— 与 stardust
//     doc.go 的锁序契约一致:活动 / 商品行锁在前,余额行锁在后;
//  3. 每人限购在商品行锁内数:锁外数会让并发的 N 个请求各在各的快照里读到旧计数。

// maxClientRequestID 是 client_request_id 的长度上限,与 lottery 同值。
// "<user_id>:" 前缀之后仍落在 idem_key 的 varchar(96) 之内。
const maxClientRequestID = 64

// maxCodeIssueAttempts 是选码 CAS 落空后的重试次数。商品行锁已把同商品的下单
// 串行化,落空只可能来自撤回 / 删码这类管理端并发,3 次足够。
const maxCodeIssueAttempts = 3

// 地址与联系方式的长度上限(按 rune)。密文列没有长度上界,上界由这里给。
const (
	maxAddressRunes = 500
	maxContactRunes = 128
)

// directOrderInput 是一次 code / physical 下单的全部输入(已通过 handler 校验)。
type directOrderInput struct {
	UserId int
	// ClientRequestId 已经过 NormalizeIdemClientKey 折叠。
	ClientRequestId string
	Product         *Product
	Address         string
	Contact         string
}

// idemKeyOf 拼出落库的幂等键。用户 id 必须是前缀:client_request_id 由前端生成,
// 不带 user 前缀的话,两个用户碰巧用了同一个 UUID 就会互相顶掉对方的订单。
func idemKeyOf(userId int, clientKey string) string {
	return strconv.Itoa(userId) + ":" + clientKey
}

// findOrderByIdemKey 按幂等键取原单。没有原单时返回 (nil, nil)。
func findOrderByIdemKey(gdb *gorm.DB, idemKey string) (*Order, error) {
	var o Order
	err := gdb.Where("idem_key = ?", idemKey).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取原单", err)
	}
	return &o, nil
}

// placeDirectOrder 完成一次 code / physical 下单。第二个返回值为 true 表示幂等重放。
func placeDirectOrder(ctx context.Context, in directOrderInput) (*Order, bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, false, db.ErrNotReady
	}
	// 句柄一次性绑上调用方的预算:逐条 WithContext 漏一条,就等于在这条链路上
	// 开了一个没有上界的口子。
	gdb = gdb.WithContext(ctx)

	p := in.Product
	if p.Kind != KindCode && p.Kind != KindPhysical {
		return nil, false, errBadRequest("该商品不能走直接下单")
	}
	now := common.GetTimestamp()
	o := &Order{
		OrderNo:   newOrderNo(),
		UserId:    in.UserId,
		ProductId: p.Id,
		ProductNo: p.ProductNo,
		Kind:      p.Kind,
		Title:     p.Title,
		Price:     p.Price,
		Status:    StatusPaid,
		Source:    SourceMall,
		IdemKey:   idemKeyOf(in.UserId, in.ClientRequestId),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if p.Kind == KindPhysical {
		if err := sealAddress(o, in.Address, in.Contact); err != nil {
			return nil, false, err
		}
	}

	err := gdb.Transaction(func(tx *gorm.DB) error {
		if err := reserveProductTx(tx, p.Id, in.UserId); err != nil {
			return err
		}
		if err := debitForOrderTx(tx, o); err != nil {
			return err
		}
		// 先落单再发码:订单行是码的归属,而且 uk_qy_mlo_idem 的撞键要在
		// 动库存之前就把事务打断。
		if err := tx.Create(o).Error; err != nil {
			return err
		}
		if err := writeEvent(tx, o.Id, ActionPay, "已支付 "+strconv.FormatInt(o.Price, 10)+" "+stardust.UnitName(), in.UserId); err != nil {
			return err
		}
		if p.Kind != KindCode {
			return nil
		}
		stockId, err := issueCodeTx(tx, p.Id, o.Id)
		if err != nil {
			return err
		}
		return markCodeIssuedTx(tx, o, stockId, now)
	})
	if err == nil {
		return o, false, nil
	}
	if db.IsDuplicateKey(err) {
		// 预读没看到而唯一索引挡下了:两个完全相同的请求同时进来。
		// 返回原单而不是报错 —— 这是双击、多标签、客户端超时重试的正常结果。
		existing, ferr := findOrderByIdemKey(gdb, o.IdemKey)
		if ferr != nil {
			return nil, false, ferr
		}
		if existing == nil {
			return nil, false, wrapInternal("幂等冲突但原单不存在", err)
		}
		if existing.ProductNo != p.ProductNo {
			return nil, false, errIdemConflict
		}
		return existing, true, nil
	}
	if _, ok := AsBizError(BizOf(err)); ok {
		return nil, false, BizOf(err)
	}
	db.MarkFailure(err)
	return nil, false, wrapInternal("下单", err)
}

// reserveProductTx 锁商品行并占一件:条件 UPDATE 把"上架、在窗、有货"三条判据
// 与 sold+1 写在同一条语句里,RowsAffected=0 再回读一次说清是哪一条挡住的。
// 随后在同一把锁内数每人限购。
func reserveProductTx(tx *gorm.DB, productId int64, userId int) error {
	now := common.GetTimestamp()
	res := tx.Model(&Product{}).
		Where("id = ? AND enabled = ? AND (sale_start_at = 0 OR sale_start_at <= ?) AND (sale_end_at = 0 OR sale_end_at > ?) AND (stock = ? OR sold < stock)",
			productId, true, now, now, StockUnlimited).
		Updates(map[string]any{"sold": gorm.Expr("sold + 1"), "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		var p Product
		if err := tx.Where("id = ?", productId).Take(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errProductNotFound
			}
			return err
		}
		if !p.Enabled || !saleWindowOpen(&p, now) {
			return errOffSale
		}
		return errSoldOut
	}
	var p Product
	if err := db.LockForUpdate(tx).Where("id = ?", productId).Take(&p).Error; err != nil {
		return err
	}
	if p.PerUserLimit <= 0 {
		return nil
	}
	var mine int64
	if err := countedStatusesSQL(tx.Model(&Order{}).
		Where("user_id = ? AND product_id = ?", userId, productId)).Count(&mine).Error; err != nil {
		return err
	}
	if mine >= int64(p.PerUserLimit) {
		return errLimit
	}
	return nil
}

// debitForOrderTx 扣掉这张订单的星屑,并把流水号写回订单。
//
// 幂等键 mall:<order_no>:订单号每次现生成,这一笔不可能命中已有流水;
// 它存在的意义是让流水能按单号追回,以及让 stardust 的参数校验有键可查。
func debitForOrderTx(tx *gorm.DB, o *Order) error {
	res, err := stardust.Debit(tx, stardust.Posting{
		UserId:    o.UserId,
		Kind:      stardust.KindMallOrder,
		Amount:    o.Price,
		IdemScope: idemScope,
		IdemKey:   "mall:" + o.OrderNo,
		RefType:   "mall_order",
		RefNo:     o.OrderNo,
		Remark:    audit.Truncate(o.Title, 255),
	})
	if err != nil {
		return err
	}
	o.LedgerNo = res.LedgerNo
	return nil
}

// issueCodeTx 从库存里取一枚 unused 的码发给订单:先选后 CAS,落空重选。
//
// MySQL 5.7 没有 SKIP LOCKED,不用它;商品行锁已把同商品的下单串行化,
// 这里的重试只兜管理端并发(撤回、删码)那一点点窗口。
func issueCodeTx(tx *gorm.DB, productId, orderId int64) (int64, error) {
	now := common.GetTimestamp()
	for attempt := 0; attempt < maxCodeIssueAttempts; attempt++ {
		var row CodeStock
		err := tx.Select("id").Where("product_id = ? AND status = ?", productId, CodeUnused).
			Order("id asc").Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, errSoldOut
		}
		if err != nil {
			return 0, err
		}
		res := tx.Model(&CodeStock{}).
			Where("id = ? AND status = ?", row.Id, CodeUnused).
			Updates(map[string]any{"status": CodeIssued, "order_id": orderId, "issued_at": now})
		if res.Error != nil {
			return 0, res.Error
		}
		if res.RowsAffected == 1 {
			return row.Id, nil
		}
	}
	return 0, errSoldOut
}

// revealCode 解出用户自己订单上的兑换码。归属判定在 WHERE 里(loadUserOrder)。
func revealCode(ctx context.Context, userId int, orderNo string) (string, error) {
	gdb := db.Get()
	if gdb == nil {
		return "", db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)
	o, err := loadUserOrder(gdb, userId, orderNo)
	if err != nil {
		return "", err
	}
	if o.Kind != KindCode || o.CodeStockId == 0 {
		return "", errBadStatus
	}
	if o.Status == StatusRevoked {
		return "", errCodeRevoked
	}
	var row CodeStock
	if err := gdb.Where("id = ? AND order_id = ?", o.CodeStockId, o.Id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errOrderNotFound
		}
		db.MarkFailure(err)
		return "", wrapInternal("读取兑换码", err)
	}
	return openCode(&row, o.ProductNo)
}

// cancelOrder 由用户本人取消一张 paid 态的实物订单,星屑全额退回。
//
// 与管理员发货 / 标记失败是同一把锁:三侧都先锁订单行再做 `WHERE status='paid'` 的
// CAS,先到者胜,后到者拿到"状态刚刚被改变"。
func cancelOrder(ctx context.Context, userId int, orderNo string) (*Order, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)
	o, err := loadUserOrder(gdb, userId, orderNo)
	if err != nil {
		return nil, err
	}
	if o.Kind != KindPhysical || o.Status != StatusPaid {
		return nil, errBadStatus
	}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		lo, err := lockOrderByNo(tx, orderNo)
		if err != nil {
			return err
		}
		// 归属再判一次:锁内读到的才是此刻的事实。
		if lo.UserId != userId {
			return errOrderNotFound
		}
		if err := refundAndTransition(tx, lo, "用户取消订单", transition{
			From: []string{StatusPaid}, To: StatusCancelled, Action: ActionCancel,
			Note: "用户取消," + stardust.UnitName() + "已退回", ActorId: userId,
		}); err != nil {
			return err
		}
		*o = *lo
		return nil
	})
	if err != nil {
		if _, ok := AsBizError(BizOf(err)); ok {
			return nil, BizOf(err)
		}
		db.MarkFailure(err)
		return nil, wrapInternal("取消订单", err)
	}
	return o, nil
}
