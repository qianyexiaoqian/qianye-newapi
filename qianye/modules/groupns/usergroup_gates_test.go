package groupns

// usergroup_gates_test.go —— 删除路径上三道「在册用户数为 0 也不能放行」的闸门,
// 以及「迁移期间有人掉队时必须停在清配置之前」。
//
// 这四条守的都是同一类形状:影响面统计里看不到的东西被静默改掉。
//
//	rewrite 残留   新注册用户的默认分组指着被删的分组,而那一档人**还不存在**,
//	               一个都不在 impact.Users 里。目标为空时它只能被清掉,
//	               此后每个新用户静默落进上游 default 档(不同的倍率、不同的清单)。
//	目标校验       users==0 时目标也会被写进 user_subscriptions 的三列快照
//	               (那三条 UPDATE 按列值全表匹配,与被迁移的用户无关)。
//	               一个从未校验过存在性的名字写进去,买家将来被降级进一个不存在的分组。
//	掉队的人       行锁锁不住 INSERT。继续清配置 ⇒ 那几个账号指向一个没有倍率的名字
//	               ⇒ GetGroupRatio fail-open 1.0 ⇒ 静默按原价扣费、零告警。

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useRewriteResidue 注册一个处置为 rewrite、恒命中 1 行的假模块,
// 并记录 Sweep 被调用时拿到的 (from, to)。
func useRewriteResidue(t *testing.T) *[]string {
	t.Helper()
	calls := make([]string, 0, 2)
	ResetResiduesForTest()
	t.Cleanup(ResetResiduesForTest)
	RegisterResidue(ResidueHandler{
		Module: "fakerewrite",
		Probe: func(*gorm.DB, string) ([]Residue, error) {
			return []Residue{{
				Module: "fakerewrite", Table: "qy_settings(usergroup/default_group)",
				Label: "新注册用户的默认分组", Rows: 1,
				Disposition: ResidueRewrite,
			}}, nil
		},
		Sweep: func(_ *gorm.DB, from, to string, _ bool) error {
			calls = append(calls, from+"→"+to)
			return nil
		},
	})
	return &calls
}

// TestDeleteUserGroupWithoutUsersStillNeedsATarget 是本轮修掉的那个洞的回归。
//
// 「新人体验档」还没有人注册进去(users=0),但它是新注册用户的默认分组。
// 修之前:impact.Users == 0 ⇒ 整段目标校验被跳过 ⇒ target=="" ⇒
// usergroup 的 Sweep 把 default_group 写成空串 = 「取消配置、回到上游默认」,
// 而弹窗从头到尾把这一行标成「跟着改写」。
func TestDeleteUserGroupWithoutUsersStillNeedsATarget(t *testing.T) {
	gdb := newTestDB(t)
	newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	swept := useRewriteResidue(t)

	require.NoError(t, gdb.Create(newUserGroup("新人体验档", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("default", 1, now())).Error)

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/新人体验档",
		gin.Param{Key: "name", Value: "新人体验档"}, `{"expect_users":0}`, adminDeleteUserGroup)

	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Contains(t, res.Body.String(), "qy_groupns_migration_required")
	assert.Contains(t, res.Body.String(), "新注册用户的默认分组",
		"拒绝正文必须点名是哪一处配置在等目标 —— 这一处没有任何界面会显示")
	assert.Empty(t, *swept, "被拒绝的删除一次 Sweep 都不许跑")

	var still int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "新人体验档").Count(&still).Error)
	assert.EqualValues(t, 1, still, "被拒绝的删除不能动登记表")
}

