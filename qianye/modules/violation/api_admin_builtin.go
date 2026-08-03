package violation

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_builtin.go —— 内置规则包的目录与导入接口。
//
// 规则内容与升级判据在 builtin.go,这里只负责"读库对账 + 写库 + 审计"。

// builtinItem 是目录接口的一行:模板本身 + 它在**这个站点**上的当前状态。
//
// 两件事必须在同一行里返回。分开返回(先拿目录、再拿已导入列表、前端自己 join)
// 会让"导入状态"成为同一个事实的第二份拷贝,而它一旦漂移,界面就会出现
// "显示未导入、点了却报已存在"这种没人能自证的状态。
type builtinItem struct {
	builtinRule
	State string `json:"state"`
	// RuleId / ImportedVersion 在未导入时为 0。
	RuleId          int64 `json:"rule_id"`
	ImportedVersion int   `json:"imported_version"`
	// RuleEnabled / RuleMode 是那条规则**现在**的开关与模式。
	// 摆出来是因为运营最常问的一句话是"我导进去的那批现在到底在不在扣钱"。
	RuleEnabled bool   `json:"rule_enabled"`
	RuleMode    string `json:"rule_mode"`
}

// adminListBuiltinRules 返回内置目录与每条的导入状态。
func adminListBuiltinRules(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	existing, err := loadBuiltinRows(ctxDB(c))
	if err != nil {
		internalError(c, err)
		return
	}
	items := make([]builtinItem, 0, len(builtinCatalog))
	for _, b := range builtinCatalog {
		row := existing[b.Key]
		item := builtinItem{builtinRule: b, State: upgradeState(row, b)}
		if row != nil {
			item.RuleId = row.Id
			item.ImportedVersion = row.BuiltinVersion
			item.RuleEnabled = row.Enabled
			item.RuleMode = row.Mode
		}
		items = append(items, item)
	}
	respond(c, gin.H{
		"categories": builtinCategories,
		"items":      items,
		// 导入出来一律是影子。把它作为数据下发而不是让前端写死一句文案:
		// 哪天这条约束变了,界面会跟着变,而不是继续显示一句已经不成立的承诺。
		"import_mode": ModeShadow,
	})
}

// ctxDB 把请求预算绑到扩展库句柄上,并容忍句柄尚未就绪。
//
// 对 nil 句柄调 WithContext 会 panic,所以 nil 必须在这里就折成 nil 返回,
// 由下游的 db.ErrNotReady 分支统一报错 —— 而不是让每个调用点各写一次判空。
func ctxDB(c *gin.Context) *gorm.DB {
	gdb := db.Get()
	if gdb == nil || c.Request == nil {
		return gdb
	}
	return gdb.WithContext(c.Request.Context())
}

// loadBuiltinRows 读出库里已有的内置规则,按 builtin_key 索引。
//
// 走默认作用域(不含软删):被管理员删掉的规则应当可以重新导入,那是"我删错了、
// 想要回来"的唯一出口。代价是同一个 key 在库里可能留下多行(一行软删 + 一行在用),
// 而这不会造成任何运行期歧义 —— 快照只加载未删除且 enabled 的行。
//
// 同一个 key 出现多行未删除行(并发导入撞上了)时取 id 最小的那一行,并让
// 后续导入把其余行报成 already_imported 而不是继续新建。
func loadBuiltinRows(gdb *gorm.DB) (map[string]*Rule, error) {
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []Rule
	if err := gdb.Where("builtin_key <> ?", "").Order("id asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	out := make(map[string]*Rule, len(rows))
	for i := range rows {
		if _, ok := out[rows[i].BuiltinKey]; ok {
			continue
		}
		out[rows[i].BuiltinKey] = &rows[i]
	}
	return out, nil
}

// importOutcome 是一次导入的逐条结果。
type importOutcome struct {
	Key    string `json:"key"`
	Action string `json:"action"` // created | upgraded | skipped
	Reason string `json:"reason,omitempty"`
	RuleId int64  `json:"rule_id,omitempty"`
}

// 导入动作。
const (
	importCreated  = "created"
	importUpgraded = "upgraded"
	importSkipped  = "skipped"
)

// adminImportBuiltinRules 一键导入(或升级)内置规则包。
//
// 请求体:
//
//	{"keys": ["jailbreak.dan_persona", ...], "upgrade": false}
//
// keys 为空 = 全部。upgrade=false(默认)时只新建,已存在的一律跳过;
// upgrade=true 时额外把 pristine 的旧版规则替换成新版模式串。
//
// # 为什么导入是"一条条独立成败"而不是一个事务
//
// 十二条规则之间没有任何关系。一条写失败就整批回滚,只会让管理员失去已经成功的
// 十一条,而重试的结果完全一样。所以逐条写、逐条上报,响应里如实列出每条的结局。
func adminImportBuiltinRules(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req struct {
		Keys    []string `json:"keys"`
		Upgrade bool     `json:"upgrade"`
	}
	_ = c.ShouldBindJSON(&req)

	gdb := ctxDB(c)
	existing, err := loadBuiltinRows(gdb)
	if err != nil {
		internalError(c, err)
		return
	}

	wanted, err := resolveImportKeys(req.Keys)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	now := common.GetTimestamp()
	operatorId := c.GetInt("id")
	results := make([]importOutcome, 0, len(wanted))
	changed := 0
	for _, b := range wanted {
		out := importOne(gdb, b, existing[b.Key], req.Upgrade, now, operatorId)
		if out.Action != importSkipped {
			changed++
		}
		results = append(results, out)
	}

	// 只在真的写过库时才 bump 版本 + 重载:一次"全部跳过"的导入不该让所有节点
	// 白拉一遍全表规则。
	if changed > 0 {
		bumpRuleVersion()
		if e := reload(true); e != nil {
			common.SysError("qianye/violation: 内置规则导入后重载失败: " + e.Error())
		}
	}

	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      "rules.import_builtin",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: operatorId,
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      fmt.Sprintf("导入内置防护规则包(upgrade=%t),生效 %d 条", req.Upgrade, changed),
		AfterSnap:   common.MapToJsonStr(map[string]any{"results": results, "mode": ModeShadow}),
	})
	respond(c, gin.H{"results": results, "changed": changed, "mode": ModeShadow})
}

