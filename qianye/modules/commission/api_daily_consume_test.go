package commission

// api_daily_consume_test.go —— 日消费明细。
//
// 这一批用例守的是四件在这个功能上一旦错掉就没人会发现的事:
//
//  1. **日界**必须与 accrual.bucket_date / 结算是同一个,并且跟着
//     commission.day_offset_minutes 走。差一小时的报表看起来完全正常,
//     只是每天有一段消费落在了错误的那一天上;
//  2. **0% 分组 / 没有邀请关系 / 违规扣费**的用户必须照常出现在报表里 ——
//     这正是"只读计佣表"会静默漏掉的那批人,也是选 logs 当数据源的全部理由;
//  3. **区间上界**与行数上界必须真的挡住,而不是被忽略后放一条全表扫描出去;
//  4. **越权**:上线只能看见自己名下的下线,且看不到真实用户名。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ───────────────────────── 脚手架 ─────────────────────────

// useLogDB 临时替换日志库句柄。
//
// 与 useMainDB 分开:生产里 LOG_DB 可以是与主库完全不同的一个库(甚至
// ClickHouse),而本报表的两条聚合恰好一条打 LOG_DB、一条打 model.DB。
// 把它们塞进同一个 sqlite 会让"我查错了库"这类错误在测试里看不见。
func useLogDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.Log{}))

	prev := model.LOG_DB
	model.LOG_DB = gdb
	t.Cleanup(func() {
		model.LOG_DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// seedLog 插入一条消费日志。
func seedLog(t *testing.T, gdb *gorm.DB, userId int, at int64, quota int, logType int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.Log{
		UserId:    userId,
		CreatedAt: at,
		Type:      logType,
		Quota:     quota,
		ModelName: "qy-test",
	}).Error)
}

// dayTs 返回 yyyymmdd 那一天起点之后 offset 秒的时刻。
func dayTs(t *testing.T, day string, offset int64) int64 {
	t.Helper()
	start, ok := dayKeyStart(day)
	require.True(t, ok, "日键不合法: %s", day)
	return start + offset
}

// newDailyCtx 造一个只带查询串的 gin 上下文,给纯解析函数用。
func newDailyCtx(t *testing.T, rawQuery string, userId int) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x?"+rawQuery, nil)
	c.Set("id", userId)
	c.Set("username", "u"+itoa(userId))
	return c
}

// callUserHandler 以某个登录用户的身份跑一条用户端处理器。
func callUserHandler(t *testing.T, rawQuery string, userId int, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/x?"+rawQuery, nil)
	c.Set("id", userId)
	c.Set("username", "u"+itoa(userId))
	h(c)
	return rec
}

// ───────────────────────── 日界与区间 ─────────────────────────

