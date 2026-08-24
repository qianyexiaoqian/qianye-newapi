package config

import (
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lottery_caps_test.go —— 抽奖那三个额度上限的零值语义。
//
// 它们从"必须大于 0,否则拒绝启动"改成了"**0 = 不限制**,而且是默认值"。
// 这不是放宽口径:拦住"奖品金额多写一个零"的东西换成了
// large_prize_alert_quota 的二次确认(qianye/modules/lottery/caps.go),
// 而一道谁都能调大的硬拒绝本来就拦不住手滑 —— 它只能把手滑推迟到更大的数字上。
//
// 这份测试盯两件事:0 真的能启动(否则改了等于没改),
// 以及那道二次确认不会被配成一个**永远不响的铃**。

// lotteryBaseline 是一份除了三个额度上限之外全部合法的抽奖配置。
func lotteryBaseline() Lottery {
	return Lottery{
		Enabled: true, RevealDelaySeconds: 60,
		MaxGuessFeeBps: 2000, DefaultGuessFeeBps: 500,
		MaxTotalEntriesHard: 50000, MaxPrizeTiers: 10, MaxOptions: 12,
		PayoutMaxAttempts: 8, CoverMaxBytes: 1 << 20,
		SpendMaxLookbackDays: 90, SpendScanBatch: 100, SpendRetentionDays: 120,
	}
}

func TestValidateLotteryQuotaCeilings(t *testing.T) {
	// 前置:基线本身必须是合法的,否则下面每一条都在断言别的东西。
	baseline := lotteryBaseline()
	require.NoError(t, validateLottery(&baseline), "基线配置必须能通过校验")

	cases := []struct {
		name    string
		mutate  func(*Lottery)
		wantErr bool
	}{
		{
			// 这是新的默认形态。旧的 validateLottery 在这里直接拒绝启动,
			// 理由是"这是唯一能拦住多写一个零的闸门" —— 那句话现在不成立了。
			name: "三项全 0:启动", mutate: func(l *Lottery) {}, wantErr: false,
		},
		{
			name: "奖品硬顶为负", wantErr: true,
			mutate: func(l *Lottery) { l.MaxTotalPrizeQuota = -1 },
		},
		{
			name: "参与费上限为负", wantErr: true,
			mutate: func(l *Lottery) { l.MaxStakeQuota = -1 },
		},
		{
			name: "二次确认阈值为负", wantErr: true,
			mutate: func(l *Lottery) { l.LargePrizeAlertQuota = -1 },
		},
		{
			// 一道装上去却不通电的闸门:够到阈值之前,活动就已经被硬顶 400 掉了。
			// 而这道确认是本模块唯一还在盯着"多写一个零"的东西。
			name: "阈值高过硬顶", wantErr: true,
			mutate: func(l *Lottery) {
				l.MaxTotalPrizeQuota = 1_000_000
				l.LargePrizeAlertQuota = 2_000_000
			},
		},
		{
			name: "阈值恰好等于硬顶:放行", wantErr: false,
			mutate: func(l *Lottery) {
				l.MaxTotalPrizeQuota = 1_000_000
				l.LargePrizeAlertQuota = 1_000_000
			},
		},
		{
			// 硬顶不限时阈值多高都只是"少响几次",一分钱都不会多发。
			name: "硬顶不限而阈值很高:放行", wantErr: false,
			mutate: func(l *Lottery) {
				l.MaxTotalPrizeQuota = 0
				l.LargePrizeAlertQuota = math.MaxInt32
			},
		},
		{
			// 与本次改动无关的那条硬约束必须原样还在:它是承诺-揭示协议的
			// 核心间隔,为 0 等于整个协议退化成"平台自己说它没改"。
			name: "reveal_delay 仍然不接受 0", wantErr: true,
			mutate: func(l *Lottery) { l.RevealDelaySeconds = 0 },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lot := lotteryBaseline()
			tc.mutate(&lot)
			err := validateLottery(&lot)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// 默认值必须**真的**是 0。
//
// "写在 defaults.go 里的意图"与"加载之后的取值"在本仓分过一次家
// (int64Default 的哨兵判据),所以这里断的是 applyDefaults 跑完之后的形态,
// 而不是那一行代码长什么样。
func TestLotteryQuotaCeilingsDefaultToUnlimited(t *testing.T) {
	// 走真实的加载顺序:先打哨兵(区分"写了 0"与"没写"),再套默认值。
	// 少了第一步,applyDefaults 看到的每个字段都是"写了 0",什么都不会补上,
	// 于是这条测试会在一份全 0 的配置上空转通过。
	var c Config
	markNumbersUnset(reflect.ValueOf(&c).Elem())
	applyDefaults(&c)

	assert.EqualValues(t, 0, c.Lottery.MaxStakeQuota,
		"参与费上限默认不限 —— 参与费是用户自己付的钱,配得离谱只会没人报名")
	assert.EqualValues(t, 0, c.Lottery.MaxTotalPrizeQuota,
		"奖品硬顶默认不限 —— 拦手滑的是二次确认,不是这道硬拒绝")
	assert.EqualValues(t, 5_000_000, c.Lottery.LargePrizeAlertQuota,
		"二次确认阈值必须仍然有默认值,它是唯一还在盯着「多写一个零」的东西")

	// 这份默认组合必须能通过校验,否则"什么都不写就能起来"这条承诺是假的。
	c.Lottery.Enabled = true
	assert.NoError(t, validateLottery(&c.Lottery))
}
