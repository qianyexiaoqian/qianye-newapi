package transfer

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_group_rules.go —— 分组划转规则的管理端增删改查。
//
// 全部用 guard.FlagCore 而不是 FlagTransfer,与本模块已有的
// handleAdminListRecords 同口径:划转功能被临时关停时,管理员仍然必须能查看和
// 调整规则 —— 恰恰是"先把规则配好再打开功能"这个顺序最需要它。

// groupRuleReq 是规则的新建/编辑入参。
//
// ToGroups 走一个字符串而不是 []string:管理端表单本来就是一个多行/逗号分隔的
// 输入框,而 @self 这类令牌在数组里同样是字符串元素,拆成数组只是把同一份解析
// 逻辑在前后端各写一遍。
type groupRuleReq struct {
	FromGroup string `json:"from_group"`
	Policy    string `json:"policy"`
	ToGroups  string `json:"to_groups"`
	Enabled   bool   `json:"enabled"`
	Remark    string `json:"remark"`
}

func (r groupRuleReq) apply(dst *GroupRule) error {
	dst.FromGroup = r.FromGroup
	dst.Policy = r.Policy
	dst.ToGroups = r.ToGroups
	dst.Enabled = r.Enabled
	dst.Remark = r.Remark
	return validateGroupRule(dst)
}

// handleAdminListGroupRules 返回规则原文 + 已解析的"谁能转给谁"矩阵。
//
// 两者一起下发是刻意的:只给规则列表,管理员要在脑子里叠加兜底规则与 @self
// 才知道当前实际生效的是什么;只给矩阵,又没法编辑。
func handleAdminListGroupRules(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	rows, err := listGroupRuleRows()
	if err != nil {
		respondErr(c, err)
		return
	}
	// 取值域必须把规则表自己引用的分组算进去,否则刚配好的分组在矩阵里
	// 既不成行也不成列(见 knownGroups 的缺陷回归说明)。
	groups := knownGroups(rows)
	candidates, probeOK := listGroupCandidates()
	// 刻意不下发策略枚举、@self、通配符这三样:前端必须为每个策略准备一条
	// i18n 文案,拿到一个它不认识的策略也只能渲染出裸 key。下发一份没有消费方的
	// 数据,正是本扩展反复栽跟头的那个形状。前端用自己的常量,靠
	// types.ts 的注释与后端保持同步。
	respondOK(c, gin.H{
		"items":        rows,
		"known_groups": groups,
		// 带元数据的下拉候选。裸 datalist 只提示、不校验、不告警,运营把分组名
		// 打错一个字母时得不到任何信号。
		"group_options": candidates,
		// abilities 探测是否成功。false 时 has_channels 一律是"不确定"而非"没有"。
		"channels_probe_ok": probeOK,
		// 规则引用了、但站点没定义过的分组。**软告警** —— 历史分组仍然要能配规则,
		// 前端据此打黄标而不是拦下保存。
		"unknown_groups": unknownRuleGroups(rows),
		"matrix":         buildGroupMatrix(buildGroupRuleSet(rows), groups),
		"max_rule_count": maxGroupRuleCount,
	})
}

func handleAdminCreateGroupRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req groupRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	now := common.GetTimestamp()
	row := GroupRule{CreatedAt: now, UpdatedAt: now, UpdatedBy: c.GetInt("id")}
	if err := req.apply(&row); err != nil {
		respondRuleInvalid(c, err)
		return
	}

	gdb := db.Get()
	var count int64
	if err := gdb.Model(&GroupRule{}).Count(&count).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	if count >= maxGroupRuleCount {
		respondRuleInvalid(c, errors.New("规则数量已达上限 "+strconv.Itoa(maxGroupRuleCount)))
		return
	}
	if taken, err := groupRuleTaken(row.FromGroup, 0); err != nil {
		respondErr(c, err)
		return
	} else if taken {
		respondErr(c, errGroupRuleDuplicate)
		return
	}

	if err := gdb.Create(&row).Error; err != nil {
		// 预检与写入之间有窗口,唯一索引才是最终裁判。回读一次把它翻译成
		// 管理员看得懂的 409,而不是把驱动的错误原文(含表名与索引名)吐出去。
		if taken, e := groupRuleTaken(row.FromGroup, 0); e == nil && taken {
			respondErr(c, errGroupRuleDuplicate)
			return
		}
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	writeGroupRuleAudit(c, "transfer.group_rule.create", "", &row)
	// 保存成功之后才回执告警:分组名不认识**不构成拒绝**,只是提醒运营确认
	// 是不是打错了字。放在成功响应里而不是 400,是这条口径的落点。
	respondOK(c, gin.H{"id": row.Id, "unknown_groups": unknownRuleGroups([]GroupRule{row})})
}

func handleAdminUpdateGroupRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := groupRuleIdParam(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req groupRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}

	gdb := db.Get()
	var row GroupRule
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondErr(c, errGroupRuleNotFound)
			return
		}
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	// 快照必须在 apply 之前取:apply 是就地修改,取晚了 before 与 after 会一模一样,
	// 审计里就看不出这次到底改了什么。
	before := groupRuleSnapshot(&row)
	if err := req.apply(&row); err != nil {
		respondRuleInvalid(c, err)
		return
	}
	if taken, err := groupRuleTaken(row.FromGroup, row.Id); err != nil {
		respondErr(c, err)
		return
	} else if taken {
		respondErr(c, errGroupRuleDuplicate)
		return
	}

	row.UpdatedAt = common.GetTimestamp()
	row.UpdatedBy = c.GetInt("id")
	if err := gdb.Save(&row).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	writeGroupRuleAudit(c, "transfer.group_rule.update", before, &row)
	respondOK(c, gin.H{"id": row.Id, "unknown_groups": unknownRuleGroups([]GroupRule{row})})
}

// handleAdminDeleteGroupRule 硬删一条规则。
//
// 刻意不软删:from_group 上有唯一索引,软删的行仍然占着索引位,运营删掉
// "vip 的规则"之后再也建不出新的 vip 规则,只会得到一个无法解释的 409。
// 规则不是资金凭据,删除前的完整内容已由审计的 before 快照保留。
func handleAdminDeleteGroupRule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := groupRuleIdParam(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	gdb := db.Get()
	var row GroupRule
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondErr(c, errGroupRuleNotFound)
			return
		}
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	if err := gdb.Where("id = ?", id).Delete(&GroupRule{}).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	writeGroupRuleAudit(c, "transfer.group_rule.delete", groupRuleSnapshot(&row), &row)
	respondOK(c, gin.H{"id": id})
}

// ─────────────────────────── 辅助 ───────────────────────────

// listGroupRuleRows 成功时**一律返回非 nil 切片** —— 返回值会原样进
// handleAdminListGroupRules 的 items 字段,nil 切片序列化成 null 而不是 [],
// 前端对着 null 调 .map 直接白屏。判据与机器校验见 qianye/json_array_guard_test.go。
func listGroupRuleRows() ([]GroupRule, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	rows := make([]GroupRule, 0, 16)
	if err := gdb.Order("from_group asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}

// groupRuleTaken 判断该发起分组是否已经被别的规则占用。
func groupRuleTaken(fromGroup string, excludeId int64) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	q := gdb.Model(&GroupRule{}).Where("from_group = ?", fromGroup)
	if excludeId > 0 {
		q = q.Where("id <> ?", excludeId)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		db.MarkFailure(err)
		return false, err
	}
	return n > 0, nil
}

func groupRuleIdParam(c *gin.Context) (int64, bool) {
	return httpq.PathInt64(c, "id")
}

// respondRuleInvalid 把逐条校验消息原样透出。
//
// 与 respondErr 的通用 400 分开是刻意的:"白名单为空等同于禁止发起划转"这类
// 消息是管理员唯一的修正依据,退化成"请求参数不合法"等于让人瞎猜。代价是这段
// 文案是后端硬编码中文 —— 对只有管理员看得到的诊断信息,可诊断性优先于可翻译性
// (与 violation 规则保存同口径)。
func respondRuleInvalid(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{
		"success": false,
		"code":    "qy_transfer_group_rule_invalid",
		"message": err.Error(),
	})
}

// writeGroupRuleAudit 记录一次规则变更。
//
// 强制写审计的理由与费率变更相同:这条规则直接决定"谁的钱能流到谁那里",
// 事后必须能回答"是谁在什么时候把 vip 的白名单加上了 default"。
func writeGroupRuleAudit(c *gin.Context, action, before string, row *GroupRule) {
	audit.Write(c, audit.Entry{
		TraceNo:     "qy_transfer_group_rule:" + strconv.FormatInt(row.Id, 10),
		Category:    qymodel.AuditCategoryConfig,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      "调整划转分组限制",
		BeforeSnap:  before,
		AfterSnap:   groupRuleSnapshot(row),
	})
}

func groupRuleSnapshot(row *GroupRule) string {
	if row == nil {
		return ""
	}
	return common.MapToJsonStr(map[string]any{
		"id":         row.Id,
		"from_group": row.FromGroup,
		"policy":     row.Policy,
		"to_groups":  row.ToGroups,
		"enabled":    row.Enabled,
		"remark":     row.Remark,
	})
}