func TestParseDailyRange(t *testing.T) {
	// 2026-08-17 12:00:00 UTC。选一个正午,这样 ±8 小时的偏移都不跨日,
	// 唯一会跨日的是"昨天"这个减法本身 —— 那正是要测的东西。
	const now = int64(1786968000)

	cases := []struct {
		name        string
		offsetMin   int
		query       string
		wantStart   string
		wantEnd     string
		wantDays    int
		wantStartTs int64
		wantErr     error
	}{
		{
			name: "缺省即昨日", query: "",
			wantStart: "20260816", wantEnd: "20260816", wantDays: 1,
			wantStartTs: 1786924800 - 86400,
		},
		{
			name: "只给 start_date 时不延伸到今天", query: "start_date=20260801",
			wantStart: "20260801", wantEnd: "20260801", wantDays: 1,
		},
		{
			name: "只给 end_date", query: "end_date=20260801",
			wantStart: "20260801", wantEnd: "20260801", wantDays: 1,
		},
		{
			name: "同一天首尾相同", query: "start_date=20260803&end_date=20260803",
			wantStart: "20260803", wantEnd: "20260803", wantDays: 1,
		},
		{
			name: "区间含首尾", query: "start_date=20260801&end_date=20260803",
			wantStart: "20260801", wantEnd: "20260803", wantDays: 3,
		},
		{
			// 边界:31 天恰好放行。7 月 1 日到 7 月 31 日正好 31 天。
			name: "上界之内", query: "start_date=20260701&end_date=20260731",
			wantStart: "20260701", wantEnd: "20260731", wantDays: 31,
		},
		{
			name: "超过上界一天就拒绝", query: "start_date=20260701&end_date=20260801",
			wantErr: errDailyRangeTooBig,
		},
		{name: "首尾颠倒", query: "start_date=20260803&end_date=20260801", wantErr: errDailyRangeOrder},
		{name: "格式非法", query: "start_date=2026-08-01", wantErr: errDailyRangeFormat},
		{name: "月份非法", query: "start_date=20261301", wantErr: errDailyRangeFormat},
		{name: "空串以外的空白也算缺省", query: "start_date=%20&end_date=%20", wantStart: "20260816", wantEnd: "20260816", wantDays: 1},
		{
			// 日界偏移必须一路生效:UTC+8 下,UTC 12:00 已经是 20 点,
			// "昨天"仍然是 8 月 16 日,但它的起点比 UTC 早 8 小时开始。
			name: "day_offset_minutes 参与日界", offsetMin: 480, query: "",
			wantStart: "20260816", wantEnd: "20260816", wantDays: 1,
			wantStartTs: 1786924800 - 86400 - 8*3600,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useConfig(t, &config.Config{Enabled: true, Commission: config.Commission{
				Enabled: true, DayOffsetMinutes: tc.offsetMin,
			}})
			got, err := parseDailyRange(newDailyCtx(t, tc.query, 1), now)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantStart, got.StartDay, "start_date")
			assert.Equal(t, tc.wantEnd, got.EndDay, "end_date")
			assert.Equal(t, tc.wantDays, got.Days, "天数")
			// 半开区间必须恰好覆盖 Days 个自然日,一秒不多一秒不少。
			assert.Equal(t, int64(tc.wantDays)*secondsPerDay, got.EndTs-got.StartTs,
				"[StartTs, EndTs) 的长度必须等于天数")
			if tc.wantStartTs != 0 {
				assert.Equal(t, tc.wantStartTs, got.StartTs, "区间起点")
			}
			// 日界必须与账本的分桶键可逆:bucketDate(起点) 就是 start_date 本身,
			// 否则报表按 logs 取的那一天与 accrual.bucket_date 不是同一天。
			assert.Equal(t, got.StartDay, bucketDate(got.StartTs),
				"区间起点必须落在 start_date 这个分桶里")
			assert.Equal(t, got.EndDay, bucketDate(got.EndTs-1),
				"区间末秒必须落在 end_date 这个分桶里")
			assert.NotEqual(t, got.EndDay, bucketDate(got.EndTs),
				"EndTs 是开区间,它本身必须已经属于下一天")
		})
	}
}

// ───────────────────────── logs 侧聚合 ─────────────────────────

