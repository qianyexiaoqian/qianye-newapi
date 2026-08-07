package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildGroup2Model2ChannelsSurvivesAbilityGap 钉死一个会杀掉整个进程的缺陷。
//
// 场景来自两个真实窗口:FixAbility 的全表 TRUNCATE 期间、以及 UpdateChannel
// 先提交 channels.group 新值再重建 abilities 的那一小段。两者都会短暂出现
// 「某个 enabled 渠道的分组在 abilities 里没有任何行」。
//
// 修复前:外层 map 的键只从 abilities 建,内层 map 为 nil,
// 写入即 `assignment to entry in nil map` panic;而这段代码跑在
// `go model.SyncChannelCache(...)` 这个没有 recover 的后台 goroutine 里,
// panic = 进程死亡。改错(把补建那三行删掉)这条测试立刻红。
func TestBuildGroup2Model2ChannelsSurvivesAbilityGap(t *testing.T) {
	channels := []*Channel{
		{Id: 1, Group: "已登记分组", Models: "gpt-4o", Status: common.ChannelStatusEnabled},
		// abilities 里完全没有这个分组 —— 这就是 TRUNCATE / 改名窗口里的状态。
		{Id: 2, Group: "abilities里还没有的分组", Models: "gpt-4o,claude-3", Status: common.ChannelStatusEnabled},
	}
	abilities := []*Ability{{Group: "已登记分组", Model: "gpt-4o", ChannelId: 1, Enabled: true}}
	id2channel := map[int]*Channel{1: channels[0], 2: channels[1]}

	var index map[string]map[string][]int
	require.NotPanics(t, func() {
		index = buildGroup2Model2Channels(channels, abilities, id2channel)
	}, "abilities 缺行时不得 panic —— 它跑在无 recover 的后台 goroutine 上,panic 即进程死亡")

	assert.Equal(t, []int{2}, index["abilities里还没有的分组"]["gpt-4o"],
		"补建外层键之后该分组必须仍然可路由,否则修复就退化成一次静默 503")
	assert.Equal(t, []int{2}, index["abilities里还没有的分组"]["claude-3"])
	assert.Equal(t, []int{1}, index["已登记分组"]["gpt-4o"])
}

// TestBuildGroup2Model2ChannelsKeepsAbilityOnlyGroupsAndSkipsDisabled 锁住另外两条
// 与修复无关、但很容易在重构中被顺手改掉的既有行为:
//
//   - abilities 里有、channels 里没有 enabled 渠道的分组,外层键仍然存在
//     (值为空 map)。GetRandomSatisfiedChannel 靠 `index[group][model]` 取空切片
//     返回「无可用渠道」,而不是把这个分组当成不存在。
//   - disabled 渠道一行都不进索引。
func TestBuildGroup2Model2ChannelsKeepsAbilityOnlyGroupsAndSkipsDisabled(t *testing.T) {
	channels := []*Channel{
		{Id: 3, Group: "停用池", Models: "gpt-4o", Status: common.ChannelStatusManuallyDisabled},
	}
	abilities := []*Ability{{Group: "只在abilities里", Model: "gpt-4o", ChannelId: 9, Enabled: true}}

	index := buildGroup2Model2Channels(channels, abilities, map[int]*Channel{3: channels[0]})

	inner, ok := index["只在abilities里"]
	require.True(t, ok, "abilities 里的分组键必须保留")
	assert.Empty(t, inner)
	assert.Empty(t, index["停用池"], "disabled 渠道不得进入路由索引")
}
