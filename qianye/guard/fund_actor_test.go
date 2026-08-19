package guard

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
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
