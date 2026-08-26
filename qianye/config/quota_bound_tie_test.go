package config

import (
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuantumNous/new-api/common"
)

// quota_bound_tie_test.go —— 把 common.MaxQuota 与 lottery.max_total_entries_hard
// 钉在一起。
//
// 两个常量单看都像是各自领域的运维口味,合起来才是一条算术不变量:
// 抽奖预估支出里的 `hit * p.AmountQuota`(qianye/modules/lottery/api_admin.go)
// 是全站唯一一处"额度 × 一个可配置整数"却没有就地溢出检查的乘法 ——
// hit ≤ 全场参与上限 ≤ max_total_entries_hard,AmountQuota ≤ common.MaxQuota。
// 任何一边单独抬高,那条乘法都能在 int64 上绕回负数,而负的预估支出会让
// 二次确认(requireNetIssueConfirm)与大额告警一起静默通过。
//
// common/quota_math.go 里有编译期断言盯着 MaxQuota 那一侧;这里盯的是
// 配置这一侧真的把闸门装上了,以及两个常量确实是同一个数。

// defaultLotteryConfig 取一份**补过默认值**的抽奖配置作为基线。
//
// 刻意不复用 lotteryBaseline():那是一份手写的字段清单,新增一个必填项就要
// 同步一次,而本文件要证的与那些字段无关。补默认值得到的基线按定义必须合法,
// 新增必填项时它自己会跟上。
func defaultLotteryConfig(t *testing.T) Lottery {
	t.Helper()
	var c Config
	// markNumbersUnset 是 Load 在 YAML 解析前做的那一步:没有它,零值会被当成
	// "显式写了 0",applyDefaults 一项都不会补(见 explicit_zero_test.go)。
	markNumbersUnset(reflect.ValueOf(&c).Elem())
	applyDefaults(&c)
	clearUnsetNumbers(reflect.ValueOf(&c).Elem())
	c.Lottery.Enabled = true
	require.NoError(t, validateLottery(&c.Lottery),
		"补过默认值的抽奖配置必须合法,否则下面每一条断言测的都是别的东西")
	return c.Lottery
}

// TestLotteryEntriesHardCapMatchesQuotaBound 证明两个常量之间的算术关系成立。
func TestLotteryEntriesHardCapMatchesQuotaBound(t *testing.T) {
	// 乘积必须留出至少一倍余量:压在 MaxInt64 上等于没有余量,
	// 中间量再加一次(哪怕加 1)就溢出。
	product := int64(common.MaxQuota) * int64(MaxLotteryEntriesHard)
	assert.Positive(t, product, "额度上界 × 名单上界必须仍是正数,负数即已回绕")
	assert.LessOrEqual(t, product, int64(1)<<62,
		"额度上界 × 名单上界必须落在 int64 容量的一半以内;当前 %d × %d = %d",
		common.MaxQuota, MaxLotteryEntriesHard, product)

	// 默认值必须落在硬上界之内,否则一份不写这一项的配置启动不了。
	assert.LessOrEqual(t, defaultLotteryConfig(t).MaxTotalEntriesHard, MaxLotteryEntriesHard,
		"默认名单上界不得超过硬上界,否则默认配置自己就启动不了")
}

// TestValidateLotteryRejectsEntriesHardCapAboveTheBound 证明校验器真的装上了。
//
// 上界这一格必须放行、上界 +1 必须拒 —— 写成 `>=` 会让一个合法的极值配置
// 被拒,而漏掉整条判断则等于两个常量之间那条不变量根本没人执行。
func TestValidateLotteryRejectsEntriesHardCapAboveTheBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries int
		wantErr bool
	}{
		{name: "默认值", entries: defaultLotteryConfig(t).MaxTotalEntriesHard, wantErr: false},
		{name: "硬上界本身", entries: MaxLotteryEntriesHard, wantErr: false},
		{name: "硬上界 +1", entries: MaxLotteryEntriesHard + 1, wantErr: true},
		{name: "远超硬上界", entries: MaxLotteryEntriesHard * 4096, wantErr: true},
		{name: "0 仍然是配置错误", entries: 0, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lt := defaultLotteryConfig(t)
			lt.MaxTotalEntriesHard = tc.entries
			err := validateLottery(&lt)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "max_total_entries_hard")
		})
	}
}

// TestCheckQuotaCapTracksMaxQuota 守 checkQuotaCap 的边界:它是全部额度类配置项
// 共用的那道闸,判据必须是 common.MaxQuota 本身而不是一个抄下来的常量。
func TestCheckQuotaCapTracksMaxQuota(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   int64
		wantErr bool
	}{
		{name: "0 表示不限制", value: 0, wantErr: false},
		{name: "负数", value: -1, wantErr: true},
		{name: "上界本身放行", value: int64(common.MaxQuota), wantErr: false},
		{name: "上界 +1 必须拒", value: int64(common.MaxQuota) + 1, wantErr: true},
		{name: "旧 int32 上界之上、新上界之内,必须放行", value: math.MaxInt32 + 1, wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkQuotaCap("qy.test_quota", tc.value)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
		})
	}
}

// TestQuotaWorstMultiplierMirrorsLotteryEntriesHardCap 把两个互为镜像的常量
// 逐值钉在一起。
//
// 这是那句「两边由测试钉在一起,谁先漂移谁先红」唯一能成立的地方:
// common 不能 import qianye/config(会成环),所以 common 侧只能留一份镜像;
// 而镜像与本体的相等**只有这个包看得见**(它同时 import 得到两者)。
//
// 少了这一条,单独把 common 侧的镜像砍半会:编译通过、全部测试通过,
// 同时把 common/quota_math.go 里那条 `MaxInt64/镜像 - MaxQuota` 的编译期断言
// 放宽一倍 —— 足以让 MaxQuota 被抬到 2^44,而那里真实最坏乘积是 2^63。
func TestQuotaWorstMultiplierMirrorsLotteryEntriesHardCap(t *testing.T) {
	assert.EqualValues(t, MaxLotteryEntriesHard, common.MaxQuotaWorstMultiplier,
		"common.MaxQuotaWorstMultiplier 是 config.MaxLotteryEntriesHard 的镜像,"+
			"两者必须逐值相同 —— 前者是额度上界那条编译期断言的分母,"+
			"后者是它所声称的那个真实上界")
}
