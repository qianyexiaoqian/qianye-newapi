package ticket

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// concurrency_test.go —— 三道"先查再写"闸门的并发回归。
//
// # 为什么这几条必须跑真实 MySQL
//
// 被测的缺陷不在 WHERE 条件里,而在【事务隔离级别】里:扩展库固定是 MySQL,
// 默认 REPEATABLE READ,并发事务各读各的快照,于是"COUNT 一下再决定放不放行"
// 这种写法会让 N 个并发请求同时读到旧计数、同时通过。
//
// sqlite 复现不了这个形状,而且会给出**误导性的绿**:
//
//   - ":memory:" 按连接隔离,本包其他测试把连接数锁成 1,根本没有并发;
//   - 文件库(WAL)遇到"读快照已过期还要写"时直接返回 SQLITE_BUSY 把后来者
//     打回去 —— 闸门看起来生效了,其实是数据库替它挡的,换成 MySQL 立刻失效;
//   - db.LockForUpdate 挂的 FOR UPDATE 子句在 sqlite 上要被测试脚手架吞掉
//     (见 newTestDB),也就是修复的核心那把锁在 sqlite 上根本不存在。
//
// 所以这里只认 MySQL,没有 DSN 就跳过并把命令写在跳过理由里。
//
// # 每条用例都要能证明"撤掉修复就变红"
//
// 断言一律是【精确条数】+【失败原因必须全是业务错误】,不是"不超过上限"。
// 前者在闸门被绕过时必然变红;后者能识破"靠数据库报错挡下来"的假通过 ——
// 一次死锁/超时也会让成功数变少,但那不是闸门在起作用。

// mysqlDSNEnv 是并发回归用的扩展库 DSN。
//
// 不复用根模块的 TEST_MYSQL_DSN:那个指向主库 schema,而这里要建的是 qy_ 系列
// 扩展表,两者混在同一个变量里迟早会把扩展表建进主库。
//
// ⚠️ 它必须指向一个**专用的空库**,而且同一时刻只能有一个 go test 进程在用:
// 每个用例都会 DropTable + AutoMigrate 自己那几张表,两个进程同时跑会互相
// 把表拆掉,失败现场看起来像"闸门漏了几笔",而根因只是另一个进程在建表。
// (同一次 go test ./... 里没有这个问题:ticket 与 withdraw 用的表不重叠。)
const mysqlDSNEnv = "QY_TEST_MYSQL_DSN"

// concurrentRequests 是并发请求数。
//
// 取 8 与缺陷报告里的实测口径一致(8 并发建单 8/8 成功)。再往上只是让 MySQL
// 的行锁排队更久,不会提高这几条断言的分辨力 —— 判据是"精确等于上限",
// 而不是"跑得够久"。
const concurrentRequests = 8

// newConcurrentEnv 接上真实 MySQL 并把它挂成扩展库句柄。
func newConcurrentEnv(t *testing.T, extraTicketYAML string) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(mysqlDSNEnv))
	if dsn == "" {
		t.Skipf("并发回归需要真实 MySQL(隔离级别正是被测对象),已跳过。\n"+
			"运行方式:%s='user:pass@tcp(127.0.0.1:3306)/qy_ext_test?charset=utf8mb4&parseTime=true' "+
			"go test ./qianye/modules/ticket/ -run Concurrent", mysqlDSNEnv)
	}
	loadTicketConfig(t, extraTicketYAML)

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

	// qy_audit_logs 也要建:create() 成功与失败都写审计,表不在时每个协程
	// 都会打一行错误日志,把真正的失败原因淹掉。
	tables := append(ticketTables(), &qymodel.AuditLog{})
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

// runConcurrently 让 n 个协程尽量同时进入 fn,返回各自的错误。
//
// 用一个"起跑线"channel 而不是直接起协程:go 语句之间有可观的启动间隔,
// 不同步的话前几个协程可能已经提交完了,后来者读到的是新计数 ——
// 那样即使闸门是坏的,测试也可能侥幸变绿。
//
// fn 里**不允许**调用 require/assert:它们在非测试协程里会走 runtime.Goexit,
// 失败信息反而丢了。一律把 error 带回主协程再断言。
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
// 第二个返回值是这套断言的关键:如果闸门是靠死锁、锁等待超时或
// SQLITE_BUSY 之类的数据库错误"挡"下来的,成功数也会变少 ——
// 只断言成功数会把那种假通过当成修复成功。
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