// TestDeleteUserGroupWithoutUsersValidatesTheTarget:users==0 时目标仍然会被
// 写进 user_subscriptions 的三列快照与默认注册分组,所以它必须先被校验。
func TestDeleteUserGroupWithoutUsersValidatesTheTarget(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	swept := useFakeResidue(t, gdb)

	require.NoError(t, gdb.Create(newUserGroup("空档", 1, now())).Error)
	// 一条已售订阅的降级目标指着这个已经没有人的分组。
	require.NoError(t, main.Create(&model.UserSubscription{
		Id: 1, UserId: 7, PlanId: 1, Status: "active", DowngradeGroup: "空档",
	}).Error)

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/空档",
		gin.Param{Key: "name", Value: "空档"},
		`{"expect_users":0,"migrate_to":"从来没登记过的档"}`, adminDeleteUserGroup)

	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Contains(t, res.Body.String(), "qy_groupns_unknown_target")
	assert.Empty(t, *swept)

	var sub model.UserSubscription
	require.NoError(t, main.Take(&sub, 1).Error)
	assert.Equal(t, "空档", sub.DowngradeGroup,
		"被拒绝的删除不能把订阅快照改指到一个未登记的名字")
}

// TestDeleteUserGroupRejectsItselfAsTargetWithoutUsers 补上同一段校验的另一半:
// 目标是自己时,阶段二会被 QyRewriteUserGroupTx 的 from==to 拦住,但阶段三仍然
// 会把这个分组的配置清光 —— 表现是「删了一半、用户还在」。
func TestDeleteUserGroupRejectsItselfAsTargetWithoutUsers(t *testing.T) {
	gdb := newTestDB(t)
	newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	swept := useFakeResidue(t, gdb)

	require.NoError(t, gdb.Create(newUserGroup("空档", 1, now())).Error)

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/空档",
		gin.Param{Key: "name", Value: "空档"},
		`{"expect_users":0,"migrate_to":"空档"}`, adminDeleteUserGroup)

	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Empty(t, *swept)
}

// TestRewriteUserGroupStopsBeforeCleanupWhenSomeoneIsLeftBehind 用一个 GORM
// 回调模拟"迁移事务执行期间又有人注册进源分组"。
//
// 修之前:阶段二把他数进 Stragglers 之后,阶段三照样 SweepResidues + 删登记行 +
// 清 options,于是那个账号的 users.group 指向一个没有倍率、没有权威清单的名字。
// 修之后:整件事停在清配置之前,源分组的配置一个字节都没动 —— 那个账号仍然
// 按完整配置计费,运营把他改走之后再按一次同样的操作即可收敛。
func TestRewriteUserGroupStopsBeforeCleanupWhenSomeoneIsLeftBehind(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	swept := useFakeResidue(t, gdb)

	require.NoError(t, gdb.Create(newUserGroup("旧档", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("新档", 1, now())).Error)
	seedUser(t, main, 1, "旧档")

	// users 的 UPDATE 一提交,就往源分组里塞一个"刚注册"的账号。
	// 行锁只锁得住已经存在的行,拦不住 INSERT —— 这正是生产上那条缝隙。
	injected := false
	require.NoError(t, main.Callback().Update().After("gorm:update").
		Register("qy_test_inject_straggler", func(tx *gorm.DB) {
			if injected || tx.Statement.Table != "users" {
				return
			}
			injected = true
			_, execErr := tx.Statement.ConnPool.ExecContext(context.Background(),
				"INSERT INTO users (id, username, password, `group`, status) VALUES (?, ?, ?, ?, ?)",
				99, "late-signup", "x", "旧档", 1)
			require.NoError(t, execErr)
		}))
	t.Cleanup(func() {
		_ = main.Callback().Update().Remove("qy_test_inject_straggler")
	})

	res, err := rewriteUserGroup(gdb, "旧档", "新档", false, 1)

	require.Error(t, err, "有人掉队时必须报错,而不是报成功然后走人")
	assert.EqualValues(t, 1, res.Stragglers)
	require.NotNil(t, res.Partial)
	assert.Equal(t, StageMigrate, res.Partial.Stage,
		"停在阶段二 = 源分组配置未动 = 掉队的人不是孤儿")
	assert.Empty(t, *swept, "一次残留清理都不许跑")

	var still int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "旧档").Count(&still).Error)
	assert.EqualValues(t, 1, still, "登记行必须原地不动 —— 掉队的账号还指着它")
}
