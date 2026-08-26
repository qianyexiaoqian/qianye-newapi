package lottery

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// caps_test.go —— 额度闸门放开之后必须仍然成立的那几条。
//
// 这一组盯的是本次改造的**两个方向**,少一个方向就是假绿:
//
//	放开的那些:超大值现在真的能过(不然改了等于没改);
//	保留的那些:概率和 > 100%、奖品额度 ≤ 0、int64 溢出、以及越过阈值时的
//	           二次确认 —— 它们一条都不能因为"顺手把上限拿掉了"而一起消失。

// withoutSettingsCache 把运营配置的进程内缓存清干净。
//
// 同包里那些接了扩展库的用例会把一份带覆盖值的快照留在 settingsCache 里,
// 而本文件全部走 YAML 基线。不清的表现是这一组用例**单跑全绿、全包跑随机红**,
// 而且红在一个与被测代码无关的字段上。
func withoutSettingsCache(t *testing.T) {
	t.Helper()
	invalidateSettings()
	t.Cleanup(invalidateSettings)
}

// ─────────────── 1. 二次确认:两个 0 的语义不同 ───────────────

func TestRequireNetIssueConfirm(t *testing.T) {
	const threshold = 5_000_000

	cases := []struct {
		name      string
		threshold int64
		total     int64
		echoed    int64
		wantErr   bool
	}{
		{
			// 阈值 0 = 完全不打扰。这是"配成 0 连确认都不要"那一档,
			// 也是运营抱怨"卡了半天"时唯一能一次性关掉全部打扰的开关。
			name: "阈值 0:任何金额都放行", threshold: 0,
			total: math.MaxInt32, echoed: 0, wantErr: false,
		},
		{
			name: "阈值 0 且回显也是 0:仍然放行", threshold: 0,
			total: 0, echoed: 0, wantErr: false,
		},
		{
			name: "低于阈值:不问", threshold: threshold,
			total: threshold - 1, echoed: 0, wantErr: false,
		},
		{
			// 判据是 >=,与 buildPrizes 那条 SysError 以及前端 NetIssueMeter
			// 逐字同源。一边 > 一边 >= 的表现是恰好等于阈值的那一场"日志喊了
			// 但没要确认",或者反过来"界面不弹、提交吃 400"。
			name: "恰好等于阈值且没回显:拒绝", threshold: threshold,
			total: threshold, echoed: 0, wantErr: true,
		},
		{
			name: "恰好等于阈值且回显正确:放行", threshold: threshold,
			total: threshold, echoed: threshold, wantErr: false,
		},
		{
			// 零值必须落在**安全**的一侧:一个漏传字段的旧客户端要被拒绝,
			// 不能被放行。
			name: "越过阈值但没回显:拒绝", threshold: threshold,
			total: threshold * 3, echoed: 0, wantErr: true,
		},
		{
			name: "回显差一:拒绝", threshold: threshold,
			total: threshold * 3, echoed: threshold*3 - 1, wantErr: true,
		},
		{
			// 回显的必须是**总额**而不是阈值。抄错这一个数的人显然没看那一屏,
			// 而"看过那个数"正是这道确认要证明的唯一一件事。
			name: "回显成阈值:拒绝", threshold: threshold,
			total: threshold * 3, echoed: threshold, wantErr: true,
		},
		{
			name: "回显正确:放行", threshold: threshold,
			total: threshold * 3, echoed: threshold * 3, wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireNetIssueConfirm(tc.threshold, tc.total, tc.echoed)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			be, ok := AsBizError(err)
			require.True(t, ok, "必须是可以安全回给运营的业务错误")
			assert.Equal(t, codeNetIssueConfirm, be.ErrCode())
			assert.Contains(t, be.Message(), strconv.FormatInt(tc.total, 10),
				"文案里必须出现要回填的那个精确数字,否则运营无从照抄")
		})
	}
}

