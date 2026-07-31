package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 A5 的数据库级回归。
//
// A5 的根因是"算术对了、调度断链":computeSettlement 一直算得出
// carry,但没有任何一条路径会**再次选中**这个邀请人,settleUser 也在
// len(rows)==0 处直接早退。因此只测纯函数(mergeInviterIds / settleNeeded /
// computeSettlement)是测不到缺陷的 —— 把两处修复整段回滚,那些测试全绿。
//
// 下面两条测试各自独立地钉死修复的一半:
//   - TestPendingInvitersSelectsCarryOnlyBalance 只调 pendingInviters,
//     不碰 settleUser;回滚"carry 来源"那一路即失败。
//   - TestSettleUserFlushesCarryWithoutAccrualRows 只调 settleUser,
//     不碰 pendingInviters;回滚"len(rows)==0 早退"即失败。

// commissionConfig 返回一份最小可用的扩展配置。
//
// 刻意只设被测逻辑真正读到的两项:费率一律走 qy_settings 运营覆盖
// (见 setSettingOverride),这样费率配置项的单位/命名怎么演进都不会
// 波及这批调度层测试。
func commissionConfig(minSettle int64) *config.Config {
	c := &config.Config{}
	c.Enabled = true
	c.Commission.Enabled = true
	c.Commission.MinSettleQuota = minSettle
	return c
}

// setSettingOverride 写一条运营覆盖到 qy_settings 并让缓存立即失效。
func setSettingOverride(t *testing.T, gdb *gorm.DB, key, value string) {
	t.Helper()
	require.NoError(t, gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "updated_at"}),
	}).Create(&qymodel.Setting{
		Scope: settingScope, K: key, V: value, UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()
}

// TestPendingInvitersSelectsCarryOnlyBalance 是 A5 的第一半:选人。
//
// 场景:邀请人 42 上一轮被日封顶削掉 4000,全额留在 unsettled_amount 里,
// 而 absorbAccruals 已经把本批**全部** accrual 的 settled_amount 写成
// gross_amount —— 于是"还有未被吸收的计佣行"那一路 WHERE 对它再也不命中。
// 只按第一路选人的话,这 4000 要等下线再产生新的计佣行才有机会发出;
// 下线一停消费就是永久拿不到。
//
// 断言直接落在 pendingInviters 的返回值上,SQL 由真实数据库执行。
func TestPendingInvitersSelectsCarryOnlyBalance(t *testing.T) {
	t.Run("只剩余数、无任何计佣行的邀请人必须被选中", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		// 邀请人 42:一条 accrual 都没有,只有 4000 的余数。
		seedBalance(t, gdb, 42, "4000")

		var accrualRows int64
		require.NoError(t, gdb.Model(&Accrual{}).Count(&accrualRows).Error)
		require.EqualValues(t, 0, accrualRows, "前提:库里确实没有任何计佣行")

		ids, err := pendingInviters(settleInviterBatch)
		require.NoError(t, err)
		assert.Equal(t, []int{42}, ids,
			"carry-only 的邀请人没被选中 = 这 4000 永远发不出去")
	})

	t.Run("两路来源合并且按 user_id 升序", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		// 第一路:还有未被吸收的计佣行(settled_amount <> gross_amount)。
		seedAccrual(t, gdb, 1, func(a *Accrual) { a.InviterId = 7 })
		// 第二路:只剩余数。
		seedBalance(t, gdb, 42, "4000")
		// 两路都命中的邀请人只能出现一次。
		seedAccrual(t, gdb, 2, func(a *Accrual) { a.InviterId = 9 })
		seedBalance(t, gdb, 9, "2500")

		ids, err := pendingInviters(settleInviterBatch)
		require.NoError(t, err)
		assert.Equal(t, []int{7, 9, 42}, ids)
	})

	t.Run("已被完全吸收的计佣行不再选人,余数才是唯一入口", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		// 完全复刻第一轮结算之后的库状态:settled_amount == gross_amount。
		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(5000)
			a.SettledAmount = decimal.NewFromInt(5000)
			a.Status = StatusSettled
		})
		seedBalance(t, gdb, 42, "4000")

		ids, err := pendingInviters(settleInviterBatch)
		require.NoError(t, err)
		require.Equal(t, []int{42}, ids)

		// 把余数也清掉,第一路依旧不命中 —— 这一步证明上面那条命中确实
		// 来自 carry 来源,而不是 accrual 那一路顺带捞到的。
		require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", 42).
			Update("unsettled_amount", decimal.Zero).Error)
		ids, err = pendingInviters(settleInviterBatch)
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("余数不够发的邀请人不选,避免每轮白跑一次加锁事务", func(t *testing.T) {
		gdb := newTestDB(t)
		// 结算门槛 1000:net 要 >= 1000 才发得出去,选人门槛必须是同一个数。
		useConfig(t, commissionConfig(1000))

		seedBalance(t, gdb, 11, "999")  // 选中也发不出来
		seedBalance(t, gdb, 12, "1000") // 恰好够
		seedBalance(t, gdb, 13, "0.4")  // 连 1 都不到

		ids, err := pendingInviters(settleInviterBatch)
		require.NoError(t, err)
		assert.Equal(t, []int{12}, ids)
	})

	t.Run("limit 截断在合并之后", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		// 第一路给出 8、9,第二路给出 1。先截第一路会让 carry 来源整批
		// 被挤掉,那正是"carry 永远排不上队"的另一种形态。
		seedAccrual(t, gdb, 1, func(a *Accrual) { a.InviterId = 8 })
		seedAccrual(t, gdb, 2, func(a *Accrual) { a.InviterId = 9 })
		seedBalance(t, gdb, 1, "4000")

		ids, err := pendingInviters(2)
		require.NoError(t, err)
		assert.Equal(t, []int{1, 8}, ids)
	})
}

