package channelops

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// bizError 与 apiaddr / paypass / transfer 的同名类型同形,刻意保持包私有:
// 把它提到公共包意味着所有模块的错误码空间从此耦合。必须一致的只有信封形状,
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

// 整批级别的失败:这类错误让整个请求 4xx,一条都不会执行。
var (
	errInvalidParam = newBizError("qy_chops_invalid_param",
		"请求参数不合法", http.StatusBadRequest)
	errNoIds = newBizError("qy_chops_no_ids",
		"请先选择要操作的渠道", http.StatusBadRequest)
	errTooMany = newBizError("qy_chops_too_many",
		"一次最多操作 200 个渠道,请分批进行", http.StatusBadRequest)
	errBadStatus = newBizError("qy_chops_bad_status",
		"只能批量启用或手动停用渠道", http.StatusBadRequest)
	// errNothingToReset 单独成码而不是并进 errInvalidParam:两者要求管理员做的
	// 下一步完全不同 —— 前者是"勾一个要清的项",后者是"这个请求本身有问题"。
	errNothingToReset = newBizError("qy_chops_nothing_to_reset",
		"请至少选择一项要重置的数据", http.StatusBadRequest)
)

// 单条级别的失败码。它们**不**走 respondErr —— 整批仍然返回 200,
// 这些码落在 batchResult.Items[i].Code 里,由前端逐条渲染。
//
// 分成四个码而不是一句"失败":它们要求管理员做的下一步完全不同 ——
// 已被别人删掉的那条只需刷新列表,数据库错误要看后端日志,
// 超时中断的那批可以原样重试,而"已经是目标状态"根本不是失败。
const (
	itemCodeNotFound = "qy_chops_item_not_found"
	itemCodeDBError  = "qy_chops_item_db_error"
	itemCodeAborted  = "qy_chops_item_aborted"
	itemCodeNoChange = "qy_chops_item_no_change"
	// itemCodeNotApplied 是「状态没变,但后端一行日志都没有」。
	//
	// 它专门对应 model.UpdateChannelStatus 那三条**不写日志**就返回 false 的路径:
	// 开着内存缓存时本节点缓存里没有这个渠道(它在别的节点上刚建出来,
	// 本节点最长 SYNC_FREQUENCY 秒后才同步到)、缓存里的状态已经等于目标值
	// (别的节点刚自动停用过它,本节点缓存还没跟上)、以及 GetChannelById 失败。
	//
	// 把这三条一律翻译成 itemCodeDBError(「请查看后端日志」)是在说假话:
	// 后端日志里什么都没有,管理员会去翻一份空日志,重试还会得到同一句话。
	// 这一档告诉他真正的下一步:这是本节点缓存与库不一致,等一个同步周期再来。
	itemCodeNotApplied = "qy_chops_item_not_applied"
)

// errChannelGone 是"读到了但写的时候行已经不在"。
// 它只在单条路径里出现,永远不会变成整批的 4xx。
var errChannelGone = errors.New("channel row disappeared before write")

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
	common.SysError("qianye/channelops: 接口处理失败: " + err.Error())
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}
