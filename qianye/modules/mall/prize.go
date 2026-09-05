package mall

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/planentitlement"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"gorm.io/gorm"
)

// prize.go —— 抽奖中奖生成的商城订单(source=lottery)。
//
// 项目方原话(2026-09-04):「转盘不要局限于星屑,增加一些套餐、兑换码、实物的东西
// 进去,套餐立即生效(这个套餐抽中的激活不要触发星屑返还)」。
//
// # 与自购订单只差一件事:没有钱
//
// 奖品单价格恒 0、不扣星屑、没有资金单;三种 kind 的履行方式与自购完全相同
// (码走 issueCodeTx、实物走管理端发货、套餐走 applyPlanOnMain 发订阅),所以它们
// 落在同一张 qy_ml_order 上,由 source 列区分。退款类动作(fail / revoke-code /
// cancel)对它们仍然可用,只是没有"退星屑"那一步(refundAndTransition 按 source
// 跳过 Credit):一张 0 元单没有"退回去"这回事,状态机与库存放回照旧。
//
// # 入口在事务内:GrantPrizeTx
//
// lottery 的派奖与商城在**同一个扩展库**,所以订单与出款行在同一个事务里落库:
// 转盘的 spinTx、批次派奖 worker 的 driveProductPayout 都把自己的 tx 传进来。
// 任何一步失败整笔回滚 —— 出款行不落、订单不落,重试是干净的重来。
//
// # 幂等键 lotprize:<payout_no>
//
// 出款号由 lottery 用 crypto/rand 生成、全局唯一;同一笔中奖重放(worker 重入、
// 转动请求超时重试)命中原单,不重复建单。
//
// # 套餐奖的主库那一步不在事务里
//
// 发订阅要写主库,而这里握着的是扩展库事务。GrantPrizeTx 只把套餐奖品单落成 paid,
// 调用方在自己的事务提交之后调 FulfillPrizePlan 立刻发订阅("立即生效");它没做到
// 的由 mall.reconcile 兜住(runReconcile 里的 fulfillPendingPrizePlans)。
//
// **刻意不走 twophase.Execute**:那条链的第一步 validateAmount 要求金额 > 0,而奖品单
// 的金额就是 0 —— 往资金单上写一个"名义价"会让 compensate 的积压告警把没扣过的星屑
// 当成积压合计。奖品套餐唯一的收敛锚点是主库 subscription_orders.trade_no 的唯一索引
// (trade_no = SUBSD + order_no):两路(请求线程与对账任务)同时发订阅时,后到的那一路
// 在 INSERT 上撞键回滚,然后走"按 trade_no 回读"那一支收尾 —— 一份订阅最多发一次。
//
// # 不丢中奖
//
// 兑换码库存不足时订单停在 paid 并记 await_code 事件,调用方(lottery)据此挂旗告警;
// 运营上传码之后由上传接口顺手把这些单补齐(fillAwaitingCodeOrders)。套餐被主库
// 业务拒绝(套餐已停用 / 已达购买上限)时订单落 failed 并写明原因 —— 那不是重试
// 能解决的,但必须被人看见(SysError + 事件行),而不是永远停在 paid。

// prizeIdemPrefix 是奖品单幂等键的前缀。
const prizeIdemPrefix = "lotprize:"

// 奖品单专用的事件动作。
const (
	// ActionGrant:抽奖中奖生成了这张单。
	ActionGrant = "grant"
	// ActionAwaitCode:兑换码库存不足,订单停在 paid 等运营补码。
	ActionAwaitCode = "await_code"
	// ActionAddress:中奖者补填了收货地址。
	ActionAddress = "address"
)

// GrantPrizeInput 是 lottery 交给商城的一次中奖。
type GrantPrizeInput struct {
	UserId    int
	ProductNo string
	// RefType / RefNo 是这张单指回的中奖凭据:lottery 传 "lot_payout" 与出款号。
	RefType string
	RefNo   string
	// ActNo 只进事件与审计文案。
	ActNo string
}

// GrantResult 是 GrantPrizeTx 的结论。
type GrantResult struct {
	Order *Order
	// Replayed 表示命中了原单(同一笔中奖的重放),本次没有建单。
	Replayed bool
	// AwaitingCode 表示兑换码库存不足,订单停在 paid;调用方应当告警。
	AwaitingCode bool
	// NeedsPlanFulfill 表示这是一张套餐奖品单,调用方在自己的事务提交后应当调
	// FulfillPrizePlan(Replayed 时若原单仍是 paid 也为 true:上一次的主库那一步没做完)。
	NeedsPlanFulfill bool
}

