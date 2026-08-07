package transfer

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// api_admin_limits.go —— 「按用户分组分档」的管理端接口。
//
// 与 api_admin_config.go(全站门槛)分开是刻意的:那一页写的是 qy_settings 的
// 一组 KV,这一页写的是 qy_transfer_group_limits 的一整行。两者的并发语义不同 ——
// 前者按键写、后者按行写,合成一个接口就必须发明一套「这次请求到底动了哪些键」
// 的协议,而那正是三态最容易被压扁成零值的地方。
//
// 四条硬性约束与那边逐条对齐:校验全部前置、写入包在一个事务里、失败也要写审计、
// 白名单之外的键让整个请求 400。
//
// 权限用 guard.FlagCore 而不是 FlagTransfer,与 handleAdminListGroupRules 同口径:
// 划转被临时关停时管理员仍然必须能配门槛 —— 恰恰是「先把门槛配好再打开功能」
// 这个顺序最需要它。

// groupLimitReq 是一档的新建/编辑入参。
//
// # 为什么覆盖项走一个 map 而不是逐个字段
//
// 三态在 JSON 上的表达只有一种可靠形式:**值为 null 表示不覆盖**。用具名的
// *int64 字段同样能表达,但那样一来「传了哪些键」这件事就散落在 7 个字段上,
// 而后端要对它们做的事情完全一致(查白名单 → 查区间 → 写指针)。一个 map 让
// 白名单校验只有一处,多传一个键就是 400,而不是被结构体解析静默丢掉。
type groupLimitReq struct {
	UserGroup string `json:"user_group"`
	// Enabled 是整档开关,**必须显式传**,因此是指针。
	//
	// 非指针时缺席会被 Go 零值化成 false:一次按最小请求体重放的保存
	// (运维脚本、工单机器人)就会把这一档静默停用,那组人当场回落到全站门槛 ——
	// 而全站门槛通常更宽松。请求方与界面上都不会有任何信号,只有审计快照里
	// 多一个 enabled:false。Overrides 已经用 map[string]*int64 把「缺席」与
	// 「显式 null」分开了,这一位不能是唯一还在做零值推断的字段。
	Enabled *bool  `json:"enabled"`
	Remark  string `json:"remark"`
	// Overrides 的键必须落在 tierableKeys 里,值是**整数或 null**。
	// null 与「键缺席」都表示不覆盖这一项 —— 本接口是整行替换语义
	// (PUT),缺席即清除,不存在「只改一项、其余保持不变」的半量更新。
	// 半量更新会让「清除一项覆盖」这个动作无法表达。
	Overrides map[string]*int64 `json:"overrides"`
}

// apply 把入参落到一行上并做全部校验。global 是当前生效的全站门槛。
func (r groupLimitReq) apply(dst *GroupLimit, global config.Transfer) error {
	dst.UserGroup = r.UserGroup
	if r.Enabled == nil {
		return errors.New("必须显式给出 enabled:缺席会把这一档静默停用,该分组当场回落到更宽松的全站门槛")
	}
	dst.Enabled = *r.Enabled
	dst.Remark = r.Remark
	// 先整行清空再按 map 写入:PUT 是整行替换,残留上一次的覆盖会让
	// 「把某一项改回跟随全局」这个动作在界面上做完之后毫无效果。
	for _, key := range tierableKeys {
		*dst.fieldPtr(key) = nil
	}
	for key, v := range r.Overrides {
		p := dst.fieldPtr(key)
		if p == nil {
			return errTierKeyUnknown(key)
		}
		if v == nil {
			continue
		}
		value := *v
		*p = &value
	}
	return validateGroupLimit(dst, global)
}

