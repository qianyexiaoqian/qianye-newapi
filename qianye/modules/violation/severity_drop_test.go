package violation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// severity_drop_test.go —— `qy_violation_rule.severity` 的下线。
//
// # 这一列为什么能删
//
// severity(1=低 2=中 3=高)从头到尾只写不读:不参与判定、不参与排序、不进导出、
// 不进告警。上一轮撤掉了结构体字段、表单与 API 面,但保留了数据库列,理由是
// "删列不可逆"。项目方确认本项目尚未上线生产,那条理由不再成立。
//
// # 这个文件挡的三件事
//
//  1. **迁移真的删掉了列,而且是幂等的。** 这一条不是形式主义:
//     GORM 的 Migrator().DropColumn 在 glebarez/sqlite 上**会静默失败** ——
//     驱动不发 DROP COLUMN,而是拿正则改写建表 DDL 再重建表,正则是
//     `(`|'|"| |\[)severity(`|'|"| |\]) .*?,`:列名之后要先吃掉一个
//     "定界符"(反引号/引号/空格/右方括号),再吃掉一个**字面空格**。
//     所以真正的分水岭是**列名与类型之间怎么隔开的**,与列的位置无关:
//
//     ── 实测(与下面三个用例一一对应)──
//     ", severity integer …"   单空格、不带引号 → 匹配不上 → **静默失败**
//     ", severity  integer …"  双空格           → 匹配得上 → GORM 真删了
//     ", `severity` integer …" 反引号包裹       → 匹配得上 → GORM 真删了
//
//     而 ADD COLUMN 追加出来的恰恰是第一种(单空格、不带引号),
//     也就是现网那张表的形状 —— GORM 偏偏在唯一需要它生效的那一格上失败。
//     一条"报告成功却什么都没做"的迁移比不写更糟,所以判据必须是
//     **迁移之后 HasColumn 为 false**,而不是"迁移没报错"。
//  2. **结构体上不会有人再把这个字段加回来。**
//  3. **删列不动内置规则指纹。** 指纹是 sha256(pattern),从来不含 severity。
//     若有人日后把更多列并进指纹,现网每一条已导入的内置规则都会在下一次打开
//     管理端时集体变成 "modified" —— 一屏纯粹由重构制造的假告警。

// newSeverityMigrationDB 建一张**带 severity 列**的规则表,复现现网的形状。
//
// addSQL 决定这一列在建表 DDL 里长什么样。列名与类型之间的定界方式(单空格 /
// 双空格 / 反引号)正是 glebarez 那条正则匹不匹配得上的分水岭,所以由调用方
// 逐例给出;传多条时按顺序执行,用来把 severity 挤到非末列的位置。
func newSeverityMigrationDB(t *testing.T, addSQL ...string) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 内存库按连接隔离
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, gdb.AutoMigrate(&Rule{}))
	for _, s := range addSQL {
		if s == "" {
			continue
		}
		require.NoError(t, gdb.Exec(s).Error)
	}
	return gdb
}

