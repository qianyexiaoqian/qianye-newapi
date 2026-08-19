package commission

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_daily_consume_byday_test.go —— 按天下钻的回归。
//
// 下钻这条接口有两件事会无声地错,而且错出来的报表看起来完全正常:
//
//	① 日界不走 dayline。SQL 里但凡用一次 DATE()/FROM_UNIXTIME()/date_trunc(),
//	   日界就交给了数据库会话的时区,而返佣的日界是 commission.day_offset_minutes
//	   那一个固定偏移。差一小时的结果是每天的数都对不上主表那一格,而且
//	   合计仍然相等 —— 最难发现的那种不一致。
//	② 空的那些天不出行。运营会把"这天没花钱"与"这天没查出来"看成同一件事。

// byDayItem 是下钻接口的一行。
type byDayItem struct {
	Date                string `json:"date"`
	DayStart            int64  `json:"day_start"`
	RequestCount        int64  `json:"request_count"`
	ConsumeQuota        int64  `json:"consume_quota"`
	CommissionBaseQuota int64  `json:"commission_base_quota"`
	UncountedQuota      int64  `json:"uncounted_quota"`
	CommissionGross     string `json:"commission_gross"`
}

func callByDay(t *testing.T, rawQuery string) (items []byDayItem, summary struct {
	RequestCount        int64  `json:"request_count"`
	ConsumeQuota        int64  `json:"consume_quota"`
	CommissionBaseQuota int64  `json:"commission_base_quota"`
	UncountedQuota      int64  `json:"uncounted_quota"`
	CommissionGross     string `json:"commission_gross"`
}) {
	t.Helper()
	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/daily-consume/by-day?"+rawQuery, "", adminUserDailyConsume)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items   []byDayItem `json:"items"`
			Summary struct {
				RequestCount        int64  `json:"request_count"`
				ConsumeQuota        int64  `json:"consume_quota"`
				CommissionBaseQuota int64  `json:"commission_base_quota"`
				UncountedQuota      int64  `json:"uncounted_quota"`
				CommissionGross     string `json:"commission_gross"`
			} `json:"summary"`
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
// 期望(独立算出来的):
//
//	20260803  没有消费           0 / 0 / 0
//	20260804  1000 + 500 = 1500  计佣基数 1500,未计佣 0
//	20260805  没有消费           0 / 0 / 0
//	合计 1500,三行一行不少
//
// 变异验证:
//   - 把 dayBucketSQL 里的 off 参数去掉(退回按 UTC 分天)→ 8 月 3 日冒出 1000、
//     8 月 4 日只剩 500,两行断言红;
//   - 把补零那段循环换成"只输出查到的天"→ Len 断言红。
func TestAdminUserDailyConsumeSplitsByDaylineNotByServerTimezone(t *testing.T) {
	cfg := &config.Config{Enabled: true}
	cfg.Commission.Enabled = true
	cfg.Commission.DayOffsetMinutes = 480 // UTC+8
	useConfig(t, cfg)
	extDB := newTestDB(t)
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

	seedAccrual(t, extDB, 1, func(a *Accrual) {
		a.IdemScope, a.IdemKey, a.SourceType = SourceConsume, "consume:601", SourceConsume
		a.InviterId, a.InviteeId, a.BucketDate = 600, user, "20260804"
		a.BaseQuota, a.GrossAmount = 1500, decimal.NewFromInt(75)
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
	assert.EqualValues(t, 1500, byDate["20260804"].CommissionBaseQuota)
	assert.EqualValues(t, 0, byDate["20260804"].UncountedQuota)
	assert.Equal(t, "75", byDate["20260804"].CommissionGross)
	assert.EqualValues(t, 0, byDate["20260805"].ConsumeQuota)

	// 每一行的 day_start 必须真的是那一天的日界,前端据此排序与画图。
	for _, it := range items {
		assert.Equal(t, it.Date, dayKey(it.DayStart), "day_start 与 date 必须同源")
		assert.Equal(t, it.DayStart, dayStart(it.DayStart), "day_start 必须正好落在日界上")
	}

	assert.EqualValues(t, 1500, summary.ConsumeQuota)
	assert.EqualValues(t, 1500, summary.CommissionBaseQuota)
	assert.EqualValues(t, 0, summary.UncountedQuota)
	assert.Equal(t, "75", summary.CommissionGross)
}

// TestAdminUserDailyConsumeSumsMultipleAccrualsOfOneDay 守"同一天多行计佣"。
//
// 同一天可以有多行计佣:费率、汇率、**成熟期**、上线任一变化都会落新行
// (见 consumeIdemKey)。下钻取一行就会少算,而少算的那部分正好是改配置
// 之后挣的那一段 —— 与"改成熟期当天"那条缺陷是同一个根。
//
// 变异验证:把 aggregateCommissionByDay 的 SUM(base_quota) 换成 MAX(base_quota)
// → 计佣基数从 1500 掉到 1000,断言红。
func TestAdminUserDailyConsumeSumsMultipleAccrualsOfOneDay(t *testing.T) {
	cfg := &config.Config{Enabled: true}
	cfg.Commission.Enabled = true
	useConfig(t, cfg)
	extDB := newTestDB(t)
	useMainDB(t, &model.User{})
	logDB := useLogDB(t)

	const user, day = 611, "20260810"
	seedLog(t, logDB, user, dayTs(t, day, 3600), 2000, model.LogTypeConsume)

	// 同一天两行:一行是改成熟期之前的,一行是之后的。
	seedAccrual(t, extDB, 1, func(a *Accrual) {
		a.IdemScope, a.IdemKey, a.SourceType = SourceConsume, "consume:611:h7", SourceConsume
		a.InviterId, a.InviteeId, a.BucketDate = 610, user, day
		a.BaseQuota, a.GrossAmount = 1000, decimal.NewFromInt(50)
	})
	seedAccrual(t, extDB, 2, func(a *Accrual) {
		a.IdemScope, a.IdemKey, a.SourceType = SourceConsume, "consume:611:h0", SourceConsume
		a.InviterId, a.InviteeId, a.BucketDate = 610, user, day
		a.BaseQuota, a.GrossAmount = 500, decimal.NewFromInt(25)
	})
	// 作废的那行不算。
	seedAccrual(t, extDB, 3, func(a *Accrual) {
		a.IdemScope, a.IdemKey, a.SourceType = SourceConsume, "consume:611:void", SourceConsume
		a.InviterId, a.InviteeId, a.BucketDate = 610, user, day
		a.BaseQuota, a.GrossAmount = 400, decimal.NewFromInt(20)
		a.Status = StatusVoided
	})

	items, _ := callByDay(t, "user_id=611&start_date="+day+"&end_date="+day)
	require.Len(t, items, 1)
	assert.EqualValues(t, 2000, items[0].ConsumeQuota)
	assert.EqualValues(t, 1500, items[0].CommissionBaseQuota, "同一天的多行计佣必须相加")
	assert.EqualValues(t, 500, items[0].UncountedQuota, "2000 − 1500")
	assert.Equal(t, "75", items[0].CommissionGross)
}

// TestAdminUserDailyConsumeGuardsItsInputs 守参数。
//
// user_id 必填是这条接口不打挂主库的前提之一:缺了它就退化成一条全站按天的
// 聚合,而那正是"点开就慢 5 秒"的形状。区间上界与主表共用同一个,理由也一样。
func TestAdminUserDailyConsumeGuardsItsInputs(t *testing.T) {
	cfg := &config.Config{Enabled: true}
	cfg.Commission.Enabled = true
	useConfig(t, cfg)
	newTestDB(t)
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
				"/api/qy/admin/commission/daily-consume/by-day?"+tc.query, "", adminUserDailyConsume)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}
