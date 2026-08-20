package twophase

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// indoubt_test.go —— "Failed 到底是什么意思"。
//
// 收敛前 Execute 对**任何** mainErr 都置 Failed,于是 Failed 同时承载两件事:
// "COMMIT 从没发出去过"与"COMMIT 发出去了但结果不知道"。四个业务模块都按
// order.Status == Failed 做不可逆动作(退风控预占、解冻佣金、换代次重发出款),
// 拿一个歧义值做不可逆动作,超发与错扣是必然 —— 抽奖双发就是这么来的。
//
// 这一组用例钉住的不变量:
//
//  1. COMMIT 没发出 → Failed;COMMIT 发出但报错 → InDoubt。判据是**阶段**,
//     不是错误文本,也不是探针(探针在那一刻可能还读不到结果)。
//  2. InDoubt 留在补偿任务的收敛范围内,并且和 pending 走同一套探针判据。
//  3. Uncertain 有自动出口:探针恢复后复判,判得出来就自动落定。
//  4. 提交后收尾(缓存失效 + 账本行)在两条入口之间恰好发生一次。

// newInDoubtEnv 装好扩展库(资金单)与主库(探针表 + 一对延迟外键表)。
//
// 延迟外键那两张表是本文件的关键道具:SQLite 在 PRAGMA foreign_keys=ON 时,
// DEFERRABLE INITIALLY DEFERRED 的违例只在 **COMMIT** 时报出来,事务体内的
// INSERT 一切正常。这给出一个确定性的"commit 阶段失败"形状 —— 不靠 sleep、
// 不靠杀连接、不靠随机,每次都从同一个位置断。
func newInDoubtEnv(t *testing.T, outboxOn bool) (ext *gorm.DB, main *gorm.DB) {
	t.Helper()

	open := func(name, extra string) *gorm.DB {
		dsn := filepath.Join(t.TempDir(), name) + "?_pragma=busy_timeout(5000)" + extra
		gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
		require.NoError(t, err)
		sqlDB, err := gdb.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return gdb
	}

	ext = open("qy_ext.db", "")
	require.NoError(t, ext.AutoMigrate(&qymodel.FundOrder{}, &qymodel.KV{}))

	main = open("main.db", "&_pragma=foreign_keys(1)")
	require.NoError(t, main.AutoMigrate(&model.QyFundOutbox{}))
	require.NoError(t, main.Exec(`CREATE TABLE parent(id INTEGER PRIMARY KEY)`).Error)
	require.NoError(t, main.Exec(
		`CREATE TABLE child(id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`).Error)

	prevHandle := qyDBHandle.Swap(ext)
	prevHealthy := qyDBHealthy.Swap(true)
	prevMain := model.DB
	model.DB = main

	auditOff := false
	prevCfg := qyConfig.Swap(&config.Config{
		Audit: config.Audit{Enabled: &auditOff},
		TwoPhase: config.TwoPhase{
			MainOutboxEnabled:        &outboxOn,
			PendingGraceSeconds:      60,
			MaxProbeAttempts:         10,
			ManualReviewAfterSeconds: 900,
			BatchSize:                200,
		},
		Runtime: config.Runtime{ColdPathTimeoutMs: 3000},
	})

	// 测试二进制不跑 InitRedisClient,而 common.RedisEnabled 的声明默认就是 true,
	// 于是 InvalidateUserCache 会拿着一个 nil 客户端去发 DEL。本机部署也没配 Redis,
	// 显式关掉才是与生产一致的前提。
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false

	prevResolvers := resolverRegistry
	prevPost := postCommitRegistry
	resolverRegistry = map[string]Resolver{}
	postCommitRegistry = map[string]PostCommit{}

	t.Cleanup(func() {
		common.RedisEnabled = prevRedis
		resolverRegistry = prevResolvers
		postCommitRegistry = prevPost
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		model.DB = prevMain
	})
	return ext, main
}

// breakCommit 是"COMMIT 阶段失败"的注入点:插一条指向不存在父行的子记录。
// 事务体内合法,COMMIT 时才违例 —— 与主库在 COMMIT 期间断连的形状同构。
func breakCommit(tx *gorm.DB) error {
	return tx.Exec(`INSERT INTO child(id, pid) VALUES (NULL, 999999)`).Error
}

