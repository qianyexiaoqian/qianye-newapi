package transfer

import (
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/shopspring/decimal"
)

const (
	// maxRemarkRunes 限制备注长度,防止流水被当成留言板刷量。
	maxRemarkRunes = 200
	// maxClientRequestIdLen 与 qy_fund_orders.idem_key(varchar(96))对齐:
	// 64 + ':' + 最长 10 位用户 ID = 75,留有余量。超长会被数据库静默截断,
	// 那意味着两个不同请求可能塌缩成同一个幂等键。
	maxClientRequestIdLen = 64
	// maxIdentifierLen 用户名 validate:"max=20"、邮箱 validate:"max=50",
	// 超过这个长度不可能命中任何账号,直接判不存在即可,免去一次查库。
	maxIdentifierLen = 64
)

// acceptedRequest 是通过受理校验后的规范化请求。
// 金额相关字段一律 int64,跨库写主库前再统一收敛到 int32 容量。
type acceptedRequest struct {
	FromUserId int
	ToUserId   int
	// FromGroup 是受理这一刻发起方的用户分组(归一化后)。
	// 它会被记进 qy_transfer_user_state.day_out_group —— 见那一列的注释:
	// 今天的计数是在哪一档下累起来的,今天剩下的时间就继续按那一档取严。
	FromGroup string
	Amount    int64
	Fee       int64
	Total     int64
	Remark    string
	IdemKey   string
}

// validateCreate 做全部不需要访问数据库的受理校验。
//
// 单独拆出来是因为它承载了本模块最容易出资损的一组不变量(自转、金额区间、
// 手续费溢出、幂等键构造),这些必须能脱离数据库直接测试。
func validateCreate(fromUserId int, req createRequest, cfg config.Transfer) (acceptedRequest, error) {
	var out acceptedRequest

	// 服务端二次确认标记。前端弹窗可以被绕过,但这一位必须由调用方显式带上,
	// 保证"不可逆"这件事在协议层留有痕迹。
	if !req.Confirm {
		return out, errConfirmRequired
	}
	if fromUserId <= 0 || req.ToUserId <= 0 {
		return out, errInvalidParam
	}
	// 自己转自己硬编码拒绝,不做配置项:它没有任何合法用途,
	// 却会让"扣款 + 加款"落在同一行上,把余额守恒的校验彻底搅浑。
	if req.ToUserId == fromUserId {
		return out, errSelfTransfer
	}

	idemKey, err := buildIdemKey(fromUserId, req.ClientRequestId)
	if err != nil {
		return out, err
	}

	if req.Amount <= 0 || req.Amount > int64(common.MaxQuota) {
		return out, errAmountOutOfRange
	}
	if cfg.MinQuota > 0 && req.Amount < cfg.MinQuota {
		return out, errAmountOutOfRange
	}
	if cfg.MaxPerTxQuota > 0 && req.Amount > cfg.MaxPerTxQuota {
		return out, errAmountOutOfRange
	}

	fee, err := computeFee(req.Amount, cfg.FeeBps, cfg.FeeMinQuota)
	if err != nil {
		return out, err
	}
	// 用 int64 中间量相加后再与 MaxQuota 比较,绝不在 int 上直接相加:
	// 主库 users.quota 是 int32,溢出会把扣款变成加款。
	total := req.Amount + fee
	if total > int64(common.MaxQuota) {
		return out, errAmountOutOfRange
	}

	out = acceptedRequest{
		FromUserId: fromUserId,
		ToUserId:   req.ToUserId,
		Amount:     req.Amount,
		Fee:        fee,
		Total:      total,
		Remark:     sanitizeRemark(req.Remark),
		IdemKey:    idemKey,
	}
	return out, nil
}

// computeFee 按 bps(万分比整数)计算手续费。
//
// 手续费直接销毁(发起方扣 amount+fee,收款方只加 amount),不引入第三方账户。
// fee_min_quota 只在费率启用时生效 —— 费率为 0 却收固定费属于配置事故,
// 这里按"没开手续费"处理而不是替运维猜测意图。
func computeFee(amount int64, feeBps int, feeMinQuota int64) (int64, error) {
	if feeBps <= 0 {
		return 0, nil
	}
	d := decimal.NewFromInt(amount).
		Mul(decimal.NewFromInt(int64(feeBps))).
		Div(decimal.NewFromInt(10000))
	// 必须用 Checked 变体:QuotaFromDecimal 会静默 clamp,
	// 而划转场景下"少收了手续费"和"金额被截断"都属于必须报错的事故。
	fee, clamp := common.QuotaFromDecimalChecked(d)
	if clamp != nil {
		return 0, errAmountOutOfRange
	}
	f := int64(fee)
	if feeMinQuota > 0 && f < feeMinQuota {
		f = feeMinQuota
	}
	if f < 0 || f > int64(common.MaxQuota) {
		return 0, errAmountOutOfRange
	}
	return f, nil
}

// buildIdemKey 拼接幂等键(裁定 C10:客户端统一传 client_request_id)。
//
// 字符集刻意收紧到 UUID / 短随机串会用到的范围:幂等键要进唯一索引,
// 放开任意字节会让攻击者用多字节内容撑爆 varchar 并制造截断碰撞 ——
// 两个不同请求塌缩成同一个键,意味着第二笔划转被当成重复提交而静默吞掉。
func buildIdemKey(userId int, clientRequestId string) (string, error) {
	id := strings.TrimSpace(clientRequestId)
	if id == "" {
		return "", errIdemKeyRequired
	}
	if len(id) > maxClientRequestIdLen {
		return "", errInvalidParam
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return "", errInvalidParam
		}
	}
	return strconv.Itoa(userId) + ":" + id, nil
}

// sanitizeRemark 过滤控制字符并按 rune 截断。
// 控制字符会污染主库账本日志的 content,是典型的日志注入面。
func sanitizeRemark(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if n >= maxRemarkRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// maskUsername 脱敏用户名,用于确认弹窗回显与对手方展示。
//
// 脱敏必须在后端做:把原文下发给前端再由前端打码等于没脱敏。
// 短名单独处理是因为"首字符 + 尾字符"在长度 2 时等于原样输出。
func maskUsername(s string) string {
	r := []rune(s)
	switch n := len(r); {
	case n == 0:
		return ""
	case n == 1:
		return "*"
	case n == 2:
		return string(r[0]) + "*"
	case n <= 4:
		return string(r[0]) + strings.Repeat("*", n-2) + string(r[n-1])
	default:
		return string(r[:2]) + "***" + string(r[n-2:])
	}
}

// maskEmail 脱敏邮箱。域名保留(它对确认收款人有用且不构成标识),本地部分只留首字符。
func maskEmail(s string) string {
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return ""
	}
	local := []rune(s[:at])
	if len(local) == 1 {
		return "*" + s[at:]
	}
	return string(local[0]) + "***" + s[at:]
}

// dayBucket 把时间戳折成形如 20260730 的自然日编号。
//
// 用服务器本地时区:配置里没有独立的时区字段,而进程内所有节点共享同一个
// TZ 环境变量,比各自按 UTC 偏移换算更不容易出现"两个节点跨日时刻不一致"。
func dayBucket(ts int64) int32 {
	y, m, d := time.Unix(ts, 0).Date()
	return int32(y*10000 + int(m)*100 + d)
}
