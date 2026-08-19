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

// pricing_single_resolver_guard_test.go —— 把「费率与法币折算比例取自同一个人
// 同一时刻的分组」变成一条会红的事实。
//
// # 为什么需要这条守卫
//
// 这两档比例一度取自**两个不同的人**:
//
//	grouprate.go  三档费率   ← 下线的分组
//	fiatrate.go   折算比例   ← 上线的分组
//
// 那不是设计,是两轮开发各自选了一个口径而没人把它们放在一起看。而它能活下来
// 是因为**没有任何东西会红**:一条 accrual 行的三条恒等式(base × rate = gross、
// gross = settled + outstanding、usd_rate 加权平均)在口径分叉时全部成立,
// 降级计数器也不会响。发现它只能靠人拿着两个文件的头注释对读。
//
// 本轮把"取谁的分组"这个决定收进 resolveInviterPricing 一处。收拢本身不产生
// 持续保护:下一个人加一条计佣路径时,就地写一句 resolveRate(ctx, e.Group, …)
// 仍然是最省事的写法,而那一句会不会传错人,只有代码评审看得出来。
// 因此把「计佣路径不得自己解析费率/比例」写成断言。
//
// # 这条守卫抓不到什么
//
// 它只看谁调了解析函数、以及那两个调用是不是喂了同一个变量,不看那个变量
// 装的是不是上线。"装的确实是上线"由 pricing_test.go 的 DB 级表驱动负责
// (那里上线与下线一律在不同分组,取错人会体现成一个具体的错金额)。

// pricingResolverEntryFile 是允许调用底层解析函数的**唯一**文件。
const pricingResolverEntryFile = "pricing.go"

// singleResolverOnly 是只能从 pricingResolverEntryFile 调用的解析函数。
//
// fiatRateFor 也在列,但它多一个合法调用点:fiatrate.go 自己
// (resolveFiatRate 就是它的包装)。值里写的是**额外**允许的文件。
var singleResolverOnly = map[string][]string{
	"resolveRate":     nil,
	"resolveFiatRate": nil,
	// fiatRateFor 是**纯层级判定**,签名里根本没有"人":它拿一条已经查好的
	// 规则加全局配置,回答"这落在哪一层"。因此它不可能把两档喂给不同的人,
	// 管理端的回显与试算(settingsSnapshot / fiatRateViews)直接用它是对的 ——
	// 那两处要的正是"没配分组档的人走哪一层",复刻一份才是隐患。
	"fiatRateFor": {"fiatrate.go", "api_admin.go"},
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
			// 函数自己的定义体不算调用点;这里只看 CallExpr,所以定义不会命中。
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
		"计佣路径必须经由 %s 的 resolveInviterPricing 解析费率与法币比例。"+
			"就地解析就是在重新打开两档口径分叉的那道口子 —— 上一次分叉的表现是"+
			"费率读下线、比例读上线,而账本每一行仍然自洽、没有任何守卫会响,"+
			"只能靠人对读两个文件的头注释才发现。"+
			"新增计佣路径请调 resolveInviterPricing,不要在这里加白名单",
		pricingResolverEntryFile)

	// 反向断言:合一之后 pricing.go **确实**在用那两个解析器。
	// 没有这一半,把 resolveInviterPricing 整段删成"恒返全局默认档"同样能让
	// 上面的断言通过 —— 全站分组费率一夜归零,而守卫全绿。
	for _, name := range []string{"resolveRate", "resolveFiatRate"} {
		assert.True(t, entryCalls[name],
			"%s 不再调用 %s —— 分组档要么被整段删掉了,要么又被换成了本地实现",
			pricingResolverEntryFile, name)
	}
}

