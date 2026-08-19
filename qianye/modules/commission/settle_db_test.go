package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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

	t.Run("两路来源合并且每人只出现一次", func(t *testing.T) {
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
		// 顺序由"等得最久的先发"决定(见下面那条饥饿测试),这里只钉住集合与去重:
		// 三个人一个不少、9 不因为两路都命中就出现两次。
		assert.ElementsMatch(t, []int{7, 9, 42}, ids)
		assert.Len(t, ids, 3, "两路都命中的邀请人被算了两次")
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
		// 轮流取:第一路给 8,第二路给 1,批量用满。绝不能是 [8,9] ——
		// 那就是第二路被第一路整批挤掉。
		assert.Equal(t, []int{8, 1}, ids)
	})
}

// TestPendingInvitersDoesNotStarveLargeIds 钉住"活跃邀请人超过批量时不会有人
// 永远排不进来"。
//
// 旧代码两路来源都是 ORDER BY <id> ASC LIMIT settleInviterBatch:一旦活跃
// 邀请人多于批量,每一轮取到的永远是 id 最小的那一批,而他们的下线还在消费,
// 所以下一轮他们照样命中。id 更大的邀请人永远排不进来,佣金无限期停在
// qy_commission_accrual 里发不出去 —— 钱不会丢,但用户永远拿不到,而且三条
// 恒等式全部成立、队列没满、没有降级,没有任何信号会响。
//
// 断言直接落在 pendingInviters 的返回值上,SQL 由真实数据库执行。
func TestPendingInvitersDoesNotStarveLargeIds(t *testing.T) {
	t.Run("批量装不下时先发等得最久的那一个", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		now := common.GetTimestamp()
		// id 最大的人等得最久(mature_at 最老),id 最小的人刚刚成熟。
		// 按 id 升序会取到 11,按等待时长取到 13。
		seedAccrual(t, gdb, 1, func(a *Accrual) { a.InviterId = 11; a.MatureAt = now - 60 })
		seedAccrual(t, gdb, 2, func(a *Accrual) { a.InviterId = 12; a.MatureAt = now - 600 })
		seedAccrual(t, gdb, 3, func(a *Accrual) { a.InviterId = 13; a.MatureAt = now - 6000 })

		ids, err := pendingInviters(1)
		require.NoError(t, err)
		assert.Equal(t, []int{13}, ids,
			"按 id 升序选人 = id 大的邀请人永远排不进来")
	})

	t.Run("发过的人退到队尾,下一轮轮到别人", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		now := common.GetTimestamp()
		seedAccrual(t, gdb, 1, func(a *Accrual) { a.InviterId = 11; a.MatureAt = now - 600 })
		seedAccrual(t, gdb, 2, func(a *Accrual) { a.InviterId = 12; a.MatureAt = now - 6000 })

		// 批量只有 1:第一轮必须是等得最久的 12。
		ids, err := pendingInviters(1)
		require.NoError(t, err)
		require.Equal(t, []int{12}, ids)

		// 复刻结算之后的库状态:12 名下的计佣行被吸收,于是他退出候选集。
		require.NoError(t, gdb.Model(&Accrual{}).Where("inviter_id = ?", 12).
			Updates(map[string]any{"settled_amount": gorm.Expr("gross_amount")}).Error)

		// 第二轮必须轮到 11。旧口径下 11 只有在 12 消失之后才轮得到,
		// 而真实站点上 12 的下线一直在消费,他永远不会消失。
		ids, err = pendingInviters(1)
		require.NoError(t, err)
		assert.Equal(t, []int{11}, ids, "上一轮发过的人没有退到队尾")

		// 12 再次产生新的计佣行:mature_at 是新的,他排到 11 后面而不是插队。
		seedAccrual(t, gdb, 3, func(a *Accrual) { a.InviterId = 12; a.MatureAt = now - 10 })
		ids, err = pendingInviters(1)
		require.NoError(t, err)
		assert.Equal(t, []int{11}, ids, "新计佣行不该让老用户插回队首")
	})

	t.Run("carry 那一路按上次结算时间排,不按 user_id", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		now := common.GetTimestamp()
		// 21 刚结算过,23 已经很久没结算了。按 user_id 升序会取到 21。
		b1 := seedBalance(t, gdb, 21, "4000")
		b3 := seedBalance(t, gdb, 23, "4000")
		require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", b1.UserId).
			Update("last_settled_at", now-60).Error)
		require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", b3.UserId).
			Update("last_settled_at", now-6000).Error)

		ids, err := pendingInviters(1)
		require.NoError(t, err)
		assert.Equal(t, []int{23}, ids,
			"carry 那一路按 user_id 升序 = user_id 大的零头永远发不出去")
	})

	t.Run("两路都塞满时各拿到一半名额", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		now := common.GetTimestamp()
		// 第一路 4 个人,全部比第二路等得久 —— 顺序拼接会让第二路一个名额都没有。
		for i, id := range []int{31, 32, 33, 34} {
			seedAccrual(t, gdb, i+1, func(a *Accrual) { a.InviterId = id; a.MatureAt = now - 9000 })
		}
		for _, id := range []int{41, 42, 43, 44} {
			seedBalance(t, gdb, id, "4000")
		}

		ids, err := pendingInviters(4)
		require.NoError(t, err)
		require.Len(t, ids, 4)
		var fromAccrual, fromCarry int
		for _, id := range ids {
			if id < 40 {
				fromAccrual++
			} else {
				fromCarry++
			}
		}
		assert.Equal(t, 2, fromAccrual, "第一路占满了整个批量")
		assert.Equal(t, 2, fromCarry, "carry 那一路一个名额都没拿到")
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

		settleUserOnce(t, 42)

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

		settleUserOnce(t, 42)
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
		settleUserOnce(t, 42)
		bal = balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.Equal(t, "4000", bal.UnsettledAmount.String())

		// 把日封顶去掉,余数必须一次性全额补发出来。
		require.NoError(t, gdb.Exec("DELETE FROM qy_settings WHERE k = ?", keyDailyCapQuota).Error)
		invalidateSettings()
		settleUserOnce(t, 42)

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
		settleUserOnce(t, 13)

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

// ───────────────────────── 空值结算轮次不落行 ─────────────────────────

// ledgerDrift 独立重算 I1:Σ计佣行已结算金额 − Σ结算单净额 − 未结算余数。
//
// 刻意不复用生产代码里那段(api_admin.go 的体检)—— 用被测代码去证明被测
// 代码是自洽的,证不出任何东西。这里直接从三张表把数拉出来自己算。
func ledgerDrift(t *testing.T, gdb *gorm.DB, userId int) decimal.Decimal {
	t.Helper()
	var settled, granted, reclaimed, carry string
	require.NoError(t, gdb.Model(&Accrual{}).Where("inviter_id = ? AND status <> ?", userId, StatusVoided).
		Select("COALESCE(SUM(settled_amount), 0)").Scan(&settled).Error)
	require.NoError(t, gdb.Model(&Settlement{}).Where("user_id = ?", userId).
		Select("COALESCE(SUM(granted_quota), 0)").Scan(&granted).Error)
	require.NoError(t, gdb.Model(&Settlement{}).Where("user_id = ?", userId).
		Select("COALESCE(SUM(reclaimed_quota), 0)").Scan(&reclaimed).Error)
	require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", userId).
		Select("COALESCE(SUM(unsettled_amount), 0)").Scan(&carry).Error)
	return decimal.RequireFromString(settled).
		Sub(decimal.RequireFromString(granted)).
		Add(decimal.RequireFromString(reclaimed)).
		Sub(decimal.RequireFromString(carry))
}

