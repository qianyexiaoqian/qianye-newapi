package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// qy_ratio_option_guard_test.go —— 倍率必须在**落库之前**被校验。
//
// UpdateOption 的顺序是「先 DB.Save,后 updateOptionMap」。校验若只挂在 controller
// 那一层,任何绕过它的写入(扩展模块的矩阵页、分组改名、批量写、以及原始
// /api/option 路)一旦带着非法值进来,坏值会先被持久化,而内存里的表在装载失败
// 时停在旧值 —— 库与内存分家,重启也不自愈。
//
// 负倍率是这里唯一会把「扣费」变成「给用户充值」的取值:饱和转换只在
// <= MinQuota 时才夹,区间内的负值原样返回。

// TestNegativeCrossRatioIsRejectedBeforeItReachesTheDatabase 钉住那条顺序。
func TestNegativeCrossRatioIsRejectedBeforeItReachesTheDatabase(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)

	before := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(before) })

	require.Error(t, UpdateOption("GroupGroupRatio", `{"vip":{"pool":-5}}`),
		"负交叉倍率必须被拒:ResolveGroupRatio 会原样返回 -5,预扣与结算双双为负")

	var option Option
	assert.ErrorIs(t, db.Where(&Option{Key: "GroupGroupRatio"}).First(&option).Error,
		gorm.ErrRecordNotFound,
		"被拒的值绝不能已经落库 —— 落库之后重启也不自愈")
	assert.EqualValues(t, 1, ratio_setting.ResolveGroupRatio("vip", "pool").Ratio,
		"内存里的表同样不得被这次写入污染")
}

// TestMalformedCrossRatioJSONIsRejectedBeforeItReachesTheDatabase —— 语法错误同理。
// 它此前的后果比负倍率更大:整张交叉倍率表被清空,全站每一笔回落兜底价,且零告警。
func TestMalformedCrossRatioJSONIsRejectedBeforeItReachesTheDatabase(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)

	before := ratio_setting.GroupGroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"vip":{"pool":0.1}}`))
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(before) })

	require.Error(t, UpdateOption("GroupGroupRatio", `{"vip":{"pool":0.1,`))

	var option Option
	assert.ErrorIs(t, db.Where(&Option{Key: "GroupGroupRatio"}).First(&option).Error,
		gorm.ErrRecordNotFound)
	assert.EqualValues(t, 0.1, ratio_setting.ResolveGroupRatio("vip", "pool").Ratio,
		"内存表被清空了 —— 全站谈好的价当场消失,而兜底价存在意味着一条告警都不会发")
}

// TestValidCrossRatioStillPersists 是对照组:闸门不能把正常保存一起挡掉。
func TestValidCrossRatioStillPersists(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	// 成功路径会走到 updateOptionMap,它直写 common.OptionMap(进程启动时才建)。
	prevMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = prevMap })

	before := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupGroupRatioByJSONString(before) })

	// 显式配成 0 的免费档必须能存进去:它与「没配」是两句话。
	require.NoError(t, UpdateOption("GroupGroupRatio", `{"vip":{"pool":0}}`))
	assert.Equal(t, `{"vip":{"pool":0}}`, requireOptionValue(t, db, "GroupGroupRatio"))
	assert.Zero(t, ratio_setting.ResolveGroupRatio("vip", "pool").Ratio)
	assert.Equal(t, ratio_setting.GroupRatioSourceOverride,
		ratio_setting.ResolveGroupRatio("vip", "pool").Source)
}