// 拒绝文案里的金额必须是**站内余额刻度**,不是裸额度。
//
// "不得超过 5000000" 对着界面上的 $10 是对不上号的 —— 项目方那句
// "怎么在抽奖设置这里不能超过 100 站点余额"的困惑就是从这里来的。
func TestNetIssueConfirmMessageUsesSiteBalanceScale(t *testing.T) {
	require.Equal(t, 500000.0, common.QuotaPerUnit,
		"下面的期望值按 500000 quota = 1 USD 手算,单位变了必须重算")
	withQuotaDisplay(t, operation_setting.QuotaDisplayTypeUSD, "", 0)

	// 5000 万额度 = $100,500 万额度 = $10。两个数都独立手算。
	err := netIssueConfirmRequired(50_000_000, 5_000_000)

	assert.Contains(t, err.Message(), "＄100.000000 额度", "总额要按站内余额刻度写出来")
	assert.Contains(t, err.Message(), "＄10.000000 额度", "阈值同样按站内余额刻度")
	// 回填值仍然是**存储用的整数**:运营要照抄进 confirm_net_issue_quota,
	// 那个字段认的是额度不是美元。两个刻度必须同屏出现,不能二选一。
	assert.Contains(t, err.Message(), "50000000", "要回填的整数必须原样给出")
}

// TOKENS 口径下同一条文案换一种写法,而不是硬编码一个美元符号。
func TestQuotaTextFollowsTheSiteDisplaySetting(t *testing.T) {
	withQuotaDisplay(t, operation_setting.QuotaDisplayTypeTokens, "", 0)
	assert.Equal(t, "50000000 点额度", quotaText(50_000_000))

	withQuotaDisplay(t, operation_setting.QuotaDisplayTypeUSD, "", 0)
	assert.Equal(t, "＄100.000000 额度", quotaText(50_000_000))
}

// ─────────────── 2. 放开的那些:超大值现在能过 ───────────────

// prizeEnv 是 buildPrizes 的最小上下文:一场名次制抽奖。
func prizeEnv() (config.Lottery, *Activity) {
	return config.Lottery{MaxPrizeTiers: 10, MaxTotalEntriesHard: 50000},
		&Activity{DrawMode: DrawModeRank, Algo: AlgoV2, MaxTotalEntries: 100}
}

func TestBuildPrizesAcceptsHugeTotalsWhenNoCeilingIsConfigured(t *testing.T) {
	cfg, act := prizeEnv()

	// 5 亿额度 = $1000,是旧默认硬顶(5000 万 = $100)的十倍。
	// 这一条就是"奖品总额上限,你不要限制了"的可执行形式。
	rows, _, err := buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 100_000_000, Count: 5},
	}, cfg, opSettings{MaxTotalPrizeQuota: 0}, act)

	require.NoError(t, err, "上限配成 0 之后超大奖品总额必须能建出来")
	assert.EqualValues(t, 500_000_000, prizeTotalRows(rows))
}

// 站点**自己**配了硬顶时,行为与从前完全一致 —— 这一档不能被顺手删掉,
// 否则"我确实想要一道谁都绕不过去的硬顶"的站点就没有任何办法了。
func TestBuildPrizesStillEnforcesASelfConfiguredCeiling(t *testing.T) {
	require.Equal(t, 500000.0, common.QuotaPerUnit)
	withQuotaDisplay(t, operation_setting.QuotaDisplayTypeUSD, "", 0)
	cfg, act := prizeEnv()
	set := opSettings{MaxTotalPrizeQuota: 1_000_000}

	// 恰好等于硬顶:必须放行。闭区间的边界写反是这类闸门最常见的错法。
	_, _, err := buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 500_000, Count: 2},
	}, cfg, set, act)
	require.NoError(t, err, "恰好等于硬顶必须放行")

	// 超出一个额度:拒绝,而且文案里的两个数都是站内余额刻度。
	_, _, err = buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 500_001, Count: 2},
	}, cfg, set, act)
	require.Error(t, err)
	be, ok := AsBizError(err)
	require.True(t, ok)
	assert.Equal(t, codePrizeCap, be.ErrCode())
	assert.Contains(t, be.Message(), "＄2.000004 额度", "总额按站内余额刻度")
	assert.Contains(t, be.Message(), "＄2.000000 额度", "硬顶按站内余额刻度")
}

