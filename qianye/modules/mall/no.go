package mall

import (
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// no.go —— 订单号与商品号。
//
// 两者都用 crypto/rand 派生(common.GetUUID 走 google/uuid v4),禁止 math/rand:
// 订单号可预测意味着可以被枚举 —— 而订单详情、码揭示、地址揭示都按单号寻址。
// 单号里刻意不编码用户 id。

var orderSeq atomic.Uint64

// newOrderNo 生成订单号,形如 ML20260904T091533-3f2-9c1e4b7d20,恰好 32 字符
// (2 + 15 + 1 + 3 + 1 + 10),与 qy_ml_order.order_no 的 varchar(32) 对齐。
// 形状照 stardust.NewLedgerNo:UTC 时间给人看、进程内序列抗同一秒内的高频碰撞、
// 随机段保证不可预测;碰撞由 uk_qy_mlo_no 兜底。
func newOrderNo() string {
	ts := time.Now().UTC().Format("20060102T150405")
	sq := strconv.FormatUint(orderSeq.Add(1)%46656, 36)
	rnd := common.GetUUID()
	if len(rnd) > 10 {
		rnd = rnd[:10]
	}
	return "ML" + ts + "-" + sq + "-" + rnd
}

// newProductNo 生成商品号,形如 MLP + 20 位十六进制(23 字符)。
// 商品号出现在 URL 与订单快照里,不需要时间信息,只需要不可枚举。
func newProductNo() string {
	rnd := common.GetUUID()
	if len(rnd) > 20 {
		rnd = rnd[:20]
	}
	return "MLP" + rnd
}