// GrantPrizeTx 在调用方的扩展库事务里为一次中奖生成一张 0 元订单。**必须在事务内调用。**
//
// 商品不看 enabled、不看发售窗、不数限购:奖档在发布时已经进了承诺,商品事后下架
// 不能让一个已经中奖的人拿不到东西。实物 / 套餐照常 sold+1(商城那一侧的"还剩几件"
// 要跟着少一件),但**不设库存闸门** —— 发布期 lottery 已经校验过 stock ≥ Σcount,
// 之后商城卖超了也是运营的账,不能落在中奖者头上。
func GrantPrizeTx(tx *gorm.DB, in GrantPrizeInput) (*GrantResult, error) {
	if in.UserId <= 0 || strings.TrimSpace(in.ProductNo) == "" || strings.TrimSpace(in.RefNo) == "" {
		return nil, errors.New("qianye/mall: GrantPrizeTx 缺少 user_id / product_no / ref_no")
	}
	idemKey := prizeIdemPrefix + in.RefNo
	var existing Order
	err := tx.Where("idem_key = ?", idemKey).Take(&existing).Error
	if err == nil {
		return &GrantResult{
			Order: &existing, Replayed: true,
			AwaitingCode:     existing.Kind == KindCode && existing.Status == StatusPaid,
			NeedsPlanFulfill: existing.Kind == KindPlan && existing.Status == StatusPaid,
		}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var p Product
	if err := db.LockForUpdate(tx).Where("product_no = ?", in.ProductNo).Take(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errProductNotFound
		}
		return nil, err
	}
	now := common.GetTimestamp()
	if p.Kind != KindCode {
		if err := tx.Model(&Product{}).Where("id = ?", p.Id).
			Updates(map[string]any{"sold": gorm.Expr("sold + 1"), "updated_at": now}).Error; err != nil {
			return nil, err
		}
	}
	o := &Order{
		OrderNo:   newOrderNo(),
		UserId:    in.UserId,
		ProductId: p.Id,
		ProductNo: p.ProductNo,
		Kind:      p.Kind,
		Title:     p.Title,
		Price:     0,
		Status:    StatusPaid,
		Source:    SourceLottery,
		RefNo:     in.RefNo,
		IdemKey:   idemKey,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if p.Kind == KindPlan {
		o.TradeNo = tradeNoPrefix + o.OrderNo
	}
	if err := tx.Create(o).Error; err != nil {
		return nil, err
	}
	note := audit.Truncate(fmt.Sprintf("抽奖所得(活动 %s,%s %s)", in.ActNo, in.RefType, in.RefNo), 255)
	if err := writeEvent(tx, o.Id, ActionGrant, note, 0); err != nil {
		return nil, err
	}
	out := &GrantResult{Order: o}
	switch p.Kind {
	case KindCode:
		stockId, err := issueCodeTx(tx, p.Id, o.Id)
		if errors.Is(err, errSoldOut) {
			// 不丢中奖:单据留在 paid,等运营补码后由上传接口补齐。
			out.AwaitingCode = true
			return out, writeEvent(tx, o.Id, ActionAwaitCode, "兑换码库存不足,等待运营补码后自动发放", 0)
		}
		if err != nil {
			return nil, err
		}
		if err := markCodeIssuedTx(tx, o, stockId, now); err != nil {
			return nil, err
		}
	case KindPlan:
		out.NeedsPlanFulfill = true
	}
	return out, nil
}

// markCodeIssuedTx 把一张 paid 的兑换码订单推到 done 并挂上发出的那枚码。
// 自购(placeDirectOrder)、奖品单当场发码、补码三条路共用。
func markCodeIssuedTx(tx *gorm.DB, o *Order, stockId int64, now int64) error {
	res := tx.Model(&Order{}).Where("id = ? AND status = ?", o.Id, StatusPaid).
		Updates(map[string]any{
			"status": StatusDone, "code_stock_id": stockId,
			"fulfilled_at": now, "updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errStatusConflict
	}
	o.Status, o.CodeStockId, o.FulfilledAt, o.UpdatedAt = StatusDone, stockId, now, now
	return writeEvent(tx, o.Id, ActionIssueCode, "兑换码已发放", 0)
}

// fillAwaitingCodeOrders 给一件兑换码商品名下停在 paid 的奖品单逐张补码。
//
// 由上传接口在入库之后调用:运营补码的动机就是"有人中了奖还没拿到",让他再点一个
// 按钮是多余的。每张单一个事务、按 id 升序(先中的先拿),码不够时停下,剩下的
// 等下一次上传。返回补齐的张数。
func fillAwaitingCodeOrders(ctx context.Context, gdb *gorm.DB, p *Product) (int, error) {
	if p.Kind != KindCode {
		return 0, nil
	}
	ids := make([]int64, 0, 8)
	if err := gdb.Model(&Order{}).
		Where("product_id = ? AND kind = ? AND status = ? AND source = ? AND code_stock_id = 0",
			p.Id, KindCode, StatusPaid, SourceLottery).
		Order("id asc").Limit(200).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	filled := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return filled, ctx.Err()
		}
		err := gdb.Transaction(func(tx *gorm.DB) error {
			var o Order
			if err := db.LockForUpdate(tx).Where("id = ? AND status = ?", id, StatusPaid).Take(&o).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil // 别的路径已经处理
				}
				return err
			}
			stockId, err := issueCodeTx(tx, p.Id, o.Id)
			if err != nil {
				return err
			}
			if err := markCodeIssuedTx(tx, &o, stockId, common.GetTimestamp()); err != nil {
				return err
			}
			filled++
			return nil
		})
		if errors.Is(err, errSoldOut) {
			break
		}
		if err != nil {
			return filled, err
		}
	}
	return filled, nil
}

