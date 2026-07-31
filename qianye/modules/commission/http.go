package commission

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
)

// 响应信封与扩展其余部分保持一致:{success, message, data},
// 失败时额外带 code 供前端做 i18n 映射,不把中文写死在前端。
func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func respondFail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": msg})
}

func badRequest(c *gin.Context, code, msg string) {
	respondFail(c, http.StatusBadRequest, code, msg)
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/commission: 接口处理失败: " + err.Error())
	respondFail(c, http.StatusInternalServerError, "qy_internal_error", "处理失败,请稍后重试")
}

// listPaging 是佣金相关列表接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 页长上限挡的是"管理端一次拉全表把内存打满";页码上限(httpq.MaxPage)挡的是
// 深翻页 —— 这份拷贝原本只有前者,而 /commission/records 是用户端接口。
var listPaging = httpq.Spec{}
