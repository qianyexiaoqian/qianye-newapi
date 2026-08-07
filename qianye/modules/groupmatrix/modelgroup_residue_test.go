package groupmatrix

// modelgroup_residue_test.go —— 删掉一个模型分组时,不许把某一档人的**权威清单
// 清成空集**。
//
// enforce 的语义是「这一档能选哪些模型分组完全由 grants 决定」(hook.go),
// 而零授权时快照给的是**空 map 而不是缺键**(snapshot.go)——两者叠起来的意思是:
// 一个 enforce 用户分组的授权清单里只剩这一个模型分组时,删掉它等于让那一档人
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

func TestModelGroupDeleteIsBlockedWhenAnEnforceGroupWouldLoseEverything(t *testing.T) {
	gdb := newTestDB(t)
	// 「隔离档」是 enforce,清单里**只有**「私有池」。
	seedScope(t, gdb, "隔离档", ModeEnforce, false, "私有池")
	// 「普通档」也是 enforce,但清单里还有别的,删掉「私有池」不会让它变空。
	seedScope(t, gdb, "普通档", ModeEnforce, false, "私有池", "公共池")
	// 「影子档」是 shadow:清单不生效,清空它不改变任何人的可选范围。
	seedScope(t, gdb, "影子档", ModeShadow, false, "私有池")

	blocking := blockingRows(t, gdb, "私有池")
	require.Len(t, blocking, 1, "必须恰好报出一处 block")
	assert.EqualValues(t, 1, blocking[0].Rows)
	assert.Contains(t, blocking[0].Detail, "隔离档")
	assert.NotContains(t, blocking[0].Detail, "普通档",
		"清单里还有别的模型分组的档次不受影响")
	assert.NotContains(t, blocking[0].Detail, "影子档",
		"shadow 的清单不生效,清空它不会让任何人少一个可选分组")

	// 删别的模型分组时这道闸门不许误伤。
	assert.Empty(t, blockingRows(t, gdb, "公共池"))
}

// TestModelGroupDeleteIgnoresAlreadyEmptyEnforceGroups:本来就是空清单的
// 隔离组/封禁组不是这次删除的受害者,报出来只会让运营去找一个不存在的因果。
func TestModelGroupDeleteIgnoresAlreadyEmptyEnforceGroups(t *testing.T) {
	gdb := newTestDB(t)
	seedScope(t, gdb, "封禁档", ModeEnforce, false)
	seedScope(t, gdb, "普通档", ModeEnforce, false, "公共池")

	assert.Empty(t, blockingRows(t, gdb, "私有池"),
		"删一个谁都没授权过的模型分组,不该拦住任何人")
}
