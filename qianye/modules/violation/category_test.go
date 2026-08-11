package violation

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// category_test.go —— 违规类型:增删改、规则绑定、计数累加、阈值触发、归档后历史仍在。
//
// 这里守的不是"函数返回值对不对",是五件一旦错掉就会直接改变谁被封号、
// 或者把绕过方法送到用户浏览器里的事:
//
//   - 规则永远绑得到一个类型(孤儿 = 这条规则的命中不计入任何类型线);
//   - 同一类型下多条规则的命中累加到**同一个**计数桶;
//   - 两条线(账号总量线 / 单类型线)是 OR,且触发原因可追溯;
//   - 归档是软删,历史违规记录一行都不能少;
//   - 用户端公示里绝不出现内部名与内部说明。

// newCategoryDB 建一个承载类型、类型计数、规则与记录的内存库。
//
// 不 mock:本文件要验证的是 upsert 语义、软删语义与外键式的绑定关系,
// 这三件事只有真的跑一遍 SQL 才算验证过。
func newCategoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Category{}, &CategoryCounter{}, &Rule{}, &Record{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// useDBForTest 把测试库接到 db.Get()。
//
// 必须有:revertCategoryCounter 与生产代码其余部分一样自取句柄(见
// api_admin_banpolicy_test.go 顶部的说明——把 *gorm.DB 传进被测函数等于测一条
// 生产里不存在的调用路径)。句柄本身借用 rules_ctx_test.go 已经 linkname 出来的
// 那一个,不再另开一条同名符号的旁路。
func useDBForTest(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	prev := qyDBHandleForCtxTest.Swap(gdb)
	t.Cleanup(func() { qyDBHandleForCtxTest.Store(prev) })
}

// useCategorySnapshot 直接装配一份带类型的快照,绕开数据库。
//
// 解析逻辑(规则 → 类型)与加载逻辑(库 → 快照)是两件事;后者由
// TestSeedAndMigrationBindsExistingRules 走真库覆盖。
func useCategorySnapshot(t *testing.T, s *snapshot) {
	t.Helper()
	prev := current.Load()
	prevNext := nextRefreshAt.Load()
	current.Store(s)
	// 推到未来,避免 maybeRefresh 在用例中途去查一个不存在的库把快照冲掉。
	nextRefreshAt.Store(common.GetTimestamp() + 3600)
	t.Cleanup(func() {
		current.Store(prev)
		nextRefreshAt.Store(prevNext)
	})
}

// ─────────────────────── 规则绑定:永不产生孤儿 ───────────────────────

// TestCategoryForRuleNeverOrphans 是"规则绑到类型"这条链路的核心判据。
//
// 孤儿的后果是静默的:这条规则照样命中、照样扣钱、照样推进账号总量线,
// 只是它的命中**不计入任何类型线**。没有任何报错,只有几天后
// "他在破限上都犯了 8 次了怎么还没被处置"才会暴露。
func TestCategoryForRuleNeverOrphans(t *testing.T) {
	fallback := Category{Id: 1, Key: FallbackCategoryKey, Name: "未分类", IsFallback: true}
	jailbreak := Category{Id: 2, Key: CatJailbreak, Name: "破限", Enabled: true, Threshold: 3, WindowHours: 24}
	snap := &snapshot{
		catById:     map[int64]Category{1: fallback, 2: jailbreak},
		catByKey:    map[string]Category{FallbackCategoryKey: fallback, CatJailbreak: jailbreak},
		catFallback: fallback,
	}

	cases := []struct {
		name   string
		snap   *snapshot
		ruleId int64
		wantId int64
	}{
		{"绑了类型 → 就是那一类", snap, 2, 2},
		{"从未绑过(0)→ 兜底", snap, 0, 1},
		{"负数(手工 SQL 写坏)→ 兜底", snap, -7, 1},
		{"指向一个已归档 / 不存在的类型 → 兜底,而不是不计数", snap, 999, 1},
		// 快照没有类型表(扩展库刚起来、种子还没落地)时返回零值:
		// 此时 bumpCategoryCounter 直接跳过,账号总量线照常工作。
		{"快照里一个类型都没有 → 零值,类型计数整体跳过", &snapshot{}, 2, 0},
		{"快照为 nil → 零值", nil, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := categoryForRule(tc.snap, tc.ruleId)
			assert.Equal(t, tc.wantId, got.Id,
				"折叠方向错了:孤儿规则的命中会不计入任何类型线,而这件事没有任何报错")
		})
	}
}

// TestApplyCategoryFreezesAllThreeColumns 守的是"历史记录能独立解释自己算哪一类"。
//
// 只写 category_id 而漏掉后两列,类型一旦被归档或改名,这条记录在管理端与用户端
// 就只剩一个数字。ApplyCategory 是模块外(AI 审核)唯一该用的入口,
// 它必须一次写全三列。
func TestApplyCategoryFreezesAllThreeColumns(t *testing.T) {
	cat := Category{Id: 5, Key: "spam", Name: "内部代号 spam_v3", PublicTitle: "垃圾内容"}
	useCategorySnapshot(t, &snapshot{
		catById:  map[int64]Category{5: cat},
		catByKey: map[string]Category{"spam": cat},
	})

	rec := &Record{}
	ApplyCategory(rec, 5)
	assert.Equal(t, int64(5), rec.CategoryId)
	assert.Equal(t, "内部代号 spam_v3", rec.CategoryName, "内部名要冻结:管理端复核靠它")
	assert.Equal(t, "垃圾内容", rec.CategoryPublicTitle, "公示文案要冻结:用户端列表显示的是它")

	// nil 记录不能 panic:外部调用方拿到的可能是一条构造失败的记录。
	assert.NotPanics(t, func() { ApplyCategory(nil, 5) })
}

// TestCategoryByKeyIsTheExternalSeam 固化给 AI 审核等外部来源留的接口。
//
// 用 key 而不是 id:id 是自增主键,在不同站点上是不同的数字,而外部来源要写死的是
// "这次命中算破限"这个语义。
func TestCategoryByKeyIsTheExternalSeam(t *testing.T) {
	cat := Category{Id: 2, Key: CatJailbreak, Name: "破限"}
	useCategorySnapshot(t, &snapshot{
		catById:  map[int64]Category{2: cat},
		catByKey: map[string]Category{CatJailbreak: cat},
	})

	cases := []struct {
		name   string
		key    string
		wantOK bool
		wantId int64
	}{
		{"精确命中", CatJailbreak, true, 2},
		{"大小写不敏感", "JailBreak", true, 2},
		{"两侧空白被归一", "  jailbreak  ", true, 2},
		{"空 key", "", false, 0},
		{"未知 key → 调用方应回落自己的兜底,而不是丢掉这条命中", "no_such", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CategoryByKey(tc.key)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantId, got.Id)
		})
	}
}

