package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qy_pricing_hookpoint_test.go —— 用 AST 锁死本包**结算侧**两个计价挂载点的
// 位置与顺序:task_billing.go 与 tiered_settle.go。
//
// # 它为什么在这里,而不是在扩展模块里
//
// 这份断言原来住在 qianye/modules/grouppricing/hookpoint_test.go。那个模块已经
// 下线,但它断的东西**从来就不属于那个模块** —— 它断的是「本包这两个结算函数的
// 插入点还在不在、顺序对不对」,而插入点归 service 自己。
//
// # 这两处为什么单独存在
//
// 计价链路(relay/helper 的三个入口)产出 PriceData,结算侧大多直接读它。唯二的
// 例外就是这两条:
//
//	RecalculateTaskQuotaByTokens  Task(视频/MJ)拿到实际 token 后重算,直接读 ratio_setting
//	TryTieredSettle               阶梯计价从 snap.ExprString **重跑表达式**,不读 PriceData
//
// 少了任一行,预扣与结算就会各按各的口径走,差额以**追扣**形式落到用户头上 ——
// 用户先看到便宜的预扣,再被补一刀。正是 AGENTS.md「预扣与结算必须同口径」直指的情形。
// 回滚验证实测过:把调用换成恒等表达式,全仓 go test 一条都不红,只有 AST 断言能抓到。
//
// # 接缝当前是空置的
//
// grouppricing 下线之后没有任何代码给这两个变量赋值(由
// qianye.TestPricingHooksAreDeliberatelyVacant 断言),它们是恒等函数。详见
// qy_pricing_export.go 的包注释。

const (
	qyTaskBillingGoPath  = "task_billing.go"
	qyTieredSettleGoPath = "tiered_settle.go"
)

// TestQyTaskSettlementHookPointExists 锁住 Task 差额结算的调用点与顺序。
//
// 顺序一并锁住:分组要先从 task.Group 解析(为空时回落 users.group)才有意义,
// 把 hook 提到那段之前会用空分组去查规则,永远查不到,等于这一行不存在。
func TestQyTaskSettlementHookPointExists(t *testing.T) {
	seq := qySvcCallsByFunc(t, qyTaskBillingGoPath)["RecalculateTaskQuotaByTokens"]
	require.NotEmpty(t, seq,
		"service/task_billing.go 里找不到 RecalculateTaskQuotaByTokens —— 上游改了函数名?")

	hookIdx := qySvcIndexOf(seq, "QyGroupTaskRatio")
	require.GreaterOrEqual(t, hookIdx, 0,
		"RecalculateTaskQuotaByTokens 里缺少 QyGroupTaskRatio 挂载点:"+
			"任务类模型的分组级折扣会在差额结算时被追扣回全局价,而管理界面上看不出来")

	// 分组解析现在住在 taskUserGroup 里(提交时刻落库的 BillingContext.UserGroup,
	// 历史行回落 GetUserById)。它有两个调用方 —— 差额结算这一处,以及钱包出资闸门
	// 在结算尾巴上的那一处 —— 两处必须拿到同一个用户分组,所以判据只有一份。
	// 这条断言守的东西一个字没变:分组必须先解析出来,QyGroupTaskRatio 才有意义。
	fallbackIdx := qySvcIndexOf(seq, "taskUserGroup")
	require.GreaterOrEqual(t, fallbackIdx, 0,
		"RecalculateTaskQuotaByTokens 里找不到分组解析(taskUserGroup:"+
			"提交时刻的 BillingContext.UserGroup,为空时回落 users.group)")
	assert.Greater(t, hookIdx, fallbackIdx,
		"QyGroupTaskRatio 必须排在分组解析之后:提前调用会拿空分组去查规则,永远查不到")
}

// TestQyTaskSettlementUsesCrossCellGroupRatio 钉死 Task 差额结算的**交叉格**形状。
//
// 修复前是 GetGroupGroupRatio(group, group):两个实参同一个标识符,只命中分组倍率
// 矩阵的对角线。而预扣走 relay/helper/price.go 的 HandleGroupRatio,用的是
// (UserGroup, UsingGroup) 交叉格。令牌做了分组覆盖且配了交叉倍率时,Task 类模型
// 的预扣与结算不同口径,差额以**追扣**形式落到用户头上。这条断言防的是它被改回去。
//
// 本轮三条计费路径合并成 ratio_setting.ResolveGroupRatio,断言的函数名随之更换,
// 守的东西一个字都没变:第一个实参必须是所有者的 users.group,两者不能同名。
func TestQyTaskSettlementUsesCrossCellGroupRatio(t *testing.T) {
	file := qySvcParseFileOrFail(t, qyTaskBillingGoPath)

	args := make([]string, 0, 2)
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "RecalculateTaskQuotaByTokens" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ResolveGroupRatio" {
				return true
			}
			found = true
			for _, arg := range call.Args {
				if id, ok := arg.(*ast.Ident); ok {
					args = append(args, id.Name)
					continue
				}
				args = append(args, "<非标识符>")
			}
			return true
		})
	}

	require.True(t, found,
		"service/task_billing.go 的 RecalculateTaskQuotaByTokens 里找不到 ResolveGroupRatio —— "+
			"Task 差额结算的倍率来源变了,请重新确认预扣与结算是否仍然同口径")
	require.Len(t, args, 2)
	assert.NotEqual(t, args[0], args[1],
		"ResolveGroupRatio 的两个实参又是同一个标识符(%q)—— 对角线缺陷回归:"+
			"预扣按交叉格、结算按对角格,差额会以追扣落到用户头上", args[0])
	assert.Equal(t, "userGroup", args[0],
		"第一个实参必须是所有者的 users.group,与 HandleGroupRatio(relayInfo.UserGroup, ...) 同口径")
}

