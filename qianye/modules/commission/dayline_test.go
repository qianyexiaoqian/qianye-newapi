package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dayConfig 给出一份只改了日界偏移的返佣配置。
func dayConfig(offsetMinutes int) *config.Config {
	c := commissionConfig(0)
	c.Commission.DayOffsetMinutes = offsetMinutes
	return c
}

// TestDaylineIsTheSingleDayBoundary 钉住"一天"只有一个定义。
//
// 一日一结算之后,「昨日佣金」这四个字天天出现在界面上。分桶按 UTC、结算按
// 别的口径的话,今天这一跑吸收的就是横跨两个自然日的半截数据 —— 用户看到的
// 昨日佣金少了一段又多了一段,而三条恒等式全部成立,不会有任何东西报错。
//
// 所以这里断的不是"某个函数返回什么",而是**四处口径同源**:
// bucket_date、桶的成熟时刻、日封顶窗口、一日一结算的今天,全部由
// dayline.go 的同一个偏移推出来。
func TestDaylineIsTheSingleDayBoundary(t *testing.T) {
	// 2026-08-18T15:59:59Z / 16:00:00Z 是 UTC+8 的日界两侧,
	// 也就是北京时间 8 月 18 日 23:59:59 与 8 月 19 日 00:00:00。
	// 同一个时刻在 UTC 口径下还停在 18 日 —— 这一对正是"两种解释会算出
	// 不同的钱"的最小样本。
	const beforeCN = int64(1787068799) // 2026-08-18T15:59:59Z
	const atCN = int64(1787068800)     // 2026-08-18T16:00:00Z

	cases := []struct {
		name     string
		offset   int
		ts       int64
		wantDay  string
		wantOpen int64 // 该时刻所在日的起点
	}{
		{"UTC:日界就是零点", 0, 1787011200, "20260818", 1787011200}, // 2026-08-18T00:00:00Z
		{"UTC:日界前一秒还算前一天", 0, 1787011199, "20260817", 1786924800},
		{"UTC+8:北京时间 23:59:59 仍是 18 日", 480, beforeCN, "20260818", 1786982400},
		{"UTC+8:北京时间零点整翻到 19 日", 480, atCN, "20260819", 1787068800},
		{"UTC 口径下同一时刻还停在 18 日", 0, atCN, "20260818", 1787011200},
		// 2026-08-18T00:00:00Z 在 UTC-5 是 17 日 19:00,日界回落到 17 日 05:00Z。
		{"UTC-5:西半球偏移为负", -300, 1787011200, "20260817", 1786942800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useConfig(t, dayConfig(tc.offset))

			assert.Equal(t, tc.wantDay, dayKey(tc.ts), "一日一结算的『今天』")
			assert.Equal(t, tc.wantDay, bucketDate(tc.ts), "日聚合桶的分桶键必须与它同源")
			assert.Equal(t, tc.wantOpen, dayStart(tc.ts), "日封顶窗口的起点")

			// 日键 → 起点 → 日键,必须绕回原处。绕不回去意味着某一天的
			// 结算会去吸收另一天的桶。
			start, ok := dayKeyStart(tc.wantDay)
			require.True(t, ok)
			assert.Equal(t, tc.wantOpen, start)
			assert.Equal(t, tc.wantDay, dayKey(start))
			assert.Equal(t, tc.wantOpen+86400, nextDayStart(tc.ts), "下一次自动结算最早开跑的时刻")
		})
	}
}

// TestBucketMatureAtLandsOnADayBoundary 是"T+N 到账"这句话的证据。
//
// 链路:消费落进第 T 天的桶 → 桶要等**整天结束**才封板 → 再等 holding_days 天
// 成熟。所以 mature_at 恰好落在第 (T + holding_days + 1) 天的日界上,而一日一
// 结算在日界之后的第一次心跳开跑,mature_at <= now 当场成立 —— 当天发得出去。
//
// N = holding_days + 1,那个 +1 不是四舍五入:holding_days: 0 也是**次日**到账。
// 界面上写错这个数,每个 holding_days=0 的站点都会被用户追问一遍。
func TestBucketMatureAtLandsOnADayBoundary(t *testing.T) {
	cases := []struct {
		name        string
		offset      int
		holdingDays int
		wantN       int
	}{
		{"UTC / 不设成熟期 = 次日到账", 0, 0, 1},
		{"UTC / 成熟期 1 天 = T+2", 0, 1, 2},
		{"UTC / 默认成熟期 7 天 = T+8", 0, 7, 8},
		{"UTC+8 / 默认成熟期 = T+8,只是日界挪了 8 小时", 480, 7, 8},
		{"负成熟期按 0 处理,绝不能算出比消费日还早的到账日", 0, -3, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useConfig(t, dayConfig(tc.offset))

			const consumedAt = int64(1787068800) // 2026-08-18T16:00:00Z
			day := bucketDate(consumedAt)
			dayOpen, ok := dayKeyStart(day)
			require.True(t, ok)

			holding := tc.holdingDays
			if holding < 0 {
				holding = 0
			}
			mature := bucketMatureAt(day, holding)

			assert.Equal(t, dayOpen+int64(tc.wantN)*86400, mature,
				"成熟时刻必须正好落在第 T+N 天的日界上")
			assert.Equal(t, mature, dayStart(mature),
				"成熟时刻落在日界之外 = 那笔钱要多等一整天才被那一跑看到")
			assert.Equal(t, tc.wantN, payoutDayOffset(tc.holdingDays),
				"界面上写的 N 与账本算出来的 N 不是一个数")

			// 日界那一刻的那一跑必须够得着它:结算条件是 mature_at <= now。
			assert.LessOrEqual(t, mature, dayStart(mature),
				"日界开跑时 mature_at 已经到期")
		})
	}
}
