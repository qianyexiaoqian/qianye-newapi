package violation

import (
	"encoding/csv"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_export_test.go —— 影子命中分析链路的回归。
//
// 项目方对影子模式的要求是「只记录不处罚,用于抓取涉嫌违规用户的日志、上下文,
// 我要进行分析」。这句话落到代码上是三段,任何一段断了整条链路就没用:
//
//	按规则 + 影子筛出来  →  导出成能分析的文件  →  文件里字段够用
//
// 下面三组测试各锁一段。

func newExportRecordDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := newBuiltinRuleDB(t) // 复用同一套内存库工厂,只是再迁一张表
	require.NoError(t, gdb.AutoMigrate(&Record{}))
	return gdb
}

func exportCtx(t *testing.T, query string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/api/qy/admin/violation/records/export?"+query, nil)
	return c, rec
}

// TestRecordQueryShadowFilterIsThreeState 固化"按影子筛"这一步。
//
// 三态必须都成立:不传 = 全部;shadow=1 = 只看影子;shadow=0 = 只看真实。
// 用 httpq.Int 的默认值兜第三态是最容易犯的错 —— 那样"没传"与"传了 0"无法区分,
// 结果是真实命中永远筛不出来,而界面上那个筛选框看起来完全正常。
func TestRecordQueryShadowFilterIsThreeState(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	gdb := newExportRecordDB(t)

	seed := []*Record{
		{RecNo: "a", RuleId: 1, Shadow: true, ShadowReason: ShadowReasonRuleMode, UserId: 1, CreatedAt: 100},
		{RecNo: "b", RuleId: 1, Shadow: true, ShadowReason: ShadowReasonBreaker, UserId: 1, CreatedAt: 101},
		{RecNo: "c", RuleId: 1, Shadow: false, UserId: 1, CreatedAt: 102},
		{RecNo: "d", RuleId: 2, Shadow: true, ShadowReason: ShadowReasonRuleMode, UserId: 2, CreatedAt: 103},
	}
	for _, r := range seed {
		require.NoError(t, gdb.Create(r).Error)
	}

	count := func(query string) int64 {
		c, _ := exportCtx(t, query)
		var n int64
		require.NoError(t, recordQuery(c, gdb.Model(&Record{})).Count(&n).Error)
		return n
	}

	assert.EqualValues(t, 4, count(""), "不传 shadow = 全部")
	assert.EqualValues(t, 3, count("shadow=1"))
	assert.EqualValues(t, 1, count("shadow=0"),
		"只看真实命中必须可行 —— 用默认值兜的实现在这里会返回 4")
	assert.EqualValues(t, 2, count("shadow=1&rule_id=1"),
		"「这条规则的影子命中」是项目方用例的核心筛选")
	assert.EqualValues(t, 1, count("shadow=1&shadow_reason="+ShadowReasonBreaker),
		"熔断期间的记录必须能单独筛出来,否则误判率统计会被事故样本稀释")
}

