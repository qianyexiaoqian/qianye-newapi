package apiaddr

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// residue_db_test.go —— 分组改名/删除时 user_groups 名单的处置契约。
//
// 这组不变量是 groupns 残留注册表的下游承诺:改名弹窗的文案说「配置跟着名字走」,
// 说到就要做到 —— 漏改的表现是绑定了旧名字的专线对所有人不可见,而管理端
// 徽标看着一切正常。删除路径的「摘空即停用」是本表独有的方向修正:
// 空名单 = 对所有分组可见,摘空不停用等于把专属线路静默公开。

func mustRow(t *testing.T, gdb *gorm.DB, id int) Address {
	t.Helper()
	var row Address
	require.NoError(t, gdb.Where("id = ?", id).Take(&row).Error)
	return row
}

// 改名:名单里的旧名字逐行换成新名字,目标已在名单里时去重,未命中的行不动。
func TestResidueSweepRenameRewritesUserGroups(t *testing.T) {
	gdb := newTestDB(t)
	shared := seedGroupedAddress(t, gdb, "VIP 专线", "https://vip.example.com", 10, "vip,svip")
	sole := seedGroupedAddress(t, gdb, "内测线", "https://beta.example.com", 20, "vip")
	dup := seedGroupedAddress(t, gdb, "合流线", "https://both.example.com", 30, "vip,pro")
	unbound := seedGroupedAddress(t, gdb, "主线路", "https://main.example.com", 40, "")

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "vip", "pro", true)
	}))

	assert.Equal(t, "pro,svip", mustRow(t, gdb, shared.Id).UserGroups)
	after := mustRow(t, gdb, sole.Id)
	assert.Equal(t, "pro", after.UserGroups)
	assert.True(t, after.Enabled, "改名不该停用任何行 —— 那是同一档人换了个名字")
	assert.Equal(t, "pro", mustRow(t, gdb, dup.Id).UserGroups,
		"目标名字本来就在名单里时必须去重,否则写出 pro,pro")
	assert.Equal(t, "", mustRow(t, gdb, unbound.Id).UserGroups)
}

// 改名:新名字过不了本名单写入侧的闸(groupns 允许内含空格的分组名,本名单
// 拒收)时按摘除处置 —— 硬写进去的话,这一行从此每次整行编辑都被
// errGroupInvalid 挡回,连只改备注都存不了。摘空的行照常停用。
func TestResidueSweepRenameToInvalidNameRemovesInstead(t *testing.T) {
	gdb := newTestDB(t)
	shared := seedGroupedAddress(t, gdb, "VIP 专线", "https://vip.example.com", 10, "vip,svip")
	sole := seedGroupedAddress(t, gdb, "内测线", "https://beta.example.com", 20, "vip")
	unbound := seedGroupedAddress(t, gdb, "主线路", "https://main.example.com", 30, "")

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "vip", "vip 2", true)
	}))

	afterShared := mustRow(t, gdb, shared.Id)
	assert.Equal(t, "svip", afterShared.UserGroups,
		"带空格的新名字绝不能写进名单 —— 写了之后这一行每次整行编辑都会 400")
	assert.True(t, afterShared.Enabled)

	afterSole := mustRow(t, gdb, sole.Id)
	assert.Equal(t, "", afterSole.UserGroups)
	assert.False(t, afterSole.Enabled, "摘空的行必须停用,与删除路径同一条兜底")

	assert.Equal(t, "", mustRow(t, gdb, unbound.Id).UserGroups)
}

