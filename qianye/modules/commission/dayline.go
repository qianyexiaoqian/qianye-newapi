package commission

import "github.com/QuantumNous/new-api/qianye/modules/invite"

// dayline.go —— 本包对「一天」的全部引用都转到 modules/invite。
//
// 日界只有一处定义(invite.day_offset_minutes):消费日桶 bucket_date、桶的成熟时刻、
// 日封顶窗口与一日一结算的"今天"必须同源,而星屑侧的日结与下线日消费报表用的也是
// 同一把 —— 两条线并行时"同一天"这三个字只能有一种含义。

const secondsPerDay = int64(86400)

func dayKey(ts int64) string               { return invite.DayKey(ts) }
func dayStart(ts int64) int64              { return invite.DayStart(ts) }
func dayKeyStart(day string) (int64, bool) { return invite.DayKeyStart(day) }
func nextDayStart(ts int64) int64          { return invite.NextDayStart(ts) }

// payoutDayOffset 是"消费之后第几天到账"里的那个数。
//
// 链路是:消费落进第 T 天的桶 → 桶在第 T 天结束时封板 → 再等 holdingDays 天成熟,
// 即 mature_at = 第 (T + holdingDays + 1) 天的日界 → 一日一结算恰好在日界之后
// 第一次心跳开跑,mature_at <= now 成立,当天结算进可用余额。
//
// 所以 N = holdingDays + 1,holding_days: 0 就是"次日"而不是"当天"。
// 这个 +1 不是实现细节:它是"桶要等一整天结束才封板"这条设计的直接后果
// (见 bucketMatureAt),界面上必须按它写。
func payoutDayOffset(holdingDays int) int {
	if holdingDays < 0 {
		holdingDays = 0
	}
	return holdingDays + 1
}
