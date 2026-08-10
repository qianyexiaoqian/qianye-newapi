package violation

// usergroup_residue.go —— 本模块对「删除 / 改名一个用户分组」的处置声明。
//
// ══════════════ 本模块有两处以分组名为键,但只有一处属于**用户分组** ══════════════
//
// 另一处是 qy_violation_rule.group_scope,它比的是 relayInfo.UsingGroup ——
// 那是这次请求路由到的**模型分组**,已经登记在 modelgroup_residue.go 上。
// 这里管的是 qy_violation_ban_policy.user_group:它比的是 users.group,
// 也就是"这个人是谁"。两条链路不能混,混了就会在删模型分组时去动封号阈值。
//
// ══════════════ 为什么处置是 clean 而不是 keep ══════════════
//
// 一档策略是**配置**,不是账目。分组被删之后:
//
//	留着(keep)   → 一条永远不会命中的策略档。它不报错,界面上和"配置正确"
//	               长得一模一样 —— 而真正危险的是**这个名字将来被重新用上**
//	               (分组名可以重建):一批毫不相干的新用户会突然落进一档
//	               老阈值里,而没有任何人改过这档策略。
//	清掉(clean)  → 该分组回落兜底档。兜底档永远存在(model.go 的三道锁),
//	               所以清掉不会造成"落进一个不存在的策略"。
//
// **绝不把源分组的策略合并进迁移目标**:目标分组可能已经有自己的一档,
// 合并等于在运营不知情的情况下改掉另一档人的封号线。
//
// 改名(rename)则原样跟着走 —— 那是同一档人换了个名字,阈值理应跟着。
//
// ══════════════ 封禁行与计数器**不在这里** ══════════════
//
// qy_violation_ban.policy_group 是"当时按哪一档判的"这个**历史事实**,
// 与佣金流水里的 rate_group 同性质:改掉它等于篡改记录,事后拿着封禁行复盘
// 会得出另一个结论。它不是以分组为键的配置,而是冻结值,因此不登记处置。
// qy_violation_counter 以 user_id 为键,与分组名无关。

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module:      "violation",
		Probe:       probeUserGroupResidues,
		Sweep:       sweepUserGroupResidues,
		AfterCommit: refreshBanPoliciesAfterResidue,
	})
}

func probeUserGroupResidues(gdb *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	if gdb == nil {
		return nil, nil
	}
	key := groupname.Effective(userGroup)
	var rows int64
	// 兜底档恒为空 user_group,任何真实分组名都不会撞上它 —— 但仍然显式排除,
	// 因为一次误删兜底档等于让全站没配分组的用户落进一个不存在的策略。
	if err := gdb.Model(&BanPolicy{}).
		Where("user_group = ? AND is_default = ?", key, false).
		Count(&rows).Error; err != nil {
		return nil, err
	}
	return []groupns.Residue{{
		Module: "violation", Table: BanPolicy{}.TableName(),
		Label:       "这个分组的**违规处置策略档**(窗口 / 次数阈值 / 达到后处置)",
		Rows:        rows,
		Disposition: groupns.ResidueClean,
		Detail: "删掉之后该分组回落默认兜底策略(兜底档永远存在,不会落空)。" +
			"**策略不会被带到迁移目标上** —— 目标分组自己可能已经有一档," +
			"带过去要么覆盖别人的配置、要么在两档之间二选一,而这两种结果都是在" +
			"没人批准的情况下改掉了另一组人的封号线。改名则原样跟着走:" +
			"那是同一档人换了个名字。留着不清的风险是这个名字将来被重建时," +
			"一批毫不相干的新用户会突然落进一档老阈值里",
	}}, nil
}

func sweepUserGroupResidues(tx *gorm.DB, from, to string, rename bool) error {
	fromKey := groupname.Effective(from)
	if rename {
		return tx.Model(&BanPolicy{}).
			Where("user_group = ? AND is_default = ?", fromKey, false).
			Update("user_group", groupname.Effective(to)).Error
	}
	// is_default = false 这个条件是"兜底档不可删"的第四道锁。
	//
	// 按当前口径它是冗余的:兜底档的 user_group 恒为空串,而 fromKey 走的是
	// groupname.Effective,永远不会返回空串。写出来是因为这一层的代价是零,
	// 而它挡的那件事的代价是"全站没配专属策略的分组同时落进一个不存在的策略"。
	// 下一个人只要把 Effective 换成 Normalize(那是写入侧的正确口径,见
	// validateBanPolicy),空 from 就会一路走到这里 —— 那时这个条件是唯一的拦阻。
	return tx.Where("user_group = ? AND is_default = ?", fromKey, false).
		Delete(&BanPolicy{}).Error
}

// refreshBanPoliciesAfterResidue 在扩展库事务**提交之后**让策略快照回源。
//
// 不在 Sweep 里顺手刷:事务回滚时 Sweep 写的库行会被撤销,而快照不会 ——
// 那会留下一个"库里还有这一档、进程里已经没了"的分叉,表现是该分组的人
// 按兜底档被处置,而管理端列表上那一档明明还在。
func refreshBanPoliciesAfterResidue(from, to string, rename bool) {
	invalidateBanPolicies()
	common.SysLog(common.MapToJsonStr(map[string]any{
		"msg":    "qianye/violation: 用户分组变更,违规处置策略快照已失效待回源",
		"from":   from,
		"to":     to,
		"rename": rename,
	}))
}
