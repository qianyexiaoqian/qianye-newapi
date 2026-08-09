package groupns

// usergroup_migrate_test.go —— 「一键迁移」这个独立动作。
//
// 每一条守的都是一个**改错了不会报错**的形状:
//
//	default 可迁    迁移入口如果沿用删除那套闸门,项目方报上来的那件事一个字都没解决:
//	                default 删不掉 ⇒ 700 个人挪不走
//	源分组留着      迁移顺手清掉源分组的配置 = 一次没有人要求过的删除,而它对
//	                default 意味着 users.group 的数据库默认值指向一个空壳
//	缓存失效        漏掉的表现是这批人在 TTL 内继续按旧分组解析清单与倍率(默认 60s 的错价)
//	人数闸门        弹窗打开时 3 个人、按下按钮时 300 个人,而决定是照着 3 做的

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

// TestMigrateUserGroupMovesEveryoneOutOfTheUndeletableDefault 是本轮最重要的一条。
//
// 项目方原话:「既然 default 的用户分组无法删除,那么你就在用户分组这里增加一个
// 用户分组迁移的功能」。所以这条用例直接用 default 当源:它永远删不掉,而这批人
// 必须能挪走。
//
// 断言分成两半,缺任何一半都会漏掉一整类缺陷:
//
//	做到的  用户与他们自己的订阅快照迁到目标、目标人数增加
//	没做的  **源分组一个字节都没动** —— 登记行、交叉倍率、充值倍率、指向它的套餐
//	        都还在,残留清理一次都没被调用。迁移不是删除,而这两件事此前共用一条代码
func TestMigrateUserGroupMovesEveryoneOutOfTheUndeletableDefault(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	sweeps := useNoSweepResidue(t)

	require.NoError(t, gdb.Create(newUserGroup("default", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("新手档", 1, now())).Error)
	seedUser(t, main, 1, "default")
	seedUser(t, main, 2, "default")
	seedUser(t, main, 3, "新手档")
	require.NoError(t, main.Create(&model.UserSubscription{
		Id: 1, UserId: 1, PlanId: 1, Status: "active", PrevUserGroup: "default",
	}).Error)
	// 一个卖着「升级到 default」的套餐。删除路径对它是硬拦,迁移路径**不拦** ——
	// 套餐指向的是分组名,人走了名字还在,它照常工作。
	require.NoError(t, main.Create(&model.SubscriptionPlan{
		Id: 9, Title: "体验包", UpgradeGroup: "default",
	}).Error)
	seedOptionRatios(t, main,
		`{"default":{"池":0.3},"新手档":{"池":0.8}}`, `{"default":0.5,"新手档":0.9}`)

	res := callGroupHandler(t, http.MethodPost, "/user-groups/default/migrate",
		gin.Param{Key: "name", Value: "default"},
		`{"target":"新手档","expect_users":2}`, adminMigrateUserGroup)
	require.Equalf(t, http.StatusOK, res.Code, "迁移应当成功: %s", res.Body.String())

	counts := countByGroupCtx(context.Background(), "users")
	assert.EqualValues(t, 0, counts["default"], "源分组必须一个在册用户都不剩")
	assert.EqualValues(t, 3, counts["新手档"], "两个人必须真的落到目标分组上")

	var sub model.UserSubscription
	require.NoError(t, main.First(&sub, 1).Error)
	assert.Equal(t, "新手档", sub.PrevUserGroup,
		"被迁移用户自己的回落目标必须跟着走,否则订阅到期那天他又被降回源分组")

	// ── 源分组必须原样留着 ───────────────────────────────────────────
	var stillThere int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "default").Count(&stillThere).Error)
	assert.EqualValues(t, 1, stillThere, "迁移不是删除:源分组的登记行必须还在")
	assert.Empty(t, *sweeps, "迁移一次都不许调用残留清理 —— 那是删除路径的动作")

	cross, err := loadCrossRatios()
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"池": 0.3}, cross["default"],
		"源分组的交叉倍率必须完整留着 —— 迁完之后这一档随时可能再被用起来")
	assert.Equal(t, map[string]float64{"池": 0.8}, cross["新手档"],
		"目标分组的交叉倍率一个字节都不能变")

	topup, err := loadTopupRatios()
	require.NoError(t, err)
	assert.EqualValues(t, 0.5, topup["default"], "源分组的充值倍率必须留着")
	assert.EqualValues(t, 0.9, topup["新手档"])

	var plan model.SubscriptionPlan
	require.NoError(t, main.First(&plan, 9).Error)
	assert.Equal(t, "default", plan.UpgradeGroup,
		"纯迁移不改套餐引用:分组名还在,套餐照常工作")

	body := res.Body.String()
	assert.Contains(t, body, `"source_remains":true`,
		"「迁完之后源分组仍然存在」必须在响应里说出来,否则运营会以为挪完就没了")
	assert.Contains(t, body, "#9 体验包",
		"仍然会把买家送进源分组的套餐必须列出来 —— 否则人数会自己长回去而没有任何解释")
}

