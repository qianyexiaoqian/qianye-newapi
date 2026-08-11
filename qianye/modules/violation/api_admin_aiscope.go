package violation

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// api_admin_aiscope.go —— AI 审核作用域策略的管理端接口。
//
// # 这一页要回答的问题只有一个
//
// 「现在到底哪些分组在被 AI 审核监控、各自抽多少?」在此之前这个问题没有答案:
// 抽样率是全站一个数字,而项目方要的是「AI内容审核要可以监控分组」。
//
// 所以列表接口下发的不是策略行的数组,而是一张**按匹配顺序排好、末尾带兜底档**
// 的汇总表(summarizeAIScopes)。顺序就是热路径的判定顺序 —— 界面上排反了,
// 运营会照着一个错误的心智模型去调优先级,而调错了没有任何症状。
//
// # 写动作全部留痕
//
// 作用域与抽样率一起决定两件事:花多少钱、以及**谁的请求内容会被发往第三方**。
// 后者是本功能唯一一个对用户有外部影响的事实,加一个分组进来必须能事后追到人。

// aiScopeUpsertReq 是策略的新建/编辑入参。Id 为 0 表示新建。
//
// 抽样率走万分比整数而不是百分比小数,与设置页同口径:0.1 往返一次 JSON
// 就不再是 0.1,而抽样率是要能复现的量。
type aiScopeUpsertReq struct {
	Id                 int64  `json:"id"`
	Name               string `json:"name"`
	Enabled            bool   `json:"enabled"`
	Priority           int    `json:"priority"`
	ModelScope         string `json:"model_scope"`
	GroupScope         string `json:"group_scope"`
	GroupScopeMode     string `json:"group_scope_mode"`
	PreSampleRateBps   int    `json:"pre_sample_rate_bps"`
	AsyncSampleRateBps int    `json:"async_sample_rate_bps"`
	Remark             string `json:"remark"`
}

func (r *aiScopeUpsertReq) apply(dst *AIScope) error {
	dst.Name = r.Name
	dst.Enabled = r.Enabled
	dst.Priority = r.Priority
	dst.ModelScope = r.ModelScope
	dst.GroupScope = r.GroupScope
	dst.GroupScopeMode = r.GroupScopeMode
	dst.PreSampleRateBps = r.PreSampleRateBps
	dst.AsyncSampleRateBps = r.AsyncSampleRateBps
	dst.Remark = r.Remark
	return validateAIScope(dst)
}

// adminListAIScopes 返回策略表 + 兜底档的汇总,以及热路径当前真正在用的那一份。
func adminListAIScopes(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	rows := make([]AIScope, 0, 16)
	// 排序必须与 buildAIScopes 逐字相同:界面上的顺序就是线上的匹配顺序,
	// 两者不一致时"第一条匹配的说了算"这句话在界面上就是错的。
	if err := gdb.Order("priority asc, id asc").Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	var setting AISetting
	if err := gdb.Where("id = ?", 1).Take(&setting).Error; err != nil {
		setting = AISetting{Id: 1}
	}

	snap := Snapshot()
	// active_scopes 是**快照里真正生效的那一份**,不是这张表的回显。两者不同的
	// 场合很实在:刚存下的策略要等一次重载才进快照(写接口会主动重载,但
	// 多节点部署里别的节点最多晚一个刷新周期)。没有这一段,那个差别看不见。
	active := make([]gin.H, 0, 4)
	if snap.ai != nil {
		for _, s := range snap.ai.Scopes {
			active = append(active, gin.H{
				"id": s.Id, "name": s.Name,
				"pre_sample_rate_bps": s.PreBps, "async_sample_rate_bps": s.AsyncBps,
			})
		}
	}
	respond(c, gin.H{
		"items": rows,
		// summary 才是这一页的主体:它按匹配顺序排,末尾恒为兜底档,
		// 并标出被遮住(永远匹配不到)的行。
		"summary":          summarizeAIScopes(rows, setting.SampleRateBps),
		"fallback_bps":     setting.SampleRateBps,
		"max_scopes":       maxAIScopes,
		"ai_enabled":       setting.Enabled,
		"effective_active": snap.ai != nil,
		"active_scopes":    active,
	})
}

