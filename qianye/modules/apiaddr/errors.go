package apiaddr

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// bizError 是本模块对外暴露的业务错误。
//
// 与 paypass / transfer 的同名类型一样是包私有的:把它提到公共包意味着所有模块
// 的错误码空间从此耦合。必须一致的只有信封形状(success/code/message),
// 而那由 respondErr / respondOK 一处渲染。
type bizError struct {
	Code   string
	Msg    string
	Status int
}

func (e *bizError) Error() string { return e.Code + ": " + e.Msg }

func newBizError(code, msg string, status int) *bizError {
	return &bizError{Code: code, Msg: msg, Status: status}
}

// 校验类错误。
//
// # 为什么 URL 的四种非法各占一个 code
//
// 它们要求管理员做的下一步完全不同:少了 scheme 是补 `https://`,带了凭据是
// 把 `user:pass@` 删掉,带了查询串是把 `?token=…` 删掉,超长是换个短地址。
// 合并成一句"URL 不合法"之后,最常见的那种(直接粘了带 `?` 的完整请求地址)
// 会让人对着输入框反复试。
var (
	errInvalidParam = newBizError("qy_apiaddr_invalid_param",
		"请求参数不合法", http.StatusBadRequest)
	errNameRequired = newBizError("qy_apiaddr_name_required",
		"请填写地址名称", http.StatusBadRequest)
	errNameTooLong = newBizError("qy_apiaddr_name_too_long",
		"地址名称过长", http.StatusBadRequest)
	errRemarkTooLong = newBizError("qy_apiaddr_remark_too_long",
		"备注过长", http.StatusBadRequest)
	errURLRequired = newBizError("qy_apiaddr_url_required",
		"请填写 API 地址", http.StatusBadRequest)
	errURLTooLong = newBizError("qy_apiaddr_url_too_long",
		"API 地址过长", http.StatusBadRequest)
	errURLScheme = newBizError("qy_apiaddr_url_scheme",
		"API 地址必须以 http:// 或 https:// 开头", http.StatusBadRequest)
	errURLCredentials = newBizError("qy_apiaddr_url_credentials",
		"API 地址中不能包含账号密码", http.StatusBadRequest)
	errURLExtra = newBizError("qy_apiaddr_url_extra",
		"API 地址不能带查询参数或 # 片段,只填到域名/端口即可", http.StatusBadRequest)
	errURLInvalid = newBizError("qy_apiaddr_url_invalid",
		"API 地址格式不正确", http.StatusBadRequest)
	errDuplicateURL = newBizError("qy_apiaddr_duplicate",
		"该 API 地址已存在", http.StatusConflict)
	errLimitReached = newBizError("qy_apiaddr_limit",
		"API 地址数量已达上限", http.StatusBadRequest)
	errNotFound = newBizError("qy_apiaddr_not_found",
		"该 API 地址不存在,可能已被其他管理员删除", http.StatusNotFound)
	// errOrderStale 是整表重排唯一可能的失败:提交的 id 集合与库里当前的对不上。
	// 它必须与"参数不合法"分开 —— 前者的正确处置是刷新页面重来(有人在你编辑
	// 期间加了/删了一条),后者是改输入。
	errOrderStale = newBizError("qy_apiaddr_order_stale",
		"列表已被其他管理员改动,请刷新后重新排序", http.StatusConflict)
)

// respondErr 把内部错误翻译成稳定的响应信封,并 Abort。
//
// 未识别的错误一律降级为 500 且不回显原文:错误原文可能包含 SQL 片段与表结构。
func respondErr(c *gin.Context, err error) {
	var be *bizError
	if errors.As(err, &be) {
		c.AbortWithStatusJSON(be.Status, gin.H{
			"success": false, "code": be.Code, "message": be.Msg,
		})
		return
	}
	if errors.Is(err, db.ErrNotReady) {
		c.Header("Retry-After", "30")
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable,
			"message": "服务暂不可用,请稍后重试",
		})
		return
	}
	common.SysError("qianye/apiaddr: 接口处理失败: " + err.Error())
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}