// ─────────────── 3. 保留的那些:一条都不许消失 ───────────────

func TestBuildPrizesKeepsTheCorrectnessConstraints(t *testing.T) {
	cfg, act := prizeEnv()
	prob := &Activity{DrawMode: DrawModeProb, Algo: AlgoV2, MaxTotalEntries: 100}
	noCeiling := opSettings{MaxTotalPrizeQuota: 0}

	cases := []struct {
		name string
		act  *Activity
		in   []prizeInput
	}{
		{
			// 额度奖发 0 没有意义,而且 PlanPayouts 会**静默跳过** amount<=0
			// 的计划 —— 一个真中了奖的人连 payout 行都不会有。
			name: "奖品额度为 0", act: act,
			in: []prizeInput{{Tier: 1, Name: "一等奖", AmountQuota: 0, Count: 1}},
		},
		{
			name: "奖品额度为负", act: act,
			in: []prizeInput{{Tier: 1, Name: "一等奖", AmountQuota: -1, Count: 1}},
		},
		{
			// int32 是 quota 列的列宽,不是运营闸门。放开总额不等于放开列宽。
			name: "单档额度越过额度上界", act: act,
			in: []prizeInput{
				{Tier: 1, Name: "一等奖", AmountQuota: int64(common.MaxQuota) + 1, Count: 1},
			},
		},
		{
			name: "奖品数量为 0", act: act,
			in: []prizeInput{{Tier: 1, Name: "一等奖", AmountQuota: 1000, Count: 0}},
		},
		{
			// 概率之和超过 100% 意味着两档的摇号区间重叠,而"一张票同时中两档"
			// 在派奖层会撞唯一键、静默丢掉第二个奖。这是正确性约束,不是额度限制。
			name: "概率之和超过 100%", act: prob,
			in: []prizeInput{
				{Tier: 1, Name: "一等奖", AmountQuota: 100_000, Count: 1, WinPpm: 600_000},
				{Tier: 2, Name: "二等奖", AmountQuota: 100_000, Count: 1, WinPpm: 500_000},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := buildPrizes(tc.in, cfg, noCeiling, tc.act)
			require.Error(t, err, "这是正确性约束,不该跟着额度上限一起被放开")
		})
	}
}

