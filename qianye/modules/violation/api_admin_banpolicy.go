package violation

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// api_admin_banpolicy.go —— 违规处置策略档的管理端接口。
//
// 四个动作:列表、写入(新建/编辑同一入口)、删除、影响面预览。
// 每一个写动作都写审计 —— 这张表决定谁被限制、在第几次被限制,
// 它比规则表更接近"直接改账号状态"。

// banPolicyUpsertReq 是策略档的新建/编辑入参。
//
// 没有 is_default 字段:兜底档的身份由**路径**决定(PUT /ban-policies/default),
// 不由请求体决定。让请求体能声明 is_default 就等于开了两条路 ——
// 一条能把普通档提升成第二个兜底档(唯一索引会拦,但错误信息毫无意义),
// 一条能把兜底档降级成普通档(那就是"删掉兜底档"的另一种写法)。
type banPolicyUpsertReq struct {
	UserGroup   string `json:"user_group"`
	Enabled     bool   `json:"enabled"`
	WindowHours int    `json:"window_hours"`
	Threshold   int    `json:"threshold"`
	Action      string `json:"action"`
	Remark      string `json:"remark"`
	// Confirm 是二次确认位。收紧阈值会**立刻**把一批存量账号推过线,
	// 所以任何会扩大处置面的写入都要求前端先拉一次影响面预览、再带着这一位提交。
	// 服务端不信任前端有没有真的弹过窗,它信任的是"这一位只可能由那次交互产生"。
	Confirm bool `json:"confirm"`
}

// errDefaultBanPolicyUndeletable 同时充当报文与审计 Reason:两处必须是同一句话,
// 否则运营在界面上看到的拒绝理由与审计里记的拒绝理由对不上。
var errDefaultBanPolicyUndeletable = errors.New(
	"默认兜底策略不可删除:删掉它会让所有没有专属策略的用户分组落进一个不存在的策略")

func adminListBanPolicies(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	rows := make([]BanPolicy, 0, 16)
	// 兜底档排在最前:它是全部没配分组的落点,读这张表的人第一眼要看的就是它。
	if err := gdb.Order("is_default desc, user_group asc").Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	snap := banPolicies()
	respond(c, gin.H{
		"items": rows,
		// fallback_from_db 回答"我现在到底跑在哪一份兜底上"。
		// 它为 false 时兜底值来自 YAML(库里没有兜底行,或快照从未加载成功),
		// 此时管理员在这张表里改任何东西都不会影响没配分组的用户 ——
		// 不下发这一位的话,这个落差只有读源码才能发现。
		"fallback_from_db": snap.fromDB,
		"fallback": gin.H{
			"window_hours": snap.fallback.WindowHours,
			"threshold":    snap.fallback.Threshold,
			"action":       snap.fallback.Action,
		},
		"actions": []gin.H{
			{"value": PolicyActionRecord, "label": "仅记录"},
			{"value": PolicyActionRestrict, "label": "限制账号"},
			{"value": PolicyActionBan, "label": "封号"},
		},
	})
}

