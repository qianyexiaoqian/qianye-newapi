package invite

import (
	"time"

	"github.com/QuantumNous/new-api/qianye/config"
)

// dayline.go —— 扩展里唯一的「一天」定义。
//
// # 为什么必须只有一处
//
// 有三个地方要回答"这是哪一天":
//
//	星屑消费返的日桶键与日结的"今天"(stardust/settle.go)
//	星屑下线消费返的日桶键(stardust/settle_invite.go)
//	下线日消费报表的区间(api_daily_consume.go)
//
// "昨天的消费今天发"这句话天天出现在界面上,而这几处只要有任意两处口径不同,
// 那句话就是错的:结算跑在 UTC 日界上、报表按本地日,今天这一跑吸收的就是
// 横跨两个自然日的半截数据 —— 用户看到的"昨日消费"少了一段又多了一段,
// 而账本自己完全自洽,没有任何东西会报错。
//
// 所以这几处一律走本文件,偏移只有一个旋钮 invite.day_offset_minutes。
//
// # 为什么是固定偏移而不是 time.Local
//
// 日结由租约选主,可以落在任意节点上。各节点的 TZ 一旦不同,同一笔消费会进
// 两个桶,唯一索引失效、行数翻倍,而"今天跑过了没有"也会各说各话。
// 固定偏移是配置里写死的一个数,对不上时是显式的配置分歧,不是隐式的环境差异。
//
// 夏令时是这个取舍付出的代价:UTC+8 全年不变,所以国内运营不受影响;
// 需要夏令时的站点会在切换日多出/少掉一小时的桶。星屑是天粒度的账,
// 一年两次一小时的错位换来"分桶键永远可复算",这笔账划得来。

const secondsPerDay = int64(86400)

// dayOffsetSeconds 返回日界相对 UTC 的偏移(秒)。0 = UTC。
func dayOffsetSeconds() int64 {
	return int64(config.Get().Invite.DayOffsetMinutes) * 60
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
	// 1970 年之前的时刻会把日界算到未来去。
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

// nextDayStart 返回 ts 之后的下一个日界。
func nextDayStart(ts int64) int64 { return dayStart(ts) + secondsPerDay }

// DayKey 返回 ts 落在哪一天(YYYYMMDD,固定偏移)。
func DayKey(ts int64) string { return dayKey(ts) }

// DayStart 返回 ts 所在天的起点(unix 秒)。
func DayStart(ts int64) int64 { return dayStart(ts) }

// DayKeyStart 返回某个 YYYYMMDD 的起点;日键不合法时第二个返回值为 false。
func DayKeyStart(day string) (int64, bool) { return dayKeyStart(day) }

// NextDayStart 返回 ts 所在天的下一天起点。
func NextDayStart(ts int64) int64 { return nextDayStart(ts) }

// DayOffsetMinutes 是日界相对 UTC 的偏移(分钟),给用户端界面解释"这里的一天从几点算"。
func DayOffsetMinutes() int { return config.Get().Invite.DayOffsetMinutes }
