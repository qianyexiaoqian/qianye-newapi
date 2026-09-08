package groupns

// modelgroup_list_test.go —— 「模型分组」那一张表的读接口。
//
// 这一组用例守的是**行集合与切页必须在同一侧**这一条。
//
// 行集合是并集(登记表 ∪ options.GroupRatio 的键 ∪ 全局可选清单的键),而并集
// 此前是前端算的。服务端一旦切页,那个位置就结构性地错了:服务端给出第 2 页的
// 10 行,前端再并上 options 里的全部键 —— 第 2 页会渲染成「10 行 + 全站其余
// 所有名字」。所以并集搬到了服务端,而这里守它没有在搬家途中丢掉任何一个来源。
//
// 另外两件事各有一个具体的错误方向:
//
//	registered   它是**联动删除按钮的闸门**。并集之后每一行都在 items 里,
//	             前端再也不能靠"它在不在数组里"推 —— 推错的方向是给一个没有
//	             登记行的名字亮起联动删除,而那条路径只会从 options 里少一个键,
//	             abilities / channels / tokens / 两张授权表里的引用一个都不动。
//	不带参数     同一个响应形状还养着不翻页的调用方。默认翻页会让它们静默只
//	             看到前 10 行。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listModelGroups 驱动 adminListModelGroups 并解出 data。
func listModelGroups(t *testing.T, query string) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/model-groups"+query, nil)
	c.Set("id", 1)
	adminListModelGroups(c)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())

	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success)
	return body.Data
}

func listedNames(t *testing.T, data map[string]any) []string {
	t.Helper()
	items, ok := data["items"].([]any)
	require.True(t, ok, "items 必须恒为数组:JSON null 会让前端对着它调 .map 白屏")
	out := make([]string, 0, len(items))
	for _, raw := range items {
		row, ok := raw.(map[string]any)
		require.True(t, ok)
		out = append(out, row["name"].(string))
	}
	return out
}

func listedRow(t *testing.T, data map[string]any, name string) map[string]any {
	t.Helper()
	items, _ := data["items"].([]any)
	for _, raw := range items {
		row := raw.(map[string]any)
		if row["name"] == name {
			return row
		}
	}
	t.Fatalf("行 %q 不在返回里:%v", name, listedNames(t, data))
	return nil
}

// TestModelGroupListUnionsEverySourceAndFlagsRegistration 守并集的三个来源与
// registered 闸门。
func TestModelGroupListUnionsEverySourceAndFlagsRegistration(t *testing.T) {
	gdb := newTestDB(t)
	enableExtAPI(t, gdb)
	// ghost 只在全局可选清单里:没有兜底倍率、没有登记行。它是资金泄漏集合里最
	// 容易造出来的一种 —— 用户选得到它,每一次请求按 GetGroupRatio fail-open 的
	// 1.0 计费。不列出来就永远没人处理。
	useUpstreamGroups(t,
		map[string]string{"ghost": "白名单原文"},
		map[string]float64{"paid": 0.5})
	require.NoError(t, gdb.Create(&ModelGroup{
		Name: "registered_only", DisplayName: "只登记过", Note: "备注", Enabled: true,
	}).Error)

	data := listModelGroups(t, "")
	assert.Equal(t, []string{"ghost", "paid", "registered_only"}, listedNames(t, data),
		"三个来源缺一不可,且按名字升序 —— 排序是切页的前提,两侧不一致会让"+
			"相邻两页重复或漏掉行")

	ghost := listedRow(t, data, "ghost")
	assert.Equal(t, false, ghost["registered"],
		"没有登记行的名字必须显式标出来:联动删除按钮以它为闸门")
	assert.Equal(t, true, ghost["ratio_missing"])
	assert.Equal(t, true, ghost["in_usable_groups"])
	assert.Equal(t, "白名单原文", ghost["usable_description"])
	assert.Equal(t, "", ghost["display_name"],
		"并集补进来的行除了名字之外全是零值 —— 替它编一个默认值会让「没登记」"+
			"在界面上长得和「登记过」一模一样")
	assert.Equal(t, false, ghost["enabled"])

	assert.Equal(t, true, listedRow(t, data, "registered_only")["registered"])
	assert.Equal(t, "备注", listedRow(t, data, "registered_only")["note"])

	assert.NotContains(t, data, "total",
		"不带翻页参数时不下发游标 —— 调用方据此判断要不要画翻页条")
	assert.NotContains(t, data, "names")
}

// TestModelGroupListPagesOverTheUnion 守切页本身,以及只有全表才答得出的那两份
// 附加数据(全量行名、可选却没有渠道的名单)。
func TestModelGroupListPagesOverTheUnion(t *testing.T) {
	enableExtAPI(t, newTestDB(t))
	// 五个名字全部来自 options,一个登记行都没有 —— 切页必须发生在并集之后,
	// 只按登记表切会给出一张空表。
	useUpstreamGroups(t,
		map[string]string{"g2": "", "g5": ""},
		map[string]float64{"g1": 1, "g2": 1, "g3": 1, "g4": 1, "g5": 1})

	data := listModelGroups(t, "?p=2&page_size=2")
	assert.Equal(t, []string{"g3", "g4"}, listedNames(t, data))
	assert.Equal(t, float64(5), data["total"], "总数是全量,不是本页行数")
	assert.Equal(t, float64(2), data["p"])
	assert.Equal(t, float64(2), data["page_size"])

	names, ok := data["names"].([]any)
	require.True(t, ok, "全量行名必须随分页一起下发:auto 顺序的候选清单与新建时的"+
		"重名判定都只有全表才成立 —— 只看本页的话,在第 2 页新建一个与第 1 页同名的"+
		"分组不会报重名,保存时它会静默覆盖那一行的兜底倍率")
	assert.Len(t, names, 5)

	// 「用户可选、却一个启用渠道都没有」是**故障预警**:那种分组在令牌下拉里
	// 长得和正常的一模一样,选中之后每一次请求都 503。主库没接上时
	// RoutedModelGroupNames 返回空 map,于是可选的两个都算没有渠道。
	noChannel, ok := data["no_channel_names"].([]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []any{"g2", "g5"}, noChannel,
		"名单必须是全表口径 —— 一条随翻页出现又消失的故障预警,读到的人只会认为它不可靠")

	// 越界页码给空表而不是 panic,而总数仍然如实上报 —— 前端据此把页码回落。
	beyond := listModelGroups(t, "?p=99&page_size=2")
	assert.Empty(t, listedNames(t, beyond))
	assert.Equal(t, float64(5), beyond["total"])
}