// groupLimitRow 是下发给管理端的一档。
//
// 三块信息缺一不可,少任何一块运营都得靠脑补:
//
//	Overrides  这一档到底覆盖了哪几项(null = 没覆盖)
//	Effective  合并之后**真正生效**的七个数,与资金路径读到的逐位相同
//	Sources    每一项的生效值是兜底来的还是这一档覆盖的
//
// 只给 Overrides 的话,运营看不到「我只改了单笔上限,那这一档的日额度现在是
// 多少」;只给 Effective 的话,他分不清一个数字是自己配的还是全局漂过来的 ——
// 而后者会在全局改动时跟着变。
type groupLimitRow struct {
	GroupLimit
	Effective map[string]int64  `json:"effective"`
	Sources   map[string]string `json:"sources"`
	// Valid 为 false 表示这一档与当前全局门槛的组合自相矛盾,该分组的划转
	// 已经被失败关闭(见 transferFor)。这一位必须下发:后端已经把这组人的
	// 划转停了,而界面上那一行看起来与别的行没有任何区别。
	Valid bool `json:"valid"`
}

// adminListGroupLimits 返回全部分档 + 全站兜底 + 取值区间 + 用户分组候选。
func adminListGroupLimits(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	ctx := c.Request.Context()
	// 用 resolveSettings 而不是 effectiveCtx:库里现存的全局组合非法时,
	// 这一页仍然必须打得开 —— 理由与 adminGetTransferConfig 逐字同源。
	s, valid, err := resolveSettings(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	rows, err := loadTiers(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 停用的行不进 s.Tiers(buildTierSet 丢掉了它们),但**必须列出来** ——
	// 运营要能看到自己停掉的那一档并把它打开。生效值因此按「这一行的覆盖
	// 叠到全局上」现算,而不是查 s.Tiers。
	items := make([]groupLimitRow, 0, len(rows))
	for i := range rows {
		items = append(items, buildGroupLimitRow(s, rows[i]))
	}

	userGroups, probeOK := listUserGroupCandidates()
	respondOK(c, gin.H{
		"items": items,
		// 全站兜底那一份:分档页要能回答「不覆盖的话会落到哪里」,
		// 否则勾掉一项覆盖等于闭眼跳。
		"global":         settingsSnapshot(s),
		"global_valid":   valid,
		"tierable_keys":  tierableKeys,
		"bounds":         tierBoundsView(),
		"max_tier_count": maxGroupLimitCount,
		// 键名与 /admin/transfer/group-rules 逐字对齐:同一个模块的两个端点
		// 曾经用同一个 `group_options` 承载**两个不同的命名空间**(这里是用户
		// 分组、那里是模型分组)。本轮修的正是这种错位,新端点不能把它复制回来 ——
		// 任何按键名共享的前端 helper 从两个端点各拉一次就会把模型分组喂进
		// 划转的下拉,而两处各自 import 了不同的类型别名,TypeScript 编译期看不出来。
		"user_group_options":   userGroups,
		"user_groups_probe_ok": probeOK,
		"unknown_groups":       unknownTierGroups(rows),
	})
}

// buildGroupLimitRow 现算一行的生效值。
//
// 刻意**不复用 s.Tiers**:那份 map 已经丢掉了停用的行,而列表必须把它们显示
// 出来。这里把这一行(不论启停)叠到全局上算一次,得到的就是「把它启用之后
// 会变成什么样」—— 运营在按下那个开关之前需要看到的正是这个。
func buildGroupLimitRow(s opSettings, row GroupLimit) groupLimitRow {
	merged := s.Transfer
	sources := make(map[string]string, len(tierableKeys))
	for _, key := range tierableKeys {
		if v, has := row.override(key); has {
			assignTransferSetting(&merged, key, v)
			sources[key] = tierSourceGroup
			continue
		}
		sources[key] = tierSourceGlobal
	}
	effective := make(map[string]int64, len(tierableKeys))
	for _, key := range tierableKeys {
		v, _ := transferSettingValue(merged, key)
		effective[key] = v
	}
	return groupLimitRow{
		GroupLimit: row,
		Effective:  effective,
		Sources:    sources,
		Valid:      config.ValidateTransfer(&merged) == nil,
	}
}

// tierBoundsView 下发可分档键的取值区间。
//
// 与全站门槛共用 settingBounds:两份区间迟早会漂移成「分档页允许、全站页拒绝」,
// 而运营完全无法理解为什么同一个数字在两页上一个能填一个不能。
func tierBoundsView() map[string]gin.H {
	out := make(map[string]gin.H, len(tierableKeys))
	for _, key := range tierableKeys {
		b := settingBounds[key]
		out[key] = gin.H{"min": b.Lo, "max": b.Hi}
	}
	return out
}

// unknownTierGroups 返回分档引用了、但站点没有登记过的用户分组名。
//
// **软告警,不是闸门** —— 与 unknownRuleGroups 同口径:历史分组(登记表里已经
// 没有、users 里还有人挂着)恰恰是最需要单独设门槛的那一批账号,拦下来会让
// 运营在最需要配置的时刻配不进去。登记表读不到时一律返回空:那时每一个名字
// 都会被算成「未登记」,那是一片假警报,而假警报比没有警报更糟。
func unknownTierGroups(rows []GroupLimit) []string {
	defined, ok := definedUserGroups()
	out := []string{}
	if !ok {
		return out
	}
	for i := range rows {
		name := normalizeGroupName(rows[i].UserGroup)
		if !defined[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// adminPutGroupLimit 新增或整行覆盖一档。
//
// 整行替换而不是补丁:见 groupLimitReq.Overrides 的说明 ——
// 补丁语义下「把某一项改回跟随全局」这个动作无法表达。
func adminPutGroupLimit(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req groupLimitReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	ctx := c.Request.Context()
	// 非法的全局组合同样必须放行到这里:管理员可能正是要靠调整分档把某一档
	// 救回来,而 effectiveCtx 会在那种状态下直接拒绝。
	s, _, err := resolveSettings(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	now := common.GetTimestamp()
	row := GroupLimit{CreatedAt: now, UpdatedAt: now, UpdatedBy: c.GetInt("id")}
	if err := req.apply(&row, s.Transfer); err != nil {
		respondTierInvalid(c, err)
		return
	}

	// 快照必须在写入之前取,而且要取**库里那一行**:审计的 before 回答的是
	// 「改之前这一档是什么」,拿请求体推算出来的值去填等于让审计复述申请人的
	// 说法,而不是记录事实。
	before, existed, err := readGroupLimit(gdb, row.UserGroup)
	if err != nil {
		respondErr(c, err)
		return
	}
	// CreatedAt 只在新建时有意义:整行替换会把它一起写回去,不保住原值就会让
	// 「这一档是什么时候建的」在每次保存后归零。
	if existed {
		row.CreatedAt = before.CreatedAt
	}

	// 条数上限与 upsert 必须在**同一个事务**里:分开做的话两个并发的新建都会
	// 读到 count=199 并双双写入,而这个上界的存在理由正是「一次脚本误操作」——
	// 脚本恰恰是最容易并发的那一侧。
	//
	// 整行 upsert。列名显式列出而不是 clause.AssignmentColumns 全表:
	// created_at 必须排除在外(理由同上),而全表赋值会把它一起盖掉。
	var overCap bool
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !existed {
			var count int64
			// FOR UPDATE:MySQL 会锁住被扫过的范围,两个并发的新建因此排队而不是
			// 各自读到同一个旧计数。扩展库固定是 MySQL(见 db.LockForUpdate)。
			if err := db.LockForUpdate(tx).Model(&GroupLimit{}).Count(&count).Error; err != nil {
				return err
			}
			if count >= maxGroupLimitCount {
				overCap = true
				return nil
			}
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_group"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"min_quota", "max_per_tx_quota", "daily_max_quota", "daily_max_count",
				"cooldown_seconds", "receiver_daily_max_in_count", "new_account_freeze_hours",
				"enabled", "remark", "updated_at", "updated_by",
			}),
		}).Create(&row).Error
	})
	if overCap {
		respondTierInvalid(c, errors.New("分档数量已达上限"))
		return
	}
	if err != nil {
		db.MarkFailure(err)
		// 审计必须落在写入失败这一支上:「有人在这一刻试图把某一档的日额度
		// 放大十倍、失败了」与成功的那次同等重要。
		writeGroupLimitAudit(c, "transfer.group_limit.update", qymodel.ResultFail,
			"保存划转门槛分档失败: "+err.Error(), before, &row)
		respondErr(c, err)
		return
	}

	invalidateSettings()
	writeGroupLimitAudit(c, "transfer.group_limit.update", qymodel.ResultOK,
		"保存划转门槛分档", before, &row)

	// 回读一份最新快照来算生效值:刚写完的这一行叠到全局上是多少,
	// 是运营点保存之后唯一想知道的事。
	after, _, err := resolveSettings(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 只回这一档合并后的样子。软告警不在这里回:它已经在列表接口上,
	// 而调用方保存成功后必然要重新拉一次列表 —— 多回一份没有消费方的数据,
	// 正是本扩展反复栽跟头的那个形状。
	respondOK(c, gin.H{"item": buildGroupLimitRow(after, row)})
}

// adminDeleteGroupLimit 删掉一档,该分组回落全站门槛。
//
// 分组名走 query 而不是路径参数:分组名允许包含 `/`(checkGroupToken 只禁止
// 空白与逗号分号),放进路径会被路由器切开,表现是「删不掉、也没有报错」。
func adminDeleteGroupLimit(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	name := normalizeGroupName(strings.TrimSpace(c.Query("user_group")))
	if name == "" || checkGroupToken(name) != nil {
		respondErr(c, errInvalidParam)
		return
	}
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	before, existed, err := readGroupLimit(gdb, name)
	if err != nil {
		respondErr(c, err)
		return
	}
	if !existed {
		respondErr(c, errGroupLimitNotFound)
		return
	}
	if err := gdb.WithContext(c.Request.Context()).
		Where("user_group = ?", name).Delete(&GroupLimit{}).Error; err != nil {
		db.MarkFailure(err)
		writeGroupLimitAudit(c, "transfer.group_limit.delete", qymodel.ResultFail,
			"删除划转门槛分档失败: "+err.Error(), before, nil)
		respondErr(c, err)
		return
	}
	invalidateSettings()
	// 删除的 after 刻意是 nil:这一档之后就不存在了,而 before 里存着它被删
	// 之前的完整内容 —— 那是事后唯一能回答「删掉的是什么」的东西。
	writeGroupLimitAudit(c, "transfer.group_limit.delete", qymodel.ResultOK,
		"删除划转门槛分档,该分组回落全站门槛", before, nil)
	respondOK(c, gin.H{"user_group": name})
}

// readGroupLimit 读一行。第二个返回值 false 表示这一档还不存在(不是错误)。
func readGroupLimit(gdb *gorm.DB, userGroup string) (*GroupLimit, bool, error) {
	var row GroupLimit
	err := gdb.Where("user_group = ?", userGroup).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, false, err
	}
	return &row, true, nil
}

// writeGroupLimitAudit 记录一次分档变更,成功与失败共用。
//
// 门槛分档直接决定「这一组人一天能转走多少」,与全站门槛同等重要 ——
// 事后必须能回答「是谁在什么时候把 vip 的日额度放大了十倍」。
func writeGroupLimitAudit(c *gin.Context, action, result, reason string, before, after *GroupLimit) {
	name := ""
	if after != nil {
		name = after.UserGroup
	} else if before != nil {
		name = before.UserGroup
	}
	audit.Write(c, audit.Entry{
		TraceNo:     "qy_transfer_group_limit:" + name,
		Category:    qymodel.AuditCategoryConfig,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      audit.Truncate(reason, 500),
		BeforeSnap:  groupLimitSnapshot(before),
		AfterSnap:   groupLimitSnapshot(after),
	})
}

// respondTierInvalid 把逐条校验消息原样透出,理由与 respondRuleInvalid 同源:
// 「这一档没有覆盖任何门槛」这类消息是管理员唯一的修正依据,
// 退化成「请求参数不合法」等于让人瞎猜。
func respondTierInvalid(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{
		"success": false,
		"code":    "qy_transfer_group_limit_invalid_input",
		"message": err.Error(),
	})
}

func errTierKeyUnknown(key string) error {
	return errors.New("不支持按用户分组分档的配置项: " + key)
}