// TestDropLegacySeverityColumnIsIdempotent 是迁移的主判据。
//
// 每一例都跑**两遍**迁移:第一遍的返回值说明它做了什么,第二遍必须是 no-op。
// 幂等不是理论要求 —— 每个节点启动时各跑一次,滚动升级期间会有多个节点在几秒内
// 依次跑到这里,而第二次 DROP COLUMN 在三家数据库上都是报错而不是静默成功。
func TestDropLegacySeverityColumnIsIdempotent(t *testing.T) {
	// seenShapes 收集每一例造出来的建表 DDL,跑完之后互相比对。
	// 它挡的是一个真发生过的退化:上一版把"后面还跟着别的列"那一例写成
	// 只改 DEFAULT 值(1 → 2),而 SQLite 的 ADD COLUMN 只能往末尾追加 ——
	// 两例的 DDL 逐字符相同(severity 都是第 35 列、都是末列),
	// 用例名声称覆盖了另一种排布,实际是同一个分支跑了两遍。
	seenShapes := map[string]string{}

	cases := []struct {
		name        string
		addSQL      []string
		wantDropped bool
	}{
		{
			// 现网的真实形状:AutoMigrate 建完表之后 severity 由 ADD COLUMN 追加,
			// 落在 deleted_at 之后、PRIMARY KEY 之前,列名与类型之间只有一个空格、
			// 不带引号。**这一例就是 Migrator().DropColumn 静默失败的那一例**,
			// 把它写在第一行。
			name:        "现网形状:单空格不带引号(GORM 会在这一格静默失败)",
			addSQL:      []string{"ALTER TABLE qy_violation_rule ADD COLUMN severity integer NOT NULL DEFAULT 1"},
			wantDropped: true,
		},
		{
			// 同为单空格,但 severity 后面还跟着别的列(升级过程中又加过列)。
			// SQLite 的 ADD COLUMN 只能往末尾追加,所以这个形状必须靠**再加一列**
			// 把 severity 挤到中间造出来 —— 只改 DEFAULT 值是造不出来的,
			// 那样两例的建表 DDL 逐字符相同,等于同一个分支跑两遍。
			name: "单空格,且后面还跟着别的列",
			addSQL: []string{
				"ALTER TABLE qy_violation_rule ADD COLUMN severity integer NOT NULL DEFAULT 2",
				"ALTER TABLE qy_violation_rule ADD COLUMN legacy_tail text",
			},
			wantDropped: true,
		},
		{
			// 双空格:glebarez 的正则在这一格**匹配得上**,GORM 自己也能删掉。
			// 覆盖它是为了证明我们的标准 DDL 在两侧都成立,而不是只在 GORM 失效的
			// 那一格上碰巧可用 —— 手工建表或换个驱动重建出来的就可能是这个形状。
			name:        "双空格(glebarez 正则匹配得上的形状)",
			addSQL:      []string{"ALTER TABLE qy_violation_rule ADD COLUMN severity  integer NOT NULL DEFAULT 1"},
			wantDropped: true,
		},
		{
			// 反引号包裹:同上,正则匹配得上。DBA 手抄建表语句时最常见的写法。
			name:        "反引号包裹",
			addSQL:      []string{"ALTER TABLE qy_violation_rule ADD COLUMN `severity` integer NOT NULL DEFAULT 1"},
			wantDropped: true,
		},
		{
			// 全新建的库:结构体上没有这一列,AutoMigrate 根本不会建它。
			// 迁移必须认出"本来就没有"并直接返回,而不是发一条注定报错的 DDL。
			name:        "全新建的库,从来没有过这一列",
			addSQL:      nil,
			wantDropped: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newSeverityMigrationDB(t, tc.addSQL...)
			if len(tc.addSQL) > 0 {
				require.True(t, gdb.Migrator().HasColumn(&Rule{}, legacySeverityColumn),
					"前置条件没成立:这一例本该先有 severity 列")
			}
			// 造出来的形状必须真的各不相同,否则这张表就是同一个分支跑了几遍。
			// 断言的是建表 DDL 本身:HasColumn 看不出"列名与类型之间隔了几个空格",
			// 而那正是 glebarez 正则的分水岭。
			var ddl string
			require.NoError(t, gdb.Raw(
				"SELECT sql FROM sqlite_master WHERE type='table' AND name = ?",
				Rule{}.TableName()).Scan(&ddl).Error)
			require.NotEmpty(t, ddl)
			seenShapes[tc.name] = ddl
			neighbour := gdb.Migrator().HasColumn(&Rule{}, "legacy_tail")

			dropped, err := dropLegacySeverityColumn(context.Background(), gdb)
			require.NoError(t, err)
			assert.Equal(t, tc.wantDropped, dropped)

			// 全部判据里最重要的一条:列**真的**没了。
			// 只断言 err == nil 会让 Migrator().DropColumn 那种静默失败一路绿灯。
			assert.False(t, gdb.Migrator().HasColumn(&Rule{}, legacySeverityColumn),
				"迁移跑完 severity 列还在。注意 GORM 的 DropColumn 在 sqlite 驱动上"+
					"会返回 nil 却什么都没做,所以判据必须是这一行而不是上面的 err")

			// 邻座的列不能被顺手删掉。SQLite 上 DROP COLUMN 是重建表,
			// 少搬一列就是悄悄丢掉一整列数据,而这一点 HasColumn(severity) 看不出来。
			if neighbour {
				assert.True(t, gdb.Migrator().HasColumn(&Rule{}, "legacy_tail"),
					"删 severity 把它后面那一列也带走了")
			}

			// 第二遍:必须是 no-op,而不是报"列不存在"。
			again, err := dropLegacySeverityColumn(context.Background(), gdb)
			require.NoError(t, err, "重复执行不得报错 —— 每个节点启动时都会跑一次")
			assert.False(t, again, "第二遍应当认出列已不在并直接返回")
			assert.False(t, gdb.Migrator().HasColumn(&Rule{}, legacySeverityColumn))
		})
	}

	// 每一例造出来的形状必须两两不同。名字不同而 DDL 相同 = 覆盖是假的。
	for a, ddlA := range seenShapes {
		for b, ddlB := range seenShapes {
			if a >= b {
				continue
			}
			assert.NotEqualf(t, ddlA, ddlB,
				"用例 %q 与 %q 造出来的建表 DDL 逐字符相同,等于同一个分支跑了两遍。"+
					"SQLite 的 ADD COLUMN 只能往末尾追加,只改 DEFAULT 值造不出不同的排布 —— "+
					"要么再加一列把 severity 挤到中间,要么改列名与类型之间的定界方式", a, b)
		}
	}
}

