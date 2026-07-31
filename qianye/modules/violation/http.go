package violation

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

func badRequest(c *gin.Context, msg string) {
	respondFail(c, http.StatusBadRequest, "qy_vio_bad_request", msg)
}

func notFound(c *gin.Context) {
	respondFail(c, http.StatusNotFound, "qy_vio_not_found", "记录不存在")
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/violation: 接口处理失败: " + err.Error())
	respondFail(c, http.StatusInternalServerError, "qy_internal_error", "处理失败,请稍后重试")
}

// listPaging 是违规相关列表接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 页长上限挡的是"管理端一次拉全表把内存打满"(违规记录表是所有扩展表里增长最快
// 的一张);页码上限(httpq.MaxPage)挡的是深翻页 —— 这份拷贝原本只有前者。
var listPaging = httpq.Spec{}

func pathInt64(c *gin.Context, key string) (int64, bool) {
	return httpq.PathInt64(c, key)
}
