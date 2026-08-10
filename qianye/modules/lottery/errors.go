package lottery

import (
	"errors"
	"fmt"
	"net/http"
)

// errors.go —— 本模块对外的错误集合。
//
// 每个错误都带一个稳定的英文 code:前端按 code 做 i18n(t('qy_lot_...')),
// message 只是兜底。绝不把中文硬编码当契约 —— 那会让文案改动变成接口变更。

// bizError 是可以安全回给用户的业务错误。
//
// 与"内部错误"严格区分:内部错误一律 500 + 通用文案,绝不把数据库报错原文
// 透给用户(那会泄漏表结构与列名)。
type bizError struct {
	Code   string
	Msg    string
	Status int
	// Missing 只在 ineligible 上非空,承载"我为什么不能参加"的缺失项清单。
	// 用户看到的是一份清单而不是一个冷冰冰的置灰按钮。
	Missing []Missing
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

// HTTPStatus / ErrCode / Message / MissingItems 是给 HTTP 层用的读取器。
// 做成方法而不是导出字段:handler 不该有能力改写一个已经产生的错误。
func (e *bizError) HTTPStatus() int { return e.Status }
func (e *bizError) ErrCode() string { return e.Code }
func (e *bizError) Message() string { return e.Msg }
func (e *bizError) MissingItems() []Missing {
	if e.Missing == nil {
		// 下发给前端的数组永远不能是 JSON null(见 qianye/json_array_guard_test.go)。
		return make([]Missing, 0)
	}
	return e.Missing
}

// ─────────────────────────── 受理与资格 ───────────────────────────

var (
	errActivityNotFound = newBizError(http.StatusNotFound, "qy_lot_not_found", "活动不存在")
	errNotOpen          = newBizError(http.StatusConflict, "qy_lot_not_open", "活动当前不接受参与")
	errClosingSoon      = newBizError(http.StatusConflict, "qy_lot_closing_soon",
		"活动即将截止,已停止受理新的参与")
	errCapReached = newBizError(http.StatusConflict, "qy_lot_cap_reached", "本场名额已满")
	errUserCap    = newBizError(http.StatusConflict, "qy_lot_user_cap",
		"你在本场的参与次数已达上限")
	errAttemptCap = newBizError(http.StatusConflict, "qy_lot_attempt_cap",
		"你在本场的提交次数已达上限(含失败的尝试)")
	errCooldown = newBizError(http.StatusConflict, "qy_lot_cooldown",
		"两次参与之间需要间隔一段时间,请稍后再试")
	errInviterCap = newBizError(http.StatusConflict, "qy_lot_inviter_cap",
		"同一邀请人名下的参与人数已达上限")
	errIPCap = newBizError(http.StatusConflict, "qy_lot_ip_cap",
		"本活动限制同一网络出口只能有一位参与者")
	// errEntryInFlight 是**资金正确性条件**,不是风控偏好:同一用户在本活动上
	// 还有未结算的参与时,余额与名单的差额无法归因到哪一笔。
	errEntryInFlight = newBizError(http.StatusConflict, "qy_lot_entry_in_flight",
		"你上一次参与还在处理中,请稍候再试")
	errIneligible = newBizError(http.StatusUnprocessableEntity, "qy_lot_ineligible",
		"你还不满足本场的参与条件")
	errBadOption    = newBizError(http.StatusBadRequest, "qy_lot_bad_option", "选项不存在")
	errBadAmount    = newBizError(http.StatusBadRequest, "qy_lot_bad_amount", "投注金额不符合本场规则")
	errBadRequestID = newBizError(http.StatusBadRequest, "qy_lot_bad_request_id",
		"请求标识非法(长度需在 1..64 之间)")
)

// 管理员与活动创建者禁止参与这条硬规则**不在这里**,而是 Evaluate 里的
// MissAdmin / MissCreator 两个缺失项(见 eligibility.go)。
//
// 刻意只保留一条通路:另开一个专用错误意味着同一条规则有两个判定点,
// 而其中一个迟早会在重构中被漏掉 —— 那一次漏掉的后果是掌握种子的人
// 可以合法下场,commit-reveal 的固有弱点当场变成可利用的漏洞。

// ─────────────────────────── 幂等与结算 ───────────────────────────

var (
	errInProgress = newBizError(http.StatusConflict, "qy_lot_in_progress",
		"该请求正在处理中,请稍候查看参与记录")
	errIdemConflict = newBizError(http.StatusConflict, "qy_lot_idem_conflict",
		"本次提交与此前同一请求标识的内容不一致,请刷新后重试")
	// errNotSettled 是本模块最重要的一条错误。
	//
	// twophase.Execute 返回 nil **不代表**业务侧已落定:主库已扣款而扩展库回写
	// 失败时,它同样返回 (order, nil)(execute.go 的阶段四只 SysError 然后
	// return order, nil)。宁可让用户看到"尚未落定,请稍后在记录中复核",
	// 也绝不对外声称一笔并不存在的扣费或派奖 —— 后者在本仓的 violation 上
	// 真实发生过一次。
	errNotSettled = newBizError(http.StatusAccepted, "qy_lot_not_settled",
		"处理尚未落定,请稍后在参与记录中复核")
	// errEntryExcluded 是"扣费成功但已错过封盘"的诚实回答。费用会由结算任务
	// 按资金单的终态自动退回,绝不在这里投机性地宣称已退。
	errEntryExcluded = newBizError(http.StatusConflict, "qy_lot_entry_excluded",
		"本次参与在封盘之后才完成扣费,费用将自动退回")
	errInsufficientQuota = newBizError(http.StatusConflict, "qy_lot_insufficient_quota",
		"余额不足")
	errUserUnavailable = newBizError(http.StatusForbidden, "qy_lot_user_unavailable",
		"账号状态不允许本次操作")
	errQuotaOverflow = newBizError(http.StatusConflict, "qy_lot_quota_overflow",
		"到账后余额会超出系统上限,请先消耗一部分额度")
)

// ─────────────────────────── 管理端 ───────────────────────────

var (
	errStatusConflict = newBizError(http.StatusConflict, "qy_lot_status_conflict",
		"活动状态已变化,请刷新后重试")
	errResultLocked = newBizError(http.StatusConflict, "qy_lot_result_locked",
		"结果已录入且不可修改;录错只能整场作废并全额退款")
	errPrizeCapExceeded = newBizError(http.StatusBadRequest, "qy_lot_prize_cap",
		"奖品总额度超过系统上限")
	errActiveCapExceeded = newBizError(http.StatusConflict, "qy_lot_active_cap",
		"同时进行中的活动数量已达上限")
	errFlagNotFound = newBizError(http.StatusNotFound, "qy_lot_flag_not_found",
		"找不到该对账异常")
	errFlagAlreadyResolved = newBizError(http.StatusConflict, "qy_lot_flag_resolved",
		"该对账异常已被处理")
	// errCommitMismatch 触发时**绝不以种子为准继续开奖**。它拦得住"改了种子
	// 忘了改哈希"这一类真实事故,也拦得住有人直接改库。
	errCommitMismatch = errors.New("qianye/lottery: 种子与承诺哈希不一致,拒绝开奖")
	// errRosterDrift 与上一条分开:名单漂移时种子与承诺完全没动,把它报成
	// "承诺哈希不一致"会把排障方向指反 —— 而这是一场所有人的钱都被冻住的事故,
	// 第一小时找错方向的代价很高。
	errRosterDrift = errors.New("qianye/lottery: 名单与封盘时的快照不一致,拒绝开奖")
	// errSpendNotReady 把"近 N 日消费"的冷启动误拒挡在配置阶段,
	// 而不是等到用户报名时才发现全员不合格。
	errSpendNotReady = newBizError(http.StatusBadRequest, "qy_lot_spend_not_ready",
		"消费统计尚未回填到该时间窗,请缩小「近 N 日消费」条件或稍后再创建")
)

// ineligibleWith 把缺失项清单包进 errIneligible 的一份副本。
//
// 必须复制而不是改写包级变量:那是一个全局单例,改它等于让并发的另一个请求
// 看到别人的缺失项。
func ineligibleWith(missing []Missing) *bizError {
	cp := *errIneligible
	cp.Missing = missing
	return &cp
}

// wrapInternal 把内部错误包成带上下文的错误,供日志定位。
// 它**不是** bizError,因此永远不会被原样回给用户。
func wrapInternal(stage string, err error) error {
	return fmt.Errorf("qianye/lottery: %s: %w", stage, err)
}
