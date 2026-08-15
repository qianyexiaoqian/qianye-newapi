package withdraw

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// concurrency_test.go —— 申请阶段风控闸门的并发回归。
//
// # 被测的缺陷
//
// 申请路径是 count-then-insert,而第一把行锁(commission.FreezeForWithdraw 里
// 的余额行锁)排在闸门【之后】:并发的 N 个事务在 MySQL 的 REPEATABLE READ
// 快照里各读各的旧计数,一起通过闸门,再被余额行锁排成一队先后落单。
// 实测 8 并发全过,daily_max_count=3 与 max_pending_orders=3 双双失效。
//
// 不造成资损 —— 余额行锁仍然兜住"佣金不会超发"—— 但日额度、冷却与反刷号
// 这三道闸是空的,而它们要拦的本来就不是资损。
//
// # 为什么必须跑真实 MySQL
//
// 缺陷住在隔离级别里,不在 SQL 条件里。sqlite 复现不了:它遇到"读快照过期
// 还要写"时直接把后来者打回去,闸门看起来生效,其实是数据库替它挡的;
// 而 db.LockForUpdate 的 FOR UPDATE 在 sqlite 上要被脚手架吞掉(见 newTestDB),
// 也就是修复的核心那把锁在 sqlite 上根本不存在。
//
// # 断言口径
//
// 精确条数 + 失败原因必须全是业务错误 + 佣金账面必须与放行数完全对账。
// 只断言"不超过上限"的话,一次死锁把请求打回去也会让测试变绿。

// mysqlDSNEnv 与 ticket 包同名同义:并发回归用的扩展库 DSN。
//
// ⚠️ 它必须指向一个**专用的空库**,而且同一时刻只能有一个 go test 进程在用:
// 每个用例都会 DropTable + AutoMigrate 自己那几张表,两个进程同时跑会互相
// 把表拆掉,失败现场看起来像"闸门漏了几笔",而根因只是另一个进程在建表。
const mysqlDSNEnv = "QY_TEST_MYSQL_DSN"

// concurrentRequests 与缺陷报告里的实测口径一致(8 并发全过)。
const concurrentRequests = 8

// concurrentQuota 是每笔并发申请的额度。
const concurrentQuota int64 = 500000

// newConcurrentDB 接上真实 MySQL 并把它挂成扩展库句柄。
func newConcurrentDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(mysqlDSNEnv))
	if dsn == "" {
		t.Skipf("并发回归需要真实 MySQL(隔离级别正是被测对象),已跳过。\n"+
			"运行方式:%s='user:pass@tcp(127.0.0.1:3306)/qy_ext_test?charset=utf8mb4&parseTime=true' "+
			"go test ./qianye/modules/withdraw/ -run Concurrent", mysqlDSNEnv)
	}
	gdb, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Discard,
		// 与 qianye/db.Init 一致:单条语句不自动包事务,否则被测事务的边界
		// 与生产不同,加锁时机也就不同了。
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err, "连不上 %s 指定的 MySQL", mysqlDSNEnv)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 连接数必须大于并发数:少一条就会有协程在连接池里排队,那种"串行"
	// 是测试自己造出来的,会把闸门失效掩盖过去。
	sqlDB.SetMaxOpenConns(concurrentRequests + 4)

	tables := withdrawTables()
	require.NoError(t, gdb.Migrator().DropTable(tables...))
	require.NoError(t, gdb.AutoMigrate(tables...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		_ = sqlDB.Close()
	})
	return gdb
}

// 并发申请不得绕过任何一道风控闸门。
//
// 撤掉 submitInTx 开头那句 commission.LockBalance(或者把它挪到 findByIdemKey
// 之后 —— 那次普通查询会把 REPEATABLE READ 的快照钉在加锁之前),
// 每一条子用例都会变红。
func TestConcurrentSubmitCannotBypassCreateLimits(t *testing.T) {
	cases := []struct {
		name string
		// tweak 只打开被测的那一道闸,其余全部关掉 —— 否则某条断言变红时
		// 分不清是哪道闸在起作用,而"另一道闸恰好也拦住了"正是假通过的来源。
		tweak    func(*config.Withdraw)
		wantOK   int
		wantErr  error
		hitsWhat string
	}{
		{
			name: "日笔数上限",
			tweak: func(c *config.Withdraw) {
				c.DailyMaxCount = 3
			},
			wantOK:   3,
			wantErr:  errDailyCountReached,
			hitsWhat: "daily_max_count=3",
		},
		{
			name: "冷却窗口",
			tweak: func(c *config.Withdraw) {
				c.CooldownSecs = 60
			},
			wantOK:   1,
			wantErr:  errCooldown,
			hitsWhat: "cooldown_seconds=60",
		},
		{
			name: "未终态单数上限",
			tweak: func(c *config.Withdraw) {
				c.MaxPendingOrders = 3
			},
			wantOK:   3,
			wantErr:  errPendingLimit,
			hitsWhat: "max_pending_orders=3",
		},
		{
			name: "日额度上限",
			tweak: func(c *config.Withdraw) {
				c.DailyMaxQuota = concurrentQuota * 3
			},
			wantOK:   3,
			wantErr:  errDailyQuotaReached,
			hitsWhat: "daily_max_quota=3 笔的量",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newConcurrentDB(t)
			// 佣金余额远大于全部并发申请之和:这几条测的是频率/额度闸门,
			// 余额不足会用另一种方式把请求挡回去(errInsufficient),
			// 那样即使闸门是坏的测试也会变绿。
			seedBalance(t, gdb, 1, concurrentQuota*concurrentRequests*2)

			cfg := config.Withdraw{Enabled: true, MinQuota: concurrentQuota}
			tc.tweak(&cfg)

			errs := runConcurrently(concurrentRequests, func(i int) error {
				return db.Get().Transaction(func(tx *gorm.DB) error {
					_, err := submitInTx(tx, newConcurrentWithdrawal(i), nil,
						acceptedRequest{
							IdemKey: "cli-" + strconv.Itoa(i),
							Method:  config.WithdrawMethodQuota,
							Quota:   concurrentQuota,
						}, cfg, "alice")
					return err
				})
			})

			ok, unexpected := countErrors(errs, tc.wantErr)
			assert.Empty(t, unexpected,
				"被拒的申请里混着非业务错误 —— 闸门不是靠自己拦住的")
			assert.Equal(t, tc.wantOK, ok, "%s,并发申请却放行了 %d 笔", tc.hitsWhat, ok)

			var landed int64
			require.NoError(t, gdb.Model(&Withdrawal{}).Where("user_id = ?", 1).
				Count(&landed).Error)
			assert.EqualValues(t, tc.wantOK, landed, "落库单数与放行数对不上")

			// 佣金账面必须与放行数完全对账:闸门被绕过时,冻结额度会跟着多冻,
			// 这条断言是对上面那条的独立交叉验证(而且它盯的是钱那一侧)。
			var bal commission.Balance
			require.NoError(t, gdb.Where("user_id = ?", 1).Take(&bal).Error)
			assert.Equal(t, int64(tc.wantOK)*concurrentQuota, bal.FrozenQuota,
				"冻结佣金与放行单数对不上")
		})
	}
}

