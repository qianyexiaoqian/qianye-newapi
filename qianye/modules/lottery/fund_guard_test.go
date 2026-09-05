package lottery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fund_guard_test.go —— 用 AST 钉死 model.go 头部那几条"资金铁律"。
//
// # 为什么必须是这种形状的测试
//
// 参与费与派奖只经 stardust.Credit / Debit 动账。绕过账本的写入(直接 UPDATE
// qy_sd_balance、往 qy_sd_ledger 插行)不会让任何普通测试变红 —— 接口照常 200,
// 余额看起来也对 —— 但 stardust 的 I0(余额 = 累计列之差)与 I1(余额 = Σ流水)
// 当场失效,而那两条不变量是账本能被复核的全部依据。同理,重新 import 主库的
// 扣款函数或跨库两阶段,也不会被任何行为测试抓到:它们只是让钱又回到了一条
// 没人对账的路上。所以把"谁能动账"本身变成断言。
//
// # 它守住的四条
//
//  1. 不 import qianye/service/twophase —— 没有资金单、没有探针、没有补偿。
//  2. 不调 model.DecreaseUserQuota / IncreaseUserQuota / QyRecordLedgerLog /
//     QyLockForUpdate —— 主库不是本模块的账。
//  3. 不对 stardust.Balance / stardust.Ledger 做任何写(Model(&stardust.Balance{}).Update…、
//     Create(&stardust.Ledger{})、Table("qy_sd_…") 上的写语句)—— 动账只经 Credit / Debit。
//  4. 锁序:参与事务里 reserveEntry 在 stardust.Debit 之前,出款事务里活动合计的
//     UPDATE 在 stardust.Credit 之前;进入参与事务之前必须先按 idem_key 读过票。
//     转盘的转动事务同一条:活动行 UPDATE → 锁内奖档完整性 → Debit → 派奖 Credit,
//     进入事务之前先按 idem_key 读过票(原样重放不得再摇)。

var (
	forbiddenImports = map[string]string{
		"github.com/QuantumNous/new-api/qianye/service/twophase": "参与与派奖是扩展库单事务,没有跨库两阶段",
	}
	// forbiddenModelCalls 是主库上那几条会绕过账本的函数。
	forbiddenModelCalls = map[string]string{
		"DecreaseUserQuota": "参与费扣的是星屑,不碰 users.quota",
		"IncreaseUserQuota": "派奖入的是星屑,不碰 users.quota",
		"QyRecordLedgerLog": "账本流水在 qy_sd_ledger,不往主库 logs 写账本行",
		"QyLockForUpdate":   "本模块不在主库行上加锁",
	}
	// stardustWriteTargets 是只允许 stardust 包自己写的两张表。
	stardustWriteTargets = map[string]bool{"Balance": true, "Ledger": true}
	stardustTables       = map[string]bool{"qy_sd_balance": true, "qy_sd_ledger": true}
	// gormWriters 是 GORM 上会改行的方法名。Model(&stardust.X{}) 之后接上任何一个
	// 都算绕过账本;Take / Find / Count / Select 这类读法不在此列。
	gormWriters = map[string]bool{
		"Update": true, "Updates": true, "UpdateColumn": true, "UpdateColumns": true,
		"Create": true, "CreateInBatches": true, "Save": true, "Delete": true, "Exec": true,
	}
)

func TestLotteryNeverBypassesTheStardustLedger(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.Greater(t, len(files), 10, "扫到的文件太少,遍历八成写错了")

	fset := token.NewFileSet()
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		scanned++
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoErrorf(t, err, "解析 %s 失败", path)

		for _, imp := range f.Imports {
			spec := strings.Trim(imp.Path.Value, `"`)
			if why, banned := forbiddenImports[spec]; banned {
				assert.Failf(t, "禁止的 import", "%s import 了 %s:%s", path, spec, why)
			}
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// model.DecreaseUserQuota(...) 这类:接收方是包标识符 model。
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "model" {
				if why, banned := forbiddenModelCalls[sel.Sel.Name]; banned {
					assert.Failf(t, "绕过账本的主库调用",
						"%s 调用了 model.%s:%s", path, sel.Sel.Name, why)
				}
			}
			if !gormWriters[sel.Sel.Name] {
				return true
			}
			// 一条 GORM 链:X.Model(&stardust.Balance{}).Updates(…) / X.Create(&stardust.Ledger{…})
			// / X.Table("qy_sd_balance").Update(…)。沿链往回找,任何一环碰到那两张表就是绕过。
			if target := stardustWriteTarget(call); target != "" {
				assert.Failf(t, "绕过账本的写入",
					"%s 直接对 %s 写入(%s):动账只经 stardust.Credit / Debit",
					path, target, sel.Sel.Name)
			}
			return true
		})
	}
	require.Greater(t, scanned, 10, "扫到的非测试文件太少,遍历八成写错了")
}