// ─────────────────────── 计数累加与阈值触发 ───────────────────────

// TestBumpCategoryCounterAccumulatesAcrossRules 是本轮需求的核心判据:
// **同一类型下多条规则的命中累加到同一个计数桶**。
//
// 计数键是 category_id 而不是 rule_id —— 如果写成后者,"破限类累计 3 次封号"
// 会变成"某一条破限规则命中 3 次封号",而绕过方法是每次换一条规则触发。
func TestBumpCategoryCounterAccumulatesAcrossRules(t *testing.T) {
	gdb := newCategoryDB(t)
	cat := Category{Id: 2, Key: CatJailbreak, Enabled: true, WindowHours: 24, Threshold: 3}
	ctx := context.Background()
	const userId = 42

	// 三次命中来自三条不同的规则,但它们绑的是同一个类型。
	steps := []struct {
		name        string
		weight      int
		wantHit     int
		wantReached bool
	}{
		{"规则 A 第一次命中", 1, 1, false},
		{"规则 B 命中(不同规则,同一类型)", 1, 2, false},
		{"规则 C 命中 → 达到该类型阈值", 1, 3, true},
		{"阈值之后继续命中,判据仍为真(已达,不是恰好跨越)", 1, 4, true},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			hit, reached, err := bumpCategoryCounter(ctx, gdb, userId, cat, s.weight)
			require.NoError(t, err)
			assert.Equal(t, s.wantHit, hit, "同一类型下的命中必须累加到同一个桶")
			assert.Equal(t, s.wantReached, reached)
		})
	}

	// 另一个类型是独立的桶:不该被上面四次命中污染。
	other := Category{Id: 3, Key: CatReverse, Enabled: true, WindowHours: 24, Threshold: 2}
	hit, reached, err := bumpCategoryCounter(ctx, gdb, userId, other, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, hit, "不同类型必须是不同的计数桶")
	assert.False(t, reached)

	// 另一个用户同样是独立的桶。
	hit, _, err = bumpCategoryCounter(ctx, gdb, 43, cat, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, hit, "不同用户必须是不同的计数桶")

	var row CategoryCounter
	require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", userId, cat.Id).Take(&row).Error)
	assert.Equal(t, int64(4), row.TotalCount, "total_count 是终身累计,不随窗口滚动清零")
}

// TestBumpCategoryCounterResetsExpiredWindow 固化滚动窗口语义。
//
// 窗口过期不清零的后果是"计数永不过期":一年前的一次命中仍然算数,
// 而阈值是按"窗口内 N 次"配的。
func TestBumpCategoryCounterResetsExpiredWindow(t *testing.T) {
	gdb := newCategoryDB(t)
	ctx := context.Background()
	cat := Category{Id: 2, Enabled: true, WindowHours: 1, Threshold: 3}
	const userId = 7

	_, _, err := bumpCategoryCounter(ctx, gdb, userId, cat, 2)
	require.NoError(t, err)

	// 把窗口起点推回两小时前 —— 相当于窗口已经滚过。
	require.NoError(t, gdb.Model(&CategoryCounter{}).
		Where("user_id = ? AND category_id = ?", userId, cat.Id).
		Update("window_start", common.GetTimestamp()-2*3600).Error)

	hit, reached, err := bumpCategoryCounter(ctx, gdb, userId, cat, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, hit, "窗口滚过之后必须从本次权重重新起算,而不是接着 2 往上加")
	assert.False(t, reached)

	var row CategoryCounter
	require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", userId, cat.Id).Take(&row).Error)
	assert.Equal(t, int64(3), row.TotalCount, "窗口重置不该抹掉终身累计")
}

// TestBumpCategoryCounterSkipsWhenNoLine 固化两条"不该写库"的短路。
func TestBumpCategoryCounterSkipsWhenNoLine(t *testing.T) {
	gdb := newCategoryDB(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		cat    Category
		weight int
	}{
		{"类型 id 为 0(快照里没有类型表)", Category{Id: 0, Enabled: true, Threshold: 1}, 1},
		{"权重为 0(规则声明只扣费不计数)", Category{Id: 2, Enabled: true, Threshold: 1}, 0},
		{"权重为负(手工 SQL 写坏的规则)", Category{Id: 2, Enabled: true, Threshold: 1}, -3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit, reached, err := bumpCategoryCounter(ctx, gdb, 1, tc.cat, tc.weight)
			require.NoError(t, err)
			assert.Zero(t, hit)
			assert.False(t, reached)
		})
	}
	var n int64
	require.NoError(t, gdb.Model(&CategoryCounter{}).Count(&n).Error)
	assert.Zero(t, n, "这三种情况一行都不该写")
}

// TestCategoryReached 把"这一类出不出线"的全部取值组合钉死。
//
// Enabled=false 等价于 Threshold=0,即"这一类不单独触发处置"——
// **不是**"这一类不计数"。计数照常累加,停用只是把线撤掉;
// 混淆这两者会让管理员在重新打开阈值时看到一段空白。
func TestCategoryReached(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		thr     int
		after   int
		want    bool
	}{
		{"未达阈值", true, 3, 2, false},
		{"恰好达到", true, 3, 3, true},
		{"阈值之后仍为真(已达,不是恰好跨越)", true, 3, 9, true},
		{"阈值 0 = 这一类不单独触发", true, 0, 100, false},
		{"负阈值同样视为不触发", true, -1, 100, false},
		{"类型停用 = 线被撤掉,再高的计数也不触发", false, 3, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := Category{Id: 2, Enabled: tc.enabled, Threshold: tc.thr}
			assert.Equal(t, tc.want, categoryReached(cat, tc.after))
		})
	}
}

// TestAnyReachedCombinesTwoLines 是"到底几次封号"这个问题的唯一答案。
//
// 两条线是 OR:账号总量线(跨全部类型)与单类型线,任一越过即触发。
// 撞了哪条必须能被区分并冻结进封禁行 —— 一行 hit_count=3 / threshold=10 的
// 封禁记录在管理端看起来完全说不通,除非同时看到它撞的是"某类 3 次"那条线。
func TestAnyReachedCombinesTwoLines(t *testing.T) {
	cases := []struct {
		name        string
		global      bool
		category    bool
		wantReached bool
		wantTrigger string
	}{
		{"两条线都没越过", false, false, false, ""},
		{"只有账号总量线越过", true, false, true, BanTriggerGlobal},
		{"只有单类型线越过", false, true, true, BanTriggerCategory},
		{"同一次命中把两条线一起推过", true, true, true, BanTriggerBoth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached, trigger := anyReached(counterState{Reached: tc.global, CatReached: tc.category})
			assert.Equal(t, tc.wantReached, reached)
			assert.Equal(t, tc.wantTrigger, trigger)
		})
	}
}

