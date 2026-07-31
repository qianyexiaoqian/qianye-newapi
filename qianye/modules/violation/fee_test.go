package violation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useTestConfig 在测试内装载一份真实的扩展配置。
//
// 刻意走完整的 Load(含默认值填充与校验)而不是直接塞结构体:
// 计费行为依赖 defaults.go 补出来的默认值,绕过它测出来的金额不代表线上行为。
func useTestConfig(t *testing.T, violationYAML ...string) {
	t.Helper()
	section := "  enabled: true\n  shadow_mode: false\n  max_fee_quota: 2000000000\n"
	if len(violationYAML) > 0 {
		section = violationYAML[0]
	}
	content := "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\nviolation:\n" + section
	path := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	t.Setenv(config.EnvConfigPath, path)
	require.NoError(t, config.Load())
}

func infoWithPrice(p hosttypes.PriceData) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{}
	info.PriceData = p
	return info
}

func chargingRule(mode string, fixed, multiple string, maxQuota int64) *compiledRule {
	return &compiledRule{R: Rule{
		Id: 1, Action: ActionCharge, FeeMode: mode,
		FeeFixed:    decimal.RequireFromString(fixed),
		FeeMultiple: decimal.RequireFromString(multiple),
		FeeMaxQuota: maxQuota,
	}}
}

// TestComputeFeeAmounts 是扣费金额的数值走查。
//
// 违规扣费是"惩罚"而不是"计费":配错一位小数就是一次扣光用户余额,
// 所以每种模式的换算都必须有确定的期望值,而不是"大概差不多"。
func TestComputeFeeAmounts(t *testing.T) {
	useTestConfig(t)
	unit := decimal.NewFromFloat(common.QuotaPerUnit) // 500000 quota = $1

	t.Run("固定金额", func(t *testing.T) {
		info := infoWithPrice(hosttypes.PriceData{
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
		})
		res := computeFee(chargingRule(FeeFixed, "0.05", "0", 0), info)
		assert.EqualValues(t, unit.Mul(decimal.RequireFromString("0.05")).IntPart(), res.Want)
	})

	t.Run("固定金额未配时回落到 YAML 默认", func(t *testing.T) {
		info := infoWithPrice(hosttypes.PriceData{
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
		})
		res := computeFee(chargingRule(FeeFixed, "0", "0", 0), info)
		// defaults.go 的 fixed_fee_amount 默认 0.05
		assert.EqualValues(t, 25000, res.Want)
	})

	t.Run("按次计费模型:单价 × 倍数", func(t *testing.T) {
		info := infoWithPrice(hosttypes.PriceData{
			UsePrice: true, ModelPrice: 0.1,
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
		})
		res := computeFee(chargingRule(FeeModelPriceMultiple, "0", "2", 0), info)
		assert.EqualValues(t, 100000, res.Want) // $0.1 × 2 = $0.2
	})

	t.Run("按量计费模型:倍率折算美元后 × 倍数", func(t *testing.T) {
		info := infoWithPrice(hosttypes.PriceData{
			ModelRatio:     2.5,
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
		})
		res := computeFee(chargingRule(FeeModelPriceMultiple, "0", "2", 0), info)
		// 2.5 × $0.002 × 2 = $0.01 → 5000 quota
		assert.EqualValues(t, 5000, res.Want)
	})

	t.Run("分组倍率参与计算", func(t *testing.T) {
		info := infoWithPrice(hosttypes.PriceData{
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 2},
		})
		res := computeFee(chargingRule(FeeFixed, "0.05", "0", 0), info)
		assert.EqualValues(t, 50000, res.Want)
	})

	t.Run("分组倍率非正一律不扣费", func(t *testing.T) {
		for _, gr := range []float64{0, -1} {
			info := infoWithPrice(hosttypes.PriceData{
				GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: gr},
			})
			res := computeFee(chargingRule(FeeFixed, "0.05", "0", 0), info)
			assert.Zero(t, res.Want, "分组倍率 %v 不应产生扣费", gr)
		}
	})

	t.Run("动作不含 charge 时不计费", func(t *testing.T) {
		info := infoWithPrice(hosttypes.PriceData{
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
		})
		cr := chargingRule(FeeFixed, "0.05", "0", 0)
		cr.R.Action = ActionBlock
		assert.Zero(t, computeFee(cr, info).Want)
	})
}

