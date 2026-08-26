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
package lottery

import (
	"math"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// advice_test.go —— 「别再让人猜,给推荐值」这件事的可执行判据。
//
// 项目方原话:「创建活动,你不告诉我要怎么设置推荐值?一堆这种『固定奖级的预算
// (额度 × 份数)必须不小于全场参与上限』很烦啊」
//
// 推荐值只有满足一条才有意义:**它自己必须能过后端那道校验**。一个"界面说
// 可以、提交被拒"的推荐值比不给推荐值更糟 —— 前者会让人以为界面坏了。
// 所以下面每一条都是"把 tierBudgetFloor / tierCountFloor 算出来的数喂回真正的
// 校验函数",而不是断言那个函数返回了某个常量。

// 推荐值必须恰好落在校验的边界上:它自己通过,少一个单位就被拒。
//
// 只断言"推荐值能过"是不够的 —— 返回 math.MaxInt64 也能过,而那个数一秒钟
// 就把活动配成天文数字。边界的另一侧同时断言,推荐值才是**最小可行值**。
func TestTierAmountFloorIsExactlyTheSmallestValueTheValidatorAccepts(t *testing.T) {
	cases := []struct {
		name       string
		entriesCap int
		count      int
		want       int64
	}{
		{name: "整除", entriesCap: 100, count: 10, want: 10},
		{name: "除不尽必须向上取整", entriesCap: 101, count: 10, want: 11},
		{name: "只发一份", entriesCap: 50000, count: 1, want: 50000},
		{name: "份数比票数还多", entriesCap: 10, count: 100, want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			floor := tierBudgetFloor(tc.count, tc.entriesCap)
			require.EqualValues(t, tc.want, floor)

			series := &Series{RedPool: 33, RedPick: 6, BluePool: 16, BluePick: 1}
			tier := prizeInput{
				Tier: 1, Name: "一等奖", Count: tc.count,
				RedMatch: 6, BlueMatch: 1, AmountQuota: floor,
			}
			assert.NoError(t, checkBallTierInput(tier, series, tc.entriesCap),
				"推荐值自己过不了校验 —— 界面填好了、提交被拒,那比不给推荐值更糟")

			prob := prizeInput{Tier: 1, Count: tc.count, AmountQuota: floor, WinPpm: 1000}
			_, err := normalizeWinPpm(DrawModeProb, prob, PrizeTypeQuota, tc.entriesCap)
			assert.NoError(t, err, "概率制与双色球必须认同一个推荐值")

			if floor <= 1 {
				// 再低一档就是 0,而 0 会先撞上"额度必须大于 0"那条,
				// 测的就不是这条不等式了。
				return
			}
			tier.AmountQuota = floor - 1
			assert.Error(t, checkBallTierInput(tier, series, tc.entriesCap),
				"推荐值不是最小可行值:比它小一个单位居然也过了")
		})
	}
}

// 同一条不等式的另一个解:单份已经定死时,份数至少要几份。
//
// 界面上「按参与上限自动填」有两颗按钮(填单份 / 填份数),两颗都必须落在
// 合法侧,否则运营点了第二颗照样吃 400。
func TestTierCountFloorIsExactlyTheSmallestCountTheValidatorAccepts(t *testing.T) {
	cases := []struct {
		name       string
		entriesCap int
		amount     int64
		want       int64
	}{
		{name: "整除", entriesCap: 100, amount: 10, want: 10},
		{name: "除不尽必须向上取整", entriesCap: 100, amount: 30, want: 4},
		{name: "单份已经比全场还大", entriesCap: 100, amount: 500_000, want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			floor := tierCountFloor(tc.amount, tc.entriesCap)
			require.EqualValues(t, tc.want, floor)

			series := &Series{RedPool: 33, RedPick: 6, BluePool: 16, BluePick: 1}
			tier := prizeInput{
				Tier: 1, Name: "一等奖", Count: int(floor),
				RedMatch: 6, BlueMatch: 1, AmountQuota: tc.amount,
			}
			assert.NoError(t, checkBallTierInput(tier, series, tc.entriesCap))

			if floor <= 1 {
				return
			}
			tier.Count = int(floor) - 1
			assert.Error(t, checkBallTierInput(tier, series, tc.entriesCap),
				"推荐份数不是最小可行值")
		})
	}
}

// 零值不能算出一个"看起来像答案"的数。
//
// count=0 时 ceil(cap/0) 没有意义;界面上运营正好清空那一格的瞬间就是这个状态。
// 返回 0 让上层能判"此刻算不出推荐值",而不是把一个 NaN / 除零崩溃摆到界面上。
func TestBudgetFloorsReturnZeroWhenTheOtherFieldIsNotFilledYet(t *testing.T) {
	assert.Zero(t, tierBudgetFloor(0, 100))
	assert.Zero(t, tierBudgetFloor(10, 0))
	assert.Zero(t, tierBudgetFloor(-1, 100))
	assert.Zero(t, tierCountFloor(0, 100))
	assert.Zero(t, tierCountFloor(10, 0))
	assert.Zero(t, tierCountFloor(-1, 100))
}