// FulfillPrizePlan 给一张套餐奖品单发订阅("立即生效")。可重入:主库已经发过的按
// trade_no 回读收尾;正在被另一路发放的在 trade_no 唯一索引上撞键后同样回读收尾。
//
// 调用方是 lottery(事务提交之后)与 mall.reconcile。任何非业务错误原样返回,
// 订单留在 paid 等下一轮;业务拒绝落 failed 并写明原因。
func FulfillPrizePlan(ctx context.Context, orderNo string) error {
	handle := db.Get()
	if handle == nil {
		return db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)
	o, err := loadOrderByNo(gdb, orderNo)
	if err != nil {
		return err
	}
	return fulfillPrizePlan(ctx, gdb, o)
}

func fulfillPrizePlan(ctx context.Context, gdb *gorm.DB, o *Order) error {
	if o.Kind != KindPlan || o.Source != SourceLottery {
		return errBadStatus
	}
	if o.Status != StatusPaid {
		return nil
	}
	if model.DB == nil {
		return db.ErrNotReady
	}
	// 先看主库有没有已经发过:进程在"主库已提交、扩展库还没回写"之间崩过一次,
	// 这一单的订阅早就在了,再发一次就是两份。
	if grant, found, err := readGrantByTradeNo(ctx, o.TradeNo); err != nil {
		return err
	} else if found {
		return finalizePrizePlan(gdb, o, grant)
	}

	grant := &planGrant{}
	planId := 0
	if err := gdb.Model(&Product{}).Select("plan_id").Where("id = ?", o.ProductId).Scan(&planId).Error; err != nil {
		return err
	}
	if planId <= 0 {
		return failPrizePlan(gdb, o, "商品已不存在或未绑定套餐,无法发放订阅")
	}
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return applyPlanOnMain(tx, o, planId, grant)
	})
	switch {
	case err == nil:
	case db.IsDuplicateKey(err):
		// trade_no 撞键:另一路刚刚发完。回读它的结果收尾。
		g, found, rerr := readGrantByTradeNo(ctx, o.TradeNo)
		if rerr != nil {
			return rerr
		}
		if !found {
			return wrapInternal("发放奖品套餐", err)
		}
		return finalizePrizePlan(gdb, o, g)
	default:
		if be, ok := AsBizError(BizOf(err)); ok {
			// 套餐停用 / 购买上限 / 名额:重试也不会变,落 failed 让人看见。
			common.SysError(fmt.Sprintf("qianye/mall: 奖品套餐订单 %s 被主库拒绝,已标记失败: %s", o.OrderNo, be.Message()))
			return failPrizePlan(gdb, o, be.Message())
		}
		return wrapInternal("发放奖品套餐", err)
	}

	// 提交后收尾:与自购的 AfterCommit 同一份动作。用 WithoutCancel:主库已经提交,
	// 调用方预算若恰好在这里耗尽,缓存不刷会让用户组切换延迟到下一次自然过期。
	sctx, cancel := guard.ColdContext(context.WithoutCancel(ctx))
	defer cancel()
	model.QyRefreshSubscriptionUserGroupCache(o.UserId, "lottery prize plan")
	planentitlement.InvalidateUser(o.UserId)
	model.RecordLog(o.UserId, model.LogTypeTopup, fmt.Sprintf(
		"抽奖所得订阅已生效:商城订单 %s(出款 %s)", o.OrderNo, o.RefNo))
	return finalizePrizePlan(gdb.WithContext(sctx), o, *grant)
}