// TestMigrateUserGroupInvalidatesEveryMovedUserCache 锁住迁移之后那一步。
//
// Redis 里的 user:<id> 缓存着旧分组(TTL 默认 60s,而 GetUserGroup 优先读缓存)。
// 不逐个失效的表现是:迁移已经提交、界面显示成功,而这批人在接下来的一分钟里
// 仍然按**旧分组**解析可用清单与倍率 —— 也就是运营刚在弹窗里看过的那份差异
// 有一分钟不成立。
func TestMigrateUserGroupInvalidatesEveryMovedUserCache(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	useNoSweepResidue(t)

	invalidated := make([]int, 0, 2)
	prev := invalidateUserCache
	invalidateUserCache = func(id int) error {
		invalidated = append(invalidated, id)
		return nil
	}
	t.Cleanup(func() { invalidateUserCache = prev })

	require.NoError(t, gdb.Create(newUserGroup("旧档", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("新档", 1, now())).Error)
	seedUser(t, main, 11, "旧档")
	seedUser(t, main, 12, "旧档")

	res := callGroupHandler(t, http.MethodPost, "/user-groups/旧档/migrate",
		gin.Param{Key: "name", Value: "旧档"},
		`{"target":"新档","expect_users":2}`, adminMigrateUserGroup)
	require.Equalf(t, http.StatusOK, res.Code, "迁移应当成功: %s", res.Body.String())

	assert.ElementsMatch(t, []int{11, 12}, invalidated,
		"每一个被迁移的用户都必须被逐个失效 —— 漏掉的那个在 TTL 内继续按旧分组计价")
}

// TestMigrateUserGroupRejectsStaleImpact 锁住一致性闸门。
//
// 弹窗打开时是 0 个人、按下按钮时已经是 200 个人 —— 而运营是照着 0 那个数字
// 判断"这次迁移无关紧要"的。
func TestMigrateUserGroupRejectsStaleImpact(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})
	useNoSweepResidue(t)

	require.NoError(t, gdb.Create(newUserGroup("旧档", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("新档", 1, now())).Error)
	seedUser(t, main, 21, "旧档")

	res := callGroupHandler(t, http.MethodPost, "/user-groups/旧档/migrate",
		gin.Param{Key: "name", Value: "旧档"},
		`{"target":"新档","expect_users":5}`, adminMigrateUserGroup)

	assert.Equal(t, http.StatusConflict, res.Code)
	assert.Contains(t, res.Body.String(), "qy_groupns_impact_drift")
	assert.EqualValues(t, 1, countByGroupCtx(context.Background(), "users")["旧档"],
		"被拒绝的迁移不能动 users.group")
}

