package availability

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func ctxWithQuery(query string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/availability/matrix?"+query, nil)
	return c
}

// ─────────────────── 从 getMatrix 驱动的分页断言 ───────────────────
//
// 下面这组用例的被测对象是 **handler 的接线**,不是 httpq 本身。
// httpq 的取值口径由 qianye/httpq/httpq_test.go 覆盖;这里要回答的是另一个问题:
// getMatrix 到底有没有把分页交给 httpq。
//
// 这个区分不是学术的。收敛前 availability 有一份自己的
// `strconv.Atoi(c.DefaultQuery("page","1"))` + `start := (page-1)*pageSize`,
// 而当时的测试全部直接调用解析函数与切片函数 —— 把 handler 换回那份本地实现,
// 测试照样全绿。真正会 panic 的是 handler,所以断言必须从 handler 进。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

// 配置快照的 linkname 变量 qyConfig 在 sample_test.go,本包共用一份。

// matrixModels 是灌进库里的模型名,已按字典序排好 —— getMatrix 的 modelNames
// 也按字典序返回,所以期望的页内切片可以直接用下标表达。
var matrixModels = []string{"m01", "m02", "m03", "m04", "m05", "m06", "m07"}

// newMatrixEnv 搭起 getMatrix 需要的全部外部状态:扩展库、配置开关、
// 以及 groupOfferedModels 的渠道缓存(它底下是主库的 abilities 表,
// 测试里没有主库,必须预置,否则 model.DB 为 nil 直接 panic)。
func newMatrixEnv(t *testing.T) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Bucket{}))

	now := common.GetTimestamp()
	ts := alignBucket(now-600, bucketSeconds())
	for _, name := range matrixModels {
		require.NoError(t, gdb.Create(&Bucket{
			BucketTs: ts, GroupName: "default", ModelName: name,
			ReqTotal: 10, SuccessCount: 9, FailUpstream: 1, UpdatedAt: now,
		}).Error)
	}

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:      true,
		Availability: config.Availability{Enabled: true},
	})

	offered := map[string]struct{}{}
	for _, name := range matrixModels {
		offered[name] = struct{}{}
	}
	channelModelMu.Lock()
	channelModelCache["default"] = channelModelEntry{models: offered, expireAt: time.Now().Add(time.Hour)}
	channelModelMu.Unlock()

	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		channelModelMu.Lock()
		delete(channelModelCache, "default")
		channelModelMu.Unlock()
		_ = sqlDB.Close()
	})
}

type matrixBody struct {
	Success bool `json:"success"`
	Data    struct {
		Models      []string `json:"models"`
		TotalModels int      `json:"total_models"`
		Page        int      `json:"page"`
		PageSize    int      `json:"page_size"`
	} `json:"data"`
}

func callGetMatrix(t *testing.T, query string) (*httptest.ResponseRecorder, matrixBody) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/availability/matrix?groups=default&"+query, nil)
	c.Set("id", 1)
	c.Set("group", "default")

	// 刻意不套 gin.Recovery():handler 里的 panic 必须原样冒出来变成测试失败。
	// 线上那次 500 就是 names[负数:…] 的 panic,把它吞掉这条测试就白写了。
	getMatrix(c)

	var body matrixBody
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	return res, body
}

