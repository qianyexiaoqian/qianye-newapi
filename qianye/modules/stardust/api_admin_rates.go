package stardust

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// api_admin_rates.go —— 两张按键覆盖的比例表:按用户分组的比例档(qy_sd_group_rate)
// 与套餐上的返还定义(qy_sd_plan_reward)。
//
// 两张表都是"改一次就改变此后每一笔返还"的配置,所以每个写动作成功与失败各留一条审计,
// 并在落库之后失效本进程缓存(groupRates / planRewards 各缓存 60 秒)。

// 审计动作名。前端按 qy_audit_<action> 做 i18n,改任何一个都是接口变更。
const (
	auditGroupRatePut    = "stardust.group_rate.put"
	auditGroupRateDelete = "stardust.group_rate.delete"
	auditPlanRewardPut   = "stardust.plan_reward.put"
	auditPlanRewardDel   = "stardust.plan_reward.delete"
)

func init() {
	adminRouteInstallers = append(adminRouteInstallers, func(g *gin.RouterGroup) {
		crit := middleware.CriticalRateLimit()
		g.GET("/stardust/group-rates", handleListGroupRates)
		g.PUT("/stardust/group-rates/:group", crit, handlePutGroupRate)
		g.DELETE("/stardust/group-rates/:group", crit, handleDeleteGroupRate)
		g.GET("/stardust/plan-rewards/:plan_id", handleGetPlanReward)
		g.PUT("/stardust/plan-rewards/:plan_id", crit, handlePutPlanReward)
		g.DELETE("/stardust/plan-rewards/:plan_id", crit, handleDeletePlanReward)
	})
}

// knownGroups 返回可配比例档的分组名集合,键已经 groupname.Normalize。
//
// 口径与 usergroup/groups.go 的 groupExists 同源:分组倍率表(GroupRatio 的键)∪
// 用户分组登记表(qy_user_groups,经 service.QyDeclaredUserGroups),去掉 auto ——
// 那是令牌的伪分组,users.group 里不会有它。GET 下发的 groups 与 PUT 的判据都取自
// 这一个函数:两份清单各列一遍的表现永远是同一种,下拉里选得到、保存时报"未登记"。
// 两份清单都是内存查找,没有 I/O。
func knownGroups() map[string]bool {
	out := map[string]bool{}
	for name := range ratio_setting.GetGroupRatioCopy() {
		if n := groupname.Normalize(name); n != "" && n != "auto" {
			out[n] = true
		}
	}
	for _, name := range service.QyDeclaredUserGroups() {
		if n := groupname.Normalize(name); n != "" && n != "auto" {
			out[n] = true
		}
	}
	return out
}

// writeConfigUpdateAudit 是本文件全部写动作的审计出口,成功与失败同一出口。
// before / after 传行的视图(nil 渲染成空串 = 该行不存在)。
func writeConfigUpdateAudit(c *gin.Context, action, result, reason string, before, after any) {
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: action, Result: result, Reason: reason, Before: before, After: after,
	})
}

// handleListGroupRates 返回全部比例档(含禁用的)与可选分组名清单。
func handleListGroupRates(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	rows := make([]GroupRate, 0, 16)
	if err := gdb.WithContext(ctx).Order("user_group asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("读取分组比例档", err))
		return
	}
	known := knownGroups()
	groups := make([]string, 0, len(known))
	for name := range known {
		groups = append(groups, name)
	}
	sort.Strings(groups)
	respondOK(c, gin.H{"items": rows, "groups": groups})
}