// adminUpsertBanPolicy 新建或编辑一档策略。
//
// 兜底档与普通档共用这一个处理函数,由 isDefault 区分 —— 两者的校验、审计、
// 影响面口径完全相同,拆成两个函数就是把同一段逻辑抄两份,而漏抄的那一份
// 大概率是二次确认。
func adminUpsertBanPolicy(c *gin.Context, isDefault bool) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var req banPolicyUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}

	next := BanPolicy{
		IsDefault:   isDefault,
		Enabled:     req.Enabled,
		WindowHours: req.WindowHours,
		Threshold:   req.Threshold,
		Action:      strings.ToLower(strings.TrimSpace(req.Action)),
		Remark:      req.Remark,
	}
	if isDefault {
		// 兜底档没有"停用"的概念:它没有可回落的下一级,停用等于让没配分组的
		// 用户落进一个不存在的策略。恒为启用,不给这个开关。
		next.Enabled = true
		next.UserGroup = ""
	} else {
		// Normalize 而不是 Effective:空分组必须落到 validateBanPolicy 的拒绝分支,
		// 而不是被静默改写成 "default" 那一档 —— 那会覆盖掉一个真实存在的分组策略。
		next.UserGroup = groupname.Normalize(req.UserGroup)
	}
	if err := validateBanPolicy(&next); err != nil {
		badRequest(c, err.Error())
		return
	}

	var before BanPolicy
	hasBefore := false
	q := gdb.Where("user_group = ?", next.UserGroup)
	if isDefault {
		q = gdb.Where("is_default = ?", true)
	}
	if err := q.Take(&before).Error; err == nil {
		hasBefore = true
	}

	// 影响面在**写入之前**算,并且只有在这次写入会扩大处置面时才要求确认。
	// "扩大"的判据是三选一:阈值变小、窗口变长、动作变重 —— 三者都会让原本
	// 够不到的账号够到。放宽(阈值变大、动作变轻)不需要拦,那不会让任何人越线。
	//
	// # 保存本身不处置任何人 —— 报文口径必须与此一致
	//
	// 这个函数只写策略表 + 失效快照,全程不调 disableUserForViolation
	// (它只有两个调用点:counter.go 的一次新命中之后、tasks.go 的 pending 行重试,
	// 而保存不创建任何 Ban 行,所以也没有可重试的对象)。已越线的存量账号会在
	// **各自下一次违规命中**时才按新策略处置。
	//
	// 所以 impact.Matched 的语义是"已经越过新阈值的存量账号数",不是"这次保存
	// 会封掉几个人"。这两句话在管理员眼里差别极大:按前者他会去盯下一波命中,
	// 按后者他会去查封禁列表 —— 而那张表在保存之后一行都不会多。报文、界面文案
	// 与本注释必须一起改,否则二次确认这道护栏本身就在误导人。
	tightens := tightensBanPolicy(hasBefore, before, next)
	impact, impactErr := countBanPolicyImpact(c.Request.Context(), gdb, model.DB,
		next.UserGroup, next.Threshold, next.WindowHours, next.Action)
	if impactErr != nil {
		// 算不出影响面**不能**一律 500。放宽策略(降严厉度、抬阈值)恰恰是事故当天
		// 最需要能执行的动作,而事故当天主库慢查询正是算不出影响面的原因 ——
		// 把两者绑在一起,等于在最需要放宽的时刻锁死放宽。
		//
		// 所以只对"会扩大处置面"的写入要求确认,并如实告诉管理员这次没能评估。
		common.SysError("qianye/violation: 封号策略影响面评估失败: " + impactErr.Error())
		if !req.Confirm && tightens {
			respondFailData(c, 409, "confirm_required",
				"无法评估这次调整会让多少存量账号越线(影响面查询失败),确认后才会保存",
				gin.H{"impact": impact, "impact_error": true})
			return
		}
	} else if !req.Confirm && impact.Matched > 0 && tightens {
		// 409 而不是 400:请求本身是合法的,只是需要人再看一眼。
		// 把影响面原样带回去,前端不必再发一次预览请求(那次请求与这次之间
		// 计数还会变,两个数对不上只会让人更不敢按)。
		respondFailData(c, 409, "confirm_required",
			fmt.Sprintf("这次收紧之后已有 %d 个存量账号越过新阈值,他们会在各自下一次违规命中时按新策略处置"+
				"(保存本身不会处置任何人),请确认后重试", impact.Matched),
			gin.H{"impact": impact})
		return
	}

	now := common.GetTimestamp()
	next.UpdatedAt = now
	next.UpdatedBy = c.GetInt("id")
	if hasBefore {
		next.Id = before.Id
		next.CreatedAt = before.CreatedAt
	} else {
		next.CreatedAt = now
	}
	// Save 全字段写:Updates(struct) 会跳过零值,而这张表上 threshold=0
	// (不处置)与 enabled=false 都是**有意义的取值**,跳过它们等于改不掉。
	if err := gdb.Save(&next).Error; err != nil {
		db.MarkFailure(err)
		writeBanPolicyAudit(c, "ban_policies.upsert", qymodel.ResultFail, before, next, impact, err)
		internalError(c, err)
		return
	}
	invalidateBanPolicies()
	writeBanPolicyAudit(c, "ban_policies.upsert", qymodel.ResultOK, before, next, impact, nil)
	respond(c, gin.H{"policy": next, "impact": impact})
}

// tightensBanPolicy 判断一次写入是否会扩大处置面。
//
// 三条判据是 OR:任意一条成立都会让原本够不到阈值的账号够到。
//   - 阈值变小(含"从不处置 0 变成一个正数"),
//   - 窗口变长(更久以前的命中重新算数),
//   - 动作变重(record → restrict → ban)。
//
// 新建一档(hasBefore=false)一律算收紧:它把这个分组从兜底档挪走,
// 而兜底档的阈值可能比新档宽松得多。不去比较兜底档 —— 那是一次跨行推导,
// 而这里要的是一个不会算错方向的保守判据。
func tightensBanPolicy(hasBefore bool, before, next BanPolicy) bool {
	if !hasBefore {
		return true
	}
	if next.Threshold > 0 && (before.Threshold <= 0 || next.Threshold < before.Threshold) {
		return true
	}
	if next.WindowHours > before.WindowHours {
		return true
	}
	return policyActionWeight(next.Action) > policyActionWeight(before.Action)
}

