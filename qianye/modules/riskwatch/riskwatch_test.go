package riskwatch

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qydb "github.com/QuantumNous/new-api/qianye/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// riskwatch_test.go —— 本模块被要求守住的不变量。
//
// 这些用例分成两半,合在一个文件里是刻意的(AGENTS.md:一个功能不要把用例
// 摊到每一层各建一个文件):
//
//	纯函数半   作用域闸、时间窗、上下文清洗、入参校验
//	真库半     名额预留(抽满自动停止),它只活在一条带条件的原子 UPDATE 里,
//	           纯函数测不到 —— 把 persist 换成"先 COUNT 再 INSERT"照样全绿
//
// 真库那一半跑 sqlite。生产的存储节点固定是 MySQL / PostgreSQL,所以断言一律
// 只依赖跨库通用语义(带 WHERE 的条件 UPDATE、RowsAffected、事务回滚)。

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// newTestStore 建一个存储节点测试实例并接到 db.Watch()。
func newTestStore(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_riskwatch.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// sqlite 不支持 FOR UPDATE。本模块自己不用行锁,但 GORM 的子句构造器是
	// 全局的,清掉它可以避免别的包留下的构造器在这里生效。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(&Task{}, &Capture{}))

	restore := qydb.SetWatchHandleForTest(gdb)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled: true,
		RiskWatch: config.RiskWatch{
			Enabled:         true,
			Database:        config.Database{DSN: "u:p@tcp(127.0.0.1:3306)/qy_rw"},
			CaptureMaxChars: 4000,
			RetentionDays:   30,
			SnapshotSeconds: 30,
		},
	})
	t.Cleanup(func() {
		restore()
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

func seedTask(t *testing.T, gdb *gorm.DB, row Task) Task {
	t.Helper()
	now := common.GetTimestamp()
	if row.Status == "" {
		row.Status = StatusRunning
	}
	if row.SampleBps == 0 {
		row.SampleBps = 10000
	}
	row.CreatedAt, row.UpdatedAt, row.Version = now, now, 1
	require.NoError(t, gdb.Create(&row).Error)
	return row
}

// ─────────────────────── 名额预留:抽满自动停止 ───────────────────────

// 条数上限必须由落库那一步兜住,而不是由内存快照兜住。
//
// 这是这个功能唯一一条"运营在页面上写下的承诺":设了 3 条就只该有 3 条。
// 靠快照的话,多个 worker(以及多节点)会各自看到同一份"还没满"的快照并同时
// 插入 —— 表现是"我明明设了 500 条,库里有 623 条",一个没人能解释、
// 也没办法回滚的数字。
func TestReservationStopsExactlyAtMaxRecords(t *testing.T) {
	gdb := newTestStore(t)
	task := seedTask(t, gdb, Task{Name: "盯 U1", TargetUserId: 1, MaxRecords: 3})

	ctx := context.Background()
	accepted := 0
	for i := 0; i < 10; i++ {
		row := Capture{UserId: 1, CreatedAt: common.GetTimestamp()}
		ok, err := persist(ctx, gdb, task.Id, &row)
		require.NoError(t, err)
		if ok {
			accepted++
		}
	}

	assert.Equal(t, 3, accepted, "预留必须在第 3 条之后全部失败")

	var rows int64
	require.NoError(t, gdb.Model(&Capture{}).Where("task_id = ?", task.Id).Count(&rows).Error)
	assert.EqualValues(t, 3, rows, "落库行数必须与被接受的名额数一致")

	var got Task
	require.NoError(t, gdb.Where("id = ?", task.Id).Take(&got).Error)
	assert.Equal(t, 3, got.Captured, "计数不得超过上限 —— 它就是闸门本身")
}

// max_records = 0 表示不限,预留必须一直成功。
//
// 与上一条是同一个 SQL 的两个分支。写反的话(把 0 当成"上限是 0"),
// 一个不限条数的监听任务会一条都抓不到,而界面上它一直显示"运行中"。
func TestReservationIsUnboundedWhenMaxRecordsIsZero(t *testing.T) {
	gdb := newTestStore(t)
	task := seedTask(t, gdb, Task{Name: "长期盯 U2", TargetUserId: 2, MaxRecords: 0})

	for i := 0; i < 5; i++ {
		row := Capture{UserId: 2, CreatedAt: common.GetTimestamp()}
		ok, err := persist(context.Background(), gdb, task.Id, &row)
		require.NoError(t, err)
		require.True(t, ok, "不限条数的任务不该被预留挡下")
	}
}

// 已经停止的任务不再接受任何预留。
//
// 它是"我明明停了它还在记"这句用户反馈的最后一道防线:快照最多晚
// snapshot_seconds 生效,而这条 WHERE 是立即的。
func TestReservationRefusesStoppedTask(t *testing.T) {
	gdb := newTestStore(t)
	task := seedTask(t, gdb, Task{Name: "已停", TargetUserId: 3, Status: StatusStopped})

	row := Capture{UserId: 3, CreatedAt: common.GetTimestamp()}
	ok, err := persist(context.Background(), gdb, task.Id, &row)
	require.NoError(t, err)
	assert.False(t, ok)

	var rows int64
	require.NoError(t, gdb.Model(&Capture{}).Count(&rows).Error)
	assert.EqualValues(t, 0, rows, "抢不到名额时一行都不该落库")
}

// 抽满之后 writeCapture 必须把任务改成 finished,而且原因是 max_records。
//
// 状态与原因分开落库,是因为"抽满 500 条"与"到点了才抓到 37 条"在事后是完全
// 不同的结论 —— 前者往往意味着这个账号的行为比预期密集得多。
func TestWriteCaptureFinishesTheTaskWhenFull(t *testing.T) {
	gdb := newTestStore(t)
	task := seedTask(t, gdb, Task{Name: "满了就停", TargetUserId: 4, MaxRecords: 1})
	wt := &watchTask{Id: task.Id, UserId: 4, SampleBps: 10000, MaxRecords: 1}

	ctx := context.Background()
	require.NoError(t, writeCapture(ctx, wt, &Capture{UserId: 4, CreatedAt: common.GetTimestamp()}))
	require.NoError(t, writeCapture(ctx, wt, &Capture{UserId: 4, CreatedAt: common.GetTimestamp()}))

	var got Task
	require.NoError(t, gdb.Where("id = ?", task.Id).Take(&got).Error)
	assert.Equal(t, StatusFinished, got.Status)
	assert.Equal(t, ReasonMaxRecords, got.StoppedReason)
}

// 到期巡检把窗口已过的运行中任务改成 expired,并且不碰手动停止的那些。
//
// 它解决的是**状态可见性**:零流量的站点上,一个昨天到期的任务永远不会被热路径
// 看到一眼,而管理端会一直显示"运行中"。
func TestSweepWindowsExpiresOnlyRunningTasksPastTheirWindow(t *testing.T) {
	gdb := newTestStore(t)
	now := common.GetTimestamp()
	past := seedTask(t, gdb, Task{Name: "已到期", TargetUserId: 5, WindowMode: WindowRange, EndsAt: now - 60})
	future := seedTask(t, gdb, Task{Name: "还没到", TargetUserId: 6, WindowMode: WindowRange, EndsAt: now + 3600})
	manual := seedTask(t, gdb, Task{Name: "手动停的", TargetUserId: 7, WindowMode: WindowRange,
		EndsAt: now - 60, Status: StatusStopped, StoppedReason: ReasonManual})

	SweepWindows(context.Background())

	// 每次都取一个新的目标结构体:GORM 会把上一次查询留在结构体里的主键
	// 当成额外的 WHERE 条件带上,复用一个变量会让第二条断言查一个不存在的组合。
	reload := func(id int64) Task {
		var row Task
		require.NoError(t, gdb.Where("id = ?", id).Take(&row).Error)
		return row
	}

	expired := reload(past.Id)
	assert.Equal(t, StatusExpired, expired.Status)
	assert.Equal(t, ReasonWindowEnd, expired.StoppedReason)

	assert.Equal(t, StatusRunning, reload(future.Id).Status, "窗口还没过的任务不该被动")

	assert.Equal(t, ReasonManual, reload(manual.Id).StoppedReason,
		"手动停止的任务不该被改写成 window_end —— 那会让「谁停的」失去答案")
}

// 清理只删过期的行,expires_at = 0(永久保留)的一行都不碰。
func TestPruneCapturesKeepsPermanentRows(t *testing.T) {
	gdb := newTestStore(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&Capture{TaskId: 1, CreatedAt: now - 100, ExpiresAt: now - 10}).Error)
	require.NoError(t, gdb.Create(&Capture{TaskId: 1, CreatedAt: now - 100, ExpiresAt: now + 3600}).Error)
	require.NoError(t, gdb.Create(&Capture{TaskId: 1, CreatedAt: now - 100, ExpiresAt: 0}).Error)

	PruneCaptures(context.Background())

	var left []Capture
	require.NoError(t, gdb.Order("id").Find(&left).Error)
	require.Len(t, left, 2)
	assert.EqualValues(t, now+3600, left[0].ExpiresAt)
	assert.EqualValues(t, 0, left[1].ExpiresAt, "永久保留的行必须原样留着")
}

// 快照只装运行中的任务,并把"没填保留期"折算成全局默认。
func TestReloadSnapshotResolvesRetentionAndSkipsStoppedTasks(t *testing.T) {
	gdb := newTestStore(t)
	own := 7
	seedTask(t, gdb, Task{Name: "跟全局", TargetUserId: 11})
	seedTask(t, gdb, Task{Name: "自己配", TargetUserId: 12, RetentionDays: &own})
	seedTask(t, gdb, Task{Name: "停了的", TargetUserId: 13, Status: StatusStopped})

	require.NoError(t, reloadCtx(context.Background()))
	tasks := Snapshot().tasks
	require.Len(t, tasks, 2, "已停止的任务不该进快照")
	assert.Equal(t, 30, tasks[0].RetentionDays, "没填保留期的任务吃全局默认")
	assert.Equal(t, 7, tasks[1].RetentionDays, "填了的按自己那一份")
}

// ─────────────────────── 作用域与时间窗(纯函数) ───────────────────────

// 作用域三格空 = 不限,填了就必须全部对上。
//
// 判据写成"任意一格不匹配即出局"而不是"任意一格匹配即命中":后者会让一个
// 「vip 分组 + gpt-4」的任务盯上全部 vip 用户,包括他们用别的模型的请求。
func TestScopeMatchesOnEveryFilledSlot(t *testing.T) {
	cases := []struct {
		name  string
		task  watchTask
		user  int
		group string
		model string
		want  bool
	}{
		{"只盯用户:命中", watchTask{UserId: 7}, 7, "vip", "gpt-4", true},
		{"只盯用户:别人不中", watchTask{UserId: 7}, 8, "vip", "gpt-4", false},
		{"只盯分组:命中", watchTask{Group: "vip"}, 9, "vip", "gpt-4", true},
		{"只盯分组:别的分组不中", watchTask{Group: "vip"}, 9, "default", "gpt-4", false},
		{"分组+模型:两格都对才中", watchTask{Group: "vip", Model: "gpt-4"}, 9, "vip", "gpt-4", true},
		{"分组+模型:模型不对不中", watchTask{Group: "vip", Model: "gpt-4"}, 9, "vip", "claude-3", false},
		{"三格全空:谁都中", watchTask{}, 1, "default", "x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.task.matches(tc.user, tc.group, tc.model))
		})
	}
}