func seedFundOrder(t *testing.T, ext *gorm.DB, no string, status int8, mutate func(*qymodel.FundOrder)) *qymodel.FundOrder {
	t.Helper()
	now := common.GetTimestamp()
	row := &qymodel.FundOrder{
		OrderNo: no, Kind: qymodel.KindTransfer, Status: status,
		IdemScope: "transfer", IdemKey: no,
		UserId: 5, PeerUserId: 7, AmountQuota: 100,
		RefId: no, CreatedAt: now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(row)
	}
	require.NoError(t, ext.Create(row).Error)
	return row
}

func seedOutbox(t *testing.T, main *gorm.DB, orderNo string) {
	t.Helper()
	require.NoError(t, main.Create(&model.QyFundOutbox{
		OrderNo: orderNo, Kind: qymodel.KindTransfer, UserId: 5, PeerId: 7,
		Amount: 100, CreatedAt: common.GetTimestamp(),
	}).Error)
}

// ─────────────────────── 1. 三种失败形态各落什么态 ───────────────────────

// 端到端跑 Execute:三种失败形态必须落三个**不同**的结论。
//
// 只测 settleAfterMainFailure 是不够的 —— 那样等于把"阶段判定"这件事本身
// 假设成对的,而缺陷恰恰在于 applyOnMainDB 从来分不出阶段。
func TestExecute_FailureShapeDecidesStatus(t *testing.T) {
	cases := []struct {
		name string
		// prepare 在 Execute 之前做手脚,用于制造"连事务都开不起来"。
		prepare   func(t *testing.T, main *gorm.DB)
		mainApply func(tx *gorm.DB, o *qymodel.FundOrder) error
		wantErr   bool
		wantStat  int8
		// rollbackAllowed 是调用方读到这个状态后会不会做不可逆回滚。
		rollbackAllowed bool
	}{
		{
			name:      "正常提交",
			mainApply: func(tx *gorm.DB, o *qymodel.FundOrder) error { return nil },
			wantStat:  qymodel.StatusSuccess,
		},
		{
			name: "从没开始:事务开不起来",
			prepare: func(t *testing.T, main *gorm.DB) {
				sqlDB, err := main.DB()
				require.NoError(t, err)
				require.NoError(t, sqlDB.Close())
			},
			mainApply:       func(tx *gorm.DB, o *qymodel.FundOrder) error { return nil },
			wantErr:         true,
			wantStat:        qymodel.StatusFailed,
			rollbackAllowed: true,
		},
		{
			name: "主库明确回滚:事务体返回业务错误",
			mainApply: func(tx *gorm.DB, o *qymodel.FundOrder) error {
				return errors.New("余额不足")
			},
			wantErr:         true,
			wantStat:        qymodel.StatusFailed,
			rollbackAllowed: true,
		},
		{
			name:            "commit 断连:结局不明",
			mainApply:       func(tx *gorm.DB, o *qymodel.FundOrder) error { return breakCommit(tx) },
			wantErr:         true,
			wantStat:        qymodel.StatusInDoubt,
			rollbackAllowed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext, main := newInDoubtEnv(t, true)
			if tc.prepare != nil {
				tc.prepare(t, main)
			}

			order, err := Execute(context.Background(), Request{
				Kind: qymodel.KindTransfer, IdemScope: "transfer", IdemKey: tc.name,
				UserId: 5, PeerUserId: 7, AmountQuota: 100,
				MainApply: tc.mainApply,
			})
			require.NotNil(t, order)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			var got qymodel.FundOrder
			require.NoError(t, ext.Where("order_no = ?", order.OrderNo).Take(&got).Error)
			assert.Equal(t, qymodel.StatusName(tc.wantStat), qymodel.StatusName(got.Status),
				"库里的状态必须与失败形态一一对应")
			assert.Equal(t, qymodel.StatusName(tc.wantStat), qymodel.StatusName(order.Status),
				"内存状态必须等于库里的真实状态 —— 调用方的回滚判定完全依赖它")
			assert.Equal(t, tc.rollbackAllowed, order.Status == qymodel.StatusFailed,
				"只有'COMMIT 从没发出'这一种形态才允许调用方做不可逆回滚")
		})
	}
}