// handlePutGroupRate 整行 upsert 一个分组的比例档。
//
// 分组名必须在 knownGroups 里:让"比例行挂在一个不存在的分组名上"不可能出现 ——
// 那样的行永远不会命中,而运营会一直以为自己配了。四个比例都是指针三态:
// 缺失 / null = 本档回落全站默认,0 = 显式不返。
func handlePutGroupRate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	key := groupname.Normalize(c.Param("group"))
	var req struct {
		ConsumeBps       *int `json:"consume_bps"`
		InviteTopupBps   *int `json:"invite_topup_bps"`
		InviteRedeemBps  *int `json:"invite_redeem_bps"`
		InviteConsumeBps *int `json:"invite_consume_bps"`
		Enabled          bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeConfigUpdateAudit(c, auditGroupRatePut, qymodel.ResultFail, "请求体解析失败: "+key, nil, nil)
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	if key == "" || !knownGroups()[key] {
		writeConfigUpdateAudit(c, auditGroupRatePut, qymodel.ResultFail, "分组未登记: "+key, nil, nil)
		respondErr(c, errGroupUnknown)
		return
	}
	for _, item := range []struct {
		name  string
		value *int
	}{
		{"consume_bps", req.ConsumeBps},
		{"invite_topup_bps", req.InviteTopupBps},
		{"invite_redeem_bps", req.InviteRedeemBps},
		{"invite_consume_bps", req.InviteConsumeBps},
	} {
		if item.value != nil && (*item.value < 0 || *item.value > maxBps) {
			reason := item.name + " 必须落在 [0, " + strconv.Itoa(maxBps) + "],收到 " + strconv.Itoa(*item.value)
			writeConfigUpdateAudit(c, auditGroupRatePut, qymodel.ResultFail, reason+"(分组 "+key+")", nil, nil)
			respondErr(c, errBadRequest(reason))
			return
		}
	}

	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	before, err := findGroupRate(ctx, key)
	if err != nil {
		respondErr(c, err)
		return
	}
	row := GroupRate{
		UserGroup: key, ConsumeBps: req.ConsumeBps, InviteTopupBps: req.InviteTopupBps,
		InviteRedeemBps: req.InviteRedeemBps, InviteConsumeBps: req.InviteConsumeBps, Enabled: req.Enabled,
		OperatorId: c.GetInt("id"), UpdatedAt: common.GetTimestamp(),
	}
	err = gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_group"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"consume_bps", "invite_topup_bps", "invite_redeem_bps", "invite_consume_bps", "enabled", "operator_id", "updated_at",
		}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
		writeConfigUpdateAudit(c, auditGroupRatePut, qymodel.ResultFail, "写入失败: "+err.Error(), before, nil)
		respondErr(c, wrapInternal("写入分组比例档", err))
		return
	}
	// 失效在落库之后:比例是逐笔冻结进流水的,窗口内按旧行发放的每一笔都不追溯。
	invalidateGroupRates()
	writeConfigUpdateAudit(c, auditGroupRatePut, qymodel.ResultOK, "", before, row)
	respondOK(c, row)
}

