package withdraw

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

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
	errPayoutRefMissing = newBizError("qy_wd_payout_ref_required", "请填写发放凭证", http.StatusBadRequest)

	// 实发金额是「标记已发放」的必填确认项,见 checkPayoutConfirm。
	//
	// 两个 code 分开:漏填是管理员少填了一个框,填错是他手上的数和单据上的数
	// 对不上 —— 后者必须让人停下来去核对,而不是再点一次。
	errPayoutAmountRequired = newBizError("qy_wd_payout_amount_required",
		"请填写实际发放金额", http.StatusBadRequest)
	errPayoutAmountMismatch = newBizError("qy_wd_payout_amount_mismatch",
		"实际发放金额与单据金额不一致,请核对后再标记", http.StatusBadRequest)
)

// 凭证图片错误。
//
// 分得比"上传失败"细,是因为这四种情况用户能做的事完全不同:换一张图、
// 压缩一下、先去用掉已传的、还是找管理员 —— 合并成一条只会让人反复重试同一张图。
var (
	errProofDisabled     = newBizError("qy_wd_proof_disabled", "当前站点未开放上传凭证", http.StatusBadRequest)
	errProofRequired     = newBizError("qy_wd_proof_required", "请选择要上传的图片", http.StatusBadRequest)
	errProofTooLarge     = newBizError("qy_wd_proof_too_large", "图片超出大小上限", http.StatusRequestEntityTooLarge)
	errProofType         = newBizError("qy_wd_proof_type", "只接受 JPEG / PNG / WebP 图片", http.StatusBadRequest)
	errProofNotFound     = newBizError("qy_wd_proof_not_found", "凭证不存在或已被使用", http.StatusBadRequest)
	errProofPendingLimit = newBizError("qy_wd_proof_pending_limit",
		"已有多张上传好的凭证尚未提交,请先完成申请", http.StatusBadRequest)
	// errProofPurged 与 errProofNotFound 分开:前者是"确实传过,但按保留期
	// (或单据被拒绝/撤销)已经清理了",后者是"从来没有这张"。
	// 合成一条会让管理员在争议单上误以为用户根本没提交过凭证。
	errProofPurged = newBizError("qy_wd_proof_purged", "凭证已按保留期清理,无法再查看", http.StatusGone)
	errProofStore  = newBizError("qy_wd_proof_store_failed", "凭证保存失败,请稍后重试", http.StatusInternalServerError)
)

// 账户与余额错误。
var (
	errUserUnavailable   = newBizError("qy_wd_user_unavailable", "当前账号状态无法提现", http.StatusForbidden)
	errInsufficient      = newBizError("qy_wd_insufficient_commission", "可提现佣金不足", http.StatusBadRequest)
	errDebtBlocked       = newBizError("qy_wd_debt_blocked", "存在冲正欠账,暂不能提现", http.StatusBadRequest)
	errDailyCountReached = newBizError("qy_wd_daily_count_reached", "已达今日提现次数上限", http.StatusTooManyRequests)
	errFiatBelowMin      = newBizError("qy_wd_fiat_below_min", "低于法币最低提现金额", http.StatusBadRequest)
	errFeeEatsAll        = newBizError("qy_wd_fee_eats_all", "扣除手续费后实付金额为 0", http.StatusBadRequest)
	// errFiatUnavailable 与 errInsufficient 刻意分开:后者是"你没这么多佣金",
	// 前者是"你的佣金在账本上折不出一个正的法币值"(available_fiat = 0 而额度是正的)。
	// 合成一条会让用户对着一个明明有余额的账号反复重试,而真正要做的是让管理员
	// 去补佣金法币折算档 —— 用户自己一步都改不了,所以是 500 而不是 400。
	errFiatUnavailable = newBizError("qy_wd_fiat_unavailable",
		"该笔佣金暂时无法折算为法币,请联系管理员或改用站内额度提现", http.StatusInternalServerError)
)

// 申请阶段的风控闸门(withdraw.max_quota_per_order / daily_max_quota /
// cooldown_seconds / max_pending_orders)。
//
// 这四项配置此前定义了、校验了、赋了默认值,却没有任何消费方 ——
// 运维完全无法察觉闸门是空的。文案一律说清"是哪一道闸拦的",
// 否则用户只会反复重试同一个必然失败的请求。
var (
	errDailyQuotaReached  = newBizError("qy_wd_daily_quota_reached", "已达今日提现额度上限", http.StatusTooManyRequests)
	errDailySubmitReached = newBizError("qy_wd_daily_submit_reached", "今日提交的申请过多,请明天再试", http.StatusTooManyRequests)
	errCooldown           = newBizError("qy_wd_cooldown", "两次提现申请之间需要间隔一段时间,请稍后再试", http.StatusTooManyRequests)
	errPendingLimit       = newBizError("qy_wd_pending_limit", "存在过多未完成的提现申请,请等待处理完成", http.StatusTooManyRequests)
	// errIdemConflict:同一个 client_request_id 换了金额或方式再提交。
	// 这不是重复提交,而是"用同一个键提交了另一笔申请",绝不能把原单的结果
	// 当成本次的结果返回 —— 用户会把"原来那笔 300 的单"读成"我这笔 500 成功了"。
	errIdemConflict = newBizError("qy_wd_idem_conflict", "该请求标识已用于另一笔申请,请刷新后重试", http.StatusConflict)
)

