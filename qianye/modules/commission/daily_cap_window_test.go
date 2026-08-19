package commission

// daily_cap_window_test.go —— 日封顶的窗口不得随 day_offset_minutes 平移。
//
// 被守的缺陷:「今日已发」曾经是 `SUM(granted_quota) WHERE created_at >=
// dayStart(now)`,而 dayStart 的日界完全由 commission.day_offset_minutes 决定。
// 那个值不进幂等键、不进结算行、不参与任何身份 —— 改一次配置重启(或灰度期间
// 两个节点取值不同),窗口起点就整体平移,已发的结算行整批掉出窗口,SUM 读到 0,
// 当天的日封顶原地满血复活。日封顶是单个推广人「一天最多拿多少」的唯一总量闸门。
//
// 两条用例分别打两个层次:纯函数层(resolveCapWindow 的三条分支)与
// 端到端层(真事务、真结算、真回读余额行)。端到端那条必须在,因为缺陷不在
// 算术里 —— computeSettlement 一直是对的,错的是喂给它的那个 dailyRemain。

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useDayOffset 把日界偏移换成 minutes 并让配置快照生效。
func useDayOffset(t *testing.T, minutes int) {
	t.Helper()
	cfg := commissionConfig(1)
	cfg.Commission.DayOffsetMinutes = minutes
	useConfig(t, cfg)
}

// offsetMovingBoundaryInto 找一个合法的 day_offset_minutes,让 dayStart(now)
// 落进 (after, now] 这段区间 —— 也就是把日界挪到"那笔已发结算之后"。
//
// 暴力扫合法区间(validate.go: -720..840)并调**生产函数** dayStart 判定,
// 不在测试里另写一份日界算法:另写一份就等于让这条用例守着测试自己的实现。
func offsetMovingBoundaryInto(t *testing.T, now, after int64) int {
	t.Helper()
	prev := qyConfig.Load()
	defer qyConfig.Store(prev)
	cfg := commissionConfig(1)
	for off := -720; off <= 840; off++ {
		cfg.Commission.DayOffsetMinutes = off
		qyConfig.Store(cfg)
		if b := dayStart(now); b > after && b <= now {
			return off
		}
	}
	t.Fatalf("找不到能把日界挪进 (%d, %d] 的合法偏移", after, now)
	return 0
}

func TestResolveCapWindow(t *testing.T) {
	gdb := newTestDB(t)
	useDayOffset(t, 0)
	now := common.GetTimestamp()
	today := dayStart(now)

	t.Run("存量行按结算行补出今日已发", func(t *testing.T) {
		// daily_cap_window_start = 0 是本列上线前的余额行。不补的话,
		// 升级当天每个人的封顶都白白多出一份。
		require.NoError(t, gdb.Create(&Settlement{
			SettleNo: "CS-CAPW-1", UserId: 8801, GrantedQuota: 700,
			CreatedAt: today + 60,
		}).Error)
		bal := &Balance{UserId: 8801}
		w, err := resolveCapWindow(gdb, bal, 1000, now)
		require.NoError(t, err)
		assert.Equal(t, today, w.Start)
		assert.EqualValues(t, 700, w.Granted)
		assert.EqualValues(t, 300, w.remaining(1000))
	})

	t.Run("窗口内沿用余额行上记着的已发", func(t *testing.T) {
		bal := &Balance{UserId: 8802, DailyCapWindowStart: today, DailyCapGranted: 1000}
		w, err := resolveCapWindow(gdb, bal, 1000, now)
		require.NoError(t, err)
		assert.Equal(t, today, w.Start)
		assert.EqualValues(t, 0, w.remaining(1000))
	})

	t.Run("日界被挪到已发那笔之后也不许开新窗口", func(t *testing.T) {
		// 这就是缺陷本体:旧口径此刻会 SUM 到 0,封顶原地满血复活。
		off := offsetMovingBoundaryInto(t, now, now-7200)
		useDayOffset(t, off)
		require.Greater(t, dayStart(now), now-7200, "前提:日界确实被挪到了两小时内")

		bal := &Balance{UserId: 8803, DailyCapWindowStart: now - 7200, DailyCapGranted: 1000}
		w, err := resolveCapWindow(gdb, bal, 1000, now)
		require.NoError(t, err)
		assert.EqualValues(t, now-7200, w.Start, "窗口起点必须原样保留")
		assert.EqualValues(t, 0, w.remaining(1000), "同一天里第二份封顶不许出现")
	})

	t.Run("满 24 小时才开新窗口", func(t *testing.T) {
		useDayOffset(t, 0)
		bal := &Balance{UserId: 8804,
			DailyCapWindowStart: now - secondsPerDay, DailyCapGranted: 1000}
		w, err := resolveCapWindow(gdb, bal, 1000, now)
		require.NoError(t, err)
		assert.Equal(t, today, w.Start)
		assert.EqualValues(t, 1000, w.remaining(1000))
	})

	t.Run("不设封顶时返回 -1 且不查结算行", func(t *testing.T) {
		bal := &Balance{UserId: 8805}
		w, err := resolveCapWindow(gdb, bal, 0, now)
		require.NoError(t, err)
		assert.EqualValues(t, -1, w.remaining(0))
	})
}

