package ticket

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// bizError 是工单对外暴露的业务错误。
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

// 参数错误。
var (
	errInvalidParam    = newBizError("qy_invalid_param", "请求参数不合法", http.StatusBadRequest)
	errTitleRequired   = newBizError("qy_tk_title_required", "请填写工单标题", http.StatusBadRequest)
	errTitleTooLong    = newBizError("qy_tk_title_too_long", "标题超出长度限制", http.StatusBadRequest)
	errBodyRequired    = newBizError("qy_tk_body_required", "请填写正文", http.StatusBadRequest)
	errBodyTooLong     = newBizError("qy_tk_body_too_long", "正文超出长度限制", http.StatusBadRequest)
	errPriorityInvalid = newBizError("qy_tk_priority_invalid", "工单等级取值不合法", http.StatusBadRequest)
	errAssigneeInvalid = newBizError("qy_tk_assignee_invalid", "指派对象不存在或不是管理员", http.StatusBadRequest)
)

// 防滥用闸门。
//
// 每一条都说清"是哪一道闸拦的"。全部合并成一句"操作过于频繁"的话,用户只会
// 反复重试同一个必然失败的请求 —— 而这四道闸要求他做的事完全不同:
// 等一会儿 / 明天再来 / 先把手上的单关掉 / 这张单已经聊太长了另开一张。
var (
	errCooldown = newBizError("qy_tk_cooldown",
		"两次提交工单之间需要间隔一段时间,请稍后再试", http.StatusTooManyRequests)
	errReplyCooldown = newBizError("qy_tk_reply_cooldown",
		"回复过于频繁,请稍后再试", http.StatusTooManyRequests)
	errDailyLimit = newBizError("qy_tk_daily_limit",
		"今日提交的工单过多,请明天再试", http.StatusTooManyRequests)
	errOpenLimit = newBizError("qy_tk_open_limit",
		"未关闭的工单过多,请先处理或关闭已有工单", http.StatusTooManyRequests)
	errMessageLimit = newBizError("qy_tk_message_limit",
		"该工单的消息条数已达上限,请新建工单继续沟通", http.StatusTooManyRequests)
)

// 状态机与并发错误。
var (
	errNotFound = newBizError("qy_tk_not_found", "工单不存在", http.StatusNotFound)
	// errClosed 与 errIllegalTransition 分开:前者是"这张单已经结束了"(用户
	// 该做的是新建一张),后者是"这一步在当前状态下没有意义"(比如重开一张
	// 本来就开着的单)。合成一条会把"去开新单"这个唯一有用的指引丢掉。
	errClosed = newBizError("qy_tk_closed",
		"工单已关闭,无法继续追加;如仍有问题请新建工单", http.StatusConflict)
	errIllegalTransition = newBizError("qy_tk_illegal_transition",
		"当前状态不允许该操作", http.StatusConflict)
	errStatusConflict = newBizError("qy_tk_status_conflict",
		"该工单已被其他人处理,请刷新后重试", http.StatusConflict)
)

// 图片附件错误。
//
// 分得比"上传失败"细,理由与提现凭证同:这几种情况用户能做的事完全不同 ——
// 换一张图、压缩一下、先去用掉已传的、还是找管理员。
var (
	errImageDisabled = newBizError("qy_tk_image_disabled", "当前站点未开放上传图片", http.StatusBadRequest)
	errImageRequired = newBizError("qy_tk_image_required", "请选择要上传的图片", http.StatusBadRequest)
	errImageTooLarge = newBizError("qy_tk_image_too_large", "图片超出大小上限", http.StatusRequestEntityTooLarge)
	errImageType     = newBizError("qy_tk_image_type", "只接受 JPEG / PNG / WebP 图片", http.StatusBadRequest)
	errImageNotFound = newBizError("qy_tk_image_not_found", "图片不存在或已被使用", http.StatusBadRequest)
	errImageTooMany  = newBizError("qy_tk_image_too_many", "单条消息附带的图片数量超出上限", http.StatusBadRequest)
	errImagePending  = newBizError("qy_tk_image_pending_limit",
		"已有多张上传好的图片尚未提交,请先完成当前这条消息", http.StatusBadRequest)
	// errImageQuota 与 errImagePending 分开:后者的下一步是"把手上这条消息发出去"
	// (发完名额立刻回来),前者的下一步是"关掉旧工单让保留期把图片收走",
	// 两句话合成一条会让用户去做一个不可能生效的动作。
	errImageQuota = newBizError("qy_tk_image_quota",
		"你的工单图片已占满配额,请先关闭不再需要的工单", http.StatusBadRequest)
	// errImagePurged 与 errImageNotFound 分开:前者是"确实传过,但按保留期
	// 已经清理了",后者是"从来没有这张"。合成一条会让管理员在争议工单上
	// 误以为用户根本没传过图。
	errImagePurged = newBizError("qy_tk_image_purged",
		"图片已按保留期清理,无法再查看", http.StatusGone)
	errImageStore = newBizError("qy_tk_image_store_failed",
		"图片保存失败,请稍后重试", http.StatusInternalServerError)
)

// errInternal 是"服务端出了一件不该发生的事"的统一出口。
//
// 目前唯一的来源是 crypto/rand 取不到熵(单号生成失败)。刻意不复用
// errImageStore 之类的具体错误:前端按 code 出文案,拿"图片保存失败"去解释
// 一次单号生成失败,会让用户去删图重试 —— 一个与真实原因完全无关的动作。
var errInternal = newBizError("qy_internal_error", "处理失败,请稍后重试",
	http.StatusInternalServerError)

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
	if errors.Is(err, db.ErrNotReady) {
		// 工单不是热路径,库不可用时 fail-close:宁可让用户重试,
		// 也不能在"看起来提交成功了、其实什么都没写进去"的状态下放行。
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "工单服务暂不可用,请稍后重试",
		})
		return
	}
	common.SysError("qianye/ticket: 接口处理失败: " + err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}