// applyOnMainDB 报告的阶段必须与真实断点一致。
//
// 这一条与上面那条不重复:上面测的是"落什么状态",这里测的是"阶段判定本身"。
// 把阶段判定写成常量(永远返回 phaseBody 或永远 phaseCommit)会让上面那条
// 只挂掉一半用例,而这条会当场挂掉。
func TestApplyOnMainDB_ReportsRealPhase(t *testing.T) {
	cases := []struct {
		name      string
		closeMain bool
		mainApply func(tx *gorm.DB, o *qymodel.FundOrder) error
		wantPhase mainPhase
		wantErr   bool
	}{
		{"提交成功", false, func(tx *gorm.DB, o *qymodel.FundOrder) error { return nil }, phaseCommit, false},
		{"事务开不起来", true, func(tx *gorm.DB, o *qymodel.FundOrder) error { return nil }, phaseBegin, true},
		{"事务体返回错误", false, func(tx *gorm.DB, o *qymodel.FundOrder) error { return errors.New("boom") }, phaseBody, true},
		{"COMMIT 报错", false, func(tx *gorm.DB, o *qymodel.FundOrder) error { return breakCommit(tx) }, phaseCommit, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, main := newInDoubtEnv(t, true)
			if tc.closeMain {
				sqlDB, err := main.DB()
				require.NoError(t, err)
				require.NoError(t, sqlDB.Close())
			}
			order := &qymodel.FundOrder{OrderNo: "TR-phase", Kind: qymodel.KindTransfer, UserId: 5, AmountQuota: 100}

			_, phase, err := applyOnMainDB(context.Background(), Request{MainApply: tc.mainApply}, order)

			assert.Equal(t, tc.wantPhase, phase)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// settleAfterMainFailure 的路由表:阶段 × 单据现状 → 落什么态。
//
// CAS 落空的那几行同样重要:补偿任务或人工已经推进过的单,业务线程迟到的错误
// 不得覆盖 —— 覆盖之后调用方会按内存里的 Failed 去回滚,而库里是另一回事。
func TestSettleAfterMainFailure_RoutesByPhase(t *testing.T) {
	cases := []struct {
		name    string
		phase   mainPhase
		seeded  int8
		wantRow int8
	}{
		{"开不起来的单落 failed", phaseBegin, qymodel.StatusPending, qymodel.StatusFailed},
		{"事务体报错的单落 failed", phaseBody, qymodel.StatusPending, qymodel.StatusFailed},
		{"commit 断连的单落 in_doubt", phaseCommit, qymodel.StatusPending, qymodel.StatusInDoubt},
		{"补偿任务已转人工时不覆盖", phaseCommit, qymodel.StatusUncertain, qymodel.StatusUncertain},
		{"补偿任务已确认成功时不覆盖", phaseBody, qymodel.StatusSuccess, qymodel.StatusSuccess},
		{"已是 in_doubt 时不重复改写", phaseBody, qymodel.StatusInDoubt, qymodel.StatusInDoubt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext, _ := newInDoubtEnv(t, true)
			order := seedFundOrder(t, ext, "TR-"+tc.name, tc.seeded, nil)

			settleAfterMainFailure(context.Background(), ext, order, tc.phase, errors.New("主库出错"))

			var got qymodel.FundOrder
			require.NoError(t, ext.Where("order_no = ?", order.OrderNo).Take(&got).Error)
			assert.Equal(t, qymodel.StatusName(tc.wantRow), qymodel.StatusName(got.Status))
			assert.Equal(t, qymodel.StatusName(tc.wantRow), qymodel.StatusName(order.Status),
				"内存状态必须回读成库里的真实状态")
		})
	}
}

// 幂等重放撞上一张 in_doubt 单时,绝不能翻译成"此前已失败"。
//
// 翻成 ErrOrderFailed 的后果:调用方(transfer.releaseOnFailure / lottery.failPayout)
// 会拿这个错误去回滚预占、换代次重发 —— 而那笔钱可能已经扣了/发了。
func TestResolveExisting_InDoubtIsInProgressNotFailed(t *testing.T) {
	cases := []struct {
		name    string
		status  int8
		wantErr error
	}{
		{"pending", qymodel.StatusPending, ErrInProgress},
		{"in_doubt", qymodel.StatusInDoubt, ErrInProgress},
		{"uncertain", qymodel.StatusUncertain, ErrInProgress},
		{"failed", qymodel.StatusFailed, ErrOrderFailed},
		{"success", qymodel.StatusSuccess, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newInDoubtEnv(t, true)
			order := &qymodel.FundOrder{OrderNo: "TR-re", Status: tc.status, CreatedAt: common.GetTimestamp()}

			_, err := resolveExisting(order, "")

			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.wantErr)
			if tc.status != qymodel.StatusFailed {
				assert.NotErrorIs(t, err, ErrOrderFailed,
					"只有真的 failed 才能报 ErrOrderFailed;in_doubt 报成它就是把歧义又送回调用方")
			}
		})
	}
}

