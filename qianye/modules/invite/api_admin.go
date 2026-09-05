package invite

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin.go —— 拉黑 / 恢复一条邀请关系、手动失效缓存、健康面板。

// adminBlockRelation 停止或恢复一条邀请关系的返还。
//
// 拉黑之后已发放的星屑不回收:星屑账本只追加,要收回只能由管理员手调
// (POST /admin/stardust/adjust),那是一个独立的、要填事由的决定。
func adminBlockRelation(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	var req struct {
		InviteeId int    `json:"invitee_id"`
		Blocked   bool   `json:"blocked"`
		Reason    string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.InviteeId <= 0 {
		// 与上面那条 400 刻意用不同的 code:前端要能分辨"我发错了报文"
		// 与"这一行本来就不是一条邀请关系"。
		badRequest(c, "qy_rel_no_relation", "这条记录没有关联的邀请关系,无法拉黑")
		return
	}

	ctx := c.Request.Context()
	// 受益人是**邀请人**,与 bind/rebind 同一条判据 —— 只是这条接口的报文里
	// 没有 inviter_id,得先按关系解析出来。
	//
	// 两个方向都动的是别人的钱:blocked=false 是"恢复返还",一个 role=10 管理员
	// 可以用它把上级基于风控停掉的、落在**自己名下**的返还重新打开;
	// blocked=true 则能把任意更高权限账号名下的进项静默停掉。这张表的 blocked
	// 列只有 setRelationBlocked 一个写入口,闸门漏在这里就是彻底没有。
	if denyActorOverTarget(c, "invite.relation.block", currentInviterId(ctx, req.InviteeId)) {
		return
	}
	before := relationSnapshot(ctx, req.InviteeId)
	inviterId, err := setRelationBlocked(ctx, req.InviteeId, req.Blocked, req.Reason)
	if err != nil {
		writeRelationAudit(c, "invite.relation.block", req.InviteeId, qymodel.ResultFail,
			blockVerb(req.Blocked)+"邀请关系的返还失败: "+err.Error()+" | 事由: "+req.Reason,
			before, relationSnapshot(ctx, req.InviteeId))
		respondRelationError(c, err)
		return
	}

	invalidateBlocked()
	writeRelationAudit(c, "invite.relation.block", req.InviteeId, qymodel.ResultOK,
		blockVerb(req.Blocked)+"邀请关系的返还(邀请人 "+itoa(inviterId)+"):"+
			blockOutcome(req.Blocked)+" | 事由: "+req.Reason,
		before, relationSnapshot(ctx, req.InviteeId))
	respond(c, gin.H{
		"invitee_id": req.InviteeId,
		"inviter_id": inviterId,
		"blocked":    req.Blocked,
	})
}

// blockVerb 与 blockOutcome 拼出这条审计的正文。
//
// 拆成方向相关的两段,是因为共用一句话曾经写出过自相矛盾的记录:审计正文是
// 事后仲裁"这一刻到底发生了什么"的唯一凭据,它不能两头都占。
// 动词跟界面走(停止返还 / 恢复返还):运营在界面上按的是那两个字,
// 事后来查审计时找的也是那两个字。
func blockVerb(blocked bool) string {
	if blocked {
		return "停止"
	}
	return "恢复"
}

func blockOutcome(blocked bool) string {
	if blocked {
		return "邀请关系保留(可追溯、可随时恢复),只停止未来的邀请返;已发放的星屑不回收"
	}
	return "从此刻起恢复邀请返;停止期间发生的消费与充值不补算,已发放的星屑不受影响"
}

// setRelationBlocked 把拉黑标记写进快照表,快照行缺失时按主库的权威字段补建。
//
// 返回这条关系的邀请人 id,供审计与响应回显 —— 拉黑之后运营最需要知道的是
// "我刚刚断掉的是谁的进项"。
func setRelationBlocked(ctx context.Context, inviteeId int, blocked bool, reason string) (int, error) {
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	if model.DB == nil {
		return 0, db.ErrNotReady
	}

	var invitee model.User
	err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username", "email", "inviter_id", "created_at").
		Where("id = ?", inviteeId).Take(&invitee).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, errRelUserMissing
	}
	if err != nil {
		return 0, err
	}

	var snaps []InviteRelation
	if err := gdb.WithContext(ctx).Where("invitee_id = ?", inviteeId).
		Limit(1).Find(&snaps).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}

	// 权威字段优先:主库说"现在绑着谁"才是这条关系此刻的样子。主库为 0 时
	// 回落到快照的 inviter_id —— 那是"已解绑但历史还在"的形状,对它解封
	// (blocked=false)仍然有意义。两边都没有,才是真的没有这条关系。
	inviterId := invitee.InviterId
	if inviterId == 0 && len(snaps) > 0 {
		inviterId = snaps[0].InviterId
	}
	if inviterId == 0 {
		return 0, errRelNotBound
	}

	now := common.GetTimestamp()
	if len(snaps) > 0 {
		// 只写 block_reason,**不碰 risk_flags** —— 后者是 ensureRelation 写的
		// 自动风控标记(reciprocal_invite),一次人工停/恢复就把它抹掉的话,
		// 关系页上那个徽标从此显示的是人写的话,而"系统判定为互刷"这个
		// 事实再也看不到了。两列各管各的。
		if err := gdb.WithContext(ctx).Model(&InviteRelation{}).
			Where("invitee_id = ?", inviteeId).
			Updates(map[string]any{
				"blocked":      blocked,
				"block_reason": truncate(reason, 255),
				"updated_at":   now,
			}).Error; err != nil {
			db.MarkFailure(err)
			return 0, err
		}
		return inviterId, nil
	}

	// 快照缺行。补一行而不是回 200 假装成功 —— blockedInvitees() 只认这张表,
	// 没有行就等于没拉黑。bound_at 取下线的注册时间:自动绑定发生在那一刻。
	row := InviteRelation{
		InviteeId:   inviteeId,
		InviterId:   inviterId,
		MaskedName:  truncate(MaskUsername(displayName(invitee)), 64),
		InviteeRef:  inviteeRef(inviteeId, refSalt()),
		BoundAt:     invitee.CreatedAt,
		BlockReason: truncate(reason, 255),
		Blocked:     blocked,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := gdb.WithContext(ctx).Save(&row).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	return inviterId, nil
}