// TestSettleUserFlushesCarryWithoutAccrualRows 是 A5 的第二半:执行。
//
// 即使选人那一路把邀请人交了出来,settleUser 只要在 len(rows)==0 处早退,
// 这 4000 依旧发不出去。这条测试完全不经过 pendingInviters,直接调
// settleUser,断言落在真实落库的 Settlement 行与 Balance 行上。
func TestSettleUserFlushesCarryWithoutAccrualRows(t *testing.T) {
	t.Run("无计佣行时仍把余数发出去", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)

		seedBalance(t, gdb, 42, "4000")

		require.NoError(t, settleUser(42))

		rows := settlementsOf(t, gdb, 42)
		require.Len(t, rows, 1, "carry-only 结算被跳过 = 这 4000 永远发不出去")
		s := rows[0]
		assert.EqualValues(t, 4000, s.GrantedQuota)
		assert.EqualValues(t, 0, s.ReclaimedQuota)
		assert.Equal(t, 0, s.AccrualCount, "本轮确实没有任何计佣增量")
		assert.Equal(t, "4000", s.CarryBefore.String())
		assert.True(t, s.CarryAfter.IsZero(), "余数必须被清空,否则下轮会重复发")
		// batchRate 在 delta 为零时不能落回 0:那会让额度加了、法币没加,
		// AvailableFiat 与 AvailableQuota 永久漂移,提现按法币折算会少给钱。
		assert.True(t, s.UsdRateWeighted.IsPositive(), "carry-only 轮的冻结汇率不能为零")
		assert.True(t, s.FiatDelta.IsPositive())

		bal := balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.True(t, bal.UnsettledAmount.IsZero())
		assert.EqualValues(t, 4000, bal.AvailableQuota)
		assert.EqualValues(t, 4000, bal.TotalEarnedQuota)
		assert.True(t, bal.AvailableFiat.IsPositive())
		assert.False(t, bal.DebtBlocked)
		assert.Positive(t, bal.LastSettledAt)
	})

	t.Run("日封顶下逐轮补发,一分不少", func(t *testing.T) {
		gdb := newTestDB(t)
		cfg := commissionConfig(0)
		useConfig(t, cfg)
		useMoneyGlobals(t, 7.3, 500000)

		// 日封顶只能走运营覆盖(qy_settings),YAML 里没有这一项。
		setSettingOverride(t, gdb, keyDailyCapQuota, "1000")
		require.EqualValues(t, 1000, effective().DailyCapQuota, "前提:日封顶已生效")

		// 已成熟计佣合计 5000,第一轮只发得出 1000。
		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(5000)
		})

		require.NoError(t, settleUser(42))
		bal := balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		require.EqualValues(t, 1000, bal.AvailableQuota)
		require.Equal(t, "4000", bal.UnsettledAmount.String(), "被削掉的部分进了余数")

		var absorbed Accrual
		require.NoError(t, gdb.First(&absorbed).Error)
		require.True(t, absorbed.SettledAmount.Equal(absorbed.GrossAmount),
			"前提:absorbAccruals 已把整批标成完全吸收,第一路来源从此不再命中")

		// 后续每一轮都没有任何新增量。日封顶按 UTC 自然日算,同一天内
		// dailyRemaining 会算上已发的 1000,所以这几轮各自只能再发 0;
		// 关键断言是"轮轮都在落单、余数在往下走",而不是早退。
		require.NoError(t, settleUser(42))
		bal = balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.Equal(t, "4000", bal.UnsettledAmount.String())

		// 把日封顶去掉,余数必须一次性全额补发出来。
		require.NoError(t, gdb.Exec("DELETE FROM qy_settings WHERE k = ?", keyDailyCapQuota).Error)
		invalidateSettings()
		require.NoError(t, settleUser(42))

		bal = balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.True(t, bal.UnsettledAmount.IsZero())
		assert.EqualValues(t, 5000, bal.AvailableQuota, "被日封顶削掉的部分必须一分不少地补发完")
		assert.EqualValues(t, 5000, bal.TotalEarnedQuota)
	})

	t.Run("余数不足一个额度时不落单", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)

		seedBalance(t, gdb, 13, "0.4")
		require.NoError(t, settleUser(13))

		assert.Empty(t, settlementsOf(t, gdb, 13),
			"落一张全零单只会让审计表按邀请人数 × 结算周期膨胀")
		bal := balanceOf(t, gdb, 13)
		require.NotNil(t, bal)
		assert.Equal(t, "0.4", bal.UnsettledAmount.String())
		assert.EqualValues(t, 0, bal.AvailableQuota)
	})

	t.Run("对没有余额行的用户不凭空建行", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)

		// 管理端的"立即结算"可以对任意 user_id 调用。carry-only 分支必须
		// 先做一次不加锁的预筛,否则 lockBalance 会给每个被点过的 id
		// 建出一行空余额。
		require.NoError(t, settleOne(777))
		assert.Nil(t, balanceOf(t, gdb, 777))
		assert.Empty(t, settlementsOf(t, gdb, 777))
	})
}
