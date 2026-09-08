package mall

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/qianye/modules/stardust"
)

// errors.go —— 本模块对外的错误集合(形状照 lottery / stardust 的 errors.go)。
//
// 每个错误都带一个稳定的英文 code:前端按 code 做 i18n,message 只是兜底。
// code 逐字照 stardust-api-contract.md §4 / §5 / §7。

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

func (e *bizError) HTTPStatus() int { return e.Status }
func (e *bizError) ErrCode() string { return e.Code }
func (e *bizError) Message() string { return e.Msg }

// 稳定的 code。契约里的那几条一个字都不能改。
const (
	codeInsufficient     = "qy_sd_insufficient"
	codeSoldOut          = "qy_ml_sold_out"
	codeLimit            = "qy_ml_limit"
	codeOffSale          = "qy_ml_off_sale"
	codePlanStateChanged = "qy_ml_plan_state_changed"
	codePlanNeedsOutbox  = "qy_ml_plan_needs_outbox"
	codeAddressRequired  = "qy_ml_address_required"
	codeHasOpenOrders    = "qy_ml_has_open_orders"
	codeBadStatus        = "qy_ml_bad_status"
	codeIdemConflict     = "qy_idem_key_conflict"
	codeNotFound         = "qy_ml_not_found"
	codeBadRequest       = "qy_ml_bad_request"
)

var (
	errProductNotFound = newBizError(http.StatusNotFound, codeNotFound, "商品不存在")
	errOrderNotFound   = newBizError(http.StatusNotFound, codeNotFound, "订单不存在")
	errSoldOut         = newBizError(http.StatusConflict, codeSoldOut, "商品已售罄")
	errLimit           = newBizError(http.StatusConflict, codeLimit, "已达到该商品的每人限购数量")
	errOffSale         = newBizError(http.StatusConflict, codeOffSale, "商品当前不在售")
	// errPlanStateChanged:用户确认过的顶替后果与主库事务里复核出来的不一致。
	// 商城是第一条能在事务内安全拒绝的购买路径(非 paid),"跨组顶替要用户确认"
	// 这条拍板在这里由执行侧复核保证。
	errPlanStateChanged = newBizError(http.StatusConflict, codePlanStateChanged,
		"套餐状态已变化,请重新确认后再购买")
	// errPlanNeedsOutbox:套餐商品的失败退款判据是主库 outbox 探针(ProbeMainSide),
	// 探针关掉时 paid→failed(退)不可达,每一笔失败都成 held 交人工 —— 所以干脆不卖。
	errPlanNeedsOutbox = newBizError(http.StatusBadRequest, codePlanNeedsOutbox,
		"套餐商品依赖主库 outbox 探针(two_phase.main_outbox_enabled),当前未开启,暂不可售")
	errAddressRequired = newBizError(http.StatusBadRequest, codeAddressRequired, "实物商品必须填写收货地址")
	// 奖品单(source=lottery)专属的三条。
	errNotPrizeOrder = newBizError(http.StatusForbidden, "qy_ml_not_prize_order",
		"只有抽奖所得的实物订单可以事后补填收货地址;自购订单的地址在下单时已经填写")
	errAddressExists = newBizError(http.StatusConflict, "qy_ml_address_exists",
		"这张订单已经有收货地址,如需修改请联系客服")
	errAddressMissing = newBizError(http.StatusConflict, "qy_ml_address_missing",
		"中奖者尚未填写收货地址,请等待用户在「我的订单」里补填后再发货")
	// 码库存的三条。「不能提」与「不能删」刻意分成两个 code:两者的下一步不同 ——
	// 前者要去订单里查这枚码发给了谁,后者压根没有下一步(证据行不删)。
	errCodeNotFound    = newBizError(http.StatusNotFound, "qy_ml_code_not_found", "这枚兑换码不存在,或不属于该商品")
	errCodeNotTakeable = newBizError(http.StatusConflict, "qy_ml_code_not_takeable",
		"只有「未使用」的兑换码可以提取;这一枚已经发出、被撤回或刚刚被另一位管理员提走")
	errCodeNotDeletable = newBizError(http.StatusConflict, "qy_ml_code_not_deletable",
		"只有「未使用」的兑换码可以删除;已发出 / 已撤回 / 已提取的码是发放去向的证据,永久保留")
	errProductReferenced = newBizError(http.StatusConflict, "qy_ml_product_referenced",
		"该商品仍挂在进行中的抽奖活动的奖档上,不能删除;请先等活动结束或取消")
	errHasOpenOrders  = newBizError(http.StatusConflict, codeHasOpenOrders, "该商品还有未完结的订单,不能删除")
	errBadStatus      = newBizError(http.StatusConflict, codeBadStatus, "订单当前状态不允许此操作")
	errStatusConflict = newBizError(http.StatusConflict, codeBadStatus, "订单状态刚刚被改变,请刷新后重试")
	errInProgress     = newBizError(http.StatusConflict, codeBadStatus, "该请求正在处理中,请稍候")
	errNotSettled     = newBizError(http.StatusConflict, codeBadStatus, "订单尚未落定,请稍后复核")
	errCodeRevoked    = newBizError(http.StatusConflict, codeBadStatus, "该兑换码已被撤回")
	errFundOrderOpen  = newBizError(http.StatusConflict, codeBadStatus,
		"这张订单的资金单还在补偿任务手里(未落定),等它收敛成成功或失败之后再来")
	errIdemConflict = newBizError(http.StatusConflict, codeIdemConflict,
		"该请求标识已被另一次不同参数的下单占用,请刷新后重新发起")
	errAddressPruned = newBizError(http.StatusGone, "qy_ml_address_pruned",
		"收货地址已超过保留期(mall.address_retention_days)被清除")
	errMaxProducts = newBizError(http.StatusBadRequest, "qy_ml_max_products",
		"商品数量已达上限(mall.max_products)")
	errBadClientRequestID = newBizError(http.StatusBadRequest, codeBadRequest,
		"client_request_id 不合法:只接受 1~64 位的字母、数字、下划线与连字符")
	// 密钥缺失是运维事故:必须回 500 让人去补配置,而不是让用户去重试。
	errSecretKeyMissing = newBizError(http.StatusInternalServerError, "qy_ml_secret_key_missing",
		"mall.secret_key 未配置,兑换码与收货地址无法加密")
	errSecretUnreadable = newBizError(http.StatusInternalServerError, "qy_ml_secret_unreadable",
		"密文无法解密,请检查 mall.secret_key_version / secret_keys_retired 配置")

	// 操作人判据的三个方向(与 lottery 的人工落账逐字同一套理由)。
	errSelfDealing = newBizError(http.StatusForbidden, "qy_self_dealing",
		"不能对自己名下的订单执行退款类操作,请由另一位管理员处理")
	errTargetHigher = newBizError(http.StatusForbidden, "qy_target_not_manageable",
		"不能对同级或更高权限账号名下的订单执行退款类操作")
	errTargetMissing = newBizError(http.StatusNotFound, "qy_target_missing",
		"这张订单的用户在主库里查不到(多半已被硬删),请先确认账号去向")

	// 封面。
	errCoverRequired = newBizError(http.StatusBadRequest, "qy_ml_cover_required", "请选择要上传的图片")
	errCoverTooLarge = newBizError(http.StatusRequestEntityTooLarge, "qy_ml_cover_too_large", "封面图片超出大小上限")
	errCoverType     = newBizError(http.StatusBadRequest, "qy_ml_cover_type", "只接受 JPEG / PNG / WebP 图片")
	errCoverNotFound = newBizError(http.StatusBadRequest, "qy_ml_cover_not_found", "封面图片不存在或已被使用")
	errCoverPurged   = newBizError(http.StatusGone, "qy_ml_cover_purged", "封面图片已被回收")
	errCoverPending  = newBizError(http.StatusConflict, "qy_ml_cover_pending_limit",
		"还有多张已上传但未使用的封面,请先保存商品或移除它们")
	errCoverStore = newBizError(http.StatusInternalServerError, "qy_ml_cover_store_failed", "封面保存失败,请稍后重试")
)