// TestExportCarriesEveryFieldAnalysisNeeds 固化"导出的字段够不够做分析"。
//
// 逐项对应项目方点名要的东西:命中的规则、命中的文本片段、模型、分组、令牌、
// 时间、**若真实执行会扣多少钱**。外加三个事后补不回来的推导值:
// would_block / count_weight / shadow_reason。
func TestExportCarriesEveryFieldAnalysisNeeds(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")

	rec := &Record{
		Id: 1, RecNo: "vr_x_1", RequestId: "req-9",
		UserId: 42, Username: "alice", TokenId: 7, TokenName: "sk-main", Ip: "1.2.3.4",
		RuleId: 9, RuleName: "破限-DAN 越狱人格", Phase: PhasePrompt, Action: ActionBlockAndCharge,
		Shadow: true, ShadowReason: ShadowReasonRuleMode, Blocked: false,
		ModelName: "gpt-4o", UsingGroup: "vip", ChannelId: 3, RelayFormat: "openai",
		MatchedTerms: "dan", MatchSnippet: "...do anything now...",
		FeeMode: FeeFixed, FeeQuotaWant: 25000, FeeQuota: 0, FeeStatus: FeeStatusShadow,
		CountWeight: 2, Counted: false, CounterAfter: CounterAfterShadow,
		Status: RecordActive, HasPayload: true, CreatedAt: 1700000000,
	}
	row := recordRow(rec)
	require.Len(t, row, len(exportColumns), "表头与数据列数必须一致,否则整份 CSV 错位")

	cell := map[string]string{}
	for i, name := range exportColumns {
		cell[name] = row[i]
	}
	assert.Equal(t, "9", cell["rule_id"])
	assert.Equal(t, "破限-DAN 越狱人格", cell["rule_name"])
	assert.Equal(t, "...do anything now...", cell["match_snippet"])
	assert.Equal(t, "dan", cell["matched_terms"])
	assert.Equal(t, "gpt-4o", cell["model_name"])
	assert.Equal(t, "vip", cell["using_group"])
	assert.Equal(t, "7", cell["token_id"])
	assert.Equal(t, "sk-main", cell["token_name"])
	assert.Equal(t, "1700000000", cell["created_at"])
	assert.Equal(t, "25000", cell["fee_quota_want"],
		"「若真实执行会扣多少钱」是影子模式的全部价值,少了它这份导出做不了成本评估")
	assert.Equal(t, "2", cell["count_weight"], "「若真实执行会给计数加几」")
	assert.Equal(t, ShadowReasonRuleMode, cell["shadow_reason"])
	assert.Equal(t, "true", cell["would_block"],
		"影子记录的 blocked 恒为 false,只看那一列会以为这些请求本来也会放行")
	assert.Equal(t, "true", cell["has_payload"], "告诉分析者哪几行值得点进去看上下文")

	t.Run("上游阶段的规则不会被标成会阻断", func(t *testing.T) {
		post := *rec
		post.Phase = PhaseUpstreamErr
		out := recordRow(&post)
		assert.Equal(t, "false", out[indexOfColumn("would_block")],
			"上游阶段字节已经发出去了,不可能阻断")
	})
}

func indexOfColumn(name string) int {
	for i, c := range exportColumns {
		if c == name {
			return i
		}
	}
	return -1
}

// TestCsvCellNeutralizesSpreadsheetFormulas 是一条安全回归。
//
// matched_terms 与 match_snippet 直接来自用户输入。Excel / WPS / Sheets 会把以
// = + - @ 开头的单元格当公式求值 —— 于是一段精心构造的 prompt 就变成了打开这份
// CSV 的运营机器上的一次命令执行。csv.Writer 只做引号转义,管不到这一层。
func TestCsvCellNeutralizesSpreadsheetFormulas(t *testing.T) {
	for _, payload := range []string{
		`=cmd|'/c calc'!A1`,
		`+1+1`,
		`-2+3`,
		`@SUM(1:9)`,
	} {
		out := csvCell(payload)
		assert.True(t, strings.HasPrefix(out, "'"),
			"以 %q 开头的单元格会被电子表格当公式求值,必须强制成文本", payload[:1])
		assert.Equal(t, payload, out[1:], "除了前缀之外内容必须原样保留")
	}

	assert.Equal(t, "normal text", csvCell("normal text"), "正常文本不加前缀")
	assert.Equal(t, "", csvCell(""))
	assert.Equal(t, "a b c", csvCell("a\r\nb\tc"),
		"换行与制表符会让一行记录在表格里裂成几行,而「一行 = 一次命中」是这份文件唯一的阅读约定")
}

