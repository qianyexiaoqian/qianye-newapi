package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UserSessionStats 返回当前用户登录会话的分档计数:有效 / 已到期。
//
// ─────────────── 这个端点为什么必须存在 ───────────────
//
// 项目方要求个人资料页显示「当前已登录的会话一共有多少个,有多少个已到期」。
// 前者上游接口给得出,后者**给不出**:
//
//	model.ListActiveUserSessions 的 WHERE 是
//	  status = 'active' AND expires_at > now
//	(model/user_session.go:412 与 :429 两段查询都带这个条件)
//
// 也就是说已到期的会话**结构性地不会下发**。前端拿这份列表算「已到期」,
// 结果恒为 0 —— 那不是"确认没有会话到期",而是在报告一个不可能非零的数字,
// 比不显示更糟。
//
// 放宽上游那两段 WHERE 是改上游业务逻辑,违反本仓铁律;而且那会让已到期会话
// 混进"活跃会话列表",上游还有别的消费方依赖它只含有效会话。
//
// 所以走千夜自己的端点:**同一张表,不同的 WHERE**。扩展本来就在读主库
// (users / logs / abilities 都在读),多读一张 user_sessions 不构成新的耦合面,
// 上游改动为零。
//
// ─────────────── 为什么不走 requireCore ───────────────
//
// 与同目录 AdminVersion 同一理由,但更强:本端点**根本不碰扩展库**,
// 它只查主库。让一个不依赖扩展库的只读计数在扩展库不可用时 503,是纯粹的负收益。
// 鉴权没有放松 —— 路由挂在 UserAuth 之后,且整个 /api/qy 组只在扩展启用时注册。
//
// ─────────────── 口径 ───────────────
//
// 三个条件全部照抄上游 ListActiveUserSessions,只把时间那一维拆成两档:
//
//		有效   status='active' AND user_auth_version=当前 AND expires_at >  now
//		已到期 status='active' AND user_auth_version=当前 AND expires_at <= now
//
//	  - **必须带 user_auth_version**:用户改密码/主动登出全部设备会推进这个版本号,
//	    旧版本的行虽然还在库里、status 也还是 active,但已经不是"这个用户的会话"了。
//	    漏掉它会把改密码之前的历史会话全算进"已到期",数字凭空变大。
//	  - **status 只取 active**:revoked / revoking 是"被吊销",不是"到期"。
//	    把它们算进来会让主动登出的设备被报成"到期",两种性质混为一谈。
//	  - 有效数与上游列表接口口径逐字一致,所以页面上「统计」与「列表条数」
//	    不会打架 —— 除非列表撞上 userSessionListLimit 的 100 条硬上限,
//	    那种情况下反而是本端点的数字才是对的。
func UserSessionStats(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "unauthorized"})
		return
	}

	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "unauthorized"})
		return
	}

	now := common.GetTimestamp()
	base := model.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND user_auth_version = ? AND status = ?",
			userId, user.AuthVersion, model.UserSessionStatusActive)

	var active, expired int64
	if err := base.Session(&gorm.Session{}).Where("expires_at > ?", now).Count(&active).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "count failed"})
		return
	}
	if err := base.Session(&gorm.Session{}).Where("expires_at <= ?", now).Count(&expired).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "count failed"})
		return
	}

	ok(c, gin.H{"active": active, "expired": expired})
}