func TestAggregateConsumeFromLogs(t *testing.T) {
	useConfig(t, &config.Config{Enabled: true, Commission: config.Commission{Enabled: true}})
	gdb := useLogDB(t)

	const day = "20260803"
	start, ok := dayKeyStart(day)
	require.True(t, ok)

	// 用户 11:当天两笔;边界各一笔用来钉半开区间。
	seedLog(t, gdb, 11, start, 100, model.LogTypeConsume)       // 日初,含
	seedLog(t, gdb, 11, start+86399, 200, model.LogTypeConsume) // 日末最后一秒,含
	seedLog(t, gdb, 11, start-1, 999, model.LogTypeConsume)     // 前一天最后一秒,不含
	seedLog(t, gdb, 11, start+86400, 999, model.LogTypeConsume) // 次日日初,不含
	// 用户 12:只有一笔,而且 quota 是 0(免费模型)。它必须出现 ——
	// "有人在用但没花钱"与"没人用"是两件完全不同的事。
	seedLog(t, gdb, 12, start+10, 0, model.LogTypeConsume)
	// 用户 13:当天有日志,但不是消费类型,必须被过滤掉。
	seedLog(t, gdb, 13, start+10, 500, model.LogTypeTopup)
	seedLog(t, gdb, 13, start+11, 500, model.LogTypeRefund)

	r, err := parseDailyRange(newDailyCtx(t, "start_date="+day, 1), common.GetTimestamp())
	require.NoError(t, err)

	t.Run("不限用户", func(t *testing.T) {
		rows, err := aggregateConsumeFromLogs(context.Background(), r, nil)
		require.NoError(t, err)
		got := map[int]consumeAgg{}
		for _, row := range rows {
			got[row.UserId] = row
		}
		require.Len(t, got, 2, "只有 11 与 12 是当天的消费日志:%+v", got)
		assert.EqualValues(t, 300, got[11].ConsumeQuota, "日初与日末最后一秒都要算进来")
		assert.EqualValues(t, 2, got[11].RequestCount)
		assert.EqualValues(t, 0, got[12].ConsumeQuota, "0 额度的用户必须在表里")
		assert.EqualValues(t, 1, got[12].RequestCount)
		_, hasTopupOnly := got[13]
		assert.False(t, hasTopupOnly, "非 type=2 的日志不能进消费报表")
	})

	t.Run("按用户筛选", func(t *testing.T) {
		rows, err := aggregateConsumeFromLogs(context.Background(), r, []int{12})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, 12, rows[0].UserId)
	})

	t.Run("关键词一个都没命中时必须返回空而不是全表", func(t *testing.T) {
		rows, err := aggregateConsumeFromLogs(context.Background(), r, []int{})
		require.NoError(t, err)
		assert.Empty(t, rows, "空的 id 集合是「筛掉了所有人」,不是「不筛选」")
	})
}

// ───────────────────────── 计佣侧聚合 ─────────────────────────

func TestAggregateCommissionByInvitee(t *testing.T) {
	useConfig(t, &config.Config{Enabled: true, Commission: config.Commission{Enabled: true}})
	gdb := newTestDB(t)

	mk := func(seq, inviter, invitee int, day string, base int64, gross string, status, source string) {
		seedAccrual(t, gdb, seq, func(a *Accrual) {
			a.IdemScope, a.IdemKey = source, source+":"+itoa(seq)
			a.SourceType = source
			a.InviterId, a.InviteeId = inviter, invitee
			a.BucketDate = day
			a.BaseQuota = base
			a.GrossAmount = decimal.RequireFromString(gross)
			a.Status = status
		})
	}
	// 上线 1 名下:两天、两个下线。
	mk(1, 1, 101, "20260801", 1000, "50", StatusAccrued, SourceConsume)
	mk(2, 1, 101, "20260802", 2000, "100", StatusAccrued, SourceConsume)
	mk(3, 1, 102, "20260802", 500, "25", StatusAccrued, SourceConsume)
	// 区间外一天:必须不进来。
	mk(4, 1, 101, "20260803", 9999, "999", StatusAccrued, SourceConsume)
	// 作废行:必须不计。
	mk(5, 1, 101, "20260801", 7777, "777", StatusVoided, SourceConsume)
	// 充值返佣:同一区间,但不是消费,必须不进这张消费报表。
	mk(6, 1, 101, "20260801", 8888, "888", StatusAccrued, SourceTopup)
	// 别的上线名下的下线:上线维度筛选要挡住它。
	mk(7, 2, 201, "20260801", 3000, "150", StatusAccrued, SourceConsume)

	r, err := parseDailyRange(newDailyCtx(t, "start_date=20260801&end_date=20260802", 1),
		common.GetTimestamp())
	require.NoError(t, err)

	t.Run("全站视角", func(t *testing.T) {
		got, err := aggregateCommissionByInvitee(r, 0)
		require.NoError(t, err)
		require.Len(t, got, 3, "101 / 102 / 201:%+v", got)
		assert.EqualValues(t, 3000, got[101].BaseQuota,
			"只累加区间内、消费来源、未作废的行")
		assert.Equal(t, "150", got[101].Gross.String())
		assert.EqualValues(t, 500, got[102].BaseQuota)
		assert.EqualValues(t, 3000, got[201].BaseQuota)
	})

	t.Run("上线维度只看自己的下线", func(t *testing.T) {
		got, err := aggregateCommissionByInvitee(r, 1)
		require.NoError(t, err)
		require.Len(t, got, 2)
		_, leaked := got[201]
		assert.False(t, leaked, "别的上线的下线绝不能出现")
	})

	t.Run("区间外的那一天真的在外面", func(t *testing.T) {
		wide, err := parseDailyRange(
			newDailyCtx(t, "start_date=20260801&end_date=20260803", 1), common.GetTimestamp())
		require.NoError(t, err)
		got, err := aggregateCommissionByInvitee(wide, 1)
		require.NoError(t, err)
		assert.EqualValues(t, 3000+9999, got[101].BaseQuota,
			"end_date 是闭区间,20260803 必须被包含进来")
	})
}

