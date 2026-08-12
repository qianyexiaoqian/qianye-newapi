package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// option_pricing_validate_test.go —— 定价表落库前必须先证明它装载得进内存。
//
// # 守的是哪一条不变量
//
// UpdateOption 的顺序是「先 DB.Save,后 updateOptionMap」。所以对任何**会装载
// 失败**的键来说,一个坏值的后果不是"保存失败",而是:
//
//	库里已经是坏值 → 内存里那张表停在旧值 → 重启后装载再失败一次 → 永不自愈。
//
// 而管理端界面读的是库,它会把那个坏值显示成「已经生效的配置」。对定价表来说,
// 这意味着运营看到的价格与实际计费用的价格可以长期不是同一份。
//
// 这不是假想缺陷:演示库里 AudioCompletionRatio 的值就是字符串 `<nil>`,
// SyncOptions 每 60 秒打一次 "failed to update option map: invalid character '<'",
// 而音频补全倍率从那一刻起再没装载进来过。
func TestPricingTableRejectsAnythingItCannotLoad(t *testing.T) {
	// updateOptionMap 第一行就往 common.OptionMap 里写,而那个 map 的零值是 nil
	// (真实进程里由 InitOptionMap 建好)。夹具显式建它,并在结束时还原 ——
	// 本用例会把坏值喂进装载端,不还原会把它留给同包的其它用例。
	common.OptionMapRWMutex.Lock()
	prevOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = prevOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	// 这一组的共同点不是"名字里有 Ratio",而是 updateOptionMap 把它们交给
	// LoadFromJsonString —— 也就是**装载会失败**。名字里同样有 Ratio 的
	// GroupRatio / GroupGroupRatio 走各自的业务校验(带非负等约束),不在这张表里。
	pricingKeys := []string{
		"ModelRatio", "CompletionRatio", "ModelPrice", "CacheRatio",
		"CreateCacheRatio", "ImageRatio", "AudioRatio", "AudioCompletionRatio",
	}

	bad := []struct {
		name  string
		value string
		why   string
	}{
		{
			name: "线上实际出现的那个值", value: "<nil>",
			why: "某条绕过 controller 的写入把一个 Go 零值指针格式化成了字符串落库",
		},
		{
			name: "空串", value: "",
			why: "清空一张定价表的正确写法是 {},空串装载不了 —— 允许它等于允许一次分家",
		},
		{
			name: "根本不是 JSON", value: "not json",
			why: "远端 ratio_sync 推来一段 HTML 错误页时就是这个形状",
		},
		{
			name: "是合法 JSON 但不是这个形状(数组)", value: "[]",
			why: "只判 json.Valid 的闸会放行它,而它装载不进 map —— 这条区分松闸与真闸",
		},
		{
			name: "是合法 JSON 但倍率是字符串", value: `{"gpt-4o":"2"}`,
			why: "前端把数字当字符串提交是最常见的一种坏值,同样装载不了",
		},
		{
			name: "是合法 JSON 但值是对象", value: `{"gpt-4o":{"in":1}}`,
			why: "把嵌套结构误填进扁平定价表,装载不了",
		},
	}

	for _, key := range pricingKeys {
		for _, tc := range bad {
			t.Run(fmt.Sprintf("%s/%s", key, tc.name), func(t *testing.T) {
				// ① 装载端确实吃不下它 —— 这一条是"必须挡"的**理由**,
				//    没有它,下面那条断言只是在描述一个任意的口味。
				require.Error(t, updateOptionMap(key, tc.value),
					"装载端本来就吃不下这个值(%s),否则本用例的前提不成立", tc.why)
				// ② 所以落库前的闸必须挡住它。
				assert.Error(t, validateOptionValue(key, tc.value),
					"这个值会被持久化但永远装载不进内存,库与内存就此分家:%s", tc.why)
			})
		}

		t.Run(key+"/正常的定价表照常放行", func(t *testing.T) {
			assert.NoError(t, validateOptionValue(key, `{"gpt-4o":2,"gpt-4o-mini":0.15}`),
				"闸只该挡装载不了的值;挡住正常取值会让运营改不了价")
			assert.NoError(t, validateOptionValue(key, `{}`),
				"清空这张表的正确写法就是 {},必须放行")
		})
	}
}