// ─────────────────────── 2. InDoubt 的出口:补偿任务 ───────────────────────

// 补偿任务必须把 in_doubt 单和 pending 单一视同仁地收敛。
//
// 漏掉 in_doubt 的后果比不改还糟:那些单会永远停在一个既不被自动收敛、
// 也不在人工队列里的状态 —— 钱动没动没人知道,也没人会来看。
func TestCompensate_ConvergesInDoubtLikePending(t *testing.T) {
	cases := []struct {
		name      string
		seeded    int8
		hasOutbox bool
		ageSec    int64
		wantStat  int8
	}{
		{"in_doubt + 探针说已生效 → success", qymodel.StatusInDoubt, true, 3600, qymodel.StatusSuccess},
		{"in_doubt + 探针说没生效且已过人工阈值 → failed", qymodel.StatusInDoubt, false, 3600, qymodel.StatusFailed},
		{"in_doubt + 探针说没生效但还年轻 → 原地退避", qymodel.StatusInDoubt, false, 120, qymodel.StatusInDoubt},
		{"pending + 探针说已生效 → success", qymodel.StatusPending, true, 3600, qymodel.StatusSuccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext, main := newInDoubtEnv(t, true)
			now := common.GetTimestamp()
			order := seedFundOrder(t, ext, "TR-cmp", tc.seeded, func(o *qymodel.FundOrder) {
				o.CreatedAt = now - tc.ageSec
				o.UpdatedAt = now - tc.ageSec
			})
			if tc.hasOutbox {
				seedOutbox(t, main, order.OrderNo)
			}
			resolved := 0
			RegisterResolver(qymodel.KindTransfer, func(context.Context, *qymodel.FundOrder) error {
				resolved++
				return nil
			})

			Compensate(context.Background())

			var got qymodel.FundOrder
			require.NoError(t, ext.Where("order_no = ?", order.OrderNo).Take(&got).Error)
			assert.Equal(t, qymodel.StatusName(tc.wantStat), qymodel.StatusName(got.Status))
			if tc.wantStat == qymodel.StatusSuccess {
				assert.Equal(t, 1, resolved, "确认主库生效后必须回调业务模块补收尾")
			} else {
				assert.Zero(t, resolved, "没确认主库生效就绝不能让业务模块按成功收尾")
			}
		})
	}
}

// ─────────────────────── 3. Uncertain 的自动出口 ───────────────────────

// 一个只有人工出口的状态等于钱被困住。探针恢复后必须能自己判出来。
func TestReprobeUncertain(t *testing.T) {
	cases := []struct {
		name       string
		outboxOn   bool
		hasOutbox  bool
		nextProbe  int64 // 相对 now 的偏移
		wantStat   int8
		wantResolv int
	}{
		{"探针说已生效 → 自动落 success", true, true, -10, qymodel.StatusSuccess, 1},
		{"探针说没生效 → 自动落 failed", true, false, -10, qymodel.StatusFailed, 0},
		{"探针整体关掉 → 原地不动等人", false, false, -10, qymodel.StatusUncertain, 0},
		{"还没到下一次复判时刻 → 不打主库", true, true, 3600, qymodel.StatusUncertain, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext, main := newInDoubtEnv(t, tc.outboxOn)
			now := common.GetTimestamp()
			order := seedFundOrder(t, ext, "TR-unc", qymodel.StatusUncertain, func(o *qymodel.FundOrder) {
				o.NextProbeAt = now + tc.nextProbe
			})
			if tc.hasOutbox {
				seedOutbox(t, main, order.OrderNo)
			}
			resolved := 0
			RegisterResolver(qymodel.KindTransfer, func(context.Context, *qymodel.FundOrder) error {
				resolved++
				return nil
			})

			reprobeUncertain(context.Background())

			var got qymodel.FundOrder
			require.NoError(t, ext.Where("order_no = ?", order.OrderNo).Take(&got).Error)
			assert.Equal(t, qymodel.StatusName(tc.wantStat), qymodel.StatusName(got.Status))
			assert.Equal(t, tc.wantResolv, resolved)
		})
	}
}

