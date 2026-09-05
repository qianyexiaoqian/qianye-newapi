package mall

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// http.go —— 本模块唯一的响应信封、错误翻译与审计封装(形状照 lottery/http.go)。

// listPaging 是本模块所有列表接口的分页口径:?page= / ?page_size=,默认 20、上限 100
// (契约 §0:分页参数 page 从 1)。
var listPaging = httpq.Spec{PageKey: "page"}

// auditCategory 是本模块全部审计行的分类。
const auditCategory = qymodel.AuditCategoryMall

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

// respondErr 把内部错误翻译成稳定的响应信封。
//
// 未识别的错误一律降级为 500 且不回显原文:错误原文可能包含 SQL 片段与表结构。
func respondErr(c *gin.Context, err error) {
	err = BizOf(err)
	if be, ok := AsBizError(err); ok {
		c.JSON(be.HTTPStatus(), gin.H{"success": false, "code": be.ErrCode(), "message": be.Message()})
		return
	}
	switch {
	case errors.Is(err, twophase.ErrIdemConflict):
		respondErr(c, errIdemConflict)
	case errors.Is(err, twophase.ErrInProgress), errors.Is(err, twophase.ErrOrderFailed):
		respondErr(c, errInProgress)
	case errors.Is(err, twophase.ErrAmountOutOfRange):
		respondErr(c, errBadRequest("金额超出允许范围"))
	case errors.Is(err, db.ErrNotReady):
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "商城暂不可用,请稍后重试",
		})
	default:
		common.SysError("qianye/mall: 接口处理失败: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
		})
	}
}

// snapText 把审计快照序列化成入库文本。序列化失败返回显式标记而不是空串:
// 空串在审计详情里与"本来就没有快照"无法区分。
func snapText(v any) string {
	if v == nil {
		return ""
	}
	b, err := common.Marshal(v)
	if err != nil {
		return "<snapshot marshal failed: " + err.Error() + ">"
	}
	return string(b)
}

// auditReason 把错误折成审计里的 reason:业务错误带 code,内部错误原文。
func auditReason(err error) string {
	if err == nil {
		return ""
	}
	if be, ok := AsBizError(err); ok {
		return be.ErrCode() + ": " + be.Message()
	}
	return err.Error()
}

// writeAdminAudit 落一条管理端动作审计。成功与失败共用同一出口
// (qianye/audit_coverage_guard_test.go 按这个函数名计数)。
func writeAdminAudit(c *gin.Context, action, traceNo string, targetUserId int, amount int64,
	result, reason, before, after string) {
	audit.Write(c, audit.Entry{
		TraceNo:      traceNo,
		Category:     auditCategory,
		Action:       action,
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: targetUserId,
		AmountQuota:  amount,
		Result:       result,
		Reason:       audit.Truncate(reason, 500),
		BeforeSnap:   before,
		AfterSnap:    after,
	})
}

// userAuditEntry 组装一条用户自己动作的审计行;调用点用 audit.Write 落库,
// 让 audit_coverage_guard 能按名字数到它。AmountQuota 装的是星屑数(本模块只有星屑)。
func userAuditEntry(c *gin.Context, action, traceNo string, amount int64, result, reason string) audit.Entry {
	uid := c.GetInt("id")
	return audit.Entry{
		TraceNo:      traceNo,
		Category:     auditCategory,
		Action:       action,
		ActorType:    qymodel.ActorUser,
		ActorUserId:  uid,
		ActorName:    c.GetString("username"),
		TargetUserId: uid,
		AmountQuota:  amount,
		Result:       result,
		Reason:       audit.Truncate(reason, 500),
	}
}
