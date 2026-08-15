package groupmatrix

// residue.go —— 本模块对「用户分组名」这个键的处置声明。
//
// 两张表都以 user_group 为主键或索引前缀,而它们表达的都是**配置**而不是账目:
//
//	qy_group_scopes  这一档人有没有设定过可用范围(有行即生效)
//	qy_group_grants  这一档人具体能选哪些模型分组(权威清单)
//
// 曾经还有第三张 qy_group_write_denies(影子期"本可拒绝"的计数)。它随 shadow 档
// 一起退役了:写入它的分支已经不存在,表也摘出了 Tables(),因此不再由本模块处置 ——
// 一张没有代码管的表不该在删除预览上占一行"要清 0 行",那读起来像它还活着。
// 退役登记见 qianye/docs/retired_tables.md。
//
// 所以删除时一律 clean。留着的后果很具体:一条挂在死名字上的 scope 行会让
// buildMatrixView 的行轴凭空多出一档已经不存在的用户分组(listUserGroups 把
// scopes 的键也并进 seen),运营看着一个没有任何用户、也删不掉的幽灵行。
//
// 改名时一律 rewrite:清单跟着那批人走,否则改完名字这一档人当场失去全部授权
// (scope 行还在旧名字上 ⇒ 新名字是"未设定范围" ⇒ 落回上游全局白名单)——
// 那是一次静默的、方向不定的权限变更。

import (
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module: "groupmatrix",
		Probe:  probeResidue,
		Sweep:  sweepResidue,
	})
}

func probeResidue(gdb *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	if gdb == nil {
		return nil, nil
	}
	var scopes, grants int64
	if err := gdb.Model(&Scope{}).Where("user_group = ?", userGroup).Count(&scopes).Error; err != nil {
		return nil, err
	}
	if err := gdb.Model(&Grant{}).Where("user_group = ?", userGroup).Count(&grants).Error; err != nil {
		return nil, err
	}
	return []groupns.Residue{
		{
			Module: "groupmatrix", Table: Scope{}.TableName(),
			Label: "可用范围设定(有行即生效)", Rows: scopes,
			Disposition: groupns.ResidueClean,
			Detail: "留着的话行轴会凭空多出一档已经不存在的用户分组 —— " +
				"listUserGroups 把 scopes 的键也并进候选",
		},
		{
			Module: "groupmatrix", Table: Grant{}.TableName(),
			Label: "可选模型分组的权威清单", Rows: grants,
			Disposition: groupns.ResidueClean,
			Detail:      "迁移之后这批人按**目标分组**的清单走,源分组的授权不合并过去",
		},
	}, nil
}

// sweepResidue 清掉(或改写)本模块的两张表。
//
// **删除时刻意不把 grants 合并进目标分组。** 合并看起来"更贴心",实际是在运营
// 不知情的情况下给另一档人开权限:目标分组本来是 enforce + 一份精心配好的清单,
// 合并之后它突然多出源分组的全部授权,而这件事在界面上没有任何痕迹。
// 迁过去的人按目标分组现有的清单走,差异由 MigrationDiff 在删除前显式展示。
func sweepResidue(tx *gorm.DB, from, to string, rename bool) error {
	if rename {
		for _, tbl := range []any{&Scope{}, &Grant{}} {
			if err := tx.Model(tbl).Where("user_group = ?", from).
				Update("user_group", to).Error; err != nil {
				return err
			}
		}
		return nil
	}
	for _, tbl := range []any{&Scope{}, &Grant{}} {
		if err := tx.Where("user_group = ?", from).Delete(tbl).Error; err != nil {
			return err
		}
	}
	return nil
}