// TestRevertCategoryCounterRespectsWindow 守的是撤销记录时的计数回退。
//
// 只退账号总量线的话,撤销一条误判记录之后用户在这一类上仍然背着这一次,
// 而"离该类型封号还差几次"正是公示给用户看的数字。
func TestRevertCategoryCounterRespectsWindow(t *testing.T) {
	gdb := newCategoryDB(t)
	useDBForTest(t, gdb)
	ctx := context.Background()
	cat := Category{Id: 2, Enabled: true, WindowHours: 24, Threshold: 5}

	_, _, err := bumpCategoryCounter(ctx, gdb, 9, cat, 3)
	require.NoError(t, err)
	var row CategoryCounter
	require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 9, cat.Id).Take(&row).Error)

	t.Run("窗口相同 → 回退", func(t *testing.T) {
		require.NoError(t, revertCategoryCounter(9, cat.Id, 2, row.WindowStart))
		var after CategoryCounter
		require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 9, cat.Id).Take(&after).Error)
		assert.Equal(t, 1, after.HitCount)
	})

	t.Run("窗口已滚动 → 不回退,那个计数值已经失效", func(t *testing.T) {
		require.NoError(t, revertCategoryCounter(9, cat.Id, 1, row.WindowStart-999))
		var after CategoryCounter
		require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 9, cat.Id).Take(&after).Error)
		assert.Equal(t, 1, after.HitCount, "强行减会把当前窗口的合法计数扣掉,反而放过真正的违规用户")
	})

	t.Run("回退量大于计数 → 夹到 0,不得为负", func(t *testing.T) {
		var cur CategoryCounter
		require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 9, cat.Id).Take(&cur).Error)
		require.NoError(t, revertCategoryCounter(9, cat.Id, 100, cur.WindowStart))
		var after CategoryCounter
		require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 9, cat.Id).Take(&after).Error)
		assert.Zero(t, after.HitCount)
		assert.Zero(t, after.TotalCount)
	})
}

// TestRevertHitCountersRevertsBothLines 守的是"撤销一条记录要退回它推进过的**全部**计数"。
//
// 只退账号总量线是这条链路上最隐蔽的缺陷:接口照常 200、记录照常变成 revoked、
// 总量线照常回退,只有"离该类型封号还差几次"这个**公示给用户看的数字**悄悄
// 少了一次 —— 而它要等到有人被封的那一刻才会暴露。
func TestRevertHitCountersRevertsBothLines(t *testing.T) {
	newEnv := func(t *testing.T) *gorm.DB {
		t.Helper()
		gdb := newCategoryDB(t)
		require.NoError(t, gdb.AutoMigrate(&Counter{}))
		useDBForTest(t, gdb)
		now := common.GetTimestamp()
		require.NoError(t, gdb.Create(&Counter{
			UserId: 7, WindowStart: now, HitCount: 3, TotalCount: 3, UpdatedAt: now,
		}).Error)
		require.NoError(t, gdb.Create(&CategoryCounter{
			UserId: 7, CategoryId: 2, WindowStart: now, HitCount: 3, TotalCount: 3, UpdatedAt: now,
		}).Error)
		return gdb
	}
	read := func(t *testing.T, gdb *gorm.DB) (int, int) {
		t.Helper()
		var c Counter
		var cc CategoryCounter
		require.NoError(t, gdb.Where("user_id = ?", 7).Take(&c).Error)
		require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 7, 2).Take(&cc).Error)
		return c.HitCount, cc.HitCount
	}

	cases := []struct {
		name       string
		rec        Record
		wantGlobal int
		wantCat    int
	}{
		{
			"真实命中:两条线一起退",
			Record{UserId: 7, CategoryId: 2, Counted: true, CountWeight: 1}, 2, 2,
		},
		{
			"权重 2 的规则:两条线各退 2",
			Record{UserId: 7, CategoryId: 2, Counted: true, CountWeight: 2}, 1, 1,
		},
		{
			// 影子命中从来没有推进过任何计数,退一次就是凭空做减法。
			"影子记录(counted=false):一个字节都不动",
			Record{UserId: 7, CategoryId: 2, Counted: false, CountWeight: 1}, 3, 3,
		},
		{
			"count_weight 为 0(只扣费不计数的规则):不动",
			Record{UserId: 7, CategoryId: 2, Counted: true, CountWeight: 0}, 3, 3,
		},
		{
			// 迁移之前的历史记录可能没有 category_id。总量线仍然要退。
			"没有类型的历史记录:只退总量线",
			Record{UserId: 7, CategoryId: 0, Counted: true, CountWeight: 1}, 2, 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newEnv(t)
			rec := tc.rec
			revertHitCounters(gdb, &rec)
			gotGlobal, gotCat := read(t, gdb)
			assert.Equal(t, tc.wantGlobal, gotGlobal, "账号总量线")
			assert.Equal(t, tc.wantCat, gotCat,
				"类型线没退 = 用户在这一类上仍然背着这一次,而那正是公示给他看的数字")
		})
	}

	// 「窗口已滚动就不退」是 revertCounter / revertCategoryCounter 自己的判据,
	// 由 TestRevertCategoryCounterRespectsWindow 直接覆盖。这里不重复:
	// revertHitCounters 是**当场读**窗口起点再传下去的,那两个值必然相等,
	// 在这一层构造不出不相等的情形 —— 硬造一个只会得到一条永远为真的断言。

	t.Run("nil 输入不 panic", func(t *testing.T) {
		gdb := newEnv(t)
		assert.NotPanics(t, func() { revertHitCounters(gdb, nil) })
		assert.NotPanics(t, func() { revertHitCounters(nil, &Record{}) })
	})
}