// newRequestContext 造一个只带登录态的 gin 上下文。每个协程必须各持一个 ——
// gin.Context 不是并发安全的。
func newRequestContext(userId int) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/ticket", nil)
	c.Set("id", userId)
	c.Set("username", "alice")
	return c
}

// 缺陷一:建单四闸并发绕过。
//
// checkCreateLimits 此前的注释宣称"在事务里判所以并发安全"。实测 8 并发建单
// 8/8 成功,max_open_per_user=5 与 cooldown=60s 一道都没关上 —— 事务只保证
// 原子性,不保证串行化。修法是让闸门与插入共享 lockUserState 那把行锁,
// 且加锁必须是事务的第一条语句。
//
// 撤掉 checkCreateLimits 里的 lockUserState 调用,这两条子用例立刻变红。
func TestConcurrentCreateCannotBypassLimits(t *testing.T) {
	t.Run("未关闭工单数上限", func(t *testing.T) {
		gdb := newConcurrentEnv(t, "  cooldown_seconds: 0\n  max_open_per_user: 5\n  daily_max_count: 0\n")

		errs := runConcurrently(concurrentRequests, func(i int) error {
			_, err := create(newRequestContext(1), 1, "alice", createRequest{
				Title: "并发建单 " + strconv.Itoa(i), Body: "正文",
			})
			return err
		})

		ok, unexpected := countErrors(errs, errOpenLimit)
		assert.Empty(t, unexpected, "被拒的请求里混着非业务错误 —— 闸门不是靠自己拦住的")
		assert.Equal(t, 5, ok, "max_open_per_user=5,并发建单却放行了 %d 笔", ok)
		assert.EqualValues(t, 5, countTickets(t, gdb, 1), "落库条数与放行数对不上")
	})

	t.Run("冷却窗口", func(t *testing.T) {
		// 冷却是"上一张单的时间戳"判定,最容易被并发穿透:8 个请求都读到
		// "上一张单还不存在"。修复后只应放行第一笔。
		gdb := newConcurrentEnv(t, "  cooldown_seconds: 60\n  max_open_per_user: 0\n  daily_max_count: 0\n")

		errs := runConcurrently(concurrentRequests, func(i int) error {
			_, err := create(newRequestContext(1), 1, "alice", createRequest{
				Title: "并发建单 " + strconv.Itoa(i), Body: "正文",
			})
			return err
		})

		ok, unexpected := countErrors(errs, errCooldown)
		assert.Empty(t, unexpected, "被拒的请求里混着非业务错误 —— 闸门不是靠自己拦住的")
		assert.Equal(t, 1, ok, "60 秒冷却下并发建单放行了 %d 笔", ok)
		assert.EqualValues(t, 1, countTickets(t, gdb, 1), "落库条数与放行数对不上")
	})

	t.Run("日限额", func(t *testing.T) {
		gdb := newConcurrentEnv(t, "  cooldown_seconds: 0\n  max_open_per_user: 0\n  daily_max_count: 3\n")

		errs := runConcurrently(concurrentRequests, func(i int) error {
			_, err := create(newRequestContext(1), 1, "alice", createRequest{
				Title: "并发建单 " + strconv.Itoa(i), Body: "正文",
			})
			return err
		})

		ok, unexpected := countErrors(errs, errDailyLimit)
		assert.Empty(t, unexpected, "被拒的请求里混着非业务错误 —— 闸门不是靠自己拦住的")
		assert.Equal(t, 3, ok, "daily_max_count=3,并发建单却放行了 %d 笔", ok)
		assert.EqualValues(t, 3, countTickets(t, gdb, 1), "落库条数与放行数对不上")
	})

	t.Run("不同用户互不阻塞", func(t *testing.T) {
		// 这条守的是修复的**代价**:锁是按用户加的,一个人的建单不该把别人排队。
		// 写反成全局锁的话功能上依然"正确",但全站建单会被串行化,
		// 而那种退化不会有任何断言发现 —— 除了这一条。
		gdb := newConcurrentEnv(t, "  cooldown_seconds: 60\n  max_open_per_user: 0\n  daily_max_count: 0\n")

		errs := runConcurrently(concurrentRequests, func(i int) error {
			// 每个协程一个用户,人人都是自己的第一张单。
			_, err := create(newRequestContext(100+i), 100+i, "u"+strconv.Itoa(i),
				createRequest{Title: "各建各的", Body: "正文"})
			return err
		})

		ok, unexpected := countErrors(errs, errCooldown)
		assert.Empty(t, unexpected)
		assert.Equal(t, concurrentRequests, ok, "冷却是按用户算的,不该跨用户互相拦截")

		var total int64
		require.NoError(t, gdb.Model(&Ticket{}).Count(&total).Error)
		assert.EqualValues(t, concurrentRequests, total)
	})
}

