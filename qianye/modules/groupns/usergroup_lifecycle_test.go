package groupns

// usergroup_lifecycle_test.go —— 用户分组的新建 / 改名 / 删除并迁移。
//
// 每一条用例守的都是一个**改错了不会报错**的形状:
//
//	命名校验      放行一个模型分组的名字 → 两个命名空间重新合流,而这一轮的全部工作
//	              就是把它们拆开;放行一个大小写近似名 → INSERT 撞 ci 主键,
//	              运营看到的是"保存失败,请稍后重试"
//	迁移必填      直接删掉有人的分组 → 孤儿 users.group → GetGroupRatio fail-open 1.0
//	              → 整组人静默按原价扣费,零告警,没有任何一条日志指向这次删除
//	不合并配置    把源分组的授权/费率合并进目标 → 在运营不知情的情况下改掉另一档人的
//	              权限与价格
//	顺序         先删配置再迁用户 → 窗口期里那批人是孤儿
//	一个事务      残留清理半途失败 → "范围清了、费率还在"

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ─────────────────────────── 命名校验 ───────────────────────────

func TestValidateNewUserGroupNameGuardsBothNamespaces(t *testing.T) {
	gdb := newTestDB(t)
	newMainDB(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"付费池": 1, "default": 1})

	require.NoError(t, gdb.Create(newUserGroup("初衷专属分组", 1, now())).Error)
	require.NoError(t, gdb.Create(newModelGroup("免费の渠道", 1, now())).Error)

	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"空名字", "", "不能为空"},
		{"超过 64 个字符", repeatRune('夜', 65), "不能超过"},
		{"64 个汉字仍然合法", repeatRune('夜', 64), ""},
		{"带逗号", "a,b", "逗号"},
		{"首尾空白", " vip ", "空白"},
		{"auto 伪分组", "auto", "auto"},
		{"与已登记的用户分组重名", "初衷专属分组", "已经存在"},
		{"与已登记用户分组只差大小写", "DEFAULT-X", ""},
		{"与已登记的模型分组重名", "免费の渠道", "模型分组"},
		{"在分组倍率表里(那是模型分组的键)", "付费池", "分组倍率表"},
		{"全新的名字", "内测档", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNewUserGroupName(gdb, tc.in)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestValidateNewUserGroupNameFoldsCaseLikeTheStorageDoes 单列出来是因为它守的
// 不是"某个字符串被拒绝",而是**判据必须与存储层一致**。
//
// 扩展库固定是 MySQL,qy_user_groups.name 继承库默认排序规则(5.7 general_ci /
// 8.0 0900_ai_ci),两者都大小写不敏感。精确比较放行的 "VIP" 会在 INSERT 时撞
// 主键 —— 报出来的是一条 Error 1062,而运营看到的是"保存失败,请稍后重试"。
func TestValidateNewUserGroupNameFoldsCaseLikeTheStorageDoes(t *testing.T) {
	gdb := newTestDB(t)
	newMainDB(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})
	require.NoError(t, gdb.Create(newUserGroup("vip", 1, now())).Error)

	for _, candidate := range []string{"VIP", "Vip", " vip"} {
		err := validateNewUserGroupName(gdb, candidate)
		require.Errorf(t, err, "%q 与已存在的 vip 在 ci 排序规则下是同一行,必须在接口层就拒绝", candidate)
	}
}

// ─────────────────────────── 迁移影响面 ───────────────────────────