// TestPersistRecordBumpsCategoryFromFrozenColumn 是一条 AST 锁,不是行为测试。
//
// 它锁的是 persistRecord 里类型线那一段的两个要点:
//
//  1. **类型必须由 rec.CategoryId 解析**(命中当时冻结进记录的那一个),而不是
//     "现在去查一次这条规则绑的是哪一类"。异步 worker 可能在几秒后才跑到这里,
//     期间管理员完全可以把规则改绑到别的类型,而把一次几秒前的命中记到新类型上
//     是错的 —— 与分组取 rec.UsingGroup 是同一个理由,后果同样完全静默。
//  2. **bumpCategoryCounter 真的被调用了**。整段被顺手删掉的话,类型阈值从此
//     永不触发:接口照常、记录照常、账号总量线照常,只有类型线悄悄死了。
//
// 为什么只能用 AST:persistRecord 在类型线之前先调 bumpCounter,而后者走的是
// MySQL 专属的 `INSERT ... ON DUPLICATE KEY UPDATE`,在 SQLite 内存库上跑不起来
// (见 TestPersistRecordUsesFrozenGroupForPolicy 的同一段说明),
// 所以这条链路没有可用的行为测试入口。计数本身的行为由
// TestBumpCategoryCounterAccumulatesAcrossRules 直接覆盖。
func TestPersistRecordBumpsCategoryFromFrozenColumn(t *testing.T) {
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

	bumped, resolvedFromFrozen := false, false
	ast.Inspect(fn.Body, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch ident.Name {
		case "bumpCategoryCounter":
			bumped = true
		case "categoryForRule":
			require.Len(t, call.Args, 2, "categoryForRule 的签名变了,这条锁必须跟着更新")
			sel, ok := call.Args[1].(*ast.SelectorExpr)
			require.Truef(t, ok,
				"类型必须由记录里冻结的 rec.CategoryId 解析,当前是 %T", call.Args[1])
			recv, ok := sel.X.(*ast.Ident)
			require.True(t, ok)
			assert.Equal(t, "rec", recv.Name)
			assert.Equal(t, "CategoryId", sel.Sel.Name,
				"类型必须取记录里冻结的那一个:异步 worker 跑到这里时规则可能已经被改绑,"+
					"把一次几秒前的命中记到新类型上是错的,而且完全静默")
			resolvedFromFrozen = true
		}
		return true
	})
	assert.True(t, bumped,
		"persistRecord 里找不到 bumpCategoryCounter —— 类型阈值从此永不触发,而没有任何症状")
	assert.True(t, resolvedFromFrozen,
		"persistRecord 里找不到 categoryForRule(rec.CategoryId) —— 类型不是从冻结列解析的")
}

// TestUserCategoryListOnlyReturnsPublished 是一条 AST 锁。
//
// 「未公示的类型不出现在用户端」是 Published 这一列存在的全部理由:仍在观察期的
// 新类型要照常计数、照常参与处置,但不该先告诉用户。过滤条件被去掉之后没有任何
// 症状 —— 接口照常 200,只是多返回几行,而那几行正是站点刻意不想公开的。
//
// 用 AST 而不是起一个 gin 夹具:这个 handler 的其余部分(计数、账号总量线)
// 都已经由 toUserCategoryView 与 resolveBanPolicy 的行为测试覆盖,唯独这一条
// WHERE 没有别的落点。
func TestUserCategoryListOnlyReturnsPublished(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "api_user.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "userListCategories" {
			fn = d
			return false
		}
		return true
	})
	require.NotNil(t, fn, "userListCategories 不见了 —— 用户端公示整条断了")

	found := false
	ast.Inspect(fn.Body, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Where" || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		// 判据刻意是"逐字等于",不是"包含 published":`published = ? OR 1=1`
		// 同样包含它,而那正是这条锁要挡的那种改法。
		if lit.Value == `"published = ?"` {
			found = true
		}
		return true
	})
	assert.True(t, found,
		"userListCategories 的类型查询不再是严格的 `published = ?`:"+
			"未公示的类型会被下发给用户,而那是站点刻意不想公开的观察期类型")
}

// ─────────────────────── 增删改:校验 ───────────────────────