// 时间窗与条数上限都由 alive 一处判定。
func TestAliveCoversWindowAndQuota(t *testing.T) {
	const now = 1_000_000
	cases := []struct {
		name string
		task watchTask
		want bool
	}{
		{"永久监听", watchTask{}, true},
		{"还没到开始时间", watchTask{StartsAt: now + 1}, false},
		{"正好到开始时间", watchTask{StartsAt: now}, true},
		{"已过结束时间", watchTask{EndsAt: now}, false},
		{"还没到结束时间", watchTask{EndsAt: now + 1}, true},
		{"已抽满", watchTask{MaxRecords: 3, Captured: 3}, false},
		{"没抽满", watchTask{MaxRecords: 3, Captured: 2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.task.alive(now))
		})
	}
}

// 抽样是字面意思:0 恒不抓,10000 恒抓。
//
// 只钉两个端点,不去统计中间那些值 —— 那种"跑一万次看比例"的用例是随机输入的
// 假测试,而两个端点恰恰是唯一会被写错的地方(把 0 当成"不限"、把 10000 当成
// "还要再摇一次")。
func TestSampleEndpointsAreLiteral(t *testing.T) {
	assert.False(t, sample(0), "概率 0 必须一条都不抓")
	assert.False(t, sample(-1), "负数与 0 同义")
	assert.True(t, sample(10000), "概率 100% 必须每条都抓")
}