// TestExportWritesEveryFilteredRow 是导出的端到端回归。
//
// 分批游标翻页很容易写出"只导出第一批"的缺陷,而那种缺陷不会报错:
// 管理员拿到一份看起来正常、实际少了九成行的文件,并据此得出结论。
func TestExportWritesEveryFilteredRow(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	gdb := newExportRecordDB(t)

	// **匹配到的行数必须大于 exportBatch**,否则第一批就把结果取完了,
	// 「只写第一批」这个缺陷根本不会显形 —— 那正是这条测试第一版的假回归形状
	// (造 exportBatch+5 行、其中一半是影子,筛出来只有 503 条,一批就够了)。
	const shadowRows = exportBatch + 5
	const noiseRows = 20
	batch := make([]*Record, 0, shadowRows+noiseRows)
	for i := 0; i < shadowRows; i++ {
		batch = append(batch, &Record{
			RecNo: "vr_s_" + strconv.Itoa(i), RuleId: 1, UserId: 1,
			Shadow: true, ShadowReason: ShadowReasonRuleMode,
			Phase: PhasePrompt, Action: ActionRecord, CreatedAt: int64(1000 + i),
		})
	}
	// 混入真实命中:反向保证筛选真的生效,而不是"把全表都导出来了"。
	for i := 0; i < noiseRows; i++ {
		batch = append(batch, &Record{
			RecNo: "vr_r_" + strconv.Itoa(i), RuleId: 1, UserId: 1,
			Shadow: false, Phase: PhasePrompt, Action: ActionRecord,
			CreatedAt: int64(9000 + i),
		})
	}
	require.NoError(t, gdb.CreateInBatches(batch, 200).Error)

	c, rec := exportCtx(t, "shadow=1")
	rows := runExport(t, gdb, c, rec)

	require.Greater(t, len(rows), 1)
	assert.Equal(t, exportColumns, rows[0], "第一行必须是表头")
	assert.Len(t, rows[1:], shadowRows,
		"分批游标必须把筛选出来的行全部写完;只写第一批只会少几行,不会报任何错")
}

// runExport 驱动**线上那段真正的写循环**并把响应体解析成 CSV 行。
//
// 调 exportRows 而不是在测试里复刻一遍游标翻页:复刻出来的循环只会证明
// "我写的这段循环是对的",而不是"线上那段是对的",两者一旦漂移这条测试
// 会继续全绿。exportRows 被从处理器里提出来,唯一的目的就是让这一步成立。
func runExport(t *testing.T, gdb *gorm.DB, c *gin.Context, rec *httptest.ResponseRecorder) [][]string {
	t.Helper()
	require.NoError(t, exportRows(c, gdb, exportMaxRows))
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(rec.Body.String(), "\xef\xbb\xbf"))).ReadAll()
	require.NoError(t, err)
	return rows
}

// ─────────────────────── 挂载点:AST 断言 ───────────────────────
//
// 行为断言证明"这些函数做对了事",AST 断言证明"它们真的被接上了"。
// 上一轮就栽在只有前者上:变量被赋了值,而没有任何函数去调它。

func moduleIdents(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "module.go", nil, 0)
	require.NoError(t, err)
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			out[v.Name] = true
		case *ast.BasicLit:
			out[strings.Trim(v.Value, "\"")] = true
		}
		return true
	})
	return out
}

// TestNewHandlersAreActuallyRouted 挡住"写了处理器却没挂路由"。
func TestNewHandlersAreActuallyRouted(t *testing.T) {
	idents := moduleIdents(t)
	for _, name := range []string{
		"adminListBuiltinRules", "adminImportBuiltinRules", "adminExportRecords",
	} {
		assert.True(t, idents[name], "%s 没有出现在 module.go 里 —— 处理器写了但没接上路由", name)
	}
	for _, path := range []string{
		"/violation/rules/builtin", "/violation/rules/import-builtin", "/violation/records/export",
	} {
		assert.True(t, idents[path], "路由 %s 未注册", path)
	}
	// 迁移必须真的被启动流程调用。只定义不调用是本仓库最常见的断链形状。
	assert.True(t, idents["runRuleModeMigration"],
		"规则模式迁移没有被 InstallHooks 调用 —— 现网规则会停在空 mode")
}

// TestDeletedGlobalModeRouteIsGone 挡住"接口删了一半"。
//
// 全局模式层删除之后,PUT /violation/mode 必须彻底消失。留着一个还在的路由
// 指向一个已经不存在的语义,比不删更糟:前端会继续调它,而调用结果无人验证。
func TestDeletedGlobalModeRouteIsGone(t *testing.T) {
	idents := moduleIdents(t)
	assert.False(t, idents["/violation/mode"], "全局模式接口必须随全局模式层一起删除")
	assert.False(t, idents["adminPutMode"], "adminPutMode 必须已删除")
}
