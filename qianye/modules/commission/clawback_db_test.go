package commission

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件是 B5 的数据库级回归。
//
// 已有的 TestSameClawbackRequestRejectsReplayedParams 只测那个纯比对函数,
// 完全测不到"manualClawback 到底会不会调它"。而 B5 的实际缺陷正是在
// 调用链上:writeAccrual 的 OnConflict{DoNothing} 冲突不报错,回读拿到旧单
// 被当成"本次新建",调用方照着**本次请求的**金额写下一条成功审计。
// 把 manualClawback 里 `if !inserted { sameClawbackRequest(...) }` 整段删掉,
// 那条纯函数测试仍然全绿。这里让 ON CONFLICT 由真实数据库执行一遍。

// TestManualClawbackDetectsIdemReplay 锁定人工冲正的幂等命中判定。
func TestManualClawbackDetectsIdemReplay(t *testing.T) {
	ctx := context.Background()

	t.Run("换了参数复用同一个幂等键必须报冲突", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		origin := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(500)
		})
		other := seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(500)
		})

		created, err := manualClawback(ctx, origin.Id, 500, "req-x", "拒付")
		require.NoError(t, err)
		require.Equal(t, "-500", created.GrossAmount.String())
		require.EqualValues(t, origin.Id, created.RefAccrualId)
		require.EqualValues(t, -500, created.BaseQuota, "请求金额必须冻结成幂等指纹")

		// 管理员在同一个弹窗里改了目标与金额重提,client_request_id 沿用。
		_, err = manualClawback(ctx, other.Id, 9999, "req-x", "拒付")
		require.ErrorIs(t, err, ErrClawbackIdemConflict,
			"返回旧单 = 调用方会照着 9999 写下一条与账本矛盾的成功审计")

		// 资金侧必须一分没动:只有第一次那条负额行。
		var rows []Accrual
		require.NoError(t, gdb.Where("idem_scope = ?", SourceClawback).Find(&rows).Error)
		require.Len(t, rows, 1)
		assert.Equal(t, "-500", rows[0].GrossAmount.String())
	})

	t.Run("同一请求原样重放返回同一张单,不再新建", func(t *testing.T) {
		// 反向约束:合法重试(网络超时后前端重发)必须放行,否则管理员
		// 会被这个 client_request_id 永久卡住,只能换个键再冲一次 —— 那才是资损。
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		origin := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(500)
		})

		first, err := manualClawback(ctx, origin.Id, 300, "req-y", "拒付")
		require.NoError(t, err)
		second, err := manualClawback(ctx, origin.Id, 300, "req-y", "拒付")
		require.NoError(t, err)
		assert.Equal(t, first.Id, second.Id)
		assert.Equal(t, first.AccrualNo, second.AccrualNo)

		var n int64
		require.NoError(t, gdb.Model(&Accrual{}).
			Where("idem_scope = ?", SourceClawback).Count(&n).Error)
		assert.EqualValues(t, 1, n, "重放不得再落一条负额行")
	})

	t.Run("审计金额取账本真值而不是请求参数", func(t *testing.T) {
		// 管理员填 9999,而这个下线名下净佣金只有 500,落库的是 -500。
		// 审计写 9999 就是一条与账本矛盾的"成功"记录。
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		origin := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(500)
		})

		created, err := manualClawback(ctx, origin.Id, 9999, "req-z", "刷单")
		require.NoError(t, err)
		assert.Equal(t, "-500", created.GrossAmount.String(), "冲正被 remaining 削到净佣金")
		assert.EqualValues(t, 500, clawbackAuditAmount(created))
		assert.EqualValues(t, -9999, created.BaseQuota,
			"指纹记的是请求说了什么,不能拿被削过的 Gross 反推")
	})
}