// TestSettleUserSkipsZeroValueSettlement 钉死"空轮次不落行"。
//
// 生产 settle_interval_seconds 默认 300 秒 = 每天 288 轮。旧判据是
// `accrualCount > 0 || net != 0`,于是只要某个下线当天有过一笔零头消费,
// 这个邀请人每一轮都会落一张 granted=0 / reclaimed=0 的结算单,直到零头
// 攒够 1 额度为止 —— 备份库 38 行结算单里有 3 行正是这么来的。
//
// 关键是"不落行"不能变成"不干活":这批计佣行照样要被吸收、余数照样要往前滚,
// 而且 I1 在落行与不落行两种情形下都必须成立。
func TestSettleUserSkipsZeroValueSettlement(t *testing.T) {
	t.Run("有计佣行但发不出整数额度时不落结算单", func(t *testing.T) {
		gdb := newTestDB(t)
		// settleUser 会按邀请人的账号分组解析法币折算比例,那一步读主库 users。
		// 不建这张表的话 model.DB 是 nil,断言还没跑到就先 panic。
		useMainDB(t, &model.User{})
		useConfig(t, commissionConfig(1))
		useMoneyGlobals(t, 7.3, 500000)

		// 0.4 额度的零头:floor(0.4) = 0,一分都发不出去。
		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.RequireFromString("0.4")
		})

		settleUserOnce(t, 42)

		assert.Empty(t, settlementsOf(t, gdb, 42),
			"全零结算单每 300 秒一张,一年就是 288 × 365 行/人")

		// 不落行 ≠ 不干活:这批计佣行必须已被吸收,余数必须已经收下这 0.4。
		var absorbed Accrual
		require.NoError(t, gdb.First(&absorbed).Error)
		assert.True(t, absorbed.SettledAmount.Equal(absorbed.GrossAmount),
			"没被吸收的话下一轮 pendingInviters 会把它再捞出来,永远重算")
		assert.EqualValues(t, 0, absorbed.SettlementId, "本轮没有结算单可指")

		bal := balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.Equal(t, "0.4", bal.UnsettledAmount.String())
		assert.EqualValues(t, 0, bal.AvailableQuota)
		assert.True(t, ledgerDrift(t, gdb, 42).IsZero(), "I1 在不落行的轮次必须成立")

		// 再跑一轮:此时既没有新计佣行,余数也不够发 —— 什么都不该发生。
		before := bal.UpdatedAt
		settleUserOnce(t, 42)
		assert.Empty(t, settlementsOf(t, gdb, 42))
		bal = balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.Equal(t, before, bal.UpdatedAt, "空转轮次连余额行的 updated_at 都不该写")
	})

	t.Run("零头攒够之后照常落单,一分不少", func(t *testing.T) {
		gdb := newTestDB(t)
		// settleUser 会按邀请人的账号分组解析法币折算比例,那一步读主库 users。
		// 不建这张表的话 model.DB 是 nil,断言还没跑到就先 panic。
		useMainDB(t, &model.User{})
		useConfig(t, commissionConfig(1))
		useMoneyGlobals(t, 7.3, 500000)

		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.RequireFromString("0.4")
		})
		settleUserOnce(t, 42)
		require.Empty(t, settlementsOf(t, gdb, 42))

		// 第二笔零头把余数推过 1。
		seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.RequireFromString("0.7")
		})
		settleUserOnce(t, 42)

		rows := settlementsOf(t, gdb, 42)
		require.Len(t, rows, 1, "攒够了就必须落单")
		assert.EqualValues(t, 1, rows[0].GrantedQuota)
		assert.Equal(t, "0.4", rows[0].CarryBefore.String(),
			"上一轮虽然没落单,余数已经收下了那 0.4")
		assert.Equal(t, "0.1", rows[0].CarryAfter.String())
		assert.Equal(t, "0.7", rows[0].DeltaAmount.String())

		bal := balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.EqualValues(t, 1, bal.AvailableQuota)
		assert.Equal(t, "0.1", bal.UnsettledAmount.String())
		assert.True(t, ledgerDrift(t, gdb, 42).IsZero(), "I1 在落行的轮次同样必须成立")
	})

	t.Run("日封顶把发放削成零的轮次也不落单", func(t *testing.T) {
		gdb := newTestDB(t)
		// settleUser 会按邀请人的账号分组解析法币折算比例,那一步读主库 users。
		// 不建这张表的话 model.DB 是 nil,断言还没跑到就先 panic。
		useMainDB(t, &model.User{})
		useConfig(t, commissionConfig(1))
		useMoneyGlobals(t, 7.3, 500000)
		setSettingOverride(t, gdb, keyDailyCapQuota, "1000")
		require.EqualValues(t, 1000, effective().DailyCapQuota)

		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(1000)
		})
		settleUserOnce(t, 42)
		require.Len(t, settlementsOf(t, gdb, 42), 1, "第一轮把日封顶用满")

		// 第二笔计佣行落在同一个 UTC 自然日,dailyRemaining 已经是 0。
		seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(3000)
		})
		settleUserOnce(t, 42)

		assert.Len(t, settlementsOf(t, gdb, 42), 1,
			"封顶期间每轮落一张全零单 = 行数按结算周期膨胀")
		bal := balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.Equal(t, "3000", bal.UnsettledAmount.String(), "被削掉的钱必须留在余数里")
		assert.EqualValues(t, 1000, bal.AvailableQuota)
		assert.True(t, ledgerDrift(t, gdb, 42).IsZero())

		// 封顶解除后一分不少地补发出来。
		require.NoError(t, gdb.Exec("DELETE FROM qy_settings WHERE k = ?", keyDailyCapQuota).Error)
		invalidateSettings()
		settleUserOnce(t, 42)
		bal = balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.EqualValues(t, 4000, bal.AvailableQuota)
		assert.True(t, bal.UnsettledAmount.IsZero())
		assert.True(t, ledgerDrift(t, gdb, 42).IsZero())
	})

	t.Run("回收轮次照常落单", func(t *testing.T) {
		gdb := newTestDB(t)
		// settleUser 会按邀请人的账号分组解析法币折算比例,那一步读主库 users。
		// 不建这张表的话 model.DB 是 nil,断言还没跑到就先 panic。
		useMainDB(t, &model.User{})
		useConfig(t, commissionConfig(1))
		useMoneyGlobals(t, 7.3, 500000)

		seedBalance(t, gdb, 42, "0")
		require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", 42).Updates(map[string]any{
			"available_quota":    100,
			"total_earned_quota": 100,
		}).Error)
		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.SourceType = SourceClawback
			a.GrossAmount = decimal.NewFromInt(-50)
		})

		settleUserOnce(t, 42)

		rows := settlementsOf(t, gdb, 42)
		require.Len(t, rows, 1, "回收是钱动了,必须留痕")
		assert.EqualValues(t, 50, rows[0].ReclaimedQuota)
		assert.EqualValues(t, 0, rows[0].GrantedQuota)
		assert.EqualValues(t, 0, rows[0].GrantedQuota,
			"granted_quota=0 但 reclaimed_quota>0 的行不是空行,不能被当成噪音清掉")
		assert.True(t, ledgerDrift(t, gdb, 42).IsZero())
	})
}
