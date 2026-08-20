package paypass

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_actor_gate_test.go —— 管理端两个写动作的操作人闸门。
//
// 支付密码是划转(把额度转给别人)的第二因子。管理端的重置**不需要原密码、
// 不需要邮箱验证码**,所以在闸门装上之前:
//
//   - 一个 role=10 账号对自己调用 reset,等于用一次 HTTP 请求把自己账号的
//     第二因子拆掉 —— 会话被盗之后攻击者的第一步就是它;
//   - 对自己调用 unlock,等于让错误计数永远不封顶,支付密码可以在线无限次爆破;
//   - 对 root 或另一个管理员调用,是在拆**别人**的第二因子,而那正是上游
//     canManageTargetRole 要挡的形状。
//
// newRouter 里的操作人固定 role=10(与生产上 middleware.AdminAuth() 一致)。

// hashOf 读回某个账号当前的支付密码哈希。
func hashOf(t *testing.T, gdb *gorm.DB, userId int) string {
	t.Helper()
	var row PayPassword
	require.NoError(t, gdb.Where("user_id = ?", userId).Take(&row).Error)
	return row.Hash
}

// seedUserWithRole 往主库插一个指定角色的账号。
func seedUserWithRole(t *testing.T, gdb *gorm.DB, id int, username string, role int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.User{
		Id: id, Username: username, Role: role,
		AffCode: "roleaff" + username,
		Status:  common.UserStatusEnabled, Group: "default",
	}).Error)
}

// TestAdminMutateRefusesSelfAndPeerTargets 逐格证明两个写动作都被挡住。
//
// 变异验证:把 api_admin.go 的 adminMutate 里那五行 adminTargetActable 删掉,
// 六个用例的状态码、密码状态与审计断言同时变红。
func TestAdminMutateRefusesSelfAndPeerTargets(t *testing.T) {
	const actorId = 9001
	cases := []struct {
		name       string
		path       string
		action     string
		targetId   int
		targetRole int
		wantCode   string
	}{
		{"解锁自己", "/api/qy/admin/pay-password/9001/unlock", "pay_password.unlock",
			actorId, common.RoleAdminUser, "qy_self_dealing"},
		{"重置自己", "/api/qy/admin/pay-password/9001/reset", "pay_password.reset",
			actorId, common.RoleAdminUser, "qy_self_dealing"},
		{"解锁同级管理员", "/api/qy/admin/pay-password/9002/unlock", "pay_password.unlock",
			9002, common.RoleAdminUser, "qy_target_not_manageable"},
		{"重置同级管理员", "/api/qy/admin/pay-password/9002/reset", "pay_password.reset",
			9002, common.RoleAdminUser, "qy_target_not_manageable"},
		{"解锁 root", "/api/qy/admin/pay-password/9003/unlock", "pay_password.unlock",
			9003, common.RoleRootUser, "qy_target_not_manageable"},
		{"重置 root", "/api/qy/admin/pay-password/9003/reset", "pay_password.reset",
			9003, common.RoleRootUser, "qy_target_not_manageable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			mainDB := useMainDB(t)
			if tc.targetId != actorId {
				seedUserWithRole(t, mainDB, actorId, "operator", common.RoleAdminUser)
			}
			seedUserWithRole(t, mainDB, tc.targetId, "target", tc.targetRole)
			setPassword(t, gdb, tc.targetId, goodPassword)
			require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", tc.targetId).
				Updates(map[string]any{"fail_count": 5,
					"locked_until": common.GetTimestamp() + 3600}).Error)
			r := newRouter(t, actorId)
			hashBefore := hashOf(t, gdb, tc.targetId)

			rec := do(r, http.MethodPost, tc.path, `{"reason":"我自己来"}`)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantCode)

			row := rowOf(t, gdb, tc.targetId)
			assert.EqualValues(t, 5, row.FailCount, "被拒的解锁不许清错误计数")
			assert.NotZero(t, row.LockedUntil, "被拒的解锁不许解开锁定")
			assert.True(t, row.isSet(), "被拒的重置不许清掉密码")
			// 不走 verify:目标此刻正处于锁定态,验密本来就会被锁拒,
			// 那条断言无论闸门在不在都会红。与请求**之前**读到的哈希逐字比,
			// 才是"密码一个字节都没被动过"。
			assert.Equal(t, hashBefore, row.Hash)

			// 被拒也要留痕:它是"会话已经被盗、正在拆第二因子"这条链上最显眼的一步。
			assert.Equal(t, []string{tc.action + ".denied:fail"}, auditActions(t, gdb))
			var entry qymodel.AuditLog
			require.NoError(t, gdb.Order("id desc").First(&entry).Error)
			assert.Equal(t, qymodel.ActorAdmin, entry.ActorType)
			assert.Equal(t, actorId, entry.ActorUserId)
			assert.Equal(t, tc.targetId, entry.TargetUserId)
		})
	}
}

// TestAdminMutateStillServesOrdinaryUsers 是对照组。
//
// 少了它,把 adminTargetActable 改成"一律拒绝"也能让上面那组全绿 ——
// 而那是把支付密码的人工救援通道整个锁死,用户申诉将永远无人能处理。
func TestAdminMutateStillServesOrdinaryUsers(t *testing.T) {
	gdb := newTestDB(t)
	mainDB := useMainDB(t)
	seedUserWithRole(t, mainDB, 9001, "operator", common.RoleAdminUser)
	seedUserWithRole(t, mainDB, 7500, "victim", common.RoleCommonUser)
	setPassword(t, gdb, 7500, goodPassword)
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", 7500).
		Updates(map[string]any{"fail_count": 5,
			"locked_until": common.GetTimestamp() + 3600}).Error)
	r := newRouter(t, 9001)

	rec := do(r, http.MethodPost, "/api/qy/admin/pay-password/7500/unlock", `{"reason":"用户申诉"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	row := rowOf(t, gdb, 7500)
	assert.Zero(t, row.FailCount)
	assert.Zero(t, row.LockedUntil)
	assert.Equal(t, []string{"pay_password.unlock:ok"}, auditActions(t, gdb))
}
