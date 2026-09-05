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

	"github.com/QuantumNous/new-api/qianye/modules/stardust"

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
	noCeiling := opSettings{MaxTotalPrizeStardust: 0}

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
// 左边是星屑(运营在界面上填的是整数),右边 entriesCap 是 rules.max_total_entries,
// 单位是**张票**。给票数缀一个单位名会让人照着往错的方向调参;而 stardustText 的
// 返回值本身已经带着单位名,格式串里再写一次就输出"星屑 5 星屑"。
func TestProbBudgetRejectionKeepsMoneyAndTicketsApart(t *testing.T) {
	cfg, _ := prizeEnv()
	prob := &Activity{DrawMode: DrawModeProb, Algo: AlgoV2, MaxTotalEntries: 10}

	_, _, err := buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 5, Count: 1, WinPpm: 1000},
	}, cfg, opSettings{MaxTotalPrizeStardust: 0}, prob)
	require.Error(t, err)
	msg := err.Error()

	unit := stardust.UnitName()
	assert.Contains(t, msg, stardustText(5), "单份必须按星屑整数 + 单位名写")
	assert.Contains(t, msg, "全场参与上限 10 张票",
		"全场参与上限的单位是张票,不是星屑")
	assert.NotContains(t, msg, "上限 10 "+unit)
	// 全句里单位名只该出现两次:单份下限与当前单份各带一次。票数后面不许缀,
	// 格式串里也不许自己再写一次。
	assert.Equal(t, 2, strings.Count(msg, unit), "实际文案:%s", msg)
	assert.NotContains(t, msg, "额度", "星屑不是额度,拒绝文案里不该混进额度的刻度")
	assert.NotContains(t, msg, "＄", "星屑没有美元刻度")
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
	assert.Contains(t, msg, stardustText(5))
	assert.Contains(t, msg, "全场参与上限 10 张票")
	assert.Contains(t, msg, "数量 1 × 单份")
}
