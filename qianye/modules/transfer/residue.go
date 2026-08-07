package transfer

// residue.go —— 本模块对「用户分组名」这个键的处置声明。
//
// ══════════════ 为什么这一处比费率表严重 ══════════════
//
// qy_transfer_group_rules 是一道**资金闸门**:一条 deny_all 规则挂在某个分组上,
// 意思是"这一档人不许发起划转"。分组被删掉之后:
//
//	from_group 那一条留着 → 一条永远不会命中的规则。看起来无害,实际是运营
//	                        以为设了防而其实没有 —— 那批人已经迁到别处了。
//	to_groups  里留着     → **真正的漏洞**。一条 allow_list 规则里写着已被删掉的
//	                        分组名时它只是失效;但如果这个名字被将来某次新建
//	                        重新用上(名字是唯一连接键,重名完全可以被制造出来),
//	                        一批毫不相干的新用户会突然出现在一条老白名单里。
//
// 所以 to_groups 必须**逐条改写**,不能只清 from_group 那一行。这是本文件里
// 唯一一段需要读出来在 Go 侧改再写回去的逻辑 —— to_groups 是逗号串,
// 没有一种在三种方言上都成立的 SQL 拆分。

import (
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module: "transfer",
		Probe:  probeResidue,
		Sweep:  sweepResidue,
	})
}

func probeResidue(gdb *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	if gdb == nil {
		return nil, nil
	}
	key := groupname.Effective(userGroup)

	var fromRows int64
	if err := gdb.Model(&GroupRule{}).Where("from_group = ?", key).
		Count(&fromRows).Error; err != nil {
		return nil, err
	}

	// to_groups 只能拉出来在 Go 侧展开。规则条数有 maxGroupRuleCount 上界
	// (几十条),这是管理端冷路径,可以接受。
	//
	// **policy 必须一起取出来。** 摘掉一个名字在白名单上是收紧、在黑名单上是
	// 放宽,两者的处置因此完全相反(见 sweepResidue)。不取 policy 的表现是
	// 影响面把两种规则一律标成「删除时清掉」,而运营据此以为闸门还在。
	var all []GroupRule
	if err := gdb.Select("id", "from_group", "to_groups", "policy").Find(&all).Error; err != nil {
		return nil, err
	}
	hits := make([]string, 0, 4)
	denyHits := make([]string, 0, 4)
	for _, rule := range all {
		for _, name := range strings.Split(rule.ToGroups, ",") {
			if groupname.Effective(name) != key {
				continue
			}
			if rule.Policy == GroupPolicyDenyList {
				denyHits = append(denyHits, rule.FromGroup)
			} else {
				hits = append(hits, rule.FromGroup)
			}
			break
		}
	}
	sort.Strings(hits)
	sort.Strings(denyHits)

	var limitRows int64
	if err := gdb.Model(&GroupLimit{}).Where("user_group = ?", key).
		Count(&limitRows).Error; err != nil {
		return nil, err
	}

	out := []groupns.Residue{{
		Module: "transfer", Table: GroupRule{}.TableName(),
		Label: "以它为**转出方**的划转规则", Rows: fromRows,
		Disposition: groupns.ResidueClean,
		Detail: "删掉之后这一档回落兜底规则(没有兜底即不限制)。" +
			"源分组的限制不会被带到迁移目标上 —— 那会静默收紧另一档人的划转权限",
	}, {
		Module: "transfer", Table: GroupLimit{}.TableName(),
		Label: "这个分组的**划转门槛分档**", Rows: limitRows,
		Disposition: groupns.ResidueClean,
		Detail: "删掉之后这一档回落全站门槛。**分档不会被带到迁移目标上** —— " +
			"目标分组自己可能已经有一档,带过去要么覆盖别人的配置、要么在两档之间" +
			"二选一,而这两种结果都是在没人批准的情况下改掉了另一组人的日额度。" +
			"改名则原样跟着走:那是同一档人换了个名字",
	}}
	if len(hits) > 0 {
		out = append(out, groupns.Residue{
			Module: "transfer", Table: GroupRule{}.TableName(),
			Label: "以它为**转入方**出现在**白名单**(allow_list)里的规则", Rows: int64(len(hits)),
			Disposition: groupns.ResidueClean,
			Detail: "命中的转出方:" + strings.Join(hits, "、") +
				"。必须逐条摘掉这个名字 —— 留着的话,将来某次新建重新用上同一个名字时," +
				"一批毫不相干的新用户会突然出现在一条老白名单里",
		})
	}
	if len(denyHits) > 0 {
		out = append(out, groupns.Residue{
			Module: "transfer", Table: GroupRule{}.TableName(),
			Label: "以它为**转入方**出现在**黑名单**(deny_list)里的规则", Rows: int64(len(denyHits)),
			Disposition: groupns.ResidueKeep,
			Detail: "命中的转出方:" + strings.Join(denyHits, "、") +
				"。这个名字会**原样留在黑名单里** —— 黑名单上摘掉一个名字是**放宽**一道资金闸门," +
				"方向与白名单正好相反。留着一个已删分组的名字不会拦住任何人(没有人属于它)," +
				"但如果这个名字将来被重新用上,那条闸门仍然生效;摘掉的话它会凭空消失",
		})
	}
	return out, nil
}

