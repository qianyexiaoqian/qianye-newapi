package groupmatrix

import (
	"testing"

	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plan_unlock_test.go —— 读侧与写侧对「套餐解锁的模型分组」必须说同一句话。
//
// ══════════════════════ 这条守的是什么 ══════════════════════
//
// 权威可选清单是 **per-group** 的(qy_group_grants:用户分组 → 模型分组),
// 而套餐解锁是 **per-user** 的(用户买了什么)。两者住在不同的表里,唯一的汇合
// 点是 service.QyPlanUnlockedGroup 这个 hook。
//
// 少了这一条判定,一个设了范围的用户分组里的用户买了解锁 G 的套餐之后会遇到:
//
//	读侧(QyUsableGroupsForUser 并入解锁)  →  G 出现在下拉里、请求也放行
//	写侧(CheckTokenGroup 只看 grants)     →  保存令牌时被「不能使用模型分组 G」挡下
//
// 「能用却存不下」是本仓已经修过一次的形状,不要再造一次。
//
// playground 那条同理:它走 UserAuth 而不是 TokenAuth,判定另起一条链路,
// 漏掉解锁的表现是"同一个人在两个入口得到两种答案"。

// withPlanUnlock 临时把某个模型分组装成"该用户的套餐解锁的"。
func withPlanUnlock(t *testing.T, userId int, group string) {
	t.Helper()
	prev := service.QyPlanUnlockedGroup
	service.QyPlanUnlockedGroup = func(uid int, name string) bool {
		return uid == userId && name == group
	}
	t.Cleanup(func() { service.QyPlanUnlockedGroup = prev })
}

func TestWriteGuardAcceptsPlanUnlockedGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]float64{"default": 1, "vip": 1, "pro": 0.8})

	// vip 分组被接管,清单里只有 default —— 按 grants 判定,pro 是要被拒的。
	seedScope(t, gdb, "vip", ModeEnforce, false, "default")
	require.NoError(t, reload())

	c, _ := gin.CreateTestContext(nil)
	c.Set("id", 7)
	prevOwner := ownerUserGroup
	ownerUserGroup = func(_ *gin.Context) (string, bool) { return "vip", true }
	t.Cleanup(func() { ownerUserGroup = prevOwner })

	require.Error(t, CheckTokenGroup(c, "", "pro"),
		"前置条件:没有套餐解锁时这一次写入本来就该被拒,否则下面的断言证明不了任何东西")

	withPlanUnlock(t, 7, "pro")
	assert.NoError(t, CheckTokenGroup(c, "", "pro"),
		"套餐解锁的分组在写入侧必须放行 —— 读侧已经放行了,两侧口径不许分叉")
	assert.Error(t, CheckTokenGroup(c, "", "别的分组"),
		"解锁只放行它自己那一个分组,不是把整道闸门打开")
}

func TestPlaygroundAllowsPlanUnlockedGroup(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]float64{"default": 1, "vip": 1, "pro": 0.8})
	seedScope(t, gdb, "vip", ModeEnforce, false, "default")
	require.NoError(t, reload())

	c, _ := gin.CreateTestContext(nil)
	c.Set("id", 7)
	prevOwner := ownerUserGroup
	ownerUserGroup = func(_ *gin.Context) (string, bool) { return "vip", true }
	t.Cleanup(func() { ownerUserGroup = prevOwner })

	require.False(t, PlaygroundGroupAllowed(c, "", "pro"),
		"前置条件:没有套餐解锁时 playground 本来就该拒绝")

	withPlanUnlock(t, 7, "pro")
	assert.True(t, PlaygroundGroupAllowed(c, "", "pro"),
		"买了套餐的用户在 playground 里也必须能用他付过钱的分组")
}
