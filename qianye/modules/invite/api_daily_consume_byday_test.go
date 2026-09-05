package invite

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_daily_consume_byday_test.go —— 按天下钻的回归。
//
// 下钻这条接口有两件事会无声地错,而且错出来的报表看起来完全正常:
//
//	① 日界不走 dayline。SQL 里但凡用一次 DATE()/FROM_UNIXTIME()/date_trunc(),
//	   日界就交给了数据库会话的时区,而这里的日界是 invite.day_offset_minutes
//	   那一个固定偏移。差一小时的结果是每天的数都对不上主表那一格,而且
//	   合计仍然相等 —— 最难发现的那种不一致。
//	② 空的那些天不出行。运营会把"这天没花钱"与"这天没查出来"看成同一件事。

// byDayItem 是下钻接口的一行。
type byDayItem struct {
	Date            string `json:"date"`
	DayStart        int64  `json:"day_start"`
	RequestCount    int64  `json:"request_count"`
	ConsumeQuota    int64  `json:"consume_quota"`
	InviteBaseQuota int64  `json:"invite_base_quota"`
	UncountedQuota  int64  `json:"uncounted_quota"`
	InviteGross     string `json:"invite_gross"`
}

type byDaySummary struct {
	RequestCount    int64  `json:"request_count"`
	ConsumeQuota    int64  `json:"consume_quota"`
	InviteBaseQuota int64  `json:"invite_base_quota"`
	UncountedQuota  int64  `json:"uncounted_quota"`
	InviteGross     string `json:"invite_gross"`
}

func callByDay(t *testing.T, rawQuery string) ([]byDayItem, byDaySummary) {
	t.Helper()
	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/invite/daily-consume/by-day?"+rawQuery, "", adminUserDailyConsume)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items   []byDayItem  `json:"items"`
			Summary byDaySummary `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data.Items, resp.Data.Summary
}

// TestAdminUserDailyConsumeSplitsByDaylineNotByServerTimezone 是下钻的主用例。
//
// 口径固定成 UTC+8(day_offset_minutes: 480),这样"按 UTC 分天"与"按 dayline
// 分天"会给出**不同的**答案:两笔消费分别落在 UTC 的 8 月 3 日 20:00 与
// 8 月 4 日 02:00,按 UTC 是两天,按 UTC+8 的日界是同一天(8 月 4 日)。
//
// 变异验证:
//   - 把 dayBucketSQL 里的 off 参数去掉(退回按 UTC 分天)→ 8 月 3 日冒出 1000、
//     8 月 4 日只剩 500,两行断言红;
//   - 把补零那段循环换成"只输出查到的天"→ Len 断言红。
func TestAdminUserDailyConsumeSplitsByDaylineNotByServerTimezone(t *testing.T) {
	useConfig(t, inviteConfig(480))
	newTestDB(t)
	useAdminAPI(t)
	useMainDB(t, &model.User{})
	logDB := useLogDB(t)

	const user = 601
	// dayTs 已经走 dayline,所以这两个时刻直接以 UTC+8 的日界为基准描述:
	// 8 月 4 日(UTC+8)的 04:00 与 10:00,对应 UTC 的 8/3 20:00 与 8/4 02:00。
	early := dayTs(t, "20260804", 4*3600)
	late := dayTs(t, "20260804", 10*3600)
	require.Less(t, early, late)

	seedLog(t, logDB, user, early, 1000, model.LogTypeConsume)
	seedLog(t, logDB, user, late, 500, model.LogTypeConsume)
	// 别人的消费、以及同一天的非消费日志,都不该混进来。
	seedLog(t, logDB, user+1, late, 9999, model.LogTypeConsume)
	seedLog(t, logDB, user, late, 7777, model.LogTypeTopup)

	useInviteAccruals(t, nil, map[string]DayAccrual{
		"20260804": {BaseQuota: 1500, Gross: decimal.RequireFromString("0.75")},
	})

	items, summary := callByDay(t, "user_id=601&start_date=20260803&end_date=20260805")
	require.Len(t, items, 3, "区间内每一天都要出一行,没消费的那天也要:%+v", items)

	byDate := map[string]byDayItem{}
	for _, it := range items {
		byDate[it.Date] = it
	}
	assert.EqualValues(t, 0, byDate["20260803"].ConsumeQuota,
		"8/3 20:00 UTC 在 UTC+8 的日界下属于 8/4,不能留在 8/3")
	assert.EqualValues(t, 1500, byDate["20260804"].ConsumeQuota, "1000 + 500 都归 8/4")
	assert.EqualValues(t, 2, byDate["20260804"].RequestCount)
	assert.EqualValues(t, 1500, byDate["20260804"].InviteBaseQuota)
	assert.EqualValues(t, 0, byDate["20260804"].UncountedQuota)
	assert.Equal(t, "0.75", byDate["20260804"].InviteGross)
	assert.EqualValues(t, 0, byDate["20260805"].ConsumeQuota)

	// 每一行的 day_start 必须真的是那一天的日界,前端据此排序与画图。
	for _, it := range items {
		assert.Equal(t, it.Date, dayKey(it.DayStart), "day_start 与 date 必须同源")
		assert.Equal(t, it.DayStart, dayStart(it.DayStart), "day_start 必须正好落在日界上")
	}

	assert.EqualValues(t, 1500, summary.ConsumeQuota)
	assert.EqualValues(t, 1500, summary.InviteBaseQuota)
	assert.EqualValues(t, 0, summary.UncountedQuota)
	assert.Equal(t, "0.75", summary.InviteGross)
}

// TestAdminUserDailyConsumeGuardsItsInputs 守参数。
//
// user_id 必填是这条接口不打挂主库的前提之一:缺了它就退化成一条全站按天的
// 聚合,而那正是"点开就慢 5 秒"的形状。区间上界与主表共用同一个,理由也一样。
func TestAdminUserDailyConsumeGuardsItsInputs(t *testing.T) {
	useConfig(t, inviteConfig(0))
	newTestDB(t)
	useAdminAPI(t)
	useMainDB(t, &model.User{})
	useLogDB(t)

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"缺 user_id", "start_date=20260801&end_date=20260801"},
		{"user_id 不是数字", "user_id=abc"},
		{"user_id 是 0", "user_id=0"},
		{"区间超过上界", "user_id=1&start_date=20260101&end_date=20261231"},
		{"日期格式不合法", "user_id=1&start_date=2026-08-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAdminHandler(t, http.MethodGet,
				"/api/qy/admin/invite/daily-consume/by-day?"+tc.query, "", adminUserDailyConsume)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}
