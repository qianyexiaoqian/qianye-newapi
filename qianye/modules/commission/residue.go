package commission

// residue.go —— 本模块对「用户分组名」这个键的处置声明。
//
// 两处以分组名为键,处置**相反**,这正是本文件存在的理由:
//
//	qy_commission_group_rate.group_name  分组费率**配置**       → clean / rewrite
//	qy_commission_records.rate_group     计佣时冻结的分组**事实** → keep
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

// afterResidueCommit 在扩展库事务**提交之后**刷两张进程内缓存。
//
// 此前 invalidateGroupRates() 是在 sweepResidue 里、也就是**事务内**调的,
// 而框架把这件事单列成 AfterCommit 正是为了不这么做。事务内失效的具体危害
// 与注释里担心的方向相反、但同样实在:失效发生在 COMMIT **之前**,本进程
// 任何一条并发计佣走 db.Get()(事务外的另一条连接)重新填缓存时读到的是
// **未提交前的旧行**,然后按 settingsCacheSeconds 钉住最多 60 秒。
// 实测:改名 alpha→beta 之后窗口内重填一次,resolveRate("beta") 从 5000
// 掉到全局默认档 111 —— 那 60 秒里每一笔返佣都按错档冻结,而费率逐笔冻结、
// 事后不追溯。
//
// 法币档一起刷:它与费率档是同一个键(用户分组名)上的两张表,resolveInviterPricing
// 一次同时读两者,只刷一张会让两档在同一个窗口里分家。
func afterResidueCommit(from, to string, rename bool) {
	_, _, _ = from, to, rename
	invalidateGroupRates()
	invalidateFiatRates()
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
	// 法币结汇档同样以用户分组名为键,而且它决定的是**提现单的应付金额**
	// (逐笔冻结进 qy_commission_accrual.usd_rate,再由 settle 加权平均折进
	// available_fiat,QuoteWithdrawFiat 读的就是它)。此前它整张表都不在
	// 残留处置里:改名之后费率跟着走、结汇比例留在旧名字上,此后每一笔佣金
	// 都按**回落层**的比例冻结,三条恒等式全部成立、fiatRateDegrade 一次不响、
	// 影响面清单里也不会提到这张表;删除之后留下一条孤儿行,名字被将来某次
	// 新建重新用上时,新分组直接继承一个没人批准过的结汇价。
	var fiatRows int64
	if err := gdb.Model(&FiatRate{}).
		Where("group_name = ?", groupname.Normalize(userGroup)).
		Count(&fiatRows).Error; err != nil {
		return nil, err
	}
	var frozen int64
	if err := gdb.Model(&Accrual{}).Where("rate_group = ?", userGroup).
		Count(&frozen).Error; err != nil {
		return nil, err
	}
	return []groupns.Residue{
		{
			Module: "commission", Table: FiatRate{}.TableName(),
			Label: "该用户分组的**法币结汇档**", Rows: fiatRows,
			Disposition: groupns.ResidueClean,
			Detail: "删掉之后这一档人回落兜底档/全站充值汇率。**它决定提现单的应付金额**," +
				"而比例是逐笔冻结进计佣行的、改了不追溯 —— 留着一条指向已删分组的档," +
				"等于给将来重名的新分组预置一个没人批准过的结汇价",
		},
		{
			Module: "commission", Table: GroupRate{}.TableName(),
			Label: "该用户分组的返佣费率规则", Rows: rates,
			Disposition: groupns.ResidueClean,
			Detail: "删掉之后这一档人回落全局默认费率。" +
				"迁移不会把源分组的费率带到目标分组上 —— 那会静默改掉另一档人的返佣比例",
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
		if err := tx.Model(&GroupRate{}).Where("group_name = ?", key).
			Update("group_name", groupname.Normalize(to)).Error; err != nil {
			return err
		}
	} else if err := tx.Where("group_name = ?", key).Delete(&GroupRate{}).Error; err != nil {
		return err
	}
	// 法币结汇档与费率档同键、同处置。改名整行跟着走(同一档人换了个名字),
	// 删除整行删掉、**绝不改写成迁移目标** —— 目标分组可能已经有自己的一档,
	// 任何一种合并都等于在没人批准的情况下改掉另一组人的结汇价。
	if rename {
		if err := tx.Model(&FiatRate{}).Where("group_name = ?", key).
			Update("group_name", groupname.Normalize(to)).Error; err != nil {
			return err
		}
	} else if err := tx.Where("group_name = ?", key).Delete(&FiatRate{}).Error; err != nil {
		return err
	}
	// 缓存失效**不在这里**做,见 afterResidueCommit:事务内失效会让一条并发
	// 读用事务外的连接把未提交的旧行重新填回缓存,并按 60 秒 TTL 钉住。
	return nil
}