// 状态机与并发错误。
var (
	errNotFound          = newBizError("qy_wd_not_found", "提现单不存在", http.StatusNotFound)
	errStatusConflict    = newBizError("qy_wd_status_conflict", "该申请已被处理,请刷新后重试", http.StatusConflict)
	errIllegalTransition = newBizError("qy_wd_illegal_transition", "当前状态不允许该操作", http.StatusConflict)

	// 这里曾经有 errNotFiatOrder / errNotQuotaOrder 两条方式闸门。它们守的是
	// 「quota 单有一条会真的加额度的正向出口,别让人误用只核销不给钱的那一条」。
	// 自动到账下线之后两种方式的正向出口是同一件事(人发钱、系统记账),
	// 那道闸门挡的是一条不存在的错路,而它原本要防的"误标记 = 佣金被吃掉"
	// 换成了 markPayout 里的凭证必填 + 实发金额复核。
	//
	// errSelfReview 是自审自批闸门。见 loadDecidableWithdrawal 与
	// qianye/guard/fund_actor.go —— 提现单的**每一个**人工决定都要经过它,
	// 包括退回佣金的那几个:四眼原则管的是"谁有资格对这张单下结论",
	// 不是"这次结论对申请人有没有好处"。申请人自己想撤单仍然走用户端的
	// POST /withdraw/:id/cancel,不需要管理员身份,所以这道闸门不会把
	// 任何人的单据锁死。
	errSelfReview = newBizError("qy_wd_self_review",
		"不能审核自己提交的提现申请,请由另一位管理员处理", http.StatusForbidden)

	// errPeerReview 是自审自批之外的另一半。errSelfReview 只挡得住"同一个人"
	// 的闭环;两个 role=10 管理员互相批对方的提现单,它一次都不会响。
	// 佣金那一侧(balances/adjust、balances/withdrawn、relations/bind)已经按
	// 上游 canManageTargetRole 挡住了"给同级记账",提现这一侧却仍然放行
	// "给同级放行",于是整条链只是从一个人变成两个人。
	//
	// 判据与佣金那边逐字同一条(guard.ManageableTarget):root 谁都能批,
	// 其余角色只能批**严格低于**自己的账号。
	errPeerReview = newBizError("qy_wd_peer_review",
		"不能审核同级或更高权限账号提交的提现申请,请由更高权限的管理员处理", http.StatusForbidden)

	// errDebtBlockedPayout 是**放款侧**的欠账闸门。
	//
	// 建单侧一直有 errDebtBlocked(Withdrawable/FreezeForWithdraw 都判),
	// 放款侧原先一道都没有:approve 与 markPayout 只看单据状态与操作人身份。
	// 于是「先提现冻住 → 下线退款触发冲正把可用池吃穿 → 管理员照常审批放款」
	// 整段畅通,而且放出去的钱可以大于欠账本身(冲正够不到已冻额度,
	// 差额全进 unsettled 负结转,四桶恒等式照样成立,没有任何一条对账会变红)。
	//
	// 徽标(debt_blocked / unsettled_amount)确实一直下发到列表与详情,
	// 但它是**唯一**的阻力 —— 靠审核人自己看见并自行决定不放款。
	//
	// 只挡「往外放钱」的两步(approve / mark-paid)。驳回、发放失败、用户撤单
	// 都是把佣金退回可用池的方向,正好是冲正能吃到的地方,一律不挡 ——
	// 挡住它们等于把欠账用户的单据永久钉死在队列里。
	errDebtBlockedPayout = newBizError("qy_wd_debt_blocked_payout",
		"收款人存在冲正欠账,放款已被拦下。请先在佣金余额页把欠账处理掉,或驳回这张单",
		http.StatusConflict)

	// errDebtStatusUnknown:读不出欠账状态时一律不放款。
	// 这是一道资金闸门,「读不到」不是「没有欠账」。
	errDebtStatusUnknown = newBizError("qy_wd_debt_status_unknown",
		"暂时读不到收款人的佣金账本状态,放款已被拦下,请稍后重试", http.StatusServiceUnavailable)
)

// 配置与密钥错误。
//
// 这些是运维事故而非用户错误,一律 500 并只回模糊文案 ——
// "PII 密钥长度不对"这种信息不该出现在用户浏览器里。
var (
	errPIIKeyUnavailable = newBizError("qy_wd_pii_key_unavailable", "法币提现暂不可用,请联系管理员", http.StatusInternalServerError)
	errDigestKeyMissing  = newBizError("qy_wd_pii_key_unavailable", "法币提现暂不可用,请联系管理员", http.StatusInternalServerError)
	// errPIIKeyMissingVersion 与 errPayeeUndecryptable 刻意分开:前者是"轮换时忘了
	// 保留旧密钥"这一运维事故,要让人去补配置;后者是密文本身坏了,才该联系用户
	// 重新提供。混成一个 code 会把一次配置疏漏包装成一批用户的锅。
	errPIIKeyMissingVersion = newBizError("qy_wd_pii_key_version_missing",
		"收款信息所用的密钥版本未配置,请联系管理员", http.StatusInternalServerError)
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
	case errors.Is(err, commission.ErrFiatUnavailable):
		respondErr(c, errFiatUnavailable)
	case errors.Is(err, commission.ErrInvalidAmount):
		respondErr(c, errAmountOutOfRange)
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