// TestPricingEntryFeedsOneGroupToBothTiers 是上一条的另一半:
// 两个解析器必须被喂**同一个变量**。
//
// 只守住"只有 pricing.go 能调"是不够的 —— 在 pricing.go 里写
// resolveRate(ctx, invitee.Group, …) 与 resolveFiatRate(ctx, inviter.Group, …)
// 完全通得过上一条,而那正是本轮在消灭的那个形状,只是搬了个家。
func TestPricingEntryFeedsOneGroupToBothTiers(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), pricingResolverEntryFile, nil, 0)
	require.NoError(t, err)

	// groupArg 取调用的第二个实参(签名是 (ctx, group, …))并要求它是一个
	// 裸标识符。要求裸标识符是刻意的:一旦有人写成 e.Group / f(x) 这类表达式,
	// "两处是不是同一个值"就不再是语法上看得出来的事,而这条守卫的全部价值
	// 就在于它是语法上看得出来的。
	groupArg := func(call *ast.CallExpr) (string, bool) {
		if len(call.Args) < 2 {
			return "", false
		}
		id, ok := call.Args[1].(*ast.Ident)
		if !ok {
			return "", false
		}
		return id.Name, true
	}

	rateArgs := map[string]bool{}
	fiatArgs := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		name, ok := groupArg(call)
		if !ok {
			return true
		}
		switch id.Name {
		case "resolveRate":
			rateArgs[name] = true
		case "resolveFiatRate":
			fiatArgs[name] = true
		}
		return true
	})

	require.Len(t, rateArgs, 1,
		"resolveRate 必须只被喂一个分组变量(实际: %v)—— 多于一个说明费率这一档"+
			"又开始按不同的人解析了", rateArgs)
	require.Len(t, fiatArgs, 1, "resolveFiatRate 同上(实际: %v)", fiatArgs)
	assert.Equal(t, rateArgs, fiatArgs,
		"费率与法币折算比例必须被喂**同一个**分组变量。"+
			"喂两个变量正是本轮统一掉的那个缺陷 —— 同一条 accrual 行的 rate_group "+
			"记着 A 的分组、usd_rate 却按 B 的分组算出来,而三条恒等式全部成立")
}

// userGroupChangeNotifiers 是主库里**写 users.group 之后必须通知扩展侧**的出口,
// 按「文件 → 函数名」列出。
//
// 精确到函数而不是"文件里出现过这个调用":user.go 上有 Update 与 Edit 两条路
// 都能改分组,只按文件判定时删掉其中一条仍然全绿 —— 实测过,那正是这条守卫
// 第一版放过去的变异。漏掉的那一条会让"管理端改分组"或"套餐升级"其中一种
// 场景静默退回 300 秒延迟,而另一种是好的,排查的人会得出完全错误的结论。
//
// 白名单而不是"扫描所有写 group 的地方":后者要在 AST 上认出 GORM 的
// Update("group", …) / Updates(map…) / Updates(struct) 三种形态外加事务嵌套,
// 认漏一种就给人虚假的安全感。新增一个改分组的出口时,作者必须回到这里加一行,
// 而那一行正是他停下来想一想"这会不会让某个推广人的费率晚五分钟生效"的时刻。
var userGroupChangeNotifiers = map[string][]string{
	// 套餐购买/升级/降级/到期回退,四条路都汇进这一个函数(提交之后才调)。
	"model/subscription.go": {"refreshSubscriptionUserGroupCache"},
	// model 层的两个包装:自助更新走 Update,改名/改组走 Edit。
	"model/user.go": {"Update", "Edit"},
	// 管理端改用户**不走** model.User.Edit:它为了把"摘掉负责改组的订阅"与
	// 改组放进同一个事务,直接调 EditWithTx。也就是说改分组最常用的那条路径
	// 恰恰绕过了 model 层的通知,必须在处理器里自己补一句。
	"controller/user.go": {"UpdateUser"},
	// 用户分组批量改名/迁移,提交之后整表清空。
	"model/qy_groupns_export.go": {"QyRewriteUserGroupTx"},
}

// TestUserGroupWritersNotifyCommission 钉住"换上线分组立即生效"的主库一端。
//
// 费率现在按上线自己的分组取值,而那个分组被缓存在扩展侧
// (inviterEntry.Group,TTL = InviterCacheSecs,默认 300 秒)。不通知的表现是:
// 推广人刚升到 vip,接下来五分钟里他名下产生的每一笔佣金仍按旧档计 ——
// 而那些行的费率是**冻结**的,事后再刷缓存也追不回来。
//
// 扩展侧的另一端(注入进去的实现真的会清缓存)由
// TestUserGroupChangeHookInvalidatesInviterCache 守。
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
				// 两种形态都要认:model 包内是裸标识符,包外(controller)是
				// model.QyOnUserGroupChanged。只认前一种的话,恰恰是**包外**
				// 那条最常用的管理端改组路径永远查不出来。
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
				"%s 的 %s 会改写 users.group,却没有调用 QyOnUserGroupChanged 失效返佣模块的分组缓存。"+
					"后果不是「晚五分钟看到」,是那五分钟的佣金按旧档冻结进了账本,事后刷缓存也追不回来",
				rel, want)
		}
	}
}
