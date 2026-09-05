package apiaddr

// residue.go —— 本模块对「用户分组名」这个键的处置声明。
//
// user_groups 与 transfer 的 to_groups 同形:逗号串的分组名单,判定时逐个与
// users.group 比对。不登记的后果也逐字相同 —— 分组改名后名单里还是旧名字,
// 绑定它的专线从此对所有人不可见,管理端却仍显示「仅 vip」;分组删掉后名字
// 留在名单里,将来某次新建重新用上同一个名字时,一条老专线(地址簿里恰恰会放
// 「仅内网可达」「内测线路」这类入口,见包注释)会突然对一批毫不相干的新用户
// 可见。
//
// ══════════════ 删除路径上「摘空名单」必须停用而不能留空 ══════════════
//
// 本表的空名单语义与 transfer 的 allow_list 正好**相反**:transfer 摘空之后
// matchGroupList 恒 false,是收紧;这里空串 = 对所有分组可见,摘空是**放宽**。
// 一条只对已删分组可见的专属线路,绝不能因为分组没了就静默变成公开线路 ——
// 所以名单被摘空的行同时置 enabled=false,fail-closed:管理端看到的是一条
// 带「已停用」徽标的行,改绑后重新启用即可;用户侧则是这条线路消失,
// 与它原本只属于一个已不存在的分组的事实一致。
import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module: "apiaddr",
		Probe:  probeResidue,
		Sweep:  sweepResidue,
	})
}

// probeResidue 全表扫地址行的 user_groups。
//
// 不做 SQL 侧的 LIKE 预筛(与 lottery 的理由相同:分组名是自由输入,% 与 _ 的
// ESCAPE 三种方言各不相同,预筛写错的表现是漏报)。整表至多 maxAddresses 行,
// 管理端删除弹窗的冷路径,整表读四列可以接受。
func probeResidue(gdb *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	if gdb == nil {
		return nil, nil
	}
	key := groupname.Effective(userGroup)

	// 已停用的行照样算残留:名字留在名单里,重新启用或同名新建分组时照样命中。
	var rows []Address
	if err := gdb.Select("id", "name", "user_groups").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	hits := make([]string, 0, 4)
	sole := make([]string, 0, 4)
	for _, row := range rows {
		listed, others := listsUserGroup(row.UserGroups, key)
		if !listed {
			continue
		}
		hits = append(hits, row.Name)
		if !others {
			sole = append(sole, row.Name)
		}
	}
	sort.Strings(hits)
	sort.Strings(sole)

	out := []groupns.Residue{{
		Module: "apiaddr", Table: Address{}.TableName(),
		Label: "把它写进**适用分组**名单的 API 地址", Rows: int64(len(hits)),
		Disposition: groupns.ResidueClean,
		Detail: "命中的地址:" + strings.Join(hits, "、") +
			"。删除时逐条摘掉这个名字 —— 留着的话,将来某次新建重新用上同一个名字时," +
			"一条老专线(可能是内网/内测入口)会突然对一批毫不相干的新用户可见。" +
			"改名则名单里的名字原样跟着改;新名字若不符合本名单的写入约束" +
			"(含空格/分号,或改完超长),按摘除处置,名单摘空时同时停用",
	}}
	if len(hits) == 0 {
		out[0].Detail = "没有任何地址把它写进适用分组名单"
	}
	if len(sole) > 0 {
		out = append(out, groupns.Residue{
			Module: "apiaddr", Table: Address{}.TableName(),
			Label: "适用分组**只有它一个**的 API 地址", Rows: int64(len(sole)),
			Disposition: groupns.ResidueClean,
			Detail: "命中的地址:" + strings.Join(sole, "、") +
				"。摘掉后名单为空,而空名单的含义是「对所有分组可见」—— 把一条专属线路" +
				"静默变成公开线路是放宽,方向不安全。因此这些地址会**同时被停用**," +
				"请在删除分组后改绑适用分组并重新启用",
		})
	}
	return out, nil
}

