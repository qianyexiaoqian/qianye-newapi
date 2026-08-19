package commission

import (
	"time"

	"github.com/QuantumNous/new-api/qianye/config"
)

// dayline.go —— 返佣模块唯一的「一天」定义。
//
// # 为什么必须只有一处
//
// 返佣里有四个地方要回答"这是哪一天":
//
//	bucketDate       消费日聚合桶的键(它还进了幂等键,写进库就改不动了)
//	bucketMatureAt   桶的成熟时刻 = 桶所在日结束 + holding_days
//	dailyRemaining   日封顶"今日已发"的统计窗口
//	dayKey           一日一结算的"今天"
//
// 改成一日一结算之后,"昨天的佣金今天发"这句话会天天出现在界面上,而这四处
// 只要有任意两处口径不同,那句话就是错的:结算跑在 UTC 日界上、分桶按本地日,
// 今天这一跑吸收的就是横跨两个自然日的半截数据 —— 用户看到的"昨日佣金"
// 少了一段又多了一段,而三条恒等式全部成立,没有任何东西会报错。
//
// 所以这四处一律走本文件,偏移只有一个旋钮 commission.day_offset_minutes。
//
// # 为什么是固定偏移而不是 time.Local
//
// 结算与计佣可以落在任意节点上(计佣挂在 relay 上,结算由租约选主)。
// 各节点的 TZ 一旦不同,同一笔消费会进两个桶,唯一索引失效、行数翻倍,
// 而"今天跑过了没有"也会各说各话。固定偏移是配置里写死的一个数,
// 对不上时是显式的配置分歧,不是隐式的环境差异。
//
// 夏令时是这个取舍付出的代价:UTC+8 全年不变,所以国内运营不受影响;
// 需要夏令时的站点会在切换日多出/少掉一小时的桶。返佣是天粒度的账,
// 一年两次一小时的错位换来"分桶键永远可复算",这笔账划得来。

const secondsPerDay = int64(86400)

// dayOffsetSeconds 返回日界相对 UTC 的偏移(秒)。0 = UTC = 本旋钮出现之前的行为。
func dayOffsetSeconds() int64 {
	return int64(config.Get().Commission.DayOffsetMinutes) * 60
}

// dayKey 返回时刻 ts 落在哪一天,格式 yyyymmdd。
func dayKey(ts int64) string {
	return time.Unix(ts+dayOffsetSeconds(), 0).UTC().Format("20060102")
}

// dayStart 返回 ts 所在那一天的起点(unix 秒)。
func dayStart(ts int64) int64 {
	off := dayOffsetSeconds()
	shifted := ts + off
	// 必须用向下取整的模,不能用 Go 的 % —— 后者对负数向零取整,
	// 1970 年之前的时刻会把日界算到未来去。测试库里的 ts 全是正数,
	// 所以这条错误在单测里看不见,只有真的收到一个负时间戳才会现身。
	m := shifted % secondsPerDay
	if m < 0 {
		m += secondsPerDay
	}
	return shifted - m - off
}

// dayKeyStart 把日键还原成它的起点。第二个返回值为 false 表示键不合法。
func dayKeyStart(day string) (int64, bool) {
	t, err := time.ParseInLocation("20060102", day, time.UTC)
	if err != nil {
		return 0, false
	}
	return t.Unix() - dayOffsetSeconds(), true
}

// nextDayStart 返回 ts 之后的下一个日界,也就是下一次自动结算最早会开跑的时刻。
func nextDayStart(ts int64) int64 { return dayStart(ts) + secondsPerDay }

// payoutDayOffset 是"消费之后第几天到账"里的那个数。
//
// 链路是:消费落进第 T 天的桶 → 桶在第 T 天结束时封板 → 再等 holdingDays 天成熟,
// 即 mature_at = 第 (T + holdingDays + 1) 天的日界 → 一日一结算恰好在日界之后
// 第一次心跳开跑,mature_at <= now 成立,当天发出。
//
// 所以 N = holdingDays + 1,holding_days: 0 就是"次日到账"而不是"当天到账"。
// 这个 +1 不是实现细节:它是"桶要等一整天结束才封板"这条设计的直接后果
// (见 bucketMatureAt),界面上必须按它写,否则每个 holding_days=0 的站点
// 都会被用户追问"为什么说好当天到账却第二天才有"。
func payoutDayOffset(holdingDays int) int {
	if holdingDays < 0 {
		holdingDays = 0
	}
	return holdingDays + 1
}
