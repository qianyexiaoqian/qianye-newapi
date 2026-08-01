package paypass

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// recoverPurpose 是邮箱找回验证码在上游验证码表里的命名空间。
//
// 上游 common.RegisterVerificationCodeWithKey 的实现就是 map[purpose+key],
// purpose 只是一个前缀字符串,所以这里可以自带一个而不必去改 common/verification.go
// (改上游文件会在下一次合并上游时冲突)。
//
// 绝不能复用 common.EmailVerificationPurpose("v")或 PasswordResetPurpose("r"):
// 复用等于让"注册验证码"可以直接拿来改支付密码。
const recoverPurpose = "qy_paypwd"

// recoverCodeLength 是找回验证码的长度(uuid 去横线后的前 N 位十六进制)。
//
// 取 8 而不是上游注册验证码的 6:6 位十六进制只有 1600 万种,而这条路径能
// 直接改掉支付密码。8 位是 43 亿,配合 10 分钟有效期与 CriticalRateLimit 足够。
const recoverCodeLength = 8

// recoverKey 把验证码绑定到 (用户, 邮箱) 这一对上。
//
// 只用邮箱做 key 是不够的:上游 users.email 并非强唯一(历史数据里存在同邮箱多账号,
// 见 model.ErrEmailAmbiguous),只按邮箱存会让 A 申请的验证码被 B 用掉。
func recoverKey(userId int, email string) string {
	return strconv.Itoa(userId) + ":" + email
}

type setRequest struct {
	Password string `json:"password"`
}

type changeRequest struct {
	OldPassword string `json:"old_password"`
	Password    string `json:"password"`
}

type recoverResetRequest struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

// handleGetStatus 返回当前用户的支付密码状态,供前端决定弹"设置"还是"输入"。
func handleGetStatus(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	view, err := loadStatus(ctx, c.GetInt("id"))
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleSet 是首次设置。已设置时返回 qy_pay_pwd_already_set,不允许覆盖 ——
// 覆盖等于"不知道旧密码也能改密码",支付密码就没有意义了。
func handleSet(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	var req setRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	if !validateStrength(req.Password) {
		respondErr(c, errPayPwdWeak)
		return
	}
	// 与 verify 同理:落密码这一步不能被客户端断连取消 —— 半途取消会让用户
	// 以为设过了,下一次划转又被拦下。
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		respondErr(c, err)
		return
	}
	ok, err := claimFirstPassword(ctx, gdb, c.GetInt("id"), hash, common.GetTimestamp())
	if err != nil {
		respondErr(c, err)
		return
	}
	if !ok {
		respondErr(c, errPayPwdAlreadySet)
		return
	}
	writeUserAudit(c, "pay_password.set", qymodel.ResultOK, "首次设置支付密码")
	respondOK(c, gin.H{"is_set": true})
}

