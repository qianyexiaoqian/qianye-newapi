package paypass

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zz_audit_paypass_timing_test.go —— 资金系统审计:恒定成本比对覆盖**每一条**分支。
//
// 既有 TestVerifyAlwaysPaysSlowHashCost 只钉了"未设置"与"已锁定"两条路径各跑一次
// 慢哈希。而 verify 的响应时间要想不泄露账号状态,是"未设置 / 已锁定 / 空密码 /
// 密码错误 / 密码正确"**五条**路径都必须恰好付一次同 cost 的 bcrypt。留着"密码
// 错误 / 密码正确"没被钉住,等于给"把 compareHash 挪到某个早返回之后"留了一半
// 后门:改动只让被覆盖的两条变红,另外三条照样绿。
//
// 用 slowCompares 计数器而不是挂钟:CI 上时间抖动比信号大,只有"每条路径都真的
// 调了一次 compareHash"是确定性可断言的。这些用例必须串行跑(slowCompares 是
// 进程级全局量),包内测试默认不并行,前置条件成立。
func TestAuditEveryVerifyBranchPaysExactlyOneSlowHash(t *testing.T) {
	gdb := newTestDB(t)

	// 每条路径一个独立账号,避免状态耦合(比如上一条把计数推高影响下一条)。
	const (
		userNotSet  = 7960 // 从未设置
		userWrong   = 7961 // 已设置,给错密码
		userEmpty   = 7962 // 已设置,给空密码
		userLocked  = 7963 // 已设置且锁定
		userCorrect = 7964 // 已设置,给正确密码
	)
	setPassword(t, gdb, userWrong, goodPassword)
	setPassword(t, gdb, userEmpty, goodPassword)
	setPassword(t, gdb, userCorrect, goodPassword)
	setPassword(t, gdb, userLocked, goodPassword)
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", userLocked).
		Update("locked_until", common.GetTimestamp()+3600).Error)

	cases := []struct {
		name     string
		userId   int
		password string
		want     *bizError // nil = 期望成功
	}{
		{"未设置", userNotSet, goodPassword, errPayPwdNotSet},
		{"密码错误", userWrong, "definitely-wrong", errPayPwdWrong},
		{"空密码", userEmpty, "", errPayPwdRequired},
		{"已锁定", userLocked, goodPassword, errPayPwdLocked},
		{"密码正确", userCorrect, goodPassword, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := slowCompares.Load()
			err := verify(context.Background(), tc.userId, tc.password)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
			assert.Equal(t, before+1, slowCompares.Load(),
				"%q 这条路径没有恰好跑一次慢哈希 —— 响应时间成了账号状态探针", tc.name)
		})
	}
}
