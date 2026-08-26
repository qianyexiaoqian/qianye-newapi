package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ─────────────────── 法币结汇档必须进残留处置 ───────────────────

// qy_commission_fiat_rate 与费率表一样以用户分组名为键,而它决定的是
// **提现单的应付金额**。此前它整张表都不在残留处置里:改名之后费率跟着走、
// 结汇比例留在旧名字上,此后每一笔佣金都按回落层的比例冻结,三条恒等式全部
// 成立、fiatRateDegrade 一次不响、影响面清单里也不会提到这张表。
func TestFiatRateIsReportedInResidueProbe(t *testing.T) {
	gdb := newTestDB(t)
	seedFiatRate(t, gdb, "vip", "8.5", true)

	rows, err := probeResidue(gdb, "vip")
	require.NoError(t, err)

	var got *groupns.Residue
	for i := range rows {
		if rows[i].Table == (FiatRate{}).TableName() {
			got = &rows[i]
		}
	}
	require.NotNil(t, got, "影响面清单必须提到法币结汇档,否则运营在删除前看不到它")
	assert.EqualValues(t, 1, got.Rows)
	assert.Equal(t, groupns.ResidueClean, got.Disposition)
}

// 改名:整行跟着走。不跟着走的表现是这一档的结汇比例静默掉回回落层。
func TestFiatRateFollowsGroupRename(t *testing.T) {
	gdb := newTestDB(t)
	seedFiatRate(t, gdb, "vip", "8.5", true)

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "vip", "vip2", true)
	}))

	assert.EqualValues(t, 0, fiatRowsFor(t, gdb, "vip"))
	assert.EqualValues(t, 1, fiatRowsFor(t, gdb, "vip2"))
}

// 删除:整行删掉,绝不改写成迁移目标。留着的话,名字被将来某次新建重新用上时,
// 新分组直接继承一个没人批准过的结汇价。
func TestFiatRateIsRemovedOnGroupDelete(t *testing.T) {
	gdb := newTestDB(t)
	seedFiatRate(t, gdb, "vip", "8.5", true)
	seedFiatRate(t, gdb, "basic", "7.0", true)

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "vip", "basic", false)
	}))

	assert.EqualValues(t, 0, fiatRowsFor(t, gdb, "vip"), "孤儿行必须被清掉")
	assert.EqualValues(t, 1, fiatRowsFor(t, gdb, "basic"), "迁移目标自己那一档不许被覆盖")
	var kept FiatRate
	require.NoError(t, gdb.Where("group_name = ?", "basic").Take(&kept).Error)
	assert.True(t, kept.Rate.Equal(decimal.RequireFromString("7.0")),
		"源分组的比例不许被带到目标分组上")
}

// ─────────────────── 缓存失效必须在提交之后 ───────────────────

// 事务内失效会让一条并发读用**事务外**的连接把未提交的旧行重新填回缓存,
// 并按 settingsCacheSeconds 钉住最多 60 秒 —— 那 60 秒里每一笔返佣都按错档
// 冻结,而费率逐笔冻结、事后不追溯。框架把这件事单列成 AfterCommit 正是为此。
func TestCommissionResidueRefreshesCachesAfterCommitOnly(t *testing.T) {
	gdb := newTestDB(t)
	seedFiatRate(t, gdb, "vip", "8.5", true)
	require.NoError(t, gdb.Create(&GroupRate{
		GroupName: "vip", TopupRateUnits: 1000, ConsumeRateUnits: 1000, Enabled: true,
	}).Error)

	// 预热两张缓存。
	require.NotEmpty(t, groupRatesForTest(t))
	require.NotEmpty(t, fiatRatesForTest(t))

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return sweepResidue(tx, "vip", "vip2", true)
	}))
	// Sweep 自己不许刷缓存:此刻两张缓存都还该是旧的(改名前的 vip)。
	assert.Contains(t, groupRatesForTest(t), "vip",
		"sweepResidue 不许在事务里刷缓存")

	groupns.CommitResidues("vip", "vip2", true)
	assert.Contains(t, groupRatesForTest(t), "vip2", "提交之后必须刷新")
	assert.Contains(t, fiatRatesForTest(t), "vip2", "法币档必须和费率档一起刷")
}

