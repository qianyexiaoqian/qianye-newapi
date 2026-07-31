package commission

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"

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

// pageParams 解析分页。上限 100:管理端一次拉全表会把内存打满。
func pageParams(c *gin.Context) (page, size int) {
	page = queryInt(c, "p", 1)
	if page < 1 {
		page = 1
	}
	size = queryInt(c, "page_size", 20)
	if size < 1 || size > 100 {
		size = 20
	}
	return page, size
}

func queryInt(c *gin.Context, key string, def int) int {
	v, err := strconv.Atoi(c.Query(key))
	if err != nil || v < 0 {
		return def
	}
	return v
}

func queryInt64(c *gin.Context, key string, def int64) int64 {
	v, err := strconv.ParseInt(c.Query(key), 10, 64)
	if err != nil || v < 0 {
		return def
	}
	return v
}