// getMatrix 必须把分页交给 httpq —— 包括那条会让手写切片 panic 的页码。
//
// 断言看的是响应里的 models/page/page_size,也就是 httpq.Slice 的产物;
// 只要 handler 退回本地 (page-1)*pageSize,溢出页码那一条会直接 panic,
// 其余几条会在 page/page_size 的口径上对不上。
func TestGetMatrixPagesThroughSharedHelper(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantPage   int
		wantSize   int
		wantModels []string
	}{
		{"缺省", "", 1, defaultPageSize, matrixModels},
		{"第一页", "page=1&page_size=3", 1, 3, matrixModels[0:3]},
		{"第二页", "page=2&page_size=3", 2, 3, matrixModels[3:6]},
		{"末页不足一页", "page=3&page_size=3", 3, 3, matrixModels[6:7]},
		{"越过末页返回空", "page=4&page_size=3", 4, 3, []string{}},
		{"看板认 page 不认 p", "p=2&page_size=3", 1, 3, matrixModels[0:3]},
		{"页码为 0", "page=0&page_size=3", 1, 3, matrixModels[0:3]},
		{"页码为负", "page=-2&page_size=3", 1, 3, matrixModels[0:3]},
		{"页长越界回落默认", "page=1&page_size=99999", 1, defaultPageSize, matrixModels},
		{"页长上限是 200", "page=1&page_size=200", 1, maxPageSize, matrixModels},
		// 这一条是整组用例的理由:收敛前的本地实现在这里 panic。
		// (page-1)*50 回绕成 -9223372036854775766,而 `start >= len(names)`
		// 对负数不成立,names[负数:…] 直接崩。
		{"溢出页码必须回落且不得 panic", "page=184467440737095518", 1, defaultPageSize, matrixModels},
		{"超过 MaxInt64 的页码", "page=18446744073709551616", 1, defaultPageSize, matrixModels},
		{"深翻页被夹到 MaxPage", "page=999999&page_size=3", httpq.MaxPage, 3, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newMatrixEnv(t)
			res, body := callGetMatrix(t, tc.query)
			require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
			assert.True(t, body.Success)
			assert.Equal(t, tc.wantPage, body.Data.Page, "handler 没有按 listPaging 的口径分页")
			assert.Equal(t, tc.wantSize, body.Data.PageSize)
			assert.Equal(t, len(matrixModels), body.Data.TotalModels,
				"total_models 是过滤后的全量,不是页内数量")
			assert.Equal(t, tc.wantModels, body.Data.Models)
		})
	}
}

// 页码必须有硬上界。
//
// 收敛前这里用的是裸 strconv.Atoi:184467440737095518 小于 MaxInt64,能被解析成功。
// 之后 (page-1)*pageSize 溢出成负数,而切片分页里 `start >= len(names)` 对负数
// 不成立,names[start:end] 直接 panic —— 一个登录用户可达的只读看板端点,
// 被一个查询参数打成 500 + 协程 panic。
//
// 用生产的 listPaging 而不是在测试里重写一份口径:看板与扩展其余模块**不同**
// (?page= 而非 ?p=、默认 50 而非 20、上限 200 而非 100),这套差异本身就是
// 前端在依赖的契约,必须被断言覆盖。
func TestPageParamsClampsPageToHardUpperBound(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantPage int
		wantSize int
	}{
		{"缺省", "", 1, defaultPageSize},
		{"正常翻页", "page=3&page_size=20", 3, 20},
		{"页码为 0", "page=0", 1, defaultPageSize},
		{"页码为负数", "page=-5", 1, defaultPageSize},
		// 收敛前这一条得到的是 maxPage(Atoi 解析成功、随后被页码上界夹住)。
		// httpq 把上界前移进了解析本身:超过 MaxQueryInt 的取值一律回落默认页,
		// 与下面那条"超过 MaxInt64"的行为合并成同一条规则,不再取决于
		// 取值恰好落在 Atoi 能否解析的哪一侧。两者都有界,都不会溢出。
		{"接近 MaxInt64 的页码回落默认", "page=184467440737095518", 1, defaultPageSize},
		{"超过 MaxInt64 的页码回落默认", "page=18446744073709551616", 1, defaultPageSize},
		{"页码上界与最大页长同时给到", "page=999999999&page_size=200", httpq.MaxPage, maxPageSize},
		{"页长越界回落默认", "page=2&page_size=99999", 2, defaultPageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, size := httpq.Paginate(ctxWithQuery(tc.query), listPaging)
			assert.Equal(t, tc.wantPage, page)
			assert.Equal(t, tc.wantSize, size)

			// 与切片分页连起来看:夹住之后的页码乘上页长绝不能溢出成负数。
			assert.GreaterOrEqual(t, httpq.Offset(page, size), 0,
				"offset 溢出为负,names[start:end] 会 panic")
		})
	}
}

