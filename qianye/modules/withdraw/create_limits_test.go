package withdraw

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// limitsCfg 是一份四道闸门全部打开的配置。
func limitsCfg() config.Withdraw {
	return config.Withdraw{
		Enabled:          true,
		Methods:          []string{config.WithdrawMethodQuota, config.WithdrawMethodFiat},
		MinQuota:         500000,
		RemarkMaxRunes:   200,
		DailyMaxCount:    3,
		DailyMaxQuota:    1000000,
		CooldownSecs:     60,
		MaxPendingOrders: 2,
		MaxQuotaPerOrder: 800000,
	}
}

// C1:withdraw.max_quota_per_order 此前没有任何消费方 —— 单笔上界实际是主库
// int32 容量。一个长期累积的邀请人可以一次申请 20 亿额度,把整个佣金池冻在
// 一张单上,而运维看着 YAML 里写着 5 亿以为闸门是关着的。
func TestAcceptCreate_EnforcesMaxQuotaPerOrder(t *testing.T) {
	cfg := limitsCfg()
	base := createRequest{
		ClientRequestId: "11111111-2222-3333",
		Method:          config.WithdrawMethodQuota,
	}

	cases := []struct {
		name  string
		quota int64
		cap   int64
		want  error
	}{
		{"恰好等于单笔上限", 800000, 800000, nil},
		{"超出单笔上限一个额度", 800001, 800000, errAmountOutOfRange},
		{"远超单笔上限", int64(common.MaxQuota), 800000, errAmountOutOfRange},
		{"上限为 0 表示不限", int64(common.MaxQuota), 0, nil},
		{"仍然不得超过主库 int32 容量", int64(common.MaxQuota) + 1, 0, errAmountOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			c.MaxQuotaPerOrder = tc.cap
			req := base
			req.Quota = tc.quota
			_, err := acceptCreate(req, c)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// C1:cooldown_seconds / daily_max_quota / max_pending_orders 三项同样从未被
// 任何代码读取。判定必须待在落单的同一个事务里 —— 分开做的话并发请求会同时
// 读到旧计数、同时通过校验。
func TestEnforceCreateLimits(t *testing.T) {
	now := common.GetTimestamp()

	// 日限额的种子必须落在"今天"这个自然日内。
	//
	// 原先一律用 now-3600 造"今日已有的单",在午夜后一小时内跑就会掉到昨天,
	// 三个日限额用例集体失效 —— 这类只在特定时刻变红的测试比不写更糟:
	// 平时全绿会让人相信闸门在生效。
	//
	// 这里取"今日 0 点之后、且不早于一小时前"的时刻:既保证同日,
	// 又保留"不是刚刚发生"的语义(免得撞上冷却窗口)。
	sameDayEarlier := func(back int64) int64 {
		t.Helper()
		floor := dayStart()
		if v := now - back; v >= floor {
			return v
		}
		// 距 0 点不足 back 秒:退到 0 点之后一点点,仍属今日。
		return floor + 1
	}
	earlierToday := sameDayEarlier(3600)

	cases := []struct {
		name  string
		seed  func(t *testing.T, gdb *gorm.DB)
		tweak func(*config.Withdraw)
		quota int64
		want  error
	}{
		{
			name:  "干净账户放行",
			quota: 500000,
		},
		{
			name: "达到日笔数上限",
			seed: func(t *testing.T, gdb *gorm.DB) {
				for i := 0; i < 3; i++ {
					seedWithdrawal(t, gdb, idOf(i), func(w *Withdrawal) {
						w.Status = StatusPaid
						w.Quota = 1
						w.CreatedAt = earlierToday
					})
				}
			},
			quota: 500000,
			want:  errDailyCountReached,
		},
		{
			name: "撤销单不计入日笔数(手滑重填不该被惩罚)",
			seed: func(t *testing.T, gdb *gorm.DB) {
				for i := 0; i < 3; i++ {
					seedWithdrawal(t, gdb, idOf(i), func(w *Withdrawal) {
						w.Status = StatusCancelled
						w.Quota = 1
						w.CreatedAt = earlierToday
					})
				}
			},
			quota: 500000,
		},
		{
			name: "申请-撤销循环被提交总数闸门挡住",
			seed: func(t *testing.T, gdb *gorm.DB) {
				// daily_max_count=3 × dailySubmitFactor=4 → 第 12 笔就到顶。
				for i := 0; i < 12; i++ {
					seedWithdrawal(t, gdb, idOf(i), func(w *Withdrawal) {
						w.Status = StatusCancelled
						w.Quota = 1
						w.CreatedAt = earlierToday
					})
				}
			},
			quota: 500000,
			want:  errDailySubmitReached,
		},
		{
			name: "达到日额度上限",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-a", func(w *Withdrawal) {
					w.Status = StatusPaid
					w.Quota = 600000
					w.CreatedAt = earlierToday
				})
			},
			quota: 400001, // 600000 + 400001 > 1000000
			want:  errDailyQuotaReached,
		},
		{
			name: "恰好用满日额度仍放行",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-a", func(w *Withdrawal) {
					w.Status = StatusPaid
					w.Quota = 600000
					w.CreatedAt = earlierToday
				})
			},
			quota: 400000,
		},
		{
			name: "冷却窗口内不得再申请",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-a", func(w *Withdrawal) {
					w.Status = StatusPending
					w.Quota = 1
					w.CreatedAt = now - 10
				})
			},
			quota: 500000,
			want:  errCooldown,
		},
		{
			name: "冷却窗口把已撤销的单也算进来(否则撤销即可立刻重发)",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-a", func(w *Withdrawal) {
					w.Status = StatusCancelled
					w.Quota = 1
					w.CreatedAt = now - 10
				})
			},
			quota: 500000,
			want:  errCooldown,
		},
		{
			name: "冷却窗口之外放行",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-a", func(w *Withdrawal) {
					w.Status = StatusCancelled
					w.Quota = 1
					w.CreatedAt = now - 61
				})
			},
			quota: 500000,
		},
		{
			name: "未终态单达到上限",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-a", func(w *Withdrawal) {
					w.Status = StatusApproved
					w.Quota = 1
					w.CreatedAt = now - 86400*3 // 跨日,不受日限额影响
				})
				seedWithdrawal(t, gdb, "WD-b", func(w *Withdrawal) {
					w.Status = StatusPending
					w.Quota = 1
					w.CreatedAt = now - 86400*3
				})
			},
			quota: 500000,
			want:  errPendingLimit,
		},
		{
			name: "终态单不占用未终态名额",
			seed: func(t *testing.T, gdb *gorm.DB) {
				for i, s := range []string{StatusPaid, StatusRejected, StatusCancelled, StatusFailed} {
					status := s
					seedWithdrawal(t, gdb, idOf(i), func(w *Withdrawal) {
						w.Status = status
						w.Quota = 1
						w.CreatedAt = now - 86400*3
					})
				}
			},
			quota: 500000,
		},
		{
			name: "全部闸门配 0 时一律放行",
			seed: func(t *testing.T, gdb *gorm.DB) {
				for i := 0; i < 20; i++ {
					seedWithdrawal(t, gdb, idOf(i), func(w *Withdrawal) {
						w.Status = StatusPending
						w.Quota = 900000
						w.CreatedAt = now - 1
					})
				}
			},
			tweak: func(c *config.Withdraw) {
				c.DailyMaxCount, c.DailyMaxQuota = 0, 0
				c.CooldownSecs, c.MaxPendingOrders = 0, 0
			},
			quota: 500000,
		},
		{
			name: "别人的单不影响本人额度",
			seed: func(t *testing.T, gdb *gorm.DB) {
				for i := 0; i < 5; i++ {
					seedWithdrawal(t, gdb, idOf(i), func(w *Withdrawal) {
						w.UserId = 999
						w.IdemKey = idemKeyOf(999, "seed-"+idOf(i))
						w.Status = StatusPending
						w.Quota = 900000
						w.CreatedAt = now - 1
					})
				}
			},
			quota: 500000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			if tc.seed != nil {
				tc.seed(t, gdb)
			}
			cfg := limitsCfg()
			if tc.tweak != nil {
				tc.tweak(&cfg)
			}
			err := enforceCreateLimits(gdb, 1, tc.quota, cfg, now)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// 顺序不变量:幂等重放判定必须排在风控闸门之前。
//
// 反过来的话,原单自己占掉的冷却窗口会把它自己的重试判成违规 —— 用户双击一下
// 就收到"提现失败",而单其实已经落库、佣金已经冻结。这条只在冷却窗口内复现,
// 人工测试几乎碰不上,必须由测试钉住。
func TestSubmitInTx_ReplayResolvedBeforeRiskGate(t *testing.T) {
	now := common.GetTimestamp()
	acc := acceptedRequest{IdemKey: "client-abc-0001", Method: "quota", Quota: 500000}

	newIncoming := func(idem string) *Withdrawal {
		return &Withdrawal{
			WithdrawNo: "WD-new",
			IdemScope:  idemScope,
			IdemKey:    idemKeyOf(1, idem),
			UserId:     1,
			Method:     acc.Method,
			Status:     StatusPending,
			Quota:      acc.Quota,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
	}

	t.Run("重放返回原单而不是冷却错误", func(t *testing.T) {
		gdb := newTestDB(t)
		// 原单就在冷却窗口内 —— 它正是把窗口占住的那一笔。
		origin := seedWithdrawal(t, gdb, "WD-origin", func(w *Withdrawal) {
			w.IdemKey = idemKeyOf(1, acc.IdemKey)
			w.CreatedAt = now - 1
			w.Quota = acc.Quota
		})

		replay, err := submitInTx(gdb, newIncoming(acc.IdemKey), nil, acc, limitsCfg(), "u")
		require.NoError(t, err)
		require.NotNil(t, replay)
		assert.Equal(t, origin.WithdrawNo, replay.WithdrawNo)

		// 重放绝不能落第二张单。
		var cnt int64
		require.NoError(t, gdb.Model(&Withdrawal{}).Count(&cnt).Error)
		assert.EqualValues(t, 1, cnt)
	})

	t.Run("换一个幂等键则照常被冷却拦住", func(t *testing.T) {
		gdb := newTestDB(t)
		seedWithdrawal(t, gdb, "WD-origin", func(w *Withdrawal) {
			w.IdemKey = idemKeyOf(1, acc.IdemKey)
			w.CreatedAt = now - 1
		})

		other := acc
		other.IdemKey = "client-abc-0002"
		replay, err := submitInTx(gdb, newIncoming(other.IdemKey), nil, other, limitsCfg(), "u")
		assert.Nil(t, replay)
		assert.ErrorIs(t, err, errCooldown)
	})
}

// 幂等键只保证"不重复执行",保证不了"重放的是同一个请求"。
// client_request_id 由前端在打开弹窗时生成并缓存,用户改完金额**或改完收款账号**
// 再提交仍然沿用它 —— 此时返回原单等于告诉用户"你这笔 500 的申请成功了",
// 而库里躺着的是那笔 300 的;换收款人那一半更要命:界面上只有一串脱敏值,
// 而钱会照着原来那张卡打出去。
func TestEnsureReplayMatches(t *testing.T) {
	origin := &Withdrawal{
		Quota:       500000,
		Method:      config.WithdrawMethodFiat,
		PayeeDigest: "digest-of-card-A",
	}

	cases := []struct {
		name     string
		incoming *Withdrawal
		want     error
	}{
		{"同一笔请求的重放", &Withdrawal{
			Quota: 500000, Method: config.WithdrawMethodFiat, PayeeDigest: "digest-of-card-A"}, nil},
		{"换金额", &Withdrawal{
			Quota: 900000, Method: config.WithdrawMethodFiat, PayeeDigest: "digest-of-card-A"}, errIdemConflict},
		{"换方式", &Withdrawal{
			Quota: 500000, Method: config.WithdrawMethodQuota, PayeeDigest: "digest-of-card-A"}, errIdemConflict},
		{"换收款账号(钱会打到原来那张卡)", &Withdrawal{
			Quota: 500000, Method: config.WithdrawMethodFiat, PayeeDigest: "digest-of-card-B"}, errIdemConflict},
		{"同一张卡存了两次(ref 不同、指纹相同)不算冲突", &Withdrawal{
			Quota: 500000, Method: config.WithdrawMethodFiat, PayeeDigest: "digest-of-card-A"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ensureReplayMatches(origin, tc.incoming)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// 日用量的三个口径共用一条查询,口径漂移(这条排除了撤销、那条忘了)是
// 最容易出现的错误,直接体现为限额被绕过或被误判。
func TestLoadDailyUsage_CountsCancelledOnlyInSubmitted(t *testing.T) {
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	seedWithdrawal(t, gdb, "WD-paid", func(w *Withdrawal) {
		w.Status = StatusPaid
		w.Quota = 300000
		w.CreatedAt = now - 60
	})
	seedWithdrawal(t, gdb, "WD-cancelled", func(w *Withdrawal) {
		w.Status = StatusCancelled
		w.Quota = 900000
		w.CreatedAt = now - 60
	})
	// 昨天的单不该被今天的限额看见。
	seedWithdrawal(t, gdb, "WD-yesterday", func(w *Withdrawal) {
		w.Status = StatusPaid
		w.Quota = 700000
		w.CreatedAt = dayStart() - 1
	})

	usage, err := loadDailyUsage(gdb, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, usage.Submitted)
	assert.EqualValues(t, 1, usage.Active)
	assert.EqualValues(t, 300000, usage.Quota)
}