// 人工裁决必须走与补偿任务同一条收尾链路,而不是只改一个状态字段。
//
// 只改状态的后果:violation 的记录会永远停在 charged(该模块没有对账任务),
// 用户缓存也不会失效 —— 管理员按下"确认已生效",库里和用户看到的都还是旧的。
func TestResolveManually(t *testing.T) {
	cases := []struct {
		name       string
		seeded     int8
		target     int8
		wantOK     bool
		wantStat   int8
		wantResolv int
		wantPost   int
	}{
		{"判成功 → 跑 Resolver 与提交后收尾", qymodel.StatusUncertain, qymodel.StatusSuccess, true, qymodel.StatusSuccess, 1, 1},
		{"判失败 → 只落状态,不补业务收尾", qymodel.StatusUncertain, qymodel.StatusFailed, true, qymodel.StatusFailed, 0, 0},
		{"已被自动复判抢先 → CAS 落空", qymodel.StatusSuccess, qymodel.StatusFailed, false, qymodel.StatusSuccess, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext, _ := newInDoubtEnv(t, true)
			order := seedFundOrder(t, ext, "TR-man", tc.seeded, nil)
			resolved, posted := 0, 0
			RegisterResolver(qymodel.KindTransfer, func(context.Context, *qymodel.FundOrder) error {
				resolved++
				return nil
			})
			RegisterPostCommit(qymodel.KindTransfer, func(context.Context, *qymodel.FundOrder) error {
				posted++
				return nil
			})

			okDone, err := ResolveManually(context.Background(), order, tc.target, "人工裁决: 已核对银行流水")
			require.NoError(t, err)

			assert.Equal(t, tc.wantOK, okDone)
			var got qymodel.FundOrder
			require.NoError(t, ext.Where("order_no = ?", order.OrderNo).Take(&got).Error)
			assert.Equal(t, qymodel.StatusName(tc.wantStat), qymodel.StatusName(got.Status))
			assert.Equal(t, tc.wantResolv, resolved)
			assert.Equal(t, tc.wantPost, posted)
		})
	}
}

// 积压告警:静默期只能被**真的喊过**的那一轮消耗掉。
//
// 早期实现在"查到 0 笔"时也盖时间戳,于是一笔刚卡住的单最多要等一个静默期
// (1 小时)才会进值班视野 —— 而绝大多数轮次都是 0 笔,等于把响应时间从
// "下一轮"退化成"下一小时"。
func TestAlarmOnBacklog_SilenceWindowOnlyStartsAfterARealAlarm(t *testing.T) {
	ext, _ := newInDoubtEnv(t, true)

	// 第一轮:队列是空的 —— 不喊,也不许占用静默期。
	assert.Nil(t, alarmOnBacklog(context.Background()))

	now := common.GetTimestamp()
	seedFundOrder(t, ext, "TR-bk1", qymodel.StatusUncertain, func(o *qymodel.FundOrder) {
		o.AmountQuota = 700
		o.CreatedAt = now - 1200
	})
	seedFundOrder(t, ext, "TR-bk2", qymodel.StatusUncertain, func(o *qymodel.FundOrder) {
		o.AmountQuota = 300
		o.CreatedAt = now - 300
	})

	// 第二轮:刚出现积压,必须立刻喊,而不是等下一个小时。
	snap := alarmOnBacklog(context.Background())
	require.NotNil(t, snap, "队列里有钱卡住却不喊,等于没有告警")
	assert.Equal(t, int64(2), snap.Count)
	assert.Equal(t, int64(1000), snap.AmountQuota, "金额要合计,值班据此判断轻重")
	assert.Equal(t, "TR-bk1", snap.OldestNo, "最久的那一笔才是要先看的")
	assert.GreaterOrEqual(t, snap.OldestAge, int64(1200))

	// 第三轮:同一静默期内不重复喊,否则 30 秒一条会把真正的新异常淹掉。
	assert.Nil(t, alarmOnBacklog(context.Background()))
}

