package twophase

import (
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 金额跨库校验是防资损的第一道闸:扩展库用 int64,主库 users.quota 是 int32。
// 越界必须显式拒绝,绝不能静默截断成负数或小额。
func TestValidateAmount(t *testing.T) {
	cases := []struct {
		name    string
		amount  int64
		wantErr bool
	}{
		{"零金额", 0, true},
		{"负金额", -1, true},
		{"最小正数", 1, false},
		{"正常金额", 500000, false},
		{"int32 上限", int64(common.MaxQuota), false},
		{"超出 int32 一位", int64(common.MaxQuota) + 1, true},
		{"远超上限", 1 << 40, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAmount(tc.amount)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrAmountOutOfRange),
					"越界金额必须归类为 ErrAmountOutOfRange,调用方据此返回可读错误")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNewOrderNo_Format(t *testing.T) {
	cases := map[string]string{
		qymodel.KindTransfer:         "TR",
		qymodel.KindCommissionSettle: "CM",
		qymodel.KindCommissionRevers: "RV",
		qymodel.KindWithdrawQuota:    "WD",
		qymodel.KindWithdrawFiat:     "WD",
		qymodel.KindViolationFee:     "VF",
	}
	for kind, prefix := range cases {
		t.Run(kind, func(t *testing.T) {
			no := NewOrderNo(kind)
			assert.True(t, strings.HasPrefix(no, prefix), "单号应以类型码开头: %s", no)
			// logs.request_id 与 qy_fund_orders.order_no 都是 varchar(64)。
			assert.LessOrEqual(t, len(no), 64, "单号不得超过 varchar(64): %s", no)
			assert.Equal(t, 2, strings.Count(no, "-"), "单号应为 前缀时间-序列-随机 三段: %s", no)
		})
	}
}

// 单号必须唯一。虽然有唯一索引兜底,但高频碰撞会让写入不断重试。
func TestNewOrderNo_NoCollisionUnderBurst(t *testing.T) {
	const n = 20000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		no := NewOrderNo(qymodel.KindTransfer)
		_, dup := seen[no]
		require.False(t, dup, "同一秒内生成 %d 个单号出现重复: %s", n, no)
		seen[no] = struct{}{}
	}
}

// 单号绝不能编码用户 id:那等于对外泄漏划转双方的账号关系。
func TestNewOrderNo_DoesNotLeakUserIdentity(t *testing.T) {
	no := NewOrderNo(qymodel.KindTransfer)
	// 单号里除了时间戳数字外不应出现可辨识的结构分隔(如 T<id>T<id>)。
	parts := strings.Split(no, "-")
	require.Len(t, parts, 3)
	assert.Regexp(t, `^[A-Z]{2}\d{8}T\d{6}$`, parts[0],
		"首段只应是 类型码 + UTC 时间戳,不得夹带用户信息")
}

func TestStatusName(t *testing.T) {
	cases := map[int8]string{
		qymodel.StatusPending:   "pending",
		qymodel.StatusSuccess:   "success",
		qymodel.StatusFailed:    "failed",
		qymodel.StatusUncertain: "uncertain",
		qymodel.StatusReversed:  "reversed",
		int8(99):                "unknown",
	}
	for status, want := range cases {
		assert.Equal(t, want, qymodel.StatusName(status))
	}
}

// 终态判定决定补偿任务是否还会去碰这笔单,错了会导致已完成的单被反复处理。
func TestIsTerminal(t *testing.T) {
	assert.False(t, qymodel.IsTerminal(qymodel.StatusPending))
	assert.False(t, qymodel.IsTerminal(qymodel.StatusUncertain),
		"人工裁决态不是终态 —— 它仍在等待管理员处理")
	assert.True(t, qymodel.IsTerminal(qymodel.StatusSuccess))
	assert.True(t, qymodel.IsTerminal(qymodel.StatusFailed))
	assert.True(t, qymodel.IsTerminal(qymodel.StatusReversed))
}

// pending 状态的零值语义:插入时若忘记赋值,必须退化成最安全的 pending,
// 而不是意外变成 success。
func TestPendingIsZeroValue(t *testing.T) {
	var order qymodel.FundOrder
	assert.Equal(t, qymodel.StatusPending, order.Status,
		"FundOrder 的零值状态必须是 pending")
}

func TestIsDuplicateKey(t *testing.T) {
	assert.True(t, isDuplicateKey(errors.New("Error 1062 (23000): Duplicate entry 'x' for key 'uk_qy_fund_idem'")))
	assert.True(t, isDuplicateKey(errors.New("duplicate key value violates unique constraint")))
	assert.False(t, isDuplicateKey(errors.New("connection refused")))
	assert.False(t, isDuplicateKey(nil))
}