// 溢出护栏:上限拿掉之后 Σ(count × amount) 再没有配置项夹着它。
//
// 绕回负数在这里不是崩溃,是**静默放行** —— 一个负的总额会让硬顶判定、
// 阈值判定连同二次确认(echoed==total,而 0 也可能等于它)一起通过。
func TestBuildPrizesRefusesToOverflowInt64(t *testing.T) {
	// count 的上界 max_total_entries_hard 是 YAML 配置项,配得离谱时护栏必须
	// 自己站得住,不能依赖"运营不会这么配"。两档分别打两个不同的判定点:
	//
	//   单档乘积就顶穿 int64  → 只有**乘之前**那次除法判定拦得住;
	//                            算完再判 total 时数已经绕回去了。
	//   单档合法、累加才越界  → 只有累加之后那次判定拦得住。
	//
	// 两条缺一不可,而且必须各自单独验证:少写第一条时,第二条仍然会让
	// "int32 × int32"那种输入照常报错(4.6e18 还没到 int64 上界),
	// 于是缺陷藏在一个看起来已经覆盖了的用例后面。
	cases := []struct {
		name   string
		amount int64
		count  int
	}{
		{
			// 2^30 × 2^34 = 2^64 ——「绕回」在这里不是绕成一个大数,而是**恰好
			// 绕成 0**。少了乘之前那次判定,total 会安安静静地停在 0:
			// 硬顶判定通过、护栏判定通过、二次确认因为 total=0 连问都不问,
			// 而这一档实际登记的是 170 亿份、每份 10 亿额度的奖。
			//
			// 挑这个乘积不是为了刁钻,而是因为"绕回成一个大数"那种输入
			// **拦得住不代表这一条判定存在** —— 累加之后那次判定会顺手接住它,
			// 于是删掉乘之前那次判定的变异照样活着(实测 SURVIVED)。
			name: "单档乘积恰好绕回 0", amount: 1 << 30, count: 1 << 34,
		},
		{
			// 2.1e9 × 2.1e9 ≈ 4.6e18:仍在 int64 之内,只是越过了护栏。
			name: "单档不溢出但越过护栏", amount: int64(common.MaxQuota), count: math.MaxInt32,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, act := prizeEnv()
			cfg.MaxTotalEntriesHard = math.MaxInt
			act.MaxTotalEntries = math.MaxInt32

			_, _, err := buildPrizes([]prizeInput{
				{Tier: 1, Name: "一等奖", AmountQuota: tc.amount, Count: tc.count},
			}, cfg, opSettings{MaxTotalPrizeQuota: 0}, act)

			require.Error(t, err)
			be, ok := AsBizError(err)
			require.True(t, ok)
			assert.Equal(t, codeNetIssueOverflow, be.ErrCode())
		})
	}
}

// 逐档合法、累加起来才顶穿 —— 单档判定通过之后必须再判一次总额。
func TestBuildPrizesRefusesOverflowAccumulatedAcrossTiers(t *testing.T) {
	cfg, act := prizeEnv()
	cfg.MaxTotalEntriesHard = math.MaxInt32
	act.MaxTotalEntries = math.MaxInt32

	// 单档 = guard 的 0.6 倍,两档相加越过 guard 但远未溢出 int64:
	// 这一条只有"累加之后再判一次"才拦得住。
	perTier := netIssueOverflowGuard / 10 * 6
	amount := int64(common.MaxQuota)
	count := int(perTier / amount)

	_, _, err := buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: amount, Count: count},
		{Tier: 2, Name: "二等奖", AmountQuota: amount, Count: count},
	}, cfg, opSettings{MaxTotalPrizeQuota: 0}, act)

	require.Error(t, err)
	be, ok := AsBizError(err)
	require.True(t, ok)
	assert.Equal(t, codeNetIssueOverflow, be.ErrCode())
}

// ─────────────── 4. buildActivity:整条创建路径 ───────────────

// hugePrizeInput 造一份参与费与奖品都远超旧硬顶的活动。
func hugePrizeInput(confirm int64) *activityInput {
	now := common.GetTimestamp()
	return &activityInput{
		Kind:     KindDraw,
		DrawMode: DrawModeRank,
		Title:    "qy-超大活动",
		// 旧闸门是 500 万($10)。
		StakeQuota:     50_000_000,
		OpenAt:         now + 600,
		CloseAt:        now + 7200,
		DrawAt:         now + 14400,
		SettleDeadline: now + 21600,
		Prizes: []prizeInput{
			// 5 亿额度 = $1000,旧默认硬顶的十倍。
			{Tier: 1, Name: "一等奖", AmountQuota: 100_000_000, Count: 5},
		},
		ConfirmNetIssueQuota: confirm,
	}
}

