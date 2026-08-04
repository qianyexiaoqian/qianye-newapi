package lottery

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// http.go —— 本模块唯一的响应信封与错误翻译。
//
// 分页解析全部走 qianye/httpq:那套逻辑曾经在仓库里有七份各自漂移的拷贝,
// 其中五份没有整数上界 —— (page-1)*size 会溢出成负数再喂进 GORM 的 Offset()。

// listPaging 是本模块所有列表接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
var listPaging = httpq.Spec{}

// proofPaging 是证据链条目的分页口径。
//
// 上限比别处大:验证者需要**逐条**复算哈希链,一次只给 100 条会让一场
// 几千人的活动变成几十次请求。真正的全量导出走 ?format=ndjson。
var proofPaging = httpq.Spec{DefaultSize: 200, MaxSize: 1000}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

// respondErr 把内部错误翻译成稳定的响应信封。
//
// 未识别的错误一律降级为 500 且不回显原文:错误原文可能包含 SQL 片段与表结构,
// 那属于信息泄漏。
func respondErr(c *gin.Context, err error) {
	if be, ok := AsBizError(err); ok {
		body := gin.H{"success": false, "code": be.ErrCode(), "message": be.Message()}
		// 资格不足时必须把"差在哪"一并下发:一个冷冰冰的置灰按钮只会让用户
		// 反复重试并给客服发工单。
		if items := be.MissingItems(); len(items) > 0 {
			body["missing"] = items
		}
		c.JSON(be.HTTPStatus(), body)
		return
	}

	switch {
	case errors.Is(err, twophase.ErrInProgress):
		respondErr(c, errInProgress)
	case errors.Is(err, twophase.ErrOrderFailed):
		respondErr(c, errNotSettled)
	case errors.Is(err, twophase.ErrIdemConflict):
		respondErr(c, errIdemConflict)
	case errors.Is(err, twophase.ErrAmountOutOfRange):
		respondErr(c, errBadAmount)
	case errors.Is(err, ErrSubjectUnavailable):
		// fail-closed:风控条件的数据源读不到时一律拒绝,绝不回落成"不限制"。
		// 用 503 而不是 500,前端据此提示"稍后重试"而不是"出错了"。
		c.Header("Retry-After", "15")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": "qy_lot_eligibility_unavailable",
			"message": "资格校验暂时不可用,请稍后重试",
		})
	case errors.Is(err, db.ErrNotReady):
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "娱乐功能暂不可用,请稍后重试",
		})
	default:
		common.SysError("qianye/lottery: 接口处理失败: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
		})
	}
}
