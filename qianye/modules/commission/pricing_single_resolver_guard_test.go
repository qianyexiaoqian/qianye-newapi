package commission

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pricing_single_resolver_guard_test.go —— 把「费率取自上线自己的分组、且只在一处
// 决定取谁的分组」变成一条会红的事实。
//
// # 为什么需要这条守卫
//
// 费率一度取自下线的分组:那不是设计,是一轮开发选了一个口径而没人把它与
// 分组费率的定义放在一起看。而它能活下来是因为**没有任何东西会红**:一条 accrual
// 行的恒等式在口径错了的时候全部成立,降级计数器也不会响。
//
// "取谁的分组"这个决定收进 resolveInviterPricing 一处。收拢本身不产生持续保护:
// 下一个人加一条计佣路径时,就地写一句 resolveRate(ctx, e.Group, …) 仍然是最省事
// 的写法,而那一句会不会传错人,只有代码评审看得出来。因此把「计佣路径不得
// 自己解析费率」写成断言。
//
// # 这条守卫抓不到什么
//
// 它只看谁调了解析函数,不看喂进去的变量装的是不是上线。"装的确实是上线"由
// grouprate_test.go 的 DB 级用例负责(那里上线与下线一律在不同分组,取错人会
// 体现成一个具体的错金额)。

// pricingResolverEntryFile 是允许调用底层解析函数的**唯一**文件。
const pricingResolverEntryFile = "pricing.go"

// singleResolverOnly 是只能从 pricingResolverEntryFile 调用的解析函数。
// 值里写的是**额外**允许的文件。
var singleResolverOnly = map[string][]string{
	"resolveRate": nil,
	// rateUnitsFor 是 resolveRate 的纯函数内核,降级路径直接用它。
	// 它同样只有 pricing.go 与 grouprate.go 两个合法调用点 —— 别处调它
	// 等于绕开 billingGroup 的归一化,拿一个没折叠大小写的分组去算钱。
	"rateUnitsFor": {"grouprate.go"},
}

func TestPricingResolvedThroughSingleEntry(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	var offenders []string
	entryCalls := map[string]bool{}

	for _, path := range files {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, perr, "无法解析 %s", base)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			extra, guarded := singleResolverOnly[id.Name]
			if !guarded {
				return true
			}
			if base == pricingResolverEntryFile {
				entryCalls[id.Name] = true
				return true
			}
			for _, allowed := range extra {
				if base == allowed {
					return true
				}
			}
			offenders = append(offenders, base+" 直接调用 "+id.Name)
			return true
		})
	}

	assert.Empty(t, offenders,
		"计佣路径必须经由 %s 的 resolveInviterPricing 解析费率。"+
			"就地解析就是在重新打开口径分叉的那道口子 —— 上一次分叉的表现是费率读了下线的分组,"+
			"而账本每一行仍然自洽、没有任何守卫会响。新增计佣路径请调 resolveInviterPricing,不要在这里加白名单",
		pricingResolverEntryFile)

	// 反向断言:合一之后 pricing.go **确实**在用解析器。
	// 没有这一半,把 resolveInviterPricing 整段删成"恒返全局默认档"同样能让
	// 上面的断言通过 —— 全站分组费率一夜归零,而守卫全绿。
	assert.True(t, entryCalls["resolveRate"],
		"%s 不再调用 resolveRate —— 分组档要么被整段删掉了,要么又被换成了本地实现",
		pricingResolverEntryFile)
}

// userGroupChangeNotifiers 是主库里**写 users.group 之后必须通知扩展侧**的出口,
// 按「文件 → 函数名」列出。
//
// 精确到函数而不是"文件里出现过这个调用":user.go 上有 Update 与 Edit 两条路
// 都能改分组,只按文件判定时删掉其中一条仍然全绿。漏掉的那一条会让"管理端改分组"
// 或"套餐升级"其中一种场景静默退回 300 秒延迟。
//
// 白名单而不是"扫描所有写 group 的地方":后者要在 AST 上认出 GORM 的三种写法
// 外加事务嵌套,认漏一种就给人虚假的安全感。
var userGroupChangeNotifiers = map[string][]string{
	// 套餐购买/升级/降级/到期回退,四条路都汇进这一个函数(提交之后才调)。
	"model/subscription.go": {"refreshSubscriptionUserGroupCache"},
	// model 层的两个包装:自助更新走 Update,改名/改组走 Edit。
	"model/user.go": {"Update", "Edit"},
	// 管理端改用户**不走** model.User.Edit,必须在处理器里自己补一句。
	"controller/user.go": {"UpdateUser"},
	// 用户分组批量改名/迁移,提交之后整表清空。
	"model/qy_groupns_export.go": {"QyRewriteUserGroupTx"},
}

// TestUserGroupWritersNotifyCommission 钉住"换上线分组立即生效"的主库一端。
//
// 费率按上线自己的分组取值,而那个分组被缓存在 invite 侧(TTL = InviterCacheSecs,
// 默认 300 秒)。不通知的表现是:推广人刚升到 vip,接下来五分钟里他名下产生的
// 每一笔佣金仍按旧档计 —— 而那些行的费率是**冻结**的,事后再刷缓存也追不回来。
func TestUserGroupWritersNotifyCommission(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	for rel, wantFuncs := range userGroupChangeNotifiers {
		path := filepath.Join(root, filepath.FromSlash(rel))
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, perr, "无法解析 %s", rel)

		notifiers := map[string]bool{}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					name = fun.Name
				case *ast.SelectorExpr:
					name = fun.Sel.Name
				}
				if name == "QyOnUserGroupChanged" {
					notifiers[fn.Name.Name] = true
				}
				return true
			})
		}
		for _, want := range wantFuncs {
			assert.True(t, notifiers[want],
				"%s 的 %s 会改写 users.group,却没有调用 QyOnUserGroupChanged 失效分组缓存。"+
					"后果不是「晚五分钟看到」,是那五分钟的佣金按旧档冻结进了账本,事后刷缓存也追不回来",
				rel, want)
		}
	}
}
