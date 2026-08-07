package planentitlement

// modelgroup_residue.go —— 本模块对「删除一个模型分组」的处置声明。
//
// 注册在包 init() 里,理由见 groupns/modelgroup_residue.go 的文件头:
// qy_plan_group_grants 里的行不随本模块开关消失,而删除路径必须照样把它们
// 算进影响面并清掉。

import (
	"strconv"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterModelResidue(groupns.ModelResidueHandler{
		Module: "planentitlement",
		Probe:  probeModelGroupResidues,
		Sweep:  sweepModelGroupResidues,
	})
}

// probeModelGroupResidues 报告套餐解锁绑定里挂在这个模型分组上的行。
//
// 除了行数,它还单独算一档**最危险的组合**:余额范围是 restricted、而这个模型
// 分组是它**唯一**的解锁项。清掉之后那个套餐的额度池不再对应任何模型分组 ——
// 余额还在、任何一次请求都用不了它,而用户看到的是"我买的套餐突然不能用了",
// 后台看到的是"余额充足"。这一档必须在影响面里单独点名,不能混在总行数里。
func probeModelGroupResidues(tx *gorm.DB, modelGroup string) ([]groupns.Residue, error) {
	if tx == nil {
		return nil, db.ErrNotReady
	}
	var grants int64
	if err := tx.Model(&PlanGrant{}).Where("model_group = ?", modelGroup).
		Count(&grants).Error; err != nil {
		return nil, err
	}
	var planIds []int
	if err := tx.Model(&PlanGrant{}).Where("model_group = ?", modelGroup).
		Distinct().Pluck("plan_id", &planIds).Error; err != nil {
		return nil, err
	}

	// 只在真的有绑定时才去算那一档:没有绑定就没有"唯一解锁项"这回事,
	// 而这两条查询是管理端冷路径上白花的两次往返。
	stranded := make([]int, 0, len(planIds))
	if len(planIds) > 0 {
		var restricted []int
		if err := tx.Model(&PlanBalancePolicy{}).
			Where("plan_id IN ? AND balance_scope = ?", planIds, ScopeRestricted).
			Pluck("plan_id", &restricted).Error; err != nil {
			return nil, err
		}
		for _, planId := range restricted {
			var others int64
			if err := tx.Model(&PlanGrant{}).
				Where("plan_id = ? AND model_group <> ?", planId, modelGroup).
				Count(&others).Error; err != nil {
				return nil, err
			}
			if others == 0 {
				stranded = append(stranded, planId)
			}
		}
	}

	rows := []groupns.Residue{{
		Module: "planentitlement", Table: PlanGrant{}.TableName(),
		Label:       "套餐解锁的模型分组",
		Rows:        grants,
		Disposition: groupns.ResidueClean,
		Detail:      planGrantDetail(grants, len(planIds)),
	}}
	if len(stranded) > 0 {
		rows = append(rows, groupns.Residue{
			Module: "planentitlement", Table: PlanBalancePolicy{}.TableName(),
			Label: "余额范围=仅限,且这是唯一解锁项的套餐",
			Rows:  int64(len(stranded)),
			// block:正确的新值只有运营知道 —— 要么给这些套餐补一个别的解锁分组,
			// 要么把余额范围改回"通用"。替他选一个等于替他改了这批人的出资来源,
			// 而那笔钱已经收过了。
			Disposition: groupns.ResidueBlock,
			Detail: "套餐 id:" + joinInts(stranded) + "。删掉之后这些套餐的额度池" +
				"不再对应任何模型分组:余额还在、每一次请求都用不了它。" +
				"请先给它们补一个解锁分组,或把余额范围改回「通用」",
		})
	}
	return rows, nil
}

func planGrantDetail(grants int64, plans int) string {
	if grants == 0 {
		return ""
	}
	return "分布在 " + strconv.Itoa(plans) + " 个套餐上;清掉之后这些套餐不再解锁它," +
		"已购用户会当场少一个可选分组"
}

func joinInts(values []int) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += "、"
		}
		out += strconv.Itoa(v)
	}
	return out
}

func sweepModelGroupResidues(tx *gorm.DB, modelGroup string) error {
	// PlanBalancePolicy 一行都不动:它的主键是 plan_id,与模型分组无关。
	// 真正危险的那一档(restricted + 唯一解锁项)已经在 Probe 里以 block 拦住,
	// 走到这里说明运营已经处理过了。
	return tx.Where("model_group = ?", modelGroup).Delete(&PlanGrant{}).Error
}