// policyActionWeight 给三档动作一个可比较的严厉度。
// 未知取值取 0(最轻):它只可能来自历史行或手工 SQL,把它当成最重会
// 让一次无害的编辑被误判成收紧,从而要求一次没必要的二次确认。
func policyActionWeight(action string) int {
	switch action {
	case PolicyActionRecord:
		return 1
	case PolicyActionRestrict:
		return 2
	case PolicyActionBan:
		return 3
	}
	return 0
}

// adminDeleteBanPolicy 删除一档策略。**兜底档永远删不掉。**
//
// 这是"兜底档不可删"的第一道锁(另两道见 model.go 的 BanPolicy)。
// 判据用的是库里那一行的 is_default,不是路径参数或请求体里的任何东西 ——
// 前者是事实,后者是调用方说的话。
func adminDeleteBanPolicy(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok || id <= 0 {
		badRequest(c, "非法的策略 id")
		return
	}
	var row BanPolicy
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		badRequest(c, "策略档不存在")
		return
	}
	if row.IsDefault {
		// 这一次拒绝也要留痕。它不是"入参格式不对"那一类(那些一条库都没碰、
		// 也没有指向任何一行),而是一次**对着库里某一行发起的破坏性动作被挡下**。
		// 事后要回答的问题是"谁试过删兜底档",不写这一条就永远查不到 ——
		// 同模块 bind/unbind/adjust 三条路径的失败也都留痕,口径必须一致。
		writeBanPolicyAudit(c, "ban_policies.delete", qymodel.ResultFail, row, BanPolicy{},
			banPolicyImpact{}, errDefaultBanPolicyUndeletable)
		badRequest(c, errDefaultBanPolicyUndeletable.Error())
		return
	}
	if err := gdb.Where("id = ? AND is_default = ?", id, false).Delete(&BanPolicy{}).Error; err != nil {
		db.MarkFailure(err)
		writeBanPolicyAudit(c, "ban_policies.delete", qymodel.ResultFail, row, BanPolicy{}, banPolicyImpact{}, err)
		internalError(c, err)
		return
	}
	invalidateBanPolicies()
	writeBanPolicyAudit(c, "ban_policies.delete", qymodel.ResultOK, row, BanPolicy{}, banPolicyImpact{}, nil)
	respond(c, gin.H{"deleted": true})
}

// adminBanPolicyImpact 是"改成这套阈值,立刻会处置几个人"的只读预览。
//
// 独立成一个 GET 接口(而不是只在保存时回 409)是因为管理员需要**在填表的过程中**
// 看到这个数,而不是在按下保存之后。一个只在保存时才出现的数字,
// 会让人先按一次保存去探路 —— 那正是这个功能要防的动作。
func adminBanPolicyImpact(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	// 空查询参数 = 兜底档 = 不加分组过滤,所以这里是 Normalize 不是 Effective。
	group := groupname.Normalize(c.Query("user_group"))
	action := strings.ToLower(strings.TrimSpace(c.Query("action")))
	if action == "" {
		action = PolicyActionBan
	}
	impact, err := countBanPolicyImpact(c.Request.Context(), gdb, model.DB, group,
		httpq.Int(c, "threshold", 0), httpq.Int(c, "window_hours", 24), action)
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{
		"impact": impact,
		// 兜底档的影响面按"全部分组"算(见 countBanPolicyImpact 的说明),
		// 这个口径必须跟着结果一起下发,否则界面会把一个全站数字标成某个分组的。
		"scope_all_groups": group == "",
	})
}

// writeBanPolicyAudit 是这张表全部写动作的唯一审计出口,成功与失败同一出口。
//
// 一个出口而不是每个 handler 各写一段:漏写的那一次一定是失败分支
// (它在"接口返回 500"的阴影里最不显眼),而失败分支恰恰是要查的 ——
// "我改了三次都没生效"只能靠失败审计回答。
func writeBanPolicyAudit(c *gin.Context, action string, result string, before, after BanPolicy, impact banPolicyImpact, err error) {
	reason := ""
	if err != nil {
		reason = truncate("失败: "+err.Error(), 512)
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		BeforeSnap:  common.MapToJsonStr(banPolicyAuditSnap(before)),
		// 影响面进 after 快照:事后复盘"这次调整封了多少人"的唯一依据。
		// 它是写入**当时**算出来的数,几分钟后已经无法复现。
		AfterSnap: common.MapToJsonStr(map[string]any{
			"policy":        banPolicyAuditSnap(after),
			"impact_count":  impact.Matched,
			"impact_capped": impact.Capped,
			"impact_sample": impact.UserIds,
		}),
	})
}

func banPolicyAuditSnap(p BanPolicy) map[string]any {
	return map[string]any{
		"id": p.Id, "user_group": p.UserGroup, "is_default": p.IsDefault,
		"enabled": p.Enabled, "window_hours": p.WindowHours,
		"threshold": p.Threshold, "action": p.Action,
	}
}