// listsUserGroup 判断名单是否点名了该分组,以及名单里是否还有别的名字。
func listsUserGroup(userGroups, key string) (listed, others bool) {
	for _, name := range strings.Split(userGroups, ",") {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if groupname.Effective(name) == key {
			listed = true
		} else {
			others = true
		}
	}
	return listed, others
}

// sweepResidue 在扩展库事务里逐行改写 user_groups。
//
// 改名:把旧名字换成新名字(目标本来就在名单里时去重)。
// 删除:把名字摘掉,**不换成迁移目标** —— 换成目标的话,一条本来只给 A 看的
// 专线会突然对 B 可见,而 B 可能是一个完全不同信任级别的分组(transfer 的
// to_groups 同款判断:收紧是安全的,放宽不是)。摘空的行同时停用,理由见文件头。
func sweepResidue(tx *gorm.DB, from, to string, rename bool) error {
	fromKey := groupname.Effective(from)
	toKey := ""
	if rename && to != "" {
		toKey = groupname.Effective(to)
	}
	if toKey != "" {
		// 新名字要先过本名单写入侧的同一道闸(复用 normalizeUserGroups,
		// 不抄第二份判据):groupns 允许内含空格/分号的分组名,本名单拒收 ——
		// 硬写进去,这一行从此每次整行编辑都被 errGroupInvalid 挡回,连只改
		// 备注都存不了。过不了闸就按删除处置(只摘旧名,不写新名):收紧安全,
		// 放宽不安全,摘空停用的兜底照常生效。
		if _, err := normalizeUserGroups(toKey); err != nil {
			common.SysError("qianye/apiaddr: 用户分组新名字 \"" + toKey +
				"\" 不符合适用分组名单的写入约束(" + err.Error() + "),按摘除处置")
			toKey = ""
		}
	}

	var rows []Address
	if err := tx.Select("id", "name", "user_groups").
		Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if strings.TrimSpace(row.UserGroups) == "" {
			continue
		}
		next := make([]string, 0, 4)
		seen := map[string]bool{}
		changed := false
		for _, name := range strings.Split(row.UserGroups, ",") {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" {
				continue
			}
			key := groupname.Effective(trimmed)
			if key == fromKey {
				changed = true
				if !rename || toKey == "" {
					continue
				}
				trimmed, key = toKey, toKey
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			next = append(next, trimmed)
		}
		if !changed {
			continue
		}
		if toKey != "" && utf8.RuneCountInString(strings.Join(next, ",")) > maxUserGroupsRunes {
			// 换成更长的新名字可能把一份本已过闸的名单顶破总长上限:超限写库
			// 在 MySQL 非严格模式下是静默截断,截出的半个名字若恰好是另一个
			// 分组名的前缀,就是一次可见性放宽。同样按摘除处置 —— 名单会超限,
			// 当且仅当新名字不在原名单里(在的话去重只会让名单变短),所以
			// 摘掉的恰好只是这次改名想写进来的那一个。
			kept := next[:0]
			for _, name := range next {
				if name == toKey {
					continue
				}
				kept = append(kept, name)
			}
			next = kept
			common.SysError("qianye/apiaddr: 地址「" + row.Name +
				"」的适用分组名单在分组改名后将超出总长上限,新名字 \"" + toKey +
				"\" 按摘除处置")
		}
		updates := map[string]any{"user_groups": strings.Join(next, ",")}
		if len(next) == 0 {
			updates["enabled"] = false
		}
		if err := tx.Model(&Address{}).Where("id = ?", row.Id).
			Updates(updates).Error; err != nil {
			return err
		}
		common.SysLog("qianye/apiaddr: 地址「" + row.Name +
			"」的适用分组已随用户分组变更改写为 \"" + strings.Join(next, ",") + "\"")
	}
	return nil
}
