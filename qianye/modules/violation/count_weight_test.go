package violation

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// count_weight_test.go —— 「计数权重」这一格的口径,以及 severity 退场之后的兼容性。
//
// # 这个文件回答的是项目方看着截图问的那句话
//
// 「规则已经绑了违规类型、类型也带阈值了,计数权重和严重级别这两个字段还留着做什么」。
//
// 两个字段的命运是相反的:
//
//   - count_weight **留着**,因为它与类型阈值不是同一件事,而是**乘数与线**的关系:
//     N 次命中推进 N × 权重,达到该类型的 threshold 才触发处置。管理端表单现在把这句
//     算式直接写出来(「约 {{hits}} 次命中到线」,hits = ceil(threshold / weight))。
//     下面第一张表就是这句话在后端的判据 —— 界面上的算式与真实计数必须逐格相等,
//     否则运营按着界面配出来的规则会在一个他没预期的次数上封人。
//   - severity **移除**,因为它从头到尾只写不读。表单、API 字段与内置种子全部撤掉,
//     但**数据库列保留**(删列是跨三种数据库的不可逆迁移,而没人读的列代价为零)。
//     这个取舍只有在"列还在、结构体不再映射它"这一对状态下才成立,所以它必须被测到:
//     见 TestLegacySeverityColumnIsLeftAloneByRuleWrites。

// TestCountWeightIsAMultiplierAgainstTheCategoryThreshold 把管理端表单上那句
// 「N 次 × 权重 ≥ 阈值 → 约 N 次到线」钉在真实计数上。
//
// # 为什么必须逐格给出期望值,而不是在测试里再算一遍 ceil
//
// 在测试里复算等于把同一个公式写两遍:公式本身错了(比如该向上取整写成了向下),
// 两边会一起错、一起绿。表里写死的这几行是人算出来的,它们与实现之间没有共享的推导。
//
// 权重与阈值不整除的那两行是这张表的重点(weight=3/threshold=10 与
// weight=7/threshold=10):计数是整数步进的,所以到线那一刻的计数会**越过**阈值
// (12 > 10、14 > 10),而"几次到线"必须向上取整。写成向下取整的话,界面会告诉运营
// 「3 次到线」,而真实是第 4 次 —— 一次说给运营听的、关于什么时候封人的谎。
func TestCountWeightIsAMultiplierAgainstTheCategoryThreshold(t *testing.T) {
	cases := []struct {
		name string
		// weight 是规则上的 count_weight;threshold 是它所绑违规类型的次数阈值。
		weight    int
		threshold int
		// wantHits 是"第几次命中越线",即界面上的 hits。
		wantHits int
		// wantCountAtLine 是越线那一刻计数器里的数。它与 threshold 不一定相等 ——
		// 不整除时会越过去,而那正是"乘数"与"线"是两件事的最直观证据。
		wantHitCountAtLine int
	}{
		{"权重 1:一次命中算一次(最常见的一档)", 1, 3, 3, 3},
		{"权重 1 配长线", 1, 10, 10, 10},
		{"权重 2:同一条线只要一半次数", 2, 10, 5, 10},
		{"权重 3 配阈值 10:不整除,第 4 次才到线且计数越过阈值", 3, 10, 4, 12},
		{"权重 7 配阈值 10:不整除,第 2 次到线", 7, 10, 2, 14},
		{"权重 2 配阈值 3:不整除,第 2 次到线", 2, 3, 2, 4},
		{"权重大于阈值:一次命中直接到线", 5, 3, 1, 5},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newCategoryDB(t)
			cat := Category{
				Id: 2, Key: CatJailbreak, Enabled: true,
				WindowHours: 24, Threshold: tc.threshold,
			}
			const userId = 42

			// 探针跑到期望次数的两倍再多一点:既能观察到"到线之后判据仍为真"
			// (已达,不是恰好跨越),也能在实现提前/推后到线时给出具体是第几次。
			firstReachedAt, hitCountAtLine := 0, 0
			for i := 1; i <= tc.wantHits*2+2; i++ {
				hit, reached, err := bumpCategoryCounter(ctx, gdb, userId, cat, tc.weight)
				require.NoError(t, err)
				assert.Equal(t, i*tc.weight, hit,
					"第 %d 次命中之后计数应为 %d × %d", i, i, tc.weight)
				if reached && firstReachedAt == 0 {
					firstReachedAt, hitCountAtLine = i, hit
				}
				if firstReachedAt > 0 {
					assert.True(t, reached,
						"越线之后每一次命中都必须继续判为已达(判据是「已达」而不是「恰好跨越」)")
				}
			}
			assert.Equal(t, tc.wantHits, firstReachedAt,
				"界面写的是「约 %d 次命中到线」,真实计数必须在同一次越线", tc.wantHits)
			assert.Equal(t, tc.wantHitCountAtLine, hitCountAtLine)
		})
	}
}

