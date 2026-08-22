package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// token_usage_api_test.go —— 密钥页「今日消耗」那一列的口径。
//
// 这一列会被用户拿去对账,所以能出错的地方只有三处,而三处全是静默的:
//
//	① 日界不走 dayline —— 数字看起来完全正常,只是把凌晨那几笔算到了昨天,
//	   而日消费明细算今天。两张页面都不会报错。
//	② 别人的行 / 非消费类型的行混进来 —— 同上,只是数字偏大。
//	③ 超过行数上界之后**截断** —— 被截掉的密钥在界面上显示「今日 0」,
//	   那是一个看起来完全正常的金额。

// seedTokenLog 插一条带 token_id 的日志。
//
// 与 seedLog 分开而不是给它加参数:seedLog 已经被四个测试文件用着,
// 加一个所有调用点都传 0 的参数只会让那四处更难读。
func seedTokenLog(t *testing.T, gdb *gorm.DB, userId, tokenId int, at int64, quota int, logType int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.Log{
		UserId:    userId,
		TokenId:   tokenId,
		CreatedAt: at,
		Type:      logType,
		Quota:     quota,
		ModelName: "qy-test",
	}).Error)
}

// TestTokenDayUsageGroupsByTokenWithinTheConsumeDayline 是这一列的主用例。
//
// 口径固定成 UTC+8(day_offset_minutes: 480),于是"按 UTC 分天"与"按 dayline
// 分天"会给出**不同的**答案:8/4 04:00(UTC+8)= 8/3 20:00 UTC。按 UTC 它属于
// 昨天,按本站的消费日界它属于今天。
//
// 期望(独立算出来的):
//
//	令牌 71  1000(8/4 04:00,UTC 侧是 8/3) + 500(8/4 10:00) = 1500
//	令牌 72  250
//	令牌 73  当天只有一条 type=1 充值日志            → 不出现
//	令牌 74  是**别人**的令牌,同一天花了 9999        → 不出现
//	token_id = 0(后台任务/渠道测试)                  → 不出现
//	令牌 75  前一天 23:59(UTC+8)花了 800            → 不出现
//	令牌 76  次日 00:00(UTC+8)整点花了 900          → 不出现(右开区间)
//
// 变异验证见文件末尾那一段注释。
func TestTokenDayUsageGroupsByTokenWithinTheConsumeDayline(t *testing.T) {
	cfg := &config.Config{Enabled: true}
	cfg.Commission.Enabled = true
	cfg.Commission.DayOffsetMinutes = 480 // UTC+8
	useConfig(t, cfg)
	logDB := useLogDB(t)

	const me, other = 701, 702
	const day = "20260804"
	dayStartTs := dayTs(t, day, 0)
	dayEndTs := dayStartTs + ConsumeDaySeconds

	seedTokenLog(t, logDB, me, 71, dayTs(t, day, 4*3600), 1000, model.LogTypeConsume)
	seedTokenLog(t, logDB, me, 71, dayTs(t, day, 10*3600), 500, model.LogTypeConsume)
	seedTokenLog(t, logDB, me, 72, dayTs(t, day, 12*3600), 250, model.LogTypeConsume)
	seedTokenLog(t, logDB, me, 73, dayTs(t, day, 12*3600), 7777, model.LogTypeTopup)
	seedTokenLog(t, logDB, other, 74, dayTs(t, day, 12*3600), 9999, model.LogTypeConsume)
	seedTokenLog(t, logDB, me, 0, dayTs(t, day, 12*3600), 4321, model.LogTypeConsume)
	seedTokenLog(t, logDB, me, 75, dayStartTs-1, 800, model.LogTypeConsume)
	seedTokenLog(t, logDB, me, 76, dayEndTs, 900, model.LogTypeConsume)

	got, err := TokenDayUsage(context.Background(), me, dayStartTs, dayEndTs)
	require.NoError(t, err)
	assert.Equal(t, map[int]int64{71: 1500, 72: 250}, got)

	// 8/4 04:00(UTC+8)在 UTC 日历上是 8/3 —— 上面那 1000 就是靠它证明
	// 窗口走的是 dayline 而不是 UTC 午夜。这一行钉住那个前提,否则哪天
	// 有人把偏移改回 0,主断言会在完全不变的情况下失去它要证明的东西。
	require.Equal(t, day, dayKey(dayTs(t, day, 4*3600)))
	require.NotEqual(t, day, utcDayKeyForTest(dayTs(t, day, 4*3600)))
}