// TestValidateCategory 是类型写入口的全部校验。
//
// 最重的一条是"勾了公示却没填公示标题":放过它的话,用户端会看到一行空白标题,
// 而下一个人最省事的修法是回落到内部名 —— 那正是这套隔离要防的事。
func TestValidateCategory(t *testing.T) {
	ok := func(mut func(*Category)) *Category {
		c := &Category{
			Key: "spam", Name: "垃圾内容", Remark: "内部:命中三组词表",
			PublicTitle: "垃圾内容", PublicDesc: "发送垃圾信息",
			Published: true, Enabled: true, WindowHours: 24, Threshold: 3,
		}
		if mut != nil {
			mut(c)
		}
		return c
	}
	cases := []struct {
		name    string
		mut     func(*Category)
		wantErr string
	}{
		{"完整合法", nil, ""},
		{"key 为空", func(c *Category) { c.Key = "" }, "类型标识不能为空"},
		{"key 只有空白", func(c *Category) { c.Key = "   " }, "类型标识不能为空"},
		{"key 含中文", func(c *Category) { c.Key = "垃圾" }, "只能包含小写字母"},
		{"key 含空格", func(c *Category) { c.Key = "bad key" }, "只能包含小写字母"},
		{"key 含大写 → 归一后通过", func(c *Category) { c.Key = "SPAM" }, ""},
		{"key 含下划线与连字符", func(c *Category) { c.Key = "spam_v3-x" }, ""},
		{"名称为空", func(c *Category) { c.Name = "" }, "类型名称不能为空"},
		{"公示了但没填公示标题", func(c *Category) { c.PublicTitle = "" }, "必须填写公示标题"},
		{"不公示就可以不填公示标题", func(c *Category) { c.PublicTitle = ""; c.Published = false }, ""},
		{"窗口为 0", func(c *Category) { c.WindowHours = 0 }, "统计窗口必须在"},
		{"窗口超过上界", func(c *Category) { c.WindowHours = maxCategoryWindowHours + 1 }, "统计窗口必须在"},
		{"阈值为负", func(c *Category) { c.Threshold = -1 }, "次数阈值必须在"},
		{"阈值 0 合法(这一类不单独触发)", func(c *Category) { c.Threshold = 0 }, ""},
		{"阈值超过上界", func(c *Category) { c.Threshold = maxCategoryThreshold + 1 }, "次数阈值必须在"},
		// 按 rune 计长:一段中文在 byte 口径下早就超了,但 varchar(N) 是 N 个字符。
		{"名称刚好到上限", func(c *Category) { c.Name = strings.Repeat("类", categoryNameMax) }, ""},
		{"名称超一个字", func(c *Category) { c.Name = strings.Repeat("类", categoryNameMax+1) }, "类型名称过长"},
		{"内部说明超长", func(c *Category) { c.Remark = strings.Repeat("说", categoryRemarkMax+1) }, "内部说明过长"},
		{"公示说明超长", func(c *Category) { c.PublicDesc = strings.Repeat("说", categoryPublicDescMax+1) }, "公示说明过长"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := ok(tc.mut)
			err := validateCategory(cat)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestCategoryVarcharLimitsMatchColumnTags 把生产侧的长度校验表钉死在 gorm tag 上。
//
// 与 TestRuleVarcharLimitsMatchColumnTags 同形,防的是同一次事故:两份事实漂移,
// 于是校验放过数据库拒绝的行(列被改窄)或拦下数据库接受的行(列被改宽)。
// SQLite 不校验 varchar 长度,所以这类漂移在开发机上一律绿灯,只在生产的 MySQL 上炸。
func TestCategoryVarcharLimitsMatchColumnTags(t *testing.T) {
	tags := map[string]int{}
	rt := reflect.TypeOf(Category{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		m := varcharLimitRe.FindStringSubmatch(f.Tag.Get("gorm"))
		require.NotNilf(t, m, "Category.%s 是 string 字段但解析不出 type:varchar(N);"+
			"两头不占的字段会静默退出守卫射程", f.Name)
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		tags[f.Name] = n
	}
	require.NotEmpty(t, tags, "Category 上一个 varchar 列都没解析出来,守卫必然是空转的")

	seen := map[string]struct{}{}
	for _, lim := range categoryVarcharLimits {
		want, ok := tags[lim.Field]
		require.Truef(t, ok, "categoryVarcharLimits 给 %q 定了上限,但它不是 varchar 列", lim.Field)
		assert.Equalf(t, want, lim.Max,
			"Category.%s 的校验上限(%d)与 gorm tag 的列宽(%d)不一致", lim.Field, lim.Max, want)
		// Get 必须真的读那一列:指错字段的 getter 会让这一格永远看着另一列。
		row := &Category{}
		reflect.ValueOf(row).Elem().FieldByName(lim.Field).SetString("qy-probe")
		assert.Equalf(t, "qy-probe", lim.Get(row), "categoryVarcharLimits 里 %q 的 Get 读的不是这一列", lim.Field)
		seen[lim.Field] = struct{}{}
	}
	for field := range tags {
		assert.Containsf(t, seen, field,
			"Category.%s 是有列宽的 varchar 列,却没有进 categoryVarcharLimits。"+
				"没有它,超长写入在 MySQL 上是 Error 1406 → 500「处理失败」,在 SQLite 上静默存下", field)
	}
}

// TestCategoryTightens 固化"什么时候要二次确认"。
//
// 判错方向的后果不对称:漏判(该确认没确认)会让一批存量账号在管理员毫不知情的
// 情况下越线;误判(不该确认却确认了)只是多点一次。所以新建一律算收紧。
func TestCategoryTightens(t *testing.T) {
	base := Category{Enabled: true, WindowHours: 24, Threshold: 10}
	cases := []struct {
		name      string
		hasBefore bool
		before    Category
		next      Category
		want      bool
	}{
		{"新建一条出线的类型", false, Category{}, base, true},
		{"新建但不出线(阈值 0)", false, Category{}, Category{Enabled: true, Threshold: 0}, false},
		{"新建但停用", false, Category{}, Category{Enabled: false, Threshold: 3}, false},
		{"阈值调小", true, base, Category{Enabled: true, WindowHours: 24, Threshold: 3}, true},
		{"阈值调大", true, base, Category{Enabled: true, WindowHours: 24, Threshold: 20}, false},
		{"窗口变长(更久以前的命中重新算数)", true, base, Category{Enabled: true, WindowHours: 72, Threshold: 10}, true},
		{"窗口变短", true, base, Category{Enabled: true, WindowHours: 6, Threshold: 10}, false},
		{"从不出线变成出线", true, Category{Enabled: true, Threshold: 0},
			Category{Enabled: true, WindowHours: 24, Threshold: 5}, true},
		{"从停用变成启用", true, Category{Enabled: false, WindowHours: 24, Threshold: 5},
			Category{Enabled: true, WindowHours: 24, Threshold: 5}, true},
		{"改成停用", true, base, Category{Enabled: false, WindowHours: 24, Threshold: 3}, false},
		{"完全不变", true, base, base, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, categoryTightens(tc.hasBefore, tc.before, tc.next))
		})
	}
}

// ─────────────────────── 种子与存量迁移 ───────────────────────

// TestSeedAndMigrationBindsExistingRules 是存量迁移策略的整体验收。
//
// 三条必须同时成立:
//   - 内置规则按内置目录里声明的类别精确落位(不是全部倒进「未分类」);
//   - 手写规则进「未分类」,而「未分类」的阈值是 0 —— 因此迁移**不改变**
//     任何用户的封号判定,这是迁移能安全上线的全部理由;
//   - 幂等:重复执行不改变任何已有取值。
func TestSeedAndMigrationBindsExistingRules(t *testing.T) {
	gdb := newCategoryDB(t)
	ctx := context.Background()

	// 存量:一条手写规则 + 一条内置规则(取内置目录里真实存在的第一条)+ 一条软删的手写规则。
	require.NotEmpty(t, builtinCatalog, "内置目录为空,这个用例覆盖不到落位分支")
	b := builtinCatalog[0]
	manual := &Rule{Name: "运营手写", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "x", Mode: ModeShadow}
	imported := &Rule{Name: b.Name, Phase: b.Phase, MatchType: b.MatchType, Pattern: b.Pattern,
		Mode: ModeShadow, Source: SourceBuiltin, BuiltinKey: b.Key}
	deleted := &Rule{Name: "已软删", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "y", Mode: ModeShadow}
	require.NoError(t, gdb.Create(manual).Error)
	require.NoError(t, gdb.Create(imported).Error)
	require.NoError(t, gdb.Create(deleted).Error)
	require.NoError(t, gdb.Delete(deleted).Error)

	require.NoError(t, ensureSeedCategories(ctx, gdb))
	moved, err := migrateRuleCategory(ctx, gdb)
	require.NoError(t, err)
	assert.EqualValues(t, 3, moved, "三条规则都要绑上类型,软删的那条也要绑")

	var cats []Category
	require.NoError(t, gdb.Find(&cats).Error)
	byKey := map[string]Category{}
	for _, c := range cats {
		byKey[c.Key] = c
	}

	t.Run("兜底类型存在、不公示、阈值为 0", func(t *testing.T) {
		fb, ok := byKey[FallbackCategoryKey]
		require.True(t, ok, "「未分类」兜底类型必须被种子建出来")
		assert.True(t, fb.IsFallback)
		assert.False(t, fb.Published, "「未分类」对用户没有信息量,不公示")
		assert.Zero(t, fb.Threshold,
			"「未分类」阈值必须是 0 —— 否则迁移完成的那一秒会按一条没人配过的线处置一批用户")
	})

	t.Run("出厂类型阈值一律为 0", func(t *testing.T) {
		for _, c := range cats {
			assert.Zerof(t, c.Threshold, "种子类型 %q 带了阈值:那是一次没有人按下过的上线", c.Key)
		}
	})

	t.Run("内置规则按内置目录落位", func(t *testing.T) {
		var got Rule
		require.NoError(t, gdb.Where("id = ?", imported.Id).Take(&got).Error)
		want, ok := byKey[b.Category]
		require.Truef(t, ok, "内置目录声明的类别 %q 在种子里不存在", b.Category)
		assert.Equal(t, want.Id, got.CategoryId,
			"内置规则没落位就等于把几十条已经归好类的规则倒回未分类")
	})

	t.Run("手写规则进未分类", func(t *testing.T) {
		var got Rule
		require.NoError(t, gdb.Where("id = ?", manual.Id).Take(&got).Error)
		assert.Equal(t, byKey[FallbackCategoryKey].Id, got.CategoryId)
	})

	t.Run("软删的规则也要绑,否则管理端复核时是个渲染不出来的状态", func(t *testing.T) {
		var got Rule
		require.NoError(t, gdb.Unscoped().Where("id = ?", deleted.Id).Take(&got).Error)
		assert.Equal(t, byKey[FallbackCategoryKey].Id, got.CategoryId)
	})

	t.Run("幂等:再跑一次不动任何行", func(t *testing.T) {
		// 先把兜底类型的阈值改掉,证明种子不会把管理员的改动覆盖回出厂值。
		require.NoError(t, gdb.Model(&Category{}).Where("is_fallback = ?", true).
			Update("threshold", 9).Error)
		require.NoError(t, ensureSeedCategories(ctx, gdb))
		again, err := migrateRuleCategory(ctx, gdb)
		require.NoError(t, err)
		assert.Zero(t, again, "已经绑好的规则不该被再动一次")

		var fb Category
		require.NoError(t, gdb.Where("is_fallback = ?", true).Take(&fb).Error)
		assert.Equal(t, 9, fb.Threshold, "种子是 OnConflict DoNothing:绝不覆盖管理员改过的值")

		var n int64
		require.NoError(t, gdb.Model(&Category{}).Count(&n).Error)
		assert.EqualValues(t, len(seedCategories), n, "重复补建不该造出重复的类型行")
	})
}

// TestMigrateRuleCategoryRefusesWithoutFallback 守的是"兜底类型缺失时不要乱绑"。
//
// 没有兜底类型时把规则绑到别的类型上是最坏的做法:那会把一批手写规则悄悄塞进
// 某个真实业务类型的计数桶,从此该类型的阈值判定全是错的。
func TestMigrateRuleCategoryRefusesWithoutFallback(t *testing.T) {
	gdb := newCategoryDB(t)
	require.NoError(t, gdb.Create(&Category{Key: CatJailbreak, Name: "破限"}).Error)
	require.NoError(t, gdb.Create(&Rule{
		Name: "手写", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "x", Mode: ModeShadow,
	}).Error)

	_, err := migrateRuleCategory(context.Background(), gdb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "兜底类型不存在")

	var got Rule
	require.NoError(t, gdb.Take(&got).Error)
	assert.Zero(t, got.CategoryId, "宁可留 0(运行期折进兜底)也不能乱绑到某个业务类型上")
}

// TestSeedCategoriesCoverBuiltinCatalog 双向钉住"种子键 ⊇ 内置目录用到的类别"。
//
// 内置目录里出现一个种子没有的类别时,那一批规则会在迁移里静默落进「未分类」——
// 与"这条规则从来没有被归类"完全同形,而且没有任何报错。
func TestSeedCategoriesCoverBuiltinCatalog(t *testing.T) {
	seeded := map[string]struct{}{}
	for _, s := range seedCategories {
		seeded[s.Key] = struct{}{}
	}
	used := map[string]struct{}{}
	for _, b := range builtinCatalog {
		used[b.Category] = struct{}{}
		assert.Containsf(t, seeded, b.Category,
			"内置规则 %q 声明的类别 %q 没有对应的种子类型,它会静默落进「未分类」", b.Key, b.Category)
	}
	// 反向:builtinCategories(管理端目录的展示分组)与种子必须是同一组键,
	// 否则管理端目录上的分组名与类型表上的名字会各说各话。
	for _, bc := range builtinCategories {
		assert.Containsf(t, seeded, bc.Id,
			"内置目录分组 %q 在违规类型种子里不存在,管理端会出现两套互不相干的类别名", bc.Id)
	}
	assert.NotEmpty(t, used)
}

// TestSeedPublicTextIsNotTheInternalText 守的是"公示文案是重写的,不是抄内部说明"。
//
// builtinCategories[].Desc 写的是判据("DAN 人格、开发者模式、要求关闭安全过滤"),
// 原样公示等于把绕过清单印给用户。
func TestSeedPublicTextIsNotTheInternalText(t *testing.T) {
	for _, s := range seedCategories {
		if s.Key == FallbackCategoryKey {
			continue // 兜底类型不公示,没有对外文案
		}
		require.NotEmptyf(t, s.Title, "种子类型 %q 出厂即公示,必须有公示标题", s.Key)
		require.NotEmptyf(t, s.Pub, "种子类型 %q 出厂即公示,必须有公示说明", s.Key)
		assert.NotEqualf(t, s.Desc, s.Pub,
			"种子类型 %q 的公示说明与内部说明逐字相同 —— 内部说明写的就是判据,公示它等于教人绕过", s.Key)
	}
}

// ─────────────────────── 归档:历史仍在 ───────────────────────

// TestArchiveCategoryKeepsHistoryAndReassignsRules 是删除语义的核心判据。
//
// 「删除类型」在这里是**归档**:类型行软删、规则改绑、历史记录一行不动。
// 级联删除历史记录会把申诉复核、退款争议、"这个账号为什么被封"的全部依据一次抹掉。
func TestArchiveCategoryKeepsHistoryAndReassignsRules(t *testing.T) {
	gdb := newCategoryDB(t)
	ctx := context.Background()
	require.NoError(t, ensureSeedCategories(ctx, gdb))

	var doomed, fallback Category
	require.NoError(t, gdb.Where("`key` = ?", CatJailbreak).Take(&doomed).Error)
	require.NoError(t, gdb.Where("is_fallback = ?", true).Take(&fallback).Error)

	live := &Rule{Name: "活着的规则", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "x",
		Mode: ModeShadow, CategoryId: doomed.Id}
	gone := &Rule{Name: "软删的规则", Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "y",
		Mode: ModeShadow, CategoryId: doomed.Id}
	require.NoError(t, gdb.Create(live).Error)
	require.NoError(t, gdb.Create(gone).Error)
	require.NoError(t, gdb.Delete(gone).Error)

	// 两条历史记录:类型三列都已冻结。
	for i, recNo := range []string{"vr_a", "vr_b"} {
		require.NoError(t, gdb.Create(&Record{
			RecNo: recNo, UserId: 42 + i, RuleId: live.Id,
			CategoryId: doomed.Id, CategoryName: doomed.Name, CategoryPublicTitle: doomed.PublicTitle,
			Phase: PhasePrompt, Status: RecordActive, CreatedAt: common.GetTimestamp(),
		}).Error)
	}
	// 类型计数行也在。
	_, _, err := bumpCategoryCounter(ctx, gdb, 42, Category{Id: doomed.Id, Enabled: true, WindowHours: 24, Threshold: 3}, 2)
	require.NoError(t, err)

	moved, err := archiveCategory(gdb, doomed.Id, fallback.Id)
	require.NoError(t, err)
	assert.EqualValues(t, 2, moved, "活着的与软删的规则都要改绑,后者在管理端复核时会被读到")

	t.Run("类型是软删,不是硬删", func(t *testing.T) {
		var n int64
		require.NoError(t, gdb.Model(&Category{}).Where("id = ?", doomed.Id).Count(&n).Error)
		assert.Zero(t, n, "默认查询里它应该已经消失")
		var archived Category
		require.NoError(t, gdb.Unscoped().Where("id = ?", doomed.Id).Take(&archived).Error)
		assert.True(t, archived.DeletedAt.Valid, "行本身必须留着:历史记录的 category_id 指向它")
	})

	t.Run("规则被改绑到接管类型,不是变成孤儿", func(t *testing.T) {
		var got Rule
		require.NoError(t, gdb.Where("id = ?", live.Id).Take(&got).Error)
		assert.Equal(t, fallback.Id, got.CategoryId)
		var goneRule Rule
		require.NoError(t, gdb.Unscoped().Where("id = ?", gone.Id).Take(&goneRule).Error)
		assert.Equal(t, fallback.Id, goneRule.CategoryId)
	})

	t.Run("历史违规记录一行不动,而且仍然解释得了自己算哪一类", func(t *testing.T) {
		var recs []Record
		require.NoError(t, gdb.Find(&recs).Error)
		require.Len(t, recs, 2, "归档绝不能级联删除历史记录 —— 那是证据")
		for _, r := range recs {
			assert.Equal(t, doomed.Id, r.CategoryId, "记录仍指向被归档的类型")
			assert.Equal(t, doomed.Name, r.CategoryName, "冻结的内部名让管理端复核仍然读得懂")
		}
	})

	t.Run("类型计数行保留", func(t *testing.T) {
		var n int64
		require.NoError(t, gdb.Model(&CategoryCounter{}).Where("category_id = ?", doomed.Id).Count(&n).Error)
		assert.EqualValues(t, 1, n, "那些数字是历史事实,而且类型随时可能被恢复")
	})

	t.Run("归档后同名 key 还能再建一个", func(t *testing.T) {
		// 唯一索引带上 deleted_at 的全部理由:没有它,"删了就再也建不回来"。
		err := gdb.Create(&Category{
			Key: CatJailbreak, Name: "破限(重建)", WindowHours: 24,
			CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
		}).Error
		assert.NoError(t, err)
	})
}

// TestCategoryKeyIsUniqueAmongLiveRows 是一条**回归**测试:唯一索引一旦写成
// (key, deleted_at),同一个 key 就可以有任意多个活着的行。
//
// 三家数据库的唯一索引都把 NULL 视为互不相等,于是 (spam, NULL) 与 (spam, NULL)
// 不冲突。这个缺陷的表现是每次重启都把整套种子再插一遍,而 CategoryByKey
// 会在一堆同名行里返回其中之一 —— 外部审核来源绑到哪一个纯看运气。
// 约束因此挂在非空的 ArchiveSeq 上(活行恒为 0)。
func TestCategoryKeyIsUniqueAmongLiveRows(t *testing.T) {
	gdb := newCategoryDB(t)
	now := common.GetTimestamp()
	mk := func(name string) *Category {
		return &Category{Key: "spam", Name: name, WindowHours: 24, CreatedAt: now, UpdatedAt: now}
	}
	first := mk("第一条")
	require.NoError(t, gdb.Create(first).Error)

	t.Run("同 key 的第二条活行必须被数据库拒绝", func(t *testing.T) {
		err := gdb.Create(mk("第二条")).Error
		require.Error(t, err, "唯一索引没有生效:同一个 key 会有多个活行,CategoryByKey 返回哪一个纯看运气")
	})

	t.Run("归档之后同 key 可以再建", func(t *testing.T) {
		_, err := archiveCategory(gdb, first.Id, 0)
		require.NoError(t, err)
		require.NoError(t, gdb.Create(mk("重建")).Error)
	})

	t.Run("归档行之间不会互撞(archive_seq 取自己的主键)", func(t *testing.T) {
		var second Category
		require.NoError(t, gdb.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: "spam"}).
			Take(&second).Error)
		_, err := archiveCategory(gdb, second.Id, 0)
		require.NoError(t, err, "两条同 key 的归档行必须能共存,否则「建了又归档」第二次就失败")

		var n int64
		require.NoError(t, gdb.Unscoped().Model(&Category{}).
			Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: "spam"}).Count(&n).Error)
		assert.EqualValues(t, 2, n)
	})
}