func sweepResidue(tx *gorm.DB, from, to string, rename bool) error {
	fromKey := groupname.Effective(from)
	toKey := ""
	if to != "" {
		toKey = groupname.Effective(to)
	}

	if rename {
		if err := tx.Model(&GroupRule{}).Where("from_group = ?", fromKey).
			Update("from_group", toKey).Error; err != nil {
			return err
		}
	} else if err := tx.Where("from_group = ?", fromKey).Delete(&GroupRule{}).Error; err != nil {
		return err
	}

	// ── 门槛分档 ──
	//
	// 改名:整行跟着改名,那是同一档人换了个名字,不改会让这一档静默回落到
	// 全站门槛(通常更宽松)。
	// 删除:整行删掉,这一档的人迁过去之后按目标分组的口径走。**绝不改写成
	// 迁移目标**:目标分组自己可能已经有一档,主键冲突不说,任何一种合并结果
	// 都等于替运营改了另一组人的日额度。
	if rename {
		if err := tx.Model(&GroupLimit{}).Where("user_group = ?", fromKey).
			Update("user_group", toKey).Error; err != nil {
			return err
		}
	} else if err := tx.Where("user_group = ?", fromKey).Delete(&GroupLimit{}).Error; err != nil {
		return err
	}

	var all []GroupRule
	if err := tx.Find(&all).Error; err != nil {
		return err
	}
	for _, rule := range all {
		// ── 黑名单在删除路径上一个字节都不改 ──
		//
		// 摘掉一个名字在两种策略上的方向正好相反:
		//   allow_list  名单越短越严 ⇒ 摘掉 = 收紧 ⇒ 安全
		//   deny_list   名单越短越松 ⇒ 摘掉 = **放宽一道资金闸门**
		// 具体地:一条 {policy: deny_list, to_groups: [X]} 的含义是"不许转给 X"。
		// 把 X 摘掉之后名单为空,matchGroupList 恒 false,allowsGroup 直接放行 ——
		// 这条规则从"禁止转给 X"变成"随便转",而运营从界面上只会看到一行
		// 「已清理」。留着一个已删分组的名字不拦任何人(没有人属于它),
		// 却能在这个名字被重新用上时让闸门继续生效。改名仍然要跟着改:
		// 那是同一档人换了个名字,不改反而会让闸门指向一个空集。
		if !rename && rule.Policy == GroupPolicyDenyList {
			continue
		}
		names := strings.Split(rule.ToGroups, ",")
		next := make([]string, 0, len(names))
		changed := false
		for _, name := range names {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" {
				continue
			}
			if groupname.Effective(trimmed) != fromKey {
				next = append(next, trimmed)
				continue
			}
			changed = true
			// 改名:换成新名字。删除:整个摘掉 —— **不换成迁移目标**。
			// 换成目标的话,一条本来只允许转给 A 的规则会突然也允许转给 B,
			// 而 B 可能是一个完全不同信任级别的分组。收紧是安全的,放宽不是。
			if rename && toKey != "" {
				next = append(next, toKey)
			}
		}
		if !changed {
			continue
		}
		// 去重:改名时目标名字可能本来就在名单里,不去重会写出 "b,b"。
		deduped := make([]string, 0, len(next))
		seen := map[string]bool{}
		for _, name := range next {
			key := groupname.Effective(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			deduped = append(deduped, name)
		}
		if err := tx.Model(&GroupRule{}).Where("id = ?", rule.Id).
			Update("to_groups", strings.Join(deduped, ",")).Error; err != nil {
			return err
		}
		common.SysLog("qianye/transfer: 划转规则 " + rule.FromGroup +
			" 的目标名单已随用户分组变更改写为 " + strings.Join(deduped, ","))
	}
	return nil
}
