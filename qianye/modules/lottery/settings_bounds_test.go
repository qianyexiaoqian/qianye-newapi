package lottery

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settings_bounds_test.go —— 在线覆盖的上界必须与 YAML 同源,读侧和写侧都是。
//
// 写侧闸门(settingBounds)只管**今后**的写入。读侧若还写死一个旧的宽上界,
// 升级之前已经落库的越界覆盖会继续被读出来并生效 —— 敞口一点没关,而配置页会
// 同时显示 effective=500 与 bounds.max=20 却不报任何异常
// (overridesAllApplied 比的是 snapshot 与 overrides,两边都是 500)。
//
// max_active_activities 是全站累计净增发的唯一乘数:每一场活动各吃一个
// max_total_prize_quota,没有全站累计闸门。写死 1000 等于允许在线把敞口放大 50 倍。

// withLotteryConfig 装一份 YAML 基线,测完还原。
func withLotteryConfig(t *testing.T, lot config.Lottery) {
	t.Helper()
	previous := qyConfig.Swap(&config.Config{Enabled: true, Lottery: lot})
	t.Cleanup(func() { qyConfig.Store(previous) })
}

func TestMergeOverridesClampsMaxActiveActivitiesToTheYamlCeiling(t *testing.T) {
	const yamlCeiling = 20

	cases := []struct {
		name     string
		override string
		want     int
	}{
		{name: "低于上界:采纳", override: "5", want: 5},
		{name: "恰好等于上界:采纳", override: strconv.Itoa(yamlCeiling), want: yamlCeiling},
		{
			// 升级之前用旧上界(1000)写进库的那种值。丢弃并回落 YAML,
			// 而不是钳到 20 —— 钳取会让运营以为自己配的是 500、实际跑的是 20。
			name:     "超出 YAML 上界:丢弃并回落 YAML",
			override: "500", want: yamlCeiling,
		},
		{name: "旧上界本身:同样丢弃", override: "1000", want: yamlCeiling},
		{name: "下界之下:丢弃", override: "0", want: yamlCeiling},
		{name: "负数:丢弃", override: "-3", want: yamlCeiling},
		{name: "非数字:丢弃", override: "many", want: yamlCeiling},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withLotteryConfig(t, config.Lottery{
				MaxActiveActivities: yamlCeiling,
				MaxGuessFeeBps:      500,
				MaxTotalPrizeQuota:  50_000_000,
			})
			base := opSettings{MaxActiveActivities: yamlCeiling}

			got := mergeOverrides(base, map[string]string{
				keyMaxActiveActivities: tc.override,
			})

			assert.Equal(t, tc.want, got.MaxActiveActivities)
		})
	}
}

// 读侧与写侧必须给出同一个上界,否则配置页会显示一个自相矛盾的状态:
// 写不进去的值却正在生效。
func TestReadAndWriteSidesAgreeOnTheActiveActivityCeiling(t *testing.T) {
	const yamlCeiling = 7
	withLotteryConfig(t, config.Lottery{
		MaxActiveActivities: yamlCeiling,
		MaxGuessFeeBps:      500,
		MaxTotalPrizeQuota:  50_000_000,
	})

	bound, ok := settingBounds()[keyMaxActiveActivities]
	require.True(t, ok)
	assert.Equal(t, int64(yamlCeiling), bound.Hi, "写侧上界取自 YAML")

	base := opSettings{MaxActiveActivities: yamlCeiling}
	// 恰好等于写侧上界的值,读侧必须也接受它。
	accepted := mergeOverrides(base, map[string]string{
		keyMaxActiveActivities: strconv.FormatInt(bound.Hi, 10),
	})
	assert.Equal(t, yamlCeiling, accepted.MaxActiveActivities)

	// 比写侧上界大一的值,读侧必须也拒绝它。
	rejected := mergeOverrides(base, map[string]string{
		keyMaxActiveActivities: strconv.FormatInt(bound.Hi+1, 10),
	})
	assert.Equal(t, yamlCeiling, rejected.MaxActiveActivities,
		"写侧拒绝的值读侧却采纳了 —— 存量越界覆盖会一直生效")
}
