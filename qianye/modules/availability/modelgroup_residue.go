package availability

// modelgroup_residue.go —— 本模块对「删除一个模型分组」的处置声明。
//
// ══════════════════ 为什么处置是 keep 而不是 clean ══════════════════
//
// `qy_availability_bucket.group_name` 是**观测事实**:某个五分钟窗口里,走这个
// 模型分组的请求成功了多少次、失败在哪一档。它与佣金流水里的 rate_group、
// 违规记录里的 using_group 同一档 —— 是"当时发生了什么"的冻结记录,
// 不是"接下来按什么规则办"的配置。
//
// 删掉它等于篡改历史:运营下周查「上个月这个池子为什么可用率跌到 60%」时,
// 那段数据会连同分组名一起消失,而分组名被删掉恰恰常常正是因为它可用率太差。
// 桶表本来就带保留期清理,过期自然会走。
//
// 但它必须**被列出来**:0 行与"没查"在界面上必须是两件事,而"这里有 4 万行历史
// 观测数据、删除不会动它们"是运营在按下删除之前有权知道的事。

import (
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterModelResidue(groupns.ModelResidueHandler{
		Module: "availability",
		Probe:  probeModelGroupResidues,
		Sweep:  sweepModelGroupResidues,
	})
}

func probeModelGroupResidues(tx *gorm.DB, modelGroup string) ([]groupns.Residue, error) {
	if tx == nil {
		return nil, db.ErrNotReady
	}
	var rows int64
	if err := tx.Model(&Bucket{}).Where("group_name = ?", modelGroup).
		Count(&rows).Error; err != nil {
		return nil, err
	}
	detail := ""
	if rows > 0 {
		detail = "历史可用率观测数据,**删除不会动它们** —— 它们是「当时发生了什么」的" +
			"冻结记录,而一个分组被删掉往往正是因为它可用率太差,那段数据恰恰是要留着复盘的。" +
			"桶表自带保留期清理,过期会自然消失"
	}
	return []groupns.Residue{{
		Module: "availability", Table: Bucket{}.TableName(),
		Label:       "可用率观测桶",
		Rows:        rows,
		Disposition: groupns.ResidueKeep,
		Detail:      detail,
	}}, nil
}

// sweepModelGroupResidues 刻意什么都不做。
//
// 处置是 keep,而 keep 的实现就是空操作。留一个空函数而不是让本模块不登记:
// **不登记 = 这张表没有被想过**,而登记 keep = 有人显式判断过它该留下。
// 两者在数据库里的结果一样,在下一个人读代码时的结论完全相反。
func sweepModelGroupResidues(_ *gorm.DB, _ string) error { return nil }
