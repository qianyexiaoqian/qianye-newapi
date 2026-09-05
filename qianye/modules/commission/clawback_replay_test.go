package commission

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManualClawbackReplayAfterTheNetIsZeroed 钉住"冲满之后的合法重试"。
//
// 人工冲正把某一对(上线,下线)的净额冲满(或超额冲正被削到恰好归零)之后,
// remaining 就是 0。幂等回读若排在这道判断后面,同一个 client_request_id 的
// 合法重试(HTTP 超时后前端原样重发,弹窗不换键)会被提前拦掉,拿到一条
// "没有可冲正的佣金" —— 与事实完全相反:钱其实已经冲掉了。管理员照着这句提示
// 会去改金额再试,而专门为重放写的参数比对与 409 冲突保护在这条路径上根本到不了。
func TestManualClawbackReplayAfterTheNetIsZeroed(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	ctx := context.Background()

	origin := seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = 42
		a.InviteeId = 900
		a.GrossAmount = decimal.NewFromInt(500)
	})

	// 一次把净额冲满。超额请求会被削到恰好等于 remaining。
	first, err := manualClawback(ctx, origin.Id, 800, "op:req-full", "刷单")
	require.NoError(t, err)
	assert.Equal(t, "-500", first.GrossAmount.String())

	net, err := netAccrued(gdb, 900, 42)
	require.NoError(t, err)
	require.True(t, net.IsZero(), "前提不成立:净额没有被冲平")

	// 原样重放:必须命中幂等,返回同一张单,而不是"没有可冲正的佣金"。
	again, err := manualClawback(ctx, origin.Id, 800, "op:req-full", "刷单")
	require.NoError(t, err, "合法重试被一条与事实相反的失败挡住了")
	assert.Equal(t, first.AccrualNo, again.AccrualNo)

	var rows int64
	require.NoError(t, gdb.Model(&Accrual{}).
		Where("source_type = ?", SourceClawback).Count(&rows).Error)
	assert.EqualValues(t, 1, rows, "重放绝不能落第二条负额行")

	// 同一个键换了参数:必须是冲突,不能是"没有可冲正的佣金" —— 后者会让
	// "有人把同一个请求标识改了金额再发一遍"这件事无法被识别。
	_, err = manualClawback(ctx, origin.Id, 7, "op:req-full", "刷单")
	assert.ErrorIs(t, err, ErrClawbackIdemConflict)

	// 全新的键在净额已经归零时仍然该说"没有可冲正的佣金"。
	_, err = manualClawback(ctx, origin.Id, 7, "op:req-new", "刷单")
	assert.ErrorIs(t, err, ErrNothingToClawback)
}
