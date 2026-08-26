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
	errBadOption = newBizError(http.StatusBadRequest, "qy_lot_bad_option", "选项不存在")
	errBadAmount = newBizError(http.StatusBadRequest, "qy_lot_bad_amount", "投注金额不符合本场规则")
	// errBadRequestID 同时承载长度与字符集两条:`#` 是多注提交派生幂等键的
	// 分隔符(api_user.go 的 batchRequestId),客户端再用它就会与服务端派生出的
	// 键撞进同一个键空间,拿回一张不属于这次提交的票。
	errBadRequestID = newBizError(http.StatusBadRequest, "qy_lot_bad_request_id",
		"请求标识非法(长度需在 1..64 之间,且不能含 # )")
	// errBadPhase 让大厅分区的参数名漂移**变成一次可见的失败**。它替换的是一个
	// 没有 default 分支的 switch:前端发 `status=open|done`、后端读 `phase`,
	// 两张标签因此返回同一份列表,而没有任何一处报错、没有一条日志。
	errBadPhase = newBizError(http.StatusBadRequest, "qy_lot_bad_phase",
		"大厅分区参数非法(只接受 live / ended)")
	// errBadLane 与 errBadPhase 同源同形:大厅那三张选择夹(抽奖 / 竞猜 /
	// 双色球)也是一个封闭集合,拼错一个字必须当场炸,而不是退回"三张标签
	// 拿同一份列表"。取值与 kind 长得像却不同义(lane=draw 排除双色球),
	// 正因如此更需要一道会响的闸门。
	errBadLane = newBizError(http.StatusBadRequest, "qy_lot_bad_lane",
		"大厅选择夹参数非法(只接受 draw / ball / guess)")
	// errPlayHidden 是"这一类玩法当前不对外开放"。
	//
	// 409 而不是 404:活动确实存在、详情页照常打得开、已参与的人照常能查票
	// 与领奖,不能对着一个还在跑的活动说"不存在"。文案里必须说清"已参与的
	// 不受影响",否则用户看到拒绝的第一反应是自己那笔钱出事了。
	errPlayHidden = newBizError(http.StatusConflict, "qy_lot_play_hidden",
		"该玩法当前未对外开放,暂不受理新的参与;已参与的记录与奖励不受影响")
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

// 三个与"净增发"有关的错误码。抽出成常量而不是在构造点写字面量:前端按 code
// 做 i18n(qy/api.ts 的 QY_ERROR_CODE 表),而带金额的那几条错误是在运行时
// 拼出来的 —— 字符串散在构造器里,漂移的方向恰好是前端认不出其中一份。
const (
	codePrizeCap         = "qy_lot_prize_cap"
	codeNetIssueConfirm  = "qy_lot_net_issue_confirm"
	codeNetIssueOverflow = "qy_lot_net_issue_overflow"
)

// prizeCapExceeded 是带金额的那一份"超过单场硬顶"。
func prizeCapExceeded(total, ceiling int64) *bizError {
	return &bizError{
		Status: http.StatusBadRequest,
		Code:   codePrizeCap,
		Msg: fmt.Sprintf("奖品总额 %s 超过本站设置的单场上限 %s。"+
			"这道上限是站点自己配的(lottery.max_total_prize_quota),"+
			"配成 0 即为不限制,届时超过阈值的活动改走二次确认",
			quotaText(total), quotaText(ceiling)),
	}
}

// errNetIssueOverflow 是**算术**护栏,不是业务上限:Σ(count × amount) 逼近
// int64 上界时必须在这里停住,否则它会绕回负数,而一个负的总额会让后面每一道
// 判定连同二次确认一起静默通过。见 caps.go 的 netIssueOverflowGuard。
var errNetIssueOverflow = newBizError(http.StatusBadRequest, codeNetIssueOverflow,
	"奖品总额大到了整数运算的边界,已拒绝 —— 请检查奖品数量与单档额度,这几乎一定是配错了")

var (
	errStatusConflict = newBizError(http.StatusConflict, "qy_lot_status_conflict",
		"活动状态已变化,请刷新后重试")
	// errUpdateNotDraft 与 errStatusConflict 分开,理由与删除那八个 code 分开
	// 是同一条:两句话要求运营做的下一步完全不同。「状态已变化」在说"刷新一下
	// 再试",而这一条在说"这件事从此不可能做到了,封面之外一个字都改不了" ——
	// 塌成前者的表现是运营反复刷新、反复重试一件永远不会成功的事。
	errUpdateNotDraft = newBizError(http.StatusConflict, "qy_lot_update_not_draft",
		"只有草稿能改:发布那一刻参与条件、奖档、选项、四个时刻与费率全部进了 commit_hash,"+
			"改任何一项都会让已经公开的承诺变成谎言。唯一的例外是封面(它不进任何哈希原像),"+
			"走「换封面」;要改别的只能整场取消后重开一场")
	// errCancelDraft:草稿不能被「整场取消」。
	//
	// 取消原本是草稿唯一的处置路径(那时既不能改也不能删),现在两条都有了,
	// 而它留在草稿上是纯粹的伤害:一份从没对外公布过的活动被 cancel 之后会
	// status=finished / outcome=cancelled,于是
	//
	//   ① 永久出现在用户端大厅的「已结束」里 —— 大厅口径是 `status <> 'draft'`,
	//      状态一旦离开 draft 就再没有第二道判据;
	//   ② 匿名证据链端点开始下发它的 rules_text、spec_hash 与**随机种子**
	//      (seedShouldBeRevealed 只看 settling/finished),而 commit_hash 恒为
	//      空串 —— 产出一份"公开了种子、却从来没有过任何承诺"的记录,
	//      自带的 lottery-verify.py 当场判 FAIL;
	//   ③ 它从此不再是草稿,零仪式的草稿删除对它失效,运营被迫改走
	//      「原样回填活动编号 + 必填理由 + 六道闸门」那一档。
	//
	// 而这三件事换来的是零 —— 草稿上不可能有任何参与、任何出款、任何要退的钱
	// (报名的原子 UPDATE 带着 status='published'),取消对它没有任何止损作用。
	errCancelDraft = newBizError(http.StatusConflict, "qy_lot_cancel_draft",
		"草稿不用取消:它还没有对任何人公布过,没有参与、没有扣款、没有要退的钱。"+
			"写错了直接「编辑草稿」,不想要了直接「删除草稿」。"+
			"对草稿点取消只会把它变成一场公开的、已结束的空活动,并把它的随机种子公开出去")
	errResultLocked = newBizError(http.StatusConflict, "qy_lot_result_locked",
		"结果已录入且不可修改;录错只能整场作废并全额退款")
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
