package withdraw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// payout_backlog_test.go —— 「扣了佣金但管理员迟迟不发放」这个敞口的观测面。
//
// 人工发放模型把一件事从系统手里交给了人:钱由管理员自己发。系统因此**必须**
// 回答"有没有人在等",否则用户的佣金可以在 approved 队列里无声地躺到天荒地老,
// 而站点里没有任何东西会为此出声 —— 那正是这次口径变更引入的唯一新风险。
//
// 三个面各自都能单独失效,所以三个都要钉:
//
//	slaOf              单据详情/队列行上的超时标记(管理员看得见的那一格)
//	handleAdminStats   队列角标上的积压计数(管理员进页面第一眼看到的数)
//	alertPayoutBacklog 后台日志告警(没在看页面的人的第二条通道)

// 发放时限必须从 reviewed_at 起算,而不是 created_at。
//
// 用 created_at 的话,一张审核拖了三天的单在通过的那一刻就已经"发放超时",
// 而发放的人一分钟都还没耽误 —— 两道时限就都不再指向任何具体的人,红色标记
// 也就失去了全部意义(运营会学会无视它)。
func TestSlaOfPicksTheRightClockPerStatus(t *testing.T) {
	now := common.GetTimestamp()
	loadSLAConfig(t, 24, 48)

	cases := []struct {
		name        string
		status      string
		createdAt   int64
		reviewedAt  int64
		wantKind    string
		wantBreach  bool
		wantDeadEnd int64
	}{
		{
			name: "待审未超时", status: StatusPending,
			createdAt: now - 3600, wantKind: SLAKindReview,
			wantBreach: false, wantDeadEnd: now - 3600 + 24*3600,
		},
		{
			name: "待审已超时", status: StatusPending,
			createdAt: now - 25*3600, wantKind: SLAKindReview,
			wantBreach: true, wantDeadEnd: now - 25*3600 + 24*3600,
		},
		{
			// 关键用例:审核阶段拖了很久,但刚刚才通过。发放时限必须从现在起算。
			name: "审核拖很久但刚通过,不算发放超时", status: StatusApproved,
			createdAt: now - 100*3600, reviewedAt: now - 60,
			wantKind: SLAKindPayout, wantBreach: false, wantDeadEnd: now - 60 + 48*3600,
		},
		{
			name: "通过后无人发放已超时", status: StatusApproved,
			createdAt: now - 100*3600, reviewedAt: now - 49*3600,
			wantKind: SLAKindPayout, wantBreach: true, wantDeadEnd: now - 49*3600 + 48*3600,
		},
		{
			name: "终态不再计时", status: StatusPaid,
			createdAt: now - 500*3600, reviewedAt: now - 400*3600,
			wantKind: "", wantBreach: false, wantDeadEnd: 0,
		},
		{
			name: "驳回后不再计时", status: StatusRejected,
			createdAt: now - 500*3600, wantKind: "", wantBreach: false, wantDeadEnd: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &Withdrawal{
				Status: tc.status, CreatedAt: tc.createdAt,
				ReviewedAt: tc.reviewedAt, UpdatedAt: tc.createdAt,
			}
			deadline, breached, kind := slaOf(w)
			assert.Equal(t, tc.wantKind, kind)
			assert.Equal(t, tc.wantBreach, breached)
			assert.Equal(t, tc.wantDeadEnd, deadline)
		})
	}
}

// 存量 approved 单(本次改造之前落库,reviewed_at 为 0)必须仍然被计时。
//
// 把它们当成"不计时"是最糟的处理:那批单正是等得最久、最该被看见的积压,
// 而一个不计时的单在队列里长得和刚通过的单一模一样。
func TestSlaOfFallsBackForLegacyApprovedRows(t *testing.T) {
	now := common.GetTimestamp()
	loadSLAConfig(t, 24, 48)

	w := &Withdrawal{Status: StatusApproved, CreatedAt: now - 200*3600,
		ReviewedAt: 0, UpdatedAt: now - 100*3600}
	deadline, breached, kind := slaOf(w)
	assert.Equal(t, SLAKindPayout, kind)
	assert.True(t, breached, "reviewed_at 缺失的存量单必须回落到 updated_at 计时")
	assert.Equal(t, now-100*3600+48*3600, deadline)
}

