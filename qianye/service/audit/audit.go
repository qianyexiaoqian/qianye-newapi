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