// finalizePrizePlan 把奖品套餐单推到 done,并把顶替结论写进订单与完结事件。
func finalizePrizePlan(gdb *gorm.DB, o *Order, grant planGrant) error {
	return gdb.Transaction(func(tx *gorm.DB) error {
		lo, err := lockOrderByNo(tx, o.OrderNo)
		if err != nil {
			return err
		}
		if grant.Action != "" {
			if err := tx.Model(&Order{}).Where("id = ?", lo.Id).Updates(map[string]any{
				"expect_action": grant.Action, "expect_superseded": grant.Superseded,
			}).Error; err != nil {
				return err
			}
			lo.ExpectAction, lo.ExpectSuperseded = grant.Action, grant.Superseded
		}
		moved, err := finalizeOrderTx(tx, lo, grant.SubId, grant.Renewed)
		if err != nil {
			return err
		}
		if moved && grant.Action == model.UserGroupPurchaseActionSupersede {
			// 顶替没有问过用户(立即生效),事件行是他事后唯一能读到的解释。
			if err := writeEvent(tx, lo.Id, ActionDone,
				"奖品套餐立即生效,已顶替原有分组订阅: "+grant.Superseded, 0); err != nil {
				return err
			}
		}
		*o = *lo
		return nil
	})
}

// failPrizePlan 把一张奖品套餐单从 paid 推到 failed(0 元,没有退款这一步)。
func failPrizePlan(gdb *gorm.DB, o *Order, reason string) error {
	return gdb.Transaction(func(tx *gorm.DB) error {
		lo, err := lockOrderByNo(tx, o.OrderNo)
		if err != nil {
			return err
		}
		if lo.Status == StatusPaid {
			if err := applyTransition(tx, lo, transition{
				From: []string{StatusPaid}, To: StatusFailed, Action: ActionFail,
				Note:    audit.Truncate("奖品套餐发放失败: "+reason, 255),
				Updates: map[string]any{"fail_reason": audit.Truncate(reason, 255)},
			}); err != nil {
				return err
			}
			lo.FailReason = audit.Truncate(reason, 255)
		}
		*o = *lo
		return nil
	})
}

// fulfillPendingPrizePlans 是 mall.reconcile 对奖品套餐单的兜底:请求线程在事务提交
// 之后没能把订阅发出去(进程崩溃、主库抖动)的,过了宽限期再发一次。
func fulfillPendingPrizePlans(ctx context.Context, gdb *gorm.DB, cutoff int64, batch int) {
	rows := make([]Order, 0, batch)
	if err := gdb.Where("kind = ? AND source = ? AND status = ? AND created_at < ?",
		KindPlan, SourceLottery, StatusPaid, cutoff).
		Order("id asc").Limit(batch).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/mall: 扫描未发放的奖品套餐失败: " + err.Error())
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := fulfillPrizePlan(ctx, gdb, &rows[i]); err != nil {
			common.SysError("qianye/mall: 奖品套餐订单 " + rows[i].OrderNo + " 补发失败: " + err.Error())
		}
	}
}

// ─────────────────────────── 给 lottery 的只读面 ───────────────────────────

