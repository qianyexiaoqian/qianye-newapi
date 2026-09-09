package riskwatch

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
// 与 apiaddr / paypass / transfer 的同名类型一样是包私有的:把它提到公共包
// 意味着所有模块的错误码空间从此耦合。必须一致的只有信封形状
// (success/code/message),而那由 respondErr / respondOK 一处渲染。
type bizError struct {
	Code   string
	Msg    string
	Status int
}

func (e *bizError) Error() string { return e.Code + ": " + e.Msg }

func newBizError(code, msg string, status int) *bizError {
	return &bizError{Code: code, Msg: msg, Status: status}
}

// # 为什么作用域为空单独占一个 code
//
// 它是这个功能唯一一个"参数合法、语义灾难"的输入:三格全空在类型上完全正确,
// 建出来的却是一个全站监听任务,而它与一个用户级任务在列表页上长得一模一样。
// 合并进"参数不合法"之后,管理员只会以为自己少填了必填项、随手补一个再提交,
// 而真正该看到的那句话("要全站抽样请显式填一个分组")永远不会出现。
var (
	errInvalidParam = newBizError("qy_rw_invalid_param",
		"请求参数不合法", http.StatusBadRequest)
	errNameRequired = newBizError("qy_rw_name_required",
		"请填写任务名称", http.StatusBadRequest)
	errNameTooLong = newBizError("qy_rw_name_too_long",
		"任务名称过长", http.StatusBadRequest)
	errNoteTooLong = newBizError("qy_rw_note_too_long",
		"立案理由过长", http.StatusBadRequest)
	errScopeEmpty = newBizError("qy_rw_scope_empty",
		"请至少指定一个监听目标(用户、分组或模型)——三项都不填等于全站监听,会在很短时间内写满存储节点",
		http.StatusBadRequest)
	errSampleRange = newBizError("qy_rw_sample_range",
		"记录概率必须在 0.01% 到 100% 之间", http.StatusBadRequest)
	errMaxRecordsRange = newBizError("qy_rw_max_records_range",
		"抽取条数不合法(0 表示不限)", http.StatusBadRequest)
	errWindowMode = newBizError("qy_rw_window_mode",
		"监听时段形态不合法", http.StatusBadRequest)
	errWindowRange = newBizError("qy_rw_window_range",
		"监听时段的结束时间必须晚于开始时间", http.StatusBadRequest)
	errCountdownRange = newBizError("qy_rw_countdown_range",
		"倒计时时长不合法", http.StatusBadRequest)
	errRetentionRange = newBizError("qy_rw_retention_range",
		"保留天数超出允许范围", http.StatusBadRequest)
	errTargetUserNotFound = newBizError("qy_rw_target_user_not_found",
		"找不到该用户", http.StatusBadRequest)
	errTooManyActive = newBizError("qy_rw_too_many_active",
		"运行中的监听任务已达上限,请先停止一些任务", http.StatusBadRequest)
	errNotFound = newBizError("qy_rw_not_found",
		"该监听任务不存在,可能已被其他管理员删除", http.StatusNotFound)
	errCaptureNotFound = newBizError("qy_rw_capture_not_found",
		"该监听记录不存在,可能已过保留期被清理", http.StatusNotFound)
	// errConflict 是乐观锁失败。它必须与"参数不合法"分开:前者的正确处置是
	// 刷新页面重看一眼别人改成了什么,后者是改输入。
	errConflict = newBizError("qy_rw_conflict",
		"该任务已被其他管理员改动,请刷新后重试", http.StatusConflict)
	// errAlreadyFull 是"抽满了之后又点启动"。
	//
	// 与 errMaxRecordsRange 分开:那一条说的是"你填的数字不合法",这一条说的是
	// "数字没问题,但这个任务已经抓够了" —— 处置完全不同(前者改输入,
	// 后者是先把上限调大再启动),而 captured 不清零是刻意的:清零会让
	// "这个任务一共抓了多少条"失去答案,而记录还在库里。
	errAlreadyFull = newBizError("qy_rw_already_full",
		"该任务已抽满设定的条数,请先调大抽取条数上限再启动", http.StatusBadRequest)
	// errNotRestartable 是"停了但窗口也过了"。
	//
	// 与 errConflict 分开是因为处置完全不同:这一条要建新任务,而不是刷新重试。
	errNotRestartable = newBizError("qy_rw_not_restartable",
		"该任务的监听时段已经结束,无法重新启动,请新建一个任务", http.StatusBadRequest)
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
			"message": "风控预警存储节点暂不可用,请稍后重试",
		})
		return
	}
	common.SysError("qianye/riskwatch: 接口处理失败: " + err.Error())
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}