// TestDropLegacySeverityColumnToleratesConcurrentDrop 覆盖多节点同时启动。
//
// 先查后删之间有一个 TOCTOU 窗口:滚动升级时多台机器可能在同一秒里都读到
// "列还在",于是都发一条 DROP COLUMN。先到的成功,后到的报"列不存在"。
// 这不是故障 —— 结果与预期完全一致,列没了 —— 但如果照常走错误分支,
// 当天每台后到的机器都会打一条"迁移失败"告警,而运维分不清它和真正的失败
// (比如权限不足)。真正的失败必须继续报出来,所以不能一律吞掉错误。
//
// 用 GORM 的 Raw 回调在"HasColumn 已经查过、ALTER 还没发出去"的那一刻
// 从另一条连接把列删掉,精确复现这个窗口 —— 不用 sleep,也不靠并发碰运气。
func TestDropLegacySeverityColumnToleratesConcurrentDrop(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "toctou.db") + "?_pragma=busy_timeout(5000)"

	// 两个句柄都要显式关掉:Windows 上 TempDir 清理会因为文件还被占用而失败。
	openNode := func() *gorm.DB {
		t.Helper()
		g, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := g.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return g
	}

	gdb := openNode()
	require.NoError(t, gdb.AutoMigrate(&Rule{}))
	require.NoError(t, gdb.Exec(
		"ALTER TABLE qy_violation_rule ADD COLUMN severity integer NOT NULL DEFAULT 1").Error)

	// 另一个"节点"。
	other := openNode()

	fired := false
	require.NoError(t, gdb.Callback().Raw().Before("gorm:raw").
		Register("qy_test_concurrent_severity_drop", func(tx *gorm.DB) {
			if fired || !strings.Contains(tx.Statement.SQL.String(), "DROP COLUMN") {
				return
			}
			fired = true // 只抢这一次,第二遍迁移要走正常的 HasColumn 闸
			require.NoError(t, other.Exec(
				"ALTER TABLE qy_violation_rule DROP COLUMN severity").Error)
		}))

	dropped, err := dropLegacySeverityColumn(context.Background(), gdb)
	require.True(t, fired, "回调没打中:这一例没有真的复现 TOCTOU 窗口")
	require.NoError(t, err,
		"另一个节点抢先删掉了列,这是成功不是失败 —— 报错会让滚动升级当天"+
			"每台后到的机器都打一条假的迁移失败告警")
	assert.False(t, dropped, "列不是我们删的,不该报告 dropped")
	assert.False(t, gdb.Migrator().HasColumn(&Rule{}, legacySeverityColumn),
		"最终状态仍必须是列已不在")
}