// 静默期必须是**集群级**的,不是进程级的。
//
// 原实现把它放在包级变量 lastBacklogAlarmAt 上,注释说"只被持有租约的那一个
// 节点读写"。这个前提不成立:lease.runOnce 每轮结束都主动 Release,下一轮谁先
// tick 谁抢到,租约在节点之间自由漂移 —— 实测两个进程各持一份计时器,同一条
// 告警在 2 分钟内被喊了两次,而间隔常量写的是 1 小时。N 个节点就是 N 倍,
// 滚动发布时每个新进程还会立刻再喊一次。
//
// 这里用"同一个扩展库、两次独立调用"模拟两个节点:槽位落在 qy_kv 上之后,
// 第二个节点在同一小时里必须抢不到。
func TestAlarmOnBacklog_SilenceWindowIsClusterWideNotPerProcess(t *testing.T) {
	ext, _ := newInDoubtEnv(t, true)
	now := common.GetTimestamp()
	seedFundOrder(t, ext, "TR-cluster", qymodel.StatusUncertain, func(o *qymodel.FundOrder) {
		o.AmountQuota = 7777
		o.CreatedAt = now - 3000
	})

	// 节点 A 抢到槽位并喊出去。
	require.NotNil(t, alarmOnBacklog(context.Background()), "第一个节点必须喊")

	// 节点 B 是另一个进程:它的进程内计时器是全新的(包级变量在那边是 0),
	// 唯一能拦住它的只有落在库上的那个槽位。
	assert.Nil(t, alarmOnBacklog(context.Background()),
		"静默期落在库上之后,集群里第二个节点在同一小时内不得重复喊同一条告警")

	// 槽位过期之后重新可抢 —— 静默期是节流,不是永久静音。
	require.NoError(t, ext.Model(&qymodel.KV{}).Where("k = ?", backlogAlarmKey).
		Update("updated_at", now-backlogAlarmIntervalSeconds-1).Error)
	assert.NotNil(t, alarmOnBacklog(context.Background()), "静默期过了必须能再喊")
}

// ─────────────────────── 4. 提交后收尾恰好发生一次 ───────────────────────