// handleDeleteGroupRate 删掉一个分组的比例档,此后该分组回落全站默认。
// 行本来就不存在时同样 200(deleted=false):删除是幂等动作,重复点一次不该报错。
func handleDeleteGroupRate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	key := groupname.Normalize(c.Param("group"))
	if key == "" {
		respondErr(c, errBadRequest("必须指定分组名"))
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	before, err := findGroupRate(ctx, key)
	if err != nil {
		respondErr(c, err)
		return
	}
	res := gdb.WithContext(ctx).Where("user_group = ?", key).Delete(&GroupRate{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		writeConfigUpdateAudit(c, auditGroupRateDelete, qymodel.ResultFail, "删除失败: "+res.Error.Error(), before, nil)
		respondErr(c, wrapInternal("删除分组比例档", res.Error))
		return
	}
	invalidateGroupRates()
	writeConfigUpdateAudit(c, auditGroupRateDelete, qymodel.ResultOK, "分组 "+key, before, nil)
	respondOK(c, gin.H{"user_group": key, "deleted": res.RowsAffected == 1})
}

// findGroupRate 读一行比例档,不存在返回 nil(审计快照据此渲染成空串)。
func findGroupRate(ctx context.Context, key string) (*GroupRate, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []GroupRate
	if err := gdb.WithContext(ctx).Where("user_group = ?", key).Limit(1).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取分组比例档", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// planRewardView 是套餐返还定义的下发形状;sources 拆成数组,exists 说明是否配过。
type planRewardView struct {
	PlanId     int      `json:"plan_id"`
	BuyerBps   int      `json:"buyer_bps"`
	InviterBps int      `json:"inviter_bps"`
	Sources    []string `json:"sources"`
	Exists     bool     `json:"exists"`
}

func newPlanRewardView(r PlanReward, exists bool) planRewardView {
	sources := make([]string, 0, 4)
	for _, s := range strings.Split(r.Sources, ",") {
		if s != "" {
			sources = append(sources, s)
		}
	}
	return planRewardView{PlanId: r.PlanId, BuyerBps: r.BuyerBps, InviterBps: r.InviterBps, Sources: sources, Exists: exists}
}

// planIdParam 解析 /:plan_id。主库 subscription_plans.id 是 int,超出 int32 的值
// 不可能是一个真实套餐,与非数字同样按参数错误处理。
func planIdParam(c *gin.Context) (int, bool) {
	v, ok := httpq.PathInt64(c, "plan_id")
	if !ok || v > math.MaxInt32 {
		return 0, false
	}
	return int(v), true
}

// handleGetPlanReward 返回套餐的返还定义;没配过的返回默认口径且 exists=false。
func handleGetPlanReward(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	planId, ok := planIdParam(c)
	if !ok {
		respondErr(c, errBadRequest("plan_id 不合法"))
		return
	}
	r, exists := planRewardFor(c.Request.Context(), planId)
	respondOK(c, newPlanRewardView(r, exists))
}

// handlePutPlanReward 写入(upsert)套餐的返还定义。
//
// sources 先 join 再过 NormalizeSources:闭集之外的词整个拒绝(qy_sd_bad_source),
// 而不是静默丢掉那个词 —— 丢掉的表现是运营以为配了 admin,实际管理员授予的套餐
// 一颗星屑都不返。空数组按默认口径(order,balance)存。
func handlePutPlanReward(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	planId, ok := planIdParam(c)
	if !ok {
		respondErr(c, errBadRequest("plan_id 不合法"))
		return
	}
	var req struct {
		BuyerBps   int      `json:"buyer_bps"`
		InviterBps int      `json:"inviter_bps"`
		Sources    []string `json:"sources"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeConfigUpdateAudit(c, auditPlanRewardPut, qymodel.ResultFail, "请求体解析失败: plan "+strconv.Itoa(planId), nil, nil)
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	sources, valid := NormalizeSources(strings.Join(req.Sources, ","))
	if !valid {
		writeConfigUpdateAudit(c, auditPlanRewardPut, qymodel.ResultFail,
			"来源不在闭集内: "+strings.Join(req.Sources, ",")+"(plan "+strconv.Itoa(planId)+")", nil, nil)
		respondErr(c, errBadSource)
		return
	}
	for _, item := range []struct {
		name  string
		value int
	}{{"buyer_bps", req.BuyerBps}, {"inviter_bps", req.InviterBps}} {
		if item.value < 0 || item.value > maxBps {
			reason := item.name + " 必须落在 [0, " + strconv.Itoa(maxBps) + "],收到 " + strconv.Itoa(item.value)
			writeConfigUpdateAudit(c, auditPlanRewardPut, qymodel.ResultFail, reason+"(plan "+strconv.Itoa(planId)+")", nil, nil)
			respondErr(c, errBadRequest(reason))
			return
		}
	}

	ctx := c.Request.Context()
	prev, existed := planRewardFor(ctx, planId)
	var before any
	if existed {
		before = newPlanRewardView(prev, true)
	}
	row := PlanReward{
		PlanId: planId, BuyerBps: req.BuyerBps, InviterBps: req.InviterBps,
		Sources: sources, OperatorId: c.GetInt("id"),
	}
	if err := savePlanReward(ctx, row); err != nil {
		writeConfigUpdateAudit(c, auditPlanRewardPut, qymodel.ResultFail, "写入失败: "+err.Error(), before, nil)
		respondErr(c, wrapInternal("写入套餐返还定义", err))
		return
	}
	after := newPlanRewardView(row, true)
	writeConfigUpdateAudit(c, auditPlanRewardPut, qymodel.ResultOK, "", before, after)
	respondOK(c, after)
}

// handleDeletePlanReward 删掉套餐的返还定义,此后该套餐回到 DefaultPlanReward。
func handleDeletePlanReward(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	planId, ok := planIdParam(c)
	if !ok {
		respondErr(c, errBadRequest("plan_id 不合法"))
		return
	}
	ctx := c.Request.Context()
	prev, existed := planRewardFor(ctx, planId)
	var before any
	if existed {
		before = newPlanRewardView(prev, true)
	}
	if err := deletePlanReward(ctx, planId); err != nil {
		writeConfigUpdateAudit(c, auditPlanRewardDel, qymodel.ResultFail, "删除失败: "+err.Error(), before, nil)
		respondErr(c, wrapInternal("删除套餐返还定义", err))
		return
	}
	writeConfigUpdateAudit(c, auditPlanRewardDel, qymodel.ResultOK, "plan "+strconv.Itoa(planId), before, nil)
	respondOK(c, newPlanRewardView(DefaultPlanReward(planId), false))
}