// TestDailyCapSurvivesADayBoundaryShift 是端到端那条:真结算两次,中间把
// day_offset_minutes 改掉,断言第二次一分钱都发不出来。
//
// 独立算出的期望:封顶 1000,名下共 4000 已成熟佣金。第一轮发 1000、余数 0;
// 把日界挪到第一张结算单之后再跑,旧口径会再发 1000(实测过的那个形状),
// 新口径必须仍然是 1000,剩下 3000 留在 unsettled 里等明天。
func TestDailyCapSurvivesADayBoundaryShift(t *testing.T) {
	gdb := newTestDB(t)
	useMainDB(t, &model.User{})
	useDayOffset(t, 0)
	useMoneyGlobals(t, 7.3, 500000)
	setSettingOverride(t, gdb, keyDailyCapQuota, "1000")
	require.EqualValues(t, 1000, effective().DailyCapQuota, "前提:日封顶已生效")

	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = 8810
		a.GrossAmount = decimal.NewFromInt(4000)
	})
	settleUserOnce(t, 8810)

	rows := settlementsOf(t, gdb, 8810)
	require.Len(t, rows, 1)
	require.EqualValues(t, 1000, rows[0].GrantedQuota)
	bal := balanceOf(t, gdb, 8810)
	require.NotNil(t, bal)
	require.EqualValues(t, 1000, bal.AvailableQuota)
	require.EqualValues(t, 1000, bal.DailyCapGranted, "窗口状态必须落在余额行上")
	require.NotZero(t, bal.DailyCapWindowStart)

	// 把那张结算单的时间挪到两小时前,再把日界挪进这两小时里 ——
	// 这正是"改一次配置重启"在同一自然日里造成的形状。
	now := common.GetTimestamp()
	require.NoError(t, gdb.Model(&Settlement{}).Where("id = ?", rows[0].Id).
		UpdateColumn("created_at", now-7200).Error)
	require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", 8810).
		UpdateColumn("daily_cap_window_start", now-7200).Error)
	off := offsetMovingBoundaryInto(t, now, now-7200)
	useDayOffset(t, off)
	setSettingOverride(t, gdb, keyDailyCapQuota, "1000")
	require.EqualValues(t, 1000, effective().DailyCapQuota)
	require.Greater(t, dayStart(now), now-7200,
		"前提:新日界确实落在那张结算单之后(旧口径此刻会 SUM 到 0)")

	settleUserOnce(t, 8810)

	rows = settlementsOf(t, gdb, 8810)
	assert.Len(t, rows, 1, "同一天、同一个人,第二份封顶不许被发出来")
	bal = balanceOf(t, gdb, 8810)
	require.NotNil(t, bal)
	assert.EqualValues(t, 1000, bal.AvailableQuota, "可提现余额一分都不该再涨")
	assert.Equal(t, "3000", bal.UnsettledAmount.String(), "被削掉的钱仍在余数里")
}

// TestDailyCapReopensAfterAFullDay 是反向守卫:窗口该开的时候必须开,
// 否则这条修复就变成"把佣金永远卡住"。
func TestDailyCapReopensAfterAFullDay(t *testing.T) {
	gdb := newTestDB(t)
	useMainDB(t, &model.User{})
	useDayOffset(t, 0)
	useMoneyGlobals(t, 7.3, 500000)
	setSettingOverride(t, gdb, keyDailyCapQuota, "1000")

	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = 8820
		a.GrossAmount = decimal.NewFromInt(4000)
	})
	settleUserOnce(t, 8820)
	require.EqualValues(t, 1000, balanceOf(t, gdb, 8820).AvailableQuota)

	// 把窗口起点挪回昨天:等价于"过了一整天"。
	now := common.GetTimestamp()
	require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", 8820).
		UpdateColumn("daily_cap_window_start", now-secondsPerDay-60).Error)

	settleUserOnce(t, 8820)
	bal := balanceOf(t, gdb, 8820)
	require.NotNil(t, bal)
	assert.EqualValues(t, 2000, bal.AvailableQuota, "新的一天必须重新给一份封顶")
	assert.EqualValues(t, 1000, bal.DailyCapGranted, "新窗口里只发了这一份")
}

// TestDailyCapGrantedIgnoresClawback 钉住"回收不退还当天的封顶额度"。
// 退还的话,一次冲正就能换回一份新的发放余量。
func TestDailyCapGrantedIgnoresClawback(t *testing.T) {
	gdb := newTestDB(t)
	useMainDB(t, &model.User{})
	useDayOffset(t, 0)
	useMoneyGlobals(t, 7.3, 500000)
	setSettingOverride(t, gdb, keyDailyCapQuota, "1000")

	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = 8830
		a.GrossAmount = decimal.NewFromInt(1000)
	})
	settleUserOnce(t, 8830)
	require.EqualValues(t, 1000, balanceOf(t, gdb, 8830).DailyCapGranted)

	// 一条负额计佣行(冲正)。
	seedAccrual(t, gdb, 2, func(a *Accrual) {
		a.InviterId = 8830
		a.GrossAmount = decimal.NewFromInt(-400)
	})
	settleUserOnce(t, 8830)

	bal := balanceOf(t, gdb, 8830)
	require.NotNil(t, bal)
	assert.EqualValues(t, 600, bal.AvailableQuota)
	assert.EqualValues(t, 1000, bal.DailyCapGranted,
		"回收不该把当天的封顶额度退回来")
}
