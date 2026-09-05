package stardust

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
)

// http.go —— 本模块唯一的响应信封与错误翻译(形状照 lottery/http.go)。
//
// 本包的接口分散在 api_user.go / api_admin_*.go / api_admin_settle.go 几个文件里,
// 由不同的人并行写;信封与翻译只能有这一份,谁都不许再抄一个 respondOK。

// listPaging 是本模块所有列表接口的分页口径:?page= / ?page_size=,默认 20、上限 100。
//
// 页码参数名显式写 page:契约(stardust-api-contract §0)定的是 page(从 1),
// 而 httpq.Spec 的零值是 ?p= —— 零值会让前端发的 page=3 被静默当成第 1 页。
var listPaging = httpq.Spec{PageKey: "page"}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

// respondErr 把内部错误翻译成稳定的响应信封。
//
// 未识别的错误一律降级为 500 且不回显原文:错误原文可能包含 SQL 片段与表结构,
// 那属于信息泄漏。账本哨兵(ErrInsufficient 等)先经 BizOf 变成带 code 的业务错误。
func respondErr(c *gin.Context, err error) {
	err = BizOf(err)
	if be, ok := AsBizError(err); ok {
		c.JSON(be.HTTPStatus(), gin.H{"success": false, "code": be.ErrCode(), "message": be.Message()})
		return
	}
	if errors.Is(err, db.ErrNotReady) {
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "星屑功能暂不可用,请稍后重试",
		})
		return
	}
	common.SysError("qianye/stardust: 接口处理失败: " + err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}
