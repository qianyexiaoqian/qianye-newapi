package commission

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_adjust_selfdealing_test.go —— 手工增减佣金的**操作人**闸门。
//
// 审计实测出来的链条是:role=10 账号对自己 POST
// /api/qy/admin/commission/balances/adjust 凭空造出 50000 佣金 → 对自己发起提现
// → 同一个账号批准这笔提现 → 主库 users.quota +50000。整条链没有 root 复核、
// 没有二次验证、没有自审自批拦截,只有事后审计。
//
// 这里守第一环。断言刻意不止看 HTTP 码:被拒的那一次**账本上必须一个字节都没动**
// (没有 manual 计佣行、没有余额行),否则"拒绝"只是给了个错误码而钱照记。

// seedRoleUser 造一个带角色的账号。seedUser 不写 role(所有既有用例的目标都是
// 普通用户),而越级判据读的正是这一列。
func seedRoleUser(t *testing.T, mainDB *gorm.DB, id int, name string, role int) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.User{
		Id: id, Username: name, Role: role, CreatedAt: 1000,
		AffCode: "aff" + strconv.Itoa(id),
	}).Error)
}

// assertLedgerUntouched 确认这个人在佣金账本上完全不存在。
func assertLedgerUntouched(t *testing.T, gdb *gorm.DB, userId int) {
	t.Helper()
	assert.Empty(t, manualAccrualsOf(t, gdb, userId), "被拒的调整不许留下任何计佣行")
	var balances int64
	require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", userId).Count(&balances).Error)
	assert.EqualValues(t, 0, balances, "被拒的调整不许凭空建出一行余额")
}

// TestAdminAdjust_RefusesToPayTheOperator 守自审自批链的第一环。
//
// callAdminHandler 里的操作人固定是 id=7。用它去调整 7 自己,就是审计报告里
// 第 2 步的原样复现。
func TestAdminAdjust_RefusesToPayTheOperator(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useAdminAPI(t)
	useMoneyGlobals(t, 7, 500000)
	mainDB := useMainDB(t, &model.User{})
	seedRoleUser(t, mainDB, 7, "admin7", common.RoleAdminUser)

	got := callAdjust(t, adjustBody(7, 50000, "给自己补一笔推广佣金", "self-1"))

	assert.Equal(t, http.StatusForbidden, got.Code, got.Body)
	assert.Contains(t, got.Body, "qy_self_dealing")
	assertLedgerUntouched(t, gdb, 7)

	// 减也不行:这个接口对自己整体关闭。放开负方向等于在闸门上开一个
	// 需要每次判断"这次到底是加还是减"的口子,而幂等重放、并发、
	// 以及将来任何一次符号处理上的失误都会从那里漏过去。
	minus := callAdjust(t, adjustBody(7, -50000, "给自己减一笔", "self-2"))
	assert.Equal(t, http.StatusForbidden, minus.Code, minus.Body)
	assertLedgerUntouched(t, gdb, 7)
}

// TestAdminAdjust_RefusesPeerAndHigherRoleTargets 守第二环:管理员之间的闭环。
//
// 自审自批闸门只挡住"自己给自己";两个 role=10 互相记账、再互相批准对方的提现,
// 每一步的操作人都不是受益人,闸门一个都不会响。上游 canManageTargetRole
// 对同一类动作早就是这条判据,扩展这一侧必须对齐。
func TestAdminAdjust_RefusesPeerAndHigherRoleTargets(t *testing.T) {
	cases := []struct {
		name       string
		targetId   int
		targetRole int
		wantStatus int
		wantCode   string
	}{
		{"同级管理员", 201, common.RoleAdminUser, http.StatusForbidden, "qy_target_not_manageable"},
		{"root", 202, common.RoleRootUser, http.StatusForbidden, "qy_target_not_manageable"},
		{"普通用户照旧放行", 203, common.RoleCommonUser, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			useConfig(t, commissionConfig(1))
			useAdminAPI(t)
			useMoneyGlobals(t, 7, 500000)
			mainDB := useMainDB(t, &model.User{})
			seedRoleUser(t, mainDB, tc.targetId, "target", tc.targetRole)

			got := callAdjust(t, adjustBody(tc.targetId, 50000, "补发推广佣金", "peer-"+tc.name))

			require.Equal(t, tc.wantStatus, got.Code, got.Body)
			if tc.wantStatus != http.StatusOK {
				assert.Contains(t, got.Body, tc.wantCode)
				assertLedgerUntouched(t, gdb, tc.targetId)
				return
			}
			assert.Len(t, manualAccrualsOf(t, gdb, tc.targetId), 1,
				"普通用户是这个接口本来的服务对象,不能被一起挡掉")
		})
	}
}
