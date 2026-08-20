package commission

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// 响应信封与扩展其余部分保持一致:{success, message, data},
// 失败时额外带 code 供前端做 i18n 映射,不把中文写死在前端。
func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func respondFail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": msg})
}

func badRequest(c *gin.Context, code, msg string) {
	respondFail(c, http.StatusBadRequest, code, msg)
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/commission: 接口处理失败: " + err.Error())
	respondFail(c, http.StatusInternalServerError, "qy_internal_error", "处理失败,请稍后重试")
}

// listPaging 是佣金相关列表接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 页长上限挡的是"管理端一次拉全表把内存打满";页码上限(httpq.MaxPage)挡的是
// 深翻页 —— 这份拷贝原本只有前者,而 /commission/records 是用户端接口。
var listPaging = httpq.Spec{}

// denyActorOverTarget 是本模块动钱接口的操作人闸门:操作人不许是受益人,
// 也不许是同级或更高权限的账号。判据在 guard.ActorMayActOn,这里只负责把
// 哨兵错误翻成本模块的错误码表(与 api_admin_adjust.go 的那份逐字一致 ——
// 前端的提示文案按 code 映射,同一件事出两个 code 会变成两条文案)。
//
// action 是被拒动作的审计 action 名。被拒必须留痕:一次被拒的自营动作不是
// 手滑,它是"有人正在试探这条通道"的形状,而事后仲裁只认审计表。
//
// 「目标不存在」刻意**不由这里拒绝**:它不是授权问题,而每条接口对
// "查无此人"已经各有自己的错误码与审计正文(qy_rel_user_not_found、
// qy_adj_user_not_found)。闸门只因为要回查角色才顺带撞上它,抢答会让
// 同一件事在同一条接口上出两种 code。一个不存在的 id 也不可能是操作人自己
// (那一格由 SelfDealing 先答完),放过去交给调用点自己的校验是安全的。
//
// 返回 true 表示**已经写过响应**,调用方直接 return。
func denyActorOverTarget(c *gin.Context, action string, targetUserId int) bool {
	err := guard.ActorMayActOnCtx(c, targetUserId)
	switch {
	case err == nil, errors.Is(err, guard.ErrTargetMissing):
		return false
	case errors.Is(err, guard.ErrActorIsTarget):
		// 403 而不是 400:参数没问题,是**这个人**不该做这件事。
		respondFail(c, http.StatusForbidden, "qy_self_dealing", err.Error())
	case errors.Is(err, guard.ErrTargetNotLower):
		respondFail(c, http.StatusForbidden, "qy_target_not_manageable", err.Error())
	default:
		internalError(c, err)
		return true
	}
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryCommission,
		Action:       action + ".actor_denied",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: targetUserId,
		Result:       qymodel.ResultFail,
		Reason:       err.Error(),
	})
	return true
}
