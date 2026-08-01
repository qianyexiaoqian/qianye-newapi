package paypass

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// reasonMaxRunes 是管理员操作原因的长度上限。审计的 Reason 列是 varchar(512),
// audit.Truncate 会兜底,这里先挡一道是为了给出明确的 400 而不是静默截断。
const reasonMaxRunes = 200

type adminActionRequest struct {
	Reason string `json:"reason"`
}

// handleAdminGetStatus 查一个用户的支付密码状态。
//
// 用 FlagCore 而不是 FlagTransfer:划转被临时关停时,管理员仍然必须能查、能解锁 ——
// 关停划转恰恰是出事时的第一个动作,那之后才轮到处理用户的锁定申诉。
func handleAdminGetStatus(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	userId, ok := adminTargetUserId(c)
	if !ok {
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	view, err := loadStatus(ctx, userId)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleAdminUnlock 提前解除锁定。
//
// 只清错误计数与锁定,不动密码本身:用户的诉求通常是"我记得密码,只是刚才输错了几次"。
func handleAdminUnlock(c *gin.Context) {
	adminMutate(c, "pay_password.unlock", false, unlockByAdmin)
}

// handleAdminReset 重置(清空)用户的支付密码,用户下次划转时会被要求重新设置。
//
// 管理员全程不接触密码明文,见 clearPasswordByAdmin 的说明。
func handleAdminReset(c *gin.Context) {
	adminMutate(c, "pay_password.reset", true, clearPasswordByAdmin)
}

// adminMutate 是两个管理端写操作的公共骨架。
//
// 单独成函数不是为了少写几行:这两个动作的**审计口径必须完全一致** ——
// 成功与失败都要落一条、都要带目标用户与操作原因、都要带操作前后的状态快照。
// 分开写两份的话,下一次给其中一份加字段时另一份必然漏掉,
// 而审计表是这套资金系统事后仲裁的唯一凭据。
//
// requireReason 只对破坏性动作为 true:重置会让用户下一次划转被直接拦下,
// 必须能回答"为什么";解锁是恢复性动作,强制填理由只会让人填一个"1"。
func adminMutate(c *gin.Context, action string, requireReason bool,
	apply func(ctx context.Context, gdb *gorm.DB, userId int, now int64) error) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	userId, ok := adminTargetUserId(c)
	if !ok {
		return
	}
	var req adminActionRequest
	// 允许空请求体:解锁不强制填理由,前端可能直接 POST 而不带 body。
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			respondErr(c, errInvalidParam)
			return
		}
	}
	reason := strings.TrimSpace(req.Reason)
	if requireReason && reason == "" {
		respondErr(c, errReasonRequired)
		return
	}
	if utf8.RuneCountInString(reason) > reasonMaxRunes {
		respondErr(c, errInvalidParam)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	// 这一步失败意味着扩展库整体不可用,而 audit.Write 自己也会因为 db.Available()
	// 为 false 直接返回 —— 在这里写审计只是一行永远不生效的代码,不写。
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	before, _, err := loadRow(ctx, gdb, userId)
	if err != nil {
		writeAdminAudit(c, action, userId, qymodel.ResultFail, reason, "", "")
		respondErr(c, err)
		return
	}
	if err := apply(ctx, gdb, userId, common.GetTimestamp()); err != nil {
		// 失败也要落审计:"管理员点了重置但没成功"与"管理员从没点过"
		// 在事后是完全不同的两件事,只记成功等于把前者伪装成后者。
		writeAdminAudit(c, action, userId, qymodel.ResultFail, reason, snapshotOf(before), "")
		respondErr(c, err)
		return
	}
	after, _, err := loadRow(ctx, gdb, userId)
	if err != nil {
		// 回读失败不影响这次操作已经生效,但快照只能留前半段。
		common.SysError("qianye/paypass: 管理端操作后回读状态失败: " + err.Error())
		after = PayPassword{UserId: userId}
	}
	writeAdminAudit(c, action, userId, qymodel.ResultOK, reason, snapshotOf(before), snapshotOf(after))

	view, err := loadStatus(ctx, userId)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// adminTargetUserId 解析并校验路径上的目标用户,不存在时直接写响应。
//
// 必须真的去主库确认用户存在:不确认的话,一个打错的 user_id 会静默"成功",
// 管理员以为解锁了张三,实际什么都没发生 —— 而审计里还留着一条成功记录。
func adminTargetUserId(c *gin.Context) (int, bool) {
	raw, ok := httpq.PathInt64(c, "user_id")
	if !ok || raw > math.MaxInt32 {
		respondErr(c, errInvalidParam)
		return 0, false
	}
	userId := int(raw)
	if _, err := model.GetUserById(userId, false); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondErr(c, errUserNotFound)
			return 0, false
		}
		respondErr(c, err)
		return 0, false
	}
	return userId, true
}

// writeAdminAudit 落一条管理端操作审计。
//
// 分类用 admin:这是"管理员对某个账号做了什么",与用户自己改支付密码
// (分类 transfer)在事后排查时是两条不同的线索。
func writeAdminAudit(c *gin.Context, action string, targetUserId int, result, reason, before, after string) {
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryAdmin,
		Action:       action,
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: targetUserId,
		Result:       result,
		Reason:       reason,
		BeforeSnap:   before,
		AfterSnap:    after,
	})
}

// auditSnapshot 是写进审计 before/after 的字段白名单。
//
// 刻意不直接序列化 PayPassword:那个结构体里有 Hash 字段。它今天带着 `json:"-"`,
// 但审计快照会被原样存进数据库并展示给管理员 —— 一旦将来有人把那个 tag 去掉
// (比如为了给某个内部接口复用结构体),全站支付密码摘要就会静静地流进
// qy_audit_logs。白名单让"新增字段默认不进审计",而不是"新增字段默认进审计"。
type auditSnapshot struct {
	IsSet       bool  `json:"is_set"`
	FailCount   int   `json:"fail_count"`
	LockedUntil int64 `json:"locked_until"`
	ChangedAt   int64 `json:"changed_at"`
}

func snapshotOf(p PayPassword) string {
	buf, err := common.Marshal(auditSnapshot{
		IsSet:       p.isSet(),
		FailCount:   p.FailCount,
		LockedUntil: p.LockedUntil,
		ChangedAt:   p.ChangedAt,
	})
	if err != nil {
		return ""
	}
	return string(buf)
}
