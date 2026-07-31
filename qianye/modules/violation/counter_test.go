package violation

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newBanDB 建一个只承载 qy_violation_ban 的内存库。
//
// 认领语义的核心是 (user_id, ban_cycle) 唯一索引 + 状态 CAS,这两件事只有真的
// 跑一遍 INSERT/UPDATE 才算验证过,所以这里不 mock。
func newBanDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Ban{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// isolateBreaker 在用例前后各清一次进程级熔断状态。
// 封号速率窗口是包级 atomic,不隔离会让"速率闸有没有打满"变成看运行顺序的玄学。
func isolateBreaker(t *testing.T) {
	t.Helper()
	resetBreaker()
	t.Cleanup(resetBreaker)
}

func banRecord(userId int) *Record {
	return &Record{Id: 1, RecNo: "vr_test_1", UserId: userId}
}

// TestBanSignalSurvivesBlockedAttempt 是 B3 的核心回归。
//
// 旧实现用"恰好跨越阈值"作为封号判据:那是一个只出现一次的瞬时信号。
// 速率闸、影子模式,或者 bumpCounter 之后的任意一次执行失败,都会把它消费掉;
// 而下一次违规的计数已经越过阈值,判据永远为假 —— 该用户在整个滚动窗口
// (默认 24 小时)内再也不会被封号,补偿任务也救不了(它只扫已存在的封禁行)。
//
// 新判据完全由持久化的 hit_count 推导,"阻碍解除后还能不能封"因此与
// "信号有没有被消费过"彻底解耦。
func TestBanSignalSurvivesBlockedAttempt(t *testing.T) {
	const threshold = 5

	t.Run("阈值之后的每一次违规都仍在要求封号", func(t *testing.T) {
		cases := []struct {
			name  string
			after int
			want  bool
		}{
			{"未达阈值", 4, false},
			{"恰好达到阈值", 5, true},
			// 下面三条正是旧实现全部返回 false 的位置 —— 信号已被前一次消费掉。
			{"阈值后第一次(旧实现在这里丢失信号)", 6, true},
			{"阈值后第二次", 7, true},
			{"高权重规则一次跳过阈值后继续违规", 12, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, reachedThreshold(tc.after, threshold))
			})
		}
	})

	t.Run("阈值为 0 表示关闭自动封号", func(t *testing.T) {
		for after := 1; after <= 100; after++ {
			assert.False(t, reachedThreshold(after, 0))
		}
	})

	t.Run("负阈值同样视为关闭", func(t *testing.T) {
		assert.False(t, reachedThreshold(100, -1))
	})
}

// TestResolveBanClaimPersistsRateLimitedCrossing 固化"该封没封必须留痕"。
//
// 速率闸挡下的封号在旧实现里只打一行日志就 return:qy_violation_ban 里没有任何行,
// 管理端查不到,信号同时被消费掉。这里断言它变成一行 deferred,
// 并且在速率窗口滚过之后能被同一用户的下一次违规提升执行。
func TestResolveBanClaimPersistsRateLimitedCrossing(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  shadow_mode: false\n"+
		"  auto_ban_threshold: 5\n  global_ban_rate_limit_per_hour: 1\n")
	isolateBreaker(t)
	gdb := newBanDB(t)
	st := counterState{HitCount: 5, BanCycle: 0, Reached: true}

	// 直接把小时窗口填满(limit=1)。刻意不调 banRateExceeded() 来构造前置条件:
	// 它触顶时会顺带 tripShadow,那会把用例变成在测影子分支而不是速率闸分支。
	banWinStart.Store(common.GetTimestamp())
	banWinCount.Store(1)

	assert.Nil(t, resolveBanClaim(context.Background(), gdb, banRecord(42), st),
		"速率闸打满时不得执行封号")
	var row Ban
	require.NoError(t, gdb.Where("user_id = ?", 42).Take(&row).Error)
	assert.Equal(t, BanDeferred, row.Status, "被速率闸挡下的跨越必须落成 deferred,而不是消失")
	assert.Equal(t, 5, row.HitCountAt)

	// 触顶顺带打开了 30 分钟强制影子,此后的重复触发既不执行也不再生行。
	assert.Nil(t, resolveBanClaim(context.Background(), gdb, banRecord(42), st))
	var cnt int64
	require.NoError(t, gdb.Model(&Ban{}).Where("user_id = ?", 42).Count(&cnt).Error)
	assert.EqualValues(t, 1, cnt, "同一封禁周期只允许存在一行认领")

	// 窗口滚过之后(banWinStart 退到一小时以前),同一用户的下一次违规必须能把
	// deferred 提升为可执行 —— 这正是旧实现永久丢失的那条路径。
	banWinStart.Store(common.GetTimestamp() - banWindowSeconds - 1)
	clearForcedShadow() // banRateExceeded 触顶时会顺带强制回落影子模式
	ban := resolveBanClaim(context.Background(), gdb, banRecord(42), st)
	require.NotNil(t, ban, "阻碍解除后必须能重新拿到执行权")
	assert.Equal(t, BanPending, ban.Status)
	require.NoError(t, gdb.Where("user_id = ?", 42).Take(&row).Error)
	assert.Equal(t, BanPending, row.Status)
}