// resolveImportKeys 把请求里的 key 列表解析成模板;空列表表示全部。
//
// 未知 key 直接 400 而不是静默忽略:静默忽略会让前端拼错一个 key 之后
// 拿到 200 + 空结果,而排查方向会完全跑偏(去查权限、查库、查规则版本)。
func resolveImportKeys(keys []string) ([]builtinRule, error) {
	if len(keys) == 0 {
		return builtinCatalog, nil
	}
	out := make([]builtinRule, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		b, ok := builtinByKey(key)
		if !ok {
			return nil, fmt.Errorf("未知的内置规则 key: %q", key)
		}
		seen[key] = struct{}{}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("keys 里没有任何有效的内置规则 key")
	}
	return out, nil
}

// importOne 处理单条内置规则的导入 / 升级。
func importOne(gdb *gorm.DB, b builtinRule, row *Rule, upgrade bool, now int64, operatorId int) importOutcome {
	switch upgradeState(row, b) {
	case BuiltinNotImported:
		fresh := b.toRule(now, operatorId)
		// 走一遍 ValidateRule:内置模板与手写规则用同一道校验门。绕过它的话,
		// 一条编译不过的内置规则会被安静地写进库,再被 reloadCtx 安静地跳过,
		// 表现是"导入成功、状态显示已导入、线上永不命中"。
		if err := ValidateRule(fresh); err != nil {
			return importOutcome{Key: b.Key, Action: importSkipped,
				Reason: "内置规则模板校验失败(这是本仓库的 bug,请上报): " + err.Error()}
		}
		if err := gdb.Create(fresh).Error; err != nil {
			db.MarkFailure(err)
			return importOutcome{Key: b.Key, Action: importSkipped, Reason: "写入失败: " + err.Error()}
		}
		return importOutcome{Key: b.Key, Action: importCreated, RuleId: fresh.Id}

	case BuiltinModified:
		// 运营改过。**任何情况下都不覆盖**,连"版本更旧"也不是覆盖的理由。
		return importOutcome{Key: b.Key, Action: importSkipped, RuleId: row.Id,
			Reason: "该规则已被修改过,升级不会覆盖你的改动;如需新版模式串请手工比对"}

	case BuiltinUpgradable:
		if !upgrade {
			return importOutcome{Key: b.Key, Action: importSkipped, RuleId: row.Id,
				Reason: fmt.Sprintf("已导入(v%d),目录里是 v%d;勾选「同时升级」才会更新",
					row.BuiltinVersion, b.Version)}
		}
		applyUpgrade(row, b, now, operatorId)
		if err := ValidateRule(row); err != nil {
			return importOutcome{Key: b.Key, Action: importSkipped, RuleId: row.Id,
				Reason: "新版模板校验失败(这是本仓库的 bug,请上报): " + err.Error()}
		}
		// 只写这四列。Save(row) 会把 mode / enabled / action 一起写回去,
		// 而 row 是我们从库里读的、期间可能已被别人改过 —— 那就成了一次静默回滚。
		if err := gdb.Model(&Rule{}).Where("id = ?", row.Id).Updates(map[string]any{
			"pattern":             row.Pattern,
			"case_sensitive":      row.CaseSensitive,
			"builtin_version":     row.BuiltinVersion,
			"builtin_fingerprint": row.BuiltinFingerprint,
			"updated_at":          row.UpdatedAt,
			"updated_by":          row.UpdatedBy,
		}).Error; err != nil {
			db.MarkFailure(err)
			return importOutcome{Key: b.Key, Action: importSkipped, RuleId: row.Id,
				Reason: "升级写入失败: " + err.Error()}
		}
		return importOutcome{Key: b.Key, Action: importUpgraded, RuleId: row.Id}

	default: // BuiltinUpToDate
		return importOutcome{Key: b.Key, Action: importSkipped, RuleId: row.Id, Reason: "已是最新版本"}
	}
}
