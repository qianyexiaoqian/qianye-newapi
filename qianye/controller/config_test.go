package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func ctxWithQuery(query string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/admin/fund-orders?"+query, nil)
	return c
}

// 分页的两个上限都必须生效。
//
// 页长上限挡的是"一次拉爆内存",页码上限挡的是深翻页:两者是不同的攻击面。
// 整数上界让 184467440737095518 这类取值在解析阶段就回落默认值(所以
// (page-1)*size 不会溢出成负数),页码上界让 10 万页也不会变成 OFFSET 1 亿 ——
// MySQL 会扫完再全部丢掉。这两条查询都打在扩展库的资金单与审计表上。
//
// 走的是生产用的那个 listPaging,而不是测试自己拼一份口径:口径本身
// (?p= / 默认 20 / 上限 100)也是被测对象。
func TestPaginationClampsBothPageAndSize(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantPage   int
		wantSize   int
		wantOffset int
	}{
		{"缺省", "", 1, 20, 0},
		{"正常翻页", "p=3&page_size=50", 3, 50, 100},
		{"页码为 0", "p=0", 1, 20, 0},
		{"页码为负", "p=-1", 1, 20, 0},
		// 页长越界回落默认值 20 而不是夹到 100。这是收敛到 httpq 时统一的口径:
		// 被收敛的七份拷贝里六份是"越界回落默认",只有本包这一份是"越界夹到上限"。
		// 两者都有界,取多数派;前端从不发大于 100 的页长(admin 页面写死 20)。
		{"页长越界回落默认", "p=1&page_size=100000", 1, 20, 0},
		{"深翻页被夹住", "p=999999&page_size=100", httpq.MaxPage, 100, (httpq.MaxPage - 1) * 100},
		{"非数字回落默认", "p=abc", 1, 20, 0},
		{"超大数字回落默认", "p=184467440737095518", 1, 20, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, size := httpq.Paginate(ctxWithQuery(tc.query), listPaging)
			assert.Equal(t, tc.wantPage, page)
			assert.Equal(t, tc.wantSize, size)
			// 调用点一律是 Offset(httpq.Offset(page, size)),这里断言的是
			// 真正打给 MySQL 的那个数,而不只是两个中间变量。
			assert.Equal(t, tc.wantOffset, httpq.Offset(page, size))
			assert.GreaterOrEqual(t, httpq.Offset(page, size), 0)
			assert.LessOrEqual(t, httpq.Offset(page, size), (httpq.MaxPage-1)*100,
				"OFFSET 必须有硬上界,否则一条 URL 就能让 MySQL 扫全表")
		})
	}
}
