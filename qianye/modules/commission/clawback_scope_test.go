package commission

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件守一条不变式:**冲正上限只能由被记账的那个邀请人自己的净计佣决定**。
//
// 负数行落在 origin.InviterId 名下,而上限一度只按 invitee_id 汇总。两条真实
// 路径会踩到:换绑之后历史佣金留在原邀请人名下(api_admin_relation.go 的
// rebindRelation 明确支持并如此设计),以及手工调整行的 invitee_id 恒为 0
// (api_admin_adjust.go)—— 后者让上限退化成"全站所有手工调整之和"。
//
// 断言必须落在**落库的负数金额**上,而不只是有没有报错:超额部分不会让
// available 变负,它进的是 unsettled_amount 的负数结转,佣金侧的
// available+frozen+withdrawn == earned-clawback 恒等式照样成立,
// 只看余额或只看恒等式的测试全都会绿。

func TestManualClawbackCapIsPerInviter(t *testing.T) {
	ctx := context.Background()

	t.Run("换绑后不得拿原邀请人名下的计佣给新邀请人兜底", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		// 同一个下线 900:先给邀请人 1 挣了 100000,换绑后只给邀请人 2 挣了 10。
		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 1
			a.InviteeId = 900
			a.GrossAmount = decimal.NewFromInt(100000)
		})
		small := seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.InviterId = 2
			a.InviteeId = 900
			a.GrossAmount = decimal.NewFromInt(10)
		})

		created, err := manualClawback(ctx, small.Id, 50000, "req-rebind", "拒付")
		require.NoError(t, err)
		assert.Equal(t, "-10", created.GrossAmount.String(),
			"上限必须是邀请人 2 从下线 900 拿到的 10,而不是两个邀请人合计的 100010")
		assert.EqualValues(t, 2, created.InviterId)
	})

	t.Run("手工调整行的 invitee_id 恒为 0,上限不得退化成全站合计", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		// 两笔互不相干的手工调整,都带 invitee_id = 0。
		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.IdemScope, a.SourceType = SourceManual, SourceManual
			a.IdemKey = "manual:big"
			a.InviterId, a.InviteeId = 1, 0
			a.GrossAmount = decimal.NewFromInt(500000)
		})
		small := seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.IdemScope, a.SourceType = SourceManual, SourceManual
			a.IdemKey = "manual:small"
			a.InviterId, a.InviteeId = 2, 0
			a.GrossAmount = decimal.NewFromInt(1000)
		})

		created, err := manualClawback(ctx, small.Id, 300000, "req-manual", "刷单")
		require.NoError(t, err)
		assert.Equal(t, "-1000", created.GrossAmount.String(),
			"用户 2 名下只入账过 1000,冲正额必须被削到 1000")
	})

	t.Run("净额已被冲平时无事可做", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		origin := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId, a.InviteeId = 3, 901
			a.GrossAmount = decimal.NewFromInt(1000)
		})
		seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.IdemScope, a.SourceType = SourceClawback, SourceClawback
			a.IdemKey = "clawback:prev"
			a.InviterId, a.InviteeId = 3, 901
			a.GrossAmount = decimal.NewFromInt(-1000)
		})

		_, err := manualClawback(ctx, origin.Id, 500, "req-zero", "重复冲正")
		assert.ErrorIs(t, err, ErrNothingToClawback)
	})
}
