package lottery

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// residue_db_test.go —— 「删掉一个用户分组时,进行中的活动必须挡住它」。
//
// # 这条锁防的是什么
//
// 参与名单住在 rules_text 这个 JSON 里,任何按列名做的检查都看不见它。
// 两个方向的后果都不会报错:
//
//	allow_groups 里的名字被删 → 这批人在一场进行中的活动里当场失去资格
//	deny_groups  里的名字被删 → 一道被静默拆掉的闸门;而这个名字将来被重新
//	                            用上时,一批毫不相干的新用户会凭空获得资格
//
// 而 rules_text 又是**动不了的**(它进 commit_hash 并已对外公示),
// 所以唯一正确的处置是拦住删除,让运营先把活动收掉。

func seedRuledActivity(t *testing.T, gdb *gorm.DB, status string, rules Rules) {
	t.Helper()
	text, err := rules.Normalize().CanonicalText()
	require.NoError(t, err)
	seedActivity(t, gdb, func(a *Activity) {
		a.Status = status
		a.RulesText = text
	})
}

// TestUserGroupResidueBlocksLiveActivities 固化两档处置:进行中 block、已结束 keep。
func TestUserGroupResidueBlocksLiveActivities(t *testing.T) {
	gdb := newFundTestDB(t)

	seedRuledActivity(t, gdb, StatusPublished, Rules{AllowGroups: []string{"vip"}})
	seedRuledActivity(t, gdb, StatusLocked, Rules{DenyGroups: []string{"vip"}})
	seedRuledActivity(t, gdb, StatusFinished, Rules{AllowGroups: []string{"vip"}})
	seedRuledActivity(t, gdb, StatusPublished, Rules{AllowGroups: []string{"svip"}})

	rows, err := probeResidue(gdb, "vip")
	require.NoError(t, err)
	require.Len(t, rows, 2)

	assert.Equal(t, groupns.ResidueBlock, rows[0].Disposition)
	assert.Equal(t, int64(2), rows[0].Rows,
		"published(allow 名单)与 locked(deny 名单)两场都必须算进来 —— "+
			"deny 名单被静默拆掉比 allow 名单更贵")
	require.Len(t, groupns.BlockingResidues(rows), 1)

	assert.Equal(t, groupns.ResidueKeep, rows[1].Disposition)
	assert.Equal(t, int64(1), rows[1].Rows,
		"已结束的活动是冻结的事实,报出来但不动它")

	// 名单里没提到的分组必须一行都不命中,否则这条守卫会把每一次删除都拦下来,
	// 而"永远拦住"与"正确地拦住"在界面上长得一样。
	clean, err := probeResidue(gdb, "nobody")
	require.NoError(t, err)
	require.Len(t, clean, 2)
	assert.Zero(t, clean[0].Rows)
	assert.Zero(t, clean[1].Rows)
	assert.Empty(t, groupns.BlockingResidues(clean))
}

// TestUserGroupResidueSweepIsInert 固化「本模块一个字节都不改」。
//
// 它不是在测一个空函数:Sweep 在删除与改名两条路径上都会被调用,
// 而这里唯一正确的行为就是什么都不做 —— 有人将来"顺手"在这里加一句
// UPDATE rules_text,公示出去的 commit_hash 会当场全部验不过。
func TestUserGroupResidueSweepIsInert(t *testing.T) {
	gdb := newFundTestDB(t)
	seedRuledActivity(t, gdb, StatusFinished, Rules{AllowGroups: []string{"vip"}})

	var before Activity
	require.NoError(t, gdb.Take(&before).Error)

	require.NoError(t, sweepResidue(gdb, "vip", "svip", true))
	require.NoError(t, sweepResidue(gdb, "vip", "svip", false))

	var after Activity
	require.NoError(t, gdb.Where("id = ?", before.Id).Take(&after).Error)
	assert.Equal(t, before.RulesText, after.RulesText)
}
