// Package audit 提供扩展的统一审计写入。
//
// 强制规则:任何影响资金计算或对外承诺的操作与配置变更都必须写审计。
// 包括费率调整、熔断开关、口径变更 —— 否则事后无法解释"为什么这笔按 5% 算"。
package audit

import (
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/shopspring/decimal"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Entry 是一条待写入的审计记录。
type Entry struct {
	TraceNo  string
	Category string
	Action   string

	ActorType   string
	ActorUserId int
	ActorName   string

	TargetUserId int

	AmountQuota int64
	AmountFiat  decimal.Decimal
	Currency    string
	FrozenRate  decimal.Decimal

	Result string
	Reason string

	BeforeSnap string
	AfterSnap  string
}

// Write 写入一条审计。
//
// 永不返回错误:审计失败不能阻塞业务。但必须告警 —— 静默丢失审计会让事故无法复盘。
func Write(c *gin.Context, e Entry) {
	if !config.Get().Audit.On() || !db.Available() {
		return
	}
	row := build(e)
	fillFromContext(c, row)
	if err := db.Get().Create(row).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye: 写入审计失败(业务未受影响): " + err.Error())
	}
}

// WriteTx 在给定的扩展库事务内写审计。
//
// 用于"业务状态变更与审计必须同生共死"的场景,例如提现审核:
// 审核通过了却没有审计记录,是不可接受的。
func WriteTx(tx *gorm.DB, e Entry) error {
	if !config.Get().Audit.On() {
		return nil
	}
	return tx.Create(build(e)).Error
}

// ConfigChange 是一次管理员配置变更的审计输入。
//
// Before/After 可以是任意可序列化的值,也可以直接给一段已经拼好的文本/JSON ——
// 后者原样透传(见 snapshotJSON)。运营参数、门槛、默认分组、站点主题这四类
// 快照的形态本来就不同,强行统一成同一个结构体只会逼每个模块再写一层适配。
type ConfigChange struct {
	// Action 是稳定英文标识,形如 commission.config.update。
	Action string
	// Result 为空时按 ResultOK 处理(见 build)。失败路径必须显式传 ResultFail:
	// "有人在这一刻试图改配置、失败了"是最需要留痕的事实之一。
	Result string
	Reason string
	Before any
	After  any
}

// WriteConfigUpdate 落一条配置变更审计,成功与失败共用同一条路径。
//
// # 为什么这一份必须住在这里
//
// 在它之前,commission 与 transfer 各有一份 writeConfigUpdateAudit,除了
// Action 字符串以外逐字节相同;而 grouppricing / usergroup / sitetheme 三个
// 同样在改配置的模块,一份都没有 —— 于是它们的失败路径零留痕,
// grouppricing 更糟:审计写在 WriteTx 里,事务一回滚连"试过"都查不到。
//
// 这正是本仓反复出现的"同一概念的第 N 份拷贝各自漂移":两份拷贝在被复制的
// 当天都是对的,但后来给其中一份补的东西另一份不会跟上,而没有拷贝的那三个
// 模块连"应该有这件事"都不知道。
//
// 操作者不从参数取而是直接读 context:两份旧拷贝的调用方传的都是
// c.GetInt("id"),多一个参数只是多一个能传错的地方。
func WriteConfigUpdate(c *gin.Context, ch ConfigChange) {
	Write(c, Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      ch.Action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      ch.Result,
		Reason:      ch.Reason,
		BeforeSnap:  snapshotJSON(ch.Before),
		AfterSnap:   snapshotJSON(ch.After),
	})
}

// snapshotJSON 把快照值转成入库文本。
//
// 字符串原样透传:usergroup 的快照是一句人话、别处也有手工拼好的 JSON,
// 再 Marshal 一次只会把它们变成带转义引号的字符串字面量。
//
// 序列化失败返回显式标记而不是空串:空串在审计详情里与"本来就没有快照"
// (新增操作的 before、删除操作的 after)无法区分,而这两件事的含义相反。
func snapshotJSON(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := common.Marshal(v)
	if err != nil {
		return "<snapshot marshal failed: " + err.Error() + ">"
	}
	return string(b)
}

func build(e Entry) *qymodel.AuditLog {
	maxSnap := config.Get().Audit.SnapshotMaxBytes
	if maxSnap <= 0 {
		maxSnap = 4096
	}
	if e.Result == "" {
		e.Result = qymodel.ResultOK
	}
	if e.ActorType == "" {
		e.ActorType = qymodel.ActorSystem
	}
	return &qymodel.AuditLog{
		TraceNo:      e.TraceNo,
		Category:     e.Category,
		Action:       e.Action,
		ActorType:    e.ActorType,
		ActorUserId:  e.ActorUserId,
		ActorName:    Truncate(e.ActorName, 64),
		TargetUserId: e.TargetUserId,
		AmountQuota:  e.AmountQuota,
		AmountFiat:   e.AmountFiat,
		Currency:     e.Currency,
		FrozenRate:   e.FrozenRate,
		Result:       e.Result,
		Reason:       Truncate(e.Reason, 512),
		BeforeSnap:   Truncate(e.BeforeSnap, maxSnap),
		AfterSnap:    Truncate(e.AfterSnap, maxSnap),
		NodeName:     common.NodeName,
		CreatedAt:    common.GetTimestamp(),
	}
}

func fillFromContext(c *gin.Context, row *qymodel.AuditLog) {
	if c == nil {
		return
	}
	if config.Get().Audit.ShouldRecordIP() {
		row.IP = Truncate(c.ClientIP(), 64)
		row.UserAgent = Truncate(c.Request.UserAgent(), 256)
	}
	row.RequestId = Truncate(c.GetString(common.RequestIdKey), 64)
}

// Truncate 按字节上限截断并标注,切点保证落在 UTF-8 字符边界上。
//
// 为什么必须对齐 rune 边界:切点落在多字节字符中间会产生非法 UTF-8 尾巴,
// 而扩展库 DSN 强制 charset=utf8mb4,MySQL 在 STRICT_TRANS_TABLES 下会以
// 1366(Incorrect string value)拒绝**整行**。审计写入是 fail-open 的
// (只 SysError 不阻塞业务),于是丢的不是理由的尾巴,而是"谁在什么时候拒了
// 这笔提现、理由是什么"这条记录本身 —— 这套资金系统事后仲裁的唯一凭据。
// 中英混排的 512 字节切点落在非边界上的概率约 2/3,不是理论风险。
//
// 上限按字节而不按字符是刻意的:目标列是 varchar(N)(MySQL 按字符计),
// 按字节卡只会更保守,永远不会溢出。
//
// 导出是为了让 twophase 的 last_error / uncertain 理由复用同一套语义 ——
// 那几处落的也是 varchar(512),同一类裸字节切。
func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	const mark = "...[truncated]"
	if max <= len(mark) {
		return s[:safeCut(s, max)]
	}
	return s[:safeCut(s, max-len(mark))] + mark
}

// safeCut 返回不超过 n 且落在 rune 起始位上的切点。
func safeCut(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}