// adminUpsertAIScope 是新建与编辑的共用入口(请求体带 id 即为编辑)。
//
// 与违规类型同形:两者的校验、审计口径完全一致,拆成两条路由就是把同一段
// 逻辑抄两份,而漏抄的那一份大概率是审计。
func adminUpsertAIScope(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req aiScopeUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体解析失败")
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}

	now := common.GetTimestamp()
	row := AIScope{CreatedAt: now}
	var before *AIScope
	if req.Id > 0 {
		var cur AIScope
		if err := gdb.Where("id = ?", req.Id).Take(&cur).Error; err != nil {
			notFound(c)
			return
		}
		snapBefore := cur
		before = &snapBefore
		row = cur
	}
	if err := req.apply(&row); err != nil {
		writeAIScopeAudit(c, aiScopeAction(req.Id), qymodel.ResultFail, before, &row, err)
		badRequest(c, err.Error())
		return
	}
	// 条数上限挡的是热路径:sampleRatesFor 是每个请求都要跑的线性扫描。
	// 只数**启用中**的行,停用的档不参与匹配,留着它们没有代价。
	if row.Enabled {
		var enabled int64
		q := gdb.Model(&AIScope{}).Where("enabled = ?", true)
		if row.Id > 0 {
			q = q.Where("id <> ?", row.Id)
		}
		if err := q.Count(&enabled).Error; err != nil {
			writeAIScopeAudit(c, aiScopeAction(req.Id), qymodel.ResultFail, before, &row, err)
			internalError(c, err)
			return
		}
		if enabled >= int64(maxAIScopes) {
			err := fmt.Errorf("启用中的作用域策略已达上限 %d 条 —— 它是每个请求都要跑一遍的线性扫描,"+
				"请先停用不再需要的档", maxAIScopes)
			writeAIScopeAudit(c, aiScopeAction(req.Id), qymodel.ResultFail, before, &row, err)
			badRequest(c, err.Error())
			return
		}
	}
	row.UpdatedAt = now
	row.UpdatedBy = c.GetInt("id")

	if err := gdb.Save(&row).Error; err != nil {
		writeAIScopeAudit(c, aiScopeAction(req.Id), qymodel.ResultFail, before, &row, err)
		internalError(c, err)
		return
	}
	// 与渠道写入同一条理由:AI 配置与规则装在**同一份快照**里,而 reloadCtx 在
	// 版本未变时直接返回。不 bump 的话,刚存下的作用域要等到下一次有人改规则
	// 才生效,而中间这段时间界面显示"已启用",线上用的还是旧的一份。
	afterAIChange(c, "", nil, nil, func() {
		writeAIScopeAudit(c, aiScopeAction(req.Id), qymodel.ResultOK, before, &row, nil)
	})
	respond(c, row)
}

func adminDeleteAIScope(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "id 非法")
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var row AIScope
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		notFound(c)
		return
	}
	if err := gdb.Delete(&AIScope{}, "id = ?", id).Error; err != nil {
		writeAIScopeAudit(c, "ai_scope_delete", qymodel.ResultFail, &row, nil, err)
		internalError(c, err)
		return
	}
	afterAIChange(c, "", nil, nil, func() {
		writeAIScopeAudit(c, "ai_scope_delete", qymodel.ResultOK, &row, nil, nil)
	})
	respond(c, gin.H{"deleted": true})
}

func aiScopeAction(id int64) string {
	if id > 0 {
		return "ai_scope_update"
	}
	return "ai_scope_create"
}

// writeAIScopeAudit 是作用域策略全部写动作的唯一审计出口,成功与失败同一出口。
//
// # 为什么这张表的变更必须留痕
//
// 一条策略同时决定两件事:**谁的请求内容会被发往第三方**,以及为此花多少钱。
// 两者都没有任何用户可见的症状 —— 接口 200、界面正常、业务照跑。
// 把一个分组从 exclude 名单里拿掉、或者把它的抽样率从 1% 改成 50%,
// 事后只有 before/after 快照能回答"原来是什么样"。
//
// 失败那一条同样重要:被条数上限或校验挡下来的那次说明有人正在试图加档,
// 而"我配了三次都没保存上"只能靠失败审计回答。
func writeAIScopeAudit(c *gin.Context, action, result string, before, after *AIScope, err error) {
	reason := ""
	if err != nil {
		reason = truncate("失败: "+err.Error(), 512)
	}
	traceNo := ""
	if after != nil && after.Id > 0 {
		traceNo = fmt.Sprintf("ai_scope:%d", after.Id)
	} else if before != nil {
		traceNo = fmt.Sprintf("ai_scope:%d", before.Id)
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		TraceNo:     traceNo,
		BeforeSnap:  common.MapToJsonStr(aiScopeAuditSnap(before)),
		AfterSnap:   common.MapToJsonStr(aiScopeAuditSnap(after)),
	})
}

func aiScopeAuditSnap(s *AIScope) map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"id": s.Id, "name": s.Name, "enabled": s.Enabled, "priority": s.Priority,
		"model_scope": s.ModelScope, "group_scope": s.GroupScope,
		"group_scope_mode":      s.GroupScopeMode,
		"pre_sample_rate_bps":   s.PreSampleRateBps,
		"async_sample_rate_bps": s.AsyncSampleRateBps,
		"remark":                s.Remark,
	}
}