// ─────────────────────── 上下文清洗 ───────────────────────

// 内联二进制必须在**截断之前**被剥掉。
//
// 顺序反过来的后果不是"少剥一点":一张 base64 图片能把整条记录占满,
// 先截的结果是正文一个字都没留下,而管理员看到的是一条什么都看不出的记录。
func TestSanitizeStripsInlineBinaryBeforeTruncating(t *testing.T) {
	img := "data:image/png;base64," + strings.Repeat("A", 4096)
	got := sanitizeText("请看这张图 "+img+" 帮我改一下", 200)

	assert.NotContains(t, got.Text, strings.Repeat("A", 64), "base64 本体一个字节都不该入库")
	assert.Contains(t, got.Text, "image/png", "描述符要保留 MIME —— 「这里原本有一张图」是有用的信息")
	assert.Contains(t, got.Text, "帮我改一下", "剥离之后正文必须还在,而不是被图片挤掉")
	assert.False(t, got.Truncated, "剥完之后已经短于上限,不该再标截断")
}

// 凭证一律抹掉,而个人标识符原样保留。
//
// 这是本模块与违规检测**刻意不同**的一条策略(见 sanitize.go 的文件头):
// 定向取证里手机号、邮箱常常就是证据本身,而一把被粘进提示词的 API key
// 对研判毫无价值,却会让这个库变成凭证仓库。
func TestSanitizeRedactsCredentialsButKeepsIdentifiers(t *testing.T) {
	got := sanitizeText("我的 key 是 sk-abcdefghijklmnopqrstuvwxyz012345,"+
		"发到 victim@example.com 和 13800138000", 4000)

	assert.NotContains(t, got.Text, "sk-abcdefghijklmnopqrstuvwxyz012345", "凭证必须被抹掉")
	assert.Contains(t, got.Text, "«credential»")
	assert.Contains(t, got.Text, "victim@example.com", "邮箱是证据,不脱敏")
	assert.Contains(t, got.Text, "13800138000", "手机号是证据,不脱敏")
}

