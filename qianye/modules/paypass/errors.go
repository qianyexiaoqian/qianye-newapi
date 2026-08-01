package paypass

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
// 与 transfer 的同名类型是两份**刻意**分开的定义:那一份是 transfer 包的私有
// 类型,跨包用不了;而把它提到公共包意味着两个模块的错误码空间从此耦合。
// 这里唯一必须保持一致的是**信封形状**(success/code/message),而那是由
// respond 一处渲染的。
type bizError struct {
	Code   string
	Msg    string
	Status int
}

func (e *bizError) Error() string { return e.Code + ": " + e.Msg }

func newBizError(code, msg string, status int) *bizError {
	return &bizError{Code: code, Msg: msg, Status: status}
}

// 验密闸门的四个结果。code 一旦发布就不能改 —— 前端按 code 映射 i18n,
// 改 code 会让所有翻译静默回落到"未知错误"。
//
// # 为什么这四个 code 对外可分辨
//
// 「未设置」必须能被前端识别并引导用户去设置(这是产品硬要求:首次使用强制设置),
// 「已锁定」必须能告诉用户什么时候解锁,否则他只会一直重试把锁续得更久。
// 而这个接口全程要求登录态,调用方就是账号本人 —— 对本人可分辨不构成泄漏。
//
// 真正要防的是**响应时间**上的可分辨(见 verify.go 的 uniformCompare):
// 三条路径都会走一次同样成本的 bcrypt 比对,不会出现"未设置就秒回、密码错误
// 要等 80 毫秒"这种可被外部观测的差异。
var (
	errPayPwdNotSet = newBizError("qy_pay_pwd_not_set",
		"请先设置支付密码", http.StatusForbidden)
	errPayPwdRequired = newBizError("qy_pay_pwd_required",
		"请输入支付密码", http.StatusForbidden)
	errPayPwdWrong = newBizError("qy_pay_pwd_wrong",
		"支付密码错误", http.StatusForbidden)
	errPayPwdLocked = newBizError("qy_pay_pwd_locked",
		"支付密码已被锁定,请稍后再试或通过邮箱找回", http.StatusForbidden)
)

// 设置与修改相关的错误。
var (
	errInvalidParam = newBizError("qy_invalid_param",
		"请求参数不合法", http.StatusBadRequest)
	errPayPwdAlreadySet = newBizError("qy_pay_pwd_already_set",
		"支付密码已设置,请使用修改功能", http.StatusConflict)
	errPayPwdWeak = newBizError("qy_pay_pwd_weak",
		"支付密码强度不足:长度需在 6~64 位之间,且不能是连续或重复的纯数字", http.StatusBadRequest)
	errPayPwdSameAsOld = newBizError("qy_pay_pwd_same_as_old",
		"新支付密码不能与原支付密码相同", http.StatusBadRequest)
)

// 邮箱找回相关的错误。
var (
	// errPayPwdEmailUnbound 是裁决 1 里点名的那条:未绑定邮箱时**只提示去绑定**,
	// 绝不在本路径上代为绑定 —— 那等于凭一个已被盗用的会话就能改绑邮箱,
	// 再用新邮箱把支付密码找回来,支付密码就白设了。
	errPayPwdEmailUnbound = newBizError("qy_pay_pwd_email_unbound",
		"当前账号未绑定邮箱,请先在个人设置中绑定邮箱后再使用找回功能", http.StatusBadRequest)
	errPayPwdCodeInvalid = newBizError("qy_pay_pwd_code_invalid",
		"验证码无效或已过期", http.StatusBadRequest)
	errPayPwdMailUnavailable = newBizError("qy_pay_pwd_mail_unavailable",
		"邮件服务暂不可用,请稍后再试", http.StatusServiceUnavailable)
)

// 管理端错误。
var (
	errUserNotFound = newBizError("qy_pay_pwd_user_not_found",
		"用户不存在", http.StatusNotFound)
	errReasonRequired = newBizError("qy_reason_required",
		"请填写操作原因", http.StatusBadRequest)
)

// respondErr 把内部错误翻译成稳定的响应信封,并 Abort。
//
// 一律 Abort 而不是只写响应:Require 会被中间件形态复用(见 gate.go 的
// Middleware),中间件里少一次 Abort 就是"验密失败了但请求照样往下走"。
// 让 Abort 成为写响应这件事的一部分,调用方就没有忘记它的机会。
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
			"message": "支付密码服务暂不可用,请稍后重试",
		})
		return
	}
	common.SysError("qianye/paypass: 接口处理失败: " + err.Error())
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}
