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

// probeModelGroupResidues 报告 qy_group_grants 里挂在这个模型分组上的行。
//
// **0 行的残留仍然列出来**:「这里本来就没有」与「这里没查」在界面上必须是两件事。
//
// 这条规矩正是 qy_group_write_denies 不再出现在这里的理由:那张表随 shadow 档退役,
// 写入它的分支已经不存在,于是它会永远报一行"0 行、可清理" —— 一个恒真的 0 与
// "这里本来就没有"长得一模一样,而它实际在说的是"这个子系统还活着"。
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
	detail := ""
	if grants > 0 {
		detail = "分布在 " + strconv.FormatInt(userGroups, 10) + " 个用户分组上;" +
			"清掉之后这些档的用户不能再把令牌显式指向它(空分组令牌不受影响)"
	}

	// ── 会不会有哪一档人被清成空清单 ──
	//
	// 有 scope 行的语义是「这一档能选哪些模型分组**完全**由 grants 决定」
	// (hook.go 的 Resolve;snapshot.go 在零授权时给的是空 map 而不是缺键)。
	// 于是删掉某个模型分组时,如果某个已设范围用户分组的授权清单里**只有它**,
	// 清完之后 Resolve 返回空清单:那一档人的 /api/user/models 是空的、
	// 新建令牌一个分组都选不到、已有的显式分组令牌全部 403。
	//
	// 这一侧此前完全没有闸门:pin 只看 default_mode,而设过范围的档次通常是
	// inherit;令牌闸门只数**显式指向被删分组**的令牌,数不到那一档人指向
	// 别的分组或用空分组的令牌;渠道闸门更是与它无关(渠道可以早就停用光了)。
	// 用户分组删除那一侧有对称的 diff.loses_everything + 强制勾选,
	// 这里补上同一件事的 block 形态 —— 正确的新授权只有运营知道。
	orphaned, err := scopedUserGroupsLeftWithNothing(tx, modelGroup)
	if err != nil {
		return nil, err
	}
	orphanDetail := ""
	if len(orphaned) > 0 {
		orphanDetail = "受影响的用户分组:" + strings.Join(orphaned, "、") +
			"。它们**已设定范围**(有 scope 行即权威清单),而清单里**只有**这一个模型分组 —— " +
			"删掉之后这些档的用户一个模型分组都选不到:模型列表为空、" +
			"新建令牌选不了分组、已有的显式分组令牌全部 403。" +
			"请先在「用户分组」页给它们补一个别的模型分组,或取消这一档的范围设定"
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
			Label:       "**清掉之后会一个模型分组都不剩**的已设范围用户分组",
			Rows:        int64(len(orphaned)),
			Disposition: groupns.ResidueBlock,
			Detail:      orphanDetail,
		},
	}, nil
}

// scopedUserGroupsLeftWithNothing 列出「删掉 modelGroup 之后授权清单会变成空」
// 的**已设定范围**用户分组,按名字排序。
//
// ── 谓词是「有没有 scope 行」,不是 mode ──
//
// 读侧自 shadow 下线起一个字都不看 mode(hook.go 的 Resolve 与 CheckTokenGroup):
// 有 scope 行 = 清单立即生效,没有 = 逐位返回上游。这道闸门必须用同一个谓词。
// 早先这里写的是 `Where("mode = ?", ModeEnforce)`,于是一条存量的 mode='shadow' 行
// **在读侧已经完全生效**,却不会被算进受害者名单 —— 删除预览上一个字都不提示,
// 而删完之后那一档人模型列表为空、新建令牌选不了分组、已有的显式分组令牌全部 403,
// 正是这道闸门要防的那件事。
//
// 存量 shadow 行是可达的,不是理论风险:migrateShadowScopesToEnforce 只在启动时、
// 且只在 group_matrix.enabled 为真的主节点上跑一次(见 InstallHooks),而那个开关
// 是热载的 —— 升级那次启动时模块若是关的,迁移就永远不会补跑,运维随后热开启即让
// 库里的 shadow 行开始生效。所以正确的修法是消掉分叉本身,而不是加固迁移。
//
// 判据刻意是"删完之后还剩几行",而不是"现在是不是只有一行":后者在一个用户
// 分组同时授权了两次同一个模型分组(唯一索引挡住了,但历史数据可能有)时会
// 给出错误结论,而前者与 Resolve 真正读的东西同形。
func scopedUserGroupsLeftWithNothing(tx *gorm.DB, modelGroup string) ([]string, error) {
	var scopes []Scope
	if err := tx.Find(&scopes).Error; err != nil {
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
	return sweepAutoOrder(tx, modelGroup)
}

// sweepAutoOrder 把这个模型分组从所有用户分组的默认 auto 顺序里摘掉。
//
// ── 为什么必须真的改数据,而不是只靠运行期过滤 ──
//
// 运行期确实已经安全:GetUserAutoGroup 会用 IsUserSelectableGroup 逐个过滤,
// 一个已删除的模型分组不可能被选中。但**留在库里的那一项会显示在管理端**,
// 于是运营看到的顺序是「A → 已删的 B → C」,而真正执行的是「A → C」。
// 界面与行为不一致正是这个仓反复栽跟头的形状,而这里的修法极其便宜。
//
// 逐行读改写而不是一句 SQL 的字符串替换:AutoOrder 是逗号分隔的名字列表,
// 用 REPLACE(auto_order, 'x', ”) 会把 "xy" 里的 x 也换掉,而模型分组名之间
// 存在前缀关系是完全正常的(「浅梦号池测试」与「浅梦号池测试2」)。
// 范围行的数量级是"用户分组档数",几十行,逐行处理毫无压力。
func sweepAutoOrder(tx *gorm.DB, modelGroup string) error {
	var scopes []Scope
	if err := tx.Where("auto_order <> ''").Find(&scopes).Error; err != nil {
		return err
	}
	for i := range scopes {
		current := splitAutoOrder(scopes[i].AutoOrder)
		kept := make([]string, 0, len(current))
		for _, g := range current {
			if g != modelGroup {
				kept = append(kept, g)
			}
		}
		if len(kept) == len(current) {
			continue
		}
		next := joinAutoOrder(kept)
		if err := tx.Model(&Scope{}).Where("user_group = ?", scopes[i].UserGroup).
			Update("auto_order", next).Error; err != nil {
			return err
		}
	}
	return nil
}
