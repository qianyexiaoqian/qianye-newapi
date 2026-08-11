package violation

import (
	"reflect"
	"regexp"
	"strconv"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// builtin_column_fit_test.go —— 内置目录 × 列宽 的结构性守卫。
//
// # 它挡的是哪一次事故
//
// 2026-08 实弹测试:九条攻击载荷只落了一条记录。追下去发现本轮新补的六条规则
// 全部没有进库 —— 它们把大段说理写进了 builtinRule 的 Guards / FalsePositive /
// Origin,而 toRule() 会把这三段拼成 Rule.Remark,拼出来最长 1183 字,
// 撞上 `Remark string gorm:"type:varchar(512)"`,MySQL 逐条报
// `Error 1406 (22001): Data too long for column 'remark'`,每一条 INSERT 都失败。
//
// 旧的十五条都在限内,所以只有新补的六条进不去 —— 恰好是防那份载荷的六条。
//
// 为什么当时全绿:单体测试测的是**代码里的目录**(builtinCatalog),不经过数据库;
// 生产走的是**库里的行**。测试与生产之间断了一节,而断掉的那一节恰好是唯一会
// 报错的一节。这是本仓反复出现的「断链」形状里最贵的一种:测试绿、功能零。
//
// # 为什么从 gorm tag 解析列长,而不是手写一张字段→长度的表
//
// 手写表会在有人改列宽时静默失效 —— 表里还写着 512,列已经是 256,守卫照旧全绿。
// 那正是本缺陷的同一种形状(两份事实各自漂移),用它来防它自己是没有意义的。
// 解析 tag 意味着列宽只有一个来源:结构体本身。
//
// 生产代码里确实还有一张手写表(rules.go 的 ruleVarcharLimits),因为写入前要报出
// 「哪一格填多了、上限是多少」,而 gorm tag 只有数据库看得懂。那张表由
// TestRuleVarcharLimitsMatchColumnTags 逐字段钉在 tag 上,两侧不允许单独变。
//
// # 计长口径:rune(Unicode 码点),不是 byte
//
// 三种目标数据库对 `varchar(N)` 的语义:
//
//   - MySQL(utf8mb4):N 个**字符**。qianye/db/db.go 在 DSN 里强制
//     charset=utf8mb4,所以连接层不会把多字节字符拆成多个"字符"。
//   - PostgreSQL:N 个**字符**(码点)。
//   - SQLite:完全不校验长度(varchar 只是 TEXT affinity)。
//
// 三者的**唯一约束边界**因此是"字符数",而 Go 的 rune 数(码点数)正是 MySQL
// utf8mb4 与 PostgreSQL 所说的字符数。
//
// 事故本身就是这个口径的实证:失败的六条 rune 数是 1183 / 1022 / 1013 / 950 /
// 769 / 524,全部 > 512;而同一批里 jailbreak.sandbox_exemption_zh(437 rune /
// **1113 byte**)、pressure.instruction_override_zh_loose(485 / 1099)、
// distill.request_rate(242 / 532)这些**字节数早就超了 512** 的行一条都没报错。
// 报错的边界与 rune 数完全吻合,与 byte 数完全不吻合。
//
// 那为什么不用"更保守"的 byte 数?因为它不是更保守,它是**另一条不成立的约束**:
// 按 byte 判,上面三条健康的中文规则今天就会红。一个在正确数据上误报的守卫,
// 下一个人只会把它调宽或删掉 —— 那正是本文件要防的失效方式。
// (另一端的 grapheme cluster 更不行:组合字形算一个字形、却占两个码点,
// 它会**低估**长度,漏过真正会被数据库拒绝的行。)

// builtinRemarkBudget 是内置规则 Remark 的**软预算**,比列宽 varchar(512) 低一档。
//
// 只断言"不超过列宽"是一道事后守卫:它在越线**之后**才报红,而越线的那一次改动
// 通常只是给某条规则的误杀说明补了一句话。留出这段余量,就是让"补一句话"这个
// 最常见的动作永远不会直接撞墙 —— 报红时手上还有一整句的空间可以腾挪。
//
// 52 字约等于一句完整的中文说明。这个数字本身是可以调的,但它必须是一条**被断言的
// 约定**而不是注释里的一句口号:口号会在第一次超线时被无声地忽略掉。
const builtinRemarkBudget = 460

// varcharLimitRe 从 gorm tag 里抠出 `type:varchar(N)` 的 N。
//
// 容忍 `varchar (512)` 与大小写差异。它认不出 GORM 的等价写法 `size:128`,
// 所以下面的 TestRuleVarcharLimitsMatchColumnTags 不是"数够五个就算解析成功",
// 而是要求 Rule 的**每一个** string 字段都有交代:要么被这条正则认出来,
// 要么出现在下面那张显式豁免表里。任何一种新写法都会让某个字段两头落空,当场报红。
var varcharLimitRe = regexp.MustCompile(`(?i)\btype:\s*varchar\s*\(\s*(\d+)\s*\)`)

// ruleNonVarcharStringFields 是 Rule 上**故意**没有字符数上限的 string 字段。
//
// 目前只有 Pattern(`type:text`)。它的上限由 ValidateRule 按字节判(8192),
// 因为那是编译成正则之后的代价,与数据库列宽无关。
var ruleNonVarcharStringFields = map[string]string{
	"Pattern": "type:text —— 长度上限由 ValidateRule 按 8192 字节判",
}

// parseRuleVarcharTags 反射 Rule,返回 字段名 → gorm tag 里声明的字符数上限。
func parseRuleVarcharTags(t *testing.T) map[string]int {
	t.Helper()
	limits := map[string]int{}
	rt := reflect.TypeOf(Rule{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		tag := f.Tag.Get("gorm")
		m := varcharLimitRe.FindStringSubmatch(tag)
		if m == nil {
			// 不是"跳过",是"必须已经被显式豁免"。静默跳过正是本文件要防的假回归:
			// 一个字段悄悄退出扫描范围时,下面的逐条比对会对它空转通过。
			require.Containsf(t, ruleNonVarcharStringFields, f.Name,
				"Rule.%s 是 string 字段,但 gorm tag %q 里解析不出 type:varchar(N)。"+
					"要么它确实有列宽(把 tag 写成 type:varchar(N),或让 varcharLimitRe 认得新写法),"+
					"要么它确实没有(登记进 ruleNonVarcharStringFields 并写清理由)。"+
					"两头都不占的字段会静默退出守卫射程,重演一次 Error 1406。", f.Name, tag)
			continue
		}
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err, "字段 %s 的 varchar 长度解析失败", f.Name)
		require.Greater(t, n, 0, "字段 %s 的 varchar 长度必须为正", f.Name)
		limits[f.Name] = n
	}
	return limits
}

// TestRuleVarcharLimitsMatchColumnTags 把生产侧的长度校验表钉死在 gorm tag 上。
//
// rules.go 的 ruleVarcharLimits 是写入前校验用的手写副本 —— 它存在是为了把
// 「哪一格填多了、上限是多少」告诉管理员,而不是让 MySQL 用一句 Error 1406 代劳
// (SQLite 更是根本不校验,同一份数据换个库就 INSERT 失败)。
//
// 手写副本的代价就是漂移:列被改窄时校验会放过数据库拒绝的行,列被改宽时校验会
// 拦下数据库接受的行。两种都是本轮事故的同一个形状,所以这里逐字段双向比对。
func TestRuleVarcharLimitsMatchColumnTags(t *testing.T) {
	tags := parseRuleVarcharTags(t)
	require.NotEmpty(t, tags, "Rule 上一个 varchar 列都没解析出来,守卫必然是空转的")

	seen := make(map[string]struct{}, len(ruleVarcharLimits))
	rt := reflect.TypeOf(Rule{})
	for _, lim := range ruleVarcharLimits {
		f, ok := rt.FieldByName(lim.Field)
		require.Truef(t, ok, "ruleVarcharLimits 里的 %q 在 Rule 上不存在", lim.Field)
		require.Equalf(t, reflect.String, f.Type.Kind(), "Rule.%s 不是 string 字段", lim.Field)
		require.NotContainsf(t, seen, lim.Field, "ruleVarcharLimits 里 %q 重复登记", lim.Field)
		seen[lim.Field] = struct{}{}

		want, ok := tags[lim.Field]
		require.Truef(t, ok, "ruleVarcharLimits 给 %q 定了上限,但它在 model.go 上不是 varchar 列", lim.Field)
		assert.Equalf(t, want, lim.Max,
			"Rule.%s 的校验上限(%d)与 gorm tag 声明的列宽(%d)不一致。"+
				"改窄了列却没改校验 → 管理员照旧存得进去,MySQL 用 Error 1406 拒绝,"+
				"界面上只剩一句「处理失败,请稍后重试」。", lim.Field, lim.Max, want)

		// Get 必须真的读那一列。指错字段的 getter 会让这一格的校验永远看着另一列。
		row := &Rule{}
		reflect.ValueOf(row).Elem().FieldByName(lim.Field).SetString("qy-probe")
		assert.Equalf(t, "qy-probe", lim.Get(row), "ruleVarcharLimits 里 %q 的 Get 读的不是这一列", lim.Field)
	}

	for field := range tags {
		assert.Containsf(t, seen, field,
			"Rule.%s 是有列宽的 varchar 列,却没有进 rules.go 的 ruleVarcharLimits。"+
				"没有它,超长写入只会以数据库错误的形式冒出来:MySQL 报 Error 1406 → 500「处理失败」,"+
				"SQLite 干脆静默存下,直到有人把数据迁到 MySQL 才炸。", field)
	}
}

// parseCategoryVarcharTags 反射 Category,返回 字段名 → gorm tag 里声明的字符数上限。
//
// 与 parseRuleVarcharTags 同一套判据、同一条理由。手写校验表与 tag 的双向比对
// 已经由 category_test.go 的 TestCategoryVarcharLimitsMatchColumnTags 负责;
// 这里要的是另一半 —— **种子行是代码写死的**,它和内置规则目录一样绕过全部写入
// 校验直接 Create,于是同一次 Error 1406 完全可以在这张表上重演。
func parseCategoryVarcharTags(t *testing.T) map[string]int {
	t.Helper()
	limits := map[string]int{}
	rt := reflect.TypeOf(Category{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		m := varcharLimitRe.FindStringSubmatch(f.Tag.Get("gorm"))
		require.NotNilf(t, m,
			"Category.%s 是 string 字段,但 gorm tag 里解析不出 type:varchar(N)。"+
				"没有声明列宽的字段会静默退出守卫射程 —— 而种子行是代码写死的,"+
				"超长时表现是「启动没报错、类型表却少了几行」。", f.Name)
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err, "字段 %s 的 varchar 长度解析失败", f.Name)
		limits[f.Name] = n
	}
	return limits
}

// TestSeedCategoriesFitDeclaredColumnWidths 逐条比对出厂类型与列宽。
//
// 本轮往 seedCategories 里加了九条上游合规类型,每条都带一段「给 AI 的判定说明」。
// 那一列只有 varchar(256) —— 比内部说明窄一半,因为它是**每次审核调用都要付一遍**
// 的 token。多写两句话就会撞线,而撞线的表现是那几行种子一条都没进库:
// 类型页上少了几类,AI 的类型清单里也就少了几类,而启动日志里只有一行。
func TestSeedCategoriesFitDeclaredColumnWidths(t *testing.T) {
	limits := parseCategoryVarcharTags(t)
	require.Contains(t, limits, "AIGuidance")

	for _, s := range seedCategories {
		cat := Category{
			Key: s.Key, Name: s.Name, Remark: s.Desc,
			PublicTitle: s.Title, PublicDesc: s.Pub, AIGuidance: s.AI,
		}
		row := reflect.ValueOf(cat)
		for field, limit := range limits {
			v := row.FieldByName(field).String()
			assert.LessOrEqualf(t, utf8.RuneCountInString(v), limit,
				"出厂类型 %s 的 %s 有 %d 字(%d 字节),超过列宽 varchar(%d)。"+
					"MySQL 会以 Error 1406 拒绝整条 INSERT,表现是「启动成功、类型页少了这一行」。"+
					"判定说明写不下就压缩判据本身 —— 它每次审核调用都要作为 token 付一遍钱。",
				s.Key, field, utf8.RuneCountInString(v), len(v), limit)
		}
		// 参与 AI 审核的类型必须带判定说明。只给一个英文 key,模型分不清
		// illegal_goods 与 cyber_attack 的边界在哪,而分不清的那一票会加到
		// 错误的类型计数上 —— "计数说得通、结论是错的"。
		if s.NoAI {
			continue
		}
		assert.NotEmptyf(t, s.AI,
			"出厂类型 %s 参与 AI 审核却没有「给 AI 的判定说明」:模型只会拿到一个英文 key", s.Key)
	}
}

// TestBuiltinCatalogFitsDeclaredColumnWidths 逐条比对 toRule() 的产物与列宽。
func TestBuiltinCatalogFitsDeclaredColumnWidths(t *testing.T) {
	limits := parseRuleVarcharTags(t)
	// Remark 是事故发生的那一列。它必须始终在守卫射程内 ——
	// 哪怕将来它被改成别的类型,也要有人在这里显式面对这个断言。
	require.Contains(t, limits, "Remark", "Remark 必须是一个有声明长度的 varchar 列")
	require.LessOrEqual(t, builtinRemarkBudget, limits["Remark"], "软预算不得高于列宽,否则它什么都不预防")

	for _, b := range builtinCatalog {
		rule := b.toRule(1000, 7)
		row := reflect.ValueOf(*rule)
		for field, limit := range limits {
			v := row.FieldByName(field).String()
			runes := utf8.RuneCountInString(v)
			assert.LessOrEqualf(t, runes, limit,
				"内置规则 %s 的 %s 有 %d 字(%d 字节),超过列宽 varchar(%d)。"+
					"MySQL 会以 Error 1406 拒绝整条 INSERT,表现是「导入返回成功、库里一条都没有」。"+
					"把逐分支的推导搬到规则定义上方的 Go 注释里,把升级说明搬进 Advice"+
					"(它不落库、没有长度预算),字段只留运营在列表上要看的那一句。",
				b.Key, field, runes, len(v), limit)
		}
		assert.LessOrEqualf(t, utf8.RuneCountInString(rule.Remark), builtinRemarkBudget,
			"内置规则 %s 的 Remark 有 %d 字,超过软预算 %d(列宽是 %d)。"+
				"现在还能存进库,但余量已经不够下一个人补一句误杀说明了 —— "+
				"这道断言就是要在撞墙**之前**报,而不是之后。"+
				"「这一版删改了什么」属于 Advice(不落库),不要往 Guards/FalsePositive/Origin 里塞。",
			b.Key, utf8.RuneCountInString(rule.Remark), builtinRemarkBudget, limits["Remark"])
	}
}
