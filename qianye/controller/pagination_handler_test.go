package controller

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
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 资金单列表是管理端的核心排障入口。这里从真实 handler 进,断言两件事:
//
//  1. 打给数据库的 OFFSET 恒非负、恒有界(分页收敛的目标)
//  2. start_ts / end_ts 时间范围筛选真的拼进了 WHERE
//
// 第 2 条不是顺带:收敛前这两个参数走的是本包那份上界为 100 万的 intQuery,
// 而任何真实 Unix 时间戳都远大于 100 万 —— 解析恒回落 0、`v > 0` 恒不成立、
// WHERE 从来没被拼上去。给分页加上界的那次修复,顺手打死了共用同一个 helper
// 的时间筛选,而且没有任何报错:管理员点了时间范围,拿回的是全表。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

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

// GORM 在 offset<=0 时根本不输出 OFFSET 子句,所以"没有 OFFSET"等价于 OFFSET 0。
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

// seedTs 是一个真实量级的 Unix 时间戳(2025-08-01 前后),
// 刻意远大于 100 万 —— 那正是收敛前 intQuery 的上界。
const seedTs int64 = 1754006400

func newFundOrderEnv(t *testing.T) *sqlRecorder {
	t.Helper()
	rec := &sqlRecorder{Interface: logger.Discard}
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: rec})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&qymodel.FundOrder{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{Enabled: true})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})

	// 五行:创建时间依次相差一天,便于验证时间范围筛选真的生效。
	for i := 1; i <= 5; i++ {
		require.NoError(t, gdb.Create(&qymodel.FundOrder{
			OrderNo:     "FO" + strconv.Itoa(i),
			Kind:        "transfer",
			IdemScope:   "test",
			IdemKey:     "k" + strconv.Itoa(i),
			UserId:      1,
			AmountQuota: 1000,
			CreatedAt:   seedTs + int64(i)*86400,
			UpdatedAt:   seedTs + int64(i)*86400,
		}).Error)
	}
	return rec
}

func callAdminListFundOrders(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/admin/fund-orders?"+query, nil)
	c.Set("id", 1)
	c.Set("username", "admin")

	AdminListFundOrders(c)
	return res
}

type fundOrderListBody struct {
	Success bool `json:"success"`
	Data    struct {
		Items    []map[string]any `json:"items"`
		Total    int64            `json:"total"`
		Page     int              `json:"p"`
		PageSize int              `json:"page_size"`
	} `json:"data"`
}

func decodeFundOrderList(t *testing.T, res *httptest.ResponseRecorder) fundOrderListBody {
	t.Helper()
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
	var body fundOrderListBody
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success)
	return body
}

func TestAdminListFundOrdersOffsetIsBoundedForEveryQuery(t *testing.T) {
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
		{"越过末页返回空", "p=4&page_size=2", 4, 2, 6, 0},
		{"页长越界回落默认", "p=1&page_size=100000", 1, 20, 0, 5},
		{"页码为负", "p=-1", 1, 20, 0, 5},
		{"溢出攻击值回落第一页", "p=184467440737095518&page_size=100", 1, 100, 0, 5},
		{"深翻页被夹到 MaxPage", "p=999999&page_size=100", httpq.MaxPage, 100, (httpq.MaxPage - 1) * 100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newFundOrderEnv(t)
			res := callAdminListFundOrders(t, tc.query)

			sql := rec.lastSelect(t)
			got := offsetInSQL(t, sql)
			assert.Equal(t, tc.wantOffset, got, "SQL: %s", sql)
			assert.GreaterOrEqual(t, got, 0)
			assert.LessOrEqual(t, got, (httpq.MaxPage-1)*100)

			body := decodeFundOrderList(t, res)
			assert.Len(t, body.Data.Items, tc.wantItems)
			assert.EqualValues(t, 5, body.Data.Total)
			assert.Equal(t, tc.wantPage, body.Data.Page)
			assert.Equal(t, tc.wantSize, body.Data.PageSize)
		})
	}
}

// 时间范围筛选必须真的生效。
//
// 断言的是 total(经过 WHERE 的行数),不是 items 长度 —— items 会被分页截断,
// 而 total 是 COUNT 的结果,筛选没拼上去就一定等于 5。
func TestAdminListFundOrdersAppliesTimestampFilters(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantTotal int64
	}{
		{"不带时间筛选", "", 5},
		// 种子数据的 created_at 是 seedTs+1d .. seedTs+5d。
		{"start_ts 掐掉前两天", "start_ts=" + strconv.FormatInt(seedTs+3*86400, 10), 3},
		{"end_ts 掐掉后两天", "end_ts=" + strconv.FormatInt(seedTs+3*86400, 10), 3},
		{"两端同时给", "start_ts=" + strconv.FormatInt(seedTs+2*86400, 10) +
			"&end_ts=" + strconv.FormatInt(seedTs+4*86400, 10), 3},
		{"未来的 start_ts 应当筛空", "start_ts=" + strconv.FormatInt(seedTs+99*86400, 10), 0},
		// 非法取值必须退化成"不筛选",而不是筛空。
		{"start_ts 为负 → 不筛选", "start_ts=-1", 5},
		{"start_ts 非数字 → 不筛选", "start_ts=abc", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newFundOrderEnv(t)
			body := decodeFundOrderList(t, callAdminListFundOrders(t, tc.query))
			assert.Equal(t, tc.wantTotal, body.Data.Total,
				"时间范围筛选没有拼进 WHERE —— 管理员选了时间段,拿回的却是全表")
		})
	}
}

// user_id 筛选不能因为 ID 大就静默失效。
//
// 与时间戳同源的一个坑:上界过紧时 100 万以上的用户 ID 会回落 0,
// `v > 0` 不成立,WHERE 不拼 —— 管理员按用户筛,拿回的是所有人的资金单。
func TestAdminListFundOrdersAppliesLargeUserIdFilter(t *testing.T) {
	rec := newFundOrderEnv(t)
	_ = rec

	const bigUserId = 1234567 // 远超收敛前 intQuery 的 100 万上界
	gdb := qyDBHandle.Load()
	require.NoError(t, gdb.Create(&qymodel.FundOrder{
		OrderNo: "FO-big", Kind: "transfer", IdemScope: "test", IdemKey: "kbig",
		UserId: bigUserId, AmountQuota: 1000, CreatedAt: seedTs, UpdatedAt: seedTs,
	}).Error)

	body := decodeFundOrderList(t,
		callAdminListFundOrders(t, "user_id="+strconv.Itoa(bigUserId)))
	assert.EqualValues(t, 1, body.Data.Total,
		"百万级用户 ID 的筛选被静默丢弃,管理端拿回了所有人的资金单")
}
