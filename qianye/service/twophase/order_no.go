package twophase

import (
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
)

var seq atomic.Uint64

// NewOrderNo 生成资金单号,形如 TR20260730T091533-3f2a-9c1e4b7d20(31 字符)。
//
// 随机源必须是密码学安全的:common.GetUUID 走 google/uuid v4(crypto/rand)。
// 禁止用 math/rand 或时间戳自增 —— 资金单号可预测意味着可以被枚举和伪造。
//
// 单号里刻意不编码用户 id:那等于对外泄漏用户关系(尤其是划转的收付双方)。
//
// 碰撞由 uk_qy_fund_no 唯一索引兜底,调用方冲突时重新生成即可。
func NewOrderNo(kind string) string {
	code := qymodel.KindCode(kind)
	ts := time.Now().UTC().Format("20060102T150405")
	// 进程内单调序列,抗同一秒内的高频碰撞。36 进制压缩长度。
	sq := strconv.FormatUint(seq.Add(1)%1679616, 36)
	rnd := common.GetUUID()
	if len(rnd) > 10 {
		rnd = rnd[:10]
	}
	return fmt.Sprintf("%s%s-%s-%s", code, ts, sq, rnd)
}