// 缺陷二:回复冷却完全在事务外。
//
// checkReplyCooldown 此前由 handler 在事务【外面】调用,用的还是 db.Get()
// 而不是事务句柄 —— 实测 6 并发回复全过。修法是把判定挪进 appendMessage 的
// 事务,并让事务的第一条语句是这张工单的行锁。
//
// 撤掉 appendMessage 里的工单行锁(或把冷却判定挪回事务外),本用例变红。
func TestConcurrentRepliesCannotBypassCooldown(t *testing.T) {
	gdb := newConcurrentEnv(t, "  reply_cooldown_seconds: 60\n  max_messages_per_ticket: 100\n")
	seed := seedTicket(t, gdb, "TK-CONCURRENT", func(x *Ticket) { x.UserId = 1 })

	errs := runConcurrently(concurrentRequests, func(i int) error {
		// 每个协程各持一份工单副本:appendMessage 成功后会就地改写它的
		// MessageCount/Status,共用一个指针就是数据竞争(-race 会直接报)。
		tk := *seed
		_, err := appendMessage(&tk, replyInput{
			AuthorType: qymodel.ActorUser,
			AuthorId:   1,
			AuthorName: "alice",
			Body:       "并发追问 " + strconv.Itoa(i),
		}, nil)
		return err
	})

	ok, unexpected := countErrors(errs, errReplyCooldown)
	assert.Empty(t, unexpected, "被拒的请求里混着非业务错误 —— 冷却不是靠自己拦住的")
	assert.Equal(t, 1, ok, "60 秒回复冷却下并发追加放行了 %d 条", ok)

	var msgs int64
	require.NoError(t, gdb.Model(&Message{}).Where("ticket_id = ?", seed.Id).Count(&msgs).Error)
	assert.EqualValues(t, 1, msgs, "落库消息数与放行数对不上")

	// message_count 是冗余投影,唯一写入点就在这个事务里。它与真实行数对不上,
	// 说明有请求穿过了锁 —— 这条是对上面那条断言的独立交叉验证。
	var reloaded Ticket
	require.NoError(t, gdb.Where("id = ?", seed.Id).Take(&reloaded).Error)
	assert.Equal(t, 1, reloaded.MessageCount, "message_count 与实际消息数漂移")
}