// 超长正文掐头去尾保留,而且按 rune 计。
//
// 按字节截会在中文上只留下三分之一,还可能把一个字截成半个;截尾则会把
// 一次刷接口的载荷(通常在末尾)整段丢掉,而那正是要看的东西。
func TestSanitizeClipsHeadAndTailByRunes(t *testing.T) {
	body := strings.Repeat("头", 500) + strings.Repeat("尾", 500)
	got := sanitizeText(body, 100)

	require.True(t, got.Truncated)
	assert.Equal(t, 1000, got.Chars, "字符数记的是截断**之前**的长度")
	assert.LessOrEqual(t, utf8.RuneCountInString(got.Text), 100)
	assert.True(t, utf8.ValidString(got.Text), "不得把一个字截成半个")
	assert.True(t, strings.HasPrefix(got.Text, "头"), "开头要留")
	assert.True(t, strings.HasSuffix(got.Text, "尾"), "结尾也要留 —— 载荷通常在那里")
}

// ─────────────────────── 入参校验 ───────────────────────

// 三格全空必须被单独一个错误码拒绝。
//
// 它在类型上完全合法,建出来的却是一个全站监听任务,与用户级任务在列表页上
// 长得一模一样 —— 合并进"参数不合法"之后,管理员只会随手补一个字段再提交。
func TestUpsertRejectsEmptyScopeWithItsOwnCode(t *testing.T) {
	req := upsertReq{Name: "全站", SampleBps: 10000, WindowMode: WindowForever}
	err := req.applyTo(&Task{}, true)
	assert.ErrorIs(t, err, error(errScopeEmpty))
}