// TestArchiveCategoryNeverTouchesEvidence 从**源码层面**钉住"归档不碰证据表"。
//
// 上面那个用例断言的是当前实现的结果;这一条断言的是"下一个人不会顺手加一句
// 级联删除"。两者缺一不可:一个 `tx.Where(...).Delete(&Record{})` 加进去之后,
// 结果型断言会红,但改测试比改代码省事 —— 而这一条会指名道姓地说不行。
func TestArchiveCategoryNeverTouchesEvidence(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "category.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "archiveCategory" {
			fn = d
			return false
		}
		return true
	})
	require.NotNil(t, fn, "archiveCategory 不见了:归档语义没有落点了")

	forbidden := map[string]string{
		"Record":          "历史违规记录是证据:申诉复核、退款争议、封号复盘全部依赖它",
		"Payload":         "归档上下文是证据的正文,删了连「当时到底发了什么」都答不出来",
		"CategoryCounter": "类型计数是历史事实,而且类型随时可能被恢复",
		"Ban":             "封禁记录冻结了当时撞的是哪条线,删了就无法解释历史处置",
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Delete" {
			return true
		}
		for _, arg := range call.Args {
			unary, ok := arg.(*ast.UnaryExpr)
			if !ok {
				continue
			}
			lit, ok := unary.X.(*ast.CompositeLit)
			if !ok {
				continue
			}
			ident, ok := lit.Type.(*ast.Ident)
			if !ok {
				continue
			}
			why, bad := forbidden[ident.Name]
			assert.Falsef(t, bad,
				"archiveCategory 里出现了 Delete(&%s{}):归档是软删类型 + 改绑规则,"+
					"绝不删证据。%s", ident.Name, why)
		}
		return true
	})
}

