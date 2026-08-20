package guard

import (
	"context"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fund_actor_test.go —— 两条动钱判据的真值表。
//
// 它们各自只有一行代码,但那一行是"管理员能不能给自己发钱"的全部答案,
// 而且被两个模块共用。零值那两格(没有操作人 / 没有角色)是重点:
// 判据一旦在这两格上放行,任何一次把处理器挂到鉴权链之外的失误
// 都会静默地变成一条自助铸币通道。

func TestSelfDealing(t *testing.T) {
	cases := []struct {
		name    string
		actor   int
		target  int
		want    bool
		because string
	}{
		{"操作人就是受益人", 9001, 9001, true, "自铸/自批那条链的形状"},
		{"操作人给别人", 9001, 9002, false, "正常的管理动作"},
		{"上下文里没有操作人(id 缺失)", 0, 9002, true, "查不出是谁在动钱,一律拒绝"},
		{"上下文里没有操作人且目标也是 0", 0, 0, true, "同上,不是因为相等才拒绝"},
		{"负数操作人", -1, 9002, true, "同 0:不是一个能归属的账号"},
		{"目标是 0(参数没填)", 9001, 0, false, "由各模块自己的参数校验负责"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SelfDealing(tc.actor, tc.target), tc.because)
		})
	}
}

func TestManageableTarget(t *testing.T) {
	cases := []struct {
		name      string
		actorRole int
		target    int
		want      bool
	}{
		{"root 管管理员", common.RoleRootUser, common.RoleAdminUser, true},
		{"root 管 root", common.RoleRootUser, common.RoleRootUser, true},
		{"管理员管普通用户", common.RoleAdminUser, common.RoleCommonUser, true},
		{"管理员管游客", common.RoleAdminUser, common.RoleGuestUser, true},
		{"管理员管同级管理员", common.RoleAdminUser, common.RoleAdminUser, false},
		{"管理员管 root", common.RoleAdminUser, common.RoleRootUser, false},
		{"普通用户管普通用户", common.RoleCommonUser, common.RoleCommonUser, false},
		{"上下文里没有角色", 0, common.RoleCommonUser, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ManageableTarget(tc.actorRole, tc.target))
		})
	}
}

// ── ActorMayActOn:两条判据的组合 + 角色回查 ────────────────────────────────
//
// SelfDealing 与 ManageableTarget 各自的真值表在上面。这里守的是把它们接起来
// 之后才出现的三件事:回查的目标角色来自主库、软删的账号仍然按角色判、
// 主库不可用时 fail-closed。
//
// 为什么必须真跑一次数据库:漏判的形状从来不是"判据写错了",而是
// "回查根本没发生"—— 而一个不查库的实现在纯真值表下与正确实现完全同形。

func newActorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

func TestActorMayActOn(t *testing.T) {
	const actorId = 700

	cases := []struct {
		name       string
		actorId    int
		actorRole  int
		targetId   int
		targetRole int
		softDelete bool
		want       error
	}{
		{name: "管理员动普通用户", actorId: actorId, actorRole: common.RoleAdminUser,
			targetId: 701, targetRole: common.RoleCommonUser, want: nil},
		{name: "管理员动自己", actorId: actorId, actorRole: common.RoleAdminUser,
			targetId: actorId, targetRole: common.RoleAdminUser, want: ErrActorIsTarget},
		{name: "管理员动同级管理员", actorId: actorId, actorRole: common.RoleAdminUser,
			targetId: 702, targetRole: common.RoleAdminUser, want: ErrTargetNotLower},
		{name: "管理员动 root", actorId: actorId, actorRole: common.RoleAdminUser,
			targetId: 703, targetRole: common.RoleRootUser, want: ErrTargetNotLower},
		{name: "root 动管理员", actorId: actorId, actorRole: common.RoleRootUser,
			targetId: 704, targetRole: common.RoleAdminUser, want: nil},
		{name: "root 动 root(不是自己)", actorId: actorId, actorRole: common.RoleRootUser,
			targetId: 705, targetRole: common.RoleRootUser, want: nil},
		{name: "目标查无此人", actorId: actorId, actorRole: common.RoleAdminUser,
			targetId: 0, targetRole: common.RoleCommonUser, want: ErrTargetMissing},
		{name: "上下文里没有操作人", actorId: 0, actorRole: common.RoleAdminUser,
			targetId: 706, targetRole: common.RoleCommonUser, want: ErrActorIsTarget},
		{name: "上下文里没有角色", actorId: actorId, actorRole: 0,
			targetId: 707, targetRole: common.RoleCommonUser, want: ErrTargetNotLower},
		// 软删的管理员仍然是管理员:Unscoped 少写一次,这一格就会变成"目标不存在"
		// 而被放行,放行的正是"对一个更高权限账号动手"。
		{name: "软删的同级管理员", actorId: actorId, actorRole: common.RoleAdminUser,
			targetId: 708, targetRole: common.RoleAdminUser, softDelete: true,
			want: ErrTargetNotLower},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newActorTestDB(t)
			if tc.targetId > 0 && tc.targetId != tc.actorId {
				require.NoError(t, gdb.Create(&model.User{
					Id: tc.targetId, Username: "t" + strconv.Itoa(tc.targetId),
					Role: tc.targetRole, AffCode: "aff" + strconv.Itoa(tc.targetId),
				}).Error)
				if tc.softDelete {
					require.NoError(t, gdb.Delete(&model.User{}, tc.targetId).Error)
				}
			}
			// 目标是"查无此人"那一格时刻意不建行:targetId 为 0 连查库都不该发生。
			err := ActorMayActOn(context.Background(), tc.actorId, tc.actorRole, tc.targetId)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// TestActorMayActOnFailsClosedWithoutMainDB 守"主库读不到就不许动钱"。
//
// 判据要回查角色,而回查不了时唯一安全的答案是拒绝 —— 返回 nil(放行)会让
// 一次主库抖动变成一扇敞开的越权门。
func TestActorMayActOnFailsClosedWithoutMainDB(t *testing.T) {
	prev := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = prev })

	err := ActorMayActOn(context.Background(), 700, common.RoleAdminUser, 701)
	assert.ErrorIs(t, err, db.ErrNotReady)
}
