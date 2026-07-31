package commission

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSameClawbackRequestRejectsReplayedParams 锁定人工冲正的幂等指纹。
//
// 缺陷复现:
//  1. 管理员对 accrual_id=100 冲正 500,client_request_id="x" → 落 CA-1(-500)。
//  2. 同一个弹窗改成 accrual_id=200 / quota=9999 重提(前端在打开弹窗时生成并
//     缓存 client_request_id,重试沿用同一个)。
//  3. writeAccrual 的 OnConflict{DoNothing} 不报错,按同一个键回读拿到 CA-1。
//  4. 调用方当作"本次新建",写下 TraceNo=CA-1 / AmountQuota=9999 / Result=ok
//     的审计 —— 资金侧只发生了 500,而审计表是事后仲裁的唯一凭据。
//
// 指纹只比"请求本身说了什么"(冲哪一行、冲多少),不比落库后的 GrossAmount:
// 后者被 remaining 削过,同一个请求在不同时刻会得出不同的值。
func TestSameClawbackRequestRejectsReplayedParams(t *testing.T) {
	cases := []struct {
		name         string
		existing     Accrual
		accrualId    int64
		quota        int64
		wantConflict bool
	}{
		{
			name:      "同一请求重放",
			existing:  Accrual{RefAccrualId: 100, BaseQuota: -500},
			accrualId: 100, quota: 500,
			wantConflict: false,
		},
		{
			name:      "换了冲正目标",
			existing:  Accrual{RefAccrualId: 100, BaseQuota: -500},
			accrualId: 200, quota: 500,
			wantConflict: true,
		},
		{
			name:      "换了金额",
			existing:  Accrual{RefAccrualId: 100, BaseQuota: -500},
			accrualId: 100, quota: 9999,
			wantConflict: true,
		},
		{
			name:      "目标与金额都换了",
			existing:  Accrual{RefAccrualId: 100, BaseQuota: -500},
			accrualId: 200, quota: 9999,
			wantConflict: true,
		},
		{
			// 升级前落的老单没有金额指纹。判成冲突等于让管理员永远重试不了
			// 这个 client_request_id,只能换一个键再冲一次 —— 那才是真正的资损。
			name:      "历史单无金额指纹时放行",
			existing:  Accrual{RefAccrualId: 100, BaseQuota: 0},
			accrualId: 100, quota: 9999,
			wantConflict: false,
		},
		{
			// RefAccrualId 从一开始就有,所以老单仍受这一维约束。
			name:      "历史单仍校验冲正目标",
			existing:  Accrual{RefAccrualId: 100, BaseQuota: 0},
			accrualId: 200, quota: 500,
			wantConflict: true,
		},
		{
			// 被 remaining 削过的旧单:请求说 500、实际只冲了 300。
			// 拿 GrossAmount 比对会把这次合法重试误判成冲突。
			name: "金额被 remaining 削过时仍认同一请求",
			existing: Accrual{RefAccrualId: 100, BaseQuota: -500,
				GrossAmount: decimal.NewFromInt(-300)},
			accrualId: 100, quota: 500,
			wantConflict: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sameClawbackRequest(&tc.existing, tc.accrualId, tc.quota)
			if tc.wantConflict {
				require.ErrorIs(t, err, ErrClawbackIdemConflict)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestClawbackAuditAmountFollowsLedger 确认审计金额取账本真值而不是请求参数。
//
// 管理员填 9999、remaining 只剩 300 时,资金侧发生的是 300。审计写 9999
// 就是一条与账本矛盾的"成功"记录,而这类记录正是运营走人工补单、
// 同一笔退两次的起点。
func TestClawbackAuditAmountFollowsLedger(t *testing.T) {
	cases := []struct {
		name  string
		gross string
		want  int64
	}{
		{"未被削减", "-500", 500},
		{"被 remaining 削减", "-300", 300},
		{"带小数的账本金额向下取整", "-300.9", 300},
		{"零金额", "0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			created := Accrual{GrossAmount: decimal.RequireFromString(tc.gross)}
			assert.Equal(t, tc.want, clawbackAuditAmount(&created))
		})
	}
}
