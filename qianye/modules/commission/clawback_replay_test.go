package commission

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
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

// TestAdminInvalidateCacheClearsEveryCache 钉住"失效缓存"这个按钮不许漏掉任何一把。
//
// 这是一条源码级契约而不是行为断言,因为漏掉一把的症状恰恰是**没有症状**:
// 运营改完配置、按了按钮、看到成功提示,而新值要等最长 settingsCacheSeconds
// 才真正生效,这期间发出去的佣金按旧值冻结进账本,按本模块"逐笔冻结、不追溯"
// 的语义永不重算。分组费率(D4)已经这样漏过一次,法币折算比例作为新增的第五把
// 又漏了一次 —— 同一形状两次,必须由守卫兜住而不是靠记性。
func TestAdminInvalidateCacheClearsEveryCache(t *testing.T) {
	// 本包的测试脚手架 resetCommissionCaches 是这份名单的另一个抄本;
	// 两处必须一致,新增一把缓存时两边都得改。
	want := []string{
		"invalidateInviter", "invalidateSettings", "invalidateBlocked",
		"invalidateGroupRates", "invalidateFiatRates",
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "api_admin.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Recv == nil && d.Name.Name == "adminInvalidateCache" {
			fn = d
		}
		return fn == nil
	})
	require.NotNil(t, fn, "adminInvalidateCache 改名了就把这张表一起改")

	called := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			called[id.Name] = true
		}
		return true
	})
	for _, name := range want {
		assert.Truef(t, called[name],
			"adminInvalidateCache 没有调用 %s —— 运营按了按钮看到成功提示,"+
				"而这一把缓存里的旧值还要继续算钱最长一分钟", name)
	}
}