// TestCountWeightZeroAdvancesNoLine 固化 0 这一档的完整语义。
//
// 界面上这一档写的是「只按上面的处置动作办(记录 / 扣费 / 拦截照做),不给违规类型的
// 计数、也不给账号总量线加任何数 —— 这条规则永远不会把人推向封号」。
//
// 与 TestBumpCategoryCounterSkipsWhenNoLine 不重复:那条断的是"一行都不写库",
// 这条断的是"反复命中也永远不越线",即它**不是**一个把权重当 1 用的软默认。
// 把 bumpCategoryCounter 里的 `weight <= 0` 短路改成 `weight < 0`,那条仍然绿
// (0 权重的 upsert 写出的行 hit_count 也是 0),这条立刻红在第 1 次命中上。
func TestCountWeightZeroAdvancesNoLine(t *testing.T) {
	gdb := newCategoryDB(t)
	ctx := context.Background()
	// 阈值刻意配成 1 —— 只要有任何一次计数落地就会越线。
	cat := Category{Id: 2, Key: CatJailbreak, Enabled: true, WindowHours: 24, Threshold: 1}

	for i := 1; i <= 20; i++ {
		hit, reached, err := bumpCategoryCounter(ctx, gdb, 42, cat, 0)
		require.NoError(t, err)
		assert.Zero(t, hit, "第 %d 次命中仍不该推进计数", i)
		assert.False(t, reached, "权重 0 的规则永远不该把人推到线上")
	}

	var n int64
	require.NoError(t, gdb.Model(&CategoryCounter{}).Count(&n).Error)
	assert.Zero(t, n, "权重 0 连一行计数都不该写出来")
}

// TestPersistRecordFeedsTheSameWeightToBothLines 锁住"同一个数"这句话。
//
// 表单文案写的是「给所选违规类型的计数加几,同时也给账号总量线加同样多」。
// 这句话在代码里的形状就是 persistRecord 把**同一个** weight 形参交给
// bumpCounter 与 bumpCategoryCounter 两处。任何一处被改成常量 1(或改读
// 别的字段),文案立刻变成谎话,而线上没有任何症状:两条线各自都还在动,
// 只是其中一条不再听运营配的权重。
//
// 为什么是 AST 而不是行为测试:persistRecord 里账号总量线那一步走的是 MySQL 专属的
// `INSERT ... ON DUPLICATE KEY UPDATE`,SQLite 内存库连语法都过不了,这条链路没有
// 可用的行为入口(同一段理由见 TestPersistRecordBumpsCategoryFromFrozenColumn)。
// 权重本身的行为由上面两张表直接覆盖,这里只补"两处传的是同一个变量"这一格。
func TestPersistRecordFeedsTheSameWeightToBothLines(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "guard.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "persistRecord" {
			fn = d
			return false
		}
		return true
	})
	require.NotNil(t, fn, "persistRecord 不见了")

	// 形参名就是"这一次命中该加几"的唯一载体,两处调用必须都用它。
	require.Len(t, fn.Type.Params.List, 6, "persistRecord 的签名变了,这条锁必须跟着更新")
	weightParam := fn.Type.Params.List[4].Names[0].Name

	got := map[string]string{}
	ast.Inspect(fn.Body, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		// 权重在两个函数上的位置不同:bumpCounter(ctx, gdb, userId, weight, group),
		// bumpCategoryCounter(ctx, gdb, userId, cat, weight)。
		var at int
		switch ident.Name {
		case "bumpCounter":
			at = 3
		case "bumpCategoryCounter":
			at = 4
		default:
			return true
		}
		require.Lenf(t, call.Args, 5, "%s 的签名变了,这条锁必须跟着更新", ident.Name)
		arg, ok := call.Args[at].(*ast.Ident)
		require.Truef(t, ok, "%s 的权重实参不是一个标识符(%T),两条线可能已经拿到不同的数",
			ident.Name, call.Args[at])
		got[ident.Name] = arg.Name
		return true
	})

	assert.Equal(t, weightParam, got["bumpCounter"],
		"账号总量线拿到的不是 persistRecord 收到的权重")
	assert.Equal(t, weightParam, got["bumpCategoryCounter"],
		"类型线拿到的不是 persistRecord 收到的权重 —— "+
			"表单上「两条线加同样多」这句话就此失效,而线上没有任何症状")
}

