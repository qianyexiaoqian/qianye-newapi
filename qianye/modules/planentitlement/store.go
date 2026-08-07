package planentitlement

import (
	"context"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// store.go —— 两张表的读写。
//
// ══════════════ 这里为什么一个倍率字节都不存 ══════════════
//
// 分组倍率的唯一真相源是上游 options.GroupGroupRatio / options.GroupRatio。
// 任何镜像列都必须有同步机制,而同步失败的表现是「管理端显示 A、热路径乘 B」。
// 套餐只授予**成员资格**,倍率由 (用户分组, 模型分组) 决定 —— 于是本轮向计费
// 链路新增的倍率来源数是 **0**,三条复制的计费路径一行不改而天然全覆盖。

// planEntitlement 是一个套餐的完整配置,读写两侧共用一个形状。
type planEntitlement struct {
	PlanId int
	// Groups 已按 sort_order、name 排好序。
	Groups []string
	Scope  string
	Note   string
}

// readPlanEntitlement 读一个套餐的配置。没配过时返回零绑定 + universal,不是错误。
//
// 刻意不读进程内快照:管理端必须显示库里真正存着什么,否则运营刚在另一个节点
// 保存完再回来看,会以为没保存成功。这是低频请求,现读的代价可以忽略。
func readPlanEntitlement(ctx context.Context, planId int) (planEntitlement, error) {
	out := planEntitlement{PlanId: planId, Scope: ScopeUniversal, Groups: make([]string, 0, 4)}
	gdb := db.Get()
	if gdb == nil {
		return out, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	grants := make([]PlanGrant, 0, 8)
	if err := gdb.Where("plan_id = ?", planId).
		Order("sort_order asc, model_group asc").Find(&grants).Error; err != nil {
		db.MarkFailure(err)
		return out, err
	}
	for _, g := range grants {
		out.Groups = append(out.Groups, g.ModelGroup)
	}

	policies := make([]PlanBalancePolicy, 0, 1)
	if err := gdb.Where("plan_id = ?", planId).Limit(1).Find(&policies).Error; err != nil {
		db.MarkFailure(err)
		return out, err
	}
	if len(policies) == 1 && policies[0].BalanceScope == ScopeRestricted {
		out.Scope = ScopeRestricted
	}
	if len(policies) == 1 {
		out.Note = policies[0].Note
	}
	return out, nil
}

// writePlanEntitlement 整体替换一个套餐的解锁绑定与余额使用范围。
//
// ═══════════ 为什么是"整体替换"而不是增量 ═══════════
//
// 管理端那张多选框天然就是"最终状态"。做成增量(add/remove)之后,前端要自己
// 算差集,而两端各算一次差集就一定会有一次算错 —— 表现是"取消勾选之后还能用"。
//
// 单事务:绑定与范围必须一起生效。分两次写的话,中间那一刻可能是
// 「restricted 但零绑定」—— 那是一份任何请求都用不上的额度池(纯粹的死钱)。
//
// 调用方负责写审计与刷新快照(见 api_admin.go):这里只管落库。
func writePlanEntitlement(ctx context.Context, in planEntitlement, operatorId int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	now := common.GetTimestamp()
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plan_id = ?", in.PlanId).Delete(&PlanGrant{}).Error; err != nil {
			return err
		}
		if len(in.Groups) > 0 {
			rows := make([]PlanGrant, 0, len(in.Groups))
			for i, g := range in.Groups {
				rows = append(rows, PlanGrant{
					PlanId:     in.PlanId,
					ModelGroup: g,
					SortOrder:  i,
					OperatorId: operatorId,
					CreatedAt:  now,
					UpdatedAt:  now,
				})
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		policy := newPolicy(in.PlanId, in.Scope, in.Note, operatorId, now)
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "plan_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"balance_scope", "note", "operator_id", "updated_at"}),
		}).Create(policy).Error
	})
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	return nil
}

// DeleteForPlans 清掉一批套餐的解锁配置,供"删除套餐"的收尾调用。
//
// 残留行本身不会造成错误的放行(没有任何用户订阅还指着一个已删的套餐),
// 但它会让 Snapshot.AnyBindings() 恒为真,于是"全站零绑定"那条零 I/O 短路
// 永远短路不掉 —— 一个已经被删干净的功能仍在每个请求上产生一次 per-user 查询。
func DeleteForPlans(ctx context.Context, planIds []int) (int64, error) {
	if len(planIds) == 0 {
		return 0, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)
	res := gdb.Where("plan_id IN ?", planIds).Delete(&PlanGrant{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return 0, res.Error
	}
	removed := res.RowsAffected
	res = gdb.Where("plan_id IN ?", planIds).Delete(&PlanBalancePolicy{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return removed, res.Error
	}
	return removed + res.RowsAffected, nil
}

// sortedUnique 把请求里的分组列表去重排序。
//
// 去重不是宽容:唯一索引本来就会拒绝重复行,但那时报的是数据库错误,
// 运营看到的是"保存失败,请稍后重试" —— 指错方向。
func sortedUnique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
