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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 拒绝理由必须说的是**真的原因**,而且刻度不能串。
//
// 这两条不是措辞洁癖:errBadRequest 的整句话会原样进 qy_audit_logs.reason
// 并且原样回给直接调 API 的脚本/集成方。一句"奖品数量必须大于 0"回给填了
// 60000 的人,事后复盘也分不出"填了 0"与"填了 60000",而这两件事的处置
// 完全不同。

func TestPrizeCountRejectionNamesTheRealBound(t *testing.T) {
	cfg, act := prizeEnv()
	noCeiling := opSettings{MaxTotalPrizeQuota: 0}

	_, _, err := buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 1000, Count: 0},
	}, cfg, noCeiling, act)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "奖品数量必须大于 0")

	_, _, err = buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 1000, Count: cfg.MaxTotalEntriesHard + 1},
	}, cfg, noCeiling, act)
	require.Error(t, err, "越过硬顶必须被拒")
	assert.Contains(t, err.Error(), "50000",
		"上界那一支必须把真正的上限写出来，否则拿着「必须大于 0」往上调只会再吃一个 400")
	assert.NotContains(t, err.Error(), "必须大于 0",
		"填了 60000 却被告知「必须大于 0」——这句话在字面上就是假的")
}

// 同一句话里不许出现两种刻度。
//
// 左边是钱(运营在界面上填的是 $),右边 entriesCap 是 rules.max_total_entries,
// 单位是**张票**。给它缀一个"额度"会让人照着往错的方向调参;而 quotaText 的
// 返回值本身已经以" 额度"结尾,格式串里再写一次就输出"额度 ＄0.000010 额度"。
func TestProbBudgetRejectionKeepsMoneyAndTicketsApart(t *testing.T) {
	cfg, _ := prizeEnv()
	prob := &Activity{DrawMode: DrawModeProb, Algo: AlgoV2, MaxTotalEntries: 10}

	_, _, err := buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 5, Count: 1, WinPpm: 1000},
	}, cfg, opSettings{MaxTotalPrizeQuota: 0}, prob)
	require.Error(t, err)
	msg := err.Error()

	assert.Contains(t, msg, quotaText(5), "单份必须按站内余额刻度写")
	assert.Contains(t, msg, "全场参与上限 10 张票",
		"全场参与上限的单位是张票，不是额度")
	assert.NotContains(t, msg, "上限 10 额度")
	// 全句里"额度"只该出现两次:quotaText 自带的那一次后缀,以及句尾
	// "摊薄到 0 额度"。改动前是四次 —— 格式串自己写了一次"额度 %s"
	// (于是输出「额度 ＄0.000010 额度」),票数后面又缀了一次。
	assert.Equal(t, 2, strings.Count(msg, "额度"), "实际文案：%s", msg)
	assert.NotContains(t, msg, "额度 ＄",
		"quotaText 的返回值已经以“额度”结尾，格式串里不能再写一次")
}

// 双色球那一条同规则的报错必须与概率制逐字同口径 ——
// 两处分叉的表现是同一条规则在两种玩法上说两句不一样的话。
func TestBallBudgetRejectionMatchesProbWording(t *testing.T) {
	series := &Series{RedPool: 33, RedPick: 6, BluePool: 16, BluePick: 1}
	err := checkBallTierInput(prizeInput{
		Tier: 1, Name: "一等奖", AmountQuota: 5, Count: 1, RedMatch: 6, BlueMatch: 1,
	}, series, 10)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, quotaText(5))
	assert.Contains(t, msg, "全场参与上限 10 张票")
	assert.Contains(t, msg, "数量 1 × 单份")
}
