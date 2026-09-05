package invite

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dayConfig 给出一份只改了日界偏移的配置。
func dayConfig(offsetMinutes int) *config.Config {
	return inviteConfig(offsetMinutes)
}

// TestDaylineIsTheSingleDayBoundary 钉住"一天"只有一个定义。
//
// 一日一结算之后,「昨日」这两个字天天出现在界面上。分桶按 UTC、结算按
// 别的口径的话,今天这一跑吸收的就是横跨两个自然日的半截数据 —— 用户看到的
// 昨日数字少了一段又多了一段,而账本恒等式全部成立,不会有任何东西报错。
//
// 所以这里断的不是"某个函数返回什么",而是**几处口径同源**:
// 日桶的 bucket_date、日结的今天、报表的区间,全部由
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
			assert.Equal(t, tc.wantDay, DayKey(tc.ts), "导出的日键必须与它同源")
			assert.Equal(t, tc.wantOpen, dayStart(tc.ts), "那一天的起点")

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