// TestComputeFeeCaps 验证两道上限。
//
// 规则级上限防"这一条规则配错",全局上限防"所有规则一起配错"。
// 少任何一道,一个多打的 0 就能把用户余额一次扣穿。
func TestComputeFeeCaps(t *testing.T) {
	useTestConfig(t)
	info := infoWithPrice(hosttypes.PriceData{
		GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
	})

	t.Run("规则级上限", func(t *testing.T) {
		res := computeFee(chargingRule(FeeFixed, "100", "0", 1234), info)
		assert.EqualValues(t, 1234, res.Want)
	})

	t.Run("全局上限", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  max_fee_quota: 7777\n")
		res := computeFee(chargingRule(FeeFixed, "100", "0", 0), info)
		assert.EqualValues(t, 7777, res.Want)
	})

	t.Run("两道上限同时存在时取更严的一道", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  max_fee_quota: 7777\n")
		res := computeFee(chargingRule(FeeFixed, "100", "0", 100), info)
		assert.EqualValues(t, 100, res.Want)
	})

	// 额度饱和必须被记录:它几乎必然意味着倍数或单价配错了,
	// 静默截断会让这次事故没有任何线索。
	t.Run("超出 int32 时饱和并留痕", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  max_fee_quota: 2000000000\n")
		res := computeFee(chargingRule(FeeFixed, "999999999", "0", 0), info)
		assert.NotEmpty(t, res.Clamp, "饱和必须写进 quota_clamp 供管理端告警")
		assert.LessOrEqual(t, res.Want, int64(common.MaxQuota))
	})
}

// TestBalancePolicies 验证三种余额不足策略。
//
// 上游 PostConsumeQuota 底层的 DecreaseUserQuota 没有余额校验,
// 不做策略分流就会把用户余额直接扣成负数。
func TestBalancePolicies(t *testing.T) {
	cases := []struct {
		policy    string
		available int64
		want      int64
		charged   int64
		status    string
		forceBan  bool
	}{
		{"clamp", 0, 1000, 0, FeeStatusInsufficient, false},
		{"clamp", 300, 1000, 300, FeeStatusTruncated, false},
		{"clamp", 5000, 1000, 1000, FeeStatusCharged, false},
		{"negative", 0, 1000, 1000, FeeStatusCharged, false},
		{"negative", -500, 1000, 1000, FeeStatusCharged, false},
		{"ban", 300, 1000, 0, FeeStatusInsufficient, true},
		{"ban", 5000, 1000, 1000, FeeStatusCharged, false},
	}
	for _, tc := range cases {
		t.Run(tc.policy+"/"+itoa(tc.available), func(t *testing.T) {
			useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  insufficient_balance_policy: "+tc.policy+"\n")
			res := feeResult{Want: tc.want, Status: FeeStatusNone}
			applyBalancePolicy(&res, tc.available)
			assert.Equal(t, tc.charged, res.Charged)
			assert.Equal(t, tc.status, res.Status)
			assert.Equal(t, tc.forceBan, res.ForceBanWeight)
			// 无论哪种策略,实扣都不得超过应扣 —— 否则就是凭空多罚。
			assert.LessOrEqual(t, res.Charged, res.Want)
			// 也不得为负:负数会被 PostConsumeQuota 当成退款,变成给违规用户送钱。
			assert.GreaterOrEqual(t, res.Charged, int64(0))
		})
	}
}

func itoa(v int64) string { return decimal.NewFromInt(v).String() }