// stardustWriteTarget 回答"这条 GORM 调用链有没有碰到 stardust 的两张表",
// 碰到了就返回那张表的名字。
func stardustWriteTarget(call *ast.CallExpr) string {
	for cur := ast.Expr(call); cur != nil; {
		c, ok := cur.(*ast.CallExpr)
		if !ok {
			return ""
		}
		for _, arg := range c.Args {
			if name := stardustModelName(arg); name != "" {
				return name
			}
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		cur = sel.X
	}
	return ""
}

// stardustModelName 识别 &stardust.Balance{…} / stardust.Ledger{…} / "qy_sd_balance"。
func stardustModelName(arg ast.Expr) string {
	switch v := arg.(type) {
	case *ast.UnaryExpr:
		return stardustModelName(v.X)
	case *ast.CompositeLit:
		if sel, ok := v.Type.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "stardust" && stardustWriteTargets[sel.Sel.Name] {
				return "stardust." + sel.Sel.Name
			}
		}
	case *ast.BasicLit:
		lit := strings.Trim(v.Value, "`\"")
		for table := range stardustTables {
			if strings.Contains(strings.ToLower(lit), table) {
				return table
			}
		}
	}
	return ""
}

// 锁序与幂等读票的接线,从源码层钉住:纯函数写对了、顺序接反了是本仓的头号形状。
func TestLotteryFundPathsKeepTheLockOrder(t *testing.T) {
	entry := funcBody(t, "entry.go", "settleEntryTx")
	reserve := strings.Index(entry, "reserveEntry(")
	debit := strings.Index(entry, "stardust.Debit(")
	require.Positive(t, reserve, "settleEntryTx 里找不到 reserveEntry")
	require.Positive(t, debit, "settleEntryTx 里找不到 stardust.Debit")
	assert.Less(t, reserve, debit, "活动行锁必须先于余额行锁:reserveEntry 排在 stardust.Debit 之前")

	charge := funcBody(t, "entry.go", "ChargeEntry")
	lookup := strings.Index(charge, "loadEntryByIdemKey(")
	tx := strings.Index(charge, "settleEntryTx(")
	require.Positive(t, lookup, "ChargeEntry 里找不到按幂等键的读票")
	require.Positive(t, tx)
	assert.Less(t, lookup, tx, "进入参与事务之前必须先按 idem_key 读票:原样重放要拿回原票,而不是撞键")

	payout := funcBody(t, "payout.go", "drivePayout")
	totals := strings.Index(payout, "addPaidPayoutToActivityTotals(")
	credit := strings.Index(payout, "stardust.Credit(")
	require.Positive(t, totals, "drivePayout 里找不到活动合计的补计")
	require.Positive(t, credit, "drivePayout 里找不到 stardust.Credit")
	assert.Less(t, totals, credit, "活动行的 UPDATE 必须排在 stardust.Credit 之前 —— 持余额行锁的事务不得再去锁活动行")

	// 转盘:活动行 UPDATE(取锁)→ 奖档完整性 → Debit → 派奖(grantWheelPrize 里的 Credit)。
	spin := funcBody(t, "wheel.go", "spinTx")
	lock := strings.Index(spin, "Updates(")
	spec := strings.Index(spin, "checkSpecIntegrity(")
	spinDebit := strings.Index(spin, "stardust.Debit(")
	grant := strings.Index(spin, "grantWheelPrize(")
	require.Positive(t, lock, "spinTx 里找不到活动行的条件 UPDATE")
	require.Positive(t, spec, "spinTx 里找不到锁内奖档完整性校验")
	require.Positive(t, spinDebit, "spinTx 里找不到 stardust.Debit")
	require.Positive(t, grant, "spinTx 里找不到派奖")
	assert.Less(t, lock, spec, "奖档完整性必须在活动行锁内校验")
	assert.Less(t, spec, spinDebit, "奖档对不上就不能扣钱:完整性校验必须排在 stardust.Debit 之前")
	assert.Less(t, spinDebit, grant, "先扣本金再派奖:Debit 必须排在 Credit 之前")
	assert.NotContains(t, spin, "stardust.Credit(", "派奖的 Credit 只能住在 grantWheelPrize 里")
	assert.Contains(t, funcBody(t, "wheel.go", "grantWheelPrize"), "stardust.Credit(")

	handler := funcBody(t, "wheel.go", "handleSpin")
	replay := strings.Index(handler, "loadEntryByIdemKey(")
	spinCall := strings.Index(handler, "spinWheel(")
	require.Positive(t, replay, "handleSpin 里找不到按幂等键的读票")
	require.Positive(t, spinCall)
	assert.Less(t, replay, spinCall, "进入转动事务之前必须先按 idem_key 读票:原样重放要拿回原来那一转,而不是再摇一次")
}

// funcBody 取出一个顶层函数的源码正文。
func funcBody(t *testing.T, path, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Body == nil {
			continue
		}
		src, err := os.ReadFile(path)
		require.NoError(t, err)
		start := fset.Position(fn.Body.Lbrace).Offset
		end := fset.Position(fn.Body.Rbrace).Offset
		return string(src[start:end])
	}
	require.Failf(t, "找不到函数", "%s 里没有 %s", path, name)
	return ""
}