// 默认配置(三个上限缺省)下,一场超大活动只差一次回显就能建出来。
func TestBuildActivityWithDefaultConfigOnlyNeedsTheEchoedAmount(t *testing.T) {
	withoutSettingsCache(t)
	// defaults.go 只给 large_prize_alert_quota 补默认值,另外两项缺省为 0。
	// 这里用 ApplyDefaults 之后的真实形态,而不是手写一份"我以为的默认"。
	lot := config.Lottery{
		Enabled: true, MaxPrizeTiers: 10, MaxOptions: 12,
		MaxTotalEntriesHard: 50000, RevealDelaySeconds: 60,
		EntryCloseGraceSeconds: 60, SpendMaxLookbackDays: 90,
		LargePrizeAlertQuota: 5_000_000,
		// 三项额度上限一律 0 = 不限。
		MaxStakeQuota: 0, MaxTotalPrizeQuota: 0,
	}
	withLotteryConfig(t, lot)

	// 没回显:被二次确认拦住,而且拦的理由带着精确金额。
	_, _, _, err := buildActivity(context.Background(), hugePrizeInput(0), 1)
	require.Error(t, err, "越过阈值必须要求确认")
	be, ok := AsBizError(err)
	require.True(t, ok)
	assert.Equal(t, codeNetIssueConfirm, be.ErrCode())
	assert.Contains(t, be.Message(), "500000000")

	// 回显之后放行 —— 一次也不用去改配置文件。
	act, prizes, _, err := buildActivity(context.Background(), hugePrizeInput(500_000_000), 1)
	require.NoError(t, err, "回显金额之后必须放行,否则等于把硬拒绝换了个名字")
	assert.EqualValues(t, 50_000_000, act.StakeQuota, "参与费不再被 max_stake_quota 夹")
	assert.EqualValues(t, 500_000_000, prizeTotalRows(prizes))
}

// 阈值配成 0 = 连确认都不要。这是"完全不打扰"那一档。
func TestBuildActivityAsksNothingWhenTheThresholdIsZero(t *testing.T) {
	withoutSettingsCache(t)
	withLotteryConfig(t, config.Lottery{
		Enabled: true, MaxPrizeTiers: 10, MaxOptions: 12,
		MaxTotalEntriesHard: 50000, RevealDelaySeconds: 60,
		EntryCloseGraceSeconds: 60, SpendMaxLookbackDays: 90,
		LargePrizeAlertQuota: 0, MaxStakeQuota: 0, MaxTotalPrizeQuota: 0,
	})

	_, _, _, err := buildActivity(context.Background(), hugePrizeInput(0), 1)
	assert.NoError(t, err, "阈值 0 时不该问任何问题")
}

// 竞猜没有奖档,阈值再低也不会被二次确认打扰。
//
// 它是彩池制:SplitPool 结尾断言 Σpay + fee == pool,平台数学上不可能倒贴,
// 因此这道盯着"净增发"的确认对它没有任何意义。
func TestBuildActivityNeverAsksForGuess(t *testing.T) {
	withoutSettingsCache(t)
	withLotteryConfig(t, config.Lottery{
		Enabled: true, MaxPrizeTiers: 10, MaxOptions: 12,
		MaxTotalEntriesHard: 50000, RevealDelaySeconds: 60,
		EntryCloseGraceSeconds: 60, SpendMaxLookbackDays: 90,
		MaxGuessFeeBps: 2000, DefaultGuessFeeBps: 500,
		LargePrizeAlertQuota: 1, MaxStakeQuota: 0, MaxTotalPrizeQuota: 0,
	})

	now := common.GetTimestamp()
	_, _, _, err := buildActivity(context.Background(), &activityInput{
		Kind: KindGuess, Title: "qy-竞猜", StakeQuota: 50_000_000,
		OpenAt: now + 600, CloseAt: now + 7200, DrawAt: now + 14400,
		SettleDeadline: now + 21600,
		Options: []optionInput{
			{OptNo: 1, Label: "甲队胜"},
			{OptNo: 2, Label: "乙队胜", IsCatchAll: true},
		},
	}, 1)
	assert.NoError(t, err, "竞猜是彩池制,没有净增发可确认")
}

// ─────────────── 5. 单注上限:界面不许撒谎 ───────────────