// 队列角标必须把两道时限分开报。
//
// 合成一个数的话,管理员看到"3 单超时"完全不知道该去审核还是去发钱 ——
// 而这两件事在组织上往往根本不是同一个人。
func TestAdminStatsReportsBothSlaBucketsSeparately(t *testing.T) {
	gdb := wireTestDB(t)
	loadSLAConfig(t, 24, 48)
	now := common.GetTimestamp()

	seedWithdrawal(t, gdb, "WD-pending-ok", func(w *Withdrawal) {
		w.Status = StatusPending
		w.CreatedAt = now - 3600
	})
	seedWithdrawal(t, gdb, "WD-pending-late", func(w *Withdrawal) {
		w.Status = StatusPending
		w.CreatedAt = now - 30*3600
	})
	seedWithdrawal(t, gdb, "WD-approved-ok", func(w *Withdrawal) {
		w.Status = StatusApproved
		w.CreatedAt = now - 200*3600 // 审核阶段拖了很久
		w.ReviewedAt = now - 60      // 但刚刚才通过
	})
	seedWithdrawal(t, gdb, "WD-approved-late", func(w *Withdrawal) {
		w.Status = StatusApproved
		w.CreatedAt = now - 200*3600
		w.ReviewedAt = now - 60*3600
	})
	// 终态单一律不该进任何一个桶。
	seedWithdrawal(t, gdb, "WD-paid", func(w *Withdrawal) {
		w.Status = StatusPaid
		w.CreatedAt = now - 500*3600
		w.ReviewedAt = now - 400*3600
	})

	res := callAdminGet(t, handleAdminStats)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())

	var body struct {
		Data struct {
			Buckets []struct {
				Status string `json:"status"`
				Count  int64  `json:"count"`
				Quota  int64  `json:"quota"`
			} `json:"buckets"`
			SLABreached       int64 `json:"sla_breached"`
			PayoutSLABreached int64 `json:"payout_sla_breached"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))

	counts := map[string]int64{}
	for _, b := range body.Data.Buckets {
		counts[b.Status] = b.Count
	}
	assert.EqualValues(t, 2, counts[StatusPending])
	assert.EqualValues(t, 2, counts[StatusApproved])
	assert.NotContains(t, counts, StatusPaid, "终态单不该出现在待办角标里")

	assert.EqualValues(t, 1, body.Data.SLABreached, "只有那张待审超时的单算审核超时")
	assert.EqualValues(t, 1, body.Data.PayoutSLABreached,
		"只有那张通过很久还没发放的单算发放积压;刚通过的那张不算")
}

// 后台积压统计的判据必须与队列角标完全一致,而且只在真有积压时给出非零值。
//
// 两个方向都要验:有积压必须数出来(否则告警通道形同虚设),没积压必须是 0
// (每轮都喊一次的告警会被运维加进忽略列表,下次真出事就没人看了)。
func TestScanPayoutBacklogCountsOnlyOverduePendingPayouts(t *testing.T) {
	now := common.GetTimestamp()

	cases := []struct {
		name      string
		seed      func(t *testing.T, gdb *gorm.DB)
		hours     int
		wantCount int64
		wantQuota int64
	}{
		{
			name: "超时待发放单被数进来", hours: 48,
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-late-a", func(w *Withdrawal) {
					w.Status = StatusApproved
					w.Quota = 500000
					w.ReviewedAt = now - 60*3600
				})
				seedWithdrawal(t, gdb, "WD-late-b", func(w *Withdrawal) {
					w.Status = StatusApproved
					w.Quota = 300000
					w.ReviewedAt = now - 200*3600
				})
			},
			wantCount: 2, wantQuota: 800000,
		},
		{
			name: "刚通过的单不算积压", hours: 48,
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-fresh", func(w *Withdrawal) {
					w.Status = StatusApproved
					w.ReviewedAt = now - 60
				})
			},
		},
		{
			name: "待审单不归发放时限管", hours: 48,
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-pending", func(w *Withdrawal) {
					w.Status = StatusPending
					w.CreatedAt = now - 500*3600
				})
			},
		},
		{
			name: "已发放的单不再算积压", hours: 48,
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-paid", func(w *Withdrawal) {
					w.Status = StatusPaid
					w.ReviewedAt = now - 500*3600
				})
			},
		},
		{
			name: "驳回退回的单不再算积压", hours: 48,
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-rejected", func(w *Withdrawal) {
					w.Status = StatusRejected
					w.ReviewedAt = now - 500*3600
				})
			},
		},
		{
			name: "时限配 0 表示彻底关掉,不是「全都算积压」", hours: 0,
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedWithdrawal(t, gdb, "WD-late", func(w *Withdrawal) {
					w.Status = StatusApproved
					w.ReviewedAt = now - 5000*3600
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := wireTestDB(t)
			loadSLAConfig(t, 24, tc.hours)
			tc.seed(t, gdb)

			got := scanPayoutBacklog(context.Background())
			assert.Equal(t, tc.wantCount, got.Count)
			assert.Equal(t, tc.wantQuota, got.Quota)
			if tc.wantCount == 0 {
				assert.Zero(t, got.Oldest)
				assert.Zero(t, got.WaitedHours())
			} else {
				assert.Equal(t, now-200*3600, got.Oldest, "最久的那张才是 Oldest")
				assert.EqualValues(t, 200, got.WaitedHours())
			}
		})
	}
}

// 失去租约(ctx 取消)之后这一轮扫描不得再产生结果 —— 否则会与接管节点
// 一起刷同一条告警。
//
// 真正执行这条约束的是查询上的 WithContext(ctx):把它摘掉,下面这次调用会
// 照常数出那张超时单。所以这条用例钉住的是"这条查询接了 ctx",
// 而不是某一句前置判断的存在。
func TestScanPayoutBacklogStopsOnCancelledContext(t *testing.T) {
	gdb := wireTestDB(t)
	loadSLAConfig(t, 24, 48)
	seedWithdrawal(t, gdb, "WD-late", func(w *Withdrawal) {
		w.Status = StatusApproved
		w.ReviewedAt = common.GetTimestamp() - 500*3600
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Zero(t, scanPayoutBacklog(ctx).Count)
}

// 申请那一刻佣金就必须离开可用池 —— 这是「提现只做佣金扣除」这句话的字面意思,
// 也是整个模型成立的前提:管理员是照着单据发钱的,同一笔佣金只要还能再发一单,
// 就会被发第二次钱。
//
// 断言落在余额三桶的实际数值上,并逐步核对恒等式。
func TestSubmitDeductsCommissionImmediately(t *testing.T) {
	gdb := newTestDB(t)
	loadSLAConfig(t, 24, 48)
	now := common.GetTimestamp()

	require.NoError(t, gdb.Create(&commission.Balance{
		UserId: 1, AvailableQuota: 800000,
		AvailableFiat: decimal.RequireFromString("1360.000000"), FiatCurrency: "CNY",
		UnsettledAmount: decimal.Zero, CreatedAt: now, UpdatedAt: now,
	}).Error)

	w := &Withdrawal{
		WithdrawNo: "WD-deduct", IdemScope: idemScope, IdemKey: idemKeyOf(1, "req-deduct"),
		UserId: 1, Method: config.WithdrawMethodQuota, Status: StatusPending,
		Quota: 500000, CreatedAt: now, UpdatedAt: now,
	}
	cfg := config.Get().Withdraw
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		replay, err := submitInTx(tx, w, nil, acceptedRequest{
			IdemKey: "req-deduct", Method: config.WithdrawMethodQuota, Quota: 500000,
		}, cfg, "u1")
		require.Nil(t, replay)
		return err
	}))

	var bal commission.Balance
	require.NoError(t, gdb.Where("user_id = ?", 1).Take(&bal).Error)
	assert.EqualValues(t, 300000, bal.AvailableQuota, "申请即从可用池扣除")
	assert.EqualValues(t, 500000, bal.FrozenQuota, "扣掉的钱进入「已扣除待发放」")
	assert.EqualValues(t, 0, bal.WithdrawnQuota, "还没发放,不能记成已提现")
	assert.EqualValues(t, 800000,
		bal.AvailableQuota+bal.FrozenQuota+bal.WithdrawnQuota,
		"账本恒等式必须在申请这一步就成立")

	// 冻结流水必须以单号为 refNo 落一行:核销与退回都要拿它复核
	// "这笔钱当初真的被冻过且金额一致"。
	var rec commission.FreezeRecord
	require.NoError(t, gdb.Where("ref_no = ? AND action = ?",
		"WD-deduct", commission.FreezeActionFreeze).Take(&rec).Error)
	assert.EqualValues(t, 500000, rec.Quota)
}

// callAdminGet 打一次管理端 GET(角标类只读接口)。
func callAdminGet(t *testing.T, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/admin/withdraw/stats", nil)
	c.Set("id", 99)
	c.Set("username", "admin")
	c.Set("role", common.RoleAdminUser)
	h(c)
	return res
}

// wireTestDB 建一个内存库并把它挂成**全局扩展库句柄**。
//
// newTestDB 只返回句柄、不挂全局:大多数用例把 gdb 显式传进被测函数。而
// scanPayoutBacklog 与 handleAdminStats 都是自己调 db.Get() 的(生产里它们跑在
// 后台任务与 HTTP handler 里,没有地方能传句柄进来),不挂全局就永远拿到 nil,
// 于是"没有积压"和"根本没查"在断言上长得一模一样。
func wireTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := newTestDB(t)
	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
	})
	return gdb
}

// loadSLAConfig 载入一份只关心两道时限的配置。
//
// 走真实的 config.Load 而不是直接塞结构体:两道时限都有默认值,而"默认值有没有
// 生效"本身就是被测契约的一部分 —— 直接构造结构体会把 defaults.go 整个跳过。
func loadSLAConfig(t *testing.T, reviewHours, payoutHours int) {
	t.Helper()
	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota"]
  review_sla_hours: `+strconv.Itoa(reviewHours)+`
  payout_sla_hours: `+strconv.Itoa(payoutHours)+`
`)
}

// 两道时限的默认值必须都在。少配一个键就让积压彻底静默,不该是默认结局。
func TestPayoutSlaHasADefault(t *testing.T) {
	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota"]
`)
	assert.Equal(t, 72, config.Get().Withdraw.PayoutSLAHours,
		"没写 payout_sla_hours 时必须取默认值,而不是 0(=彻底关掉积压观测)")
	assert.Equal(t, 72, config.Get().Withdraw.ReviewSLAHours)
}