// TestMigrateUserGroupRefusesATargetThatCanUseNothing 锁住那道单独的闸门。
//
// 迁进一个一个模型分组都选不到的目标 = 这批人的全部令牌在迁移完成的那一刻同时
// 403。它不会有任何界面提示,只会有工单,所以必须显式勾选才放行。
func TestMigrateUserGroupRefusesATargetThatCanUseNothing(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	// 全局白名单为空、目标分组也不在分组倍率表里 ⇒ 它一个模型分组都选不到。
	// 这正是一个刚建出来、还没配任何东西的分组在真实站点上的样子。
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})
	useNoSweepResidue(t)

	require.NoError(t, gdb.Create(newUserGroup("旧档", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("空壳档", 1, now())).Error)
	seedUser(t, main, 31, "旧档")

	res := callGroupHandler(t, http.MethodPost, "/user-groups/旧档/migrate",
		gin.Param{Key: "name", Value: "旧档"},
		`{"target":"空壳档","expect_users":1}`, adminMigrateUserGroup)

	require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
	assert.Contains(t, res.Body.String(), "qy_groupns_target_unusable")
	assert.EqualValues(t, 1, countByGroupCtx(context.Background(), "users")["旧档"])

	// 显式勾选之后同一次请求必须放行 —— 闸门是"必须有人签字",不是"永远不许"。
	res = callGroupHandler(t, http.MethodPost, "/user-groups/旧档/migrate",
		gin.Param{Key: "name", Value: "旧档"},
		`{"target":"空壳档","expect_users":1,"ack_loses_everything":true}`, adminMigrateUserGroup)
	require.Equalf(t, http.StatusOK, res.Code, "勾选之后应当放行: %s", res.Body.String())
	assert.EqualValues(t, 1, countByGroupCtx(context.Background(), "users")["空壳档"])
}

// TestMigrateUserGroupRejectsUnknownOrSelfTarget 守住目标那一侧的两条。
//
// 目标未登记时放行,表现是这批人被挪进一个谁都配不了的名字(登记表是管理端
// 一切写校验的判据);目标是自己时 model 层会报错,但那条错误在界面上是一句
// 「处理失败,请稍后重试」。
func TestMigrateUserGroupRejectsUnknownOrSelfTarget(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})
	useNoSweepResidue(t)

	require.NoError(t, gdb.Create(newUserGroup("旧档", 1, now())).Error)
	seedUser(t, main, 41, "旧档")

	tests := []struct {
		name string
		body string
		want string
	}{
		{"目标为空", `{"expect_users":1}`, "qy_groupns_migration_required"},
		{"目标是自己", `{"target":"旧档","expect_users":1}`, "qy_invalid_param"},
		{"目标没有登记", `{"target":"查无此档","expect_users":1}`, "qy_groupns_unknown_target"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := callGroupHandler(t, http.MethodPost, "/user-groups/旧档/migrate",
				gin.Param{Key: "name", Value: "旧档"}, tc.body, adminMigrateUserGroup)
			assert.Equal(t, http.StatusBadRequest, res.Code)
			assert.Contains(t, res.Body.String(), tc.want)
		})
	}
	assert.EqualValues(t, 1, countByGroupCtx(context.Background(), "users")["旧档"],
		"三次被拒绝的迁移都不能动 users.group")
}

// useNoSweepResidue 注册一个**只声明、绝不清理**的残留处置。
//
// 迁移路径一次都不该调用 Sweep:它是删除路径的动作。Probe 声明的是一条
// rewrite 残留(形状与「新注册用户的默认分组」相同),用来断言迁移会把
// 「迁完之后仍然会有人被放回来」这件事列出来。
func useNoSweepResidue(t *testing.T) *[]string {
	t.Helper()
	calls := make([]string, 0, 1)
	ResetResiduesForTest()
	t.Cleanup(ResetResiduesForTest)
	RegisterResidue(ResidueHandler{
		Module: "fake",
		Probe: func(*gorm.DB, string) ([]Residue, error) {
			return []Residue{{Module: "fake", Table: "qy_default_group", Label: "新注册用户默认分组",
				Rows: 1, Disposition: ResidueRewrite}}, nil
		},
		Sweep: func(_ *gorm.DB, from, to string, _ bool) error {
			calls = append(calls, from+"→"+to)
			return nil
		},
	})
	return &calls
}
