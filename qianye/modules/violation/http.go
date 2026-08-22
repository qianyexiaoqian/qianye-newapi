package violation

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// respondFailData 是带明细的失败响应。
//
// 绝大多数失败只需要一句话,所以默认的 respondFail 不带 data。但"逐条独立成败"的
// 批量接口不一样:一次失败的批量导入里,**哪几条失败、各自为什么**是排障的全部信息,
// 而它只存在于那个已经构造好的逐条结果里。丢掉它,管理员手上就只剩一句
// "N 条全部写入失败,详见服务端日志" —— 而能点那个按钮的管理员未必有服务器日志权限,
// 排障因此必须升级一级。审计快照也接不住:AfterSnap 会按 SnapshotMaxBytes 截断,
// 多条带完整 GORM 错误串的明细会被切掉尾部。
func respondFailData(c *gin.Context, status int, code, msg string, data any) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": msg, "data": data})
}

func badRequest(c *gin.Context, msg string) {
	respondFail(c, http.StatusBadRequest, "qy_vio_bad_request", msg)
}

func notFound(c *gin.Context) {
	respondFail(c, http.StatusNotFound, "qy_vio_not_found", "记录不存在")
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/violation: 接口处理失败: " + err.Error())
	respondFail(c, http.StatusInternalServerError, "qy_internal_error", "处理失败,请稍后重试")
}

// errNoPendingBan 是「这个账号没有待解除的封禁」。
//
// 它必须与 internalError 分开:后者说的是「请稍后重试」,而重试**永远不会成功**
// —— 这个账号本来就没有要解的东西。最常见的触发不是构造出来的:两个管理员同时
// 点、封禁列表缓存陈旧(useQuery 不会自动失效,别人解封之后本地那一行还是
// banned、按钮仍可点)、脚本重放。表现是运营对着一件永远不会成功的事反复重试,
// 同时把一次正常点击伪装成服务端故障、污染 5xx 告警。
//
// 409 而不是 404:那个用户存在,只是他此刻的状态与这个动作不相容。
var errNoPendingBan = errors.New("该用户没有待解除的违规封禁")

// errBanStatusChurning 是「这一行正在被别的路径改写」。这一条**确实**该重试。
var errBanStatusChurning = errors.New("该用户的封禁状态正在变化中,请稍后重试")

// respondUnbanError 把解封失败翻译成调用方能据以行动的答复。
//
// 三档要求的下一步动作完全不同,所以必须是三个 code:
//
//	没有待解除的封禁   刷新列表,这件事已经做完了(或从来不需要做)。重试无用。
//	状态正在变化       稍后重试,这一次是真的可以重试。
//	其余               服务端故障。
func respondUnbanError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errNoPendingBan):
		respondFail(c, http.StatusConflict, "qy_vio_no_pending_ban", errNoPendingBan.Error())
	case errors.Is(err, errBanStatusChurning):
		respondFail(c, http.StatusConflict, "qy_vio_ban_churning", errBanStatusChurning.Error())
	default:
		internalError(c, err)
	}
}

// denyActorOverTarget 是「处置落在谁头上」的操作人闸门:操作人不许是被处置人
// 本人,也不许是同级或更高权限的账号。判据在 guard.ActorMayActOn,与佣金、
// 提现、支付密码三侧共用一份实现。
//
// 为什么违规处置也要接这条判据:撤销一条违规记录可以带 refund=true 把扣掉的
// 额度退回去,那是**真的动钱**;解封与计数清零不直接动钱,但它们决定这个账号
// 还能不能继续消费、离下一次自动封号还有几次 —— 一个 role=10 管理员能给自己
// 撤记录、退钱、解封、清零,等于违规处置对管理员这一档完全不生效,而这套系统
// 的自动封号恰恰不看角色。
//
// 只读接口(记录列表、证据、封禁列表、申诉列表)刻意不接:看得到自己的违规
// 记录不构成问题,挡掉只会让人以为记录丢了。
//
// action 是被拒动作的审计 action 名。被拒必须留痕:一次被拒的自营撤销不是
// 手滑,它是"这个账号正在给自己擦记录"的形状,而事后仲裁只认审计表 ——
// 只记成功的话,那件事看起来与"从没有人试过"完全一样。
//
// 返回 true 表示**已经写过响应**,调用方直接 return。
func denyActorOverTarget(c *gin.Context, action string, targetUserId int) bool {
	err := guard.ActorMayActOnCtx(c, targetUserId)
	switch {
	case err == nil:
		return false
	case errors.Is(err, guard.ErrActorIsTarget):
		// 403 而不是 400:参数没问题,是**这个人**不该做这件事。
		respondFail(c, http.StatusForbidden, "qy_self_dealing",
			"不能对自己的账号执行违规处置,请由另一位管理员操作")
	case errors.Is(err, guard.ErrTargetNotLower):
		respondFail(c, http.StatusForbidden, "qy_target_not_manageable",
			"不能对同级或更高权限账号执行违规处置")
	case errors.Is(err, guard.ErrTargetMissing):
		notFound(c)
	default:
		internalError(c, err)
		return true
	}
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryViolation,
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

// listPaging 是违规相关列表接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 页长上限挡的是"管理端一次拉全表把内存打满"(违规记录表是所有扩展表里增长最快
// 的一张);页码上限(httpq.MaxPage)挡的是深翻页 —— 这份拷贝原本只有前者。
var listPaging = httpq.Spec{}

func pathInt64(c *gin.Context, key string) (int64, bool) {
	return httpq.PathInt64(c, key)
}

// queryWindowHours 解析两个影响面预览接口的 ?window_hours=,支持无限窗口哨兵。
//
// httpq.Int 只认非负十进制(负号在别处一律是参数污染,那是它刻意的设计),
// 所以 WindowUnlimited 走一条显式分支:查询串**恰好**是 "-1" 时取哨兵,
// 其余交回 httpq.Int。不放宽 httpq 本身 —— 让全站的整数查询参数开始接受负数,
// 只为了这两个接口的一个哨兵,是把一次局部需求变成一道全局口子。
//
// 预览与保存必须认同一套取值:预览不认 -1 的话,管理员在表单里勾上"不限期限"
// 之后看到的影响面仍然是按 24 小时算的那个数,而他就是靠这个数决定要不要按保存。
func queryWindowHours(c *gin.Context) int {
	if strings.TrimSpace(c.Query("window_hours")) == strconv.Itoa(WindowUnlimited) {
		return WindowUnlimited
	}
	return httpq.Int(c, "window_hours", defaultWindowHours)
}