// TestTokenDayUsageZeroSumTokenStaysInTheMap 把「缺席」与「合计为 0」分开。
//
// 倍率 0 的免费分组会产生 quota=0 的消费日志。它必须**出现在结果里**且值为 0:
// 调用方据此知道"这把密钥今天真的发过请求",与"今天一次都没用过"是两件事。
// 用户看到的都是 0,但接口不能把两者压成同一个形状 —— 压了之后想区分就再也
// 区分不出来。
//
// 变异验证:把 Scan 之后那个 `if r.TokenId <= 0` 改成 `if r.ConsumeQuota <= 0`
// → 令牌 81 从结果里消失,断言红。
func TestTokenDayUsageZeroSumTokenStaysInTheMap(t *testing.T) {
	cfg := &config.Config{Enabled: true}
	cfg.Commission.Enabled = true
	useConfig(t, cfg)
	logDB := useLogDB(t)

	const me = 711
	const day = "20260805"
	start := dayTs(t, day, 0)
	end := start + ConsumeDaySeconds

	seedTokenLog(t, logDB, me, 81, dayTs(t, day, 3600), 0, model.LogTypeConsume)

	got, err := TokenDayUsage(context.Background(), me, start, end)
	require.NoError(t, err)
	assert.Equal(t, map[int]int64{81: 0}, got, "合计为 0 的令牌必须在表里,而且值是 0")
	_, absent := got[82]
	assert.False(t, absent, "今天没用过的令牌不该凭空出现")
}

// TestTokenDayUsageRefusesToTruncate 守「宁可报错也不给半张表」。
//
// 恰好 maxTokenDayUsageRows 把令牌必须成功;多一把就必须报错。
// 截断的表现是那多出来的密钥在界面上显示「今日 0」—— 一个看起来完全正常的
// 金额。与 maxDailyConsumeRows 同一条理由。
//
// 变异验证:把 `Limit(maxTokenDayUsageRows + 1)` 改回 `Limit(maxTokenDayUsageRows)`
// → 超限那一档查回来正好 1000 行,len(rows) > 上界不成立,函数返回一张少了一把
// 密钥的表,require.Error 红。
func TestTokenDayUsageRefusesToTruncate(t *testing.T) {
	cases := []struct {
		name      string
		tokens    int
		wantError bool
	}{
		{name: "恰好到上界", tokens: maxTokenDayUsageRows, wantError: false},
		{name: "超过上界一把", tokens: maxTokenDayUsageRows + 1, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Enabled: true}
			cfg.Commission.Enabled = true
			useConfig(t, cfg)
			logDB := useLogDB(t)

			const me = 721
			const day = "20260806"
			start := dayTs(t, day, 0)
			end := start + ConsumeDaySeconds

			rows := make([]*model.Log, 0, tc.tokens)
			for i := 1; i <= tc.tokens; i++ {
				rows = append(rows, &model.Log{
					UserId: me, TokenId: i, CreatedAt: start + 60,
					Type: model.LogTypeConsume, Quota: 1, ModelName: "qy-test",
				})
			}
			require.NoError(t, logDB.CreateInBatches(rows, 500).Error)

			got, err := TokenDayUsage(context.Background(), me, start, end)
			if tc.wantError {
				require.Error(t, err, "超过上界必须报错,不能返回一张少了密钥的表")
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tc.tokens)
		})
	}
}

// TestConsumeDayStartIsTheSameDayAsDailyConsumeReports 钉住导出的日界与
// 日消费明细逐位同源。
//
// 密钥页那一列与日消费明细是同一个用户的同一笔钱。它们各自算一次「今天」,
// 差一个小时的表现是:密钥页说今天花了 100,日消费明细里今天那一格是 0 ——
// 而两边都不会报错。
//
// 变异验证:把 ConsumeDayStart 改成 `ts - ts%86400`(UTC 午夜,丢掉偏移)
// → 在 UTC+8 下 8/4 04:00 的日键从 20260804 变成 20260803,两条断言全红。
func TestConsumeDayStartIsTheSameDayAsDailyConsumeReports(t *testing.T) {
	cfg := &config.Config{Enabled: true}
	cfg.Commission.Enabled = true
	cfg.Commission.DayOffsetMinutes = 480 // UTC+8
	useConfig(t, cfg)

	// 8/4 04:00(UTC+8)= 8/3 20:00 UTC:两种口径落在不同的自然日上。
	now := dayTs(t, "20260804", 4*3600)

	start := ConsumeDayStart(now)
	// 日消费明细取"今天"那一格时走的正是 dayKey → dayKeyStart 这一条链。
	wantStart, ok := dayKeyStart(dayKey(now))
	require.True(t, ok)
	assert.Equal(t, wantStart, start, "「今日消耗」的窗口起点必须等于日消费明细里今天那一格的起点")
	assert.Equal(t, dayKey(now), dayKey(start), "窗口起点必须仍属于今天")
	assert.Equal(t, 480, ConsumeDayOffsetMinutes(), "下发给界面的偏移必须是真正在用的那一个")
	assert.EqualValues(t, 86400, ConsumeDaySeconds)
}