// TestBuildMigrationDiffTellsThemWhatTheyLose 锁住确认弹窗里那一段。
//
// 「迁移后他们的可用分组会从 A 变成 B」是项目方点名要的东西。少了它,
// 运营看到的只是"把 707 个人从 A 迁到 B",而迁过去之后少掉的每一个模型分组
// 都是那批人手上令牌的当场 403,多出来的每一格倍率差都是从下一秒开始的账单变化。
func TestBuildMigrationDiffTellsThemWhatTheyLose(t *testing.T) {
	newTestDB(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{
		"共有池": 1, "只有A能用": 0.5, "只有B能用": 2,
	})
	prev := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(prev) })
	// A 对共有池谈到 0.3,B 没谈 → 迁过去按兜底 1。这是一次**涨价 3.3 倍**,
	// 而"删掉一个分组"这句话里一个字都没提到它。
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"A":{"共有池":0.3}}`))

	usable := func(group string) map[string]string {
		switch group {
		case "A":
			return map[string]string{"共有池": "", "只有A能用": ""}
		case "B":
			return map[string]string{"共有池": "", "只有B能用": ""}
		default:
			return map[string]string{}
		}
	}

	diff := BuildMigrationDiff("A", "B", usable)

	assert.Equal(t, 0, diff.Unchanged, "共有池的倍率从 0.3 变成 1,不算不变")
	assert.False(t, diff.LosesEverything)
	assert.Equal(t, []UsableChange{
		{ModelGroup: "共有池", Kind: "repriced", FromRatio: "0.3", ToRatio: "1"},
		{ModelGroup: "只有A能用", Kind: "removed"},
		{ModelGroup: "只有B能用", Kind: "added"},
	}, diff.Changes)
}

// TestBuildMigrationDiffFlagsATargetThatCanUseNothing 锁住那道单独的闸门。
//
// 迁进一个一个模型分组都选不到的目标 = 这批人的全部令牌在迁移完成的那一刻同时
// 403。它不会有任何界面提示,只会有工单,所以必须是一个独立的布尔而不是
// "removed 条数很多"这种要人去数的信号。
func TestBuildMigrationDiffFlagsATargetThatCanUseNothing(t *testing.T) {
	newTestDB(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"池": 1})
	usable := func(group string) map[string]string {
		if group == "A" {
			return map[string]string{"池": ""}
		}
		return map[string]string{}
	}
	diff := BuildMigrationDiff("A", "空壳", usable)
	assert.True(t, diff.LosesEverything,
		"目标分组一个模型分组都选不到时必须显式报出来 —— 它是唯一一个会让整批令牌当场停摆的形状")
}

// ─────────────────────────── 删除 ───────────────────────────

// TestDeleteUserGroupRefusesToOrphanUsers 是本轮最重要的一条。
//
// 直接删掉一个还有人的分组会造出孤儿 users.group,而 GetGroupRatio 对孤儿是
// **fail-open 返回 1.0**:那批人从此静默按原价扣费,没有任何告警、没有任何日志
// 指向这次删除。所以"没给迁移目标"必须是一个 400,而不是一次成功的删除。
func TestDeleteUserGroupRefusesToOrphanUsers(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})

	require.NoError(t, gdb.Create(newUserGroup("清芯", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("default", 1, now())).Error)
	seedUser(t, main, 1, "清芯")
	seedUser(t, main, 2, "清芯")

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/清芯",
		gin.Param{Key: "name", Value: "清芯"}, `{"expect_users":2}`, adminDeleteUserGroup)

	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Contains(t, res.Body.String(), "qy_groupns_migration_required")

	var still int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "清芯").Count(&still).Error)
	assert.EqualValues(t, 1, still, "被拒绝的删除不能动登记表")
	assert.EqualValues(t, 2, countByGroupCtx(context.Background(), "users")["清芯"],
		"被拒绝的删除不能动 users.group")
}

// TestDeleteUserGroupMigratesThenClearsEverything 走一遍完整的删除。
//
// 断言分成两半,缺任何一半这条用例都会漏掉一整类缺陷:
//
//	做到的  用户迁走、登记行消失、各模块残留按声明清掉、options 的两处键消失
//	没做的  **目标分组自己的配置一个字节都没变** —— 合并过去会在运营不知情的
//	        情况下改掉另一档人的权限与价格
func TestDeleteUserGroupMigratesThenClearsEverything(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	swept := useFakeResidue(t, gdb)

	require.NoError(t, gdb.Create(newUserGroup("旧档", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("新档", 1, now())).Error)
	seedUser(t, main, 1, "旧档")
	seedUser(t, main, 2, "旧档")
	seedUser(t, main, 3, "新档")
	// 一条已售订阅的降级目标指着旧档:不改的话它到期会把人降进一个不存在的分组,
	// 而到期扫描一个字都不校验目标是否存在。
	require.NoError(t, main.Create(&model.UserSubscription{
		Id: 1, UserId: 1, PlanId: 1, Status: "active", PrevUserGroup: "旧档",
	}).Error)

	seedOptionRatios(t, main,
		`{"旧档":{"池":0.3},"新档":{"池":0.8}}`, `{"旧档":0.5,"新档":0.9}`)

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/旧档",
		gin.Param{Key: "name", Value: "旧档"},
		`{"expect_users":2,"migrate_to":"新档"}`, adminDeleteUserGroup)
	require.Equalf(t, http.StatusOK, res.Code, "删除应当成功: %s", res.Body.String())

	counts := countByGroupCtx(context.Background(), "users")
	assert.EqualValues(t, 0, counts["旧档"], "旧档必须一个用户都不剩")
	assert.EqualValues(t, 3, counts["新档"], "两个人必须真的落到新档上")

	var sub model.UserSubscription
	require.NoError(t, main.First(&sub, 1).Error)
	assert.Equal(t, "新档", sub.PrevUserGroup,
		"已售订阅的回落目标必须跟着改 —— 不改的话它到期会把人降进一个不存在的分组")

	var left int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "旧档").Count(&left).Error)
	assert.EqualValues(t, 0, left, "登记行必须消失")
	assert.Equal(t, []string{"旧档→新档"}, *swept, "各模块的残留处置必须被调用一次")

	cross, err := loadCrossRatios()
	require.NoError(t, err)
	assert.NotContains(t, cross, "旧档", "GroupGroupRatio 的外层键必须删掉")
	assert.Equal(t, map[string]float64{"池": 0.8}, cross["新档"],
		"**目标分组的交叉倍率一个字节都不能变** —— 合并过去等于静默改掉另一档人的价")

	topup, err := loadTopupRatios()
	require.NoError(t, err)
	assert.NotContains(t, topup, "旧档", "TopupGroupRatio 的键必须删掉")
	assert.EqualValues(t, 0.9, topup["新档"], "目标分组的充值折扣不能被源分组覆盖")
}

// TestDeleteUserGroupBlockedWhileAPlanStillSellsIt 锁住套餐引用这道硬闸门。
//
// 一个卖着「升级到 X」的套餐,在 X 被删之后会把下一个买家放进一个不存在的分组 ——
// 他刚刚为"升级"付过钱,而系统按 fail-open 的 1.0 给他计费。正确的新值只有运营
// 知道(改成哪一档是商业决定),所以这里只拦不改。
func TestDeleteUserGroupBlockedWhileAPlanStillSellsIt(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})

	require.NoError(t, gdb.Create(newUserGroup("尊享", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("default", 1, now())).Error)
	require.NoError(t, main.Create(&model.SubscriptionPlan{
		Id: 7, Title: "年度尊享", UpgradeGroup: "尊享",
	}).Error)

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/尊享",
		gin.Param{Key: "name", Value: "尊享"}, `{"expect_users":0}`, adminDeleteUserGroup)

	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Contains(t, res.Body.String(), "#7 年度尊享")

	var left int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "尊享").Count(&left).Error)
	assert.EqualValues(t, 1, left)
}

// TestDeleteUserGroupRefusesToDeleteDefault 锁住那一档不可删。
//
// default 是 users.group 这一列的数据库默认值:删掉它之后,任何一次没有显式
// 指定分组的建号都会落进一个不存在的分组,而四条建号路径没有一条会报错。
func TestDeleteUserGroupRefusesToDeleteDefault(t *testing.T) {
	gdb := newTestDB(t)
	newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})
	require.NoError(t, gdb.Create(newUserGroup("default", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("其他", 1, now())).Error)

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/default",
		gin.Param{Key: "name", Value: "default"}, `{"expect_users":0}`, adminDeleteUserGroup)

	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Contains(t, res.Body.String(), "数据库默认值")
}

// TestDeleteUserGroupRejectsStaleImpact 锁住一致性闸门。
//
// 弹窗打开时是 0 个人、按下按钮时已经是 200 个人 —— 而运营是照着 0 这个数字
// 做的"直接删,不用迁"的决定。没有这道闸门,那 200 个人会当场变成孤儿。
func TestDeleteUserGroupRejectsStaleImpact(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})
	require.NoError(t, gdb.Create(newUserGroup("临时档", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("default", 1, now())).Error)
	seedUser(t, main, 1, "临时档")

	res := callGroupHandler(t, http.MethodDelete, "/user-groups/临时档",
		gin.Param{Key: "name", Value: "临时档"}, `{"expect_users":0}`, adminDeleteUserGroup)

	assert.Equal(t, http.StatusConflict, res.Code)
	assert.Contains(t, res.Body.String(), "qy_groupns_impact_drift")
}

// ─────────────────────────── 改名 ───────────────────────────

// TestRenameUserGroupCarriesUsersAndConfig 走一遍完整的改名。
//
// 改名与删除的关键差别:配置**跟着名字走**。倍率没跟过去的表现是这一档人从
// 交叉倍率(比如 0.3)静默回落到兜底倍率(1.0)—— 一次 3.3 倍的涨价,零告警。
func TestRenameUserGroupCarriesUsersAndConfig(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{"池": ""}, map[string]float64{"池": 1})
	swept := useFakeResidue(t, gdb)

	require.NoError(t, gdb.Create(newUserGroup("老名字", 1, now())).Error)
	seedUser(t, main, 1, "老名字")
	require.NoError(t, main.Create(&model.SubscriptionPlan{
		Id: 3, Title: "季卡", UpgradeGroup: "老名字",
	}).Error)
	seedOptionRatios(t, main, `{"老名字":{"池":0.3}}`, `{"老名字":0.5}`)

	res := callGroupHandler(t, http.MethodPost, "/user-groups/老名字/rename",
		gin.Param{Key: "name", Value: "老名字"},
		`{"new_name":"新名字","expect_users":1}`, adminRenameUserGroup)
	require.Equalf(t, http.StatusOK, res.Code, "改名应当成功: %s", res.Body.String())

	counts := countByGroupCtx(context.Background(), "users")
	assert.EqualValues(t, 0, counts["老名字"])
	assert.EqualValues(t, 1, counts["新名字"])

	var plan model.SubscriptionPlan
	require.NoError(t, main.First(&plan, 3).Error)
	assert.Equal(t, "新名字", plan.UpgradeGroup,
		"改名必须带上套餐的升级分组 —— 不带的话这个套餐从此把买家送进一个不存在的分组")

	cross, err := loadCrossRatios()
	require.NoError(t, err)
	assert.NotContains(t, cross, "老名字")
	assert.Equal(t, map[string]float64{"池": 0.3}, cross["新名字"],
		"交叉倍率必须跟着名字走,否则这一档人从 0.3 静默涨到兜底的 1.0")

	topup, err := loadTopupRatios()
	require.NoError(t, err)
	assert.NotContains(t, topup, "老名字")
	assert.EqualValues(t, 0.5, topup["新名字"])

	var oldLeft, newRow int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "老名字").Count(&oldLeft).Error)
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "新名字").Count(&newRow).Error)
	assert.EqualValues(t, 0, oldLeft)
	assert.EqualValues(t, 1, newRow)
	assert.Equal(t, []string{"老名字→新名字(rename)"}, *swept)
}

// TestRenameUserGroupBlockedByFrozenReference 锁住「改名不是删除的旁路」。
//
// block 这个处置的直觉是"删除专用",但对一份**冻结的、动不了的**引用来说
// 改名与删除完全等价:抽奖活动的参与名单在发布时进了 commit_hash,改完名字
// 之后那份名单指着一个再也没有人的名字,结果与被删掉逐字相同。
// 少了这道闸门,运营发现"删不掉"之后的下一个动作必然是"那我改个名",
// 而那是同一次事故换了个入口。
func TestRenameUserGroupBlockedByFrozenReference(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})
	require.NoError(t, gdb.Create(newUserGroup("老名字", 1, now())).Error)
	seedUser(t, main, 1, "老名字")

	ResetResiduesForTest()
	t.Cleanup(ResetResiduesForTest)
	RegisterResidue(ResidueHandler{
		Module: "frozen",
		Probe: func(*gorm.DB, string) ([]Residue, error) {
			return []Residue{{Module: "frozen", Table: "qy_frozen", Label: "冻结引用",
				Rows: 2, Disposition: ResidueBlock}}, nil
		},
		Sweep: func(*gorm.DB, string, string, bool) error {
			t.Error("被 block 拦下之后不应当再走清理")
			return nil
		},
	})

	res := callGroupHandler(t, http.MethodPost, "/user-groups/老名字/rename",
		gin.Param{Key: "name", Value: "老名字"},
		`{"new_name":"新名字","expect_users":1}`, adminRenameUserGroup)

	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Contains(t, res.Body.String(), "qy_groupns_rename_blocked")
	assert.Contains(t, res.Body.String(), "qy_frozen")

	// 用户与登记行都必须原地不动 —— 一次被拒绝的改名不许留下半成状态。
	assert.EqualValues(t, 1, countByGroupCtx(context.Background(), "users")["老名字"])
	var oldRow int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "老名字").Count(&oldRow).Error)
	assert.EqualValues(t, 1, oldRow)
}

// ─────────────────────────── 残留清理的原子性 ───────────────────────────

// TestSweepResiduesRollsBackAsAWhole 锁住"扩展库这一半是一个事务"。
//
// 两个模块各清各的,第二个失败时第一个必须回滚。做不到的表现是
// 「范围清了、费率还在」—— 一个删了一半的分组,而接口返回的是一句
// "请重试",重试时前一半已经不在了,运营没有任何办法知道现在是什么状态。
func TestSweepResiduesRollsBackAsAWhole(t *testing.T) {
	gdb := newTestDB(t)
	newMainDB(t)
	ResetResiduesForTest()
	t.Cleanup(ResetResiduesForTest)

	cleaned := false
	RegisterResidue(ResidueHandler{
		Module: "a_ok",
		Probe:  func(*gorm.DB, string) ([]Residue, error) { return nil, nil },
		Sweep: func(tx *gorm.DB, from, to string, rename bool) error {
			cleaned = true
			return tx.Where("name = ?", "陪葬品").Delete(&UserGroup{}).Error
		},
	})
	RegisterResidue(ResidueHandler{
		Module: "b_fails",
		Probe:  func(*gorm.DB, string) ([]Residue, error) { return nil, nil },
		Sweep: func(*gorm.DB, string, string, bool) error {
			return assert.AnError
		},
	})

	require.NoError(t, gdb.Create(newUserGroup("待删", 1, now())).Error)
	require.NoError(t, gdb.Create(newUserGroup("陪葬品", 1, now())).Error)

	_, err := rewriteUserGroup(gdb, "待删", "", false, 1)
	require.Error(t, err)
	assert.True(t, cleaned, "第一个模块的 Sweep 确实跑过")

	var survivors int64
	require.NoError(t, gdb.Model(&UserGroup{}).
		Where("name IN ?", []string{"待删", "陪葬品"}).Count(&survivors).Error)
	assert.EqualValues(t, 2, survivors,
		"任何一个模块的清理失败,整个扩展库事务都必须回滚 —— "+
			"「范围清了、费率还在」是一个没有人能修的中间态")
}

// ─────────────────────────── 新建 ───────────────────────────

// TestCreateUserGroupWarnsThatItCannotBeUsedYet 锁住那两条告警。
//
// 一个刚建出来的用户分组天然没有任何渠道:把人挪进来之后他们的**空分组令牌**
// 会走 UsingGroup = users.group,而 abilities 里没有这个键 ⇒ 503。
// 这件事必须在新建成功的那一刻就说出来,而不是等运营自己去撞。
func TestCreateUserGroupWarnsThatItCannotBeUsedYet(t *testing.T) {
	gdb := newTestDB(t)
	newMainDB(t)
	enableExtAPI(t, gdb)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{})

	res := callGroupHandler(t, http.MethodPost, "/user-groups", gin.Param{},
		`{"name":"内测档","note":"给内测用户"}`, adminCreateUserGroup)
	require.Equalf(t, http.StatusOK, res.Code, "新建应当成功: %s", res.Body.String())

	body := res.Body.String()
	assert.Contains(t, body, "一个模型分组都选不到")
	assert.Contains(t, body, "空分组令牌")

	var row UserGroup
	require.NoError(t, gdb.Where("name = ?", "内测档").Take(&row).Error)
	assert.Equal(t, DefaultModeInherit, row.DefaultMode,
		"新建出来的行必须是 inherit —— 新建本身零行为变化")
	assert.Equal(t, "给内测用户", row.Note)
	assert.True(t, row.Enabled)
}

// ─────────────────────────── 脚手架 ───────────────────────────

// useFakeResidue 把注册表整个换成一个只记录调用的假处置。
//
// 真处置(groupmatrix / commission / transfer / usergroup)各有自己的表,
// 在 groupns 的测试库里建不出来;而这里要断言的是**主干顺序**,不是那四个模块
// 各自的 SQL —— 那四份各有自己的用例。返回的切片指针里是 "from→to" 记录,
// 用来证明主干真的把处置调用了一次(而不是零次)。
func useFakeResidue(t *testing.T, gdb *gorm.DB) *[]string {
	t.Helper()
	calls := make([]string, 0, 2)
	ResetResiduesForTest()
	t.Cleanup(ResetResiduesForTest)
	RegisterResidue(ResidueHandler{
		Module: "fake",
		Probe: func(*gorm.DB, string) ([]Residue, error) {
			return []Residue{{Module: "fake", Table: "t", Label: "假表", Rows: 1,
				Disposition: ResidueClean}}, nil
		},
		Sweep: func(tx *gorm.DB, from, to string, rename bool) error {
			note := from + "→" + to
			if rename {
				note += "(rename)"
			}
			calls = append(calls, note)
			return nil
		},
	})
	_ = gdb
	return &calls
}

// seedOptionRatios 把两处以用户分组为键的 options 写进主库与内存快照。
//
// 必须两边都写:loadCrossRatios / loadTopupRatios 读的是 ratio_setting 与 common
// 的内存快照(热路径乘的就是那一份),而 updateOptionVerified 的回查读的是
// options 表本身 —— 只写一边的话,断言过的是一个生产上不存在的状态。
func seedOptionRatios(t *testing.T, main *gorm.DB, cross, topup string) {
	t.Helper()
	prevCross := ratio_setting.GroupGroupRatio2JSONString()
	prevTopup := common.TopupGroupRatio2JSONString()
	t.Cleanup(func() {
		_ = ratio_setting.UpdateGroupGroupRatioByJSONString(prevCross)
		_ = common.UpdateTopupGroupRatioByJSONString(prevTopup)
	})
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(cross))
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(topup))
	require.NoError(t, main.Create(&model.Option{Key: "GroupGroupRatio", Value: cross}).Error)
	require.NoError(t, main.Create(&model.Option{Key: "TopupGroupRatio", Value: topup}).Error)
}

// callGroupHandler 驱动一个 handler,返回 recorder。
//
// 审计表必须一起建:这四个 handler 全部写审计,建不出表时 audit.WriteConfigUpdate
// 会失败,而失败本身不会让 handler 报错 —— 于是断言过的是一条没有审计的路径。
func callGroupHandler(t *testing.T, method, path string, param gin.Param,
	body string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if param.Key != "" {
		c.Params = gin.Params{param}
	}
	c.Set("id", 1)
	h(c)
	return res
}