// 界面上那个"系统上界"到底是什么:**全站额度换算的整数上界**,不是运营策略。
//
// 项目方原话先是「发行上限不得超过系统上限 ＄4294.967294 额度 …… 这是什么问题?」,
// 后来是「不要几千 USD 太少了 …… 余额都能设定几个亿了」。两句问的是同一件事,
// 而当时那个数的**理由**(额度列是 int32)经不起查:三个方言上那些列都是 64 位
// (model.TestQuotaColumnsAre64BitOnEveryDialect)。上界已按真实约束重定为 2^43。
//
// 这条测试盯的是"抽奖这一侧读的就是那个上界本身",而不是任何一个抄下来的数:
// 上界本身的推导由 common 与 qianye/config 的测试各自守着。
func TestSystemQuotaCeilingTracksTheConversionBound(t *testing.T) {
	assert.Greater(t, common.MaxQuota, math.MaxInt32,
		"上界必须已经抬过 —— 项目方点名的正是它太小")
	// 界面上那句"填不了"必须念出**当前**上界的刻度。抄一份常量的下场,是后端
	// 抬高之后界面还在一个早就合法的数字上标红,而运营找不到任何配置能放开它。
	assert.Contains(t, quotaColumnCeilingText("发行上限"), quotaText(int64(common.MaxQuota)))
	assert.NotContains(t, quotaColumnCeilingText("发行上限"), "4294.967294",
		"旧的 int32 刻度不得再出现在任何一句面向运营的文案里")
}

// 系统上界与策略上限必须在文案上分得开。
//
// 混在一起的表现是运营拿着一句"不得超过系统上限"跑去配置页找一个根本不存在的
// 开关 —— 系统上界改任何配置都放不开,策略上限改一个数就放开了。
//
// 同时钉住**理由必须经得起查**:上一版把它说成"数据库额度列的物理宽度(int32)",
// 而 users.quota 在 MySQL / PostgreSQL 上落地成 bigint、SQLite 的 INTEGER 也是
// 8 字节 —— 运营一去查表就会发现每一列都是 64 位的,然后连带不再相信整条解释。
func TestSystemCeilingAndSiteCeilingDoNotReadTheSame(t *testing.T) {
	physical := quotaColumnCeilingText("发行上限")
	policy := prizeCapExceeded(2_000_000, 1_000_000).Error()

	assert.Contains(t, physical, quotaText(int64(common.MaxQuota)),
		"系统上界必须把那个数写出来,否则运营还是不知道能填多少")
	assert.Contains(t, physical, "common.MaxQuota")
	assert.Contains(t, physical, "改任何配置都放不开它")
	assert.NotContains(t, physical, "物理宽度",
		"额度列在 MySQL/PostgreSQL/SQLite 上都不是 32 位,这个理由经不起查")
	assert.NotContains(t, physical, "数据库",
		"同上:别再把这个上界的理由推给任何一张表或任何一列")
	assert.NotContains(t, physical, "lottery.max_total_prize_quota",
		"系统上界不该指向任何配置项 —— 那会把人送去改一个改不动的东西")

	assert.Contains(t, policy, "本站设置的单场上限")
	assert.Contains(t, policy, "lottery.max_total_prize_quota",
		"策略上限必须点名是哪一项配置,否则运营找不到该去哪里改")
	assert.NotContains(t, policy, "common.MaxQuota")
}

// 报错的第一句必须是「填多少」,而不是「为什么错」。
//
// 项目方点名烦的正是"整句都在解释后果"。判据落在句子的**前半段**上:
// 推荐值出现在"判据是"三个字之前。
func TestBudgetShortMessageLeadsWithTheValueToFill(t *testing.T) {
	msg := tierBudgetShort(3, 4, 5, 100).Error()

	head, tail, found := strings.Cut(msg, "判据是")
	require.True(t, found, "报错里必须仍然解释判据,实际:%s", msg)

	// ceil(100/4) = 25、ceil(100/5) = 20:两个解都要给,运营改哪一格都行。
	assert.Contains(t, head, quotaText(25), "单份下限没出现在句子前半段")
	assert.Contains(t, head, "20 份以上", "份数下限没出现在句子前半段")
	assert.Contains(t, head, "奖级 3", "没说是哪一档")
	assert.NotContains(t, head, "摊薄",
		"后果被写进了第一句 —— 顺序反了,先说填多少")
	assert.Contains(t, tail, "摊薄", "后果整段丢了:只说填多少而不说为什么同样不行")
	assert.Contains(t, tail, "全场参与上限 100 张票")
}

// 两种玩法**逐字**同一句话。
//
// 这条守的是"共用一个构造器"这件事本身:哪天有人在其中一支就地改一句措辞,
// 另一支不会跟着变,而运营会以为撞上了两条不同的规则。
func TestBallAndProbGiveByteIdenticalBudgetAdvice(t *testing.T) {
	const entriesCap = 100
	tier := prizeInput{
		Tier: 2, Name: "二等奖", Count: 3, AmountQuota: 7,
		RedMatch: 6, BlueMatch: 1, WinPpm: 1000,
	}

	ballErr := checkBallTierInput(tier, &Series{RedPool: 33, RedPick: 6, BluePool: 16, BluePick: 1}, entriesCap)
	require.Error(t, ballErr)
	_, probErr := normalizeWinPpm(DrawModeProb, tier, PrizeTypeQuota, entriesCap)
	require.Error(t, probErr)

	assert.Equal(t, ballErr.Error(), probErr.Error())
}
