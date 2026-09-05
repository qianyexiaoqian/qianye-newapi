package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// autocredit_db_test.go —— D-16 的主链路真跑:
//
//	日计佣(消费日桶,金额按刻度折成星屑)→ 持有期满结算进可用余额
//	  → 自动入账:一个扩展库事务里 available → credited + 星屑到账
//
// # 这里守的是什么
//
// 入账不再跨库,所以此前那三条 Failed / Uncertain / 人工裁决的用例连同它们守的
// 机制一起没了。**没被删掉的是它们真正在守的东西**,只是判据换了落点:
//
//	"钱不会凭空多出来"      → 佣金 available 减掉多少,星屑 available 就加多少
//	"钱不会半路蒸发"        → 两边在同一个事务里,星屑写失败时佣金余额一个字节不动
//	"重放不会发第二次"      → 幂等键是 credit_no,再跑一轮不产生新流水
//	"账本自洽"              → I2(可用 + 已入账 == 已结算 − 已冲正)
//	"门槛与分批照旧生效"    → min_credit_stardust / max_per_order_stardust
//
// 判据全在 WHERE 条件、跨两张账本的金额守恒与事务边界上,纯函数测不出来。

const (
	creditInviter = 42
	creditInvitee = 900
)

// creditEnv 装好扩展库(佣金 + 星屑)与主库(users / logs),并配好刻度与门槛。
type creditEnv struct {
	ext  *gorm.DB
	main *gorm.DB
}

// creditQPU 是本文件统一使用的刻度:1 星屑 = 10,000 额度。
//
// 刻意不用生产默认的 500,000:那会让"下线消费多少额度"这一列在用例里变成七位数,
// 每条断言都要先心算一次。10,000 让 5% 的计佣落在个位数星屑,金额守恒一眼可验,
// 而链路上没有任何一步依赖这个具体数字 —— calcGross 的精度由 accrual_test.go 按
// 真实刻度单独钉住。
const creditQPU = 10_000

func newCreditEnv(t *testing.T, mutate func(*config.Config)) *creditEnv {
	t.Helper()
	ext := newTestDB(t)
	main := useMainDB(t, &model.User{}, &model.Log{})

	prevType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	model.InitCol()
	prevLogDB := model.LOG_DB
	model.LOG_DB = main
	prevMem, prevRedis := common.MemoryCacheEnabled, common.RedisEnabled
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	t.Cleanup(func() {
		model.LOG_DB = prevLogDB
		common.SetMainDatabaseType(prevType)
		model.InitCol()
		common.MemoryCacheEnabled, common.RedisEnabled = prevMem, prevRedis
	})

	on := true
	cfg := commissionRateConfig("10", "5")
	cfg.Runtime.ColdPathTimeoutMs = 3000
	cfg.Audit = config.Audit{Enabled: &on}
	cfg.Stardust.Enabled = true
	cfg.Stardust.QuotaPerUnit = creditQPU
	cfg.Commission.HoldingDays = 0
	cfg.Commission.MinCreditStardust = 10
	// 它同时是单次计佣的封顶与单次入账的上限:两天各 50 星屑的计佣都够不到它,
	// 而 100 星屑的可用余额会被它切成 70 + 30 两笔。
	cfg.Commission.MaxPerOrderStardust = 70
	if mutate != nil {
		mutate(cfg)
	}
	useConfig(t, cfg)

	cacheUser(t, creditInviter, 0, "default")
	cacheUser(t, creditInvitee, creditInviter, "default")
	return &creditEnv{ext: ext, main: main}
}

func (e *creditEnv) credits(t *testing.T, userId int) []Credit {
	t.Helper()
	var rows []Credit
	require.NoError(t, e.ext.Where("user_id = ?", userId).Order("id asc").Find(&rows).Error)
	return rows
}

// sdBalance 读星屑余额行;没有行时返回零值(入账从没发生过)。
func (e *creditEnv) sdBalance(t *testing.T, userId int) stardust.Balance {
	t.Helper()
	var rows []stardust.Balance
	require.NoError(t, e.ext.Where("user_id = ?", userId).Find(&rows).Error)
	if len(rows) == 0 {
		return stardust.Balance{UserId: userId}
	}
	return rows[0]
}

// sdLedger 读这个人 kind=commission_credit 的星屑流水。
func (e *creditEnv) sdLedger(t *testing.T, userId int) []stardust.Ledger {
	t.Helper()
	var rows []stardust.Ledger
	require.NoError(t, e.ext.Where("user_id = ? AND kind = ?", userId, string(stardust.KindCommissionCredit)).
		Order("id asc").Find(&rows).Error)
	return rows
}

// accrueAndSettle 让下线在两天前与三天前各消费 quota(两个日桶),计佣后结算进邀请人的可用余额。
func (e *creditEnv) accrueAndSettle(t *testing.T, quota int64) {
	t.Helper()
	e.accrueDaysAndSettle(t, 2, quota)
}

