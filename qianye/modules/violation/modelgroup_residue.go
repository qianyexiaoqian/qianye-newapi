package violation

// modelgroup_residue.go —— 本模块对「删除一个模型分组」的处置声明。
//
// ══════════════════ 为什么违规规则必须登记在**模型分组**这条链上 ══════════════════
//
// `qy_violation_rules.group_scope` 是一串逗号分隔的分组名,而 compiledRule.inScope
// 比的是 `relayInfo.UsingGroup` —— 那是这次请求实际路由到的**模型分组**,
// 不是「这个人是谁」。所以它属于模型分组删除,而不是用户分组删除
// (后者已在 qianye/usergroup_residue_coverage_test.go 的 notAUserGroup 里显式豁免过)。
//
// 漏掉它的后果比"一条永远不会命中的配置"更糟,因为它是一道**安全闸门**:
//
//	include 模式  作用域里留着一个死名字,规则照常只对剩下那些分组生效 ——
//	              看起来没事,但运营在界面上看到的作用域与实际生效的不一致
//	exclude 模式  作用域里留着一个死名字,等于少排除了一个分组;而这个名字
//	              **将来被重新用上**时(模型分组名可以重建),一批毫不相干的新流量
//	              会突然落进一条老豁免里,而没有任何人改过这条规则
//
// 处置是 rewrite 语义的一种特例:值本身(逗号串)仍然有意义,只是要摘掉其中一段。
// 用 clean 会把整条规则删掉 —— 那是把一道防护整个关掉,方向完全错了。

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterModelResidue(groupns.ModelResidueHandler{
		Module: "violation",
		Probe:  probeModelGroupResidues,
		Sweep:  sweepModelGroupResidues,
	})
}

// probeModelGroupResidues 找出作用域里点名了这个模型分组的规则。
//
// 逗号串只能拉出来在 Go 侧展开:没有一种在 SQLite / MySQL / PostgreSQL 上都成立的
// SQL 拆分,而 `LIKE '%name%'` 会把「免费」匹配进「免费试用」——
// 一次误伤的后果是把另一条规则的作用域改掉,而那是一道正在生效的防护。
func probeModelGroupResidues(tx *gorm.DB, modelGroup string) ([]groupns.Residue, error) {
	if tx == nil {
		return nil, db.ErrNotReady
	}
	hits, err := rulesScopingModelGroup(tx, modelGroup)
	if err != nil {
		return nil, err
	}
	detail := ""
	if len(hits) > 0 {
		ids := make([]string, 0, len(hits))
		modes := map[string]int{}
		for _, rule := range hits {
			ids = append(ids, strconv.FormatInt(rule.Id, 10))
			modes[rule.GroupScopeMode]++
		}
		detail = "规则 id:" + strings.Join(ids, "、") +
			";只从作用域里摘掉这一段,规则本身保留 —— 删整条规则等于把一道防护关掉"
		if modes[GroupScopeExclude] > 0 {
			detail += ";其中 " + strconv.Itoa(modes[GroupScopeExclude]) +
				" 条是 exclude(排除)模式,留着死名字会在这个名字将来被重建时" +
				"让一批新流量凭空落进老豁免里"
		}
	}
	return []groupns.Residue{{
		Module: "violation", Table: Rule{}.TableName(),
		Label:       "违规规则的模型分组作用域",
		Rows:        int64(len(hits)),
		Disposition: groupns.ResidueRewrite,
		Detail:      detail,
	}}, nil
}

func sweepModelGroupResidues(tx *gorm.DB, modelGroup string) error {
	hits, err := rulesScopingModelGroup(tx, modelGroup)
	if err != nil {
		return err
	}
	for _, rule := range hits {
		kept := make([]string, 0, 4)
		for _, name := range strings.Split(rule.GroupScope, ",") {
			if strings.TrimSpace(name) != modelGroup {
				kept = append(kept, name)
			}
		}
		if err := tx.Model(&Rule{}).Where("id = ?", rule.Id).
			Update("group_scope", strings.Join(kept, ",")).Error; err != nil {
			return err
		}
	}
	if len(hits) == 0 {
		return nil
	}
	// 规则是编译进内存快照跑的,不重载的话这一批改动要等下一个刷新周期才生效。
	// **刻意不在这里重载**:这段跑在扩展库事务内部,而事务还没提交 ——
	// 此刻回源读到的仍是旧行,重载出来的快照与刚写的东西不一致,反而更糟。
	// 提交之后由既有的周期刷新收敛(最多晚一个 rule_cache_seconds)。
	return nil
}

// rulesScopingModelGroup 是 Probe 与 Sweep 共用的那一次扫描。
//
// 两侧共用同一段判定,否则会出现「预览说有 3 条、实际改了 2 条」——
// 而少改的那一条正是留在线上的那个死名字。
func rulesScopingModelGroup(tx *gorm.DB, modelGroup string) ([]Rule, error) {
	var rules []Rule
	if err := tx.Where("group_scope <> ''").Find(&rules).Error; err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		for _, name := range strings.Split(rule.GroupScope, ",") {
			if strings.TrimSpace(name) == modelGroup {
				out = append(out, rule)
				break
			}
		}
	}
	return out, nil
}
