package violation

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_export.go —— 命中记录导出。
//
// # 为什么必须有
//
// 项目方给影子模式定的用途只有一个:「用于抓取涉嫌违规用户的日志、上下文,
// 我要进行分析」。屏幕上一页 20 行做不了分析 —— 分析是"按用户分组看命中分布"、
// "看同一个 IP 下有几个账号"、"看命中片段里到底是不是同一段模板"。这些都要把
// 数据取下来。没有导出,影子模式的产出就只能靠人一页页翻,那个用例实际上是断的。
//
// # 边界
//
//   - **不含归档上下文正文**。正文走 adminGetEvidence(逐条、逐次写审计),
//     那是"查看他人输入原文"这一动作应有的粒度。CSV 只带 has_payload,
//     告诉分析者哪几行值得点进去。把正文塞进批量导出等于把全站用户的输入
//     一次性打包成一个可以随手转发的文件。
//   - **有行数上界**。这张表是所有扩展表里增长最快的一张,不设上界的一次导出
//     就能把内存打满。超限时截断并在响应头里如实说明,而不是悄悄少给几行。
const (
	// exportMaxRows 是单次导出的行数上界。
	//
	// 2 万行 × 约 400 字节 ≈ 8MB,在管理端是可以接受的一次下载;再大就应该
	// 收窄时间窗而不是加大这个数 —— 分析一次 10 万行的 CSV 本来也不靠浏览器。
	exportMaxRows = 20000
	// exportBatch 是分批读取的页长。用游标分批而不是一次 Find:
	// 一次性把 2 万行读进内存再写出去,峰值内存是分批的 exportMaxRows/exportBatch 倍。
	exportBatch = 1000
)

// exportColumns 是 CSV 的表头,顺序即列序。
//
// 这一列表就是"影子命中够不够做分析"的答案本身,所以逐项对应项目方点名要的东西:
// 命中的规则(rule_id/rule_name)、命中的文本片段(match_snippet/matched_terms)、
// 模型(model_name)、分组(using_group)、令牌(token_id/token_name)、
// 时间(created_at)、**若真实执行会扣多少钱**(fee_quota_want)。
// 另外三列是分析时立刻会被问到、事后补不回来的:
//   - would_block:若真实执行会不会被阻断(由 action 推出,单独一列免得每个人自己推);
//   - count_weight:若真实执行会给违规计数加几(影子命中不推进计数,见 CounterAfterShadow);
//   - shadow_reason:这一条是"规则本身就是影子"还是"熔断把它钳成了影子"。
var exportColumns = []string{
	"created_at", "rec_no", "request_id",
	"user_id", "username", "token_id", "token_name", "ip",
	"rule_id", "rule_name", "phase", "action",
	"shadow", "shadow_reason", "blocked", "would_block",
	"model_name", "using_group", "channel_id", "relay_format",
	"matched_terms", "match_snippet",
	"fee_mode", "fee_quota_want", "fee_quota", "fee_status",
	"count_weight", "counted", "counter_after",
	"status", "has_payload",
}

// adminExportRecords 按当前筛选条件导出命中记录为 CSV。
//
// 筛选参数与 adminListRecords 完全一致(共用 recordQuery):导出的就是屏幕上
// 那一批,只是不分页。
func adminExportRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	// 句柄在交出去之前就绑上请求 ctx:导出可能要跑几秒,客户端断开之后继续把
	// 两万行读出来只是白白占着扩展库的连接。nil 检查必须在前 —— 对 nil 句柄
	// 调 WithContext 会 panic。
	gdb = gdb.WithContext(c.Request.Context())

	limit := httpq.Int(c, "limit", exportMaxRows)
	if limit <= 0 || limit > exportMaxRows {
		limit = exportMaxRows
	}

	// 审计写在读之前:这是一次批量读取他人输入片段的操作,进程在写文件的过程中
	// 崩掉也必须留下"谁在什么时候导了什么条件"。adminGetEvidence 把审计写在读之后
	// 是因为它要先确认记录存在,这里没有那个前提。
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      "records.export",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Reason:      "导出违规命中记录用于分析",
		AfterSnap: common.MapToJsonStr(map[string]any{
			"rule_id": c.Query("rule_id"), "shadow": c.Query("shadow"),
			"user_id": c.Query("user_id"), "phase": c.Query("phase"),
			"start_ts": c.Query("start_ts"), "end_ts": c.Query("end_ts"),
			"limit": limit,
		}),
	})

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(
		`attachment; filename="violation-records-%d.csv"`, common.GetTimestamp()))
	// X-Qy-Export-Truncated 让调用方能判断"这就是全部"还是"被截了"。
	// 不给这个信号的话,一次静默截断会让分析结论建立在半份数据上。
	c.Header("X-Qy-Export-Limit", strconv.Itoa(limit))
	c.Status(http.StatusOK)

	if err := exportRows(c, gdb, limit); err != nil {
		// 响应头已经发出去了,这里改不成 500 —— 只能把错误留在日志里。
		// 客户端拿到的是一份短了的文件,而 X-Qy-Export-Limit 与行数对不上正是
		// 它自证"这份数据不完整"的方式。
		common.SysError("qianye/violation: 导出中断: " + err.Error())
	}
}