// ─────────────────────── severity 退场之后 ───────────────────────

// TestRuleUpsertKeepsCountWeightAndIgnoresLegacySeverity 固化写入面的两件事。
//
//  1. count_weight 在 upsert 上原样保留,含 0 与大于 1 两档(它们是这一格的全部语义)。
//  2. 旧前端(或存着旧脚本的运营)继续发 `"severity": 3` 时,保存照常成功。
//     这一条是移除 API 字段的**前提**:如果多发一个已删字段会 400,那么这次改动
//     就是一次要求所有客户端同步升级的破坏性变更,而它显然不该是。
func TestRuleUpsertKeepsCountWeightAndIgnoresLegacySeverity(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			"权重 0(只按处置动作办,不推进任何线)",
			`{"name":"r","phase":"prompt","match_type":"keyword","pattern":"危险",` +
				`"mode":"shadow","action":"record","fee_mode":"none","count_weight":0}`,
			0,
		},
		{
			"权重 1(默认档)",
			`{"name":"r","phase":"prompt","match_type":"keyword","pattern":"危险",` +
				`"mode":"shadow","action":"record","fee_mode":"none","count_weight":1}`,
			1,
		},
		{
			"权重大于 1(一次命中顶多次)",
			`{"name":"r","phase":"prompt","match_type":"keyword","pattern":"危险",` +
				`"mode":"shadow","action":"record","fee_mode":"none","count_weight":5}`,
			5,
		},
		{
			"旧前端仍然发 severity:多出来的键必须被忽略,而不是让保存失败",
			`{"name":"r","phase":"prompt","match_type":"keyword","pattern":"危险",` +
				`"mode":"shadow","action":"record","fee_mode":"none","count_weight":2,"severity":3}`,
			2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req ruleUpsertReq
			require.NoError(t, common.UnmarshalJsonStr(tc.body, &req))
			var row Rule
			require.NoError(t, req.apply(&row))
			assert.Equal(t, tc.want, row.CountWeight)
		})
	}
}

// TestLegacySeverityColumnIsLeftAloneByRuleWrites 是"留列不留字段"这个取舍的判据。
//
// 取舍本身:severity 没有任何读点,所以表单与 API 字段撤掉;但删列是跨 SQLite /
// MySQL / PostgreSQL 的不可逆迁移,而一个没人读的列的代价是零,所以列保留。
// 让这个取舍成立的关键是**结构体不再映射它** —— 只有这样 GORM 的 Save 才不会
// 把既有行的历史取值改写成零值(将来真要给它一个用途时,数据还在)。
//
// 这条测试就是那句"不再映射"的机器判据:
//   - 有人把 `Severity int` 加回 Rule 上 → Save 会带上这一列,历史值被改写成 0,红;
//   - 有人顺手写一条 DROP COLUMN 迁移 → 建表/写入路径与这条测试的假设不再一致,
//     插入仍然会成功,但下面对历史值的断言会失去意义,届时必须显式面对它。
//
// 用 SQLite 建列跑真 SQL,而不是反射断言字段不存在:后者只能证明"结构体上没有它",
// 证明不了"写入不会碰它"—— 而后者才是这个取舍的全部内容。
func TestLegacySeverityColumnIsLeftAloneByRuleWrites(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	// Rule 上不该再有这个字段 —— 有的话下面的 Save 会把历史值冲掉。
	// 先断言它,再建列:反过来的话,加回字段之后 AutoMigrate 已经把列建好了,
	// 下面那条 ALTER 会以「列重复」失败,报出来的是一条与真实原因无关的错。
	_, mapped := reflect.TypeOf(Rule{}).FieldByName("Severity")
	require.False(t, mapped,
		"Rule 上又出现了 Severity 字段。它没有任何读点,加回来只会让 Save 改写一列没人看的数据;"+
			"真要给它一个用途,那是一次新功能,得先有读点")

	require.NoError(t, gdb.AutoMigrate(&Rule{}))

	// 现网的表是带这一列的(GORM 从不删列),这里把那个状态复现出来。
	// 三种数据库上它都是 `NOT NULL DEFAULT 1`,所以不写它的 INSERT 照样合法。
	require.NoError(t, gdb.Exec(
		"ALTER TABLE qy_violation_rule ADD COLUMN severity integer NOT NULL DEFAULT 1").Error)

	// 新建:列不在结构体上,由数据库默认值兜住,插入不得失败。
	row := Rule{
		Name: "qy-probe", Enabled: true, Mode: ModeShadow,
		Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "危险",
		Action: ActionRecord, FeeMode: FeeNone, GroupScopeMode: GroupScopeInclude,
		CountWeight: 2, CreatedAt: 1000, UpdatedAt: 1000,
	}
	require.NoError(t, gdb.Create(&row).Error, "结构体不映射 severity 时,新建规则必须照常成功")

	var seeded int
	require.NoError(t, gdb.Raw("SELECT severity FROM qy_violation_rule WHERE id = ?", row.Id).
		Scan(&seeded).Error)
	assert.Equal(t, 1, seeded, "新行的 severity 应由列自带的 DEFAULT 1 兜住")

	// 把它改成一个"运营当年配过"的值,再走一次完整的管理端更新路径。
	require.NoError(t, gdb.Exec("UPDATE qy_violation_rule SET severity = 3 WHERE id = ?", row.Id).Error)

	var loaded Rule
	require.NoError(t, gdb.Where("id = ?", row.Id).Take(&loaded).Error)
	var req ruleUpsertReq
	require.NoError(t, common.UnmarshalJsonStr(
		`{"name":"qy-probe-renamed","phase":"prompt","match_type":"keyword","pattern":"危险",`+
			`"mode":"shadow","action":"record","fee_mode":"none","count_weight":4}`, &req))
	require.NoError(t, req.apply(&loaded))
	require.NoError(t, gdb.Save(&loaded).Error)

	var after int
	require.NoError(t, gdb.Raw("SELECT severity FROM qy_violation_rule WHERE id = ?", row.Id).
		Scan(&after).Error)
	assert.Equal(t, 3, after,
		"编辑一条规则不该把 severity 的历史取值冲掉 —— 那正是「留列不留字段」要保住的东西")

	var reread Rule
	require.NoError(t, gdb.Where("id = ?", row.Id).Take(&reread).Error)
	assert.Equal(t, "qy-probe-renamed", reread.Name, "这次更新本身必须真的写进去了")
	assert.Equal(t, 4, reread.CountWeight)
}