// errInsufficient 是"星屑不足"。单位名是运营可改的,兜底文案带上当前生效的名字。
func errInsufficient() *bizError {
	return newBizError(http.StatusBadRequest, codeInsufficient, stardust.UnitName()+"不足")
}

// errBadRequest 是通用的参数不合法。
func errBadRequest(msg string) *bizError {
	return newBizError(http.StatusBadRequest, codeBadRequest, msg)
}

// BizOf 把账本层的哨兵错误翻译成可回给用户的业务错误;其它错误原样返回。
//
// 只翻译"用户能改变什么"的那几条。ErrBadKind / ErrBadPosting 是调用方的编程错误,
// 原样留成内部错误(500 + SysError)。
func BizOf(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, stardust.ErrInsufficient):
		return errInsufficient()
	case errors.Is(err, stardust.ErrOverflow):
		return newBizError(http.StatusConflict, "qy_sd_overflow",
			"退回后"+stardust.UnitName()+"会超出系统上界,请先消耗一部分")
	case errors.Is(err, stardust.ErrBadAmount):
		return errBadRequest("金额必须是 1 到系统上界之间的整数")
	}
	return err
}

// wrapInternal 把内部错误包成带上下文的错误,供日志定位。它不是 bizError。
func wrapInternal(stage string, err error) error {
	return fmt.Errorf("qianye/mall: %s: %w", stage, err)
}