// seedBalance 给用户一份可提现佣金。
func seedBalance(t *testing.T, gdb *gorm.DB, userId int, available int64) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&commission.Balance{
		UserId: userId, AvailableQuota: available, CreatedAt: now, UpdatedAt: now,
	}).Error)
}

// newConcurrentWithdrawal 造第 i 笔待落库的申请。
// 单号与幂等键都按 i 派生 —— 它们必须互不相同,否则被唯一索引挡下的重复提交
// 会被误读成"闸门拦住了"。
func newConcurrentWithdrawal(i int) *Withdrawal {
	now := common.GetTimestamp()
	return &Withdrawal{
		WithdrawNo: "WD-CC-" + strconv.Itoa(i),
		IdemScope:  idemScope,
		IdemKey:    idemKeyOf(1, "cli-"+strconv.Itoa(i)),
		UserId:     1,
		Username:   "alice",
		Method:     config.WithdrawMethodQuota,
		Status:     StatusPending,
		Quota:      concurrentQuota,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// runConcurrently 让 n 个协程尽量同时进入 fn,返回各自的错误。
//
// 起跑线 channel 不能省:go 语句之间有可观的启动间隔,不同步的话前几个协程
// 可能已经提交完了,后来者读到的是新计数 —— 那样即使闸门是坏的也会变绿。
//
// fn 里不允许调用 require/assert:它们在非测试协程里走 runtime.Goexit,
// 失败信息反而丢了。
func runConcurrently(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = fn(idx)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

// countErrors 统计成功数,并把"不是预期业务错误"的那些原样带出来。
//
// 第二个返回值是关键:闸门若是靠死锁或锁等待超时"挡"下来的,成功数同样会
// 变少 —— 只看成功数会把那种假通过当成修复成功。
func countErrors(errs []error, want error) (ok int, unexpected []error) {
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case err == want:
		default:
			unexpected = append(unexpected, err)
		}
	}
	return ok, unexpected
}

// 申请事务里的第一条语句必须是佣金余额行锁。
//
// # 为什么这条不是"测实现细节"
//
// 它测的是修复能否成立的**前提**。MySQL 的 REPEATABLE READ 快照建立在事务的
// 第一次一致性读那一刻,加锁读不建立快照:
//
//	先加锁再 COUNT  → 快照晚于加锁 → 数得到上一个持锁者刚提交的单 ✅
//	先 COUNT 再加锁 → 快照早于加锁 → 拿到锁也只看得见旧值,闸门照样被绕过 ❌
//
// submitInTx 里 findByIdemKey 就排在闸门之前,只要有人把加锁挪到它后面
// (看起来完全无害:"先判重放再加锁不是更省吗"),四道闸会静默失效。
// 上面那些并发用例在没有 MySQL 的机器上是跳过的,没有任何东西会提醒他。
// 这条用例不需要 MySQL,任何时候都跑。
func TestSubmitInTxLocksBeforeItReads(t *testing.T) {
	gdb := newTestDB(t)
	seedBalance(t, gdb, 1, concurrentQuota*4)

	var stmts []string
	require.NoError(t, gdb.Callback().Query().After("gorm:query").
		Register("test:record_sql", func(tx *gorm.DB) {
			stmts = append(stmts, tx.Statement.SQL.String())
		}))

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		_, err := submitInTx(tx, newConcurrentWithdrawal(0), nil, acceptedRequest{
			IdemKey: "cli-0", Method: config.WithdrawMethodQuota, Quota: concurrentQuota,
		}, config.Withdraw{Enabled: true, DailyMaxCount: 3}, "alice")
		return err
	}))

	require.NotEmpty(t, stmts, "没有捕获到任何查询")
	assert.Contains(t, stmts[0], "qy_commission_balance",
		"申请事务的第一条查询不是佣金余额行锁 —— 快照会早于加锁建立,"+
			"四道风控闸会在并发下静默失效。当前第一条是:%s", stmts[0])
}
