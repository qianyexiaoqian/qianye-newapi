package service

// qy_group_usable_test.go —— GetUserUsableGroups 瘦身之后的两条不变量。
//
// 两条都直接决定「谁能选到哪个模型分组」与「用户看到的那段说明是哪一份」,
// 而两者写错都**不会报错**:前者的表现是一批令牌 403 或一次分组泄漏,
// 后者的表现是同一个分组在两个页面上显示两段不同的文案。

import (
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useGroupState 装载全局白名单与分组倍率表,并在用例结束后还原。
//
// 两份一起装:自我补入收窄之后的判据同时看这两处,只装其中一份的用例会在
// 判据被改写时照样全绿。
func useGroupState(t *testing.T, usableJSON, groupRatioJSON string) {
	t.Helper()
	prevUsable := setting.UserUsableGroups2JSONString()
	prevRatio := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(usableJSON))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(groupRatioJSON))
	t.Cleanup(func() {
		_ = setting.UpdateUserUsableGroupsByJSONString(prevUsable)
		_ = ratio_setting.UpdateGroupRatioByJSONString(prevRatio)
	})
}

// TestGetUserUsableGroupsSelfInsertRequiresGroupRatio 钉住自我补入的**收窄判据**。
//
// 上游这一步是**无条件**的:不管这个名字是什么,只要不在白名单里就补一条。
// 收窄成"必须在分组倍率表里"之后:
//
//	在倍率表里   仍然补(那 5 个 legacy_dual 名字靠它拿到可选性,删掉即整档 403)
//	不在倍率表里 不补(补了也够不到任何东西 —— middleware/auth.go 紧接着还要过
//	             ContainsGroupRatio,controller.GetUserGroups 只遍历 GroupRatio 的键)
//
// 这条用例是这次收窄唯一会变红的地方:两个方向各写反一次,后果分别是
// 「一整档人选不了自己的分组」与「上游那段大杂烩原样留着」。
func TestGetUserUsableGroupsSelfInsertRequiresGroupRatio(t *testing.T) {
	useGroupState(t,
		`{"default":"默认分组"}`,
		`{"default":1,"既是用户分组也是模型分组":0.5}`)

	assert.Equal(t, map[string]string{
		"default":      "默认分组",
		"既是用户分组也是模型分组": SelfUsableGroupDescription,
	}, GetUserUsableGroups("既是用户分组也是模型分组"),
		"用户分组同时是一个配了倍率的模型分组时,必须把自己补进可选清单")

	assert.Equal(t, map[string]string{"default": "默认分组"},
		GetUserUsableGroups("只是一档人不是池子"),
		"不在分组倍率表里的用户分组不再被补进自己的清单 —— "+
			"补了也够不到任何东西,而「无条件」正是上游那段大杂烩的最后一块")

	assert.Equal(t, map[string]string{"default": "默认分组"},
		GetUserUsableGroups(""),
		"匿名口径必须原样返回全局白名单:模型广场、可用率、价格表的匿名基准都靠它")
}

// TestGetUserUsableGroupsAppliesModelGroupNote 钉住备注与白名单说明的**优先级**。
//
// 项目方原话:「模型分组,你这里要加入分组备注,让用户选择的时候显示出来分组备注
// 内容。」而 options.UserUsableGroups 的 value 本来就是给用户看的说明文案,
// 本站已有真实内容。两份文案并存而没人知道哪份生效是新的混乱源,所以关系钉死成
// **覆盖**:备注非空即生效,备注为空逐位回落。
//
// 断言必须同时覆盖两个方向。只测"备注生效"的话,把回落写成"返回分组名"照样全绿,
// 而那会让每一个没写备注的分组的说明都变成它自己的名字。
func TestGetUserUsableGroupsAppliesModelGroupNote(t *testing.T) {
	useGroupState(t,
		`{"有备注的":"白名单里的原文","没备注的":"这一段要原样保留"}`,
		`{"有备注的":1,"没备注的":1}`)

	prev := setting.QyGroupNote
	setting.QyGroupNote = func(name string) string {
		if name == "有备注的" {
			return "模型分组备注:本站不留存任何数据"
		}
		return ""
	}
	t.Cleanup(func() { setting.QyGroupNote = prev })

	assert.Equal(t, map[string]string{
		"有备注的": "模型分组备注:本站不留存任何数据",
		"没备注的": "这一段要原样保留",
	}, GetUserUsableGroups(""),
		"备注非空时覆盖白名单说明,备注为空时逐位回落 —— 两份文案只有一份生效")

	// 单个分组的说明入口必须与整张清单同源:它服务的是"分组只由权威清单/套餐解锁
	// 拿到"的那条路径,给出不同答案会让同一个分组在两个页面上显示两段说明。
	assert.Equal(t, "模型分组备注:本站不留存任何数据",
		setting.GetUsableGroupDescription("有备注的"))
	assert.Equal(t, "这一段要原样保留",
		setting.GetUsableGroupDescription("没备注的"))
	assert.Equal(t, "白名单里根本没有它",
		setting.GetUsableGroupDescription("白名单里根本没有它"),
		"两处都没有时回落分组名,与上游一致")
}

// TestQyGroupNoteDefaultIsInert 证明扩展未启用时这条接缝是空操作。
//
// 默认实现返回空串 ⇒ 说明文案逐位等于上游。这是"整个 qianye 目录删掉也不影响
// 上游行为"这条可卸载性承诺在本接缝上的具体形式,而它只需要一行断言就能锁住。
func TestQyGroupNoteDefaultIsInert(t *testing.T) {
	assert.Equal(t, "上游原文", setting.QyDescribeGroup("任意分组", "上游原文"))
}