// ─────────────────────── 用户端公示:内部说明必须隔离 ───────────────────────

// TestUserCategoryViewHidesInternalText 是公示隔离的核心判据。
//
// Category 上有两组文案:内部的 Name / Remark(写匹配细节、误杀场景)与对外的
// PublicTitle / PublicDesc。公示内部说明等于把绕过方法印在用户手册上 ——
// 这个用例给内部两列塞进独一无二的串,再断言序列化结果里一个字都没有。
func TestUserCategoryViewHidesInternalText(t *testing.T) {
	const secretName = "QYSECRETNAME_jailbreak_v3_high"
	const secretRemark = "QYSECRETREMARK 命中 DAN / 开发者模式 / 忽略上述指令 三组词表"
	cat := Category{
		Id: 5, Key: "jailbreak", Name: secretName, Remark: secretRemark,
		PublicTitle: "绕过安全策略", PublicDesc: "试图让模型跳出既定的安全与合规限制。",
		Published: true, Enabled: true, WindowHours: 24, Threshold: 3,
	}
	view := toUserCategoryView(cat, CategoryCounter{}, common.GetTimestamp())

	blob, err := common.Marshal(view)
	require.NoError(t, err)
	body := string(blob)
	assert.NotContains(t, body, secretName, "内部名常含代号,公示它等于泄漏规则库的组织方式")
	assert.NotContains(t, body, secretRemark, "内部说明写的就是判据,公示它等于教人绕过")
	assert.NotContains(t, body, cat.Key, "类型标识是外部审核来源的绑定键,不必给用户")
	assert.Contains(t, body, "绕过安全策略")

	// 正面清单:字段集就是隔离本身。新增字段默认不泄露,而复用 Category 加
	// `json:"-"` 是负面清单,下一次加内部列时它会默认泄露出去。
	allowed := map[string]struct{}{
		"id": {}, "title": {}, "description": {},
		"threshold": {}, "window_hours": {}, "hit_count": {}, "remaining": {},
	}
	rt := reflect.TypeOf(userCategoryView{})
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		assert.Containsf(t, allowed, tag,
			"userCategoryView 多了一个字段 %q。它是公示白名单:每加一列都要先回答"+
				"「这一列会不会告诉用户怎么绕过」", tag)
	}
}