// exportRows 是导出的写循环本体,gdb 与 limit 由调用方注入以便直接测。
//
// 独立成函数不是为了缩短上面那个处理器,而是因为"分批游标有没有把行写全"
// 只能在这里被直接测到:它上面那层要 db.Get()(测试环境里不可用)与 HTTP 鉴权,
// 断言只能停在"接口返回了 200",而返回 200 的空文件与返回 200 的完整文件
// 在那个层面上完全一样 —— 经典的假回归。
func exportRows(c *gin.Context, gdb *gorm.DB, limit int) error {
	// UTF-8 BOM:Excel 打开无 BOM 的 UTF-8 CSV 会把中文渲染成乱码,
	// 而这份文件的第一读者就是拿 Excel 看的运营。
	if _, err := c.Writer.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	w := csv.NewWriter(c.Writer)
	defer w.Flush()
	if err := w.Write(exportColumns); err != nil {
		return err
	}

	written := 0
	lastId := int64(0)
	for written < limit {
		size := exportBatch
		if remain := limit - written; remain < size {
			size = remain
		}
		var rows []Record
		// 按主键游标翻页而不是 OFFSET:这张表边导边写,OFFSET 分页会在中途
		// 插入新行时重复或漏掉记录,而导出的用途是统计,重复一行就是错的数。
		q := recordQuery(c, gdb.Model(&Record{}))
		if lastId > 0 {
			q = q.Where("id < ?", lastId)
		}
		if err := q.Order("id desc").Limit(size).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for i := range rows {
			if err := w.Write(recordRow(&rows[i])); err != nil {
				return err
			}
			lastId = rows[i].Id
			written++
		}
		w.Flush()
		if len(rows) < size {
			return nil
		}
	}
	return nil
}

func recordRow(r *Record) []string {
	return []string{
		strconv.FormatInt(r.CreatedAt, 10),
		csvCell(r.RecNo), csvCell(r.RequestId),
		strconv.Itoa(r.UserId), csvCell(r.Username),
		strconv.Itoa(r.TokenId), csvCell(r.TokenName), csvCell(r.Ip),
		strconv.FormatInt(r.RuleId, 10), csvCell(r.RuleName), csvCell(r.Phase), csvCell(r.Action),
		strconv.FormatBool(r.Shadow), csvCell(r.ShadowReason), strconv.FormatBool(r.Blocked),
		// would_block 回答"这一条若真实执行会不会被拦"。影子记录的 blocked 恒为
		// false,只看那一列会以为这些请求本来也会放行。
		strconv.FormatBool(blocks(r.Action) && r.Phase == PhasePrompt),
		csvCell(r.ModelName), csvCell(r.UsingGroup),
		strconv.Itoa(r.ChannelId), csvCell(r.RelayFormat),
		csvCell(r.MatchedTerms), csvCell(r.MatchSnippet),
		csvCell(r.FeeMode),
		strconv.FormatInt(r.FeeQuotaWant, 10), strconv.FormatInt(r.FeeQuota, 10), csvCell(r.FeeStatus),
		strconv.Itoa(r.CountWeight), strconv.FormatBool(r.Counted), strconv.Itoa(r.CounterAfter),
		csvCell(r.Status), strconv.FormatBool(r.HasPayload),
	}
}

// csvCell 让一个单元格在电子表格里只能是文本,不可能是公式。
//
// matched_terms 与 match_snippet 直接来自用户输入。Excel / WPS / Sheets 会把以
// `=` `+` `-` `@` 开头的单元格当公式求值,于是一段 `=cmd|'…'!A1` 的 prompt
// 就变成了打开这份 CSV 的运营机器上的一次命令执行(CSV 注入)。前缀一个单引号是
// 各家电子表格通用的"强制文本"写法,而 csv.Writer 只负责引号转义,管不到这一层。
//
// 制表符与换行同时压平:它们不会造成安全问题,但会让一行记录在表格里裂成几行,
// 而"一行 = 一次命中"是这份文件唯一的阅读约定。
func csvCell(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@':
		return "'" + s
	}
	return s
}