// 缺陷三:图片配额并发绕过。
//
// image_user_quota_bytes 是"单个账号占满宿主机磁盘"唯一的总量闸(其他几道闸
// 都只管一次能传几张,图片一进工单计数就归零)。它被绕过的后果是不可回收的
// 磁盘占用,因此优先级最高。
//
// 撤掉 acceptImageUpload 里的 lockUserState 调用,两条子用例都会变红。
func TestConcurrentUploadsCannotBypassImageGates(t *testing.T) {
	t.Run("单人磁盘总量配额", func(t *testing.T) {
		// 配额刚好放得下 3 张图,pending 那道闸放到 40 张以外,
		// 于是能拦住并发的只可能是配额本身。
		//
		// image_max_bytes 也按单张图的实际字节配:校验器要求
		// image_user_quota_bytes >= image_max_bytes(否则第一张合法图就会被
		// 配额拒掉),两个值都从 pngBytes 派生才不会随测试数据变动而失配。
		one := int64(len(pngBytes))
		quota := one * 3
		gdb := newConcurrentEnv(t, "  image_max_bytes: "+strconv.FormatInt(one, 10)+"\n"+
			"  image_max_per_message: 20\n"+
			"  image_user_quota_bytes: "+strconv.FormatInt(quota, 10)+"\n")

		requests := make([]*gin.Context, concurrentRequests)
		for i := range requests {
			requests[i] = newUploadContext(t, 1)
		}
		errs := runConcurrently(concurrentRequests, func(i int) error {
			_, err := acceptImageUpload(requests[i], 1)
			return err
		})

		ok, unexpected := countErrors(errs, errImageQuota)
		assert.Empty(t, unexpected, "被拒的上传里混着非业务错误 —— 配额不是靠自己拦住的")
		assert.Equal(t, 3, ok, "配额只放得下 3 张,并发上传却收下了 %d 张", ok)

		var used int64
		require.NoError(t, gdb.Model(&Attachment{}).Where("user_id = ?", 1).
			Select("COALESCE(SUM(size), 0)").Scan(&used).Error)
		assert.LessOrEqual(t, used, quota, "磁盘占用已经超出配额 —— 这些字节没有任何任务能回收")
	})

	t.Run("未绑定上传数上限", func(t *testing.T) {
		// pendingUploadMax = image_max_per_message × 2,配置成 2 → 上限 4。
		// 配额放到不可能命中的量级,于是唯一可能拦住并发的是 pending 那道闸。
		gdb := newConcurrentEnv(t, "  image_max_bytes: 1024\n  image_max_per_message: 2\n"+
			"  image_user_quota_bytes: 0\n")

		requests := make([]*gin.Context, concurrentRequests)
		for i := range requests {
			requests[i] = newUploadContext(t, 1)
		}
		errs := runConcurrently(concurrentRequests, func(i int) error {
			_, err := acceptImageUpload(requests[i], 1)
			return err
		})

		ok, unexpected := countErrors(errs, errImagePending)
		assert.Empty(t, unexpected, "被拒的上传里混着非业务错误 —— 闸门不是靠自己拦住的")
		assert.Equal(t, 4, ok, "未绑定上传上限是 4,并发上传却收下了 %d 张", ok)

		var pending int64
		require.NoError(t, gdb.Model(&Attachment{}).
			Where("user_id = ? AND ticket_id = 0 AND purged_at = 0", 1).Count(&pending).Error)
		assert.EqualValues(t, 4, pending)
	})
}

// newUploadContext 造一次真实的 multipart 上传请求。
//
// 请求体必须在主协程里先造好:multipart 组装要用 require,而 require 在
// 非测试协程里会走 runtime.Goexit。
func newUploadContext(t *testing.T, userId int) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", "shot.png")
	require.NoError(t, err)
	_, err = fw.Write(pngBytes)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/ticket/images", body)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	c.Set("id", userId)
	c.Set("username", "alice")
	return c
}

func countTickets(t *testing.T, gdb *gorm.DB, userId int) int64 {
	t.Helper()
	var n int64
	require.NoError(t, gdb.Model(&Ticket{}).Where("user_id = ?", userId).Count(&n).Error)
	return n
}

// 建单事务里的第一条语句必须是那把行锁。
//
// # 为什么这条不是"测实现细节"
//
// 它测的是修复能否成立的**前提**。MySQL 的 REPEATABLE READ 快照建立在事务的
// 第一次一致性读那一刻,加锁读不建立快照:
//
//	先加锁再 COUNT  → 快照晚于加锁 → 数得到上一个持锁者刚提交的行 ✅
//	先 COUNT 再加锁 → 快照早于加锁 → 拿到锁也只看得见旧值,闸门照样被绕过 ❌
//
// 也就是说,以后有人在 checkCreateLimits 前面加一句"顺手查一下",四道闸会
// 静默失效,而上面那些并发用例在没有 MySQL 的机器上是跳过的 —— 没有任何东西
// 会提醒他。这条用例不需要 MySQL,任何时候都跑,专门守住这个顺序。
func TestCreateLimitsLocksBeforeItReads(t *testing.T) {
	gdb := newEnv(t, "  cooldown_seconds: 60\n  max_open_per_user: 5\n  daily_max_count: 3\n")

	var stmts []string
	require.NoError(t, gdb.Callback().Query().After("gorm:query").
		Register("test:record_sql", func(tx *gorm.DB) {
			stmts = append(stmts, tx.Statement.SQL.String())
		}))

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return checkCreateLimits(tx, 1, common.GetTimestamp())
	}))

	require.NotEmpty(t, stmts, "没有捕获到任何查询")
	assert.Contains(t, stmts[0], "qy_ticket_user_state",
		"闸门的第一条查询不是那把用户行锁 —— 快照会早于加锁建立,"+
			"三道闸会在并发下静默失效(见 userstate.go)。当前第一条是:%s", stmts[0])
}