// accrueDaysAndSettle 铺开 days 个连续日桶,每个桶消费 quota,再一次结算。
//
// 需要"可用余额远大于单次入账上限"时必须靠**多个日桶**堆出来,不能靠把单日的
// quota 调大:max_per_order_stardust 同时是单次计佣的封顶,单日调大只会被它当场
// 削掉,余额根本涨不上去(这个坑真的踩过一次,表现是分批用例少跑一轮)。
func (e *creditEnv) accrueDaysAndSettle(t *testing.T, days int, quota int64) {
	t.Helper()
	now := common.GetTimestamp()
	for d := 2; d < 2+days; d++ {
		require.NoError(t, accrueConsume(context.Background(),
			consumeEvent{InviteeId: creditInvitee, Quota: quota, At: now - int64(d)*secondsPerDay}))
	}
	settleUserOnce(t, creditInviter)
}

func TestAutoCredit_EndToEndSplitsByMaxPerOrderAndLandsInStardust(t *testing.T) {
	env := newCreditEnv(t, nil)
	// 两天各 10,000,000 额度 × 5% / 10,000 = 50 星屑,共 100;max_per_order 70 ⇒ 70 + 30。
	env.accrueAndSettle(t, 10_000_000)
	bal := balanceOf(t, env.ext, creditInviter)
	require.EqualValues(t, 100, bal.Available, "前提:持有期 0 的消费桶两天后已成熟并结算")

	runCredit(context.Background())

	bal = balanceOf(t, env.ext, creditInviter)
	assert.EqualValues(t, 0, bal.Available)
	assert.EqualValues(t, 100, bal.Credited)
	assert.Positive(t, bal.LastCreditedAt)
	assertLedgerIdentity(t, bal)

	// 跨两张账本的金额守恒:佣金侧 credited 增加多少,星屑侧 available 就增加多少。
	// 这是本文件最重要的一条断言 —— 它是"钱既没多也没少"唯一的直接证据。
	sd := env.sdBalance(t, creditInviter)
	assert.EqualValues(t, 100, sd.Available, "星屑余额必须恰好等于已入账的佣金")
	assert.EqualValues(t, 100, sd.TotalEarned)

	rows := env.credits(t, creditInviter)
	require.Len(t, rows, 2, "按 max_per_order_stardust 分成两笔")
	assert.EqualValues(t, 70, rows[0].Amount)
	assert.EqualValues(t, 30, rows[1].Amount)

	ledger := env.sdLedger(t, creditInviter)
	require.Len(t, ledger, 2, "一笔入账一行星屑流水")
	for i, c := range rows {
		assert.Equal(t, CreditStatusDone, c.Status)
		assert.Positive(t, c.FinishedAt)
		// credit 行与星屑流水行必须互相指得到:少了这一条,"这 30 星屑是哪来的"
		// 在两张表之间就断了链,只能靠时间戳猜。
		assert.Equal(t, ledger[i].LedgerNo, c.LedgerNo, "credit 行没有指向它那一笔星屑流水")
		assert.Equal(t, c.CreditNo, ledger[i].RefNo)
		assert.EqualValues(t, c.Amount, ledger[i].Amount)
	}

	var audits []qymodel.AuditLog
	require.NoError(t, env.ext.Where("action = ?", "commission.credit.batch").Find(&audits).Error)
	require.Len(t, audits, 1, "自动入账每批一条系统审计")
	assert.Equal(t, qymodel.ActorSystem, audits[0].ActorType)
	assert.EqualValues(t, 100, audits[0].AmountQuota)

	// 再跑一轮什么都不该发生:余额已经清空,没有新的入账,星屑也不再增加。
	runCredit(context.Background())
	assert.Len(t, env.credits(t, creditInviter), 2)
	assert.Len(t, env.sdLedger(t, creditInviter), 2)
	assert.EqualValues(t, 100, env.sdBalance(t, creditInviter).Available)
}

func TestAutoCredit_BelowMinCreditDoesNothing(t *testing.T) {
	env := newCreditEnv(t, func(c *config.Config) { c.Commission.MinCreditStardust = 50 })
	// 两天各 4,000,000 × 5% / 10,000 = 20 星屑,共 40 < 50。
	env.accrueAndSettle(t, 4_000_000)

	runCredit(context.Background())

	bal := balanceOf(t, env.ext, creditInviter)
	assert.EqualValues(t, 40, bal.Available, "未达门槛的余额原地不动")
	assert.EqualValues(t, 0, bal.Credited)
	assert.Empty(t, env.credits(t, creditInviter))
	assert.EqualValues(t, 0, env.sdBalance(t, creditInviter).Available, "没到门槛就一颗星屑都不发")
	var audits int64
	require.NoError(t, env.ext.Model(&qymodel.AuditLog{}).Where("action = ?", "commission.credit.batch").Count(&audits).Error)
	assert.EqualValues(t, 0, audits, "没发钱就不写审计")
}