func TestApplyBetBounds(t *testing.T) {
	require.Equal(t, 500000.0, common.QuotaPerUnit)
	withQuotaDisplay(t, operation_setting.QuotaDisplayTypeUSD, "", 0)

	t.Run("上限不限时,int32 以内的大额单注放行", func(t *testing.T) {
		act := &Activity{}
		require.NoError(t, applyBetBounds(act, &activityInput{
			BetMaxQuota: int64(common.MaxQuota),
		}, config.Lottery{MaxStakeQuota: 0}))
		assert.EqualValues(t, common.MaxQuota, act.BetMaxQuota)
	})

	t.Run("越过额度上界仍然拒绝", func(t *testing.T) {
		// acceptAmount 无条件拒绝 amount > MaxQuota,所以一个填在它之上的
		// 单注上限是一句界面谎言:页面写着能压这么多,实际到上界就报
		// "投注金额不符合本场规则",而那句话不会说真正的上界是多少。
		err := applyBetBounds(&Activity{}, &activityInput{
			BetMaxQuota: int64(common.MaxQuota) + 1,
		}, config.Lottery{MaxStakeQuota: 0})
		require.Error(t, err)
		// 报错里必须念出**当前**的上界刻度,而不是一个抄下来的旧数字。
		assert.Contains(t, err.(*bizError).Message(), quotaText(int64(common.MaxQuota)))
	})

	t.Run("站点配了硬顶就仍然拦", func(t *testing.T) {
		err := applyBetBounds(&Activity{}, &activityInput{BetMaxQuota: 5_000_001},
			config.Lottery{MaxStakeQuota: 5_000_000})
		require.Error(t, err)
		assert.Contains(t, err.(*bizError).Message(), "＄10.000000 额度",
			"文案里的数字必须是站内余额刻度")
	})

	t.Run("下限大于上限仍然拒绝", func(t *testing.T) {
		require.Error(t, applyBetBounds(&Activity{},
			&activityInput{BetMinQuota: 100, BetMaxQuota: 10},
			config.Lottery{MaxStakeQuota: 0}))
	})
}

// ─────────────── 6. 在线配置:0 = 不限,而且读写同源 ───────────────

func TestQuotaCeilingBound(t *testing.T) {
	t.Run("YAML 写了正数:在线只能调低", func(t *testing.T) {
		b := quotaCeilingBound(50_000_000)
		assert.False(t, b.NoMax)
		assert.EqualValues(t, 1, b.Lo)
		assert.EqualValues(t, 50_000_000, b.Hi)
		assert.True(t, b.contains(50_000_000), "恰好等于 YAML 上界要收")
		assert.False(t, b.contains(50_000_001), "调高必须被拒")
		// 关键:不许在线写 0。允许写 0 等于允许一个 HTTP 接口把站点自己立的
		// 硬顶变成"不限" —— 那正是"上界必须取自 YAML"这条规则要防的事。
		assert.False(t, b.contains(0), "YAML 立了硬顶时不许在线改成不限")
	})

	t.Run("YAML 是 0:本来就不限", func(t *testing.T) {
		b := quotaCeilingBound(0)
		assert.True(t, b.NoMax)
		assert.EqualValues(t, 0, b.Lo)
		assert.True(t, b.contains(0), "0 = 不限,是一个合法取值")
		assert.True(t, b.contains(math.MaxInt64), "没有上界")
		assert.False(t, b.contains(-1), "负数仍然不是取值")
	})
}