// TestDropLegacySeverityColumnStillReportsRealFailure 是上一条的反面。
//
// "列已经不在" 才收敛成 no-op;列还在而 DDL 失败(权限不足、表被锁、
// 磁盘满)必须**照常报错**,否则真正删不掉时没有任何人会知道。
func TestDropLegacySeverityColumnStillReportsRealFailure(t *testing.T) {
	gdb := newSeverityMigrationDB(t,
		"ALTER TABLE qy_violation_rule ADD COLUMN severity integer NOT NULL DEFAULT 1")

	// 让 DDL 失败,但列原封不动:改写成一条注定语法错误的语句。
	require.NoError(t, gdb.Callback().Raw().Before("gorm:raw").
		Register("qy_test_break_ddl", func(tx *gorm.DB) {
			if strings.Contains(tx.Statement.SQL.String(), "DROP COLUMN") {
				tx.Statement.SQL.Reset()
				tx.Statement.SQL.WriteString("ALTER TABLE qy_violation_rule DROP COLUMN")
			}
		}))

	dropped, err := dropLegacySeverityColumn(context.Background(), gdb)
	require.Error(t, err,
		"列还在却删失败,必须报错。吞掉它等于把「权限不足」伪装成「已经删过了」")
	assert.False(t, dropped)
	assert.True(t, gdb.Migrator().HasColumn(&Rule{}, legacySeverityColumn),
		"前置条件:这一例本该删不掉,列仍在")
}

// TestDropLegacySeverityColumnKeepsRulesIntact 固化"删的是列,不是数据"。
//
// DROP COLUMN 在 MySQL / PostgreSQL 上是原地改表,在 SQLite 上是重建表并拷贝数据 ——
// 后者存在把行拷丢、或把某一列的取值错位的可能。规则行决定谁被扣钱、谁被封号,
// 所以必须当场验一遍:行数不变,而且每一列的取值原样还在。
func TestDropLegacySeverityColumnKeepsRulesIntact(t *testing.T) {
	gdb := newSeverityMigrationDB(t,
		"ALTER TABLE qy_violation_rule ADD COLUMN severity integer NOT NULL DEFAULT 1")

	seed := []Rule{
		{
			Name: "真实执行的高权重规则", Enabled: true, Mode: ModeEnforce,
			Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "危险",
			Action: ActionBlock, FeeMode: FeeNone, GroupScopeMode: GroupScopeInclude,
			CountWeight: 3, Priority: 10, CategoryId: 7,
			CreatedAt: 1000, UpdatedAt: 1000,
		},
		{
			Name: "影子规则", Enabled: false, Mode: ModeShadow,
			Phase: PhaseUpstreamErr, MatchType: MatchRegex, Pattern: "(?i)refus(al|ed)",
			Action: ActionRecord, FeeMode: FeeNone, GroupScopeMode: GroupScopeExclude,
			CountWeight: 0, Priority: 200, CategoryId: 9,
			Source: SourceBuiltin, BuiltinKey: "probe_key", BuiltinVersion: 2,
			BuiltinFingerprint: patternFingerprint("(?i)refus(al|ed)"),
			CreatedAt:          2000, UpdatedAt: 2000,
		},
	}
	for i := range seed {
		require.NoError(t, gdb.Create(&seed[i]).Error)
	}

	_, err := dropLegacySeverityColumn(context.Background(), gdb)
	require.NoError(t, err)

	var after []Rule
	require.NoError(t, gdb.Order("id asc").Find(&after).Error)
	require.Len(t, after, len(seed), "删列不该丢行")

	for i := range seed {
		got := after[i]
		assert.Equal(t, seed[i].Name, got.Name)
		assert.Equal(t, seed[i].Enabled, got.Enabled, "启停状态错位 = 一条规则被悄悄开/关")
		assert.Equal(t, seed[i].Mode, got.Mode, "模式错位 = 影子规则开始真的扣钱")
		assert.Equal(t, seed[i].Pattern, got.Pattern)
		assert.Equal(t, seed[i].MatchType, got.MatchType)
		assert.Equal(t, seed[i].Action, got.Action)
		assert.Equal(t, seed[i].GroupScopeMode, got.GroupScopeMode)
		assert.Equal(t, seed[i].CountWeight, got.CountWeight, "权重错位 = 到线次数变了")
		assert.Equal(t, seed[i].Priority, got.Priority)
		assert.Equal(t, seed[i].CategoryId, got.CategoryId, "类型错位 = 计数进了别的桶")
		assert.Equal(t, seed[i].BuiltinFingerprint, got.BuiltinFingerprint)
		assert.Equal(t, seed[i].BuiltinVersion, got.BuiltinVersion)
	}

	// 删完之后写入面照常:新建与更新都不该再碰那一列。
	fresh := Rule{
		Name: "删列之后新建的规则", Enabled: true, Mode: ModeShadow,
		Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "新词",
		Action: ActionRecord, FeeMode: FeeNone, GroupScopeMode: GroupScopeInclude,
		CountWeight: 1, CreatedAt: 3000, UpdatedAt: 3000,
	}
	require.NoError(t, gdb.Create(&fresh).Error, "删列之后新建规则必须照常成功")
	fresh.CountWeight = 5
	require.NoError(t, gdb.Save(&fresh).Error, "删列之后更新规则必须照常成功")
}

