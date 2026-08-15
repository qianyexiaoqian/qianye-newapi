package groupmatrix

// modelgroup_residue_test.go —— 删掉一个模型分组时,不许把某一档人的**权威清单
// 清成空集**。
//
// 有 scope 行的语义是「这一档能选哪些模型分组完全由 grants 决定」(hook.go),
// 而零授权时快照给的是**空 map 而不是缺键**(snapshot.go)——两者叠起来的意思是:
// 一个已设范围用户分组的授权清单里只剩这一个模型分组时,删掉它等于让那一档人
// 一个模型分组都选不到:模型列表为空、新建令牌选不了分组、已有的显式分组令牌
// 全部 403。
//
// 这一侧此前完全没有闸门,而它绕得过所有现成的:
//
//	pin 闸门     只看 default_mode,enforce 的档次通常是 inherit
//	令牌闸门     只数**显式指向被删分组**的令牌;那一档人用空分组令牌就数不到
//	渠道闸门     与它无关 —— 渠道可以早就全停用了(AbilityRows=0)
//
// 用户分组删除那一侧有对称的 diff.loses_everything + 强制勾选,这里是它的
// block 形态:正确的新授权只有运营知道。

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// blockingRows 从探测结果里取出处置为 block 且真的有行的那些。
func blockingRows(t *testing.T, gdb *gorm.DB, modelGroup string) []groupns.Residue {
	t.Helper()
	rows, err := probeModelGroupResidues(gdb, modelGroup)
	require.NoError(t, err)
	return groupns.BlockingResidues(rows)
}

func TestModelGroupDeleteIsBlockedWhenAScopedGroupWouldLoseEverything(t *testing.T) {
	gdb := newTestDB(t)
	// 「隔离档」设过范围,清单里**只有**「私有池」。
	seedScope(t, gdb, "隔离档", ModeEnforce, false, "私有池")
	// 「普通档」也设过范围,但清单里还有别的,删掉「私有池」不会让它变空。
	seedScope(t, gdb, "普通档", ModeEnforce, false, "私有池", "公共池")

	blocking := blockingRows(t, gdb, "私有池")
	require.Len(t, blocking, 1, "必须恰好报出一处 block")
	assert.EqualValues(t, 1, blocking[0].Rows)
	assert.Contains(t, blocking[0].Detail, "隔离档")
	assert.NotContains(t, blocking[0].Detail, "普通档",
		"清单里还有别的模型分组的档次不受影响")

	// 删别的模型分组时这道闸门不许误伤。
	assert.Empty(t, blockingRows(t, gdb, "公共池"))
}

// TestModelGroupDeleteCountsLegacyShadowRows 钉住这道闸门与读侧**同一个谓词**:
// 有 scope 行就算数,mode 列一个字都不看。
//
// shadow 下线之后 hook.go 的 Resolve / CheckTokenGroup 都不再判 mode,而存量
// mode='shadow' 行是可达的:migrateShadowScopesToEnforce 只在启动时、且只在
// group_matrix.enabled 为真的主节点上跑一次,而那个开关是热载的 —— 升级那次启动
// 模块若是关的,迁移永远不会补跑,运维随后热开启即让那些行开始生效。
//
// 这条闸门此前写的是 `Where("mode = ?", ModeEnforce)`,于是这样一行在读侧完全生效、
// 却不进受害者名单:删除预览上一个字都不提示,而删完之后那一档人模型列表为空、
// 新建令牌选不了分组、已有的显式分组令牌全部 403。
func TestModelGroupDeleteCountsLegacyShadowRows(t *testing.T) {
	gdb := newTestDB(t)
	// 存量遗留行:mode 写着 shadow,读侧却已经把它当权威清单在用。
	seedScope(t, gdb, "遗留影子档", ModeShadow, false, "私有池")

	blocking := blockingRows(t, gdb, "私有池")
	require.Len(t, blocking, 1,
		"读侧已经按这份清单在限制人,闸门就必须把它算进受害者名单")
	assert.EqualValues(t, 1, blocking[0].Rows)
	assert.Contains(t, blocking[0].Detail, "遗留影子档")
}

// TestModelGroupDeleteIgnoresAlreadyEmptyScopedGroups:本来就是空清单的
// 隔离组/封禁组不是这次删除的受害者,报出来只会让运营去找一个不存在的因果。
func TestModelGroupDeleteIgnoresAlreadyEmptyScopedGroups(t *testing.T) {
	gdb := newTestDB(t)
	seedScope(t, gdb, "封禁档", ModeEnforce, false)
	seedScope(t, gdb, "普通档", ModeEnforce, false, "公共池")

	assert.Empty(t, blockingRows(t, gdb, "私有池"),
		"删一个谁都没授权过的模型分组,不该拦住任何人")
}
