package config

import (
	"encoding/base64"
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
// large_prize_alert_stardust 的二次确认(qianye/modules/lottery/caps.go),
// 而一道谁都能调大的硬拒绝本来就拦不住手滑 —— 它只能把手滑推迟到更大的数字上。
//
// 这份测试盯两件事:0 真的能启动(否则改了等于没改),
// 以及那道二次确认不会被配成一个**永远不响的铃**。

// lotteryBaseline 是一份除了三个额度上限之外全部合法的抽奖配置。
func lotteryBaseline() Lottery {
	return Lottery{
		Enabled: true, RevealDelaySeconds: 60,
		// 开着抽奖就必须有兑换码密钥(文本奖不允许明文落库),否则基线自己就不合法。
		// 全零字节只是一个形状合法的测试用值,真实部署必须是随机的。
		PrizeSecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		MaxGuessFeeBps: 2000, DefaultGuessFeeBps: 500,
		MaxTotalEntriesHard: 50000, MaxPrizeTiers: 10, MaxOptions: 12,
		PayoutMaxAttempts: 8, CoverMaxBytes: 1 << 20,
		EntryBatchMaxMs:      45_000,
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
			mutate: func(l *Lottery) { l.MaxTotalPrizeStardust = -1 },
		},
		{
			name: "参与费上限为负", wantErr: true,
			mutate: func(l *Lottery) { l.MaxStakeStardust = -1 },
		},
		{
			name: "二次确认阈值为负", wantErr: true,
			mutate: func(l *Lottery) { l.LargePrizeAlertStardust = -1 },
		},
		{
			// 一道装上去却不通电的闸门:够到阈值之前,活动就已经被硬顶 400 掉了。
			// 而这道确认是本模块唯一还在盯着"多写一个零"的东西。
			name: "阈值高过硬顶", wantErr: true,
			mutate: func(l *Lottery) {
				l.MaxTotalPrizeStardust = 1_000_000
				l.LargePrizeAlertStardust = 2_000_000
			},
		},
		{
			name: "阈值恰好等于硬顶:放行", wantErr: false,
			mutate: func(l *Lottery) {
				l.MaxTotalPrizeStardust = 1_000_000
				l.LargePrizeAlertStardust = 1_000_000
			},
		},
		{
			// 硬顶不限时阈值多高都只是"少响几次",一分钱都不会多发。
			name: "硬顶不限而阈值很高:放行", wantErr: false,
			mutate: func(l *Lottery) {
				l.MaxTotalPrizeStardust = 0
				l.LargePrizeAlertStardust = math.MaxInt32
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

	assert.EqualValues(t, 0, c.Lottery.MaxStakeStardust,
		"参与费上限默认不限 —— 参与费是用户自己付的钱,配得离谱只会没人报名")
	assert.EqualValues(t, 0, c.Lottery.MaxTotalPrizeStardust,
		"奖品硬顶默认不限 —— 拦手滑的是二次确认,不是这道硬拒绝")
	assert.EqualValues(t, 0, c.Lottery.LargePrizeAlertStardust,
		"二次确认阈值默认 0 = 完全不打扰;机制留着,想开的站点配一个正数即可")

	// 这份默认组合必须能通过校验,否则"什么都不写就能起来"这条承诺是假的。
	//
	// 兑换码密钥是这句话**唯一**的例外,而且是刻意的:它没有、也不能有默认值
	// (硬编码常量 = 全站共用一把钥匙;每次启动随机 = 重启后历史密文全部不可读),
	// 所以开着抽奖的部署必须自己填一行。这里补上它,好让这条断言仍然只测
	// "三个额度上限的默认值组合合法"这一件事。
	c.Lottery.Enabled = true
	c.Lottery.PrizeSecretKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	assert.NoError(t, validateLottery(&c.Lottery))
}

// ─────────────── 文本奖兑换码密钥(v1.0.0 起必填) ───────────────

// 开着抽奖却没配密钥 = 拒绝启动。
//
// 这是本 fork 记为 MAJOR 的那处破坏性变更(qianye/version/baseline.txt v1.0.0)。
// 它必须是**校验器**里的一条,而不是一句文档:留空的语义是兑换码明文落库,
// 而那一列的名字叫 cipher —— 没有这条断言,回落到"留空即明文"只需要删掉
// validateLottery 里的四行,并且不会有任何测试变红。
func TestValidateLotteryRequiresThePrizeSecretKey(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Lottery)
		wantErr bool
	}{
		{
			name: "关着抽奖时不管这一项", wantErr: false,
			mutate: func(l *Lottery) { l.Enabled = false; l.PrizeSecretKey = "" },
		},
		{
			name: "开着抽奖却留空:拒绝启动", wantErr: true,
			mutate: func(l *Lottery) { l.PrizeSecretKey = "" },
		},
		{
			name: "只有空白字符同样算留空", wantErr: true,
			mutate: func(l *Lottery) { l.PrizeSecretKey = "   " },
		},
		{
			name: "不是 base64", wantErr: true,
			mutate: func(l *Lottery) { l.PrizeSecretKey = "这不是 base64" },
		},
		{
			// 长度不对的表现是"已履行的文本奖全部读不出来",而那要等到管理员
			// 点开某一条 reveal 才会发现 —— 必须在启动时就查。
			name: "base64 合法但不是 32 字节", wantErr: true,
			mutate: func(l *Lottery) {
				l.PrizeSecretKey = base64.StdEncoding.EncodeToString(make([]byte, 16))
			},
		},
		{
			// 一次做了一半的轮换:旧钥搬进了退役表,却忘了抬版本号。
			// 后果是同版本下新密文用新钥、历史密文用旧钥,后者永久不可读,
			// 且没有任何迹象(选钥时当前版本先命中,退役表里那把轮不到)。
			name: "退役表里含当前版本号", wantErr: true,
			mutate: func(l *Lottery) {
				l.PrizeSecretKeyVersion = 2
				l.PrizeSecretKeysRetired = map[int]string{
					2: base64.StdEncoding.EncodeToString(make([]byte, 32)),
				}
			},
		},
		{
			// 版本号没填(0)时按 1 算,退役表里的 1 同样要被拒 ——
			// 两处对"当前版本是几"的归一必须是同一个函数。
			name: "版本号缺省时退役表里的 1 同样被拒", wantErr: true,
			mutate: func(l *Lottery) {
				l.PrizeSecretKeyVersion = 0
				l.PrizeSecretKeysRetired = map[int]string{
					1: base64.StdEncoding.EncodeToString(make([]byte, 32)),
				}
			},
		},
		{
			name: "正常轮换:旧钥在退役表、新钥版本更大", wantErr: false,
			mutate: func(l *Lottery) {
				l.PrizeSecretKeyVersion = 2
				l.PrizeSecretKeysRetired = map[int]string{
					1: base64.StdEncoding.EncodeToString(make([]byte, 32)),
				}
			},
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

// 整段缺失时给出的修复片段,粘回去必须带着一把**能用的**兑换码密钥。
//
// lottery 段现在有一个既必填、又不可能有默认值的密钥项。片段里不带上它,
// 运维照着"修复指引"粘完之后网关起不来 —— 一条不阻断启动的告警,其修复指引
// 反而把网关关停了,那正是这套告警自己要消灭的形状。
//
// 与 sections_test.go 的 TestFixSnippetsPasteBackIntoAParsableFile 分工:
// 那一条盯"粘回去还能解析/校验",这一条盯**那把钥匙是真的钥匙** ——
// 一个写死的示例值、一个占位串、一把 16 字节的钥匙都能让前者继续绿。
func TestLotteryFixSnippetCarriesAUsablePrizeSecretKey(t *testing.T) {
	var gate ModuleGate
	for _, g := range gatedSwitches() {
		if g.Module == "lottery" {
			gate = g
			break
		}
	}
	require.Equal(t, "lottery", gate.Module, "moduleGates 里没有 lottery 这一行")

	snippet := sectionFixSnippet(gate)
	require.Contains(t, snippet, "prize_secret_key")

	// 走真实的加载路径:片段追加到一份最小配置后面,由 parseFile 补默认值并校验。
	c, _, err := parseFile(writeTemp(t, minimalValid+"\n"+snippet+"\n"))
	require.NoError(t, err, "照着修复片段粘回去的配置必须能启动,否则这条告警的修复指引会把网关关停")
	require.True(t, c.Lottery.Enabled)

	key, err := base64.StdEncoding.DecodeString(c.Lottery.PrizeSecretKey)
	require.NoError(t, err)
	assert.Len(t, key, 32, "片段给的必须是一把真钥匙,不是占位串")
	assert.NotEqual(t, make([]byte, 32), key, "全零不是随机密钥")

	// 每次生成的都是一把新钥匙:片段是给"段还不存在"的部署用的,那里没有
	// 任何历史密文,而一个写死的示例密钥会被抄进每一个照做的站点。
	assert.NotEqual(t, snippet, sectionFixSnippet(gate))
}