// handleChange 是"知道旧密码"的修改。旧密码走与划转完全相同的 verify ——
// 包括错误计数与锁定,否则这里就成了一个不受锁定策略约束的密码试探口。
func handleChange(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	var req changeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	if !validateStrength(req.Password) {
		respondErr(c, errPayPwdWeak)
		return
	}
	if req.Password == req.OldPassword {
		respondErr(c, errPayPwdSameAsOld)
		return
	}
	// 先验旧密码。Require 失败时已写好响应并 Abort。
	userId := c.GetInt("id")
	if !Require(c, userId, req.OldPassword) {
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	row, _, err := loadRow(ctx, gdb, userId)
	if err != nil {
		respondErr(c, err)
		return
	}
	newHash, err := hashPassword(req.Password)
	if err != nil {
		respondErr(c, err)
		return
	}
	ok, err := replacePassword(ctx, gdb, userId, row.Hash, newHash, common.GetTimestamp())
	if err != nil {
		respondErr(c, err)
		return
	}
	if !ok {
		// CAS 没命中:验旧密到写新密之间,密码已被另一个会话改掉。
		// 报"旧密码错误"是最准确的说法 —— 用户手上那个旧密码确实已经不是当前密码了。
		respondErr(c, errPayPwdWrong)
		return
	}
	writeUserAudit(c, "pay_password.change", qymodel.ResultOK, "用户自助修改支付密码")
	respondOK(c, gin.H{"is_set": true})
}

// handleRecoverCode 往用户已绑定的邮箱发一封找回验证码。
//
// 裁决 1 点名的红线:未绑定邮箱时**只提示去绑定**,绝不在这条路径上代为绑定。
// 允许在这里填一个新邮箱再往那个邮箱发码,等于给了一条"绕过原邮箱改绑"的路 ——
// 拿到会话的攻击者可以把找回邮箱换成自己的,支付密码立刻形同虚设。
func handleRecoverCode(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	userId := c.GetInt("id")
	user, err := model.GetUserById(userId, false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondErr(c, errUserNotFound)
			return
		}
		respondErr(c, err)
		return
	}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		respondErr(c, errPayPwdEmailUnbound)
		return
	}

	code := common.GenerateVerificationCode(recoverCodeLength)
	common.RegisterVerificationCodeWithKey(recoverKey(userId, email), code, recoverPurpose)
	subject := fmt.Sprintf("%s支付密码找回", common.SystemName)
	content := fmt.Sprintf("<p>您好,你正在找回%s的支付密码。</p>"+
		"<p>您的验证码为: <strong>%s</strong></p>"+
		"<p>验证码 %d 分钟内有效。如果不是本人操作,请立即修改登录密码。</p>",
		common.SystemName, code, common.VerificationValidMinutes)
	if err := common.SendEmail(subject, email, content); err != nil {
		common.SysError("qianye/paypass: 发送支付密码找回邮件失败: " + err.Error())
		respondErr(c, errPayPwdMailUnavailable)
		return
	}
	// 回显脱敏邮箱,让用户知道该去哪个信箱找,又不至于把完整地址暴露在响应里。
	respondOK(c, gin.H{"email_masked": maskEmail(email),
		"expires_in_minutes": common.VerificationValidMinutes})
}

// handleRecoverReset 用邮箱验证码重设支付密码,并清掉锁定。
func handleRecoverReset(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	var req recoverResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	if !validateStrength(req.Password) {
		respondErr(c, errPayPwdWeak)
		return
	}
	userId := c.GetInt("id")
	user, err := model.GetUserById(userId, false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondErr(c, errUserNotFound)
			return
		}
		respondErr(c, err)
		return
	}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		respondErr(c, errPayPwdEmailUnbound)
		return
	}
	key := recoverKey(userId, email)
	if strings.TrimSpace(req.Code) == "" || !common.VerifyCodeWithKey(key, req.Code, recoverPurpose) {
		respondErr(c, errPayPwdCodeInvalid)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	newHash, err := hashPassword(req.Password)
	if err != nil {
		respondErr(c, err)
		return
	}
	if _, err := replacePassword(ctx, gdb, userId, "", newHash, common.GetTimestamp()); err != nil {
		respondErr(c, err)
		return
	}
	// 验证码是一次性的。放在写库成功之后删:先删再写的话,写库失败会让用户
	// 手里那封邮件作废,只能重新申请一封。
	common.DeleteKey(key, recoverPurpose)
	writeUserAudit(c, "pay_password.recover", qymodel.ResultOK, "用户通过邮箱验证码重设支付密码")
	respondOK(c, gin.H{"is_set": true})
}

// writeUserAudit 写一条用户自助操作的审计。
//
// 分类用 transfer:支付密码是划转的一部分,管理员排查一笔可疑划转时会按
// trace/category 去翻同一批记录,分到别的类目里只会让人找不到。
func writeUserAudit(c *gin.Context, action, result, reason string) {
	userId := c.GetInt("id")
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryTransfer,
		Action:       action,
		ActorType:    qymodel.ActorUser,
		ActorUserId:  userId,
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		Result:       result,
		Reason:       reason,
	})
}

// maskEmail 把 alice@example.com 脱敏成 a***e@example.com。
func maskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := email[:at], email[at:]
	if len(local) <= 2 {
		return "*" + domain
	}
	return local[:1] + "***" + local[len(local)-1:] + domain
}