// TestBuiltinSeedStillImportsAfterSeverityDrop 证明内置种子在删列之后仍能落库。
//
// 内置目录里的 Severity 已随结构体一并撤掉。若还有哪条种子在给一个不存在的列赋值,
// 或者 toRule 铺出来的行与删列后的表对不上,整条 INSERT 会被数据库拒绝 ——
// 表现是"一键导入内置规则包"点下去报错,而这正是内置包这个功能的全部内容。
func TestBuiltinSeedStillImportsAfterSeverityDrop(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")

	gdb := newSeverityMigrationDB(t,
		"ALTER TABLE qy_violation_rule ADD COLUMN severity integer NOT NULL DEFAULT 1")
	dropped, err := dropLegacySeverityColumn(context.Background(), gdb)
	require.NoError(t, err)
	require.True(t, dropped)

	require.NotEmpty(t, builtinCatalog)
	for _, b := range builtinCatalog {
		row := b.toRule(1000, 7)
		require.NoErrorf(t, ValidateRule(row), "内置条目 %s 过不了写入校验", b.Key)
		require.NoErrorf(t, gdb.Create(row).Error,
			"内置条目 %s 在删掉 severity 列之后写不进库了", b.Key)
	}

	var n int64
	require.NoError(t, gdb.Model(&Rule{}).Count(&n).Error)
	assert.Equal(t, int64(len(builtinCatalog)), n, "内置目录必须一条不少地落库")
}