// ───────────────────────── 排序 ─────────────────────────

func TestSortDailyConsume(t *testing.T) {
	rows := func() []dailyConsumeRow {
		return []dailyConsumeRow{
			{UserId: 3, ConsumeQuota: 100, RequestCount: 1, CommissionBaseQuota: 100},
			{UserId: 1, ConsumeQuota: 300, RequestCount: 9, CommissionBaseQuota: 0},
			{UserId: 2, ConsumeQuota: 100, RequestCount: 5, CommissionBaseQuota: 50},
		}
	}
	cases := []struct {
		name       string
		sortKey    string
		order      string
		wantUserId []int
	}{
		{"默认按消费额降序,并列按 user_id 升序", "", "", []int{1, 2, 3}},
		{"消费额升序", "consume_quota", "asc", []int{2, 3, 1}},
		{"请求数降序", "request_count", "", []int{1, 2, 3}},
		{"计佣基数降序", "commission_base_quota", "", []int{3, 2, 1}},
		{"user_id 升序", "user_id", "asc", []int{1, 2, 3}},
		{"未知排序键回落到消费额", "no_such_column", "", []int{1, 2, 3}},
		{"order 非 asc 一律当降序", "consume_quota", "garbage", []int{1, 2, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rows()
			sortDailyConsume(got, tc.sortKey, tc.order)
			ids := make([]int, 0, len(got))
			for _, r := range got {
				ids = append(ids, r.UserId)
			}
			assert.Equal(t, tc.wantUserId, ids)
		})
	}

	t.Run("并列次序稳定,翻页不会丢人也不会重复", func(t *testing.T) {
		// 全部并列在 0:只要次序键没接上 user_id,不同页之间就会换位置。
		flat := []dailyConsumeRow{{UserId: 9}, {UserId: 4}, {UserId: 7}, {UserId: 1}}
		sortDailyConsume(flat, "consume_quota", "")
		assert.Equal(t, []int{1, 4, 7, 9},
			[]int{flat[0].UserId, flat[1].UserId, flat[2].UserId, flat[3].UserId},
			"金额全并列时必须退化成 user_id 升序这个全序")
	})
}

// ───────────────────────── 管理端接口 ─────────────────────────

