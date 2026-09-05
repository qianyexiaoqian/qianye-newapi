package commission

// residue.go —— 本模块对「用户分组名」这个键的处置声明。
//
// 两处以分组名为键,处置**相反**,这正是本文件存在的理由:
//
//	qy_commission_group_rate.group_name  分组费率**配置**       → clean / rewrite
//	qy_commission_accrual.rate_group     计佣时冻结的分组**事实** → keep
//
// 后者一个字节都不能动。它是"这一笔佣金当时按哪个分组的费率算出来的"这个事实,
// 事后拿着流水行复算全靠它。改掉它等于篡改账目:复算会得出另一个数字,
// 而没有任何地方记录过这次改写。分组被删掉之后那个名字变成一个死字符串 ——
// 这正是它应有的样子,历史就是发生在一个已经不存在的分组上。

import (
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module:      "commission",
		Probe:       probeResidue,
		Sweep:       sweepResidue,
		AfterCommit: afterResidueCommit,
	})
}

// afterResidueCommit 在扩展库事务**提交之后**刷进程内缓存。
//
// 此前 invalidateGroupRates() 曾在 sweepResidue 里、也就是**事务内**调过,
// 而框架把这件事单列成 AfterCommit 正是为了不这么做。事务内失效的危害:失效发生
// 在 COMMIT **之前**,本进程任何一条并发计佣走 db.Get()(事务外的另一条连接)重新
// 填缓存时读到的是**未提交前的旧行**,然后按 settingsCacheSeconds 钉住最多 60 秒 ——
// 那 60 秒里每一笔计佣都按错档冻结,而费率逐笔冻结、事后不追溯。
func afterResidueCommit(from, to string, rename bool) {
	_, _, _ = from, to, rename
	invalidateGroupRates()
}

// probeResidue 用 groupname.Normalize 查表:group_name 这一列**一律以归一化后的
// 形式存储**(见 grouprate.go),拿原始大小写去查会漏掉整条规则,
// 而漏掉的表现是"删完之后费率表里还挂着一条永远不会命中的规则"。
func probeResidue(gdb *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	if gdb == nil {
		return nil, nil
	}
	var rates int64
	if err := gdb.Model(&GroupRate{}).
		Where("group_name = ?", groupname.Normalize(userGroup)).
		Count(&rates).Error; err != nil {
		return nil, err
	}
	var frozen int64
	if err := gdb.Model(&Accrual{}).Where("rate_group = ?", userGroup).
		Count(&frozen).Error; err != nil {
		return nil, err
	}
	return []groupns.Residue{
		{
			Module: "commission", Table: GroupRate{}.TableName(),
			Label: "该用户分组的星辉佣金费率规则", Rows: rates,
			Disposition: groupns.ResidueClean,
			Detail: "删掉之后这一档人回落全局默认费率。" +
				"迁移不会把源分组的费率带到目标分组上 —— 那会静默改掉另一档人的计佣比例",
		},
		{
			Module: "commission", Table: Accrual{}.TableName(),
			Label: "已计佣流水里冻结的分组名(rate_group)", Rows: frozen,
			Disposition: groupns.ResidueKeep,
			Detail: "**保留**。它是「这笔佣金当时按哪个分组算的」这个事实," +
				"改掉它等于篡改账目 —— 事后复算会得出另一个数字",
		},
	}, nil
}

func sweepResidue(tx *gorm.DB, from, to string, rename bool) error {
	key := groupname.Normalize(from)
	if rename {
		// 改名走 Update 而不是"删掉再建":后者会丢掉 operator_id 与 created_at,
		// 而"这条费率是谁在什么时候配的"是审计之外唯一的线索。
		return tx.Model(&GroupRate{}).Where("group_name = ?", key).
			Update("group_name", groupname.Normalize(to)).Error
	}
	// 删除整行删掉、**绝不改写成迁移目标** —— 目标分组可能已经有自己的一档,
	// 任何一种合并都等于在没人批准的情况下改掉另一组人的计佣比例。
	// 缓存失效**不在这里**做,见 afterResidueCommit。
	return tx.Where("group_name = ?", key).Delete(&GroupRate{}).Error
}
