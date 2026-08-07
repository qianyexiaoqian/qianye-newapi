package groupns

// modelgroup_api.go —— 模型分组登记表的管理端写接口:备注编辑 + 删除联动。
//
// ══════════════════ 编辑器唯一性(与 api_admin.go 的约束同源)══════════════════
//
//	display_name / note / enabled / sort_order   只在本文件编辑(模型分组登记)
//	GroupRatio / AutoGroups / MaxTokenAutoGroups 只在上游「模型分组」页编辑
//	UserUsableGroups                             只在「用户分组可用的模型分组配置」页编辑
//
// 删除是唯一的例外,而且是**刻意**的:它必须同时改上面全部三处,否则就只是把
// 一行从倍率表里拿掉、剩下十一处引用原样留着(见 modelgroup_store.go 文件头)。
// 所以删除走这一个端点、写一次审计,而不是让运营分别去三个页面各删一次 ——
// 那三次之间的任何一次中断都会留下一个说不清的中间态。

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// maxModelGroupNoteLen 与 qy_model_groups.note 的列宽一致(varchar(255))。
//
// 按**字符**判而不是按字节:MySQL/PostgreSQL 的 varchar(N) 计的是字符,
// 而按字节裁一段中文会留下半个 UTF-8 序列,MySQL 在 STRICT_TRANS_TABLES 下
// 以 Error 1366 拒绝整条 UPDATE —— 表现是"保存失败"却没人说得清为什么。
// 这里**拒绝**而不是截断:截断出来的说明文案会少最后一句,
// 而那一句往往正是"本站不留存数据"这种要紧的话。
const maxModelGroupNoteLen = 255

// maxModelGroupDisplayNameLen 与 display_name 的列宽一致(varchar(64))。
const maxModelGroupDisplayNameLen = 64

// updateModelGroupRequest 是备注编辑的请求体。
//
// 四个字段**全部用指针**:这是一个部分更新接口,而 `enabled: false` 与
// 「这次没提交 enabled」必须区分得开 —— 用零值表达"没给"会让前端只想改备注的
// 那一次请求把 enabled 静默改成 false,而 enabled=false 会在下拉里遮断这个分组。
type updateModelGroupRequest struct {
	DisplayName *string `json:"display_name"`
	Note        *string `json:"note"`
	Enabled     *bool   `json:"enabled"`
	SortOrder   *int    `json:"sort_order"`
}

// adminUpdateModelGroup 编辑一条模型分组登记(显示名 / 备注 / 启停 / 排序)。
//
// **它一个倍率字节都不写。** 真相源恒为 options.GroupRatio,理由见 model.go。
func adminUpdateModelGroup(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		badRequest(c, "qy_invalid_param", "缺少模型分组名")
		return
	}
	var req updateModelGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.Note != nil && utf8.RuneCountInString(*req.Note) > maxModelGroupNoteLen {
		badRequest(c, "qy_invalid_param", fmt.Sprintf(
			"备注最多 %d 个字符(当前 %d)。它会原样显示给用户,"+
				"截断会把最后一句话吃掉,所以这里直接拒绝而不是截断",
			maxModelGroupNoteLen, utf8.RuneCountInString(*req.Note)))
		return
	}
	if req.DisplayName != nil && utf8.RuneCountInString(*req.DisplayName) > maxModelGroupDisplayNameLen {
		badRequest(c, "qy_invalid_param", fmt.Sprintf(
			"显示名最多 %d 个字符", maxModelGroupDisplayNameLen))
		return
	}

	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var before ModelGroup
	if err := gdb.WithContext(c).Where("name = ?", name).First(&before).Error; err != nil {
		badRequest(c, "qy_groupns_unknown",
			"模型分组 "+name+" 还没有登记。请先执行一次回填,"+
				"或确认这个名字确实存在于分组倍率表 / abilities 里")
		return
	}

	updates := map[string]any{"operator_id": c.GetInt("id"), "updated_at": now()}
	after := before
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
		after.DisplayName = *req.DisplayName
	}
	if req.Note != nil {
		updates["note"] = *req.Note
		after.Note = *req.Note
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
		after.Enabled = *req.Enabled
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
		after.SortOrder = *req.SortOrder
	}

	if err := gdb.WithContext(c).Model(&ModelGroup{}).Where("name = ?", name).
		Updates(updates).Error; err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "groupns.model_group.update", Result: qymodel.ResultFail,
			Reason: "编辑模型分组登记失败: " + err.Error(),
			Before: before, After: after,
		})
		internalError(c, err)
		return
	}
	// 备注一改,全站说明文案就跟着变(它经 setting.QyDescribeGroup 覆盖白名单原文),
	// 所以必须立刻重载快照 —— 否则运营改完刷新页面看到的还是旧文案,
	// 会以为没保存成功而再改一遍。
	if reloadErr := InvalidateAndReload(); reloadErr != nil {
		common.SysError("qianye/groupns: 编辑模型分组后重载快照失败: " + reloadErr.Error())
	}

	reason := "编辑模型分组登记"
	if req.Note != nil {
		// 备注**面向用户**:它会出现在令牌分组下拉与模型广场上,并且**覆盖**
		// options.UserUsableGroups 里那段说明。改它等于改一段对外文案,
		// 所以要在审计正文里单独点名,而不是只留在 before/after 的字段差里。
		reason += ";备注已更新(它会覆盖全局可选清单里那段说明,直接显示给用户)"
	}
	if req.Enabled != nil && !*req.Enabled {
		reason += ";已停用(只在下拉与可选清单里遮断,**不触碰 abilities、不影响路由**)"
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "groupns.model_group.update", Result: qymodel.ResultOK,
		Reason: reason, Before: before, After: after,
	})
	respond(c, after)
}

