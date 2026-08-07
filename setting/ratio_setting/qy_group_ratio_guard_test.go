package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qy_group_ratio_guard_test.go —— 两张倍率表的**写入闸门**与**装载原子性**。
//
// 两条不变量各自对应一次会真金白银出事的路径:
//
//  1. 负倍率一路到底。common/quota_math.go 的饱和转换只在 <= MinQuota 时才夹,
//     区间内的负值原样返回 —— 预扣是负的、结算是负的,等于给用户充值。
//     GroupRatio 一直有 CheckGroupRatio 挡着,GroupGroupRatio 曾经一条校验都没有,
//     而它才是唯一「按用户分组定价」的载体。
//  2. 一次格式非法的写入把整张交叉倍率表清空。清空之后每一笔回落
//     GroupRatio[模型分组] 的兜底价(通常贵得多),而因为兜底价存在 ⇒
//     BaseMissing=false ⇒ SilentFallback() 为假 ⇒ **一条告警都不发**。

// TestCheckGroupGroupRatioRejectsRatiosThatWouldCreditTheUser 钉住写入闸门。
func TestCheckGroupGroupRatioRejectsRatiosThatWouldCreditTheUser(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "正常交叉倍率", payload: `{"vip":{"pool":0.2}}`},
		{name: "显式免费的 0 必须放行", payload: `{"vip":{"pool":0}}`},
		{name: "空表", payload: `{}`},
		{name: "负倍率 —— 扣费会变成给用户充值", payload: `{"vip":{"pool":-5}}`, wantErr: true},
		{name: "越界的巨大倍率", payload: `{"vip":{"pool":1000001}}`, wantErr: true},
		{name: "被截断的 JSON", payload: `{"vip":{"pool":0.1,`, wantErr: true},
		{name: "内层不是对象", payload: `{"vip":0.1}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckGroupGroupRatio(tc.payload)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestCheckGroupRatioRejectsTheSameShapes —— 两张表的判据必须同源。
// 一张严一张松的话,运营会发现"同一个数字在这一页能填、在那一页不能"。
func TestCheckGroupRatioRejectsTheSameShapes(t *testing.T) {
	require.NoError(t, CheckGroupRatio(`{"pool":0}`))
	require.NoError(t, CheckGroupRatio(`{"pool":2}`))
	require.Error(t, CheckGroupRatio(`{"pool":-1}`))
	require.Error(t, CheckGroupRatio(`{"pool":1000001}`))
}

// TestMalformedPayloadNeverWipesTheCrossRatioTable —— 装载必须是「解析成功才整体
// 换上去」,而不是「先清空再解析」。
//
// 后者会把一次 JSON 语法错误放大成一次全站定价事故:上游 UpdateOption 是
// 「先 DB.Save,后 updateOptionMap」,坏值先落库、内存后被清空,重启也不自愈。
func TestMalformedPayloadNeverWipesTheCrossRatioTable(t *testing.T) {
	loadRatioTables(t, `{"pool":2}`, `{"vip":{"pool":0.1},"svip":{"pool":0.05}}`)
	require.EqualValues(t, 0.1, ResolveGroupRatio("vip", "pool").Ratio)

	require.Error(t, UpdateGroupGroupRatioByJSONString(`{"vip":{"pool":0.1,`))

	assert.EqualValues(t, 0.1, ResolveGroupRatio("vip", "pool").Ratio,
		"一次语法错误把整张交叉倍率表清空了 —— 全站谈好的价当场消失,而且零告警")
	assert.EqualValues(t, 0.05, ResolveGroupRatio("svip", "pool").Ratio)
	assert.NotEqual(t, "{}", GroupGroupRatio2JSONString())
}

// TestMalformedPayloadNeverWipesTheBaseRatioTable —— 兜底倍率表同一条不变量。
func TestMalformedPayloadNeverWipesTheBaseRatioTable(t *testing.T) {
	loadRatioTables(t, `{"pool":2,"cheap":0.5}`, `{}`)

	require.Error(t, UpdateGroupRatioByJSONString(`{"pool":2,`))

	assert.EqualValues(t, 2, ResolveGroupRatio("anyone", "pool").Ratio)
	assert.EqualValues(t, 0.5, ResolveGroupRatio("anyone", "cheap").Ratio)
}