// 切片分页自己也要挡住越界页码 —— 切片下标越界是 panic,不是错误返回,
// 防线必须贴着 names[start:end] 那一行放,不能只依赖上游夹过。
func TestPaginateNeverPanicsOnOutOfRangePage(t *testing.T) {
	names := []string{"a", "b", "c"}
	cases := []struct {
		name           string
		names          []string
		page, pageSize int
		want           []string
	}{
		{"第一页", names, 1, 2, []string{"a", "b"}},
		{"末页不足一页", names, 2, 2, []string{"c"}},
		{"越过末页", names, 9, 2, []string{}},
		{"空清单", nil, 1, 50, []string{}},
		// 184467440737095518 是 ?page= 能被 Atoi 接受的取值,乘上页长后
		// 回绕成负数 —— 这正是修复前 names[start:end] panic 的那条路径。
		{"页码溢出乘法后为负(修复前必 panic)", names, 184467440737095518, 50, []string{}},
		{"空清单 + 溢出页码", nil, 184467440737095518, 50, []string{}},
		{"页码溢出乘法后为正", names, 1 << 60, 50, []string{}},
		{"页码为 0", names, 0, 2, []string{}},
		{"负页码", names, -1, 2, []string{}},
		{"页长为 0", names, 1, 0, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			require.NotPanics(t, func() { got = httpq.Slice(tc.names, tc.page, tc.pageSize) })
			assert.Equal(t, tc.want, got)
		})
	}
}

// 分组取值域必须去重且有上限。
//
// 这是两个只读端点唯一的分组入口:去重与上限钉在这里,而不是钉在各调用点上。
//   - 不去重:?groups=vip,vip,vip 会让同一分组在 IN 子句里出现 N 次,
//     矩阵里同一个格子渲染 N 遍,并且把 maxSeries 的名额吃光,真实分组反而挤不进去。
//   - 无上限:一条 URL 就能把几百个分组塞进 IN 子句。
func TestIntersectGroupsDeduplicatesAndCaps(t *testing.T) {
	visible := map[string]string{"default": "", "vip": "", "svip": ""}

	t.Run("重复项被折叠", func(t *testing.T) {
		got := intersectGroups([]string{"vip", "vip", "default", "vip"}, visible)
		assert.Equal(t, []string{"default", "vip"}, got)
	})

	t.Run("不可见分组被剔除", func(t *testing.T) {
		got := intersectGroups([]string{"vip", "hidden"}, visible)
		assert.Equal(t, []string{"vip"}, got)
	})

	t.Run("请求为空表示全部可见分组", func(t *testing.T) {
		assert.Equal(t, []string{"default", "svip", "vip"}, intersectGroups(nil, visible))
	})

	t.Run("超量请求被上限截断", func(t *testing.T) {
		wide := make(map[string]string, maxGroupFilter*3)
		requested := make([]string, 0, maxGroupFilter*3)
		for i := 0; i < maxGroupFilter*3; i++ {
			g := "g" + strconv.Itoa(i)
			wide[g] = ""
			requested = append(requested, g)
		}
		got := intersectGroups(requested, wide)
		assert.Len(t, got, maxGroupFilter)
	})
}

// splitCSV 在调用方没给上限时必须回落到兜底上限,而不是"不限"。
//
// 修复前 limit<=0 表示不限,而 groups 的三处调用点传的正是 0:
// 一条 URL 可以让服务端解析出数十万个字符串。上限要在分配之前生效 ——
// 先 strings.Split 整串再截断,内存已经吃下去了。
func TestSplitCSVIsAlwaysBounded(t *testing.T) {
	huge := strings.Repeat("g,", maxCSVItems*10) + "tail"

	t.Run("limit 为 0 时回落到兜底上限而不是不限", func(t *testing.T) {
		assert.Len(t, splitCSV(huge, 0), maxCSVItems)
	})
	t.Run("limit 为负同理", func(t *testing.T) {
		assert.Len(t, splitCSV(huge, -1), maxCSVItems)
	})
	t.Run("超过兜底上限的 limit 被封顶", func(t *testing.T) {
		assert.Len(t, splitCSV(huge, 1_000_000), maxCSVItems)
	})
	t.Run("更紧的 limit 被尊重", func(t *testing.T) {
		assert.Len(t, splitCSV(huge, 3), 3)
	})

	// 解析语义不能在加固过程中被改坏。
	t.Run("空白与空项", func(t *testing.T) {
		assert.Nil(t, splitCSV("", maxCSVItems))
		assert.Nil(t, splitCSV("   ", maxCSVItems))
		assert.Equal(t, []string{"a", "b"}, splitCSV(" a , , b ,", maxCSVItems))
		assert.Equal(t, []string{"gpt-4o"}, splitCSV("gpt-4o", maxCSVItems))
	})
	t.Run("截断后不得把剩余整串当成一项", func(t *testing.T) {
		got := splitCSV("a,b,c,d", 2)
		assert.Equal(t, []string{"a", "b"}, got)
		for _, g := range got {
			assert.NotContains(t, g, ",", "截断产物里绝不能残留分隔符")
		}
	})
}
