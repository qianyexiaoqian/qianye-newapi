package withdraw

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// bizError 是提现对外暴露的业务错误。
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

// 参数与受理阶段错误。
var (
	errInvalidParam     = newBizError("qy_invalid_param", "请求参数不合法", http.StatusBadRequest)
	errIdemKeyRequired  = newBizError("qy_idem_key_required", "缺少 client_request_id", http.StatusBadRequest)
	errMethodNotAllowed = newBizError("qy_wd_method_not_allowed", "该提现方式未开放", http.StatusBadRequest)
	errAmountTooSmall   = newBizError("qy_wd_amount_too_small", "低于最低提现额度", http.StatusBadRequest)
	errAmountOutOfRange = newBizError("qy_wd_amount_out_of_range", "提现额度超出允许范围", http.StatusBadRequest)
	errRemarkTooLong    = newBizError("qy_wd_remark_too_long", "说明超出长度限制", http.StatusBadRequest)
	errPayeeRequired    = newBizError("qy_wd_payee_required", "请填写收款信息", http.StatusBadRequest)
	errPayeeInvalid     = newBizError("qy_wd_payee_invalid", "收款信息格式不正确", http.StatusBadRequest)
	errPayeeNotFound    = newBizError("qy_wd_payee_not_found", "收款方式不存在", http.StatusBadRequest)
	errPayeeLimit       = newBizError("qy_wd_payee_limit", "已达收款方式数量上限", http.StatusBadRequest)
	errReasonRequired   = newBizError("qy_wd_reason_required", "请填写理由", http.StatusBadRequest)
	errPayoutRefMissing = newBizError("qy_wd_payout_ref_required", "请填写打款单号", http.StatusBadRequest)
)

// 账户与余额错误。
var (
	errUserUnavailable   = newBizError("qy_wd_user_unavailable", "当前账号状态无法提现", http.StatusForbidden)
	errInsufficient      = newBizError("qy_wd_insufficient_commission", "可提现佣金不足", http.StatusBadRequest)
	errDebtBlocked       = newBizError("qy_wd_debt_blocked", "存在冲正欠账,暂不能提现", http.StatusBadRequest)
	errQuotaOverflow     = newBizError("qy_wd_quota_overflow", "您的账户余额已达上限,请先消费后再提现", http.StatusBadRequest)
	errDailyCountReached = newBizError("qy_wd_daily_count_reached", "已达今日提现次数上限", http.StatusTooManyRequests)
	errFiatBelowMin      = newBizError("qy_wd_fiat_below_min", "低于法币最低提现金额", http.StatusBadRequest)
	errFeeEatsAll        = newBizError("qy_wd_fee_eats_all", "扣除手续费后实付金额为 0", http.StatusBadRequest)
)

// 状态机与并发错误。
var (
	errNotFound          = newBizError("qy_wd_not_found", "提现单不存在", http.StatusNotFound)
	errStatusConflict    = newBizError("qy_wd_status_conflict", "该申请已被处理,请刷新后重试", http.StatusConflict)
	errIllegalTransition = newBizError("qy_wd_illegal_transition", "当前状态不允许该操作", http.StatusConflict)
	errInProgress        = newBizError("qy_wd_in_progress", "该请求正在处理中,请稍候", http.StatusConflict)
)

// 配置与密钥错误。
//
// 这些是运维事故而非用户错误,一律 500 并只回模糊文案 ——
// "PII 密钥长度不对"这种信息不该出现在用户浏览器里。
var (
	errPIIKeyUnavailable  = newBizError("qy_wd_pii_key_unavailable", "法币提现暂不可用,请联系管理员", http.StatusInternalServerError)
	errDigestKeyMissing   = newBizError("qy_wd_pii_key_unavailable", "法币提现暂不可用,请联系管理员", http.StatusInternalServerError)
	errRateUnavailable    = newBizError("qy_wd_rate_unavailable", "计费参数异常,请联系管理员", http.StatusInternalServerError)
	errPayeeUndecryptable = newBizError("qy_wd_payee_undecryptable", "收款信息无法解密,请联系用户重新提供", http.StatusBadRequest)
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
	case errors.Is(err, commission.ErrInsufficient):
		respondErr(c, errInsufficient)
	case errors.Is(err, commission.ErrDebtBlocked):
		respondErr(c, errDebtBlocked)
	case errors.Is(err, commission.ErrInvalidAmount):
		respondErr(c, errAmountOutOfRange)
	case errors.Is(err, twophase.ErrAmountOutOfRange):
		respondErr(c, errAmountOutOfRange)
	case errors.Is(err, twophase.ErrInProgress):
		respondErr(c, errInProgress)
	case errors.Is(err, db.ErrNotReady):
		// 提现是非热路径,库不可用时 fail-close。资金路径上的 fail-open 等于送钱。
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "提现服务暂不可用,请稍后重试",
		})
	default:
		common.SysError("qianye/withdraw: 接口处理失败: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
		})
	}
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}
