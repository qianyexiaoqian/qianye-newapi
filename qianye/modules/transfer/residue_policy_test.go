package transfer

// residue_policy_test.go —— 「删掉一个用户分组时,它的名字要不要从 to_groups
// 里摘掉」在两种策略上的方向**正好相反**。
//
//	allow_list  名单越短越严 ⇒ 摘掉 = 收紧 ⇒ 必须摘
//	deny_list   名单越短越松 ⇒ 摘掉 = **放宽一道资金闸门** ⇒ 必须留
//
// 摘错方向不会有任何症状:接口返回 200,弹窗上写着「已清理」,而一条本意是
// 「不许转给 X」的规则变成了「随便转」——deny_list + 空名单在 allowsGroup 里
// 直接落到 return nil。这条用例是那个方向的唯一信号。

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newResidueDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&GroupRule{}, &GroupLimit{}))
	return gdb
}

func TestUserGroupResidueKeepsDenyListEntriesOnDelete(t *testing.T) {
	gdb := newResidueDB(t)
	// 白名单:default 只能转给「内部账号池」与 agent。
	require.NoError(t, gdb.Create(&GroupRule{
		FromGroup: "default", Policy: GroupPolicyAllowList,
		ToGroups: "内部账号池,agent", Enabled: true,
	}).Error)
	// 黑名单:vip 不许转给「内部账号池」。
	require.NoError(t, gdb.Create(&GroupRule{
		FromGroup: "vip", Policy: GroupPolicyDenyList,
		ToGroups: "内部账号池", Enabled: true,
	}).Error)

	require.NoError(t, sweepResidue(gdb, "内部账号池", "普通用户", false))

	var allow, deny GroupRule
	require.NoError(t, gdb.Where("from_group = ?", "default").Take(&allow).Error)
	require.NoError(t, gdb.Where("from_group = ?", "vip").Take(&deny).Error)

	assert.Equal(t, "agent", allow.ToGroups,
		"白名单上摘掉一个名字是收紧,而且必须摘 —— 名字被重新用上时那批新用户会凭空进白名单")
	assert.Equal(t, "内部账号池", deny.ToGroups,
		"黑名单上摘掉一个名字是**放宽**:名单变空之后 matchGroupList 恒 false,"+
			"这条『不许转给 X』的规则会变成『随便转』")

	// 迁移目标绝不能被写进任何一条名单:那会让一条本来只允许/只禁止转给 A 的
	// 规则突然也覆盖 B,而 B 是一个完全不同信任级别的分组。
	assert.NotContains(t, allow.ToGroups, "普通用户")
	assert.NotContains(t, deny.ToGroups, "普通用户")
}

// TestUserGroupResidueRewritesBothPoliciesOnRename:改名是同一档人换了个名字,
// 两种策略都必须跟着改。黑名单不改的表现是闸门指向一个空集 —— 与删除时摘掉
// 名字是同一个后果,只是入口不同。
func TestUserGroupResidueRewritesBothPoliciesOnRename(t *testing.T) {
	gdb := newResidueDB(t)
	require.NoError(t, gdb.Create(&GroupRule{
		FromGroup: "default", Policy: GroupPolicyAllowList,
		ToGroups: "内部账号池,agent", Enabled: true,
	}).Error)
	require.NoError(t, gdb.Create(&GroupRule{
		FromGroup: "vip", Policy: GroupPolicyDenyList,
		ToGroups: "内部账号池", Enabled: true,
	}).Error)

	require.NoError(t, sweepResidue(gdb, "内部账号池", "内部池", true))

	var allow, deny GroupRule
	require.NoError(t, gdb.Where("from_group = ?", "default").Take(&allow).Error)
	require.NoError(t, gdb.Where("from_group = ?", "vip").Take(&deny).Error)
	assert.Equal(t, "内部池,agent", allow.ToGroups)
	assert.Equal(t, "内部池", deny.ToGroups)
}

// TestUserGroupResidueProbeSplitsByPolicy:影响面必须把两种策略分成两行、
// 给出两种处置。合成一行的表现是运营看到「删除时清掉」,以为闸门被干净地
// 移除了,而黑名单那一侧真正发生的是「闸门原地保留」——方向说反了。
func TestUserGroupResidueProbeSplitsByPolicy(t *testing.T) {
	gdb := newResidueDB(t)
	require.NoError(t, gdb.Create(&GroupRule{
		FromGroup: "default", Policy: GroupPolicyAllowList,
		ToGroups: "内部账号池", Enabled: true,
	}).Error)
	require.NoError(t, gdb.Create(&GroupRule{
		FromGroup: "vip", Policy: GroupPolicyDenyList,
		ToGroups: "内部账号池", Enabled: true,
	}).Error)

	rows, err := probeResidue(gdb, "内部账号池")
	require.NoError(t, err)

	byDisposition := map[string]int64{}
	for _, row := range rows {
		if row.Rows > 0 {
			byDisposition[row.Disposition] += row.Rows
		}
	}
	assert.EqualValues(t, 1, byDisposition["clean"], "白名单命中 1 条,处置是清掉")
	assert.EqualValues(t, 1, byDisposition["keep"], "黑名单命中 1 条,处置是原样保留")
}
