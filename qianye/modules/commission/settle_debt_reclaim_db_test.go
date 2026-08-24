/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 负结转(欠账)必须能被**账上已有的**可用余额抵掉。
//
// # 缺陷长什么样
//
// 冲正金额大于当时的 available_quota 时,吃不下的那部分挂成负的
// unsettled_amount 并置 debt_blocked。此后:
//
//   - 第一路选人要求 `settled_amount <> gross_amount`,而 absorbAccruals 已经把
//     冲正行写成 settled==gross —— 他从第一路消失;
//   - 第二路选人要求 `unsettled_amount >= carryFloor(minSettle)`,负数不命中 ——
//     他从第二路也消失;
//   - 就算管理端手动对他调 settleOne,settleUser 的 carry-only 预筛写的是
//     `LessThan(1)`,而**所有负数都满足它**,直接早退。
//
// 于是 debt_blocked 永久钉住:提现建单、approve、mark-paid 三处闸门全线冻结,
// 用户提不出自己账上确实存在的钱,平台也永远收不回那笔应收。三条对账恒等式
// 在这个状态下**全部成立**,没有任何告警会响。而提现被拒时给出的文案还把
// "驳回这张单"列为补救 —— 驳回只是把 frozen 搬回 available,欠账一位不动。
//
// 下面两条各自独立钉死修复的一半,回滚任意一半都有一条会红。
func TestPendingInvitersSelectsDebtWithReclaimableBalance(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(0))

	// 欠 5000、账上还有 20000 可用(刚被驳回的提现退回来的那一笔)。
	seedDebtBalance(t, gdb, 9970003, "-5000", 20000, 0)
	// 对照一:同样欠钱,但账上一分可回收的都没有 —— 选它进来只会空转。
	seedDebtBalance(t, gdb, 9970004, "-5000", 0, 20000)
	// 对照二:余数是正的但不足 1 额度,原来的下界照旧生效。
	seedDebtBalance(t, gdb, 9970005, "0.4", 20000, 0)

	ids, err := pendingInviters(500)
	require.NoError(t, err)
	assert.Contains(t, ids, 9970003,
		"欠账 + 账上有可回收余额 = 这一轮就能把钱收回来，必须被调度选中")
	assert.NotContains(t, ids, 9970004,
		"账上没有可回收余额时选进来只是空转一次加锁事务")
	assert.NotContains(t, ids, 9970005,
		"正余数不足 1 额度的下界不能被这次改动顺手放开")
}

func TestSettleUserReclaimsDebtFromAvailableWithoutNewAccruals(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(0))
	useMoneyGlobals(t, 7, 500000)

	// 场景与实测复现逐字一致:结算出 available=25000 → 冻结 20000 提现 →
	// 冲正 10000 只吃到 5000 → unsettled=-5000 / debt_blocked=1 →
	// 管理员按提示驳回提现 → frozen 20000 回到 available。
	seedDebtBalance(t, gdb, 9970003, "-5000", 20000, 0)
	require.NoError(t, gdb.Model(&Balance{}).Where("user_id = ?", 9970003).
		Updates(map[string]any{
			"debt_blocked": true,
			// 原始形态:结算发出去 25000,冲正已经吃掉 5000,余额 20000。
			// 种子数据自己就必须满足 I2,否则下面那条断言是被夹具弄脏的。
			"total_earned_quota":   25000,
			"total_clawback_quota": 5000,
		}).Error)

	more, err := settleUser(9970003)
	require.NoError(t, err)
	assert.False(t, more)

	var after Balance
	require.NoError(t, gdb.Where("user_id = ?", 9970003).Take(&after).Error)
	assert.EqualValues(t, 15000, after.AvailableQuota,
		"账上那 20000 里必须被回收掉 5000")
	assert.Equal(t, "0", after.UnsettledAmount.String(), "欠账必须清零")
	assert.False(t, after.DebtBlocked, "欠账清掉之后提现闸门必须自己松开")

	// 恒等式:回收之后 available + frozen + withdrawn 仍等于 earned − clawback。
	assert.EqualValues(t,
		after.TotalEarnedQuota-after.TotalClawbackQuota,
		after.AvailableQuota+after.FrozenQuota+after.WithdrawnQuota,
		"I2 必须在回收之后仍然成立")

	// 反面:账上没钱可收时不能凭空把欠账抹掉,也不能把余额做成负数。
	seedDebtBalance(t, gdb, 9970004, "-5000", 0, 20000)
	_, err = settleUser(9970004)
	require.NoError(t, err)
	var broke Balance
	require.NoError(t, gdb.Where("user_id = ?", 9970004).Take(&broke).Error)
	assert.EqualValues(t, 0, broke.AvailableQuota)
	assert.Equal(t, "-5000", broke.UnsettledAmount.String(),
		"没有可回收余额时欠账必须原样留着，等未来的佣金抵扣")
}

// seedDebtBalance 造一行"欠账 + 指定可用/冻结余额"的佣金余额。
//
// 恒等式 I2(available + frozen + withdrawn == earned − clawback)必须在种子
// 数据上就成立,否则测试断言的那条恒等式是被夹具自己弄脏的。
func seedDebtBalance(t *testing.T, gdb *gorm.DB, userId int, unsettled string, available, frozen int64) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&Balance{
		UserId:           userId,
		UnsettledAmount:  decimal.RequireFromString(unsettled),
		AvailableQuota:   available,
		FrozenQuota:      frozen,
		TotalEarnedQuota: available + frozen,
		AvailableFiat:    decimal.Zero,
		LastSettledAt:    now - 3600,
		CreatedAt:        now - 7200,
		UpdatedAt:        now - 3600,
	}).Error)
}
