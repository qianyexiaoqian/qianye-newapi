package common

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoteGroupRatioFallbackIsRecomputedEveryRound 锁住「标记按轮重算,不是粘住的」。
//
// ── 它保护的是一条会让管理员退错钱的路径 ──
//
// HandleGroupRatio 在 auto / 跨分组重试的**每一轮**都会被重跑(controller/relay.go
// 的重试循环里每轮一次 getChannel → 一次 HandleGroupRatio),而 relayInfo 是循环外
// 创建的同一个对象。此前非静默兜底时直接 return、从不清空,于是:
//
//	第 1 轮 auto 落到「已删掉倍率的池」→ fail-open 1.0 → 挂上标记 → 渠道 5xx 重试
//	第 2 轮 auto 换到「低价池」(0.125)→ 正确计费并成功 → 旧标记原封不动带进消费日志
//
// 管理员按这张标记补差,会给一笔本来就收对了的请求退钱,而且退的是 1.0 与 0.125
// 的差价;标记里的 model_group 还指向一个这笔请求根本没用上的分组。
func TestNoteGroupRatioFallbackIsRecomputedEveryRound(t *testing.T) {
	silent := ratio_setting.GroupRatioResolution{
		Ratio: 1, Source: ratio_setting.GroupRatioSourceInherit, Base: 1, BaseMissing: true,
	}
	require.True(t, silent.SilentFallback())

	priced := ratio_setting.GroupRatioResolution{
		Ratio: 0.125, Source: ratio_setting.GroupRatioSourceInherit, Base: 0.125,
	}
	require.False(t, priced.SilentFallback())

	// 命中交叉格但兜底缺失:金额是运营配出来的,不是资损,同样不得留下标记。
	overridden := ratio_setting.GroupRatioResolution{
		Ratio: 0.5, Source: ratio_setting.GroupRatioSourceOverride, Base: 1, BaseMissing: true,
	}
	require.False(t, overridden.SilentFallback())

	info := &RelayInfo{UserGroup: "default", UsingGroup: "已删掉倍率的池"}

	info.NoteGroupRatioFallback(silent)
	require.NotNil(t, info.GroupRatioFallback, "静默 fail-open 必须留下标记")
	assert.Equal(t, "已删掉倍率的池", info.GroupRatioFallback.ModelGroup)

	// 第 2 轮换到一个配了倍率的分组:标记必须被清掉。
	info.UsingGroup = "低价池"
	info.NoteGroupRatioFallback(priced)
	assert.Nil(t, info.GroupRatioFallback,
		"成功那一轮必须清掉失败那一轮留下的标记,否则消费日志里挂着一个陈旧的补差凭据")

	// 再来一轮 override:同样不留标记。
	info.NoteGroupRatioFallback(silent)
	require.NotNil(t, info.GroupRatioFallback)
	info.NoteGroupRatioFallback(overridden)
	assert.Nil(t, info.GroupRatioFallback,
		"命中交叉格时金额是运营配出来的,不是资损,标记必须被清掉")
}
