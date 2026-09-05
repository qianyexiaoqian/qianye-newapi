package stardust

import (
	"errors"
	"fmt"
	"net/http"
)

// errors.go —— 本模块对外的错误集合。
//
// 每个错误都带一个稳定的英文 code:前端按 code 做 i18n(t('qy_sd_...')),
// message 只是兜底。绝不把中文硬编码当契约 —— 那会让文案改动变成接口变更。
// 形状照 lottery/errors.go。

// bizError 是可以安全回给用户的业务错误。
//
// 与"内部错误"严格区分:内部错误一律 500 + 通用文案,绝不把数据库报错原文
// 透给用户(那会泄漏表结构与列名)。
type bizError struct {
	Code   string
	Msg    string
	Status int
}

func (e *bizError) Error() string { return e.Code + ": " + e.Msg }

func newBizError(status int, code, msg string) *bizError {
	return &bizError{Code: code, Msg: msg, Status: status}
}

// AsBizError 把错误还原成可回给用户的形状。第二个返回值为 false 时,
// 调用方必须按内部错误处理(500 + 通用文案 + SysError)。
func AsBizError(err error) (*bizError, bool) {
	var be *bizError
	if errors.As(err, &be) {
		return be, true
	}
	return nil, false
}

// HTTPStatus / ErrCode / Message 是给 HTTP 层用的读取器。
// 做成方法而不是导出字段:handler 不该有能力改写一个已经产生的错误。
func (e *bizError) HTTPStatus() int { return e.Status }
func (e *bizError) ErrCode() string { return e.Code }
func (e *bizError) Message() string { return e.Msg }

// 稳定的 code。抽成常量是因为其中几条要在运行时带上单位名或金额拼出来 ——
// 字符串散在构造点里,漂移的方向恰好是前端认不出其中一份。
const (
	codeInsufficient       = "qy_sd_insufficient"
	codeOverflow           = "qy_sd_overflow"
	codeComplianceRequired = "qy_sd_compliance_required"
	codeGroupUnknown       = "qy_sd_group_unknown"
	codeBadSource          = "qy_sd_bad_source"
	codeAdjustTooLarge     = "qy_sd_adjust_too_large"
	codeBadSetting         = "qy_sd_bad_setting"
	codeBadRequest         = "qy_sd_bad_request"
	// codeIdemConflict 与 transfer 同名:同一个 client_request_id 换了
	// 参数重放,前端已有这一条的文案。
	codeIdemConflict = "qy_idem_key_conflict"
)

var (
	// errComplianceRequired:邀请类奖励(invite_topup_bps / invite_redeem_bps /
	// invite_register_stardust)收到正值,而系统设置里的支付合规声明还没确认。
	// 星屑能在商城换套餐,有真实价值,与上游的邀请奖励同一道门(D-G)。
	errComplianceRequired = newBizError(http.StatusBadRequest, codeComplianceRequired,
		"邀请类奖励需要先在「系统设置」确认支付合规声明;未确认时这三项一律按 0 生效")
	// errGroupUnknown:分组费率只接受已登记于 qy_user_groups 的分组名,
	// 让"费率行挂在未登记名字上"不可能出现。
	errGroupUnknown = newBizError(http.StatusBadRequest, codeGroupUnknown,
		"该用户分组未登记,请先在「用户分组」里创建它")
	errBadSource = newBizError(http.StatusBadRequest, codeBadSource,
		"套餐返还来源只接受 order / balance / admin / redemption 的组合")
	errIdemConflict = newBizError(http.StatusConflict, codeIdemConflict,
		"该请求标识已被另一次不同参数的操作占用,请刷新后重新发起")
)

// errInsufficient 是"星屑不足"。
//
// 单位名是运营可改的(stardust.name),兜底文案带上当前生效的名字;前端仍按 code 翻译。
func errInsufficient() *bizError {
	return newBizError(http.StatusBadRequest, codeInsufficient, UnitName()+"不足")
}

// errOverflow 是"到账后余额会超出系统上界"。
// 409 而不是 400:请求本身合法,是账户当前的状态容不下它。
func errOverflow() *bizError {
	return newBizError(http.StatusConflict, codeOverflow,
		"到账后"+UnitName()+"会超出系统上界,请先消耗一部分")
}

// errAdjustTooLarge 是带上限的那一份"手调超过 stardust.max_manual_adjust"。
func errAdjustTooLarge(limit int64) *bizError {
	return newBizError(http.StatusBadRequest, codeAdjustTooLarge,
		fmt.Sprintf("单次手调的绝对值不得超过 %d %s(stardust.max_manual_adjust)", limit, UnitName()))
}

// errBadSetting 是运营参数写入被拒的那一份,reason 说清是哪一项、为什么。
func errBadSetting(reason string) *bizError {
	return newBizError(http.StatusBadRequest, codeBadSetting, reason)
}

// errBadRequest 是通用的参数不合法。
func errBadRequest(msg string) *bizError {
	return newBizError(http.StatusBadRequest, codeBadRequest, msg)
}

// BizOf 把账本层的哨兵错误翻译成可回给用户的业务错误;其它错误原样返回。
//
// 只翻译"用户能改变什么"的那几条:星屑不足、余额超上界、金额非法。ErrBadKind /
// ErrBadPosting 是调用方的编程错误,原样留成内部错误(500 + SysError),
// 翻成 400 只会让一个真正的缺陷被当成用户输错了。
func BizOf(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInsufficient):
		return errInsufficient()
	case errors.Is(err, ErrOverflow):
		return errOverflow()
	case errors.Is(err, ErrBadAmount):
		return errBadRequest("金额必须是 1 到系统上界之间的整数")
	}
	return err
}

// wrapInternal 把内部错误包成带上下文的错误,供日志定位。
// 它**不是** bizError,因此永远不会被原样回给用户。
func wrapInternal(stage string, err error) error {
	return fmt.Errorf("qianye/stardust: %s: %w", stage, err)
}