func TestAutoCredit_DebtBlockedIsSkipped(t *testing.T) {
	env := newCreditEnv(t, nil)
	env.accrueAndSettle(t, 10_000_000)
	require.NoError(t, env.ext.Model(&Balance{}).Where("user_id = ?", creditInviter).
		Update("debt_blocked", true).Error)

	runCredit(context.Background())

	assert.Empty(t, env.credits(t, creditInviter), "欠账账号一笔都不发")
	assert.EqualValues(t, 100, balanceOf(t, env.ext, creditInviter).Available)
	assert.EqualValues(t, 0, env.sdBalance(t, creditInviter).Available)
}

// TestAutoCredit_NegativeCarryIsSkipped 钉住"负余数 = 欠账"这一支。
//
// debt_blocked 与负余数是两个独立的闸(前者由冲正打开,后者是结算算出来的),
// 预筛 SQL 与锁内复核都要各判一次。少了任何一处,一个欠着钱的推广人会一边欠账
// 一边继续拿到入账。
func TestAutoCredit_NegativeCarryIsSkipped(t *testing.T) {
	env := newCreditEnv(t, nil)
	env.accrueAndSettle(t, 10_000_000)
	require.NoError(t, env.ext.Model(&Balance{}).Where("user_id = ?", creditInviter).
		Update("unsettled_amount", "-0.5").Error)

	runCredit(context.Background())

	assert.Empty(t, env.credits(t, creditInviter), "负余数的账号一笔都不发")
	assert.EqualValues(t, 0, env.sdBalance(t, creditInviter).Available)
}

// TestAutoCredit_StardustFailureLeavesCommissionBalanceUntouched 是原子性那一条。
//
// D-15 时"星屑写失败但佣金余额已经扣了"这种半截状态需要资金单、探针与人工裁决
// 才能收敛;D-16 它由事务边界直接排除掉 —— 但**只有真的把两次写放在同一个事务里**
// 才成立。这条用例就是那个事务边界的证据:把星屑余额顶到上界让 Credit 必然报
// ErrOverflow,然后断言佣金侧一个字节都没动。
//
// 换成"先扣佣金余额、再发星屑"的写法(或者把两步拆成两个事务),这条会立刻变红。
func TestAutoCredit_StardustFailureLeavesCommissionBalanceUntouched(t *testing.T) {
	env := newCreditEnv(t, nil)
	env.accrueAndSettle(t, 10_000_000)
	// 星屑余额顶到上界:再加任何正数都会 ErrOverflow。
	require.NoError(t, env.ext.Create(&stardust.Balance{
		UserId: creditInviter, Available: int64(common.MaxQuota),
		UpdatedAt: common.GetTimestamp(),
	}).Error)

	runCredit(context.Background())

	bal := balanceOf(t, env.ext, creditInviter)
	assert.EqualValues(t, 100, bal.Available, "星屑发不进去时佣金可用余额必须原样留着")
	assert.EqualValues(t, 0, bal.Credited)
	assertLedgerIdentity(t, bal)
	assert.Empty(t, env.credits(t, creditInviter), "事务回滚了就不该留下 credit 行")
	assert.Empty(t, env.sdLedger(t, creditInviter))
	assert.EqualValues(t, int64(common.MaxQuota), env.sdBalance(t, creditInviter).Available)
}

// TestAutoCredit_DrainsAcrossRoundsUntilBelowThreshold 钉住分批排空:
// 可用余额远大于单次上限时,一个周期内连续发多笔,直到剩余不足门槛。
func TestAutoCredit_DrainsAcrossRoundsUntilBelowThreshold(t *testing.T) {
	env := newCreditEnv(t, func(c *config.Config) {
		c.Commission.MaxPerOrderStardust = 30
		c.Commission.MinCreditStardust = 25
	})
	// 五天各 4,000,000 × 5% / 10,000 = 20 星屑(单日不触发 30 的计佣封顶),共 100。
	// 入账按 30 分批:30 + 30 + 30 之后剩 10 < 25,停在可用余额里。
	env.accrueDaysAndSettle(t, 5, 4_000_000)

	runCredit(context.Background())

	rows := env.credits(t, creditInviter)
	require.Len(t, rows, 3)
	for _, c := range rows {
		assert.EqualValues(t, 30, c.Amount)
	}
	bal := balanceOf(t, env.ext, creditInviter)
	assert.EqualValues(t, 10, bal.Available, "不足门槛的零头留在可用余额里等下一轮")
	assert.EqualValues(t, 90, bal.Credited)
	assertLedgerIdentity(t, bal)
	assert.EqualValues(t, 90, env.sdBalance(t, creditInviter).Available)
}