// TestBuiltinFingerprintIsPatternOnly 是"删列不产生假告警"的判据。
//
// # 为什么这条测试必须存在
//
// 指纹回答的唯一问题是「这条内置规则运营改过没有」:不等就是改过,而改过的规则
// **一律不覆盖**,并在管理端单独列成一屏「已被修改」。所以指纹的覆盖面一旦变宽,
// 现网每一条已导入的内置规则都会在下一次打开管理端时集体翻成 modified ——
// 它们存的是按旧算法算出来的值。那是一屏纯粹由重构制造的假告警,而运营分不清
// 哪些是真的被人改过。applyUpgrade 的注释里已经为 StatusScope 写过同一段推理。
//
// severity 从来不在指纹里,所以这一轮删列**不需要**给指纹加版本号、也不需要
// 重新落库任何存量内置规则:指纹的输入一个字节都没变。这条测试把那个前提钉住。
// # 期望值必须独立算出,不能回头调 patternFingerprint
//
// 这一点是这条测试的全部力量所在,而且是当场被变异验证抓出来的:最初的写法用
// `BuiltinFingerprint: patternFingerprint(b.Pattern)` 造"存量行",于是把指纹算法
// 改宽之后,存量行的指纹跟着一起变宽,两边一起错、一起绿 —— 那正是它要挡的
// 那次事故的形状。所以下面的存量指纹由测试自己用 crypto/sha256 算,
// 与被测函数没有任何共享推导。
func TestBuiltinFingerprintIsPatternOnly(t *testing.T) {
	// legacyFingerprint 复刻**升级之前落库的那个值**:sha256(pattern) 的全长十六进制。
	// 它刻意不调用 patternFingerprint —— 那样就成了拿实现验实现。
	legacyFingerprint := func(pattern string) string {
		sum := sha256.Sum256([]byte(pattern))
		return hex.EncodeToString(sum[:])
	}

	// ① 先钉死"指纹到底是什么":一个手工核对过的常量。
	// 有了它,连 legacyFingerprint 自己写错都会被抓住。
	const probePattern = "(?i)ignore previous instructions"
	assert.Equal(t,
		"b511e4181611da4a981bfd018b8276c0eb8cf485e00123897bc3dd23078595c3",
		patternFingerprint(probePattern),
		"指纹必须是 sha256(pattern) 的全长十六进制。这个常量是独立算出来的,"+
			"它一旦对不上,说明指纹的输入或算法变了 —— 而那意味着现网每一条已导入的"+
			"内置规则都会在下一次打开管理端时集体翻成 modified")
	assert.Len(t, patternFingerprint(probePattern), 64,
		"必须放得进 builtin_fingerprint(varchar(64))")
	assert.NotEqual(t, patternFingerprint(probePattern),
		patternFingerprint("(?i)ignore prevoius instructions"),
		"pattern 改一个字符指纹必须变,否则改过的规则会被当成没改过、直接覆盖")

	// ② 一条**存量**内置规则(指纹是当初按 sha256(pattern) 存下来的)在这一轮之后
	//    仍然判为 up_to_date,而不是 modified。这就是"运营不会看到一屏假告警"。
	//    逐条过整个目录:假告警是按条计的,漏掉哪一条就是漏掉那一屏里的一行。
	require.NotEmpty(t, builtinCatalog)
	for _, b := range builtinCatalog {
		legacyRow := &Rule{
			Pattern:            b.Pattern,
			StatusScope:        b.StatusScope,
			BuiltinKey:         b.Key,
			BuiltinVersion:     b.Version,
			BuiltinFingerprint: legacyFingerprint(b.Pattern), // 升级前落库的那个值
		}
		assert.Equalf(t, BuiltinUpToDate, upgradeState(legacyRow, b),
			"内置条目 %s:删掉 severity 之后,一条没被人动过的存量规则仍必须是 up_to_date。"+
				"变成 modified 就意味着升级当天运营会看到一屏假的「内置规则已被修改」,"+
				"而他分不清哪些是真的被人改过", b.Key)
	}

	// ③ 运营真改过 pattern 的那一条,仍然必须被认出来 —— 这是指纹存在的理由本身,
	//    上面那批断言不能是靠"指纹永远相等"换来的。
	b := builtinCatalog[0]
	edited := &Rule{
		Pattern:            b.Pattern + "|额外加的一段",
		StatusScope:        b.StatusScope,
		BuiltinKey:         b.Key,
		BuiltinVersion:     b.Version,
		BuiltinFingerprint: legacyFingerprint(b.Pattern),
	}
	assert.Equal(t, BuiltinModified, upgradeState(edited, b),
		"运营改窄/改宽过的规则必须仍判为 modified,否则升级会把他的判断覆盖掉")
}

// TestRuleStructHasNoSeverityField 挡住"把字段加回来"。
//
// 加回来的直接后果:AutoMigrate 会把列重新建出来,而上面那条迁移下一次启动
// 又会把它删掉 —— 两边每次重启互相拆台,在 MySQL 上就是每次重启两条 DDL。
func TestRuleStructHasNoSeverityField(t *testing.T) {
	_, mapped := reflect.TypeOf(Rule{}).FieldByName("Severity")
	assert.False(t, mapped,
		"Rule 上又出现了 Severity 字段。它没有任何读点,而列已经删了 —— "+
			"加回字段会让 AutoMigrate 重建列、迁移再删掉它,每次重启互相拆台。"+
			"真要给「严重程度」一个用途,那是一次新功能,读点应该是违规类型的阈值")

	// 内置目录侧同理:种子里再出现 Severity,ValidateRule 之前就编译不过,
	// 但目录结构体是可以被加字段的,所以这里也钉一遍。
	_, seeded := reflect.TypeOf(builtinRule{}).FieldByName("Severity")
	assert.False(t, seeded, "内置目录条目上不该再有 Severity")
}