// TestDroppableRetiredLogsIndexes 守「先建后删」这条顺序。
//
// 退役索引只有在取代它的那条**真的建好之后**才允许删。反过来会开出一段
// 两条索引都没有的窗口:那段时间里按天下钻要跑 6.5 秒(见 logs_index.go),
// 而且没有任何一处会说明为什么突然变慢。
//
// 变异验证:把 droppableRetiredLogsIndexes 里的 `if !ready(...) { continue }`
// 删掉(无条件返回全部退役索引)→ "超集还没建好" 那一档从 Empty 变成 1 条,断言红。
func TestDroppableRetiredLogsIndexes(t *testing.T) {
	require.NotEmpty(t, retiredLogsIndexes, "退役清单空了,这条守卫就没有被守的东西")

	cases := []struct {
		name  string
		ready map[string]bool
		want  []string
	}{
		{
			name:  "超集还没建好:一条都不能删",
			ready: map[string]bool{},
			want:  nil,
		},
		{
			name:  "超集已就绪:退役的那条可以删",
			ready: map[string]bool{logsTokenDailyIndex: true},
			want:  []string{logsUserDailyIndex},
		},
		{
			name:  "只有主表那条就绪也不算数",
			ready: map[string]bool{logsDailyConsumeIndex: true},
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := droppableRetiredLogsIndexes(func(name string) bool { return tc.ready[name] })
			assert.Equal(t, tc.want, nilIfEmpty(got))
		})
	}
}

// TestLogsTokenDailyIndexCoversTheTokenAggregation 钉住覆盖索引的列集合。
//
// 这条聚合的全部性能都建立在「Select 的列一个不落地在索引里」这件事上:
// 备份库实测 token_id 不在索引里时同一条查询 3.9 秒、在里面时 0.23 秒。
// 而"有人给 Select 加了一列"是一次纯粹的可读性改动,不会有任何测试变红 ——
// 除了这一条。
//
// 变异验证:从 logsIndexSpecs 里 idx_qy_logs_token_daily 的 cols 中去掉
// "token_id" → 断言红。
func TestLogsTokenDailyIndexCoversTheTokenAggregation(t *testing.T) {
	var spec *logsIndexSpec
	for _, s := range logsIndexSpecs {
		if s.name == logsTokenDailyIndex {
			spec = s
		}
	}
	require.NotNil(t, spec, "覆盖索引不在补建清单里,那这条聚合永远走不上它")
	// TokenDayUsage 的 WHERE + GROUP BY + SELECT 一共只碰这五列。
	assert.Equal(t,
		[]string{"user_id", "type", "created_at", "token_id", "quota"},
		spec.cols,
		"等值列(user_id,type)必须排在范围列(created_at)前面,"+
			"分组列与被聚合列必须都在索引里,否则回表")
}

// nilIfEmpty 让"什么都不能删"在断言里有一个稳定的形状。
func nilIfEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return in
}

// utcDayKeyForTest 按 UTC 午夜算日键,只用来证明测试里那个时刻确实跨了自然日。
func utcDayKeyForTest(ts int64) string {
	prev := qyConfig.Load()
	cfg := *prev
	cfg.Commission.DayOffsetMinutes = 0
	qyConfig.Store(&cfg)
	defer qyConfig.Store(prev)
	return dayKey(ts)
}

