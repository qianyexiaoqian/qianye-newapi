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

// TestUnsafeTopupGroupRatioIsRejectedBeforeItReachesTheDatabase 把同一道闸门
// 补到**充值倍率**上。
//
// 扩展侧的 PUT /group-namespace/user-groups/:name 已经有一道 validateTopupRatio,
// 但那不是这个值唯一的入口:通用的 /api/option 与系统设置里的 JSON 抽屉可以直接
// 提交整份 TopupGroupRatio,而 validateOptionValue 此前对这个键一条 case 都没有。
// 前端把抽屉改成只读并不构成闸门 —— 端点仍然收。
//
// 三个取值各自的后果:
//
//	负数  controller/topup.go 的 payMoney 变成负的,一张负价订单
//	0     四条支付路径 `if ratio == 0 { ratio = 1 }` 把它抬回 1 ——
//	      配置值与收款值分家,而这个偏差不出现在任何告警里
//	超大  金额被推过 decimal → float 的可用范围,网关收到的数没有意义
func TestUnsafeTopupGroupRatioIsRejectedBeforeItReachesTheDatabase(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	// 成功路径会走到 updateOptionMap,它直写 common.OptionMap(进程启动时才建)。
	prevMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = prevMap })

	before := common.TopupGroupRatio2JSONString()
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"vip":0.9}`))
	t.Cleanup(func() { _ = common.UpdateTopupGroupRatioByJSONString(before) })

	for name, value := range map[string]string{
		"负倍率":    `{"vip":-5}`,
		"零倍率":    `{"vip":0}`,
		"超过上限":   `{"vip":100000}`,
		"坏 JSON": `{"vip":`,
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, UpdateOption("TopupGroupRatio", value))

			var option Option
			assert.ErrorIs(t, db.Where(&Option{Key: "TopupGroupRatio"}).First(&option).Error,
				gorm.ErrRecordNotFound, "被拒的值绝不能已经落库")
			assert.EqualValues(t, 0.9, common.GetTopupGroupRatio("vip"),
				"内存里的表同样不得被这次写入污染")
		})
	}

	require.NoError(t, UpdateOption("TopupGroupRatio", `{"vip":0.8}`),
		"闸门不能把正常保存一起挡掉")
	assert.EqualValues(t, 0.8, common.GetTopupGroupRatio("vip"))
}
