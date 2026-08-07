package groupmatrix

// modelgroup_residue.go —— 本模块对「删除一个模型分组」的处置声明。
//
// 注册在包 init() 里,不在 InstallHooks():qy_group_grants 里的行不随
// group_matrix.enabled 消失,而删除路径必须照样把它们算进影响面并清掉。
// 完整理由见 groupns/modelgroup_residue.go 的文件头。

import (
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterModelResidue(groupns.ModelResidueHandler{
		Module: "groupmatrix",
		Probe:  probeModelGroupResidues,
		Sweep:  sweepModelGroupResidues,
	})
}

// probeModelGroupResidues 报告 qy_group_grants / qy_group_write_denies 里
// 挂在这个模型分组上的行。
//
// **0 行的残留仍然列出来**:「这里本来就没有」与「这里没查」在界面上必须是两件事。
func probeModelGroupResidues(tx *gorm.DB, modelGroup string) ([]groupns.Residue, error) {
	if tx == nil {
		return nil, db.ErrNotReady
	}
	var grants int64
	if err := tx.Model(&Grant{}).Where("model_group = ?", modelGroup).
		Count(&grants).Error; err != nil {
		return nil, err
	}
	// 一并报出这些授权分布在几个用户分组上。行数回答"要清多少行",
	// 分组数回答"多少档人会当场少一个可选分组" —— 后者才是运营要判断的东西。
	var userGroups int64
	if err := tx.Model(&Grant{}).Where("model_group = ?", modelGroup).
		Distinct("user_group").Count(&userGroups).Error; err != nil {
		return nil, err
	}
	var denies int64
	if err := tx.Model(&WriteDeny{}).Where("model_group = ?", modelGroup).
		Count(&denies).Error; err != nil {
		return nil, err
	}

	detail := ""
	if grants > 0 {
		detail = "分布在 " + strconv.FormatInt(userGroups, 10) + " 个用户分组上;" +
			"清掉之后这些档的用户不能再把令牌显式指向它(空分组令牌不受影响)"
	}

	// ── 会不会有哪一档人被清成空清单 ──
	//
	// enforce 的语义是「这一档能选哪些模型分组**完全**由 grants 决定」
	// (hook.go:86-101,snapshot.go 在零授权时给的是空 map 而不是缺键)。
	// 于是删掉某个模型分组时,如果某个 enforce 用户分组的授权清单里**只有它**,
	// 清完之后 Resolve 返回空清单:那一档人的 /api/user/models 是空的、
	// 新建令牌一个分组都选不到、已有的显式分组令牌全部 403。
	//
	// 这一侧此前完全没有闸门:pin 只看 default_mode,而 enforce 的档次通常是
	// inherit;令牌闸门只数**显式指向被删分组**的令牌,数不到那一档人指向
	// 别的分组或用空分组的令牌;渠道闸门更是与它无关(渠道可以早就停用光了)。
	// 用户分组删除那一侧有对称的 diff.loses_everything + 强制勾选,
	// 这里补上同一件事的 block 形态 —— 正确的新授权只有运营知道。
	orphaned, err := enforceUserGroupsLeftWithNothing(tx, modelGroup)
	if err != nil {
		return nil, err
	}
	orphanDetail := ""
	if len(orphaned) > 0 {
		orphanDetail = "受影响的用户分组:" + strings.Join(orphaned, "、") +
			"。它们是 enforce(权威清单),而清单里**只有**这一个模型分组 —— " +
			"删掉之后这些档的用户一个模型分组都选不到:模型列表为空、" +
			"新建令牌选不了分组、已有的显式分组令牌全部 403。" +
			"请先在「用户分组」页给它们补一个别的模型分组,或把范围改回 shadow / 取消范围"
	}

	return []groupns.Residue{
		{
			Module: "groupmatrix", Table: Grant{}.TableName(),
			Label:       "用户分组 × 模型分组的权威授权",
			Rows:        grants,
			Disposition: groupns.ResidueClean,
			Detail:      detail,
		},
		{
			Module: "groupmatrix", Table: Scope{}.TableName(),
			Label:       "**清掉之后会一个模型分组都不剩**的 enforce 用户分组",
			Rows:        int64(len(orphaned)),
			Disposition: groupns.ResidueBlock,
			Detail:      orphanDetail,
		},
		{
			Module: "groupmatrix", Table: WriteDeny{}.TableName(),
			Label: "影子期令牌写入拒绝计数",
			Rows:  denies,
			// clean 而不是 keep:这张表是**观测计数器**,不是账目冻结值。
			// 分组名消失之后它再也不可能被处置,留着只会让影子面板上出现一个
			// 站里已经不存在的分组名,而那正是"这个分组是不是没删干净"的假信号。
			Disposition: groupns.ResidueClean,
		},
	}, nil
}

// enforceUserGroupsLeftWithNothing 列出「删掉 modelGroup 之后授权清单会变成空」
// 的 enforce 用户分组,按名字排序。
//
// 判据刻意是"删完之后还剩几行",而不是"现在是不是只有一行":后者在一个用户
// 分组同时授权了两次同一个模型分组(唯一索引挡住了,但历史数据可能有)时会
// 给出错误结论,而前者与 Resolve 真正读的东西同形。
func enforceUserGroupsLeftWithNothing(tx *gorm.DB, modelGroup string) ([]string, error) {
	var scopes []Scope
	if err := tx.Where("mode = ?", ModeEnforce).Find(&scopes).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		var remaining int64
		if err := tx.Model(&Grant{}).
			Where("user_group = ? AND model_group <> ?", sc.UserGroup, modelGroup).
			Count(&remaining).Error; err != nil {
			return nil, err
		}
		if remaining > 0 {
			continue
		}
		// 本来就是空清单(隔离组 / 封禁组)的档次不算受害者:删这个模型分组
		// 不会改变它们的任何事。只报"因为这次删除才变空"的那些。
		var current int64
		if err := tx.Model(&Grant{}).
			Where("user_group = ? AND model_group = ?", sc.UserGroup, modelGroup).
			Count(&current).Error; err != nil {
			return nil, err
		}
		if current > 0 {
			out = append(out, sc.UserGroup)
		}
	}
	sort.Strings(out)
	return out, nil
}

func sweepModelGroupResidues(tx *gorm.DB, modelGroup string) error {
	if err := tx.Where("model_group = ?", modelGroup).Delete(&Grant{}).Error; err != nil {
		return err
	}
	return tx.Where("model_group = ?", modelGroup).Delete(&WriteDeny{}).Error
}