// TestLogsIndexDDLNeverBlocksLogWritesOnPostgres 钉住 PG 上那两条 DDL 的写法。
//
// 被守的不是"语句合法",是**在 hot path 上会不会把 logs 的写入停掉**:
//
//   - PG 的普通 CREATE INDEX 对目标表取 SHARE 锁,整段构建期间该表写入排队。
//     而 logs 的写入就在每一次 relay 消费日志的路径上(model/log.go 的
//     createLog 是裸的 LOG_DB.Create,没有 WithContext、没有超时),被挡住的
//     INSERT 是无限等待而不是报错返回。备份库实测(PG 17.6,logs 300 万行):
//     建索引 6421 ms,跨过窗口的那一条单行 INSERT 6288 ms(基线 98~155 ms)。
//   - DROP INDEX 要 ACCESS EXCLUSIVE,它排队等一个 25 秒的慢查询时,后到的
//     INSERT 被挡 22067 ms —— 停摆时长由 logs 上最慢的并发读封顶,与索引
//     大小无关。
//
// MySQL 8 走 online DDL 不挡 DML(同一条索引实测 15253 ms,707 次并发 INSERT
// 最差 220 ms,零阻塞),SQLite 单写者本来就没有这个问题,ClickHouse 不建。
// 所以 CONCURRENTLY 只能出现在 PG 这一支上,而且**只能**出现在这一支上 ——
// MySQL 不认这个关键字,写上去是每 5 分钟一次的语法错误。
func TestLogsIndexDDLNeverBlocksLogWritesOnPostgres(t *testing.T) {
	spec := logsIndexSpecByName(logsTokenDailyIndex)
	require.NotNil(t, spec, "被守的索引不见了")

	t.Run("建索引", func(t *testing.T) {
		cases := []struct {
			name        string
			dbType      common.DatabaseType
			supported   bool
			mustContain []string
			mustNotHave []string
		}{
			{
				name:      "PostgreSQL 必须 CONCURRENTLY,否则整段构建期间 logs 写不进去",
				dbType:    common.DatabaseTypePostgreSQL,
				supported: true,
				mustContain: []string{
					"CREATE INDEX CONCURRENTLY IF NOT EXISTS " + logsTokenDailyIndex,
					`ON "logs"`,
					`"token_id"`,
				},
			},
			{
				name:        "MySQL 不认 CONCURRENTLY,写上去就是每 5 分钟一次语法错误",
				dbType:      common.DatabaseTypeMySQL,
				supported:   true,
				mustContain: []string{"CREATE INDEX " + logsTokenDailyIndex, "`logs`", "`token_id`"},
				mustNotHave: []string{"CONCURRENTLY"},
			},
			{
				name:        "SQLite 同样不认",
				dbType:      common.DatabaseTypeSQLite,
				supported:   true,
				mustContain: []string{"CREATE INDEX " + logsTokenDailyIndex, "`logs`"},
				mustNotHave: []string{"CONCURRENTLY"},
			},
			{
				name:      "ClickHouse 这两条索引本来就不建",
				dbType:    common.DatabaseTypeClickHouse,
				supported: false,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				ddl, ok := logsIndexDDLFor(tc.dbType, spec)
				require.Equal(t, tc.supported, ok)
				if !ok {
					assert.Equal(t, "", ddl)
					return
				}
				for _, want := range tc.mustContain {
					assert.Contains(t, ddl, want)
				}
				for _, unwanted := range tc.mustNotHave {
					assert.NotContains(t, ddl, unwanted)
				}
			})
		}
	})

	t.Run("删索引", func(t *testing.T) {
		ddl, ok := logsIndexDropDDLFor(common.DatabaseTypePostgreSQL, logsUserDailyIndex)
		require.True(t, ok, "PG 必须走自己的 DROP 语句,不能落到 Migrator().DropIndex")
		assert.Equal(t, "DROP INDEX CONCURRENTLY IF EXISTS "+logsUserDailyIndex, ddl)

		for _, dbType := range []common.DatabaseType{
			common.DatabaseTypeMySQL,
			common.DatabaseTypeSQLite,
			common.DatabaseTypeClickHouse,
		} {
			_, ok := logsIndexDropDDLFor(dbType, logsUserDailyIndex)
			assert.False(t, ok, "%s 上仍然走 GORM Migrator().DropIndex", dbType)
		}
	})

	t.Run("无效索引的判据必须看 indisvalid,而不是只看索引在不在", func(t *testing.T) {
		// CONCURRENTLY 建到一半失败会在 pg_class 里留下一条 indisvalid=false
		// 的索引:HasIndex 说"在",查询规划器不用它。只看 HasIndex 的话补建
		// 任务从此认为已经建好,而报表永远是慢的,既没有告警也没有自愈路径。
		assert.Contains(t, logsIndexUsableSQL, "indisvalid")
		assert.Contains(t, logsIndexUsableSQL, "indisready")
		assert.Contains(t, logsIndexUsableSQL, "pg_index")
	})
}