// 账本行不能漏写,也不能写两遍。两条入口(业务线程 / 补偿任务)之间必须恰好一次。
//
// 缺陷原样:commit 断连那一支 AfterCommit 从不执行,补偿任务也没有对应入口 ——
// 主库额度已经变了,用户账单里一行都没有。
func TestAfterCommit_ExactlyOnceAcrossBothEntries(t *testing.T) {
	t.Run("正常提交:业务线程跑一次,补偿任务不再跑", func(t *testing.T) {
		ext, main := newInDoubtEnv(t, true)
		inline, posted := 0, 0
		RegisterPostCommit(qymodel.KindTransfer, func(context.Context, *qymodel.FundOrder) error {
			posted++
			return nil
		})
		RegisterResolver(qymodel.KindTransfer, func(context.Context, *qymodel.FundOrder) error { return nil })

		order, err := Execute(context.Background(), Request{
			Kind: qymodel.KindTransfer, IdemScope: "transfer", IdemKey: "once-ok",
			UserId: 5, AmountQuota: 100,
			MainApply:   func(tx *gorm.DB, o *qymodel.FundOrder) error { return nil },
			AfterCommit: func(*qymodel.FundOrder) { inline++ },
		})
		require.NoError(t, err)
		assert.Equal(t, 1, inline, "正常路径必须就地跑一次提交后收尾")

		// 即使补偿任务因为别的原因又扫到这张单,也不能再写一次账本行。
		_ = main
		runPostCommit(context.Background(), ext, order)
		assert.Zero(t, posted, "已经被业务线程认领过的单,补偿任务不得重复补做")
	})

	// 下面两半刻意分成两个独立环境。SQLite 在 COMMIT 因延迟外键失败之后会**保留**
	// 事务(与 MySQL/PostgreSQL 不同),那条连接从此不能再开新事务 —— 让同一个
	// 环境接着跑补偿任务只会测出驱动的这个怪癖,而不是我们要钉的不变量。
	t.Run("commit 断连:业务线程绝不就地收尾", func(t *testing.T) {
		newInDoubtEnv(t, true)
		inline := 0

		order, err := Execute(context.Background(), Request{
			Kind: qymodel.KindTransfer, IdemScope: "transfer", IdemKey: "once-broken",
			UserId: 5, AmountQuota: 100,
			MainApply:   func(tx *gorm.DB, o *qymodel.FundOrder) error { return breakCommit(tx) },
			AfterCommit: func(*qymodel.FundOrder) { inline++ },
		})
		require.Error(t, err)
		require.Equal(t, qymodel.StatusInDoubt, order.Status)
		assert.Zero(t, inline, "结局不明时绝不能就地宣布收尾 —— 钱到底动没动还不知道")
		assert.Zero(t, order.AfterCommitAt, "没跑就不能占住认领戳,否则补偿任务永远补不上")
	})

	t.Run("commit 断连:补偿任务确认生效后补做一次", func(t *testing.T) {
		ext, main := newInDoubtEnv(t, true)
		posted := 0
		RegisterPostCommit(qymodel.KindTransfer, func(context.Context, *qymodel.FundOrder) error {
			posted++
			return nil
		})
		RegisterResolver(qymodel.KindTransfer, func(context.Context, *qymodel.FundOrder) error { return nil })

		now := common.GetTimestamp()
		order := seedFundOrder(t, ext, "TR-broken", qymodel.StatusInDoubt, func(o *qymodel.FundOrder) {
			o.CreatedAt = now - 3600
			o.UpdatedAt = now - 3600
		})
		// 主库其实提交了 —— 探针行就是那个唯一证据。
		seedOutbox(t, main, order.OrderNo)

		Compensate(context.Background())

		var got qymodel.FundOrder
		require.NoError(t, ext.Where("order_no = ?", order.OrderNo).Take(&got).Error)
		assert.Equal(t, qymodel.StatusSuccess, got.Status)
		assert.Equal(t, 1, posted, "commit 断连那一支的账本行只能由补偿任务补,少了它就永远没有")
		assert.NotZero(t, got.AfterCommitAt, "补做之后必须留下认领戳,否则下一轮会再写一遍")

		// 再扫一轮:单据已是 success,不该再补第二条账本行。
		Compensate(context.Background())
		assert.Equal(t, 1, posted, "提交后收尾必须恰好一次")
	})
}

// ─────────────────────── 5. 四个模块共同依赖的那条契约 ───────────────────────

// 各业务模块的回滚闸门形状统一:**只有 Failed 允许不可逆动作**。
//
// 这一条不是在测某个模块的实现,而是在锁住模块之间共享的判据本身:
// 一旦有人把 in_doubt 归进"未定局之外",或者把 uncertain 塞回补偿任务的
// 扫描范围,这里会当场挂掉。
func TestStatusContract_OnlyFailedAllowsIrreversibleRollback(t *testing.T) {
	cases := []struct {
		status int8
		// rollback 表示 transfer.releaseOnFailure / lottery.releaseEntryOnFailure /
		// lottery.failPayout 会不会据此做不可逆动作。
		rollback bool
		// unsettled 表示补偿任务是否还会继续推进它。
		unsettled bool
		terminal  bool
	}{
		{qymodel.StatusPending, false, true, false},
		{qymodel.StatusInDoubt, false, true, false},
		{qymodel.StatusUncertain, false, false, false},
		{qymodel.StatusFailed, true, false, true},
		{qymodel.StatusSuccess, false, false, true},
		{qymodel.StatusReversed, false, false, true},
	}
	for _, tc := range cases {
		t.Run(qymodel.StatusName(tc.status), func(t *testing.T) {
			assert.Equal(t, tc.rollback, tc.status == qymodel.StatusFailed,
				"回滚闸门只认 Failed;放宽到别的状态就是超发或错扣")
			assert.Equal(t, tc.unsettled, qymodel.IsUnsettled(tc.status),
				"补偿任务的扫描范围必须恰好是 pending + in_doubt")
			assert.Equal(t, tc.terminal, qymodel.IsTerminal(tc.status))
		})
	}
	assert.ElementsMatch(t,
		[]int8{qymodel.StatusPending, qymodel.StatusInDoubt},
		qymodel.UnsettledStatuses())
}