// TestResolveBanClaimRespectsExistingOutcome 保证提升路径不会踩到别人的结论。
//
// deferred 是唯一可以被重新认领的状态:pending / failed 归补偿任务管,
// banned / skipped / unbanned 都是已经有结论的终态,再执行一次就是重复封号。
func TestResolveBanClaimRespectsExistingOutcome(t *testing.T) {
	cases := []struct {
		status    string
		wantClaim bool
	}{
		{BanDeferred, true},
		{BanPending, false},
		{BanFailed, false},
		{BanBanned, false},
		{BanSkipped, false},
		{BanUnbanned, false},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  auto_ban_threshold: 5\n")
			isolateBreaker(t)
			gdb := newBanDB(t)
			require.NoError(t, gdb.Create(&Ban{
				UserId: 7, BanCycle: 0, Status: tc.status,
				CreatedAt: common.GetTimestamp(),
			}).Error)

			ban := resolveBanClaim(context.Background(), gdb,
				banRecord(7), counterState{HitCount: 9, Reached: true})
			assert.Equal(t, tc.wantClaim, ban != nil)

			var row Ban
			require.NoError(t, gdb.Where("user_id = ?", 7).Take(&row).Error)
			if tc.wantClaim {
				assert.Equal(t, BanPending, row.Status)
			} else {
				assert.Equal(t, tc.status, row.Status, "别的路径的结论不得被覆盖")
			}
		})
	}
}

// TestResolveBanClaimWritesNothingInShadowMode 固化影子模式的定义。
//
// 影子模式是"只观察、不产生任何处置副作用",写认领行会污染管理端的封禁列表。
// 信号不会因此丢失:判据是持久化的 hit_count,影子解除后的下一次违规会重新走到这里。
func TestResolveBanClaimWritesNothingInShadowMode(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  shadow_mode: true\n  auto_ban_threshold: 5\n")
	isolateBreaker(t)
	gdb := newBanDB(t)
	st := counterState{HitCount: 5, Reached: true}

	assert.Nil(t, resolveBanClaim(context.Background(), gdb, banRecord(9), st))
	var cnt int64
	require.NoError(t, gdb.Model(&Ban{}).Count(&cnt).Error)
	assert.EqualValues(t, 0, cnt, "影子模式下不得写任何封禁行")

	// 关掉影子模式,同一个计数状态必须立刻能封 —— 信号没有被消费掉。
	useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  auto_ban_threshold: 5\n")
	require.NotNil(t, resolveBanClaim(context.Background(), gdb, banRecord(9), st))
}

// TestResolveBanClaimAfterUnban 固化解封后两种选择的差别。
//
// 解封时 ban_cycle 一定 +1(否则唯一键会让自动封号对该用户永久失效),
// 而 reset_counter 决定旧计数还算不算数。判据改成"已达阈值"之后这个选择是实在的:
// 不清零 = 再违规一次立刻重封;清零 = 真的重新开始。
func TestResolveBanClaimAfterUnban(t *testing.T) {
	cases := []struct {
		name      string
		hitCount  int
		wantClaim bool
	}{
		{"解封时未清零:再违规一次立刻重封", 7, true},
		{"解封时已清零:重新从头累计", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  auto_ban_threshold: 5\n")
			isolateBreaker(t)
			gdb := newBanDB(t)
			// 上一周期留下的已解封行。周期已经 +1,不该挡住新周期的认领。
			require.NoError(t, gdb.Create(&Ban{
				UserId: 11, BanCycle: 0, Status: BanUnbanned,
				CreatedAt: common.GetTimestamp(),
			}).Error)

			st := counterState{HitCount: tc.hitCount, BanCycle: 1,
				Reached: reachedThreshold(tc.hitCount, 5)}
			ban := resolveBanClaim(context.Background(), gdb, banRecord(11), st)
			assert.Equal(t, tc.wantClaim, ban != nil)

			var cnt int64
			require.NoError(t, gdb.Model(&Ban{}).
				Where("user_id = ? AND ban_cycle = ?", 11, 1).Count(&cnt).Error)
			if tc.wantClaim {
				assert.EqualValues(t, 1, cnt, "新周期必须能认领,否则解封等于永久豁免")
			} else {
				assert.EqualValues(t, 0, cnt)
			}
		})
	}
}

// TestResolveBanClaimIgnoresUnreachedCounter 是反向保护:没达阈值就绝不认领。
func TestResolveBanClaimIgnoresUnreachedCounter(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  auto_ban_threshold: 5\n")
	isolateBreaker(t)
	gdb := newBanDB(t)

	assert.Nil(t, resolveBanClaim(context.Background(), gdb,
		banRecord(3), counterState{HitCount: 4, Reached: false}))
	var cnt int64
	require.NoError(t, gdb.Model(&Ban{}).Count(&cnt).Error)
	assert.EqualValues(t, 0, cnt)
}
