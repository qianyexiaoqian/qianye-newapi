package usergroup

// residue_tx_test.go —— 「新注册用户的默认分组」这一行必须住在调用方的事务里。
//
// 修之前 sweepResidue 丢掉传进来的 tx,改用 db.Get() 另开一条连接。表现:
// 删除用户分组 A 并迁移到 B 时,SweepResidues 所在的事务只要后续任何一步失败
// (删登记行遇到锁等待、连接中断),整个事务回滚 —— A 的登记行、范围、授权、
// 返佣费率、划转规则全部还原,**唯独 default_group 已经落库成 B 且回滚不了**。
// 接口返回 500,正文写的是「扩展库配置没有清掉」,与实际发生的事正好相反;
// 从那一刻起所有新注册用户进 B,而 A 仍然存在、界面上看不出任何异常。
//
// 缓存是同一件事的第二半:在事务里刷缓存,回滚撤销得了库行、撤销不了内存,
// 进程会拿着一个数据库中并不存在的默认分组继续给新用户分组,直到重启。

import (
	"errors"
	"testing"

	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/QuantumNous/new-api/qianye/modules/groupns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// readDefaultGroupRow 直接读库,绕开一切缓存。
func readDefaultGroupRow(t *testing.T, gdb *gorm.DB) string {
	t.Helper()
	var row qymodel.Setting
	err := gdb.Where("scope = ? AND k = ?", settingScope, keyDefaultGroup).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "<无此行>"
	}
	require.NoError(t, err)
	return row.V
}

func TestDefaultGroupSweepRollsBackWithTheTransaction(t *testing.T) {
	gdb := newExtDB(t)
	require.NoError(t, writeDefaultGroup("体验档", 1))
	require.Equal(t, "体验档", currentDefaultGroup())

	// 模拟真实调用形状:SweepResidues 与「删登记行」在同一个事务里,
	// 后者失败让整个事务回滚。
	boom := errors.New("删登记行失败(锁等待)")
	err := gdb.Transaction(func(tx *gorm.DB) error {
		if err := sweepResidue(tx, "体验档", "正式档", false); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)

	assert.Equal(t, "体验档", readDefaultGroupRow(t, gdb),
		"事务回滚了,这一行必须跟着回滚 —— 否则分组还在、默认注册分组却已经改掉")
	assert.Equal(t, "体验档", currentDefaultGroup(),
		"缓存也不许在事务里被改:回滚撤销不了内存,进程会一直用一个库里没有的值")
}

func TestDefaultGroupSweepThenCommitMovesBothRowAndCache(t *testing.T) {
	gdb := newExtDB(t)
	require.NoError(t, writeDefaultGroup("体验档", 1))

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "体验档", "正式档", false)
	}))
	assert.Equal(t, "正式档", readDefaultGroupRow(t, gdb))
	assert.Equal(t, "体验档", currentDefaultGroup(),
		"提交之前缓存不动;刷新是 AfterCommit 的事")

	groupns.CommitResidues("体验档", "正式档", false)
	assert.Equal(t, "正式档", currentDefaultGroup())
}

// TestDefaultGroupSweepRefusesToClearOnEmptyTarget:目标为空时**保留原值**。
//
// 清空的含义是「取消配置、回到上游默认」——那是一次没有任何人决定过的策略变更
// (新用户从体验档静默变成 default 档,不同的倍率、不同的可用模型分组)。
// 正常路径上 adminDeleteUserGroup 的 RewriteResidueRows 闸门已经拦住了空目标;
// 这里守的是绕过接口直接调用时的方向。
func TestDefaultGroupSweepRefusesToClearOnEmptyTarget(t *testing.T) {
	gdb := newExtDB(t)
	require.NoError(t, writeDefaultGroup("体验档", 1))

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "体验档", "", false)
	}))
	assert.Equal(t, "体验档", readDefaultGroupRow(t, gdb))
	assert.Equal(t, "体验档", currentDefaultGroup())
}