// ─────────────────── 欠账调度不许有 (-1, 0) 盲区 ───────────────────

// `<= -1` 把 (-1, 0) 的欠账整段漏在调度之外,而那正是 reclaim 被 available
// 钳住时 carry 的落点(自动冲正额天然带小数)。落进去的人从两路里同时消失:
// 第一路要求 settled_amount <> gross_amount,而 absorbAccruals 已经把冲正行
// 写成 settled==gross。表现是 debt_blocked 永久为真、Withdrawable 恒返 0,
// 而 I1/I2 两条恒等式全部成立,面板一致显示正常。
func TestPendingInvitersPageSchedulesSubUnitDebts(t *testing.T) {
	cases := []struct {
		name      string
		unsettled string
		available int64
		want      bool
	}{
		{"欠账 -0.5 且账上还有可回收余额", "-0.5", 10000, true},
		{"欠账 -0.9", "-0.9", 10000, true},
		{"欠账 -1(旧判据的边界,必须仍然在)", "-1", 10000, true},
		{"欠账 -5000", "-5000", 10000, true},
		{"欠账 -0.5 但账上一分可回收余额都没有", "-0.5", 0, false},
		{"没有欠账也没有够格的正余数", "0.5", 10000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newTestDB(t)
			b := seedBalance(t, gdb, 777, tc.unsettled)
			b.AvailableQuota = tc.available
			b.DebtBlocked = tc.unsettled[0] == '-'
			require.NoError(t, gdb.Save(b).Error)

			ids, _, _, err := pendingInvitersPage(500, inviterCursor{})
			require.NoError(t, err)
			if tc.want {
				assert.Contains(t, ids, 777, "这个欠账必须被日结调度选中,否则提现被永久冻结")
			} else {
				assert.NotContains(t, ids, 777)
			}
		})
	}
}

// ─────────────────── 冲正上限的读与写不许有间隙 ───────────────────

// 「读 netAccrued → 写负额行」之间没有事务与行锁时,同一个下线的两笔并发退款
// 各读到同一个 remaining、各写一条 -remaining 的行,总冲正额可以是净计佣额的
// 两倍;两笔的 idemKey 各带各的 task_id,唯一索引拦不住。超额部分进 unsettled
// 负结转 → debt_blocked → 提现冻结,而 I1/I2 恒等式照样成立。
//
// 这里断言的是那条钱的不变量本身:无论冲正被调用多少次,
// 净额都不许被冲成负数。
func TestClawbackNeverExceedsNetAccrued(t *testing.T) {
	gdb := newTestDB(t)
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = 7001
		a.InviteeId = 8001
		a.SourceType = SourceConsume
		a.IdemScope = SourceConsume
		a.IdemKey = "consume:seed-1"
		a.GrossAmount = decimal.NewFromInt(500)
		a.BaseQuota = 10000
	})

	ctx := context.Background()
	for i := 0; i < 8; i++ {
		key := clawbackIdemKey("task-"+itoa(i), 8001, 10000)
		require.NoError(t, clawback(ctx, 8001, 10000, key, "ref", "audit"))

		net, err := netAccrued(gdb, 8001, 7001)
		require.NoError(t, err)
		assert.Falsef(t, net.IsNegative(),
			"第 %d 次冲正之后净额变成 %s —— 上限被冲穿,邀请人为别人名下的计佣买单", i, net)
	}
	net, err := netAccrued(gdb, 8001, 7001)
	require.NoError(t, err)
	assert.True(t, net.IsZero(), "冲平之后应当恰好为 0,实际 %s", net)
}

// ─────────────────── helpers ───────────────────

func fiatRowsFor(t *testing.T, gdb *gorm.DB, group string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, gdb.Model(&FiatRate{}).
		Where("group_name = ?", groupname.Normalize(group)).Count(&n).Error)
	return n
}

func groupRatesForTest(t *testing.T) map[string]GroupRate {
	t.Helper()
	return groupRates(context.Background())
}

func fiatRatesForTest(t *testing.T) map[string]FiatRate {
	t.Helper()
	return fiatRates(context.Background())
}