// adminModelGroupImpact 是删除前的影响面预览。**只读。**
//
// 它与删除端点调用**同一个** ProbeModelGroupImpact:预览与执行判据分家的表现是
// 「预览说能删、点删除被拒」,或者更糟的「预览说没影响、删完一批人 403」。
func adminModelGroupImpact(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		badRequest(c, "qy_invalid_param", "缺少模型分组名")
		return
	}
	impact, err := ProbeModelGroupImpact(c, model.DB, db.Get(), name)
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, impact)
}

// adminDeleteModelGroup 删除一个模型分组并清掉它的全部引用。
//
// 闸门、顺序、以及 channels/abilities 为什么一行都不动,全部写在
// modelgroup_store.go 的文件头。这里只负责:参数、审计、以及把半成状态
// **原样**交给前端。
func adminDeleteModelGroup(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		badRequest(c, "qy_invalid_param", "缺少模型分组名")
		return
	}
	var opts DeleteModelGroupOptions
	// DELETE 允许没有请求体。绑定失败一律按"两道覆盖都没勾"处理 ——
	// 覆盖必须是显式动作,解析不出来时倒向"不覆盖"。
	_ = c.ShouldBindJSON(&opts)

	res, err := DeleteModelGroup(c, model.DB, db.Get(), name, opts, c.GetInt("id"))
	if err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "groupns.model_group.delete", Result: qymodel.ResultFail,
			Reason: "删除模型分组 " + name + " 被拒绝或失败: " + err.Error(),
			Before: res.Impact,
		})
		// 三种拒绝都是**运营可以自己解决**的配置冲突,不是服务端故障,所以是 400。
		// 错误码前缀让前端能把"要勾覆盖"与"去别的页面先处理"分开渲染。
		if code, ok := modelGroupRejectCode(err); ok {
			badRequest(c, code, err.Error())
			return
		}
		internalError(c, err)
		return
	}

	reason := fmt.Sprintf(
		"删除模型分组(清理 options:%s;授权/套餐解锁等残留见 before 快照)",
		strings.Join(res.RemovedFrom, "、"))
	if opts.ForceHasRoute {
		// 强制覆盖必须落进审计**正文**,不是一个布尔字段:事故复盘时第一个问题
		// 就是"有没有人点过那个强制勾选"。
		reason += fmt.Sprintf(
			"(**强制覆盖渠道闸门**:仍有 %d 个 enabled 渠道 / %d 行 abilities 绑着它,"+
				"它们保持原样,从此「有渠道但谁都选不到」)",
			res.Impact.EnabledChannels, res.Impact.AbilityRows)
	}
	if opts.ForceOrphanTokens {
		reason += fmt.Sprintf(
			"(**强制覆盖孤儿令牌闸门**:%d 个启用令牌变成孤儿,属于 %d 个用户,"+
				"它们下一次请求起全部被拒)",
			res.Impact.Tokens, res.Impact.TokenOwners)
	}
	result := qymodel.ResultOK
	if res.Partial != nil {
		// 半成必须记成失败:一条 Result=ok 的记录会让复盘的人直接跳过它,
		// 而这正是最需要被读到的一条。
		result = qymodel.ResultFail
		reason += ";**部分失败**:" + res.Partial.Message
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "groupns.model_group.delete", Result: result,
		Reason: reason, Before: res.Impact, After: res,
	})
	respond(c, res)
}

// modelGroupRejectCode 把 DeleteModelGroup 的三种"配置冲突"错误映射成前端能分辨的码。
//
// 用前缀而不是 errors.Is 的哨兵值:这三条错误的**正文**才是主要内容
// (它们每一条都写着下一步该做什么),包一层 sentinel 只会诱使调用方丢掉正文
// 改用一句通用文案,而通用文案在这里等于没有信息。
func modelGroupRejectCode(err error) (string, bool) {
	for _, code := range []string{
		"qy_modelgroup_blocked",
		"qy_modelgroup_has_route",
		"qy_modelgroup_orphan_tokens",
	} {
		if strings.HasPrefix(err.Error(), code+":") {
			return code, true
		}
	}
	return "", false
}

// ModelGroupNote 是 setting.QyGroupNote 的实现体:模型分组登记表里的备注。
//
// ══════════════════════════ 硬约束 ══════════════════════════
//
//  1. **零 I/O、零锁。** 它被 setting.GetUserUsableGroupsCopy 调用,而后者挂在
//     middleware/auth.go 的令牌分组校验上,每一个带令牌分组的 relay 请求会为
//     白名单里的**每一个键**调用一次。只允许 atomic.Pointer.Load 与 map 查找。
//  2. **未配置必须返回空串。** 空串是"回落上游文案"的唯一信号;返回分组名会把
//     每一个没写备注的分组的说明都覆盖成它自己的名字。
//  3. 刻意**不看 enabled**:停用只在可见性层遮断,而一个已经渲染出来的分组
//     (例如被套餐解锁的)仍然应该显示它的备注。让备注跟着 enabled 一起消失,
//     表现是"停用之后说明文案变了",而那两件事毫无关系。
func ModelGroupNote(groupName string) string {
	if !enabled() || groupName == "" {
		return ""
	}
	s := activeSnapshot()
	if s == nil {
		return ""
	}
	return s.ModelGroups[groupName].Note
}
