// Package audit 提供扩展的统一审计写入。
//
// 强制规则:任何影响资金计算或对外承诺的操作与配置变更都必须写审计。
// 包括费率调整、熔断开关、口径变更 —— 否则事后无法解释"为什么这笔按 5% 算"。
package audit

import (
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
		ActorName:    truncate(e.ActorName, 64),
		TargetUserId: e.TargetUserId,
		AmountQuota:  e.AmountQuota,
		AmountFiat:   e.AmountFiat,
		Currency:     e.Currency,
		FrozenRate:   e.FrozenRate,
		Result:       e.Result,
		Reason:       truncate(e.Reason, 512),
		BeforeSnap:   truncate(e.BeforeSnap, maxSnap),
		AfterSnap:    truncate(e.AfterSnap, maxSnap),
		NodeName:     common.NodeName,
		CreatedAt:    common.GetTimestamp(),
	}
}

func fillFromContext(c *gin.Context, row *qymodel.AuditLog) {
	if c == nil {
		return
	}
	if config.Get().Audit.ShouldRecordIP() {
		row.IP = truncate(c.ClientIP(), 64)
		row.UserAgent = truncate(c.Request.UserAgent(), 256)
	}
	row.RequestId = truncate(c.GetString(common.RequestIdKey), 64)
}

// truncate 按字节截断并标注,避免超长字段被数据库静默切掉。
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	const mark = "...[truncated]"
	if max <= len(mark) {
		return s[:max]
	}
	return s[:max-len(mark)] + mark
}