// TestBuiltinCatalogNeverDecidesCountWeight 固化内置目录对权重的态度。
//
// 目录里的条目一律 CountWeight = 1:权重是运营对"这一类值几次"的判断,与 Mode /
// Action / FeeMode 同性质,内置目录没有立场替他放大或归零(applyUpgrade 因此也
// 一个字都不碰它)。有人在目录里写下 3,现网每一次导入都会在运营不知情的情况下
// 把这一类的到线次数缩到三分之一。
func TestBuiltinCatalogNeverDecidesCountWeight(t *testing.T) {
	require.NotEmpty(t, builtinCatalog)
	for _, b := range builtinCatalog {
		assert.Equalf(t, 1, b.CountWeight,
			"内置条目 %s 的 count_weight 是 %d。目录只提供模式串,权重归运营 —— "+
				"导入时替他放大计数等于替他改了封号次数", b.Key, b.CountWeight)
		assert.Equalf(t, 1, b.toRule(1000, 7).CountWeight,
			"内置条目 %s 铺成规则之后权重变了", b.Key)
	}
}

/*
── 变异验证(逐条改坏生产代码再跑本文件,改完即还原;括号里是实测结果)──

基线:本文件全绿,violation 包全绿。

	B1  category.go:bumpCategoryCounter 的短路 `weight <= 0` 改成 `weight < 0`
	                → TestCountWeightZeroAdvancesNoLine 红
	                  (0 权重开始写出计数行,阈值 1 立刻越线)
	B2  category.go:累加表达式 `hit_count + ?, weight` 改成常量 1
	                → TestCountWeightIsAMultiplierAgainstTheCategoryThreshold
	                  七格里红五格,只有两条 weight=1 的仍绿 —— 正是"权重是乘数"
	                  这句话被抽掉之后应有的形状
	B3  guard.go:persistRecord 给类型线传常量 1
	                → TestPersistRecordFeedsTheSameWeightToBothLines 红
	B4  model.go:把 `Severity int` 字段加回 Rule
	                → TestLegacySeverityColumnIsLeftAloneByRuleWrites 红在反射那一格
	B5  guard.go:persistRecord 给账号总量线传常量 1
	                → TestPersistRecordFeedsTheSameWeightToBothLines 红
	B6  builtin.go:把目录里第一条的 CountWeight 改成 3
	                → TestBuiltinCatalogNeverDecidesCountWeight 红

前端同一条判据(界面上的「约 N 次到线」)的变异见
web/src/features/qy/pages/admin-violation-rules/__tests__/rule-form-count-weight.test.tsx 末尾。
*/