// TestAdminDailyConsumeSurfacesUncountedUsers 是选 logs 当数据源的**理由本身**。
//
// 三种"消费了但计佣表里没有行"的用户必须一个不落地出现在报表里,
// 且各自的未计佣额可以手算复现。只读计佣表的实现会让这三行全部消失。
func TestAdminDailyConsumeSurfacesUncountedUsers(t *testing.T) {
	useConfig(t, &config.Config{Enabled: true, Commission: config.Commission{Enabled: true}})
	extDB := newTestDB(t)
	mainDB := useMainDB(t, &model.User{})
	logDB := useLogDB(t)

	const day = "20260803"
	at := dayTs(t, day, 3600)

	users := []struct {
		id      int
		name    string
		group   string
		inviter int
	}{
		{501, "qy-normal", "default", 500},   // 正常:有上线、有费率
		{502, "qy-zerorate", "qy-free", 500}, // 0% 分组:有上线,但一行计佣都没有
		{503, "qy-noinviter", "default", 0},  // 没有上线
		{504, "qy-fined", "default", 500},    // 有上线,但当天一半消费是违规扣费
	}
	for _, u := range users {
		require.NoError(t, mainDB.Create(&model.User{
			Id: u.id, Username: u.name, DisplayName: u.name, Group: u.group,
			InviterId: u.inviter, AffCode: "aff" + itoa(u.id),
		}).Error)
	}
	require.NoError(t, mainDB.Create(&model.User{Id: 500, Username: "qy-up", AffCode: "aff500"}).Error)

	seedLog(t, logDB, 501, at, 1000, model.LogTypeConsume)
	seedLog(t, logDB, 502, at, 1125, model.LogTypeConsume)
	seedLog(t, logDB, 503, at, 2250, model.LogTypeConsume)
	seedLog(t, logDB, 504, at, 6750, model.LogTypeConsume)
	seedLog(t, logDB, 504, at+1, 25000, model.LogTypeConsume) // 违规扣费,不计佣

	// 计佣表里只有 501 与 504,而且 504 的基数只有没被罚的那部分。
	seedAccrual(t, extDB, 1, func(a *Accrual) {
		a.IdemScope, a.IdemKey, a.SourceType = SourceConsume, "consume:501", SourceConsume
		a.InviterId, a.InviteeId, a.BucketDate = 500, 501, day
		a.BaseQuota, a.GrossAmount = 1000, decimal.NewFromInt(50)
	})
	seedAccrual(t, extDB, 2, func(a *Accrual) {
		a.IdemScope, a.IdemKey, a.SourceType = SourceConsume, "consume:504", SourceConsume
		a.InviterId, a.InviteeId, a.BucketDate = 500, 504, day
		a.BaseQuota, a.GrossAmount = 6750, decimal.NewFromInt(337)
	})

	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/daily-consume?start_date="+day, "", adminListDailyConsume)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items   []dailyConsumeRow `json:"items"`
			Total   int               `json:"total"`
			Summary struct {
				ConsumeQuota        int64 `json:"consume_quota"`
				CommissionBaseQuota int64 `json:"commission_base_quota"`
				UncountedQuota      int64 `json:"uncounted_quota"`
			} `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))

	byId := map[int]dailyConsumeRow{}
	for _, it := range resp.Data.Items {
		byId[it.UserId] = it
	}
	require.Len(t, byId, 4, "四个消费用户一个都不能少:%+v", byId)

	// 每一行都手算:消费额来自 logs,计佣基数来自 accrual,差额是两者之差。
	for _, want := range []struct {
		id            int
		consume, base int64
		uncounted     int64
		hasCommission bool
	}{
		{501, 1000, 1000, 0, true},
		{502, 1125, 0, 1125, false},     // 0% 分组:全额未计佣
		{503, 2250, 0, 2250, false},     // 没有上线:全额未计佣
		{504, 31750, 6750, 25000, true}, // 违规扣费那 25000 未计佣
	} {
		row := byId[want.id]
		assert.EqualValues(t, want.consume, row.ConsumeQuota, "user %d 消费额", want.id)
		assert.EqualValues(t, want.base, row.CommissionBaseQuota, "user %d 计佣基数", want.id)
		assert.EqualValues(t, want.uncounted, row.UncountedQuota, "user %d 未计佣额", want.id)
		assert.Equal(t, want.hasCommission, row.HasCommission, "user %d 是否有计佣", want.id)
	}
	assert.Equal(t, "qy-zerorate", byId[502].Username, "管理端要给真实用户名")
	assert.Equal(t, "qy-up", byId[501].InviterUsername, "上线名要补上")
	assert.Empty(t, byId[503].InviterUsername, "没有上线的人不能凭空补出一个上线")

	assert.EqualValues(t, 1000+1125+2250+31750, resp.Data.Summary.ConsumeQuota)
	assert.EqualValues(t, 1000+6750, resp.Data.Summary.CommissionBaseQuota)
	assert.EqualValues(t, 1125+2250+25000, resp.Data.Summary.UncountedQuota)
}

// TestAdminDailyConsumeRejectsUnboundedRange 守住区间上界。
//
// 上界失守不会有任何报错,只会有一条在主库上跑几分钟的聚合 —— 而实测
// 7 天不带索引就要 9 分钟,那正是"上线就打挂主库"的形状。
func TestAdminDailyConsumeRejectsUnboundedRange(t *testing.T) {
	useConfig(t, &config.Config{Enabled: true, Commission: config.Commission{Enabled: true}})
	newTestDB(t)
	useMainDB(t, &model.User{})
	useLogDB(t)

	cases := []struct {
		name, query, wantCode string
	}{
		{"超过 31 天", "start_date=20260101&end_date=20261231", "qy_daily_range_too_big"},
		{"首尾颠倒", "start_date=20260803&end_date=20260801", "qy_daily_range_order"},
		{"日期格式非法", "start_date=08/01/2026", "qy_daily_range_format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAdminHandler(t, http.MethodGet,
				"/api/qy/admin/commission/daily-consume?"+tc.query, "", adminListDailyConsume)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			var resp struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
			}
			require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
			assert.False(t, resp.Success)
			assert.Equal(t, tc.wantCode, resp.Code)
		})
	}
}

// ───────────────────────── 用户端接口:越权与脱敏 ─────────────────────────

// TestInviteeDailyIsScopedAndMasked 守两件事:上线只看得见自己的下线,
// 而且看到的是脱敏名 —— 真实用户名、邮箱、user_id 一个都不下发。
func TestInviteeDailyIsScopedAndMasked(t *testing.T) {
	useConfig(t, &config.Config{Enabled: true, Commission: config.Commission{Enabled: true}})
	extDB := newTestDB(t)
	useHealthyExtDB(t)

	const day = "20260803"
	seedAccrual(t, extDB, 1, func(a *Accrual) {
		a.IdemScope, a.IdemKey, a.SourceType = SourceConsume, "consume:601", SourceConsume
		a.InviterId, a.InviteeId, a.BucketDate = 600, 601, day
		a.BaseQuota, a.GrossAmount = 5000, decimal.NewFromInt(250)
	})
	seedAccrual(t, extDB, 2, func(a *Accrual) {
		a.IdemScope, a.IdemKey, a.SourceType = SourceConsume, "consume:701", SourceConsume
		a.InviterId, a.InviteeId, a.BucketDate = 700, 701, day
		a.BaseQuota, a.GrossAmount = 9999, decimal.NewFromInt(999)
	})
	require.NoError(t, extDB.Create(&InviteRelation{
		InviteeId: 601, InviterId: 600,
		MaskedName: "zh***an", InviteeRef: "abcdef123456",
	}).Error)

	call := func(inviterId int) map[string]any {
		rec := callUserHandler(t, "start_date="+day, inviterId, listMyInviteeDailyConsume)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
		return resp.Data
	}

	t.Run("只看得见自己名下的下线", func(t *testing.T) {
		data := call(600)
		items, _ := data["items"].([]any)
		require.Len(t, items, 1, "上线 600 名下只有 601 一个下线")
		row, _ := items[0].(map[string]any)
		assert.Equal(t, "zh***an", row["masked_name"])
		assert.Equal(t, "abcdef123456", row["ref"])
		assert.EqualValues(t, 5000, row["base_quota"])
		// 下发的字段集合本身就是隐私边界:多一个 user_id 就是把 id 空间漏出去。
		for _, forbidden := range []string{"user_id", "invitee_id", "username", "email", "consume_quota"} {
			_, leaked := row[forbidden]
			assert.Falsef(t, leaked, "用户端不能下发 %s", forbidden)
		}
	})

	t.Run("换一个上线就看不到别人的下线", func(t *testing.T) {
		data := call(700)
		items, _ := data["items"].([]any)
		require.Len(t, items, 1)
		row, _ := items[0].(map[string]any)
		assert.EqualValues(t, 9999, row["base_quota"], "700 只看得到 701")
	})

	t.Run("没有下线的人拿到空表而不是全站", func(t *testing.T) {
		data := call(9999)
		items, _ := data["items"].([]any)
		assert.Empty(t, items)
	})
}
