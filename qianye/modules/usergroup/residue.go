package usergroup

// residue.go —— 本模块对「用户分组名」这个键的处置声明。
//
// 只有一处:qy_settings 里 scope=usergroup / k=default_group 的那一行,
// 也就是「新注册用户落进哪个分组」。
//
// ══════════════ 为什么是 rewrite 而不是 clean,更不是 block ══════════════
//
//	clean(清空)  新用户会回落到上游数据库默认值 default。那是一次**没有任何人
//	              决定过**的策略变更:运营本来把新用户放在"体验档",删掉体验档
//	              之后新用户悄悄变成 default 档 —— 而 default 档的倍率可能是 1.0,
//	              也可能开着完全不同的模型分组。
//	block(拦住)  技术上最保守,但它会把一个非常常见的操作(把体验档改名、
//	              或者把两个档合并)变成"必须先去另一个页面改配置再回来",
//	              而那个页面运营多半不知道在哪。
//	rewrite       新用户跟着老用户走。这是唯一一个"运营刚刚已经做出的决定"的
//	              自然延伸:他决定这一档人去 Y,新人自然也去 Y。
//
// 它必须出现在删除确认弹窗里(Residue.Rows = 1),因为这是**唯一一处**改动会
// 影响到"还不存在的用户"的地方 —— 那批人不在任何影响面统计里。

import (
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module:      "usergroup",
		Probe:       probeResidue,
		Sweep:       sweepResidue,
		AfterCommit: commitResidue,
	})
}

// probeResidue 直接读配置而不是查库:currentDefaultGroup 已经有缓存与硬超时,
// 再开一条裸查询只会多一条需要单独证明超时正确性的路径。
func probeResidue(_ *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	rows := int64(0)
	if currentDefaultGroup() == userGroup {
		rows = 1
	}
	return []groupns.Residue{{
		Module: "usergroup", Table: "qy_settings(usergroup/default_group)",
		Label: "新注册用户的默认分组", Rows: rows,
		Disposition: groupns.ResidueRewrite,
		Detail: "配置指向被删的分组时会**跟着改成迁移目标**;正因为如此," +
			"即使这一档此刻一个在册用户都没有,删除也必须选一个迁移目标。" +
			"清空它会让新用户悄悄落进上游默认的 default 档,而那是一次没有人决定过的策略变更",
	}}, nil
}

// sweepResidue 把配置改指到新分组。
//
// ── 必须走传进来的 tx ──
// 这一行与"删掉登记行、清掉范围/授权/费率/划转规则"是同一个扩展库事务。
// 用 db.Get() 另开一条连接的表现是:后续任何一步失败 ⇒ 事务回滚 ⇒ 那个分组的
// 全部配置原样还原,**唯独默认注册分组已经改成了迁移目标且回不去**,
// 而接口返回的 500 正文写的是"扩展库配置没有清掉"——与实际发生的事正好相反。
// 从那一刻起所有新注册用户进目标分组,界面上看不出任何异常。
//
// 迁移目标为空时**不再清空配置**,而是原样保留并让上层拒绝这次删除:
// 清空的含义是"回到上游 default 档",那是一次没有任何人决定过的策略变更
// (见 adminDeleteUserGroup 里 RewriteResidueRows 那道闸门)。走到这里而
// to 仍为空,只可能是绕过接口直接调用,保留原值是唯一不改变策略的选择。
func sweepResidue(tx *gorm.DB, from, to string, _ bool) error {
	if currentDefaultGroup() != from {
		return nil
	}
	if to == "" {
		return nil
	}
	// operatorId 传 0 表示"系统改写"。这一次改写由 groupns 的删除/改名审计
	// 记录负责留痕(Residue 会整份进那条审计的正文),这里不再写第二条 ——
	// 两条独立的审计会让复盘时看起来像发生过两次配置变更。
	return writeDefaultGroupTx(tx, to, 0)
}

// commitResidue 在扩展库事务提交之后才把新值写进进程内缓存。
//
// 缓存刷新刻意不放在 sweepResidue 里:事务回滚会撤销库里的那一行,却撤销不了
// 内存,于是进程会拿着一个数据库中并不存在的默认分组继续给新用户分组,
// 直到重启为止。
func commitResidue(from, to string, _ bool) {
	if to == "" || currentDefaultGroup() != from {
		return
	}
	storeDefaultGroup(to)
}