// TestQyTieredSettleHookPointExists 锁住阶梯计价**最终结算**的调用点与顺序。
func TestQyTieredSettleHookPointExists(t *testing.T) {
	seq := qySvcCallsByFunc(t, qyTieredSettleGoPath)["TryTieredSettle"]
	require.NotEmpty(t, seq,
		"service/tiered_settle.go 里找不到 TryTieredSettle —— 上游改了函数名?")

	hookIdx := qySvcIndexOf(seq, "QyGroupTieredSettle")
	require.GreaterOrEqual(t, hookIdx, 0,
		"TryTieredSettle 里缺少 QyGroupTieredSettle 挂载点:"+
			"阶梯计价的分组级折扣会在结算时丢失,差额以追扣形式落到用户头上")

	computeIdx := qySvcIndexOf(seq, "ComputeTieredQuotaWithRequest")
	require.GreaterOrEqual(t, computeIdx, 0,
		"TryTieredSettle 里找不到 ComputeTieredQuotaWithRequest —— 结算路径变了?")
	assert.Greater(t, hookIdx, computeIdx,
		"乘数必须作用在表达式重跑之后:提前调用拿到的不是 ActualQuotaBeforeGroup")

	roundIdx := qySvcIndexOf(seq, "QuotaRoundChecked")
	require.GreaterOrEqual(t, roundIdx, 0,
		"乘数作用后必须按 billingexpr 同一公式重算 after-group,否则两侧舍入口径不同")
	assert.Greater(t, roundIdx, hookIdx, "重算必须排在乘数之后")
}

// TestQyTieredRetryReservationHookPointExists 锁住同一个变量的**第二个调用点**。
//
// refreshTieredBillingGroup 跑在 auto 重试切分组之后,拿
// snap.EstimatedQuotaBeforeGroup 乘新分组的倍率重算预留额。分组级乘数和分组倍率
// 一样是"当前分组"的属性,这里不重算就会把原分组的乘数带进新分组的预留额。
//
// 为什么单靠上一条不够:那条锁的是最终扣费,这条锁的是预留额。两者是两个函数、
// 两条后果 —— 最终扣费一直是对的,错的是预扣多了(冻结用户额度)或少了(误判余额不足)。
func TestQyTieredRetryReservationHookPointExists(t *testing.T) {
	seq := qySvcCallsByFunc(t, qyTieredSettleGoPath)["refreshTieredBillingGroup"]
	require.NotEmpty(t, seq,
		"service/tiered_settle.go 里找不到 refreshTieredBillingGroup —— 上游改了函数名?")

	hookIdx := qySvcIndexOf(seq, "QyGroupTieredSettle")
	require.GreaterOrEqual(t, hookIdx, 0,
		"refreshTieredBillingGroup 里缺少 QyGroupTieredSettle 挂载点:"+
			"auto 重试切分组后预留额仍按原分组的乘数算")

	roundIdx := qySvcIndexOf(seq, "QuotaRoundStrict")
	require.GreaterOrEqual(t, roundIdx, 0,
		"refreshTieredBillingGroup 里找不到额度换算调用 —— 预留额换算路径变了?")
	assert.Greater(t, roundIdx, hookIdx,
		"乘数必须在乘分组倍率、换算成额度之前应用")
}

// ─────────────────────────────── 测试辅助 ───────────────────────────────

func qySvcCallsByFunc(t *testing.T, path string) map[string][]string {
	t.Helper()
	file := qySvcParseFileOrFail(t, path)

	out := map[string][]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var seq []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				seq = append(seq, f.Name)
			case *ast.SelectorExpr:
				seq = append(seq, f.Sel.Name)
			}
			return true
		})
		out[fn.Name.Name] = seq
	}
	return out
}

func qySvcParseFileOrFail(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err, "应当可解析: %s", path)
	return file
}

func qySvcIndexOf(seq []string, name string) int {
	for i, s := range seq {
		if s == name {
			return i
		}
	}
	return -1
}
