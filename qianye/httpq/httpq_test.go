package httpq

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctx(query string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x?"+query, nil)
	return c
}

func ctxWithParam(key, value string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Params = gin.Params{{Key: key, Value: value}}
	return c
}

// PathInt64 是 violation / grouppricing / transfer / withdraw 四份
// `strconv.ParseInt(c.Param(key), 10, 64)` + `<= 0` 的收敛目标。
//
// 这张表逐条对齐那四份的可观察行为:它们判 false 的输入这里也必须判 false,
// 否则迁移过去会静默改变某个接口对畸形 ID 的响应码。
func TestPathInt64MatchesTheCopiesItReplaces(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int64
		ok    bool
	}{
		{"正常 ID", "42", 42, true},
		{"前导零", "007", 7, true},
		{"最大 int64", "9223372036854775807", 9223372036854775807, true},
		{"零不是合法 ID", "0", 0, false},
		{"负数", "-1", 0, false},
		{"正号", "+1", 0, false},
		{"空", "", 0, false},
		{"非数字", "12a", 0, false},
		{"带空格", " 12", 0, false},
		{"超过 MaxInt64", "9223372036854775808", 0, false},
		{"溢出攻击值仍在 int64 内,合法", "184467440737095518", 184467440737095518, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := PathInt64(ctxWithParam("id", tc.value), "id")
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("参数不存在", func(t *testing.T) {
		got, ok := PathInt64(ctxWithParam("other", "9"), "id")
		assert.False(t, ok)
		assert.Zero(t, got)
	})
}

// 扩展里实际用到的两套口径。收敛时逐个核对过七份拷贝的参数名与默认值,
// 差异只有这一处 —— 看板用 ?page=、默认 50、上限 200,其余全是 ?p=、20、100。
var (
	commonSpec = Spec{}
	boardSpec  = Spec{PageKey: "page", DefaultSize: 50, MaxSize: 200}
)

// Paginate 的返回值必须恒落在 [1, MaxPage] × [1, MaxSize] 内,
// 而且换算出的 offset 恒非负、恒有上界。
//
// 极端取值这一栏是本次收敛的起点:?p=184467440737095518 能被 strconv.Atoi
// 成功解析(它小于 MaxInt64),(page-1)*size 随后溢出成负数 ——
// 七份拷贝里有五份是这个状态,其中两份是资金模块的用户端只读接口。
func TestPaginateAlwaysReturnsBoundedValues(t *testing.T) {
	cases := []struct {
		name       string
		spec       Spec
		query      string
		wantPage   int
		wantSize   int
		wantOffset int
	}{
		{"缺省(通用口径)", commonSpec, "", 1, 20, 0},
		{"缺省(看板口径)", boardSpec, "", 1, 50, 0},
		{"正常翻页", commonSpec, "p=3&page_size=50", 3, 50, 100},
		{"看板的参数名是 page 而不是 p", boardSpec, "page=4&page_size=30", 4, 30, 90},
		{"看板不认 p", boardSpec, "p=4", 1, 50, 0},
		{"通用口径不认 page", commonSpec, "page=4", 1, 20, 0},
		{"页码为 0", commonSpec, "p=0", 1, 20, 0},
		{"页码为负", commonSpec, "p=-1", 1, 20, 0},
		{"页码非数字", commonSpec, "p=abc", 1, 20, 0},
		{"页码带正号也不认", commonSpec, "p=+5", 1, 20, 0},
		{"页长为 0 回落默认", commonSpec, "page_size=0", 1, 20, 0},
		{"页长为负回落默认", commonSpec, "page_size=-5", 1, 20, 0},
		{"页长越界回落默认", commonSpec, "page_size=100000", 1, 20, 0},
		{"页长恰好等于上限", commonSpec, "page_size=100", 1, 100, 0},
		{"看板页长上限是 200", boardSpec, "page_size=200", 1, 200, 0},
		{"看板页长 201 越界", boardSpec, "page_size=201", 1, 50, 0},
		{"深翻页被夹到 MaxPage", commonSpec, "p=999999&page_size=100", MaxPage, 100, (MaxPage - 1) * 100},
		{"页码恰好等于 MaxPage", commonSpec, "p=10000&page_size=20", MaxPage, 20, (MaxPage - 1) * 20},
		{"溢出攻击值:能被 Atoi 解析的大数", commonSpec, "p=184467440737095518&page_size=100", 1, 100, 0},
		{"溢出攻击值:超过 MaxInt64", commonSpec, "p=18446744073709551616", 1, 20, 0},
		{"页长也拿溢出值打", commonSpec, "p=2&page_size=184467440737095518", 2, 20, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, size := Paginate(ctx(tc.query), tc.spec)
			assert.Equal(t, tc.wantPage, page)
			assert.Equal(t, tc.wantSize, size)
			// 断言真正打给数据库的那个数,而不只是两个中间变量。
			assert.Equal(t, tc.wantOffset, Offset(page, size))

			maxSize := tc.spec.MaxSize
			if maxSize <= 0 {
				maxSize = 100
			}
			assert.GreaterOrEqual(t, page, 1)
			assert.LessOrEqual(t, page, MaxPage)
			assert.GreaterOrEqual(t, size, 1)
			assert.LessOrEqual(t, size, maxSize)
			assert.GreaterOrEqual(t, Offset(page, size), 0,
				"offset 为负会被 GORM 静默丢弃,结果是"+
					"深翻页悄悄退回第一页;更糟的调用点会直接 panic")
			assert.LessOrEqual(t, Offset(page, size), (MaxPage-1)*maxSize)
		})
	}
}