// 改名:换成更长的新名字把名单顶破总长上限时,新名字按摘除处置 —— 超限写库
// 在 MySQL 非严格模式下是静默截断,截出的半个名字若恰好是另一个分组名的前缀,
// 就是一次可见性放宽。名单没顶破的行照常改写,证明兜底只在超限时触发。
func TestResidueSweepRenameOverflowingListRemovesInstead(t *testing.T) {
	gdb := newTestDB(t)
	longName := strings.Repeat("n", 64)

	// 16 个 60 rune 的名字 + "vip":975 + 1 + 3 = 979 rune,写入侧过闸;
	// 把 vip 换成 64 rune 的新名字后是 1040 rune,超出 1024 的总长上限。
	filler := make([]string, 0, 16)
	for i := 0; i < 16; i++ {
		filler = append(filler, fmt.Sprintf("g%02d%s", i, strings.Repeat("x", 57)))
	}
	crowded := seedGroupedAddress(t, gdb, "拥挤线", "https://crowded.example.com", 10,
		strings.Join(append(append([]string{}, filler...), "vip"), ","))
	roomy := seedGroupedAddress(t, gdb, "宽松线", "https://roomy.example.com", 20, "vip")

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "vip", longName, true)
	}))

	afterCrowded := mustRow(t, gdb, crowded.Id)
	assert.Equal(t, strings.Join(filler, ","), afterCrowded.UserGroups,
		"顶破上限的行只摘旧名不写新名,其余名字原样保留")
	assert.True(t, afterCrowded.Enabled, "名单没摘空就不该停用")

	afterRoomy := mustRow(t, gdb, roomy.Id)
	assert.Equal(t, longName, afterRoomy.UserGroups,
		"同一次改名里装得下的行必须照常改写 —— 兜底只对超限的行生效")
	assert.True(t, afterRoomy.Enabled)
}

// 删除:摘掉名字、不换成迁移目标;摘空的行同时停用(空名单 = 对所有分组可见,
// 摘空不停用是把专属线路静默公开 —— 放宽方向,不安全)。
func TestResidueSweepDeleteRemovesNameAndDisablesEmptied(t *testing.T) {
	gdb := newTestDB(t)
	shared := seedGroupedAddress(t, gdb, "VIP 专线", "https://vip.example.com", 10, "vip,svip")
	sole := seedGroupedAddress(t, gdb, "内测线", "https://beta.example.com", 20, "vip")
	unbound := seedGroupedAddress(t, gdb, "主线路", "https://main.example.com", 30, "")

	// to 是删除时的迁移目标 —— 名单绝不能改写成它:一条只给 vip 看的专线
	// 不能因为 vip 的人迁去了 default 就对整个 default 档可见。
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "vip", "default", false)
	}))

	afterShared := mustRow(t, gdb, shared.Id)
	assert.Equal(t, "svip", afterShared.UserGroups)
	assert.True(t, afterShared.Enabled, "名单里还有别的分组时不该停用")

	afterSole := mustRow(t, gdb, sole.Id)
	assert.Equal(t, "", afterSole.UserGroups, "死名字必须摘掉,不能等着被将来的同名新分组复用")
	assert.False(t, afterSole.Enabled, "摘空的行必须同时停用,否则专属线路变成对所有人可见")

	afterUnbound := mustRow(t, gdb, unbound.Id)
	assert.Equal(t, "", afterUnbound.UserGroups)
	assert.True(t, afterUnbound.Enabled, "本来就不绑定分组的默认线路不该被波及")
}

// 探测:命中行数、处置口径、以及「仅绑定它一个」的行单列一条 —— 那一条会被停用,
// 运营必须在确认弹窗里看到这件事。
func TestResidueProbeReportsHitsAndSoleBindings(t *testing.T) {
	gdb := newTestDB(t)
	seedGroupedAddress(t, gdb, "VIP 专线", "https://vip.example.com", 10, "vip,svip")
	seedGroupedAddress(t, gdb, "内测线", "https://beta.example.com", 20, "vip")
	seedGroupedAddress(t, gdb, "主线路", "https://main.example.com", 30, "")

	rows, err := probeResidue(gdb, "VIP")
	require.NoError(t, err, "分组名带大小写差异也必须命中 —— 探测与判定同一套折叠")
	require.Len(t, rows, 2)

	assert.EqualValues(t, 2, rows[0].Rows, "vip 出现在两条地址的名单里")
	assert.Equal(t, groupns.ResidueClean, rows[0].Disposition)
	assert.EqualValues(t, 1, rows[1].Rows, "只绑定 vip 的地址恰有一条")
	assert.Equal(t, groupns.ResidueClean, rows[1].Disposition)

	none, err := probeResidue(gdb, "enterprise")
	require.NoError(t, err)
	require.Len(t, none, 1, "没命中也要报一条 0 行 —— 「这里本来就没有」与「这里没查」是两件事")
	assert.EqualValues(t, 0, none[0].Rows)
}
