package transfer

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// bizError 是划转对外暴露的业务错误。
//
// Code 是给前端做 i18n 的机器可读标识,一旦发布就不能再改 —— 文案可以随便调,
// code 变了会让所有前端翻译静默回落到"未知错误"。
type bizError struct {
	Code   string
	Msg    string
	Status int
}

func (e *bizError) Error() string { return e.Code + ": " + e.Msg }

func newBizError(code, msg string, status int) *bizError {
	return &bizError{Code: code, Msg: msg, Status: status}
}

// 受理阶段(未触碰任何库)的错误。
var (
	errInvalidParam     = newBizError("qy_invalid_param", "请求参数不合法", http.StatusBadRequest)
	errConfirmRequired  = newBizError("qy_confirm_required", "请先确认划转不可撤销", http.StatusBadRequest)
	errIdemKeyRequired  = newBizError("qy_idem_key_required", "缺少 client_request_id", http.StatusBadRequest)
	errSelfTransfer     = newBizError("qy_self_transfer", "不能划转给自己", http.StatusBadRequest)
	errAmountOutOfRange = newBizError("qy_amount_out_of_range", "划转金额超出允许范围", http.StatusBadRequest)
)

// 用户与收款人状态相关的错误。
//
// 刻意全部用 400 而不是 404:guard 用 404 表示"功能未启用",
// 前端据此隐藏整个入口,收款人不存在若也返回 404 会被误判成功能被关掉。
var (
	errSenderDisabled    = newBizError("qy_sender_disabled", "当前账号状态无法发起划转", http.StatusForbidden)
	errAccountTooNew     = newBizError("qy_account_too_new", "账号注册时间过短,暂不能发起划转", http.StatusForbidden)
	errReceiverNotFound  = newBizError("qy_receiver_not_found", "收款人不存在", http.StatusBadRequest)
	errReceiverDisabled  = newBizError("qy_receiver_disabled", "该账号当前无法接收划转", http.StatusBadRequest)
	errReceiverOverflow  = newBizError("qy_receiver_overflow", "对方余额已达上限,无法接收本次划转", http.StatusBadRequest)
	errInsufficientQuota = newBizError("qy_insufficient_quota", "余额不足", http.StatusBadRequest)
)

// 风控错误。用 429 而不是 400:这些不是请求写错了,而是"现在不行,过会儿再来"。
var (
	errDailyLimitExceeded = newBizError("qy_daily_limit_exceeded", "已达今日划转额度上限", http.StatusTooManyRequests)
	errDailyCountExceeded = newBizError("qy_daily_count_exceeded", "已达今日划转次数上限", http.StatusTooManyRequests)
	errCooldown           = newBizError("qy_cooldown", "操作过于频繁,请稍后再试", http.StatusTooManyRequests)
	errPendingExists      = newBizError("qy_pending_exists", "你有一笔划转正在处理中,请稍后再试", http.StatusConflict)
)

// 两阶段中间态相关的错误。
var (
	errInProgress     = newBizError("qy_in_progress", "该请求正在处理中,请稍候", http.StatusConflict)
	errTransferFailed = newBizError("qy_transfer_failed", "该划转此前已失败", http.StatusBadRequest)
)

// respondErr 把内部错误翻译成稳定的响应信封。
//
// 未识别的错误一律降级为 500 且不回显原文:错误原文可能包含 SQL 片段与表结构,
// 那属于信息泄漏。
func respondErr(c *gin.Context, err error) {
	var be *bizError
	if errors.As(err, &be) {
		c.JSON(be.Status, gin.H{"success": false, "code": be.Code, "message": be.Msg})
		return
	}
	switch {
	case errors.Is(err, twophase.ErrInProgress):
		respondErr(c, errInProgress)
	case errors.Is(err, twophase.ErrOrderFailed):
		respondErr(c, errTransferFailed)
	case errors.Is(err, twophase.ErrAmountOutOfRange):
		respondErr(c, errAmountOutOfRange)
	case errors.Is(err, db.ErrNotReady):
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "划转服务暂不可用,请稍后重试",
		})
	default:
		common.SysError("qianye/transfer: 接口处理失败: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
		})
	}
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}