// TestUserCategoryViewNumbers 固化公示里那几个数字的口径。
//
// 「还剩几次」是用户唯一会认真读的数字,给错的数比不给更糟。
func TestUserCategoryViewNumbers(t *testing.T) {
	now := common.GetTimestamp()
	base := Category{Id: 5, PublicTitle: "T", Enabled: true, WindowHours: 24, Threshold: 3}

	cases := []struct {
		name          string
		cat           Category
		ct            CategoryCounter
		wantThreshold int
		wantHit       int
		wantRemaining int
		wantWindow    int
	}{
		{"没有计数行", base, CategoryCounter{}, 3, 0, 3, 24},
		{"窗口内有计数", base,
			CategoryCounter{CategoryId: 5, HitCount: 2, WindowStart: now - 60}, 3, 2, 1, 24},
		{"窗口已滚过 → 旧计数不展示", base,
			CategoryCounter{CategoryId: 5, HitCount: 2, WindowStart: now - 25*3600}, 3, 0, 3, 24},
		{"已越线 → 剩余夹到 0,不给负数", base,
			CategoryCounter{CategoryId: 5, HitCount: 9, WindowStart: now - 60}, 3, 9, 0, 24},
		{"类型停用 → 阈值按 0 展示(线当下并不生效)",
			Category{Id: 5, PublicTitle: "T", Enabled: false, WindowHours: 24, Threshold: 3},
			CategoryCounter{CategoryId: 5, HitCount: 2, WindowStart: now - 60}, 0, 2, 0, 24},
		{"阈值 0 = 这一类不单独触发",
			Category{Id: 5, PublicTitle: "T", Enabled: true, WindowHours: 24, Threshold: 0},
			CategoryCounter{CategoryId: 5, HitCount: 2, WindowStart: now - 60}, 0, 2, 0, 24},
		{"窗口列为 0(历史行)→ 按 24 小时读",
			Category{Id: 5, PublicTitle: "T", Enabled: true, WindowHours: 0, Threshold: 3},
			CategoryCounter{CategoryId: 5, HitCount: 1, WindowStart: now - 60}, 3, 1, 2, 24},
		{"计数行属于别的类型 → 不能串台", base,
			CategoryCounter{CategoryId: 6, HitCount: 9, WindowStart: now - 60}, 3, 0, 3, 24},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toUserCategoryView(tc.cat, tc.ct, now)
			assert.Equal(t, tc.wantThreshold, got.Threshold)
			assert.Equal(t, tc.wantHit, got.HitCount)
			assert.Equal(t, tc.wantRemaining, got.Remaining)
			assert.Equal(t, tc.wantWindow, got.WindowHours)
		})
	}
}

// TestUserRecordViewUsesFrozenPublicTitle 守的是用户端记录列表里的类型名。
//
// 必须用冻结的**公示标题**,不是 CategoryName(内部名)、也不是现查类型表 ——
// 类型被归档或改名之后,这一条记录仍要显示当时那个名字。
func TestUserRecordViewUsesFrozenPublicTitle(t *testing.T) {
	rec := &Record{
		Id: 1, PublicReason: "内容违反使用策略",
		CategoryId: 5, CategoryName: "QYSECRET_内部代号", CategoryPublicTitle: "绕过安全策略",
	}
	view := toUserView(rec)
	assert.Equal(t, "绕过安全策略", view.Category)

	blob, err := common.Marshal(view)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), "QYSECRET_内部代号",
		"用户端记录列表绝不能出现类型的内部名")
}