// 读侧(mergeOverrides)与写侧(settingBounds)必须给出同一个区间。
//
// 写侧闸门只管**今后**的写入;读侧若自己写死一份区间,升级之前落库的越界覆盖
// 会继续被读出来并生效,敞口一点没关,而配置页会显示一个写不进去、却正在生效
// 的值。max_active_activities 那条早就有这个测试,奖品硬顶这条一直没有。
func TestPrizeCeilingReadAndWriteSidesAgree(t *testing.T) {
	t.Run("YAML 立了硬顶", func(t *testing.T) {
		const ceiling = 50_000_000
		withLotteryConfig(t, config.Lottery{
			MaxActiveActivities: 20, MaxGuessFeeBps: 2000,
			MaxTotalPrizeQuota: ceiling,
		})
		base := opSettings{MaxTotalPrizeQuota: ceiling}

		accepted := mergeOverrides(base, map[string]string{
			keyMaxTotalPrizeQuota: strconv.Itoa(ceiling / 2),
		})
		assert.EqualValues(t, ceiling/2, accepted.MaxTotalPrizeQuota, "调低要收")

		rejected := mergeOverrides(base, map[string]string{
			keyMaxTotalPrizeQuota: strconv.Itoa(ceiling + 1),
		})
		assert.EqualValues(t, ceiling, rejected.MaxTotalPrizeQuota,
			"写侧拒绝的值读侧却采纳了 —— 存量越界覆盖会一直生效")

		// 在线把硬顶抹成"不限"是写侧明确拒绝的,读侧必须同样拒绝。
		unlimited := mergeOverrides(base, map[string]string{
			keyMaxTotalPrizeQuota: "0",
		})
		assert.EqualValues(t, ceiling, unlimited.MaxTotalPrizeQuota,
			"库里被人手写了 0,读侧不能把它当成「不限」采纳")
	})

	t.Run("YAML 不限:在线怎么配都行", func(t *testing.T) {
		withLotteryConfig(t, config.Lottery{
			MaxActiveActivities: 20, MaxGuessFeeBps: 2000, MaxTotalPrizeQuota: 0,
		})
		base := opSettings{MaxTotalPrizeQuota: 0}

		// 配一个正数 = 运营给自己加闸门,方向是收紧,没有理由拦。
		tightened := mergeOverrides(base, map[string]string{
			keyMaxTotalPrizeQuota: "12345",
		})
		assert.EqualValues(t, 12345, tightened.MaxTotalPrizeQuota)

		// 配一个天文数字也收:上界本来就不存在。
		huge := mergeOverrides(base, map[string]string{
			keyMaxTotalPrizeQuota: strconv.FormatInt(math.MaxInt64, 10),
		})
		assert.EqualValues(t, int64(math.MaxInt64), huge.MaxTotalPrizeQuota)

		// 负数仍然丢弃回落。
		negative := mergeOverrides(base, map[string]string{
			keyMaxTotalPrizeQuota: "-1",
		})
		assert.EqualValues(t, 0, negative.MaxTotalPrizeQuota)
	})
}

// 二次确认阈值没有上界:它是一道会不会响的铃,不是一道会放大敞口的闸门。
func TestAlertThresholdIsNotClampedByThePrizeCeiling(t *testing.T) {
	withLotteryConfig(t, config.Lottery{
		MaxActiveActivities: 20, MaxGuessFeeBps: 2000,
		MaxTotalPrizeQuota: 1_000_000, LargePrizeAlertQuota: 500_000,
	})
	base := opSettings{MaxTotalPrizeQuota: 1_000_000, LargePrizeAlertQuota: 500_000}

	got := mergeOverrides(base, map[string]string{
		keyLargePrizeAlertQuota: "9000000",
	})
	assert.EqualValues(t, 9_000_000, got.LargePrizeAlertQuota,
		"阈值配高只是少响几次,一分钱都不会多发,不该被夹")
}

// 一条把上面几个数字串起来的自检:文案里出现的金额必须能被独立算出来。
func TestQuotaTextIsSelfConsistentWithQuotaPerUnit(t *testing.T) {
	withQuotaDisplay(t, operation_setting.QuotaDisplayTypeUSD, "", 0)
	for _, quota := range []int64{1, 500_000, 5_000_000, 50_000_000} {
		want := fmt.Sprintf("＄%.6f 额度", float64(quota)/common.QuotaPerUnit)
		assert.Equal(t, want, quotaText(quota))
		assert.True(t, strings.HasPrefix(quotaText(quota), "＄"))
	}
}