// 时间窗三种形态各自的判据。
func TestResolveWindowCoversAllThreeModes(t *testing.T) {
	now := common.GetTimestamp()

	t.Run("永久", func(t *testing.T) {
		start, end, err := resolveWindow(&upsertReq{WindowMode: WindowForever}, true, &Task{})
		require.NoError(t, err)
		assert.EqualValues(t, 0, start)
		assert.EqualValues(t, 0, end, "永久监听的 ends_at 必须是 0")
	})

	t.Run("新建时不接受已经过去的结束时间", func(t *testing.T) {
		_, _, err := resolveWindow(&upsertReq{WindowMode: WindowRange, EndsAt: now - 1}, true, &Task{})
		assert.ErrorIs(t, err, error(errWindowRange))
	})

	t.Run("倒计时从此刻起算", func(t *testing.T) {
		start, end, err := resolveWindow(&upsertReq{WindowMode: WindowCountdown, CountdownSeconds: 3600}, true, &Task{})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, start, now)
		assert.Equal(t, start+3600, end)
	})

	t.Run("编辑时不重置倒计时", func(t *testing.T) {
		existing := &Task{StartsAt: now - 1800, EndsAt: now + 1800}
		start, end, err := resolveWindow(&upsertReq{WindowMode: WindowCountdown, CountdownSeconds: 3600}, false, existing)
		require.NoError(t, err)
		assert.Equal(t, existing.StartsAt, start)
		assert.Equal(t, existing.EndsAt, end,
			"只改备注不该把剩余时间悄悄重置成满格 —— 重新计时是「启动」那个动作的语义")
	})

	t.Run("形态取值非法", func(t *testing.T) {
		_, _, err := resolveWindow(&upsertReq{WindowMode: "whenever"}, true, &Task{})
		assert.ErrorIs(t, err, error(errWindowMode))
	})
}

// 保留期上界由配置给出,超了就拒,而 nil 与 0 是两个不同的合法值。
func TestCheckRetentionDistinguishesUnsetFromForever(t *testing.T) {
	prev := qyConfig.Swap(&config.Config{RiskWatch: config.RiskWatch{MaxRetentionDays: 90}})
	t.Cleanup(func() { qyConfig.Store(prev) })

	assert.NoError(t, checkRetention(nil), "nil = 跟随全局默认,永远合法")
	zero, ninety, over := 0, 90, 91
	assert.NoError(t, checkRetention(&zero), "0 = 永久保留,是一个合法选择")
	assert.NoError(t, checkRetention(&ninety))
	assert.ErrorIs(t, checkRetention(&over), error(errRetentionRange))
}

// 到期时刻在落库那一刻折算成绝对时间戳,0 天表示永不清理。
func TestExpiryForFoldsRetentionIntoAnAbsoluteInstant(t *testing.T) {
	assert.EqualValues(t, 0, expiryFor(0, 1000), "0 天 = 永久保留,expires_at 必须是 0")
	assert.EqualValues(t, 1000+30*86400, expiryFor(30, 1000))
}