// adminInvalidateCache 在管理员绕过本模块改过 users.inviter_id 或拉黑名单之后手动失效缓存。
func adminInvalidateCache(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	invalidateInviter(httpq.Int(c, "user_id", 0))
	invalidateBlocked()
	// 失效缓存本身不改任何账目,但它是"改完关系立刻让新关系生效"这条动作链的
	// 最后一步 —— 排查"这笔星屑为什么给了新上线"时,需要知道这一步发生在
	// 哪个时刻、是谁做的。
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryInvite,
		Action:       "invite.cache.invalidate",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: httpq.Int(c, "user_id", 0),
		Result:       qymodel.ResultOK,
		Reason:       "手动失效邀请缓存(邀请人/拉黑名单)",
	})
	respond(c, gin.H{"invalidated": true})
}

// adminHealth 暴露邀请关系链路的关键指标。
//
// cache_sync.enabled=false 或 failed 持续增长,意味着别的节点可能仍在按旧关系
// 或旧拉黑名单发放 —— 那是一段没有任何账本痕迹的错价窗口(见 cachesync.go)。
// 星屑自己的账本体检在 GET /admin/stardust/ledger-check。
func adminHealth(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	ctx := c.Request.Context()
	respond(c, gin.H{
		"hot_queue":         guard.QueueStats(),
		"inviter_cache":     inviterCacheStats(),
		"cache_sync":        cacheSyncStats(),
		"blocked_relations": len(blockedInvitees(ctx)),
		"logs_index": gin.H{
			"daily_consume": logsDailyConsumeIndexReady(),
			"token_daily":   logsIndexReady(logsTokenDailyIndex),
		},
	})
}