/*
── 变异验证(逐条改坏生产代码再跑本文件,改完即还原;括号里是实测结果)──

基线:本文件全绿,violation 包全绿。

	M1  migrate.go:把 Exec("ALTER TABLE ? DROP COLUMN ?") 换回
	                gdb.Migrator().DropColumn(&Rule{}, legacySeverityColumn)
	                → 三条测试同时红:
	                  · IsIdempotent 红在两个**单空格**用例("迁移跑完 severity 列还在",
	                    DropColumn 返回 nil 却没删掉);双空格与反引号两例仍绿 ——
	                    那两个形状 glebarez 的正则匹配得上,它确实能删。
	                    这一格正好把"分水岭是定界方式而不是列的位置"实证了一遍。
	                  · ToleratesConcurrentDrop 红在"回调没打中"(DropColumn 不发
	                    DROP COLUMN,而是重建表,注入点根本不触发)。
	                  · StillReportsRealFailure 红在"An error is expected but got nil"。
	                  这正是本文件存在的理由:换成看起来更"正统"的 GORM API,
	                  迁移会变成一条报告成功的空操作。
	M2  migrate.go:删掉 `if !m.HasColumn(...) { return false, nil }` 这道闸
	                → 三例的第二遍全红(SQLite 报 no such column: "`severity`"),
	                  也就是幂等性没了 —— 第二个节点启动就会打一条迁移失败告警。
	M4  migrate.go:把 legacySeverityColumn 改成 "severity_x"
	                → TestDropLegacySeverityColumnIsIdempotent 前两例红
	                  (dropped 返回 false,列还在);第三例本就 wantDropped=false,仍绿。
	M5  builtin.go:把指纹的输入改宽 —— toRule 传 b.Pattern+b.StatusScope,
	                upgradeState 同步改成 patternFingerprint(row.Pattern+row.StatusScope)
	                → TestBuiltinFingerprintIsPatternOnly 的第 ② 批红,
	                  逐条点名(upstream.cybersecurity_refusal 等带 StatusScope 的条目
	                  全部翻成 modified),即"一屏假告警"被当场抓住。

	                  **这一格的第一版测试没抓到,值得写下来。** 当时用
	                  `BuiltinFingerprint: patternFingerprint(b.Pattern)` 造"存量行",
	                  于是指纹算法一改宽,存量行的指纹跟着一起变宽 —— 两边一起错、
	                  一起绿,M5 全程 PASS。改成由测试自己用 crypto/sha256 算出存量值、
	                  再加一个手工核对过的 sha256 常量之后才红。
	                  拿实现验实现的测试在这里是负资产:它会让人相信"删列不会产生
	                  假告警"这件事已经被守住了。
	M7  migrate.go:去掉出错之后的 TOCTOU 重查(把那道 if 改成恒不成立)
	                → TestDropLegacySeverityColumnToleratesConcurrentDrop 红在
	                  "Received unexpected error: no such column: `severity`" ——
	                  也就是滚动升级当天,每台后到的机器都会打一条假的迁移失败告警。
	M8  migrate.go:把重查改成恒成立(出错一律当成"别人删掉了",全部吞掉)
	                → TestDropLegacySeverityColumnStillReportsRealFailure 红在
	                  "An error is expected but got nil"。这一格挡的是过度收敛:
	                  权限不足 / 表被锁 / 磁盘满被伪装成"已经删过了",
	                  真的删不掉时没有任何人会知道。
	                  M7 与 M8 一起把这道闸的两侧都钉住了。
	M9  severity_drop_test.go:把"单空格,且后面还跟着别的列"那一例改回
	                只改 DEFAULT 值(1 → 2)的旧写法
	                → IsIdempotent 红在 seenShapes 的两两比对,并把两份逐字符相同的
	                  建表 DDL 打了出来。这一格挡的是本轮真发生过的退化:
	                  SQLite 的 ADD COLUMN 只能往末尾追加,只改 DEFAULT 值造不出
	                  另一种排布,用例名声称的覆盖是假的。
	M6  model.go:把 `Severity int` 字段加回 Rule
	                → TestRuleStructHasNoSeverityField 红;
	                  且 TestDropLegacySeverityColumnIsIdempotent **三例全红** ——
	                  AutoMigrate 把列建了回来,连"全新建的库"那一例的
	                  wantDropped=false 都不再成立。两条独立判据同时抓到。

	── 没做变异、以及为什么 ──

	migrate.go:`if !m.HasTable(&Rule{}) { return false, nil }` 这道闸,
	本文件的三例都建好了表,改坏它不会变红。它兜的是"扩展库还没建表"那条启动
	路径,而那条路径的语义归 qianye/db/migrate.go 管。同理
	runLegacySeverityColumnDrop 里的 ShouldAutoMigrate 判断也不在这里测 ——
	auto_migrate 的语义只该有一个判据,在这里再测一遍就是把它抄成两份。
*/