// Offset 是导出函数,会收到没有经过 Paginate 的裸整数。
func TestOffsetIsAlwaysNonNegativeAndBounded(t *testing.T) {
	cases := []struct {
		name       string
		page, size int
		want       int
	}{
		{"首页", 1, 20, 0},
		{"第二页", 2, 20, 20},
		{"页码为 0", 0, 20, 0},
		{"页码为负", -3, 20, 0},
		{"页长为 0", 5, 0, 0},
		{"页长为负", 5, -1, 0},
		{"页码超过 MaxPage 被夹住", 1 << 40, 20, (MaxPage - 1) * 20},
		{"能让 int64 相乘溢出的组合", 1 << 40, 1 << 40, MaxQueryInt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Offset(tc.page, tc.size)
			assert.Equal(t, tc.want, got)
			assert.GreaterOrEqual(t, got, 0)
		})
	}
}

// Slice 的防线必须贴着 items[start:end] 那一行:下标越界是 panic,不是错误返回。
func TestSliceNeverPanics(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	cases := []struct {
		name       string
		items      []string
		page, size int
		want       []string
	}{
		{"第一页", items, 1, 2, []string{"a", "b"}},
		{"中间页", items, 2, 2, []string{"c", "d"}},
		{"末页不足一页", items, 3, 2, []string{"e"}},
		{"页长大于总数", items, 1, 50, items},
		{"越过末页", items, 9, 2, []string{}},
		{"空清单", nil, 1, 50, []string{}},
		{"页码为 0", items, 0, 2, []string{}},
		{"负页码", items, -1, 2, []string{}},
		{"页长为 0", items, 1, 0, []string{}},
		// 下面两条是修复前必 panic 的路径:(page-1)*size 回绕成负数,
		// 而 `start >= len(items)` 对负数不成立,于是负下标直接切片。
		{"页码溢出乘法后为负", items, 184467440737095518, 50, []string{}},
		{"空清单 + 溢出页码", nil, 184467440737095518, 50, []string{}},
		{"页码溢出乘法后为正", items, 1 << 60, 50, []string{}},
		{"页长本身就是溢出值", items, 1, 1 << 62, items},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			require.NotPanics(t, func() { got = Slice(tc.items, tc.page, tc.size) })
			assert.Equal(t, tc.want, got)
		})
	}
}

// Int / Int64 的上界必须是解析的一部分。
//
// 上界收得太紧同样是缺陷:这两个函数被 user_id 与 Unix 时间戳复用,
// 回落默认值 0 会让调用点的 `if v > 0` 不成立,WHERE 根本不拼上去 ——
// 筛选条件静默失效,接口返回未经筛选的全表。被收敛掉的 controller 那份
// intQuery 上界是 100 万,资金单与审计日志的时间范围筛选一直是死的。
func TestIntAndInt64Bounds(t *testing.T) {
	t.Run("Int", func(t *testing.T) {
		cases := []struct {
			name  string
			query string
			want  int
		}{
			{"缺省", "", 7},
			{"正常取值", "v=42", 42},
			{"前导零", "v=0042", 42},
			{"零", "v=0", 0},
			{"负数回落", "v=-1", 7},
			{"非数字回落", "v=12a", 7},
			{"空格回落", "v=%201", 7},
			{"真实用户 ID(百万级)必须通得过", "v=1000001", 1000001},
			{"真实 Unix 时间戳必须通得过", "v=1753900000", 1753900000},
			{"恰好等于 MaxQueryInt", "v=2147483647", MaxQueryInt},
			{"超过 MaxQueryInt 回落", "v=2147483648", 7},
			{"溢出攻击值回落", "v=184467440737095518", 7},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, Int(ctx(tc.query), "v", 7))
			})
		}
	})

	t.Run("Int64", func(t *testing.T) {
		cases := []struct {
			name  string
			query string
			want  int64
		}{
			{"缺省", "", 9},
			{"真实 Unix 时间戳", "v=1753900000", 1753900000},
			{"远超 int32 仍然合法", "v=184467440737095518", 184467440737095518},
			{"恰好 MaxInt64", "v=9223372036854775807", 9223372036854775807},
			{"超过 MaxInt64 回落", "v=9223372036854775808", 9},
			{"负数回落", "v=-1", 9},
			{"非数字回落", "v=x", 9},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, Int64(ctx(tc.query), "v", 9))
			})
		}
	})
}
