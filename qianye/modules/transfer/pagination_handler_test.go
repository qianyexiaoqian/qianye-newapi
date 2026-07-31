package transfer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
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

// 划转流水是一个**登录用户就能打到的资金模块只读接口**,收敛前它的分页参数
// 解析是一份没有任何上界的私有拷贝(裸 strconv.Atoi + 只夹 page<1)。
//
// 断言一律从 HTTP handler 进,看的是真正打给数据库的那条 SQL,
// 而不是 page/size 两个中间变量:解析函数写得再对,只要 handler 没接上、
// 或者接上了却仍在调用点自己重算一遍 (page-1)*size,漏洞照样在线上。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// sqlRecorder 记录 GORM 最终执行的 SQL(变量已内联)。
type sqlRecorder struct {
	logger.Interface
	mu   sync.Mutex
	sqls []string
}

func (r *sqlRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	r.mu.Lock()
	r.sqls = append(r.sqls, sql)
	r.mu.Unlock()
}

func (r *sqlRecorder) lastSelect(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.sqls) - 1; i >= 0; i-- {
		if len(r.sqls[i]) >= 6 && r.sqls[i][:6] == "SELECT" {
			return r.sqls[i]
		}
	}
	require.Fail(t, "handler 没有真的查库")
	return ""
}

// GORM 在 offset<=0 时**根本不输出** OFFSET 子句 —— 这正是收敛前那个缺陷
// 最阴的地方:溢出成负数不会报错,只会让"第 1844 亿页"静默返回第一页。
var offsetRe = regexp.MustCompile(`OFFSET (-?\d+)`)

func offsetInSQL(t *testing.T, sql string) int {
	t.Helper()
	m := offsetRe.FindStringSubmatch(sql)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

func newListEnv(t *testing.T) *sqlRecorder {
	t.Helper()
	rec := &sqlRecorder{Interface: logger.Discard}
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: rec})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Order{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: config.Transfer{Enabled: true},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})

	now := common.GetTimestamp()
	for i := 1; i <= 5; i++ {
		require.NoError(t, gdb.Create(&Order{
			OrderNo:    "TR" + strconv.Itoa(i),
			FromUserId: 1,
			ToUserId:   2,
			Amount:     1000,
			Status:     "success",
			CreatedAt:  now,
			SettledAt:  now,
		}).Error)
	}
	return rec
}

func callListRecords(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/transfer/records?"+query, nil)
	c.Set("id", 1)

	handleListRecords(c)
	return res
}

// 打给数据库的 OFFSET 在任何查询串下都必须非负且有界。
func TestListRecordsOffsetIsBoundedForEveryQuery(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantPage   int
		wantSize   int
		wantOffset int
		wantItems  int
	}{
		{"缺省", "", 1, 20, 0, 5},
		{"第一页", "p=1&page_size=2", 1, 2, 0, 2},
		{"第二页", "p=2&page_size=2", 2, 2, 2, 2},
		{"第三页", "p=3&page_size=2", 3, 2, 4, 1},
		{"越过末页返回空", "p=4&page_size=2", 4, 2, 6, 0},
		{"页长越界回落默认", "p=1&page_size=100000", 1, 20, 0, 5},
		{"页码为负", "p=-1", 1, 20, 0, 5},
		{"页码非数字", "p=abc", 1, 20, 0, 5},
		// 三条各自锁一道上界,理由见 withdraw 包同名测试的注释:
		// 整数上界挡住会回绕的取值,页码上界挡住不回绕但足够深的翻页,
		// 而页码上界有两处实现(Paginate 的返回值 / Offset 的入参兜底),
		// 所以 wantPage 与 wantOffset 必须同时断言。
		{"溢出攻击值回落第一页", "p=184467440737095518&page_size=100", 1, 100, 0, 5},
		{"超过 MaxInt64 回落第一页", "p=18446744073709551616&page_size=100", 1, 100, 0, 5},
		{"深翻页被夹到 MaxPage", "p=999999&page_size=100", httpq.MaxPage, 100, (httpq.MaxPage - 1) * 100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newListEnv(t)
			res := callListRecords(t, tc.query)
			require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

			sql := rec.lastSelect(t)
			got := offsetInSQL(t, sql)
			assert.Equal(t, tc.wantOffset, got, "SQL: %s", sql)
			assert.GreaterOrEqual(t, got, 0,
				"OFFSET 为负数会被 GORM 静默丢弃,深翻页会悄悄退回第一页")
			assert.LessOrEqual(t, got, (httpq.MaxPage-1)*100,
				"OFFSET 必须有硬上界,否则一条 URL 就能让数据库扫全表")

			var body struct {
				Success bool `json:"success"`
				Data    struct {
					Items    []map[string]any `json:"items"`
					Total    int64            `json:"total"`
					Page     int              `json:"p"`
					PageSize int              `json:"page_size"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
			assert.True(t, body.Success)
			assert.Len(t, body.Data.Items, tc.wantItems)
			assert.EqualValues(t, 5, body.Data.Total)
			assert.Equal(t, tc.wantPage, body.Data.Page)
			assert.Equal(t, tc.wantSize, body.Data.PageSize)
		})
	}
}