// ProductBrief 是 lottery 在发布期校验与详情展示时需要知道的那几件事。
type ProductBrief struct {
	ProductNo string `json:"product_no"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Enabled   bool   `json:"enabled"`
	// Remaining 是此刻还能发出去的件数:code 类 = 未用码数;其余 = stock − sold;
	// 不限库存为 -1(StockUnlimited)。
	Remaining int64 `json:"remaining"`
}

// ProductBriefs 按商品号批量取摘要。不存在的商品不在结果里。
func ProductBriefs(ctx context.Context, productNos []string) (map[string]ProductBrief, error) {
	out := make(map[string]ProductBrief, len(productNos))
	if len(productNos) == 0 {
		return out, nil
	}
	handle := db.Get()
	if handle == nil {
		return nil, db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)
	rows := make([]Product, 0, len(productNos))
	if err := gdb.Where("product_no IN ?", productNos).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取商品摘要", err)
	}
	counts, err := codeCountsByProduct(gdb, productIdsOf(rows))
	if err != nil {
		return nil, err
	}
	for i := range rows {
		p := &rows[i]
		remaining := int64(StockUnlimited)
		switch {
		case p.Kind == KindCode:
			remaining = counts[p.Id].Unused
		case p.Stock != StockUnlimited:
			remaining = int64(p.Stock - p.Sold)
			if remaining < 0 {
				remaining = 0
			}
		}
		out[p.ProductNo] = ProductBrief{
			ProductNo: p.ProductNo, Kind: p.Kind, Title: p.Title, Enabled: p.Enabled, Remaining: remaining,
		}
	}
	return out, nil
}

// ProductReferenced 由 lottery 在 InstallHooks 里赋值:回答"这件商品是不是还挂在某场
// 进行中的活动的奖档上"。删商品那条路据此拒绝 —— 商品行没了,中奖那一刻就建不出单。
// 依赖方向是 lottery → mall(module_import_guard_test),所以只能是 mall 暴露变量、
// lottery 来赋值。
var ProductReferenced = func(ctx context.Context, productNo string) (bool, error) { return false, nil }

// ─────────────────────────── 中奖者补填收货地址 ───────────────────────────

// setPrizeOrderAddress 给一张实物奖品单补填收货地址(只允许一次)。
//
// 自购的实物单在下单时就带地址;奖品单是中奖那一刻生成的,地址只能事后补。
// 只对 source=lottery 开放:自购单改地址是另一件事(发货前改地址要与运营协调),
// 本轮不做。已有地址的单 409:改地址等于改收货人,必须走人工。
func setPrizeOrderAddress(ctx context.Context, userId int, orderNo, address, contact string) (*Order, error) {
	handle := db.Get()
	if handle == nil {
		return nil, db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)
	o, err := loadUserOrder(gdb, userId, orderNo)
	if err != nil {
		return nil, err
	}
	if o.Source != SourceLottery {
		return nil, errNotPrizeOrder
	}
	if o.Kind != KindPhysical || o.Status != StatusPaid {
		return nil, errBadStatus
	}
	if !addressMissing(o) {
		return nil, errAddressExists
	}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		lo, err := lockOrderByNo(tx, orderNo)
		if err != nil {
			return err
		}
		if lo.UserId != userId {
			return errOrderNotFound
		}
		if lo.Status != StatusPaid {
			return errBadStatus
		}
		if !addressMissing(lo) {
			return errAddressExists
		}
		if err := sealAddress(lo, address, contact); err != nil {
			return err
		}
		lo.UpdatedAt = common.GetTimestamp()
		// 只写地址那几列(按列名 Select,密文字段名不出现在封装函数之外);WHERE 上的
		// address_set_at = 0 是"只允许填一次"的 CAS。
		res := tx.Model(&Order{}).Where("id = ? AND address_set_at = 0", lo.Id).
			Select("address_cipher", "address_nonce", "contact_cipher", "contact_nonce",
				"address_key_version", "address_set_at", "updated_at").
			Updates(lo)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errAddressExists
		}
		if err := writeEvent(tx, lo.Id, ActionAddress, "中奖者已填写收货地址", userId); err != nil {
			return err
		}
		*o = *lo
		return nil
	})
	if err != nil {
		return nil, bizOrInternal("补填收货地址", err)
	}
	return o, nil
}

// addressMissing 回答"这张实物单还没有收货地址"(奖品单未补填)。判据是 address_set_at,
// 不碰密文列(secret_guard);被保留期清空的单地址曾经有过,不算"没填"。
func addressMissing(o *Order) bool {
	return o.Kind == KindPhysical && o.AddressSetAt == 0
}
